package ircd

import (
	"context"
	"os"
	"time"

	"github.com/seanheiney/keelmux/internal/diagnosticfs"
)

// Diagnostic is deliberately smaller than Config and Status: no addresses,
// credential references or saved terminal/native input can escape this view.
type Diagnostic struct {
	Stage, SavedState string
	UpdatedAt         time.Time
}

// DiagnoseExisting checks the exact persisted configuration and pinned runtime
// bytes without New/Ensure, locks, subprocesses, downloads or endpoint probes.
// Config validity does not prove a running server or authenticated transport.
func DiagnoseExisting(ctx context.Context, dir *diagnosticfs.Dir) (Diagnostic, error) {
	var out Diagnostic
	if _, err := dir.Read("config.json", 0600, 65536); err != nil {
		if os.IsNotExist(err) {
			return out, ErrNotInitialized
		}
		return out, ErrRepair
	}
	runtimeDir, err := dir.Child("runtime")
	if err != nil {
		return out, ErrRepair
	}
	defer runtimeDir.Close()
	m := &Manager{dir: dir.Path}
	r, err := m.readConfigWithRuntime(ctx, func(ctx context.Context) (RuntimeInfo, error) {
		return m.runtimeInfoWithDirectory(ctx, func(string) error {
			if !runtimeDir.Unchanged() {
				return ErrRepair
			}
			return nil
		})
	})
	if err != nil {
		return out, ErrRepair
	}
	out.Stage = r.Stage
	saved, err := m.loadLifecycle(r, m.lifecyclePath())
	if err == nil {
		out.SavedState = saved.State
		out.UpdatedAt = saved.UpdatedAt
	} else if !os.IsNotExist(err) {
		return Diagnostic{}, err
	}
	if !dir.Unchanged() || !runtimeDir.Unchanged() {
		return Diagnostic{}, ErrRepair
	}
	return out, nil
}
