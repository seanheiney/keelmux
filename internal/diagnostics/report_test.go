package diagnostics

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/clientstate"
	"github.com/seanheiney/keelmux/internal/config"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
	"github.com/seanheiney/keelmux/internal/store"
)

func diagnosticDir(t *testing.T) config.Paths {
	t.Helper()
	d, e := os.MkdirTemp("/tmp", "km-diag-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	p, e := config.Resolve(d)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(d, "host-id"), []byte(domain.NewID("host")), 0600); e != nil {
		t.Fatal(e)
	}
	return p
}
func check(t *testing.T, r Report, name string) Check {
	t.Helper()
	for _, c := range r.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("missing check %s", name)
	return Check{}
}

func TestPlaceCorruptionAndPrivateContentRedacted(t *testing.T) {
	p := diagnosticDir(t)
	state := clientstate.New()
	raw, _ := json.Marshal(state)
	os.WriteFile(p.Place, raw, 0600)
	r := Collect(context.Background(), p, "", false)
	if check(t, r, "client_place").State != "ok" {
		t.Fatal(r)
	}
	for _, raw := range []string{`{"version":2,"client_id":"private-token","client_id":"replacement"}`, `{"version":2,"client_id":"private-token","extra":"PRIVATE"}`} {
		os.WriteFile(p.Place, []byte(raw), 0600)
		r = Collect(context.Background(), p, "", false)
		if check(t, r, "client_place").State != "invalid" {
			t.Fatal(r)
		}
		b, _ := json.Marshal(r)
		if strings.Contains(string(b), "private-token") || strings.Contains(string(b), "PRIVATE") {
			t.Fatal("leaked place contents")
		}
	}
	os.Remove(p.Place)
	os.Symlink(filepath.Join(p.Dir, "host-id"), p.Place)
	if check(t, Collect(context.Background(), p, "", false), "client_place").State != "invalid" {
		t.Fatal("followed place alias")
	}
}

func TestSavedRuntimeStalenessIsNotPIDAuthority(t *testing.T) {
	boot, start, e := processIdentity(os.Getpid())
	if e != nil {
		t.Fatal(e)
	}
	c := runtimeCheck([]domain.RuntimeRef{{Backend: "native", PID: os.Getpid(), BootID: boot, ProcessStart: start}, {Backend: "native", PID: os.Getpid(), BootID: boot, ProcessStart: start + "1"}, {PID: -1}})
	if c.State != "stale" || c.Counts["matching"] != 1 || c.Counts["stale"] != 1 || c.Counts["unavailable"] != 1 {
		t.Fatalf("%+v", c)
	}
	now := time.Now()
	if freshness(time.Time{}, now) != "unknown" || freshness(now.Add(-6*time.Minute), now) != "stale" || freshness(now.Add(2*time.Second), now) != "invalid_future" {
		t.Fatal("freshness boundary")
	}
}

func TestOfflineRegistryRuntimeProjectionRedactsPrivateMetadata(t *testing.T) {
	p := diagnosticDir(t)
	db, e := store.Open(p.DB)
	if e != nil {
		t.Fatal(e)
	}
	boot, start, e := processIdentity(os.Getpid())
	if e != nil {
		t.Fatal(e)
	}
	s := domain.NewSession(domain.NewID("host"), "private-session-name", "/private-workspace", "codex", "private-version")
	s.Execution.Runtime = domain.RuntimeRef{Backend: "native", PID: os.Getpid(), BootID: boot, ProcessStart: start + "1"}
	s.Execution.Account = domain.Evidence{Value: "private-account", Source: "fixture", ObservedAt: time.Now()}
	_, _, e = db.Record(context.Background(), domain.Operation{ID: domain.NewID("op"), Kind: "launch", SessionID: s.ID, ExecutionID: s.Execution.ID}, []byte("private-native-input"), &s)
	if e != nil {
		t.Fatal(e)
	}
	db.Close()
	before, _ := os.ReadFile(p.DB)
	r := Collect(context.Background(), p, "", false)
	after, _ := os.ReadFile(p.DB)
	if string(before) != string(after) || check(t, r, "registry").State != "ok" || check(t, r, "saved_native_runtime").State != "stale" {
		t.Fatal(r)
	}
	raw, _ := json.Marshal(r)
	for _, secret := range []string{"private-session-name", "private-workspace", "private-version", "private-account", "private-native-input"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("private registry data escaped", secret)
		}
	}
}

