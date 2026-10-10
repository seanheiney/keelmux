package nativepty

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/seanheiney/keelmux/internal/domain"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"
)

const maxMessage = 2 << 20
const maxInput = 32 << 10
const ioTimeout = 2 * time.Second

// ErrUnavailable means the exact execution outcome cannot be established.
// Callers must not treat this as proof of exit or authorize replacement.
var ErrUnavailable = errors.New("native supervisor unavailable; no PID-based recovery or replay")

type request struct {
	Op         string            `json:"op"`
	Auth       string            `json:"auth,omitempty"`
	Ref        domain.RuntimeRef `json:"ref,omitempty"`
	ReadOnly   bool              `json:"read_only,omitempty"`
	Data       []byte            `json:"data,omitempty"`
	Rows, Cols uint16
	PID        int    `json:"pid,omitempty"`
	Offset     uint64 `json:"offset,omitempty"`
}
type response struct {
	Kind            string            `json:"kind,omitempty"`
	Error           string            `json:"error,omitempty"`
	Ref             domain.RuntimeRef `json:"ref,omitempty"`
	Alive           bool              `json:"alive,omitempty"`
	WritableClients int               `json:"writable_clients,omitempty"`
	Detail          string            `json:"detail,omitempty"`
	Data            []byte            `json:"data,omitempty"`
	Start, End      uint64
	Gap             bool             `json:"gap,omitempty"`
	Clients         []clientIdentity `json:"clients,omitempty"`
}
type clientIdentity struct {
	PID      int    `json:"pid"`
	Start    string `json:"start"`
	ReadOnly bool   `json:"read_only"`
}

func writeMessage(w io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > maxMessage {
		return errors.New("native message exceeds bound")
	}
	if c, ok := w.(net.Conn); ok {
		_ = c.SetWriteDeadline(time.Now().Add(ioTimeout))
	}
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(data)))
	if err := writeAll(w, prefix[:]); err != nil {
		return err
	}
	return writeAll(w, data)
}
func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
func readMessage(r io.Reader, value any) error {
	var prefix [4]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return err
	}
	size := binary.BigEndian.Uint32(prefix[:])
	if size == 0 || size > maxMessage {
		return errors.New("native message exceeds bound")
	}
	data := make([]byte, int(size))
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}
func (b *Backend) connect(ctx context.Context, r receipt) (*net.UnixConn, error) {
	if err := privateDirectory(b.state); err != nil {
		return nil, err
	}
	boot, err := bootID()
	if err != nil {
		return nil, err
	}
	match, err := sameBoot(boot, r.Ref.BootID)
	if err != nil {
		return nil, err
	}
	if !match {
		return nil, ErrUnavailable
	}
	if r.SupervisorPID <= 0 || r.SupervisorStart == "" {
		return nil, ErrUnavailable
	}
	start, err := processStart(r.SupervisorPID)
	if err != nil {
		return nil, ErrUnavailable
	}
	if start != r.SupervisorStart {
		return nil, conflict("supervisor PID was reused; refusing runtime")
	}
	info, err := os.Lstat(r.Ref.Socket)
	if os.IsNotExist(err) {
		return nil, ErrUnavailable
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0077 != 0 || !owned(info) {
		return nil, conflict("native socket is not private, owned and a Unix socket")
	}
	dialer := net.Dialer{}
	raw, err := dialer.DialContext(ctx, "unix", r.Ref.Socket)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	conn := raw.(*net.UnixConn)
	pid, uid, err := peerIdentity(conn)
	if err != nil || uid != os.Getuid() || pid != r.SupervisorPID {
		conn.Close()
		return nil, conflict("native socket peer is not the recorded supervisor")
	}
	start, err = processStart(pid)
	if err != nil || start != r.SupervisorStart {
		conn.Close()
		return nil, conflict("native supervisor process lifetime changed")
	}
	deadline := time.Now().Add(commandTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	return conn, nil
}

// ClientAttached verifies the exact attach process at the supervisor. The
// client enters raw mode before registering, allowing host input readiness.
func ClientAttached(ctx context.Context, ref domain.RuntimeRef, pid int, readOnly bool) (bool, error) {
	b, err := New(filepath.Dir(ref.Socket))
	if err != nil {
		return false, err
	}
	r, err := b.receiptForRef(ref)
	if err != nil {
		return false, err
	}
	c, err := b.connect(ctx, r)
	if err != nil {
		return false, err
	}
	defer c.Close()
	if err = writeMessage(c, request{Op: "inspect", Auth: r.Auth, Ref: ref}); err != nil {
		return false, err
	}
	var reply response
	if err = readMessage(c, &reply); err != nil {
		return false, err
	}
	if reply.Error != "" {
		return false, conflict(reply.Error)
	}
	start, err := processStart(pid)
	if err != nil {
		return false, err
	}
	for _, client := range reply.Clients {
		if client.PID == pid && client.Start == start && client.ReadOnly == readOnly {
			return true, nil
		}
	}
	return false, nil
}
