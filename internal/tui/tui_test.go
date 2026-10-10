package tui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/seanheiney/keelmux/internal/clientstate"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
)

func fixtureSession(id, name string) domain.Session {
	return domain.Session{ID: domain.ID(id), ThreadID: domain.ID("thread_" + id), WorkID: domain.ID("work_" + id), LaneID: domain.ID("lane_" + id), Name: name, Workspace: "/work/api", Mode: domain.Managed, Capabilities: domain.ManagedCapabilities(), Execution: domain.Execution{ID: domain.ID("execution_" + id), Generation: 1, Provider: "fixture", State: "running", ActualModel: domain.Unknown(), Account: domain.Unknown()}}
}
func testModel(t *testing.T, s clientstate.State) *model {
	t.Helper()
	return newModel(context.Background(), &protocol.Client{}, filepath.Join(t.TempDir(), "place.json"), s)
}
func key(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func TestInventoryCannotStealFocusAndMissingAnchorIsExplained(t *testing.T) {
	s := clientstate.New()
	s.Places[""] = clientstate.Place{SessionID: "b", ThreadID: "thread_b", Filter: "", Scroll: 3}
	m := testModel(t, s)
	a, b, c := fixtureSession("a", "A"), fixtureSession("b", "B"), fixtureSession("c", "C")
	m.Update(inventoryMsg{sessions: []domain.Session{b, c}})
	m.Update(inventoryMsg{sessions: []domain.Session{a, c, b}})
	if got := m.place().SessionID; got != b.ID {
		t.Fatalf("insertion stole focus: %s", got)
	}
	m.Update(inventoryMsg{sessions: []domain.Session{a, c}})
	if m.place().SessionID != b.ID || !strings.Contains(m.View(), "Saved selection is unavailable") {
		t.Fatal("missing anchor silently chose different work")
	}
	if cmd := m.acquire(false); cmd != nil {
		t.Fatal("stale selection acquired another target")
	}
	m.Update(inventoryMsg{sessions: []domain.Session{a, b, c}})
	if got, ok := m.selected(); !ok || got.ID != b.ID {
		t.Fatal("reappearing anchor did not restore selection")
	}
	m.Update(inventoryMsg{err: errors.New("connection lost")})
	if m.place().SessionID != b.ID || !strings.Contains(m.View(), "OFFLINE") || m.acquire(false) != nil {
		t.Fatal("offline inventory was treated as current attach authority")
	}
}

func TestBackgroundArrivalCannotChooseAnEmptySelection(t *testing.T) {
	m := testModel(t, clientstate.New())
	m.Update(inventoryMsg{})
	m.Update(inventoryMsg{sessions: []domain.Session{fixtureSession("a", "A")}})
	if m.place().SessionID != "" {
		t.Fatal("background arrival stole focus from empty view")
	}
	m.move(1)
	if m.place().SessionID != "a" {
		t.Fatal("explicit navigation did not select newly available work")
	}
}

func TestGroupPlaceDraftAndTargetChangeStayAddressed(t *testing.T) {
	s := clientstate.New()
	left, _ := s.AddGroup("Left", "")
	right, _ := s.AddGroup("Right", "")
	s.AddReference(left, clientstate.Reference{Kind: "workspace", Workspace: "/work/api"})
	s.AddReference(right, clientstate.Reference{Kind: "workspace", Workspace: "/work/api"})
	s.Places[left] = clientstate.Place{SessionID: "a", Filter: "A"}
	s.Places[right] = clientstate.Place{SessionID: "b", Filter: "B"}
	s.SwitchGroup(left)
	m := testModel(t, s)
	a, b := fixtureSession("a", "A"), fixtureSession("b", "B")
	m.Update(inventoryMsg{sessions: []domain.Session{a, b}})
	m.Update(key("p"))
	m.Update(key("draft for A"))
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m.state.SwitchGroup(right)
	m.Update(key("p"))
	m.Update(key("draft for B"))
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m.state.SwitchGroup(left)
	if m.place().SessionID != "a" || m.place().Filter != "A" {
		t.Fatal("group change lost remembered place")
	}
	a.Execution.ID = "replacement"
	a.Execution.Generation = 2
	m.Update(inventoryMsg{sessions: []domain.Session{a, b}})
	m.Update(key("p"))
	if m.draft.Text != "draft for A" || !strings.Contains(m.View(), "Target changed") {
		t.Fatal("saved draft lost or silently rebound to replacement execution")
	}
	if other, _ := m.state.Draft(b.ThreadID, b.LaneID); other.Text != "draft for B" {
		t.Fatal("draft leaked across group recipient")
	}
	got, err := clientstate.Load(m.statePath)
	if err != nil {
		t.Fatal(err)
	}
	if got.Places[left].Filter != "A" || got.Places[right].SessionID != "b" {
		t.Fatal("reconnect place lost")
	}
}

func TestAttachLostResponseRetriesSameDurableIntentAndReleasesExactLease(t *testing.T) {
	var mu sync.Mutex
	var requests []protocol.LeaseRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req protocol.LeaseRequest
		json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		requests = append(requests, req)
		count := len(requests)
		mu.Unlock()
		if r.URL.Path == "/v1/release" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if count == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
			return
		}
		json.NewEncoder(w).Encode(specFor(req))
	}))
	defer server.Close()
	m := testModel(t, clientstate.New())
	m.client = &protocol.Client{HTTP: server.Client(), BaseURL: server.URL}
	s := fixtureSession("a", "A")
	m.Update(inventoryMsg{sessions: []domain.Session{s}})
	cmd := m.acquire(false)
	if cmd == nil {
		t.Fatal("no acquisition")
	}
	before, err := clientstate.Load(m.statePath)
	if err != nil {
		t.Fatal(err)
	}
	if before.Pending == nil || before.Pending.OperationID == "" || before.Pending.Generation != 1 {
		t.Fatal("request intent not saved before HTTP effect")
	}
	first := cmd().(attachMsg)
	if first.err == nil {
		t.Fatal("fixture did not lose acknowledgement")
	}
	m.Update(first)
	if m.state.Pending == nil {
		t.Fatal("unknown acquisition discarded")
	}
	retry := m.acquire(false)().(attachMsg)
	if retry.err != nil || first.req != retry.req {
		t.Fatalf("retry changed intent or failed: %+v %+v", first.req, retry.req)
	}
	m.Update(retry)
	if m.state.Pending.Token == "" {
		t.Fatal("lease token not saved before native execution")
	}
	exact := request(*m.state.Pending)
	_, releaseCmd := m.Update(detachedMsg{req: exact})
	m.Update(releaseCmd())
	if m.state.Pending != nil {
		t.Fatal("released lease retained")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 3 || requests[0] != requests[1] || requests[2] != exact {
		t.Fatalf("not exact acquire/retry/release: %+v", requests)
	}
}

