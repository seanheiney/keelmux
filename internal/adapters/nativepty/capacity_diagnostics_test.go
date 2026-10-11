//go:build native_capacity

package nativepty

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seanheiney/keelmux/internal/domain"
)

func capacityPrivateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func capacityRecords(t *testing.T, path string) []capacityRecord {
	t.Helper()
	data, err := readCapacityFile(path, capacityMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(data, []byte{'\n'}) {
		t.Fatal("incomplete record after diagnostic close")
	}
	var records []capacityRecord
	for _, line := range bytes.Split(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'}) {
		var record capacityRecord
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
		if record.Version != 1 || record.Sequence != uint64(len(records)) || record.ElapsedNS < 0 {
			t.Fatal("non-monotonic or malformed numeric checkpoint")
		}
	}
	return records
}

func TestCapacityDiagnosticsRequireExplicitOptIn(t *testing.T) {
	dir := capacityPrivateDir(t)
	id := domain.NewID("execution")
	t.Setenv(capacityDiagnosticEnv, "")
	if d := startCapacityDiagnostics(dir, id); d != nil {
		t.Fatal("tag alone enabled diagnostic files")
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatal("disabled diagnostic created state")
	}
	t.Setenv(capacityDiagnosticEnv, "1")
	if d := startCapacityDiagnostics(dir, "../invalid"); d != nil {
		t.Fatal("invalid execution admitted a file")
	}
}

func TestCapacityDiagnosticsExclusivePrivateAndClose(t *testing.T) {
	t.Setenv(capacityDiagnosticEnv, "1")
	dir := capacityPrivateDir(t)
	id := domain.NewID("execution")
	d := startCapacityDiagnostics(dir, id)
	if d == nil {
		t.Fatal("fixture telemetry was not opened")
	}
	f := d.file
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !owned(info) {
		t.Fatal("numeric file is not private/owned/regular")
	}
	d.close()
	d.close()
	if _, err := f.Write([]byte("foreign")); !errors.Is(err, os.ErrClosed) {
		t.Fatal("diagnostic close leaked its descriptor")
	}
	path := capacityDiagnosticPath(dir, id)
	before, _ := os.ReadFile(path)
	if startCapacityDiagnostics(dir, id) != nil {
		t.Fatal("existing diagnostic file was overwritten")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("exclusive refusal changed existing bytes")
	}
	records := capacityRecords(t, path)
	if len(records) != 2 || records[0].Checkpoint != "start" || records[1].Checkpoint != "close" {
		t.Fatal("lost terminal diagnostic checkpoint")
	}
}

func TestCapacityDiagnosticsRefuseSymlinkAndPublicDirectory(t *testing.T) {
	t.Setenv(capacityDiagnosticEnv, "1")
	dir := capacityPrivateDir(t)
	id := domain.NewID("execution")
	foreign := filepath.Join(t.TempDir(), "foreign")
	if err := os.WriteFile(foreign, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(foreign, capacityDiagnosticPath(dir, id)); err != nil {
		t.Fatal(err)
	}
	if startCapacityDiagnostics(dir, id) != nil {
		t.Fatal("followed a diagnostic symlink")
	}
	if data, _ := os.ReadFile(foreign); string(data) != "unchanged" {
		t.Fatal("refusal changed foreign file")
	}
	public := t.TempDir()
	if err := os.Chmod(public, 0755); err != nil {
		t.Fatal(err)
	}
	if startCapacityDiagnostics(public, id) != nil {
		t.Fatal("public directory admitted diagnostics")
	}
	if entries, _ := os.ReadDir(public); len(entries) != 0 {
		t.Fatal("unsafe-state refusal created a file")
	}
}

func TestCapacityDiagnosticsCountersAndFiniteLifetime(t *testing.T) {
	t.Setenv(capacityDiagnosticEnv, "1")
	dir := capacityPrivateDir(t)
	id := domain.NewID("execution")
	d := startCapacityDiagnostics(dir, id)
	if d == nil {
		t.Fatal("missing telemetry")
	}
	defer d.close()
	for i := 0; i < 512; i++ {
		d.readDone(maxInput, d.started(), nil)
		d.screenDone(maxInput, 0, int64(i+1))
	}
	d.close()
	path := capacityDiagnosticPath(dir, id)
	data, _ := os.ReadFile(path)
	if len(data) > capacityMaxBytes {
		t.Fatal("diagnostics exceeded byte bound")
	}
	records := capacityRecords(t, path)
	if len(records) != capacityMaxRecords {
		t.Fatal("diagnostics did not stop at finite lifetime bound")
	}
	last := records[len(records)-1]
	if last.ScreenBytes != 127*capacityCheckpointBytes || last.ReadBytes != last.ScreenBytes || last.ScreenCalls != 254 || last.ReadCalls != last.ScreenCalls || last.ScreenNS == 0 || last.ScreenMaxNS != 254 {
		t.Fatal("checkpoints lost exact processed-prefix counters")
	}
	d.readDone(1, d.started(), nil)
	d.replyDone(7, d.started(), nil)
	if got, _ := os.ReadFile(path); !bytes.Equal(got, data) {
		t.Fatal("bounded exhausted diagnostics resumed recording")
	}
}

func TestCapacityDiagnosticsReplyFailureIsNotSuccessfulBytes(t *testing.T) {
	t.Setenv(capacityDiagnosticEnv, "1")
	dir := capacityPrivateDir(t)
	id := domain.NewID("execution")
	d := startCapacityDiagnostics(dir, id)
	if d == nil {
		t.Fatal("missing telemetry")
	}
	d.readDone(12, d.started(), nil)
	d.screenDone(12, 7, 123)
	d.replyDone(7, d.started(), context.DeadlineExceeded)
	d.replyDone(7, d.started(), nil)
	d.close()
	records := capacityRecords(t, capacityDiagnosticPath(dir, id))
	failed, success := records[1], records[2]
	if failed.LastReplyOutcome != "timeout" || failed.ReplyAttemptedBytes != 7 || failed.ReplySuccessfulBytes != 0 || success.LastReplyOutcome != "success" || success.ReplyAttemptedBytes != 14 || success.ReplySuccessfulBytes != 7 || success.RepliesGenerated != 7 {
		t.Fatal("reply failure was laundered as delivered bytes")
	}
}

func TestCapacityDiagnosticsFileFailureDoesNotPanicOrReopen(t *testing.T) {
	t.Setenv(capacityDiagnosticEnv, "1")
	dir := capacityPrivateDir(t)
	id := domain.NewID("execution")
	d := startCapacityDiagnostics(dir, id)
	if d == nil {
		t.Fatal("missing telemetry")
	}
	_ = d.file.Close()
	d.screenDone(capacityCheckpointBytes, 0, 1)
	d.replyDone(7, d.started(), nil)
	d.close()
	records := capacityRecords(t, capacityDiagnosticPath(dir, id))
	if len(records) != 1 || records[0].Checkpoint != "start" {
		t.Fatal("failed diagnostic writer rewrote/reopened file")
	}
}

func TestCapacityDiagnosticClassesNeverCopyErrorText(t *testing.T) {
	for _, example := range []struct {
		err   error
		class string
	}{{nil, "success"}, {context.DeadlineExceeded, "timeout"}, {context.Canceled, "canceled"}, {io.EOF, "closed"}, {os.ErrNotExist, "missing"}, {os.ErrPermission, "permission"}, {errors.New("credential-fixture-literal"), "other"}} {
		if got := capacityErrorClass(example.err); got != example.class {
			t.Fatalf("class %s became %s", example.class, got)
		}
		if example.err != nil && capacityErrorClass(fmt.Errorf("local-path: %w", example.err)) != example.class {
			t.Fatal("wrapped error lost its class")
		}
	}
}

func TestCapacityRecordRefusesInjectedStringsAndNegativeTimings(t *testing.T) {
	record := capacityRecord{Version: 1, Checkpoint: "start", LastReadOutcome: "none", LastReplyOutcome: "none"}
	if !validCapacityRecord(record) {
		t.Fatal("valid finite record refused")
	}
	for _, alter := range []func(*capacityRecord){
		func(r *capacityRecord) { r.Checkpoint = "/private/account" },
		func(r *capacityRecord) { r.LastReadOutcome = "credential-fixture-literal" },
		func(r *capacityRecord) { r.LastReplyOutcome = "native-content" },
		func(r *capacityRecord) { r.ScreenNS = -1 },
	} {
		changed := record
		alter(&changed)
		if validCapacityRecord(changed) {
			t.Fatal("unsafe string or negative timing admitted")
		}
	}
}

func TestCapacityUnobservedLifecycleSerializesUnknown(t *testing.T) {
	data, err := json.Marshal(capacityFixtureObservation{PostFailureSnapshot: "not_requested", PostFailureInspect: "not_requested"})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"snapshot_cursor", "tail_present", "child_alive"} {
		if !bytes.Contains(data, []byte(`"`+field+`":null`)) {
			t.Fatal("unobserved lifecycle was serialized as zero/false")
		}
	}
}

func TestCapacityFixturePreservesScriptWithoutEnvironment(t *testing.T) {
	t.Setenv(capacityDiagnosticEnv, "")
	spec := testSpec("literal-original")
	prepareCapacityFixture(t, nil, "unused", &spec)
	if spec.Argv[2] != "literal-original" {
		t.Fatal("tag alone changed uninstrumented fixture")
	}
}

func TestCapacityFixtureStageMappingPreservesPTYWorkload(t *testing.T) {
	t.Setenv(capacityDiagnosticEnv, "1")
	original := `stty -echo; IFS= read -r line; stty raw -echo; head -c 2097152 /dev/zero | tr '\000' x; printf '\r\nDONE-e\314\201\033[6n'; answer=$(dd bs=1 count=7 2>/dev/null); printf '%s' "$answer" > "$1.pending"; mv "$1.pending" "$1"; exec sleep 30`
	spec := testSpec(original)
	prepareCapacityFixture(t, nil, "unused", &spec)
	got := spec.Argv[2]
	for stage := 1; stage <= 4; stage++ {
		fragment := fmt.Sprintf(`printf %d > "$1.stage%d"; `, stage, stage)
		if strings.Count(got, fragment) != 1 {
			t.Fatal("missing or duplicated sidecar stage")
		}
		got = strings.Replace(got, fragment, "", 1)
	}
	if got != original {
		t.Fatal("diagnostic script changed original PTY workload/reply/marker")
	}
}
