package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	cb "github.com/seanheiney/keelmux/internal/codexbroker"
	"github.com/seanheiney/keelmux/internal/domain"
	ps "github.com/seanheiney/keelmux/internal/primarystdio"
)

const (
	primaryStdioChannelKind = "codex.primary.stdio.channel"
	primaryStdioImportKind  = "codex.primary.stdio.import"
	// These lifetime caps never compact or forget established continuity.
	MaxPrimaryStdioImports       = 4096
	MaxPrimaryStdioEvents        = 4096
	MaxPrimaryStdioBytes         = 32 << 20
	maxPrimaryStdioRecordBytes   = 512 << 10
	maxPrimaryStdioMetadataBytes = 4096
)

// PrimaryStdioAnchor names the complete retained journal, not the greatest row
// that happens to survive. It is atomically updated with every immutable import.
type PrimaryStdioAnchor struct {
	Version                                                int
	ChannelOperationID                                     domain.ID
	ChannelSHA256, LastImportSHA256                        string
	Ledger                                                 ps.Ledger
	EventCount, StoredBytes, BlockingEvents, BlockingBytes uint64
}

type primaryStdioImport struct {
	Batch                ps.ImportBatch
	Receipt              ps.ImportReceipt
	PreviousImportID     domain.ID
	PreviousImportSHA256 string
}

