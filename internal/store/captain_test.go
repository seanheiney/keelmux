package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	c "github.com/seanheiney/keelmux/internal/captain"
	"github.com/seanheiney/keelmux/internal/domain"
)

type captainFixture struct {
	s                            *Store
	path                         string
	owner, runner, reader, other c.AuthCaller
	sources                      []domain.Session
	assignment                   c.Assignment
	readerGrants                 []c.ReadGrant
}

func newCaptainFixture(t *testing.T, max uint32) *captainFixture {
	t.Helper()
	ctx := context.Background()
	x := &captainFixture{path: filepath.Join(t.TempDir(), "state", "registry.db")}
	var err error
	x.s, err = Open(x.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { x.s.Close() })
	var version int
	if err = x.s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 5 {
		t.Fatalf("actual Store.Open migration: version=%d error=%v", version, err)
	}
	ownerID := domain.NewID(c.PrincipalIDKind)
	if _, err = x.s.BootstrapCaptainOwner(ctx, ownerID); err != nil {
		t.Fatal(err)
	}
	x.owner = c.AuthCaller{PrincipalID: ownerID, Role: c.Owner, RoleRevision: 1}
	for _, out := range []*c.AuthCaller{&x.runner, &x.reader, &x.other} {
		role := c.Reader
		if out == &x.runner {
			role = c.Runner
		}
		p := c.Principal{Version: c.Version, Ref: c.Ref{ID: domain.NewID(c.PrincipalIDKind), Revision: 1}, Roles: []c.Role{role}, Active: true}
		if _, err = x.s.PutCaptainPrincipal(ctx, x.owner, c.Mutation{RequestID: domain.NewID(c.RequestIDKind)}, p); err != nil {
			t.Fatal(err)
		}
		*out = c.AuthCaller{PrincipalID: p.Ref.ID, Role: role, RoleRevision: 1}
	}
	host := domain.NewID("host")
	captainSession := domain.NewSession(host, "captain", t.TempDir(), "fixture", "fixture")
	for i := 0; i < 3; i++ {
		v := captainSession
		if i < 2 {
			v = domain.NewSession(host, "worker", t.TempDir(), "fixture", "fixture")
			x.sources = append(x.sources, v)
		}
		if _, _, err = x.s.Record(ctx, domain.Operation{ID: domain.NewID("op"), Kind: "launch", SessionID: v.ID, ExecutionID: v.Execution.ID}, []byte(`{"fixture":true}`), &v); err != nil {
			t.Fatal(err)
		}
	}
	refs := []c.Ref{}
	readerRefs := []c.Ref{}
	for _, source := range x.sources {
		for _, recipient := range []domain.ID{x.runner.PrincipalID, x.reader.PrincipalID} {
			g := c.ReadGrant{Version: c.Version, Ref: c.Ref{ID: domain.NewID(c.GrantIDKind), Revision: 1}, OwnerPrincipal: ownerID, RecipientPrincipal: recipient, Source: c.Source{Target: captainTestTarget(source), FirstSequence: 1}, Active: true}
			if _, err = x.s.PutCaptainGrant(ctx, x.owner, c.Mutation{RequestID: domain.NewID(c.RequestIDKind)}, g); err != nil {
				t.Fatal(err)
			}
			if recipient == x.runner.PrincipalID {
				refs = append(refs, g.Ref)
			} else {
				readerRefs = append(readerRefs, g.Ref)
				x.readerGrants = append(x.readerGrants, g)
			}
		}
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].ID < refs[j].ID })
	sort.Slice(readerRefs, func(i, j int) bool { return readerRefs[i].ID < readerRefs[j].ID })
	x.assignment = c.Assignment{Version: c.Version, Ref: c.Ref{ID: domain.NewID(c.AssignmentIDKind), Revision: 1}, OwnerPrincipal: ownerID, RunnerPrincipal: x.runner.PrincipalID, Captain: captainTestTarget(captainSession), Grants: refs, Readers: []c.ReaderScope{{PrincipalID: x.reader.PrincipalID, Grants: readerRefs}}, PolicyVersion: c.PolicyVersion, MaxReservations: max, Control: c.Active}
	x.runner.RunnerTarget = x.assignment.Captain
	if _, err = x.s.CreateCaptainAssignment(ctx, x.owner, c.Mutation{RequestID: domain.NewID(c.RequestIDKind)}, x.assignment); err != nil {
		t.Fatal(err)
	}
	return x
}

