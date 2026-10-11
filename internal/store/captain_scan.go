package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"

	c "github.com/seanheiney/keelmux/internal/captain"
	"github.com/seanheiney/keelmux/internal/domain"
)

func historicalCaptainGrant(a *captainAuthority, r c.Ref) (c.ReadGrant, error) {
	for _, cmd := range a.commands {
		if cmd.ObjectID == r.ID && cmd.Revision == r.Revision && cmd.Grant != nil {
			return *cmd.Grant, nil
		}
	}
	return c.ReadGrant{}, c.ErrCorrupt
}

func historicalCaptainAssignment(a *captainAuthority, r c.Ref) (c.Assignment, error) {
	for _, cmd := range a.commands {
		if cmd.ObjectID == r.ID && cmd.Revision == r.Revision && cmd.Assignment != nil {
			return *cmd.Assignment, nil
		}
	}
	return c.Assignment{}, c.ErrCorrupt
}

func captainSourceRow(ctx context.Context, q captainQuery, g c.ReadGrant, sequence int64) (c.Event, int, error) {
	var n int64
	var raw []byte
	err := q.QueryRowContext(ctx, `SELECT length(data),CASE WHEN length(data)<=131072 THEN data END FROM events WHERE thread_id=? AND sequence=?`, g.Source.Target.ThreadID, sequence).Scan(&n, &raw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return c.Event{}, 0, c.ErrCorrupt
		}
		return c.Event{}, 0, err
	}
	if n <= 0 || n > c.MaxScanBytes || int64(len(raw)) != n {
		return c.Event{}, 0, c.ErrCapacity
	}
	v, err := captainDecode[domain.Event](raw)
	if err != nil {
		return c.Event{}, 0, err
	}
	if v.Sequence != sequence || v.ThreadID != g.Source.Target.ThreadID || v.SessionID != g.Source.Target.SessionID || v.At.IsZero() || v.At.Location() != time.UTC || !c.SafeKind(v.Kind) {
		return c.Event{}, 0, c.ErrCorrupt
	}
	d, category := c.Classify(v.Kind)
	kind := v.Kind
	if d == c.Unsupported {
		kind = "unsupported"
	}
	operationID := v.OperationID
	if !c.ValidOperationID(operationID) {
		operationID = ""
	}
	e := c.Event{Key: c.EventKey{HostID: g.Source.Target.HostID, ThreadID: v.ThreadID, Sequence: sequence}, SessionID: v.SessionID, OperationID: operationID, RowDigest: c.Hash("captain.source.row.v1", raw), Kind: kind, Category: category, Disposition: d, At: v.At}
	return e, int(n), nil
}

func captainAnchor(ctx context.Context, q captainQuery, g c.ReadGrant, p c.Position) error {
	if p.Grant != g.Ref || p.Through < g.Source.FirstSequence-1 {
		return c.ErrCorrupt
	}
	if p.Through == g.Source.FirstSequence-1 {
		if p.AnchorDigest != c.Genesis(g) {
			return c.ErrCorrupt
		}
		return nil
	}
	e, _, err := captainSourceRow(ctx, q, g, p.Through)
	if err != nil {
		return err
	}
	if e.RowDigest != p.AnchorDigest {
		return c.ErrCorrupt
	}
	return nil
}

// scanCaptainPrefix queries only exact granted threads. Fixed limits bound the
// combined prefix; no unbounded Store.Events call or text projection occurs.
// fixed!=nil revalidates a previously selected prefix, ignoring later tails.
func scanCaptainPrefix(ctx context.Context, q captainQuery, ref c.Ref, grants []c.ReadGrant, before []c.Position, fixed *c.Scan) (c.Scan, error) {
	s := c.Scan{Version: c.Version, Assignment: ref, Before: append([]c.Position{}, before...), Through: append([]c.Position{}, before...), Events: []c.Event{}, Coverage: c.EmptyCoverage()}
	if len(grants) != len(before) {
		return s, c.ErrCorrupt
	}
	if fixed != nil {
		s.More = fixed.More
		if len(fixed.Through) != len(grants) {
			return s, c.ErrConflict
		}
	}
	for i, g := range grants {
		if err := captainAnchor(ctx, q, g, before[i]); err != nil {
			return s, err
		}
		end := int64(math.MaxInt64)
		if fixed != nil {
			end = fixed.Through[i].Through
			if fixed.Through[i].Grant != g.Ref || end < before[i].Through {
				return s, c.ErrConflict
			}
		}
		remaining := c.MaxScanRows - len(s.Events)
		rows, err := q.QueryContext(ctx, `SELECT sequence,length(data) FROM events WHERE thread_id=? AND sequence>? AND sequence<=? ORDER BY sequence LIMIT ?`, g.Source.Target.ThreadID, before[i].Through, end, remaining+1)
		if err != nil {
			return s, err
		}
		type sourceLength struct{ seq, n int64 }
		selected := []sourceLength{}
		for rows.Next() {
			var r sourceLength
			if err = rows.Scan(&r.seq, &r.n); err != nil {
				break
			}
			if len(selected) >= remaining {
				if fixed != nil {
					err = c.ErrConflict
				} else {
					s.More = true
				}
				break
			}
			if r.n <= 0 || r.n > c.MaxScanBytes || uint64(r.n) > uint64(c.MaxScanBytes)-uint64(s.SourceBytes) {
				err = c.ErrCapacity
				break
			}
			s.SourceBytes += uint32(r.n)
			selected = append(selected, r)
		}
		if err = closeChannelRows(rows, err); err != nil {
			return s, err
		}
		expected := before[i].Through
		for _, r := range selected {
			if expected == math.MaxInt64 || r.seq != expected+1 {
				return s, c.ErrCorrupt
			}
			e, n, err := captainSourceRow(ctx, q, g, r.seq)
			if err != nil {
				return s, err
			}
			if int64(n) != r.n {
				return s, c.ErrCorrupt
			}
			s.Events = append(s.Events, e)
			if e.Disposition == c.Unsupported {
				s.Coverage.UnsupportedEvents++
			}
			expected = r.seq
			s.Through[i].Through = r.seq
			s.Through[i].AnchorDigest = e.RowDigest
		}
		if fixed != nil && expected != end {
			return s, c.ErrCorrupt
		}
	}
	s.Digest = c.ScanDigest(s)
	if c.ValidateScan(s) != nil {
		return s, c.ErrCorrupt
	}
	return s, nil
}

