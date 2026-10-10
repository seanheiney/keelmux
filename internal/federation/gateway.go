// Package federation is an early private one-entry inventory and native-stream
// experiment. Enrollment and independent remote hosts remain separate gates.
package federation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/seanheiney/agent-backplane/internal/domain"
	"github.com/seanheiney/agent-backplane/internal/protocol"
	"github.com/seanheiney/agent-backplane/internal/transport"
)

type InventorySource interface {
	List(context.Context) ([]domain.Session, error)
}
type StreamSource interface {
	InventorySource
	Open(context.Context, transport.Request) (*transport.Stream, error)
	Attach(context.Context, protocol.LeaseRequest) (domain.AttachSpec, error)
	Release(context.Context, protocol.LeaseRequest) error
}
type Peer struct {
	HostID domain.ID
	Source InventorySource
}
type Grant struct {
	Principal string
	HostID    domain.ID
	Sessions  []domain.ID
}
type Availability struct {
	HostID domain.ID `json:"host_id"`
	State  string    `json:"state"`
}
type Directory struct {
	Version  string           `json:"version"`
	Sessions []domain.Session `json:"sessions"`
	Places   []Availability   `json:"places"`
}
type Gateway struct {
	mu            sync.RWMutex
	peers         map[domain.ID]InventorySource
	grants        map[string]map[domain.ID]map[domain.ID]bool
	streams       map[uint64]activeStream
	nextStream    uint64
	attachCommand func(transport.Request) []string
	attachments   map[domain.ID]attachment
	acquisitions  map[domain.ID]acquisition
	receipts      *receiptStore
	closed        bool
	// Failures reports retained home grants when revoke cleanup is unconfirmed.
	Failures chan error
}

type acquisition struct {
	principal string
	host      domain.ID
	request   protocol.LeaseRequest
}

type activeStream struct {
	principal     string
	host, session domain.ID
	cancel        context.CancelFunc
}
type attachment struct {
	principal string
	host      domain.ID
	source    StreamSource
	lease     domain.Lease
	readOnly  bool
}

func New(peers []Peer, grants []Grant) (*Gateway, error) {
	g := &Gateway{peers: map[domain.ID]InventorySource{}, grants: map[string]map[domain.ID]map[domain.ID]bool{}, streams: map[uint64]activeStream{}, attachments: map[domain.ID]attachment{}, acquisitions: map[domain.ID]acquisition{}, Failures: make(chan error, 16)}
	for _, p := range peers {
		if p.HostID == "" || p.Source == nil {
			return nil, errors.New("peer needs exact host and authenticated private source")
		}
		if _, ok := g.peers[p.HostID]; ok {
			return nil, errors.New("duplicate host")
		}
		g.peers[p.HostID] = p.Source
	}
	for _, grant := range grants {
		if err := g.SetGrant(grant); err != nil {
			return nil, err
		}
	}
	return g, nil
}
func (g *Gateway) SetGrant(v Grant) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if v.Principal == "" {
		return errors.New("grant needs linked principal")
	}
	if _, ok := g.peers[v.HostID]; !ok {
		return errors.New("unknown peer")
	}
	if g.grants[v.Principal] == nil {
		g.grants[v.Principal] = map[domain.ID]map[domain.ID]bool{}
	}
	set := map[domain.ID]bool{}
	for _, id := range v.Sessions {
		if id == "" {
			return errors.New("empty session grant")
		}
		set[id] = true
	}
	g.grants[v.Principal][v.HostID] = set
	for _, active := range g.streams {
		if active.principal == v.Principal && active.host == v.HostID && !set[active.session] {
			active.cancel()
		}
	}
	for _, pending := range g.attachments {
		if pending.principal == v.Principal && pending.host == v.HostID && !set[pending.lease.SessionID] {
			// This also covers a TUI acquisition revoked before its companion
			// opens. Home Release refuses to erase a still-attached native writer.
			go g.releaseRevoked(pending)
		}
	}
	return nil
}

