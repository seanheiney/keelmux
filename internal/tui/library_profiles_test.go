package tui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/library"
)

type libraryProfileFixture struct {
	m        *model
	registry library.Registry
	pack     library.Package
	source   string
	marker   string
}

func newLibraryProfileFixture(t *testing.T) libraryProfileFixture {
	t.Helper()
	m := usageTestModel(t)
	state, err := filepath.EvalSymlinks(filepath.Dir(m.statePath))
	if err != nil {
		t.Fatal(err)
	}
	m.statePath = filepath.Join(state, "place.json")
	source, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(source, 0700); err != nil {
		t.Fatal(err)
	}
	bin, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(bin, "invoked")
	command := filepath.Join(bin, "must-not-run")
	quotedMarker := "'" + strings.ReplaceAll(marker, "'", "'\\''") + "'"
	if err = os.WriteFile(command, []byte("#!/bin/sh\n: > "+quotedMarker+"\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"skills/review/SKILL.md": []byte("---\nname: review\ndescription: Review the current change.\n---\nReport evidence.\n"),
		"rules/review.md":        []byte("Keep claims bound to evidence.\n"),
	}
	manifest := library.Manifest{Schema: 1, ID: "fixture", Version: "1", Files: map[string]string{},
		Skills: []library.Asset{{ID: "review", Path: "skills/review"}},
		Rules:  []library.Asset{{ID: "review", Path: "rules/review.md"}},
		Tools:  []library.Tool{{ID: "mcp", Transport: "stdio", Command: []string{command}}},
	}
	for path, data := range files {
		if err = os.MkdirAll(filepath.Dir(filepath.Join(source, path)), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(source, path), data, 0600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(data)
		manifest.Files[path] = hex.EncodeToString(digest[:])
	}
	data, _ := json.Marshal(manifest)
	if err = os.WriteFile(filepath.Join(source, library.ManifestFilename), data, 0600); err != nil {
		t.Fatal(err)
	}
	reg, err := library.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	pack, err := reg.Import(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reg.CreateProfile(context.Background(), "review", "Code review"); err != nil {
		t.Fatal(err)
	}
	for _, profile := range []string{"default", "review"} {
		if _, err = reg.SelectPackage(context.Background(), profile, pack.Manifest.ID, pack.Digest, true); err != nil {
			t.Fatal(err)
		}
	}
	m.acceptLibrary(m.openLibrary()().(libraryMsg))
	t.Cleanup(func() {
		if _, err := os.Lstat(marker); !os.IsNotExist(err) {
			t.Error("library controls started the declared executable", err)
		}
	})
	return libraryProfileFixture{m, reg, pack, source, marker}
}

func chooseLibraryProfile(t *testing.T, m *model, id string) {
	t.Helper()
	if cmd := m.libraryKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}}); cmd != nil || !m.libraryProfiles {
		t.Fatal("profile picker did not open")
	}
	for tries := 0; m.libraryProfileChoice != id && tries < 64; tries++ {
		key := tea.KeyDown
		if id < m.libraryProfileChoice {
			key = tea.KeyUp
		}
		m.libraryKey(tea.KeyMsg{Type: key})
	}
	if m.libraryProfileChoice != id {
		t.Fatal("profile unavailable", id)
	}
	cmd := m.libraryKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || m.libraryProfiles || m.editedLibraryProfile() != id {
		t.Fatal("profile selection was not explicit", id)
	}
	m.acceptLibrary(cmd().(libraryMsg))
	if m.libraryError != "" || m.libraryLoading {
		t.Fatal("profile read failed", m.libraryError)
	}
}

func catalogProfile(t *testing.T, reg library.Registry, id string) library.Profile {
	t.Helper()
	c, err := reg.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p, ok := libraryProfile(&c, id)
	if !ok {
		t.Fatal("missing profile", id)
	}
	return p
}

