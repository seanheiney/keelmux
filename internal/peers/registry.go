package peers

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/seanheiney/keelmux/internal/config"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/federation"
)

const MaxPeers = 16

type Place struct {
	HostID      domain.ID `json:"host_id"`
	Alias       string    `json:"alias"`
	State       string    `json:"state"`
	Reason      string    `json:"reason,omitempty"`
	LastSeenAt  time.Time `json:"last_seen_at,omitempty"`
	FreshUntil  time.Time `json:"fresh_until,omitempty"`
	RetainUntil time.Time `json:"retain_until,omitempty"`
}
type View struct {
	Place
	Sessions   []domain.Session `json:"sessions"`
	Workspaces []Workspace      `json:"workspaces"`
}
type Directory struct {
	Version    string           `json:"version"`
	Sessions   []domain.Session `json:"sessions"`
	Workspaces []Workspace      `json:"workspaces"`
	Places     []Place          `json:"places"`
}

type record struct {
	Config     Config    `json:"config"`
	Revision   uint64    `json:"revision"`
	Revoked    bool      `json:"revoked"`
	Snapshot   *Snapshot `json:"snapshot,omitempty"`
	ReceivedAt time.Time `json:"received_at,omitempty"`
	LastError  string    `json:"last_error,omitempty"`
}
type records struct {
	Version string   `json:"version"`
	Peers   []record `json:"peers"`
}

// Registry persists explicit enrollment and current grants. A short private
// file transaction revalidates every read, including other processes' revokes;
// network fetches never hold its lock. No background worker or model runs here.
type Registry struct {
	mu        sync.Mutex
	directory *os.File
	transport Transport
	now       func() time.Time
	closed    bool
}

// Open creates only this explicitly selected private peer store. Export does
// not call Open; contacting a missing home cannot create any home state.
func Open(dir string, transport Transport) (*Registry, error) {
	if !cleanPath(dir, 4096) {
		return nil, ErrMalformed
	}
	if err := config.EnsurePrivateDirectory(dir); err != nil {
		return nil, err
	}
	d, err := openDirectory(dir)
	if err != nil {
		return nil, err
	}
	if transport == nil {
		transport = SSHTransport{}
	}
	r := &Registry{directory: d, transport: transport, now: func() time.Time { return time.Now().UTC() }}
	if err := r.transaction(false, func(*records) error { return nil }); err != nil {
		_ = d.Close()
		return nil, err
	}
	return r, nil
}

func (r *Registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	return r.directory.Close()
}

func normalize(c Config) Config {
	c.Grant.Sessions = append([]domain.ID{}, c.Grant.Sessions...)
	sort.Slice(c.Grant.Sessions, func(i, j int) bool { return c.Grant.Sessions[i] < c.Grant.Sessions[j] })
	return c
}

func sameConfig(a, b Config) bool {
	first, _ := json.Marshal(normalize(a))
	second, _ := json.Marshal(normalize(b))
	return string(first) == string(second)
}

func samePin(a, b Config) bool {
	a.Grant, b.Grant = federation.Grant{}, federation.Grant{}
	return sameConfig(a, b)
}

func find(rs *records, host domain.ID) *record {
	for i := range rs.Peers {
		if rs.Peers[i].Config.HostID == host {
			return &rs.Peers[i]
		}
	}
	return nil
}

