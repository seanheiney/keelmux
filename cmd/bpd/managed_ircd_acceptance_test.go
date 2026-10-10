package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/adapters/nativepty"
	"github.com/seanheiney/keelmux/internal/channels"
	"github.com/seanheiney/keelmux/internal/chatadapter"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/ircd"
	"github.com/seanheiney/keelmux/internal/protocol"
)

// This opt-in product qualification builds the actual control executables from
// a clean frozen checkout. It uses only a private owned Ergo realm, its private
// account files, and a shell input sentinel. No provider or terminal input API is
// involved. The default unit suite performs no provisioning or external IO.
//
//	KEELMUX_MANAGED_IRCD_ACCEPTANCE=1 KEELMUX_ERGO_ARCHIVE=/absolute/pinned.tar.gz \
//	  go test ./cmd/bpd -run '^TestManagedIRCDProductAcceptance$' -v -count=1
//
// An optional absolute KEELMUX_MANAGED_IRCD_REPORT names a new JSON receipt.
func TestManagedIRCDProductAcceptance(t *testing.T) {
	if os.Getenv("KEELMUX_MANAGED_IRCD_ACCEPTANCE") != "1" {
		t.Skip("explicit owned product acceptance only")
	}
	archive := os.Getenv("KEELMUX_ERGO_ARCHIVE")
	if !filepath.IsAbs(archive) {
		t.Fatal("explicit absolute pinned offline archive required")
	}
	sourceDir, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal("source directory unavailable")
	}
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = sourceDir
		b, e := c.Output()
		if e != nil {
			t.Fatal("frozen source identity unavailable")
		}
		return strings.TrimSpace(string(b))
	}
	if git("status", "--porcelain") != "" {
		t.Fatal("product qualification requires a clean frozen source checkout")
	}
	source := git("rev-parse", "HEAD")
	root, err := os.MkdirTemp("/tmp", "km-product-")
	if err != nil {
		t.Fatal("owned private test root unavailable")
	}
	bin, state := filepath.Join(root, "bin"), filepath.Join(root, "s")
	if os.Mkdir(bin, 0700) != nil || os.Mkdir(state, 0700) != nil {
		t.Fatal("owned private test directories unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	report := map[string]any{
		"schema": "keelmux-managed-ircd-product/v1", "source_commit": source,
		"source_clean": true, "host_target": runtime.GOOS + "/" + runtime.GOARCH,
		"go_version": runtime.Version(), "started_at": time.Now().UTC(),
		"scope":                      "isolated owned CLI/API/native/TLS/IRC authored-inbox acceptance",
		"provider_or_model_activity": false, "native_input_sent": false,
		"production_or_customer_proof": false,
		"limitations": []string{
			"IRC intake is bounded live coverage; disconnects explicitly report a history gap.",
			"Delivered proves matching server echo, not reader receipt or model acceptance.",
			"Registration permission consumed before failed authentication can require manual repair.",
			"This proof covers the recorded source and host only; package and customer qualification are separate.",
		},
	}
	checks := map[string]bool{}
	report["checks"] = checks
	stage := "build"
	client := protocol.NewClient(filepath.Join(state, "control.sock"))
	defer client.HTTP.CloseIdleConnections()
	var native *nativepty.Backend
	var manager *ircd.Manager
	var fixture domain.RuntimeRef
	var ircInstance domain.ID
	var peer *chatadapter.IRC
	var controlDone <-chan error
	var supervisorPIDs []int
	var controlPIDs []int
	// Cleanup only uses exact private daemon/supervisor authentication. Retain
	// failed state for repair if any owned process's exit cannot be established.
	t.Cleanup(func() {
		clean, c := context.WithTimeout(context.Background(), 20*time.Second)
		defer c()
		if peer != nil {
			_ = peer.Close()
		}
		cleanupOK := true
		if manager != nil {
			if s, e := manager.Status(clean); e == nil && s.Alive {
				cleanupOK = manager.Stop(clean, s.Instance) == nil
			} else if e != nil && ircInstance != "" {
				cleanupOK = false
			}
		}
		if native != nil && fixture.ExecutionID != "" {
			if e := native.Stop(clean, fixture); e != nil {
				cleanupOK = false
			}
			if o, e := native.Inspect(clean, fixture); e != nil || o.Alive {
				cleanupOK = false
			}
		}
		if d, e := client.Daemon(clean); e == nil {
			if client.ShutdownDaemon(clean, d.Instance) != nil {
				cleanupOK = false
			}
			if !managedProductWait(clean, 5*time.Second, func() bool {
				_, e := os.Lstat(filepath.Join(state, "control.sock"))
				return os.IsNotExist(e)
			}) {
				cleanupOK = false
			}
		}
		if controlDone != nil {
			select {
			case <-controlDone:
			case <-clean.Done():
				cleanupOK = false
			}
		}
		// A native helper exits when its owned child exits and releases its socket.
		if native != nil && !managedProductWait(clean, 5*time.Second, func() bool {
			paths, _ := filepath.Glob(filepath.Join(state, "native", "*.sock"))
			for _, p := range paths {
				if i, e := os.Lstat(p); e == nil && i.Mode()&os.ModeSocket != 0 {
					return false
				}
			}
			return true
		}) {
			cleanupOK = false
		}
		if !managedProductWait(clean, 5*time.Second, func() bool {
			for _, pid := range append(supervisorPIDs, controlPIDs...) {
				if syscall.Kill(pid, 0) != syscall.ESRCH {
					return false
				}
			}
			return true
		}) {
			cleanupOK = false
		}
		checks["exact_authenticated_cleanup"] = cleanupOK
		report["finished_at"] = time.Now().UTC()
		report["failed_stage"] = stage
		report["passed"] = !t.Failed() && cleanupOK
		if path := os.Getenv("KEELMUX_MANAGED_IRCD_REPORT"); path != "" {
			if !filepath.IsAbs(path) || !strings.HasPrefix(filepath.Base(path), "managed-ircd-product-") || filepath.Ext(path) != ".json" {
				t.Error("receipt requires a new absolute managed-ircd-product-*.json path")
			} else if b, e := json.MarshalIndent(report, "", "  "); e != nil {
				t.Error("finite receipt encoding failed")
			} else if f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600); e != nil {
				t.Error("new owned receipt could not be created")
			} else {
				_, e = f.Write(append(b, '\n'))
				closeErr := f.Close()
				if e != nil || closeErr != nil {
					t.Error("finite receipt write failed")
				}
			}
		}
		if !cleanupOK {
			t.Error("owned process cleanup unconfirmed; private state retained")
		}
		if !t.Failed() && cleanupOK {
			_ = os.RemoveAll(root)
		} else {
			t.Log("owned private acceptance state retained at", root)
		}
	})
	ensure := func(ok bool, code string) {
		t.Helper()
		if !ok {
			t.Fatalf("product stage %s: %s", stage, code)
		}
		checks[code] = true
	}
	var binaryDigests = map[string]string{}
	for _, pair := range [][2]string{{"keelmux", "bp"}, {"keelmuxd", "bpd"}, {"keelmux-supervisor", "keelmux-supervisor"}} {
		c := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags", "-X github.com/seanheiney/keelmux/internal/buildinfo.Commit="+source, "-o", filepath.Join(bin, pair[0]), "./cmd/"+pair[1])
		c.Dir = sourceDir
		out, e := c.CombinedOutput()
		ensure(e == nil, fmt.Sprintf("actual_%s_built_diagnostic_bytes_%d", pair[0], len(out)))
		binaryDigests[pair[0]] = managedProductDigest(t, filepath.Join(bin, pair[0]))
	}
	report["binary_sha256"] = binaryDigests
	report["archive_sha256"] = managedProductDigest(t, archive)
	env := []string{"PATH=" + bin, "HOME=" + root, "TMPDIR=" + root, "TERM=xterm-256color", "LANG=C.UTF-8"}
	ensure(managedProductMissing(filepath.Join(bin, "tmux")), "no_tmux_on_control_path")
	cli := func(out any, wantSuccess bool, args ...string) {
		t.Helper()
		c := exec.CommandContext(ctx, filepath.Join(bin, "keelmux"), append([]string{"--state", state}, args...)...)
		c.Env = env
		var stdout, stderr bytes.Buffer
		c.Stdout, c.Stderr = &stdout, &stderr
		e := c.Run()
		if (e == nil) != wantSuccess {
			t.Fatalf("product stage %s: CLI %s failed expectation (safe diagnostics %d bytes)", stage, strings.Join(args[:min(2, len(args))], " "), stderr.Len())
		}
		if out != nil && wantSuccess && json.Unmarshal(stdout.Bytes(), out) != nil {
			t.Fatalf("product stage %s: CLI response schema invalid", stage)
		}
	}
	stage = "daemon startup"
	control := exec.Command(filepath.Join(bin, "keelmuxd"), "--state", state)
	control.Env = env
	ensure(control.Start() == nil, "actual_daemon_started")
	done := make(chan error, 1)
	controlDone = done
	go func() { done <- control.Wait() }()
	ensure(managedProductWait(ctx, 10*time.Second, func() bool { _, e := client.Daemon(ctx); return e == nil }), "actual_private_bp1_ready")
	for _, name := range []string{"tmux.sock", "tmux.sock.bp", "irc"} {
		ensure(managedProductMissing(filepath.Join(state, name)), "lazy_optional_"+name)
	}
	native, err = nativepty.NewWithOptions(filepath.Join(state, "native"), nativepty.Options{HelperPath: filepath.Join(bin, "keelmux-supervisor")})
	ensure(err == nil, "owned_native_backend")
	stage = "inert native fixture"
	sentinel, ready := filepath.Join(root, "native-input"), filepath.Join(root, "fixture-ready")
	ensure(os.WriteFile(sentinel, nil, 0600) == nil, "input_sentinel_created")
	launch := protocol.LaunchRequest{OperationID: domain.NewID("op"), Name: "inert IRC inbox fixture", Workspace: root, Provider: "fixture", ProviderVersion: "owned-test", Argv: []string{"/bin/sh", "-c", "/bin/stty -icanon -echo min 1 time 0; printf ready > '" + ready + "'; exec /bin/cat > '" + sentinel + "'"}}
	session, e := client.Launch(ctx, launch)
	fixture = session.Session.Execution.Runtime
	ensure(e == nil && fixture.Backend == "native" && fixture.ExecutionID != "" && fixture.Pane == "", "actual_native_fixture_running")
	supervisorPIDs = append(supervisorPIDs, managedProductSupervisorPID(t, state, fixture.ExecutionID))
	ensure(managedProductWait(ctx, 5*time.Second, func() bool { return !managedProductMissing(ready) }), "fixture_byte_input_sentinel_ready")
	report["fixture_execution"] = fixture.ExecutionID
	report["stable_thread"] = session.Session.ThreadID
	stage = "offline provisioning and actual CLI"
	manager, err = ircd.NewWithSupervisor(filepath.Join(state, "irc"), native)
	ensure(err == nil, "owned_ircd_manager")
	f, e := os.Open(archive)
	ensure(e == nil, "pinned_offline_archive_opened")
	info, e := manager.InstallArchive(ctx, f)
	f.Close()
	ensure(e == nil && info.Version == ircd.Version && info.Source == ircd.SourceCommit, "verified_pinned_offline_runtime")
	report["ergo_runtime"] = map[string]string{"version": info.Version, "source": info.Source, "target": info.Target, "binary_sha256": info.BinarySHA256}
	var connection protocol.IRCDConnection
	cli(&connection, true, "ircd", "init")
	ensure(connection.ServerID != "" && connection.Account == "keelmux-bot", "actual_cli_init")
	var started protocol.IRCDStatus
	startOp := domain.NewID("operation")
	cli(&started, true, "ircd", "start", "--operation", string(startOp))
	ircInstance = started.Instance
	ensure(started.Alive && started.OperationID == startOp, "actual_cli_ircd_start")
	supervisorPIDs = append(supervisorPIDs, managedProductSupervisorPID(t, state, ircInstance))
	var same protocol.IRCDStatus
	cli(&same, true, "ircd", "start", "--operation", string(startOp))
	ensure(same.Instance == ircInstance, "same_ircd_start_operation_no_replay")
	var human protocol.IRCDConnection
	cli(&human, true, "ircd", "account", "alice")
	ensure(human.Account == "alice" && human.CredentialRef != connection.CredentialRef && human.RealmID == connection.RealmID, "actual_human_account_private_ref")
	var binding protocol.ChannelStatus
	bindID, channelOp := domain.NewID("binding"), domain.NewID("operation")
	bindArgs := []string{"ircd", "bind", "--session", string(session.Session.ID), "--allow-user", "alice", "--binding", string(bindID), "--operation", string(channelOp)}
	cli(&binding, true, bindArgs...)
	ensure(binding.Active && binding.ThreadID == session.Session.ThreadID && !binding.SemanticDispatch && binding.SelfID == connection.Account, "actual_cli_stable_thread_registered_bind")
	registered, e := manager.ConfirmChannel(ctx, binding.ChannelID, binding.ThreadID)
	ensure(e == nil && registered.State == "confirmed" && registered.Owner == connection.Account && registered.ID == channelOp && registered.RegisteredAt != "", "authenticated_exact_registered_channel_owner")
	report["registered_channel"] = map[string]any{"channel": binding.ChannelID, "owner": registered.Owner, "operation": registered.ID, "registered_at": registered.RegisteredAt}
	stage = "actual human TLS SASL peer"
	peer = chatadapter.NewIRCWithDialer(manager.Dial)
	peerBinding := channels.Binding{ID: domain.NewID("binding"), Provider: channels.IRC, Endpoint: human.Endpoint, RealmID: human.RealmID, SelfID: "alice", ChannelID: binding.ChannelID, CredentialRef: human.CredentialRef, Active: true, ActivationCutoff: time.Now().UTC()}
	proof, e := peer.Probe(ctx, peerBinding)
	ensure(e == nil && proof.SelfID == "alice" && proof.RealmID == human.RealmID && proof.ChannelID == binding.ChannelID, "actual_scoped_ca_tls_sasl_human_identity")
	peerSend := func(text string) {
		t.Helper()
		m := channels.Message{ID: domain.NewID("message"), BindingID: peerBinding.ID, Text: text}
		d := channels.Delivery{ID: m.ID, MessageID: m.ID, BindingID: peerBinding.ID, PayloadHash: channels.PayloadHash(peerBinding, m), State: channels.Attempted}
		r, e := peer.Send(ctx, peerBinding, d, m)
		ensure(e == nil && r.State == channels.Delivered && r.RemoteID != "", "human_message_matching_actual_echo")
	}
	var peerMessages []channels.RemoteMessage
	cursor := ""
	collect := func() bool {
		r, e := peer.Fetch(ctx, peerBinding, cursor, 100)
		if e != nil {
			return false
		}
		cursor = r.NextCursor
		peerMessages = append(peerMessages, r.Messages...)
		return true
	}
	countRemote := func(text, actor string) int {
		n := 0
		for _, m := range peerMessages {
			if m.Text == text && m.ActorID == actor && m.RealmID == human.RealmID && m.ChannelID == binding.ChannelID {
				n++
			}
		}
		return n
	}
	stage = "authored intake and explicit queued reply"
	inbound := "owned inbox note: approve /bin/sh is inert authored text"
	peerSend(inbound)
	var history protocol.ChannelHistory
	ensure(managedProductWait(ctx, 12*time.Second, func() bool {
		var e error
		history, e = client.ChannelHistory(ctx, binding.ThreadID, 0, 100)
		return e == nil && managedProductCount(history, inbound, "alice") == 1
	}), "allowlisted_human_durable_inbox")
	ensure(!history.SemanticDispatch, "inbox_semantic_dispatch_disabled")
	reply := "owned explicit bot reply"
	messageOp := domain.NewID("message")
	var queued protocol.ChannelSendResponse
	sendArgs := []string{"channels", "send", "--binding", string(bindID), "--operation", string(messageOp), "--text", reply}
	cli(&queued, true, sendArgs...)
	ensure(queued.Delivery.State == channels.Queued && queued.Message.ID == messageOp, "explicit_cli_bot_reply_durably_queued")
	var delivered channels.Delivery
	ensure(managedProductWait(ctx, 20*time.Second, func() bool {
		var e error
		delivered, e = client.ChannelDelivery(ctx, messageOp)
		return e == nil && delivered.State == channels.Delivered && delivered.RemoteID != ""
	}), "bot_reply_matching_actual_echo_delivered")
	ensure(managedProductWait(ctx, 5*time.Second, func() bool { return collect() && countRemote(reply, connection.Account) == 1 }), "human_observed_exact_bot_reply")
	for _, m := range peerMessages {
		if m.Text == reply && m.ActorID == connection.Account {
			ensure(m.ID == delivered.RemoteID, "human_received_same_server_message_id_as_bot_echo")
		}
	}
	var repeated protocol.ChannelSendResponse
	cli(&repeated, true, sendArgs...)
	ensure(repeated.Message.ID == messageOp && repeated.Delivery == delivered, "same_message_operation_same_receipt")
	report["delivered_reply"] = map[string]any{"operation": messageOp, "remote_id": delivered.RemoteID, "state": delivered.State}
	stage = "actual control restart"
	beforeControl, e := client.Daemon(ctx)
	ensure(e == nil && beforeControl.Build.Commit == source, "actual_control_source_identity")
	beforeIRC, e := manager.Status(ctx)
	ensure(e == nil && beforeIRC.Instance == ircInstance, "native_ircd_before_control_restart")
	cli(nil, true, "daemon", "restart", "--instance", beforeControl.Instance)
	afterControl, e := client.Daemon(ctx)
	ensure(e == nil && afterControl.Instance != beforeControl.Instance && afterControl.PID != beforeControl.PID && afterControl.Build.Commit == source, "actual_control_process_replaced")
	controlPIDs = append(controlPIDs, beforeControl.PID, afterControl.PID)
	afterIRC, e := manager.Status(ctx)
	ensure(e == nil && afterIRC.Alive && afterIRC.Instance == beforeIRC.Instance && afterIRC.StartTime == beforeIRC.StartTime, "native_ircd_survives_control_restart")
	sessions, e := client.List(ctx)
	ensure(e == nil && len(sessions) == 1 && sessions[0].ThreadID == binding.ThreadID && sessions[0].Execution.Runtime == fixture && sessions[0].Execution.State == "running", "native_fixture_stable_identity_survives_control_restart")
	history, e = client.ChannelHistory(ctx, binding.ThreadID, 0, 100)
	ensure(e == nil && managedProductCount(history, inbound, "alice") == 1 && managedProductCount(history, reply, "local-operator") == 1, "authored_inbox_persists_control_restart")
	cli(&repeated, true, sendArgs...)
	ensure(repeated.Delivery == delivered, "same_message_operation_after_restart_no_replay")
	var rebound protocol.ChannelStatus
	cli(&rebound, true, bindArgs...)
	ensure(rebound.ID == binding.ID && rebound.ThreadID == binding.ThreadID && rebound.ChannelID == binding.ChannelID && rebound.ActivationCutoff == binding.ActivationCutoff, "registered_binding_readonly_reconfirmation")
	// Allow enough time for the restarted daemon's bounded intake poll. Current
	// IRC adapter generations explicitly preserve the disconnection gap warning.
	ensure(managedProductWait(ctx, 12*time.Second, func() bool {
		list, e := client.Channels(ctx)
		return e == nil && len(list) == 1 && list[0].LastSuccess != nil
	}), "actual_bot_adapter_reconnected")
	secondInbound := "owned human note after control restart"
	peerSend(secondInbound)
	ensure(managedProductWait(ctx, 12*time.Second, func() bool {
		var e error
		history, e = client.ChannelHistory(ctx, binding.ThreadID, 0, 100)
		return e == nil && managedProductCount(history, secondInbound, "alice") == 1
	}), "allowlisted_intake_after_control_restart")
	ensure(collect() && countRemote(reply, connection.Account) == 1, "actual_bot_reply_no_duplicate_after_retries_restart")
	stage = "native input isolation"
	input, e := os.ReadFile(sentinel)
	observed, observeErr := native.Inspect(ctx, fixture)
	ensure(e == nil && len(input) == 0 && observeErr == nil && observed.Alive && observed.Ref == fixture && observed.WritableClients == 0, "authored_inbox_never_native_input")
	report["native_input_bytes"] = len(input)
	report["control_instances"] = []string{beforeControl.Instance, afterControl.Instance}
	stage = "exact instance replacement and stale stop"
	peer.Close()
	peer = nil
	var replacement protocol.IRCDStatus
	cli(&replacement, true, "ircd", "restart", "--instance", string(ircInstance), "--operation", string(domain.NewID("operation")))
	ensure(replacement.Alive && replacement.Instance != ircInstance, "actual_ircd_explicit_replacement")
	cli(nil, false, "ircd", "stop", "--instance", string(ircInstance))
	ircInstance = replacement.Instance
	supervisorPIDs = append(supervisorPIDs, managedProductSupervisorPID(t, state, ircInstance))
	cli(&same, true, "ircd", "status")
	ensure(same.Alive && same.Instance == replacement.Instance, "stale_stop_preserves_replacement")
	registeredAgain, e := manager.ConfirmChannel(ctx, binding.ChannelID, binding.ThreadID)
	ensure(e == nil && registeredAgain.Owner == registered.Owner && registeredAgain.RegisteredAt == registered.RegisteredAt && registeredAgain.ID == registered.ID, "registered_channel_durable_across_ircd_replacement")
	cli(&human, true, "ircd", "account", "alice")
	ensure(human.Account == "alice", "human_account_readonly_after_ircd_replacement")
	report["ircd_instances"] = []domain.ID{started.Instance, replacement.Instance}
	stage = "complete"
}

