// Package store is the host-local durable registry and ordered operation journal.
package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/seanheiney/keelmux/internal/config"
	"github.com/seanheiney/keelmux/internal/domain"
	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("record not found")

type Store struct {
	mu   sync.Mutex
	db   *sql.DB
	lock *os.File
}

// Open requires a private state directory. An advisory lock excludes another
// cooperating host authority; SQLite files must never be shared across hosts.
func Open(path string) (*Store, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("store path must be clean and absolute")
	}
	dir := filepath.Dir(path)
	if err := config.EnsurePrivateDirectory(dir); err != nil {
		return nil, err
	}
	lock, err := privateFile(path + ".lock")
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("store authority already open: %w", err)
	}
	f, err := privateFile(path)
	if err != nil {
		lock.Close()
		return nil, err
	}
	f.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		lock.Close()
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, lock: lock}
	var schemaVersion int
	if err = db.QueryRow(`PRAGMA user_version`).Scan(&schemaVersion); err != nil || schemaVersion > 2 {
		s.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("unsupported store schema version %d", schemaVersion)
	}
	// DELETE avoids sensitive WAL sidecars; FULL makes committed intent durable
	// before the caller may invoke the external runtime.
	_, err = db.Exec(`PRAGMA journal_mode=DELETE; PRAGMA synchronous=FULL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;`)
	if err == nil && schemaVersion < 2 {
		err = migrate(context.Background(), db)
	}
	if err == nil {
		err = validateCodexSchema(context.Background(), db)
	}
	if err != nil {
		s.Close()
		return nil, err
	}
	// Confirm the database/authority-lock directory entries as well as every
	// created parent. SQLite FULL sync covers committed transactional contents.
	parent, err := os.Open(dir)
	if err == nil {
		err = parent.Sync()
		_ = parent.Close()
	}
	if err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// Migration only adds metadata. Legacy lease, operation and payload bytes are
