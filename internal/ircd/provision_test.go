package ircd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seanheiney/keelmux/internal/domain"
)

func TestPrivatePathsAndFiles(t *testing.T) {
	d := t.TempDir()
	if _, e := New(d + "/../other"); e == nil {
		t.Fatal("unclean path")
	}
	owned := filepath.Join(d, "owned")
	m, e := New(owned)
	if e != nil {
		t.Fatal(e)
	}
	u, e := m.lock(context.Background(), true)
	if e != nil {
		t.Fatal(e)
	}
	u()
	if e = publish(filepath.Join(owned, "keep"), []byte("original"), 0600, false); e != nil {
		t.Fatal(e)
	}
	if e = publish(filepath.Join(owned, "keep"), []byte("replacement"), 0600, false); e == nil {
		t.Fatal("overwrite")
	}
	if e = os.Symlink("keep", filepath.Join(owned, "alias")); e != nil {
		t.Fatal(e)
	}
	if _, e = readFile(filepath.Join(owned, "alias"), 100, 0600); e == nil {
		t.Fatal("followed link")
	}
	os.Chmod(filepath.Join(owned, "keep"), 0644)
	if _, e = readFile(filepath.Join(owned, "keep"), 100, 0600); e == nil {
		t.Fatal("accepted public file")
	}
	os.Chmod(owned, 0755)
	if _, e = New(owned); e == nil {
		t.Fatal("accepted public directory")
	}
}
func TestStrictResponse(t *testing.T) {
	valid := []byte(`{"success":true,"accountName":"keelmux-bot"}`)
	var a authResponse
	if strictJSON(valid, &a) != nil || !a.Success {
		t.Fatal("valid response")
	}
	for _, s := range []string{`{"Success":true}`, `{"success":true,"success":false}`, `{"success":true} {}`, `{"success":true,"token":"private"}`, `{"success":null}`, `{"success":true,"accountName":["x"]}`, "{\"success\":true,\"accountName\":\"\xff\"}"} {
		a = authResponse{}
		if strictJSON([]byte(s), &a) == nil {
			t.Fatalf("accepted %q", s)
		}
		if a.Success {
			t.Fatal("partial decode committed")
		}
	}
	var secret saslSecret
	if strictJSON([]byte(`{"account":"keelmux-bot","password":"private"}`), &secret) != nil {
		t.Fatal("private credential schema")
	}
}
func testArchive(t *testing.T, headers []*tar.Header, extra []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for _, h := range headers {
		if e := tw.WriteHeader(h); e != nil {
			t.Fatal(e)
		}
		if h.Typeflag == tar.TypeReg {
			tw.Write(bytes.Repeat([]byte("x"), int(h.Size)))
		}
	}
	tw.Close()
	gz.Write(extra)
	gz.Close()
	return b.Bytes()
}
func TestArchiveShape(t *testing.T) {
	base := []*tar.Header{{Name: "pin/ergo", Mode: 0700, Size: 1, Typeflag: tar.TypeReg}, {Name: "pin/LICENSE", Mode: 0600, Size: 1, Typeflag: tar.TypeReg}, {Name: "pin/default.yaml", Mode: 0600, Size: 1, Typeflag: tar.TypeReg}}
	if _, e := unpack(testArchive(t, base, nil), "pin"); e != nil {
		t.Fatal(e)
	}
	if _, e := unpack(testArchive(t, base, []byte("trailing")), "pin"); e == nil {
		t.Fatal("trailing archive payload")
	}
	for _, h := range []*tar.Header{{Name: "pin/../outside", Typeflag: tar.TypeReg}, {Name: "/absolute", Typeflag: tar.TypeReg}, {Name: "pin/link", Typeflag: tar.TypeSymlink, Linkname: "ergo"}, {Name: "pin/ergo", Typeflag: tar.TypeReg}, {Name: "pin/a\\b", Typeflag: tar.TypeReg}} {
		if _, e := unpack(testArchive(t, append(base, h), nil), "pin"); e == nil {
			t.Fatalf("accepted %s", h.Name)
		}
	}
}
func TestConfigGrammar(t *testing.T) {
	text := "server:\n    websockets:\n        enabled: true\n    name: old\naccounts:\n    auth: true\n"
	next, e := replaceYAML(text, []string{"server", "websockets"}, "enabled: false")
	if e != nil || !strings.Contains(next, "        enabled: false") {
		t.Fatal("block config", e)
	}
	next, e = replaceYAML(next, []string{"server", "name"}, quote("colon: literal"))
	if e != nil || !strings.Contains(next, `    name: "colon: literal"`) {
		t.Fatal("scalar config", e)
	}
	if _, e = replaceYAML(text, []string{"missing"}, "false"); e == nil {
		t.Fatal("missing grammar")
	}
	if !validLocalID(domain.NewID("ircd"), "ircd") || validLocalID("ircd_bad", "ircd") {
		t.Fatal("ID grammar")
	}
}
func TestRealPinnedInit(t *testing.T) {
	archive := os.Getenv("KEELMUX_ERGO_ARCHIVE")
	if archive == "" {
		t.Skip("explicit pinned real Ergo archive required")
	}
	b, e := os.ReadFile(archive)
	if e != nil {
		t.Fatal(e)
	}
	dir := filepath.Join(t.TempDir(), "server")
	m, e := New(dir)
	if e != nil {
		t.Fatal(e)
	}
	r, e := m.InstallArchive(context.Background(), bytes.NewReader(b))
	if e != nil {
		t.Fatal("install", e)
	}
	c, e := m.Init(context.Background(), InitOptions{})
	if e != nil {
		t.Fatal("init", e)
	}
	if c.Runtime != r || c.ID == "" || c.RealmID == "" {
		t.Fatal("configuration mismatch")
	}
	again, e := m.Init(context.Background(), InitOptions{})
	if e != nil || again != c {
		t.Fatal("idempotent init", e)
	}
	if conf, e := m.ScopedTLS(); e != nil || conf.InsecureSkipVerify || conf.RootCAs == nil {
		t.Fatal("scoped TLS", e)
	}
	if _, e = m.Dial(context.Background(), "127.0.0.1:1"); e != ErrConflict {
		t.Fatal("foreign realm dial")
	}
	raw, e := os.ReadFile(filepath.Join(dir, "ergo.yaml"))
	if e != nil {
		t.Fatal(e)
	}
	for _, s := range []string{"exempted: []", "allow-environment-overrides: false", "authentication-enabled: true"} {
		if !bytes.Contains(raw, []byte(s)) {
			t.Fatal("config missing", s)
		}
	}
	var rec configRecord
	if e = readJSON(filepath.Join(dir, "config.json"), &rec); e != nil {
		t.Fatal(e)
	}
	rec.Config.RealmID = "127.0.0.1:1"
	changed, _ := json.Marshal(rec)
	os.WriteFile(filepath.Join(dir, "config.json"), changed, 0600)
	if _, e = m.Config(); e == nil {
		t.Fatal("mutated scope accepted")
	}
}
