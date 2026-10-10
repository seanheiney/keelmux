package main

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/seanheiney/agent-backplane/internal/protocol"
)

func TestEntryDirectoryFetchesOnceAndManagementStaysLocal(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "bp-cli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	socket := filepath.Join(root, "entry.sock")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(socket, 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.URL.Path != "/v1/sessions" || r.Header.Get("X-Backplane-Protocol") != protocol.Version {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(protocol.Inventory{Version: protocol.Version, Places: []protocol.Place{{HostID: "fixture_home", State: "unreachable"}}})
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
	base := []string{"bp", "--state", filepath.Join(root, "client"), "--entry", socket}
	os.Args = append(append([]string{}, base...), "directory")
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("one user directory query triggered %d upstream requests; each may consume the home deadline", calls.Load())
	}
	data, err := os.ReadFile(output.Name())
	if err != nil || !strings.Contains(string(data), "fixture_home") || !strings.Contains(string(data), "unreachable") {
		t.Fatal("directory did not return execution-home availability")
	}
	for _, command := range []string{"run", "stop", "link", "reconcile"} {
		os.Args = append(append([]string{}, base...), command)
		if err := run(); err == nil || !strings.Contains(err.Error(), "execution home") {
			t.Fatalf("entry management %q was not refused locally: %v", command, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("unsupported management commands contacted execution homes")
	}
}
