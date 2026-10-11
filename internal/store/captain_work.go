package store

import (
	"context"
	"database/sql"
	"time"

	c "github.com/seanheiney/keelmux/internal/captain"
	"github.com/seanheiney/keelmux/internal/domain"
)

var _ c.Repository = (*Store)(nil)

func captainBegin(ctx context.Context, s *Store) (*sql.Tx, *captainAuthority, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	a, err := loadCaptainAuthority(ctx, tx)
	if err != nil {
		tx.Rollback()
		return nil, nil, err
	}
	return tx, a, nil
}

func currentCaptainScan(ctx context.Context, tx *sql.Tx, a *captainAuthority, caller c.AuthCaller, scan c.Scan) (c.Status, []captainEntry, error) {
	if c.ValidateScan(scan) != nil {
		return c.Status{}, nil, c.ErrConflict
	}
	st, entries, err := loadCaptainLedger(ctx, tx, a, scan.Assignment.ID)
	if err != nil {
		return st, nil, err
	}
	if st.Assignment.Ref != scan.Assignment {
		return st, nil, c.ErrConflict
	}
	grants, err := captainRunner(ctx, tx, a, caller, st.Assignment)
	if err != nil {
		return st, nil, err
	}
	if err = validateCaptainSources(ctx, tx, a, entries); err != nil {
		return st, nil, err
	}
	if !captainSame(st.Cursors, scan.Before) {
		return st, entries, c.ErrConflict
	}
	actual, err := scanCaptainPrefix(ctx, tx, scan.Assignment, grants, st.Cursors, &scan)
	if err != nil {
		return st, nil, err
	}
	if !captainSame(actual, scan) {
		return st, nil, c.ErrConflict
	}
	return st, entries, nil
}

func (s *Store) CheckpointCaptainQuiet(ctx context.Context, caller c.AuthCaller, id domain.ID, scan c.Scan) (c.Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !c.ValidID(id, c.RequestIDKind) || c.ValidateScan(scan) != nil {
		return c.Status{}, c.ErrConflict
	}
	tx, a, err := captainBegin(ctx, s)
	if err != nil {
		return c.Status{}, err
	}
	defer tx.Rollback()
	st, entries, err := loadCaptainLedger(ctx, tx, a, scan.Assignment.ID)
	if err != nil {
		return c.Status{}, err
	}
	if st.Assignment.Ref != scan.Assignment {
		return c.Status{}, c.ErrConflict
	}
	if _, err = captainRunner(ctx, tx, a, caller, st.Assignment); err != nil {
		return c.Status{}, err
	}
	if err = validateCaptainSources(ctx, tx, a, entries); err != nil {
		return c.Status{}, err
	}
	for _, e := range entries {
		if e.ID == id {
			if e.Kind != "quiet" || e.Actor != caller.PrincipalID || !captainSame(e.Scan, &scan) {
				return c.Status{}, c.ErrConflict
			}
			return st, nil
		}
	}
	st, _, err = currentCaptainScan(ctx, tx, a, caller, scan)
	if err != nil {
		return c.Status{}, err
	}
	if st.Pending != nil || len(c.Findings(scan)) > 0 {
		return c.Status{}, c.ErrConflict
	}
	if len(scan.Events) == 0 {
		return st, nil
	}
	if err = captainAdvance(&st, scan); err != nil {
		return c.Status{}, err
	}
	e := captainEntry{ID: id, Assignment: scan.Assignment, Kind: "quiet", Actor: caller.PrincipalID, ActorRevision: caller.RoleRevision, Scan: &scan}
	if err = appendCaptainEntry(ctx, tx, a, &st, e); err != nil {
		return c.Status{}, err
	}
	if err = tx.Commit(); err != nil {
		return c.Status{}, err
	}
	return st, nil
}

