package nativepty

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/seanheiney/keelmux/internal/domain"
	"golang.org/x/sys/unix"
)

type outputRing struct {
	data  []byte
	limit int
	end   uint64
}

func (r *outputRing) append(p []byte) {
	r.end += uint64(len(p))
	if len(p) >= r.limit {
		r.data = append(r.data[:0], p[len(p)-r.limit:]...)
		return
	}
	drop := len(r.data) + len(p) - r.limit
	if drop > 0 {
		copy(r.data, r.data[drop:])
		r.data = r.data[:len(r.data)-drop]
	}
	r.data = append(r.data, p...)
}
func (r *outputRing) read(offset uint64) (data []byte, start, end uint64, gap bool) {
	base := r.end - uint64(len(r.data))
	if offset < base {
		offset = base
		gap = true
	}
	if offset > r.end {
		offset = r.end
		gap = true
	}
	end = offset + maxInput
	if end > r.end {
		end = r.end
	}
	return append([]byte(nil), r.data[offset-base:end-base]...), offset, end, gap
}

type attachment struct {
	conn     *net.UnixConn
	identity clientIdentity
	notify   chan struct{}
}
type supervisor struct {
	b        *Backend
	r        receipt
	cmd      *exec.Cmd
	terminal *os.File
	listener *net.UnixListener
	mu       sync.Mutex
	ring     outputRing
	screen   *terminalScreen
	clients  map[*attachment]bool
	writer   *attachment
	exited   bool
	done     chan struct{}
	inputMu  sync.Mutex
	capacity *capacityDiagnostics
}

