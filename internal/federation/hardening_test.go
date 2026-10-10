package federation

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/seanheiney/agent-backplane/internal/domain"
	"github.com/seanheiney/agent-backplane/internal/protocol"
	"github.com/seanheiney/agent-backplane/internal/transport"
)

type fetchSource struct {
	host    domain.ID
	session domain.Session
	fetch   func(domain.ID)
}

func (s fetchSource) List(context.Context) ([]domain.Session, error) {
	s.fetch(s.host)
	return []domain.Session{s.session}, nil
}

func TestRevokeEarlierHomeBeforeDirectoryReturn(t *testing.T) {
	a := domain.NewSession("a", "private-A", "/private-a", "fixture", "test")
	b := domain.NewSession("b", "private-B", "/private-b", "fixture", "test")
	var g *Gateway
	var first domain.ID
	var mu sync.Mutex
	fetch := func(host domain.ID) {
		mu.Lock()
		defer mu.Unlock()
		if first == "" {
			first = host
		} else if err := g.SetGrant(Grant{"human", first, nil}); err != nil {
			t.Error(err)
		}
	}
	var err error
	g, err = New([]Peer{{"a", fetchSource{"a", a, fetch}}, {"b", fetchSource{"b", b, fetch}}}, []Grant{{"human", "a", []domain.ID{a.ID}}, {"human", "b", []domain.ID{b.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	d, err := g.Directory(context.Background(), "human")
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(d.Sessions) != 1 || len(d.Places) != 1 {
		t.Fatalf("revocation projection: %+v", d)
	}
	if d.Sessions[0].HostID == first || d.Places[0].HostID == first {
		t.Fatal("revoked host remains in final projection")
	}
}

type quietSource struct{ entered, exited chan struct{} }

func (s quietSource) List(ctx context.Context) ([]domain.Session, error) {
	close(s.entered)
	defer close(s.exited)
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestDirectoryReturnsHealthyHomeWhenAnotherIsSilent(t *testing.T) {
	healthy := domain.NewSession("healthy", "work", "/tmp", "fixture", "test")
	quiet := quietSource{make(chan struct{}), make(chan struct{})}
	g, err := New([]Peer{{"healthy", source{sessions: []domain.Session{healthy}}}, {"quiet", quiet}}, []Grant{{"human", "healthy", []domain.ID{healthy.ID}}, {"human", "quiet", []domain.ID{"quiet-session"}}})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	d, err := g.Directory(context.Background(), "human")
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > directoryTimeout+time.Second {
		t.Fatalf("silent home consumed downstream deadline: %v", elapsed)
	}
	if len(d.Sessions) != 1 || d.Sessions[0].ID != healthy.ID || len(d.Places) != 2 {
		t.Fatalf("healthy partial directory missing: %+v", d)
	}
	if d.Places[0].HostID != "healthy" || d.Places[0].State != "reachable" || d.Places[1].State != "unreachable" {
		t.Fatalf("availability: %+v", d.Places)
	}
	select {
	case <-quiet.exited:
	case <-time.After(time.Second):
		t.Fatal("home request was not cancelled")
	}
}
func TestDirectoryCancellationDoesNotWaitForPeerDeadline(t *testing.T) {
	quiet := quietSource{make(chan struct{}), make(chan struct{})}
	g, _ := New([]Peer{{"home", quiet}}, []Grant{{"human", "home", []domain.ID{"s"}}})
	ctx, cancel := context.WithCancel(context.Background())
	ended := make(chan error, 1)
	go func() { _, err := g.Directory(ctx, "human"); ended <- err }()
	<-quiet.entered
	cancel()
	select {
	case err := <-ended:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("directory ignored caller cancellation")
	}
	<-quiet.exited
}

type durableHome struct {
	mu           sync.Mutex
	request      protocol.LeaseRequest
	lease        domain.Lease
	calls        int
	loseFirst    bool
	beforeAttach func()
}

func (h *durableHome) List(context.Context) ([]domain.Session, error) { return nil, nil }
func (h *durableHome) Open(context.Context, transport.Request) (*transport.Stream, error) {
	return nil, errors.New("lost open acknowledgement")
}
func (h *durableHome) Release(_ context.Context, r protocol.LeaseRequest) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if r.SessionID != h.lease.SessionID || r.ExecutionID != h.lease.ExecutionID || r.Generation != h.lease.Generation || r.OperationID != h.lease.OperationID || r.ClientID != h.lease.ClientID || r.Token != h.lease.Token || r.ReadOnly != h.request.ReadOnly {
		return errors.New("release collision")
	}
	return nil
}
func (h *durableHome) Attach(_ context.Context, r protocol.LeaseRequest) (domain.AttachSpec, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.beforeAttach != nil {
		h.beforeAttach()
	}
	h.calls++
	if h.calls == 1 {
		h.request = r
		h.lease = domain.Lease{SessionID: r.SessionID, ExecutionID: r.ExecutionID, Generation: r.Generation, OperationID: r.OperationID, ClientID: r.ClientID, Token: "private-original-token"}
		if h.loseFirst {
			return domain.AttachSpec{}, errors.New("lost home acquisition acknowledgement")
		}
	}
	if r != h.request {
		return domain.AttachSpec{}, errors.New("home collision")
	}
	return domain.AttachSpec{Argv: []string{"private-home-tmux"}, Lease: h.lease, ReadOnly: r.ReadOnly}, nil
}
func pendingRequest() protocol.LeaseRequest {
	return protocol.LeaseRequest{SessionID: "session", ExecutionID: "execution", Generation: 1, OperationID: "public-operation", ClientID: "public-client"}
}
func durableGateway(t *testing.T, dir string, home *durableHome) *Gateway {
	t.Helper()
	g, err := New([]Peer{{"home", home}}, []Grant{{"alice", "home", []domain.ID{"session"}}, {"bob", "home", []domain.ID{"session"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = g.SetStateDirectory(dir); err != nil {
		t.Fatal(err)
	}
	g.SetAttachCommand(func(transport.Request) []string { return []string{"client-local-bp"} })
	return g
}
func readReceipt(t *testing.T, dir string, r protocol.LeaseRequest) attachmentReceipt {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, receiptName(r.OperationID)))
	if err != nil {
		t.Fatal(err)
	}
	var record attachmentReceipt
	if err = json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	return record
}
func TestLostHomeAckRestartPreservesOriginalPrincipal(t *testing.T) {
	cases := []struct {
		name            string
		open, loseFirst bool
	}{
		{"attach-lost-acquisition-ack", false, true},
		{"stream-lost-acquisition-ack", true, true},
		{"stream-lost-open-ack", true, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "receipts")
			home := &durableHome{loseFirst: test.loseFirst}
			r := pendingRequest()
			home.beforeAttach = func() {
				record := readReceipt(t, dir, r)
				if record.Principal != "alice" || record.HostID != "home" || record.State != "pending" || record.Lease.Token != "" {
					t.Fatalf("home reached before durable pending identity: %+v", record)
				}
			}
			g := durableGateway(t, dir, home)
			if test.open {
				_, err := g.Open(context.Background(), "alice", transport.Request{HostID: "home", Lease: r, Rows: 24, Cols: 80})
				if err == nil {
					t.Fatal("expected lost ack")
				}
			} else {
				if _, err := g.Attach(context.Background(), "alice", r); err == nil {
					t.Fatal("expected lost ack")
				}
			}
			home.beforeAttach = nil
			if err := g.Close(); err != nil {
				t.Fatal(err)
			}
			restarted := durableGateway(t, dir, home)
			defer restarted.Close()
			if _, err := restarted.Attach(context.Background(), "bob", r); err == nil {
				t.Fatal("another principal recovered original token")
			}
			if _, err := restarted.Open(context.Background(), "bob", transport.Request{HostID: "home", Lease: r, Rows: 24, Cols: 80}); err == nil {
				t.Fatal("another principal recovered original stream")
			}
			collision := r
			collision.Generation++
			if _, err := restarted.Attach(context.Background(), "alice", collision); err == nil {
				t.Fatal("pending generation collision accepted")
			}
			collision = r
			collision.ReadOnly = true
			if _, err := restarted.Attach(context.Background(), "alice", collision); err == nil {
				t.Fatal("pending read-only collision accepted")
			}
			ack, err := restarted.Attach(context.Background(), "alice", r)
			if err != nil || ack.Lease.Token != home.lease.Token {
				t.Fatal("exact original recovery failed", ack, err)
			}
			record := readReceipt(t, dir, r)
			if record.State != "confirmed" || record.Lease != ack.Lease {
				t.Fatal("confirmed token not durable", record)
			}
		})
	}
}
func TestIntentPersistenceFailureNeverContactsHome(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "receipts")
	home := &durableHome{}
	g := durableGateway(t, dir, home)
	defer g.Close()
	if err := os.Rename(dir, dir+"-held"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("obstruction"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := g.Attach(context.Background(), "alice", pendingRequest()); err == nil {
			t.Fatal("persistence failure allowed acquisition")
		}
	}
	if home.calls != 0 {
		t.Fatal("home contacted despite unconfirmed intent")
	}
	if _, err := g.Attach(context.Background(), "bob", pendingRequest()); err == nil {
		t.Fatal("failed intent changed principal in memory")
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir+"-held", dir); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Attach(context.Background(), "alice", pendingRequest()); err != nil {
		t.Fatal("intent retry did not persist before effect", err)
	}
}
func TestConfirmedAcquisitionCannotChangeToken(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "receipts")
	home := &durableHome{}
	g := durableGateway(t, dir, home)
	r := pendingRequest()
	original, err := g.Attach(context.Background(), "alice", r)
	if err != nil {
		t.Fatal(err)
	}
	_ = g.Close()
	restarted := durableGateway(t, dir, home)
	defer restarted.Close()
	home.mu.Lock()
	home.lease.Token = "replacement-token"
	home.mu.Unlock()
	if _, err := restarted.Attach(context.Background(), "alice", r); err == nil {
		t.Fatal("home replaced saved token")
	}
	if record := readReceipt(t, dir, r); record.Lease != original.Lease {
		t.Fatal("original receipt overwritten")
	}
}
func TestGatewayIntentCrashHelper(t *testing.T) {
	if os.Getenv("BP_GATEWAY_INTENT_CRASH_CHILD") != "1" {
		return
	}
	dir := os.Getenv("BP_GATEWAY_INTENT_CRASH_DIR")
	home := &durableHome{}
	g := durableGateway(t, dir, home)
	home.beforeAttach = func() {
		record := readReceipt(t, dir, pendingRequest())
		if record.State != "pending" || record.Principal != "alice" {
			os.Exit(74)
		}
		if err := os.WriteFile(filepath.Join(dir, "home-effect"), []byte("acquired"), 0600); err != nil {
			os.Exit(75)
		}
		os.Exit(73)
	}
	_, _ = g.Attach(context.Background(), "alice", pendingRequest())
	t.Fatal("crash boundary was not reached")
}
func TestAbruptEntryExitKeepsUnacknowledgedPrincipalIntent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "receipts")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "-test.run=^TestGatewayIntentCrashHelper$")
	cmd.Env = append(os.Environ(), "BP_GATEWAY_INTENT_CRASH_CHILD=1", "BP_GATEWAY_INTENT_CRASH_DIR="+dir)
	data, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 73 {
		t.Fatalf("crash helper did not terminate at external-effect boundary: %v %s", err, data)
	}
	if _, err = os.Stat(filepath.Join(dir, "home-effect")); err != nil {
		t.Fatal("home effect boundary missing", err)
	}
	home := &durableHome{}
	restarted := durableGateway(t, dir, home)
	defer restarted.Close()
	if _, err = restarted.Attach(context.Background(), "bob", pendingRequest()); err == nil {
		t.Fatal("abrupt restart transferred original principal")
	}
	if home.calls != 0 {
		t.Fatal("conflicting principal reached home after crash")
	}
}

