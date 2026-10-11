package nativepty

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
	ps "github.com/seanheiney/keelmux/internal/primarystdio"
)

func TestStdioStopSuccessWaitsForTerminalReceipt(t *testing.T) {
	s := readyStdioReceiver(t)
	s.terminalReceiptDone = make(chan struct{})
	entered, release, written := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(func() {
		unblock()
		select {
		case <-written:
		case <-time.After(time.Second):
			t.Error("owned receipt writer did not finish")
		}
	})
	go func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.markExitedLocked()
		s.commitTerminalReceiptLocked(func(receipt) error {
			close(entered)
			<-release
			return nil
		})
		close(written)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("owned receipt writer did not enter")
	}
	// Death is known, but the terminal receipt is deliberately uncommitted.
	// Cancellation must preserve uncertainty rather than acknowledge success.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := s.stopOutcome(ctx); !errors.Is(e, ps.ErrUncertain) {
		t.Fatal("child death acknowledged before terminal receipt", e)
	}
	unblock()
	<-written
	if e := s.stopOutcome(context.Background()); e != nil {
		t.Fatal("committed terminal result unavailable", e)
	}
}

func TestStdioTerminalReceiptFailureCannotAcknowledgeStop(t *testing.T) {
	s := readyStdioReceiver(t)
	s.terminalReceiptDone = make(chan struct{})
	s.markExitedLocked()
	s.commitTerminalReceiptLocked(func(receipt) error { return os.ErrPermission })
	if e := s.stopOutcome(context.Background()); !errors.Is(e, ps.ErrStorage) {
		t.Fatal("failed terminal persistence acknowledged", e)
	}
	if !errors.Is(s.terminalReceiptError, os.ErrPermission) {
		t.Fatal("receipt write outcome lost")
	}
}

// This pure transport control creates an owned synthetic terminal receipt; it
// does not claim real child death or execute a helper, native CLI or signal.
func TestStdioStopReconcilesTerminalAfterIPCClosure(t *testing.T) {
	for _, kind := range []string{"matching", "different_operation", "corrupt_spool", "not_terminal", "foreign_ref"} {
		t.Run(kind, func(t *testing.T) {
			s := readyStdioReceiver(t)
			op := prepareSyntheticStopReceipt(t, s)
			spec := s.state.Channel.Spec
			ref := s.state.Channel.Ref
			stale, e := s.b.stdioReceipt(ref)
			if e != nil {
				t.Fatal(e)
			}
			if kind != "not_terminal" {
				s.r.Stage = "stopped"
				code := 0
				s.r.ExitCode = &code
				if e = s.b.writeReceipt(s.r); e != nil {
					t.Fatal(e)
				}
			}
			request := ps.StopRequest{OperationID: op}
			if kind == "different_operation" {
				request.OperationID = domain.NewID("different")
			}
			if kind == "foreign_ref" {
				ref.Runtime.Session = nonce()
			}
			if kind == "corrupt_spool" {
				if e = os.WriteFile(s.b.stdioPath(spec.Binding.ExecutionID), []byte("terminal-corruption"), 0600); e != nil {
					t.Fatal(e)
				}
			}
			beforeReceipt, _ := os.ReadFile(s.b.receiptPath(spec.Binding.ExecutionID))
			beforeSpool, _ := os.ReadFile(s.b.stdioPath(spec.Binding.ExecutionID))
			e = s.b.stopStdioFromReceipt(context.Background(), ref, request, stale)
			switch kind {
			case "matching":
				if e != nil {
					t.Fatal("exact terminal stop lost with IPC", e)
				}
			case "different_operation":
				if !errors.Is(e, ps.ErrConflict) {
					t.Fatal("terminal operation mismatch did not return conflict", e)
				}
			case "corrupt_spool":
				if !errors.Is(e, ps.ErrStorage) {
					t.Fatal("corrupt terminal evidence did not return storage failure", e)
				}
			default:
				if e == nil {
					t.Fatal("unproven terminal stop accepted", kind)
				}
			}
			afterReceipt, _ := os.ReadFile(s.b.receiptPath(spec.Binding.ExecutionID))
			afterSpool, _ := os.ReadFile(s.b.stdioPath(spec.Binding.ExecutionID))
			if string(beforeReceipt) != string(afterReceipt) || string(beforeSpool) != string(afterSpool) {
				t.Fatal("terminal reconciliation mutated durable state")
			}
		})
	}
}