func (g *Gateway) releaseRevoked(a attachment) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	l := a.lease
	r := protocol.LeaseRequest{SessionID: l.SessionID, ExecutionID: l.ExecutionID, Generation: l.Generation, OperationID: l.OperationID, ClientID: l.ClientID, Token: l.Token, ReadOnly: a.readOnly}
	for {
		if err := a.source.Release(ctx, r); err == nil {
			return
		}
		select {
		case <-ctx.Done():
			select {
			case g.Failures <- errors.New("revoked home attachment release unconfirmed; retain the exact grant for repair"):
			default:
			}
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// claim binds public operation/client IDs to the verified original principal.
// Knowing another viewer's receipt cannot transfer that acquisition's authority.
func (g *Gateway) claim(principal string, host domain.ID, request protocol.LeaseRequest) error {
	if g.closed {
		return &protocol.HTTPError{StatusCode: 503, Message: "entry authority is closed"}
	}
	if request.SessionID == "" || request.ExecutionID == "" || request.Generation == 0 || request.OperationID == "" || request.ClientID == "" || request.Token != "" {
		return &protocol.HTTPError{StatusCode: 400, Message: "exact acquisition IDs without a supplied token required"}
	}
	expected := acquisition{principal, host, request}
	if existing, ok := g.acquisitions[request.OperationID]; ok && existing != expected {
		return &protocol.HTTPError{StatusCode: 409, Message: "acquisition already belongs to a different original principal or route"}
	}
	g.acquisitions[request.OperationID] = expected
	return nil
}

// Directory revalidates grants after upstream requests; revocation never returns a cached permitted view.
func (g *Gateway) Directory(ctx context.Context, principal string) (Directory, error) {
	out := Directory{Version: protocol.Version, Sessions: []domain.Session{}, Places: []Availability{}}
	g.mu.RLock()
	perm := g.grants[principal]
	sources := map[domain.ID]InventorySource{}
	for host, set := range perm {
		if len(set) > 0 {
			sources[host] = g.peers[host]
		}
	}
	g.mu.RUnlock()
	if perm == nil {
		return out, errors.New("principal has no granted directory")
	}
	for host, source := range sources {
		sessions, err := source.List(ctx)
		g.mu.RLock()
		current := g.grants[principal][host]
		if len(current) == 0 {
			g.mu.RUnlock()
			continue
		}
		state := "reachable"
		if err != nil {
			state = "unreachable"
		}
		out.Places = append(out.Places, Availability{host, state})
		if err == nil {
			for _, s := range sessions {
				if current[s.ID] && s.HostID == host {
					// Terminal locators belong to the home and never become a
					// downstream route. Clients address stable work/execution IDs.
					s.Execution.Runtime = domain.RuntimeRef{}
					out.Sessions = append(out.Sessions, s)
				}
			}
		}
		g.mu.RUnlock()
	}
	return out, nil
}

// Handler binds a verified principal at the authenticated entry, never from request body or display names.
func (g *Gateway) Handler(principal string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("X-Backplane-Protocol") != protocol.Version {
			w.WriteHeader(400)
			_ = json.NewEncoder(w).Encode(protocol.Error{Message: "protocol bp/1 required"})
			return
		}
		if r.Method == "POST" && r.URL.Path == "/v1/terminal" {
			transport.Handler(func(ctx context.Context, request transport.Request) (*transport.Stream, error) {
				return g.Open(ctx, principal, request)
			}).ServeHTTP(w, r)
			return
		}
		if r.Method == "POST" && (r.URL.Path == "/v1/attach" || r.URL.Path == "/v1/release") {
			var request protocol.LeaseRequest
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&request); err != nil {
				gatewayFailure(w, 400, "invalid attachment request")
				return
			}
			if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
				gatewayFailure(w, 400, "one attachment request required")
				return
			}
			if r.URL.Path == "/v1/attach" {
				v, err := g.Attach(r.Context(), principal, request)
				if err != nil {
					gatewayError(w, err)
					return
				}
				_ = json.NewEncoder(w).Encode(v)
			} else {
				if err := g.Release(r.Context(), principal, request); err != nil {
					gatewayError(w, err)
					return
				}
				w.WriteHeader(http.StatusNoContent)
			}
			return
		}
		if r.Method != "GET" || r.URL.Path != "/v1/sessions" {
			w.WriteHeader(501)
			_ = json.NewEncoder(w).Encode(protocol.Error{Message: "early private experiment: only scoped inventory and native attachment are available"})
			return
		}
		d, err := g.Directory(r.Context(), principal)
		if err != nil {
			w.WriteHeader(403)
			_ = json.NewEncoder(w).Encode(protocol.Error{Message: fmt.Sprint(err)})
			return
		}
		_ = json.NewEncoder(w).Encode(d)
	})
}

