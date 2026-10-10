package tui

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/seanheiney/keelmux/internal/clientstate"
	"github.com/seanheiney/keelmux/internal/domain"
)

func TestResponsiveRowsKeepSelectedAnchorVisible(t *testing.T) {
	m := testModel(t, clientstate.New())
	var rows []domain.Session
	for i := range 35 {
		rows = append(rows, fixtureSession(fmt.Sprintf("session_%02d", i), fmt.Sprintf("Work %02d", i)))
	}
	m.Update(inventoryMsg{sessions: rows})
	for range 28 {
		m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	want := m.place().SessionID
	for _, size := range [][2]int{{120, 32}, {80, 24}, {40, 14}, {24, 8}, {80, 24}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		view := ansi.Strip(m.View())
		if m.place().SessionID != want || !strings.Contains(view, "› Work 28") {
			t.Fatalf("resize %v moved or hid the exact selected row: %q", size, view)
		}
		if !strings.Contains(view, "Enter attach") {
			t.Fatalf("resize %v lost the primary attach action", size)
		}
	}
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 14})
	view := ansi.Strip(m.View())
	if strings.Count(view, "Work ") < 4 {
		t.Fatalf("compact viewport should show several rows, got %q", view)
	}
}

func TestAllViewsClipCellsAndEmitOnlyOwnedPalette(t *testing.T) {
	// OSC, cursor movement, newline, tabs, bidi reordering and wide graphemes
	// are untrusted data in every field, not just the session name.
	poison := "工程 👩‍💻 é \x1b]52;c;clipboard\a\x1b[2J\r\n\t\u202eEND\u2066"
	s := clientstate.New()
	g, err := s.AddGroup(poison, "")
	if err != nil {
		t.Fatal(err)
	}
	s.AddReference(g, clientstate.Reference{Kind: "workspace", Workspace: "/work/api"})
	m := testModel(t, s)
	session := fixtureSession("a", poison)
	session.Workspace, session.ThreadID = poison, domain.ID(poison)
	session.Execution.Provider, session.Execution.State = poison, poison
	session.Execution.ActualModel = domain.Evidence{Value: poison, Source: poison}
	session.Execution.Account = domain.Evidence{Value: poison, Source: poison}
	session.Capabilities.NativeAttach = false
	session.Capabilities.Explanation = poison
	m.Update(inventoryMsg{sessions: []domain.Session{session}})
	m.status, m.input = poison, poison
	m.draft = clientstate.Draft{ThreadID: domain.ID(poison), RecipientID: domain.ID(poison), Text: poison}
	sgr := regexp.MustCompile(`\x1b\[([0-9;]*)m`)
	allowed := map[string]bool{"0": true, "90": true, "1;36": true, "33": true, "7": true}
	for _, color := range []bool{false, true} {
		m.color = color
		for _, screen := range []string{"", "groups", "newgroup", "draft"} {
			m.screen = screen
			for _, size := range [][2]int{{1, 1}, {8, 3}, {24, 8}, {40, 14}, {80, 24}, {120, 32}} {
				m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				view := m.View()
				for _, code := range sgr.FindAllStringSubmatch(view, -1) {
					if !color || !allowed[code[1]] {
						t.Fatalf("unowned color/control in %q: %q", screen, code)
					}
				}
				text := sgr.ReplaceAllString(view, "")
				if strings.ContainsAny(text, "\x1b\a\r\t\u202e\u2066") {
					t.Fatalf("untrusted field escaped sanitization in %q: %q", screen, view)
				}
				lines := strings.Split(view, "\n")
				if len(lines) > size[1] {
					t.Fatalf("%q overflowed height %v", screen, size)
				}
				for _, line := range lines {
					if width := ansi.StringWidth(line); width > size[0] {
						t.Fatalf("%q cell width %d exceeds %v: %q", screen, width, size, line)
					}
				}
			}
		}
	}
}

func TestPaletteUsesTerminalPreferences(t *testing.T) {
	for _, tc := range []struct {
		term, noColor string
		want          bool
	}{{"xterm-256color", "", true}, {"xterm", "1", false}, {"dumb", "", false}, {"", "", false}} {
		t.Setenv("TERM", tc.term)
		t.Setenv("NO_COLOR", tc.noColor)
		m := newModel(nil, nil, "", clientstate.New())
		if m.color != tc.want {
			t.Fatalf("TERM=%q NO_COLOR=%q color=%v", tc.term, tc.noColor, m.color)
		}
	}
}

func TestPendingRecoveryActionsSurviveOfflineNarrowLayout(t *testing.T) {
	m := testModel(t, clientstate.New())
	m.Update(inventoryMsg{sessions: []domain.Session{fixtureSession("a", "A")}})
	m.acquire(false)
	want := *m.state.Pending
	m.state.Pending.Token = "private-test-token"
	m.Update(releasedMsg{req: request(*m.state.Pending), err: errors.New("exact release requires retry")})
	m.Update(inventoryMsg{err: errors.New("\x1b[2J fixture offline")})
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 14})
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "Connection offline") || !strings.Contains(view, "lease release pending") || !strings.Contains(view, "x reconcile/release") {
		t.Fatalf("pending release lost its actionable recovery state: %q", view)
	}
	if strings.Contains(view, m.state.Pending.Token) || m.state.Pending.OperationID != want.OperationID {
		t.Fatal("layout exposed the private token or changed intent identity")
	}
}

func TestResponsiveNavigationPersistsGroupFilterAndAddressedDraft(t *testing.T) {
	s := clientstate.New()
	g, _ := s.AddGroup("API", "")
	s.AddReference(g, clientstate.Reference{Kind: "workspace", Workspace: "/work/api"})
	m := testModel(t, s)
	m.Update(inventoryMsg{sessions: []domain.Session{fixtureSession("a", "Alpha"), fixtureSession("b", "Beta")}})
	m.Update(key("g"))
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(key("/"))
	m.Update(key("Beta"))
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	want := m.place()
	m.Update(key("p"))
	m.Update(key("local text"))
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(key("second line"))
	for _, size := range [][2]int{{24, 8}, {80, 24}, {120, 32}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		if !strings.Contains(m.View(), "second line_") {
			t.Fatalf("draft cursor hidden after resize %v", size)
		}
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.state.ActiveGroup != g || !reflect.DeepEqual(m.place(), want) {
		t.Fatal("visual navigation altered the remembered exact group/filter/selection")
	}
	loaded, err := clientstate.Load(m.statePath)
	if err != nil {
		t.Fatal(err)
	}
	draft, ok := loaded.Draft("thread_b", "lane_b")
	if !ok || draft.Text != "local text\nsecond line" || draft.ExecutionID != "execution_b" || loaded.Pending != nil {
		t.Fatal("draft addressing/content changed or layout acquired an attachment")
	}
}
