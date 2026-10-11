package peers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/peerbridge"
	"github.com/seanheiney/keelmux/internal/protocol"
)

type countedBridge struct {
	net.Conn
	once   sync.Once
	closed *atomic.Int32
}

func (c *countedBridge) Close() error {
	var err error
	c.once.Do(func() { c.closed.Add(1); err = c.Conn.Close() })
	return err
}

type readSourceFixture struct {
	registry       *Registry
	config         Config
	snapshot       Snapshot
	selection      ReadSelection
	starts, closes atomic.Int32
	done           chan error
}

func newReadSourceFixture(t *testing.T) *readSourceFixture {
	t.Helper()
	c, snapshot := fixture(t)
	writeKnownKey(t, &c)
	snapshot.Sessions[0].Execution.State = "running"
	snapshot.Sessions[0].Capabilities = domain.Capabilities{NativeAttach: true, ReadOnlyAttach: true, Stop: true, SemanticPrompt: true, NativeResume: true}
	socket := ownedSocket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/sessions":
			json.NewEncoder(w).Encode(protocol.Inventory{Version: protocol.Version, Sessions: snapshot.Sessions})
		case "/v1/peer-inventory":
			w.Header().Set("X-Backplane-Protocol", protocol.Version)
			json.NewEncoder(w).Encode(snapshot)
		default:
			t.Errorf("unexpected home effect: %s", r.URL.Path)
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	registryRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := Open(filepath.Join(registryRoot, "peers"), transportFunc(func(context.Context, Config) (Snapshot, error) { return snapshot, nil }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	if _, err := r.Enroll(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	selection := ReadSelection{PolicyPath: filepath.Join(dir, "authority.json"), ScopeID: domain.NewID("scope")}
	policy := peerbridge.ReadPolicy{Version: peerbridge.Version, ScopeID: selection.ScopeID, HostID: c.HostID, Principal: c.Grant.Principal, Socket: socket, Revision: 1, Enabled: true}
	for _, session := range snapshot.Sessions {
		policy.Sessions = append(policy.Sessions, peerbridge.SessionActions{SessionID: session.ID, Inventory: true, TerminalRead: true})
	}
	if err := peerbridge.ProvisionPolicy(context.Background(), selection.PolicyPath, policy); err != nil {
		t.Fatal(err)
	}
	return &readSourceFixture{registry: r, config: c, snapshot: snapshot, selection: selection, done: make(chan error, 1)}
}

func (f *readSourceFixture) source(t *testing.T) *ReadSource {
	t.Helper()
	s, err := newReadSource(f.registry, f.config.HostID, f.config.Grant.Principal, f.selection, func(ctx context.Context, c Config, selected ReadSelection) (io.ReadWriteCloser, error) {
		if c.HostID != f.config.HostID || selected != f.selection {
			return nil, ErrIdentity
		}
		f.starts.Add(1)
		local, home := net.Pipe()
		go func() { f.done <- peerbridge.Serve(ctx, selected.PolicyPath, home, home) }()
		return &countedBridge{Conn: local, closed: &f.closes}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.Close()
		if f.starts.Load() != 0 {
			select {
			case <-f.done:
			case <-time.After(4 * time.Second):
				t.Error("owned bridge did not finish")
			}
		}
	})
	return s
}

func TestReadSourceOptInIsLazyAndKeepsScopeAndCapabilities(t *testing.T) {
	f := newReadSourceFixture(t)
	s := f.source(t)
	if f.starts.Load() != 0 {
		t.Fatal("constructor contacted home")
	}
	if _, err := newReadSource(f.registry, f.config.HostID, "other", f.selection, s.start); !errors.Is(err, ErrGrant) {
		t.Fatal("principal changed", err)
	}
	writer := protocol.LeaseRequest{SessionID: f.snapshot.Sessions[0].ID}
	if _, err := s.Attach(context.Background(), writer); err == nil || f.starts.Load() != 0 {
		t.Fatal("writer contacted exporter", err)
	}
	got, err := s.List(context.Background())
	if err != nil || len(got) != 1 || f.starts.Load() != 1 {
		t.Fatal("exact enrollment not intersected", len(got), err)
	}
	v := got[0]
	if v.ID != f.snapshot.Sessions[0].ID || v.WorkID != f.snapshot.Sessions[0].WorkID || v.ThreadID != f.snapshot.Sessions[0].ThreadID || v.Execution.Runtime != (domain.RuntimeRef{}) || !v.Capabilities.ObserverOnly || !v.Capabilities.NativeAttach || !v.Capabilities.ReadOnlyAttach || v.Capabilities.Stop || v.Capabilities.SemanticPrompt || v.Capabilities.NativeResume {
		t.Fatal("read view changed identity or imported authority", v)
	}
	denied := protocol.LeaseRequest{SessionID: f.snapshot.Sessions[1].ID, ExecutionID: f.snapshot.Sessions[1].Execution.ID, Generation: 1, OperationID: domain.NewID("attach"), ClientID: "viewer", ReadOnly: true}
	if _, err := s.Attach(context.Background(), denied); !errors.Is(err, ErrGrant) {
		t.Fatal("ungranted session reached home", err)
	}
	if _, err := s.List(context.Background()); err != nil || f.closes.Load() != 0 || f.starts.Load() != 1 {
		t.Fatal("one denied session invalidated unrelated view", err)
	}
}

func TestReadSourceEnrollmentRevokeRegrantCannotReviveIncarnation(t *testing.T) {
	f := newReadSourceFixture(t)
	s := f.source(t)
	if _, err := s.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.registry.Revoke(f.config.HostID, f.config.Grant.Principal); err != nil {
		t.Fatal(err)
	}
	if err := f.registry.SetGrant(f.config.Grant); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(context.Background()); !errors.Is(err, ErrConflict) {
		t.Fatal("revision ABA revived old source", err)
	}
	if f.starts.Load() != 1 || f.closes.Load() != 1 {
		t.Fatal("invalidated source reconnected or failed ownership cleanup")
	}
	if _, err := s.List(context.Background()); err == nil || f.starts.Load() != 1 {
		t.Fatal("automatic reconnect after revoke", err)
	}
}

func TestReadSourceQuietTrustRevocationClosesOwnedConnection(t *testing.T) {
	f := newReadSourceFixture(t)
	s := f.source(t)
	if _, err := s.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.config.KnownHostsFile, []byte("untrusted\n"), 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	for f.closes.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("quiet key revocation did not close observer")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if _, err := s.List(context.Background()); !errors.Is(err, ErrHostKey) || f.starts.Load() != 1 {
		t.Fatal("trust loss redialed home", err)
	}
}

func TestReadSourceCanceledBeforeContactDoesNotDamageIncarnation(t *testing.T) {
	f := newReadSourceFixture(t)
	s := f.source(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.List(ctx); err == nil {
		t.Fatal("canceled handshake published inventory")
	}
	if f.starts.Load() != 0 {
		t.Fatal("canceled caller started SSH")
	}
	if _, err := s.List(context.Background()); err != nil || f.starts.Load() != 1 || f.closes.Load() != 0 {
		t.Fatal("pre-contact cancellation damaged healthy route", err)
	}
}

type refusedCleanupBridge struct {
	net.Conn
	refusal error
	closes  *atomic.Int32
}

func (c *refusedCleanupBridge) Close() error { c.closes.Add(1); c.Conn.Close(); return c.refusal }

func TestReadSourceFailedHandshakeKeepsCleanupRefusalAndNeverRedials(t *testing.T) {
	f := newReadSourceFixture(t)
	refusal := errors.New("owned fixture cleanup unconfirmed")
	s, err := newReadSource(f.registry, f.config.HostID, f.config.Grant.Principal, f.selection, func(context.Context, Config, ReadSelection) (io.ReadWriteCloser, error) {
		f.starts.Add(1)
		local, home := net.Pipe()
		home.Close()
		return &refusedCleanupBridge{Conn: local, refusal: refusal, closes: &f.closes}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.List(context.Background()); err == nil {
		t.Fatal("failed Hello published inventory")
	}
	if _, err = s.List(context.Background()); err == nil || f.starts.Load() != 1 {
		t.Fatal("failed Hello reopened connection", err)
	}
	for i := 0; i < 2; i++ {
		if err := s.Close(); !errors.Is(err, refusal) {
			t.Fatal("cleanup refusal lost", err)
		}
	}
}

func TestReadSSHStderrHasBoundAndRetainsNoRawBytes(t *testing.T) {
	var canceled atomic.Int32
	w := &readSSHStderr{cancel: func() { canceled.Add(1) }}
	if n, err := w.Write(make([]byte, 65536)); err != nil || n != 65536 || w.overflow() {
		t.Fatal("wrong exact diagnostic limit", n, err)
	}
	if n, err := w.Write([]byte("private-token")); !errors.Is(err, ErrMalformed) || n != 0 || !w.overflow() || canceled.Load() != 1 {
		t.Fatal("diagnostic overflow did not terminate owned process", n, err)
	}
}

func TestReadSourceEnrollmentPathOrLockLossCannotRecoverAuthority(t *testing.T) {
	for _, mode := range []string{"lock-missing", "lock-replaced", "directory-replaced"} {
		t.Run(mode, func(t *testing.T) {
			f := newReadSourceFixture(t)
			s := f.source(t)
			if _, err := s.List(context.Background()); err != nil {
				t.Fatal(err)
			}
			dir := f.registry.directory.Name()
			lock := filepath.Join(dir, "peers.lock")
			switch mode {
			case "lock-missing", "lock-replaced":
				if err := os.Rename(lock, lock+".held"); err != nil {
					t.Fatal(err)
				}
				if mode == "lock-replaced" {
					if err := os.WriteFile(lock, nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
			case "directory-replaced":
				if err := os.Rename(dir, dir+".held"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.List(context.Background()); !errors.Is(err, ErrConflict) {
				t.Fatal("stale held enrollment remained authoritative", err)
			}
			if f.starts.Load() != 1 || f.closes.Load() != 1 {
				t.Fatal("authority loss reopened or left exporter live")
			}
			if mode == "lock-missing" {
				if _, err := os.Lstat(lock); !os.IsNotExist(err) {
					t.Fatal("read route created missing enrollment lock", err)
				}
			}
		})
	}
}

func TestOpenReadRegistryNeverCreatesMissingState(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if r, err := OpenReadRegistry(dir); err == nil {
		r.Close()
		t.Fatal("empty directory acquired enrollment")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("existing-only opener created state", len(entries), err)
	}
}