func runLibraryKey(t *testing.T, m *model, key tea.KeyMsg) libraryMsg {
	t.Helper()
	cmd := m.libraryKey(key)
	if cmd == nil {
		t.Fatal("expected registry command", key.String())
	}
	msg := cmd().(libraryMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	m.acceptLibrary(msg)
	return msg
}

func TestLibraryProfilePickerEditsIndependentSkillRuleToolPreferences(t *testing.T) {
	f := newLibraryProfileFixture(t)
	m := f.m
	before := catalogProfile(t, f.registry, "default")
	// A session's declaration must not silently change the edited local profile.
	m.sessions[0].Library = &domain.LibrarySnapshot{ProfileID: "review", ProfileRevision: 2, Loaded: "unknown"}
	if m.editedLibraryProfile() != "default" || !strings.Contains(ansi.Strip(m.View()), "Editing profile: default") {
		t.Fatal("selected session retargeted profile", m.View())
	}
	chooseLibraryProfile(t, m, "review")
	if !strings.Contains(ansi.Strip(m.View()), "Editing profile: review r2") {
		t.Fatal("edited revision not visible", m.View())
	}
	m.libraryKey(tea.KeyMsg{Type: tea.KeyTab})
	for _, key := range []string{"skill/review", "rule/review", "tool/mcp"} {
		runLibraryKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
		if librarySelection(m.libraryCatalog, "review", "fixture").Components[key] {
			t.Fatal("component preference stayed on", key)
		}
		m.libraryKey(tea.KeyMsg{Type: tea.KeyDown})
	}
	r, err := f.registry.Resolve(context.Background(), "review")
	if err != nil || len(r.Skills)+len(r.Rules)+len(r.Tools) != 0 || r.Loaded != "unknown" {
		t.Fatal("disabled selection resolved", r, err)
	}
	if after := catalogProfile(t, f.registry, "default"); !reflect.DeepEqual(before, after) {
		t.Fatal("nondefault edits changed default", before, after)
	}
	m.libraryKey(tea.KeyMsg{Type: tea.KeyEsc}) // Component view -> packs.
	runLibraryKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	runLibraryKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	sel := librarySelection(m.libraryCatalog, "review", "fixture")
	if !sel.Enabled || sel.Digest != f.pack.Digest || len(sel.Components) != 3 {
		t.Fatal("pack toggles lost pin/preferences", sel)
	}
}

func TestLibraryNondefaultMutationsRefuseConcurrentRepin(t *testing.T) {
	for _, component := range []bool{false, true} {
		t.Run(map[bool]string{false: "pack", true: "component"}[component], func(t *testing.T) {
			f := newLibraryProfileFixture(t)
			m := f.m
			chooseLibraryProfile(t, m, "review")
			if component {
				m.libraryKey(tea.KeyMsg{Type: tea.KeyTab})
			}
			queued := m.libraryKey(tea.KeyMsg{Type: tea.KeyEnter})
			if queued == nil {
				t.Fatal("mutation was not queued")
			}
			manifest := f.pack.Manifest
			manifest.Version = "2"
			data, _ := json.Marshal(manifest)
			if err := os.WriteFile(filepath.Join(f.source, library.ManifestFilename), data, 0600); err != nil {
				t.Fatal(err)
			}
			updated, err := f.registry.Import(context.Background(), f.source)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.registry.SelectPackage(context.Background(), "review", "fixture", updated.Digest, true); err != nil {
				t.Fatal(err)
			}
			before, _ := f.registry.Read(context.Background())
			msg := queued().(libraryMsg)
			if !errors.Is(msg.err, library.ErrStale) || msg.profile != "review" {
				t.Fatal("stale mutation admitted", msg)
			}
			after, _ := f.registry.Read(context.Background())
			if !reflect.DeepEqual(before, after) {
				t.Fatal("stale mutation changed current profiles")
			}
			m.acceptLibrary(msg)
			if m.libraryCatalog != nil || m.libraryLoading || m.libraryKey(tea.KeyMsg{Type: tea.KeyEnter}) != nil {
				t.Fatal("failed command left actionable stale catalog")
			}
		})
	}
}

func TestLibraryProfileRepliesAndQueuedMutationStayBoundToOriginalProfile(t *testing.T) {
	f := newLibraryProfileFixture(t)
	m := f.m
	oldRead := m.libraryRead(false)
	chooseLibraryProfile(t, m, "review")
	currentCatalog := m.libraryCatalog
	m.acceptLibrary(oldRead().(libraryMsg))
	if m.editedLibraryProfile() != "review" || m.libraryCatalog != currentCatalog {
		t.Fatal("old profile reply replaced current profile")
	}
	wrongProfile := libraryMsg{epoch: m.libraryEpoch, profile: "default", err: errors.New("wrong profile")}
	m.acceptLibrary(wrongProfile)
	if m.libraryCatalog != currentCatalog || m.libraryError != "" {
		t.Fatal("same-epoch foreign profile reply was admitted")
	}
	beforeDefault := catalogProfile(t, f.registry, "default")
	queued := m.libraryKey(tea.KeyMsg{Type: tea.KeyEnter}) // Explicit review off.
	if queued == nil {
		t.Fatal("no review command")
	}
	m.libraryKey(tea.KeyMsg{Type: tea.KeyEsc})
	m.acceptLibrary(m.openLibrary()().(libraryMsg)) // New panel defaults explicitly.
	visibleDefault := m.libraryCatalog
	msg := queued().(libraryMsg)
	if msg.err != nil || msg.profile != "review" {
		t.Fatal(msg)
	}
	m.acceptLibrary(msg)
	if m.editedLibraryProfile() != "default" || m.libraryCatalog != visibleDefault {
		t.Fatal("old mutation reply retargeted the panel")
	}
	if after := catalogProfile(t, f.registry, "default"); !reflect.DeepEqual(beforeDefault, after) {
		t.Fatal("captured review operation changed default")
	}
	if catalogProfile(t, f.registry, "review").Packages["fixture"].Enabled {
		t.Fatal("original explicit review operation was retargeted or dropped")
	}
}

func TestLibraryComponentStatusSeparatesSavedChoicePackGateAndUnselectedPin(t *testing.T) {
	f := newLibraryProfileFixture(t)
	m := f.m
	chooseLibraryProfile(t, m, "review")
	runLibraryKey(t, m, tea.KeyMsg{Type: tea.KeyEnter}) // Pack off, components default on.
	m.libraryKey(tea.KeyMsg{Type: tea.KeyTab})
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "saved on · inactive") || !strings.Contains(view, "Pack off; saved choices inactive") {
		t.Fatal("package gate concealed", view)
	}
	runLibraryKey(t, m, tea.KeyMsg{Type: tea.KeyEnter}) // Save skill off while pack remains off.
	sel := librarySelection(m.libraryCatalog, "review", "fixture")
	if sel.Enabled || sel.Digest != f.pack.Digest || sel.Components["skill/review"] {
		t.Fatal("component change enabled package or changed pin", sel)
	}
	r, err := f.registry.Resolve(context.Background(), "review")
	if err != nil || len(r.Skills)+len(r.Rules)+len(r.Tools) != 0 {
		t.Fatal("disabled pack had effective components", r, err)
	}
	manifest := f.pack.Manifest
	manifest.Version = "2"
	data, _ := json.Marshal(manifest)
	if err = os.WriteFile(filepath.Join(f.source, library.ManifestFilename), data, 0600); err != nil {
		t.Fatal(err)
	}
	other, err := f.registry.Import(context.Background(), f.source)
	if err != nil {
		t.Fatal(err)
	}
	runLibraryKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	for i, p := range m.libraryCatalog.Packages {
		if p.Digest == other.Digest {
			m.libraryIndex = i
		}
	}
	view = ansi.Strip(m.View())
	if !strings.Contains(view, "Revision not selected") || strings.Contains(view, "saved off") || strings.Contains(view, "saved on") {
		t.Fatal("unselected revision borrowed selected pin preferences", view)
	}
	before, _ := f.registry.Read(context.Background())
	if m.libraryKey(tea.KeyMsg{Type: tea.KeyEnter}) != nil {
		t.Fatal("unselected revision allowed component mutation")
	}
	after, _ := f.registry.Read(context.Background())
	if !reflect.DeepEqual(before, after) {
		t.Fatal("unselected revision changed saved state")
	}
}

