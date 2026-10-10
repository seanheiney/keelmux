// Package tui is the single terminal switcher. Native rendering remains with
// the daemon's terminal backend; this package never forwards raw keystrokes.
package tui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/seanheiney/keelmux/internal/clientstate"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
)

// Run reuses the caller's sole daemon connection. Navigation has no model cost.
func Run(ctx context.Context, client *protocol.Client, statePath string) error {
	if client == nil || statePath == "" {
		return errors.New("switcher requires a daemon client and local place path")
	}
	lock, err := clientstate.AcquireLock(statePath)
	if err != nil {
		return err
	}
	defer lock.Close()
	s, err := clientstate.Load(statePath)
	if err != nil {
		return err
	}
	if err := clientstate.Save(statePath, s); err != nil {
		return err
	}
	m := newModel(ctx, client, statePath, s)
	_, runErr := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx)).Run()
	// Program shutdown may prevent the detach callback from being processed.
	// A failed release remains durable for reconnect reconciliation.
	if m.state.Pending != nil && m.state.Pending.Token != "" {
		if err := release(client, request(*m.state.Pending)); err == nil {
			m.state.Pending = nil
		} else if runErr == nil {
			runErr = err
		}
	}
	if err := clientstate.Save(statePath, m.state); err != nil && runErr == nil {
		runErr = err
	}
	return runErr
}

type inventoryMsg struct {
	sessions []domain.Session
	err      error
	places   []protocol.Place
}
type pollMsg struct{}
type attachMsg struct {
	req    protocol.LeaseRequest
	spec   domain.AttachSpec
	err    error
	cancel bool
}
type detachedMsg struct {
	req protocol.LeaseRequest
	err error
}
type releasedMsg struct {
	req       protocol.LeaseRequest
	err       error
	nativeErr error
}

type model struct {
	ctx                  context.Context
	client               *protocol.Client
	statePath            string
	state                clientstate.State
	sessions             []domain.Session
	places               []protocol.Place
	loaded               bool
	inventoryInitialized bool
	offline              error
	busy                 bool
	width, height        int
	screen               string
	searching            bool
	groupIndex           int
	groupParent          domain.ID
	input                string
	draft                clientstate.Draft
	status               string
	deleting             domain.ID
}

func newModel(ctx context.Context, client *protocol.Client, path string, s clientstate.State) *model {
	return &model{ctx: ctx, client: client, statePath: path, state: s, width: 80, height: 24}
}

func (m *model) Init() tea.Cmd {
	if m.state.Pending != nil && m.state.Pending.Token != "" {
		m.busy = true
		m.status = "Reconciling the prior attachment; observing until its lease is released."
		return tea.Batch(m.fetch(), m.releaseCommand(request(*m.state.Pending), nil))
	}
	if m.state.Pending != nil {
		m.status = "Prior attach outcome unknown. Enter retries its exact target; x reconciles cancellation."
	}
	return m.fetch()
}

func (m *model) fetch() tea.Cmd {
	return func() tea.Msg {
		d, err := m.client.Directory(m.ctx)
		return inventoryMsg{sessions: d.Sessions, err: err, places: d.Places}
	}
}
func poll() tea.Cmd { return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return pollMsg{} }) }
func (m *model) save() bool {
	if err := clientstate.Save(m.statePath, m.state); err != nil {
		m.status = "Place could not be saved: " + err.Error()
		return false
	}
	return true
}
func (m *model) place() clientstate.Place     { return m.state.Places[m.state.ActiveGroup] }
func (m *model) setPlace(p clientstate.Place) { m.state.Places[m.state.ActiveGroup] = p }

