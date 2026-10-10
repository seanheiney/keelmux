package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/seanheiney/keelmux/internal/clientstate"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/usage"
)

func usageTestModel(t *testing.T) *model {
	t.Helper()
	s := domain.NewSession(domain.NewID("host"), "Selected work", "/work/api", "codex", "fixture")
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	m := newModel(context.Background(), nil, filepath.Join(dir, "place.json"), clientstate.New())
	m.sessions = []domain.Session{s}
	m.loaded = true
	m.setPlace(clientstate.Place{SessionID: s.ID, ThreadID: s.ThreadID, Filter: "Selected"})
	return m
}

func TestUsagePanelKeepsShortViewportScrollableAndModelAttributionDistinct(t *testing.T) {
	m := usageTestModel(t)
	count := int64(42)
	m.screen = "usage"
	m.usagePanel = &usagePanel{target: m.sessions[0], present: true, projection: usage.Projection{
		Counts: usage.Counts{UnknownActualModel: 3},
		Tokens: []usage.TokenTotal{{Provider: "codex", ActualModel: "model-a", Dimension: "input", ObservedCount: &count}, {Provider: "codex", ActualModel: "model-b", Dimension: "input", ObservedCount: &count}},
	}}
	m.width, m.height = 120, 32
	view := ansi.Strip(m.View())
	for _, want := range []string{string(m.sessions[0].HostID), string(m.sessions[0].ID), "unknown actual model: 3", "model-a / input: 42", "model-b / input: 42"} {
		if !strings.Contains(view, want) {
			t.Fatal("usage attribution omitted", want)
		}
	}
	for _, size := range [][2]int{{40, 5}, {16, 3}, {80, 4}} {
		m.width, m.height = size[0], size[1]
		m.usagePanel.scroll = 0
		first := ansi.Strip(m.View())
		for i := 0; i < 30; i++ {
			m.usageKey(tea.KeyMsg{Type: tea.KeyDown})
			m.View()
		}
		last := ansi.Strip(m.View())
		if first == last || m.usagePanel.scroll == 0 {
			t.Fatal("short viewport clipped every data row", size)
		}
		if len(strings.Split(last, "\n")) > size[1] {
			t.Fatal("usage exceeded short viewport", size)
		}
	}
}

func TestUsagePanelReadsLazilyWithoutCreatingStateOrChangingPlace(t *testing.T) {
	m := usageTestModel(t)
	before := m.place()
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	if cmd == nil || m.screen != "usage" {
		t.Fatal("selected usage was not opened")
	}
	msg := cmd().(usageSnapshotMsg)
	if msg.err != nil || msg.present {
		t.Fatal("missing ledger did not stay unknown", msg.err)
	}
	m.Update(msg)
	if !strings.Contains(m.View(), "No local usage ledger") || !strings.Contains(m.View(), "unknown") {
		t.Fatal("missing evidence presented as known")
	}
	if entries, _ := os.ReadDir(filepath.Dir(m.statePath)); len(entries) != 0 {
		t.Fatal("usage read created client/runtime state")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != "" || m.place().SessionID != before.SessionID || m.place().ThreadID != before.ThreadID || m.place().Filter != before.Filter {
		t.Fatal("usage changed remembered navigation")
	}
	m.Update(msg)
	if m.usagePanel != nil {
		t.Fatal("late read reopened closed view")
	}
}

func TestUsagePanelScopesExactHostAndSessionAndLabelsManualEvidence(t *testing.T) {
	m := usageTestModel(t)
	selected := m.sessions[0]
	now := time.Now().UTC()
	l, err := usage.Open(filepath.Join(filepath.Dir(m.statePath), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	for i, host := range []string{string(selected.HostID), string(domain.NewID("host"))} {
		o := usage.Observation{ID: []string{"one", "other"}[i], Source: usage.Source{ID: []string{"manual-one", "manual-other"}[i], Kind: "manual", Quality: usage.UserEntered, ObservedAt: now, Sequence: 1, Coverage: usage.Unknown}, Attempt: &usage.Attempt{Key: usage.AttemptKey{Provider: "codex", Namespace: "manual-fixture", RequestID: []string{"one", "other"}[i], AttemptID: "one"}, At: now.Add(-time.Minute), Context: usage.Context{Host: host, Session: string(selected.ID)}, ActualModel: "authored-model"}}
		if _, err = l.Ingest(context.Background(), o); err != nil {
			t.Fatal(err)
		}
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	first := m.openUsage()
	old := first().(usageSnapshotMsg)
	newRead := m.readUsage()
	newMsg := newRead().(usageSnapshotMsg)
	m.Update(old)
	if !m.usagePanel.loading {
		t.Fatal("older refresh overwrote current observation")
	}
	m.Update(newMsg)
	p := m.usagePanel.projection
	if p.Counts.DeclaredCalls != 1 || p.Counts.ProviderCalls != 0 || p.Coverage.Scope != usage.Unknown || len(p.ActualModels) != 0 || p.MoneyVisible {
		t.Fatal("scope/manual evidence became provider/account authority", p)
	}
	for _, size := range [][2]int{{40, 14}, {80, 24}, {120, 32}} {
		m.width, m.height = size[0], size[1]
		view := ansi.Strip(m.View())
		for _, line := range strings.Split(view, "\n") {
			if ansi.StringWidth(line) > size[0] {
				t.Fatal("usage overflowed terminal", size, line)
			}
		}
		if len(strings.Split(view, "\n")) > size[1] {
			t.Fatal("usage overflowed height", size)
		}
	}
}
