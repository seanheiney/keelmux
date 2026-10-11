package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	cb "github.com/seanheiney/keelmux/internal/codexbroker"
	cc "github.com/seanheiney/keelmux/internal/codexcontroller"
	"github.com/seanheiney/keelmux/internal/domain"
)

const primaryReserveKind = "codex.primary.reserve"

// PrimaryReservation is durable authority for an explicitly selected existing
// native thread and local realm. It is not provider/account identity evidence.
type PrimaryReservation struct {
	Name    string
	Request cc.StartRequest
	Start   *PrimaryStartAttempt `json:",omitempty"`
	Stdio   *PrimaryStdioAnchor  `json:",omitempty"`
}

func primaryReservation(ctx context.Context, q queryer, sessionID, executionID domain.ID) (PrimaryReservation, error) {
	reader, ok := q.(interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	})
	if !ok {
		return PrimaryReservation{}, repair("primary query unavailable")
	}
	rows, err := reader.QueryContext(ctx, `SELECT id,session_id,generation,data,payload FROM operations WHERE session_id=?`, sessionID)
	if err != nil {
		return PrimaryReservation{}, err
	}
	defer rows.Close()
	var found *PrimaryReservation
	for rows.Next() {
		var raw, data []byte
		var id, sid domain.ID
		var generation uint64
		var op domain.Operation
		if err = rows.Scan(&id, &sid, &generation, &raw, &data); err != nil {
			return PrimaryReservation{}, err
		}
		if json.Unmarshal(raw, &op) != nil {
			return PrimaryReservation{}, repair("invalid primary operation")
		}
		if op.Kind != primaryReserveKind || op.ExecutionID != executionID {
			continue
		}
		var r PrimaryReservation
		if json.Unmarshal(data, &r) != nil || !bytes.Equal(encoded(r), data) || !bytes.Equal(encoded(op), raw) || op.ID != id || op.SessionID != sid || r.Request.OperationID != op.ID || r.Request.Target.SessionID != sessionID || r.Request.Target.ExecutionID != executionID || r.Request.Target.Generation != generation || !validPrimaryStartAttempt(r.Start) || !validPrimaryStdioAnchor(r.Stdio) || found != nil {
			return PrimaryReservation{}, repair("invalid or duplicate primary reservation")
		}
		found = &r
	}
	if err = rows.Err(); err != nil {
		return PrimaryReservation{}, err
	}
	if found == nil {
		return PrimaryReservation{}, ErrNotFound
	}
	return *found, nil
}

func primaryStopReserved(ctx context.Context, q queryer, sessionID, executionID domain.ID) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM operations WHERE session_id=? AND json_extract(data,'$.execution_id')=? AND json_extract(data,'$.kind')='codex.primary.stop'`, sessionID, executionID).Scan(&n)
	return n != 0, err
}

func (s *Store) CodexPrimaryReservation(ctx context.Context, sessionID, executionID domain.ID) (PrimaryReservation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return primaryReservation(ctx, s.db, sessionID, executionID)
}

func (s *Store) ReserveCodexPrimary(ctx context.Context, v domain.Session, r PrimaryReservation) error {
	_, _, err := s.Record(ctx, domain.Operation{ID: r.Request.OperationID, Kind: primaryReserveKind, SessionID: v.ID, ExecutionID: v.Execution.ID}, encoded(r), &v)
	return err
}

// PublishCodexPrimary records only the exact binding freshly verified by the
// controller. No writer is installed and no native terminal is advertised.
func (s *Store) PublishCodexPrimary(ctx context.Context, a cb.AgentBinding) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := primaryReservation(ctx, tx, a.SessionID, a.ExecutionID)
	if err != nil {
		return err
	}
	v, err := session(ctx, tx, a.SessionID)
	if err != nil {
		return err
	}
	if r.Start == nil || r.Start.Incarnation != a.AppServerIncarnation || a.HostID != v.HostID || a.Generation != v.Execution.Generation || a.ExecutionID != v.Execution.ID || a.NativeThreadID != r.Request.NativeThreadID || a.ProtocolVersion != cc.ProtocolVersion || v.Execution.State == "primary_stopping" || v.Execution.State == "primary_stopped" {
		return conflict("primary binding differs from current reservation or is fenced")
	}
	blocked, err := codexEffectsBlocked(ctx, tx, v.ID, nil)
	if err != nil {
		return err
	}
	if blocked {
		return conflict("primary has outstanding or incomplete evidence")
	}
	if r.Stdio != nil {
		l, err := primaryStdioLedger(ctx, tx, r)
		if err != nil {
			return err
		}
		runtime := l.Channel.Ref.Runtime
		if !primaryStdioReady(l) || a.AppServerPID != runtime.PID || a.AppServerBootID != runtime.BootID || a.AppServerProcessStart != runtime.ProcessStart {
			return conflict("primary stdio requires exact complete imported readiness")
		}
	}
	var prior []byte
	err = tx.QueryRowContext(ctx, `SELECT data FROM codex_agents WHERE session_id=? AND execution_id=?`, a.SessionID, a.ExecutionID).Scan(&prior)
	if err == nil && !bytes.Equal(prior, encoded(a)) {
		return conflict("primary binding is immutable")
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO codex_agents(session_id,execution_id,data) VALUES(?,?,?)`, a.SessionID, a.ExecutionID, encoded(a)); err != nil {
		return err
	}
	if fenced, e := primaryStopReserved(ctx, tx, v.ID, v.Execution.ID); e != nil {
		return e
	} else if fenced {
		return conflict("primary stop fence survives restart")
	}
	if v.Execution.State == "primary_ready" {
		return tx.Commit()
	}
	v.Execution.State = "primary_ready"
	v.Execution.NativeThread = domain.Evidence{Value: a.NativeThreadID, Source: "owned_codex_primary", ObservedAt: time.Now().UTC()}
	if _, err = tx.ExecContext(ctx, `UPDATE sessions SET data=? WHERE id=?`, encoded(v), v.ID); err != nil {
		return err
	}
	if err = event(ctx, tx, v, r.Request.OperationID, "codex.primary.verified", "owned primary verified; product submission and native projection unavailable"); err != nil {
		return err
	}
	return tx.Commit()
}