func captainTestTarget(v domain.Session) c.Target {
	return c.Target{HostID: v.HostID, SessionID: v.ID, ThreadID: v.ThreadID, ExecutionID: v.Execution.ID, Generation: v.Execution.Generation}
}
func (x *captainFixture) scan(t *testing.T) c.Scan {
	t.Helper()
	scan, err := x.s.ScanCaptainEvents(context.Background(), x.runner, x.assignment.Ref)
	if err != nil {
		t.Fatal(err)
	}
	return scan
}
func (x *captainFixture) meaningful(t *testing.T) {
	t.Helper()
	for _, v := range x.sources {
		if _, err := x.s.SetState(context.Background(), v.ID, v.Execution.ID, "running"); err != nil {
			t.Fatal(err)
		}
	}
}
func (x *captainFixture) reserve(t *testing.T) c.Reservation {
	t.Helper()
	r, err := x.s.ReserveCaptainWork(context.Background(), x.runner, domain.NewID(c.ReservationIDKind), x.scan(t))
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func (x *captainFixture) restart(t *testing.T) {
	t.Helper()
	if err := x.s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(x.path)
	if err != nil {
		t.Fatal(err)
	}
	x.s = s
	if err = validateCaptainSchema(context.Background(), s.db); err != nil {
		t.Fatal(err)
	}
}

func TestCaptainAtomicReportRestartAndExactRetries(t *testing.T) {
	x := newCaptainFixture(t, 2)
	ctx := context.Background()
	quiet := x.scan(t)
	st, err := x.s.CheckpointCaptainQuiet(ctx, x.runner, domain.NewID(c.RequestIDKind), quiet)
	if err != nil || st.ReservationsConsumed != 0 {
		t.Fatalf("quiet: %+v %v", st, err)
	}
	x.meaningful(t)
	scan := x.scan(t)
	rid := domain.NewID(c.ReservationIDKind)
	r, err := x.s.ReserveCaptainWork(ctx, x.runner, rid, scan)
	if err != nil {
		t.Fatal(err)
	}
	x.restart(t)
	retry, err := x.s.ReserveCaptainWork(ctx, x.runner, rid, scan)
	if err != nil || !captainSame(r, retry) {
		t.Fatalf("reserve retry: %v", err)
	}
	reportID := domain.NewID(c.ReportIDKind)
	report, err := x.s.CommitCaptainReport(ctx, x.runner, r.Ref, reportID)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Findings) != 2 || report.Usage.NativeInvocation != "not_invoked" || report.Usage.ActualProviderTurns != nil || report.Usage.ActualTokens != nil || report.Usage.ActualCharges != "unknown" {
		t.Fatalf("report: %+v", report)
	}
	x.restart(t)
	again, err := x.s.CommitCaptainReport(ctx, x.runner, r.Ref, reportID)
	if err != nil || !captainSame(report, again) {
		t.Fatalf("lost reply retry: %v", err)
	}
	read, err := x.s.ReadCaptainReport(ctx, x.reader, x.assignment.Ref, reportID)
	if err != nil || !captainSame(report, read) {
		t.Fatalf("reader: %v", err)
	}
	st, err = x.s.ReadCaptainStatus(ctx, x.runner, x.assignment.Ref)
	if err != nil || st.Pending != nil || st.ReservationsConsumed != 1 || st.EntryCount != 3 {
		t.Fatalf("status: %+v %v", st, err)
	}
	for _, p := range st.Cursors {
		if p.Through != 2 {
			t.Fatalf("cursor not atomically committed: %+v", p)
		}
	}
	if _, err = x.s.ReserveCaptainWork(ctx, x.runner, domain.NewID(c.ReservationIDKind), scan); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("consumed scan accepted: %v", err)
	}
}

