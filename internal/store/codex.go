package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	cb "github.com/seanheiney/keelmux/internal/codexbroker"
	"github.com/seanheiney/keelmux/internal/domain"
)

// Broker metadata augments grants; active and the existing one_writer index
// remain the single source of writer ownership.
func codexGrant(ctx context.Context, q queryer, id domain.ID) (cb.Grant, cb.AgentBinding, error) {
	var g cb.Grant
	var a cb.AgentBinding
	var b, agent []byte
	var active int
	err := q.QueryRowContext(ctx, `SELECT c.data,c.agent,g.active FROM codex_grants c JOIN grants g ON g.operation_id=c.operation_id WHERE c.operation_id=?`, id).Scan(&b, &agent, &active)
	if err != nil {
		return g, a, missing(err)
	}
	if err = json.Unmarshal(b, &g); err == nil {
		err = json.Unmarshal(agent, &a)
	}
	g.Active = active != 0
	return g, a, err
}

func codexBound(ctx context.Context, q queryer, session, execution domain.ID) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM codex_agents WHERE session_id=? AND execution_id=?`, session, execution).Scan(&n)
	if err != nil || n != 0 {
		return n != 0, err
	}
	_, err = primaryReservation(ctx, q, session, execution)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) CodexBound(ctx context.Context, session, execution domain.ID) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return codexBound(ctx, s.db, session, execution)
}

func (s *Store) CodexGrant(ctx context.Context, id domain.ID) (cb.Grant, cb.AgentBinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return codexGrant(ctx, s.db, id)
}

func same(a, b any) bool { return bytes.Equal(encoded(a), encoded(b)) }

func nextEpoch(ctx context.Context, tx *sql.Tx, id domain.ID) (uint64, error) {
	var n int64
	err := tx.QueryRowContext(ctx, `SELECT value FROM codex_epochs WHERE session_id=?`, id).Scan(&n)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if n < 0 || n == math.MaxInt64 {
		return 0, conflict("writer epoch exhausted")
	}
	n++
	_, err = tx.ExecContext(ctx, `INSERT INTO codex_epochs(session_id,value) VALUES(?,?) ON CONFLICT(session_id) DO UPDATE SET value=excluded.value`, id, n)
	return uint64(n), err
}

func insertCodexGrant(ctx context.Context, tx *sql.Tx, v domain.Session, req cb.WriterRequest, kind string, request []byte) (cb.Grant, error) {
	epoch, err := nextEpoch(ctx, tx, v.ID)
	if err != nil {
		return cb.Grant{}, err
	}
	g := cb.Grant{WriterRef: cb.WriterRef{
		Lease:       domain.Lease{SessionID: v.ID, ExecutionID: v.Execution.ID, Generation: v.Execution.Generation, OperationID: req.OperationID, ClientID: req.ClientID, Token: string(domain.NewID("lease"))},
		PrincipalID: req.PrincipalID, Kind: req.Kind, Epoch: epoch,
	}, ReadOnly: req.ReadOnly, Active: true, Admission: cb.Admitting}
	now := time.Now().UTC()
	op := domain.Operation{ID: req.OperationID, Kind: kind, SessionID: v.ID, ExecutionID: v.Execution.ID, Outcome: domain.Completed, CreatedAt: now, UpdatedAt: now, Detail: "verified broker grant; native projection is not primary lifecycle evidence"}
	if _, err = tx.ExecContext(ctx, `INSERT INTO operations(id,session_id,generation,data,payload) VALUES(?,?,?,?,?)`, op.ID, v.ID, v.Execution.Generation, encoded(op), request); err != nil {
		return g, err
	}
	legacy := Grant{Lease: g.Lease, ReadOnly: g.ReadOnly, Active: true}
	if _, err = tx.ExecContext(ctx, `INSERT INTO grants(operation_id,session_id,execution_id,read_only,active,data) VALUES(?,?,?,?,1,?)`, op.ID, v.ID, v.Execution.ID, g.ReadOnly, encoded(legacy)); err != nil {
		return g, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO codex_grants(operation_id,data,agent) VALUES(?,?,?)`, op.ID, encoded(g), encoded(req.Agent)); err != nil {
		return g, err
	}
	if err = event(ctx, tx, v, op.ID, kind+".granted", op.Detail); err != nil {
		return g, err
	}
	return g, nil
}