// Enroll performs one identity-pinned discovery before saving the reviewed
// configuration. Repeating the same config is safe. Pin/route changes require
// a separate reviewed record; this method never replaces them after an outage.
// Explicit re-enrollment of a revoked record requires the same pin and another
// successful discovery. Only SetGrant can change an active enrollment's scope.
func (r *Registry) Enroll(ctx context.Context, c Config) (View, error) {
	c = normalize(c)
	if err := c.Validate(); err != nil {
		return View{}, err
	}
	var prior uint64
	var wasRevoked bool
	err := r.transaction(false, func(rs *records) error {
		for _, existing := range rs.Peers {
			if existing.Config.Alias == c.Alias && existing.Config.HostID != c.HostID {
				return ErrConflict
			}
		}
		if existing := find(rs, c.HostID); existing != nil {
			if !samePin(existing.Config, c) || !existing.Revoked && !sameConfig(existing.Config, c) {
				return ErrConflict
			}
			prior = existing.Revision
			wasRevoked = existing.Revoked
		} else if len(rs.Peers) >= MaxPeers {
			return errors.New("peer enrollment capacity reached (16 explicit homes)")
		}
		return nil
	})
	if err != nil {
		return View{}, err
	}
	if prior != 0 && !wasRevoked {
		return r.Refresh(ctx, c.HostID, c.Grant.Principal)
	}
	snapshot, err := r.fetch(ctx, c)
	if err != nil {
		return View{}, err
	}
	var out View
	err = r.transaction(true, func(rs *records) error {
		existing := find(rs, c.HostID)
		if existing == nil {
			if prior != 0 || len(rs.Peers) >= MaxPeers {
				return ErrConflict
			}
			for _, p := range rs.Peers {
				if p.Config.Alias == c.Alias {
					return ErrConflict
				}
			}
			rs.Peers = append(rs.Peers, record{Config: c})
			existing = &rs.Peers[len(rs.Peers)-1]
		} else if existing.Revision != prior {
			return ErrConflict
		}
		if existing.Revision == ^uint64(0) {
			return ErrConflict
		}
		existing.Revision++
		existing.Config, existing.Revoked, existing.LastError = c, false, ""
		existing.Snapshot, existing.ReceivedAt = &snapshot, r.now()
		out = view(*existing, r.now())
		return nil
	})
	return out, err
}

func (r *Registry) fetch(ctx context.Context, c Config) (Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()
	s, err := r.transport.Fetch(ctx, c)
	if ctx.Err() != nil {
		return Snapshot{}, ErrTimeout
	}
	if err != nil {
		return Snapshot{}, err
	}
	if err := s.validate(c.HostID); err != nil {
		return Snapshot{}, err
	}
	now := r.now()
	if s.CapturedAt.After(now.Add(30*time.Second)) || s.CapturedAt.Before(now.Add(-time.Duration(c.RetainForSeconds)*time.Second)) {
		return Snapshot{}, ErrStale
	}
	s = project(s, c.Grant)
	b, err := json.Marshal(s)
	if err != nil || len(b) > SnapshotLimit {
		return Snapshot{}, ErrMalformed
	}
	return s, nil
}

// SetGrant applies the existing principal/Host/session grant immediately. A
// broader grant cannot resurrect records that were never in the permitted
// cache; refresh is required. Grant changes invalidate freshness until fetched.
func (r *Registry) SetGrant(g federation.Grant) error {
	if err := validateGrant(g); err != nil {
		return err
	}
	return r.transaction(true, func(rs *records) error {
		p := find(rs, g.HostID)
		if p == nil || p.Config.Grant.Principal != g.Principal {
			return ErrGrant
		}
		if sameConfig(Config{Grant: p.Config.Grant}, Config{Grant: g}) {
			return nil
		}
		if p.Revision == ^uint64(0) {
			return ErrConflict
		}
		p.Config.Grant = normalize(Config{Grant: g}).Grant
		p.Revision++
		p.LastError = "grant_changed"
		if p.Snapshot != nil {
			s := project(*p.Snapshot, g)
			p.Snapshot = &s
		}
		return nil
	})
}

// Revoke removes permitted cached metadata durably and prevents any subsequent
// transport call until the owner explicitly enrolls the same pinned home again.
func (r *Registry) Revoke(host domain.ID, principal string) error {
	return r.transaction(true, func(rs *records) error {
		p := find(rs, host)
		if p == nil || p.Config.Grant.Principal != principal {
			return ErrGrant
		}
		if p.Revision == ^uint64(0) {
			return ErrConflict
		}
		p.Revision++
		p.Revoked, p.Snapshot, p.LastError = true, nil, "revoked"
		p.Config.Grant.Sessions = []domain.ID{}
		return nil
	})
}

func (r *Registry) View(host domain.ID, principal string) (View, error) {
	var out View
	err := r.transaction(false, func(rs *records) error {
		p := find(rs, host)
		if p == nil || p.Config.Grant.Principal != principal {
			return ErrGrant
		}
		out = view(*p, r.now())
		return nil
	})
	return out, err
}