func TestLibraryProfilePickerReadsOnlyAndKeepsChoiceByID(t *testing.T) {
	f := newLibraryProfileFixture(t)
	m := f.m
	before, err := f.registry.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m.libraryKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m.libraryKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if m.libraryProfileChoice != "review" {
		t.Fatal("browse did not choose review")
	}
	// Reordered profile data must not change the user's chosen stable ID.
	c := *m.libraryCatalog
	c.Profiles = append([]library.Profile(nil), c.Profiles...)
	for i, j := 0, len(c.Profiles)-1; i < j; i, j = i+1, j-1 {
		c.Profiles[i], c.Profiles[j] = c.Profiles[j], c.Profiles[i]
	}
	m.acceptLibrary(libraryMsg{epoch: m.libraryEpoch, profile: "default", catalog: c})
	if m.libraryProfileChoice != "review" {
		t.Fatal("refresh retargeted picker")
	}
	cmd := m.libraryKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || m.editedLibraryProfile() != "review" {
		t.Fatal("stable profile ID was not selected")
	}
	m.acceptLibrary(cmd().(libraryMsg))
	chooseLibraryProfile(t, m, "default")
	m.libraryKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m.libraryKey(tea.KeyMsg{Type: tea.KeyDown})
	m.libraryKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != "library" || m.libraryProfiles || m.editedLibraryProfile() != "default" {
		t.Fatal("picker escape changed profile or closed library")
	}
	after, err := f.registry.Read(context.Background())
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("browsing changed registry", err)
	}
	entries, err := os.ReadDir(filepath.Dir(m.statePath))
	if err != nil || len(entries) != 1 || entries[0].Name() != "library" {
		t.Fatal("browse created native/client/runtime state", entries, err)
	}
}

