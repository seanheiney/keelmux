package owned

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/seanheiney/keelmux/internal/adapters/nativepty"
	"github.com/seanheiney/keelmux/internal/domain"
	bpruntime "github.com/seanheiney/keelmux/internal/runtime"
)

type recordingBackend struct {
	calls int
	err   error
	ref   domain.RuntimeRef
}

func (b *recordingBackend) Launch(context.Context, domain.LaunchSpec) (domain.RuntimeRef, error) {
	b.calls++
	return b.ref, b.err
}
func (b *recordingBackend) Lookup(context.Context, domain.ID, domain.ID) (bpruntime.Observation, error) {
	b.calls++
	return bpruntime.Observation{Ref: b.ref}, b.err
}
func (b *recordingBackend) Inspect(context.Context, domain.RuntimeRef) (bpruntime.Observation, error) {
	b.calls++
	return bpruntime.Observation{Ref: b.ref}, b.err
}
func (b *recordingBackend) Stop(context.Context, domain.RuntimeRef) error { b.calls++; return b.err }
func (b *recordingBackend) AttachCommand(domain.RuntimeRef, bool) ([]string, error) {
	b.calls++
	return []string{"native-helper"}, b.err
}
func (b *recordingBackend) ValidateLink(context.Context, domain.ID, domain.ID, domain.ID, string) error {
	b.calls++
	return b.err
}
func TestNewExecutionAndUnknownNativeNeverFallBack(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	primary := &recordingBackend{err: nativepty.ErrUnavailable}
	factories := 0
	b := &Backend{primary: primary, nativeDir: filepath.Join(dir, "native"), legacySocket: filepath.Join(dir, "legacy.sock"), legacyFactory: func() (bpruntime.Backend, error) { factories++; return nil, errors.New("tmux unavailable") }}
	spec := domain.LaunchSpec{Session: domain.Session{Execution: domain.Execution{ID: "execution_new"}}, OperationID: "op_new"}
	if _, e := b.Launch(ctx, spec); !errors.Is(e, nativepty.ErrUnavailable) {
		t.Fatal(e)
	}
	if _, e := b.Lookup(ctx, "execution_new", "op_new"); !errors.Is(e, nativepty.ErrUnavailable) {
		t.Fatal(e)
	}
	ref := domain.RuntimeRef{Backend: "native"}
	_, _ = b.Inspect(ctx, ref)
	_ = b.Stop(ctx, ref)
	_, _ = b.AttachCommand(ref, false)
	_ = b.ValidateLink(ctx, "s", "execution_new", "op_new", "token")
	if primary.calls != 6 || factories != 0 {
		t.Fatalf("native operations=%d legacy factory=%d", primary.calls, factories)
	}
	if _, e := b.Inspect(ctx, domain.RuntimeRef{Backend: "arbitrary"}); e == nil {
		t.Fatal("unknown backend adopted")
	}
}
func TestLegacyReceiptRoutesOnlyItsExactLookup(t *testing.T) {
	dir := t.TempDir()
	legacySocket := filepath.Join(dir, "legacy.sock")
	nativeDir := filepath.Join(dir, "native")
	if e := os.Mkdir(legacySocket+".bp", 0700); e != nil {
		t.Fatal(e)
	}
	primary := &recordingBackend{}
	old := &recordingBackend{ref: domain.RuntimeRef{ExecutionID: "execution_old", Backend: "tmux"}}
	factory := 0
	b := &Backend{primary: primary, nativeDir: nativeDir, legacySocket: legacySocket, legacyFactory: func() (bpruntime.Backend, error) { factory++; return old, nil }}
	sum := sha256.Sum256([]byte("execution_old"))
	key := hex.EncodeToString(sum[:])
	if e := os.WriteFile(filepath.Join(legacySocket+".bp", key+".json"), []byte("routing fence only"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := b.Lookup(context.Background(), "execution_old", "op_old"); e != nil {
		t.Fatal(e)
	}
	if _, e := b.Launch(context.Background(), domain.LaunchSpec{Session: domain.Session{Execution: domain.Execution{ID: "execution_old"}}}); e == nil {
		t.Fatal("legacy receipt relaunched")
	}
	if primary.calls != 0 || old.calls != 1 || factory != 1 {
		t.Fatalf("primary=%d old=%d factory=%d", primary.calls, old.calls, factory)
	}
	if e := os.Mkdir(nativeDir, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(nativeDir, key[:24]+".json"), []byte("ambiguous"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := b.Lookup(context.Background(), "execution_old", "op_old"); e == nil {
		t.Fatal("two receipts adopted")
	}
	if old.calls != 1 || primary.calls != 0 {
		t.Fatal("ambiguous lookup touched backend")
	}
}