// CodexAcquire is called only under the Service gate after home verification.
func (s *Store) CodexAcquire(ctx context.Context, req cb.WriterRequest) (cb.Grant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return cb.Grant{}, err
	}
	defer tx.Rollback()
	if op, p, e := operation(ctx, tx, req.OperationID); e == nil {
		if op.Kind != "codex.acquire" || !bytes.Equal(p, encoded(req)) {
			return cb.Grant{}, conflict("operation ID already names a different acquisition")
		}
		g, _, e := codexGrant(ctx, tx, req.OperationID)
		return g, e // A retry returns the original fenced/retired receipt, never rearms it.
	} else if !errors.Is(e, ErrNotFound) {
		return cb.Grant{}, e
	}
	v, err := session(ctx, tx, req.Agent.SessionID)
	if err != nil {
		return cb.Grant{}, err
	}
	if v.HostID != req.Agent.HostID || v.Execution.ID != req.Agent.ExecutionID || v.Execution.Generation != req.Agent.Generation {
		return cb.Grant{}, conflict("stale app-server execution binding")
	}
	if reservation, e := primaryReservation(ctx, tx, v.ID, v.Execution.ID); e == nil {
		if reservation.Stdio != nil {
			if _, err := primaryStdioLedger(ctx, tx, reservation); err != nil {
				return cb.Grant{}, err
			}
			return cb.Grant{}, conflict("primary stdio writer and observer acquisition unavailable")
		}
		fenced, err := primaryStopReserved(ctx, tx, v.ID, v.Execution.ID)
		if err != nil {
			return cb.Grant{}, err
		}
		if fenced || v.Execution.State != "primary_ready" {
			return cb.Grant{}, conflict("reserved primary is not verified or admission is fenced")
		}
	} else if !errors.Is(e, ErrNotFound) {
		return cb.Grant{}, e
	}
	var prior []byte
	err = tx.QueryRowContext(ctx, `SELECT data FROM codex_agents WHERE session_id=? AND execution_id=?`, v.ID, v.Execution.ID).Scan(&prior)
	if err == nil && !bytes.Equal(prior, encoded(req.Agent)) {
		return cb.Grant{}, conflict("app-server lifetime binding is immutable")
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return cb.Grant{}, err
	}
	// Even a broker observer cannot promote a live legacy native writer.
	var legacy, writers int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM grants g LEFT JOIN codex_grants c ON c.operation_id=g.operation_id WHERE g.session_id=? AND g.active=1 AND g.read_only=0 AND c.operation_id IS NULL`, v.ID).Scan(&legacy); err != nil {
		return cb.Grant{}, err
	}
	if legacy != 0 {
		return cb.Grant{}, conflict("legacy native writer owns the shared slot")
	}
	if !req.ReadOnly {
		blocked, e := codexEffectsBlocked(ctx, tx, v.ID, nil)
		if e != nil {
			return cb.Grant{}, e
		}
		if blocked {
			return cb.Grant{}, conflict("unresolved broker effects reserve the shared writer slot")
		}
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM grants WHERE session_id=? AND active=1 AND read_only=0`, v.ID).Scan(&writers); err != nil {
			return cb.Grant{}, err
		}
		if writers != 0 {
			return cb.Grant{}, conflict("shared writer slot is reserved")
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO codex_agents(session_id,execution_id,data) VALUES(?,?,?)`, v.ID, v.Execution.ID, encoded(req.Agent)); err != nil {
		return cb.Grant{}, err
	}
	g, err := insertCodexGrant(ctx, tx, v, req, "codex.acquire", encoded(req))
	if err != nil {
		return g, err
	}
	return g, tx.Commit()
}

func codexConnection(ctx context.Context, q queryer, id domain.ID) (cb.ConnectionBinding, bool, error) {
	var c cb.ConnectionBinding
	var b []byte
	var retired bool
	err := q.QueryRowContext(ctx, `SELECT data,retired FROM codex_connections WHERE id=?`, id).Scan(&b, &retired)
	if err == nil {
		err = json.Unmarshal(b, &c)
	}
	return c, retired, missing(err)
}
func (s *Store) CodexConnection(ctx context.Context, id domain.ID) (cb.ConnectionBinding, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return codexConnection(ctx, s.db, id)
}
func (s *Store) CodexBind(ctx context.Context, c cb.ConnectionBinding) (cb.ConnectionBinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return c, err
	}
	defer tx.Rollback()
	if old, retired, e := codexConnection(ctx, tx, c.ID); e == nil {
		if retired || !same(old, c) {
			return old, conflict("connection incarnation is immutable and never rearms")
		}
		return old, nil
	} else if !errors.Is(e, ErrNotFound) {
		return c, e
	}
	g, a, err := codexGrant(ctx, tx, c.Writer.Lease.OperationID)
	if err != nil {
		return c, err
	}
	if !g.Active || g.Admission != cb.Admitting || !same(g.WriterRef, c.Writer) || !same(a, c.Agent) {
		return c, conflict("connection has no admitting original grant")
	}
	// A fresh connection ID must not disguise an old endpoint or native UI.
	rows, err := tx.QueryContext(ctx, `SELECT data FROM codex_connections`)
	if err != nil {
		return c, err
	}
	reused := false
	for rows.Next() {
		var b []byte
		var old cb.ConnectionBinding
		if err = rows.Scan(&b); err == nil {
			err = json.Unmarshal(b, &old)
		}
		if err != nil {
			rows.Close()
			return c, err
		}
		if old.EndpointIncarnation == c.EndpointIncarnation || old.NativeLaunchID == c.NativeLaunchID ||
			c.Role == cb.NativeLocal && old.Role == cb.NativeLocal && old.Agent.AppServerBootID == c.Agent.AppServerBootID && old.NativePID == c.NativePID && old.NativeProcessStart == c.NativeProcessStart {
			reused = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return c, err
	}
	if reused {
		return c, conflict("old endpoint, launch or native process cannot bind at a new epoch")
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO codex_connections(id,data,retired) VALUES(?,?,0)`, c.ID, encoded(c)); err != nil {
		return c, err
	}
	v, err := session(ctx, tx, a.SessionID)
	if err != nil {
		return c, err
	}
	if err = event(ctx, tx, v, g.Lease.OperationID, "codex.connection.bound", "owned connection bound to immutable writer epoch"); err != nil {
		return c, err
	}
	return c, tx.Commit()
}

