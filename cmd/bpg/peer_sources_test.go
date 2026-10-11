package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/federation"
	peerregistry "github.com/seanheiney/keelmux/internal/peers"
)

type entryInventoryFixture struct{ snapshot peerregistry.Snapshot }

func (f entryInventoryFixture) Fetch(context.Context, peerregistry.Config) (peerregistry.Snapshot, error) {
	return f.snapshot, nil
}

func entryEnrollment(t *testing.T) (configuration, string, domain.ID) {
	t.Helper()
	host := domain.NewID("host")
	session := domain.NewSession(host, "fixture", "/fixture", "fixture", "test")
	dir := filepath.Join(t.TempDir(), "peers")
	r, err := peerregistry.Open(dir, entryInventoryFixture{peerregistry.Snapshot{Version: peerregistry.Version, HostID: host, CapturedAt: time.Now().UTC(), Sessions: []domain.Session{session}}})
	if err != nil {
		t.Fatal(err)
	}
	c := peerregistry.Config{HostID: host, Alias: "fixture", KnownHostsFile: "/fixture/known_hosts", Fingerprint: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", RemoteExecutable: "/fixture/keelmux", RemoteSocket: "/fixture/control.sock", Grant: federation.Grant{Principal: "owner", HostID: host, Sessions: []domain.ID{session.ID}}, FreshForSeconds: 10, RetainForSeconds: 100}
	if _, err := r.Enroll(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	var conf configuration
	b, _ := json.Marshal(map[string]any{"principal": "owner", "peers": []any{map[string]any{"host_id": host, "inventory_store": dir}}})
	if err := json.Unmarshal(b, &conf); err != nil {
		t.Fatal(err)
	}
	return conf, dir, host
}

func TestEntryInventorySourceRequiresExistingStoreAndExactPrincipal(t *testing.T) {
	conf, dir, _ := entryEnrollment(t)
	missing := filepath.Join(t.TempDir(), "absent")
	conf.Peers[0].InventoryStore = missing
	if _, _, err := entrySources(conf); err == nil {
		t.Fatal("entry manufactured an unenrolled peer store")
	}
	if _, err := os.Lstat(missing); !os.IsNotExist(err) {
		t.Fatal("missing store was created", err)
	}
	conf.Peers[0].InventoryStore = dir
	conf.Principal = "another-owner"
	if _, _, err := entrySources(conf); err == nil {
		t.Fatal("entry widened the enrolled principal grant")
	}
	conf.Principal = "owner"
	sources, closeAll, err := entrySources(conf)
	if err != nil || len(sources) != 1 {
		t.Fatal("explicit enrollment was rejected", err)
	}
	defer closeAll()
	if _, ok := sources[0].Source.(federation.StreamSource); ok {
		t.Fatal("inventory-only source acquired runtime attachment")
	}
}

func TestEntryInventoryClosesRegistriesAfterLaterRouteFailure(t *testing.T) {
	conf, dir, host := entryEnrollment(t)
	conf.Peers = append(conf.Peers, conf.Peers[0])
	conf.Peers[1].InventoryStore = filepath.Join(t.TempDir(), "absent")
	var opened *peerregistry.Registry
	count := 0
	_, _, err := buildEntrySources(conf, func(path string) (*peerregistry.Registry, error) {
		count++
		var e error
		opened, e = peerregistry.Open(path, nil)
		return opened, e
	})
	if err == nil || count != 1 || opened == nil {
		t.Fatal("later route did not fail after opening exact enrollment", err, count)
	}
	if _, err := opened.View(host, "owner"); err == nil {
		t.Fatal("registry survived a failed entry construction")
	}
	r, err := peerregistry.Open(dir, nil)
	if err != nil {
		t.Fatal("cleanup prevented independent reopen", err)
	}
	defer r.Close()
	if _, err := r.View(host, "owner"); err != nil {
		t.Fatal("cleanup changed durable enrollment", err)
	}
}
