package store

import (
	"context"
	"database/sql"
	"math"

	c "github.com/seanheiney/keelmux/internal/captain"
	"github.com/seanheiney/keelmux/internal/domain"
)

type captainEntry struct {
	Version        uint32            `json:"version"`
	ID             domain.ID         `json:"id"`
	Assignment     c.Ref             `json:"assignment"`
	Sequence       uint32            `json:"sequence"`
	Kind           string            `json:"kind"`
	Actor          domain.ID         `json:"actor"`
	ActorRevision  uint64            `json:"actor_revision"`
	PreviousDigest string            `json:"previous_digest"`
	Scan           *c.Scan           `json:"scan"`
	Reservation    *c.Reservation    `json:"reservation"`
	Report         *c.Report         `json:"report"`
	ReservationRef *c.ReservationRef `json:"reservation_ref"`
	Digest         string            `json:"digest"`
}

func captainEntryDigest(e captainEntry) string { e.Digest = ""; return c.Digest("captain.entry.v1", e) }

func captainAdvance(st *c.Status, scan c.Scan) error {
	if !captainSame(st.Cursors, scan.Before) || uint64(st.Coverage.UnsupportedEvents)+uint64(scan.Coverage.UnsupportedEvents) > math.MaxUint32 {
		return c.ErrCorrupt
	}
	st.Cursors = append([]c.Position{}, scan.Through...)
	st.Coverage.UnsupportedEvents += scan.Coverage.UnsupportedEvents
	return nil
}

func historicalCaptainRole(a *captainAuthority, id domain.ID, rev uint64, role c.Role) bool {
	for _, cmd := range a.commands {
		if cmd.ObjectID == id && cmd.Revision == rev && cmd.Principal != nil {
			return c.HasRole(*cmd.Principal, role)
		}
	}
	return false
}

func loadCaptainLedger(ctx context.Context, q captainQuery, a *captainAuthority, id domain.ID) (c.Status, []captainEntry, error) {
	v, ok := a.assignments[id]
	if !ok {
		return c.Status{}, nil, c.ErrNotFound
	}
	var n int64
	var raw []byte
	if err := q.QueryRowContext(ctx, `SELECT length(data),CASE WHEN length(data)<=524288 THEN data END FROM captain_assignments WHERE id=?`, id).Scan(&n, &raw); err != nil {
		return c.Status{}, nil, err
	}
	if n <= 0 || n > c.MaxRecordBytes {
		return c.Status{}, nil, c.ErrCorrupt
	}
	saved, err := captainDecode[c.Status](raw)
	if err != nil {
		return c.Status{}, nil, err
	}
	initial, err := historicalCaptainAssignment(a, c.Ref{ID: id, Revision: 1})
	if err != nil {
		return c.Status{}, nil, err
	}
	st := c.Status{Assignment: v, Cursors: []c.Position{}, Coverage: c.EmptyCoverage(), EntryDigest: c.EntryGenesis(initial)}
	for _, ref := range initial.Grants {
		g, e := historicalCaptainGrant(a, ref)
		if e != nil {
			return st, nil, e
		}
		st.Cursors = append(st.Cursors, c.Position{Grant: ref, Through: g.Source.FirstSequence - 1, AnchorDigest: c.Genesis(g)})
	}
	entries := []captainEntry{}
	var pending *c.Reservation
	err = captainRows(ctx, q, `SELECT sequence,id,kind,reservation_id,report_id,length(data),CASE WHEN length(data)<=524288 THEN data END FROM captain_entries WHERE assignment_id=? ORDER BY sequence LIMIT 1025`, c.MaxEntries, c.MaxLedgerBytes, func(rows *sql.Rows, raw []byte) error {
		var seq uint32
		var eid, reserveID, reportID domain.ID
		var kind string
		var n int64
		var b []byte
		if err := rows.Scan(&seq, &eid, &kind, &reserveID, &reportID, &n, &b); err != nil {
			return err
		}
		e, err := captainDecode[captainEntry](raw)
		if err != nil {
			return err
		}
		if e.Version != c.Version || e.Assignment.ID != id || e.Sequence != seq || seq != st.EntryCount+1 || e.ID != eid || e.Kind != kind || e.PreviousDigest != st.EntryDigest || e.Digest != captainEntryDigest(e) || !c.ValidDigest(e.Digest) || a.commands[e.ID].ID != "" {
			return c.ErrCorrupt
		}
		historical, err := historicalCaptainAssignment(a, e.Assignment)
		if err != nil {
			return err
		}
		if kind == "cancel" {
			if e.Actor != historical.OwnerPrincipal || !historicalCaptainRole(a, e.Actor, e.ActorRevision, c.Owner) {
				return c.ErrCorrupt
			}
		} else if historical.Control != c.Active || e.Actor != historical.RunnerPrincipal || !historicalCaptainRole(a, e.Actor, e.ActorRevision, c.Runner) {
			return c.ErrCorrupt
		}
		if kind != "cancel" {
			scan := e.Scan
			if kind == "reserve" && e.Reservation != nil {
				scan = &e.Reservation.Scan
			}
			if kind == "report" && e.Report != nil {
				scan = &e.Report.Scan
			}
			if scan == nil || scan.Assignment != e.Assignment || !captainSame(scan.Before, st.Cursors) {
				return c.ErrCorrupt
			}
			if err = validateStoredCaptainScanMetadata(a, *scan); err != nil {
				return err
			}
		}
		switch kind {
		case "quiet":
			if !c.ValidID(e.ID, c.RequestIDKind) || e.Scan == nil || len(e.Scan.Events) == 0 || len(c.Findings(*e.Scan)) != 0 || e.Reservation != nil || e.Report != nil || e.ReservationRef != nil || reserveID != "" || reportID != "" || pending != nil {
				return c.ErrCorrupt
			}
			if err = captainAdvance(&st, *e.Scan); err != nil {
				return err
			}
		case "reserve":
			r := e.Reservation
			if r == nil || c.ValidateReservation(*r) != nil || r.Ref.ID != e.ID || reserveID != r.Ref.ID || reportID != "" || e.Scan != nil || e.Report != nil || e.ReservationRef != nil || pending != nil || r.Assignment != e.Assignment || r.RunnerPrincipal != e.Actor || r.RoleRevision != e.ActorRevision || r.Ordinal != st.ReservationsConsumed+1 || st.ReservationsConsumed >= initial.MaxReservations {
				return c.ErrCorrupt
			}
			pending = r
			st.ReservationsConsumed++
			copyRef := r.Ref
			st.Pending = &copyRef
		case "report":
			r := e.Report
			if r == nil || c.ValidateReport(*r) != nil || r.ID != e.ID || reportID != r.ID || reserveID != r.Reservation.ID || e.Scan != nil || e.Reservation != nil || e.ReservationRef != nil || pending == nil || r.Reservation != pending.Ref || !captainSame(r.Scan, pending.Scan) || r.Assignment != e.Assignment || r.Usage.ReservationsConsumed != st.ReservationsConsumed || e.ActorRevision != pending.RoleRevision {
				return c.ErrCorrupt
			}
			if err = captainAdvance(&st, r.Scan); err != nil {
				return err
			}
			pending = nil
			st.Pending = nil
		case "cancel":
			if !c.ValidID(e.ID, c.RequestIDKind) || e.ReservationRef == nil || pending == nil || *e.ReservationRef != pending.Ref || reserveID != pending.Ref.ID || reportID != "" || e.Scan != nil || e.Reservation != nil || e.Report != nil {
				return c.ErrCorrupt
			}
			pending = nil
			st.Pending = nil
		default:
			return c.ErrCorrupt
		}
		st.EntryCount++
		st.EntryDigest = e.Digest
		st.StoredBytes += uint64(len(raw))
		entries = append(entries, e)
		return nil
	}, id)
	if err != nil {
		return st, nil, err
	}
	if !captainSame(st, saved) {
		return st, nil, c.ErrCorrupt
	}
	return st, entries, nil
}

