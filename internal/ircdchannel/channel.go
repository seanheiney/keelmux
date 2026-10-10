// Package ircdchannel registers stable thread addresses on an owned Ergo IRCD.
// Registration is journaled before any JOIN and is never blindly repeated.
package ircdchannel

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/ircd"
)

var (
	ErrScope          = errors.New("managed IRC channel scope is invalid")
	ErrAuthentication = errors.New("managed IRC channel authentication was not confirmed")
	ErrTransport      = errors.New("managed IRC channel transport failed")
	ErrUncertain      = errors.New("managed IRC channel outcome uncertain; read-only confirmation required")
	ErrClosed         = errors.New("managed IRC channel service is closed")
)

// ManagedRuntime is implemented by ircd.Manager. Its Dial must restrict trust
// and authority to the exact provisioned loopback endpoint and private CA.
// BeginChannel grants mutation permission only for a newly durable attempt.
// ConfirmChannel never mutates the IRCD: it proves registration owner and the
// immutable full thread/server route against the journal and authenticated API.
type ManagedRuntime interface {
	Config() (ircd.Config, error)
	EnsureAccount(context.Context, string) (ircd.Credential, error)
	BeginChannel(context.Context, string, domain.ID, domain.ID) (ircd.ChannelOperation, bool, error)
	ConfirmChannel(context.Context, string, domain.ID) (ircd.ChannelOperation, error)
	Dial(context.Context, string) (net.Conn, error)
}

// Route is registration evidence, not authority to dispatch native/model input.
// Its private credential reference must not be included in public status views.
type Route struct {
	ServerID, ThreadID, OperationID                            domain.ID
	Endpoint, RealmID, Account, CredentialRef, CAFile, Channel string
	RegisteredAt                                               string
	ConfirmedAt                                                time.Time
}

// ChannelName uses the full digest, rather than a lossy display name or runtime
// locator. The manager additionally journals the full thread ID and rejects any
// collision or attempt to replace the channel's original owner/registration.
func ChannelName(threadID domain.ID) (string, error) {
	if !validID(threadID, "thread") {
		return "", ErrScope
	}
	digest := sha256.Sum256([]byte(threadID))
	return "#keelmux-" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(digest[:])), nil
}

func validID(id domain.ID, prefix string) bool {
	s := string(id)
	if !strings.HasPrefix(s, prefix+"_") || len(s) != len(prefix)+33 {
		return false
	}
	for _, c := range s[len(prefix)+1:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func validOperationID(id domain.ID) bool { return validID(id, "operation") || validID(id, "op") }

func validateConfig(c ircd.Config) error {
	u, err := url.Parse(c.Endpoint)
	if err != nil || !validID(c.ID, "ircd") || c.Account != "keelmux-bot" || u.Scheme != "ircs" || u.Host != c.RealmID || u.String() != c.Endpoint || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return ErrScope
	}
	host, port, err := net.SplitHostPort(c.RealmID)
	n, e := strconv.Atoi(port)
	if err != nil || e != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port || host != "127.0.0.1" || net.JoinHostPort(host, port) != c.RealmID {
		return ErrScope
	}
	for _, p := range []string{c.CredentialRef, c.CAFile} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p {
			return ErrScope
		}
	}
	return nil
}

type Service struct {
	runtime ManagedRuntime
	mu      sync.Mutex
	closed  bool
	next    uint64
	active  map[uint64]context.CancelFunc
	wg      sync.WaitGroup
	// All protocol work is synchronous, bounded by context and a socket deadline.
	// No reader goroutine or connection survives an Ensure call.
	timeout time.Duration
}

func New(runtime ManagedRuntime) (*Service, error) {
	if runtime == nil {
		return nil, ErrScope
	}
	c, err := runtime.Config()
	if err != nil {
		return nil, err
	}
	if err = validateConfig(c); err != nil {
		return nil, err
	}
	return &Service{runtime: runtime, active: map[uint64]context.CancelFunc{}, timeout: 10 * time.Second}, nil
}

