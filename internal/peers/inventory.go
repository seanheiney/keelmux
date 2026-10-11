// Package peers implements explicit, pinned SSH enrollment and read-only
// discovery. A peer record selects an existing Host authority; it never creates
// a second identity, owns a terminal, or grants execution failover.
package peers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/federation"
	"github.com/seanheiney/keelmux/internal/protocol"
)

const (
	Version       = "keelmux-peer/1"
	InventoryPath = "/v1/peer-inventory"
	RequestLimit  = 64 << 10
	SnapshotLimit = 1 << 20
	MaxSessions   = 256
	FetchTimeout  = 3 * time.Second
)

var (
	ErrUnavailable    = errors.New("peer inventory unavailable")
	ErrMalformed      = errors.New("invalid peer inventory")
	ErrIdentity       = errors.New("peer host identity differs from the enrolled authority")
	ErrHostKey        = errors.New("selected SSH host key was rejected or is unavailable")
	ErrAuthentication = errors.New("SSH authentication unavailable; use the host-local native login")
	ErrTimeout        = errors.New("peer inventory deadline exceeded")
	ErrRevoked        = errors.New("peer inventory grant revoked")
	ErrGrant          = errors.New("peer inventory is outside the current principal grant")
	ErrStale          = errors.New("peer inventory is stale")
	ErrConflict       = errors.New("peer enrollment changed; review the exact authority and grant")
)

// Snapshot describes home registry records, not live process or workspace
// health. CapturedAt timestamps the registry read. Freshness is checked locally
// as well; a remote clock cannot indefinitely extend a cache's lifetime.
type Snapshot struct {
	Version    string           `json:"version"`
	HostID     domain.ID        `json:"host_id"`
	CapturedAt time.Time        `json:"captured_at"`
	Sessions   []domain.Session `json:"sessions"`
}

// ExportRequest is the fixed stdin exchange used by peer-export. The grant is
// the existing directional entry grant, with no wildcard or transitive access.
type ExportRequest struct {
	Version string           `json:"version"`
	Socket  string           `json:"socket"`
	Grant   federation.Grant `json:"grant"`
}

// Workspace is derived only from permitted Session records. Discovery does
// not probe a directory, run Git, or claim the recorded path still exists.
type Workspace struct {
	HostID   domain.ID   `json:"host_id"`
	Path     string      `json:"path"`
	Sessions []domain.ID `json:"sessions"`
	State    string      `json:"state"`
}

func cleanPath(p string, limit int) bool {
	return len(p) <= limit && filepath.IsAbs(p) && filepath.Clean(p) == p && utf8.ValidString(p) && !strings.ContainsFunc(p, unicode.IsControl)
}

func validPrincipal(p string) bool {
	return p != "" && len(p) <= 128 && p == strings.TrimSpace(p) && utf8.ValidString(p) && !strings.ContainsFunc(p, unicode.IsControl)
}

func validateGrant(g federation.Grant) error {
	if !validPrincipal(g.Principal) || !domain.ValidObjectID(g.HostID, "host") || len(g.Sessions) > MaxSessions {
		return ErrGrant
	}
	seen := map[domain.ID]bool{}
	for _, id := range g.Sessions {
		if !domain.ValidObjectID(id, "session") || seen[id] {
			return ErrGrant
		}
		seen[id] = true
	}
	return nil
}

