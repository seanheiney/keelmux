package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/charmbracelet/x/term"
	"github.com/seanheiney/agent-backplane/internal/domain"
	"github.com/seanheiney/agent-backplane/internal/protocol"
	"github.com/seanheiney/agent-backplane/internal/transport"
)

func driveNative(entry string, args []string) error {
	if !filepath.IsAbs(entry) {
		return errors.New("native drive requires an explicit absolute --entry socket")
	}
	fs := flag.NewFlagSet("bp drive", flag.ContinueOnError)
	host := fs.String("host", "", "exact execution home")
	session := fs.String("session", "", "stable session")
	execution := fs.String("execution", "", "current execution")
	generation := fs.Uint64("generation", 0, "execution generation")
	operation := fs.String("operation", "", "saved acquisition operation")
	client := fs.String("client", "", "saved client identity")
	readOnly := fs.Bool("read-only", false, "explicit observer")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) != 0 {
		return errors.New("unexpected native drive arguments")
	}
	cols, rows, err := term.GetSize(os.Stdin.Fd())
	if err != nil || rows <= 0 || cols <= 0 || rows > 4096 || cols > 4096 {
		return errors.New("native drive needs a terminal with valid dimensions")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	request := transport.Request{HostID: domain.ID(*host), Lease: protocol.LeaseRequest{SessionID: domain.ID(*session), ExecutionID: domain.ID(*execution), Generation: *generation, OperationID: domain.ID(*operation), ClientID: *client, ReadOnly: *readOnly}, Rows: uint16(rows), Cols: uint16(cols), Term: os.Getenv("TERM")}
	stream, err := transport.NewClient(entry).Open(ctx, request)
	if err != nil {
		return err
	}
	defer stream.Close()
	return transport.Drive(ctx, stream, os.Stdin, os.Stdout)
}
