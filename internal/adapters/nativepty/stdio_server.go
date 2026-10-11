package nativepty

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
	ps "github.com/seanheiney/keelmux/internal/primarystdio"
	"golang.org/x/sys/unix"
)

type stdioWait struct {
	ID, Name string
	Reply    chan ps.Envelope
	Received bool // protected by mu; consuming Reply never rearms correlation
}
type stdioEngine struct {
	b                             *Backend
	r                             receipt
	cmd                           *exec.Cmd
	listener                      *net.UnixListener
	stdin, stdout, stderr         *os.File
	mu                            sync.Mutex
	admission                     sync.Mutex // receive registration versus guard/stop, never Service
	rpcMu                         sync.Mutex
	state                         stdioState
	wait                          *stdioWait
	failed                        bool
	exchangeFrames, exchangeBytes int
	done                          chan struct{}
	notify                        chan struct{}
	codec                         ps.Codec
	disk                          stdioDisk
}

func serveStdio(b *Backend, r receipt, m launchMessage, listener *net.UnixListener) error {
	if !stdioImageAvailable() || m.Stdio == nil || r.Stdio == nil || m.Spec.Argv[0] != m.Stdio.Binary || len(m.Spec.Argv) != 2 || m.Spec.Argv[1] != "app-server" || environmentDigest(m.Env) != environmentDigest(stdioEnvironment(m.Stdio.Environment)) {
		return ps.ErrUnavailable
	}
	state, err := b.readStdio(r)
	if err != nil {
		return err
	}
	if !sameStdioDirectories(state.Channel.Spec, state.Directories) {
		return ps.ErrConflict
	}
	cmd := exec.Command(m.Executable, "app-server")
	cmd.Dir = m.Stdio.Workspace
	cmd.Env = stdioEnvironment(m.Stdio.Environment)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	inputRD, inputWR, err := os.Pipe()
	if err != nil {
		return err
	}
	defer inputRD.Close()
	defer inputWR.Close()
	outputRD, outputWR, err := os.Pipe()
	if err != nil {
		return err
	}
	defer outputRD.Close()
	defer outputWR.Close()
	errorRD, errorWR, err := os.Pipe()
	if err != nil {
		return err
	}
	defer errorRD.Close()
	defer errorWR.Close()
	cmd.Stdin = inputRD
	cmd.Stdout = outputWR
	cmd.Stderr = errorWR
	if err = cmd.Start(); err != nil {
		r.Stage = "failed"
		r.Detail = "stdio child could not start"
		_ = b.writeReceipt(r)
		return ps.ErrUnavailable
	}
	_ = inputRD.Close()
	_ = outputWR.Close()
	_ = errorWR.Close()
	// Only this retained child handle is used, including initialization failure.
	defer func() { _ = cmd.Process.Kill() }()
	r.Ref.PID = cmd.Process.Pid
	r.Ref.ProcessStart, err = processStart(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return ps.ErrUncertain
	}
	r.Stdio.Ref.Runtime = r.Ref
	state.Channel = *r.Stdio
	if err = stdioImageVerifier(state.Channel); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		r.Stage = "failed"
		r.Detail = "stdio running image unavailable"
		_ = b.writeReceipt(r)
		return err
	}
	for _, f := range []*os.File{inputWR, outputRD, errorRD} {
		fd, e := fileDescriptor(f)
		if e != nil {
			err = e
			break
		}
		if e = unix.SetNonblock(fd, true); e != nil {
			err = e
			break
		}
	}
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	s := &stdioEngine{b: b, r: r, cmd: cmd, listener: listener, stdin: inputWR, stdout: outputRD, stderr: errorRD, state: state, done: make(chan struct{}), notify: make(chan struct{}, 1)}
	if err = b.saveStdio(state); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	s.disk, err = b.stdioDisk(r.Ref.ExecutionID)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	s.r.Stage = "running"
	if err = b.writeReceipt(s.r); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	var drains sync.WaitGroup
	drains.Add(2)
	go func() { defer drains.Done(); s.receive() }()
	go func() { defer drains.Done(); s.drainStderr() }()
	go func() {
		err := cmd.Wait()
		_ = inputWR.Close()
		drained := make(chan struct{})
		go func() { drains.Wait(); close(drained) }()
		select {
		case <-drained:
		case <-time.After(200 * time.Millisecond):
			_ = outputRD.Close()
			_ = errorRD.Close()
			<-drained
		}
		s.mu.Lock()
		code := cmd.ProcessState.ExitCode()
		s.r.ExitCode = &code
		s.r.Stage = "exited"
		if s.state.TermAttempted {
			s.r.Stage = "stopped"
		}
		s.r.Detail = "owned stdio child exited"
		if err != nil && code < 0 {
			s.r.Detail = "owned stdio child terminated"
		}
		s.markExitedLocked()
		_ = b.writeReceipt(s.r)
		s.mu.Unlock()
		s.signal()
		_ = listener.Close()
	}()
	connections := make(chan struct{}, 64)
	for {
		c, e := listener.AcceptUnix()
		if e != nil {
			break
		}
		select {
		case connections <- struct{}{}:
			go func() { defer func() { <-connections }(); s.handle(c) }()
		default:
			_ = c.Close()
		}
	}
	<-s.done
	return nil
}
func (s *stdioEngine) signal() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}
func (s *stdioEngine) markExitedLocked() {
	s.state.Snapshot = ps.Snapshot{}
	close(s.done)
}
func stdioFrameHash(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}
func (s *stdioEngine) persistLocked() error {
	if s.checkDiskLocked() != nil || s.b.saveStdio(s.state) != nil {
		s.failed = true
		s.state.Pending = true
		s.state.Continuity = ps.ContinuityUnknown
		s.signal()
		return ps.ErrStorage
	}
	var err error
	s.disk, err = s.b.stdioDisk(s.r.Ref.ExecutionID)
	if err != nil {
		s.failed = true
		s.state.Pending = true
		s.state.Continuity = ps.ContinuityUnknown
		return ps.ErrStorage
	}
	return nil
}

