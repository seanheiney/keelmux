package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/seanheiney/keelmux/internal/config"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/library/adoption"
)

// This panel is an explicit observation of one selected execution. Inventory
// refreshes may invalidate it, but never initiate local application reads.
type libraryAdoptionPanel struct {
	target          domain.Session
	profile         string
	profileRevision uint64
	libraryEpoch    uint64
	local           bool
	report          *adoption.Report
	loading         bool
	err             error
	scroll          int
}

type libraryAdoptionMsg struct {
	epoch           uint64
	libraryEpoch    uint64
	profile         string
	profileRevision uint64
	target          domain.Session
	report          adoption.Report
	err             error
}

func libraryAdoptionTarget(s domain.Session) domain.Session {
	if s.Library != nil {
		copy := *s.Library
		s.Library = &copy
	}
	return s
}

func libraryAdoptionIdentity(s domain.Session) adoption.Report {
	return adoption.Report{
		HostID: s.HostID, SessionID: s.ID, ExecutionID: s.Execution.ID,
		Generation: s.Execution.Generation, Provider: s.Execution.Provider,
		Workspace: s.Workspace, ExecutionState: s.Execution.State,
		Launch: libraryAdoptionTarget(s).Library,
	}
}

func libraryAdoptionSame(a, b domain.Session) bool {
	return a.Mode == b.Mode && a.Linked == b.Linked &&
		a.Capabilities.ObserverOnly == b.Capabilities.ObserverOnly &&
		adoption.MatchesSession(libraryAdoptionIdentity(a), b)
}

func (m *model) openLibraryAdoption() tea.Cmd {
	if _, ok := m.selected(); !ok {
		m.libraryError = "Choose a session in the switcher to inspect its capability status."
		return nil
	}
	m.screen = "library-adoption"
	m.libraryAdoptionPanel = &libraryAdoptionPanel{}
	return m.readLibraryAdoption()
}

func (m *model) readLibraryAdoption() tea.Cmd {
	if m.screen != "library-adoption" || m.libraryAdoptionPanel == nil {
		return nil
	}
	m.libraryAdoptionEpoch++
	p := m.libraryAdoptionPanel
	p.report, p.err, p.loading, p.scroll = nil, nil, false, 0
	if !m.loaded || m.offline != nil {
		return nil
	}
	s, ok := m.selected()
	if !ok {
		return nil
	}
	p.target = libraryAdoptionTarget(s)
	p.profile, p.libraryEpoch = m.editedLibraryProfile(), m.libraryEpoch
	p.profileRevision = libraryRevision(m.libraryCatalog, p.profile)
	p.local = m.libraryAdoptionLocal
	p.loading = true
	epoch, libraryEpoch, profile := m.libraryAdoptionEpoch, p.libraryEpoch, p.profile
	profileRevision, local := p.profileRevision, p.local
	target, state, parent := libraryAdoptionTarget(p.target), filepath.Dir(m.statePath), m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, 5*time.Second)
		defer cancel()
		if err := ctx.Err(); err != nil {
			return libraryAdoptionMsg{epoch: epoch, libraryEpoch: libraryEpoch, profile: profile, profileRevision: profileRevision, target: target, err: err}
		}
		// ReadHostID observes existing local identity only. An unreadable identity
		// stays empty, causing Inspect to return unavailable before workspace reads.
		var localHost domain.ID
		if local {
			id, err := config.ReadHostID(state)
			if err == nil {
				localHost = id
			}
		}
		report, err := adoption.Inspect(ctx, adoption.Request{StateDir: state, LocalHost: localHost, Session: target})
		return libraryAdoptionMsg{epoch: epoch, libraryEpoch: libraryEpoch, profile: profile, profileRevision: profileRevision, target: target, report: report, err: err}
	}
}

func (m *model) libraryAdoptionCurrent() bool {
	p := m.libraryAdoptionPanel
	if !m.loaded || m.offline != nil || m.screen != "library-adoption" || p == nil || p.profile != m.editedLibraryProfile() || p.libraryEpoch != m.libraryEpoch ||
		p.profileRevision != libraryRevision(m.libraryCatalog, p.profile) || p.local != m.libraryAdoptionLocal {
		return false
	}
	s, ok := m.selected()
	return ok && libraryAdoptionSame(p.target, s)
}

func (m *model) acceptLibraryAdoption(msg libraryAdoptionMsg) {
	p := m.libraryAdoptionPanel
	if !m.libraryAdoptionCurrent() || msg.epoch != m.libraryAdoptionEpoch ||
		msg.libraryEpoch != p.libraryEpoch || msg.profile != p.profile || msg.profileRevision != p.profileRevision ||
		!libraryAdoptionSame(msg.target, p.target) {
		return
	}
	if msg.err == nil && !adoption.MatchesSession(msg.report, p.target) {
		return
	}
	p.loading, p.err = false, msg.err
	p.report = nil
	if msg.err == nil {
		report := msg.report
		if report.Launch != nil {
			copy := *report.Launch
			report.Launch = &copy
		}
		if report.Saved != nil {
			copy := *report.Saved
			report.Saved = &copy
		}
		if report.Applied != nil {
			copy := *report.Applied
			report.Applied = &copy
		}
		p.report = &report
	}
}

