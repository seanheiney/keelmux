package ircd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/seanheiney/keelmux/internal/diagnosticfs"
	"github.com/seanheiney/keelmux/internal/domain"
)

// Receipt fixture only: this does not qualify Ergo itself or the archive.
func diagnosticIRCFixture(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "irc")
	os.Mkdir(dir, 0700)
	runtimeDir := filepath.Join(dir, "runtime")
	os.Mkdir(runtimeDir, 0700)
	binary := []byte("#!/bin/sh\necho forbidden > '" + filepath.Join(dir, "started") + "'\nexit 9\n")
	files := map[string][]byte{"ergo": binary, "LICENSE": []byte("fixture license"), "default.yaml": []byte("fixture default")}
	for name, raw := range files {
		mode := os.FileMode(0600)
		if name == "ergo" {
			mode = 0700
		}
		os.WriteFile(filepath.Join(runtimeDir, name), raw, mode)
	}
	target, p, e := nativePin()
	if e != nil {
		t.Fatal(e)
	}
	runtime := RuntimeInfo{Version: Version, Source: SourceCommit, Target: target, ArchiveSHA256: p.sha, BinarySHA256: digest(binary), DefaultSHA256: digest(files["default.yaml"]), LicenseSHA256: digest(files["LICENSE"]), Path: filepath.Join(runtimeDir, "ergo")}
	if e = writeJSON(filepath.Join(runtimeDir, "runtime.json"), runtime, false); e != nil {
		t.Fatal(e)
	}
	id := domain.NewID("ircd")
	ircPort, apiPort := 34561, 34562
	realm := "127.0.0.1:" + strconv.Itoa(ircPort)
	r := configRecord{Format: 1, Stage: "ready", IRCPort: ircPort, APIPort: apiPort, ServerName: "km" + strings.TrimPrefix(string(id), "ircd_") + ".local", Config: Config{ID: id, Endpoint: "ircs://" + realm, RealmID: realm, APIEndpoint: "https://127.0.0.1:" + strconv.Itoa(apiPort), CAFile: filepath.Join(dir, "ca.pem"), CredentialRef: filepath.Join(dir, "bot.json"), Account: "keelmux-bot", Runtime: runtime}}
	for name, hash := range map[string]*string{"ergo.yaml": &r.YAMLSHA256, "ca.pem": &r.CASHA256, "server.pem": &r.CertSHA256, "server.key": &r.KeySHA256} {
		raw := []byte("private-fixture-" + name)
		os.WriteFile(filepath.Join(dir, name), raw, 0600)
		*hash = digest(raw)
	}
	os.WriteFile(filepath.Join(dir, "ergo.db"), nil, 0600)
	if e = writeJSON(filepath.Join(dir, "config.json"), r, false); e != nil {
		t.Fatal(e)
	}
	return dir
}

func TestIRCDDiagnosticStaticAndUnchanged(t *testing.T) {
	path := diagnosticIRCFixture(t)
	dir, e := diagnosticfs.OpenDir(path)
	if e != nil {
		t.Fatal(e)
	}
	defer dir.Close()
	before := map[string][]byte{}
	filepath.Walk(path, func(p string, i os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		if i.Mode().IsRegular() {
			before[p], _ = os.ReadFile(p)
		}
		return nil
	})
	v, e := DiagnoseExisting(context.Background(), dir)
	if e != nil || v.Stage != "ready" || v.SavedState != "" {
		t.Fatal(v, e)
	}
	for p, b := range before {
		after, e := os.ReadFile(p)
		if e != nil || !bytes.Equal(b, after) {
			t.Fatal("modified configuration", p)
		}
	}
	for _, name := range []string{"started", "manager.lock", "lifecycle.json"} {
		if _, e := os.Lstat(filepath.Join(path, name)); !os.IsNotExist(e) {
			t.Fatal("started/initialized/locked managed IRC", name)
		}
	}
}

func TestIRCDDiagnosticRejectsDamagedReceipts(t *testing.T) {
	for _, kind := range []string{"changed_config", "changed_binary", "runtime_alias", "public_config", "config_alias", "missing_runtime"} {
		t.Run(kind, func(t *testing.T) {
			path := diagnosticIRCFixture(t)
			switch kind {
			case "changed_config":
				os.WriteFile(filepath.Join(path, "ergo.yaml"), []byte("changed"), 0600)
			case "changed_binary":
				os.WriteFile(filepath.Join(path, "runtime", "ergo"), []byte("changed"), 0700)
			case "runtime_alias":
				os.Rename(filepath.Join(path, "runtime"), filepath.Join(path, "actual"))
				os.Symlink("actual", filepath.Join(path, "runtime"))
			case "public_config":
				os.Chmod(filepath.Join(path, "config.json"), 0644)
			case "config_alias":
				os.Rename(filepath.Join(path, "config.json"), filepath.Join(path, "actual.json"))
				os.Symlink("actual.json", filepath.Join(path, "config.json"))
			case "missing_runtime":
				os.RemoveAll(filepath.Join(path, "runtime"))
			}
			dir, e := diagnosticfs.OpenDir(path)
			if e != nil {
				t.Fatal(e)
			}
			defer dir.Close()
			if _, e = DiagnoseExisting(context.Background(), dir); e == nil {
				t.Fatal("accepted damaged configuration")
			}
			if kind == "missing_runtime" {
				if _, e := os.Lstat(filepath.Join(path, "runtime")); !os.IsNotExist(e) {
					t.Fatal("recreated runtime directory")
				}
			}
		})
	}
}