func (m *model) visible() []domain.Session {
	f := strings.ToLower(m.place().Filter)
	var out []domain.Session
	for _, s := range m.sessions {
		if !m.state.Contains(m.state.ActiveGroup, s) {
			continue
		}
		search := strings.Join([]string{s.Name, s.Workspace, s.Execution.Provider, s.Execution.ActualModel.Value, s.Execution.Account.Value, string(s.HostID), string(s.WorkID), string(s.ThreadID), s.Execution.NativeThread.Value}, " ")
		for _, g := range m.state.Groups {
			if m.state.Contains(g.ID, s) {
				search += " " + g.Name
			}
		}
		if f != "" && !strings.Contains(strings.ToLower(search), f) {
			continue
		}
		out = append(out, s)
	}
	return out
}
func (m *model) selected() (domain.Session, bool) {
	for _, s := range m.visible() {
		if s.ID == m.place().SessionID {
			return s, true
		}
	}
	return domain.Session{}, false
}
func (m *model) selectSession(s domain.Session) {
	p := m.place()
	p.SessionID, p.ThreadID = s.ID, s.ThreadID
	m.setPlace(p)
	m.keepVisible()
	m.save()
}
func (m *model) slots() int { return max(1, m.height-13) }
func (m *model) keepVisible() {
	p := m.place()
	rows := m.visible()
	p.Scroll = max(0, min(p.Scroll, max(0, len(rows)-m.slots())))
	for i, s := range rows {
		if s.ID != p.SessionID {
			continue
		}
		if i < p.Scroll {
			p.Scroll = i
		}
		if i >= p.Scroll+m.slots() {
			p.Scroll = i - m.slots() + 1
		}
		break
	}
	m.setPlace(p)
}
func (m *model) move(delta int) {
	rows := m.visible()
	if len(rows) == 0 {
		return
	}
	i := -1
	for j, s := range rows {
		if s.ID == m.place().SessionID {
			i = j
			break
		}
	}
	if i == -1 {
		if delta < 0 {
			i = len(rows) - 1
		} else {
			i = 0
		}
	} else {
		i = max(0, min(len(rows)-1, i+delta))
	}
	m.selectSession(rows[i])
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(1, msg.Width), max(1, msg.Height)
		m.keepVisible()
	case inventoryMsg:
		m.loaded = true
		m.offline = msg.err
		if msg.err == nil {
			initial := !m.inventoryInitialized
			m.inventoryInitialized = true
			m.sessions = msg.sessions
			m.places = msg.places
			sort.SliceStable(m.sessions, func(i, j int) bool {
				if m.sessions[i].Name == m.sessions[j].Name {
					return m.sessions[i].ID < m.sessions[j].ID
				}
				return m.sessions[i].Name < m.sessions[j].Name
			})
			// Only a genuinely empty saved place receives an initial selection.
			if initial && m.place().SessionID == "" {
				if rows := m.visible(); len(rows) > 0 {
					m.selectSession(rows[0])
				}
			}
			m.keepVisible()
		}
		return m, poll()
	case pollMsg:
		return m, m.fetch()
	case attachMsg:
		if msg.err != nil {
			m.busy = false
			var rejection *protocol.HTTPError
			if errors.As(msg.err, &rejection) && rejection.StatusCode >= 400 && rejection.StatusCode < 500 && (m.state.Pending == nil || m.state.Pending.Token == "") {
				m.state.Pending = nil
				m.save()
				m.status = "Attach refused: " + msg.err.Error() + ". v selects read-only where supported."
			} else {
				m.status = "Attach outcome unknown: " + msg.err.Error() + ". Enter retries the same intent; x cancels after reconciliation."
			}
			return m, nil
		}
		if err := validSpec(msg.req, msg.spec); err != nil {
			m.busy = false
			m.status = "Attach reply rejected: " + err.Error() + ". Pending intent retained; x reconciles it."
			return m, nil
		}
		pending := attachment(msg.req)
		pending.Token = msg.spec.Lease.Token
		m.state.Pending = &pending
		if !m.save() {
			return m, m.releaseCommand(request(pending), nil)
		}
		if msg.cancel {
			return m, m.releaseCommand(request(pending), nil)
		}
		m.status = "Native terminal: Ctrl-G d detaches; Ctrl-G Ctrl-G sends the literal prefix."
		cmd := exec.CommandContext(m.ctx, msg.spec.Argv[0], msg.spec.Argv[1:]...)
		return m, tea.ExecProcess(cmd, func(err error) tea.Msg { return detachedMsg{request(pending), err} })
	case detachedMsg:
		return m, m.releaseCommand(msg.req, msg.err)
	case releasedMsg:
		m.busy = false
		if msg.err != nil {
			m.status = "Native attachment ended; lease release pending: " + msg.err.Error() + ". x retries exact release."
		} else {
			if p := m.state.Pending; p != nil && p.OperationID == msg.req.OperationID {
				m.state.Pending = nil
			}
			m.status = "Returned to the same work and group."
			if msg.nativeErr != nil {
				m.status = "Native attach ended: " + msg.nativeErr.Error()
			}
			m.save()
		}
		return m, m.fetch()
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if m.busy {
			return m, nil
		}
		if m.screen == "draft" {
			return m, m.draftKey(msg)
		}
		if m.screen == "newgroup" {
			return m, m.nameKey(msg)
		}
		if m.screen == "groups" {
			return m, m.groupKey(msg)
		}
		if m.searching {
			if msg.String() == "esc" || msg.String() == "enter" {
				m.searching = false
				m.save()
				return m, nil
			}
			p := m.place()
			p.Filter = edit(p.Filter, msg)
			m.setPlace(p)
			// Search is an explicit navigation action, so choose a matching row.
			if rows := m.visible(); len(rows) > 0 {
				m.selectSession(rows[0])
			} else {
				p = m.place()
				p.SessionID, p.ThreadID = "", ""
				m.setPlace(p)
				m.save()
			}
			return m, nil
		}
		switch msg.String() {
		case "q":
			return m, tea.Quit
		case "down", "j":
			m.move(1)
		case "up", "k":
			m.move(-1)
		case "/":
			m.searching = true
		case "esc":
			p := m.place()
			p.Filter = ""
			m.setPlace(p)
			m.keepVisible()
			m.save()
		case "g":
			m.screen = "groups"
			m.groupIndex = 0
			for i, g := range m.state.Groups {
				if g.ID == m.state.ActiveGroup {
					m.groupIndex = i + 1
				}
			}
		case "b":
			m.restoreGroup(m.state.PreviousGroup)
		case "r":
			return m, m.fetch()
		case "enter":
			return m, m.acquire(false)
		case "v":
			return m, m.acquire(true)
		case "x":
			if m.state.Pending != nil {
				m.busy = true
				req := request(*m.state.Pending)
				if req.Token != "" {
					return m, m.releaseCommand(req, nil)
				}
				return m, m.attachCommand(req, true)
			}
		case "p":
			if s, ok := m.selected(); ok {
				m.screen = "draft"
				m.draft, _ = m.state.Draft(s.ThreadID, s.LaneID)
				if m.draft.ThreadID == "" {
					m.draft = clientstate.Draft{ThreadID: s.ThreadID, RecipientID: s.LaneID, ExecutionID: s.Execution.ID, Generation: s.Execution.Generation}
				}
			}
		case "?":
			m.status = "Enter owns native input; v observes read-only. Groups and drafts are local. No semantic prompt route is available."
		}
	}
	return m, nil
}