func (m *model) libraryAdoptionKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "q", "v":
		m.screen = "library"
		m.libraryAdoptionPanel = nil
		m.libraryAdoptionEpoch++
	case "r":
		return m.readLibraryAdoption()
	case "j", "down":
		if m.libraryAdoptionPanel != nil {
			m.libraryAdoptionPanel.scroll++
		}
	case "k", "up":
		if m.libraryAdoptionPanel != nil {
			m.libraryAdoptionPanel.scroll = max(0, m.libraryAdoptionPanel.scroll-1)
		}
	}
	return nil
}

func libraryAdoptionComparison(value string) string {
	switch value {
	case "match", "different":
		return value
	default:
		return "unknown"
	}
}

func libraryAdoptionDigest(value string) string {
	if len(value) != 64 {
		return "unknown"
	}
	return value[:12] + "…"
}

func libraryAdoptionGuidance(action string) string {
	switch action {
	case "inspect_local_session":
		return "Inspect this session at its verified local execution home."
	case "inspect_launch":
		return "Inspect the recorded launch profile and application identity."
	case "inspect_registry":
		return "Inspect the recorded profile in the local library."
	case "inspect_application":
		return "Inspect the owned application with plugins status and preview."
	case "recover_application":
		return "Inspect the pending application before explicit recovery."
	case "refresh":
		return "Press r for a fresh observation."
	case "preview_apply":
		return "Review plugins preview before explicitly applying saved changes."
	case "new_session_required":
		return "Start a new session after reviewing the applied profile; native reload is unverified."
	case "none":
		return "No declaration change indicated. Native loaded state remains unknown."
	default:
		return "Inspect capability status before taking further action."
	}
}

func (m *model) libraryAdoptionView() string {
	p := m.libraryAdoptionPanel
	header := []viewLine{line("SESSION CAPABILITIES", accent)}
	rows := []viewLine{}
	add := func(text string, style tone) { rows = append(rows, m.footerText(text, style)...) }
	// Neither a file receipt nor matching digests establishes these native facts.
	add("Native loaded: unknown · Permissions: unknown", warning)
	switch {
	case !m.loaded || m.offline != nil:
		add("Session inventory unavailable. Reconnect before reading capability status.", warning)
	case p == nil || !m.libraryAdoptionCurrent():
		add("Selection changed or unavailable. Press r to inspect the current selected session.", warning)
	case p.loading:
		add("Reading this session's saved and applied declarations…", muted)
	case p.err != nil:
		add("Capability evidence unavailable. Press r to retry this read.", warning)
	case p.report == nil:
		add("No capability observation. Press r to inspect this session.", muted)
	default:
		r := p.report
		add("Session: "+p.target.Name, accent)
		add(fmt.Sprintf("Execution: %s · generation %d", r.ExecutionID, r.Generation), muted)
		if r.Launch == nil {
			add("Launch declared: no recorded profile/application", muted)
		} else {
			add(fmt.Sprintf("Launch declared: %s r%d", r.Launch.ProfileID, r.Launch.ProfileRevision), normal)
			add("  resolution "+libraryAdoptionDigest(r.Launch.ResolutionDigest), muted)
			if r.Launch.ApplicationID == "" {
				add("  application not recorded", muted)
			} else {
				add("  application "+r.Launch.ApplicationID+" · "+libraryAdoptionDigest(r.Launch.ApplicationDigest), muted)
			}
		}
		if r.Saved == nil {
			add("Saved now: unavailable", muted)
		} else {
			add(fmt.Sprintf("Saved now: %s r%d · %s", r.Saved.ProfileID, r.Saved.ProfileRevision, libraryAdoptionDigest(r.Saved.ResolutionDigest)), normal)
		}
		if r.Applied == nil {
			add("Applied files: unavailable", muted)
		} else {
			state := "unverified"
			if r.Reason == "application_not_applied" {
				state = "not applied; receipt retained"
			} else if !r.Applied.Present {
				state = "absent"
			} else if r.Applied.Verified {
				state = "verified owned files"
			}
			add("Applied files: "+r.Applied.ID+" · "+state, normal)
			add("  receipt "+libraryAdoptionDigest(r.Applied.Digest), muted)
		}
		add("Saved / launch: "+libraryAdoptionComparison(r.SavedToLaunch), normal)
		add("Saved / applied: "+libraryAdoptionComparison(r.SavedToApplied), normal)
		add("Applied / launch: "+libraryAdoptionComparison(r.AppliedToLaunch), normal)
		switch r.State {
		case "aligned":
			add("Declarations agree; native adoption remains unknown.", normal)
		case "pending":
			add("Declaration change pending; inspect the next action.", warning)
		case "blocked":
			add("Inspection blocked; resolve the recorded condition.", warning)
		default:
			add("Local capability inspection unavailable.", warning)
		}
		if r.Reason != "" {
			add("Reason: "+r.Reason, muted)
		}
		if r.NextAction != "" {
			add("Next: "+libraryAdoptionGuidance(r.NextAction), accent)
		}
		add("Read-only snapshot. Native reload or a new session remains a separate action.", muted)
	}
	footer := m.footerText("↑↓ scroll  r reread  Esc library", accent)
	if m.height > 1 && len(footer) >= m.height {
		footer = m.footerText("↑↓ r Esc", accent)
		footer = footer[:min(len(footer), m.height-1)]
	}
	available := max(0, m.height-len(footer))
	header = header[:min(len(header), max(0, available-1))]
	room := max(0, available-len(header))
	start := 0
	if p != nil {
		p.scroll = max(0, min(p.scroll, max(0, len(rows)-room)))
		start = p.scroll
	}
	return m.render(append(header, rows[start:min(len(rows), start+room)]...), footer)
}
