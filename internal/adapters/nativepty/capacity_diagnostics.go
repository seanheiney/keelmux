//go:build native_capacity

package nativepty

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
	"golang.org/x/sys/unix"
)

const capacityDiagnosticEnv = "KEELMUX_TEST_NATIVE_CAPACITY"
const capacityMaxRecords = 128
const capacityMaxBytes = 128 << 10
const capacityCheckpointBytes = 64 << 10

// This record is a numeric fixture observation, never an authority receipt.
// In particular, failed writes do not establish how many bytes reached a child.
type capacityRecord struct {
	Version              int    `json:"version"`
	Sequence             uint64 `json:"sequence"`
	ElapsedNS            int64  `json:"elapsed_ns"`
	Checkpoint           string `json:"checkpoint"`
	ReadCalls            uint64 `json:"read_calls"`
	ReadBytes            uint64 `json:"read_bytes"`
	ReadNS               int64  `json:"read_ns"`
	LastReadOutcome      string `json:"last_read_outcome"`
	ScreenCalls          uint64 `json:"screen_calls"`
	ScreenBytes          uint64 `json:"screen_bytes"`
	ScreenNS             int64  `json:"screen_ns"`
	ScreenMaxNS          int64  `json:"screen_max_ns"`
	RepliesGenerated     uint64 `json:"replies_generated_bytes"`
	ReplyCalls           uint64 `json:"reply_write_calls"`
	ReplyAttemptedBytes  uint64 `json:"reply_attempted_bytes"`
	ReplySuccessfulBytes uint64 `json:"reply_successful_bytes"`
	ReplyNS              int64  `json:"reply_write_ns"`
	LastReplyOutcome     string `json:"last_reply_outcome"`
}

type capacityDiagnostics struct {
	mu       sync.Mutex
	file     *os.File
	start    time.Time
	last     uint64
	written  int
	disabled bool
	record   capacityRecord
}

func capacityDiagnosticPath(state string, execution domain.ID) string {
	return filepath.Join(state, executionKey(execution)+".capacity.jsonl")
}

func startCapacityDiagnostics(state string, execution domain.ID) *capacityDiagnostics {
	if os.Getenv(capacityDiagnosticEnv) != "1" || !validID(execution) || privateDirectory(state) != nil {
		return nil
	}
	f, err := openPrivate(capacityDiagnosticPath(state, execution), unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_APPEND)
	if err != nil {
		// Missing/partial diagnostics are reported by the fixture separately.
		// They never change launch, drain, reply or acceptance behavior.
		return nil
	}
	d := &capacityDiagnostics{file: f, start: time.Now(), record: capacityRecord{Version: 1, LastReadOutcome: "none", LastReplyOutcome: "none"}}
	d.appendLocked("start")
	return d
}

func (d *capacityDiagnostics) started() time.Time {
	if d == nil {
		return time.Time{}
	}
	return time.Now()
}

func (d *capacityDiagnostics) elapsed(started time.Time) int64 {
	if d == nil {
		return 0
	}
	return time.Since(started).Nanoseconds()
}

func (d *capacityDiagnostics) readDone(n int, started time.Time, err error) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.disabled {
		return
	}
	d.record.ReadCalls++
	if n > 0 {
		d.record.ReadBytes += uint64(n)
	}
	d.record.ReadNS += time.Since(started).Nanoseconds()
	d.record.LastReadOutcome = capacityErrorClass(err)
}

func (d *capacityDiagnostics) screenDone(n, replies int, duration int64) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.disabled {
		return
	}
	d.record.ScreenCalls++
	d.record.ScreenBytes += uint64(n)
	d.record.ScreenNS += duration
	d.record.ScreenMaxNS = max(d.record.ScreenMaxNS, duration)
	d.record.RepliesGenerated += uint64(replies)
	if d.record.ScreenBytes-d.last >= capacityCheckpointBytes {
		d.last = d.record.ScreenBytes
		d.appendLocked("screen")
	}
}

func (d *capacityDiagnostics) replyDone(n int, started time.Time, err error) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.disabled {
		return
	}
	d.record.ReplyCalls++
	d.record.ReplyAttemptedBytes += uint64(n)
	if err == nil {
		d.record.ReplySuccessfulBytes += uint64(n)
	}
	d.record.ReplyNS += time.Since(started).Nanoseconds()
	d.record.LastReplyOutcome = capacityErrorClass(err)
	d.appendLocked("reply")
}

func (d *capacityDiagnostics) appendLocked(reason string) {
	if d.disabled || d.record.Sequence >= capacityMaxRecords {
		d.disabled = true
		return
	}
	d.record.Sequence++
	d.record.ElapsedNS = time.Since(d.start).Nanoseconds()
	d.record.Checkpoint = reason
	data, err := json.Marshal(d.record)
	data = append(data, '\n')
	if err != nil || len(data) > capacityMaxBytes-d.written {
		d.disabled = true
		return
	}
	if err := writeAll(d.file, data); err != nil {
		d.disabled = true
		return
	}
	d.written += len(data)
}

func (d *capacityDiagnostics) close() {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.file == nil {
		return
	}
	d.appendLocked("close")
	_ = d.file.Close()
	d.file = nil
	d.disabled = true
}

func capacityErrorClass(err error) string {
	if err == nil {
		return "success"
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return "timeout"
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) {
		return "closed"
	}
	if errors.Is(err, os.ErrNotExist) {
		return "missing"
	}
	if errors.Is(err, os.ErrPermission) {
		return "permission"
	}
	return "other"
}
