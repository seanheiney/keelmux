package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/seanheiney/keelmux/internal/adapters/nativepty"
	"github.com/seanheiney/keelmux/internal/adapters/owned"
	"github.com/seanheiney/keelmux/internal/buildinfo"
	"github.com/seanheiney/keelmux/internal/channelservice"
	"github.com/seanheiney/keelmux/internal/config"
	"github.com/seanheiney/keelmux/internal/daemon"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/ircd"
	"github.com/seanheiney/keelmux/internal/ircdservice"
	"github.com/seanheiney/keelmux/internal/projectservice"
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
	nativeDir := filepath.Join(p.Dir, "native")
	native, err := nativepty.New(nativeDir)
	if err != nil {
		return err
	}
	backend, err := owned.New(native, nativeDir, p.Tmux)
	if err != nil {
		return err
	}
	svc, err := daemon.New(db, backend, host)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ircManager, err := ircd.NewWithSupervisor(filepath.Join(p.Dir, "irc"), native)
	if err != nil {
		return err
	}
	registrar := &localRegistrar{manager: ircManager}
	defer registrar.Close()
	chat, err := channelservice.New(ctx, db, localChatFactory(ircManager))
	if err != nil {
		return fmt.Errorf("channel delivery recovery: %w", err)
	}
	ircService, err := ircdservice.New(ircManager, registrar, chat, db)
	if err != nil {
		return err
	}
	projects, err := projectservice.New(db, host)
	if err != nil {
		return err
	}
	if err = svc.Reconcile(ctx); err != nil {
		return fmt.Errorf("startup reconciliation: %w", err)
	}
	terminal, err := transport.NewHost(svc, db, host)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	lifecycle := daemon.LifecycleHandler(protocol.DaemonStatus{Instance: string(domain.NewID("daemon")), PID: os.Getpid(), Protocol: protocol.Version, Build: buildinfo.Current(protocol.Version)}, cancel)
	mux.Handle("/v1/daemon", lifecycle)
	mux.Handle("/v1/daemon/stop", lifecycle)
	mux.Handle("/v1/terminal", terminal.Handler())
	mux.Handle("/v1/ircd/", ircService.Handler())
	mux.Handle("/v1/channels", chat.Handler())
	mux.Handle("/v1/channels/", chat.Handler())
	mux.Handle("/v1/projects", projects.Handler())
	mux.Handle("/v1/projects/", projects.Handler())
	mux.Handle("/", svc.Handler())
	l, err := p.Listen()
	if err != nil {
		return err
	}
	defer l.Close()
	srv := &http.Server{Handler: mux, BaseContext: func(net.Listener) context.Context { return ctx }, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 25 * time.Second, MaxHeaderBytes: 16 << 10}
	chatDone := make(chan struct{})
	go func() { defer close(chatDone); chat.Run(ctx) }()
	defer func() {
		cancel()
		stop, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		_ = srv.Shutdown(stop)
		<-chatDone
		_ = chat.Close()
	}()
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
