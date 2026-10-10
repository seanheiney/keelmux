package ircdservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/channels"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/ircd"
	"github.com/seanheiney/keelmux/internal/ircdchannel"
	"github.com/seanheiney/keelmux/internal/protocol"
)

type testManager struct {
	mu              sync.Mutex
	cfg             ircd.Config
	status          ircd.Status
	calls           map[string]int
	startIDs        []domain.ID
	stopIDs         []domain.ID
	launches, stops int
	uncertain       bool
	failure         error
}

func (m *testManager) Config() (ircd.Config, error) { return m.cfg, nil }
func (m *testManager) Init(ctx context.Context, in ircd.InitOptions) (ircd.Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls["init"]++
	return m.cfg, m.failure
}
func (m *testManager) Status(ctx context.Context) (ircd.Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls["status"]++
	return m.status, m.failure
}
func (m *testManager) Start(ctx context.Context, id domain.ID) (ircd.Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls["start"]++
	m.startIDs = append(m.startIDs, id)
	if m.status.OperationID != "" && m.status.OperationID != id {
		return m.status, ircd.ErrConflict
	}
	if m.status.OperationID == "" {
		m.launches++
		m.status.Instance = domain.NewID("execution")
		m.status.OperationID = id
	}
	if m.uncertain {
		return m.status, ircd.ErrUncertain
	}
	m.status.State = "running"
	m.status.Alive = true
	m.status.Version = ircd.Version
	return m.status, m.failure
}
func (m *testManager) Stop(ctx context.Context, id domain.ID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls["stop"]++
	m.stopIDs = append(m.stopIDs, id)
	if id != m.status.Instance {
		return ircd.ErrConflict
	}
	m.stops++
	m.status.State = "stopped"
	m.status.Alive = false
	return m.failure
}
func (m *testManager) Restart(ctx context.Context, instance, id domain.ID) (ircd.Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls["restart"]++
	if instance != m.status.Instance {
		return m.status, ircd.ErrConflict
	}
	m.stops++
	m.launches++
	m.status.Instance = domain.NewID("execution")
	m.status.OperationID = id
	m.status.State = "running"
	m.status.Alive = true
	m.status.Version = ircd.Version
	return m.status, m.failure
}
func (m *testManager) EnsureAccount(ctx context.Context, account string) (ircd.Credential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls["account"]++
	ref := m.cfg.CredentialRef
	if account != m.cfg.Account {
		ref = filepath.Join(filepath.Dir(ref), "accounts", account+".json")
	}
	return ircd.Credential{Account: account, CredentialRef: ref}, m.failure
}

type testSessions struct {
	afterMembership               *domain.Session
	session                       domain.Session
	project                       domain.Project
	projectCalls, membershipCalls int
	membershipError               error
	calls                         int
}

func (s *testSessions) Session(ctx context.Context, id domain.ID) (domain.Session, error) {
	s.calls++
	if s.calls > 1 && s.afterMembership != nil {
		return *s.afterMembership, nil
	}
	return s.session, nil
}

func (s *testSessions) Project(ctx context.Context, id domain.ID) (domain.Project, error) {
	s.projectCalls++
	return s.project, nil
}
func (s *testSessions) BindProjectThread(ctx context.Context, project, session domain.ID) (domain.Project, error) {
	s.membershipCalls++
	if project != s.project.ID || session != s.session.ID {
		return domain.Project{}, ircd.ErrConflict
	}
	return s.project, s.membershipError
}

type testRegistrar struct {
	cfg            ircd.Config
	routes         map[domain.ID]ircdchannel.Route
	calls, effects int
	failure        error
	change         func(*ircdchannel.Route)
}

func (r *testRegistrar) EnsureProject(ctx context.Context, project ircdchannel.Project, thread, op domain.ID) (ircdchannel.Route, error) {
	r.calls++
	if r.failure != nil {
		return ircdchannel.Route{}, r.failure
	}
	route, ok := r.routes[thread]
	if !ok {
		r.effects++
		channel, _ := ircdchannel.ProjectChannelName(project)
		account, _ := ircdchannel.ThreadAccount(thread)
		route = ircdchannel.Route{ServerID: r.cfg.ID, ProjectID: project.ID, ThreadID: thread, OperationID: op, Endpoint: r.cfg.Endpoint, RealmID: r.cfg.RealmID, Account: account, Nick: account, CAFile: r.cfg.CAFile, CredentialRef: filepath.Join(filepath.Dir(r.cfg.CredentialRef), "accounts", account, "credential.json"), Channel: channel, RegisteredAt: "2026-10-10T20:00:00Z", ConfirmedAt: time.Now().UTC()}
		r.routes[thread] = route
	}
	if r.change != nil {
		r.change(&route)
	}
	return route, nil
}