func codexReceipt(ctx context.Context, q queryer, id domain.ID) (cb.Receipt, error) {
	var r cb.Receipt
	var b []byte
	err := q.QueryRowContext(ctx, `SELECT data FROM codex_receipts WHERE operation_id=?`, id).Scan(&b)
	if err == nil {
		err = json.Unmarshal(b, &r)
	}
	return r, missing(err)
}
func (s *Store) CodexReceipt(ctx context.Context, id domain.ID) (cb.Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return codexReceipt(ctx, s.db, id)
}

// CodexAttempt atomically records intent and the one durable attempted claim.
// No transaction survives this call, and no attempted receipt is replayable.
func (s *Store) CodexAttempt(ctx context.Context, in cb.Intent, class cb.EffectClass) (cb.Receipt, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return cb.Receipt{}, false, err
	}
	defer tx.Rollback()
	if old, e := codexReceipt(ctx, tx, in.OperationID); e == nil {
		if !same(old.Intent, in) {
			return old, false, conflict("operation ID has different original provenance or payload")
		}
		return old, false, nil
	} else if !errors.Is(e, ErrNotFound) {
		return cb.Receipt{}, false, e
	}
	g, a, err := codexGrant(ctx, tx, in.Origin.Writer.Lease.OperationID)
	if err != nil {
		return cb.Receipt{}, false, err
	}
	c, retired, err := codexConnection(ctx, tx, in.Origin.ID)
	if err != nil {
		return cb.Receipt{}, false, err
	}
	v, err := session(ctx, tx, a.SessionID)
	if err != nil {
		return cb.Receipt{}, false, err
	}
	if retired || !same(c, in.Origin) || !same(a, in.Origin.Agent) || !same(g.WriterRef, in.Origin.Writer) || !g.Active || g.Admission != cb.Admitting || v.Execution.ID != a.ExecutionID || v.Execution.Generation != a.Generation {
		return cb.Receipt{}, false, conflict("original connection is fenced, retired or stale")
	}
	// No late response can be reassigned through request-ID reuse. The owned
	// controller maps a fresh upstream channel to each immutable connection.
	rows, err := tx.QueryContext(ctx, `SELECT data,effect_class FROM codex_receipts`)
	if err != nil {
		return cb.Receipt{}, false, err
	}
	collision := false
	for rows.Next() {
		var b []byte
		var prior cb.Receipt
		var priorClass cb.EffectClass
		if err = rows.Scan(&b, &priorClass); err == nil {
			err = json.Unmarshal(b, &prior)
		}
		if err != nil {
			rows.Close()
			return cb.Receipt{}, false, err
		}
		if class != cb.ViewerLocal && priorClass != cb.ViewerLocal {
			if prior.Intent.Origin.ID == in.Origin.ID && prior.Intent.UpstreamChannelID != in.UpstreamChannelID {
				collision = true
			}
			if in.Frame.Kind == cb.Request && prior.Intent.Frame.Kind == cb.Request && prior.Intent.UpstreamChannelID == in.UpstreamChannelID && bytes.Equal(prior.Intent.Frame.ID, in.Frame.ID) {
				collision = true
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return cb.Receipt{}, false, err
	}
	if collision {
		return cb.Receipt{}, false, conflict("upstream channel or request identity was already bound")
	}
	if class == cb.NativeBootstrap && (in.Frame.Method == "initialize" || in.Frame.Method == "initialized" || in.Frame.Method == "thread/resume") {
		rows, e := tx.QueryContext(ctx, `SELECT data FROM codex_receipts WHERE effect_class=?`, cb.NativeBootstrap)
		if e != nil {
			return cb.Receipt{}, false, e
		}
		duplicate := false
		for rows.Next() {
			var b []byte
			var r cb.Receipt
			if e = rows.Scan(&b); e == nil {
				e = json.Unmarshal(b, &r)
			}
			if e != nil {
				rows.Close()
				return cb.Receipt{}, false, e
			}
			if r.Intent.Origin.ID == in.Origin.ID && r.Intent.Frame.Method == in.Frame.Method {
				duplicate = true
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return cb.Receipt{}, false, e
		}
		if duplicate {
			return cb.Receipt{}, false, conflict("native bootstrap is one-shot for this connection")
		}
	}
	now := time.Now().UTC()
	op := domain.Operation{ID: in.OperationID, Kind: "codex.rpc", SessionID: a.SessionID, ExecutionID: a.ExecutionID, Outcome: domain.Attempted, CreatedAt: now, UpdatedAt: now, Detail: "bounded upstream write may occur; acknowledgement unknown"}
	if _, err = tx.ExecContext(ctx, `INSERT INTO operations(id,session_id,generation,data,payload) VALUES(?,?,?,?,?)`, op.ID, op.SessionID, a.Generation, encoded(op), encoded(in)); err != nil {
		return cb.Receipt{}, false, err
	}
	r := cb.Receipt{Intent: in, State: cb.Attempted, Detail: op.Detail}
	if _, err = tx.ExecContext(ctx, `INSERT INTO codex_receipts(operation_id,effect_class,data) VALUES(?,?,?)`, op.ID, class, encoded(r)); err != nil {
		return r, false, err
	}
	if err = event(ctx, tx, v, op.ID, "codex.rpc.attempted", op.Detail); err != nil {
		return r, false, err
	}
	return r, true, tx.Commit()
}

func receiptTransition(old, next cb.ReceiptState) bool {
	if old == next {
		return true
	}
	switch old {
	case cb.Attempted, cb.PendingAck:
		return next == cb.PendingAck || next == cb.Uncertain || next == cb.Acknowledged || next == cb.Completed || next == cb.Failed
	case cb.Acknowledged, cb.Uncertain:
		return next == cb.Uncertain || next == cb.Acknowledged || next == cb.Completed || next == cb.Failed
	}
	return false
}
func updateCodexReceipt(ctx context.Context, tx *sql.Tx, r cb.Receipt) error {
	op, _, err := operation(ctx, tx, r.Intent.OperationID)
	if err != nil {
		return err
	}
	switch r.State {
	case cb.Completed:
		op.Outcome = domain.Completed
	case cb.Failed:
		op.Outcome = domain.Failed
	case cb.Uncertain:
		op.Outcome = domain.Uncertain
	default:
		op.Outcome = domain.Attempted
	}
	op.UpdatedAt = time.Now().UTC()
	op.Detail = "broker receipt " + string(r.State) + "; no execution or current-owner projection"
	if _, err = tx.ExecContext(ctx, `UPDATE operations SET data=? WHERE id=?`, encoded(op), op.ID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE codex_receipts SET data=? WHERE operation_id=?`, encoded(r), op.ID)
	return err
}

// Historical settlement updates the original receipt only, even after the
// current execution or writer changes. It does not call generic Resolve.
func (s *Store) CodexSettle(ctx context.Context, r cb.Receipt) (cb.Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return r, err
	}
	defer tx.Rollback()
	old, err := codexReceipt(ctx, tx, r.Intent.OperationID)
	if err != nil {
		return old, err
	}
	if !same(old.Intent, r.Intent) || !receiptTransition(old.State, r.State) {
		return old, conflict("settlement differs from original operation/channel/request/epoch")
	}
	if old.NativeTurnID != "" && old.NativeTurnID != r.NativeTurnID || old.NativeItemID != "" && old.NativeItemID != r.NativeItemID {
		return old, conflict("native receipt identity changed")
	}
	if same(old, r) {
		return old, nil
	}
	if err = updateCodexReceipt(ctx, tx, r); err != nil {
		return old, err
	}
	v, err := session(ctx, tx, r.Intent.Origin.Agent.SessionID)
	if err != nil {
		return old, err
	}
	if err = event(ctx, tx, v, r.Intent.OperationID, "codex.rpc."+string(r.State), "original broker receipt settled; current execution and writer unchanged"); err != nil {
		return old, err
	}
	return r, tx.Commit()
}

func fenceCodex(ctx context.Context, tx *sql.Tx, ref cb.WriterRef, retire bool) (cb.Grant, error) {
	g, _, err := codexGrant(ctx, tx, ref.Lease.OperationID)
	if err != nil {
		return g, err
	}
	if !same(g.WriterRef, ref) {
		return g, conflict("fence requires the exact original writer identity")
	}
	if !g.Active {
		return g, nil
	}
	g.Admission = cb.Fenced
	if retire {
		g.Admission = cb.Retired
		g.Active = false
	}
	if _, err = tx.ExecContext(ctx, `UPDATE codex_grants SET data=? WHERE operation_id=?`, encoded(g), ref.Lease.OperationID); err != nil {
		return g, err
	}
	if retire {
		if _, err = tx.ExecContext(ctx, `UPDATE grants SET active=0 WHERE operation_id=?`, ref.Lease.OperationID); err != nil {
			return g, err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,data FROM codex_connections WHERE retired=0`)
	if err != nil {
		return g, err
	}
	var ids []domain.ID
	for rows.Next() {
		var id domain.ID
		var b []byte
		var c cb.ConnectionBinding
		if err = rows.Scan(&id, &b); err == nil {
			err = json.Unmarshal(b, &c)
		}
		if err != nil {
			rows.Close()
			return g, err
		}
		if same(c.Writer, ref) {
			ids = append(ids, id)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return g, err
	}
	for _, id := range ids {
		if _, err = tx.ExecContext(ctx, `UPDATE codex_connections SET retired=1 WHERE id=?`, id); err != nil {
			return g, err
		}
	}
	return g, nil
}
func (s *Store) CodexFence(ctx context.Context, ref cb.WriterRef) (cb.Grant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return cb.Grant{}, err
	}
	defer tx.Rollback()
	g, err := fenceCodex(ctx, tx, ref, false)
	if err != nil {
		return g, err
	}
	v, err := session(ctx, tx, ref.Lease.SessionID)
	if err != nil {
		return g, err
	}
	if err = event(ctx, tx, v, ref.Lease.OperationID, "codex.writer.fenced", "writer reserved; old connections cannot rearm"); err != nil {
		return g, err
	}
	return g, tx.Commit()
}

func codexBlocked(ctx context.Context, tx *sql.Tx, a cb.AgentBinding) (bool, error) {
	return codexEffectsBlocked(ctx, tx, a.SessionID, &a)
}

// A new acquisition must not leap an unresolved effect merely because a grant
// is inactive or the session projection now points at a later execution.
// Handover uses the exact agent; ordinary acquisition checks the whole session.
func codexEffectsBlocked(ctx context.Context, tx *sql.Tx, id domain.ID, agent *cb.AgentBinding) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT r.data,r.effect_class FROM codex_receipts r JOIN operations o ON o.id=r.operation_id WHERE o.session_id=?`, id)
	if err != nil {
		return true, err
	}
	blocked := false
	for rows.Next() {
		var b []byte
		var r cb.Receipt
		var class cb.EffectClass
		if err = rows.Scan(&b, &class); err == nil {
			err = json.Unmarshal(b, &r)
		}
		if err != nil {
			rows.Close()
			return true, err
		}
		resolved := r.State == cb.Completed || r.State == cb.Failed || r.State == cb.Acknowledged && class != cb.Mutation
		origin := r.Intent.Origin.Agent
		if !resolved && (agent == nil || origin.ExecutionID == agent.ExecutionID && origin.Generation == agent.Generation) {
			blocked = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return true, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT data FROM codex_callbacks`)
	if err != nil {
		return true, err
	}
	defer rows.Close()
	for rows.Next() {
		var b []byte
		var c cb.CallbackRecord
		if err = rows.Scan(&b); err == nil {
			err = json.Unmarshal(b, &c)
		}
		if err != nil {
			return true, err
		}
		if c.Origin.Agent.SessionID == id && !c.Resolved && (agent == nil || same(c.Origin.Agent, *agent)) {
			blocked = true
		}
	}
	if err = rows.Err(); err != nil {
		return true, err
	}
	rows.Close()
	stdioBlocked, err := primaryStdioBlocked(ctx, tx, id, agent)
	return blocked || stdioBlocked, err
}
func (s *Store) CodexBlocked(ctx context.Context, a cb.AgentBinding) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return true, err
	}
	defer tx.Rollback()
	return codexBlocked(ctx, tx, a)
}

