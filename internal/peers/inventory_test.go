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
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/federation"
	"github.com/seanheiney/keelmux/internal/protocol"
)

type inventoryFunc func(context.Context) ([]domain.Session, error)

func (f inventoryFunc) List(ctx context.Context) ([]domain.Session, error) { return f(ctx) }

func fixture(t *testing.T) (Config, Snapshot) {
	t.Helper()
	host := domain.NewID("host")
	a := domain.NewSession(host, "permitted", "/private/permitted", "fixture", "test")
	a.Execution.Runtime = domain.RuntimeRef{Backend: "native", Socket: "/private/native.sock", PID: 42}
	b := domain.NewSession(host, "private-hidden", "/private/hidden", "fixture", "test")
	c := Config{HostID: host, Alias: "owned-fixture", KnownHostsFile: filepath.Join(t.TempDir(), "known_hosts"), Fingerprint: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", RemoteExecutable: "/private/bin/keelmux", RemoteSocket: "/private/control.sock", Grant: federation.Grant{Principal: "local", HostID: host, Sessions: []domain.ID{a.ID}}, FreshForSeconds: 10, RetainForSeconds: 100}
	return c, Snapshot{Version: Version, HostID: host, CapturedAt: time.Now().UTC(), Sessions: []domain.Session{a, b}}
}

func ownedSocket(t *testing.T, handler http.Handler) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "km-peer-")
	if err != nil {
		t.Fatal(err)
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "home.sock")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(socket, 0600); err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return socket
}