func appendCaptainEntry(ctx context.Context, tx *sql.Tx, a *captainAuthority, st *c.Status, e captainEntry) error {
	if st.EntryCount >= c.MaxEntries || a.commands[e.ID].ID != "" {
		return c.ErrCapacity
	}
	var used int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM captain_entries WHERE id=?`, e.ID).Scan(&used); err != nil {
		return err
	}
	if used != 0 {
		return c.ErrConflict
	}
	e.Version = c.Version
	e.Sequence = st.EntryCount + 1
	e.PreviousDigest = st.EntryDigest
	e.Digest = captainEntryDigest(e)
	raw := encoded(e)
	if len(raw) > c.MaxRecordBytes || uint64(len(raw)) > c.MaxLedgerBytes-st.StoredBytes {
		return c.ErrCapacity
	}
	var reserveID, reportID domain.ID
	if e.Reservation != nil {
		reserveID = e.Reservation.Ref.ID
	}
	if e.Report != nil {
		reserveID = e.Report.Reservation.ID
		reportID = e.Report.ID
	}
	if e.ReservationRef != nil {
		reserveID = e.ReservationRef.ID
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO captain_entries VALUES(?,?,?,?,?,?,?)`, e.Assignment.ID, e.Sequence, e.ID, e.Kind, reserveID, reportID, raw); err != nil {
		return err
	}
	st.EntryCount++
	st.EntryDigest = e.Digest
	st.StoredBytes += uint64(len(raw))
	_, err := tx.ExecContext(ctx, `UPDATE captain_assignments SET data=? WHERE id=?`, encoded(st), e.Assignment.ID)
	return err
}

func createCaptainSchema(ctx context.Context, tx *sql.Tx) error {
	for _, d := range captainDefinitions {
		if _, err := tx.ExecContext(ctx, d.sql); err != nil {
			return c.ErrCorrupt
		}
	}
	return nil
}

func validateCaptainSchema(ctx context.Context, db *sql.DB) error {
	for _, d := range captainDefinitions {
		var kind, definition string
		if err := db.QueryRowContext(ctx, `SELECT type,sql FROM sqlite_master WHERE name=?`, d.name).Scan(&kind, &definition); err != nil || kind != d.kind || normalizedChannelSQL(definition) != normalizedChannelSQL(d.sql) {
			return c.ErrCorrupt
		}
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	a, err := loadCaptainAuthority(ctx, tx)
	if err != nil {
		return err
	}
	var orphan int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM captain_entries e LEFT JOIN captain_assignments a ON a.id=e.assignment_id WHERE a.id IS NULL`).Scan(&orphan); err != nil {
		return err
	}
	if orphan != 0 {
		return c.ErrCorrupt
	}
	for id := range a.assignments {
		var entries []captainEntry
		if _, entries, err = loadCaptainLedger(ctx, tx, a, id); err != nil {
			return err
		}
		if err = validateCaptainSources(ctx, tx, a, entries); err != nil {
			return err
		}
	}
	return nil
}
