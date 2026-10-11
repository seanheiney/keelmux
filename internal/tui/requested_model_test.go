package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/seanheiney/keelmux/internal/clientstate"
	"github.com/seanheiney/keelmux/internal/domain"
)

func TestRequestedRouteStaysDistinctFromUnknownActualModel(t *testing.T) {
	m := usageTestModel(t)
	s := fixtureSession("route", "Router work")
	s.Execution.Provider = "opencode"
	s.Execution.RequestedModel = domain.Evidence{Value: "openrouter/vendor/model:variant", Source: "native-argv-declaration"}
	s.Execution.ActualModel, s.Execution.Account = domain.Unknown(), domain.Unknown()
	m.sessions = []domain.Session{s}
	m.loaded = true
	m.width, m.height = 120, 32
	m.setPlace(clientstate.Place{SessionID: s.ID, ThreadID: s.ThreadID})
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "Requested: openrouter/vendor/model:variant") || !strings.Contains(view, "Actual model: unknown") || !strings.Contains(view, "Account: unknown") {
		t.Fatal("requested route not visibly separate from observed acceptance", view)
	}
	for _, size := range [][2]int{{80, 24}, {40, 14}, {16, 3}} {
		m.width, m.height = size[0], size[1]
		view := ansi.Strip(m.View())
		if len(strings.Split(view, "\n")) > size[1] {
			t.Fatal("requested route exceeded rows", size)
		}
		for _, row := range strings.Split(view, "\n") {
			if ansi.StringWidth(row) > size[0] {
				t.Fatal("requested route exceeded columns", size)
			}
		}
	}
}