func TestExistingDaemonIdentityAndReadOnlyQueries(t *testing.T) {
	for _, mode := range []string{"valid", "wrong_pid", "changed_instance", "oversized", "protocol", "duplicate_identity", "wrong_inventory_host", "socket_alias", "bad_host", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			p := diagnosticDir(t)
			l, e := net.Listen("unix", p.Control)
			if e != nil {
				t.Fatal(e)
			}
			os.Chmod(p.Control, 0600)
			var mu sync.Mutex
			var routes []string
			instance := string(domain.NewID("daemon"))
			daemonReads := 0
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				routes = append(routes, r.Method+" "+r.URL.Path)
				daemonReadsBefore := daemonReads
				if r.URL.Path == "/v1/daemon" {
					daemonReads++
				}
				mu.Unlock()
				w.Header().Set("X-Backplane-Protocol", protocol.Version)
				if r.Method != "GET" {
					t.Error("mutation requested")
					http.Error(w, "private", 400)
					return
				}
				if mode == "cancel" {
					<-r.Context().Done()
					return
				}
				if mode == "oversized" {
					w.Write([]byte(strings.Repeat("PRIVATE", 20000)))
					return
				}
				if mode == "protocol" {
					w.Header().Set("X-Backplane-Protocol", "wrong")
				}
				if r.URL.Path == "/v1/daemon" {
					s := protocol.DaemonStatus{Instance: instance, PID: os.Getpid(), Protocol: protocol.Version}
					if mode == "duplicate_identity" {
						raw, _ := json.Marshal(s)
						w.Write([]byte(strings.Replace(string(raw), `"instance":`, `"instance":"`+instance+`","instance":`, 1)))
						return
					}
					if mode == "wrong_pid" {
						s.PID++
					}
					if mode == "changed_instance" && daemonReadsBefore > 0 {
						s.Instance = string(domain.NewID("daemon"))
					}
					json.NewEncoder(w).Encode(s)
					return
				}
				if r.URL.Path == "/v1/channels" {
					old := time.Now().Add(-time.Hour)
					json.NewEncoder(w).Encode([]protocol.ChannelStatus{{Endpoint: "private-endpoint", SelfID: "private-self", Allowlist: []string{"private-user"}, Active: true, LastCheck: &old, HistoryGap: true}})
					return
				}
				if r.URL.Path == "/v1/sessions" {
					host, _ := os.ReadFile(filepath.Join(p.Dir, "host-id"))
					s := domain.NewSession(domain.ID(host), "private-session-name", "/private-workspace", "codex", "unknown")
					if mode == "wrong_inventory_host" {
						s.HostID = domain.NewID("host")
					}
					json.NewEncoder(w).Encode(protocol.Inventory{Version: protocol.Version, Sessions: []domain.Session{s}})
					return
				}
				t.Errorf("unexpected route %s", r.URL.Path)
			})
			srv := &http.Server{Handler: h}
			done := make(chan struct{})
			go func() { srv.Serve(l); close(done) }()
			defer func() { srv.Close(); <-done }()
			if mode == "socket_alias" {
				os.Rename(p.Control, p.Control+".real")
				os.Symlink(p.Control+".real", p.Control)
			}
			if mode == "bad_host" {
				os.WriteFile(filepath.Join(p.Dir, "host-id"), []byte("invalid-private"), 0600)
			}
			ctx := context.Background()
			if mode == "cancel" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 30*time.Millisecond)
				defer cancel()
			}
			r := Collect(ctx, p, "", false)
			b, _ := json.Marshal(r)
			for _, secret := range []string{"private-endpoint", "private-self", "private-user", "PRIVATE", "invalid-private", "private-session-name", "private-workspace"} {
				if strings.Contains(string(b), secret) {
					t.Fatal("leaked response")
				}
			}
			mu.Lock()
			got := append([]string{}, routes...)
			mu.Unlock()
			if mode == "valid" || mode == "wrong_inventory_host" {
				if r.Daemon == nil || r.Daemon.Instance != instance || r.Daemon.PID != os.Getpid() || len(got) != 4 {
					t.Fatal(string(b), got)
				}
				if check(t, r, "live_channels").State != "needs_attention" {
					t.Fatal(string(b))
				}
				if mode == "wrong_inventory_host" && check(t, r, "live_registry").State != "unavailable" {
					t.Fatal("accepted foreign host projection")
				}
			} else {
				if r.Daemon != nil {
					t.Fatal("accepted mismatched authority", string(b))
				}
				if (mode == "wrong_pid" || mode == "protocol" || mode == "oversized" || mode == "duplicate_identity") && len(got) != 1 {
					t.Fatal("queried beyond unverified lifecycle", got)
				}
				if (mode == "socket_alias" || mode == "bad_host") && len(got) != 0 {
					t.Fatal("contacted unsafe state", got)
				}
			}
		})
	}
}