func specFor(req protocol.LeaseRequest) domain.AttachSpec {
	return domain.AttachSpec{Argv: []string{"/usr/bin/true"}, ReadOnly: req.ReadOnly, Lease: domain.Lease{SessionID: req.SessionID, ExecutionID: req.ExecutionID, Generation: req.Generation, OperationID: req.OperationID, ClientID: req.ClientID, Token: "lease_fixture"}}
}

func TestAttachRejectsChangedGenerationAndKeepsReadOnlyExplicit(t *testing.T) {
	m := testModel(t, clientstate.New())
	s := fixtureSession("a", "A")
	m.Update(inventoryMsg{sessions: []domain.Session{s}})
	m.acquire(true)
	req := request(*m.state.Pending)
	if !req.ReadOnly {
		t.Fatal("read-only action acquired writer")
	}
	spec := specFor(req)
	spec.Lease.Generation++
	_, cmd := m.Update(attachMsg{req: req, spec: spec})
	if cmd != nil || m.state.Pending == nil || m.state.Pending.Token != "" {
		t.Fatal("mismatched generation entered native terminal")
	}
	m.Update(attachMsg{req: req, err: &protocol.HTTPError{StatusCode: 409, Message: "writer conflict"}})
	if m.state.Pending != nil {
		t.Fatal("durable rejection remained unknown")
	}
	if !strings.Contains(m.status, "v selects read-only") {
		t.Fatal("writer conflict lacks explicit observe action")
	}
	s.Capabilities.ReadOnlyAttach = false
	m.Update(inventoryMsg{sessions: []domain.Session{s}})
	if m.acquire(true) != nil {
		t.Fatal("read-only unsupported path acquired input")
	}
}

