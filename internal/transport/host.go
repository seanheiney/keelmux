package transport

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/seanheiney/agent-backplane/internal/domain"
	"github.com/seanheiney/agent-backplane/internal/protocol"
	"github.com/seanheiney/agent-backplane/internal/store"
	"golang.org/x/sys/unix"
)

type LeaseAuthority interface {
	Attach(context.Context, protocol.LeaseRequest) (domain.AttachSpec, error)
	Release(context.Context, protocol.LeaseRequest) error
}

type Host struct {
	authority LeaseAuthority
	state     *store.Store
	hostID    domain.ID
	mu        sync.Mutex
	active    map[domain.ID]bool
	// Cleanup failures are actionable retained grants, not successful detach.
	// Callers may collect these without receiving private tokens or native argv.
	Failures chan error
}

func NewHost(authority LeaseAuthority, state *store.Store, hostID domain.ID) (*Host, error) {
	if authority == nil || state == nil || hostID == "" {
		return nil, errors.New("terminal host requires its local authority, registry and exact host ID")
	}
	return &Host{authority: authority, state: state, hostID: hostID, active: map[domain.ID]bool{}, Failures: make(chan error, 16)}, nil
}

func (h *Host) Handler() http.Handler { return Handler(h.Open) }

func (h *Host) Open(ctx context.Context, request Request) (*Stream, error) {
	if err := request.validate(); err != nil {
		return nil, err
	}
	if request.HostID != h.hostID {
		return nil, domain.ConflictError{Reason: "stream host differs from this local authority"}
	}
	h.mu.Lock()
	if h.active[request.Lease.OperationID] {
		h.mu.Unlock()
		return nil, domain.ConflictError{Reason: "attachment operation already has a native stream; never replay input"}
	}
	h.active[request.Lease.OperationID] = true
	h.mu.Unlock()
	unclaim := func() { h.mu.Lock(); delete(h.active, request.Lease.OperationID); h.mu.Unlock() }
	attach, err := h.authority.Attach(ctx, request.Lease)
	if err != nil {
		unclaim()
		return nil, err
	}
	release := request.Lease
	release.Token = attach.Lease.Token
	if attach.Lease != leaseFor(release) || attach.ReadOnly != request.Lease.ReadOnly || len(attach.Argv) == 0 {
		unclaim()
		return nil, terminalError("authority returned conflicting attach identity; grant retained for repair")
	}
	if err := h.validateGrant(ctx, release); err != nil {
		h.release(release)
		unclaim()
		return nil, terminalError("current attachment grant rejected")
	}
	cmd := exec.Command(attach.Argv[0], attach.Argv[1:]...)
	cmd.Env = terminalEnv(request.Term)
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: request.Rows, Cols: request.Cols})
	if err != nil {
		h.release(release)
		unclaim()
		return nil, terminalError("owned native client could not start")
	}
	terminalFD, _, err := descriptor(terminal)
	if err == nil {
		err = unix.SetNonblock(terminalFD, true)
	}
	if err != nil {
		_ = cmd.Process.Kill()
		_ = terminal.Close()
		_ = cmd.Wait()
		h.release(release)
		unclaim()
		return nil, terminalError("owned terminal could not support bounded IO")
	}
	serverConn, clientConn := net.Pipe()
	server := newStream(ctx, serverConn, nil)
	client := newStream(ctx, clientConn, nil)
	go h.run(ctx, request, release, cmd, terminal, terminalFD, server, unclaim)
	if err := readReady(client, request); err != nil {
		_ = client.Close()
		return nil, terminalError("owned native stream did not confirm acquisition")
	}
	return client, nil
}

func leaseFor(r protocol.LeaseRequest) domain.Lease {
	return domain.Lease{SessionID: r.SessionID, ExecutionID: r.ExecutionID, Generation: r.Generation, OperationID: r.OperationID, ClientID: r.ClientID, Token: r.Token}
}

func (h *Host) validateGrant(ctx context.Context, lease protocol.LeaseRequest) error {
	g, err := h.state.Grant(ctx, lease.OperationID)
	if err != nil || !g.Active || g.Lease != leaseFor(lease) || g.ReadOnly != lease.ReadOnly {
		return errors.New("exact active attachment grant unavailable")
	}
	session, err := h.state.Session(ctx, lease.SessionID)
	if err != nil || session.HostID != h.hostID || session.Execution.ID != lease.ExecutionID || session.Execution.Generation != lease.Generation || session.Execution.State != "running" {
		return errors.New("attachment execution generation or authority changed")
	}
	return nil
}

