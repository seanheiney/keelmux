package ircdchannel

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/ircd"
)

type fixtureRuntime struct {
	mu                         sync.Mutex
	cfg                        ircd.Config
	op                         ircd.ChannelOperation
	registered                 bool
	placeholder                bool
	confirmErr                 error
	proof                      func(*ircd.ChannelOperation)
	dials, mutations, confirms int
	script                     func(net.Conn) error
	dial                       func(context.Context) (net.Conn, error)
	errors                     chan error
	wg                         sync.WaitGroup
}

func fixture(t *testing.T) (*fixtureRuntime, *Service) {
	t.Helper()
	dir := t.TempDir()
	c := ircd.Config{ID: domain.NewID("ircd"), Endpoint: "ircs://127.0.0.1:6697", RealmID: "127.0.0.1:6697", Account: "keelmux-bot", CredentialRef: filepath.Join(dir, "bot.json"), CAFile: filepath.Join(dir, "ca.pem")}
	raw, _ := json.Marshal(botSecret{Account: c.Account, Password: strings.Repeat("a", 64)})
	if err := os.WriteFile(c.CredentialRef, raw, 0600); err != nil {
		t.Fatal(err)
	}
	r := &fixtureRuntime{cfg: c, errors: make(chan error, 8)}
	s, err := New(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.Close()
		r.wg.Wait()
		close(r.errors)
		for err := range r.errors {
			if err != nil {
				t.Error(err)
			}
		}
	})
	return r, s
}

func (r *fixtureRuntime) Config() (ircd.Config, error) { return r.cfg, nil }
func (r *fixtureRuntime) EnsureAccount(ctx context.Context, account string) (ircd.Credential, error) {
	if ctx.Err() != nil {
		return ircd.Credential{}, ctx.Err()
	}
	if account != r.cfg.Account {
		return ircd.Credential{}, ircd.ErrConflict
	}
	return ircd.Credential{Account: account, CredentialRef: r.cfg.CredentialRef}, nil
}
func (r *fixtureRuntime) BeginChannel(ctx context.Context, channel string, threadID, operationID domain.ID) (ircd.ChannelOperation, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.op.ID != "" {
		if r.op.ThreadID != threadID || r.op.Channel != channel {
			return ircd.ChannelOperation{}, false, ircd.ErrConflict
		}
		return r.op, false, nil
	}
	r.op = ircd.ChannelOperation{ID: operationID, ServerID: r.cfg.ID, ThreadID: threadID, Channel: channel, State: "attempted", Owner: r.cfg.Account, UpdatedAt: time.Now().UTC()}
	return r.op, true, nil
}
func (r *fixtureRuntime) ConfirmChannel(ctx context.Context, channel string, threadID domain.ID) (ircd.ChannelOperation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.confirms++
	if ctx.Err() != nil {
		return r.op, ctx.Err()
	}
	if r.confirmErr != nil {
		return r.op, r.confirmErr
	}
	if !r.registered {
		return r.op, ircd.ErrUncertain
	}
	r.op.State = "confirmed"
	r.op.RegisteredAt = "2026-10-10T20:00:00Z"
	r.op.UpdatedAt = time.Now().UTC()
	proof := r.op
	if r.proof != nil {
		r.proof(&proof)
	}
	return proof, nil
}
func (r *fixtureRuntime) Dial(ctx context.Context, authority string) (net.Conn, error) {
	r.mu.Lock()
	r.dials++
	r.mu.Unlock()
	if authority != r.cfg.RealmID {
		return nil, ErrScope
	}
	if r.dial != nil {
		return r.dial(ctx)
	}
	client, peer := net.Pipe()
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer peer.Close()
		if err := r.script(peer); err != nil {
			r.errors <- err
		}
	}()
	return client, nil
}
func (r *fixtureRuntime) markRegistered() {
	r.mu.Lock()
	r.registered = true
	r.mutations++
	r.mu.Unlock()
}
func (r *fixtureRuntime) counts() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dials, r.mutations
}

