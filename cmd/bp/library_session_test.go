package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/library/adoption"
	"github.com/seanheiney/keelmux/internal/protocol"
)

func TestLibrarySessionStatusDoesNotCreateMissingState(t *testing.T) {
	root := libraryCLIDir(t)
	state := filepath.Join(root, "absent")
	t.Setenv("PATH", root)
	s := domain.NewSession(domain.NewID("host"), "read only", "/unused", "codex", "test")
	var out, diagnostics bytes.Buffer
	err := manageLibrary(context.Background(), state, "", "plugins", []string{"session-status", string(s.ID)}, &diagnostics, &out)
	if err == nil || out.Len() != 0 || !strings.Contains(err.Error(), "dial unix") {
		t.Fatalf("missing daemon must refuse without output: %v %q", err, out.String())
	}
	if _, err := os.Lstat(state); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read created state: %v", err)
	}
}

func TestLibrarySessionStatusMainEntryUsesExistingSocketWithoutLocalAuthority(t *testing.T) {
	root := libraryCLIDir(t)
	state := filepath.Join(root, "client")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	host := domain.NewID("host")
	if err := os.WriteFile(filepath.Join(state, "host-id"), []byte(host), 0600); err != nil {
		t.Fatal(err)
	}
	s := domain.NewSession(host, "entry fixture", "/must-remain-unread-remote", "gemini", "fixture")
	s.Library = &domain.LibrarySnapshot{ProfileID: "review", ProfileRevision: 7, ResolutionDigest: strings.Repeat("a", 64), ApplicationID: "work", ApplicationDigest: strings.Repeat("b", 64), Source: "client-declaration", Loaded: "unknown"}
	socket := filepath.Join(root, "entry.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(socket, 0600); err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/v1/sessions" || r.Header.Get("X-Backplane-Protocol") != protocol.Version {
			t.Error("unexpected entry request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(protocol.Inventory{Version: protocol.Version, Sessions: []domain.Session{s}})
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	output, err := os.CreateTemp(root, "output-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = output.Close() })
	oldArgs, oldOut := os.Args, os.Stdout
	t.Cleanup(func() { os.Args, os.Stdout = oldArgs, oldOut })
	os.Args = []string{"keelmux", "--state", state, "--entry", socket, "plugins", "session-status", string(s.ID)}
	os.Stdout = output
	t.Setenv("PATH", root)
	if err := run(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	var report adoption.Report
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 || report.State != adoption.Unavailable || report.Reason != "local_host_unproven" || report.Saved != nil || report.Applied != nil || report.Launch == nil || *report.Launch != *s.Library {
		t.Fatalf("entry acquired local evidence: requests=%d report=%+v", requests.Load(), report)
	}
	entries, err := os.ReadDir(state)
	if err != nil || len(entries) != 1 || entries[0].Name() != "host-id" {
		t.Fatal("read-only main route created local state", entries, err)
	}
}

func librarySessionClient(t *testing.T, inventory func(int) []domain.Session) (*protocol.Client, *int) {
	t.Helper()
	requests := new(int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/sessions" {
			t.Errorf("unexpected capability effect %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		(*requests)++
		_ = json.NewEncoder(w).Encode(protocol.Inventory{Version: protocol.Version, Sessions: inventory(*requests)})
	}))
	t.Cleanup(server.Close)
	return &protocol.Client{HTTP: server.Client(), BaseURL: server.URL}, requests
}

func TestInspectLibrarySessionRejectsChangedExecution(t *testing.T) {
	s := domain.NewSession(domain.NewID("host"), "read only", "/unused", "codex", "test")
	s.Execution.State = "running"
	c, requests := librarySessionClient(t, func(n int) []domain.Session {
		v := s
		if n == 2 {
			v.Execution.Generation++
		}
		return []domain.Session{v}
	})
	_, err := inspectLibrarySession(context.Background(), c, adoption.Request{}, s.ID)
	if !errors.Is(err, errLibrarySessionChanged) || *requests != 2 {
		t.Fatalf("stale execution was accepted: %v requests=%d", err, *requests)
	}
}

func TestInspectLibrarySessionEntryNeverAssumesLocalWorkspace(t *testing.T) {
	s := domain.NewSession(domain.NewID("host"), "remote", "/unavailable/remote/project", "grok", "reviewed")
	s.Execution.State = "running"
	s.Library = &domain.LibrarySnapshot{ProfileID: "review", ProfileRevision: 7, ResolutionDigest: strings.Repeat("a", 64), ApplicationID: "work", ApplicationDigest: strings.Repeat("b", 64), Source: "client-declaration", Loaded: "unknown"}
	c, requests := librarySessionClient(t, func(int) []domain.Session { return []domain.Session{s} })
	state := filepath.Join(t.TempDir(), "absent")
	r, err := inspectLibrarySession(context.Background(), c, adoption.Request{StateDir: state}, s.ID)
	if err != nil || *requests != 2 || r.State != "unavailable" || r.NativeLoaded != "unknown" || r.Permissions != "unknown" || r.Saved != nil || r.Applied != nil || r.Launch == nil || *r.Launch != *s.Library {
		t.Fatalf("remote launch declaration expanded into local evidence: %+v %v requests=%d", r, err, *requests)
	}
	if _, err := os.Lstat(state); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remote read created local state: %v", err)
	}
}

func TestExactLibrarySessionRejectsAmbiguousIdentity(t *testing.T) {
	s := domain.NewSession(domain.NewID("host"), "ambiguous", "/unused", "codex", "test")
	c, requests := librarySessionClient(t, func(int) []domain.Session { return []domain.Session{s, s} })
	if _, err := exactLibrarySession(context.Background(), c, s.ID); err == nil || *requests != 1 {
		t.Fatalf("duplicate session identity accepted: %v requests=%d", err, *requests)
	}
}

func TestInspectLibrarySessionRejectsChangedEligibility(t *testing.T) {
	for _, field := range []string{"mode", "linked", "observer"} {
		t.Run(field, func(t *testing.T) {
			s := domain.NewSession(domain.NewID("host"), "read only", "/unused", "codex", "test")
			c, requests := librarySessionClient(t, func(n int) []domain.Session {
				v := s
				if n == 2 {
					switch field {
					case "mode":
						v.Mode = domain.Observed
					case "linked":
						v.Linked = true
					case "observer":
						v.Capabilities.ObserverOnly = true
					}
				}
				return []domain.Session{v}
			})
			if _, err := inspectLibrarySession(context.Background(), c, adoption.Request{}, s.ID); !errors.Is(err, errLibrarySessionChanged) || *requests != 2 {
				t.Fatalf("changed eligibility accepted: %v requests=%d", err, *requests)
			}
		})
	}
}

func TestLibrarySessionInventoryRejectsUnboundedOrMalformedResponse(t *testing.T) {
	valid, err := json.Marshal(protocol.Inventory{Version: protocol.Version})
	if err != nil {
		t.Fatal(err)
	}
	oversizedCount, err := json.Marshal(protocol.Inventory{Version: protocol.Version, Sessions: make([]domain.Session, librarySessionInventoryCount+1)})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"bytes", http.StatusOK, string(valid) + strings.Repeat(" ", librarySessionInventoryBytes), "byte limit"},
		{"count", http.StatusOK, string(oversizedCount), "session limit"},
		{"trailing", http.StatusOK, string(valid) + "{}", "trailing data"},
		{"malformed", http.StatusOK, "{secret-sentinel", "invalid"},
		{"protocol", http.StatusOK, `{"version":"other","sessions":[]}`, "protocol differs"},
		{"status", http.StatusForbidden, "secret-sentinel", "unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/v1/sessions" || r.Header.Get("X-Backplane-Protocol") != protocol.Version {
					t.Error("unexpected inventory request")
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			c := &protocol.Client{HTTP: server.Client(), BaseURL: server.URL}
			if _, err := librarySessionInventory(context.Background(), c); err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "secret-sentinel") {
				t.Fatalf("response accepted or leaked: %v", err)
			}
		})
	}
}