func TestRemoteEntryNeverConnects(t *testing.T) {
	p := diagnosticDir(t)
	l, e := net.Listen("unix", p.Control)
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	os.Chmod(p.Control, 0600)
	r := Collect(context.Background(), p, p.Control, false)
	if r.Daemon != nil || check(t, r, "entry_socket").State != "socket_present" {
		t.Fatal(r)
	}
	l.(*net.UnixListener).SetDeadline(time.Now().Add(20 * time.Millisecond))
	if c, e := l.Accept(); e == nil {
		c.Close()
		t.Fatal("connected to entry")
	}
}

func TestVersionProbeIsExactBoundedAndOptIn(t *testing.T) {
	d := t.TempDir()
	marker := filepath.Join(d, "args")
	for _, name := range []string{"codex", "claude"} {
		script := "#!/bin/sh\nprintf '%s' \"$*\" > '" + marker + "'\nif [ \"$#\" != 1 ] || [ \"$1\" != --version ]; then exit 9; fi\nprintf 'codex-cli 0.159.0\\n'\n"
		if name == "claude" {
			script = strings.ReplaceAll(script, "codex-cli 0.159.0", "2.1.0 (Claude Code)")
		}
		os.WriteFile(filepath.Join(d, name), []byte(script), 0700)
	}
	t.Setenv("PATH", d)
	static := providerChecks(context.Background(), false)
	if static[0].State != "available" {
		t.Fatal(static)
	}
	if _, e := os.Stat(marker); !os.IsNotExist(e) {
		t.Fatal("default probe executed")
	}
	probed := providerChecks(context.Background(), true)
	if probed[0].State != "observed" || probed[1].State != "observed" {
		t.Fatal(probed)
	}
	args, _ := os.ReadFile(marker)
	if string(args) != "--version" {
		t.Fatal(string(args))
	}
	os.WriteFile(filepath.Join(d, "codex"), []byte("#!/bin/sh\nprintf 'PRIVATE TOKEN\\n'\n"), 0700)
	probed = providerChecks(context.Background(), true)
	raw, _ := json.Marshal(probed)
	if probed[0].State != "unavailable" || strings.Contains(string(raw), "PRIVATE") {
		t.Fatal(string(raw))
	}
	os.WriteFile(filepath.Join(d, "codex"), []byte("#!/bin/sh\nwhile :; do :; done\n"), 0700)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	at := time.Now()
	if _, e := probeVersion(ctx, filepath.Join(d, "codex"), "codex"); e == nil || time.Since(at) > time.Second {
		t.Fatal("unbounded probe", e)
	}
}
