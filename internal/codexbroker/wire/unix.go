// Package wire carries bounded native JSON-RPC frames over owned Unix sockets.
// It provides transport only; the broker must authorize every RPC and callback.
package wire

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"

	"github.com/coder/websocket"
	"github.com/seanheiney/agent-backplane/internal/config"
)

const MaxFrameBytes = 1 << 20

// Conn preserves one text frame per native message. Closing is immediate;
// callers own joining their readers and keeping unresolved effects pending.
type Conn struct {
	conn      *websocket.Conn
	transport *http.Transport
}

// Listen creates a fresh owned endpoint and never replaces an existing path.
// Its enclosing authority must own the state lock before starting a listener.
func Listen(path string) (net.Listener, error) {
	if err := validatePath(path); err != nil {
		return nil, err
	}
	if err := config.EnsurePrivateDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); err == nil {
		return nil, errors.New("broker endpoint already exists; reconcile its owning incarnation")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		_ = l.Close()
		return nil, err
	}
	return l, nil
}

// Dial uses only the exact private socket; HTTP proxies and TCP fallback are
// disabled. This does not authenticate a product principal or a writer epoch.
func Dial(ctx context.Context, path string) (*Conn, error) {
	if err := validatePath(path); err != nil {
		return nil, err
	}
	if err := privateSocket(path); err != nil {
		return nil, err
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
	c, _, err := websocket.Dial(ctx, "ws://localhost/", &websocket.DialOptions{
		HTTPClient:      &http.Client{Transport: transport},
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		transport.CloseIdleConnections()
		return nil, err
	}
	c.SetReadLimit(MaxFrameBytes)
	return &Conn{conn: c, transport: transport}, nil
}

// Accept is called only on a handler served by an owned Unix listener. The
// caller supplies a separate connection lifetime, not the hijacked HTTP one.
func Accept(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	local, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok || local.Network() != "unix" {
		http.Error(w, "native broker requires a private Unix listener", http.StatusForbidden)
		return nil, errors.New("native broker requires a private Unix listener")
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(MaxFrameBytes)
	return &Conn{conn: c}, nil
}

func (c *Conn) Read(ctx context.Context) ([]byte, error) {
	kind, frame, err := c.conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	if kind != websocket.MessageText {
		_ = c.conn.CloseNow()
		return nil, errors.New("native broker requires text messages")
	}
	return frame, nil
}

// Write returning nil proves a frame write, not upstream acknowledgement or
// model acceptance. The authority must persist attempted before invoking it.
func (c *Conn) Write(ctx context.Context, frame []byte) error {
	if len(frame) > MaxFrameBytes {
		return errors.New("native message exceeds broker frame limit")
	}
	return c.conn.Write(ctx, websocket.MessageText, frame)
}

func (c *Conn) Close() error {
	err := c.conn.CloseNow()
	if c.transport != nil {
		c.transport.CloseIdleConnections()
	}
	return err
}

func validatePath(path string) error {
	if !filepath.IsAbs(path) || len(path) > 100 {
		return errors.New("broker socket requires a short absolute path")
	}
	return nil
}

func privateSocket(path string) error {
	for _, item := range []struct {
		path   string
		socket bool
	}{{filepath.Dir(path), false}, {path, true}} {
		info, err := os.Lstat(item.path)
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		mode := os.FileMode(0700)
		kind := info.IsDir()
		if item.socket {
			mode = 0600
			kind = info.Mode()&os.ModeSocket != 0
		}
		if !ok || stat.Uid != uint32(os.Geteuid()) || !kind || info.Mode().Perm() != mode {
			return fmt.Errorf("broker endpoint requires owned private directory and socket: %s", item.path)
		}
	}
	return nil
}
