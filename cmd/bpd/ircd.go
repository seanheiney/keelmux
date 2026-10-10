package main

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/seanheiney/keelmux/internal/channels"
	"github.com/seanheiney/keelmux/internal/chatadapter"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/ircd"
	"github.com/seanheiney/keelmux/internal/ircdchannel"
)

// The optional realm is initialized only by an explicit local command. Creating
// the daemon or inspecting unrelated sessions never downloads/starts a server.
type localRegistrar struct {
	manager *ircd.Manager
	mu      sync.Mutex
	service *ircdchannel.Service
}

func (r *localRegistrar) EnsureProject(ctx context.Context, project ircdchannel.Project, thread, operation domain.ID) (ircdchannel.Route, error) {
	r.mu.Lock()
	if r.service == nil {
		s, e := ircdchannel.New(r.manager)
		if e != nil {
			r.mu.Unlock()
			return ircdchannel.Route{}, e
		}
		r.service = s
	}
	s := r.service
	r.mu.Unlock()
	return s.EnsureProject(ctx, project, thread, operation)
}
func (r *localRegistrar) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.service != nil {
		return r.service.Close()
	}
	return nil
}
func localChatFactory(manager *ircd.Manager) func(channels.Provider) (channels.Adapter, error) {
	return func(provider channels.Provider) (channels.Adapter, error) {
		if provider != channels.IRC {
			return chatadapter.New(provider)
		}
		return chatadapter.NewIRCWithDialer(func(ctx context.Context, authority string) (net.Conn, error) {
			cfg, e := manager.Config()
			if e == nil && cfg.RealmID == authority {
				return manager.Dial(ctx, authority)
			}
			// Corrupt or incomplete managed configuration must be repaired before
			// choosing a trust scope. An absent optional realm permits normal
			// external IRC through the host's verified public trust roots.
			if e != nil && !errors.Is(e, ircd.ErrNotInitialized) {
				return nil, e
			}
			host, _, split := net.SplitHostPort(authority)
			if split != nil {
				return nil, channels.ErrInvalid
			}
			dial := tls.Dialer{NetDialer: &net.Dialer{Timeout: 10 * time.Second}, Config: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}}
			return dial.DialContext(ctx, "tcp", authority)
		}), nil
	}
}