func managedProductWait(ctx context.Context, duration time.Duration, f func() bool) bool {
	deadline := time.Now().Add(duration)
	for ctx.Err() == nil && time.Now().Before(deadline) {
		if f() {
			return true
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
	return false
}
func managedProductMissing(path string) bool { _, e := os.Lstat(path); return os.IsNotExist(e) }
func managedProductCount(h protocol.ChannelHistory, text, actor string) int {
	n := 0
	for _, m := range h.Messages {
		if m.Text == text && m.AuthorID == actor && m.ThreadID == h.ThreadID {
			n++
		}
	}
	return n
}
func managedProductDigest(t *testing.T, path string) string {
	t.Helper()
	f, e := os.Open(path)
	if e != nil {
		t.Fatal("owned artifact digest unavailable")
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		t.Fatal("owned artifact digest read failed")
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Project only the owned helper PID for a read-only exit check; never publish
// the receipt's authentication token or send signals from persisted PID data.
func managedProductSupervisorPID(t *testing.T, state string, execution domain.ID) int {
	t.Helper()
	sum := sha256.Sum256([]byte(execution))
	b, e := os.ReadFile(filepath.Join(state, "native", hex.EncodeToString(sum[:])[:24]+".json"))
	var projected struct {
		PID int `json:"supervisor_pid"`
	}
	if e != nil || json.Unmarshal(b, &projected) != nil || projected.PID <= 0 {
		t.Fatal("owned native helper receipt unavailable")
	}
	return projected.PID
}