// SetAttachCommand supplies a local companion argv factory. It receives stable
// route/acquisition IDs only; home native argv is never forwarded to the client.
func (g *Gateway) SetAttachCommand(factory func(transport.Request) []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.attachCommand = factory
}

func (g *Gateway) allowed(principal string, host, session domain.ID) bool {
	return g.grants[principal][host][session]
}

func (g *Gateway) Open(ctx context.Context, principal string, request transport.Request) (*transport.Stream, error) {
	g.mu.Lock()
	if !g.allowed(principal, request.HostID, request.Lease.SessionID) {
		g.mu.Unlock()
		return nil, &protocol.HTTPError{StatusCode: 403, Message: "terminal session is outside this principal's current grant"}
	}
	source, ok := g.peers[request.HostID].(StreamSource)
	if !ok {
		g.mu.Unlock()
		return nil, &protocol.HTTPError{StatusCode: 501, Message: "home does not advertise native stream routing"}
	}
	if err := g.claim(principal, request.HostID, request.Lease); err != nil {
		g.mu.Unlock()
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	g.nextStream++
	id := g.nextStream
	g.streams[id] = activeStream{principal, request.HostID, request.Lease.SessionID, cancel}
	g.mu.Unlock()
	remove := func() { cancel(); g.mu.Lock(); delete(g.streams, id); g.mu.Unlock() }
	stream, err := source.Open(ctx, request)
	if err != nil {
		remove()
		return nil, upstreamError(err)
	}
	g.mu.RLock()
	stillAllowed := g.allowed(principal, request.HostID, request.Lease.SessionID)
	g.mu.RUnlock()
	if !stillAllowed || ctx.Err() != nil {
		_ = stream.Close()
		remove()
		return nil, &protocol.HTTPError{StatusCode: 403, Message: "terminal grant changed while opening"}
	}
	go func() { <-stream.Done(); remove() }()
	return stream, nil
}

func (g *Gateway) sourceForSession(principal string, session domain.ID) (domain.ID, StreamSource, error) {
	var selected domain.ID
	for host, set := range g.grants[principal] {
		if set[session] {
			if selected != "" {
				return "", nil, domain.ConflictError{Reason: "session route is ambiguous across homes"}
			}
			selected = host
		}
	}
	if selected == "" {
		return "", nil, &protocol.HTTPError{StatusCode: 403, Message: "session is outside this principal's current grant"}
	}
	source, ok := g.peers[selected].(StreamSource)
	if !ok {
		return "", nil, &protocol.HTTPError{StatusCode: 501, Message: "home does not advertise routed native attachment"}
	}
	return selected, source, nil
}

func (g *Gateway) Attach(ctx context.Context, principal string, request protocol.LeaseRequest) (domain.AttachSpec, error) {
	g.mu.Lock()
	host, source, err := g.sourceForSession(principal, request.SessionID)
	factory := g.attachCommand
	if err == nil {
		err = g.claim(principal, host, request)
	}
	g.mu.Unlock()
	if err != nil {
		return domain.AttachSpec{}, err
	}
	if factory == nil {
		return domain.AttachSpec{}, &protocol.HTTPError{StatusCode: 501, Message: "entry native companion is not configured; use the bounded drive experiment"}
	}
	argv := factory(transport.Request{HostID: host, Lease: request, Rows: 24, Cols: 80, Term: "xterm-256color"})
	if len(argv) == 0 {
		return domain.AttachSpec{}, errors.New("entry native companion did not supply argv")
	}
	result, err := source.Attach(ctx, request)
	if err != nil {
		return domain.AttachSpec{}, upstreamError(err)
	}
	expected := domain.Lease{SessionID: request.SessionID, ExecutionID: request.ExecutionID, Generation: request.Generation, OperationID: request.OperationID, ClientID: request.ClientID, Token: result.Lease.Token}
	if result.Lease.Token == "" || result.Lease != expected || result.ReadOnly != request.ReadOnly {
		return domain.AttachSpec{}, domain.ConflictError{Reason: "home returned a conflicting acquisition; grant retained for repair"}
	}
	g.mu.Lock()
	if !g.allowed(principal, host, request.SessionID) || g.closed {
		g.mu.Unlock()
		release := request
		release.Token = result.Lease.Token
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := source.Release(cleanup, release); err != nil {
			select {
			case g.Failures <- errors.New("acquisition revoked during acknowledgement; home release unconfirmed"):
			default:
			}
		}
		return domain.AttachSpec{}, &protocol.HTTPError{StatusCode: 403, Message: "attachment grant changed while acquiring"}
	}
	a := attachment{principal, host, source, result.Lease, result.ReadOnly}
	if g.receipts != nil {
		if err := g.receipts.save(a); err != nil {
			g.mu.Unlock()
			g.releaseRevoked(a)
			return domain.AttachSpec{}, errors.New("entry receipt persistence unconfirmed; inspect home acquisition before retry")
		}
	}
	g.attachments[request.OperationID] = a
	g.mu.Unlock()
	result.Argv = argv
	return result, nil
}

func (g *Gateway) Release(ctx context.Context, principal string, request protocol.LeaseRequest) error {
	g.mu.RLock()
	a, ok := g.attachments[request.OperationID]
	g.mu.RUnlock()
	l := domain.Lease{SessionID: request.SessionID, ExecutionID: request.ExecutionID, Generation: request.Generation, OperationID: request.OperationID, ClientID: request.ClientID, Token: request.Token}
	if !ok || a.principal != principal || a.lease != l || a.readOnly != request.ReadOnly {
		return &protocol.HTTPError{StatusCode: 403, Message: "release requires this principal's exact routed attachment grant"}
	}
	// Releasing one's confirmed grant remains valid after navigation access was
	// revoked. The home confirms actual native detach before releasing its writer.
	if err := a.source.Release(ctx, request); err != nil {
		return upstreamError(err)
	}
	return nil
}

func upstreamError(err error) error {
	var httpErr *protocol.HTTPError
	if errors.As(err, &httpErr) {
		return &protocol.HTTPError{StatusCode: httpErr.StatusCode, Message: "home rejected native attachment; inspect its current generation and ownership"}
	}
	return &protocol.HTTPError{StatusCode: 503, Message: "home transport is unreachable; native work has not moved"}
}

func gatewayFailure(w http.ResponseWriter, status int, message string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(protocol.Error{Message: message})
}
func gatewayError(w http.ResponseWriter, err error) {
	status := 500
	var h *protocol.HTTPError
	var conflict domain.ConflictError
	if errors.As(err, &h) {
		status = h.StatusCode
	} else if errors.As(err, &conflict) {
		status = 409
	}
	gatewayFailure(w, status, err.Error())
}