func (m *model) acquire(readOnly bool) tea.Cmd {
	if m.offline != nil {
		m.status = "Daemon offline; reconnect inventory before attaching."
		return nil
	}
	s, ok := m.selected()
	if !ok {
		m.status = "Saved selection is unavailable; use arrows to select available work."
		return nil
	}
	if !s.Capabilities.NativeAttach || (readOnly && !s.Capabilities.ReadOnlyAttach) {
		m.status = capability(s)
		return nil
	}
	req := protocol.LeaseRequest{SessionID: s.ID, ExecutionID: s.Execution.ID, Generation: s.Execution.Generation, ClientID: m.state.ClientID, OperationID: domain.NewID("attach"), ReadOnly: readOnly}
	if m.state.Pending != nil {
		old := request(*m.state.Pending)
		if old.SessionID != req.SessionID || old.ExecutionID != req.ExecutionID || old.Generation != req.Generation || old.ReadOnly != readOnly {
			m.status = "A prior attachment still needs reconciliation. x releases or cancels its exact intent first."
			return nil
		}
		req = old
	} else {
		pending := attachment(req)
		m.state.Pending = &pending
	}
	if !m.save() {
		return nil
	}
	m.busy = true
	m.status = "Acquiring native attachment for this execution generation..."
	return m.attachCommand(req, false)
}
func (m *model) attachCommand(req protocol.LeaseRequest, cancel bool) tea.Cmd {
	return func() tea.Msg { spec, err := m.client.Attach(m.ctx, req); return attachMsg{req, spec, err, cancel} }
}
func (m *model) releaseCommand(req protocol.LeaseRequest, nativeErr error) tea.Cmd {
	return func() tea.Msg { return releasedMsg{req, release(m.client, req), nativeErr} }
}
func release(client *protocol.Client, req protocol.LeaseRequest) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return client.Release(ctx, req)
}
func request(p clientstate.Attachment) protocol.LeaseRequest {
	return protocol.LeaseRequest{SessionID: p.SessionID, ExecutionID: p.ExecutionID, Generation: p.Generation, OperationID: p.OperationID, ClientID: p.ClientID, ReadOnly: p.ReadOnly, Token: p.Token}
}
func attachment(r protocol.LeaseRequest) clientstate.Attachment {
	return clientstate.Attachment{SessionID: r.SessionID, ExecutionID: r.ExecutionID, Generation: r.Generation, OperationID: r.OperationID, ClientID: r.ClientID, ReadOnly: r.ReadOnly, Token: r.Token}
}
func validSpec(req protocol.LeaseRequest, spec domain.AttachSpec) error {
	l := spec.Lease
	if l.SessionID != req.SessionID || l.ExecutionID != req.ExecutionID || l.Generation != req.Generation || l.OperationID != req.OperationID || l.ClientID != req.ClientID || l.Token == "" || spec.ReadOnly != req.ReadOnly {
		return errors.New("lease does not match requested client, operation and generation")
	}
	if len(spec.Argv) == 0 || spec.Argv[0] == "" {
		return errors.New("daemon returned no native attach executable")
	}
	return nil
}