type testBinder struct {
	cfg      ircd.Config
	session  domain.Session
	calls    []protocol.ChannelBindRequest
	bindings map[domain.ID]protocol.ChannelStatus
	change   func(*protocol.ChannelStatus)
}

func (b *testBinder) Bind(ctx context.Context, in protocol.ChannelBindRequest) (protocol.ChannelStatus, error) {
	b.calls = append(b.calls, in)
	if existing, ok := b.bindings[in.ID]; ok {
		if existing.SessionID != in.SessionID || existing.ChannelID != in.ChannelID || !reflect.DeepEqual(existing.Allowlist, in.Allowlist) {
			return protocol.ChannelStatus{}, domain.ConflictError{Reason: "immutable binding conflict"}
		}
		return existing, nil
	}
	account, _ := ircdchannel.ThreadAccount(b.session.ThreadID)
	out := protocol.ChannelStatus{ProjectID: in.ProjectID, IRCAddressed: in.IRCAddressed, ID: in.ID, SessionID: in.SessionID, ThreadID: b.session.ThreadID, Provider: in.Provider, Endpoint: in.Endpoint, RealmID: b.cfg.RealmID, SelfID: account, ChannelID: in.ChannelID, RootID: in.RootID, Allowlist: in.Allowlist, Active: true, ActivationCutoff: time.Now().UTC()}
	if b.change != nil {
		b.change(&out)
	}
	b.bindings[in.ID] = out
	return out, nil
}

func setup(t *testing.T) (*Service, *testManager, *testRegistrar, *testBinder, *testSessions) {
	t.Helper()
	dir := t.TempDir()
	cfg := ircd.Config{ID: domain.NewID("ircd"), Endpoint: "ircs://127.0.0.1:6697", RealmID: "127.0.0.1:6697", Account: "keelmux-bot", CredentialRef: filepath.Join(dir, "bot.json"), CAFile: filepath.Join(dir, "ca.pem"), Runtime: ircd.RuntimeInfo{Version: ircd.Version, Source: ircd.SourceCommit, Target: "darwin/arm64"}}
	m := &testManager{cfg: cfg, calls: map[string]int{}, status: ircd.Status{Config: cfg, State: "initialized", ObservedAt: time.Now().UTC()}}
	sessions := &testSessions{session: domain.Session{ID: domain.NewID("session"), ThreadID: domain.NewID("thread"), WorkID: domain.NewID("work"), HostID: domain.NewID("host"), Workspace: dir}}
	sessions.project = domain.Project{ID: domain.NewID("project"), HostID: sessions.session.HostID, Name: "Owned project", Workspace: dir, CreatedAt: time.Now().UTC()}
	r := &testRegistrar{cfg: cfg, routes: map[domain.ID]ircdchannel.Route{}}
	b := &testBinder{cfg: cfg, session: sessions.session, bindings: map[domain.ID]protocol.ChannelStatus{}}
	s, err := New(m, r, b, sessions)
	if err != nil {
		t.Fatal(err)
	}
	return s, m, r, b, sessions
}
func client(t *testing.T, s *Service) *protocol.Client {
	t.Helper()
	server := httptest.NewServer(s.Handler())
	t.Cleanup(server.Close)
	return &protocol.Client{HTTP: server.Client(), BaseURL: server.URL}
}
func rawRequest(s *Service, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest("POST", path, strings.NewReader(body))
	request.Header.Set("X-Backplane-Protocol", protocol.Version)
	request.Header.Set("Content-Type", "application/json")
	out := httptest.NewRecorder()
	s.Handler().ServeHTTP(out, request)
	return out
}

