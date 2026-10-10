package ircdchannel

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/channels"
	"github.com/seanheiney/keelmux/internal/chatadapter"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/ircd"
	"github.com/seanheiney/keelmux/internal/runtime"
)

// This opt-in test only operates on a newly provisioned pinned Ergo instance.
// Its supervisor is an owned process fixture, not a daemon persistence
// claim. No native CLI login, model turn, or external/customer IRCD is touched.
func TestRealPinnedRegisteredChannels(t *testing.T) {
	archive := os.Getenv("KEELMUX_ERGO_ARCHIVE")
	if archive == "" {
		t.Skip("explicit pinned real Ergo archive required")
	}
	raw, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "server")
	supervisor := &ownedSupervisor{dir: dir, processes: map[domain.ID]*ownedProcess{}}
	t.Cleanup(supervisor.cleanup)
	m, err := ircd.NewWithSupervisor(dir, supervisor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.InstallArchive(context.Background(), bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	cfg, err := m.Init(context.Background(), ircd.InitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	status, err := m.Start(ctx, domain.NewID("operation"))
	if err != nil || !status.Alive {
		t.Fatal("owned managed Ergo start", err)
	}
	if _, err = m.EnsureAccount(ctx, cfg.Account); err != nil {
		t.Fatal("owned managed Ergo account", err)
	}
	var traceMu sync.Mutex
	seenTrace := map[string]bool{}
	counted := &realRuntime{ManagedRuntime: m, trace: func(msg message) {
		if msg.command == "900" || msg.command == "903" || msg.command == "001" {
			traceMu.Lock()
			defer traceMu.Unlock()
			if !seenTrace[msg.command] {
				seenTrace[msg.command] = true
				t.Logf("owned server identity %s %q", msg.command, msg.params)
			}
		}
	}}
	s, err := New(counted)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	thread, op := domain.NewID("thread"), domain.NewID("operation")
	route, err := s.Ensure(ctx, thread, op)
	if err != nil {
		t.Fatal("real TLS/SASL/JOIN/ChanServ registration", err)
	}
	if route.OperationID != op || route.ServerID != cfg.ID || route.Account != cfg.Account || route.RegisteredAt == "" {
		t.Fatal("real registered route missing immutable evidence")
	}
	again, err := s.Ensure(ctx, thread, domain.NewID("operation"))
	if err != nil || again.OperationID != op || again.RegisteredAt != route.RegisteredAt {
		t.Fatal("real same-thread retry", err)
	}
	if counted.count() != 1 {
		t.Fatal("real retry opened mutation connection")
	}
	if _, _, err = m.BeginChannel(ctx, route.Channel, domain.NewID("thread"), domain.NewID("operation")); err != ircd.ErrConflict {
		t.Fatal("real journal allowed channel/thread collision", err)
	}
	if _, err = m.Dial(ctx, "127.0.0.1:1"); err == nil {
		t.Fatal("managed TLS dial allowed foreign authority")
	}
	// Keep the existing authored-communications adapter connected while two
	// distinct thread registrations authenticate concurrently with the same bot
	// account. Unique per-connection nicks must preserve exact account authority.
	adapter := chatadapter.NewIRCWithDialer(m.Dial)
	t.Cleanup(func() { adapter.Close() })
	binding := channels.Binding{ID: domain.NewID("binding"), SessionID: domain.NewID("session"), ThreadID: thread, Provider: channels.IRC, Endpoint: route.Endpoint, RealmID: route.RealmID, SelfID: route.Account, ChannelID: route.Channel, CredentialRef: route.CredentialRef}
	proof, err := adapter.Probe(ctx, binding)
	if err != nil || proof.RealmID != route.RealmID || proof.SelfID != route.Account {
		t.Fatal("real authored adapter account proof", err)
	}
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			parallelThread := domain.NewID("thread")
			parallelRoute, e := s.Ensure(ctx, parallelThread, domain.NewID("operation"))
			if e == nil && (parallelRoute.ThreadID != parallelThread || parallelRoute.Account != cfg.Account) {
				e = ErrScope
			}
			results <- e
		}()
	}
	for i := 0; i < 2; i++ {
		if err = <-results; err != nil {
			t.Fatal("concurrent real registrars with authored adapter", err)
		}
	}
	if counted.count() != 3 {
		t.Fatal("parallel registrations repeated mutation")
	}
	// Lose the actual services acknowledgement after Ergo has written it. The
	// manager's authenticated read-only proof must recover the original route.
	counted.drop = true
	lostThread := domain.NewID("thread")
	lost, err := s.Ensure(ctx, lostThread, domain.NewID("operation"))
	if err != nil || lost.RegisteredAt == "" {
		t.Fatal("real lost ACK was not confirmed read-only", err)
	}
	if counted.count() != 4 {
		t.Fatal("real lost ACK repeated mutation")
	}
	// Restart the exact owned server and reconstruct both manager and service.
	// Durable Ergo registration and full thread journal retain their incarnation.
	s.Close()
	adapter.Close()
	status, err = m.Restart(ctx, status.Instance, domain.NewID("operation"))
	if err != nil || !status.Alive {
		t.Fatal("owned managed Ergo restart", err)
	}
	newManager, err := ircd.NewWithSupervisor(dir, supervisor)
	if err != nil {
		t.Fatal(err)
	}
	counted2 := &realRuntime{ManagedRuntime: newManager}
	recovered, err := New(counted2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { recovered.Close() })
	for {
		again, err = recovered.Ensure(ctx, thread, domain.NewID("operation"))
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("real registered channel restart confirmation", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if again.ServerID != route.ServerID || again.ThreadID != thread || again.Channel != route.Channel || again.OperationID != op || again.RegisteredAt != route.RegisteredAt {
		t.Fatal("restart changed registered channel identity")
	}
	if counted2.count() != 0 {
		t.Fatal("restart replayed channel creation")
	}
	if err = m.Stop(ctx, status.Instance); err != nil {
		t.Fatal("owned managed Ergo stop", err)
	}
	t.Log("pinned Ergo", ircd.Version, "source", ircd.SourceCommit, "real TLS SASL account+nick, ChanServ registration, lost ACK read-only proof, managed start/restart/stop via owned process fixture, durable route passed")
}

// This fixture uses exact process handles and never a shared socket or tmux.
// It only proves the manager's lifecycle/account/channel boundary. The product
// native persistent helper and daemon restart require separate acceptance.
type ownedSupervisor struct {
	mu        sync.Mutex
	dir       string
	processes map[domain.ID]*ownedProcess
}
type ownedProcess struct {
	ref   domain.RuntimeRef
	cmd   *exec.Cmd
	done  chan struct{}
	alive bool
}

func (s *ownedSupervisor) LaunchProcess(ctx context.Context, spec domain.LaunchSpec) (domain.RuntimeRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if spec.Validate() != nil {
		return domain.RuntimeRef{}, ircd.ErrConflict
	}
	if _, ok := s.processes[spec.Session.Execution.ID]; ok {
		return domain.RuntimeRef{}, ircd.ErrConflict
	}
	args := append([]string{"-c", `umask 077; exec "$@"`, "private-channel-test"}, spec.Argv...)
	cmd := exec.Command("/bin/sh", args...)
	cmd.Dir = spec.Session.Workspace
	cmd.Env = spec.Env
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return domain.RuntimeRef{}, err
	}
	ref := domain.RuntimeRef{Backend: "owned_process_fixture", Socket: filepath.Join(s.dir, "test-reference"), ExecutionID: spec.Session.Execution.ID, OperationID: spec.OperationID, PID: cmd.Process.Pid, Session: string(spec.Session.ID)}
	p := &ownedProcess{ref: ref, cmd: cmd, done: make(chan struct{}), alive: true}
	s.processes[ref.ExecutionID] = p
	go func() { cmd.Wait(); s.mu.Lock(); p.alive = false; s.mu.Unlock(); close(p.done) }()
	return ref, nil
}
func (s *ownedSupervisor) Lookup(ctx context.Context, execution, operation domain.ID) (runtime.Observation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.processes[execution]
	if p == nil || p.ref.OperationID != operation {
		return runtime.Observation{}, ircd.ErrUnavailable
	}
	return runtime.Observation{Ref: p.ref, Alive: p.alive}, nil
}
func (s *ownedSupervisor) Inspect(ctx context.Context, ref domain.RuntimeRef) (runtime.Observation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.processes[ref.ExecutionID]
	if p == nil || p.ref != ref {
		return runtime.Observation{}, ircd.ErrUnavailable
	}
	return runtime.Observation{Ref: p.ref, Alive: p.alive}, nil
}
func (s *ownedSupervisor) Stop(ctx context.Context, ref domain.RuntimeRef) error {
	s.mu.Lock()
	p := s.processes[ref.ExecutionID]
	if p == nil || p.ref != ref {
		s.mu.Unlock()
		return ircd.ErrConflict
	}
	alive := p.alive
	s.mu.Unlock()
	if !alive {
		return nil
	}
	if p.cmd.Process.Signal(syscall.SIGTERM) != nil {
		return ircd.ErrUnavailable
	}
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ircd.ErrUncertain
	}
}
func (s *ownedSupervisor) cleanup() {
	s.mu.Lock()
	all := make([]*ownedProcess, 0, len(s.processes))
	for _, p := range s.processes {
		all = append(all, p)
	}
	s.mu.Unlock()
	for _, p := range all {
		p.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-p.done:
		case <-time.After(5 * time.Second):
			p.cmd.Process.Kill()
			<-p.done
		}
	}
}

