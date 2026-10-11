package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/seanheiney/keelmux/internal/diagnosticfs"
	"github.com/seanheiney/keelmux/internal/domain"
	"golang.org/x/sys/unix"
)

var ErrDiagnosticBusy = errors.New("registry authority is in use")

// DiagnosticSnapshot contains no authored text, credentials or model/account
// metadata. Runtime references are saved evidence, never permission to signal.
type DiagnosticSnapshot struct {
	Sessions, Bindings, ActiveBindings int
	LastEvent                          time.Time
	Runtimes                           []domain.RuntimeRef
}

// DiagnoseExisting never invokes Open or migration. The existing authority lock
// excludes cooperating writers; immutable mode prevents SQLite sidecar creation
// or journal recovery even when damaged state is supplied.
func DiagnoseExisting(ctx context.Context, dir *diagnosticfs.Dir) (DiagnosticSnapshot, error) {
	var out DiagnosticSnapshot
	lock, err := dir.Open("registry.sqlite.lock", 0600, 1<<20)
	if err != nil {
		return out, err
	}
	defer lock.Close()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_SH|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return out, ErrDiagnosticBusy
		}
		return out, err
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := dir.Stat("registry.sqlite" + suffix); !os.IsNotExist(err) {
			return out, diagnosticfs.ErrUnsafe
		}
	}
	f, err := dir.Open("registry.sqlite", 0600, 64<<20)
	if err != nil {
		return out, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return out, err
	}
	header := make([]byte, 100)
	if _, err = io.ReadFull(f, header); err != nil || string(header[:16]) != "SQLite format 3\x00" || header[18] != 1 || header[19] != 1 {
		return out, repair("diagnostics requires an existing rollback-journal SQLite database")
	}
	// A private directory plus exact inode checks binds this conventional SQLite
	// pathname to the descriptor. No untrusted schema functions are registered.
	path := filepath.Join(dir.Path, "registry.sqlite")
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&immutable=1&_pragma=query_only(1)&_pragma=trusted_schema(0)"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return out, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var version int
	if err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || (version != 4 && version != 5) {
		return out, repair("diagnostic schema unavailable")
	}
	var integrity string
	if err = db.QueryRowContext(ctx, "PRAGMA quick_check(1)").Scan(&integrity); err != nil || integrity != "ok" {
		return out, repair("diagnostic integrity failed")
	}
	for _, check := range []func(context.Context, *sql.DB) error{validateCodexSchema, validateChannelSchema, validateProjectSchema, validatePrimaryStdioSchema} {
		if err = check(ctx, db); err != nil {
			return out, err
		}
	}
	if version == 5 {
		if err = validateCaptainSchema(ctx, db); err != nil {
			return out, err
		}
	}
	s := &Store{db: db}
	sessions, err := s.Sessions(ctx)
	if err != nil {
		return out, err
	}
	out.Sessions = len(sessions)
	for _, s := range sessions {
		if s.Execution.Runtime.PID != 0 {
			out.Runtimes = append(out.Runtimes, s.Execution.Runtime)
		}
	}
	if err = db.QueryRowContext(ctx, "SELECT count(*),coalesce(sum(active),0) FROM channel_bindings").Scan(&out.Bindings, &out.ActiveBindings); err != nil {
		return out, err
	}
	rows, err := db.QueryContext(ctx, "SELECT data FROM events")
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var raw []byte
		var event domain.Event
		if err = rows.Scan(&raw); err != nil {
			break
		}
		if err = json.Unmarshal(raw, &event); err != nil {
			break
		}
		if event.At.After(out.LastEvent) {
			out.LastEvent = event.At
		}
	}
	if err = closeChannelRows(rows, err); err != nil {
		return out, err
	}
	after, err := os.Lstat(path)
	if err != nil || !dir.Unchanged() || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return DiagnosticSnapshot{}, diagnosticfs.ErrUnsafe
	}
	lockNow, err := os.Lstat(filepath.Join(dir.Path, "registry.sqlite.lock"))
	lockBefore, e := lock.Stat()
	if err != nil || e != nil || !os.SameFile(lockBefore, lockNow) {
		return DiagnosticSnapshot{}, diagnosticfs.ErrUnsafe
	}
	return out, nil
}
