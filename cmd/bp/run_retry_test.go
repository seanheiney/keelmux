package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/seanheiney/keelmux/internal/config"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
)

func TestRunOperationOnlyRetryIgnoresCurrentLibraryAndProvider(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "km-retry-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	p, err := config.Resolve(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	// An unreadable current profile and no provider binaries must not prevent
	// selection of the home-recorded original intent.
	libraryRoot := filepath.Join(p.Dir, "library")
	if err := os.Mkdir(libraryRoot, 0700); err != nil {
		t.Fatal(err)
	}
	invalidCatalog := []byte("not a current profile")
	if err := os.WriteFile(filepath.Join(libraryRoot, "catalog.json"), invalidCatalog, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root)
	l, err := net.Listen("unix", p.Control)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	var posts atomic.Int32
	bodies := make(chan string, 2)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Backplane-Protocol") != protocol.Version {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/v1/sessions" {
			_ = json.NewEncoder(w).Encode(protocol.Inventory{Version: protocol.Version})
			return
		}
		posts.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		bodies <- r.URL.Path + " " + string(body)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/launch/retry" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if strings.Contains(string(body), "missing") {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(protocol.Error{Message: "original operation absent"})
			return
		}
		_ = json.NewEncoder(w).Encode(protocol.LaunchResponse{
			Operation: domain.Operation{ID: "original", Outcome: domain.Completed},
			Session:   domain.Session{ID: "original-session"},
		})
	})}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	args, stdout := os.Args, os.Stdout
	t.Cleanup(func() { os.Args, os.Stdout = args, stdout })
	output, err := os.CreateTemp(root, "output-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = output.Close() })
	os.Stdout = output
	base := []string{"keelmux", "--state", p.Dir, "run", "--operation", "original"}
	os.Args = base
	if err := run(); err != nil {
		t.Fatal("original retry inspected current profile or native binary", err)
	}
	if got := <-bodies; got != "/v1/launch/retry {\"operation_id\":\"original\"}\n" {
		t.Fatal("CLI rebuilt original launch arguments", got)
	}
	for _, flags := range [][]string{{"--profile", "default"}, {"--application", "work"}, {"--workspace", root}, {"--name", "new name"}} {
		os.Args = append(append([]string{}, base...), flags...)
		if err := run(); err == nil || !strings.Contains(err.Error(), "accepts only --operation") {
			t.Fatal("retry accepted new launch selections", flags, err)
		}
	}
	if posts.Load() != 1 {
		t.Fatal("new selection refusal contacted mutation endpoint", posts.Load())
	}
	os.Args = []string{"keelmux", "--state", p.Dir, "run", "--operation", "missing"}
	if err := run(); err == nil || !strings.Contains(err.Error(), "original operation absent") {
		t.Fatal("missing original retry did not propagate refusal", err)
	}
	if got := <-bodies; got != "/v1/launch/retry {\"operation_id\":\"missing\"}\n" || posts.Load() != 2 {
		t.Fatal("missing retry used another route or replayed", got, posts.Load())
	}
	data, err := os.ReadFile(output.Name())
	if err != nil || !strings.Contains(string(data), "original-session") {
		t.Fatal("CLI did not return original response", err)
	}
	catalog, err := os.ReadFile(filepath.Join(libraryRoot, "catalog.json"))
	if err != nil || string(catalog) != string(invalidCatalog) {
		t.Fatal("retry changed current profile", err)
	}
}
