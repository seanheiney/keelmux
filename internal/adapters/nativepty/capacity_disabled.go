//go:build !native_capacity

package nativepty

import (
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
)

// Ordinary builds have no diagnostic environment, clocks, files or counters.
// These empty hooks inline away; only the explicit diagnostic tag enables them.
type capacityDiagnostics struct{}

func startCapacityDiagnostics(string, domain.ID) *capacityDiagnostics { return nil }
func (*capacityDiagnostics) started() time.Time                       { return time.Time{} }
func (*capacityDiagnostics) elapsed(time.Time) int64                  { return 0 }
func (*capacityDiagnostics) readDone(int, time.Time, error)           {}
func (*capacityDiagnostics) screenDone(int, int, int64)               {}
func (*capacityDiagnostics) replyDone(int, time.Time, error)          {}
func (*capacityDiagnostics) close()                                   {}