func (m *model) groupID() domain.ID {
	if m.groupIndex == 0 {
		return ""
	}
	return m.state.Groups[m.groupIndex-1].ID
}
func (m *model) restoreGroup(id domain.ID) {
	if err := m.state.SwitchGroup(id); err != nil {
		m.status = err.Error()
		return
	}
	if m.place().SessionID == "" {
		if rows := m.visible(); len(rows) > 0 {
			m.selectSession(rows[0])
		}
	}
	m.keepVisible()
	m.save()
}
func (m *model) groupKey(key tea.KeyMsg) tea.Cmd {
	if key.String() != "d" {
		m.deleting = ""
	}
	switch key.String() {
	case "esc", "g", "q":
		m.screen = ""
	case "down", "j":
		m.groupIndex = min(len(m.state.Groups), m.groupIndex+1)
	case "up", "k":
		m.groupIndex = max(0, m.groupIndex-1)
	case "enter":
		m.restoreGroup(m.groupID())
		m.screen = ""
	case "n", "c":
		m.groupParent = ""
		if key.String() == "c" {
			m.groupParent = m.groupID()
		}
		m.screen = "newgroup"
		m.input = ""
	case "a", "s":
		if s, ok := m.selected(); ok {
			ref := clientstate.Reference{Kind: "workspace", Workspace: s.Workspace, HostID: s.HostID}
			if key.String() == "s" {
				ref = clientstate.Reference{Kind: "session", ID: s.ID}
			}
			if err := m.state.AddReference(m.groupID(), ref); err != nil {
				m.status = err.Error()
			} else {
				m.status = "Added selected " + ref.Kind + " to the group; work continues."
				m.save()
			}
		} else {
			m.status = "Return to All work and select a session before adding membership."
		}
	case "d":
		id := m.groupID()
		if id == "" {
			return nil
		}
		if m.deleting != id {
			m.deleting = id
			m.status = "Press d again to remove only this navigation group. Its sessions keep running."
		} else {
			m.state.DeleteGroup(id)
			m.groupIndex = min(m.groupIndex, len(m.state.Groups))
			m.deleting = ""
			m.status = "Group removed. Sessions and drafts retained."
			m.save()
		}
	}
	return nil
}
func (m *model) nameKey(key tea.KeyMsg) tea.Cmd {
	switch key.String() {
	case "esc":
		m.screen = "groups"
	case "enter":
		if id, err := m.state.AddGroup(m.input, m.groupParent); err != nil {
			m.status = err.Error()
		} else {
			m.screen = "groups"
			m.groupIndex = len(m.state.Groups)
			m.status = "Group created; a adds selected project, s pins the selected session."
			_ = id
			m.save()
		}
	default:
		m.input = edit(m.input, key)
	}
	return nil
}
func (m *model) draftKey(key tea.KeyMsg) tea.Cmd {
	if key.String() == "esc" {
		m.screen = ""
		m.status = "Draft saved locally. Delivery is unavailable; use Enter for the native terminal."
		m.save()
		return nil
	}
	if key.String() == "enter" {
		m.draft.Text += "\n"
	} else {
		m.draft.Text = edit(m.draft.Text, key)
	}
	m.state.SetDraft(m.draft)
	m.save()
	return nil
}
func edit(s string, key tea.KeyMsg) string {
	if key.String() == "backspace" || key.String() == "ctrl+h" {
		r := []rune(s)
		if len(r) > 0 {
			return string(r[:len(r)-1])
		}
		return s
	}
	if key.Type == tea.KeyRunes {
		return s + string(key.Runes)
	}
	if key.Type == tea.KeySpace {
		return s + " "
	}
	return s
}

