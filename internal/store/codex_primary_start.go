package store

import (
	"context"
	"encoding/hex"
	"strings"
	"time"

	cb "github.com/seanheiney/keelmux/internal/codexbroker"
	cc "github.com/seanheiney/keelmux/internal/codexcontroller"
	"github.com/seanheiney/keelmux/internal/domain"
)

// PrimaryStartAttempt is an immutable common-store fence, independent of the
// controller journal. It proves only that this incarnation's start was admitted;
// it cannot prove a process exists or that resume succeeded.
type PrimaryStartAttempt struct {
	Version     int
	Incarnation domain.ID
	AttemptedAt time.Time
}

func primaryIncarnation(id domain.ID) bool {
	s := string(id)
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 16 && s == strings.ToLower(s)
}
func validPrimaryStartAttempt(a *PrimaryStartAttempt) bool {
	return a == nil || a.Version == 1 && primaryIncarnation(a.Incarnation) && !a.AttemptedAt.IsZero() && a.AttemptedAt.Year() >= 1970 && a.AttemptedAt.Year() <= 9999
}

// ClaimCodexPrimaryStart runs under the existing Service gate. Only a newly
// committed claim permits the start callback. Matching retries return false
// without mutation; changed incarnations, prior publication or stop fences
// refuse. Losing the controller journal never erases this attempted receipt.
// The reservation's original Request remains unchanged and replayable as data.
func (s *Store) ClaimCodexPrimaryStart(ctx context.Context, req cc.LifecycleRequest) (bool, error) {
	if req.Action != cc.StartPrimary || req.Agent != (cb.AgentBinding{}) || !primaryIncarnation(req.StartIncarnation) {
		return false, conflict("invalid primary start claim")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	r, err := primaryReservation(ctx, tx, req.Target.SessionID, req.Target.ExecutionID)
	if err != nil {
		return false, err
	}
	v, err := session(ctx, tx, req.Target.SessionID)
	if err != nil {
		return false, err
	}
	if req.OperationID != r.Request.OperationID || req.Target != r.Request.Target || v.HostID != req.Target.HostID || v.Execution.ID != req.Target.ExecutionID || v.Execution.Generation != req.Target.Generation || v.Mode != domain.Managed || v.Execution.Provider != "codex" || req.Workspace != v.Workspace || req.Workspace != r.Request.Workspace || req.NativeThreadID != r.Request.NativeThreadID || req.RealmRevision != r.Request.RealmRevision {
		return false, conflict("start claim differs from durable primary selection")
	}
	if r.Start != nil {
		if r.Start.Incarnation != req.StartIncarnation {
			return false, conflict("primary start already attempted by another incarnation; reconcile without replay")
		}
		return false, nil
	}
	if fenced, e := primaryStopReserved(ctx, tx, v.ID, v.Execution.ID); e != nil {
		return false, e
	} else if fenced || v.Execution.State != "primary_reserved" {
		return false, conflict("primary start is fenced or was already published")
	}
	var published int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM codex_agents WHERE session_id=? AND execution_id=?`, v.ID, v.Execution.ID).Scan(&published); err != nil {
		return false, err
	}
	if published != 0 {
		return false, conflict("existing primary evidence cannot authorize another start")
	}
	op, _, err := operation(ctx, tx, req.OperationID)
	if err != nil {
		return false, err
	}
	if op.Outcome != domain.Recorded {
		return false, conflict("primary reservation is not an unattempted intent")
	}
	now := time.Now().UTC()
	r.Start = &PrimaryStartAttempt{Version: 1, Incarnation: req.StartIncarnation, AttemptedAt: now}
	op.Outcome = domain.Attempted
	op.UpdatedAt = now
	op.Detail = "primary start admission durably claimed; process and resume outcome unknown"
	if _, err = tx.ExecContext(ctx, `UPDATE operations SET data=?,payload=? WHERE id=?`, encoded(op), encoded(r), op.ID); err != nil {
		return false, err
	}
	if err = event(ctx, tx, v, op.ID, "codex.primary.start_attempted", op.Detail); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
