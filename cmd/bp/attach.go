package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/seanheiney/agent-backplane/internal/clientstate"
	"github.com/seanheiney/agent-backplane/internal/config"
	"github.com/seanheiney/agent-backplane/internal/domain"
	"github.com/seanheiney/agent-backplane/internal/protocol"
	"os"
	"os/exec"
	"path/filepath"
)

func directAttach(ctx context.Context, c *protocol.Client, p config.Paths, s domain.Session, readOnly bool) error {
	path := filepath.Join(p.Dir, "cli-place.json")
	lock, err := clientstate.AcquireLock(path)
	if err != nil {
		return err
	}
	defer lock.Close()
	state, err := clientstate.Load(path)
	if err != nil {
		return err
	}
	if state.Pending != nil && state.Pending.Token != "" {
		r := attachmentRequest(*state.Pending)
		if err = c.Release(ctx, r); err != nil {
			return fmt.Errorf("prior attachment still requires reconciliation: %w", err)
		}
		state.Pending = nil
		if err = clientstate.Save(path, state); err != nil {
			return err
		}
	}
	if state.Pending != nil {
		a := state.Pending
		if a.SessionID != s.ID || a.ExecutionID != s.Execution.ID || a.Generation != s.Execution.Generation || a.ReadOnly != readOnly {
			return errors.New("prior attach outcome unknown for another target; recover that exact attachment before opening new work")
		}
	} else {
		state.Pending = &clientstate.Attachment{SessionID: s.ID, ExecutionID: s.Execution.ID, Generation: s.Execution.Generation, OperationID: domain.NewID("attach"), ClientID: state.ClientID, ReadOnly: readOnly}
		if err = clientstate.Save(path, state); err != nil {
			return err
		}
	}
	req := attachmentRequest(*state.Pending)
	fmt.Fprintln(os.Stderr, "Attachment intent:", req.OperationID)
	a, err := c.Attach(ctx, req)
	if err != nil {
		var rejection *protocol.HTTPError
		if errors.As(err, &rejection) && rejection.StatusCode >= 400 && rejection.StatusCode < 500 {
			state.Pending = nil
			_ = clientstate.Save(path, state)
		}
		return fmt.Errorf("attach: %w; retry same target to reconcile saved intent", err)
	}
	l := a.Lease
	if len(a.Argv) == 0 || l.SessionID != req.SessionID || l.ExecutionID != req.ExecutionID || l.Generation != req.Generation || l.OperationID != req.OperationID || l.ClientID != req.ClientID || l.Token == "" || a.ReadOnly != readOnly {
		return errors.New("attach reply identity changed; saved intent requires reconciliation")
	}
	state.Pending.Token = l.Token
	if err = clientstate.Save(path, state); err != nil {
		return fmt.Errorf("persist attachment token: %w; saved intent retained", err)
	}
	req.Token = l.Token
	cmd := exec.Command(a.Argv[0], a.Argv[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	nativeErr := cmd.Run()
	if err = c.Release(ctx, req); err != nil {
		return fmt.Errorf("native attachment ended (%v), release uncertain: %w; retry same target", nativeErr, err)
	}
	state.Pending = nil
	if err = clientstate.Save(path, state); err != nil {
		return err
	}
	return nativeErr
}
func attachmentRequest(a clientstate.Attachment) protocol.LeaseRequest {
	return protocol.LeaseRequest{SessionID: a.SessionID, ExecutionID: a.ExecutionID, Generation: a.Generation, OperationID: a.OperationID, ClientID: a.ClientID, ReadOnly: a.ReadOnly, Token: a.Token}
}