// Refresh reports transport failure in the Place rather than discarding known
// history or calling it fresh success. Revocation or scope change while fetching
// invalidates the entire response before it can enter the cache.
func (r *Registry) Refresh(ctx context.Context, host domain.ID, principal string) (View, error) {
	var selected record
	err := r.transaction(false, func(rs *records) error {
		p := find(rs, host)
		if p == nil || p.Config.Grant.Principal != principal {
			return ErrGrant
		}
		selected = *p
		return nil
	})
	if err != nil {
		return View{}, err
	}
	if selected.Revoked || len(selected.Config.Grant.Sessions) == 0 {
		return r.View(host, principal)
	}
	s, fetchErr := r.fetch(ctx, selected.Config)
	if ctx.Err() != nil {
		fetchErr = ErrTimeout
	}
	var out View
	err = r.transaction(true, func(rs *records) error {
		p := find(rs, host)
		if p == nil || p.Config.Grant.Principal != principal {
			return ErrGrant
		}
		if p.Revision != selected.Revision {
			out = view(*p, r.now())
			return nil // Late metadata is discarded, never reauthorized.
		}
		if fetchErr == nil && p.Snapshot != nil {
			fetchErr = consistent(*p.Snapshot, s)
		}
		if fetchErr == nil {
			p.Snapshot, p.ReceivedAt, p.LastError = &s, r.now(), ""
		} else {
			p.LastError = reason(fetchErr)
		}
		out = view(*p, r.now())
		return nil
	})
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	return out, err
}

// A stable Session cannot silently refer to another Work, Thread or Lane.
// Execution attempts can advance, but a delayed older generation cannot
// replace newer home evidence merely because it arrived later over SSH.
func consistent(old, next Snapshot) error {
	if next.CapturedAt.Before(old.CapturedAt) {
		return ErrStale
	}
	previous := map[domain.ID]domain.Session{}
	for _, s := range old.Sessions {
		previous[s.ID] = s
	}
	for _, s := range next.Sessions {
		p, known := previous[s.ID]
		if !known {
			continue
		}
		if p.WorkID != s.WorkID || p.ThreadID != s.ThreadID || p.LaneID != s.LaneID {
			return ErrIdentity
		}
		if s.Execution.Generation < p.Execution.Generation || s.Execution.Generation == p.Execution.Generation && s.Execution.ID != p.Execution.ID {
			return ErrStale
		}
	}
	return nil
}

func reason(err error) string {
	switch {
	case errors.Is(err, ErrHostKey):
		return "host_key_rejected"
	case errors.Is(err, ErrIdentity):
		return "host_identity_changed"
	case errors.Is(err, ErrMalformed):
		return "malformed_inventory"
	case errors.Is(err, ErrAuthentication):
		return "authentication_unavailable"
	case errors.Is(err, ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, ErrStale):
		return "stale_advertisement"
	default:
		return "transport_unavailable"
	}
}

func view(p record, now time.Time) View {
	out := View{Place: Place{HostID: p.Config.HostID, Alias: p.Config.Alias, State: "unavailable", Reason: p.LastError, LastSeenAt: p.ReceivedAt}, Sessions: []domain.Session{}, Workspaces: []Workspace{}}
	if p.Revoked || len(p.Config.Grant.Sessions) == 0 {
		out.State, out.Reason = "revoked", "no_current_inventory_grant"
		return out
	}
	if p.Snapshot == nil {
		return out
	}
	out.FreshUntil = p.ReceivedAt.Add(time.Duration(p.Config.FreshForSeconds) * time.Second)
	if remote := p.Snapshot.CapturedAt.Add(time.Duration(p.Config.FreshForSeconds) * time.Second); remote.Before(out.FreshUntil) {
		out.FreshUntil = remote
	}
	out.RetainUntil = p.ReceivedAt.Add(time.Duration(p.Config.RetainForSeconds) * time.Second)
	if remote := p.Snapshot.CapturedAt.Add(time.Duration(p.Config.RetainForSeconds) * time.Second); remote.Before(out.RetainUntil) {
		out.RetainUntil = remote
	}
	if now.Before(p.ReceivedAt.Add(-30 * time.Second)) {
		out.Reason = "local_clock_changed"
		return out
	}
	if !now.Before(out.RetainUntil) {
		out.Reason = "cache_expired"
		return out
	}
	out.State = "available"
	if p.LastError != "" || !now.Before(out.FreshUntil) {
		out.State = "stale"
		if out.Reason == "" {
			out.Reason = "freshness_expired"
		}
	}
	s := project(*p.Snapshot, p.Config.Grant)
	out.Sessions, out.Workspaces = s.Sessions, workspaces(s.Sessions)
	return out
}

