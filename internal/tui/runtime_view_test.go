package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/seanheiney/keelmux/internal/clientstate"
	"github.com/seanheiney/keelmux/internal/domain"
)

func TestRuntimeStripUsesOnlyRecordedBackendAndKeepsUnknownExplicit(t *testing.T) {
	for _, tc := range []struct{ backend, state, want string }{
		{"native", "running", "Runtime: native / running"},
		{"tmux", "running", "Runtime: legacy tmux / running"},
		{"", "running", "Runtime: unknown / running"},
		{"future-backend", "", "Runtime: unknown / unknown"},
		{"native", "outcome_uncertain", "Runtime: native / uncertain"},
	} {
		t.Run(tc.backend+"/"+tc.state, func(t *testing.T) {
			m := testModel(t, clientstate.New())
			s := fixtureSession("a", "Alpha")
			s.Execution.Runtime.Backend, s.Execution.State = tc.backend, tc.state
			// Managed mode and a plausible native path cannot fill absent evidence.
			s.Mode, s.Execution.Runtime.Socket = domain.Managed, "/private/native/execution_a.sock"
			m.Update(inventoryMsg{sessions: []domain.Session{s}})
			for _, size := range [][2]int{{40, 14}, {80, 24}, {120, 32}} {
				m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				view := ansi.Strip(m.View())
				for _, want := range []string{tc.want, "› Alpha", "/work/api / managed", "Enter attach"} {
					if !strings.Contains(view, want) {
						t.Fatalf("%v lost %q: %q", size, want, view)
					}
				}
				if strings.Contains(view, s.Execution.Runtime.Socket) {
					t.Fatal("ordinary runtime strip exposed a private locator")
				}
			}
		})
	}
}

func TestUnavailableRuntimeKeepsRepairStateAndNavigationOnSmallTerminal(t *testing.T) {
	for _, tc := range []struct{ backend, state, repair string }{
		{"native", "missing", "Runtime missing; inspect, reconcile."},
		{"native", "outcome_uncertain", "Outcome unknown; inspect before retry."},
		{"tmux", "running", "Legacy unavailable; inspect tmux."},
		{"native", "stopped", "Attach unavailable; inspect this run."},
	} {
		m := testModel(t, clientstate.New())
		s := fixtureSession("a", "Alpha")
		s.Execution.Runtime.Backend, s.Execution.State = tc.backend, tc.state
		s.Capabilities.NativeAttach, s.Capabilities.ReadOnlyAttach = false, false
		m.Update(inventoryMsg{sessions: []domain.Session{s}})
		m.Update(tea.WindowSizeMsg{Width: 40, Height: 14})
		view := ansi.Strip(m.View())
		if !strings.Contains(view, tc.repair) || !strings.Contains(view, "↑/↓ select") || strings.Contains(view, "Enter attach") {
			t.Fatalf("unavailable %s/%s lost state or offered attachment: %q", tc.backend, tc.state, view)
		}
	}
}

func TestSavedRecoveryShowsOriginalTargetAfterSelectionAndGenerationChange(t *testing.T) {
	m := testModel(t, clientstate.New())
	a, b := fixtureSession("a", "Original work"), fixtureSession("b", "Selected work")
	a.Execution.Runtime.Backend, b.Execution.Runtime.Backend = "native", "native"
	m.Update(inventoryMsg{sessions: []domain.Session{a, b}})
	m.acquire(false)
	want := *m.state.Pending
	m.state.Pending.Token = "private-lease-token"
	m.Update(releasedMsg{req: request(*m.state.Pending), err: errors.New("offline")})
	m.selectSession(b)
	// Even a replacement generation cannot retarget the saved acquisition.
	a.Execution.Generation = 2
	m.Update(inventoryMsg{sessions: []domain.Session{a, b}})
	m.Update(inventoryMsg{err: errors.New("offline")})
	for _, size := range [][2]int{{40, 14}, {80, 24}, {120, 32}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		view := ansi.Strip(m.View())
		for _, text := range []string{"› Selected work", "Saved attach: Original work", "g1", "lease release pending", "x reconcile/release", "(stale)"} {
			if !strings.Contains(view, text) {
				t.Fatalf("%v lost original recovery context %q: %q", size, text, view)
			}
		}
		if strings.Contains(view, "private-lease-token") || m.state.Pending.SessionID != want.SessionID || m.state.Pending.Generation != want.Generation || m.state.Pending.OperationID != want.OperationID {
			t.Fatal("render changed acquisition authority or exposed its token")
		}
	}
}
