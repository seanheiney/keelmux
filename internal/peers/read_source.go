package peers

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/peerbridge"
	"github.com/seanheiney/keelmux/internal/protocol"
	"github.com/seanheiney/keelmux/internal/transport"
)

// ReadSelection explicitly selects one home-owned export policy. Enrollment
// alone remains inventory-only. It never supplies a socket or home grant.
type ReadSelection struct {
	PolicyPath string    `json:"policy_path"`
	ScopeID    domain.ID `json:"scope_id"`
}

type bridgeStarter func(context.Context, Config, ReadSelection) (io.ReadWriteCloser, error)

// ReadSource has one owned SSH incarnation, with no automatic reconnection.
// A changed enrollment revision invalidates it even after revoke/regrant.
// Revoked enrollment may be constructed for exact historical cleanup only.
type ReadSource struct {
	registry   *Registry
	host       domain.ID
	principal  string
	selected   record
	binding    readRegistryBinding
	selection  ReadSelection
	start      bridgeStarter
	ctx        context.Context
	cancel     context.CancelFunc
	mu         sync.Mutex
	client     *peerbridge.Client
	closed     bool
	failed     bool
	closeOnce  sync.Once
	cleanupErr error
}

func NewReadSource(r *Registry, host domain.ID, principal string, selection ReadSelection) (*ReadSource, error) {
	return newReadSource(r, host, principal, selection, startReadSSH)
}

func newReadSource(r *Registry, host domain.ID, principal string, selection ReadSelection, start bridgeStarter) (*ReadSource, error) {
	if r == nil || start == nil || !domain.ValidObjectID(host, "host") || !validPrincipal(principal) || !cleanPath(selection.PolicyPath, 4096) || !domain.ValidObjectID(selection.ScopeID, "scope") {
		return nil, ErrMalformed
	}
	var selected record
	var binding readRegistryBinding
	err := readRegistryTransaction(r, &binding, func(rs *records) error {
		p := find(rs, host)
		if p == nil || p.Config.Grant.Principal != principal {
			return ErrGrant
		}
		selected = *p
		selected.Config = normalize(p.Config)
		selected.Snapshot = nil
		return nil
	})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &ReadSource{registry: r, host: host, principal: principal, selected: selected, binding: binding, selection: selection, start: start, ctx: ctx, cancel: cancel}, nil
}

func (s *ReadSource) current(session domain.ID, cleanup bool) error {
	return readRegistryTransaction(s.registry, &s.binding, func(rs *records) error {
		p := find(rs, s.host)
		if p == nil || p.Config.Grant.Principal != s.principal || p.Revision != s.selected.Revision || !sameConfig(p.Config, s.selected.Config) {
			return ErrConflict
		}
		if cleanup {
			return nil
		}
		if p.Revoked || len(p.Config.Grant.Sessions) == 0 {
			return ErrRevoked
		}
		if session != "" {
			for _, id := range p.Config.Grant.Sessions {
				if id == session {
					return nil
				}
			}
			return ErrGrant
		}
		return nil
	})
}

func (s *ReadSource) trust() error {
	c := s.selected.Config
	name := c.KnownHost
	if name == "" {
		name = c.Alias
	}
	_, err := SelectKnownHost(c.KnownHostsFile, name, c.Fingerprint)
	return err
}

func (s *ReadSource) invalidate() {
	s.mu.Lock()
	s.failed = true
	client := s.client
	s.mu.Unlock()
	s.cancel()
	if client != nil {
		err := client.Close()
		s.mu.Lock()
		s.cleanupErr = errors.Join(s.cleanupErr, err)
		s.mu.Unlock()
	}
}

func (s *ReadSource) get(ctx context.Context, session domain.ID, cleanup bool) (*peerbridge.Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.current(session, cleanup); err != nil {
		if !errors.Is(err, ErrGrant) {
			s.invalidate()
		}
		return nil, err
	}
	if err := s.trust(); err != nil {
		s.invalidate()
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.failed || s.ctx.Err() != nil {
		return nil, ErrUnavailable
	}
	if s.client != nil {
		return s.client, nil
	}
	// Holding this local construction lock cannot hold the Registry lock. Only
	// one SSH process is admitted; a failed handshake permanently closes it.
	conn, err := s.start(s.ctx, s.selected.Config, s.selection)
	if err != nil {
		s.failed = true
		s.cancel()
		return nil, err
	}
	client, err := peerbridge.NewClient(conn, peerbridge.Selector{ScopeID: s.selection.ScopeID, HostID: s.host, Principal: s.principal})
	if err != nil {
		s.cleanupErr = errors.Join(s.cleanupErr, conn.Close())
		s.failed = true
		s.cancel()
		return nil, err
	}
	handshake, stop := context.WithTimeout(ctx, FetchTimeout)
	_, err = client.Hello(handshake)
	stop()
	if err != nil {
		s.cleanupErr = errors.Join(s.cleanupErr, client.Close())
		s.failed = true
		s.cancel()
		return nil, err
	}
	if err = s.current(session, cleanup); err == nil {
		err = s.trust()
	}
	if err != nil {
		s.cleanupErr = errors.Join(s.cleanupErr, client.Close())
		s.failed = true
		s.cancel()
		return nil, err
	}
	s.client = client
	go s.watch()
	return client, nil
}

func (s *ReadSource) watch() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			if err := s.current("", true); err != nil {
				s.invalidate()
				return
			}
			if err := s.trust(); err != nil {
				s.invalidate()
				return
			}
		}
	}
}