func expect(reader *bufio.Reader, want string) error {
	line, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("fixture read: %w", err)
	}
	if line != want+"\r\n" {
		return errors.New("fixture received unexpected command")
	}
	return nil
}
func emit(conn net.Conn, lines ...string) error {
	for _, line := range lines {
		if _, err := io.WriteString(conn, line+"\r\n"); err != nil {
			return err
		}
	}
	return nil
}
func startHandshake(conn net.Conn, reader *bufio.Reader) (string, error) {
	if err := expect(reader, "CAP LS 302"); err != nil {
		return "", err
	}
	line, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "NICK kmc-") {
		return "", errors.New("fixture expected owned nick")
	}
	nick := strings.TrimSuffix(strings.TrimPrefix(line, "NICK "), "\r\n")
	if err = expect(reader, "USER keelmux 0 * :Keelmux managed channel"); err != nil {
		return "", err
	}
	return nick, nil
}
func saslExchange(conn net.Conn, reader *bufio.Reader) (string, error) {
	nick, err := startHandshake(conn, reader)
	if err != nil {
		return "", err
	}
	if err = emit(conn, ":owned CAP * LS * :account-tag", ":owned CAP * LS :sasl=EXTERNAL,PLAIN"); err != nil {
		return "", err
	}
	if err = expect(reader, "CAP REQ :sasl account-tag"); err != nil {
		return "", err
	}
	if err = emit(conn, ":owned CAP * ACK :sasl", ":owned CAP * ACK :account-tag"); err != nil {
		return "", err
	}
	if err = expect(reader, "AUTHENTICATE PLAIN"); err != nil {
		return "", err
	}
	if err = emit(conn, "AUTHENTICATE +"); err != nil {
		return "", err
	}
	line, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "AUTHENTICATE ") {
		return "", errors.New("fixture expected SASL payload")
	}
	payload, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(strings.TrimPrefix(line, "AUTHENTICATE "), "\r\n"))
	if err != nil || string(payload) != "keelmux-bot\x00keelmux-bot\x00"+strings.Repeat("a", 64) {
		return "", errors.New("fixture SASL payload mismatch")
	}
	return nick, nil
}
func handshake(conn net.Conn, reader *bufio.Reader) (string, error) {
	return handshakeTarget(conn, reader, false)
}
func handshakeTarget(conn net.Conn, reader *bufio.Reader, placeholder bool) (string, error) {
	nick, err := saslExchange(conn, reader)
	if err != nil {
		return "", err
	}
	target, mask := nick, nick+"!keelmux@localhost"
	if placeholder {
		target, mask = "*", "*"
	}
	if err = emit(conn, ":owned 900 "+target+" "+mask+" keelmux-bot :You are now logged in", ":owned 903 "+target+" :SASL authentication successful"); err != nil {
		return "", err
	}
	if err = expect(reader, "CAP END"); err != nil {
		return "", err
	}
	if err = emit(conn, ":owned 001 "+nick+" :Welcome"); err != nil {
		return "", err
	}
	return nick, nil
}
func registration(r *fixtureRuntime, ack bool) func(net.Conn) error {
	return func(conn net.Conn) error {
		reader := bufio.NewReader(conn)
		nick, err := handshakeTarget(conn, reader, r.placeholder)
		if err != nil {
			return err
		}
		r.mu.Lock()
		channel := r.op.Channel
		r.mu.Unlock()
		if err = expect(reader, "JOIN "+channel); err != nil {
			return err
		}
		if err = emit(conn, "PING :keepalive"); err != nil {
			return err
		}
		if err = expect(reader, "PONG :keepalive"); err != nil {
			return err
		}
		if err = emit(conn, "@account=keelmux-bot :"+nick+"!keelmux@localhost JOIN :"+channel); err != nil {
			return err
		}
		if err = expect(reader, "PRIVMSG ChanServ :REGISTER "+channel); err != nil {
			return err
		}
		r.markRegistered()
		if ack {
			return emit(conn, ":ChanServ!ChanServ@localhost NOTICE "+nick+" :Channel "+channel+" successfully registered")
		}
		return nil
	}
}