func exportInput(t *testing.T, c Config) []byte {
	t.Helper()
	b, err := json.Marshal(ExportRequest{Version: Version, Socket: c.RemoteSocket, Grant: c.Grant})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestExportIdentityGrantAndWorkspaceWithoutEffects(t *testing.T) {
	c, s := fixture(t)
	calls := 0
	c.RemoteSocket = ownedSocket(t, HomeHandler(c.HostID, inventoryFunc(func(context.Context) ([]domain.Session, error) { calls++; return s.Sessions, nil })))
	var out bytes.Buffer
	if err := Export(context.Background(), bytes.NewReader(exportInput(t, c)), &out); err != nil {
		t.Fatal(err)
	}
	var got Snapshot
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || got.HostID != c.HostID || len(got.Sessions) != 1 || got.Sessions[0].ID != s.Sessions[0].ID || got.Sessions[0].WorkID != s.Sessions[0].WorkID || got.Sessions[0].ThreadID != s.Sessions[0].ThreadID {
		t.Fatal("stable identity/grant lost", got)
	}
	if strings.Contains(out.String(), "private-hidden") || strings.Contains(out.String(), "/private/hidden") || strings.Contains(out.String(), "native.sock") {
		t.Fatal("unpermitted metadata or runtime locator exported")
	}
	if got.Sessions[0].Capabilities != scrub(s.Sessions[0]).Capabilities || got.Sessions[0].Execution.Runtime != (domain.RuntimeRef{}) {
		t.Fatal("discovery grants action authority")
	}
	w := workspaces(got.Sessions)
	if len(w) != 1 || w[0].Path != s.Sessions[0].Workspace || w[0].State != "recorded" {
		t.Fatal("workspace falsely probed or lost", w)
	}
}

func TestExportEmptyInventoryStillPinsHostAndZeroGrantHidesMetadata(t *testing.T) {
	for _, empty := range []bool{false, true} {
		c, s := fixture(t)
		if empty {
			s.Sessions = nil
		}
		c.Grant.Sessions = nil
		c.RemoteSocket = ownedSocket(t, HomeHandler(c.HostID, inventoryFunc(func(context.Context) ([]domain.Session, error) { return s.Sessions, nil })))
		var out bytes.Buffer
		if err := Export(context.Background(), bytes.NewReader(exportInput(t, c)), &out); err != nil {
			t.Fatal(err)
		}
		var got Snapshot
		_ = json.Unmarshal(out.Bytes(), &got)
		if got.HostID != c.HostID || len(got.Sessions) != 0 || got.CapturedAt.IsZero() {
			t.Fatal("empty home identity not established", got)
		}
		c.Grant.HostID = domain.NewID("host")
		out.Reset()
		if err := Export(context.Background(), bytes.NewReader(exportInput(t, c)), &out); !errors.Is(err, ErrIdentity) || out.Len() != 0 {
			t.Fatal("wrong empty host accepted", err)
		}
	}
}

func TestExportAbsentSocketCreatesNoState(t *testing.T) {
	c, _ := fixture(t)
	dir, err := os.MkdirTemp("", "km-absent-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	c.RemoteSocket = filepath.Join(dir, "never-created", "home.sock")
	var out bytes.Buffer
	if err := Export(context.Background(), bytes.NewReader(exportInput(t, c)), &out); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Dir(c.RemoteSocket)); !os.IsNotExist(err) {
		t.Fatal("export created home state", err)
	}
	if out.Len() != 0 {
		t.Fatal("failure emitted success envelope")
	}
}

func TestExportRejectsMalformedRequestBeforeSocket(t *testing.T) {
	c, _ := fixture(t)
	b := string(exportInput(t, c))
	cases := []string{b + "{}", b[:len(b)-1] + `,"unknown":true}`, `{"version":"x","version":"y"}`, strings.Repeat(" ", RequestLimit+1), b[:len(b)-1]}
	for _, in := range cases {
		if err := Export(context.Background(), strings.NewReader(in), io.Discard); !errors.Is(err, ErrMalformed) {
			t.Fatalf("malformed request accepted: %v", err)
		}
	}
}

func TestExportRejectsRedirectMalformedForeignAndDuplicateInventory(t *testing.T) {
	c, s := fixture(t)
	for _, mode := range []string{"redirect", "foreign", "transitive", "duplicate", "unknown", "trailing", "overflow", "protocol"} {
		t.Run(mode, func(t *testing.T) {
			c.RemoteSocket = ownedSocket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != InventoryPath {
					t.Error("export performed another operation")
				}
				w.Header().Set("X-Backplane-Protocol", protocol.Version)
				copy := s
				switch mode {
				case "redirect":
					w.Header().Set("Location", "/v1/reconcile")
					w.WriteHeader(302)
					return
				case "foreign":
					copy.HostID = domain.NewID("host")
				case "transitive":
					copy.Sessions = append([]domain.Session{}, s.Sessions...)
					copy.Sessions[1].HostID = domain.NewID("host")
				case "duplicate":
					copy.Sessions = []domain.Session{s.Sessions[0], s.Sessions[0]}
				case "protocol":
					w.Header().Set("X-Backplane-Protocol", "future/1")
				case "overflow":
					_, _ = io.WriteString(w, strings.Repeat(" ", SnapshotLimit+1))
					return
				}
				b, _ := json.Marshal(copy)
				if mode == "unknown" {
					b = append(b[:len(b)-1], []byte(`,"future":true}`)...)
				}
				_, _ = w.Write(b)
				if mode == "trailing" {
					_, _ = io.WriteString(w, "{}")
				}
			}))
			var out bytes.Buffer
			if err := Export(context.Background(), bytes.NewReader(exportInput(t, c)), &out); err == nil || out.Len() != 0 {
				t.Fatal("untrusted remote envelope exported", err)
			}
		})
	}
}

func TestExportCancelsSilentHome(t *testing.T) {
	c, _ := fixture(t)
	ended := make(chan struct{})
	c.RemoteSocket = ownedSocket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done(); close(ended) }))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := Export(ctx, bytes.NewReader(exportInput(t, c)), io.Discard); !errors.Is(err, ErrTimeout) {
		t.Fatal(err)
	}
	select {
	case <-ended:
	case <-time.After(time.Second):
		t.Fatal("home read not cancelled")
	}
}