func (s *Service) enter(parent context.Context) (context.Context, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil, ErrClosed
	}
	ctx, cancel := context.WithCancel(parent)
	s.next++
	id := s.next
	s.active[id] = cancel
	s.wg.Add(1)
	return ctx, func() {
		cancel()
		s.mu.Lock()
		delete(s.active, id)
		s.mu.Unlock()
		s.wg.Done()
	}, nil
}

// Ensure either performs one newly journaled registration or only confirms an
// existing attempt. A lost ACK, timeout, crash, or server rejection never grants
// permission to replay JOIN/REGISTER under a new operation ID.
func (s *Service) Ensure(parent context.Context, threadID, operationID domain.ID) (Route, error) {
	channel, err := ChannelName(threadID)
	if err != nil || !validOperationID(operationID) {
		return Route{}, ErrScope
	}
	ctx, leave, err := s.enter(parent)
	if err != nil {
		return Route{}, err
	}
	defer leave()
	cfg, err := s.runtime.Config()
	if err != nil {
		return Route{}, err
	}
	if err = validateConfig(cfg); err != nil {
		return Route{}, err
	}
	credential, err := s.runtime.EnsureAccount(ctx, cfg.Account)
	if err != nil {
		return Route{}, err
	}
	if credential.Account != cfg.Account || credential.CredentialRef != cfg.CredentialRef {
		return Route{}, ErrScope
	}
	secret, err := loadSecret(cfg)
	if err != nil {
		return Route{}, err
	}
	op, fresh, err := s.runtime.BeginChannel(ctx, channel, threadID, operationID)
	if err != nil {
		return Route{}, err
	}
	if !matchesOperation(op, cfg, channel, threadID) || fresh && (op.ID != operationID || op.State != "attempted") {
		return Route{}, ErrScope
	}
	if !fresh {
		return s.confirm(ctx, cfg, channel, threadID)
	}
	// Permission has now been consumed even if the connection fails before JOIN.
	// This deliberately leaves an attempted journal for read-only repair.
	attemptCtx, cancel := context.WithTimeout(ctx, s.timeout)
	mutationErr := s.register(attemptCtx, cfg, secret, channel)
	cancel()
	route, confirmErr := s.confirm(ctx, cfg, channel, threadID)
	if confirmErr == nil {
		return route, nil
	}
	if mutationErr != nil && (errors.Is(mutationErr, ErrAuthentication) || errors.Is(mutationErr, ErrScope)) {
		return Route{}, mutationErr
	}
	return Route{}, ErrUncertain
}

func matchesOperation(op ircd.ChannelOperation, c ircd.Config, channel string, threadID domain.ID) bool {
	return validOperationID(op.ID) && op.ServerID == c.ID && op.ThreadID == threadID && op.Channel == channel && (op.Owner == "" || op.Owner == c.Account)
}

func (s *Service) confirm(ctx context.Context, cfg ircd.Config, channel string, threadID domain.ID) (Route, error) {
	bounded, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	op, err := s.runtime.ConfirmChannel(bounded, channel, threadID)
	if err != nil {
		return Route{}, ErrUncertain
	}
	registered, err := time.Parse(time.RFC3339Nano, op.RegisteredAt)
	if !matchesOperation(op, cfg, channel, threadID) || op.State != "confirmed" || op.Owner != cfg.Account || err != nil || registered.IsZero() || op.UpdatedAt.IsZero() {
		return Route{}, ErrScope
	}
	return Route{ServerID: cfg.ID, ThreadID: threadID, OperationID: op.ID, Endpoint: cfg.Endpoint, RealmID: cfg.RealmID, Account: cfg.Account, CredentialRef: cfg.CredentialRef, CAFile: cfg.CAFile, Channel: channel, RegisteredAt: op.RegisteredAt, ConfirmedAt: op.UpdatedAt}, nil
}

// Close cancels and waits for every owned attempt, including blocked dials and
// reads. The manager lifecycle and unrelated IRC clients remain untouched.
func (s *Service) Close() error {
	s.mu.Lock()
	s.closed = true
	for _, cancel := range s.active {
		cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
	return nil
}
