package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/seanheiney/keelmux/internal/clientstate"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
)

func TestObserverOnlyEnterPersistsAndSendsExactReadOnlyIntent(t *testing.T) {
	for _, observer := range []bool{true, false} {
		t.Run(map[bool]string{true: "scoped_observer", false: "native_writer"}[observer], func(t *testing.T) {
			s := domain.NewSession(domain.NewID("host"), "Selected Café", "/work/project", "fixture", "test")
			s.Execution.State = "running"
			s.Capabilities.ObserverOnly = observer
			m := testModel(t, clientstate.New())
			m.width, m.height = 80, 24
			m.Update(inventoryMsg{sessions: []domain.Session{s}})
			m.selectSession(s)
			before := m.View()
			if observer && (!strings.Contains(before, "Enter observe") || strings.Contains(before, "Enter attach")) {
				t.Fatal("observer advertised native writer", before)
			}
			var got protocol.LeaseRequest
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/attach" {
					t.Errorf("unexpected effect %s", r.URL.Path)
					w.WriteHeader(400)
					return
				}
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Error(err)
				}
				_ = json.NewEncoder(w).Encode(domain.AttachSpec{})
			}))
			defer server.Close()
			m.client = &protocol.Client{HTTP: server.Client(), BaseURL: server.URL}
			_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			if cmd == nil || m.state.Pending == nil {
				t.Fatal("explicit Enter did not record attachment")
			}
			durable, err := clientstate.Load(m.statePath)
			if err != nil || durable.Pending == nil || durable.Pending.ReadOnly != observer {
				t.Fatal("durable intent lost actual observer choice", err, durable.Pending)
			}
			_ = cmd()
			if got.ReadOnly != observer || got.SessionID != s.ID || got.ExecutionID != s.Execution.ID || got.Generation != s.Execution.Generation || got.OperationID != durable.Pending.OperationID || got.ClientID != durable.Pending.ClientID || got.Token != "" {
				t.Fatal("wrong native authority sent", got)
			}
		})
	}
}