func (s *Store) CodexHandover(ctx context.Context, h cb.Handover) (cb.Grant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return cb.Grant{}, err
	}
	defer tx.Rollback()
	if op, p, e := operation(ctx, tx, h.OperationID); e == nil {
		if op.Kind != "codex.handover" || !bytes.Equal(p, encoded(h)) {
			return cb.Grant{}, conflict("handover operation ID collision")
		}
		g, _, e := codexGrant(ctx, tx, h.OperationID)
		return g, e
	} else if !errors.Is(e, ErrNotFound) {
		return cb.Grant{}, e
	}
	g, a, err := codexGrant(ctx, tx, h.Previous.Lease.OperationID)
	if err != nil {
		return g, err
	}
	if !same(g.WriterRef, h.Previous) || !g.Active || g.ReadOnly {
		return g, conflict("handover requires the exact reserved writer")
	}
	v, err := session(ctx, tx, a.SessionID)
	if err != nil {
		return g, err
	}
	if v.Execution.ID != a.ExecutionID || v.Execution.Generation != a.Generation {
		return g, conflict("handover cannot promote a stale execution")
	}
	blocked, err := codexBlocked(ctx, tx, a)
	if err != nil {
		return g, err
	}
	if blocked {
		return g, conflict("pending send or privileged callback blocks handover")
	}
	if _, err = fenceCodex(ctx, tx, h.Previous, true); err != nil {
		return g, err
	}
	req := cb.WriterRequest{Agent: a, OperationID: h.OperationID, PrincipalID: h.NextPrincipalID, ClientID: h.NextClientID, Kind: h.NextKind}
	g, err = insertCodexGrant(ctx, tx, v, req, "codex.handover", encoded(h))
	if err != nil {
		return g, err
	}
	return g, tx.Commit()
}