func prepareSyntheticStopReceipt(t *testing.T, s *stdioEngine) domain.ID {
	t.Helper()
	spec := s.state.Channel.Spec
	op := domain.NewID("stop")
	s.r.Version, s.r.Mode, s.r.Stage = 2, "stdio", "stopping"
	s.r.SessionID, s.r.HostID = spec.Binding.SessionID, spec.Binding.HostID
	s.r.Executable, s.r.Digest = spec.Binary, stdioLaunchDigest(spec)
	s.r.EnvDigest = environmentDigest(stdioEnvironment(spec.Environment))
	s.r.Auth, s.r.LinkToken = nonce(), nonce()
	s.r.SupervisorPID, s.r.SupervisorStart = os.Getpid(), s.r.Ref.ProcessStart
	s.state.StopOperation, s.state.TermAttempted = op, true
	s.state.Snapshot = ps.Snapshot{}
	if e := s.persistLocked(); e != nil {
		t.Fatal(e)
	}
	if e := s.b.writeReceipt(s.r); e != nil {
		t.Fatal(e)
	}
	return op
}

func TestStdioStopExplicitFailureIsNotOverriddenByVisibleReceipt(t *testing.T) {
	for _, code := range []string{"storage", "conflict", "uncertain"} {
		t.Run(code, func(t *testing.T) {
			s := readyStdioReceiver(t)
			op := prepareSyntheticStopReceipt(t, s)
			ref := s.state.Channel.Ref
			listener, e := net.ListenUnix("unix", &net.UnixAddr{Name: ref.Runtime.Socket, Net: "unix"})
			if e != nil {
				t.Fatal(e)
			}
			defer listener.Close()
			if e = os.Chmod(ref.Runtime.Socket, 0600); e != nil {
				t.Fatal(e)
			}
			served := make(chan error, 1)
			go func() {
				_ = listener.SetDeadline(time.Now().Add(time.Second))
				c, e := listener.AcceptUnix()
				if e != nil {
					served <- e
					return
				}
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(time.Second))
				var request stdioRequest
				if e = readStdioMessage(c, &request); e != nil {
					served <- e
					return
				}
				if request.Op != "stdio_stop" || request.Auth != s.r.Auth || request.Channel == nil || *request.Channel != ref || request.Stop == nil || request.Stop.OperationID != op {
					served <- ps.ErrInvalid
					return
				}
				// A rename can make a terminal file visible while the following
				// directory fsync fails. An explicit failure is authoritative.
				s.r.Stage = "stopped"
				if e = s.b.writeReceipt(s.r); e == nil {
					e = writeStdioMessage(c, stdioResponse{Error: code})
				}
				served <- e
			}()
			e = s.b.StopStdio(context.Background(), ref, ps.StopRequest{OperationID: op})
			if !errors.Is(e, stdioError(code)) {
				t.Fatal("visible receipt overrode explicit stop failure", e)
			}
			if e = <-served; e != nil {
				t.Fatal("owned IPC fixture failed", e)
			}
			if code == "storage" {
				_ = listener.Close()
				// A separate call may reconcile the earlier visible bytes only
				// after independently confirming file and directory persistence.
				if e = s.b.StopStdio(context.Background(), ref, ps.StopRequest{OperationID: op}); e != nil {
					t.Fatal("fresh local persistence reconciliation failed", e)
				}
			}
		})
	}
}