type primaryStdioRecord struct {
	op         domain.Operation
	payload    []byte
	generation uint64
}
type primaryStdioQuery interface {
	queryer
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func primaryStdioSHA(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func primaryStdioChannelID(c ps.Channel) domain.ID {
	return domain.ID("primary_stdio_" + string(c.Ref.ChannelIncarnation))
}
func primaryStdioBinding(r PrimaryReservation) ps.Binding {
	b := ps.Binding{HostID: r.Request.Target.HostID, SessionID: r.Request.Target.SessionID, ExecutionID: r.Request.Target.ExecutionID, Generation: r.Request.Target.Generation, StartOperationID: r.Request.OperationID, NativeThreadID: r.Request.NativeThreadID, RealmRevision: r.Request.RealmRevision}
	if r.Start != nil {
		b.PrimaryIncarnation = r.Start.Incarnation
	}
	return b
}
func validPrimaryStdioAnchor(a *PrimaryStdioAnchor) bool {
	return a == nil || a.Version == ps.Version && a.Ledger.Bound && ps.ValidateChannel(a.Ledger.Channel) == nil && ps.ValidateCursor(a.Ledger.Cursor) == nil && a.ChannelOperationID == primaryStdioChannelID(a.Ledger.Channel) && a.ChannelSHA256 == primaryStdioSHA(encoded(a.Ledger.Channel)) && a.Ledger.ImportCount <= MaxPrimaryStdioImports && a.EventCount <= MaxPrimaryStdioEvents && a.StoredBytes <= MaxPrimaryStdioBytes && a.BlockingEvents <= ps.MaxEvents && a.BlockingBytes <= ps.MaxEventBytes
}
func initialPrimaryStdioAnchor(c ps.Channel) PrimaryStdioAnchor {
	b := encoded(c)
	return PrimaryStdioAnchor{Version: ps.Version, ChannelOperationID: primaryStdioChannelID(c), ChannelSHA256: primaryStdioSHA(b), StoredBytes: uint64(len(b)), Ledger: ps.Ledger{Bound: true, Channel: c, Cursor: ps.Genesis(c.Ref.ChannelIncarnation), Continuity: ps.ContinuityUnknown}}
}
func primaryStdioReady(l ps.Ledger) bool {
	s := l.Snapshot
	return l.Bound && l.ImportCount != 0 && l.Continuity == ps.ContinuityComplete && !l.Pending && l.Cursor == s.Cursor && s.Channel == l.Channel.Ref && s.Binding == l.Channel.Spec.Binding && s.Bootstrap == ps.BootstrapComplete && s.Continuity == ps.ContinuityComplete && !s.Pending && s.ProcessAlive && s.ExactImage && s.ExactDirectories && s.ExactLoadedThread && s.Idle && s.EmptyQueue && s.EmptyBackground
}
func primaryStdioAppend(a PrimaryStdioAnchor, record primaryStdioImport) (PrimaryStdioAnchor, error) {
	b, o := record.Batch, record.Batch.Observation
	if ps.ValidateImport(b) != nil || b.Channel != a.Ledger.Channel || o.From != a.Ledger.Cursor || record.PreviousImportID != a.Ledger.LastImportID || record.PreviousImportSHA256 != a.LastImportSHA256 || record.Receipt != (ps.ImportReceipt{ImportID: b.ImportID, Channel: b.Channel.Ref, From: o.From, Through: o.Through, BatchSHA256: o.BatchSHA256}) {
		return a, ps.ErrConflict
	}
	n := a
	n.Ledger.ImportCount++
	n.EventCount += uint64(len(o.Events))
	n.StoredBytes += uint64(len(encoded(record)))
	if n.Ledger.ImportCount > MaxPrimaryStdioImports || n.EventCount > MaxPrimaryStdioEvents || n.StoredBytes > MaxPrimaryStdioBytes {
		return a, ps.ErrUnavailable
	}
	continuity := o.Snapshot.Continuity
	if a.Ledger.Continuity == ps.ContinuityBroken || continuity == ps.ContinuityBroken {
		continuity = ps.ContinuityBroken
	} else if a.Ledger.ImportCount != 0 && a.Ledger.Continuity == ps.ContinuityUnknown {
		continuity = ps.ContinuityUnknown
	}
	n.Ledger.Pending = a.Ledger.Pending || o.Snapshot.Pending
	for _, e := range o.Events {
		if ps.EventBlocks(e) {
			n.Ledger.Pending = true
			// A zero-byte gap is the bounded terminal fault marker, not another
			// retained native callback payload beyond the event-storage ceiling.
			if e.Kind != ps.EventGap {
				n.BlockingEvents++
				n.BlockingBytes += uint64(e.FrameBytes)
			}
		}
		if e.Kind == ps.EventGap || e.Kind == ps.EventMalformed {
			continuity = ps.ContinuityBroken
		}
	}
	if n.BlockingEvents > ps.MaxEvents || n.BlockingBytes > ps.MaxEventBytes {
		return a, ps.ErrUnavailable
	}
	n.Ledger.Continuity = continuity
	n.Ledger.Cursor = o.Through
	n.Ledger.LastImportID = b.ImportID
	n.Ledger.LastBatchSHA256 = o.BatchSHA256
	n.Ledger.Snapshot = o.Snapshot
	n.LastImportSHA256 = primaryStdioSHA(encoded(record))
	return n, nil
}

// All stdio rows in this session are read, so a foreign execution or orphan row
// cannot masquerade as an empty selected stream. Canonical row identities and
// generation columns are checked independently of their JSON payloads.
func primaryStdioRecords(ctx context.Context, q primaryStdioQuery, sessionID, executionID domain.ID) (map[domain.ID]primaryStdioRecord, error) {
	rows, err := q.QueryContext(ctx, `SELECT id,session_id,generation,data,payload FROM operations WHERE session_id=?`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[domain.ID]primaryStdioRecord{}
	foreign := map[domain.ID]bool{}
	var retainedBytes uint64
	stdioRows := 0
	for rows.Next() {
		var id, sid domain.ID
		var generation uint64
		var raw, payload []byte
		if err = rows.Scan(&id, &sid, &generation, &raw, &payload); err != nil {
			return nil, err
		}
		var op domain.Operation
		if json.Unmarshal(raw, &op) != nil {
			return nil, repair("invalid primary operation metadata")
		}
		if !strings.HasPrefix(op.Kind, "codex.primary.stdio.") {
			continue
		}
		if op.Kind != primaryStdioChannelKind && op.Kind != primaryStdioImportKind || len(raw) > maxPrimaryStdioMetadataBytes || !bytes.Equal(encoded(op), raw) || op.ID != id || op.SessionID != sid || op.ExecutionID == "" || op.Outcome != domain.Completed || op.CreatedAt.IsZero() || op.UpdatedAt != op.CreatedAt || len(payload) > maxPrimaryStdioRecordBytes {
			return nil, repair("invalid primary stdio operation")
		}
		// Bound all selected stdio payloads BEFORE retaining a row or foreign
		// identity. Individually bounded corrupt rows must not add up to GiB.
		stdioRows++
		if stdioRows > MaxPrimaryStdioImports+1 || uint64(len(payload)) > MaxPrimaryStdioBytes-retainedBytes {
			return nil, repair("primary stdio retained evidence exceeds lifetime bound")
		}
		retainedBytes += uint64(len(payload))
		if op.ExecutionID != executionID {
			foreign[op.ExecutionID] = true
			continue
		}
		if generation == 0 {
			return nil, repair("missing primary stdio generation")
		}
		out[id] = primaryStdioRecord{op: op, payload: payload, generation: generation}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for execution := range foreign {
		r, err := primaryReservation(ctx, q, sessionID, execution)
		if err != nil || r.Stdio == nil {
			return nil, repair("orphan foreign primary stdio journal")
		}
	}
	return out, nil
}

func primaryStdioLedger(ctx context.Context, q primaryStdioQuery, r PrimaryReservation) (ps.Ledger, error) {
	rows, err := primaryStdioRecords(ctx, q, r.Request.Target.SessionID, r.Request.Target.ExecutionID)
	if err != nil {
		return ps.Ledger{}, err
	}
	if r.Stdio == nil {
		if len(rows) != 0 {
			return ps.Ledger{}, repair("orphan primary stdio journal without anchor")
		}
		return ps.Ledger{}, nil
	}
	a := *r.Stdio
	if !validPrimaryStdioAnchor(&a) || r.Start == nil || a.Ledger.Channel.Spec.Binding != primaryStdioBinding(r) || a.Ledger.Channel.Spec.Workspace != r.Request.Workspace {
		return ps.Ledger{}, repair("invalid primary stdio reservation anchor")
	}
	channelRow, ok := rows[a.ChannelOperationID]
	if !ok || channelRow.op.Kind != primaryStdioChannelKind || channelRow.op.Detail != "immutable primary stdio channel evidence" || !bytes.Equal(channelRow.payload, encoded(a.Ledger.Channel)) || len(rows) != int(a.Ledger.ImportCount)+1 {
		return ps.Ledger{}, repair("missing, extra or changed primary stdio channel/import receipt")
	}
	// The SQL generation is checked for every selected row separately below.
	for _, row := range rows {
		if row.generation != r.Request.Target.Generation {
			return ps.Ledger{}, repair("primary stdio journal generation mismatch")
		}
	}
	ordered := make([]primaryStdioImport, int(a.Ledger.ImportCount))
	id, wantSHA := a.Ledger.LastImportID, a.LastImportSHA256
	visited := map[domain.ID]bool{}
	for i := len(ordered) - 1; i >= 0; i-- {
		row, ok := rows[id]
		var record primaryStdioImport
		if !ok || visited[id] || row.op.Kind != primaryStdioImportKind || row.op.Detail != "immutable primary stdio import receipt" || primaryStdioSHA(row.payload) != wantSHA || json.Unmarshal(row.payload, &record) != nil || !bytes.Equal(encoded(record), row.payload) || record.Batch.ImportID != id {
			return ps.Ledger{}, repair("missing, changed or cyclic primary stdio import receipt")
		}
		visited[id] = true
		ordered[i] = record
		id, wantSHA = record.PreviousImportID, record.PreviousImportSHA256
	}
	if id != "" || wantSHA != "" {
		return ps.Ledger{}, repair("primary stdio import count or predecessor mismatch")
	}
	derived := initialPrimaryStdioAnchor(a.Ledger.Channel)
	for _, record := range ordered {
		derived, err = primaryStdioAppend(derived, record)
		if err != nil {
			return ps.Ledger{}, repair("invalid primary stdio prefix or lifetime bound")
		}
	}
	if !bytes.Equal(encoded(derived), encoded(a)) {
		return ps.Ledger{}, repair("primary stdio anchor differs from complete retained journal")
	}
	return derived.Ledger, nil
}

func (s *Store) CodexPrimaryStdio(ctx context.Context, b ps.Binding) (ps.Ledger, error) {
	if ps.ValidateBinding(b) != nil {
		return ps.Ledger{}, ps.ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := primaryReservation(ctx, s.db, b.SessionID, b.ExecutionID)
	if err != nil {
		return ps.Ledger{}, err
	}
	if b != primaryStdioBinding(r) {
		return ps.Ledger{}, ps.ErrConflict
	}
	v, err := session(ctx, s.db, b.SessionID)
	if err != nil {
		return ps.Ledger{}, err
	}
	if v.HostID != b.HostID || v.Execution.ID != b.ExecutionID || v.Execution.Generation != b.Generation {
		return ps.Ledger{}, ps.ErrConflict
	}
	return primaryStdioLedger(ctx, s.db, r)
}

// InvalidateCodexPrimaryStdio records loss of operational currentness only.
// It neither fabricates receive evidence nor retires a blocker or stop fence.
// Fresh imported evidence and the Service's live helper guard may publish again.
func (s *Store) InvalidateCodexPrimaryStdio(ctx context.Context, b ps.Binding) error {
	if ps.ValidateBinding(b) != nil {
		return ps.ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := primaryReservation(ctx, tx, b.SessionID, b.ExecutionID)
	if err != nil {
		return err
	}
	v, err := session(ctx, tx, b.SessionID)
	if err != nil {
		return err
	}
	if b != primaryStdioBinding(r) || v.HostID != b.HostID || v.Execution.ID != b.ExecutionID || v.Execution.Generation != b.Generation || v.Mode != domain.Managed || v.Execution.Provider != "codex" || v.Workspace != r.Request.Workspace {
		return ps.ErrConflict
	}
	if _, err = primaryStdioLedger(ctx, tx, r); err != nil {
		return err
	}
	switch v.Execution.State {
	case "primary_reserved", "primary_ready":
		v.Execution.State = "primary_uncertain"
	case "primary_uncertain", "primary_stopping", "primary_stopped":
		return nil
	default:
		return ps.ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `UPDATE sessions SET data=? WHERE id=?`, encoded(v), v.ID); err != nil {
		return err
	}
	if err = event(ctx, tx, v, r.Request.OperationID, "codex.primary.stdio.currentness_lost", "current stdio verification unavailable; retained evidence and admission fences preserved"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) BindCodexPrimaryStdio(ctx context.Context, c ps.Channel) (ps.Ledger, error) {
	if ps.ValidateChannel(c) != nil {
		return ps.Ledger{}, ps.ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ps.Ledger{}, err
	}
	defer tx.Rollback()
	b := c.Spec.Binding
	r, err := primaryReservation(ctx, tx, b.SessionID, b.ExecutionID)
	if err != nil {
		return ps.Ledger{}, err
	}
	v, err := session(ctx, tx, b.SessionID)
	if err != nil {
		return ps.Ledger{}, err
	}
	if b != primaryStdioBinding(r) || v.HostID != b.HostID || v.Execution.ID != b.ExecutionID || v.Execution.Generation != b.Generation || v.Mode != domain.Managed || v.Execution.Provider != "codex" || c.Spec.Workspace != v.Workspace || c.Spec.Workspace != r.Request.Workspace {
		return ps.Ledger{}, ps.ErrConflict
	}
	l, err := primaryStdioLedger(ctx, tx, r)
	if err != nil {
		return ps.Ledger{}, err
	}
	if l.Bound {
		if l.Channel != c {
			return ps.Ledger{}, ps.ErrConflict
		}
		return l, nil
	}
	fenced, err := primaryStopReserved(ctx, tx, v.ID, v.Execution.ID)
	if err != nil {
		return ps.Ledger{}, err
	}
	if fenced || r.Start == nil || v.Execution.State != "primary_reserved" {
		return ps.Ledger{}, ps.ErrConflict
	}
	var published, grants int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM codex_agents WHERE session_id=? AND execution_id=?`, v.ID, v.Execution.ID).Scan(&published); err != nil {
		return ps.Ledger{}, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM grants WHERE session_id=? AND active=1`, v.ID).Scan(&grants); err != nil {
		return ps.Ledger{}, err
	}
	if published != 0 || grants != 0 {
		return ps.Ledger{}, ps.ErrConflict
	}
	a := initialPrimaryStdioAnchor(c)
	if _, _, err = operation(ctx, tx, a.ChannelOperationID); err == nil {
		return ps.Ledger{}, ps.ErrConflict
	} else if !errors.Is(err, ErrNotFound) {
		return ps.Ledger{}, err
	}
	if err = insertPrimaryStdioOperation(ctx, tx, v, a.ChannelOperationID, primaryStdioChannelKind, "immutable primary stdio channel evidence", encoded(c)); err != nil {
		return ps.Ledger{}, err
	}
	r.Stdio = &a
	if _, err = tx.ExecContext(ctx, `UPDATE operations SET payload=? WHERE id=?`, encoded(r), r.Request.OperationID); err != nil {
		return ps.Ledger{}, err
	}
	if err = event(ctx, tx, v, a.ChannelOperationID, "codex.primary.stdio.bound", "stdio evidence bound; bootstrap and product authority unavailable"); err != nil {
		return ps.Ledger{}, err
	}
	if err = tx.Commit(); err != nil {
		return ps.Ledger{}, err
	}
	return a.Ledger, nil
}

func insertPrimaryStdioOperation(ctx context.Context, tx *sql.Tx, v domain.Session, id domain.ID, kind, detail string, payload []byte) error {
	if len(payload) > maxPrimaryStdioRecordBytes {
		return ps.ErrUnavailable
	}
	now := time.Now().UTC()
	op := domain.Operation{ID: id, Kind: kind, SessionID: v.ID, ExecutionID: v.Execution.ID, Outcome: domain.Completed, Detail: detail, CreatedAt: now, UpdatedAt: now}
	_, err := tx.ExecContext(ctx, `INSERT INTO operations(id,session_id,generation,data,payload) VALUES(?,?,?,?,?)`, id, v.ID, v.Execution.Generation, encoded(op), payload)
	return err
}

func (s *Store) ImportCodexPrimaryStdio(ctx context.Context, b ps.ImportBatch) (ps.ImportReceipt, error) {
	if ps.ValidateImport(b) != nil {
		return ps.ImportReceipt{}, ps.ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ps.ImportReceipt{}, err
	}
	defer tx.Rollback()
	bound := b.Channel.Spec.Binding
	r, err := primaryReservation(ctx, tx, bound.SessionID, bound.ExecutionID)
	if err != nil {
		return ps.ImportReceipt{}, err
	}
	v, err := session(ctx, tx, bound.SessionID)
	if err != nil {
		return ps.ImportReceipt{}, err
	}
	if r.Stdio == nil || bound != primaryStdioBinding(r) || v.HostID != bound.HostID || v.Execution.ID != bound.ExecutionID || v.Execution.Generation != bound.Generation {
		return ps.ImportReceipt{}, ps.ErrConflict
	}
	l, err := primaryStdioLedger(ctx, tx, r)
	if err != nil {
		return ps.ImportReceipt{}, err
	}
	if l.Channel != b.Channel {
		return ps.ImportReceipt{}, ps.ErrConflict
	}
	if op, raw, e := operation(ctx, tx, b.ImportID); e == nil {
		var prior primaryStdioImport
		if op.Kind != primaryStdioImportKind || json.Unmarshal(raw, &prior) != nil || !bytes.Equal(encoded(prior.Batch), encoded(b)) {
			return ps.ImportReceipt{}, ps.ErrConflict
		}
		return prior.Receipt, nil
	} else if !errors.Is(e, ErrNotFound) {
		return ps.ImportReceipt{}, e
	}
	o := b.Observation
	receipt := ps.ImportReceipt{ImportID: b.ImportID, Channel: b.Channel.Ref, From: o.From, Through: o.Through, BatchSHA256: o.BatchSHA256}
	record := primaryStdioImport{Batch: b, Receipt: receipt, PreviousImportID: l.LastImportID, PreviousImportSHA256: r.Stdio.LastImportSHA256}
	a, err := primaryStdioAppend(*r.Stdio, record)
	if err != nil {
		return ps.ImportReceipt{}, err
	}
	if err = insertPrimaryStdioOperation(ctx, tx, v, b.ImportID, primaryStdioImportKind, "immutable primary stdio import receipt", encoded(record)); err != nil {
		return ps.ImportReceipt{}, err
	}
	r.Stdio = &a
	if _, err = tx.ExecContext(ctx, `UPDATE operations SET payload=? WHERE id=?`, encoded(r), r.Request.OperationID); err != nil {
		return ps.ImportReceipt{}, err
	}
	if !primaryStdioReady(a.Ledger) {
		if err = fencePrimaryStdioGrants(ctx, tx, v.ID, v.Execution.ID); err != nil {
			return ps.ImportReceipt{}, err
		}
		if v.Execution.State == "primary_ready" {
			v.Execution.State = "primary_uncertain"
			if _, err = tx.ExecContext(ctx, `UPDATE sessions SET data=? WHERE id=?`, encoded(v), v.ID); err != nil {
				return ps.ImportReceipt{}, err
			}
		}
	}
	if err = event(ctx, tx, v, b.ImportID, "codex.primary.stdio.imported", "contiguous evidence committed; acknowledgment grants no callback or writer authority"); err != nil {
		return ps.ImportReceipt{}, err
	}
	if err = tx.Commit(); err != nil {
		return ps.ImportReceipt{}, err
	}
	return receipt, nil
}

func fencePrimaryStdioGrants(ctx context.Context, tx *sql.Tx, sessionID, executionID domain.ID) error {
	rows, err := tx.QueryContext(ctx, `SELECT c.data FROM codex_grants c JOIN grants g ON g.operation_id=c.operation_id WHERE g.session_id=? AND g.execution_id=? AND g.active=1`, sessionID, executionID)
	if err != nil {
		return err
	}
	var grants []cb.Grant
	for rows.Next() {
		var raw []byte
		var g cb.Grant
		if err = rows.Scan(&raw); err != nil || json.Unmarshal(raw, &g) != nil {
			rows.Close()
			return repair("invalid grant during primary evidence fence")
		}
		grants = append(grants, g)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, g := range grants {
		g.Admission = cb.Fenced
		if _, err = tx.ExecContext(ctx, `UPDATE codex_grants SET data=? WHERE operation_id=?`, encoded(g), g.Lease.OperationID); err != nil {
			return err
		}
	}
	return nil
}

func primaryStdioBlocked(ctx context.Context, q primaryStdioQuery, sessionID domain.ID, agent *cb.AgentBinding) (bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT data,payload FROM operations WHERE session_id=?`, sessionID)
	if err != nil {
		return true, err
	}
	var reservations []PrimaryReservation
	for rows.Next() {
		var raw, payload []byte
		var op domain.Operation
		var r PrimaryReservation
		if err = rows.Scan(&raw, &payload); err != nil || json.Unmarshal(raw, &op) != nil {
			rows.Close()
			return true, repair("invalid primary stdio metadata")
		}
		if op.Kind != primaryReserveKind {
			continue
		}
		if json.Unmarshal(payload, &r) != nil || !bytes.Equal(encoded(r), payload) || !validPrimaryStartAttempt(r.Start) || !validPrimaryStdioAnchor(r.Stdio) {
			rows.Close()
			return true, repair("invalid primary stdio reservation")
		}
		if agent == nil || r.Request.Target.ExecutionID == agent.ExecutionID && r.Request.Target.Generation == agent.Generation {
			reservations = append(reservations, r)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return true, err
	}
	blocked := false
	for _, r := range reservations {
		validated, err := primaryReservation(ctx, q, r.Request.Target.SessionID, r.Request.Target.ExecutionID)
		if err != nil {
			return true, err
		}
		r = validated
		l, err := primaryStdioLedger(ctx, q, r)
		if err != nil {
			return true, err
		}
		if l.Bound && !primaryStdioReady(l) {
			blocked = true
		}
	}
	return blocked, nil
}

// Validation adds no tables/migration and never reconstructs lost evidence.
func validatePrimaryStdioSchema(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT data,payload FROM operations`)
	if err != nil {
		return err
	}
	var reservations []PrimaryReservation
	stdioExecutions := map[[2]domain.ID]bool{}
	for rows.Next() {
		var raw, payload []byte
		var op domain.Operation
		if err = rows.Scan(&raw, &payload); err != nil || json.Unmarshal(raw, &op) != nil {
			rows.Close()
			return repair("invalid primary stdio operation metadata")
		}
		if strings.HasPrefix(op.Kind, "codex.primary.stdio.") {
			stdioExecutions[[2]domain.ID{op.SessionID, op.ExecutionID}] = true
		}
		if op.Kind == primaryReserveKind {
			var r PrimaryReservation
			if json.Unmarshal(payload, &r) != nil || !bytes.Equal(encoded(r), payload) || r.Request.OperationID != op.ID || r.Request.Target.SessionID != op.SessionID || r.Request.Target.ExecutionID != op.ExecutionID || !validPrimaryStartAttempt(r.Start) || !validPrimaryStdioAnchor(r.Stdio) {
				rows.Close()
				return repair("invalid primary stdio reservation anchor")
			}
			reservations = append(reservations, r)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	seen := map[[2]domain.ID]bool{}
	for _, r := range reservations {
		key := [2]domain.ID{r.Request.Target.SessionID, r.Request.Target.ExecutionID}
		if seen[key] {
			return repair("duplicate primary reservation")
		}
		seen[key] = true
		validated, err := primaryReservation(ctx, db, r.Request.Target.SessionID, r.Request.Target.ExecutionID)
		if err != nil {
			return err
		}
		r = validated
		if _, err = primaryStdioLedger(ctx, db, r); err != nil {
			return err
		}
		delete(stdioExecutions, key)
	}
	if len(stdioExecutions) != 0 {
		return repair("orphan primary stdio journal")
	}
	return nil
}