// untouched, and no principal or broker authority is invented for old grants.
func migrate(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS sessions (id TEXT PRIMARY KEY, thread_id TEXT NOT NULL, data BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS operations (id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id), generation INTEGER NOT NULL, data BLOB NOT NULL, payload BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS events (thread_id TEXT NOT NULL, sequence INTEGER NOT NULL, data BLOB NOT NULL, PRIMARY KEY(thread_id, sequence));
CREATE TABLE IF NOT EXISTS grants (operation_id TEXT PRIMARY KEY REFERENCES operations(id), session_id TEXT NOT NULL REFERENCES sessions(id), execution_id TEXT NOT NULL, read_only INTEGER NOT NULL, active INTEGER NOT NULL, data BLOB NOT NULL);
CREATE UNIQUE INDEX IF NOT EXISTS one_writer ON grants(session_id,execution_id) WHERE active=1 AND read_only=0;
CREATE TABLE IF NOT EXISTS codex_agents (session_id TEXT NOT NULL REFERENCES sessions(id), execution_id TEXT NOT NULL, data BLOB NOT NULL, PRIMARY KEY(session_id,execution_id));
CREATE TABLE IF NOT EXISTS codex_epochs (session_id TEXT PRIMARY KEY REFERENCES sessions(id), value INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS codex_grants (operation_id TEXT PRIMARY KEY REFERENCES grants(operation_id), data BLOB NOT NULL, agent BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS codex_connections (id TEXT PRIMARY KEY, data BLOB NOT NULL, retired INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS codex_receipts (operation_id TEXT PRIMARY KEY REFERENCES operations(id), effect_class TEXT NOT NULL, data BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS codex_callbacks (id TEXT PRIMARY KEY, data BLOB NOT NULL);
PRAGMA user_version=2;`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func privateFile(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || stat.Uid != uint32(os.Geteuid()) {
		f.Close()
		return nil, errors.New("store and authority lock require owned regular files")
	}
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	if s.db != nil {
		err = s.db.Close()
		s.db = nil
	}
	if s.lock != nil {
		if e := s.lock.Close(); err == nil {
			err = e
		}
		s.lock = nil
	}
	return err
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func session(ctx context.Context, q queryer, id domain.ID) (domain.Session, error) {
	var b []byte
	var v domain.Session
	if err := q.QueryRowContext(ctx, `SELECT data FROM sessions WHERE id=?`, id).Scan(&b); err != nil {
		return v, missing(err)
	}
	err := json.Unmarshal(b, &v)
	return v, err
}
func operation(ctx context.Context, q queryer, id domain.ID) (domain.Operation, []byte, error) {
	var b, payload []byte
	var v domain.Operation
	if err := q.QueryRowContext(ctx, `SELECT data,payload FROM operations WHERE id=?`, id).Scan(&b, &payload); err != nil {
		return v, nil, missing(err)
	}
	err := json.Unmarshal(b, &v)
	return v, payload, err
}
func missing(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func operationGeneration(ctx context.Context, q queryer, id domain.ID) (uint64, error) {
	var generation uint64
	err := q.QueryRowContext(ctx, `SELECT generation FROM operations WHERE id=?`, id).Scan(&generation)
	return generation, missing(err)
}
func conflict(detail string) error { return domain.ConflictError{Reason: detail} }
func encoded(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func (s *Store) Session(ctx context.Context, id domain.ID) (domain.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return session(ctx, s.db, id)
}
func (s *Store) Operation(ctx context.Context, id domain.ID) (domain.Operation, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return operation(ctx, s.db, id)
}
func (s *Store) Sessions(ctx context.Context) ([]domain.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM sessions ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	v := []domain.Session{}
	for rows.Next() {
		var b []byte
		var x domain.Session
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &x); err != nil {
			return nil, err
		}
		v = append(v, x)
	}
	return v, rows.Err()
}
func (s *Store) Operations(ctx context.Context) ([]domain.Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM operations ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	v := []domain.Operation{}
	for rows.Next() {
		var b []byte
		var x domain.Operation
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &x); err != nil {
			return nil, err
		}
		v = append(v, x)
	}
	return v, rows.Err()
}

// Record atomically persists an operation's exact request and optionally its new
// session. Reusing an idempotency key with any other payload is a conflict.
func (s *Store) Record(ctx context.Context, op domain.Operation, payload []byte, newSession *domain.Session) (domain.Operation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return op, false, err
	}
	defer tx.Rollback()
	prior, p, err := operation(ctx, tx, op.ID)
	if err == nil {
		if !bytes.Equal(p, payload) || prior.Kind != op.Kind || (newSession == nil && (prior.SessionID != op.SessionID || prior.ExecutionID != op.ExecutionID)) {
			return prior, false, conflict("operation ID already names a different request")
		}
		return prior, false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return op, false, err
	}
	if op.ID == "" || op.Kind == "" || op.SessionID == "" || op.ExecutionID == "" {
		return op, false, errors.New("operation requires identity, kind, session and execution")
	}
	if newSession != nil {
		if newSession.ID != op.SessionID || newSession.Execution.ID != op.ExecutionID || newSession.ThreadID == "" || newSession.WorkID == "" || newSession.LaneID == "" || newSession.HostID == "" || newSession.Execution.Generation == 0 {
			return op, false, errors.New("session identities must match the operation")
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO sessions(id,thread_id,data) VALUES(?,?,?)`, newSession.ID, newSession.ThreadID, encoded(newSession)); err != nil {
			return op, false, err
		}
	}
	v, err := session(ctx, tx, op.SessionID)
	if err != nil {
		return op, false, err
	}
	if v.Execution.ID != op.ExecutionID {
		return op, false, conflict("stale execution")
	}
	now := time.Now().UTC()
	op.Outcome = domain.Recorded
	op.CreatedAt = now
	op.UpdatedAt = now
	if _, err = tx.ExecContext(ctx, `INSERT INTO operations(id,session_id,generation,data,payload) VALUES(?,?,?,?,?)`, op.ID, op.SessionID, v.Execution.Generation, encoded(op), payload); err != nil {
		return op, false, err
	}
	if err = event(ctx, tx, v, op.ID, op.Kind+".recorded", "intent persisted before runtime effect"); err != nil {
		return op, false, err
	}
	return op, true, tx.Commit()
}

func event(ctx context.Context, tx *sql.Tx, v domain.Session, op domain.ID, kind, detail string) error {
	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0)+1 FROM events WHERE thread_id=?`, v.ThreadID).Scan(&sequence); err != nil {
		return err
	}
	e := domain.Event{Sequence: sequence, ThreadID: v.ThreadID, SessionID: v.ID, OperationID: op, Kind: kind, Detail: detail, At: time.Now().UTC()}
	_, err := tx.ExecContext(ctx, `INSERT INTO events(thread_id,sequence,data) VALUES(?,?,?)`, v.ThreadID, sequence, encoded(e))
	return err
}

// Attempt is the one durable claim before an external effect. False means the
// effect must not be attempted again, even if a previous response was lost.
func (s *Store) Attempt(ctx context.Context, id domain.ID) (domain.Operation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Operation{}, false, err
	}
	defer tx.Rollback()
	op, _, err := operation(ctx, tx, id)
	if err != nil {
		return op, false, err
	}
	if op.Outcome != domain.Recorded {
		return op, false, nil
	}
	v, err := session(ctx, tx, op.SessionID)
	if err != nil {
		return op, false, err
	}
	generation, err := operationGeneration(ctx, tx, id)
	if err != nil {
		return op, false, err
	}
	if v.Execution.ID != op.ExecutionID || v.Execution.Generation != generation {
		return op, false, conflict("stale execution")
	}
	op.Outcome = domain.Attempted
	op.UpdatedAt = time.Now().UTC()
	if _, err = tx.ExecContext(ctx, `UPDATE operations SET data=? WHERE id=?`, encoded(op), id); err != nil {
		return op, false, err
	}
	if op.Kind == "launch" {
		v.Execution.State = "starting"
		if _, err = tx.ExecContext(ctx, `UPDATE sessions SET data=? WHERE id=?`, encoded(v), v.ID); err != nil {
			return op, false, err
		}
	}
	if err = event(ctx, tx, v, id, op.Kind+".attempted", "external effect may occur after this commit"); err != nil {
		return op, false, err
	}
	return op, true, tx.Commit()
}

// Resolve commits outcome and registry projection in the same transaction.
func (s *Store) Resolve(ctx context.Context, id domain.ID, outcome domain.Outcome, detail string, projection *domain.Session) (domain.Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Operation{}, err
	}
	defer tx.Rollback()
	op, _, err := operation(ctx, tx, id)
	if err != nil {
		return op, err
	}
	if !domain.CanTransition(op.Outcome, outcome) {
		return op, conflict("invalid operation outcome transition")
	}
	v, err := session(ctx, tx, op.SessionID)
	if err != nil {
		return op, err
	}
	generation, err := operationGeneration(ctx, tx, id)
	if err != nil {
		return op, err
	}
	if v.Execution.ID != op.ExecutionID || v.Execution.Generation != generation {
		return op, conflict("stale execution outcome")
	}
	broker, err := codexBound(ctx, tx, v.ID, v.Execution.ID)
	if err != nil {
		return op, err
	}
	if broker && (projection != nil || op.Kind == "stop" && outcome == domain.Completed) {
		return op, conflict("native projection cannot settle or stop the broker primary")
	}
	if projection != nil {
		if projection.ID != v.ID || projection.Execution.ID != v.Execution.ID || projection.Execution.Generation != v.Execution.Generation || projection.ThreadID != v.ThreadID || projection.WorkID != v.WorkID || projection.LaneID != v.LaneID || projection.HostID != v.HostID {
			return op, conflict("outcome cannot replace stable identities or execution")
		}
		v = *projection
	}
	if op.Outcome == outcome && op.Detail == detail {
		return op, nil
	}
	op.Outcome = outcome
	op.Detail = detail
	op.UpdatedAt = time.Now().UTC()
	if _, err = tx.ExecContext(ctx, `UPDATE operations SET data=? WHERE id=?`, encoded(op), id); err != nil {
		return op, err
	}
	if projection != nil {
		if _, err = tx.ExecContext(ctx, `UPDATE sessions SET data=? WHERE id=?`, encoded(v), v.ID); err != nil {
			return op, err
		}
	}
	if op.Kind == "stop" && outcome == domain.Completed {
		if _, err = tx.ExecContext(ctx, `UPDATE grants SET active=0 WHERE session_id=? AND execution_id=?`, v.ID, v.Execution.ID); err != nil {
			return op, err
		}
	}
	if err = event(ctx, tx, v, id, op.Kind+"."+string(outcome), detail); err != nil {
		return op, err
	}
	return op, tx.Commit()
}

func (s *Store) SetState(ctx context.Context, id domain.ID, execution domain.ID, state string) (domain.Session, error) {
	return s.update(ctx, id, execution, func(v *domain.Session) { v.Execution.State = state }, "execution."+state, state)
}

// SetRecovery records an actionable per-session failure without blocking healthy
// registry entries or repeating unchanged recovery events on every inspection.
func (s *Store) SetRecovery(ctx context.Context, id, execution domain.ID, state, detail string) (domain.Session, error) {
	return s.update(ctx, id, execution, func(v *domain.Session) { v.Execution.State = state }, "execution."+state, detail)
}
func (s *Store) SetLinked(ctx context.Context, id domain.ID, execution domain.ID, linked bool) (domain.Session, error) {
	kind := "link.off"
	if linked {
		kind = "link.on"
	}
	return s.update(ctx, id, execution, func(v *domain.Session) { v.Linked = linked }, kind, "managed runtime context verified")
}
func (s *Store) update(ctx context.Context, id, execution domain.ID, modify func(*domain.Session), kind, detail string) (domain.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Session{}, err
	}
	defer tx.Rollback()
	v, err := session(ctx, tx, id)
	if err != nil {
		return v, err
	}
	if v.Execution.ID != execution {
		return v, conflict("stale execution")
	}
	before := encoded(v)
	modify(&v)
	if bytes.Equal(before, encoded(v)) {
		return v, nil
	}
	if _, err = tx.ExecContext(ctx, `UPDATE sessions SET data=? WHERE id=?`, encoded(v), v.ID); err != nil {
		return v, err
	}
	if err = event(ctx, tx, v, "", kind, detail); err != nil {
		return v, err
	}
	return v, tx.Commit()
}

type Grant struct {
	Lease    domain.Lease
	ReadOnly bool
	Active   bool
}

func grant(ctx context.Context, q queryer, id domain.ID) (Grant, error) {
	var b []byte
	var active int
	var g Grant
	err := q.QueryRowContext(ctx, `SELECT data,active FROM grants WHERE operation_id=?`, id).Scan(&b, &active)
	if err != nil {
		return g, missing(err)
	}
	err = json.Unmarshal(b, &g)
	g.Active = active != 0
	return g, err
}
func (s *Store) Grant(ctx context.Context, id domain.ID) (Grant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return grant(ctx, s.db, id)
}

func (s *Store) Acquire(ctx context.Context, l domain.Lease, readOnly bool, payload []byte) (domain.Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return l, err
	}
	defer tx.Rollback()
	v, err := session(ctx, tx, l.SessionID)
	if err != nil {
		return l, err
	}
	if v.Execution.ID != l.ExecutionID || v.Execution.Generation != l.Generation {
		return l, conflict("stale execution generation")
	}
	broker, err := codexBound(ctx, tx, v.ID, v.Execution.ID)
	if err != nil {
		return l, err
	}
	if broker {
		return l, conflict("broker execution requires the verified common authority")
	}
	op, p, err := operation(ctx, tx, l.OperationID)
	if err == nil {
		if op.Kind != "attach" || !bytes.Equal(p, payload) {
			return l, conflict("operation ID already names a different request")
		}
		g, err := grant(ctx, tx, l.OperationID)
		if err != nil {
			return l, err
		}
		if !g.Active {
			return l, conflict("attachment grant was released; use a new acquisition operation")
		}
		return g.Lease, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return l, err
	}
	if l.ClientID == "" || l.OperationID == "" || l.Token == "" {
		return l, errors.New("attachment requires client, operation and lease token")
	}
	if !readOnly {
		blocked, e := codexEffectsBlocked(ctx, tx, l.SessionID, nil)
		if e != nil {
			return l, e
		}
		if blocked {
			return l, conflict("unresolved broker effects reserve the shared writer slot")
		}
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM grants WHERE session_id=? AND active=1 AND read_only=0`, l.SessionID).Scan(&count); err != nil {
			return l, err
		}
		if count != 0 {
			return l, conflict("managed writer lease already held; release its exact token")
		}
	}
	now := time.Now().UTC()
	op = domain.Operation{ID: l.OperationID, Kind: "attach", SessionID: l.SessionID, ExecutionID: l.ExecutionID, Outcome: domain.Completed, Detail: "durable attachment grant; native attachment has not been observed", CreatedAt: now, UpdatedAt: now}
	if _, err = tx.ExecContext(ctx, `INSERT INTO operations(id,session_id,generation,data,payload) VALUES(?,?,?,?,?)`, op.ID, op.SessionID, l.Generation, encoded(op), payload); err != nil {
		return l, err
	}
	g := Grant{Lease: l, ReadOnly: readOnly, Active: true}
	if _, err = tx.ExecContext(ctx, `INSERT INTO grants(operation_id,session_id,execution_id,read_only,active,data) VALUES(?,?,?,?,1,?)`, l.OperationID, l.SessionID, l.ExecutionID, readOnly, encoded(g)); err != nil {
		return l, err
	}
	if err = event(ctx, tx, v, l.OperationID, "attach.granted", op.Detail); err != nil {
		return l, err
	}
	return l, tx.Commit()
}

