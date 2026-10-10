package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/seanheiney/keelmux/internal/protocol"
)

func (m *model) destinationLines(b protocol.ChannelStatus) []viewLine {
	if m.width >= 72 {
		return []viewLine{
			line(string(b.Provider)+" / "+b.ChannelID+" / root: "+rootLabel(b), accent),
			line("Endpoint: "+b.Endpoint+" / realm: "+b.RealmID, muted),
			line("Bot: "+b.SelfID+" / binding: "+string(b.ID), muted),
		}
	}
	rows := []viewLine{line(string(b.Provider)+" / "+b.ChannelID, accent), line("Root: "+rootLabel(b), muted), line("Bot: "+b.SelfID+" / "+b.RealmID, muted)}
	if m.screen != "channels" {
		rows = append(rows, line("Binding: "+string(b.ID), muted), line("Endpoint: "+b.Endpoint, muted))
	}
	return rows
}

func intakeLabel(b protocol.ChannelStatus) string {
	status := "not observed"
	switch b.Intake {
	case "idle", "healthy", "unavailable", "disabled", "gap":
		status = b.Intake
	}
	if b.HistoryGap {
		status += " / history gap"
	} else if b.MoreHistory {
		status += " / more remote history"
	}
	return "Intake: " + status + " · delivery receipts are separate"
}

func rootLabel(b protocol.ChannelStatus) string {
	if b.IRCAddressed {
		return "project channel (IRC)"
	}
	if b.RootID == "" {
		return "channel (IRC)"
	}
	return b.RootID
}

func (m *model) channelView() string {
	body := []viewLine{line("Keelmux / authored inbox", accent), line("Thread: "+string(m.channelThread), muted)}
	footer := []viewLine{}
	if m.channelError != "" {
		footer = append(footer, line(m.channelError, warning))
	}
	if m.status != "" {
		footer = append(footer, line(m.status, warning))
	}
	if m.channelSending {
		footer = append(footer, line("Checking this immutable reply…", warning))
	}
	if p := m.state.PendingChannel; p != nil {
		footer = append(footer, line("Saved reply: "+string(p.ThreadID)+" / "+string(p.BindingID), warning))
	}
	switch m.screen {
	case "channelhelp":
		body[0] = line("Keelmux / inbox keys", accent)
		for _, text := range []string{"b  Choose one active destination", "p  Compose local channel reply", "Ctrl-S  Queue the exact saved reply", "Enter  Add a newline while composing", "↑/↓  Select authored note", "PgUp/PgDn  Scroll selected note text", "[ / ]  Previous / next history page", "d  Query exact local delivery receipt", "u  Reconcile uncertain receipt read-only", "r  Refresh snapshot / pending receipt", "Incoming text never becomes model work"} {
			body = append(body, line(text, normal))
		}
		footer = append(footer, line("Esc inbox", accent))
	case "channelreply":
		body[0] = line("Keelmux / channel reply", accent)
		if m.channelBinding != nil {
			body = append(body, m.destinationLines(*m.channelBinding)...)
		}
		body = append(body, line("Chat only · no model submission", muted))
		footer = append(footer, m.footerText("Ctrl-S queue reply  Enter newline  Esc inbox", accent)...)
		text := strings.Split(m.channelDraft.Text+"_", "\n")
		var wrapped []string
		for _, row := range text {
			wrapped = append(wrapped, strings.Split(ansi.Wrap(plain(row), m.contentWidth(), ""), "\n")...)
		}
		slots := max(1, m.height-len(body)-len(footer))
		for _, row := range wrapped[max(0, len(wrapped)-slots):] {
			body = append(body, line(row, normal))
		}
	case "channelbinding":
		body[0] = line("Keelmux / choose destination", accent)
		body = append(body, line("One active destination · no automatic fanout", muted))
		if len(m.channelBindings) != 0 {
			body = append(body, m.destinationLines(m.channelBindings[m.channelIndex])...)
		}
		footer = append(footer, m.footerText("↑/↓ choose  Enter confirm  Esc inbox", accent)...)
		slots := max(1, m.height-len(body)-len(footer))
		start := max(0, m.channelIndex-slots+1)
		for i := start; i < min(len(m.channelBindings), start+slots); i++ {
			b := m.channelBindings[i]
			marker, style := "  ", normal
			if i == m.channelIndex {
				marker, style = "› ", selected
			}
			state := "active"
			if !b.Active {
				state = "disabled"
			}
			body = append(body, line(columns(marker+string(b.ID), state, m.contentWidth()), style))
		}
		if len(m.channelBindings) == 0 {
			body = append(body, line("No destinations for this thread.", normal))
		}
	default:
		if m.channelBinding != nil {
			body = append(body, m.destinationLines(*m.channelBinding)...)
		} else {
			body = append(body, line("Destination: unchosen · b selects one", accent))
		}
		body = append(body, line("First-note snapshots · remote edits/deletions are not projected", muted))
		if m.channelBinding != nil {
			body = append(body, line(intakeLabel(*m.channelBinding), muted))
			if m.height >= 20 && m.channelBinding.LastCheck != nil {
				body = append(body, line("Last intake check: "+m.channelBinding.LastCheck.UTC().Format("2006-01-02 15:04:05Z"), muted))
			}
			if m.height >= 24 && m.channelBinding.LastSuccess != nil {
				body = append(body, line("Last intake success: "+m.channelBinding.LastSuccess.UTC().Format("2006-01-02 15:04:05Z"), muted))
			}
		}
		action := "b destination  p reply  r refresh  Esc work"
		if m.width < 52 {
			action = "b destination  p reply  r  Esc work"
		}
		footer = append(footer, m.footerText(action, accent)...)
		pages := "↑/↓ notes  [ prev  ] next  PgUp/PgDn text"
		if m.width < 52 {
			pages = "↑/↓ notes  [ prev  ] next  ? keys"
		}
		footer = append(footer, line(pages, muted))
		if m.channelLoading {
			body = append(body, line("Loading authored notes…", muted))
		} else if len(m.channelMessages) == 0 {
			body = append(body, line("No authored notes in this snapshot.", normal))
		} else {
			body = append(body, line(fmt.Sprintf("Page %d · note %d / %d · up to 100", len(m.channelBack)+1, m.channelScroll+1, len(m.channelMessages)), muted))
			note := m.channelMessages[m.channelScroll]
			body = append(body, line(note.AuthorID+" / "+string(note.BindingID), accent))
			if d, ok := m.channelReceipts[note.ID]; ok {
				body = append(body, line(receiptStatus(d), warning))
			} else if note.RemoteID != "" || note.Historical || note.AuthorID != "local-operator" {
				body = append(body, line("Received authored note · inert text", muted))
			} else {
				body = append(body, line("Local authored note · d checks its receipt", muted))
			}
			var text []string
			for _, row := range strings.Split(note.Text, "\n") {
				text = append(text, strings.Split(ansi.Wrap(plain(row), m.contentWidth(), ""), "\n")...)
			}
			slots := max(1, m.height-len(body)-len(footer))
			start := min(m.channelNoteOffset, max(0, len(text)-slots))
			for _, row := range text[start:min(len(text), start+slots)] {
				body = append(body, line(row, normal))
			}
		}
	}
	return m.render(body, footer)
}
