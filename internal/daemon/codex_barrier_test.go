package daemon

import (
	"context"
	"encoding/json"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	cb "github.com/seanheiney/keelmux/internal/codexbroker"
)

func waitCodexStack(t *testing.T, name string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		stack := make([]byte, 1<<16)
		n := runtime.Stack(stack, true)
		if strings.Contains(string(stack[:n]), name) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("owned goroutine did not reach %s", name)
		}
		runtime.Gosched()
	}
}

// The independent review reproduced the gap by holding the existing Store
// mutex after the final probe. This test adds no production hook or authority:
// it fixes that exact boundary while registration waits behind the commit.
// Registration before the barrier is covered by the probe-order test.
func TestCodexCallbackBehindHandoverCommitFencesReplacement(t *testing.T) {
	x := actorSetup(t)
	g := x.acquire(t, "grant", cb.NativeWriter, false)
	c := x.bind(t, g, "native")
	h := x.handover(g, "next", cb.SemanticWriter)
	storeMutex := (*sync.Mutex)(unsafe.Pointer(reflect.ValueOf(x.e.store).Elem().FieldByName("mu").UnsafeAddr()))
	probeReturned := make(chan struct{})
	x.probe.fn = func(agent cb.AgentBinding) (cb.Quiescence, error) {
		storeMutex.Lock()
		close(probeReturned)
		return cb.Quiescence{Agent: agent, Idle: true, EmptyQueue: true, EmptyBackground: true, NativeWritersDetached: true, Source: "owned-commit-boundary", ObservedAt: time.Now()}, nil
	}
	type result struct {
		grant cb.Grant
		err   error
	}
	handoverDone := make(chan result, 1)
	go func() {
		next, err := x.a.Handover(context.Background(), h)
		handoverDone <- result{next, err}
	}()
	select {
	case <-probeReturned:
	case <-time.After(time.Second):
		t.Fatal("handover did not reach its owned probe")
	}
	locked := true
	defer func() {
		if locked {
			storeMutex.Unlock()
		}
	}()
	waitCodexStack(t, "internal/store.(*Store).CodexHandover")
	callbackDone := make(chan error, 1)
	go func() {
		_, err := x.a.RecordCallback(context.Background(), cb.CallbackRecord{ID: "pending-callback", Origin: c, Key: cb.CallbackKey{UpstreamChannelID: "owned-channel", RequestID: json.RawMessage("7")}, Payload: json.RawMessage(`{"method":"unknown/privileged"}`)})
		callbackDone <- err
	}()
	waitCodexStack(t, "internal/daemon.(*codexAuthority).RecordCallback")
	// Waiting for a nonzero counter here would deadlock the correct barrier.
	// The reader has arrived, but cannot register before this commit completes.
	registeredBetweenCheckAndCommit := x.e.service.codexCallbacks.Load() != 0
	storeMutex.Unlock()
	locked = false
	var next cb.Grant
	select {
	case got := <-handoverDone:
		if got.err != nil || got.grant.Epoch <= g.Epoch {
			t.Fatal("commit-first handover did not linearize first", got.err)
		}
		next = got.grant
	case <-time.After(time.Second):
		t.Fatal("handover did not finish after the store gate opened")
	}
	select {
	case err := <-callbackDone:
		if err != nil {
			t.Fatal("historical source callback failed to persist", err)
		}
	case <-time.After(time.Second):
		t.Fatal("callback did not finish after handover")
	}
	if registeredBetweenCheckAndCommit {
		t.Fatal("callback registered between the final check and durable commit")
	}
	current, _, err := x.e.store.CodexGrant(context.Background(), next.Lease.OperationID)
	if err != nil || !current.Active || current.Admission != cb.Fenced {
		t.Fatal("post-commit callback did not fence the replacement", err)
	}
	old, _, err := x.e.store.CodexGrant(context.Background(), g.Lease.OperationID)
	if err != nil || old.Active || old.Admission != cb.Retired {
		t.Fatal("callback rewrote the historical writer", err)
	}
	_, err = x.a.Handover(context.Background(), x.handover(current, "third", cb.NativeWriter))
	assertConflict(t, err)
}
