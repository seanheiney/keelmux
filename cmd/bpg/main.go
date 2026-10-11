// bpg is the bounded private entry experiment. Peers may be local sockets or
// sockets forwarded by an independently authenticated SSH connection.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/seanheiney/keelmux/internal/buildinfo"
	"github.com/seanheiney/keelmux/internal/config"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/federation"
	"github.com/seanheiney/keelmux/internal/peers"
	"github.com/seanheiney/keelmux/internal/protocol"
	"github.com/seanheiney/keelmux/internal/transport"
)

type configuration struct {
	Principal string `json:"principal"`
	Peers     []struct {
		HostID         domain.ID            `json:"host_id"`
		Socket         string               `json:"socket"`
		InventoryStore string               `json:"inventory_store,omitempty"`
		TerminalRead   *peers.ReadSelection `json:"terminal_read,omitempty"`
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
	if err := config.ReadPrivateJSON(path, 65536, &conf); err != nil {
		return conf, err
	}
	if conf.Principal == "" || len(conf.Principal) > 128 || conf.Principal != strings.TrimSpace(conf.Principal) || !utf8.ValidString(conf.Principal) || strings.ContainsFunc(conf.Principal, unicode.IsControl) || len(conf.Peers) == 0 || len(conf.Peers) > peers.MaxPeers {
		return conf, errors.New("entry configuration needs one bounded principal and explicit peers")
	}
	seen := map[domain.ID]bool{}
	for _, peer := range conf.Peers {
		if peer.HostID == "" || seen[peer.HostID] {
			return conf, errors.New("entry peers require distinct exact home identities")
		}
		seen[peer.HostID] = true
		if (peer.Socket == "") == (peer.InventoryStore == "") {
			return conf, errors.New("select exactly one peer socket or enrolled inventory_store")
		}
		if peer.Socket != "" && (!filepath.IsAbs(peer.Socket) || filepath.Clean(peer.Socket) != peer.Socket || len(peer.Socket) > 100) {
			return conf, errors.New("peer sockets must be explicit clean absolute Unix paths")
		}
		if peer.InventoryStore != "" && (!filepath.IsAbs(peer.InventoryStore) || filepath.Clean(peer.InventoryStore) != peer.InventoryStore || len(peer.InventoryStore) > 4096 || !domain.ValidObjectID(peer.HostID, "host")) {
			return conf, errors.New("SSH inventory needs an existing absolute inventory_store and exact host ID")
		}
		if peer.TerminalRead != nil {
			read := peer.TerminalRead
			if peer.InventoryStore == "" || !filepath.IsAbs(read.PolicyPath) || filepath.Clean(read.PolicyPath) != read.PolicyPath || len(read.PolicyPath) > 4096 || !utf8.ValidString(read.PolicyPath) || strings.ContainsFunc(read.PolicyPath, unicode.IsControl) || !domain.ValidObjectID(read.ScopeID, "scope") {
				return conf, errors.New("terminal_read requires an enrolled inventory store and explicit canonical home policy selector")
			}
		}
	}
	return conf, nil
}

func run() (resultErr error) {
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
	peers, closePeers, err := entrySources(conf)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, closePeers()) }()
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
