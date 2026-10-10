package wire

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This opt-in gate checks the Go transport against an actual pinned app-server.
// It does not create a thread, submit model work, or exercise broker authority.
func TestNativeCodexUnixInitialization(t *testing.T) {
	if os.Getenv("BP_NATIVE_CODEX_WIRE") != "1" {
		t.Skip("set BP_NATIVE_CODEX_WIRE=1 and BP_CODEX_NATIVE_BINARY to the installed native Codex binary")
	}
	binary := os.Getenv("BP_CODEX_NATIVE_BINARY")
	if !filepath.IsAbs(binary) || filepath.Clean(binary) != binary {
		t.Fatal("BP_CODEX_NATIVE_BINARY must identify a clean absolute native binary path")
	}
	dir, err := os.MkdirTemp("/tmp", "bp-codec-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	home, nativeHome, workspace := filepath.Join(dir, "home"), filepath.Join(dir, "codex"), filepath.Join(dir, "workspace")
	for _, path := range []string{home, nativeHome, workspace} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	// An allowlist avoids inherited credentials, proxies and native configuration.
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "CODEX_HOME=" + nativeHome, "TMPDIR=" + dir, "TERM=xterm-256color"}
	versionCtx, versionCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer versionCancel()
	versionCmd := exec.CommandContext(versionCtx, binary, "--version")
	versionCmd.Env = env
	version, err := versionCmd.Output()
	if err != nil || strings.TrimSpace(string(version)) != "codex-cli 0.159.0" {
		t.Fatal("native version differs from the pinned Codex 0.159.0 profile")
	}
	f, err := os.Open(binary)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	_, hashErr := io.Copy(hash, f)
	closeErr := f.Close()
	if hashErr != nil || closeErr != nil {
		t.Fatal("native binary digest could not be recorded")
	}
	config := "[projects." + strconv.Quote(workspace) + "]\ntrust_level = \"trusted\"\n"
	if err := os.WriteFile(filepath.Join(nativeHome, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "app.sock")
	args := []string{"app-server", "--listen", "unix://" + path, "--disable", "plugins", "--disable", "apps"}
	for _, override := range []string{
		"check_for_update_on_startup=false", "analytics.enabled=false", `cli_auth_credentials_store="file"`,
		`model="gpt-6.1-sol"`, `model_provider="bp_wire_probe"`,
		`model_providers.bp_wire_probe.name="Owned transport probe"`,
		`model_providers.bp_wire_probe.base_url="http://127.0.0.1:1/v1"`,
		`model_providers.bp_wire_probe.wire_api="responses"`,
		"model_providers.bp_wire_probe.requires_openai_auth=false", "model_providers.bp_wire_probe.supports_websockets=false",
		`sandbox_mode="read-only"`, `approval_policy="on-request"`,
	} {
		args = append(args, "-c", override)
	}
	// The owned launcher fixes umask before exec; an inherited permissive umask
	// otherwise creates a socket that the private transport correctly refuses.
	launchArgs := append([]string{"-c", `umask 077; exec "$@"`, "bp-owned-native", binary}, args...)
	cmd := exec.Command("/bin/sh", launchArgs...)
	cmd.Dir, cmd.Env = workspace, env
	// No inherited terminal or raw native output is displayed or recorded.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var targetInfo os.FileInfo
	serverExited := false
	// The native server may place its real socket in a shared per-user private
	// directory. Remove only our exact observed inode, after our server exits.
	t.Cleanup(func() {
		if !serverExited || targetInfo == nil {
			return
		}
		current, err := os.Lstat(path)
		if err == nil && os.SameFile(targetInfo, current) {
			if err := os.Remove(path); err != nil {
				t.Error("owned endpoint cleanup failed", err)
			}
		}
	})
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		select {
		case <-done:
			serverExited = true
			return
		default:
		}
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case <-done:
			serverExited = true
		case <-time.After(time.Second):
			_ = cmd.Process.Kill()
			select {
			case <-done:
				serverExited = true
			case <-time.After(3 * time.Second):
				t.Error("owned app-server cleanup did not complete")
			}
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	var upstreamMode os.FileMode
	requestedPath := path
	for {
		if info, err := os.Lstat(path); err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				target, err := filepath.EvalSymlinks(path)
				if err != nil {
					t.Fatal("owned native endpoint alias could not be resolved", err)
				}
				path = target
				continue
			}
			if info.Mode()&os.ModeSocket == 0 {
				t.Fatalf("owned app-server endpoint is not a socket: mode=%s", info.Mode())
			}
			upstreamMode = info.Mode().Perm()
			targetInfo = info
			break
		}
		select {
		case err := <-done:
			done <- err
			t.Fatal("owned app-server exited before creating its endpoint")
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("owned app-server did not create its endpoint within five seconds")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Resolve the owned server's alias explicitly; Dial still verifies the exact
	// canonical parent and socket owner/kind/mode. Never chmod shared native state.
	for _, id := range []json.RawMessage{json.RawMessage(`"wire-string-id"`), json.RawMessage(`9223372036854775807`)} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		c, err := Dial(ctx, path)
		if err != nil {
			cancel()
			t.Fatal("owned native WebSocket handshake failed", err)
		}
		t.Cleanup(func() { _ = c.Close() })
		frame, _ := json.Marshal(map[string]any{"id": id, "method": "initialize", "params": map[string]any{
			"clientInfo":   map[string]string{"name": "bp_wire_probe", "title": "Owned zero-turn transport probe", "version": "0.0.0"},
			"capabilities": map[string]any{"experimentalApi": true, "requestAttestation": false},
		}})
		if err := c.Write(ctx, frame); err != nil {
			cancel()
			t.Fatal("native initialization write failed", err)
		}
		acknowledged := false
		for !acknowledged {
			raw, err := c.Read(ctx)
			if err != nil {
				cancel()
				t.Fatal("native initialization response was not received", err)
			}
			var envelope struct {
				ID     json.RawMessage `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if json.Unmarshal(raw, &envelope) != nil {
				cancel()
				t.Fatal("native app-server returned a malformed JSON frame")
			}
			if string(envelope.ID) != string(id) {
				continue
			}
			if len(envelope.Result) == 0 || len(envelope.Error) != 0 {
				cancel()
				t.Fatal("native app-server did not acknowledge the exact typed initialization ID")
			}
			var result struct {
				CodexHome string `json:"codexHome"`
			}
			expectedHome, expectedErr := filepath.EvalSymlinks(nativeHome)
			if json.Unmarshal(envelope.Result, &result) != nil {
				cancel()
				t.Fatal("initialization returned invalid pinned metadata")
			}
			actualHome, actualErr := filepath.EvalSymlinks(result.CodexHome)
			if expectedErr != nil || actualErr != nil || actualHome != expectedHome {
				cancel()
				t.Fatal("initialization did not name the exact disposable native home")
			}
			acknowledged = true
		}
		if err := c.Write(ctx, []byte(`{"method":"initialized"}`)); err != nil {
			cancel()
			t.Fatal("native initialized notification write failed", err)
		}
		_ = c.Close()
		cancel()
	}
	t.Logf("native=%s binary_sha256=%s endpoint_alias=%t upstream_creation_mode=%04o enforced_mode=0600 initialized_channels=2 typed_ids=string,int64_max model_turns_submitted=0", strings.TrimSpace(string(version)), hex.EncodeToString(hash.Sum(nil)), requestedPath != path, upstreamMode)
}