func (s Snapshot) validate(host domain.ID) error {
	if s.Version != Version || !domain.ValidObjectID(s.HostID, "host") || s.CapturedAt.IsZero() || len(s.Sessions) > MaxSessions {
		return ErrMalformed
	}
	if s.HostID != host {
		return ErrIdentity
	}
	seen := map[domain.ID]bool{}
	for _, v := range s.Sessions {
		if v.HostID != host {
			return ErrIdentity // No transitive peer advertisements.
		}
		if !domain.ValidObjectID(v.ID, "session") || !domain.ValidObjectID(v.WorkID, "work") || !domain.ValidObjectID(v.ThreadID, "thread") || !domain.ValidObjectID(v.LaneID, "lane") || !domain.ValidObjectID(v.Execution.ID, "execution") || v.Execution.Generation == 0 || seen[v.ID] || !cleanPath(v.Workspace, 4096) || v.Name == "" || len(v.Name) > 128 || !utf8.ValidString(v.Name) || strings.ContainsFunc(v.Name, unicode.IsControl) || v.CreatedAt.IsZero() || (v.Mode != domain.Managed && v.Mode != domain.Bridge && v.Mode != domain.Observed) {
			return ErrMalformed
		}
		b, err := json.Marshal(v)
		if err != nil || len(b) > 16384 {
			return ErrMalformed
		}
		for _, value := range []string{v.Execution.Provider, v.Execution.ProviderVersion, v.Execution.State, v.Execution.RequestedModel.Value, v.Execution.RequestedModel.Source, v.Execution.ActualModel.Value, v.Execution.ActualModel.Source, v.Execution.Account.Value, v.Execution.Account.Source, v.Execution.NativeThread.Value, v.Execution.NativeThread.Source} {
			if len(value) > 4096 || !utf8.ValidString(value) || strings.ContainsFunc(value, unicode.IsControl) {
				return ErrMalformed
			}
		}
		seen[v.ID] = true
	}
	return nil
}

func scrub(v domain.Session) domain.Session {
	v.Execution.Runtime = domain.RuntimeRef{}
	v.Capabilities = domain.Capabilities{Explanation: "Read-only peer registry observation; attachment, execution, native credentials and authority remain at the home."}
	return v
}

func project(s Snapshot, g federation.Grant) Snapshot {
	allowed := map[domain.ID]bool{}
	for _, id := range g.Sessions {
		allowed[id] = true
	}
	out := Snapshot{Version: Version, HostID: s.HostID, CapturedAt: s.CapturedAt, Sessions: []domain.Session{}}
	for _, v := range s.Sessions {
		if allowed[v.ID] && v.HostID == g.HostID {
			out.Sessions = append(out.Sessions, scrub(v))
		}
	}
	sort.Slice(out.Sessions, func(i, j int) bool { return out.Sessions[i].ID < out.Sessions[j].ID })
	return out
}

