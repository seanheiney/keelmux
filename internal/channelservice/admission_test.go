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

func TestIRCChannelAliasesHaveOneDurableRoute(t *testing.T) {
	s, db, old, _ := serviceFixture(t)
	b, _, err := db.ChannelBinding(context.Background(), old.ID)
	if err != nil {
		t.Fatal(err)
	}
	r := protocol.ChannelBindRequest{ID: domain.NewID("binding"), SessionID: b.SessionID, Provider: channels.IRC, Endpoint: "ircs://IRC.INVALID:6697/", ChannelID: "#Agents", CredentialRef: b.CredentialRef, Allowlist: b.Allowlist}
	bound, err := s.Bind(context.Background(), r)
	if err != nil || bound.ChannelID != "#agents" {
		t.Fatalf("canonical IRC route: %+v %v", bound, err)
	}
	r.ID = domain.NewID("binding")
	r.ChannelID = "#AGENTS"
	if _, err := s.Bind(context.Background(), r); err == nil {
		t.Fatal("wire case alias obtained a second route")
	}
	r.ID = bound.ID
	r.ChannelID = "#aGeNtS"
	if retry, err := s.Bind(context.Background(), r); err != nil || retry.ID != bound.ID {
		t.Fatalf("original alias retry: %+v %v", retry, err)
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
	_, db, status, _ := serviceFixture(t)
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
}

func TestIntakeStatusPreservesSuccessAndSuppressesPrivateFailures(t *testing.T) {
	s, db, b, f := serviceFixture(t)
	ctx := context.Background()
	if _, err := s.Sync(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	bound, _, err := db.ChannelBinding(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	first := s.status(bound)
	if first.Intake != "healthy" || first.LastSuccess == nil || first.LastCheck == nil || first.Coverage != "since_activation_snapshots" {
		t.Fatalf("unqualified success: %+v", first)
	}
	f.fetchErr = errors.New("private token or server diagnostic")
	s.mu.Lock()
	delete(s.lastFetch, b.ID)
	s.mu.Unlock()
	if _, err := s.Sync(ctx, b.ID); err == nil {
		t.Fatal("failed intake reported success")
	}
	next := s.status(bound)
	if next.Intake != "unavailable" || next.LastCheck == nil || next.LastSuccess == nil || !next.LastSuccess.Equal(*first.LastSuccess) {
		t.Fatalf("failure erased last success: %+v", next)
	}
	r := httptest.NewRequest("GET", "/v1/channels", nil)
	r.Header.Set("X-Backplane-Protocol", protocol.Version)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), "private token") || !strings.Contains(w.Body.String(), `"intake":"unavailable"`) {
		t.Fatalf("unsafe intake diagnostics: %d %s", w.Code, w.Body.String())
	}
	f.fetchErr = channels.ErrHistoryGap
	s.mu.Lock()
	delete(s.lastFetch, b.ID)
	s.mu.Unlock()
	if _, err := s.Sync(ctx, b.ID); err == nil {
		t.Fatal("missing history reported as complete")
	}
	gap := s.status(bound)
	if gap.Intake != "gap" || !gap.HistoryGap || gap.Coverage != string(channels.CoverageHistoryGap) {
		t.Fatalf("coverage gap hidden: %+v", gap)
	}
	if err := s.Disable(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	bound, _, err = db.ChannelBinding(ctx, b.ID)
	if err != nil || s.status(bound).Intake != "disabled" {
		t.Fatal("disabled binding appears healthy")
	}
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