func TestStableChannelAndRegisteredRoute(t *testing.T) {
	r, s := fixture(t)
	r.script = registration(r, true)
	thread, op := domain.NewID("thread"), domain.NewID("operation")
	route, err := s.Ensure(context.Background(), thread, op)
	if err != nil {
		t.Fatal(err)
	}
	channel, _ := ChannelName(thread)
	if len(channel) != 61 || channel != strings.ToLower(channel) || route.Channel != channel || route.ThreadID != thread || route.ServerID != r.cfg.ID || route.OperationID != op || route.Account != r.cfg.Account || route.RegisteredAt == "" {
		t.Fatal("registered route mismatch")
	}
	again, err := s.Ensure(context.Background(), thread, domain.NewID("operation"))
	if err != nil || again.OperationID != op || again.Channel != channel || again.RegisteredAt != route.RegisteredAt {
		t.Fatal("retry changed immutable route", err)
	}
	if dials, mutations := r.counts(); dials != 1 || mutations != 1 {
		t.Fatal("retry repeated mutation", dials, mutations)
	}
	other, _ := ChannelName(domain.NewID("thread"))
	if channel == other {
		t.Fatal("distinct stable thread reused channel")
	}
}

func TestLostACKConfirmedReadOnly(t *testing.T) {
	r, s := fixture(t)
	r.script = registration(r, false)
	thread := domain.NewID("thread")
	route, err := s.Ensure(context.Background(), thread, domain.NewID("op"))
	if err != nil || route.RegisteredAt == "" {
		t.Fatal("lost ACK did not reconcile", err)
	}
	if dials, mutations := r.counts(); dials != 1 || mutations != 1 {
		t.Fatal("reconciliation repeated mutation")
	}
}

func TestPairedPreRegistrationPlaceholderRequiresExactWelcome(t *testing.T) {
	r, s := fixture(t)
	r.placeholder = true
	r.script = registration(r, true)
	thread := domain.NewID("thread")
	route, err := s.Ensure(context.Background(), thread, domain.NewID("operation"))
	if err != nil || route.ThreadID != thread || route.Account != "keelmux-bot" || route.RegisteredAt == "" {
		t.Fatal("paired placeholder and exact final nick rejected", err)
	}
	if dials, mutations := r.counts(); dials != 1 || mutations != 1 {
		t.Fatal("placeholder login repeated mutation")
	}
}

func TestUnknownOutcomeNeverReplaysWithNewOperation(t *testing.T) {
	r, s := fixture(t)
	r.script = registration(r, false)
	r.confirmErr = ircd.ErrUnavailable
	thread, op := domain.NewID("thread"), domain.NewID("operation")
	for _, id := range []domain.ID{op, op, domain.NewID("operation")} {
		if _, err := s.Ensure(context.Background(), thread, id); !errors.Is(err, ErrUncertain) {
			t.Fatal("unknown state did not fail closed", err)
		}
	}
	if dials, mutations := r.counts(); dials != 1 || mutations != 1 {
		t.Fatal("unknown mutation replayed", dials, mutations)
	}
	r.mu.Lock()
	r.confirmErr = nil
	r.mu.Unlock()
	route, err := s.Ensure(context.Background(), thread, domain.NewID("operation"))
	if err != nil || route.OperationID != op {
		t.Fatal("read-only repair lost original attempt", err)
	}
	if dials, _ := r.counts(); dials != 1 {
		t.Fatal("repair dialed IRC")
	}
}

func TestRegistrationNoticeCannotInventOwnership(t *testing.T) {
	for _, change := range []struct {
		name  string
		proof func(*ircd.ChannelOperation)
	}{
		{"owner", func(o *ircd.ChannelOperation) { o.Owner = "foreign" }},
		{"thread", func(o *ircd.ChannelOperation) { o.ThreadID = domain.NewID("thread") }},
		{"server", func(o *ircd.ChannelOperation) { o.ServerID = domain.NewID("ircd") }},
		{"channel", func(o *ircd.ChannelOperation) { o.Channel = "#different" }},
		{"operation", func(o *ircd.ChannelOperation) { o.ID = domain.NewID("operation") }},
		{"state", func(o *ircd.ChannelOperation) { o.State = "attempted" }},
		{"registration", func(o *ircd.ChannelOperation) { o.RegisteredAt = "" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			r, s := fixture(t)
			r.script = registration(r, true)
			r.proof = change.proof
			if route, err := s.Ensure(context.Background(), domain.NewID("thread"), domain.NewID("operation")); err == nil || route.Channel != "" {
				t.Fatal("invalid API ownership proof accepted")
			}
		})
	}
}