func (s *Store) ReserveCaptainWork(ctx context.Context, caller c.AuthCaller, id domain.ID, scan c.Scan) (c.Reservation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !c.ValidID(id, c.ReservationIDKind) || c.ValidateScan(scan) != nil {
		return c.Reservation{}, c.ErrConflict
	}
	tx, a, err := captainBegin(ctx, s)
	if err != nil {
		return c.Reservation{}, err
	}
	defer tx.Rollback()
	st, entries, err := loadCaptainLedger(ctx, tx, a, scan.Assignment.ID)
	if err != nil {
		return c.Reservation{}, err
	}
	if st.Assignment.Ref != scan.Assignment {
		return c.Reservation{}, c.ErrConflict
	}
	if _, err = captainRunner(ctx, tx, a, caller, st.Assignment); err != nil {
		return c.Reservation{}, err
	}
	if err = validateCaptainSources(ctx, tx, a, entries); err != nil {
		return c.Reservation{}, err
	}
	for _, e := range entries {
		if e.ID == id {
			if e.Kind != "reserve" || e.Actor != caller.PrincipalID || !captainSame(e.Reservation.Scan, scan) {
				return c.Reservation{}, c.ErrConflict
			}
			return *e.Reservation, nil
		}
	}
	st, _, err = currentCaptainScan(ctx, tx, a, caller, scan)
	if err != nil {
		return c.Reservation{}, err
	}
	if st.Pending != nil || len(c.Findings(scan)) == 0 {
		return c.Reservation{}, c.ErrConflict
	}
	if st.ReservationsConsumed >= st.Assignment.MaxReservations {
		return c.Reservation{}, c.ErrBudget
	}
	r := c.Reservation{Version: c.Version, Ref: c.ReservationRef{ID: id}, Assignment: scan.Assignment, Ordinal: st.ReservationsConsumed + 1, RunnerPrincipal: caller.PrincipalID, RoleRevision: caller.RoleRevision, Scan: scan, CreatedAt: time.Now().UTC()}
	r.Ref.Digest = c.ReservationDigest(r)
	st.ReservationsConsumed++
	copyRef := r.Ref
	st.Pending = &copyRef
	e := captainEntry{ID: id, Assignment: scan.Assignment, Kind: "reserve", Actor: caller.PrincipalID, ActorRevision: caller.RoleRevision, Reservation: &r}
	if err = appendCaptainEntry(ctx, tx, a, &st, e); err != nil {
		return c.Reservation{}, err
	}
	if err = tx.Commit(); err != nil {
		return c.Reservation{}, err
	}
	return r, nil
}

func (s *Store) CommitCaptainReport(ctx context.Context, caller c.AuthCaller, res c.ReservationRef, id domain.ID) (c.Report, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !c.ValidID(id, c.ReportIDKind) || !c.ValidID(res.ID, c.ReservationIDKind) || !c.ValidDigest(res.Digest) {
		return c.Report{}, c.ErrConflict
	}
	tx, a, err := captainBegin(ctx, s)
	if err != nil {
		return c.Report{}, err
	}
	defer tx.Rollback()
	// Exact immutable reserve ID locates only its assignment; no event scan or
	// source content is returned before current independent authorization.
	var assignmentID domain.ID
	if err = tx.QueryRowContext(ctx, `SELECT assignment_id FROM captain_entries WHERE reservation_id=? AND kind='reserve'`, res.ID).Scan(&assignmentID); err != nil {
		if err == sql.ErrNoRows {
			return c.Report{}, c.ErrNotFound
		}
		return c.Report{}, err
	}
	st, entries, err := loadCaptainLedger(ctx, tx, a, assignmentID)
	if err != nil {
		return c.Report{}, err
	}
	inputs, err := captainRunner(ctx, tx, a, caller, st.Assignment)
	if err != nil {
		return c.Report{}, err
	}
	if err = captainAudience(ctx, tx, a, st.Assignment, inputs, ""); err != nil {
		return c.Report{}, err
	}
	if err = validateCaptainSources(ctx, tx, a, entries); err != nil {
		return c.Report{}, err
	}
	var reservation *c.Reservation
	for _, e := range entries {
		if e.Kind == "reserve" && e.Reservation.Ref == res {
			reservation = e.Reservation
		}
		if e.Kind == "report" && (e.Report.ID == id || e.Report.Reservation == res) {
			if e.Report.ID != id || e.Report.Reservation != res || e.Actor != caller.PrincipalID || e.Assignment != st.Assignment.Ref {
				return c.Report{}, c.ErrConflict
			}
			return *e.Report, nil
		}
	}
	if reservation == nil || st.Pending == nil || *st.Pending != res || reservation.Assignment != st.Assignment.Ref || reservation.RoleRevision != caller.RoleRevision {
		return c.Report{}, c.ErrConflict
	}
	if _, _, err = currentCaptainScan(ctx, tx, a, caller, reservation.Scan); err != nil {
		return c.Report{}, err
	}
	r := c.Report{Version: c.Version, ID: id, Assignment: st.Assignment.Ref, Reservation: res, Scan: reservation.Scan, Findings: c.Findings(reservation.Scan), CommittedAt: time.Now().UTC(), Usage: c.Usage{ReservationsConsumed: st.ReservationsConsumed, NativeInvocation: "not_invoked", ActualCharges: "unknown"}}
	r.Digest = c.ReportDigest(r)
	if err = captainAdvance(&st, r.Scan); err != nil {
		return c.Report{}, err
	}
	st.Pending = nil
	e := captainEntry{ID: id, Assignment: st.Assignment.Ref, Kind: "report", Actor: caller.PrincipalID, ActorRevision: caller.RoleRevision, Report: &r}
	if err = appendCaptainEntry(ctx, tx, a, &st, e); err != nil {
		return c.Report{}, err
	}
	if err = tx.Commit(); err != nil {
		return c.Report{}, err
	}
	return r, nil
}