func TestCaptainReportTransactionRollback(t *testing.T) {
	x := newCaptainFixture(t, 3)
	ctx := context.Background()
	x.meaningful(t)
	r := x.reserve(t)
	before, err := x.s.ReadCaptainStatus(ctx, x.runner, x.assignment.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = x.s.db.Exec(`CREATE TRIGGER captain_report_fault AFTER INSERT ON captain_entries WHEN NEW.kind='report' BEGIN SELECT RAISE(ABORT,'fixture publication failure'); END`); err != nil {
		t.Fatal(err)
	}
	id := domain.NewID(c.ReportIDKind)
	if _, err = x.s.CommitCaptainReport(ctx, x.runner, r.Ref, id); err == nil {
		t.Fatal("publication fault accepted")
	}
	after, err := x.s.ReadCaptainStatus(ctx, x.runner, x.assignment.Ref)
	if err != nil || !captainSame(before, after) {
		t.Fatalf("partial cursor/report mutation: %v", err)
	}
	if _, err = x.s.db.Exec(`DROP TRIGGER captain_report_fault`); err != nil {
		t.Fatal(err)
	}
	if _, err = x.s.CommitCaptainReport(ctx, x.runner, r.Ref, id); err != nil {
		t.Fatal(err)
	}
}

func TestCaptainDirectionalPrivacyAndRoleFences(t *testing.T) {
	x := newCaptainFixture(t, 3)
	ctx := context.Background()
	x.meaningful(t)
	r := x.reserve(t)
	id := domain.NewID(c.ReportIDKind)
	if _, err := x.s.CommitCaptainReport(ctx, x.runner, r.Ref, id); err != nil {
		t.Fatal(err)
	}
	for _, caller := range []c.AuthCaller{x.owner, x.other, {PrincipalID: x.reader.PrincipalID, Role: c.Reader, RoleRevision: 2}, {PrincipalID: x.runner.PrincipalID, Role: c.Reader, RoleRevision: 1}} {
		report, err := x.s.ReadCaptainReport(ctx, caller, x.assignment.Ref, id)
		if !errors.Is(err, c.ErrDenied) || !captainSame(report, c.Report{}) {
			t.Fatalf("scope leaked: %+v %v", caller, err)
		}
	}
	g := x.readerGrants[0]
	g.Ref.Revision++
	g.Active = false
	if _, err := x.s.PutCaptainGrant(ctx, x.owner, c.Mutation{RequestID: domain.NewID(c.RequestIDKind), ExpectedRevision: 1}, g); err != nil {
		t.Fatal(err)
	}
	if report, err := x.s.ReadCaptainReport(ctx, x.reader, x.assignment.Ref, id); !errors.Is(err, c.ErrDenied) || !captainSame(report, c.Report{}) {
		t.Fatalf("revoked report leaked: %v", err)
	}
	// Runner retains its distinct scopes, independently of the reader's grant.
	if _, err := x.s.ReadCaptainReport(ctx, x.runner, x.assignment.Ref, id); err != nil {
		t.Fatal(err)
	}
}

func TestCaptainReaderRevocationBeforePublishRetainsPending(t *testing.T) {
	x := newCaptainFixture(t, 2)
	ctx := context.Background()
	x.meaningful(t)
	r := x.reserve(t)
	g := x.readerGrants[0]
	g.Ref.Revision++
	g.Active = false
	if _, err := x.s.PutCaptainGrant(ctx, x.owner, c.Mutation{RequestID: domain.NewID(c.RequestIDKind), ExpectedRevision: 1}, g); err != nil {
		t.Fatal(err)
	}
	if _, err := x.s.CommitCaptainReport(ctx, x.runner, r.Ref, domain.NewID(c.ReportIDKind)); !errors.Is(err, c.ErrDenied) {
		t.Fatalf("publish after recipient revoke: %v", err)
	}
	st, err := x.s.ReadCaptainStatus(ctx, x.runner, x.assignment.Ref)
	if err != nil || st.Pending == nil || *st.Pending != r.Ref || st.ReservationsConsumed != 1 {
		t.Fatalf("pending lost: %v", err)
	}
	for _, p := range st.Cursors {
		if p.Through != 0 {
			t.Fatal("cursor advanced on denied publication")
		}
	}
}

func TestCaptainPauseCancelNeverRefundsBudget(t *testing.T) {
	x := newCaptainFixture(t, 1)
	ctx := context.Background()
	x.meaningful(t)
	r := x.reserve(t)
	st, err := x.s.SetCaptainControl(ctx, x.owner, c.Mutation{RequestID: domain.NewID(c.RequestIDKind), ExpectedRevision: 1}, x.assignment.Ref, c.Paused)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = x.s.CommitCaptainReport(ctx, x.runner, r.Ref, domain.NewID(c.ReportIDKind)); !errors.Is(err, c.ErrDenied) {
		t.Fatalf("paused publish: %v", err)
	}
	if st, err = x.s.CancelCaptainReservation(ctx, x.owner, domain.NewID(c.RequestIDKind), st.Assignment.Ref, r.Ref); err != nil || st.ReservationsConsumed != 1 || st.Pending != nil {
		t.Fatalf("cancel/refund: %+v %v", st, err)
	}
	st, err = x.s.SetCaptainControl(ctx, x.owner, c.Mutation{RequestID: domain.NewID(c.RequestIDKind), ExpectedRevision: 2}, st.Assignment.Ref, c.Active)
	if err != nil {
		t.Fatal(err)
	}
	x.assignment = st.Assignment
	if _, err = x.s.ReserveCaptainWork(ctx, x.runner, domain.NewID(c.ReservationIDKind), x.scan(t)); !errors.Is(err, c.ErrBudget) {
		t.Fatalf("refund or counter reset: %v", err)
	}
	x.restart(t)
}

func TestCaptainConcurrentReservationAndZeroCap(t *testing.T) {
	x := newCaptainFixture(t, 2)
	x.meaningful(t)
	scan := x.scan(t)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := x.s.ReserveCaptainWork(context.Background(), x.runner, domain.NewID(c.ReservationIDKind), scan)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, c.ErrConflict) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("accepted %d overlapping reservations", success)
	}
	y := newCaptainFixture(t, 0)
	y.meaningful(t)
	if _, err := y.s.ReserveCaptainWork(context.Background(), y.runner, domain.NewID(c.ReservationIDKind), y.scan(t)); !errors.Is(err, c.ErrBudget) {
		t.Fatalf("zero cap: %v", err)
	}
}

