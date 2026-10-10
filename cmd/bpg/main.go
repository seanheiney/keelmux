// bpg is the bounded private entry experiment. Peers may be local sockets or
// sockets forwarded by an independently authenticated SSH connection.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/seanheiney/keelmux/internal/buildinfo"
	"github.com/seanheiney/keelmux/internal/config"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/federation"
	"github.com/seanheiney/keelmux/internal/protocol"
	"github.com/seanheiney/keelmux/internal/transport"
)

type configuration struct {
	Principal string `json:"principal"`
	Peers     []struct {
		HostID domain.ID `json:"host_id"`
		Socket string    `json:"socket"`
	} `json:"peers"`
	Grants []struct {
		HostID   domain.ID   `json:"host_id"`
		Sessions []domain.ID `json:"sessions"`
	} `json:"grants"`
}

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

func readConfiguration(path string) (configuration, error) {
	var conf configuration
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return conf, errors.New("private entry configuration unavailable")
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) || info.Size() > 65536 {
		return conf, errors.New("entry configuration must be an owned regular file with mode 0600, at most 64 KiB")
	}
	d := json.NewDecoder(io.LimitReader(f, 65537))
	d.DisallowUnknownFields()
	if err := d.Decode(&conf); err != nil {
		return conf, errors.New("invalid private entry configuration")
	}
	if !errors.Is(d.Decode(new(any)), io.EOF) || conf.Principal == "" || len(conf.Peers) == 0 {
		return conf, errors.New("entry configuration needs one principal and explicit peers")
	}
	for _, peer := range conf.Peers {
		if !filepath.IsAbs(peer.Socket) || filepath.Clean(peer.Socket) != peer.Socket || len(peer.Socket) > 100 {
			return conf, errors.New("peer sockets must be explicit clean absolute Unix paths")
		}
	}
	return conf, nil
}

func run() error {
	state := flag.String("state", "", "explicit private entry state directory")
	file := flag.String("config", "", "owned mode-0600 peer/session grant configuration")
	companion := flag.String("companion", "", "client-local Keelmux executable; defaults beside entry")
	flag.Parse()
	if *state == "" || *file == "" || len(flag.Args()) != 0 {
		return errors.New("use keelmux-entry --state ENTRYDIR --config PRIVATE_JSON [--companion CLIENT_LOCAL_KEELMUX]")
	}
	conf, err := readConfiguration(*file)
	if err != nil {
		return err
	}
	p, err := config.Resolve(*state)
	if err != nil {
		return err
	}
	if err := p.Ensure(); err != nil {
		return err
	}
	lock, err := p.Lock()
	if err != nil {
		return err
	}
	defer lock.Close()
	if *companion == "" {
		self, err := os.Executable()
		if err != nil {
			return err
		}
		*companion = filepath.Join(filepath.Dir(self), "keelmux")
		if _, err := os.Stat(*companion); os.IsNotExist(err) {
			*companion = filepath.Join(filepath.Dir(self), "bp")
		} else if err != nil {
			return err
		}
	}
	if !filepath.IsAbs(*companion) {
		return errors.New("entry companion must be an absolute client-local executable")
	}
	info, err := os.Stat(*companion)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return errors.New("entry companion executable unavailable")
	}
	var peers []federation.Peer
	for _, peer := range conf.Peers {
		peers = append(peers, federation.Peer{HostID: peer.HostID, Source: transport.NewClient(peer.Socket)})
	}
	var grants []federation.Grant
	for _, grant := range conf.Grants {
		grants = append(grants, federation.Grant{Principal: conf.Principal, HostID: grant.HostID, Sessions: grant.Sessions})
	}
	entry, err := federation.New(peers, grants)
	if err != nil {
		return err
	}
	if err := entry.SetStateDirectory(filepath.Join(p.Dir, "entry-receipts")); err != nil {
		return err
	}
	defer entry.Close()
	entry.SetAttachCommand(func(r transport.Request) []string {
		l := r.Lease
		argv := []string{*companion, "--entry", p.Control, "drive", "--host", string(r.HostID), "--session", string(l.SessionID), "--execution", string(l.ExecutionID), "--generation", strconv.FormatUint(l.Generation, 10), "--operation", string(l.OperationID), "--client", l.ClientID}
		if l.ReadOnly {
			argv = append(argv, "--read-only")
		}
		return argv
	})
	l, err := p.Listen()
	if err != nil {
		return err
	}
	defer l.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	srv := &http.Server{Handler: entry.Handler(conf.Principal), BaseContext: func(net.Listener) context.Context { return ctx }, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 25 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() {
		<-ctx.Done()
		stop, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = entry.Close()
		_ = srv.Shutdown(stop)
	}()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case failure := <-entry.Failures:
				fmt.Fprintln(os.Stderr, "bpg: native attachment requires home repair:", failure)
			}
		}
	}()
	fmt.Fprintln(os.Stderr, "bpg ready:", p.Control)
	err = srv.Serve(l)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
