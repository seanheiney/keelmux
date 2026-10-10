package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/seanheiney/keelmux/internal/adapters/tmux"
	"github.com/seanheiney/keelmux/internal/buildinfo"
	"github.com/seanheiney/keelmux/internal/config"
	"github.com/seanheiney/keelmux/internal/daemon"
	"github.com/seanheiney/keelmux/internal/protocol"
	"github.com/seanheiney/keelmux/internal/store"
	"github.com/seanheiney/keelmux/internal/transport"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println(buildinfo.String(protocol.Version))
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, filepath.Base(os.Args[0])+":", err)
		os.Exit(1)
	}
}
func run() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	helper := filepath.Join(filepath.Dir(self), "keelmux")
	if _, err := os.Stat(helper); os.IsNotExist(err) {
		helper = filepath.Join(filepath.Dir(self), "bp")
	} else if err != nil {
		return err
	}
	if err := os.Setenv("BP_HELPER_PATH", helper); err != nil {
		return err
	}
	state := flag.String("state", "", "private state directory")
	flag.Parse()
	p, err := config.Resolve(*state)
	if err != nil {
		return err
	}
	if err = p.Ensure(); err != nil {
		return err
	}
	lock, err := p.Lock()
	if err != nil {
		return err
	}
	defer lock.Close()
	host, err := p.HostID()
	if err != nil {
		return err
	}
	db, err := store.Open(p.DB)
	if err != nil {
		return err
	}
	defer db.Close()
	backend, err := tmux.New(p.Tmux)
	if err != nil {
		return err
	}
	svc, err := daemon.New(db, backend, host)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err = svc.Reconcile(ctx); err != nil {
		return fmt.Errorf("startup reconciliation: %w", err)
	}
	terminal, err := transport.NewHost(svc, db, host)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("/v1/terminal", terminal.Handler())
	mux.Handle("/", svc.Handler())
	l, err := p.Listen()
	if err != nil {
		return err
	}
	defer l.Close()
	srv := &http.Server{Handler: mux, BaseContext: func(net.Listener) context.Context { return ctx }, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 25 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case failure := <-terminal.Failures:
				fmt.Fprintln(os.Stderr, "bpd: terminal cleanup requires repair:", failure)
			}
		}
	}()
	go func() {
		<-ctx.Done()
		stop, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = srv.Shutdown(stop)
	}()
	fmt.Fprintln(os.Stderr, "bpd ready:", p.Control)
	err = srv.Serve(l)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