// Authority checks must validate the durable evidence even when no write is
// needed. Failure is sticky in memory and never repairs a replaced spool.
func (s *stdioEngine) checkDiskLocked() error {
	disk, err := s.b.stdioDisk(s.r.Ref.ExecutionID)
	if s.failed || err != nil || disk != s.disk {
		s.failed = true
		s.state.Pending = true
		s.state.Continuity = ps.ContinuityUnknown
		s.state.Snapshot = ps.Snapshot{}
		s.signal()
		return ps.ErrStorage
	}
	return nil
}
func (s *stdioEngine) appendLocked(kind ps.EventKind, reason ps.ReasonCode, id ps.NativeID, raw []byte) error {
	if s.failed {
		return ps.ErrStorage
	}
	blocks := 0
	rawBytes := 0
	for _, record := range s.state.Records {
		rawBytes += len(record.Raw)
		if ps.EventBlocks(record.Event) {
			blocks++
		}
	}
	if len(raw) > ps.MaxFrameBytes || len(s.state.Records) >= ps.MaxExchangeFrames+ps.MaxEvents-1 || blocks >= ps.MaxEvents-1 || rawBytes+len(raw) > (ps.MaxEventBytes-(256<<10))*3/4 {
		kind = ps.EventGap
		reason = ps.ReasonOverflow
		id = ps.NativeID{}
		raw = nil
	}
	event := ps.Event{Kind: kind, Reason: reason, RequestID: id, FrameSHA256: stdioFrameHash(raw), FrameBytes: uint32(len(raw))}
	cur, err := ps.AdvanceCursor(s.state.Cursor, event)
	if err != nil {
		return err
	}
	event.Cursor = cur
	s.state.Cursor = cur
	s.state.Records = append(s.state.Records, stdioRecord{event, append([]byte(nil), raw...)})
	if ps.EventBlocks(event) {
		s.state.Pending = true
	}
	if kind == ps.EventGap || kind == ps.EventMalformed {
		s.state.Continuity = ps.ContinuityBroken
	}
	// Every receive invalidates the prior multi-query snapshot, even a result.
	s.state.Snapshot = ps.Snapshot{}
	err = s.persistLocked()
	s.signal()
	return err
}
func (s *stdioEngine) fault(reason ps.ReasonCode) {
	s.admission.Lock()
	defer s.admission.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Continuity == ps.ContinuityBroken || s.failed {
		return
	}
	kind := ps.EventGap
	if reason == ps.ReasonMalformed {
		kind = ps.EventMalformed
	}
	_ = s.appendLocked(kind, reason, ps.NativeID{}, nil)
}
func nativeID(raw []byte) ps.NativeID {
	if len(raw) == 0 {
		return ps.NativeID{}
	}
	var text string
	if raw[0] == '"' && json.Unmarshal(raw, &text) == nil {
		return ps.NativeID{Kind: "string", Text: text}
	}
	return ps.NativeID{Kind: "int64", Text: string(raw)}
}
func (s *stdioEngine) receiveLine(raw []byte) {
	s.admission.Lock()
	defer s.admission.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed || s.state.Continuity == ps.ContinuityBroken {
		return
	}
	s.exchangeFrames++
	s.exchangeBytes += len(raw)
	if s.exchangeFrames > ps.MaxExchangeFrames || s.exchangeBytes > ps.MaxExchangeBytes {
		_ = s.appendLocked(ps.EventGap, ps.ReasonOverflow, ps.NativeID{}, nil)
		return
	}
	e, err := s.codec.DecodeLine(raw)
	if err != nil {
		_ = s.appendLocked(ps.EventMalformed, ps.ReasonMalformed, ps.NativeID{}, raw)
		return
	}
	kind, reason := ps.EventResult, ps.ReasonNone
	switch e.Kind {
	case ps.Request:
		kind = ps.EventCallback
		reason = ps.ReasonPendingCallback
	case ps.Notification:
		notice, err := s.codec.ClassifyNotification(e)
		kind = ps.EventNotice
		if err != nil || notice.Kind == ps.NoticeUnsupported {
			kind = ps.EventUnsupported
			reason = ps.ReasonUnsupported
		}
		if notice.Kind != ps.NoticeRemoteStatus && notice.ThreadID != s.state.Channel.Spec.Binding.NativeThreadID {
			kind = ps.EventUnsupported
			reason = ps.ReasonCorrelation
		}
		if notice.Thread != nil && (notice.Thread.ID != s.state.Channel.Spec.Binding.NativeThreadID || notice.Thread.CWD != s.state.Channel.Spec.Workspace) {
			kind, reason = ps.EventUnsupported, ps.ReasonCorrelation
		}
		if notice.Kind == ps.NoticeRemoteStatus && notice.RemoteStatus != "disabled" {
			kind, reason = ps.EventUnsupported, ps.ReasonUnsupported
		}
		if notice.Kind != ps.NoticeRemoteStatus {
			s.state.NoticeRevision++
		}
	case ps.Result, ps.RPCError:
		quoted, _ := json.Marshal("")
		if s.wait != nil {
			quoted, _ = json.Marshal(s.wait.ID)
		}
		if s.wait == nil || s.wait.Received || !bytes.Equal(e.ID, quoted) {
			kind = ps.EventUnsupported
			reason = ps.ReasonCorrelation
		}
	default:
		kind = ps.EventUnsupported
		reason = ps.ReasonUnsupported
	}
	if s.appendLocked(kind, reason, nativeID(e.ID), raw) != nil {
		return
	}
	if (e.Kind == ps.Result || e.Kind == ps.RPCError) && kind == ps.EventResult && s.wait != nil {
		s.wait.Received = true
		select {
		case s.wait.Reply <- e:
		default:
			_ = s.appendLocked(ps.EventUnsupported, ps.ReasonCorrelation, nativeID(e.ID), raw)
		}
	}
}
func (s *stdioEngine) receive() {
	fd, _ := fileDescriptor(s.stdout)
	buffer := make([]byte, maxInput)
	pending := make([]byte, 0, maxInput)
	for {
		n, err := readTerminal(context.Background(), fd, buffer)
		if n > 0 {
			pending = append(pending, buffer[:n]...)
			for {
				at := bytes.IndexByte(pending, '\n')
				if at < 0 {
					break
				}
				line := pending[:at+1]
				if len(line) > ps.MaxFrameBytes {
					s.fault(ps.ReasonOverflow)
				} else {
					s.receiveLine(line)
				}
				pending = pending[at+1:]
			}
			if len(pending) > ps.MaxFrameBytes {
				s.fault(ps.ReasonOverflow)
				pending = pending[:0]
			}
		}
		if err != nil {
			if len(pending) > 0 {
				s.fault(ps.ReasonPartialEOF)
			}
			return
		}
	}
}
func (s *stdioEngine) drainStderr() {
	fd, _ := fileDescriptor(s.stderr)
	buffer := make([]byte, maxInput)
	for {
		n, err := readTerminal(context.Background(), fd, buffer)
		if n > 0 {
			s.mu.Lock()
			if s.state.StderrBytes <= ps.MaxStderrBytes {
				s.state.StderrBytes += uint64(n)
				s.state.StderrTail = append(s.state.StderrTail, buffer[:n]...)
				if len(s.state.StderrTail) > ps.MaxStderrTail {
					s.state.StderrTail = append([]byte(nil), s.state.StderrTail[len(s.state.StderrTail)-ps.MaxStderrTail:]...)
					s.state.StderrTruncated = true
				}
				if s.state.StderrBytes > ps.MaxStderrBytes {
					s.state.StderrTruncated = true
				}
				_ = s.persistLocked()
			}
			s.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}
func (s *stdioEngine) identity() error {
	if !sameStdioDirectories(s.state.Channel.Spec, s.state.Directories) {
		return ps.ErrConflict
	}
	return stdioImageVerifier(s.state.Channel)
}
func (s *stdioEngine) call(ctx context.Context, name string, encode func(string) ([]byte, error)) (ps.Envelope, string, error) {
	if ctx.Err() != nil {
		return ps.Envelope{}, "", ps.ErrUncertain
	}
	s.admission.Lock()
	s.mu.Lock()
	if s.failed || s.state.Pending || s.state.Continuity != ps.ContinuityComplete || s.wait != nil {
		s.mu.Unlock()
		s.admission.Unlock()
		return ps.Envelope{}, "", ps.ErrUnavailable
	}
	if err := s.identity(); err != nil {
		s.mu.Unlock()
		s.admission.Unlock()
		s.fault(ps.ReasonIdentity)
		return ps.Envelope{}, "", err
	}
	id := "stdio-" + nonce()[:32]
	raw, err := encode(id)
	if err != nil {
		s.mu.Unlock()
		s.admission.Unlock()
		return ps.Envelope{}, id, err
	}
	step := stdioStep{Name: name, ID: id, Attempted: true}
	s.state.Steps = append(s.state.Steps, step)
	// Only bootstrap attempt tombstones need indefinite retention. Inspection
	// retains one exchange of step IDs; unacknowledged receive events retain IDs.
	if len(s.state.Steps) > 12 {
		s.state.Steps = append(s.state.Steps[:3:3], s.state.Steps[len(s.state.Steps)-9:]...)
	}
	at := len(s.state.Steps) - 1
	wait := &stdioWait{ID: id, Name: name, Reply: make(chan ps.Envelope, 1)}
	s.wait = wait
	if err = s.persistLocked(); err != nil {
		s.mu.Unlock()
		s.admission.Unlock()
		return ps.Envelope{}, id, err
	}
	s.mu.Unlock()
	if ctx.Err() != nil || writeStdioPipe(ctx, s.stdin, raw) != nil {
		s.admission.Unlock()
		s.fault(ps.ReasonTimeout)
		return ps.Envelope{}, id, ps.ErrUncertain
	}
	s.mu.Lock()
	s.state.Steps[at].Sent = true
	err = s.persistLocked()
	s.mu.Unlock()
	s.admission.Unlock()
	if err != nil {
		return ps.Envelope{}, id, err
	}
	for {
		select {
		case e := <-wait.Reply:
			s.mu.Lock()
			s.wait = nil
			s.state.Steps[at].Completed = true
			err = s.persistLocked()
			pending := s.state.Pending
			s.mu.Unlock()
			if err != nil {
				return e, id, err
			}
			if pending {
				return e, id, ps.ErrUnavailable
			}
			return e, id, nil
		case <-ctx.Done():
			s.fault(ps.ReasonTimeout)
			return ps.Envelope{}, id, ps.ErrUncertain
		case <-s.done:
			return ps.Envelope{}, id, ps.ErrUncertain
		case <-s.notify:
			s.mu.Lock()
			bad := s.failed || s.state.Pending
			s.mu.Unlock()
			if bad {
				return ps.Envelope{}, id, ps.ErrUnavailable
			}
		}
	}
}
func writeStdioPipe(ctx context.Context, f *os.File, raw []byte) error {
	bound, cancel := context.WithTimeout(ctx, ioTimeout)
	defer cancel()
	fd, err := fileDescriptor(f)
	if err != nil {
		return err
	}
	for len(raw) > 0 {
		if bound.Err() != nil || !stdioPeerPresent(ctx) {
			return ps.ErrUncertain
		}
		poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}
		_, err = unix.Poll(poll, 25)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if poll[0].Revents == 0 {
			continue
		}
		n, err := unix.Write(fd, raw)
		if err == unix.EINTR || err == unix.EAGAIN {
			continue
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		raw = raw[n:]
	}
	return nil
}

func (s *stdioEngine) bootstrap(ctx context.Context) error {
	if lockStdio(ctx, &s.rpcMu) != nil {
		return ps.ErrUncertain
	}
	defer s.rpcMu.Unlock()
	s.mu.Lock()
	if s.state.Bootstrap != ps.BootstrapUnsent {
		s.mu.Unlock()
		return nil
	}
	s.state.Bootstrap = ps.BootstrapAttempted
	s.state.Snapshot = ps.Snapshot{}
	s.exchangeFrames = 0
	s.exchangeBytes = 0
	err := s.persistLocked()
	s.mu.Unlock()
	if err != nil {
		return err
	}
	e, id, err := s.call(ctx, "initialize", s.codec.Initialize)
	if err == nil {
		result, parse := s.codec.ParseInitialize(e, id)
		if parse != nil || result.Home != s.state.Channel.Spec.Environment.CodexHome {
			err = ps.ErrUnavailable
		}
	}
	if err == nil {
		s.admission.Lock()
		s.mu.Lock()
		if s.failed || s.state.Pending || s.identity() != nil || ctx.Err() != nil {
			err = ps.ErrUnavailable
		}
		if err == nil {
			s.state.Steps = append(s.state.Steps, stdioStep{Name: "initialized", Attempted: true})
		}
		at := len(s.state.Steps) - 1
		if err == nil {
			err = s.persistLocked()
		}
		s.mu.Unlock()
		if err == nil {
			raw, _ := s.codec.Initialized()
			err = writeStdioPipe(ctx, s.stdin, raw)
			s.mu.Lock()
			if err == nil {
				s.state.Steps[at].Sent = true
				s.state.Steps[at].Completed = true
				err = s.persistLocked()
			}
			s.mu.Unlock()
		}
		s.admission.Unlock()
	}
	if err == nil {
		thread := s.state.Channel.Spec.Binding.NativeThreadID
		e, id, err = s.call(ctx, "resume", func(id string) ([]byte, error) { return s.codec.Resume(id, thread) })
		if err == nil {
			result, parse := s.codec.ParseResume(e, id)
			if parse != nil || result.ID != thread || result.CWD != s.state.Channel.Spec.Workspace || result.ResumeCWD != s.state.Channel.Spec.Workspace || result.Ephemeral {
				err = ps.ErrUnavailable
			}
		}
	}
	return s.finishBootstrap(err)
}
func (s *stdioEngine) finishBootstrap(err error) error {
	s.mu.Lock()
	if err == nil {
		s.state.Bootstrap = ps.BootstrapComplete
	} else {
		s.state.Bootstrap = ps.BootstrapUncertain
	}
	s.state.Snapshot = ps.Snapshot{}
	saved := s.persistLocked()
	s.mu.Unlock()
	if saved != nil {
		return saved
	}
	if err != nil {
		s.fault(ps.ReasonCorrelation)
	}
	return err
}
func (s *stdioEngine) inspect(ctx context.Context) error {
	if lockStdio(ctx, &s.rpcMu) != nil {
		return ps.ErrUncertain
	}
	defer s.rpcMu.Unlock()
	s.mu.Lock()
	ready := s.state.Bootstrap == ps.BootstrapComplete && !s.state.Pending && !s.failed && s.state.Continuity == ps.ContinuityComplete && s.state.StopOperation == ""
	noticeRevision := s.state.NoticeRevision
	s.exchangeFrames = 0
	s.exchangeBytes = 0
	s.mu.Unlock()
	if !ready {
		return nil
	}
	binding := s.state.Channel.Spec.Binding
	workspace := s.state.Channel.Spec.Workspace
	e, id, err := s.call(ctx, "loaded", s.codec.Loaded)
	if err != nil {
		return err
	}
	loaded, err := s.codec.ParseLoaded(e, id)
	if err != nil || loaded.Cursor.State != ps.PageCursorNull {
		s.fault(ps.ReasonMalformed)
		return ps.ErrUnavailable
	}
	exact := err == nil && len(loaded.IDs) == 1 && loaded.IDs[0] == binding.NativeThreadID && loaded.Cursor.State == ps.PageCursorNull
	e, id, err = s.call(ctx, "read", func(id string) ([]byte, error) { return s.codec.Read(id, binding.NativeThreadID) })
	if err != nil {
		return err
	}
	thread, err := s.codec.ParseRead(e, id)
	if err != nil {
		s.fault(ps.ReasonMalformed)
		return ps.ErrUnavailable
	}
	idle := err == nil && thread.ID == binding.NativeThreadID && thread.CWD == workspace && !thread.Ephemeral && thread.Status == ps.StatusIdle && len(thread.ActiveFlags) == 0
	e, id, err = s.call(ctx, "queue", func(id string) ([]byte, error) { return s.codec.Queue(id, binding.NativeThreadID) })
	if err != nil {
		return err
	}
	queue, err := s.codec.ParseQueue(e, id)
	if err != nil || queue.Cursor.State != ps.PageCursorNull {
		s.fault(ps.ReasonMalformed)
		return ps.ErrUnavailable
	}
	emptyQueue := err == nil && queue.Count == 0 && queue.Cursor.State == ps.PageCursorNull
	e, id, err = s.call(ctx, "background", func(id string) ([]byte, error) { return s.codec.Background(id, binding.NativeThreadID) })
	if err != nil {
		return err
	}
	background, err := s.codec.ParseBackground(e, id)
	if err != nil || background.Cursor.State != ps.PageCursorNull {
		s.fault(ps.ReasonMalformed)
		return ps.ErrUnavailable
	}
	emptyBackground := err == nil && background.Count == 0 && background.Cursor.State == ps.PageCursorNull
	s.admission.Lock()
	defer s.admission.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed || s.state.Pending || s.wait != nil || s.state.NoticeRevision != noticeRevision {
		return ps.ErrUnavailable
	}
	if err = s.identity(); err != nil {
		_ = s.appendLocked(ps.EventGap, ps.ReasonIdentity, ps.NativeID{}, nil)
		return err
	}
	s.state.Snapshot = s.snapshotLocked()
	s.state.Snapshot.ExactLoadedThread = exact
	s.state.Snapshot.Idle = idle
	s.state.Snapshot.EmptyQueue = emptyQueue
	s.state.Snapshot.EmptyBackground = emptyBackground
	s.state.Snapshot.SnapshotSHA256 = ps.SnapshotDigest(s.state.Snapshot)
	return s.persistLocked()
}
func (s *stdioEngine) snapshotLocked() ps.Snapshot {
	q := ps.Snapshot{Channel: s.state.Channel.Ref, Binding: s.state.Channel.Spec.Binding, Cursor: s.state.Cursor, Continuity: s.state.Continuity, Bootstrap: s.state.Bootstrap, Pending: s.state.Pending || s.failed, StderrTruncated: s.state.StderrTruncated}
	select {
	case <-s.done:
	default:
		q.ProcessAlive = true
	}
	q.ExactDirectories = sameStdioDirectories(s.state.Channel.Spec, s.state.Directories)
	if q.ProcessAlive {
		q.ExactImage = stdioImageVerifier(s.state.Channel) == nil
	}
	q.SnapshotSHA256 = ps.SnapshotDigest(q)
	return q
}
func (s *stdioEngine) observation(from ps.Cursor) (ps.Observation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed {
		return ps.Observation{}, ps.ErrStorage
	}
	if from.ChannelIncarnation != s.state.Channel.Ref.ChannelIncarnation || ps.ValidateCursor(from) != nil || from.Seq < s.state.Ack.Seq || from.Seq > s.state.Cursor.Seq {
		return ps.Observation{}, ps.ErrConflict
	}
	found := from == s.state.Ack || from == s.state.Cursor
	for _, record := range s.state.Records {
		if record.Event.Cursor == from {
			found = true
		}
	}
	if !found {
		return ps.Observation{}, ps.ErrConflict
	}
	snapshot := s.state.Snapshot
	if snapshot.Channel != s.state.Channel.Ref || snapshot.Cursor != s.state.Cursor {
		snapshot = s.snapshotLocked()
	}
	snapshot.StderrTruncated = s.state.StderrTruncated
	snapshot.SnapshotSHA256 = ps.SnapshotDigest(snapshot)
	s.state.Snapshot = snapshot
	o := ps.Observation{Snapshot: snapshot, From: from, Through: from}
	bytes := uint64(0)
	for _, record := range s.state.Records {
		if record.Event.Cursor.Seq <= from.Seq {
			continue
		}
		if len(o.Events) >= ps.MaxExchangeFrames || bytes+uint64(record.Event.FrameBytes) > ps.MaxExchangeBytes {
			o.More = true
			break
		}
		o.Events = append(o.Events, record.Event)
		o.Through = record.Event.Cursor
		bytes += uint64(record.Event.FrameBytes)
	}
	o.BatchSHA256 = ps.BatchDigest(o)
	if ps.ValidateObservation(s.state.Channel, o) != nil {
		return o, ps.ErrStorage
	}
	ack := ps.ImportAck{From: o.From, Through: o.Through, BatchSHA256: o.BatchSHA256}
	s.state.Batches = append(s.state.Batches, ack)
	if len(s.state.Batches) > 16 {
		s.state.Batches = s.state.Batches[len(s.state.Batches)-16:]
	}
	if err := s.persistLocked(); err != nil {
		return o, err
	}
	return o, nil
}
func (s *stdioEngine) acknowledge(ack ps.ImportAck) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed {
		return ps.ErrStorage
	}
	known := false
	for _, batch := range s.state.Batches {
		if batch == ack {
			known = true
			break
		}
	}
	if !known {
		return ps.ErrConflict
	}
	if ack.Through == s.state.Ack {
		return nil
	}
	if ack.From.Seq < s.state.Ack.Seq || ack.Through.Seq < s.state.Ack.Seq {
		return ps.ErrConflict
	}
	s.state.Ack = ack.Through
	kept := s.state.Records[:0]
	for _, record := range s.state.Records {
		if record.Event.Cursor.Seq > ack.Through.Seq || ps.EventBlocks(record.Event) {
			kept = append(kept, record)
		}
	}
	s.state.Records = kept
	return s.persistLocked()
}
func (s *stdioEngine) quiescentLocked(q ps.Quiescence) bool {
	if s.checkDiskLocked() != nil {
		return false
	}
	snap := s.state.Snapshot
	return !s.failed && !s.state.Pending && s.state.StopOperation == "" && s.wait == nil && s.state.Continuity == ps.ContinuityComplete && s.state.Bootstrap == ps.BootstrapComplete && s.state.Ack == s.state.Cursor && q.Cursor == s.state.Cursor && q.SnapshotSHA256 == snap.SnapshotSHA256 && snap.Cursor == s.state.Cursor && snap.ExactLoadedThread && snap.Idle && snap.EmptyQueue && snap.EmptyBackground && snap.ProcessAlive && snap.ExactImage && snap.ExactDirectories && s.identity() == nil
}
func (s *stdioEngine) stop(ctx context.Context, stop ps.StopRequest) error {
	if !validID(stop.OperationID) {
		return ps.ErrInvalid
	}
	if lockStdio(ctx, &s.rpcMu) != nil {
		return ps.ErrUncertain
	}
	defer s.rpcMu.Unlock()
	s.admission.Lock()
	s.mu.Lock()
	if s.state.StopOperation != "" {
		same := s.state.StopOperation == stop.OperationID
		s.mu.Unlock()
		s.admission.Unlock()
		if !same {
			return ps.ErrConflict
		}
		select {
		case <-s.done:
			return nil
		default:
			return ps.ErrUncertain
		}
	}
	if !s.quiescentLocked(stop.Quiescence) {
		s.mu.Unlock()
		s.admission.Unlock()
		return ps.ErrConflict
	}
	if ctx.Err() != nil {
		s.mu.Unlock()
		s.admission.Unlock()
		return ps.ErrUncertain
	}
	err := s.recordStopAttemptLocked(stop.OperationID)
	if err == nil {
		err = s.b.writeReceipt(s.r)
	}
	if err == nil {
		err = s.cmd.Process.Signal(syscall.SIGTERM)
	}
	s.mu.Unlock()
	s.admission.Unlock()
	if err != nil && !errors.Is(err, os.ErrProcessDone) {
		return ps.ErrUncertain
	}
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ps.ErrUncertain
	case <-time.After(500 * time.Millisecond):
	}
	s.mu.Lock()
	s.state.KillAttempted = true
	err = s.persistLocked()
	if err == nil {
		err = s.cmd.Process.Kill()
	}
	s.mu.Unlock()
	if err != nil && !errors.Is(err, os.ErrProcessDone) {
		return ps.ErrUncertain
	}
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ps.ErrUncertain
	case <-time.After(time.Second):
		return ps.ErrUncertain
	}
}
func (s *stdioEngine) recordStopAttemptLocked(operation domain.ID) error {
	s.state.StopOperation = operation
	s.state.TermAttempted = true
	s.state.Snapshot = ps.Snapshot{}
	s.r.Stage = "stopping"
	return s.persistLocked()
}
func lockStdio(ctx context.Context, mu *sync.Mutex) error {
	for {
		if ctx.Err() != nil {
			return ps.ErrUncertain
		}
		if mu.TryLock() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ps.ErrUncertain
		case <-time.After(5 * time.Millisecond):
		}
	}
}
func (s *stdioEngine) handle(c *net.UnixConn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	_, uid, err := peerIdentity(c)
	if err != nil || uid != os.Getuid() {
		return
	}
	var req stdioRequest
	if readStdioMessage(c, &req) != nil {
		return
	}
	if req.Ref != s.r.Ref || subtle.ConstantTimeCompare([]byte(req.Auth), []byte(s.r.Auth)) != 1 {
		return
	}
	if req.Op == "inspect" && req.Version == 0 && req.Channel == nil && req.Cursor == nil && req.Ack == nil && req.Quiescence == nil && req.Stop == nil && req.GuardID == "" && req.Rows == 0 && req.Cols == 0 {
		select {
		case <-s.done:
			_ = writeStdioMessage(c, stdioResponse{Ref: s.r.Ref})
		default:
			_ = writeStdioMessage(c, stdioResponse{Ref: s.r.Ref, Alive: true, Detail: "Owned stdio process; lifecycle readiness requires private typed evidence."})
		}
		return
	}
	if req.Version != ps.Version || req.Channel == nil || *req.Channel != s.state.Channel.Ref || req.Rows != 0 || req.Cols != 0 {
		return
	}
	if req.Op == "stdio_guard" {
		s.guard(c, req)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), stdioPeerContextKey{}, c), 30*time.Second)
	defer cancel()
	// No next request is legal on ordinary one-shot IPC. EOF cancels any unsent
	// bootstrap/query steps while the helper keeps reading the native channel.
	go func() { var p [1]byte; _, _ = c.Read(p[:]); cancel() }()
	var reply stdioResponse
	valid := func(cursor, ack, quiet, stop bool) bool {
		return (req.Cursor != nil) == cursor && (req.Ack != nil) == ack && (req.Quiescence != nil) == quiet && (req.Stop != nil) == stop && req.GuardID == ""
	}
	switch req.Op {
	case "stdio_bootstrap":
		if !valid(false, false, false, false) {
			err = ps.ErrInvalid
			break
		}
		err = s.bootstrap(ctx)
		s.mu.Lock()
		from := s.state.Ack
		s.mu.Unlock()
		o, e := s.observation(from)
		if e == nil {
			reply.Observation = &o
		} else {
			err = e
		}
	case "stdio_observe":
		if !valid(true, false, false, false) {
			err = ps.ErrInvalid
			break
		}
		err = s.inspect(ctx)
		o, e := s.observation(*req.Cursor)
		if e == nil {
			reply.Observation = &o
		} else {
			err = e
		}
	case "stdio_retained":
		if !valid(true, false, false, false) {
			err = ps.ErrInvalid
			break
		}
		o, e := s.observation(*req.Cursor)
		if e == nil {
			reply.Observation = &o
		} else {
			err = e
		}
	case "stdio_ack":
		if !valid(false, true, false, false) {
			err = ps.ErrInvalid
		} else {
			err = s.acknowledge(*req.Ack)
		}
	case "stdio_stop":
		if !valid(false, false, false, true) {
			err = ps.ErrInvalid
		} else {
			err = s.stop(ctx, *req.Stop)
		}
	default:
		err = ps.ErrInvalid
	}
	reply.Error = stdioErrorCode(err)
	_ = writeStdioMessage(c, reply)
}

