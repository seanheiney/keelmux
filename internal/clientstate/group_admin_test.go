package clientstate

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/seanheiney/keelmux/internal/domain"
)

func TestGroupAdministrationKeepsStableNavigationAndDrafts(t *testing.T) {
	s := New()
	company, _ := s.AddGroup("Company", "")
	product, _ := s.AddGroup("Product", company)
	other, _ := s.AddGroup("Incident review", "")
	thread := domain.NewID("thread")
	hostA, hostB := domain.NewID("host"), domain.NewID("host")
	ref := Reference{Kind: "thread", ID: thread}
	a := Reference{Kind: "workspace", Workspace: "/work/api", HostID: hostA}
	b := Reference{Kind: "workspace", Workspace: "/work/api", HostID: hostB}
	for _, r := range []Reference{ref, a, b} {
		if err := s.AddReference(product, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AddReference(other, ref); err != nil {
		t.Fatal(err)
	}
	s.Places[product] = Place{ThreadID: thread, Filter: "codex", Scroll: 9}
	s.SetDraft(Draft{ThreadID: thread, RecipientID: "lane_1", Text: "private unsent draft"})
	if err := s.SwitchGroup(product); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameGroup(product, "  Payments — production  "); err != nil {
		t.Fatal(err)
	}
	if err := s.ReparentGroup(product, other); err != nil {
		t.Fatal(err)
	}
	g, _ := s.Group(product)
	if g.Name != "Payments — production" || g.ParentID != other || len(g.Members) != 3 || s.ActiveGroup != product || s.Places[product].Scroll != 9 {
		t.Fatal("rename/reparent changed stable place", s)
	}
	if err := s.RemoveReference(product, a); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveReference(product, a); err != nil {
		t.Fatal(err)
	}
	g, _ = s.Group(product)
	if !reflect.DeepEqual(g.Members, []Reference{ref, b}) {
		t.Fatal("exact removal erased another host or thread", g)
	}
	if err := s.RemoveReference(product, ref); err != nil {
		t.Fatal(err)
	}
	session := domain.Session{ThreadID: thread, HostID: hostA}
	if s.Contains(product, session) || !s.Contains(other, session) {
		t.Fatal("overlapping collection was changed")
	}
	if err := s.ReparentGroup(product, ""); err != nil {
		t.Fatal(err)
	}
	if draft, ok := s.Draft(thread, "lane_1"); !ok || draft.Text != "private unsent draft" {
		t.Fatal("navigation erased addressed draft")
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidGroupAdministrationIsAtomic(t *testing.T) {
	s := New()
	a, _ := s.AddGroup("A", "")
	b, _ := s.AddGroup("B", a)
	c, _ := s.AddGroup("C", b)
	before, _ := json.Marshal(s)
	for _, change := range []func() error{
		func() error { return s.ReparentGroup(a, c) },
		func() error { return s.ReparentGroup(b, b) },
		func() error { return s.ReparentGroup(b, "missing") },
		func() error { return s.ReparentGroup("", a) },
		func() error { return s.ReparentGroup("missing", a) },
		func() error { return s.RenameGroup(b, "\x1b[31munsafe") },
		func() error { return s.RenameGroup(b, strings.Repeat("é", 65)) },
		func() error { return s.RenameGroup(b, string([]byte{0xff})) },
		func() error { return s.RemoveReference(b, Reference{Kind: "workspace", Workspace: "relative"}) },
		func() error { return s.RemoveReference("missing", Reference{Kind: "thread", ID: "thread_1"}) },
	} {
		if err := change(); err == nil {
			t.Fatal("invalid mutation accepted")
		}
		after, _ := json.Marshal(s)
		if string(after) != string(before) {
			t.Fatal("rejected mutation changed saved navigation")
		}
	}
}