func evidence(e domain.Evidence) string {
	if e.Value == "" || e.Source == "" || e.Source == "unknown" {
		return "unknown"
	}
	return e.Value + " [" + e.Source + "]"
}
func capability(s domain.Session) string {
	if s.Capabilities.Explanation != "" {
		return s.Capabilities.Explanation
	}
	if !s.Capabilities.NativeAttach {
		return "Native attachment unavailable; capabilities unverified. Use bp link status for the supported route."
	}
	return "Native attach supported. Semantic prompt, account and model controls are unverified."
}
func (m *model) groupName() string {
	if g, ok := m.state.Group(m.state.ActiveGroup); ok {
		if p, ok := m.state.Group(g.ParentID); ok {
			return p.Name + " / " + g.Name
		}
		return g.Name
	}
	return "All work"
}

func (m *model) View() string {
	if m.screen == "groups" {
		return m.groupsView()
	}
	if m.screen == "newgroup" {
		return m.fit([]string{"KEELMUX / new group", "Name: " + m.input + "_", "Groups are optional local navigation.", ""}, []string{m.status, "Enter create  Esc cancel"})
	}
	if m.screen == "draft" {
		return m.draftView()
	}
	p := m.place()
	rows := m.visible()
	lines := []string{"KEELMUX / " + m.groupName(), fmt.Sprintf("%d in view / %d total   g groups", len(rows), len(m.sessions)), "Search / " + p.Filter}
	unreachable := 0
	for _, home := range m.places {
		if home.State != "reachable" {
			unreachable++
		}
	}
	if unreachable > 0 {
		lines = append(lines, fmt.Sprintf("%d execution homes unreachable; entry remains connected.", unreachable))
	}
	if m.searching {
		lines[2] += "_"
	}
	if !m.loaded {
		lines = append(lines, "Loading authorized inventory...")
	} else if m.offline != nil {
		lines = append(lines, "OFFLINE / last observed inventory", "Reconnect with r. Attach is unavailable.")
	} else if len(rows) == 0 {
		if len(m.sessions) == 0 {
			if len(m.places) > 0 {
				lines = append(lines, "No permitted reachable sessions.", "Use bp directory to inspect execution homes.")
			} else {
				lines = append(lines, "No sessions yet.", "Launch with bp run codex or bp run claude.")
			}
		} else {
			lines = append(lines, "No matching work in this group.", "Esc clears search; g opens All work.")
		}
	}
	end := min(len(rows), p.Scroll+m.slots())
	for i := min(p.Scroll, len(rows)); i < end; i++ {
		s := rows[i]
		marker := "  "
		if s.ID == p.SessionID {
			marker = "> "
		}
		state := s.Execution.State
		if state == "" {
			state = "unknown"
		}
		lines = append(lines, marker+s.Name+"  ["+s.Execution.Provider+" / "+state+"]")
	}
	if s, ok := m.selected(); ok {
		lines = append(lines, "", s.Workspace+" / "+string(s.Mode), "Model: "+evidence(s.Execution.ActualModel)+"  Account: "+evidence(s.Execution.Account), "Thread: "+string(s.ThreadID), capability(s))
	} else if p.SessionID != "" {
		lines = append(lines, "", "Saved selection is unavailable in this view.", "Its place is retained; choose available work with arrows.")
	}
	status := m.status
	if m.offline != nil {
		status = "Connection offline: " + m.offline.Error()
	}
	help := "Enter attach  v read-only  / search  g groups  p draft  q quit"
	if m.busy {
		help = "Waiting for native attachment / lease reconciliation..."
	} else if m.state.Pending != nil {
		help = "Enter retry same attach  x reconcile/release  q quit"
	} else if m.searching {
		help = "Type to search this group  Enter finish  Esc finish"
	}
	if m.width < 52 && !m.busy && !m.searching && m.state.Pending == nil {
		help = "Enter attach  v view  / find  g groups  q quit"
	}
	footer := []string{status, help}
	if m.offline != nil && m.state.Pending != nil && m.status != "" {
		// Connection failure must not hide the saved acquisition's recovery
		// state. Its exact intent still needs reconciliation after reconnect.
		footer = []string{status, m.status, help}
	}
	return m.fit(lines, footer)
}
func (m *model) groupsView() string {
	lines := []string{"KEELMUX / groups", "Optional collections; All work includes ungrouped sessions."}
	labels := []string{"All work"}
	for _, g := range m.state.Groups {
		name := g.Name
		if p, ok := m.state.Group(g.ParentID); ok {
			name = p.Name + " / " + name
		}
		labels = append(labels, name)
	}
	slots := max(1, m.height-7)
	start := max(0, m.groupIndex-slots+1)
	for i := start; i < min(len(labels), start+slots); i++ {
		marker := "  "
		if i == m.groupIndex {
			marker = "> "
		}
		lines = append(lines, marker+labels[i])
	}
	return m.fit(lines, []string{m.status, "Enter open  n new  c child  a add project  s pin session", "d remove group  Esc return"})
}
func (m *model) draftView() string {
	lines := []string{"KEELMUX / local draft", "Thread: " + string(m.draft.ThreadID), "Recipient: " + string(m.draft.RecipientID), "Delivery unavailable; no prompt is submitted."}
	for _, s := range m.sessions {
		if s.ThreadID == m.draft.ThreadID && s.LaneID == m.draft.RecipientID {
			lines = append(lines, "Current target: "+s.Execution.Provider+" / "+evidence(s.Execution.ActualModel))
			if s.Execution.ID != m.draft.ExecutionID || s.Execution.Generation != m.draft.Generation {
				lines = append(lines, "Target changed since this draft began. Review before any future delivery.")
			}
			break
		}
	}
	lines = append(lines, "")
	lines = append(lines, strings.Split(m.draft.Text+"_", "\n")...)
	return m.fit(lines, []string{m.status, "Text saves to this thread/recipient. Esc returns; Enter newline."})
}

// Inventory names are untrusted text: strip controls before measuring cells.
func plain(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '?'
		}
		return r
	}, s)
}
func (m *model) fit(lines, footer []string) string {
	limit := max(0, m.height-len(footer))
	if len(lines) > limit {
		lines = lines[:limit]
	}
	for len(lines) < limit {
		lines = append(lines, "")
	}
	lines = append(lines, footer...)
	if len(lines) > m.height {
		lines = lines[len(lines)-m.height:]
	}
	for i, s := range lines {
		lines[i] = ansi.Truncate(plain(s), m.width, "")
	}
	return strings.Join(lines, "\n")
}