// Missing native IDs are retained exactly as absent quarantine evidence, never
// usable for attribution or a decision. No wire/request ID is invented here.
func validCallbackQuarantine(c cb.CallbackRecord) bool {
	if len(c.Key.RequestID) != 0 && !bytes.Equal(bytes.TrimSpace(c.Key.RequestID), []byte("null")) {
		return true
	}
	return !c.Attributed && !c.Resolved && len(c.Allowed) == 0 && c.DisplayConnectionID == "" && c.DecisionOperationID == "" && c.DecisionState == cb.Uncertain
}

func (s *Store) CodexCallback(ctx context.Context, c cb.CallbackRecord) (cb.CallbackRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validCallbackQuarantine(c) {
		return c, conflict("callback without native request ID requires non-actionable quarantine")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return c, err
	}
	defer tx.Rollback()
	var prior []byte
	err = tx.QueryRowContext(ctx, `SELECT data FROM codex_callbacks WHERE id=?`, c.ID).Scan(&prior)
	if err == nil {
		var old cb.CallbackRecord
		if err = json.Unmarshal(prior, &old); err != nil {
			return c, err
		}
		if !same(old, c) {
			return old, conflict("callback ID names different original evidence")
		}
		return old, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return c, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO codex_callbacks(id,data) VALUES(?,?)`, c.ID, encoded(c)); err != nil {
		return c, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT c.data FROM codex_grants c JOIN grants g ON c.operation_id=g.operation_id WHERE g.session_id=? AND g.execution_id=? AND g.active=1`, c.Origin.Agent.SessionID, c.Origin.Agent.ExecutionID)
	if err != nil {
		return c, err
	}
	var refs []cb.WriterRef
	for rows.Next() {
		var b []byte
		var g cb.Grant
		if err = rows.Scan(&b); err == nil {
			err = json.Unmarshal(b, &g)
		}
		if err != nil {
			rows.Close()
			return c, err
		}
		refs = append(refs, g.WriterRef)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return c, err
	}
	for _, ref := range refs {
		if _, err = fenceCodex(ctx, tx, ref, false); err != nil {
			return c, err
		}
	}
	v, err := session(ctx, tx, c.Origin.Agent.SessionID)
	if err != nil {
		return c, err
	}
	if err = event(ctx, tx, v, c.OriginOperationID, "codex.callback.repair_required", "privileged callback recorded; no approval decision supported"); err != nil {
		return c, err
	}
	return c, tx.Commit()
}