func TestExplicitLifecycleIDsAndUnknownResponsesNeverReplay(t *testing.T) {
	s, m, _, _, _ := setup(t)
	c := client(t, s)
	ctx := context.Background()
	op := domain.NewID("operation")
	if m.launches != 0 || len(m.calls) != 0 {
		t.Fatal("constructor changed runtime")
	}
	if _, err := c.IRCD(ctx); err != nil {
		t.Fatal(err)
	}
	if m.launches != 0 {
		t.Fatal("status started IRCD")
	}
	m.uncertain = true
	for _, id := range []domain.ID{op, op, domain.NewID("operation")} {
		if _, err := c.StartIRCD(ctx, id); err == nil {
			t.Fatal("unknown launch reported success")
		}
	}
	if m.launches != 1 || len(m.startIDs) != 3 || m.startIDs[0] != op || m.startIDs[1] != op {
		t.Fatal("local handler changed/replayed launch intent")
	}
	m.uncertain = false
	status, err := c.StartIRCD(ctx, op)
	if err != nil || status.OperationID != op || !status.Alive {
		t.Fatal("original launch did not reconcile", err)
	}
	stale := domain.NewID("execution")
	if err = c.StopIRCD(ctx, stale); err == nil {
		t.Fatal("stale stop accepted")
	}
	if _, err = c.RestartIRCD(ctx, stale, domain.NewID("operation")); err == nil {
		t.Fatal("stale restart accepted")
	}
	if m.stops != 0 || m.launches != 1 {
		t.Fatal("stale instance changed runtime")
	}
	newOp := domain.NewID("operation")
	restarted, err := c.RestartIRCD(ctx, status.Instance, newOp)
	if err != nil || restarted.Instance == status.Instance || restarted.OperationID != newOp {
		t.Fatal("restart lost expected-instance/new-operation", err)
	}
	if err = c.StopIRCD(ctx, restarted.Instance); err != nil {
		t.Fatal(err)
	}
}

