//go:build native_capacity

package nativepty

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
	"golang.org/x/sys/unix"
)

type capacityFixture struct {
	enabled bool
	failed  bool
	marker  string
	start   time.Time
	last    time.Time
	stages  [4]int64
}

func capacityHelperBuildArgs() []string { return []string{"-tags", "native_capacity"} }

func prepareCapacityFixture(t *testing.T, b *Backend, marker string, spec *domain.LaunchSpec) *capacityFixture {
	d := &capacityFixture{enabled: os.Getenv(capacityDiagnosticEnv) == "1", marker: marker}
	if !d.enabled {
		return d
	}
	// Sidecar writes are outside PTY output but inside this diagnostic journey's
	// wall time. The default script above is unchanged when diagnostics are off.
	spec.Argv[2] = strings.Replace(spec.Argv[2], "IFS= read -r line;", `IFS= read -r line; printf 1 > "$1.stage1";`, 1)
	spec.Argv[2] = strings.Replace(spec.Argv[2], `printf '\r\nDONE-e`, `printf 2 > "$1.stage2"; printf '\r\nDONE-e`, 1)
	spec.Argv[2] = strings.Replace(spec.Argv[2], `printf '%s' "$answer"`, `printf 3 > "$1.stage3"; printf '%s' "$answer"`, 1)
	spec.Argv[2] = strings.Replace(spec.Argv[2], `exec sleep 30`, `printf 4 > "$1.stage4"; exec sleep 30`, 1)
	return d
}

func (d *capacityFixture) begin(start time.Time) { d.start = start }
func (d *capacityFixture) poll() {
	if !d.enabled || time.Since(d.last) < 100*time.Millisecond {
		return
	}
	d.last = time.Now()
	for i := range d.stages {
		if d.stages[i] != 0 {
			continue
		}
		if data, err := readCapacityFile(fmt.Sprintf("%s.stage%d", d.marker, i+1), 1); err == nil && len(data) == 1 && data[0] == byte('1'+i) {
			d.stages[i] = max(int64(1), time.Since(d.start).Nanoseconds())
		}
	}
}

type capacityFileObservation struct {
	Outcome string `json:"outcome"`
	Bytes   int    `json:"bytes"`
}

type capacityFixtureObservation struct {
	Version             int                     `json:"version"`
	OriginalFailure     bool                    `json:"original_failure"`
	ParentElapsedNS     int64                   `json:"parent_elapsed_ns"`
	StageFirstSeenNS    [4]int64                `json:"stage_first_seen_ns"`
	Marker              capacityFileObservation `json:"marker"`
	Pending             capacityFileObservation `json:"pending"`
	Telemetry           capacityFileObservation `json:"telemetry"`
	TelemetryRecords    int                     `json:"telemetry_complete_records"`
	TelemetryLast       *capacityRecord         `json:"telemetry_last,omitempty"`
	TelemetryPartial    bool                    `json:"telemetry_partial_record"`
	PostFailureSnapshot string                  `json:"post_failure_snapshot"`
	SnapshotCursor      *uint64                 `json:"snapshot_cursor"`
	TailPresent         *bool                   `json:"tail_present"`
	PostFailureInspect  string                  `json:"post_failure_inspect"`
	ChildAlive          *bool                   `json:"child_alive"`
}

func readCapacityFile(path string, limit int) ([]byte, error) {
	f, err := openPrivate(path, unix.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(limit+1)))
	if len(data) > limit {
		return nil, fmt.Errorf("capacity diagnostic exceeds bound")
	}
	return data, err
}

func observeCapacityFile(path string, limit int) capacityFileObservation {
	data, err := readCapacityFile(path, limit)
	return capacityFileObservation{Outcome: capacityErrorClass(err), Bytes: len(data)}
}

func (d *capacityFixture) collect(t *testing.T, b *Backend, ref domain.RuntimeRef) {
	if !d.enabled {
		return
	}
	d.last = time.Time{}
	d.poll()
	d.failed = d.failed || t.Failed()
	o := capacityFixtureObservation{Version: 1, OriginalFailure: d.failed, ParentElapsedNS: time.Since(d.start).Nanoseconds(), StageFirstSeenNS: d.stages,
		Marker: observeCapacityFile(d.marker, 7), Pending: observeCapacityFile(d.marker+".pending", 7),
		PostFailureSnapshot: "not_requested", PostFailureInspect: "not_requested"}
	data, err := readCapacityFile(capacityDiagnosticPath(b.state, ref.ExecutionID), capacityMaxBytes)
	o.Telemetry = capacityFileObservation{Outcome: capacityErrorClass(err), Bytes: len(data)}
	if err == nil {
		lines := bytes.Split(data, []byte{'\n'})
		o.TelemetryPartial = len(lines[len(lines)-1]) != 0
		for _, line := range lines[:len(lines)-1] {
			if len(line) == 0 || o.TelemetryRecords >= capacityMaxRecords {
				o.Telemetry.Outcome = "invalid"
				break
			}
			var record capacityRecord
			if json.Unmarshal(line, &record) != nil || !validCapacityRecord(record) || record.Sequence != uint64(o.TelemetryRecords+1) {
				o.Telemetry.Outcome = "invalid"
				break
			}
			// Publish only the fixed numeric schema and finite enums emitted by
			// this fixture. A same-user edited file cannot inject raw strings.
			canonical, marshalErr := json.Marshal(record)
			if marshalErr != nil || !bytes.Equal(canonical, line) {
				o.Telemetry.Outcome = "invalid"
				break
			}
			o.TelemetryRecords++
			o.TelemetryLast = &record
		}
	}
	// The fatal predicate has already failed. These observations cannot change
	// that result; they neither input nor resize, and each has its own 1s bound.
	if d.failed {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		stream, snapshotErr := b.Open(ctx, ref, true, 24, 80)
		o.PostFailureSnapshot = capacityErrorClass(snapshotErr)
		if snapshotErr == nil {
			cursor := stream.Offset
			tailPresent := bytes.Contains(stream.Snapshot, []byte("DONE-e\u0301"))
			o.SnapshotCursor = &cursor
			o.TailPresent = &tailPresent
			_ = stream.Close()
		}
		cancel()
		ctx, cancel = context.WithTimeout(context.Background(), time.Second)
		observed, inspectErr := b.Inspect(ctx, ref)
		o.PostFailureInspect = capacityErrorClass(inspectErr)
		if inspectErr == nil {
			alive := observed.Alive
			o.ChildAlive = &alive
		}
		cancel()
	}
	encoded, err := json.Marshal(o)
	if err != nil {
		t.Log("native_capacity_diagnostic_unavailable=marshal")
		return
	}
	t.Logf("native_capacity_diagnostic=%s", encoded)
}

func validCapacityRecord(r capacityRecord) bool {
	if r.Version != 1 || r.ElapsedNS < 0 || r.ReadNS < 0 || r.ScreenNS < 0 || r.ScreenMaxNS < 0 || r.ReplyNS < 0 {
		return false
	}
	switch r.Checkpoint {
	case "start", "screen", "reply", "close":
	default:
		return false
	}
	for _, outcome := range []string{r.LastReadOutcome, r.LastReplyOutcome} {
		switch outcome {
		case "none", "success", "timeout", "canceled", "closed", "missing", "permission", "other":
		default:
			return false
		}
	}
	return true
}
