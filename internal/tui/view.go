package tui

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/seanheiney/keelmux/internal/domain"
)

type tone uint8

const (
	normal tone = iota
	muted
	accent
	warning
	selected
)

type viewLine struct {
	text string
	tone tone
}

// Every line enters the layout as plain data. Only render adds our finite SGR
// palette, after sanitization and cell clipping; inventory never supplies ANSI.
func line(text string, style tone) viewLine { return viewLine{plain(text), style} }

func (m *model) margin() string {
	if m.width < 52 {
		return " "
	}
	return "  "
}

func (m *model) contentWidth() int { return max(1, m.width-len(m.margin())*2) }

func clipped(text string, width int) string {
	return ansi.Truncate(plain(text), max(0, width), "…")
}

func columns(left, right string, width int) string {
	right = plain(right)
	if ansi.StringWidth(right)+4 >= width {
		return clipped(left, width)
	}
	left = clipped(left, width-ansi.StringWidth(right)-2)
	return left + strings.Repeat(" ", max(2, width-ansi.StringWidth(left)-ansi.StringWidth(right))) + right
}

func (m *model) heading() []viewLine {
	rows := m.visible()
	count := fmt.Sprintf("%d sessions", len(rows))
	if len(rows) == 1 {
		count = "1 session"
	}
	if len(rows) != len(m.sessions) {
		count = fmt.Sprintf("%d / %d sessions", len(rows), len(m.sessions))
	}
	title := "Keelmux / " + m.groupName()
	filter := "/ Search sessions"
	if m.place().Filter != "" || m.searching {
		filter = "/ " + m.place().Filter
	}
	if m.searching {
		filter += "_"
	}
	return []viewLine{line(columns(title, count, m.contentWidth()), accent), line(filter, muted), line(m.sessionRow("SESSION", "PROVIDER", "STATE", "  "), muted)}
}

func (m *model) sessionRow(name, provider, state, marker string) string {
	w := m.contentWidth()
	if w < 48 {
		return columns(marker+name, provider, w)
	}
	providerWidth, stateWidth := 10, 10
	nameWidth := max(1, w-providerWidth-stateWidth-6)
	cell := func(text string, width int) string {
		text = clipped(text, width)
		return text + strings.Repeat(" ", max(0, width-ansi.StringWidth(text)))
	}
	return marker + cell(name, nameWidth) + "  " + cell(provider, providerWidth) + "  " + clipped(state, stateWidth)
}

func (m *model) inventoryNotice() []viewLine {
	var lines []viewLine
	missing := 0
	for _, home := range m.places {
		if home.State != "reachable" {
			missing++
		}
	}
	if missing > 0 {
		lines = append(lines, line(fmt.Sprintf("%d execution homes unreachable; entry remains connected.", missing), warning))
	}
	switch {
	case !m.loaded:
		lines = append(lines, line("Loading authorized inventory…", muted))
	case m.offline != nil:
		lines = append(lines, line("OFFLINE / last observed inventory", warning))
	case len(m.visible()) == 0:
		if len(m.sessions) == 0 && len(m.places) == 0 {
			lines = append(lines, line("No sessions yet.", normal), line("Launch with keelmux run codex or keelmux run claude.", muted))
		} else if len(m.sessions) == 0 {
			lines = append(lines, line("No permitted reachable sessions.", normal), line("Use keelmux directory to inspect execution homes.", muted))
		} else {
			lines = append(lines, line("No matching work in this group.", normal), line("Esc clears search; g opens All work.", muted))
		}
	}
	return lines
}

func (m *model) selectionDetails() []viewLine {
	if s, ok := m.selected(); ok {
		lines := []viewLine{line("", normal), line(s.Workspace+" / "+string(s.Mode), normal)}
		lines = append(lines, runtimeLine(s, m.offline != nil))
		if repair := runtimeRepair(s); repair != "" {
			lines = append(lines, line(repair, warning))
		}
		if m.height >= 16 {
			lines = append(lines, line("Model: "+evidence(s.Execution.ActualModel)+"  Account: "+evidence(s.Execution.Account), muted))
		}
		if m.height >= 20 {
			lines = append(lines, line("Thread: "+string(s.ThreadID)+"  Generation: "+fmt.Sprint(s.Execution.Generation), muted))
		}
		if !s.Capabilities.NativeAttach && m.height >= 20 {
			lines = append(lines, line(capability(s), warning))
		}
		return lines
	}
	if m.place().SessionID != "" {
		return []viewLine{line("", normal), line("Saved selection is unavailable in this view.", warning), line("Its place is retained; choose available work with arrows.", muted)}
	}
	return nil
}