func (h *Host) run(ctx context.Context, request Request, release protocol.LeaseRequest, cmd *exec.Cmd, terminal *os.File, terminalFD int, stream *Stream, unclaim func()) {
	ctx, cancel := context.WithCancel(ctx)
	defer unclaim()
	var workers sync.WaitGroup
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	defer func() {
		cancel()
		_ = stream.Close()
		workers.Wait()
		_ = terminal.Close() // HUP affects this owned tmux client, not its pane.
		_ = cmd.Process.Kill()
		select {
		case <-waited:
			h.release(release)
		case <-time.After(2 * time.Second):
			h.failure(errors.New("owned terminal client did not exit; attachment grant retained for repair"))
		}
	}()
	if err := h.validateGrant(ctx, release); err != nil {
		return
	}
	// A spawned process is not yet an attached client. In particular, bytes
	// written before tmux configures its tty can become terminal signals. Confirm
	// this exact owned PID at the private server before accepting native input.
	if err := h.waitNativeClient(ctx, release, cmd); err != nil {
		return
	}
	data, _ := json.Marshal(receiptFor(request))
	if err := stream.WriteFrame(Frame{Ready, data}); err != nil {
		return
	}
	done := make(chan error, 3)
	workers.Add(3)
	go func() {
		defer workers.Done()
		buffer := make([]byte, MaxFrame)
		for {
			n, err := readTerminal(ctx, terminalFD, buffer)
			if n > 0 {
				if e := stream.WriteFrame(Frame{Output, buffer[:n]}); e != nil {
					done <- e
					return
				}
			}
			if err != nil {
				done <- err
				return
			}
		}
	}()
	go func() {
		defer workers.Done()
		for {
			frame, err := stream.ReadFrame()
			if err != nil {
				done <- err
				return
			}
			if err := h.validateGrant(ctx, release); err != nil {
				done <- err
				return
			}
			select {
			case <-ctx.Done():
				done <- ctx.Err()
				return
			default:
			}
			switch frame.Kind {
			case Input:
				if release.ReadOnly {
					done <- errors.New("read-only stream rejects input")
					return
				}
				if err := writeTerminal(ctx, terminalFD, frame.Data); err != nil {
					done <- err
					return
				}
			case Resize:
				if release.ReadOnly {
					done <- errors.New("read-only stream rejects resize")
					return
				}
				if len(frame.Data) != 4 {
					done <- errors.New("invalid resize frame")
					return
				}
				rows, cols := binary.BigEndian.Uint16(frame.Data), binary.BigEndian.Uint16(frame.Data[2:])
				if rows == 0 || cols == 0 || rows > 4096 || cols > 4096 {
					done <- errors.New("invalid resize dimensions")
					return
				}
				if err := unix.IoctlSetWinsize(terminalFD, unix.TIOCSWINSZ, &unix.Winsize{Row: rows, Col: cols}); err != nil {
					done <- err
					return
				}
			case Close:
				if len(frame.Data) != 0 {
					done <- errors.New("invalid close frame")
					return
				}
				done <- nil
				return
			default:
				done <- errors.New("terminal client sent a forbidden frame direction")
				return
			}
		}
	}()
	go func() {
		defer workers.Done()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := h.validateGrant(ctx, release); err != nil {
					done <- err
					return
				}
			}
		}
	}()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, io.EOF) {
			_ = stream.WriteFrame(Frame{Failure, []byte("Native stream ended; attach again after current ownership is confirmed.")})
		} else {
			_ = stream.WriteFrame(Frame{Close, nil})
		}
	case <-ctx.Done():
	}
}

func (h *Host) waitNativeClient(ctx context.Context, lease protocol.LeaseRequest, native *exec.Cmd) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	session, err := h.state.Session(ctx, lease.SessionID)
	if err != nil || session.Execution.Runtime.Backend != "tmux" {
		return errors.New("private native tmux evidence unavailable")
	}
	ref := session.Execution.Runtime
	expected := strconv.Itoa(native.Process.Pid) + "|" + ref.Session + "|0"
	if lease.ReadOnly {
		expected = strconv.Itoa(native.Process.Pid) + "|" + ref.Session + "|1"
	}
	for {
		if err := h.validateGrant(ctx, lease); err != nil {
			return err
		}
		probe, stop := context.WithTimeout(ctx, time.Second)
		out, err := exec.CommandContext(probe, native.Path, "-N", "-S", ref.Socket, "list-clients", "-t", ref.Session, "-F", "#{client_pid}|#{session_id}|#{client_readonly}").Output()
		stop()
		if err == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				if line == expected {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (h *Host) release(lease protocol.LeaseRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		if err := h.authority.Release(ctx, lease); err == nil {
			return
		}
		select {
		case <-ctx.Done():
			h.failure(errors.New("native attachment release unconfirmed; retain exact grant and inspect surviving clients"))
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (h *Host) failure(err error) {
	select {
	case h.Failures <- err:
	default:
	}
}

func terminalEnv(term string) []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if key != "TMUX" && key != "TMUX_PANE" && key != "TERM" {
			env = append(env, value)
		}
	}
	if term == "" {
		term = "xterm-256color"
	}
	return append(env, "TERM="+term)
}
