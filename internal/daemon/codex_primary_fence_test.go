package daemon

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	cc "github.com/seanheiney/keelmux/internal/codexcontroller"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/store"
)

// All dependencies here are owned fixtures. These tests establish admission,
// receipt and restart behavior, not native process or provider qualification.
type startFenceSupervisor struct {
	*primarySupervisor
	beforeLaunch func()
}

func (s *startFenceSupervisor) LaunchProcess(ctx context.Context, spec domain.LaunchSpec) (domain.RuntimeRef, error) {
	s.beforeLaunch()
	return s.primarySupervisor.LaunchProcess(ctx, spec)
}

func TestPrimaryJournalLossCannotReplayReadyStoppedOrUncertainStart(t *testing.T) {
	for _, state := range []string{"ready", "stopped", "resume_uncertain", "before_journal"} {
		t.Run(state, func(t *testing.T) {
			x := primarySetup(t)
			ctx := context.Background()
			switch state {
			case "ready":
				x.start(t)
			case "stopped":
				out := x.start(t)
				if _, err := x.p.Stop(ctx, cc.StopRequest{OperationID: "stop", Target: x.r.Target, Incarnation: out.Primary.Incarnation}); err != nil {
					t.Fatal(err)
				}
			case "resume_uncertain":
				x.u.failResume = true
				if _, err := x.p.Start(ctx, x.r); !errors.Is(err, cc.ErrUncertain) {
					t.Fatal("missing resume receipt was not uncertain", err)
				}
			case "before_journal":
				if err := os.Remove(x.options.Binary); err != nil {
					t.Fatal(err)
				}
				if _, err := x.p.Start(ctx, x.r); err == nil {
					t.Fatal("missing executable did not fail")
				}
				if err := os.WriteFile(x.options.Binary, []byte("fixture executable never executed"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			reservation, err := x.e.store.CodexPrimaryReservation(ctx, x.r.Target.SessionID, x.r.Target.ExecutionID)
			if err != nil || reservation.Start == nil {
				t.Fatal("missing common start-attempt fence", err)
			}
			claim := *reservation.Start
			launches, resumes := x.s.launches, x.u.resumes
			if err = x.j.Close(); err != nil {
				t.Fatal(err)
			}
			// Simulate loss of the journal contents at the same owned pathname.
			if err = os.Remove(filepath.Join(x.dir, "journal", "primaries.json")); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			x.e.restart(t)
			j, err := cc.OpenJournal(filepath.Join(x.dir, "journal"))
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			p, err := x.e.service.NewCodexPrimary(j, x.s, x.u, x.options)
			if err != nil {
				t.Fatal(err)
			}
			out, err := p.Start(ctx, x.r)
			if err == nil || out.Capabilities.OwnedPrimaryVerified || x.s.launches != launches || x.u.resumes != resumes {
				t.Fatalf("journal loss replayed lifecycle: launches %d->%d resumes %d->%d status=%+v err=%v", launches, x.s.launches, resumes, x.u.resumes, out, err)
			}
			after, err := x.e.store.CodexPrimaryReservation(ctx, x.r.Target.SessionID, x.r.Target.ExecutionID)
			if err != nil || after.Start == nil || *after.Start != claim {
				t.Fatal("journal loss changed immutable common receipt", err)
			}
		})
	}
}

func TestPrimaryStartClaimPrecedesEffectsAndIdenticalClaimCannotRunAgain(t *testing.T) {
	x := primarySetup(t)
	ctx := context.Background()
	// Both launch and resume must observe a committed common-store attempt.
	check := func() {
		r, err := x.e.store.CodexPrimaryReservation(ctx, x.r.Target.SessionID, x.r.Target.ExecutionID)
		if err != nil || r.Start == nil || r.Start.Incarnation == "" {
			t.Error("native fixture preceded common start claim", err)
		}
	}
	p, err := x.e.service.NewCodexPrimary(x.j, &startFenceSupervisor{primarySupervisor: x.s, beforeLaunch: check}, x.u, x.options)
	if err != nil {
		t.Fatal(err)
	}
	x.p = p
	x.u.beforeInspect = check
	out := x.start(t)
	x.u.beforeInspect = nil
	r, err := x.e.store.CodexPrimaryReservation(ctx, x.r.Target.SessionID, x.r.Target.ExecutionID)
	if err != nil || r.Start == nil || r.Start.Incarnation != out.Primary.Incarnation {
		t.Fatal("attempt does not name exact verified incarnation", err)
	}
	op, raw, err := x.e.store.Operation(ctx, x.r.OperationID)
	if err != nil || op.Outcome != domain.Attempted {
		t.Fatal("common operation lost attempted receipt", err)
	}
	gate := &primaryGate{service: x.e.service, realm: x.options.RealmRevision}
	request := cc.LifecycleRequest{Action: cc.StartPrimary, OperationID: x.r.OperationID, Target: x.r.Target, NativeThreadID: x.r.NativeThreadID, RealmRevision: x.r.RealmRevision, Workspace: x.r.Workspace, StartIncarnation: r.Start.Incarnation}
	called := false
	bounded, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err = gate.WithPrimaryLifecycle(bounded, request, func(context.Context) error { called = true; return nil }); err == nil || called {
		t.Fatal("existing matching incarnation authorized another callback", err)
	}
	x.start(t) // Matching journal retry is a saved receipt, with fresh publication checks.
	after, afterRaw, err := x.e.store.Operation(ctx, x.r.OperationID)
	if err != nil || after != op || !bytes.Equal(raw, afterRaw) || x.s.launches != 1 || x.u.resumes != 1 {
		t.Fatal("matching retry changed claim or repeated effects", err)
	}
}

func TestPrimaryWrongIncarnationCannotPersistStopFence(t *testing.T) {
	x := primarySetup(t)
	x.start(t)
	ctx := context.Background()
	before, err := x.e.store.Session(ctx, x.r.Target.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	out, err := x.p.Stop(ctx, cc.StopRequest{OperationID: "wrong-stop", Target: x.r.Target, Incarnation: "wrong-incarnation"})
	if !errors.Is(err, cc.ErrConflict) || out.Capabilities.OwnedPrimaryVerified {
		t.Fatal("stale stop has verified capability", out, err)
	}
	after, err := x.e.store.Session(ctx, x.r.Target.SessionID)
	if err != nil || after.Execution.State != before.Execution.State || x.s.stops != 0 {
		t.Fatal("stale stop changed primary state or had an effect", err)
	}
	if _, _, err = x.e.store.Operation(ctx, "wrong-stop"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("stale stop persisted a durable fence", err)
	}
}

func TestPrimaryLifecycleErrorsNeverAdvertiseCachedReadyVerification(t *testing.T) {
	t.Run("stop_missing_process", func(t *testing.T) {
		x := primarySetup(t)
		out := x.start(t)
		x.s.alive = false
		failed, err := x.p.Stop(context.Background(), cc.StopRequest{OperationID: "stop", Target: x.r.Target, Incarnation: out.Primary.Incarnation})
		if err == nil || failed.Primary.State != cc.Ready || failed.Capabilities.OwnedPrimaryVerified {
			t.Fatal("failed fresh verification advertised cached ready capability", failed, err)
		}
	})
	t.Run("start_conflicting_options", func(t *testing.T) {
		x := primarySetup(t)
		x.start(t)
		changed := x.options
		changed.BinarySHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
		p, err := x.e.service.NewCodexPrimary(x.j, x.s, x.u, changed)
		if err != nil {
			t.Fatal(err)
		}
		failed, err := p.Start(context.Background(), x.r)
		if err == nil || failed.Primary.State != cc.Ready || failed.Capabilities.OwnedPrimaryVerified || x.s.launches != 1 || x.u.resumes != 1 {
			t.Fatal("conflicting cached start advertised verified capability", failed, err)
		}
	})
	t.Run("reconcile_conflicting_options", func(t *testing.T) {
		x := primarySetup(t)
		x.start(t)
		changed := x.options
		changed.BinarySHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
		p, err := x.e.service.NewCodexPrimary(x.j, x.s, x.u, changed)
		if err != nil {
			t.Fatal(err)
		}
		failed, err := p.Reconcile(context.Background(), x.r.Target)
		if err == nil || failed.Primary.State != cc.Ready || failed.Capabilities.OwnedPrimaryVerified || x.s.launches != 1 || x.u.resumes != 1 {
			t.Fatal("conflicting reconcile advertised cached verified capability", failed, err)
		}
	})
}