// Inventory names the recorded runtime; attachment capability and liveness are
// separate observations. A missing/uncertain helper never proves process exit.
func runtimeLine(s domain.Session, stale bool) viewLine {
	backend := "unknown"
	switch s.Execution.Runtime.Backend {
	case "native":
		backend = "native"
	case "tmux":
		backend = "legacy tmux"
	}
	state := s.Execution.State
	if state == "" {
		state = "unknown"
	} else if state == "outcome_uncertain" {
		state = "uncertain"
	}
	style := muted
	if state != "running" || backend == "unknown" || stale {
		style = warning
	}
	if stale {
		state += " (stale)"
	}
	return line("Runtime: "+backend+" / "+state, style)
}

func runtimeRepair(s domain.Session) string {
	switch s.Execution.State {
	case "outcome_uncertain":
		return "Outcome unknown; inspect before retry."
	case "missing":
		return "Runtime missing; inspect, reconcile."
	case "stopped", "failed":
		return "Attach unavailable; inspect this run."
	}
	if !s.Capabilities.NativeAttach {
		if s.Execution.Runtime.Backend == "tmux" {
			return "Legacy unavailable; inspect tmux."
		}
		return "Attach unavailable; inspect this run."
	}
	return ""
}

func (m *model) footerText(text string, style tone) []viewLine {
	var out []viewLine
	for _, text := range strings.Split(ansi.Wrap(plain(text), m.contentWidth(), ""), "\n") {
		out = append(out, line(text, style))
	}
	return out
}

func (m *model) switcherFooter() []viewLine {
	var out []viewLine
	if m.offline != nil {
		// Keep the durable pending action and its outcome alongside connection
		// status; a stale inventory never becomes permission to attach.
		out = append(out, line("Connection offline; r reconnects. Attach unavailable.", warning))
	}
	if m.state.PendingChannel != nil {
		out = append(out, line("Saved channel reply awaits its exact receipt; c then r checks.", warning))
	}
	if p := m.state.Pending; p != nil {
		// The saved acquisition can address different work from the current
		// selection. Keep its destination and generation beside recovery, but
		// never render its private token or runtime locators.
		target := string(p.SessionID)
		for _, s := range m.sessions {
			if s.ID == p.SessionID {
				target = s.Name
				break
			}
		}
		out = append(out, line(columns("Saved attach: "+target, fmt.Sprintf("g%d", p.Generation), m.contentWidth()), warning))
	}
	if m.status != "" {
		// Bound transient detail, leaving the action below visible on small
		// terminals. The complete status is retained in the model.
		status := m.status
		if m.state.Pending != nil && strings.Contains(status, "lease release pending") {
			status = "lease release pending; x retries exact release."
		}
		out = append(out, line(status, warning))
	}
	switch {
	case m.busy:
		out = append(out, m.footerText("Waiting for native attachment / lease reconciliation…", accent)...)
	case m.state.Pending != nil:
		if m.width < 52 {
			out = append(out, m.footerText("Enter retry same attach", accent)...)
			out = append(out, m.footerText("x reconcile/release  q quit", accent)...)
		} else {
			out = append(out, m.footerText("Enter retry same attach  x reconcile/release  q quit", accent)...)
		}
	case m.searching:
		out = append(out, m.footerText("Type to search  Enter finish  Esc finish", accent)...)
	default:
		action := "Enter attach  v read-only  / search"
		if m.offline != nil {
			action = "r reconnect  / search  g groups"
		} else if s, ok := m.selected(); !ok || !s.Capabilities.NativeAttach {
			action = "↑/↓ select  / search  g groups"
		} else if !s.Capabilities.ReadOnlyAttach {
			action = "Enter attach  / search"
		}
		if m.width < 52 && m.offline == nil {
			if s, ok := m.selected(); ok && s.Capabilities.NativeAttach {
				action = "Enter attach  v view  / find"
				if !s.Capabilities.ReadOnlyAttach {
					action = "Enter attach  / find"
				}
			}
		}
		out = append(out, m.footerText(action, accent)...)
		if m.width >= 72 {
			out = append(out, line("↑/↓ select  c inbox  g groups  p draft  r refresh  q quit", muted))
		} else if m.width >= 38 {
			out = append(out, line("c inbox  g groups  p draft  q quit", muted))
		} else {
			out = append(out, line("c inbox  g groups  q quit", muted))
		}
	}
	return out
}