// FenceCodexPrimaryStop checks and commits under the same transaction. Active
// grants remain blockers, including observers; this initial lifecycle has no
// grant retirement or writable-projection handover path. Unknown effects block.
func (s *Store) FenceCodexPrimaryStop(ctx context.Context, req cc.LifecycleRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	v, err := session(ctx, tx, req.Target.SessionID)
	if err != nil {
		return err
	}
	if v.Execution.ID != req.Target.ExecutionID || v.Execution.Generation != req.Target.Generation {
		return conflict("stale primary stop")
	}
	reserved, err := primaryReservation(ctx, tx, v.ID, v.Execution.ID)
	if err != nil {
		return err
	}
	if req.Action != cc.StopPrimary || req.Target != reserved.Request.Target || req.Workspace != reserved.Request.Workspace || req.NativeThreadID != reserved.Request.NativeThreadID || req.RealmRevision != reserved.Request.RealmRevision {
		return conflict("primary stop differs from durable selection")
	}
	retry := false
	if old, raw, e := operation(ctx, tx, req.OperationID); e == nil {
		if old.Kind != "codex.primary.stop" || !bytes.Equal(raw, encoded(req)) {
			return conflict("stop operation already used")
		}
		retry = true
	} else if !errors.Is(e, ErrNotFound) {
		return e
	}
	var agent []byte
	if err = tx.QueryRowContext(ctx, `SELECT data FROM codex_agents WHERE session_id=? AND execution_id=?`, v.ID, v.Execution.ID).Scan(&agent); err != nil {
		return missing(err)
	}
	if !bytes.Equal(agent, encoded(req.Agent)) {
		return conflict("stop requires original verified primary")
	}
	blocked, err := codexEffectsBlocked(ctx, tx, v.ID, nil)
	if err != nil {
		return err
	}
	var grants int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM grants WHERE session_id=? AND active=1`, v.ID).Scan(&grants); err != nil {
		return err
	}
	if blocked || grants != 0 {
		return conflict("primary has outstanding effects, callbacks or active grants")
	}
	if retry {
		return nil
	}
	if fenced, e := primaryStopReserved(ctx, tx, v.ID, v.Execution.ID); e != nil {
		return e
	} else if fenced {
		return conflict("another stop owns the durable primary fence")
	}
	if v.Execution.State == "primary_stopping" || v.Execution.State == "primary_stopped" {
		return conflict("primary already fenced by another stop")
	}
	now := time.Now().UTC()
	op := domain.Operation{ID: req.OperationID, Kind: "codex.primary.stop", SessionID: v.ID, ExecutionID: v.Execution.ID, Outcome: domain.Attempted, CreatedAt: now, UpdatedAt: now, Detail: "primary admission durably fenced; stop acknowledgement unknown"}
	if _, err = tx.ExecContext(ctx, `INSERT INTO operations(id,session_id,generation,data,payload) VALUES(?,?,?,?,?)`, op.ID, v.ID, v.Execution.Generation, encoded(op), encoded(req)); err != nil {
		return err
	}
	v.Execution.State = "primary_stopping"
	if _, err = tx.ExecContext(ctx, `UPDATE sessions SET data=? WHERE id=?`, encoded(v), v.ID); err != nil {
		return err
	}
	if err = event(ctx, tx, v, op.ID, "codex.primary.stop_fenced", op.Detail); err != nil {
		return err
	}
	return tx.Commit()
}

// CompleteCodexPrimaryStop records observed process death without removing the
// reservation or stop fence. Completion never makes replacement replayable.
func (s *Store) CompleteCodexPrimaryStop(ctx context.Context, req cc.StopRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	op, raw, err := operation(ctx, tx, req.OperationID)
	if err != nil {
		return err
	}
	var fenced cc.LifecycleRequest
	if op.Kind != "codex.primary.stop" || json.Unmarshal(raw, &fenced) != nil || fenced.Target != req.Target || fenced.Agent.AppServerIncarnation != req.Incarnation {
		return conflict("stop completion differs from durable fence")
	}
	v, err := session(ctx, tx, req.Target.SessionID)
	if err != nil {
		return err
	}
	if v.Execution.ID != req.Target.ExecutionID || v.Execution.Generation != req.Target.Generation {
		return conflict("stale stop completion")
	}
	if op.Outcome == domain.Completed && v.Execution.State == "primary_stopped" {
		return nil
	}
	op.Outcome = domain.Completed
	op.UpdatedAt = time.Now().UTC()
	op.Detail = "owned primary process death verified; admission remains fenced"
	v.Execution.State = "primary_stopped"
	if _, err = tx.ExecContext(ctx, `UPDATE operations SET data=? WHERE id=?`, encoded(op), op.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE sessions SET data=? WHERE id=?`, encoded(v), v.ID); err != nil {
		return err
	}
	if err = event(ctx, tx, v, op.ID, "codex.primary.stopped", op.Detail); err != nil {
		return err
	}
	return tx.Commit()
}
