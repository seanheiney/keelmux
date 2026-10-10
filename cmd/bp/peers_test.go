package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/federation"
	"github.com/seanheiney/keelmux/internal/peers"
)

type cliPeerSource struct{ snapshot peers.Snapshot }

func (s cliPeerSource) Fetch(context.Context, peers.Config) (peers.Snapshot, error) {
	return s.snapshot, nil
}

func TestPeerCLIAbsentReadsAndInvalidArgumentsDoNotCreateState(t *testing.T) {
	dir := filepath.Join(groupsTestRoot(t), "missing")
	for _, args := range [][]string{{}, {"--help"}, {"list", "--principal", "owner"}} {
		if err := managePeers(context.Background(), dir, args, &bytes.Buffer{}); err != nil {
			t.Fatal(args, err)
		}
	}
	host := string(domain.NewID("host"))
	for _, args := range [][]string{{"unknown"}, {"enroll"}, {"list"}, {"list", "--principal", "owner", "extra"}, {"inspect", "--principal", "owner", "host_bad"}, {"grant", "--principal", "owner", host}, {"grant", "--principal", "owner", "--sessions", "session_bad", host}, {"list", "--principal", "bad\x1bowner"}} {
		if err := managePeers(context.Background(), dir, args, &bytes.Buffer{}); err == nil {
			t.Fatal("invalid arguments accepted", args)
		}
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatal("read/invalid arguments created runtime state", err)
	}
}

func TestPeerCLIRevokeAndScopeChangesPersistWithoutResurrectingCache(t *testing.T) {
	dir := filepath.Join(groupsTestRoot(t), "state")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	host := domain.NewID("host")
	session := domain.NewSession(host, "owned fixture", "/work/api", "fixture", "0")
	cfg := peers.Config{HostID: host, Alias: "fixture", KnownHostsFile: "/tmp/fixture-known-hosts", Fingerprint: "SHA256:" + strings.Repeat("A", 43), RemoteExecutable: "/usr/bin/keelmux", RemoteSocket: "/tmp/home/control.sock", Grant: federation.Grant{Principal: "owner", HostID: host, Sessions: []domain.ID{session.ID}}, FreshForSeconds: 30, RetainForSeconds: 60}
	r, err := peers.Open(filepath.Join(dir, "peers"), cliPeerSource{peers.Snapshot{Version: peers.Version, HostID: host, CapturedAt: time.Now().UTC(), Sessions: []domain.Session{session}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Enroll(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err = managePeers(context.Background(), dir, []string{"list", "--principal", "owner"}, &out); err != nil || !strings.Contains(out.String(), string(session.ID)) {
		t.Fatal("enrolled inventory missing", err, out.String())
	}
	var directory peers.Directory
	if err := json.Unmarshal(out.Bytes(), &directory); err != nil {
		t.Fatal(err)
	}
	for _, s := range directory.Sessions {
		if s.Execution.Runtime != (domain.RuntimeRef{}) || s.Capabilities.NativeAttach || s.Capabilities.Stop || s.Capabilities.SemanticPrompt {
			t.Fatal("inventory exported runtime authority")
		}
	}
	if err = managePeers(context.Background(), dir, []string{"grant", "--principal", "owner", "--sessions", "", string(host)}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err = managePeers(context.Background(), dir, []string{"grant", "--principal", "owner", "--sessions", string(session.ID), string(host)}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), string(session.ID)) {
		t.Fatal("scope expansion resurrected discarded records")
	}
	if err = managePeers(context.Background(), dir, []string{"revoke", "--principal", "owner", string(host)}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err = managePeers(context.Background(), dir, []string{"refresh", "--principal", "owner", string(host)}, &out); err != nil || !strings.Contains(out.String(), `"state": "revoked"`) {
		t.Fatal("revoked peer attempted transport or changed state", err, out.String())
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "peers" {
		t.Fatal("peer administration started a daemon", err)
	}
}

func TestPeerCLIRefusesSymlinkedClientState(t *testing.T) {
	root := groupsTestRoot(t)
	state := filepath.Join(root, "private")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "alias")
	if err := os.Symlink(state, link); err != nil {
		t.Fatal(err)
	}
	if err := managePeers(context.Background(), link, []string{"list", "--principal", "owner"}, &bytes.Buffer{}); err == nil {
		t.Fatal("symlink client namespace accepted")
	}
	if entries, _ := os.ReadDir(state); len(entries) != 0 {
		t.Fatal("refused command created state")
	}
}
