package tui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/seanheiney/keelmux/internal/library"
)

func TestLibraryPanelEmptyReadAndStaleMessagesHaveNoEffects(t *testing.T) {
	m := usageTestModel(t)
	dir, err := filepath.EvalSymlinks(filepath.Dir(m.statePath))
	if err != nil {
		t.Fatal(err)
	}
	m.statePath = filepath.Join(dir, "place.json")
	before := m.place()
	cmd := m.openLibrary()
	msg := cmd().(libraryMsg)
	if msg.err != nil || msg.catalog.Present {
		t.Fatal(msg)
	}
	m.acceptLibrary(msg)
	if !strings.Contains(ansi.Strip(m.View()), "No packs yet") {
		t.Fatal(m.View())
	}
	m.libraryKey(tea.KeyMsg{Type: tea.KeyDown})
	if m.libraryIndex != 0 {
		t.Fatal("negative empty index")
	}
	m.libraryKey(tea.KeyMsg{Type: tea.KeyEsc})
	m.acceptLibrary(libraryMsg{epoch: msg.epoch, catalog: library.Catalog{Present: true}})
	if m.screen != "" || !reflect.DeepEqual(m.place(), before) {
		t.Fatal("stale result changed place")
	}
	if entries, err := os.ReadDir(filepath.Dir(m.statePath)); err != nil || len(entries) != 0 {
		t.Fatal("read wrote state", entries, err)
	}
}

func TestLibraryPanelStaleComponentCannotChangeConcurrentRepin(t *testing.T) {
	m := usageTestModel(t)
	dir, err := filepath.EvalSymlinks(filepath.Dir(m.statePath))
	if err != nil {
		t.Fatal(err)
	}
	m.statePath = filepath.Join(dir, "place.json")
	source, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	os.Chmod(source, 0700)
	manifest := library.Manifest{Schema: 1, ID: "fixture", Version: "1", Files: map[string]string{}, Tools: []library.Tool{{ID: "mcp", Transport: "stdio", Command: []string{"never-run"}}}}
	write := func() {
		raw, _ := json.Marshal(manifest)
		if err := os.WriteFile(filepath.Join(source, library.ManifestFilename), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	reg, err := library.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, err := reg.Import(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reg.SelectPackage(context.Background(), "default", "fixture", a.Digest, true); err != nil {
		t.Fatal(err)
	}
	m.acceptLibrary(m.openLibrary()().(libraryMsg))
	m.libraryKey(tea.KeyMsg{Type: tea.KeyTab})
	queued := m.libraryKey(tea.KeyMsg{Type: tea.KeyEnter})
	if queued == nil {
		t.Fatal("no component command")
	}
	manifest.Version = "2"
	write()
	b, err := reg.Import(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reg.SelectPackage(context.Background(), "default", "fixture", b.Digest, true); err != nil {
		t.Fatal(err)
	}
	before, err := reg.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	msg := queued().(libraryMsg)
	if !errors.Is(msg.err, library.ErrStale) {
		t.Fatal("stale UI silently changed new pin", msg.err)
	}
	after, err := reg.Read(context.Background())
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("stale command changed current catalog", err)
	}
	m.acceptLibrary(msg)
	if m.libraryCatalog != nil {
		t.Fatal("stale catalog remained actionable")
	}
}

func TestLibraryPanelPackAndComponentTogglePreservesPinsAndNoNativeEffects(t *testing.T) {
	m := usageTestModel(t)
	dir, err := filepath.EvalSymlinks(filepath.Dir(m.statePath))
	if err != nil {
		t.Fatal(err)
	}
	m.statePath = filepath.Join(dir, "place.json")
	source, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	os.Chmod(source, 0700)
	manifest := `{"schema":1,"id":"fixture","version":"1","files":{},"tools":[{"id":"mcp","transport":"stdio","command":["never-run-fixture"]}]}`
	if err = os.WriteFile(filepath.Join(source, library.ManifestFilename), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	reg, err := library.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := reg.Import(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	m.acceptLibrary(m.openLibrary()().(libraryMsg))
	cmd := m.libraryKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("toggle unavailable")
	}
	m.acceptLibrary(cmd().(libraryMsg))
	s := defaultLibrarySelection(m.libraryCatalog, "fixture")
	if !s.Enabled || s.Digest != p.Digest {
		t.Fatal(s)
	}
	m.libraryKey(tea.KeyMsg{Type: tea.KeyTab})
	if !m.libraryComponents || !strings.Contains(ansi.Strip(m.View()), "tool/mcp") {
		t.Fatal(m.View())
	}
	cmd = m.libraryKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("component toggle unavailable")
	}
	m.acceptLibrary(cmd().(libraryMsg))
	s = defaultLibrarySelection(m.libraryCatalog, "fixture")
	if !s.Enabled || s.Digest != p.Digest || s.Components["tool/mcp"] {
		t.Fatal(s)
	}
	r, err := reg.Resolve(context.Background(), "default")
	if err != nil || len(r.Tools) != 0 || r.Loaded != "unknown" {
		t.Fatal(r, err)
	}
	for _, size := range [][2]int{{120, 32}, {80, 24}, {40, 14}, {16, 3}} {
		m.width, m.height = size[0], size[1]
		v := ansi.Strip(m.View())
		if len(strings.Split(v, "\n")) > size[1] {
			t.Fatal("exceeded terminal", size)
		}
		for _, line := range strings.Split(v, "\n") {
			if ansi.StringWidth(line) > size[0] {
				t.Fatal("wide row", size, line)
			}
		}
	}
	m.acceptLibrary(libraryMsg{epoch: m.libraryEpoch, err: errors.New("read failed")})
	if m.libraryCatalog != nil || m.libraryKey(tea.KeyMsg{Type: tea.KeyEnter}) != nil {
		t.Fatal("failed refresh permits stale mutation")
	}
}
