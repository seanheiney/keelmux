// Package owned makes native supervision authoritative for new executions while
// preserving exact, explicitly recorded legacy tmux executions. It never falls
// back to another backend after a failed launch, lookup or inspection.
package owned

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/seanheiney/keelmux/internal/adapters/nativepty"
	"github.com/seanheiney/keelmux/internal/adapters/tmux"
	"github.com/seanheiney/keelmux/internal/domain"
	bpruntime "github.com/seanheiney/keelmux/internal/runtime"
)

type Backend struct {
	primary                 bpruntime.Backend
	nativeDir, legacySocket string
	legacyFactory           func() (bpruntime.Backend, error)
	mu                      sync.Mutex
	legacy                  bpruntime.Backend
}

var _ bpruntime.Backend = (*Backend)(nil)
var _ bpruntime.LinkVerifier = (*Backend)(nil)

func New(native *nativepty.Backend, nativeDir, legacySocket string) (*Backend, error) {
	if native == nil || !filepath.IsAbs(nativeDir) || !filepath.IsAbs(legacySocket) {
		return nil, errors.New("owned backend requires native supervision and exact private state paths")
	}
	return &Backend{primary: native, nativeDir: nativeDir, legacySocket: legacySocket, legacyFactory: func() (bpruntime.Backend, error) { return tmux.New(legacySocket) }}, nil
}
func (b *Backend) compatibility() (bpruntime.Backend, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.legacy != nil {
		return b.legacy, nil
	}
	v, err := b.legacyFactory()
	if err != nil {
		return nil, err
	}
	b.legacy = v
	return v, nil
}
func exists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

// Path existence only fences routing. Each backend validates its private receipt,
// full IDs, authenticated runtime and process lifetime before any authority use.
func (b *Backend) executionBackend(execution domain.ID) (bpruntime.Backend, error) {
	sum := sha256.Sum256([]byte(execution))
	key := hex.EncodeToString(sum[:])
	old, err := exists(filepath.Join(b.legacySocket+".bp", key+".json"))
	if err != nil {
		return nil, err
	}
	current, err := exists(filepath.Join(b.nativeDir, key[:24]+".json"))
	if err != nil {
		return nil, err
	}
	if old && current {
		return nil, domain.ConflictError{Reason: "execution has receipts in two backends; explicit repair required"}
	}
	if old {
		return b.compatibility()
	}
	return b.primary, nil
}
func (b *Backend) refBackend(ref domain.RuntimeRef) (bpruntime.Backend, error) {
	switch ref.Backend {
	case "native":
		return b.primary, nil
	case "tmux":
		return b.compatibility()
	default:
		return nil, errors.New("runtime backend is unknown; explicit repair required")
	}
}
func (b *Backend) Launch(ctx context.Context, spec domain.LaunchSpec) (domain.RuntimeRef, error) {
	selected, err := b.executionBackend(spec.Session.Execution.ID)
	if err != nil {
		return domain.RuntimeRef{}, err
	}
	if selected != b.primary {
		return domain.RuntimeRef{}, domain.ConflictError{Reason: "legacy execution cannot be relaunched through a different backend"}
	}
	return b.primary.Launch(ctx, spec)
}
func (b *Backend) Lookup(ctx context.Context, execution, operation domain.ID) (bpruntime.Observation, error) {
	selected, err := b.executionBackend(execution)
	if err != nil {
		return bpruntime.Observation{}, err
	}
	o, err := selected.Lookup(ctx, execution, operation)
	if err == nil && o.Ref.ExecutionID == "" {
		return o, nativepty.ErrUnavailable
	}
	return o, err
}
func (b *Backend) Inspect(ctx context.Context, ref domain.RuntimeRef) (bpruntime.Observation, error) {
	selected, err := b.refBackend(ref)
	if err != nil {
		return bpruntime.Observation{}, err
	}
	return selected.Inspect(ctx, ref)
}
func (b *Backend) Stop(ctx context.Context, ref domain.RuntimeRef) error {
	selected, err := b.refBackend(ref)
	if err != nil {
		return err
	}
	return selected.Stop(ctx, ref)
}
func (b *Backend) AttachCommand(ref domain.RuntimeRef, readOnly bool) ([]string, error) {
	selected, err := b.refBackend(ref)
	if err != nil {
		return nil, err
	}
	return selected.AttachCommand(ref, readOnly)
}
func (b *Backend) ValidateLink(ctx context.Context, session, execution, operation domain.ID, token string) error {
	selected, err := b.executionBackend(execution)
	if err != nil {
		return err
	}
	verifier, ok := selected.(bpruntime.LinkVerifier)
	if !ok {
		return errors.New("runtime cannot verify inherited link authority")
	}
	return verifier.ValidateLink(ctx, session, execution, operation, token)
}