func (s *ReadSource) List(ctx context.Context) ([]domain.Session, error) {
	client, err := s.get(ctx, "", false)
	if err != nil {
		return nil, err
	}
	sessions, err := client.List(ctx)
	if err != nil {
		return nil, err
	}
	if err := (Snapshot{Version: Version, HostID: s.host, CapturedAt: time.Now().UTC(), Sessions: sessions}).validate(s.host); err != nil {
		s.invalidate()
		return nil, err
	}
	if err = s.current("", false); err != nil {
		s.invalidate()
		return nil, err
	}
	if err = s.trust(); err != nil {
		s.invalidate()
		return nil, err
	}
	allowed := map[domain.ID]bool{}
	for _, id := range s.selected.Config.Grant.Sessions {
		allowed[id] = true
	}
	out := make([]domain.Session, 0, len(sessions))
	seen := map[domain.ID]bool{}
	for _, v := range sessions {
		if v.HostID != s.host || !domain.ValidObjectID(v.ID, "session") || seen[v.ID] {
			s.invalidate()
			return nil, ErrIdentity
		}
		seen[v.ID] = true
		if !allowed[v.ID] {
			continue
		}
		read := v.Capabilities.NativeAttach && v.Capabilities.ReadOnlyAttach
		v = scrub(v)
		if read {
			v.Capabilities = domain.Capabilities{ObserverOnly: true, NativeAttach: true, ReadOnlyAttach: true, Explanation: "Scoped home-authorized read-only SSH observer; no writer or lifecycle control."}
		}
		out = append(out, v)
	}
	if err = s.current("", false); err == nil {
		err = s.trust()
	}
	if err != nil {
		s.invalidate()
		return nil, err
	}
	return out, nil
}

func readLease(r protocol.LeaseRequest, release bool) error {
	if !r.ReadOnly || r.SessionID == "" || r.ExecutionID == "" || r.Generation == 0 || r.OperationID == "" || r.ClientID == "" || (!release && r.Token != "") || (release && r.Token == "") {
		return errors.New("exact read-only acquisition identity required")
	}
	return nil
}

func (s *ReadSource) Attach(ctx context.Context, request protocol.LeaseRequest) (domain.AttachSpec, error) {
	if err := readLease(request, false); err != nil {
		return domain.AttachSpec{}, err
	}
	client, err := s.get(ctx, request.SessionID, false)
	if err != nil {
		return domain.AttachSpec{}, err
	}
	result, err := client.Attach(ctx, request)
	if err != nil {
		return domain.AttachSpec{}, err
	}
	if err = s.current(request.SessionID, false); err != nil {
		s.invalidate()
		return domain.AttachSpec{}, err
	}
	if err = s.trust(); err != nil {
		s.invalidate()
		return domain.AttachSpec{}, err
	}
	// The home client also validates this receipt. Do not expose native argv.
	result.Argv = nil
	return result, nil
}

func (s *ReadSource) Open(ctx context.Context, request transport.Request) (*transport.Stream, error) {
	if request.HostID != s.host {
		return nil, ErrIdentity
	}
	if err := readLease(request.Lease, false); err != nil {
		return nil, err
	}
	client, err := s.get(ctx, request.Lease.SessionID, false)
	if err != nil {
		return nil, err
	}
	stream, err := client.Open(ctx, request)
	if err != nil {
		return nil, err
	}
	if err = s.current(request.Lease.SessionID, false); err == nil {
		err = s.trust()
	}
	if err != nil {
		_ = stream.Close()
		s.invalidate()
		return nil, err
	}
	return stream, nil
}

func (s *ReadSource) Release(ctx context.Context, request protocol.LeaseRequest) error {
	if err := readLease(request, true); err != nil {
		return err
	}
	client, err := s.get(ctx, request.SessionID, true)
	if err != nil {
		return err
	}
	return client.Release(ctx, request)
}

func (s *ReadSource) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		client := s.client
		s.mu.Unlock()
		s.cancel()
		if client != nil {
			err := client.Close()
			s.mu.Lock()
			s.cleanupErr = errors.Join(s.cleanupErr, err)
			s.mu.Unlock()
		}
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cleanupErr
}
