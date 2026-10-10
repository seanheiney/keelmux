package ircd

import (
	"context"
	"io"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/runtime"
)

// ownedTestSupervisor intentionally uses live os.Process handles. It is only a
// fixture for Manager journals, and makes no persistent-helper qualification.
type ownedTestSupervisor struct {
	mu                   sync.Mutex
	dir                  string
	records              map[domain.ID]*testProcess
	launches, stops      int
	loseLaunch, loseStop bool
}
type testProcess struct {
	ref   domain.RuntimeRef
	cmd   *exec.Cmd
	done  chan struct{}
	alive bool
}

func newTestSupervisor(dir string) *ownedTestSupervisor {
	return &ownedTestSupervisor{dir: dir, records: map[domain.ID]*testProcess{}}
}
func (s *ownedTestSupervisor) LaunchProcess(ctx context.Context, spec domain.LaunchSpec) (domain.RuntimeRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.records[spec.Session.Execution.ID]; ok {
		return domain.RuntimeRef{}, ErrConflict
	}
	if spec.Validate() != nil {
		return domain.RuntimeRef{}, ErrConflict
	}
	s.launches++
	args := append([]string{"-c", `umask 077; exec "$@"`, "private-test-server"}, spec.Argv...)
	cmd := exec.Command("/bin/sh", args...)
	cmd.Dir = spec.Session.Workspace
	cmd.Env = spec.Env
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if e := cmd.Start(); e != nil {
		return domain.RuntimeRef{}, e
	}
	ref := domain.RuntimeRef{Backend: "owned_test_fixture", Socket: filepath.Join(s.dir, "test-reference"), ExecutionID: spec.Session.Execution.ID, OperationID: spec.OperationID, Session: string(spec.Session.ID), PID: cmd.Process.Pid}
	p := &testProcess{ref: ref, cmd: cmd, done: make(chan struct{}), alive: true}
	s.records[ref.ExecutionID] = p
	go func() { cmd.Wait(); s.mu.Lock(); p.alive = false; s.mu.Unlock(); close(p.done) }()
	if s.loseLaunch {
		return domain.RuntimeRef{}, ErrUnavailable
	}
	return ref, nil
}
func (s *ownedTestSupervisor) Lookup(ctx context.Context, execution, operation domain.ID) (runtime.Observation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.records[execution]
	if !ok || p.ref.OperationID != operation {
		return runtime.Observation{}, ErrUnavailable
	}
	return runtime.Observation{Ref: p.ref, Alive: p.alive}, nil
}
func (s *ownedTestSupervisor) Inspect(ctx context.Context, ref domain.RuntimeRef) (runtime.Observation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.records[ref.ExecutionID]
	if !ok || p.ref != ref {
		return runtime.Observation{}, ErrUnavailable
	}
	return runtime.Observation{Ref: p.ref, Alive: p.alive}, nil
}
func (s *ownedTestSupervisor) Stop(ctx context.Context, ref domain.RuntimeRef) error {
	s.mu.Lock()
	p, ok := s.records[ref.ExecutionID]
	if !ok || p.ref != ref {
		s.mu.Unlock()
		return ErrConflict
	}
	if !p.alive {
		s.mu.Unlock()
		return nil
	}
	s.stops++
	lost := s.loseStop
	s.mu.Unlock()
	if p.cmd.Process.Signal(syscall.SIGTERM) != nil {
		return ErrUnavailable
	}
	select {
	case <-p.done:
		if lost {
			return ErrUnavailable
		}
		return nil
	case <-ctx.Done():
		return ErrUncertain
	case <-time.After(5 * time.Second):
		return ErrUncertain
	}
}
func (s *ownedTestSupervisor) cleanup() {
	s.mu.Lock()
	all := make([]*testProcess, 0, len(s.records))
	for _, p := range s.records {
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