func workspaces(sessions []domain.Session) []Workspace {
	byPath := map[string]*Workspace{}
	for _, v := range sessions {
		w := byPath[v.Workspace]
		if w == nil {
			w = &Workspace{HostID: v.HostID, Path: v.Workspace, Sessions: []domain.ID{}, State: "recorded"}
			byPath[v.Workspace] = w
		}
		w.Sessions = append(w.Sessions, v.ID)
	}
	out := []Workspace{}
	for _, w := range byPath {
		sort.Slice(w.Sessions, func(i, j int) bool { return w.Sessions[i] < w.Sessions[j] })
		out = append(out, *w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func decodeStrict(r io.Reader, limit int64, v any) error {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil || int64(len(b)) > limit || !utf8.Valid(b) {
		return ErrMalformed
	}
	// Duplicate object keys are ambiguous authority/scope statements even when
	// encoding/json would accept the last value. Reject them before decoding.
	tokens := json.NewDecoder(bytes.NewReader(b))
	if err := uniqueJSON(tokens, 0); err != nil {
		return ErrMalformed
	}
	if _, err := tokens.Token(); !errors.Is(err, io.EOF) {
		return ErrMalformed
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil || !errors.Is(d.Decode(new(any)), io.EOF) {
		return ErrMalformed
	}
	return nil
}

func uniqueJSON(d *json.Decoder, depth int) error {
	if depth > 64 {
		return ErrMalformed
	}
	t, err := d.Token()
	if err != nil {
		return err
	}
	delim, compound := t.(json.Delim)
	if !compound {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			t, err := d.Token()
			key, ok := t.(string)
			key = strings.ToLower(key) // encoding/json matches struct fields case-insensitively.
			if err != nil || !ok || seen[key] {
				return ErrMalformed
			}
			seen[key] = true
			if err := uniqueJSON(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := uniqueJSON(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return ErrMalformed
	}
	close, err := d.Token()
	if err != nil || (delim == '{' && close != json.Delim('}')) || (delim == '[' && close != json.Delim(']')) {
		return ErrMalformed
	}
	return nil
}

// HomeHandler is mounted only on the existing private home socket. That socket
// authenticates its same-user owner; it is not a remotely exposed team API.
// The SSH exporter subsequently applies the explicit directional grant before
// any session metadata leaves the home. This path performs no reconciliation.
func HomeHandler(host domain.ID, source federation.InventorySource) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Backplane-Protocol", protocol.Version)
		if r.Method != http.MethodGet || r.URL.Path != InventoryPath || r.URL.RawQuery != "" || r.Header.Get("X-Backplane-Protocol") != protocol.Version {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(protocol.Error{Message: "one read-only peer inventory request with protocol bp/1 required"})
			return
		}
		if !domain.ValidObjectID(host, "host") || source == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), FetchTimeout)
		defer cancel()
		v, err := source.List(ctx)
		if err != nil || len(v) > MaxSessions {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		s := Snapshot{Version: Version, HostID: host, CapturedAt: time.Now().UTC(), Sessions: []domain.Session{}}
		for _, session := range v {
			s.Sessions = append(s.Sessions, scrub(session))
		}
		if err != nil || s.validate(host) != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		b, err := json.Marshal(s)
		if err != nil || len(b) > SnapshotLimit {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write(b)
	})
}

// Export reads a bounded stdin request and an already running private socket.
// It does not resolve/create state, launch a daemon, install a connector, inspect
// native credential files or perform a terminal/backend operation.
func Export(ctx context.Context, input io.Reader, output io.Writer) error {
	var r ExportRequest
	if err := decodeStrict(input, RequestLimit, &r); err != nil {
		return err
	}
	if r.Version != Version || !cleanPath(r.Socket, 100) || validateGrant(r.Grant) != nil {
		return ErrMalformed
	}
	pin, err := inspectHomeSocket(r.Socket)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()
	t := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", r.Socket)
	}, DisableKeepAlives: true}
	defer t.CloseIdleConnections()
	c := &http.Client{Transport: t, Timeout: FetchTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	q, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://home"+InventoryPath, nil)
	q.Header.Set("X-Backplane-Protocol", protocol.Version)
	res, err := c.Do(q)
	if err != nil {
		if ctx.Err() != nil {
			return ErrTimeout
		}
		return ErrUnavailable
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || res.Header.Get("X-Backplane-Protocol") != protocol.Version {
		return ErrUnavailable
	}
	var s Snapshot
	if err := decodeStrict(res.Body, SnapshotLimit, &s); err != nil {
		return err
	}
	if err := s.validate(r.Grant.HostID); err != nil {
		return err
	}
	current, err := inspectHomeSocket(r.Socket)
	if err != nil || !os.SameFile(pin.socket, current.socket) || !os.SameFile(pin.parent, current.parent) {
		return ErrUnavailable
	}
	return json.NewEncoder(output).Encode(project(s, r.Grant))
}

type homeSocketPin struct{ socket, parent os.FileInfo }

// The exporter uses only the existing private home endpoint. Canonical paths
// reject symlink aliases, and both the parent and socket identities are pinned
// around the read. Inspection never repairs modes or creates missing state.
func inspectHomeSocket(path string) (homeSocketPin, error) {
	var pin homeSocketPin
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || canonical != path {
		return pin, ErrUnavailable
	}
	pin.parent, err = os.Lstat(filepath.Dir(path))
	if err != nil || !privateHomeInfo(pin.parent, true) {
		return homeSocketPin{}, ErrUnavailable
	}
	pin.socket, err = os.Lstat(path)
	if err != nil || !privateHomeInfo(pin.socket, false) {
		return homeSocketPin{}, ErrUnavailable
	}
	return pin, nil
}

func privateHomeInfo(info os.FileInfo, parent bool) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return false
	}
	if parent {
		return info.IsDir() && info.Mode().Perm() == 0700
	}
	return info.Mode()&os.ModeSocket != 0 && info.Mode().Perm() == 0600
}