func TestExportRequiresExistingPrivateCanonicalEndpointWithoutRepair(t *testing.T) {
	for _, mode := range []string{"public-socket", "public-parent", "socket-alias", "parent-alias"} {
		t.Run(mode, func(t *testing.T) {
			c, s := fixture(t)
			c.RemoteSocket = ownedSocket(t, HomeHandler(c.HostID, inventoryFunc(func(context.Context) ([]domain.Session, error) {
				t.Error("unsafe endpoint was contacted")
				return s.Sessions, nil
			})))
			original := c.RemoteSocket
			switch mode {
			case "public-socket":
				if err := os.Chmod(original, 0666); err != nil {
					t.Fatal(err)
				}
			case "public-parent":
				if err := os.Chmod(filepath.Dir(original), 0755); err != nil {
					t.Fatal(err)
				}
			case "socket-alias":
				c.RemoteSocket = filepath.Join(filepath.Dir(original), "alias.sock")
				if err := os.Symlink(original, c.RemoteSocket); err != nil {
					t.Fatal(err)
				}
			case "parent-alias":
				alias := filepath.Dir(original) + "-alias"
				if err := os.Symlink(filepath.Dir(original), alias); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Remove(alias) })
				c.RemoteSocket = filepath.Join(alias, "home.sock")
			}
			var out bytes.Buffer
			if err := Export(context.Background(), bytes.NewReader(exportInput(t, c)), &out); !errors.Is(err, ErrUnavailable) || out.Len() != 0 {
				t.Fatal("unsafe endpoint accepted", err)
			}
			if mode == "public-socket" {
				info, _ := os.Lstat(original)
				if info.Mode().Perm() != 0666 {
					t.Fatal("export repaired socket permissions")
				}
			}
			if mode == "public-parent" {
				info, _ := os.Lstat(filepath.Dir(original))
				if info.Mode().Perm() != 0755 {
					t.Fatal("export repaired parent permissions")
				}
			}
		})
	}
}

func TestExportPinsSocketAndParentIdentityAcrossRead(t *testing.T) {
	for _, mode := range []string{"socket", "parent"} {
		t.Run(mode, func(t *testing.T) {
			c, s := fixture(t)
			var socket string
			socket = ownedSocket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "socket" {
					if err := os.Rename(socket, socket+"-held"); err != nil {
						t.Error(err)
						w.WriteHeader(500)
						return
					}
				} else {
					parent := filepath.Dir(socket)
					if err := os.Rename(parent, parent+"-held"); err != nil {
						t.Error(err)
						w.WriteHeader(500)
						return
					}
					t.Cleanup(func() { _ = os.RemoveAll(parent + "-held") })
					if err := os.Mkdir(parent, 0700); err != nil {
						t.Error(err)
						w.WriteHeader(500)
						return
					}
				}
				l, err := net.Listen("unix", socket)
				if err != nil {
					t.Error(err)
					w.WriteHeader(500)
					return
				}
				t.Cleanup(func() { _ = l.Close() })
				if err := os.Chmod(socket, 0600); err != nil {
					t.Error(err)
					w.WriteHeader(500)
					return
				}
				w.Header().Set("X-Backplane-Protocol", protocol.Version)
				_ = json.NewEncoder(w).Encode(s)
			}))
			c.RemoteSocket = socket
			var out bytes.Buffer
			if err := Export(context.Background(), bytes.NewReader(exportInput(t, c)), &out); !errors.Is(err, ErrUnavailable) || out.Len() != 0 {
				t.Fatal("replaced endpoint supplied authorized inventory", mode, err)
			}
		})
	}
}

type foreignFileInfo struct {
	os.FileInfo
	stat syscall.Stat_t
}

func (f foreignFileInfo) Sys() any { return &f.stat }

func TestHomeEndpointRejectsForeignOwnership(t *testing.T) {
	socket := ownedSocket(t, http.NotFoundHandler())
	for _, parent := range []bool{false, true} {
		path := socket
		if parent {
			path = filepath.Dir(socket)
		}
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !privateHomeInfo(info, parent) {
			t.Fatal("owned private fixture rejected")
		}
		foreign := foreignFileInfo{FileInfo: info, stat: *info.Sys().(*syscall.Stat_t)}
		foreign.stat.Uid = uint32(os.Geteuid()) + 1
		if privateHomeInfo(foreign, parent) {
			t.Fatal("foreign endpoint ownership accepted")
		}
	}
}
