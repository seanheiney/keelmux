package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/seanheiney/keelmux/internal/config"
	"github.com/seanheiney/keelmux/internal/protocol"
)

func daemonAbsent(err error) bool {
	return errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)
}

func manageDaemon(p config.Paths, args []string) error {
	command := "status"
	if len(args) != 0 {
		command, args = args[0], args[1:]
	}
	if command != "status" && command != "stop" && command != "restart" {
		return errors.New("daemon accepts status, stop or restart")
	}
	fs := flag.NewFlagSet("keelmux daemon "+command, flag.ContinueOnError)
	instance := fs.String("instance", "", "retry stopping this exact daemon instance")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) != 0 || (command == "status" && *instance != "") {
		return errors.New("daemon lifecycle accepts only --instance for an exact stop/restart retry")
	}
	if _, err := os.Lstat(p.Dir); err != nil {
		if os.IsNotExist(err) && (command == "status" || command == "stop") {
			if command == "status" {
				return printJSON(map[string]any{"running": false})
			}
			fmt.Println("Daemon is not running; native sessions were preserved.")
			return nil
		}
		if !os.IsNotExist(err) || command != "restart" || *instance != "" {
			return err
		}
	}
	if command != "status" || directoryExists(p.Dir) {
		if err := p.Ensure(); err != nil {
			return err
		}
	}
	c := protocol.NewClient(p.Control)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	status, err := c.Daemon(ctx)
	if err != nil {
		if !daemonAbsent(err) {
			return fmt.Errorf("daemon lifecycle unavailable: %w; no saved PID will be signalled", err)
		}
		if command == "status" {
			return printJSON(map[string]any{"running": false})
		}
		if command == "stop" {
			fmt.Println("Daemon is not running; native sessions were preserved.")
			return nil
		}
		return ensureDaemon(ctx, c, p)
	}
	if command == "status" {
		return printJSON(map[string]any{"running": true, "daemon": status})
	}
	if *instance != "" && *instance != status.Instance {
		return errors.New("daemon instance changed; current daemon was preserved")
	}
	fmt.Fprintln(os.Stderr, "Daemon stop intent:", status.Instance)
	if err := c.ShutdownDaemon(ctx, status.Instance); err != nil {
		return fmt.Errorf("daemon stop %s: %w; retry only with --instance %s", status.Instance, err, status.Instance)
	}
	var lastError error
	for {
		observed, err := c.Daemon(ctx)
		if daemonAbsent(err) {
			break
		}
		lastError = err
		if err == nil && observed.Instance != status.Instance {
			return errors.New("daemon instance changed while waiting; replacement was preserved")
		}
		select {
		case <-ctx.Done():
			if lastError != nil {
				return fmt.Errorf("daemon stop %s outcome needs inspection: %w", status.Instance, lastError)
			}
			return fmt.Errorf("daemon stop %s outcome needs inspection: %w", status.Instance, ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	if command == "restart" {
		// Socket absence can precede release of the daemon's state lock.
		// Wait for that exact private lock, without signalling a saved PID.
		for {
			lock, err := p.Lock()
			if err == nil {
				_ = lock.Close()
				break
			}
			if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
				return err
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("daemon restart waits for its private lock: %w", ctx.Err())
			case <-time.After(50 * time.Millisecond):
			}
		}
		if err := ensureDaemon(ctx, c, p); err != nil {
			return err
		}
		fmt.Println("Daemon restarted; surviving native sessions reconciled.")
	} else {
		fmt.Println("Daemon stopped; native sessions and state were preserved.")
	}
	return nil
}

func directoryExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