func (m *model) View() string {
	switch m.screen {
	case "groups":
		return m.groupsView()
	case "newgroup":
		return m.render([]viewLine{line("Keelmux / new group", accent), line("Groups are optional local navigation.", muted), line("", normal), line("Name: "+m.input+"_", normal)}, []viewLine{line(m.status, warning), line("Enter create  Esc cancel", accent)})
	case "draft":
		return m.draftView()
	case "channels", "channelbinding", "channelreply", "channelhelp":
		return m.channelView()
	}
	body := append(m.heading(), m.inventoryNotice()...)
	rows, p := m.visible(), m.place()
	end := min(len(rows), p.Scroll+m.slots())
	for i := min(p.Scroll, len(rows)); i < end; i++ {
		s := rows[i]
		marker, style := "  ", normal
		if s.ID == p.SessionID {
			marker, style = "› ", selected
		}
		state := s.Execution.State
		if state == "" {
			state = "unknown"
		}
		if m.width < 52 && !s.Capabilities.NativeAttach {
			// A narrow row still exposes an unavailable execution rather than
			// hiding the state behind the provider column.
			stateProvider := state + " / " + s.Execution.Provider
			body = append(body, line(m.sessionRow(s.Name, stateProvider, state, marker), style))
		} else {
			body = append(body, line(m.sessionRow(s.Name, s.Execution.Provider, state, marker), style))
		}
	}
	body = append(body, m.selectionDetails()...)
	return m.render(body, m.switcherFooter())
}

func (m *model) groupsView() string {
	body := []viewLine{line("Keelmux / groups", accent), line("Local collections · sessions keep running", muted), line("", normal)}
	labels := []string{"All work"}
	for _, g := range m.state.Groups {
		name := g.Name
		if parent, ok := m.state.Group(g.ParentID); ok {
			name = parent.Name + " / " + name
		}
		labels = append(labels, name)
	}
	footer := []viewLine{line(m.status, warning)}
	footer = append(footer, m.footerText("Enter open  n new  c child  a add project  s pin session", accent)...)
	footer = append(footer, line("d remove group  Esc return", muted))
	slots := max(1, m.height-len(body)-len(footer))
	start := max(0, m.groupIndex-slots+1)
	for i := start; i < min(len(labels), start+slots); i++ {
		marker, style := "  ", normal
		if i == m.groupIndex {
			marker, style = "› ", selected
		}
		body = append(body, line(marker+labels[i], style))
	}
	return m.render(body, footer)
}

func (m *model) draftView() string {
	body := []viewLine{line("Keelmux / local draft", accent), line("Delivery unavailable; no prompt is submitted.", warning), line("Thread: "+string(m.draft.ThreadID), muted), line("Recipient: "+string(m.draft.RecipientID), muted)}
	for _, s := range m.sessions {
		if s.ThreadID == m.draft.ThreadID && s.LaneID == m.draft.RecipientID {
			body = append(body, line("Current target: "+s.Execution.Provider+" / "+evidence(s.Execution.ActualModel), muted))
			if s.Execution.ID != m.draft.ExecutionID || s.Execution.Generation != m.draft.Generation {
				body = append(body, line("Target changed since this draft began. Review before any future delivery.", warning))
			}
			break
		}
	}
	body = append(body, line("", normal))
	// Keep the editing cursor and latest lines visible; all text remains in
	// the addressed local draft when the terminal is too short to show it.
	footer := []viewLine{line("Esc save & return  Enter newline · local only", accent)}
	text := strings.Split(m.draft.Text+"_", "\n")
	slots := max(1, m.height-len(body)-len(footer))
	for _, text := range text[max(0, len(text)-slots):] {
		body = append(body, line(text, normal))
	}
	return m.render(body, footer)
}

// Also neutralize bidi controls: terminal labels must not reorder exact IDs.
// ZWJ/combining marks remain intact so grapheme-aware cell clipping is useful.
func plain(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r >= '\u202a' && r <= '\u202e' || r >= '\u2066' && r <= '\u2069' || r == '\u061c' || r == '\u200e' || r == '\u200f' {
			return '?'
		}
		return r
	}, s)
}

func (m *model) render(body, footer []viewLine) string {
	// Actions have priority if the viewport cannot hold the full layout.
	if len(footer) > m.height {
		footer = footer[len(footer)-m.height:]
	}
	limit := max(0, m.height-len(footer))
	if len(body) > limit {
		body = body[:limit]
	}
	for len(body) < limit {
		body = append(body, viewLine{})
	}
	lines := append(body, footer...)
	out := make([]string, len(lines))
	for i, row := range lines {
		text := clipped(row.text, m.contentWidth())
		if row.tone == selected {
			text += strings.Repeat(" ", max(0, m.contentWidth()-ansi.StringWidth(text)))
		}
		if text != "" {
			text = m.margin() + m.paint(text, row.tone)
		}
		out[i] = ansi.Truncate(text, m.width, "")
	}
	return strings.Join(out, "\n")
}

func (m *model) paint(text string, style tone) string {
	if !m.color || text == "" || style == normal {
		return text
	}
	sgr := map[tone]string{muted: "90", accent: "1;36", warning: "33", selected: "7"}[style]
	return "\x1b[" + sgr + "m" + text + "\x1b[0m"
}
