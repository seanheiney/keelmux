package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/seanheiney/keelmux/internal/library"
)

type libraryMsg struct {
	epoch   uint64
	catalog library.Catalog
	err     error
	changed bool
}

func (m *model) openLibrary() tea.Cmd {
	m.screen = "library"
	m.libraryEpoch++
	m.libraryLoading = true
	m.libraryError = ""
	m.libraryIndex = 0
	m.libraryComponents = false
	m.libraryComponentIndex = 0
	return m.libraryRead(false)
}

func (m *model) libraryRead(changed bool) tea.Cmd {
	epoch, state := m.libraryEpoch, filepath.Dir(m.statePath)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
		defer cancel()
		r, err := library.Open(state)
		if err != nil {
			return libraryMsg{epoch: epoch, err: err}
		}
		c, err := r.Read(ctx)
		return libraryMsg{epoch: epoch, catalog: c, err: err, changed: changed}
	}
}

func (m *model) acceptLibrary(msg libraryMsg) {
	if m.screen != "library" || msg.epoch != m.libraryEpoch {
		return
	}
	m.libraryLoading = false
	m.libraryError = ""
	if msg.err != nil {
		m.libraryError = msg.err.Error()
		m.libraryCatalog = nil
		return
	}
	m.libraryCatalog = &msg.catalog
	m.libraryIndex = max(0, min(m.libraryIndex, len(msg.catalog.Packages)-1))
	if msg.changed {
		m.libraryError = "Saved for next explicit apply/launch. Running sessions need reload; loaded state unknown."
	}
	if m.libraryComponents && len(msg.catalog.Packages) > 0 {
		m.libraryComponentIndex = max(0, min(m.libraryComponentIndex, len(libraryComponentKeys(msg.catalog.Packages[m.libraryIndex]))-1))
	}
}

func libraryComponentKeys(p library.Package) []string {
	keys := []string{}
	for _, s := range p.Manifest.Skills {
		keys = append(keys, "skill/"+s.ID)
	}
	for _, r := range p.Manifest.Rules {
		keys = append(keys, "rule/"+r.ID)
	}
	for _, t := range p.Manifest.Tools {
		keys = append(keys, "tool/"+t.ID)
	}
	return keys
}

func defaultLibrarySelection(c *library.Catalog, id string) library.Selection {
	if c != nil {
		for _, p := range c.Profiles {
			if p.ID == "default" {
				return p.Packages[id]
			}
		}
	}
	return library.Selection{}
}

func defaultLibraryRevision(c *library.Catalog) uint64 {
	if c != nil {
		for _, p := range c.Profiles {
			if p.ID == "default" {
				return p.Revision
			}
		}
	}
	return 0
}

func (m *model) libraryKey(key tea.KeyMsg) tea.Cmd {
	if key.String() == "esc" && m.libraryComponents {
		m.libraryComponents = false
		return nil
	}
	if key.String() == "esc" || key.String() == "l" || key.String() == "q" {
		m.screen = ""
		m.libraryEpoch++
		return nil
	}
	if m.libraryLoading {
		return nil
	}
	if key.String() == "tab" && m.libraryCatalog != nil && len(m.libraryCatalog.Packages) > 0 {
		m.libraryComponents = !m.libraryComponents
		m.libraryComponentIndex = 0
		return nil
	}
	if m.libraryComponents {
		return m.libraryComponentKey(key)
	}
	switch key.String() {
	case "down", "j":
		if m.libraryCatalog != nil {
			m.libraryIndex = max(0, min(len(m.libraryCatalog.Packages)-1, m.libraryIndex+1))
		}
	case "up", "k":
		m.libraryIndex = max(0, m.libraryIndex-1)
	case "r":
		m.libraryLoading = true
		return m.libraryRead(false)
	case "enter", " ":
		if m.libraryCatalog == nil || len(m.libraryCatalog.Packages) == 0 {
			return nil
		}
		p := m.libraryCatalog.Packages[m.libraryIndex]
		selection := defaultLibrarySelection(m.libraryCatalog, p.Manifest.ID)
		enabled := !(selection.Enabled && selection.Digest == p.Digest)
		epoch, state := m.libraryEpoch, filepath.Dir(m.statePath)
		revision := defaultLibraryRevision(m.libraryCatalog)
		m.libraryLoading = true
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
			defer cancel()
			r, err := library.Open(state)
			if err != nil {
				return libraryMsg{epoch: epoch, err: err}
			}
			guarded, ok := r.(library.GuardedSelections)
			if !ok {
				return libraryMsg{epoch: epoch, err: library.ErrInvalid}
			}
			if _, err = guarded.SelectPackageAt(ctx, "default", p.Manifest.ID, p.Digest, enabled, revision); err != nil {
				return libraryMsg{epoch: epoch, err: err}
			}
			c, err := r.Read(ctx)
			return libraryMsg{epoch: epoch, catalog: c, err: err, changed: true}
		}
	}
	return nil
}

