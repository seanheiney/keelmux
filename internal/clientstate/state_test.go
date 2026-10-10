package clientstate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seanheiney/keelmux/internal/domain"
)

func TestPrivatePlaceRoundTripAndInvalidWritePreservesOldFile(t *testing.T) {
	s := New()
	g, _ := s.AddGroup("Payments", "")
	s.SwitchGroup(g)
	s.Places[g] = Place{SessionID: "session_checkout", ThreadID: "thread_checkout", Filter: "codex", Scroll: 7, Expanded: []domain.ID{"work_api"}}
	s.Pending = &Attachment{SessionID: "session_checkout", ExecutionID: "execution_2", Generation: 2, OperationID: "attach_1", ClientID: s.ClientID, Token: "lease_1"}
	s.SetDraft(Draft{ThreadID: "thread_checkout", RecipientID: "lane_1", ExecutionID: "execution_1", Generation: 1, Text: "Review first\nThen continue"})
	path := filepath.Join(t.TempDir(), "client", "place.json")
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("place permissions = %o", info.Mode().Perm())
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.ClientID != s.ClientID || got.ActiveGroup != g || got.Places[g].SessionID != "session_checkout" || got.Places[g].Scroll != 7 || got.Places[g].Filter != "codex" || got.Pending.Token != "lease_1" {
		t.Fatalf("lost identity or place: %+v", got)
	}
	before, _ := os.ReadFile(path)
	s.Version = 99
	if err := Save(path, s); err == nil {
		t.Fatal("invalid version overwritten")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("failed write changed saved place")
	}
	if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".place-*")); len(leftovers) != 0 {
		t.Fatal("temporary place files leaked")
	}
}

func TestOverlappingGroupsFollowStableWorkAndDeletionRetainsDrafts(t *testing.T) {
	s := New()
	company, _ := s.AddGroup("Acme", "")
	product, _ := s.AddGroup("Payments", company)
	flat, _ := s.AddGroup("Incidents", "")
	s.AddReference(product, Reference{Kind: "thread", ID: "thread_checkout"})
	s.AddReference(flat, Reference{Kind: "workspace", Workspace: "/work/api"})
	first := domain.Session{ID: "session_1", WorkID: "work_1", ThreadID: "thread_checkout", Workspace: "/work/api", Execution: domain.Execution{ID: "execution_1", Generation: 1}}
	replacement := first
	replacement.ID = "session_2"
	replacement.HostID = "host_2"
	replacement.Execution = domain.Execution{ID: "execution_2", Generation: 2}
	for _, session := range []domain.Session{first, replacement} {
		if !s.Contains(company, session) || !s.Contains(product, session) || !s.Contains(flat, session) || !s.Contains("", session) {
			t.Fatal("stable work lost from overlapping collection")
		}
	}
	s.Places[product] = Place{SessionID: first.ID, Filter: "first"}
	s.Places[flat] = Place{SessionID: "other", Filter: "second"}
	s.SwitchGroup(product)
	s.SwitchGroup(flat)
	s.SwitchGroup(s.PreviousGroup)
	if s.ActiveGroup != product || s.Places[product].Filter != "first" {
		t.Fatal("per-group place overwritten")
	}
	s.SetDraft(Draft{ThreadID: first.ThreadID, RecipientID: "lane_1", Text: "draft"})
	if err := s.DeleteGroup(company); err != nil {
		t.Fatal(err)
	}
	child, _ := s.Group(product)
	if child.ParentID != "" || !s.Contains(product, replacement) {
		t.Fatal("child references lost when removing parent")
	}
	if err := s.DeleteGroup(product); err != nil {
		t.Fatal(err)
	}
	if s.ActiveGroup != "" {
		t.Fatal("deleted group still active")
	}
	if d, ok := s.Draft(first.ThreadID, "lane_1"); !ok || d.Text != "draft" {
		t.Fatal("navigation delete lost shared-thread draft")
	}
	if !s.Contains(flat, replacement) {
		t.Fatal("deletion modified another overlapping group")
	}
}

func TestDraftIsolationAcrossThreadRecipientAndReplacement(t *testing.T) {
	s := New()
	s.SetDraft(Draft{ThreadID: "thread_1", RecipientID: "lane_1", ExecutionID: "exec_old", Generation: 1, Text: "original"})
	s.SetDraft(Draft{ThreadID: "thread_1", RecipientID: "lane_2", Text: "second recipient"})
	s.SetDraft(Draft{ThreadID: "thread_2", RecipientID: "lane_1", Text: "other thread"})
	s.SetDraft(Draft{ThreadID: "thread_1", RecipientID: "lane_1", ExecutionID: "exec_new", Generation: 2, Text: "revised"})
	if len(s.Drafts) != 3 {
		t.Fatal("draft target IDs collided")
	}
	for _, want := range []Draft{{ThreadID: "thread_1", RecipientID: "lane_1", Text: "revised"}, {ThreadID: "thread_1", RecipientID: "lane_2", Text: "second recipient"}, {ThreadID: "thread_2", RecipientID: "lane_1", Text: "other thread"}} {
		if got, ok := s.Draft(want.ThreadID, want.RecipientID); !ok || got.Text != want.Text {
			t.Fatalf("wrong addressed draft: %+v", got)
		}
	}
}

func TestCorruptOrUnsupportedStateIsNotSilentlyReset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "place.json")
	for _, content := range []string{"{", `{"version":99,"client_id":"c"}`, `{"version":1,"client_id":"c"} {}`, `{"version":1,"client_id":"c","groups":[{"id":"a","name":"A","parent_id":"a"}]}`} {
		os.WriteFile(path, []byte(content), 0600)
		if _, err := Load(path); err == nil {
			t.Fatalf("accepted bad place: %s", content)
		}
	}
}

func TestLifetimeClientPlaceLockRejectsSimultaneousOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client", "place.json")
	first, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := AcquireLock(path)
	if second != nil || err == nil || !strings.Contains(err.Error(), "already open") {
		t.Fatalf("simultaneous client allowed to overwrite intent: %v", err)
	}
	independent, err := AcquireLock(path + ".independent")
	if err != nil {
		t.Fatal(err)
	}
	independent.Close()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("closed owner did not release kernel lock: %v", err)
	}
	third.Close()
	info, _ := os.Stat(path + ".lock")
	if info.Mode().Perm() != 0600 {
		t.Fatal("client lock file is not private")
	}
}

func TestWorkspaceGroupNamespaceDoesNotMatchUnrelatedHost(t *testing.T) {
	s := New()
	id, _ := s.AddGroup("Project", "")
	s.AddReference(id, Reference{Kind: "workspace", Workspace: "/work/api", HostID: "host_a"})
	local := domain.Session{Workspace: "/work/api", HostID: "host_a"}
	remote := local
	remote.HostID = "host_b"
	if !s.Contains(id, local) || s.Contains(id, remote) {
		t.Fatal("workspace group matched the same path on an unrelated server")
	}
}