// Cancellation is metadata-only owner control: it can retire stale/paused work
// without reading a newly revoked source or returning any budget slot.
func (s *Store) CancelCaptainReservation(ctx context.Context, caller c.AuthCaller, id domain.ID, ref c.Ref, res c.ReservationRef) (c.Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !c.ValidID(id, c.RequestIDKind) || !c.ValidID(res.ID, c.ReservationIDKind) || !c.ValidDigest(res.Digest) {
		return c.Status{}, c.ErrConflict
	}
	tx, a, err := captainBegin(ctx, s)
	if err != nil {
		return c.Status{}, err
	}
	defer tx.Rollback()
	if err = captainAuthenticate(a, caller, c.Owner); err != nil {
		return c.Status{}, err
	}
	st, entries, err := loadCaptainLedger(ctx, tx, a, ref.ID)
	if err != nil {
		return c.Status{}, err
	}
	if st.Assignment.OwnerPrincipal != caller.PrincipalID || st.Assignment.Ref != ref {
		return c.Status{}, c.ErrDenied
	}
	for _, e := range entries {
		if e.ID == id {
			if e.Kind != "cancel" || e.Actor != caller.PrincipalID || *e.ReservationRef != res || e.Assignment != ref {
				return c.Status{}, c.ErrConflict
			}
			return st, nil
		}
	}
	if st.Pending == nil || *st.Pending != res {
		return c.Status{}, c.ErrConflict
	}
	st.Pending = nil
	e := captainEntry{ID: id, Assignment: ref, Kind: "cancel", Actor: caller.PrincipalID, ActorRevision: caller.RoleRevision, ReservationRef: &res}
	if err = appendCaptainEntry(ctx, tx, a, &st, e); err != nil {
		return c.Status{}, err
	}
	if err = tx.Commit(); err != nil {
		return c.Status{}, err
	}
	return st, nil
}

func captainRead(ctx context.Context, tx *sql.Tx, a *captainAuthority, caller c.AuthCaller, v c.Assignment) error {
	if v.Control != c.Active {
		return c.ErrDenied
	}
	if caller.Role == c.Runner {
		_, err := captainRunner(ctx, tx, a, caller, v)
		return err
	}
	if err := captainAuthenticate(a, caller, c.Reader); err != nil {
		return err
	}
	inputs, err := captainScopes(ctx, tx, a, v)
	if err != nil {
		return err
	}
	return captainAudience(ctx, tx, a, v, inputs, caller.PrincipalID)
}

func (s *Store) ReadCaptainStatus(ctx context.Context, caller c.AuthCaller, ref c.Ref) (c.Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, a, err := captainBegin(ctx, s)
	if err != nil {
		return c.Status{}, err
	}
	defer tx.Rollback()
	st, entries, err := loadCaptainLedger(ctx, tx, a, ref.ID)
	if err != nil {
		return c.Status{}, err
	}
	if st.Assignment.Ref != ref {
		return c.Status{}, c.ErrConflict
	}
	if err = captainRead(ctx, tx, a, caller, st.Assignment); err != nil {
		return c.Status{}, err
	}
	if err = validateCaptainSources(ctx, tx, a, entries); err != nil {
		return c.Status{}, err
	}
	return st, nil
}

func (s *Store) ReadCaptainReport(ctx context.Context, caller c.AuthCaller, ref c.Ref, id domain.ID) (c.Report, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !c.ValidID(id, c.ReportIDKind) {
		return c.Report{}, c.ErrConflict
	}
	tx, a, err := captainBegin(ctx, s)
	if err != nil {
		return c.Report{}, err
	}
	defer tx.Rollback()
	st, entries, err := loadCaptainLedger(ctx, tx, a, ref.ID)
	if err != nil {
		return c.Report{}, err
	}
	if st.Assignment.Ref != ref {
		return c.Report{}, c.ErrConflict
	}
	if err = captainRead(ctx, tx, a, caller, st.Assignment); err != nil {
		return c.Report{}, err
	}
	if err = validateCaptainSources(ctx, tx, a, entries); err != nil {
		return c.Report{}, err
	}
	for _, e := range entries {
		if e.Report != nil && e.Report.ID == id {
			if e.Report.Assignment != ref {
				return c.Report{}, c.ErrDenied
			}
			return *e.Report, nil
		}
	}
	return c.Report{}, c.ErrNotFound
}
