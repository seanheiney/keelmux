package tui

import (
	"context"
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
	"github.com/seanheiney/keelmux/internal/library/adoption"
)

func libraryAdoptionTestModel(t *testing.T) *model {
	t.Helper()
	m := usageTestModel(t)
	dir, err := filepath.EvalSymlinks(filepath.Dir(m.statePath))
	if err != nil {
		t.Fatal(err)
	}
	m.statePath = filepath.Join(dir, "place.json")
	m.sessions[0].Library = &domain.LibrarySnapshot{
		ProfileID: "review", ProfileRevision: 7, ResolutionDigest: strings.Repeat("a", 64),
		ApplicationID: "review-codex", ApplicationDigest: strings.Repeat("b", 64),
		Source: "client-declaration", Loaded: "unknown",
	}
	m.screen, m.libraryProfileID, m.libraryEpoch = "library", "default", 3
	m.libraryAdoptionLocal = true
	m.libraryCatalog = &library.Catalog{Profiles: []library.Profile{{ID: "default", Revision: 1}}}
	return m
}

func libraryAdoptionTestReply(m *model) libraryAdoptionMsg {
	p := m.libraryAdoptionPanel
	r := libraryAdoptionIdentity(p.target)
	r.Saved = &adoption.Selection{ProfileID: "review", ProfileRevision: 7, ResolutionDigest: strings.Repeat("a", 64)}
	r.Applied = &adoption.Application{ID: "review-codex", Digest: strings.Repeat("b", 64), Present: true, Verified: true, Coverage: "complete_for_declared_resolution"}
	r.SavedToLaunch, r.SavedToApplied, r.AppliedToLaunch = "match", "match", "match"
	r.State, r.Reason, r.NextAction = "aligned", "aligned", "none"
	r.NativeLoaded, r.Permissions = "unknown", "unknown"
	return libraryAdoptionMsg{epoch: m.libraryAdoptionEpoch, libraryEpoch: p.libraryEpoch, profile: p.profile, profileRevision: p.profileRevision, target: libraryAdoptionTarget(p.target), report: r}
}

func TestLibraryAdoptionLazyMissingIdentityReadPreservesStateAndNavigation(t *testing.T) {
	m := libraryAdoptionTestModel(t)
	before := m.place()
	snapshot := *m.sessions[0].Library
	cmd := m.openLibraryAdoption()
	if cmd == nil || m.screen != "library-adoption" || !m.libraryAdoptionPanel.loading {
		t.Fatal("explicit session inspection was not opened")
	}
	if entries, err := os.ReadDir(filepath.Dir(m.statePath)); err != nil || len(entries) != 0 {
		t.Fatal("open created state before its read", entries, err)
	}
	msg := cmd().(libraryAdoptionMsg)
	if msg.err != nil || msg.report.State != "unavailable" || msg.report.NativeLoaded != "unknown" || msg.report.Permissions != "unknown" || msg.report.Saved != nil || msg.report.Applied != nil {
		t.Fatal("unproven local home became application evidence", msg)
	}
	m.acceptLibraryAdoption(msg)
	if !reflect.DeepEqual(m.place(), before) || *m.sessions[0].Library != snapshot {
		t.Fatal("inspection changed navigation or launch history")
	}
	if entries, err := os.ReadDir(filepath.Dir(m.statePath)); err != nil || len(entries) != 0 {
		t.Fatal("missing identity read created state", entries, err)
	}
	m.libraryAdoptionKey(tea.KeyMsg{Type: tea.KeyEsc})
	m.acceptLibraryAdoption(msg)
	if m.screen != "library" || m.libraryAdoptionPanel != nil || !reflect.DeepEqual(m.place(), before) {
		t.Fatal("late inspection reopened panel or changed navigation")
	}
}