func TestCaptainQuietUnsupportedCoverageAndNoIdleWork(t *testing.T) {
	x := newCaptainFixture(t, 3)
	ctx := context.Background()
	scan := x.scan(t)
	if _, err := x.s.ReserveCaptainWork(ctx, x.runner, domain.NewID(c.ReservationIDKind), scan); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("quiet reserved: %v", err)
	}
	for _, v := range x.sources {
		if _, err := x.s.SetState(ctx, v.ID, v.Execution.ID, "unknown-sensitive-kind"); err != nil {
			t.Fatal(err)
		}
	}
	scan = x.scan(t)
	for _, e := range scan.Events {
		if e.Disposition == c.Unsupported && e.Kind != "unsupported" {
			t.Fatal("unknown kind copied")
		}
	}
	st, err := x.s.CheckpointCaptainQuiet(ctx, x.runner, domain.NewID(c.RequestIDKind), scan)
	if err != nil || st.Coverage.UnsupportedEvents != 2 {
		t.Fatalf("coverage: %+v %v", st, err)
	}
	empty := x.scan(t)
	st2, err := x.s.CheckpointCaptainQuiet(ctx, x.runner, domain.NewID(c.RequestIDKind), empty)
	if err != nil || !captainSame(st, st2) {
		t.Fatalf("idle changed ledger: %v", err)
	}
	x.meaningful(t)
	r := x.reserve(t)
	if _, err = x.s.CommitCaptainReport(ctx, x.runner, r.Ref, domain.NewID(c.ReportIDKind)); err != nil {
		t.Fatal(err)
	}
	st, err = x.s.ReadCaptainStatus(ctx, x.runner, x.assignment.Ref)
	if err != nil || st.Coverage.UnsupportedEvents != 2 {
		t.Fatal("quiet coverage forgotten")
	}
}