// Release never revokes another writer, even if the caller shares its client ID.
func (s *Store) Release(ctx context.Context, l domain.Lease) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	v, err := session(ctx, tx, l.SessionID)
	if err != nil {
		return err
	}
	if v.Execution.ID != l.ExecutionID || v.Execution.Generation != l.Generation {
		return conflict("stale execution generation")
	}
	g, err := grant(ctx, tx, l.OperationID)
	if err != nil {
		return err
	}
	if g.Lease != l {
		return conflict("release requires the exact stored lease token, client, execution and operation")
	}
	if !g.Active {
		return nil
	}
	var broker int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM codex_grants WHERE operation_id=?`, l.OperationID).Scan(&broker); err != nil {
		return err
	}
	if broker != 0 {
		return conflict("native projection detach cannot release a broker grant")
	}
	op, _, err := operation(ctx, tx, l.OperationID)
	if err != nil {
		return err
	}
	if op.Kind == "codex.acquire" || op.Kind == "codex.handover" {
		return conflict("broker metadata missing; repair required, reservation retained")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE grants SET active=0 WHERE operation_id=?`, l.OperationID); err != nil {
		return err
	}
	if err = event(ctx, tx, v, l.OperationID, "attach.released", "exact lease released"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Events(ctx context.Context, thread domain.ID, after int64) ([]domain.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM events WHERE thread_id=? AND sequence>? ORDER BY sequence`, thread, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	v := []domain.Event{}
	for rows.Next() {
		var b []byte
		var e domain.Event
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &e); err != nil {
			return nil, err
		}
		v = append(v, e)
	}
	return v, rows.Err()
}