func TestLibraryAdoptionUsesExistingHostIdentityInsteadOfSelectedHost(t *testing.T) {
	m := libraryAdoptionTestModel(t)
	local := domain.NewID("host")
	hostPath := filepath.Join(filepath.Dir(m.statePath), "host-id")
	if err := os.WriteFile(hostPath, []byte(local), 0600); err != nil {
		t.Fatal(err)
	}
	m.sessions[0].Workspace = filepath.Join(filepath.Dir(m.statePath), "remote-workspace-must-remain-absent")
	msg := m.openLibraryAdoption()().(libraryAdoptionMsg)
	if msg.err != nil || msg.report.State != "unavailable" || msg.report.Reason != "remote_session" || msg.report.Saved != nil || msg.report.Applied != nil {
		t.Fatal("selected remote host was treated as local", msg)
	}
	after, err := os.ReadFile(hostPath)
	if err != nil || string(after) != string(local) {
		t.Fatal("existing host identity changed", err)
	}
	entries, err := os.ReadDir(filepath.Dir(m.statePath))
	if err != nil || len(entries) != 1 || entries[0].Name() != "host-id" {
		t.Fatal("remote inspection created local state", entries, err)
	}
}

func TestLibraryAdoptionDropsRepliesAfterEveryRelevantRetarget(t *testing.T) {
	cases := []struct {
		name     string
		retarget func(*model)
	}{
		{"screen", func(m *model) { m.screen = "library" }},
		{"unloaded inventory", func(m *model) { m.loaded = false }},
		{"offline inventory", func(m *model) { m.offline = errors.New("owned offline fixture") }},
		{"request epoch", func(m *model) { m.libraryAdoptionEpoch++ }},
		{"library epoch", func(m *model) { m.libraryEpoch++ }},
		{"edited profile", func(m *model) { m.libraryProfileID = "other" }},
		{"edited profile revision", func(m *model) { m.libraryCatalog.Profiles[0].Revision++ }},
		{"local route eligibility", func(m *model) { m.libraryAdoptionLocal = false }},
		{"selected session", func(m *model) { p := m.place(); p.SessionID = "other"; m.setPlace(p) }},
		{"host", func(m *model) { m.sessions[0].HostID = domain.NewID("host") }},
		{"execution", func(m *model) { m.sessions[0].Execution.ID = domain.NewID("execution") }},
		{"generation", func(m *model) { m.sessions[0].Execution.Generation++ }},
		{"provider", func(m *model) { m.sessions[0].Execution.Provider = "claude" }},
		{"workspace", func(m *model) { m.sessions[0].Workspace = "/different" }},
		{"execution state", func(m *model) { m.sessions[0].Execution.State = "stopped" }},
		{"mode", func(m *model) { m.sessions[0].Mode = domain.Observed }},
		{"linked", func(m *model) { m.sessions[0].Linked = true }},
		{"observer route", func(m *model) { m.sessions[0].Capabilities.ObserverOnly = true }},
		{"snapshot absence", func(m *model) { m.sessions[0].Library = nil }},
		{"launch profile", func(m *model) { m.sessions[0].Library.ProfileID = "other" }},
		{"launch revision", func(m *model) { m.sessions[0].Library.ProfileRevision++ }},
		{"resolution", func(m *model) { m.sessions[0].Library.ResolutionDigest = strings.Repeat("c", 64) }},
		{"application", func(m *model) { m.sessions[0].Library.ApplicationID = "other" }},
		{"receipt", func(m *model) { m.sessions[0].Library.ApplicationDigest = strings.Repeat("d", 64) }},
		{"source", func(m *model) { m.sessions[0].Library.Source = "other" }},
		{"loaded assertion", func(m *model) { m.sessions[0].Library.Loaded = "loaded" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			m := libraryAdoptionTestModel(t)
			m.openLibraryAdoption() // Do not execute the returned read.
			msg := libraryAdoptionTestReply(m)
			test.retarget(m)
			m.acceptLibraryAdoption(msg)
			if m.libraryAdoptionPanel.report != nil || !m.libraryAdoptionPanel.loading {
				t.Fatal("stale observation became current")
			}
		})
	}
}

func TestLibraryAdoptionEntryRouteNeverAdoptsMatchingLocalHost(t *testing.T) {
	m := libraryAdoptionTestModel(t)
	m.libraryAdoptionLocal = false
	hostPath := filepath.Join(filepath.Dir(m.statePath), "host-id")
	if err := os.WriteFile(hostPath, []byte(m.sessions[0].HostID), 0600); err != nil {
		t.Fatal(err)
	}
	msg := m.openLibraryAdoption()().(libraryAdoptionMsg)
	if msg.err != nil || msg.report.State != "unavailable" || msg.report.Reason != "local_host_unproven" || msg.report.Saved != nil || msg.report.Applied != nil {
		t.Fatal("entry inventory claimed local application authority", msg)
	}
	entries, err := os.ReadDir(filepath.Dir(m.statePath))
	if err != nil || len(entries) != 1 || entries[0].Name() != "host-id" {
		t.Fatal("entry inspection created local state", entries, err)
	}
}