func TestBindResolvesStableThreadAndImmutableManagedScope(t *testing.T) {
	s, m, r, b, sessions := setup(t)
	in := protocol.IRCDBindRequest{ProjectID: sessions.project.ID, ID: domain.NewID("binding"), SessionID: sessions.session.ID, OperationID: domain.NewID("operation"), Allowlist: []string{"zoe", "alice"}}
	out, err := s.Bind(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	channel, _ := ircdchannel.ProjectChannelName(ircdchannel.Project{ID: sessions.project.ID, HostID: sessions.project.HostID, Name: sessions.project.Name, Workspace: sessions.project.Workspace})
	account, _ := ircdchannel.ThreadAccount(sessions.session.ThreadID)
	if out.ThreadID != sessions.session.ThreadID || out.ChannelID != channel || out.Endpoint != m.cfg.Endpoint || out.SemanticDispatch {
		t.Fatal("bind did not preserve stable managed route")
	}
	got := b.calls[0]
	if got.CredentialRef != filepath.Join(filepath.Dir(m.cfg.CredentialRef), "accounts", account, "credential.json") || got.ProjectID != sessions.project.ID || !got.IRCAddressed || got.RootID != "" || got.Provider != channels.IRC || !reflect.DeepEqual(got.Allowlist, []string{"alice", "zoe"}) {
		t.Fatal("unverified binding scope")
	}
	in.OperationID = domain.NewID("operation")
	again, err := s.Bind(context.Background(), in)
	if err != nil || again.ID != out.ID || r.effects != 1 {
		t.Fatal("same-thread retry repeated channel creation", err)
	}
	in.Allowlist = []string{"other"}
	if _, err = s.Bind(context.Background(), in); err == nil || r.effects != 1 {
		t.Fatal("binding identity reused with altered immutable allowlist")
	}
}

func TestUncertainRegistrationNeverCreatesBinding(t *testing.T) {
	s, _, r, b, sessions := setup(t)
	r.failure = ircdchannel.ErrUncertain
	in := protocol.IRCDBindRequest{ProjectID: sessions.project.ID, ID: domain.NewID("binding"), SessionID: sessions.session.ID, OperationID: domain.NewID("operation"), Allowlist: []string{"alice"}}
	for i := 0; i < 2; i++ {
		if _, err := s.Bind(context.Background(), in); !errors.Is(err, ircdchannel.ErrUncertain) {
			t.Fatal(err)
		}
	}
	if len(b.calls) != 0 || r.effects != 0 {
		t.Fatal("uncertain registration granted binding authority")
	}
}

func TestBindingProofCannotGrantNativeDispatchOrChangeIdentity(t *testing.T) {
	for _, change := range []struct {
		name   string
		change func(*protocol.ChannelStatus)
	}{
		{"dispatch", func(out *protocol.ChannelStatus) { out.SemanticDispatch = true }},
		{"account", func(out *protocol.ChannelStatus) { out.SelfID = "foreign" }},
		{"project", func(out *protocol.ChannelStatus) { out.ProjectID = domain.NewID("project") }},
		{"addressing", func(out *protocol.ChannelStatus) { out.IRCAddressed = false }},
		{"thread", func(out *protocol.ChannelStatus) { out.ThreadID = domain.NewID("thread") }},
	} {
		t.Run(change.name, func(t *testing.T) {
			s, _, _, b, sessions := setup(t)
			b.change = change.change
			out, err := s.Bind(context.Background(), protocol.IRCDBindRequest{ProjectID: sessions.project.ID, ID: domain.NewID("binding"), SessionID: sessions.session.ID, OperationID: domain.NewID("operation"), Allowlist: []string{"alice"}})
			if err == nil || !reflect.DeepEqual(out, protocol.ChannelStatus{}) {
				t.Fatal("unverified binding result published authority")
			}
		})
	}
}

func TestBindRejectsIdentityDriftBeforeBinding(t *testing.T) {
	for _, change := range []struct {
		name   string
		change func(*ircdchannel.Route)
	}{
		{"thread", func(r *ircdchannel.Route) { r.ThreadID = domain.NewID("thread") }},
		{"project", func(r *ircdchannel.Route) { r.ProjectID = domain.NewID("project") }},
		{"nick", func(r *ircdchannel.Route) { r.Nick = "foreign" }},
		{"server", func(r *ircdchannel.Route) { r.ServerID = domain.NewID("ircd") }},
		{"realm", func(r *ircdchannel.Route) { r.RealmID = "127.0.0.1:1" }},
		{"endpoint", func(r *ircdchannel.Route) { r.Endpoint = "ircs://external.example:6697" }},
		{"account", func(r *ircdchannel.Route) { r.Account = "foreign" }},
		{"credential", func(r *ircdchannel.Route) { r.CredentialRef = "/tmp/foreign.json" }},
		{"channel", func(r *ircdchannel.Route) { r.Channel = "#foreign" }},
		{"proof", func(r *ircdchannel.Route) { r.RegisteredAt = "" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			s, _, r, b, sessions := setup(t)
			r.change = change.change
			_, err := s.Bind(context.Background(), protocol.IRCDBindRequest{ProjectID: sessions.project.ID, ID: domain.NewID("binding"), SessionID: sessions.session.ID, OperationID: domain.NewID("operation"), Allowlist: []string{"alice"}})
			if err == nil || len(b.calls) != 0 {
				t.Fatal("unverified route reached binding")
			}
		})
	}
	s, _, r, _, sessions := setup(t)
	sessions.session.ID = domain.NewID("session")
	if _, err := s.Bind(context.Background(), protocol.IRCDBindRequest{ProjectID: sessions.project.ID, ID: domain.NewID("binding"), SessionID: domain.NewID("session"), OperationID: domain.NewID("operation"), Allowlist: []string{"alice"}}); err == nil || r.calls != 0 {
		t.Fatal("session resolver mismatch reached registrar")
	}
}

func TestStrictJSONRejectedBeforeAnyDependencyAction(t *testing.T) {
	for _, body := range []string{
		`null`, `[]`, `{}`, `{"operation_id":null}`, `{"operation_id":"x","operation_id":"y"}`, `{"Operation_id":"x"}`, `{"operation_id":"x","unknown":true}`, `{"operation_id":"x"} {}`, `{"operation_id":"x"} null`, `{"operation_id":42}`, `{"operation_id":"` + string([]byte{255}) + `"}`, strings.Repeat(" ", 128<<10) + `{}`,
	} {
		s, m, r, b, sessions := setup(t)
		out := rawRequest(s, "/v1/ircd/start", body)
		if out.Code != 400 || len(m.calls) != 0 || r.calls != 0 || len(b.calls) != 0 || sessions.calls != 0 {
			t.Fatal("invalid start touched dependencies", out.Code)
		}
	}
	for _, body := range []string{`{"irc_port":null}`, `{"api_port":-1}`, `{"irc_port":65536}`, `{"irc_port":1,"api_port":1}`, `{"irc_port":0,"IRc_port":0}`} {
		s, m, _, _, _ := setup(t)
		if out := rawRequest(s, "/v1/ircd/init", body); out.Code != 400 || len(m.calls) != 0 {
			t.Fatal("invalid init reached manager")
		}
	}
	for _, body := range []string{`{"binding_id":null}`, `{"allow_users":null}`, `{"allow_users":[null]}`, `{"allow_users":["alice"],"thread_id":"external"}`, `{"endpoint":"ircs://foreign:6697"}`, `{"allow_users":["alice","alice"]}`} {
		s, _, r, b, sessions := setup(t)
		if out := rawRequest(s, "/v1/ircd/bind", body); out.Code != 400 || r.calls != 0 || len(b.calls) != 0 || sessions.calls != 0 {
			t.Fatal("invalid bind reached dependencies")
		}
	}
}

func TestAccountConnectionFieldsAreExplicitAndStatusRedacted(t *testing.T) {
	s, m, _, _, _ := setup(t)
	c := client(t, s)
	connection, err := c.IRCDAccount(context.Background(), "alice")
	if err != nil || connection.Account != "alice" || connection.CredentialRef == m.cfg.CredentialRef || connection.CAFile != m.cfg.CAFile {
		t.Fatal("explicit private human connection fields", err)
	}
	for _, account := range []string{"Alice", "alice\nOPER root", "", strings.Repeat("a", 33)} {
		body, _ := json.Marshal(protocol.IRCDAccountRequest{Account: account})
		before := m.calls["account"]
		if out := rawRequest(s, "/v1/ircd/account", string(body)); out.Code != 400 || m.calls["account"] != before {
			t.Fatal("invalid services account reached manager")
		}
	}
	request := httptest.NewRequest("GET", "/v1/ircd/status", nil)
	request.Header.Set("X-Backplane-Protocol", protocol.Version)
	out := httptest.NewRecorder()
	s.Handler().ServeHTTP(out, request)
	if bytes.Contains(out.Body.Bytes(), []byte(m.cfg.CredentialRef)) || bytes.Contains(out.Body.Bytes(), []byte(m.cfg.CAFile)) || bytes.Contains(out.Body.Bytes(), []byte("credential")) {
		t.Fatal("generic status leaked private connection references")
	}
	m.failure = errors.New("sensitive-password-and-credential-file")
	failedOut := rawRequest(s, "/v1/ircd/init", `{}`)
	if failedOut.Code != 503 || strings.Contains(failedOut.Body.String(), "sensitive-password") {
		t.Fatal("dependency error leaked secret material")
	}
}

func TestProtocolAndRequestScopeAreExact(t *testing.T) {
	for _, scenario := range []string{"no-version", "old-version", "duplicate-version", "query", "body", "method"} {
		t.Run(scenario, func(t *testing.T) {
			s, m, _, _, _ := setup(t)
			method, path, body := "GET", "/v1/ircd/status", ""
			if scenario == "query" {
				path += "?instance=other"
			}
			if scenario == "body" {
				body = "{}"
			}
			if scenario == "method" {
				method = "POST"
			}
			req := httptest.NewRequest(method, path, strings.NewReader(body))
			if scenario != "no-version" {
				req.Header.Set("X-Backplane-Protocol", protocol.Version)
			}
			if scenario == "old-version" {
				req.Header.Set("X-Backplane-Protocol", "bp/0")
			}
			if scenario == "duplicate-version" {
				req.Header.Add("X-Backplane-Protocol", protocol.Version)
			}
			out := httptest.NewRecorder()
			s.Handler().ServeHTTP(out, req)
			if out.Code < 400 || len(m.calls) != 0 {
				t.Fatal("ambiguous request scope reached manager")
			}
		})
	}
}

type sessionOnly struct{ session domain.Session }

func (s *sessionOnly) Session(ctx context.Context, id domain.ID) (domain.Session, error) {
	return s.session, nil
}

func TestManagedBindRequiresExplicitProjectAndMembershipBeforeExternalIO(t *testing.T) {
	for _, kind := range []string{"missing-project", "project-id", "host", "workspace", "invalid-name", "missing-registry", "membership-conflict", "thread-drift"} {
		t.Run(kind, func(t *testing.T) {
			s, _, r, b, sessions := setup(t)
			in := protocol.IRCDBindRequest{ProjectID: sessions.project.ID, ID: domain.NewID("binding"), SessionID: sessions.session.ID, OperationID: domain.NewID("operation"), Allowlist: []string{"alice"}}
			switch kind {
			case "missing-project":
				in.ProjectID = ""
			case "project-id":
				sessions.project.ID = domain.NewID("project")
			case "host":
				sessions.project.HostID = domain.NewID("host")
			case "workspace":
				sessions.project.Workspace = filepath.Join(sessions.project.Workspace, "other")
			case "invalid-name":
				sessions.project.Name = "bad\nname"
			case "missing-registry":
				s.sessions = &sessionOnly{session: sessions.session}
			case "membership-conflict":
				sessions.membershipError = domain.ConflictError{Reason: "different immutable membership"}
			case "thread-drift":
				copy := sessions.session
				copy.ThreadID = domain.NewID("thread")
				sessions.afterMembership = &copy
			}
			if _, e := s.Bind(context.Background(), in); e == nil || r.calls != 0 || len(b.calls) != 0 {
				t.Fatal("unproven project membership reached external routing")
			}
			if kind == "missing-project" && sessions.calls != 0 {
				t.Fatal("missing explicit project resolved inferred session")
			}
			if (kind == "project-id" || kind == "host" || kind == "workspace" || kind == "invalid-name") && sessions.membershipCalls != 0 {
				t.Fatal("invalid registry identity committed membership")
			}
		})
	}
}
