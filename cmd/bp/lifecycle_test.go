package main

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/seanheiney/keelmux/internal/config"
	"github.com/seanheiney/keelmux/internal/protocol"
)

func TestDaemonReadOnlyAndStaleIntentPreserveState(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "km-life-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	p, err := config.Resolve(filepath.Join(root, "absent"))
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"status"}, {"stop"}} {
		if err := manageDaemon(p, args); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(p.Dir); !os.IsNotExist(err) {
			t.Fatal("absent daemon inspection/stop created state")
		}
	}
	for _, args := range [][]string{{"status", "--instance", "old"}, {"restart", "extra"}, {"purge"}} {
		if err := manageDaemon(p, args); err == nil {
			t.Fatal("invalid lifecycle arguments accepted")
		}
		if _, err := os.Lstat(p.Dir); !os.IsNotExist(err) {
			t.Fatal("invalid arguments created state")
		}
	}
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", p.Control)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	var posts atomic.Int32
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			posts.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_ = json.NewEncoder(w).Encode(protocol.DaemonStatus{Instance: "daemon_current", PID: 123, Protocol: protocol.Version})
	})}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	for _, command := range []string{"stop", "restart"} {
		if err := manageDaemon(p, []string{command, "--instance", "daemon_old"}); err == nil {
			t.Fatal("stale intent was accepted")
		}
	}
	if posts.Load() != 0 {
		t.Fatal("stale stop/restart contacted mutation endpoint")
	}
	if err := manageDaemon(p, []string{"status"}); err != nil {
		t.Fatal(err)
	}
	if posts.Load() != 0 {
		t.Fatal("status changed authority")
	}
}