func TestLibraryAdoptionRefreshAndMismatchedReportCannotReplaceCurrentRequest(t *testing.T) {
	m := libraryAdoptionTestModel(t)
	m.openLibraryAdoption()
	old := libraryAdoptionTestReply(m)
	if cmd := m.libraryAdoptionKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}}); cmd == nil {
		t.Fatal("explicit refresh did not start a read")
	}
	m.acceptLibraryAdoption(old)
	if !m.libraryAdoptionPanel.loading || m.libraryAdoptionPanel.report != nil {
		t.Fatal("old refresh replaced pending read")
	}
	fresh := libraryAdoptionTestReply(m)
	wrong := fresh
	wrong.report.ExecutionID = domain.NewID("execution")
	m.acceptLibraryAdoption(wrong)
	if !m.libraryAdoptionPanel.loading || m.libraryAdoptionPanel.report != nil {
		t.Fatal("report for another execution was accepted")
	}
	m.acceptLibraryAdoption(fresh)
	if m.libraryAdoptionPanel.loading || m.libraryAdoptionPanel.report == nil {
		t.Fatal("matching fresh observation was refused")
	}
	fresh.report.Launch.ProfileID = "mutated-after-reply"
	fresh.report.Saved.ProfileID = "mutated-after-reply"
	fresh.report.Applied.ID = "mutated-after-reply"
	if m.libraryAdoptionPanel.report.Launch.ProfileID != "review" || m.libraryAdoptionPanel.report.Saved.ProfileID != "review" || m.libraryAdoptionPanel.report.Applied.ID != "review-codex" {
		t.Fatal("accepted report retained mutable response pointers")
	}
}

func TestLibraryAdoptionViewSeparatesEvidenceAndInvalidatesChangedInventory(t *testing.T) {
	m := libraryAdoptionTestModel(t)
	m.openLibraryAdoption()
	msg := libraryAdoptionTestReply(m)
	msg.report.NativeLoaded, msg.report.Permissions = "loaded", "granted"
	m.acceptLibraryAdoption(msg)
	m.width, m.height = 100, 32
	view := ansi.Strip(m.libraryAdoptionView())
	for _, want := range []string{"Launch declared: review r7", "Saved now: review r7", "Applied files: review-codex", "Native loaded: unknown", "Permissions: unknown", "Declarations agree; native adoption remains unknown", "No declaration change indicated"} {
		if !strings.Contains(view, want) {
			t.Fatal("capability evidence distinction absent", want, view)
		}
	}
	if strings.Contains(view, "Permissions: granted") || strings.Contains(view, "Native loaded: loaded") {
		t.Fatal("declaration observation asserted native acceptance")
	}
	m.sessions[0].Execution.Generation++
	stale := ansi.Strip(m.libraryAdoptionView())
	if strings.Contains(stale, "Declarations agree") || !strings.Contains(stale, "Selection changed") {
		t.Fatal("changed inventory left obsolete agreement visible", stale)
	}
}

func TestLibraryAdoptionShortViewportKeepsGuidanceReachable(t *testing.T) {
	m := libraryAdoptionTestModel(t)
	m.openLibraryAdoption()
	msg := libraryAdoptionTestReply(m)
	msg.report.State, msg.report.NextAction = "pending", "new_session_required"
	m.acceptLibraryAdoption(msg)
	for _, size := range [][2]int{{40, 14}, {80, 24}, {120, 32}, {16, 3}, {40, 5}} {
		m.width, m.height = size[0], size[1]
		m.libraryAdoptionPanel.scroll = 0
		seen := ""
		for i := 0; i < 160; i++ {
			view := ansi.Strip(m.libraryAdoptionView())
			seen += view + "\n"
			if len(strings.Split(view, "\n")) > size[1] {
				t.Fatal("panel overflowed height", size)
			}
			for _, row := range strings.Split(view, "\n") {
				if ansi.StringWidth(row) > size[0] {
					t.Fatal("panel overflowed width", size, row)
				}
			}
			m.libraryAdoptionKey(tea.KeyMsg{Type: tea.KeyDown})
		}
		if !strings.Contains(seen, "Next:") || !strings.Contains(seen, "snapshot") {
			t.Fatal("scrolling never reached guidance", size)
		}
	}
}