type realRuntime struct {
	ManagedRuntime
	mu    sync.Mutex
	dials int
	drop  bool
	trace func(message)
}

func (r *realRuntime) Dial(ctx context.Context, authority string) (net.Conn, error) {
	conn, err := r.ManagedRuntime.Dial(ctx, authority)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.dials++
	drop := r.drop
	r.mu.Unlock()
	if drop || r.trace != nil {
		return &lostACKConn{Conn: conn, reader: bufio.NewReader(conn), drop: drop, trace: r.trace}, nil
	}
	return conn, nil
}
func (r *realRuntime) count() int { r.mu.Lock(); defer r.mu.Unlock(); return r.dials }

type lostACKConn struct {
	net.Conn
	reader  *bufio.Reader
	pending []byte
	drop    bool
	trace   func(message)
}

func (c *lostACKConn) Read(buf []byte) (int, error) {
	if len(c.pending) == 0 {
		line, err := c.reader.ReadBytes('\n')
		if err != nil {
			return 0, err
		}
		if c.trace != nil {
			if msg, err := parse(strings.TrimSuffix(string(line), "\r\n")); err == nil {
				c.trace(msg)
			}
		}
		if c.drop && strings.Contains(string(line), " NOTICE ") && strings.Contains(string(line), "successfully registered") {
			c.Conn.Close()
			return 0, io.EOF
		}
		c.pending = line
	}
	n := copy(buf, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}