type stdioPeerContextKey struct{}

func stdioPeerPresent(ctx context.Context) bool {
	c, _ := ctx.Value(stdioPeerContextKey{}).(*net.UnixConn)
	if c == nil {
		return true
	}
	raw, err := c.SyscallConn()
	if err != nil {
		return false
	}
	alive := false
	if raw.Control(func(fd uintptr) {
		var peek [1]byte
		n, _, e := unix.Recvfrom(int(fd), peek[:], unix.MSG_PEEK|unix.MSG_DONTWAIT)
		alive = n > 0 || e == unix.EAGAIN || e == unix.EWOULDBLOCK
	}) != nil {
		return false
	}
	return alive
}
func (s *stdioEngine) guard(c *net.UnixConn, req stdioRequest) {
	if req.Quiescence == nil || req.Cursor != nil || req.Ack != nil || req.Stop != nil || req.GuardID != "" {
		_ = writeStdioMessage(c, stdioResponse{Error: "invalid"})
		return
	}
	// TryLock avoids an unbounded writer lock wait; callers can observe again.
	if !s.rpcMu.TryLock() {
		_ = writeStdioMessage(c, stdioResponse{Error: "conflict"})
		return
	}
	defer s.rpcMu.Unlock()
	if !s.admission.TryLock() {
		_ = writeStdioMessage(c, stdioResponse{Error: "conflict"})
		return
	}
	defer s.admission.Unlock()
	s.mu.Lock()
	valid := s.quiescentLocked(*req.Quiescence)
	s.mu.Unlock()
	if !valid {
		_ = writeStdioMessage(c, stdioResponse{Error: "conflict"})
		return
	}
	deadline := time.Now().Add(ioTimeout)
	_ = c.SetDeadline(deadline)
	id := nonce()
	if writeStdioMessage(c, stdioResponse{GuardID: id}) != nil {
		return
	}
	var release stdioRequest
	if readStdioMessage(c, &release) != nil || time.Now().After(deadline) || release.Op != "stdio_release" || release.Version != ps.Version || release.Channel == nil || *release.Channel != *req.Channel || release.Ref != req.Ref || subtle.ConstantTimeCompare([]byte(release.Auth), []byte(req.Auth)) != 1 || release.GuardID != id || release.Cursor != nil || release.Ack != nil || release.Quiescence != nil || release.Stop != nil || release.Rows != 0 || release.Cols != 0 {
		return
	}
	s.mu.Lock()
	valid = s.quiescentLocked(*req.Quiescence)
	s.mu.Unlock()
	if !valid {
		return // local commit may have happened; no success acknowledgment
	}
	_ = writeStdioMessage(c, stdioResponse{GuardID: id})
}