func TestLibraryProfilePickerEmptyReadAndUnavailableProfileAreNotActionable(t *testing.T) {
	m := usageTestModel(t)
	dir, err := filepath.EvalSymlinks(filepath.Dir(m.statePath))
	if err != nil {
		t.Fatal(err)
	}
	m.statePath = filepath.Join(dir, "place.json")
	before := m.place()
	m.acceptLibrary(m.openLibrary()().(libraryMsg))
	chooseLibraryProfile(t, m, "default")
	if entries, err := os.ReadDir(filepath.Dir(m.statePath)); err != nil || len(entries) != 0 {
		t.Fatal("empty picker created state", entries, err)
	}
	if !reflect.DeepEqual(before, m.place()) {
		t.Fatal("picker changed session navigation")
	}
	m.acceptLibrary(libraryMsg{epoch: m.libraryEpoch, profile: "default", catalog: library.Catalog{}})
	if m.libraryLoading || m.libraryCatalog != nil || m.libraryKey(tea.KeyMsg{Type: tea.KeyEnter}) != nil {
		t.Fatal("missing profile retained actionable catalog")
	}
}

func TestLibraryProfilePickerAndComponentViewsBoundedAtTerminalSizes(t *testing.T) {
	f := newLibraryProfileFixture(t)
	m := f.m
	for _, id := range []string{"a-very-long-profile-name-for-terminal-clipping", "z-last"} {
		if _, err := f.registry.CreateProfile(context.Background(), id, "Long description 界界"); err != nil {
			t.Fatal(err)
		}
	}
	runLibraryKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	for _, picker := range []bool{true, false} {
		m.libraryProfiles, m.libraryComponents = picker, !picker
		m.libraryProfileChoice = "z-last"
		for _, size := range [][2]int{{120, 32}, {80, 24}, {40, 14}, {16, 3}} {
			m.width, m.height = size[0], size[1]
			view := ansi.Strip(m.View())
			if len(strings.Split(view, "\n")) > size[1] || strings.Contains(view, "\x1b") {
				t.Fatal("unbounded or unsafe view", size, view)
			}
			for _, row := range strings.Split(view, "\n") {
				if ansi.StringWidth(row) > size[0] {
					t.Fatal("row exceeds width", size, row)
				}
			}
			if picker && size[0] == 40 && !strings.Contains(view, "z-last") {
				t.Fatal("focused profile not visible at narrow size", view)
			}
		}
	}
}