// Directory collects homes within one shared deadline and projects all results
// against one final current-grant transaction. Healthy homes remain available
// when another is offline. Stale records are observations, never action targets.
func (r *Registry) Directory(ctx context.Context, principal string, refresh bool) (Directory, error) {
	if ctx.Err() != nil {
		return Directory{}, ctx.Err()
	}
	if !validPrincipal(principal) {
		return Directory{}, ErrGrant
	}
	if refresh {
		var hosts []domain.ID
		versions := map[domain.ID]uint64{}
		if err := r.transaction(false, func(rs *records) error {
			for _, p := range rs.Peers {
				if p.Config.Grant.Principal == principal {
					hosts = append(hosts, p.Config.HostID)
					versions[p.Config.HostID] = p.Revision
				}
			}
			return nil
		}); err != nil {
			return Directory{}, err
		}
		ctx, cancel := context.WithTimeout(ctx, FetchTimeout)
		defer cancel()
		var wg sync.WaitGroup
		var completedMu sync.Mutex
		completed := map[domain.ID]bool{}
		sem := make(chan struct{}, 4)
		for _, host := range hosts {
			wg.Add(1)
			go func(host domain.ID) {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					return
				}
				defer func() { <-sem }()
				_, _ = r.Refresh(ctx, host, principal)
				completedMu.Lock()
				completed[host] = true
				completedMu.Unlock()
			}(host)
		}
		wg.Wait()
		// Peers which never acquired the bounded fetch slot must not appear
		// freshly checked when the shared deadline was consumed by other homes.
		if err := r.transaction(true, func(rs *records) error {
			for _, host := range hosts {
				if completed[host] {
					continue
				}
				p := find(rs, host)
				if p != nil && p.Revision == versions[host] && !p.Revoked && len(p.Config.Grant.Sessions) > 0 {
					p.LastError = "timeout"
				}
			}
			return nil
		}); err != nil {
			return Directory{}, err
		}
	}
	if ctx.Err() != nil {
		return Directory{}, ctx.Err()
	}
	out := Directory{Version: Version, Sessions: []domain.Session{}, Workspaces: []Workspace{}, Places: []Place{}}
	err := r.transaction(false, func(rs *records) error {
		for _, p := range rs.Peers {
			if p.Config.Grant.Principal != principal {
				continue
			}
			v := view(p, r.now())
			out.Places = append(out.Places, v.Place)
			out.Sessions = append(out.Sessions, v.Sessions...)
			out.Workspaces = append(out.Workspaces, v.Workspaces...)
		}
		return nil
	})
	sort.Slice(out.Places, func(i, j int) bool { return out.Places[i].HostID < out.Places[j].HostID })
	sort.Slice(out.Sessions, func(i, j int) bool {
		if out.Sessions[i].HostID != out.Sessions[j].HostID {
			return out.Sessions[i].HostID < out.Sessions[j].HostID
		}
		return out.Sessions[i].ID < out.Sessions[j].ID
	})
	sort.Slice(out.Workspaces, func(i, j int) bool {
		if out.Workspaces[i].HostID != out.Workspaces[j].HostID {
			return out.Workspaces[i].HostID < out.Workspaces[j].HostID
		}
		return out.Workspaces[i].Path < out.Workspaces[j].Path
	})
	return out, err
}

// Source fits the existing entry InventorySource contract only. Even a fresh
// SSH inventory does not implement StreamSource or establish attach authority.
type Source struct {
	Registry  *Registry
	HostID    domain.ID
	Principal string
}

func (s Source) List(ctx context.Context) ([]domain.Session, error) {
	if s.Registry == nil {
		return nil, ErrUnavailable
	}
	v, err := s.Registry.Refresh(ctx, s.HostID, s.Principal)
	if err != nil {
		return nil, err
	}
	if v.State == "revoked" {
		return nil, ErrRevoked
	}
	if v.State != "available" {
		return nil, ErrStale
	}
	return v.Sessions, nil
}
