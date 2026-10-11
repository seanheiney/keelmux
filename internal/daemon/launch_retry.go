package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
	"github.com/seanheiney/keelmux/internal/store"
)

// RetryLaunch resumes only an existing durable launch intent. The original
// request is private store data, never reconstructed from current selections
// and never returned to the caller. The owner-private store remains trusted;
// canonical encoding and binding checks do not authenticate owner rewrites.
func (s *Service) RetryLaunch(ctx context.Context, id domain.ID) (protocol.LaunchResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out protocol.LaunchResponse
	if id == "" || len(id) > protocol.MaxControlRequestBytes {
		return out, invalid("launch retry requires a bounded operation ID")
	}
	prior, raw, err := s.store.Operation(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return out, err
		}
		return out, conflict("stored launch operation is unreadable; repair the original record")
	}
	if prior.ID != id || prior.Kind != "launch" || prior.SessionID == "" || prior.ExecutionID == "" {
		return out, conflict("operation does not identify an exact stored launch")
	}
	switch prior.Outcome {
	case domain.Recorded, domain.Attempted, domain.Uncertain, domain.Completed, domain.Failed:
	default:
		return out, conflict("stored launch outcome is invalid; repair the original record")
	}
	in, err := storedLaunchRequest(id, raw)
	if err != nil {
		return out, err
	}
	v, err := s.store.Session(ctx, prior.SessionID)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return out, err
		}
		return out, conflict("stored launch session is unreadable; repair the original record")
	}
	if v.ID != prior.SessionID || v.HostID != s.hostID || v.Execution.ID != prior.ExecutionID ||
		v.Execution.Generation == 0 || v.WorkID == "" || v.ThreadID == "" || v.LaneID == "" || v.Mode != domain.Managed ||
		v.Name != in.Name || v.Workspace != in.Workspace || v.Execution.Provider != in.Provider || v.Execution.ProviderVersion != in.ProviderVersion ||
		!sameLaunchLibrary(v.Library, in.Library) {
		return out, conflict("stored launch request and home session bindings disagree; repair the original record")
	}
	return s.retryLaunchLocked(ctx, in, prior)
}

// storedLaunchRequest accepts exactly the canonical representation written by
// Launch. Byte equality also rejects duplicate keys, case aliases, extra JSON
// values and field omissions without exposing saved arguments in diagnostics.
func storedLaunchRequest(id domain.ID, raw []byte) (protocol.LaunchRequest, error) {
	var in protocol.LaunchRequest
	refuse := func() (protocol.LaunchRequest, error) {
		return protocol.LaunchRequest{}, conflict("stored launch request is invalid; repair the original record")
	}
	if len(raw) == 0 || len(raw) >= protocol.MaxControlRequestBytes {
		return refuse()
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&in); err != nil {
		return refuse()
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return refuse()
	}
	if !bytes.Equal(raw, payload(in)) || protocol.ValidateLaunchRequestSize(in) != nil || in.OperationID != id ||
		in.Provider == "" || strings.TrimSpace(in.Name) == "" || !filepath.IsAbs(in.Workspace) ||
		strings.ContainsRune(in.Workspace, 0) || len(in.Argv) == 0 || in.Argv[0] == "" {
		return refuse()
	}
	for _, arg := range in.Argv {
		if strings.ContainsRune(arg, 0) {
			return refuse()
		}
	}
	if in.Library != nil && in.Library.Validate() != nil {
		return refuse()
	}
	return in, nil
}

func sameLaunchLibrary(a, b *domain.LibrarySnapshot) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// retryLaunchLocked is shared with Launch's existing-operation path. Its caller
// holds s.mu and has already matched the original request and launch operation.
// Only Recorded can claim an attempt; unknown effects are looked up, not replayed.
func (s *Service) retryLaunchLocked(ctx context.Context, in protocol.LaunchRequest, prior domain.Operation) (protocol.LaunchResponse, error) {
	if prior.Outcome == domain.Recorded {
		return s.attemptLaunch(ctx, in, prior)
	}
	if prior.Outcome == domain.Attempted || prior.Outcome == domain.Uncertain {
		if err := s.reconcileOperation(ctx, prior); err != nil {
			return protocol.LaunchResponse{}, err
		}
	}
	return s.launchResponse(ctx, in.OperationID)
}