func TestCaptainSourceDriftGapAndGeneration(t *testing.T) {
	for _, kind := range []string{"drift", "gap", "generation", "captain-generation"} {
		t.Run(kind, func(t *testing.T) {
			x := newCaptainFixture(t, 3)
			ctx := context.Background()
			x.meaningful(t)
			scan := x.scan(t)
			switch kind {
			case "drift":
				var raw []byte
				if err := x.s.db.QueryRow(`SELECT data FROM events WHERE thread_id=? AND sequence=2`, x.sources[0].ThreadID).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var e domain.Event
				if err := json.Unmarshal(raw, &e); err != nil {
					t.Fatal(err)
				}
				e.Detail = "changed after scan"
				if _, err := x.s.db.Exec(`UPDATE events SET data=? WHERE thread_id=? AND sequence=2`, encoded(e), e.ThreadID); err != nil {
					t.Fatal(err)
				}
			case "gap":
				if _, err := x.s.db.Exec(`DELETE FROM events WHERE thread_id=? AND sequence=1`, x.sources[0].ThreadID); err != nil {
					t.Fatal(err)
				}
			default:
				id := x.sources[0].ID
				if kind == "captain-generation" {
					id = x.assignment.Captain.SessionID
				}
				v, err := x.s.Session(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				v.Execution.Generation++
				v.Execution.ID = domain.NewID("execution")
				if _, err = x.s.db.Exec(`UPDATE sessions SET data=? WHERE id=?`, encoded(v), id); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := x.s.ReserveCaptainWork(ctx, x.runner, domain.NewID(c.ReservationIDKind), scan); err == nil {
				t.Fatal("stale or missing source accepted")
			}
		})
	}
}

func TestCaptainBoundedSourceBytesAndRedactedReport(t *testing.T) {
	x := newCaptainFixture(t, 2)
	ctx := context.Background()
	x.meaningful(t)
	secret := "private-account:/private/workspace/credential"
	for _, v := range x.sources {
		var raw []byte
		if err := x.s.db.QueryRow(`SELECT data FROM events WHERE thread_id=? AND sequence=2`, v.ThreadID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var e domain.Event
		if err := json.Unmarshal(raw, &e); err != nil {
			t.Fatal(err)
		}
		e.Detail = secret
		if _, err := x.s.db.Exec(`UPDATE events SET data=? WHERE thread_id=? AND sequence=2`, encoded(e), v.ThreadID); err != nil {
			t.Fatal(err)
		}
	}
	r := x.reserve(t)
	report, err := x.s.CommitCaptainReport(ctx, x.runner, r.Ref, domain.NewID(c.ReportIDKind))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded(report), []byte(secret)) || report.Scan.Coverage.CompleteHistory || report.Scan.Coverage.HistoricalOrigin != "unknown" {
		t.Fatal("raw detail/full-history attribution leaked")
	}
	y := newCaptainFixture(t, 2)
	for _, v := range y.sources {
		if _, err = y.s.SetRecovery(ctx, v.ID, v.Execution.ID, "running", strings.Repeat("x", 66000)); err != nil {
			t.Fatal(err)
		}
	}
	scan, err := y.s.ScanCaptainEvents(ctx, y.runner, y.assignment.Ref)
	if !errors.Is(err, c.ErrCapacity) || !captainSame(scan, c.Scan{}) {
		t.Fatalf("combined source byte bound leaked partial result: %v", err)
	}
}

func TestCaptainLedgerMissingTailInteriorAndCommandRefuse(t *testing.T) {
	for _, kind := range []string{"tail", "interior", "command", "oversized", "counter"} {
		t.Run(kind, func(t *testing.T) {
			x := newCaptainFixture(t, 3)
			x.meaningful(t)
			r := x.reserve(t)
			if _, err := x.s.CommitCaptainReport(context.Background(), x.runner, r.Ref, domain.NewID(c.ReportIDKind)); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "tail":
				_, _ = x.s.db.Exec(`DELETE FROM captain_entries WHERE assignment_id=? AND sequence=2`, x.assignment.Ref.ID)
			case "interior":
				_, _ = x.s.db.Exec(`DELETE FROM captain_entries WHERE assignment_id=? AND sequence=1`, x.assignment.Ref.ID)
			case "command":
				_, _ = x.s.db.Exec(`UPDATE captain_commands SET data='{}' WHERE object_id=?`, x.reader.PrincipalID)
			case "oversized":
				_, _ = x.s.db.Exec(`UPDATE captain_entries SET data=zeroblob(33554433) WHERE assignment_id=? AND sequence=1`, x.assignment.Ref.ID)
			case "counter":
				var raw []byte
				if err := x.s.db.QueryRow(`SELECT data FROM captain_assignments WHERE id=?`, x.assignment.Ref.ID).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var st c.Status
				if err := json.Unmarshal(raw, &st); err != nil {
					t.Fatal(err)
				}
				st.ReservationsConsumed = 0
				_, _ = x.s.db.Exec(`UPDATE captain_assignments SET data=? WHERE id=?`, encoded(st), x.assignment.Ref.ID)
			}
			if st, err := x.s.ReadCaptainStatus(context.Background(), x.runner, x.assignment.Ref); err == nil || !captainSame(st, c.Status{}) {
				t.Fatalf("corrupt state accepted/leaked: %v", err)
			}
			if err := validateCaptainSchema(context.Background(), x.s.db); err == nil {
				t.Fatal("open validator accepted corrupt ledger")
			}
		})
	}
}

func TestCaptainIndependentAudienceAndSelfSources(t *testing.T) {
	x := newCaptainFixture(t, 2)
	ctx := context.Background()
	for _, kind := range []string{"empty", "borrowed-runner", "self"} {
		v := x.assignment
		v.Ref.ID = domain.NewID(c.AssignmentIDKind)
		switch kind {
		case "empty":
			v.Grants = nil
		case "borrowed-runner":
			v.Readers = []c.ReaderScope{{PrincipalID: x.reader.PrincipalID, Grants: append([]c.Ref{}, v.Grants...)}}
		case "self":
			g := c.ReadGrant{Version: c.Version, Ref: c.Ref{ID: domain.NewID(c.GrantIDKind), Revision: 1}, OwnerPrincipal: x.owner.PrincipalID, RecipientPrincipal: x.runner.PrincipalID, Source: c.Source{Target: v.Captain, FirstSequence: 1}, Active: true}
			if _, err := x.s.PutCaptainGrant(ctx, x.owner, c.Mutation{RequestID: domain.NewID(c.RequestIDKind)}, g); err != nil {
				t.Fatal(err)
			}
			v.Grants = []c.Ref{g.Ref}
			v.Readers = nil
		}
		if st, err := x.s.CreateCaptainAssignment(ctx, x.owner, c.Mutation{RequestID: domain.NewID(c.RequestIDKind)}, v); err == nil || !captainSame(st, c.Status{}) {
			t.Fatalf("%s accepted or returned private state: %v", kind, err)
		}
	}
}

func TestCaptainConfigIdempotencyAndPermanentRevocation(t *testing.T) {
	x := newCaptainFixture(t, 2)
	ctx := context.Background()
	id := domain.NewID(c.RequestIDKind)
	p := c.Principal{Version: c.Version, Ref: c.Ref{ID: domain.NewID(c.PrincipalIDKind), Revision: 1}, Roles: []c.Role{c.Reader}, Active: true}
	m := c.Mutation{RequestID: id}
	first, err := x.s.PutCaptainPrincipal(ctx, x.owner, m, p)
	if err != nil {
		t.Fatal(err)
	}
	again, err := x.s.PutCaptainPrincipal(ctx, x.owner, m, p)
	if err != nil || !captainSame(first, again) {
		t.Fatalf("config retry: %v", err)
	}
	changed := p
	changed.Roles = []c.Role{c.Owner}
	if _, err = x.s.PutCaptainPrincipal(ctx, x.owner, m, changed); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("conflicting command ID: %v", err)
	}
	if _, err = x.s.PutCaptainPrincipal(ctx, x.reader, c.Mutation{RequestID: domain.NewID(c.RequestIDKind)}, changed); !errors.Is(err, c.ErrDenied) {
		t.Fatalf("caller role elevated: %v", err)
	}
	g := x.readerGrants[0]
	g.Ref.Revision = 2
	g.Active = false
	gm := c.Mutation{RequestID: domain.NewID(c.RequestIDKind), ExpectedRevision: 1}
	if _, err = x.s.PutCaptainGrant(ctx, x.owner, gm, g); err != nil {
		t.Fatal(err)
	}
	if _, err = x.s.PutCaptainGrant(ctx, x.owner, gm, g); err != nil {
		t.Fatal(err)
	}
	g.Ref.Revision = 3
	g.Active = true
	if _, err = x.s.PutCaptainGrant(ctx, x.owner, c.Mutation{RequestID: domain.NewID(c.RequestIDKind), ExpectedRevision: 2}, g); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("revoked grant rearmed: %v", err)
	}
}

