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
	"sort"
	"sync"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
	"github.com/seanheiney/keelmux/internal/transport"
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
	claimsDurable map[domain.ID]bool
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
	g := &Gateway{peers: map[domain.ID]InventorySource{}, grants: map[string]map[domain.ID]map[domain.ID]bool{}, streams: map[uint64]activeStream{}, attachments: map[domain.ID]attachment{}, acquisitions: map[domain.ID]acquisition{}, claimsDurable: map[domain.ID]bool{}, Failures: make(chan error, 16)}
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
	if g.claimsDurable[request.OperationID] {
		return nil
	}
	// Keep the in-memory reservation even if persistence is unconfirmed. No
	// external effect is attempted until the original principal is durable.
	g.acquisitions[request.OperationID] = expected
	if g.receipts != nil {
		if err := g.receipts.saveIntent(expected); err != nil {
			return errors.New("entry acquisition intent persistence unconfirmed; inspect private receipt before retry")
		}
	}
	g.claimsDurable[request.OperationID] = true
	return nil
}

// Each inventory request has one bounded home-fetch budget. A silent home
// cannot consume the downstream control client's entire response deadline.
const directoryTimeout = 3 * time.Second

// Directory projects fetched home metadata against one current grant snapshot.
func (g *Gateway) Directory(ctx context.Context, principal string) (Directory, error) {
	out := Directory{Version: protocol.Version, Sessions: []domain.Session{}, Places: []Availability{}}
	g.mu.RLock()
	if g.closed {
		g.mu.RUnlock()
		return out, errors.New("entry authority is closed")
	}
	perm, exists := g.grants[principal]
	sources := map[domain.ID]InventorySource{}
	for host, set := range perm {
		if len(set) > 0 {
			sources[host] = g.peers[host]
		}
	}
	g.mu.RUnlock()
	if !exists {
		return out, errors.New("principal has no granted directory")
	}
	fetchCtx, cancel := context.WithTimeout(ctx, directoryTimeout)
	defer cancel()
	type result struct {
		host     domain.ID
		sessions []domain.Session
		err      error
	}
	results := make(chan result, len(sources))
	for host, source := range sources {
		go func(host domain.ID, source InventorySource) {
			sessions, err := source.List(fetchCtx)
			results <- result{host, sessions, err}
		}(host, source)
	}
	fetched := make(map[domain.ID]result, len(sources))
	for len(fetched) < len(sources) {
		select {
		case r := <-results:
			fetched[r.host] = r
		case <-fetchCtx.Done():
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			// Drain already completed homes, then mark the remaining homes
			// unreachable. Late results cannot alter this projection.
			for {
				select {
				case r := <-results:
					fetched[r.host] = r
				default:
					goto projected
				}
			}
		}
	}
projected:
	hosts := make([]domain.ID, 0, len(sources))
	for host := range sources {
		hosts = append(hosts, host)
	}
	sort.Slice(hosts, func(i, j int) bool { return hosts[i] < hosts[j] })
	// All collected results are projected against one current grant snapshot.
	// Revocation during a later home fetch also removes earlier home metadata.
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.closed {
		return out, errors.New("entry authority is closed")
	}
	for _, host := range hosts {
		current := g.grants[principal][host]
		if len(current) == 0 {
			continue
		}
		r, complete := fetched[host]
		state := "unreachable"
		if complete && r.err == nil {
			state = "reachable"
		}
		out.Places = append(out.Places, Availability{host, state})
		if !complete || r.err != nil {
			continue
		}
		for _, s := range r.sessions {
			if current[s.ID] && s.HostID == host {
				s.Execution.Runtime = domain.RuntimeRef{}
				s.Capabilities.Stop = false
				s.Capabilities.SemanticPrompt = false
				s.Capabilities.NativeResume = false
				s.Capabilities.Explanation = "This entry permits native terminal attachment; management and native credentials remain at the execution home."
				out.Sessions = append(out.Sessions, s)
			}
		}
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
	if _, err := g.acquire(ctx, principal, request.HostID, source, request.Lease); err != nil {
		remove()
		return nil, err
	}
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
	g.mu.RLock()
	host, source, err := g.sourceForSession(principal, request.SessionID)
	factory := g.attachCommand
	g.mu.RUnlock()
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
	result, err := g.acquire(ctx, principal, host, source, request)
	if err != nil {
		return domain.AttachSpec{}, err
	}
	result.Argv = argv
	return result, nil
}

// acquire persists the original principal and exact route before the home can
// create a grant. An uncertain home acknowledgement leaves that pending intent
// reserved across restart; only its original principal can explicitly reconcile
// the same request through the home's idempotent generation/lease checks.
func (g *Gateway) acquire(ctx context.Context, principal string, host domain.ID, source StreamSource, request protocol.LeaseRequest) (domain.AttachSpec, error) {
	g.mu.Lock()
	if !g.allowed(principal, host, request.SessionID) {
		g.mu.Unlock()
		return domain.AttachSpec{}, &protocol.HTTPError{StatusCode: 403, Message: "attachment session is outside this principal's current grant"}
	}
	err := g.claim(principal, host, request)
	g.mu.Unlock()
	if err != nil {
		return domain.AttachSpec{}, err
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
		g.releaseRevoked(attachment{principal, host, source, result.Lease, result.ReadOnly})
		return domain.AttachSpec{}, &protocol.HTTPError{StatusCode: 403, Message: "attachment grant changed while acquiring"}
	}
	a := attachment{principal, host, source, result.Lease, result.ReadOnly}
	if prior, ok := g.attachments[request.OperationID]; ok && (prior.lease != a.lease || prior.readOnly != a.readOnly) {
		g.mu.Unlock()
		return domain.AttachSpec{}, domain.ConflictError{Reason: "home changed the saved acquisition outcome; retain original receipt for repair"}
	}
	if g.receipts != nil {
		if err := g.receipts.save(a); err != nil {
			g.mu.Unlock()
			g.releaseRevoked(a)
			return domain.AttachSpec{}, errors.New("entry receipt persistence unconfirmed; inspect home acquisition before retry")
		}
	}
	g.attachments[request.OperationID] = a
	g.mu.Unlock()
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
