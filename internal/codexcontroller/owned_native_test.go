package codexcontroller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/coder/websocket"
	cb "github.com/seanheiney/keelmux/internal/codexbroker"
	"github.com/seanheiney/keelmux/internal/domain"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This zero-turn test launches only an owned isolated app-server. It reads no
// user credential/config/thread files and cannot reach a live model endpoint.
func ownedNativeFixture(t *testing.T, tcp bool) (Launch, domain.RuntimeRef, string) {
	t.Helper()
	if os.Getenv("BP_OWNED_CODEX_PROBE") != "1" {
		t.Skip("opt in with BP_OWNED_CODEX_PROBE=1 and BP_CODEX_NATIVE_BINARY")
	}
	binary := os.Getenv("BP_CODEX_NATIVE_BINARY")
	if !cleanPath(binary) {
		t.Fatal("native binary path required")
	}
	dir, err := os.MkdirTemp("/tmp", "kn-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	home, codexHome, workspace := filepath.Join(dir, "home"), filepath.Join(dir, "codex"), filepath.Join(dir, "workspace")
	for _, path := range []string{home, codexHome, workspace} {
		if err = os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "CODEX_HOME=" + codexHome, "TMPDIR=" + dir, "TERM=xterm-256color"}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	versionCmd := exec.CommandContext(ctx, binary, "--version")
	versionCmd.Env = env
	version, err := versionCmd.Output()
	if err != nil || strings.TrimSpace(string(version)) != "codex-cli 0.159.0" {
		t.Fatal("native version not pinned 0.159.0")
	}
	f, err := os.Open(binary)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.New()
	_, err = io.Copy(sum, f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	l := Launch{Endpoint: filepath.Join(dir, "app.sock"), CodexHome: codexHome, capability: strings.Repeat("a", 64), Binary: binary, BinarySHA256: hex.EncodeToString(sum.Sum(nil)), Request: StartRequest{OperationID: "native-probe", Workspace: workspace, Target: Target{ExecutionID: "native-probe"}}}
	listen := "unix://" + l.Endpoint
	if tcp {
		reserved, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		listen = "ws://" + reserved.Addr().String()
		reserved.Close()
	}
	args := []string{"app-server", "--listen", listen, "--ws-auth", "capability-token", "--ws-token-sha256", l.CapabilitySHA256(), "--disable", "plugins", "--disable", "apps"}
	for _, override := range []string{"check_for_update_on_startup=false", "analytics.enabled=false", `cli_auth_credentials_store="file"`, `model="gpt-6.1-sol"`, `model_provider="owned_probe"`, `model_providers.owned_probe.name="Owned zero-turn probe"`, `model_providers.owned_probe.base_url="http://127.0.0.1:1/v1"`, `model_providers.owned_probe.wire_api="responses"`, "model_providers.owned_probe.requires_openai_auth=false", "model_providers.owned_probe.supports_websockets=false", `approval_policy="on-request"`, `sandbox_mode="read-only"`} {
		args = append(args, "-c", override)
	}
	launchArgs := append([]string{"-c", `umask 077; exec "$@"`, "owned-codex-probe", binary}, args...)
	cmd := exec.Command("/bin/sh", launchArgs...)
	cmd.Env = env
	cmd.Dir = workspace
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var socketPath string
	var socketInfo os.FileInfo
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case <-done:
		case <-time.After(time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		if socketInfo != nil {
			if current, e := os.Lstat(socketPath); e == nil && os.SameFile(socketInfo, current) {
				os.Remove(socketPath)
			}
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if tcp {
			probe, probeErr := net.DialTimeout("tcp", strings.TrimPrefix(listen, "ws://"), 100*time.Millisecond)
			if probeErr == nil {
				probe.Close()
				break
			}
		}
		socketPath, _, err = ownedSocket(l.Endpoint)
		if !tcp && err == nil {
			socketInfo, _ = os.Lstat(socketPath)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("owned private endpoint unavailable")
		}
		select {
		case e := <-done:
			done <- e
			t.Fatal("owned app-server exited")
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	ref := ownedCurrentRef(t, cmd.Process.Pid)
	ref.ExecutionID = l.Request.Target.ExecutionID
	ref.OperationID = l.Request.OperationID
	return l, ref, listen
}

func TestOwnedNativeCodexCapabilityHandshake(t *testing.T) {
	l, ref, _ := ownedNativeFixture(t, false)
	// Pinned 0.159.0 ignores the websocket token flags for its Unix listener.
	// Record that native fact, then prove the guarded adapter fails closed.
	c, stop := context.WithTimeout(context.Background(), 3*time.Second)
	wrong, status, err := dialOwned(c, l, ref, strings.Repeat("b", 64))
	stop()
	if err != nil || wrong == nil || status != 101 {
		t.Fatal("pinned Unix auth behavior changed; requalify transport")
	}
	wrong.close()
	c, stop = context.WithTimeout(context.Background(), 3*time.Second)
	err = NewOwnedUpstream().Probe(c, l, ref)
	stop()
	if err == nil {
		t.Fatal("adapter accepted native listener without token enforcement")
	}
	t.Logf("native=0.159.0 binary_sha256=%s wrong_token_upgrade=accepted guarded_adapter=refused peer_pid_boot_start=verified model_turns=0", l.BinarySHA256)
}

// TCP auth is independently tested. There is no claimed TCP peer-PID or loaded
// executable identity proof, and this test does not enable the product adapter.
func TestOwnedNativeCodexLoopbackTokenEnforcement(t *testing.T) {
	l, _, endpoint := ownedNativeFixture(t, true)
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	bad, response, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPClient: client, HTTPHeader: http.Header{"Authorization": []string{"Bearer " + strings.Repeat("b", 64)}}})
	if bad != nil {
		bad.CloseNow()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatal("loopback did not reject wrong token before initialize")
	}
	good, _, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPClient: client, HTTPHeader: http.Header{"Authorization": []string{"Bearer " + l.Capability()}}})
	if err != nil {
		t.Fatal("loopback rejected exact token")
	}
	defer good.CloseNow()
	good.SetReadLimit(1 << 20)
	anchor := &ownedAnchor{conn: good, transport: transport}
	var init struct {
		CodexHome string `json:"codexHome"`
	}
	if err = anchor.call(ctx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "keelmux_owned_probe", "version": "0.1.0"}, "capabilities": map[string]any{"experimentalApi": true, "requestAttestation": false, "explicitGatewayOauth": true, "optOutNotificationMethods": []string{"remoteControl/status/changed", "thread/started", "thread/status/changed"}}}, &init); err != nil || init.CodexHome != l.CodexHome {
		t.Fatal("loopback native-home initialize did not match", err)
	}
	if err = anchor.write(ctx, map[string]any{"method": "initialized"}); err != nil {
		t.Fatal(err)
	}
	var loaded struct {
		Data []string        `json:"data"`
		Next json.RawMessage `json:"nextCursor"`
	}
	if err = anchor.write(ctx, map[string]any{"id": "loaded-probe", "method": "thread/loaded/list", "params": map[string]any{"limit": 1}}); err != nil {
		t.Fatal(err)
	}
	matched := false
	for count := 0; count < 16; count++ {
		_, raw, err := good.Read(ctx)
		if err != nil {
			t.Fatal("loaded probe read unavailable")
		}
		var envelope struct {
			ID     string          `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(raw, &envelope) != nil {
			t.Fatal("bad owned probe envelope")
		}
		if envelope.Method != "" {
			t.Logf("native initialization notification method=%s", envelope.Method)
			continue
		}
		if envelope.ID != "loaded-probe" || len(envelope.Error) != 0 {
			t.Fatal("unexpected native loaded-list ack")
		}
		if json.Unmarshal(envelope.Result, &loaded) != nil || len(loaded.Data) != 0 || string(loaded.Next) != "null" {
			t.Fatal("unexpected native loaded-thread list")
		}
		matched = true
		break
	}
	if !matched {
		t.Fatal("loaded list ack missing")
	}

	var started struct {
		Thread struct {
			ID     string `json:"id"`
			Cwd    string `json:"cwd"`
			Status struct {
				Type string `json:"type"`
			} `json:"status"`
		} `json:"thread"`
	}
	if err = anchor.call(ctx, "thread/start", map[string]any{"cwd": l.Request.Workspace, "ephemeral": false}, &started); err != nil || started.Thread.ID == "" || started.Thread.Cwd != l.Request.Workspace || started.Thread.Status.Type != "idle" {
		t.Fatal("owned empty thread creation failed", err)
	}
	threadID := started.Thread.ID
	if err = anchor.call(ctx, "thread/loaded/list", map[string]any{"limit": 2}, &loaded); err != nil || len(loaded.Data) != 1 || loaded.Data[0] != threadID || string(loaded.Next) != "null" {
		t.Fatal("exact newly created thread not uniquely loaded", err)
	}
	var read struct {
		Thread struct {
			ID     string `json:"id"`
			Cwd    string `json:"cwd"`
			Status struct {
				Type string `json:"type"`
			} `json:"status"`
		} `json:"thread"`
	}
	if err = anchor.call(ctx, "thread/read", map[string]any{"threadId": threadID, "includeTurns": false}, &read); err != nil || read.Thread.ID != threadID || read.Thread.Cwd != l.Request.Workspace || read.Thread.Status.Type != "idle" {
		t.Fatal("exact-thread inspection failed", err)
	}
	for _, method := range []string{"thread/queue/list", "thread/backgroundTerminals/list"} {
		var response struct {
			Data json.RawMessage `json:"data"`
			Next json.RawMessage `json:"nextCursor"`
		}
		if err = ownedProbeCall(t, ctx, anchor, method, map[string]any{"threadId": threadID, "limit": 1}, &response); err != nil || string(response.Data) != "[]" || string(response.Next) != "null" {
			t.Fatal("empty native queue/background not proven", method, err)
		}
	}

	t.Logf("native=0.159.0 binary_sha256=%s loopback=127.0.0.1 wrong_token=401 exact_token=accepted native_home=verified initial_loaded_threads=0 owned_persisted_thread=idle exact_thread_workspace=verified queue_background=empty model_turns=0 tcp_peer_image_identity=unqualified", l.BinarySHA256)
}

func ownedProbeCall(t *testing.T, ctx context.Context, a *ownedAnchor, method string, params any, result any) error {
	t.Helper()
	a.next++
	id := fmt.Sprintf("probe-%d", a.next)
	if err := a.write(ctx, map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return err
	}
	for count := 0; count < 32; count++ {
		_, raw, err := a.conn.Read(ctx)
		if err != nil {
			return err
		}
		f, err := cb.NewPolicy().Decode(raw)
		if err != nil {
			return err
		}
		if f.Kind == cb.Notification {
			t.Logf("native empty-thread notification=%s", f.Method)
			continue
		}
		if f.Kind == cb.RPCError {
			var failure struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			}
			json.Unmarshal(f.Payload, &failure)
			t.Logf("isolated native probe %s error=%d %s", method, failure.Code, failure.Message)
			return ErrUnavailable
		}
		if f.Kind != cb.Result || string(f.ID) != fmt.Sprintf("%q", id) {
			return ErrUnavailable
		}
		return json.Unmarshal(f.Payload, result)
	}
	return ErrUnavailable
}