// RecoverCodex never derives primary death from a disposable pane. A fresh
// trusted controller must settle exact unknown receipts and obtain a new epoch.
func (s *Store) RecoverCodex(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT c.data FROM codex_grants c JOIN grants g ON g.operation_id=c.operation_id WHERE g.active=1`)
	if err != nil {
		return err
	}
	var refs []cb.WriterRef
	for rows.Next() {
		var b []byte
		var g cb.Grant
		if err = rows.Scan(&b); err == nil {
			err = json.Unmarshal(b, &g)
		}
		if err != nil {
			rows.Close()
			return err
		}
		refs = append(refs, g.WriterRef)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, ref := range refs {
		if _, err = fenceCodex(ctx, tx, ref, false); err != nil {
			return err
		}
	}
	rows, err = tx.QueryContext(ctx, `SELECT data,effect_class FROM codex_receipts`)
	if err != nil {
		return err
	}
	var unknown []cb.Receipt
	for rows.Next() {
		var b []byte
		var r cb.Receipt
		var class cb.EffectClass
		if err = rows.Scan(&b, &class); err == nil {
			err = json.Unmarshal(b, &r)
		}
		if err != nil {
			rows.Close()
			return err
		}
		if r.State == cb.Attempted || r.State == cb.PendingAck || r.State == cb.Acknowledged && class == cb.Mutation {
			r.State = cb.Uncertain
			r.Detail = "restart lost exact native acknowledgement; no replay"
			unknown = append(unknown, r)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, r := range unknown {
		if err = updateCodexReceipt(ctx, tx, r); err != nil {
			return err
		}
		v, e := session(ctx, tx, r.Intent.Origin.Agent.SessionID)
		if e != nil {
			return e
		}
		if err = event(ctx, tx, v, r.Intent.OperationID, "codex.rpc.outcome_uncertain", "restart lost exact native acknowledgement; original effect will not replay"); err != nil {
			return err
		}
	}
	return tx.Commit()
}