func TestCaptainPauseDeniesSourceReadBeforeDriftAndCancelStillWorks(t *testing.T) {
	x := newCaptainFixture(t, 2)
	ctx := context.Background()
	x.meaningful(t)
	r := x.reserve(t)
	st, err := x.s.SetCaptainControl(ctx, x.owner, c.Mutation{RequestID: domain.NewID(c.RequestIDKind), ExpectedRevision: 1}, x.assignment.Ref, c.Paused)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = x.s.db.Exec(`UPDATE events SET data='{}' WHERE thread_id=? AND sequence=2`, x.sources[0].ThreadID); err != nil {
		t.Fatal(err)
	}
	// ErrDenied, rather than source corruption, proves revoked work does not
	// inspect that source row before its current pause fence.
	if out, err := x.s.ReadCaptainStatus(ctx, x.runner, st.Assignment.Ref); !errors.Is(err, c.ErrDenied) || !captainSame(out, c.Status{}) {
		t.Fatalf("paused source read: %v", err)
	}
	if out, err := x.s.ScanCaptainEvents(ctx, x.runner, st.Assignment.Ref); !errors.Is(err, c.ErrDenied) || !captainSame(out, c.Scan{}) {
		t.Fatalf("paused scan: %v", err)
	}
	st, err = x.s.CancelCaptainReservation(ctx, x.owner, domain.NewID(c.RequestIDKind), st.Assignment.Ref, r.Ref)
	if err != nil || st.Pending != nil || st.ReservationsConsumed != 1 {
		t.Fatalf("revoked cancellation read source/refunded: %v", err)
	}
	if err = validateCaptainSchema(ctx, x.s.db); err == nil {
		t.Fatal("private integrity validator missed source drift")
	}
}