func TestCompetingPrincipalsHaveOneDurableOriginalOwner(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "receipts")
	home := &durableHome{}
	g := durableGateway(t, dir, home)
	defer g.Close()
	start := make(chan struct{})
	type result struct {
		principal string
		err       error
	}
	results := make(chan result, 2)
	for _, principal := range []string{"alice", "bob"} {
		go func(principal string) {
			<-start
			_, err := g.Attach(context.Background(), principal, pendingRequest())
			results <- result{principal, err}
		}(principal)
	}
	close(start)
	var original string
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err == nil {
			if original != "" {
				t.Fatal("two principals recovered one operation")
			}
			original = r.principal
		}
	}
	if original == "" {
		t.Fatal("no original owner acquired")
	}
	home.mu.Lock()
	calls := home.calls
	home.mu.Unlock()
	if calls != 1 {
		t.Fatalf("conflicting principal reached home: %d calls", calls)
	}
	if record := readReceipt(t, dir, pendingRequest()); record.Principal != original || record.State != "confirmed" {
		t.Fatal("durable original principal differs", record)
	}
}

func TestLegacyConfirmedReceiptStillAuthorizesExactRelease(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "receipts")
	home := &durableHome{}
	g := durableGateway(t, dir, home)
	r := pendingRequest()
	ack, err := g.Attach(context.Background(), "alice", r)
	if err != nil {
		t.Fatal(err)
	}
	record := readReceipt(t, dir, r)
	record.State = ""
	if err = g.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, receiptName(r.OperationID)), data, 0600); err != nil {
		t.Fatal(err)
	}
	restarted := durableGateway(t, dir, home)
	defer restarted.Close()
	r.Token = ack.Lease.Token
	if err = restarted.Release(context.Background(), "alice", r); err != nil {
		t.Fatal("legacy exact release unavailable", err)
	}
	if err = restarted.Release(context.Background(), "bob", r); err == nil {
		t.Fatal("legacy receipt transferred original principal")
	}
}

func TestInvalidPendingReceiptCannotAcquireAuthority(t *testing.T) {
	for _, state := range []string{"pending", "confirmed", "future", ""} {
		t.Run(state, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "receipts")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			r := pendingRequest()
			record := attachmentReceipt{Version: protocol.Version, Principal: "alice", HostID: "home", State: state, Lease: domain.Lease{SessionID: r.SessionID, ExecutionID: r.ExecutionID, Generation: r.Generation, OperationID: r.OperationID, ClientID: r.ClientID}}
			if state == "pending" {
				record.Lease.Token = "forged-pending-token"
			}
			data, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(dir, receiptName(r.OperationID)), data, 0600); err != nil {
				t.Fatal(err)
			}
			g, _ := New([]Peer{{"home", &durableHome{}}}, []Grant{{"alice", "home", []domain.ID{"session"}}})
			if err = g.SetStateDirectory(dir); err == nil {
				_ = g.Close()
				t.Fatal("malformed receipt granted saved authority")
			}
		})
	}
}
