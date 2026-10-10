package channelservice

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/channels"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
)

func TestCanonicalBindingRetryAndEarlyScopeRefusal(t *testing.T) {
	s, db, status, f := serviceFixture(t)
	b, _, err := db.ChannelBinding(context.Background(), status.ID)
	if err != nil {
		t.Fatal(err)
	}
	r := protocol.ChannelBindRequest{ID: b.ID, SessionID: b.SessionID, Provider: b.Provider, Endpoint: "https://MATTERMOST.INVALID///", ChannelID: b.ChannelID, RootID: b.RootID, CredentialRef: b.CredentialRef, Allowlist: b.Allowlist}
	got, err := s.Bind(context.Background(), r)
	if err != nil || got.ID != b.ID || got.Endpoint != b.Endpoint {
		t.Fatalf("canonical retry: %+v %v", got, err)
	}
	r.ID = domain.NewID("binding")
	r.Endpoint = "http://mattermost.invalid"
	if _, err = s.Bind(context.Background(), r); !errors.Is(err, channels.ErrInvalid) {
		t.Fatalf("insecure scope: %v", err)
	}
	if f.sends.Load() != 0 {
		t.Fatal("scope admission caused an external send")
	}
	bindings, err := db.ChannelBindings(context.Background())
	if err != nil || len(bindings) != 1 {
		t.Fatalf("failed admission persisted a binding: %v %v", bindings, err)
	}
}

type refusingProbe struct {
	fixtureAdapter
	closes atomic.Int64
}

func (*refusingProbe) Probe(context.Context, channels.Binding) (channels.Evidence, error) {
	return channels.Evidence{}, errors.New("unverified destination")
}
func (f *refusingProbe) Close() error { f.closes.Add(1); return nil }

func TestUncommittedBindingClosesItsTransport(t *testing.T) {
	old, db, status, _ := serviceFixture(t)
	b, _, err := db.ChannelBinding(context.Background(), status.ID)
	if err != nil {
		t.Fatal(err)
	}
	f := &refusingProbe{}
	s, err := New(context.Background(), db, func(channels.Provider) (channels.Adapter, error) { return f, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := protocol.ChannelBindRequest{ID: domain.NewID("binding"), SessionID: b.SessionID, Provider: b.Provider, Endpoint: b.Endpoint, ChannelID: b.ChannelID, RootID: b.RootID, CredentialRef: b.CredentialRef, Allowlist: b.Allowlist}
	if _, err := s.Bind(context.Background(), r); err == nil {
		t.Fatal("unverified binding accepted")
	}
	if f.closes.Load() != 1 {
		t.Fatal("failed binding retained its live transport")
	}
	if _, err := db.ChannelBindings(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.ChannelBinding(context.Background(), r.ID); err == nil {
		t.Fatal("failed binding left durable routing")
	}
	_ = old
}

func TestValidEscapedTextSurvivesBoundedHTTPAdmission(t *testing.T) {
	s, db, b, _ := serviceFixture(t)
	text := strings.Repeat("<", channels.MaxTextBytes)
	id := domain.NewID("message")
	body, err := json.Marshal(protocol.ChannelSendRequest{ID: id, BindingID: b.ID, Text: text})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/v1/channels/send", strings.NewReader(string(body)))
	r.Header.Set("X-Backplane-Protocol", protocol.Version)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("valid maximum escaped text: %d %s", w.Code, w.Body.String())
	}
	m, err := db.ChannelMessage(context.Background(), id)
	if err != nil || m.Text != text {
		t.Fatal("bounded wire altered authored content")
	}
}

func TestWorkerCancellationSettlesAttemptBeforeExit(t *testing.T) {
	s, db, b, f := serviceFixture(t)
	entered := make(chan struct{})
	f.send = func(ctx context.Context, _ channels.Binding, _ channels.Delivery, _ channels.Message) (channels.SendResult, error) {
		close(entered)
		<-ctx.Done()
		return channels.SendResult{}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	t.Cleanup(cancel)
	r := protocol.ChannelSendRequest{ID: domain.NewID("message"), BindingID: b.ID, Text: "One admitted effect"}
	if _, err := s.Send(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not claim queued message")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("worker failed to settle its canceled attempt")
	}
	d, err := db.ChannelDelivery(context.Background(), r.ID)
	if err != nil || d.State != channels.Uncertain {
		t.Fatalf("shutdown lost outcome: %+v %v", d, err)
	}
	fresh, err := New(context.Background(), db, func(channels.Provider) (channels.Adapter, error) { return f, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if _, err := fresh.Sync(context.Background(), b.ID); err != nil {
		t.Fatal(err)
	}
	if f.sends.Load() != 1 {
		t.Fatal("canceled external attempt was replayed")
	}
}
