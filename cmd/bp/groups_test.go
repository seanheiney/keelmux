package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seanheiney/keelmux/internal/clientstate"
	"github.com/seanheiney/keelmux/internal/domain"
)

func groupsTestRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "km-groups-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

func groupCLI(t *testing.T, dir string, args ...string) clientstate.Group {
	t.Helper()
	var out bytes.Buffer
	if err := manageGroups(dir, args, &out); err != nil {
		t.Fatal(args, err)
	}
	var g clientstate.Group
	if err := json.Unmarshal(out.Bytes(), &g); err != nil {
		t.Fatal(err)
	}
	return g
}

func TestGroupsCLICompleteNavigationJourneyWithoutDaemon(t *testing.T) {
	dir := filepath.Join(groupsTestRoot(t), "state")
	company := groupCLI(t, dir, "create", "--name", "Acme")
	product := groupCLI(t, dir, "create", "--name", "Payments", "--parent", string(company.ID))
	incident := groupCLI(t, dir, "create", "--name", "Incidents")
	thread := string(domain.NewID("thread"))
	for _, group := range []domain.ID{product.ID, incident.ID} {
		g := groupCLI(t, dir, "add", "--kind", "thread", "--id", thread, string(group))
		if len(g.Members) != 1 {
			t.Fatal("membership was not persisted")
		}
	}
	groupCLI(t, dir, "rename", "--name", "Checkout", string(product.ID))
	groupCLI(t, dir, "parent", "--parent", string(incident.ID), string(product.ID))
	for _, args := range [][]string{{"select", string(product.ID)}, {"delete", string(company.ID)}} {
		if err := manageGroups(dir, args, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	}
	s, err := clientstate.Load(filepath.Join(dir, "place.json"))
	if err != nil {
		t.Fatal(err)
	}
	g, _ := s.Group(product.ID)
	if s.ActiveGroup != product.ID || g.Name != "Checkout" || g.ParentID != incident.ID || len(s.Groups) != 2 || !s.Contains(incident.ID, domain.Session{ThreadID: domain.ID(thread)}) {
		t.Fatal("persisted switcher view disagrees with CLI", s)
	}
	groupCLI(t, dir, "remove", "--kind", "thread", "--id", thread, string(product.ID))
	var out bytes.Buffer
	if err := manageGroups(dir, []string{"list"}, &out); err != nil {
		t.Fatal(err)
	}
	var list groupList
	if err := json.Unmarshal(out.Bytes(), &list); err != nil || len(list.Groups) != 2 || list.Active != product.ID {
		t.Fatal("bad list", err, out.String())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "place.json" && e.Name() != "place.json.lock" {
			t.Fatal("navigation contacted or created runtime authority", e.Name())
		}
	}
}

func TestGroupsHelpReadAndInvalidArgumentsNeverCreateState(t *testing.T) {
	dir := filepath.Join(groupsTestRoot(t), "missing")
	for _, args := range [][]string{{}, {"--help"}, {"list"}} {
		var out bytes.Buffer
		if err := manageGroups(dir, args, &out); err != nil {
			t.Fatal(args, err)
		}
		if args != nil && len(args) == 1 && args[0] == "list" && !strings.Contains(out.String(), `"groups": []`) {
			t.Fatal("empty navigation was not clear")
		}
	}
	for _, args := range [][]string{{"unknown"}, {"create"}, {"create", "--name", "bad\x1bname"}, {"create", "--name", "A", "extra"}, {"rename", "--name", "A", "group_bad"}, {"add", "--kind", "thread", "--id", "thread_bad", string(domain.NewID("group"))}, {"add", "--kind", "workspace", "--workspace", "/work/../api", string(domain.NewID("group"))}} {
		if err := manageGroups(dir, args, &bytes.Buffer{}); err == nil {
			t.Fatal("accepted invalid command", args)
		}
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatal("read or malformed command created state", err)
	}
}

func TestGroupsEditingRefusesActiveSwitcherAndPreservesFile(t *testing.T) {
	dir := filepath.Join(groupsTestRoot(t), "state")
	g := groupCLI(t, dir, "create", "--name", "A")
	path := filepath.Join(dir, "place.json")
	lock, err := clientstate.AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	before, _ := os.ReadFile(path)
	if err := manageGroups(dir, []string{"rename", "--name", "B", string(g.ID)}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "already open") {
		t.Fatal("editing bypassed switcher lock", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("locked edit changed place")
	}
	if err := manageGroups(dir, []string{"list"}, &bytes.Buffer{}); err != nil {
		t.Fatal("coherent read requires writer authority", err)
	}
}

func TestGroupsRejectUnsafePlaceAndCycleWithoutRewriting(t *testing.T) {
	root := groupsTestRoot(t)
	dir := filepath.Join(root, "state")
	a := groupCLI(t, dir, "create", "--name", "A")
	b := groupCLI(t, dir, "create", "--name", "B", "--parent", string(a.ID))
	path := filepath.Join(dir, "place.json")
	before, _ := os.ReadFile(path)
	if err := manageGroups(dir, []string{"parent", "--parent", string(b.ID), string(a.ID)}, &bytes.Buffer{}); err == nil {
		t.Fatal("cycle accepted")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("rejected cycle rewrote place")
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"list"}, {"create", "--name", "C"}} {
		if err := manageGroups(alias, args, &bytes.Buffer{}); err == nil {
			t.Fatal("symlink state accepted")
		}
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := manageGroups(dir, []string{"list"}, &bytes.Buffer{}); err == nil {
		t.Fatal("public client place was read")
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0644 {
		t.Fatal("read command repaired permissions")
	}
}