func (m *model) libraryComponentKey(key tea.KeyMsg) tea.Cmd {
	if m.libraryCatalog == nil || len(m.libraryCatalog.Packages) == 0 {
		return nil
	}
	p := m.libraryCatalog.Packages[m.libraryIndex]
	keys := libraryComponentKeys(p)
	switch key.String() {
	case "down", "j":
		m.libraryComponentIndex = max(0, min(len(keys)-1, m.libraryComponentIndex+1))
	case "up", "k":
		m.libraryComponentIndex = max(0, m.libraryComponentIndex-1)
	case "r":
		m.libraryLoading = true
		return m.libraryRead(false)
	case "enter", " ":
		if len(keys) == 0 {
			return nil
		}
		sel := defaultLibrarySelection(m.libraryCatalog, p.Manifest.ID)
		if sel.Digest != p.Digest {
			m.libraryError = "Select this pack revision in the pack view before editing components."
			return nil
		}
		component := keys[m.libraryComponentIndex]
		on, explicit := sel.Components[component]
		if !explicit {
			on = true
		}
		epoch, state := m.libraryEpoch, filepath.Dir(m.statePath)
		revision := defaultLibraryRevision(m.libraryCatalog)
		m.libraryLoading = true
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
			defer cancel()
			r, err := library.Open(state)
			if err != nil {
				return libraryMsg{epoch: epoch, err: err}
			}
			guarded, ok := r.(library.GuardedSelections)
			if !ok {
				return libraryMsg{epoch: epoch, err: library.ErrInvalid}
			}
			if _, err = guarded.SelectComponentAt(ctx, "default", p.Manifest.ID, component, !on, revision, p.Digest); err != nil {
				return libraryMsg{epoch: epoch, err: err}
			}
			c, err := r.Read(ctx)
			return libraryMsg{epoch: epoch, catalog: c, err: err, changed: true}
		}
	}
	return nil
}

func (m *model) libraryView() string {
	body := []viewLine{line("LIBRARY", accent), line("Local default profile · reusable across native harnesses", muted)}
	if s, ok := m.selected(); ok && s.Library != nil {
		body = append(body, line(fmt.Sprintf("Selected session: %s r%d · loaded unknown", s.Library.ProfileID, s.Library.ProfileRevision), muted))
	}
	foot := m.footerText("Enter/Space toggle  Tab components  r refresh  Esc return", accent)
	foot = append(foot, m.footerText("Saved selection only · use plugins --help for component and profile controls", muted)...)
	if m.libraryError != "" {
		foot = append(m.footerText(m.libraryError, warning), foot...)
	}
	if m.libraryLoading {
		body = append(body, line("Reading private library…", muted))
		return m.render(body, foot)
	}
	if m.libraryCatalog == nil {
		body = append(body, line("Library unavailable; existing work continues.", warning))
		return m.render(body, foot)
	}
	if len(m.libraryCatalog.Packages) == 0 {
		body = append(body, line("No packs yet.", normal), line("Import with keelmux plugins import DIRECTORY.", muted))
		return m.render(body, foot)
	}
	if m.libraryComponents {
		p := m.libraryCatalog.Packages[m.libraryIndex]
		keys, s := libraryComponentKeys(p), defaultLibrarySelection(m.libraryCatalog, p.Manifest.ID)
		body = append(body, line(p.Manifest.ID+" / component preferences", accent))
		if !s.Enabled || s.Digest != p.Digest {
			body = append(body, line("Pack is off; component preferences are retained.", warning))
		}
		if len(keys) == 0 {
			body = append(body, line("This pack declares no components.", muted))
		}
		slots := max(1, m.height-len(body)-len(foot)-3)
		start := max(0, m.libraryComponentIndex-slots+1)
		for i := start; i < min(len(keys), start+slots); i++ {
			on, explicit := s.Components[keys[i]]
			if !explicit {
				on = true
			}
			state := "on"
			if !on {
				state = "off"
			}
			marker, style := "  ", normal
			if i == m.libraryComponentIndex {
				marker, style = "› ", accent
			}
			body = append(body, line(columns(marker+keys[i], state, m.contentWidth()), style))
		}
		return m.render(body, foot)
	}
	slots := max(1, m.height-len(body)-len(foot)-3)
	start := max(0, m.libraryIndex-slots+1)
	for i := start; i < min(len(m.libraryCatalog.Packages), start+slots); i++ {
		p := m.libraryCatalog.Packages[i]
		s := defaultLibrarySelection(m.libraryCatalog, p.Manifest.ID)
		state := "off"
		if s.Enabled && s.Digest == p.Digest {
			state = "on"
		}
		marker, style := "  ", normal
		if i == m.libraryIndex {
			marker = "› "
			style = accent
		}
		body = append(body, line(columns(marker+p.Manifest.ID+" "+p.Manifest.Version, state, m.contentWidth()), style))
	}
	p := m.libraryCatalog.Packages[m.libraryIndex]
	body = append(body, line(fmt.Sprintf("%d skills · %d rules · %d tools · pin %s", len(p.Manifest.Skills), len(p.Manifest.Rules), len(p.Manifest.Tools), p.Digest[:12]), muted))
	return m.render(body, foot)
}