func validateStoredCaptainScan(ctx context.Context, q captainQuery, a *captainAuthority, scan c.Scan) error {
	if err := validateStoredCaptainScanMetadata(a, scan); err != nil {
		return err
	}
	v, err := historicalCaptainAssignment(a, scan.Assignment)
	if err != nil || v.Control != c.Active {
		return c.ErrCorrupt
	}
	grants := []c.ReadGrant{}
	for _, ref := range v.Grants {
		g, e := historicalCaptainGrant(a, ref)
		if e != nil || !g.Active || g.RecipientPrincipal != v.RunnerPrincipal || g.Source.Target.HostID != v.Captain.HostID || g.Source.Target.SessionID == v.Captain.SessionID || g.Source.Target.ThreadID == v.Captain.ThreadID {
			return c.ErrCorrupt
		}
		grants = append(grants, g)
	}
	actual, err := scanCaptainPrefix(ctx, q, scan.Assignment, grants, scan.Before, &scan)
	if err != nil {
		return err
	}
	if !captainSame(actual, scan) {
		return c.ErrCorrupt
	}
	return nil
}

// Metadata replay does not read a source: owner cancellation/control must remain
// possible after revocation. Source-byte revalidation follows fresh scoped
// authorization for every read/publication, or private Open integrity checking.
func validateStoredCaptainScanMetadata(a *captainAuthority, scan c.Scan) error {
	if c.ValidateScan(scan) != nil {
		return c.ErrCorrupt
	}
	v, err := historicalCaptainAssignment(a, scan.Assignment)
	if err != nil || v.Control != c.Active || len(v.Grants) != len(scan.Before) {
		return c.ErrCorrupt
	}
	seen := map[c.EventKey]bool{}
	for i, ref := range v.Grants {
		g, err := historicalCaptainGrant(a, ref)
		if err != nil || !g.Active || g.RecipientPrincipal != v.RunnerPrincipal || scan.Before[i].Grant != ref || scan.Before[i].Through < g.Source.FirstSequence-1 || g.Source.Target.HostID != v.Captain.HostID || g.Source.Target.SessionID == v.Captain.SessionID || g.Source.Target.ThreadID == v.Captain.ThreadID {
			return c.ErrCorrupt
		}
		pos := scan.Before[i]
		for _, e := range scan.Events {
			if e.Key.ThreadID != g.Source.Target.ThreadID {
				continue
			}
			if e.Key.HostID != g.Source.Target.HostID || e.SessionID != g.Source.Target.SessionID || pos.Through == math.MaxInt64 || e.Key.Sequence != pos.Through+1 || seen[e.Key] {
				return c.ErrCorrupt
			}
			pos.Through = e.Key.Sequence
			pos.AnchorDigest = e.RowDigest
			seen[e.Key] = true
		}
		if pos != scan.Through[i] {
			return c.ErrCorrupt
		}
	}
	if len(seen) != len(scan.Events) {
		return c.ErrCorrupt
	}
	return nil
}

func validateCaptainSources(ctx context.Context, q captainQuery, a *captainAuthority, entries []captainEntry) error {
	for _, e := range entries {
		scan := e.Scan
		if e.Reservation != nil {
			scan = &e.Reservation.Scan
		}
		if e.Report != nil {
			scan = &e.Report.Scan
		}
		if scan != nil {
			if err := validateStoredCaptainScan(ctx, q, a, *scan); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) ScanCaptainEvents(ctx context.Context, caller c.AuthCaller, ref c.Ref) (c.Scan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return c.Scan{}, err
	}
	defer tx.Rollback()
	a, err := loadCaptainAuthority(ctx, tx)
	if err != nil {
		return c.Scan{}, err
	}
	st, entries, err := loadCaptainLedger(ctx, tx, a, ref.ID)
	if err != nil {
		return c.Scan{}, err
	}
	if st.Assignment.Ref != ref {
		return c.Scan{}, c.ErrConflict
	}
	grants, err := captainRunner(ctx, tx, a, caller, st.Assignment)
	if err != nil {
		return c.Scan{}, err
	}
	if st.Pending != nil {
		return c.Scan{}, c.ErrConflict
	}
	if err = validateCaptainSources(ctx, tx, a, entries); err != nil {
		return c.Scan{}, err
	}
	scan, err := scanCaptainPrefix(ctx, tx, ref, grants, st.Cursors, nil)
	if err != nil {
		return c.Scan{}, err
	}
	return scan, nil
}
