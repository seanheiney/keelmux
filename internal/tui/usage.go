package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/usage"
)

type usagePanel struct {
	target           domain.Session
	projection       usage.Projection
	present, loading bool
	err              error
	scroll           int
}
type usageSnapshotMsg struct {
	epoch      uint64
	projection usage.Projection
	present    bool
	err        error
}

func (m *model) openUsage() tea.Cmd {
	s, ok := m.selected()
	if !ok {
		m.status = "Choose a session to inspect its local usage observations."
		return nil
	}
	m.screen = "usage"
	m.usagePanel = &usagePanel{target: s}
	return m.readUsage()
}
func (m *model) readUsage() tea.Cmd {
	if m.usagePanel == nil {
		return nil
	}
	m.usageEpoch++
	epoch, target, path := m.usageEpoch, m.usagePanel.target, filepath.Join(filepath.Dir(m.statePath), "usage.db")
	m.usagePanel.loading = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 2*time.Second)
		defer cancel()
		now := time.Now().UTC()
		q := usage.Query{Start: now.Add(-24 * time.Hour), End: now, Timezone: "UTC", Now: now, MaxAge: 15 * time.Minute, Filter: usage.Context{Host: string(target.HostID), Session: string(target.ID)}, Authorization: usage.Authorization{Sessions: []string{string(target.ID)}}}
		p, present, err := usage.ReadProjection(ctx, path, q)
		return usageSnapshotMsg{epoch: epoch, projection: p, present: present, err: err}
	}
}
func (m *model) acceptUsage(msg usageSnapshotMsg) {
	if m.screen != "usage" || m.usagePanel == nil || msg.epoch != m.usageEpoch {
		return
	}
	m.usagePanel.projection, m.usagePanel.present, m.usagePanel.err, m.usagePanel.loading = msg.projection, msg.present, msg.err, false
}
func (m *model) usageKey(msg tea.KeyMsg) tea.Cmd {
	if m.usagePanel == nil {
		m.screen = ""
		return nil
	}
	switch msg.String() {
	case "esc", "q", "u":
		m.screen = ""
		m.usagePanel = nil
		m.usageEpoch++
	case "r":
		return m.readUsage()
	case "j", "down":
		m.usagePanel.scroll++
	case "k", "up":
		m.usagePanel.scroll = max(0, m.usagePanel.scroll-1)
	}
	return nil
}
func (m *model) usageView() string {
	u := m.usagePanel
	if u == nil {
		return m.render([]viewLine{line("Usage unavailable", warning)}, nil)
	}
	identity := func(label string, id domain.ID) string {
		text := string(id)
		if len(label)+len(text) > m.contentWidth() && len(text) > 16 {
			text = text[:min(8, len(text))] + "…" + text[len(text)-8:]
		}
		return label + text
	}
	body := []viewLine{line("Keelmux / usage · "+u.target.Name, accent), line(identity("Host ", u.target.HostID), muted), line(identity("Session ", u.target.ID), muted), line("Local observations · last 24 hours · UTC", muted), line("Coverage unknown; totals describe the recorded subset.", warning)}
	rows := []viewLine{}
	switch {
	case u.loading:
		rows = append(rows, line("Reading existing local ledger…", muted))
	case u.err != nil:
		rows = append(rows, line("Usage evidence unavailable; ledger preserved.", warning), line("Use keelmux usage --json to inspect the local read failure.", muted))
	default:
		p := u.projection
		if !u.present {
			rows = append(rows, line("No local usage ledger. No state was created.", muted))
		}
		rows = append(rows, line(fmt.Sprintf("Provider calls observed: %d", p.Counts.ProviderCalls), normal), line(fmt.Sprintf("User-entered calls: %d · not provider evidence", p.Counts.DeclaredCalls), muted))
		models := "unknown"
		if len(p.ActualModels) > 0 {
			models = strings.Join(p.ActualModels, ", ")
		}
		rows = append(rows, line("Actual models observed: "+models, normal), line(fmt.Sprintf("Calls with unknown actual model: %d", p.Counts.UnknownActualModel), muted), line(fmt.Sprintf("Stale records: %d · conflicted records: %d", p.Coverage.StaleRecords, p.Coverage.ConflictedRecords), muted))
		tools := "unknown"
		if p.Counts.ToolCalls != nil {
			tools = fmt.Sprint(*p.Counts.ToolCalls)
		}
		rows = append(rows, line(fmt.Sprintf("Tool calls measured: %s · %d calls unknown", tools, p.Counts.UnknownToolCalls), normal))
		if len(p.Tokens) == 0 {
			rows = append(rows, line("Native token measurements: unknown", muted))
		}
		for _, t := range p.Tokens {
			count := "unknown"
			if t.ObservedCount != nil {
				count = fmt.Sprint(*t.ObservedCount)
			}
			actualModel := t.ActualModel
			if actualModel == "" {
				actualModel = "unknown model"
			}
			rows = append(rows, line(t.Provider+" / "+actualModel+" / "+t.Dimension+": "+count, normal))
			if t.IncludedIn != "" {
				rows = append(rows, line("Included in "+t.IncludedIn+"; do not add again.", muted))
			}
			rows = append(rows, line(fmt.Sprintf("%d known · %d unknown · %d conflicted attempts", t.KnownAttempts, t.UnknownAttempts, t.ConflictedAttempts), muted))
		}
		rows = append(rows, line("Accounts and money hidden in this session view.", muted), line("keelmux usage --help opens filters and explicit manual import.", muted))
	}
	footer := m.footerText("↑↓ scroll   r reread   Esc return", accent)
	// Keep observations reachable on short or narrow terminals. Wrapped action
	// rows consume real viewport space; headers yield before the data row.
	if m.height > 1 && len(footer) >= m.height {
		footer = m.footerText("↑↓  r  Esc", accent)
		footer = footer[:min(len(footer), m.height-1)]
	}
	available := max(0, m.height-len(footer))
	body = body[:min(len(body), max(0, available-1))]
	room := max(0, available-len(body))
	u.scroll = max(0, min(u.scroll, max(0, len(rows)-room)))
	body = append(body, rows[u.scroll:min(len(rows), u.scroll+room)]...)
	return m.render(body, footer)
}