func TestStrictCredentialsAndScopeBeforeMutation(t *testing.T) {
	for _, raw := range []string{
		`{"account":"keelmux-bot","password":"` + strings.Repeat("a", 64) + `","password":"` + strings.Repeat("b", 64) + `"}`,
		`{"account":"foreign","password":"` + strings.Repeat("a", 64) + `"}`,
		`{"account":"keelmux-bot","password":"bad\r\nJOIN #owned"}`,
		`{"account":"keelmux-bot","password":"` + strings.Repeat("a", 64) + `","nick":"foreign"}`,
		`{"account":"keelmux-bot","password":"` + strings.Repeat("a", 64) + `"} {}`,
	} {
		t.Run(fmt.Sprintf("json%d", len(raw)), func(t *testing.T) {
			r, s := fixture(t)
			os.WriteFile(r.cfg.CredentialRef, []byte(raw), 0600)
			if _, err := s.Ensure(context.Background(), domain.NewID("thread"), domain.NewID("operation")); !errors.Is(err, ErrAuthentication) {
				t.Fatal("unowned credential accepted", err)
			}
			if r.op.ID != "" {
				t.Fatal("bad credentials consumed mutation permission")
			}
		})
	}
	r, s := fixture(t)
	if err := os.Chmod(r.cfg.CredentialRef, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ensure(context.Background(), domain.NewID("thread"), domain.NewID("operation")); !errors.Is(err, ErrAuthentication) {
		t.Fatal("public credential accepted")
	}
	for _, id := range []domain.ID{"thread_x", domain.ID("thread_" + strings.Repeat("A", 32)), domain.ID("thread_" + strings.Repeat("a", 32) + "\r\nJOIN #other")} {
		if _, err := ChannelName(id); err == nil {
			t.Fatal("invalid stable thread ID accepted")
		}
	}
	r.cfg.Endpoint = "ircs://example.com:6697"
	r.cfg.RealmID = "example.com:6697"
	if _, err := New(r); !errors.Is(err, ErrScope) {
		t.Fatal("public endpoint accepted")
	}
}

func TestCloseCancelsBlockedDialAndRejectsNewWork(t *testing.T) {
	r, s := fixture(t)
	started := make(chan struct{})
	r.dial = func(ctx context.Context) (net.Conn, error) { close(started); <-ctx.Done(); return nil, ctx.Err() }
	done := make(chan error, 1)
	go func() {
		_, err := s.Ensure(context.Background(), domain.NewID("thread"), domain.NewID("operation"))
		done <- err
	}()
	<-started
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled registration succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("Close left blocked dial")
	}
	if _, err := s.Ensure(context.Background(), domain.NewID("thread"), domain.NewID("operation")); !errors.Is(err, ErrClosed) {
		t.Fatal("work admitted after Close")
	}
}

func TestTimeoutClosesReadAndDoesNotRetry(t *testing.T) {
	r, s := fixture(t)
	s.timeout = 40 * time.Millisecond
	closed := make(chan struct{})
	r.script = func(conn net.Conn) error {
		defer close(closed)
		reader := bufio.NewReader(conn)
		if _, err := startHandshake(conn, reader); err != nil {
			return err
		}
		_, err := reader.ReadString('\n')
		if err == nil {
			return errors.New("fixture expected closed read")
		}
		return nil
	}
	thread := domain.NewID("thread")
	if _, err := s.Ensure(context.Background(), thread, domain.NewID("operation")); !errors.Is(err, ErrUncertain) {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("timeout left owned connection")
	}
	if _, err := s.Ensure(context.Background(), thread, domain.NewID("operation")); !errors.Is(err, ErrUncertain) {
		t.Fatal(err)
	}
	if dials, _ := r.counts(); dials != 1 {
		t.Fatal("timed-out unknown operation dialed again")
	}
}

func TestCloseClosesOwnedRead(t *testing.T) {
	r, s := fixture(t)
	started, closed := make(chan struct{}), make(chan struct{})
	r.script = func(conn net.Conn) error {
		defer close(closed)
		reader := bufio.NewReader(conn)
		if _, err := startHandshake(conn, reader); err != nil {
			return err
		}
		close(started)
		if _, err := reader.ReadString('\n'); err == nil {
			return errors.New("closed service sent another command")
		}
		return nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.Ensure(context.Background(), domain.NewID("thread"), domain.NewID("operation"))
		done <- err
	}()
	<-started
	s.Close()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close left owned IRC reader")
	}
	if err := <-done; err == nil {
		t.Fatal("Close completed unknown registration")
	}
}