func TestFailedReleaseSurvivesReconnectAndNewTargetCannotReplaceIntent(t *testing.T) {
	m := testModel(t, clientstate.New())
	a, b := fixtureSession("a", "A"), fixtureSession("b", "B")
	m.Update(inventoryMsg{sessions: []domain.Session{a, b}})
	m.acquire(false)
	p := m.state.Pending
	p.Token = "old_token"
	m.save()
	req := request(*p)
	m.Update(releasedMsg{req: req, err: errors.New("daemon vanished")})
	m.Update(inventoryMsg{err: errors.New("connection lost")})
	view := m.View()
	if !strings.Contains(view, "Connection offline") || !strings.Contains(view, "lease release pending") || !strings.Contains(view, "x reconcile/release") {
		t.Fatal("connection failure hid the exact pending release recovery action")
	}
	m.Update(inventoryMsg{sessions: []domain.Session{a, b}})
	m.selectSession(b)
	if m.acquire(false) != nil || m.state.Pending.OperationID != req.OperationID {
		t.Fatal("new target overwrote unreconciled lease")
	}
	s, err := clientstate.Load(m.statePath)
	if err != nil {
		t.Fatal(err)
	}
	if s.Pending.Token != "old_token" || s.ClientID != m.state.ClientID {
		t.Fatal("reconnect lost exact lease identity")
	}
	next := newModel(context.Background(), m.client, m.statePath, s)
	next.Init()
	if !next.busy {
		t.Fatal("reconnect allowed input before reconciliation")
	}
}

func TestPlainNarrowViewDoesNotEmitInventoryEscapeSequences(t *testing.T) {
	m := testModel(t, clientstate.New())
	s := fixtureSession("a", "\x1b[2J工程-a")
	m.Update(inventoryMsg{sessions: []domain.Session{s}})
	m.Update(tea.WindowSizeMsg{Width: 24, Height: 8})
	view := m.View()
	if strings.Contains(view, "\x1b") {
		t.Fatal("untrusted name emitted terminal controls")
	}
	lines := strings.Split(view, "\n")
	if len(lines) > 8 {
		t.Fatal("narrow view overflowed height")
	}
	for _, line := range lines {
		if ansi.StringWidth(line) > 24 {
			t.Fatalf("wide Unicode row overflowed: %q", line)
		}
	}
	if !strings.Contains(view, "Enter attach") {
		t.Fatal("narrow view lost primary action")
	}
}

func TestConnectedEntryExplainsUnreachableExecutionHome(t *testing.T) {
	m := testModel(t, clientstate.New())
	m.Update(inventoryMsg{places: []protocol.Place{{HostID: "remote_home", State: "unreachable"}}})
	view := m.View()
	if !strings.Contains(view, "execution homes unreachable; entry remains connected") || !strings.Contains(view, "No permitted reachable sessions") || strings.Contains(view, "Launch with bp run") {
		t.Fatal("connected entry misrepresented execution-home loss")
	}
}

// This is process handoff plumbing, not native CLI/terminal-fidelity proof.
type handoffModel struct{ m *model }

func (h handoffModel) Init() tea.Cmd { return func() tea.Msg { return tea.KeyMsg{Type: tea.KeyEnter} } }
func (h handoffModel) View() string  { return h.m.View() }
func (h handoffModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := h.m.Update(msg)
	if _, ok := msg.(releasedMsg); ok {
		return h, tea.Quit
	}
	return h, cmd
}
func TestExecProcessReturnsToSameSwitcherAndReleases(t *testing.T) {
	var requests []protocol.LeaseRequest
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/sessions" {
			json.NewEncoder(w).Encode(protocol.Inventory{Version: protocol.Version})
			return
		}
		var req protocol.LeaseRequest
		json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		requests = append(requests, req)
		mu.Unlock()
		if r.URL.Path == "/v1/attach" {
			json.NewEncoder(w).Encode(specFor(req))
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s := clientstate.New()
	g, _ := s.AddGroup("API", "")
	s.AddReference(g, clientstate.Reference{Kind: "workspace", Workspace: "/work/api"})
	s.SwitchGroup(g)
	m := testModel(t, s)
	m.ctx = ctx
	m.client = &protocol.Client{HTTP: server.Client(), BaseURL: server.URL}
	a := fixtureSession("a", "A")
	m.Update(inventoryMsg{sessions: []domain.Session{a}})
	want := m.place()
	_, err := tea.NewProgram(handoffModel{m}, tea.WithContext(ctx), tea.WithInput(strings.NewReader("")), tea.WithOutput(io.Discard), tea.WithoutRenderer(), tea.WithoutSignalHandler()).Run()
	if err != nil {
		t.Fatal(err)
	}
	if m.state.Pending != nil || m.state.ActiveGroup != g || !reflect.DeepEqual(m.place(), want) {
		t.Fatal("ExecProcess lost place or lease")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 || requests[0].OperationID != requests[1].OperationID || requests[1].Token != "lease_fixture" {
		t.Fatalf("native process did not release same lease: %+v", requests)
	}
}
