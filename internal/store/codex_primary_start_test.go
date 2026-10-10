package store

import (
	"bytes"
	"context"
	"path/filepath"
	"sync"
	"testing"

	cb "github.com/seanheiney/keelmux/internal/codexbroker"
	cc "github.com/seanheiney/keelmux/internal/codexcontroller"
	"github.com/seanheiney/keelmux/internal/domain"
)

func primaryStartStore(t *testing.T) (*Store, domain.Session, cc.LifecycleRequest, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state", "registry.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	v := domain.NewSession("host", "Primary", t.TempDir(), "codex", cc.ProtocolVersion)
	v.Execution.State = "primary_reserved"
	r := cc.StartRequest{OperationID: "reserve", Target: cc.Target{HostID: v.HostID, SessionID: v.ID, ExecutionID: v.Execution.ID, Generation: v.Execution.Generation}, NativeThreadID: "native", Workspace: v.Workspace, RealmRevision: "realm"}
	if err = s.ReserveCodexPrimary(context.Background(), v, PrimaryReservation{Name: v.Name, Request: r}); err != nil {
		t.Fatal(err)
	}
	req := cc.LifecycleRequest{Action: cc.StartPrimary, OperationID: r.OperationID, Target: r.Target, NativeThreadID: r.NativeThreadID, Workspace: r.Workspace, RealmRevision: r.RealmRevision, StartIncarnation: "0123456789abcdef0123456789abcdef"}
	return s, v, req, path
}

func TestPrimaryStartClaimIsImmutableAcrossRetryAndStoreRestart(t *testing.T) {
	s, v, req, path := primaryStartStore(t)
	ctx := context.Background()
	claimed, err := s.ClaimCodexPrimaryStart(ctx, req)
	if err != nil || !claimed {
		t.Fatal("first claim was not committed", claimed, err)
	}
	r, err := s.CodexPrimaryReservation(ctx, v.ID, v.Execution.ID)
	if err != nil || r.Start == nil || r.Start.Incarnation != req.StartIncarnation || r.Start.AttemptedAt.IsZero() {
		t.Fatal("missing exact immutable claim", err)
	}
	claim := *r.Start
	op, raw, err := s.Operation(ctx, req.OperationID)
	if err != nil || op.Outcome != domain.Attempted {
		t.Fatal("operation does not record attempt", err)
	}
	events, err := s.Events(ctx, v.ThreadID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	claimed, err = s.ClaimCodexPrimaryStart(ctx, req)
	if err != nil || claimed {
		t.Fatal("matching claim authorized another effect", claimed, err)
	}
	replacement := req
	replacement.StartIncarnation = "abcdef0123456789abcdef0123456789"
	if claimed, err = s.ClaimCodexPrimaryStart(ctx, replacement); err == nil || claimed {
		t.Fatal("replacement incarnation took durable attempt", claimed, err)
	}
	after, afterRaw, err := s.Operation(ctx, req.OperationID)
	if err != nil || after != op || !bytes.Equal(raw, afterRaw) {
		t.Fatal("retry mutated receipt", err)
	}
	afterR, err := s.CodexPrimaryReservation(ctx, v.ID, v.Execution.ID)
	if err != nil || afterR.Start == nil || *afterR.Start != claim {
		t.Fatal("retry changed incarnation or timestamp", err)
	}
	afterEvents, err := s.Events(ctx, v.ThreadID, 0)
	if err != nil || len(afterEvents) != len(events) {
		t.Fatal("retry emitted repeated admission events", err)
	}
}

func TestPrimaryStartClaimRefusesForgedSelectionWithoutMutation(t *testing.T) {
	for _, mutate := range []func(*cc.LifecycleRequest){
		func(r *cc.LifecycleRequest) { r.StartIncarnation = "invalid" },
		func(r *cc.LifecycleRequest) { r.Target.Generation++ },
		func(r *cc.LifecycleRequest) { r.Target.HostID = "foreign" },
		func(r *cc.LifecycleRequest) { r.OperationID = "other" },
		func(r *cc.LifecycleRequest) { r.NativeThreadID = "foreign" },
		func(r *cc.LifecycleRequest) { r.Workspace = "/foreign" },
		func(r *cc.LifecycleRequest) { r.RealmRevision = "foreign" },
		func(r *cc.LifecycleRequest) { r.Action = cc.StopPrimary },
	} {
		s, v, req, _ := primaryStartStore(t)
		ctx := context.Background()
		op, raw, err := s.Operation(ctx, req.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		changed := req
		mutate(&changed)
		if claimed, err := s.ClaimCodexPrimaryStart(ctx, changed); err == nil || claimed {
			t.Fatal("changed selection was admitted", changed, err)
		}
		after, afterRaw, err := s.Operation(ctx, req.OperationID)
		if err != nil || after != op || !bytes.Equal(raw, afterRaw) {
			t.Fatal("refused selection changed original operation", err)
		}
		r, err := s.CodexPrimaryReservation(ctx, v.ID, v.Execution.ID)
		if err != nil || r.Start != nil {
			t.Fatal("refused selection manufactured attempt", err)
		}
	}
}

func TestConcurrentPrimaryStartClaimsPermitOneEffect(t *testing.T) {
	s, _, req, _ := primaryStartStore(t)
	var wg sync.WaitGroup
	results := make(chan bool, 16)
	for i := 0; i < cap(results); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimed, err := s.ClaimCodexPrimaryStart(context.Background(), req)
			if err != nil {
				t.Error(err)
			}
			results <- claimed
		}()
	}
	wg.Wait()
	close(results)
	claims := 0
	for claimed := range results {
		if claimed {
			claims++
		}
	}
	if claims != 1 {
		t.Fatal("concurrent callbacks permitted", claims)
	}
}

func TestLegacyPublishedPrimaryCannotManufactureNewStartClaim(t *testing.T) {
	s, v, req, _ := primaryStartStore(t)
	if _, err := s.SetState(context.Background(), v.ID, v.Execution.ID, "primary_ready"); err != nil {
		t.Fatal(err)
	}
	if claimed, err := s.ClaimCodexPrimaryStart(context.Background(), req); err == nil || claimed {
		t.Fatal("legacy ready state admitted another launch", claimed, err)
	}
}

func TestPrimaryPublicationRequiresExactAttemptedIncarnation(t *testing.T) {
	s, v, req, _ := primaryStartStore(t)
	ctx := context.Background()
	a := cb.AgentBinding{HostID: v.HostID, SessionID: v.ID, ExecutionID: v.Execution.ID, Generation: v.Execution.Generation, AppServerIncarnation: req.StartIncarnation, AppServerPID: 1, AppServerBootID: "boot", AppServerProcessStart: "start", ProtocolVersion: cc.ProtocolVersion, NativeThreadID: req.NativeThreadID}
	if err := s.PublishCodexPrimary(ctx, a); err == nil {
		t.Fatal("publication bypassed missing common attempted claim")
	}
	if claimed, err := s.ClaimCodexPrimaryStart(ctx, req); err != nil || !claimed {
		t.Fatal(err)
	}
	wrong := a
	wrong.AppServerIncarnation = "abcdef0123456789abcdef0123456789"
	if err := s.PublishCodexPrimary(ctx, wrong); err == nil {
		t.Fatal("different incarnation published against original attempt")
	}
	if err := s.PublishCodexPrimary(ctx, a); err != nil {
		t.Fatal("exact trusted fixture binding did not publish", err)
	}
}