func TestCaptainCombinedRowBoundAndScopedMore(t *testing.T) {
	x := newCaptainFixture(t, 2)
	ctx := context.Background()
	tx, err := x.s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < c.MaxScanRows+1; i++ {
		if err = event(ctx, tx, x.sources[0], "", "link.on", "fixture quiet observation"); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	scan := x.scan(t)
	if len(scan.Events) != c.MaxScanRows || !scan.More {
		t.Fatalf("combined bound: %d more=%v", len(scan.Events), scan.More)
	}
	st, err := x.s.CheckpointCaptainQuiet(ctx, x.runner, domain.NewID(c.RequestIDKind), scan)
	if err != nil {
		t.Fatal(err)
	}
	next := x.scan(t)
	if len(next.Events) != 3 || next.More {
		t.Fatalf("tail continuation: %d %v", len(next.Events), next.More)
	}
	if st.ReservationsConsumed != 0 {
		t.Fatal("quiet bounded continuation spent a slot")
	}
}

func TestCaptainSelectedEntryRawByteBoundPrecedesDecode(t *testing.T) {
	x := newCaptainFixture(t, 2)
	ctx := context.Background()
	tx, err := x.s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// The shared bounded row reader is exercised through the actual SQL schema
	// and a deliberately tiny total. Each row is individually below record max;
	// the second must be refused before visit/decode can retain it.
	for i := 1; i <= 2; i++ {
		if _, err = tx.ExecContext(ctx, `INSERT INTO captain_entries VALUES(?,?,?,?,?,?,?)`, x.assignment.Ref.ID, i, domain.NewID(c.RequestIDKind), "quiet", "", "", bytes.Repeat([]byte(" "), 600)); err != nil {
			t.Fatal(err)
		}
	}
	visited := 0
	err = captainRows(ctx, tx, `SELECT sequence,length(data),CASE WHEN length(data)<=524288 THEN data END FROM captain_entries WHERE assignment_id=? ORDER BY sequence`, c.MaxEntries, 1000, func(_ *sql.Rows, _ []byte) error { visited++; return nil }, x.assignment.Ref.ID)
	if !errors.Is(err, c.ErrCapacity) || visited != 1 {
		t.Fatalf("retained beyond cumulative bound: visits=%d error=%v", visited, err)
	}
}

func TestCaptainCanonicalAndUnknownOperationProjection(t *testing.T) {
	x := newCaptainFixture(t, 2)
	ctx := context.Background()
	x.meaningful(t)
	ids := []domain.ID{domain.NewID("operation"), "legacy-private-path:/secret/account"}
	for i, v := range x.sources {
		var raw []byte
		if err := x.s.db.QueryRow(`SELECT data FROM events WHERE thread_id=? AND sequence=2`, v.ThreadID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var e domain.Event
		if err := json.Unmarshal(raw, &e); err != nil {
			t.Fatal(err)
		}
		e.OperationID = ids[i]
		if _, err := x.s.db.Exec(`UPDATE events SET data=? WHERE thread_id=? AND sequence=2`, encoded(e), v.ThreadID); err != nil {
			t.Fatal(err)
		}
	}
	scan := x.scan(t)
	foundCanonical := false
	for _, e := range scan.Events {
		if e.Key.ThreadID == x.sources[0].ThreadID && e.Key.Sequence == 2 {
			foundCanonical = e.OperationID == ids[0]
		}
		if e.Key.ThreadID == x.sources[1].ThreadID && e.Key.Sequence == 2 && e.OperationID != "" {
			t.Fatal("unknown legacy label leaked")
		}
	}
	if !foundCanonical || bytes.Contains(encoded(scan), []byte(ids[1])) {
		t.Fatal("canonical operation lost or arbitrary label exposed")
	}
	if _, err := x.s.ReserveCaptainWork(ctx, x.runner, domain.NewID(c.ReservationIDKind), scan); err != nil {
		t.Fatal(err)
	}
}
