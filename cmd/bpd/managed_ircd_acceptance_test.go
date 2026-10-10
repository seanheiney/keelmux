package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/adapters/nativepty"
	"github.com/seanheiney/keelmux/internal/channels"
	"github.com/seanheiney/keelmux/internal/chatadapter"
	"github.com/seanheiney/keelmux/internal/config"
	"github.com/seanheiney/keelmux/internal/daemon"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/ircd"
	"github.com/seanheiney/keelmux/internal/ircdchannel"
	"github.com/seanheiney/keelmux/internal/protocol"
	bpruntime "github.com/seanheiney/keelmux/internal/runtime"
	"github.com/seanheiney/keelmux/internal/store"
	"golang.org/x/sys/unix"
)

// This opt-in product qualification builds the actual control executables from
// a clean frozen checkout. It uses only a private owned Ergo realm, its private
// account files, and three shell input sentinels in two explicit projects. No provider or terminal input API is
// involved. The default unit suite performs no provisioning or external IO.
//
//	KEELMUX_MANAGED_IRCD_ACCEPTANCE=1 KEELMUX_ERGO_ARCHIVE=/absolute/pinned.tar.gz \
//	  go test ./cmd/bpd -run '^TestManagedIRCDProductAcceptance$' -v -count=1
//
// An optional absolute KEELMUX_MANAGED_IRCD_REPORT names a new JSON receipt.
// KEELMUX_MANAGED_IRCD_BIN_DIR optionally names an existing owned directory of
// actual package executables. Those three explicit files are frozen into the
// private bin; all --version metadata must match this clean checkout first.
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	report := map[string]any{
		"schema": "keelmux-irc-project-product/v1", "source_commit": source,
		"source_clean": true, "host_target": runtime.GOOS + "/" + runtime.GOARCH,
		"go_version": runtime.Version(), "started_at": time.Now().UTC(),
		"scope":                      "isolated owned project CLI/API/native/TLS/IRC addressed authored-inbox acceptance",
		"provider_or_model_activity": false, "native_input_sent": false,
		"production_or_customer_proof": false,
		"limitations": []string{
			"IRC intake is bounded live coverage; disconnects explicitly report a history gap.",
			"Delivered proves matching server echo, not reader receipt or model acceptance.",
			"Registration permission consumed before failed authentication can require manual repair.",
			"Lost native launch acknowledgements require exact authenticated reconciliation; unavailable authority leaves cleanup unresolved and retains private state.",
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
	var fixtures []domain.RuntimeRef
	var launchIntents []managedProductLaunchIntent
	var ircInstance domain.ID
	var peers [2]*chatadapter.IRC
	var controlDone <-chan error
	var supervisorPIDs []int
	var controlPIDs []int
	var processProofs []managedProductProcessProof
	// Cleanup only uses exact private daemon/supervisor authentication. Retain
	// failed state for repair if any owned process's exit cannot be established.
	t.Cleanup(func() {
		clean, c := context.WithTimeout(context.Background(), 20*time.Second)
		defer c()
		for _, peer := range peers {
			if peer != nil {
				_ = peer.Close()
			}
		}
		// Resolve every operation recorded before launch IO. A missing HTTP ACK
		// cannot make the absence of an acknowledged runtime into absence proof.
		var cleanupLookup managedProductLaunchLookup
		if native != nil {
			cleanupLookup = native
		}
		recovered, unresolved := managedProductRecoverLaunches(clean, client, cleanupLookup, launchIntents)
		cleanupOK := len(unresolved) == 0
		report["native_launch_intent_count"] = len(launchIntents)
		report["unresolved_native_launch_operations"] = unresolved
		for _, ref := range recovered {
			fixtures = append(fixtures, ref)
			proof, e := managedProductReadOwnedProcess(state, ref.ExecutionID)
			if e != nil {
				cleanupOK = false
			} else {
				processProofs = append(processProofs, proof)
				supervisorPIDs = append(supervisorPIDs, proof.SupervisorPID)
			}
		}
		if manager != nil {
			if s, e := manager.Status(clean); e == nil && s.Alive {
				if manager.Stop(clean, s.Instance) != nil {
					cleanupOK = false
				}
			} else if e != nil && ircInstance != "" {
				cleanupOK = false
			}
		}
		if native != nil {
			for _, fixture := range fixtures {
				if e := native.Stop(clean, fixture); e != nil {
					cleanupOK = false
				}
				if o, e := native.Inspect(clean, fixture); e != nil || o.Alive {
					cleanupOK = false
				}
			}
		}
		if d, e := client.Daemon(clean); e == nil {
			controlPIDs = append(controlPIDs, d.PID)
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
			ownedPIDs := append(append([]int{}, supervisorPIDs...), controlPIDs...)
			for _, proof := range processProofs {
				ownedPIDs = append(ownedPIDs, proof.PID)
			}
			for _, pid := range ownedPIDs {
				if syscall.Kill(pid, 0) != syscall.ESRCH {
					return false
				}
			}
			return true
		}) {
			cleanupOK = false
		}
		checks["exact_authenticated_cleanup"] = cleanupOK
		report["owned_process_proofs"] = processProofs
		report["owned_native_execution_count"] = len(processProofs)
		report["owned_native_helper_count"] = len(processProofs)
		report["finished_at"] = time.Now().UTC()
		report["final_stage"] = stage
		report["passed"] = !t.Failed() && cleanupOK
		if path := os.Getenv("KEELMUX_MANAGED_IRCD_REPORT"); path != "" {
			if !filepath.IsAbs(path) || !strings.HasPrefix(filepath.Base(path), "irc-project-product-") || filepath.Ext(path) != ".json" {
				t.Error("receipt requires a new absolute irc-project-product-*.json path")
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
	env := []string{"PATH=" + bin, "HOME=" + root, "TMPDIR=" + root, "TERM=xterm-256color", "LANG=C.UTF-8"}
	var binaryDigests map[string]string
	if supplied := os.Getenv("KEELMUX_MANAGED_IRCD_BIN_DIR"); supplied != "" {
		report["binary_origin"] = "supplied-package-executables"
		binaryDigests, err = managedProductFreezePackageBinaries(ctx, supplied, bin)
		if err != nil {
			t.Fatalf("product stage %s: supplied executable tuple could not be safely frozen", stage)
		}
		for _, name := range managedProductBinaryNames {
			checks["actual_"+name+"_package_bytes_frozen"] = true
		}
	} else {
		report["binary_origin"] = "clean-source-build"
		binaryDigests = map[string]string{}
		for _, pair := range [][2]string{{"keelmux", "bp"}, {"keelmuxd", "bpd"}, {"keelmux-supervisor", "keelmux-supervisor"}} {
			c := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags", "-X github.com/seanheiney/keelmux/internal/buildinfo.Commit="+source, "-o", filepath.Join(bin, pair[0]), "./cmd/"+pair[1])
			c.Dir = sourceDir
			out, e := c.CombinedOutput()
			ensure(e == nil, fmt.Sprintf("actual_%s_built_diagnostic_bytes_%d", pair[0], len(out)))
			binaryDigests[pair[0]] = managedProductDigest(t, filepath.Join(bin, pair[0]))
		}
	}
	executableVersion, err := managedProductVerifyExecutableVersions(ctx, bin, env, source)
	if err != nil {
		t.Fatalf("product stage %s: actual executable versions do not match clean source and one another", stage)
	}
	for name, expected := range binaryDigests {
		if managedProductDigest(t, filepath.Join(bin, name)) != expected {
			t.Fatalf("product stage %s: frozen executable bytes changed during version preflight", stage)
		}
	}
	report["binary_version"] = executableVersion
	report["binary_version_commit"] = source
	report["binary_version_protocol"] = protocol.Version
	report["binary_sha256"] = binaryDigests
	report["archive_sha256"] = managedProductDigest(t, archive)
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
	controlPIDs = append(controlPIDs, control.Process.Pid)
	done := make(chan error, 1)
	controlDone = done
	go func() { done <- control.Wait() }()
	ensure(managedProductWait(ctx, 10*time.Second, func() bool { _, e := client.Daemon(ctx); return e == nil }), "actual_private_bp1_ready")
	for _, name := range []string{"tmux.sock", "tmux.sock.bp", "irc"} {
		ensure(managedProductMissing(filepath.Join(state, name)), "lazy_optional_"+name)
	}
	native, err = nativepty.NewWithOptions(filepath.Join(state, "native"), nativepty.Options{HelperPath: filepath.Join(bin, "keelmux-supervisor")})
	ensure(err == nil, "owned_native_backend")
	stage = "three inert native fixtures and explicit projects"
	var sessions [3]protocol.LaunchResponse
	var sentinels [3]string
	var fixtureProofs [3]managedProductProcessProof
	workspaces := [2]string{filepath.Join(root, "project-a"), filepath.Join(root, "project-b")}
	for _, workspace := range workspaces {
		ensure(os.Mkdir(workspace, 0700) == nil, "owned_project_workspace_created")
	}
	projectIndex := [3]int{0, 0, 1}
	for i := range sessions {
		sentinels[i] = filepath.Join(root, fmt.Sprintf("native-input-%d", i))
		ready := filepath.Join(root, fmt.Sprintf("fixture-ready-%d", i))
		ensure(os.WriteFile(sentinels[i], nil, 0600) == nil, fmt.Sprintf("input_sentinel_%d_created", i))
		launch := protocol.LaunchRequest{OperationID: domain.NewID("op"), Name: fmt.Sprintf("inert IRC project fixture %d", i), Workspace: workspaces[projectIndex[i]], Provider: "fixture", ProviderVersion: "owned-test", Argv: []string{"/bin/sh", "-c", "/bin/stty -icanon -echo min 1 time 0; printf ready > '" + ready + "'; exec /bin/cat > '" + sentinels[i] + "'"}}
		// Keep the exact intent before IO; on a lost or malformed ACK cleanup
		// may only recover that operation through the private control/native APIs.
		launchIntents = append(launchIntents, managedProductLaunchIntent{Request: launch})
		var e error
		sessions[i], e = client.Launch(ctx, launch)
		fixture := sessions[i].Session.Execution.Runtime
		acknowledged := e == nil && managedProductLaunchAcknowledged(launch, sessions[i])
		if acknowledged {
			launchIntents[len(launchIntents)-1].Runtime = fixture
			fixtures = append(fixtures, fixture)
		}
		ensure(acknowledged, fmt.Sprintf("actual_native_fixture_%d_running", i))
		fixtureProofs[i] = managedProductOwnedProcess(t, state, fixture.ExecutionID)
		processProofs = append(processProofs, fixtureProofs[i])
		supervisorPIDs = append(supervisorPIDs, fixtureProofs[i].SupervisorPID)
		ensure(managedProductWait(ctx, 5*time.Second, func() bool { return !managedProductMissing(ready) }), fmt.Sprintf("fixture_%d_byte_input_sentinel_ready", i))
	}
	var projects [2]domain.Project
	for i := range projects {
		id := domain.NewID("project")
		cli(&projects[i], true, "projects", "create", "--name", fmt.Sprintf("Owned project %d", i), "--workspace", workspaces[i], "--project", string(id))
		ensure(projects[i].Validate() == nil && projects[i].ID == id && projects[i].Workspace == workspaces[i] && projects[i].HostID == sessions[0].Session.HostID, fmt.Sprintf("actual_cli_explicit_project_%d", i))
		var repeated domain.Project
		cli(&repeated, true, "projects", "create", "--name", projects[i].Name, "--workspace", projects[i].Workspace, "--project", string(id))
		ensure(repeated == projects[i], fmt.Sprintf("same_project_%d_identity_no_replay", i))
	}
	projectList, e := client.Projects(ctx)
	ensure(e == nil && len(projectList) == 2, "actual_durable_project_registry")
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
	ircProof := managedProductOwnedProcess(t, state, ircInstance)
	processProofs = append(processProofs, ircProof)
	supervisorPIDs = append(supervisorPIDs, ircProof.SupervisorPID)
	var same protocol.IRCDStatus
	cli(&same, true, "ircd", "start", "--operation", string(startOp))
	ensure(same.Instance == ircInstance, "same_ircd_start_operation_no_replay")
	var human protocol.IRCDConnection
	cli(&human, true, "ircd", "account", "alice")
	ensure(human.Account == "alice" && human.CredentialRef != connection.CredentialRef && human.RealmID == connection.RealmID, "actual_human_account_private_ref")
	originalHuman := human
	var bindings [3]protocol.ChannelStatus
	var bindArgs [3][]string
	var threadCredentials [3]ircd.Credential
	var channelOps [3]domain.ID
	for i := range bindings {
		id, operation := domain.NewID("binding"), domain.NewID("operation")
		channelOps[i] = operation
		bindArgs[i] = []string{"ircd", "bind", "--project", string(projects[projectIndex[i]].ID), "--session", string(sessions[i].Session.ID), "--allow-user", "alice", "--binding", string(id), "--operation", string(operation)}
		cli(&bindings[i], true, bindArgs[i]...)
		nick, nickErr := ircd.ThreadAccount(sessions[i].Session.ThreadID)
		channel, channelErr := ircdchannel.ProjectChannelName(ircdchannel.Project{ID: projects[projectIndex[i]].ID, HostID: projects[projectIndex[i]].HostID, Name: projects[projectIndex[i]].Name, Workspace: projects[projectIndex[i]].Workspace})
		ensure(bindings[i].Active && bindings[i].ThreadID == sessions[i].Session.ThreadID && bindings[i].ProjectID == projects[projectIndex[i]].ID && bindings[i].IRCAddressed && !bindings[i].SemanticDispatch && nickErr == nil && channelErr == nil && bindings[i].SelfID == nick && bindings[i].ChannelID == channel, fmt.Sprintf("actual_cli_project_thread_%d_registered_addressed_bind", i))
		threadCredentials[i], e = manager.EnsureThreadAccount(ctx, bindings[i].ThreadID)
		ensure(e == nil && threadCredentials[i].Account == nick, fmt.Sprintf("thread_%d_exact_owned_service_account", i))
	}
	ensure(bindings[0].ChannelID == bindings[1].ChannelID && bindings[0].SelfID != bindings[1].SelfID && bindings[0].ChannelID != bindings[2].ChannelID && bindings[0].SelfID != bindings[2].SelfID && bindings[1].SelfID != bindings[2].SelfID, "two_threads_shared_project_distinct_bots_other_project_separate")
	ensure(threadCredentials[0].CredentialRef != threadCredentials[1].CredentialRef && threadCredentials[0].CredentialRef != threadCredentials[2].CredentialRef && threadCredentials[1].CredentialRef != threadCredentials[2].CredentialRef, "all_thread_credentials_distinct_private_owned_refs")
	var registered [2]ircd.ProjectChannelOperation
	for i, b := range []protocol.ChannelStatus{bindings[0], bindings[2]} {
		registered[i], e = manager.ConfirmProjectChannel(ctx, b.ChannelID, b.ProjectID)
		firstOperation := channelOps[0]
		if i == 1 {
			firstOperation = channelOps[2]
		}
		ensure(e == nil && registered[i].State == "confirmed" && registered[i].Owner == connection.Account && registered[i].ProjectID == projects[i].ID && registered[i].ID == firstOperation && registered[i].RegisteredAt != "", fmt.Sprintf("authenticated_exact_project_%d_registered_channel_owner", i))
	}
	report["projects"] = []map[string]any{
		{"project_id": projects[0].ID, "channel": registered[0].Channel, "owner": registered[0].Owner, "operation": registered[0].ID, "registered_at": registered[0].RegisteredAt},
		{"project_id": projects[1].ID, "channel": registered[1].Channel, "owner": registered[1].Owner, "operation": registered[1].ID, "registered_at": registered[1].RegisteredAt},
	}
	var threadProofs []map[string]any
	for i, b := range bindings {
		threadProofs = append(threadProofs, map[string]any{"session_id": b.SessionID, "thread_id": b.ThreadID, "execution_id": fixtures[i].ExecutionID, "project_id": b.ProjectID, "channel": b.ChannelID, "account": b.SelfID, "nick": b.SelfID, "binding_id": b.ID, "addressed": b.IRCAddressed})
	}
	report["threads"] = threadProofs
	var adaptersAfter time.Time
	readyAdapters := func() bool {
		list, err := client.Channels(ctx)
		if err != nil || len(list) != len(bindings) {
			return false
		}
		for _, b := range bindings {
			found := false
			for _, status := range list {
				if status.ID == b.ID && status.SelfID == b.SelfID && status.ProjectID == b.ProjectID && status.IRCAddressed && status.LastSuccess != nil && !status.LastSuccess.Before(adaptersAfter) {
					found = true
				}
			}
			if !found {
				return false
			}
		}
		return true
	}
	ensure(managedProductWait(ctx, 15*time.Second, readyAdapters), "all_three_actual_stable_bot_adapters_connected")
	stage = "actual human TLS SASL peers"
	var peerBindings [2]channels.Binding
	var peerMessages [2][]channels.RemoteMessage
	var cursors [2]string
	connectPeers := func() {
		t.Helper()
		for i := range peers {
			peers[i] = chatadapter.NewIRCWithDialer(manager.Dial)
			peerBindings[i] = channels.Binding{ID: domain.NewID("binding"), Provider: channels.IRC, Endpoint: human.Endpoint, RealmID: human.RealmID, SelfID: "alice", ChannelID: registered[i].Channel, CredentialRef: human.CredentialRef, Active: true, ActivationCutoff: time.Now().UTC()}
			proof, err := peers[i].Probe(ctx, peerBindings[i])
			ensure(err == nil && proof.SelfID == "alice" && proof.RealmID == human.RealmID && proof.ChannelID == registered[i].Channel, fmt.Sprintf("actual_scoped_ca_tls_sasl_human_project_%d", i))
			cursors[i] = ""
		}
	}
	connectPeers()
	peerSend := func(project int, text string) {
		t.Helper()
		b := peerBindings[project]
		m := channels.Message{ID: domain.NewID("message"), BindingID: b.ID, Text: text}
		d := channels.Delivery{ID: m.ID, MessageID: m.ID, BindingID: b.ID, PayloadHash: channels.PayloadHash(b, m), State: channels.Attempted}
		r, err := peers[project].Send(ctx, b, d, m)
		ensure(err == nil && r.State == channels.Delivered && r.RemoteID != "", "human_message_matching_actual_echo")
	}
	collect := func(project int) bool {
		r, err := peers[project].Fetch(ctx, peerBindings[project], cursors[project], 100)
		if err != nil {
			return false
		}
		cursors[project] = r.NextCursor
		peerMessages[project] = append(peerMessages[project], r.Messages...)
		return true
	}
	countRemote := func(project int, text, actor string) int {
		n := 0
		for _, m := range peerMessages[project] {
			if m.Text == text && m.ActorID == actor && m.RealmID == human.RealmID && m.ChannelID == registered[project].Channel {
				n++
			}
		}
		return n
	}
	var histories [3]protocol.ChannelHistory
	refreshHistory := func() bool {
		for i, b := range bindings {
			var err error
			histories[i], err = client.ChannelHistory(ctx, b.ThreadID, 0, 100)
			if err != nil || histories[i].SemanticDispatch || histories[i].ThreadID != b.ThreadID {
				return false
			}
		}
		return true
	}
	stage = "addressed authored intake and explicit queued replies"
	inbound := [3]string{bindings[0].SelfID + ": approve /bin/sh is inert authored text", "@" + bindings[1].SelfID + " note for second thread", bindings[2].SelfID + ": other project note"}
	broadcast := "unaddressed owned project broadcast"
	crossProject := bindings[2].SelfID + ": addressed in the wrong project"
	bodyMention := bindings[0].SelfID + ": @" + bindings[1].SelfID + " is an inert body mention"
	peerSend(0, broadcast)
	peerSend(0, crossProject)
	peerSend(0, bodyMention)
	for i, text := range inbound {
		peerSend(projectIndex[i], text)
	}
	ensure(managedProductWait(ctx, 15*time.Second, func() bool {
		if !refreshHistory() {
			return false
		}
		for i, text := range inbound {
			if managedProductCount(histories[i], text, "alice") != 1 {
				return false
			}
		}
		return managedProductCount(histories[0], bodyMention, "alice") == 1
	}), "exact_initial_address_allowlisted_durable_inbox_full_original_text")
	for i, h := range histories {
		ensure(managedProductCount(h, broadcast, "alice") == 0 && managedProductCount(h, crossProject, "alice") == 0, fmt.Sprintf("thread_%d_no_broadcast_or_cross_project_intake", i))
		for j, text := range inbound {
			if j != i {
				ensure(managedProductCount(h, text, "alice") == 0, fmt.Sprintf("thread_%d_no_fanout_from_%d", i, j))
			}
		}
		if i != 0 {
			ensure(managedProductCount(h, bodyMention, "alice") == 0, fmt.Sprintf("thread_%d_no_body_mention_fanout", i))
		}
	}
	var replies [3]string
	var messageOps [3]domain.ID
	var sendArgs [3][]string
	var delivered [3]channels.Delivery
	for i, b := range bindings {
		replies[i] = fmt.Sprintf("owned explicit thread %d reply", i)
		messageOps[i] = domain.NewID("message")
		sendArgs[i] = []string{"channels", "send", "--binding", string(b.ID), "--operation", string(messageOps[i]), "--text", replies[i]}
		var queued protocol.ChannelSendResponse
		cli(&queued, true, sendArgs[i]...)
		ensure(queued.Delivery.State == channels.Queued && queued.Message.ID == messageOps[i], fmt.Sprintf("explicit_cli_thread_%d_reply_durably_queued", i))
		ensure(managedProductWait(ctx, 20*time.Second, func() bool {
			var err error
			delivered[i], err = client.ChannelDelivery(ctx, messageOps[i])
			return err == nil && delivered[i].State == channels.Delivered && delivered[i].RemoteID != ""
		}), fmt.Sprintf("thread_%d_reply_matching_actual_echo_delivered", i))
		ensure(managedProductWait(ctx, 5*time.Second, func() bool {
			return collect(projectIndex[i]) && countRemote(projectIndex[i], replies[i], b.SelfID) == 1
		}), fmt.Sprintf("human_observed_exact_thread_%d_bot_reply", i))
		for _, m := range peerMessages[projectIndex[i]] {
			if m.Text == replies[i] && m.ActorID == b.SelfID {
				ensure(m.ID == delivered[i].RemoteID, fmt.Sprintf("thread_%d_human_server_id_equals_bot_echo", i))
			}
		}
		var repeated protocol.ChannelSendResponse
		cli(&repeated, true, sendArgs[i]...)
		ensure(repeated.Message.ID == messageOps[i] && repeated.Delivery == delivered[i], fmt.Sprintf("thread_%d_same_message_operation_same_receipt", i))
	}
	var replyProofs []map[string]any
	for i := range replies {
		replyProofs = append(replyProofs, map[string]any{"operation": messageOps[i], "bot_account": bindings[i].SelfID, "remote_id": delivered[i].RemoteID, "state": delivered[i].State})
	}
	report["delivered_replies"] = replyProofs
	stage = "actual control restart"
	beforeControl, e := client.Daemon(ctx)
	ensure(e == nil && beforeControl.Build.Commit == source, "actual_control_source_identity")
	beforeControlStart := managedProductControlStart(t, beforeControl.PID)
	beforeIRC, e := manager.Status(ctx)
	ensure(e == nil && beforeIRC.Instance == ircInstance, "native_ircd_before_control_restart")
	cli(nil, true, "daemon", "restart", "--instance", beforeControl.Instance)
	afterControl, e := client.Daemon(ctx)
	ensure(e == nil && afterControl.Instance != beforeControl.Instance && afterControl.PID != beforeControl.PID && afterControl.Build.Commit == source, "actual_control_process_replaced")
	controlPIDs = append(controlPIDs, beforeControl.PID, afterControl.PID)
	report["control_processes"] = []map[string]any{
		{"instance": beforeControl.Instance, "pid": beforeControl.PID, "process_start": beforeControlStart},
		{"instance": afterControl.Instance, "pid": afterControl.PID, "process_start": managedProductControlStart(t, afterControl.PID)},
	}
	afterIRC, e := manager.Status(ctx)
	ensure(e == nil && afterIRC.Alive && afterIRC.Instance == beforeIRC.Instance && afterIRC.StartTime == beforeIRC.StartTime, "native_ircd_survives_control_restart")
	ensure(managedProductOwnedProcess(t, state, ircInstance) == ircProof, "exact_ircd_process_and_helper_lifetime_preserved")
	listed, e := client.List(ctx)
	ensure(e == nil && len(listed) == 3, "all_native_fixtures_survive_control_restart")
	for i, session := range sessions {
		found := false
		for _, listedSession := range listed {
			if listedSession.ID == session.Session.ID && listedSession.ThreadID == bindings[i].ThreadID && listedSession.Execution.Runtime == fixtures[i] && listedSession.Execution.State == "running" {
				found = true
			}
		}
		ensure(found && managedProductOwnedProcess(t, state, fixtures[i].ExecutionID) == fixtureProofs[i], fmt.Sprintf("exact_thread_%d_native_identity_and_lifetime_preserved", i))
	}
	projectList, e = client.Projects(ctx)
	ensure(e == nil && len(projectList) == 2, "project_registry_survives_control_restart")
	for _, original := range projects {
		p, err := client.Project(ctx, original.ID)
		ensure(err == nil && p == original, "exact_immutable_project_survives_control_restart")
	}
	ensure(refreshHistory(), "durable_inbox_available_after_control_restart")
	for i := range bindings {
		ensure(managedProductCount(histories[i], inbound[i], "alice") == 1 && managedProductCount(histories[i], replies[i], "local-operator") == 1, fmt.Sprintf("thread_%d_authored_inbox_persists_control_restart", i))
		var repeated protocol.ChannelSendResponse
		cli(&repeated, true, sendArgs[i]...)
		ensure(repeated.Delivery == delivered[i], fmt.Sprintf("thread_%d_message_operation_after_restart_no_replay", i))
	}
	verifyRoutes := func(stageName string) {
		t.Helper()
		for i, original := range bindings {
			var rebound protocol.ChannelStatus
			cli(&rebound, true, bindArgs[i]...)
			ensure(rebound.ID == original.ID && rebound.ThreadID == original.ThreadID && rebound.ProjectID == original.ProjectID && rebound.IRCAddressed && rebound.SelfID == original.SelfID && rebound.ChannelID == original.ChannelID && rebound.ActivationCutoff == original.ActivationCutoff, fmt.Sprintf("thread_%d_exact_membership_account_nick_route_%s", i, stageName))
			credential, err := manager.EnsureThreadAccount(ctx, original.ThreadID)
			ensure(err == nil && credential == threadCredentials[i], fmt.Sprintf("thread_%d_private_account_ref_stable_%s", i, stageName))
		}
		for i, original := range registered {
			confirmed, err := manager.ConfirmProjectChannel(ctx, original.Channel, original.ProjectID)
			ensure(err == nil && confirmed.Owner == original.Owner && confirmed.RegisteredAt == original.RegisteredAt && confirmed.ID == original.ID, fmt.Sprintf("project_%d_registration_timestamp_operation_stable_%s", i, stageName))
		}
	}
	verifyRoutes("control_restart")
	ensure(managedProductWait(ctx, 15*time.Second, readyAdapters), "all_stable_bot_adapters_reconnected_after_control_restart")
	secondInbound := [3]string{bindings[0].SelfID + ": first thread after control restart", "@" + bindings[1].SelfID + " second thread after control restart", bindings[2].SelfID + ": other project after control restart"}
	for i, text := range secondInbound {
		peerSend(projectIndex[i], text)
	}
	ensure(managedProductWait(ctx, 15*time.Second, func() bool {
		if !refreshHistory() {
			return false
		}
		for i, text := range secondInbound {
			if managedProductCount(histories[i], text, "alice") != 1 {
				return false
			}
		}
		return true
	}), "all_exact_addressed_intake_after_control_restart")
	observeNoReplay := func() {
		t.Helper()
		for n := 0; n < 20; n++ {
			for project := range peers {
				ensure(collect(project), "bounded_actual_peer_fetch")
			}
			for i, b := range bindings {
				ensure(countRemote(projectIndex[i], replies[i], b.SelfID) == 1, fmt.Sprintf("thread_%d_actual_reply_no_duplicate_after_retries_restart", i))
			}
			timer := time.NewTimer(50 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				t.Fatal("bounded duplicate observation cancelled")
			case <-timer.C:
			}
		}
	}
	observeNoReplay()
	stage = "exact IRC replacement and stale stop"
	for i, peer := range peers {
		ensure(peer.Close() == nil, "owned_human_peer_closed")
		peers[i] = nil
	}
	var replacement protocol.IRCDStatus
	cli(&replacement, true, "ircd", "restart", "--instance", string(ircInstance), "--operation", string(domain.NewID("operation")))
	ensure(replacement.Alive && replacement.Instance != ircInstance, "actual_ircd_explicit_replacement")
	cli(nil, false, "ircd", "stop", "--instance", string(ircInstance))
	ircInstance = replacement.Instance
	replacementProof := managedProductOwnedProcess(t, state, ircInstance)
	processProofs = append(processProofs, replacementProof)
	supervisorPIDs = append(supervisorPIDs, replacementProof.SupervisorPID)
	cli(&same, true, "ircd", "status")
	ensure(same.Alive && same.Instance == replacement.Instance, "stale_stop_preserves_replacement")
	cli(&human, true, "ircd", "account", "alice")
	ensure(human == originalHuman, "human_private_account_identity_stable_after_ircd_replacement")
	verifyRoutes("ircd_restart")
	adaptersAfter = time.Now().UTC()
	ensure(managedProductWait(ctx, 20*time.Second, readyAdapters), "all_exact_stable_bot_nicks_reconnected_after_ircd_restart")
	connectPeers()
	thirdInbound := [3]string{bindings[0].SelfID + ": first thread after IRC restart", "@" + bindings[1].SelfID + " second thread after IRC restart", bindings[2].SelfID + ": other project after IRC restart"}
	for i, text := range thirdInbound {
		peerSend(projectIndex[i], text)
	}
	ensure(managedProductWait(ctx, 15*time.Second, func() bool {
		if !refreshHistory() {
			return false
		}
		for i, text := range thirdInbound {
			if managedProductCount(histories[i], text, "alice") != 1 {
				return false
			}
		}
		return true
	}), "all_exact_addressed_intake_after_ircd_restart")
	for i := range bindings {
		var repeated protocol.ChannelSendResponse
		cli(&repeated, true, sendArgs[i]...)
		ensure(repeated.Delivery == delivered[i], fmt.Sprintf("thread_%d_message_operation_after_ircd_restart_no_replay", i))
		for j := range bindings {
			if j != i {
				ensure(managedProductCount(histories[i], secondInbound[j], "alice") == 0 && managedProductCount(histories[i], thirdInbound[j], "alice") == 0, fmt.Sprintf("thread_%d_no_cross_recipient_intake_after_both_restarts_%d", i, j))
			}
		}
	}
	observeNoReplay()
	for _, b := range bindings {
		_, err := client.SyncChannel(ctx, b.ID)
		ensure(err == nil, "final_exact_recipient_sync")
	}
	ensure(refreshHistory(), "final_durable_history_available")
	for i, h := range histories {
		ensure(managedProductCount(h, broadcast, "alice") == 0 && managedProductCount(h, crossProject, "alice") == 0, fmt.Sprintf("thread_%d_final_no_broadcast_cross_project", i))
		for j := range bindings {
			want := 0
			if i == j {
				want = 1
			}
			ensure(managedProductCount(h, inbound[j], "alice") == want && managedProductCount(h, secondInbound[j], "alice") == want && managedProductCount(h, thirdInbound[j], "alice") == want, fmt.Sprintf("thread_%d_final_exact_recipient_%d_all_three_generations", i, j))
		}
		wantMention := 0
		if i == 0 {
			wantMention = 1
		}
		ensure(managedProductCount(h, bodyMention, "alice") == wantMention, fmt.Sprintf("thread_%d_final_exact_initial_body_mention", i))
	}
	report["ircd_instances"] = []domain.ID{started.Instance, replacement.Instance}
	stage = "native input isolation and exact fixture cleanup"
	var inputSizes []int
	for i, fixture := range fixtures {
		input, err := os.ReadFile(sentinels[i])
		observed, inspectErr := native.Inspect(ctx, fixture)
		ensure(err == nil && len(input) == 0 && inspectErr == nil && observed.Alive && observed.Ref == fixture && observed.WritableClients == 0, fmt.Sprintf("thread_%d_authored_inbox_never_native_input", i))
		inputSizes = append(inputSizes, len(input))
		cli(nil, true, "stop", string(sessions[i].Session.ID), "--operation", string(domain.NewID("op")))
		stopped, err := native.Inspect(ctx, fixture)
		ensure(err == nil && !stopped.Alive && stopped.Ref == fixture, fmt.Sprintf("actual_cli_exact_native_fixture_%d_stop", i))
	}
	report["native_input_bytes"] = inputSizes
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

type managedProductProcessProof struct {
	ExecutionID     domain.ID `json:"execution_id"`
	PID             int       `json:"execution_pid"`
	ProcessStart    string    `json:"execution_process_start"`
	SupervisorPID   int       `json:"supervisor_pid"`
	SupervisorStart string    `json:"supervisor_process_start"`
}

// Project only owned lifetime evidence for a read-only exit check; never publish
// the receipt's authentication token or send signals from persisted PID data.
func managedProductOwnedProcess(t *testing.T, state string, execution domain.ID) managedProductProcessProof {
	t.Helper()
	proof, e := managedProductReadOwnedProcess(state, execution)
	if e != nil {
		t.Fatal("owned native helper receipt unavailable")
	}
	return proof
}

func managedProductReadOwnedProcess(state string, execution domain.ID) (managedProductProcessProof, error) {
	sum := sha256.Sum256([]byte(execution))
	b, e := config.ReadPrivateSecret(filepath.Join(state, "native", hex.EncodeToString(sum[:])[:24]+".json"))
	var projected struct {
		PID   int               `json:"supervisor_pid"`
		Start string            `json:"supervisor_start"`
		Ref   domain.RuntimeRef `json:"ref"`
	}
	if e != nil || json.Unmarshal(b, &projected) != nil || projected.PID <= 0 || projected.Start == "" || projected.Ref.PID <= 0 || projected.Ref.ExecutionID != execution || projected.Ref.ProcessStart == "" {
		return managedProductProcessProof{}, errors.New("owned native helper receipt unavailable")
	}
	return managedProductProcessProof{ExecutionID: execution, PID: projected.Ref.PID, ProcessStart: projected.Ref.ProcessStart, SupervisorPID: projected.PID, SupervisorStart: projected.Start}, nil
}

func managedProductControlStart(t *testing.T, pid int) string {
	t.Helper()
	b, e := exec.Command("/bin/ps", "-p", fmt.Sprint(pid), "-o", "lstart=").Output()
	if e != nil || strings.TrimSpace(string(b)) == "" {
		t.Fatal("owned control process lifetime observation unavailable")
	}
	return strings.TrimSpace(string(b))
}

// Test intent is independent of a successful Launch response. A zero Runtime
// means the effect may exist but its exact private authority is unresolved.
type managedProductLaunchIntent struct {
	Request protocol.LaunchRequest
	Runtime domain.RuntimeRef
}

type managedProductLaunchDirectory interface {
	Reconcile(context.Context) error
	List(context.Context) ([]domain.Session, error)
}
type managedProductLaunchLookup interface {
	Lookup(context.Context, domain.ID, domain.ID) (bpruntime.Observation, error)
}

func managedProductNativeRef(ref domain.RuntimeRef) bool {
	return ref.Backend == "native" && domain.ValidObjectID(ref.ExecutionID, "execution") && (domain.ValidObjectID(ref.OperationID, "op") || domain.ValidObjectID(ref.OperationID, "operation")) && ref.Socket != "" && ref.Session != "" && ref.Pane == "" && ref.PID > 0 && ref.BootID != "" && ref.ProcessStart != ""
}
func managedProductLaunchAcknowledged(request protocol.LaunchRequest, response protocol.LaunchResponse) bool {
	s, op, ref := response.Session, response.Operation, response.Session.Execution.Runtime
	return managedProductNativeRef(ref) && ref.OperationID == request.OperationID && ref.ExecutionID == s.Execution.ID && domain.ValidObjectID(s.ID, "session") && s.Mode == domain.Managed && s.Execution.Generation == 1 && s.Name == request.Name && s.Workspace == request.Workspace && s.Execution.Provider == request.Provider && s.Execution.ProviderVersion == request.ProviderVersion && op.ID == request.OperationID && op.Kind == "launch" && op.SessionID == s.ID && op.ExecutionID == s.Execution.ID && op.Outcome == domain.Completed
}

// This performs only reconciliation, inventory and exact native Lookup. It
// never resubmits Launch, selects by display name alone, or guesses a process
// locator. Inventory locates the recorded operation; native Lookup authenticates
// the exact execution/operation before any recovered ref can be stopped.
func managedProductRecoverLaunches(ctx context.Context, directory managedProductLaunchDirectory, native managedProductLaunchLookup, intents []managedProductLaunchIntent) ([]domain.RuntimeRef, []domain.ID) {
	var pending []managedProductLaunchIntent
	for _, intent := range intents {
		if intent.Runtime.ExecutionID == "" {
			pending = append(pending, intent)
		}
	}
	if len(pending) == 0 {
		return nil, []domain.ID{}
	}
	unresolved := func() []domain.ID {
		var ids []domain.ID
		for _, intent := range pending {
			ids = append(ids, intent.Request.OperationID)
		}
		return ids
	}
	if directory == nil || native == nil || directory.Reconcile(ctx) != nil {
		return nil, unresolved()
	}
	listed, e := directory.List(ctx)
	if e != nil {
		return nil, unresolved()
	}
	var recovered []domain.RuntimeRef
	var missing []domain.ID
	for _, intent := range pending {
		var matches []domain.Session
		for _, s := range listed {
			if s.Execution.Runtime.OperationID == intent.Request.OperationID {
				matches = append(matches, s)
			}
		}
		if len(matches) != 1 {
			missing = append(missing, intent.Request.OperationID)
			continue
		}
		s := matches[0]
		ref := s.Execution.Runtime
		if !managedProductNativeRef(ref) || ref.ExecutionID != s.Execution.ID || !domain.ValidObjectID(s.ID, "session") || s.Mode != domain.Managed || s.Execution.Generation != 1 || s.Name != intent.Request.Name || s.Workspace != intent.Request.Workspace || s.Execution.Provider != intent.Request.Provider || s.Execution.ProviderVersion != intent.Request.ProviderVersion {
			missing = append(missing, intent.Request.OperationID)
			continue
		}
		proof, e := native.Lookup(ctx, s.Execution.ID, intent.Request.OperationID)
		if e != nil || proof.Ref != ref {
			missing = append(missing, intent.Request.OperationID)
			continue
		}
		recovered = append(recovered, proof.Ref)
	}
	return recovered, missing
}

// Provider-free failure regression: the real daemon/store handles Launch, but
// the HTTP connection closes after the effect is recorded and before any ACK.
// The injected native boundary never spawns a process or accepts terminal input.
func TestManagedProductLostLaunchACKCleanup(t *testing.T) {
	for _, failure := range []string{"recoverable", "control-unavailable", "supervisor-unavailable", "foreign-operation", "duplicate-operation", "drift-observation"} {
		t.Run(failure, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			st, e := store.Open(filepath.Join(t.TempDir(), "state", "registry.db"))
			if e != nil {
				t.Fatal(e)
			}
			defer st.Close()
			backend := &managedProductLaunchFixture{mode: failure}
			svc, e := daemon.New(st, backend, domain.NewID("host"))
			if e != nil {
				t.Fatal(e)
			}
			handler := svc.Handler()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/launch" {
					recorder := httptest.NewRecorder()
					handler.ServeHTTP(recorder, r)
					if recorder.Code != http.StatusOK {
						t.Error("owned fixture launch was not recorded")
					}
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error("owned ACK-loss fixture could not close transport")
						return
					}
					_ = conn.Close()
					return
				}
				if failure == "control-unavailable" {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				if r.URL.Path == "/v1/sessions" && (failure == "foreign-operation" || failure == "duplicate-operation") {
					recorder := httptest.NewRecorder()
					handler.ServeHTTP(recorder, r)
					var inventory protocol.Inventory
					if json.Unmarshal(recorder.Body.Bytes(), &inventory) != nil || len(inventory.Sessions) != 1 {
						t.Error("owned inventory fixture missing")
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					if failure == "foreign-operation" {
						inventory.Sessions[0].Execution.Runtime.OperationID = domain.NewID("op")
					} else {
						second := inventory.Sessions[0]
						second.ID = domain.NewID("session")
						inventory.Sessions = append(inventory.Sessions, second)
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(inventory)
					return
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			client := &protocol.Client{HTTP: server.Client(), BaseURL: server.URL}
			defer client.HTTP.CloseIdleConnections()
			request := protocol.LaunchRequest{OperationID: domain.NewID("op"), Name: "owned lost ACK sentinel", Workspace: t.TempDir(), Provider: "fixture", ProviderVersion: "owned-test", Argv: []string{"/owned-fixture/no-process"}}
			intents := []managedProductLaunchIntent{{Request: request}}
			ack, err := client.Launch(ctx, request)
			if err == nil || ack.Session.Execution.Runtime.ExecutionID != "" {
				t.Fatal("response acknowledgement was not genuinely lost")
			}
			refs, unresolved := managedProductRecoverLaunches(ctx, client, backend, intents)
			if failure == "recoverable" {
				if len(unresolved) != 0 || len(refs) != 1 || refs[0] != backend.ref {
					t.Fatal("exact owned launch did not reconcile")
				}
				if backend.Stop(ctx, refs[0]) != nil {
					t.Fatal("exact recovered cleanup fixture failed")
				}
			} else {
				if len(refs) != 0 || len(unresolved) != 1 || unresolved[0] != request.OperationID {
					t.Fatal("unknown launch was treated as absent or recoverable")
				}
			}
			backend.mu.Lock()
			defer backend.mu.Unlock()
			if backend.launches != 1 {
				t.Fatal("lost launch ACK was replayed")
			}
			if failure != "recoverable" && backend.stops != 0 {
				t.Fatal("unknown execution was stopped using unconfirmed evidence")
			}
			if (failure == "foreign-operation" || failure == "duplicate-operation" || failure == "control-unavailable") && backend.lookups != 0 {
				t.Fatal("ambiguous or foreign operation reached native lookup")
			}
			if intents[0].Runtime.ExecutionID != "" {
				t.Fatal("unacknowledged intent was retroactively marked as acknowledged")
			}
		})
	}
}

type managedProductLaunchFixture struct {
	mu                       sync.Mutex
	ref                      domain.RuntimeRef
	mode                     string
	launches, lookups, stops int
	stopped                  bool
}

func (f *managedProductLaunchFixture) Launch(_ context.Context, spec domain.LaunchSpec) (domain.RuntimeRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.launches++
	f.ref = domain.RuntimeRef{Backend: "native", Socket: "owned-test-authority", Session: "owned-test-instance", PID: 123, BootID: "owned-test-boot", ProcessStart: "owned-test-start", ExecutionID: spec.Session.Execution.ID, OperationID: spec.OperationID}
	return f.ref, nil
}
func (f *managedProductLaunchFixture) Lookup(_ context.Context, execution, operation domain.ID) (bpruntime.Observation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups++
	if f.mode == "supervisor-unavailable" || execution != f.ref.ExecutionID || operation != f.ref.OperationID {
		return bpruntime.Observation{}, errors.New("exact owned fixture authority unavailable")
	}
	ref := f.ref
	if f.mode == "drift-observation" {
		ref.ProcessStart = "foreign-test-lifetime"
	}
	return bpruntime.Observation{Ref: ref, Alive: !f.stopped}, nil
}
func (f *managedProductLaunchFixture) Inspect(_ context.Context, ref domain.RuntimeRef) (bpruntime.Observation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ref != f.ref {
		return bpruntime.Observation{}, errors.New("different owned fixture ref")
	}
	return bpruntime.Observation{Ref: f.ref, Alive: !f.stopped}, nil
}
func (f *managedProductLaunchFixture) Stop(_ context.Context, ref domain.RuntimeRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ref != f.ref {
		return errors.New("different owned fixture stop ref")
	}
	f.stops++
	f.stopped = true
	return nil
}
func (f *managedProductLaunchFixture) AttachCommand(domain.RuntimeRef, bool) ([]string, error) {
	return nil, errors.New("input/attachment is outside this provider-free fixture")
}

var managedProductBinaryNames = []string{"keelmux", "keelmuxd", "keelmux-supervisor"}

const managedProductBinaryBound = 256 << 20

// Open the explicit source directory once, then each fixed basename relative
// to that fd without following symlinks. Copy only owned regular executables;
// never modify the originals or discover executables through the system PATH.
func managedProductFreezePackageBinaries(ctx context.Context, supplied, destination string) (map[string]string, error) {
	if !filepath.IsAbs(supplied) || filepath.Clean(supplied) != supplied || strings.ContainsAny(supplied, "\x00\r\n") {
		return nil, errors.New("explicit clean absolute package directory required")
	}
	fd, e := unix.Open(supplied, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, errors.New("owned package directory unavailable")
	}
	directory := os.NewFile(uintptr(fd), "owned supplied package directory")
	defer directory.Close()
	var directoryInfo unix.Stat_t
	if unix.Fstat(fd, &directoryInfo) != nil || directoryInfo.Uid != uint32(os.Geteuid()) || directoryInfo.Mode&0022 != 0 {
		return nil, errors.New("package directory authority invalid")
	}
	var created []string
	done := false
	defer func() {
		if !done {
			for _, path := range created {
				_ = os.Remove(path)
			}
		}
	}()
	digests := map[string]string{}
	for _, name := range managedProductBinaryNames {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		fileFD, e := unix.Openat(fd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
		if e != nil {
			return nil, errors.New("explicit package executable unavailable")
		}
		input := os.NewFile(uintptr(fileFD), "owned package executable")
		sha, e := managedProductFreezeExecutable(ctx, input, destination, name)
		if e != nil {
			_ = input.Close()
			return nil, e
		}
		path := filepath.Join(destination, name)
		created = append(created, path)
		// Refuse a source pathname replaced while copying, even when the
		// original open descriptor still refers to valid owned bytes.
		againFD, e := unix.Openat(fd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
		if e != nil {
			_ = input.Close()
			return nil, errors.New("package executable changed during freeze")
		}
		again := os.NewFile(uintptr(againFD), "owned package executable confirmation")
		firstInfo, firstErr := input.Stat()
		secondInfo, secondErr := again.Stat()
		_ = input.Close()
		_ = again.Close()
		if firstErr != nil || secondErr != nil || !os.SameFile(firstInfo, secondInfo) {
			return nil, errors.New("package executable changed during freeze")
		}
		digests[name] = sha
	}
	done = true
	return digests, nil
}

func managedProductFreezeExecutable(ctx context.Context, input *os.File, destination, name string) (string, error) {
	before, e := input.Stat()
	owner, ok := func() (*syscall.Stat_t, bool) {
		if before == nil {
			return nil, false
		}
		v, ok := before.Sys().(*syscall.Stat_t)
		return v, ok
	}()
	if e != nil || !ok || owner.Uid != uint32(os.Geteuid()) || !before.Mode().IsRegular() || before.Mode().Perm()&0100 == 0 || before.Mode().Perm()&0022 != 0 || before.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || before.Size() <= 0 || before.Size() > managedProductBinaryBound {
		return "", errors.New("owned regular bounded executable required")
	}
	outputPath := filepath.Join(destination, name)
	output, e := os.OpenFile(outputPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0500)
	if e != nil {
		return "", errors.New("private executable destination unavailable")
	}
	done := false
	defer func() {
		_ = output.Close()
		if !done {
			_ = os.Remove(outputPath)
		}
	}()
	hash := sha256.New()
	n, e := io.Copy(io.MultiWriter(output, hash), io.LimitReader(&managedProductContextReader{ctx: ctx, input: input}, managedProductBinaryBound+1))
	if e != nil || n != before.Size() || output.Sync() != nil || output.Close() != nil {
		return "", errors.New("bounded executable freeze failed")
	}
	after, e := input.Stat()
	if e != nil || after.Size() != before.Size() || after.Mode() != before.Mode() || !after.ModTime().Equal(before.ModTime()) {
		return "", errors.New("package executable changed during freeze")
	}
	if _, e = input.Seek(0, io.SeekStart); e != nil {
		return "", errors.New("package executable reread unavailable")
	}
	recheck := sha256.New()
	if n, e = io.Copy(recheck, io.LimitReader(&managedProductContextReader{ctx: ctx, input: input}, managedProductBinaryBound+1)); e != nil || n != before.Size() || !bytes.Equal(hash.Sum(nil), recheck.Sum(nil)) {
		return "", errors.New("package executable bytes changed during freeze")
	}
	done = true
	return hex.EncodeToString(hash.Sum(nil)), nil
}

type managedProductContextReader struct {
	ctx   context.Context
	input io.Reader
}

func (r *managedProductContextReader) Read(p []byte) (int, error) {
	if r.ctx.Err() != nil {
		return 0, r.ctx.Err()
	}
	return r.input.Read(p)
}

type managedProductBoundedOutput struct{ bytes.Buffer }

func (b *managedProductBoundedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 4096 {
		return 0, errors.New("version output exceeds finite bound")
	}
	return b.Buffer.Write(p)
}

var managedProductVersionPattern = regexp.MustCompile(`^Keelmux ([A-Za-z0-9][A-Za-z0-9._+\-]{0,63}) \(commit ([a-f0-9]{40}), protocol bp/1\)\n?$`)

func managedProductVerifyExecutableVersions(ctx context.Context, bin string, env []string, source string) (string, error) {
	if len(source) != 40 {
		return "", errors.New("full source commit required")
	}
	var version string
	for _, name := range managedProductBinaryNames {
		probe, cancel := context.WithTimeout(ctx, 3*time.Second)
		command := exec.CommandContext(probe, filepath.Join(bin, name), "--version")
		command.Env = env
		var stdout, stderr managedProductBoundedOutput
		command.Stdout, command.Stderr = &stdout, &stderr
		e := command.Run()
		cancel()
		parts := managedProductVersionPattern.FindStringSubmatch(stdout.String())
		if e != nil || stderr.Len() != 0 || len(parts) != 3 || parts[2] != source || version != "" && version != parts[1] {
			return "", errors.New("executable version tuple does not match frozen source")
		}
		version = parts[1]
	}
	return version, nil
}

func TestManagedProductPackageFreezeAndProvenance(t *testing.T) {
	const source = "0123456789abcdef0123456789abcdef01234567"
	for _, refusal := range []string{"valid", "relative-directory", "symlink-directory", "missing-executable", "symlink-executable", "directory-executable", "non-executable", "writable-executable", "oversized-executable", "wrong-commit", "mixed-version", "malformed-version", "trailing-version", "nonzero-version", "bounded-output", "bounded-version-time"} {
		t.Run(refusal, func(t *testing.T) {
			original, frozen, private := t.TempDir(), t.TempDir(), t.TempDir()
			versionLine := func(version, commit string) string {
				return "Keelmux " + version + " (commit " + commit + ", protocol bp/1)"
			}
			var before = map[string][]byte{}
			for i, name := range managedProductBinaryNames {
				line, suffix := versionLine("v0.1.0", source), ""
				if i == 2 {
					switch refusal {
					case "wrong-commit":
						line = versionLine("v0.1.0", strings.Repeat("a", 40))
					case "mixed-version":
						line = versionLine("v0.2.0", source)
					case "malformed-version":
						line = "unstructured untrusted provenance"
					case "trailing-version":
						line += "\nextra metadata"
					case "nonzero-version":
						suffix = "exit 17\n"
					case "bounded-output":
						line = strings.Repeat("v", 5000)
					case "bounded-version-time":
						suffix = "while :; do :; done\n"
					}
				}
				// This provider-free executable only serves --version. No runtime,
				// native process, discovery, or caller-installed file is touched.
				data := []byte("#!/bin/sh\n[ \"$1\" = --version ] || exit 91\nprintf '%s\\n' '" + line + "'\n" + suffix)
				path := filepath.Join(original, name)
				if os.WriteFile(path, data, 0700) != nil {
					t.Fatal("owned executable fixture")
				}
				before[name] = data
			}
			// Extra caller files are neither copied nor discovered on PATH.
			if os.WriteFile(filepath.Join(original, "tmux"), []byte("not an executable"), 0600) != nil {
				t.Fatal("extra owned caller fixture")
			}
			input := original
			path := filepath.Join(original, "keelmux-supervisor")
			expectsFreeze := true
			switch refusal {
			case "relative-directory":
				input, expectsFreeze = "relative/bin", false
			case "symlink-directory":
				input, expectsFreeze = filepath.Join(private, "alias"), false
				if os.Symlink(original, input) != nil {
					t.Fatal("owned directory symlink fixture")
				}
			case "missing-executable":
				expectsFreeze = false
				if os.Remove(path) != nil {
					t.Fatal("owned missing file fixture")
				}
			case "symlink-executable":
				expectsFreeze = false
				if os.Remove(path) != nil || os.Symlink(filepath.Join(original, "keelmux"), path) != nil {
					t.Fatal("owned file symlink fixture")
				}
			case "directory-executable":
				expectsFreeze = false
				if os.Remove(path) != nil || os.Mkdir(path, 0700) != nil {
					t.Fatal("owned directory file fixture")
				}
			case "non-executable":
				expectsFreeze = false
				if os.Chmod(path, 0600) != nil {
					t.Fatal("owned non-executable fixture")
				}
			case "writable-executable":
				expectsFreeze = false
				if os.Chmod(path, 0777) != nil {
					t.Fatal("owned writable fixture")
				}
			case "oversized-executable":
				expectsFreeze = false
				if os.Truncate(path, managedProductBinaryBound+1) != nil {
					t.Fatal("owned oversized fixture")
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			digests, err := managedProductFreezePackageBinaries(ctx, input, frozen)
			if !expectsFreeze {
				if err == nil {
					t.Fatal("malformed caller tuple was frozen")
				}
				entries, _ := os.ReadDir(frozen)
				if len(entries) != 0 {
					t.Fatal("refused tuple left partial frozen executable authority")
				}
				return
			}
			if err != nil || len(digests) != 3 {
				t.Fatal("valid owned tuple did not freeze", err)
			}
			for _, name := range managedProductBinaryNames {
				actual, err := os.ReadFile(filepath.Join(frozen, name))
				originalBytes, originalErr := os.ReadFile(filepath.Join(original, name))
				sum := sha256.Sum256(actual)
				info, infoErr := os.Lstat(filepath.Join(frozen, name))
				if err != nil || originalErr != nil || infoErr != nil || info.Mode().Perm() != 0500 || !bytes.Equal(actual, before[name]) || !bytes.Equal(originalBytes, before[name]) || digests[name] != hex.EncodeToString(sum[:]) {
					t.Fatal("frozen bytes/digest or caller originals changed")
				}
			}
			if !managedProductMissing(filepath.Join(frozen, "tmux")) {
				t.Fatal("caller/system executable discovery escaped the explicit tuple")
			}
			env := []string{"PATH=" + frozen, "HOME=" + private, "TMPDIR=" + private, "LANG=C.UTF-8"}
			version, err := managedProductVerifyExecutableVersions(ctx, frozen, env, source)
			if refusal == "valid" {
				if err != nil || version != "v0.1.0" {
					t.Fatal("matching actual versions rejected", err)
				}
			} else if err == nil {
				t.Fatal("mismatched or malformed executable provenance accepted")
			}
		})
	}
}