func TestLibraryAdoptionViewLabelsRetainedRemovalReceiptAsNotApplied(t *testing.T) {
	m := libraryAdoptionTestModel(t)
	m.openLibraryAdoption()
	msg := libraryAdoptionTestReply(m)
	msg.report.State, msg.report.Reason, msg.report.NextAction = "pending", "application_not_applied", "preview_apply"
	msg.report.SavedToApplied, msg.report.AppliedToLaunch = "different", "different"
	m.acceptLibraryAdoption(msg)
	m.width, m.height = 100, 32
	view := ansi.Strip(m.libraryAdoptionView())
	if !strings.Contains(view, "Applied files: review-codex · not applied; receipt retained") {
		t.Fatal("retained removal receipt was not distinguished from active application", view)
	}
	if strings.Contains(view, "verified owned files") || strings.Contains(view, "Declarations agree") {
		t.Fatal("verified removal receipt was presented as active application", view)
	}
	for _, want := range []string{"Native loaded: unknown", "Permissions: unknown", "Review plugins preview"} {
		if !strings.Contains(view, want) {
			t.Fatal("removal view lost evidence limits or next action", want, view)
		}
	}
}

func TestLibraryAdoptionCancelledReadNeverReturnsPositiveEvidence(t *testing.T) {
	m := libraryAdoptionTestModel(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.ctx = ctx
	msg := m.openLibraryAdoption()().(libraryAdoptionMsg)
	if !errors.Is(msg.err, context.Canceled) {
		t.Fatal("inspection lost cancellation", msg.err)
	}
	m.acceptLibraryAdoption(msg)
	if m.libraryAdoptionPanel.report != nil || m.libraryAdoptionPanel.err == nil {
		t.Fatal("cancelled observation became current")
	}
}

func TestLibraryAdoptionDispatchFromLibraryAndReturnIsReadOnly(t *testing.T) {
	m := libraryAdoptionTestModel(t)
	before := m.place()
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	if cmd == nil || m.screen != "library-adoption" {
		t.Fatal("library shortcut did not open session inspection")
	}
	m.Update(cmd())
	if m.libraryAdoptionPanel.report == nil || !strings.Contains(ansi.Strip(m.View()), "SESSION CAPABILITIES") {
		t.Fatal("reply/view dispatch did not reach inspection panel")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != "library" || m.libraryAdoptionPanel != nil || !reflect.DeepEqual(m.place(), before) {
		t.Fatal("return changed navigation or kept a stale panel")
	}
	if entries, err := os.ReadDir(filepath.Dir(m.statePath)); err != nil || len(entries) != 0 {
		t.Fatal("dispatch created state", entries, err)
	}
}

func TestLibraryAdoptionUnavailableInventoryCannotReadOrKeepAgreement(t *testing.T) {
	for _, name := range []string{"offline", "not loaded"} {
		t.Run(name, func(t *testing.T) {
			m := libraryAdoptionTestModel(t)
			m.openLibraryAdoption()
			m.acceptLibraryAdoption(libraryAdoptionTestReply(m))
			if m.libraryAdoptionPanel.report == nil {
				t.Fatal("fixture agreement not accepted")
			}
			if name == "offline" {
				m.Update(inventoryMsg{err: errors.New("owned offline fixture")})
			} else {
				m.loaded = false
			}
			m.width, m.height = 100, 32
			view := ansi.Strip(m.View())
			if strings.Contains(view, "Declarations agree") || !strings.Contains(view, "Session inventory unavailable") {
				t.Fatal("unavailable inventory kept obsolete agreement", view)
			}
			if cmd := m.readLibraryAdoption(); cmd != nil || m.libraryAdoptionPanel.loading || m.libraryAdoptionPanel.report != nil {
				t.Fatal("unavailable inventory admitted a filesystem read")
			}
			if entries, err := os.ReadDir(filepath.Dir(m.statePath)); err != nil || len(entries) != 0 {
				t.Fatal("unavailable inventory created state", entries, err)
			}
		})
	}
}
