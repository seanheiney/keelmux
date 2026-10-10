package wire

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func endpoint(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "bp-wire-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "rpc.sock")
}

func serve(t *testing.T, handler http.Handler) string {
	t.Helper()
	path := endpoint(t)
	l, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	s := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	finished := make(chan struct{})
	go func() { defer close(finished); _ = s.Serve(l) }()
	t.Cleanup(func() { _ = s.Close(); <-finished })
	return path
}

func TestUnixFramesAndOwnedEndpoint(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	path := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := Accept(w, r)
		if err != nil {
			t.Error(err)
			return
		}
		defer c.Close()
		for i := 0; i < 2; i++ {
			frame, err := c.Read(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			if err = c.Write(ctx, frame); err != nil {
				t.Error(err)
				return
			}
		}
	}))
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("socket permissions: %v %v", info, err)
	}
	c, err := Dial(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, frame := range [][]byte{[]byte(`{"id":1,"method":"thread/read","params":{"threadId":"owned"}}`), []byte(`{"id":"native-id","result":{"text":"café λ"}}`)} {
		if err := c.Write(ctx, frame); err != nil {
			t.Fatal(err)
		}
		got, err := c.Read(ctx)
		if err != nil || !bytes.Equal(got, frame) {
			t.Fatalf("frame boundary/bytes changed: %q %v", got, err)
		}
	}
	if _, err := Listen(path); err == nil {
		t.Fatal("existing endpoint was replaced")
	}
	if err := os.Chmod(path, 0660); err != nil {
		t.Fatal(err)
	}
	if _, err := Dial(ctx, path); err == nil {
		t.Fatal("nonprivate upstream accepted")
	}
}

func TestCloseReclaimsBlockedReader(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverDone := make(chan struct{})
	path := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(serverDone)
		c, err := Accept(w, r)
		if err != nil {
			t.Error(err)
			return
		}
		defer c.Close()
		_, _ = c.Read(ctx)
	}))
	c, err := Dial(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	readDone := make(chan error, 1)
	go func() { _, err := c.Read(context.Background()); readDone <- err }()
	start := time.Now()
	_ = c.Close()
	select {
	case err := <-readDone:
		if err == nil {
			t.Fatal("closed reader unexpectedly succeeded")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("close left reader blocked")
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("close exceeded bound")
	}
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("upstream still blocked after close")
	}
}

func TestFrameLimitAndBinaryMessages(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	path := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := Accept(w, r)
		if err != nil {
			t.Error(err)
			return
		}
		defer c.Close()
		_ = c.conn.Write(ctx, websocket.MessageBinary, []byte("unsupported"))
		_, _ = c.Read(ctx)
	}))
	c, err := Dial(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Write(ctx, make([]byte, MaxFrameBytes+1)); err == nil {
		t.Fatal("oversize outbound frame accepted")
	}
	if _, err := c.Read(ctx); err == nil {
		t.Fatal("binary native message accepted")
	}
}

func TestPrivateListenerAndDialNeverUseTCP(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
	r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, &net.TCPAddr{}))
	if _, err := Accept(w, r); err == nil || w.Code != http.StatusForbidden {
		t.Fatal("TCP listener accepted")
	}
	if _, err := Listen("relative.sock"); err == nil {
		t.Fatal("relative endpoint accepted")
	}
	path := endpoint(t)
	if err := os.WriteFile(path, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(path); err == nil {
		t.Fatal("existing file replaced")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "preserve" {
		t.Fatal("existing file changed")
	}
	if _, err := Dial(context.Background(), path); err == nil {
		t.Fatal("regular file used as upstream")
	}
}
