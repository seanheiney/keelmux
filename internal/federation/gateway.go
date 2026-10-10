// Package federation is an early private one-entry inventory experiment.
// Terminal stream routing, enrollment and real remote hosts remain separate gates.
package federation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/seanheiney/agent-backplane/internal/domain"
	"github.com/seanheiney/agent-backplane/internal/protocol"
	"net/http"
	"sync"
)

type InventorySource interface {
	List(context.Context) ([]domain.Session, error)
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
	mu     sync.RWMutex
	peers  map[domain.ID]InventorySource
	grants map[string]map[domain.ID]map[domain.ID]bool
}

func New(peers []Peer, grants []Grant) (*Gateway, error) {
	g := &Gateway{peers: map[domain.ID]InventorySource{}, grants: map[string]map[domain.ID]map[domain.ID]bool{}}
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
		if r.Method != "GET" || r.URL.Path != "/v1/sessions" {
			w.WriteHeader(501)
			_ = json.NewEncoder(w).Encode(protocol.Error{Message: "early inventory experiment: routed terminal streams and mutations not implemented"})
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