// Serve is the installed helper entrypoint. Launch credentials arrive only over
// the inherited stdin pipe; no command-line secret or persisted environment.
func Serve(state string, execution domain.ID, input io.Reader) error {
	// This process is the detached helper; never change the control daemon umask.
	syscall.Umask(0077)
	b, err := New(state)
	if err != nil {
		return err
	}
	if !validID(execution) {
		return errors.New("invalid execution")
	}
	// Read a bounded private launch packet; parent disappearance cannot authorize
	// a replacement helper or an execution with changed content.
	var message launchMessage
	decoder := json.NewDecoder(io.LimitReader(input, 2<<20))
	if err = decoder.Decode(&message); err != nil {
		return err
	}
	if err = validateSpec(message.Spec); err != nil {
		return err
	}
	if message.HistoryBytes < 4096 || message.HistoryBytes > 8<<20 {
		return errors.New("invalid history bound")
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	unlock, err := fileLock(ctx, filepath.Join(state, executionKey(execution)+".run.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	r, err := b.readReceipt(execution)
	if err != nil {
		return err
	}
	digestMatches := r.Digest == launchDigest(message.Spec, r.Mode)
	if r.Mode == "stdio" {
		digestMatches = message.Stdio != nil && r.Stdio != nil && r.Stdio.Spec == *message.Stdio && r.Digest == stdioLaunchDigest(*message.Stdio)
	}
	if r.Executable != message.Executable || r.EnvDigest != environmentDigest(message.Env) || r.Stage != "intent" || r.Ref.ExecutionID != message.Spec.Session.Execution.ID || r.Ref.OperationID != message.Spec.OperationID || !digestMatches || subtle.ConstantTimeCompare([]byte(r.Auth), []byte(message.Auth)) != 1 {
		return conflict("helper packet does not match unconsumed launch intent")
	}
	boot, err := bootID()
	if err != nil {
		return err
	}
	if boot != r.Ref.BootID {
		return conflict("launch intent belongs to another host boot")
	}
	if _, err := os.Lstat(r.Ref.Socket); !os.IsNotExist(err) {
		return conflict("refusing existing execution socket")
	}
	r.SupervisorPID = os.Getpid()
	r.SupervisorStart, err = processStart(os.Getpid())
	if err != nil {
		return err
	}
	// Consume the intent durably before child Start. A helper crash at any later
	// instruction is uncertain; a second Serve cannot execute this intent again.
	r.Stage = "attempted"
	if err = b.writeReceipt(r); err != nil {
		return err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: r.Ref.Socket, Net: "unix"})
	if err != nil {
		return err
	}
	defer listener.Close()
	if err = os.Chmod(r.Ref.Socket, 0600); err != nil {
		return err
	}
	if r.Mode == "stdio" {
		return serveStdio(b, r, message, listener)
	}
	cmd := exec.Command(message.Executable, message.Spec.Argv[1:]...)
	cmd.Dir = message.Spec.Session.Workspace
	cmd.Env = message.Env
	s := &supervisor{b: b, r: r, cmd: cmd, listener: listener, ring: outputRing{limit: message.HistoryBytes}, clients: map[*attachment]bool{}, done: make(chan struct{})}
	capacity := startCapacityDiagnostics(b.state, execution)
	if capacity != nil {
		defer capacity.close()
	}
	s.capacity = capacity
	var output io.ReadCloser
	if r.Mode == "pty" {
		s.terminal, err = pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
		output = s.terminal
		s.screen = newTerminalScreen(80, 24)
	} else {
		var rd, wr *os.File
		rd, wr, err = os.Pipe()
		if err == nil {
			cmd.Stdout = wr
			cmd.Stderr = wr
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			err = cmd.Start()
			_ = wr.Close()
			if err != nil {
				_ = rd.Close()
			}
			output = rd
		}
	}
	if err != nil {
		r.Stage = "failed"
		r.Detail = "native executable could not start"
		_ = b.writeReceipt(r)
		return err
	}
	defer output.Close()
	outputFile := output.(*os.File)
	outputFD, descriptorErr := fileDescriptor(outputFile)
	if descriptorErr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return descriptorErr
	}
	if descriptorErr = unix.SetNonblock(outputFD, true); descriptorErr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return descriptorErr
	}
	defer func() { _ = cmd.Process.Kill() }()
	s.r.Ref.PID = cmd.Process.Pid
	s.r.Ref.ProcessStart, err = processStart(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		s.r.Stage = "failed"
		s.r.Detail = "child exited before lifetime verification"
		_ = b.writeReceipt(s.r)
		return err
	}
	s.r.Stage = "running"
	if err = b.writeReceipt(s.r); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	var drain sync.WaitGroup
	drain.Add(1)
	go func() {
		defer drain.Done()
		buffer := make([]byte, maxInput)
		for {
			readStarted := s.capacity.started()
			n, err := readTerminal(context.Background(), outputFD, buffer)
			s.capacity.readDone(n, readStarted, err)
			if n > 0 {
				s.mu.Lock()
				visible := buffer[:n]
				var replies []byte
				screenStarted := s.capacity.started()
				if s.screen != nil {
					visible, replies = s.screen.process(buffer[:n])
				}
				screenNS := s.capacity.elapsed(screenStarted)
				s.ring.append(visible)
				s.notifyLocked()
				s.mu.Unlock()
				s.capacity.screenDone(n, len(replies), screenNS)
				if len(replies) > 0 {
					replyStarted := s.capacity.started()
					s.inputMu.Lock()
					replyErr := writePTY(s.terminal, replies)
					s.inputMu.Unlock()
					s.capacity.replyDone(len(replies), replyStarted, replyErr)
				}
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		err := cmd.Wait()
		drained := make(chan struct{})
		go func() { drain.Wait(); close(drained) }()
		select {
		case <-drained:
		case <-time.After(200 * time.Millisecond):
			_ = output.Close()
			<-drained
		}
		_ = output.Close()
		s.mu.Lock()
		s.exited = true
		code := cmd.ProcessState.ExitCode()
		s.r.ExitCode = &code
		if s.r.Stage != "stopping" {
			s.r.Stage = "exited"
		} else {
			s.r.Stage = "stopped"
		}
		s.r.Detail = fmt.Sprintf("exit status %d", code)
		if err != nil && code < 0 {
			s.r.Detail = "terminated by signal"
		}
		_ = b.writeReceipt(s.r)
		s.notifyLocked()
		s.mu.Unlock()
		close(s.done)
		_ = listener.Close()
	}()
	connections := make(chan struct{}, 64)
	for {
		conn, e := listener.AcceptUnix()
		if e != nil {
			break
		}
		select {
		case connections <- struct{}{}:
			go func() { defer func() { <-connections }(); s.handle(conn) }()
		default:
			_ = conn.Close()
		}
	}
	<-s.done
	// Give bounded terminal output a chance to flush; then release all clients.
	time.Sleep(20 * time.Millisecond)
	s.mu.Lock()
	for client := range s.clients {
		_ = client.conn.Close()
	}
	if s.screen != nil {
		s.screen.close()
	}
	s.mu.Unlock()
	return nil
}
func (s *supervisor) notifyLocked() {
	for client := range s.clients {
		select {
		case client.notify <- struct{}{}:
		default:
		}
	}
}
func (s *supervisor) handle(conn *net.UnixConn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(commandTimeout))
	pid, uid, err := peerIdentity(conn)
	if err != nil || uid != os.Getuid() {
		return
	}
	var req request
	if err = readMessage(conn, &req); err != nil {
		return
	}
	s.mu.Lock()
	valid := req.Ref == s.r.Ref && subtle.ConstantTimeCompare([]byte(req.Auth), []byte(s.r.Auth)) == 1
	s.mu.Unlock()
	if !valid {
		_ = writeMessage(conn, response{Error: "exact runtime identity or authority does not match"})
		return
	}
	switch req.Op {
	case "inspect":
		s.mu.Lock()
		reply := response{Ref: s.r.Ref, Alive: !s.exited, Detail: "Owned native supervisor; Ctrl-G d detaches. Bounded observed output; account/model/turn acceptance unknown."}
		if s.writer != nil {
			reply.WritableClients = 1
		}
		for c := range s.clients {
			reply.Clients = append(reply.Clients, c.identity)
		}
		s.mu.Unlock()
		_ = writeMessage(conn, reply)
	case "stop":
		if err := s.stop(); err != nil {
			_ = writeMessage(conn, response{Error: err.Error()})
			return
		}
		_ = writeMessage(conn, response{Kind: "stopped", Ref: req.Ref})
	case "attach":
		start, err := processStart(pid)
		if err != nil {
			return
		}
		s.attach(conn, req, clientIdentity{PID: pid, Start: start, ReadOnly: req.ReadOnly})
	default:
		_ = writeMessage(conn, response{Error: "unsupported native operation"})
	}
}
func (s *supervisor) stop() error {
	s.mu.Lock()
	if s.exited {
		s.mu.Unlock()
		return nil
	}
	s.r.Stage = "stopping"
	err := s.b.writeReceipt(s.r)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	// os.Process owns this direct child and serializes Signal with Wait; no
	// persisted PID or negative process-group locator is ever signalled.
	err = s.cmd.Process.Signal(syscall.SIGTERM)
	if err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	select {
	case <-s.done:
		return nil
	case <-time.After(500 * time.Millisecond):
	}
	if err = s.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	select {
	case <-s.done:
		return nil
	case <-time.After(time.Second):
		return errors.New("child stop unconfirmed")
	}
}
func (s *supervisor) attach(conn *net.UnixConn, req request, identity clientIdentity) {
	s.mu.Lock()
	if s.r.Mode != "pty" || s.exited || len(s.clients) >= 32 {
		s.mu.Unlock()
		_ = writeMessage(conn, response{Error: "native terminal unavailable"})
		return
	}
	if !req.ReadOnly && s.writer != nil {
		s.mu.Unlock()
		_ = writeMessage(conn, response{Error: "native terminal already has a managed writer"})
		return
	}
	a := &attachment{conn: conn, identity: identity, notify: make(chan struct{}, 1)}
	s.clients[a] = true
	if !req.ReadOnly {
		s.writer = a
	}
	// Resize is authorized only after this exact live connection acquires writer
	// ownership. Read-only clients neither resize nor acquire an input route.
	if !req.ReadOnly && validSize(req.Rows, req.Cols) {
		if err := pty.Setsize(s.terminal, &pty.Winsize{Rows: req.Rows, Cols: req.Cols}); err == nil {
			s.screen.resize(int(req.Cols), int(req.Rows))
		}
	}
	cursor := s.ring.end
	snapshot := s.screen.snapshot(!req.ReadOnly)
	ref := s.r.Ref
	s.mu.Unlock()
	defer func() {
		_ = conn.Close()
		// Fence any in-flight input before the next writer can acquire.
		s.inputMu.Lock()
		defer s.inputMu.Unlock()
		s.mu.Lock()
		delete(s.clients, a)
		if s.writer == a {
			s.writer = nil
		}
		s.mu.Unlock()
	}()
	if err := writeMessage(conn, response{Kind: "ready", Ref: ref, Start: cursor, End: cursor, Gap: cursor > 0, Detail: "Screen snapshot restored; prior raw terminal output and scrollback are not replayed.", Data: snapshot}); err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	done := make(chan struct{})
	defer close(done)
	inputDone := make(chan struct{})
	go func() {
		defer conn.Close()
		defer close(inputDone)
		for {
			var input request
			if err := readMessage(conn, &input); err != nil {
				return
			}
			if input.Op == "detach" {
				return
			}
			if req.ReadOnly {
				return
			}
			s.inputMu.Lock()
			s.mu.Lock()
			valid := s.writer == a && !s.exited
			s.mu.Unlock()
			if !valid {
				s.inputMu.Unlock()
				return
			}
			var err error
			switch input.Op {
			case "input":
				if len(input.Data) == 0 || len(input.Data) > maxInput {
					err = errors.New("invalid terminal input bound")
				} else {
					err = writePTY(s.terminal, input.Data)
				}
			case "resize":
				if !validSize(input.Rows, input.Cols) {
					err = errors.New("invalid terminal size")
				} else {
					s.mu.Lock()
					err = pty.Setsize(s.terminal, &pty.Winsize{Rows: input.Rows, Cols: input.Cols})
					if err == nil {
						s.screen.resize(int(input.Cols), int(input.Rows))
					}
					s.mu.Unlock()
				}
			default:
				err = errors.New("unsupported terminal action")
			}
			s.inputMu.Unlock()
			if err != nil {
				return
			}
			select {
			case <-done:
				return
			default:
			}
		}
	}()
	for {
		s.mu.Lock()
		data, start, end, gap := s.ring.read(cursor)
		exited := s.exited
		s.mu.Unlock()
		if gap {
			_ = writeMessage(conn, response{Kind: "gap", Gap: true, Start: start, End: end, Detail: "Output overran bounded history; detach and reconnect for a current screen snapshot."})
			return
		}
		if len(data) > 0 {
			if err := writeMessage(conn, response{Kind: "output", Data: data, Start: start, End: end}); err != nil {
				return
			}
			cursor = end
			continue
		}
		if exited {
			_ = writeMessage(conn, response{Kind: "exit"})
			return
		}
		select {
		case <-a.notify:
		case <-inputDone:
			return
		case <-done:
			return
		case <-time.After(time.Second):
			if err := writeMessage(conn, response{Kind: "heartbeat"}); err != nil {
				return
			}
		}
	}
}
func validSize(rows, cols uint16) bool {
	return rows > 0 && cols > 0 && rows <= 512 && cols <= 512 && uint32(rows)*uint32(cols) <= 65536
}
