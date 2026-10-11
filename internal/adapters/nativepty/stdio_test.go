package nativepty

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"github.com/seanheiney/keelmux/internal/domain"
	ps "github.com/seanheiney/keelmux/internal/primarystdio"
	"golang.org/x/sys/unix"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func testStdio(t *testing.T, mode string) (*Backend, ps.LaunchSpec) {
	t.Helper()
	oldAvailable, oldVerifier := stdioImageAvailable, stdioImageVerifier
	stdioImageAvailable = func() bool { return true }
	stdioImageVerifier = verifyStdioTestImage
	t.Cleanup(func() { stdioImageAvailable = oldAvailable; stdioImageVerifier = oldVerifier })
	base, err := os.MkdirTemp("/tmp", "km-s-")
	if err != nil {
		t.Fatal(err)
	}
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	dirs := []string{}
	for _, name := range []string{"state", "home", "codex", "config", "cache", "data", "tmp", "workspace"} {
		p := filepath.Join(base, name)
		if err = os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, p)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	digest, err := stdioFileDigest(self)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewWithOptions(dirs[0], Options{HelperPath: self})
	if err != nil {
		t.Fatal(err)
	}
	binding := ps.Binding{HostID: domain.NewID("host"), SessionID: domain.NewID("session"), ExecutionID: domain.NewID("execution"), Generation: 1, StartOperationID: domain.NewID("start"), PrimaryIncarnation: domain.ID(nonce()[:32]), NativeThreadID: "fixture-thread", RealmRevision: "fixture-realm"}
	spec := ps.LaunchSpec{Binding: binding, Workspace: dirs[7], Binary: self, BinarySHA256: digest, SchemaSHA256: ps.CodecSchemaSHA256, Profile: ps.Codex0159, Environment: ps.CleanEnvironment{Home: dirs[1], CodexHome: dirs[2], XDGConfigHome: dirs[3], XDGCacheHome: dirs[4], XDGDataHome: dirs[5], TempDir: dirs[6], Path: "/usr/bin:/bin", Locale: "C"}}
	data, _ := json.Marshal(stdioFixtureConfig{Mode: mode, Thread: binding.NativeThreadID, Workspace: spec.Workspace})
	if err = os.WriteFile(filepath.Join(spec.Environment.Home, "fixture.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return b, spec
}
func launchStdioFixture(t *testing.T, b *Backend, spec ps.LaunchSpec) ps.ChannelRef {
	t.Helper()
	ref, err := b.LaunchStdio(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Fixture-owned emergency cleanup, never claimed as product stop success.
		if e := os.WriteFile(filepath.Join(spec.Environment.Home, "exit"), []byte("yes"), 0600); e != nil {
			t.Error(e)
			return
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			r, e := b.readReceipt(ref.Runtime.ExecutionID)
			if e == nil && (r.Stage == "exited" || r.Stage == "stopped" || r.Stage == "failed") {
				if r.SupervisorPID > 0 {
					_, e = processStart(r.SupervisorPID)
					if e == nil && time.Now().Before(deadline) {
						time.Sleep(5 * time.Millisecond)
						continue
					}
				}
				return
			}
			if time.Now().After(deadline) {
				t.Error("fixture child/helper cleanup not confirmed")
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
	return ref
}
func waitStdioFile(t *testing.T, p string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, e := os.Stat(p); e == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture marker unavailable")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func stdioMethods(t *testing.T, spec ps.LaunchSpec) []string {
	t.Helper()
	raw, e := os.ReadFile(filepath.Join(spec.Environment.Home, "methods.jsonl"))
	if e != nil {
		t.Fatal(e)
	}
	out := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var name string
		if json.Unmarshal([]byte(line), &name) != nil {
			t.Fatal("invalid fixture audit")
		}
		out = append(out, name)
	}
	return out
}
func ackStdio(t *testing.T, b *Backend, ref ps.ChannelRef, o ps.Observation) {
	t.Helper()
	if e := b.AckStdioImported(context.Background(), ref, ps.ImportAck{From: o.From, Through: o.Through, BatchSHA256: o.BatchSHA256}); e != nil {
		t.Fatal(e)
	}
}
func readyStdio(t *testing.T, b *Backend, ref ps.ChannelRef) ps.Observation {
	t.Helper()
	o, e := b.BootstrapStdioOnce(context.Background(), ref)
	if e != nil || o.Snapshot.Bootstrap != ps.BootstrapComplete {
		t.Fatalf("bootstrap %+v %v", o.Snapshot, e)
	}
	ackStdio(t, b, ref, o)
	o, e = b.ObserveStdio(context.Background(), ref, o.Through)
	if e != nil || !o.Snapshot.Idle || !o.Snapshot.ExactLoadedThread || !o.Snapshot.EmptyQueue || !o.Snapshot.EmptyBackground {
		t.Fatalf("inspect %+v %v", o.Snapshot, e)
	}
	ackStdio(t, b, ref, o)
	return o
}

func TestStdioLifecycleCleanEnvironmentAndNoReplay(t *testing.T) {
	b, spec := testStdio(t, "")
	t.Setenv("API_KEY", "fixture-ambient-key")
	t.Setenv("HTTPS_PROXY", "fixture-ambient-proxy")
	t.Setenv("BP_LINK_TOKEN", "fixture-ambient-link")
	t.Setenv("TERM", "fixture-ambient-term")
	ref := launchStdioFixture(t, b, spec)
	o := readyStdio(t, b, ref)
	envRaw, e := os.ReadFile(filepath.Join(spec.Environment.Home, "environment.json"))
	if e != nil {
		t.Fatal(e)
	}
	var env []string
	if json.Unmarshal(envRaw, &env) != nil {
		t.Fatal("env")
	}
	if strings.Join(env, "\n") != strings.Join(stdioEnvironment(spec.Environment), "\n") {
		t.Fatalf("ambient environment forwarded: %q", env)
	}
	again, e := b.LaunchStdio(context.Background(), spec)
	if e != nil || again != ref {
		t.Fatal("launch replay identity", e)
	}
	before := len(stdioMethods(t, spec))
	if _, e = b.BootstrapStdioOnce(context.Background(), ref); e != nil {
		t.Fatal(e)
	}
	if len(stdioMethods(t, spec)) != before {
		t.Fatal("bootstrap replayed")
	}
	changed := spec
	changed.Binding.Generation++
	if _, e = b.LaunchStdio(context.Background(), changed); e == nil {
		t.Fatal("changed generation admitted")
	}
	if e = b.Stop(context.Background(), ref.Runtime); e == nil {
		t.Fatal("generic stop bypass")
	}
	if _, e = b.Open(context.Background(), ref.Runtime, true, 24, 80); e == nil {
		t.Fatal("stdio attached")
	}
	calls := 0
	q := ps.Quiescence{Cursor: o.Snapshot.Cursor, SnapshotSHA256: o.Snapshot.SnapshotSHA256}
	if e = b.WithStdioQuiescence(context.Background(), ref, q, func(context.Context) error { calls++; return nil }); e != nil || calls != 1 {
		t.Fatal("guard", calls, e)
	}
	stop := ps.StopRequest{OperationID: domain.NewID("stop"), Quiescence: q}
	if e = b.StopStdio(context.Background(), ref, stop); e != nil {
		t.Fatal("typed stop", e)
	}
	if e = b.StopStdio(context.Background(), ref, stop); e != nil {
		t.Fatal("stop replay", e)
	}
	final, e := b.RetainedStdio(context.Background(), ref, o.Through)
	if e != nil || final.Snapshot.ProcessAlive || final.Snapshot.Idle {
		t.Fatal("terminal proof", final.Snapshot, e)
	}
	if len(stdioMethods(t, spec)) != 7 {
		t.Fatalf("unexpected native operations %v", stdioMethods(t, spec))
	}
}
func TestStdioDisconnectCannotCompleteOrReplayBootstrap(t *testing.T) {
	b, spec := testStdio(t, "pause_init")
	ref := launchStdioFixture(t, b, spec)
	c, r, e := b.stdioConnect(context.Background(), ref)
	if e != nil {
		t.Fatal(e)
	}
	req := stdioRequest{Op: "stdio_bootstrap", Version: ps.Version, Auth: r.Auth, Ref: r.Ref, Channel: &ref}
	if e = writeMessage(c, req); e != nil {
		t.Fatal(e)
	}
	waitStdioFile(t, filepath.Join(spec.Environment.Home, "init-received"))
	_ = c.Close()
	if e = os.WriteFile(filepath.Join(spec.Environment.Home, "release-init"), []byte("yes"), 0600); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		o, e := b.RetainedStdio(context.Background(), ref, ps.Genesis(ref.ChannelIncarnation))
		if e == nil && o.Snapshot.Bootstrap == ps.BootstrapUncertain {
			break
		}
		if time.Now().After(deadline) {
			r, receiptErr := b.readReceipt(ref.Runtime.ExecutionID)
			state, stateErr := b.readStdio(r)
			t.Fatalf("disconnect attempt not uncertain: observed_bootstrap=%s observed_continuity=%s observed_pending=%v observed_seq=%d durable_bootstrap=%s durable_continuity=%s durable_pending=%v durable_seq=%d methods=%v retained_error=%v receipt_error=%v state_error=%v", o.Snapshot.Bootstrap, o.Snapshot.Continuity, o.Snapshot.Pending, o.Snapshot.Cursor.Seq, state.Bootstrap, state.Continuity, state.Pending, state.Cursor.Seq, stdioMethods(t, spec), e, receiptErr, stateErr)
		}
		time.Sleep(5 * time.Millisecond)
	}
	_, _ = b.BootstrapStdioOnce(context.Background(), ref)
	if got := stdioMethods(t, spec); len(got) != 1 || got[0] != "initialize" {
		t.Fatal("unsent steps replayed", got)
	}
}

func TestStdioBootstrapAttemptInvalidatesUnsentSnapshot(t *testing.T) {
	b, spec := testStdio(t, "pause_init")
	ref := launchStdioFixture(t, b, spec)
	from := ps.Genesis(ref.ChannelIncarnation)
	first, e := b.RetainedStdio(context.Background(), ref, from)
	if e != nil || first.Snapshot.Bootstrap != ps.BootstrapUnsent {
		t.Fatal("initial unsent snapshot unavailable", e)
	}
	c, r, e := b.stdioConnect(context.Background(), ref)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	if e = writeStdioMessage(c, stdioRequest{Op: "stdio_bootstrap", Version: ps.Version, Auth: r.Auth, Ref: r.Ref, Channel: &ref}); e != nil {
		t.Fatal(e)
	}
	waitStdioFile(t, filepath.Join(spec.Environment.Home, "init-received"))
	during, e := b.RetainedStdio(context.Background(), ref, from)
	if e != nil || during.Through != from || during.Snapshot.Bootstrap != ps.BootstrapAttempted {
		t.Fatalf("sent initialize reused unsent snapshot: bootstrap=%s seq=%d error=%v", during.Snapshot.Bootstrap, during.Through.Seq, e)
	}
	if e = os.WriteFile(filepath.Join(spec.Environment.Home, "release-init"), []byte("yes"), 0600); e != nil {
		t.Fatal(e)
	}
	var reply stdioResponse
	if e = readStdioMessage(c, &reply); e != nil || reply.Error != "" || reply.Observation == nil || reply.Observation.Snapshot.Bootstrap != ps.BootstrapComplete {
		t.Fatal("bootstrap final reply unavailable", e, reply.Error)
	}
	if len(stdioMethods(t, spec)) != 3 {
		t.Fatal("unexpected bootstrap methods")
	}
}
func TestStdioGrantlessCallbackStickyAfterAckAndReconnect(t *testing.T) {
	b, spec := testStdio(t, "callback_init")
	ref := launchStdioFixture(t, b, spec)
	o, _ := b.BootstrapStdioOnce(context.Background(), ref)
	if !o.Snapshot.Pending {
		t.Fatal("callback not pending")
	}
	found := false
	for _, event := range o.Events {
		if event.Kind == ps.EventCallback && event.RequestID.Kind == "int64" && event.RequestID.Text == "77" {
			found = true
		}
	}
	if !found {
		t.Fatal("typed callback absent")
	}
	ackStdio(t, b, ref, o)
	recovered, e := b.RetainedStdio(context.Background(), ref, o.Through)
	if e != nil || !recovered.Snapshot.Pending {
		t.Fatal("ack cleared pending", e)
	}
	if _, e = b.ObserveStdio(context.Background(), ref, o.Through); e != nil {
		t.Fatal(e)
	}
	if len(stdioMethods(t, spec)) != 1 {
		t.Fatal("pending channel wrote new query/reply")
	}
	calls := 0
	if b.WithStdioQuiescence(context.Background(), ref, ps.Quiescence{Cursor: recovered.Through, SnapshotSHA256: recovered.Snapshot.SnapshotSHA256}, func(context.Context) error { calls++; return nil }) == nil || calls != 0 {
		t.Fatal("pending guard admitted")
	}
}
func TestStdioGuardOrdersReceiveAndExpiry(t *testing.T) {
	b, spec := testStdio(t, "")
	ref := launchStdioFixture(t, b, spec)
	o := readyStdio(t, b, ref)
	q := ps.Quiescence{Cursor: o.Snapshot.Cursor, SnapshotSHA256: o.Snapshot.SnapshotSHA256}
	err := b.WithStdioQuiescence(context.Background(), ref, q, func(context.Context) error {
		if e := os.WriteFile(filepath.Join(spec.Environment.Home, "callback"), []byte("yes"), 0600); e != nil {
			return e
		}
		waitStdioFile(t, filepath.Join(spec.Environment.Home, "callback-emitted"))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		pending, e := b.RetainedStdio(context.Background(), ref, o.Through)
		if e == nil && pending.Snapshot.Pending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("guard released without retaining callback", e)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if b.StopStdio(context.Background(), ref, ps.StopRequest{OperationID: domain.NewID("stop"), Quiescence: q}) == nil {
		t.Fatal("stale pre-callback stop")
	}
}
func TestStdioLostAckCanAdvanceExactRetainedPrefix(t *testing.T) {
	b, spec := testStdio(t, "")
	ref := launchStdioFixture(t, b, spec)
	o, e := b.BootstrapStdioOnce(context.Background(), ref)
	if e != nil {
		t.Fatal(e)
	}
	// Common-store import committed but the entire old ack was lost/undelivered.
	next, e := b.ObserveStdio(context.Background(), ref, o.Through)
	if e != nil {
		t.Fatal(e)
	}
	ackStdio(t, b, ref, next)
	forged := ps.ImportAck{From: next.From, Through: next.Through, BatchSHA256: strings.Repeat("f", 64)}
	if b.AckStdioImported(context.Background(), ref, forged) == nil {
		t.Fatal("fabricated ack accepted")
	}
}
func TestStdioMalformedFramesBecomeDurableBlockers(t *testing.T) {
	for _, mode := range []string{"malformed", "unknown_id", "oversize", "partial", "flood", "pagination"} {
		t.Run(mode, func(t *testing.T) {
			b, spec := testStdio(t, mode)
			ref := launchStdioFixture(t, b, spec)
			o, _ := b.BootstrapStdioOnce(context.Background(), ref)
			from := ps.Genesis(ref.ChannelIncarnation)
			if mode == "pagination" {
				ackStdio(t, b, ref, o)
				from = o.Through
				bad, observeErr := b.ObserveStdio(context.Background(), ref, o.Through)
				if observeErr == nil && !bad.Snapshot.Pending {
					t.Fatalf("pagination accepted: methods=%v bootstrap=%s continuity=%s pending=%v", stdioMethods(t, spec), bad.Snapshot.Bootstrap, bad.Snapshot.Continuity, bad.Snapshot.Pending)
				}
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				o, e := b.RetainedStdio(context.Background(), ref, from)
				if e == nil && o.Snapshot.Pending {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("adverse frame did not persist blocker", e)
				}
				time.Sleep(5 * time.Millisecond)
			}
			if len(stdioMethods(t, spec)) > 4 {
				t.Fatal("adverse channel continued bootstrap/queries")
			}
		})
	}
}
func TestStdioStrictWireAndUnsupportedPreflight(t *testing.T) {
	for _, body := range []string{`{"op":"stdio_ack","op":"stdio_ack"}`, `{"op":"stdio_ack","raw":{"method":"turn/start"}}`} {
		var buf strings.Builder
		var prefix [4]byte
		binary.BigEndian.PutUint32(prefix[:], uint32(len(body)))
		buf.Write(prefix[:])
		buf.WriteString(body)
		var request stdioRequest
		if readStdioMessage(strings.NewReader(buf.String()), &request) == nil {
			t.Fatal("unsafe union accepted")
		}
	}
	b, spec := testStdio(t, "")
	stdioImageAvailable = func() bool { return false }
	if _, e := b.LaunchStdio(context.Background(), spec); !errors.Is(e, ps.ErrUnavailable) {
		t.Fatal(e)
	}
	entries, e := os.ReadDir(b.state)
	if e != nil || len(entries) != 0 {
		t.Fatal("unsupported platform created state", e)
	}
	if runtime.GOOS == "darwin" && stdioPlatformImageAvailable() {
		t.Fatal("Darwin image qualification enabled")
	}
}

func TestStdioGuardDeadlineAndRepeatedConcurrentBootstrap(t *testing.T) {
	b, spec := testStdio(t, "")
	ref := launchStdioFixture(t, b, spec)
	errors := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, e := b.BootstrapStdioOnce(context.Background(), ref); errors <- e }()
	}
	for i := 0; i < 2; i++ {
		if e := <-errors; e != nil {
			t.Fatal(e)
		}
	}
	if got := stdioMethods(t, spec); len(got) != 3 {
		t.Fatal("concurrent bootstrap replay", got)
	}
	o, e := b.RetainedStdio(context.Background(), ref, ps.Genesis(ref.ChannelIncarnation))
	if e != nil {
		t.Fatal(e)
	}
	ackStdio(t, b, ref, o)
	o, e = b.ObserveStdio(context.Background(), ref, o.Through)
	if e != nil {
		t.Fatal(e)
	}
	ackStdio(t, b, ref, o)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	called := 0
	e = b.WithStdioQuiescence(ctx, ref, ps.Quiescence{Cursor: o.Through, SnapshotSHA256: o.Snapshot.SnapshotSHA256}, func(inner context.Context) error { called++; <-inner.Done(); return nil })
	if !errorsIsUncertain(e) || called != 1 {
		t.Fatal("expired guard success", e, called)
	}
	after, e := b.RetainedStdio(context.Background(), ref, o.Through)
	if e != nil || after.Snapshot.Pending || after.Snapshot.Bootstrap != ps.BootstrapComplete {
		t.Fatal("IPC expiry destroyed channel", e)
	}
	if _, e = b.BootstrapStdioOnce(context.Background(), ref); e != nil {
		t.Fatal(e)
	}
	if len(stdioMethods(t, spec)) != 7 {
		t.Fatal("expiry caused native replay")
	}
}
func errorsIsUncertain(e error) bool { return errors.Is(e, ps.ErrUncertain) }

func TestStdioSpoolReplacementRefusesWithoutRepair(t *testing.T) {
	for _, kind := range []string{"corrupt", "symlink", "public", "deleted", "replaced"} {
		t.Run(kind, func(t *testing.T) {
			b, spec := testStdio(t, "")
			ref := launchStdioFixture(t, b, spec)
			o := readyStdio(t, b, ref)
			path := b.stdioPath(ref.Runtime.ExecutionID)
			original, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "corrupt":
				if e = os.WriteFile(path, []byte("retained-corruption"), 0600); e != nil {
					t.Fatal(e)
				}
			case "symlink":
				if e = os.Rename(path, path+".original"); e != nil {
					t.Fatal(e)
				}
				if e = os.Symlink(path+".original", path); e != nil {
					t.Fatal(e)
				}
			case "public":
				if e = os.Chmod(path, 0644); e != nil {
					t.Fatal(e)
				}
			case "deleted":
				if e = os.Remove(path); e != nil {
					t.Fatal(e)
				}
			case "replaced":
				if e = os.Rename(path, path+".original"); e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(path, original, 0600); e != nil {
					t.Fatal(e)
				}
			}
			// No read/observation first: cached healthy evidence must not grant
			// publication after its durable backing has changed or disappeared.
			called := false
			if b.WithStdioQuiescence(context.Background(), ref, ps.Quiescence{Cursor: o.Through, SnapshotSHA256: o.Snapshot.SnapshotSHA256}, func(context.Context) error { called = true; return nil }) == nil || called {
				t.Fatal("direct guard accepted changed spool")
			}
			if _, e = b.RetainedStdio(context.Background(), ref, o.Through); !errors.Is(e, ps.ErrStorage) {
				t.Fatal("unsafe spool accepted", e)
			}
			if kind == "corrupt" {
				now, _ := os.ReadFile(path)
				if string(now) != "retained-corruption" {
					t.Fatal("corrupt spool repaired")
				}
			}
			if kind == "symlink" {
				now, _ := os.ReadFile(path + ".original")
				if string(now) != string(original) {
					t.Fatal("alias target mutated")
				}
				if info, _ := os.Lstat(path); info.Mode()&os.ModeSymlink == 0 {
					t.Fatal("alias replaced")
				}
			}
			if kind == "deleted" {
				if _, e = os.Lstat(path); !os.IsNotExist(e) {
					t.Fatal("missing spool recreated", e)
				}
			}
			if b.StopStdio(context.Background(), ref, ps.StopRequest{OperationID: domain.NewID("stop"), Quiescence: ps.Quiescence{Cursor: o.Through, SnapshotSHA256: o.Snapshot.SnapshotSHA256}}) == nil {
				t.Fatal("storage fault stop admitted")
			}
		})
	}
}

func TestStdioProductionUnsupportedImageHasNoLaunchEffects(t *testing.T) {
	b, spec := testStdio(t, "")
	stdioImageAvailable = stdioPlatformImageAvailable
	stdioImageVerifier = verifyStdioRunningImage
	b.helper = helperPath
	if stdioPlatformImageAvailable() {
		t.Skip("normal-helper Linux runtime qualification is a separate owned Go fixture gate")
	}
	if e := b.CheckStdioReady(); !errors.Is(e, ps.ErrUnavailable) {
		t.Fatal("unsupported constructor preflight accepted", e)
	}
	if _, e := b.LaunchStdio(context.Background(), spec); !errors.Is(e, ps.ErrUnavailable) {
		t.Fatal(e)
	}
	entries, e := os.ReadDir(b.state)
	if e != nil || len(entries) != 0 {
		t.Fatal("production unsupported preflight spawned or persisted", e)
	}
}

func TestStdioGuardStorageLossDuringCommitIsUncertain(t *testing.T) {
	b, spec := testStdio(t, "")
	ref := launchStdioFixture(t, b, spec)
	o := readyStdio(t, b, ref)
	called := 0
	err := b.WithStdioQuiescence(context.Background(), ref, ps.Quiescence{Cursor: o.Through, SnapshotSHA256: o.Snapshot.SnapshotSHA256}, func(context.Context) error {
		called++
		return os.WriteFile(b.stdioPath(ref.Runtime.ExecutionID), []byte("loss-during-local-commit"), 0600)
	})
	if !errors.Is(err, ps.ErrUncertain) || called != 1 {
		t.Fatal("lost durable evidence acknowledged", err, called)
	}
	raw, e := os.ReadFile(b.stdioPath(ref.Runtime.ExecutionID))
	if e != nil || string(raw) != "loss-during-local-commit" {
		t.Fatal("guard repaired corrupt evidence", e)
	}
}

func TestStdioWireWritePreservesShortDeadline(t *testing.T) {
	writer, reader := net.Pipe()
	defer writer.Close()
	defer reader.Close()
	deadline := time.Now().Add(25 * time.Millisecond)
	if e := writer.SetDeadline(deadline); e != nil {
		t.Fatal(e)
	}
	start := time.Now()
	if writeStdioMessage(writer, stdioResponse{}) == nil || time.Since(start) > time.Second {
		t.Fatal("stdio write extended caller deadline")
	}
}

func TestStdioInvalidRuntimeIDsHaveNoLaunchEffects(t *testing.T) {
	b, spec := testStdio(t, "")
	for _, field := range []string{"host", "session", "execution", "operation"} {
		wrong := spec
		switch field {
		case "host":
			wrong.Binding.HostID = "space identity"
		case "session":
			wrong.Binding.SessionID = "space identity"
		case "execution":
			wrong.Binding.ExecutionID = "space identity"
		case "operation":
			wrong.Binding.StartOperationID = "space identity"
		}
		if _, e := b.LaunchStdio(context.Background(), wrong); !errors.Is(e, ps.ErrInvalid) {
			t.Fatal("runtime identity accepted", field, e)
		}
	}
	entries, e := os.ReadDir(b.state)
	if e != nil || len(entries) != 0 {
		t.Fatal("invalid runtime identities had effects", e)
	}
}
func TestStdioBusyUnicodeAndSchemaMismatch(t *testing.T) {
	b, spec := testStdio(t, "busy")
	wrong := spec
	wrong.SchemaSHA256 = strings.Repeat("e", 64)
	if _, e := b.LaunchStdio(context.Background(), wrong); !errors.Is(e, ps.ErrInvalid) {
		t.Fatal("schema pin accepted", e)
	}
	spec.Binding.NativeThreadID = "fixture-雪"
	data, _ := json.Marshal(stdioFixtureConfig{Mode: "busy", Thread: spec.Binding.NativeThreadID, Workspace: spec.Workspace})
	if e := os.WriteFile(filepath.Join(spec.Environment.Home, "fixture.json"), data, 0600); e != nil {
		t.Fatal(e)
	}
	ref := launchStdioFixture(t, b, spec)
	o, e := b.BootstrapStdioOnce(context.Background(), ref)
	if e != nil {
		t.Fatal(e)
	}
	ackStdio(t, b, ref, o)
	o, e = b.ObserveStdio(context.Background(), ref, o.Through)
	if e != nil || o.Snapshot.Idle || !o.Snapshot.ExactLoadedThread {
		t.Fatal("busy exact Unicode thread misclassified", e)
	}
	ackStdio(t, b, ref, o)
	called := false
	if b.WithStdioQuiescence(context.Background(), ref, ps.Quiescence{Cursor: o.Through, SnapshotSHA256: o.Snapshot.SnapshotSHA256}, func(context.Context) error { called = true; return nil }) == nil || called {
		t.Fatal("busy guard admitted")
	}
}
func TestStdioPipeWriteTimeoutIsBounded(t *testing.T) {
	rd, wr, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	defer rd.Close()
	defer wr.Close()
	fd, e := fileDescriptor(wr)
	if e != nil {
		t.Fatal(e)
	}
	if e = unix.SetNonblock(fd, true); e != nil {
		t.Fatal(e)
	}
	block := make([]byte, 4096)
	for {
		_, e = unix.Write(fd, block)
		if e == unix.EAGAIN {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	begin := time.Now()
	if e = writeStdioPipe(ctx, wr, []byte("x")); e == nil {
		t.Fatal("full pipe write succeeded")
	}
	if time.Since(begin) > time.Second {
		t.Fatal("pipe timeout not bounded")
	}
}

// No process is launched here. This reproduces the exact schedule where the
// caller consumed Reply but has not removed its wait registration under mu.
func TestStdioConsumedReplyDoesNotRearmRequest(t *testing.T) {
	s := testStdioReceiver(t)
	s.wait = &stdioWait{ID: "original-request", Reply: make(chan ps.Envelope, 1)}
	raw := []byte("{\"id\":\"original-request\",\"result\":null}\n")
	s.receiveLine(raw)
	select {
	case <-s.wait.Reply:
	default:
		t.Fatal("first correlated result absent")
	}
	s.receiveLine(raw)
	if !s.state.Pending || len(s.state.Records) != 2 || s.state.Records[1].Event.Kind != ps.EventUnsupported || s.state.Records[1].Event.Reason != ps.ReasonCorrelation {
		t.Fatal("consumed reply accepted twice")
	}
	if len(s.wait.Reply) != 0 {
		t.Fatal("duplicate delivered to caller")
	}
	stored, e := s.b.readStdio(s.r)
	if e != nil || !stored.Pending {
		t.Fatal("duplicate blocker not durable", e)
	}
}

// The receiver faults before bootstrap finishes, and a daemon reads that exact
// cursor between those two transitions. The final correlation fault is already
// covered by the broken gap and therefore cannot advance the cursor again.
func TestStdioBootstrapCompletionInvalidatesSameCursorSnapshot(t *testing.T) {
	for _, completion := range []ps.BootstrapState{ps.BootstrapUncertain, ps.BootstrapComplete} {
		t.Run(string(completion), func(t *testing.T) {
			s := testStdioReceiver(t)
			from := s.state.Cursor
			var outcome error
			if completion == ps.BootstrapUncertain {
				s.fault(ps.ReasonTimeout)
				outcome = ps.ErrUncertain
			} else {
				if e := s.appendLocked(ps.EventResult, ps.ReasonNone, ps.NativeID{Kind: "string", Text: "final-response"}, []byte("{\"id\":\"final-response\",\"result\":null}\n")); e != nil {
					t.Fatal(e)
				}
			}
			first, e := s.observation(from)
			if e != nil || first.Snapshot.Bootstrap != ps.BootstrapAttempted {
				t.Fatal("intermediate attempt snapshot unavailable", e)
			}
			if e = s.finishBootstrap(outcome); !errors.Is(e, outcome) {
				t.Fatal(e)
			}
			durable, e := s.b.readStdio(s.r)
			if e != nil || durable.Bootstrap != completion {
				t.Fatal("final bootstrap transition not durable", e)
			}
			final, e := s.observation(from)
			if e != nil || final.Through != first.Through || final.Snapshot.Bootstrap != completion || final.Snapshot.Pending != (completion == ps.BootstrapUncertain) {
				t.Fatalf("same-cursor snapshot remained stale: first=%s final=%s durable=%s continuity=%s error=%v", first.Snapshot.Bootstrap, final.Snapshot.Bootstrap, durable.Bootstrap, final.Snapshot.Continuity, e)
			}
			if e = s.bootstrap(context.Background()); e != nil || len(s.state.Steps) != 0 || len(s.state.Records) != 1 {
				t.Fatal("finished bootstrap replayed a step or frame", e)
			}
		})
	}
}

func TestStdioStoppedAttemptCannotRepublishCachedQuiescence(t *testing.T) {
	s := readyStdioReceiver(t)
	q := ps.Quiescence{Cursor: s.state.Cursor, SnapshotSHA256: s.state.Snapshot.SnapshotSHA256}
	if !s.quiescentLocked(q) {
		t.Fatal("positive pre-stop cut refused")
	}
	// Persist exactly the helper's pre-signal intent. No process is signaled;
	// this models an uncertain attempted stop with its original child alive.
	if e := s.recordStopAttemptLocked(domain.NewID("stop")); e != nil {
		t.Fatal(e)
	}
	final, e := s.observation(s.state.Cursor)
	if s.quiescentLocked(q) || e != nil || final.Snapshot.Idle || final.Snapshot.ExactLoadedThread || !final.Snapshot.ProcessAlive {
		t.Fatalf("stop intent reused ready cut: idle=%v exact_thread=%v alive=%v error=%v", final.Snapshot.Idle, final.Snapshot.ExactLoadedThread, final.Snapshot.ProcessAlive, e)
	}
}

func TestStdioExitInvalidatesSameCursorLivenessSnapshot(t *testing.T) {
	s := readyStdioReceiver(t)
	from := s.state.Cursor
	first, e := s.observation(from)
	if e != nil || !first.Snapshot.ProcessAlive || !first.Snapshot.Idle {
		t.Fatal("positive pre-exit snapshot unavailable", e)
	}
	s.markExitedLocked()
	final, e := s.observation(from)
	if e != nil || final.Through != first.Through || final.Snapshot.ProcessAlive || final.Snapshot.Idle || final.Snapshot.ExactImage {
		t.Fatalf("exit reused live cut: alive=%v idle=%v image=%v error=%v", final.Snapshot.ProcessAlive, final.Snapshot.Idle, final.Snapshot.ExactImage, e)
	}
}

func readyStdioReceiver(t *testing.T) *stdioEngine {
	t.Helper()
	s := testStdioReceiver(t)
	s.done = make(chan struct{})
	var e error
	s.state.Directories, e = stdioDirectories(s.state.Channel.Spec)
	if e != nil {
		t.Fatal(e)
	}
	s.state.Bootstrap = ps.BootstrapComplete
	s.state.Snapshot = s.snapshotLocked()
	s.state.Snapshot.ExactLoadedThread = true
	s.state.Snapshot.Idle = true
	s.state.Snapshot.EmptyQueue = true
	s.state.Snapshot.EmptyBackground = true
	s.state.Snapshot.SnapshotSHA256 = ps.SnapshotDigest(s.state.Snapshot)
	if e = s.persistLocked(); e != nil {
		t.Fatal(e)
	}
	return s
}

func testStdioReceiver(t *testing.T) *stdioEngine {
	t.Helper()
	b, spec := testStdio(t, "")
	boot, e := bootID()
	if e != nil {
		t.Fatal(e)
	}
	start, e := processStart(os.Getpid())
	if e != nil {
		t.Fatal(e)
	}
	ref := domain.RuntimeRef{Backend: "native", Socket: b.socket(spec.Binding.ExecutionID), Session: nonce(), BootID: boot, PID: os.Getpid(), ProcessStart: start, ExecutionID: spec.Binding.ExecutionID, OperationID: spec.Binding.StartOperationID}
	channel := ps.Channel{Version: ps.Version, Spec: spec, Ref: ps.ChannelRef{Runtime: ref, PrimaryIncarnation: spec.Binding.PrimaryIncarnation, ChannelIncarnation: domain.ID(nonce()[:32])}}
	cur := ps.Genesis(channel.Ref.ChannelIncarnation)
	s := &stdioEngine{b: b, r: receipt{Ref: ref, Stdio: &channel}, state: stdioState{Version: ps.Version, Channel: channel, Bootstrap: ps.BootstrapAttempted, Continuity: ps.ContinuityComplete, Cursor: cur, Ack: cur}, notify: make(chan struct{}, 1)}
	if e = b.saveStdio(s.state); e != nil {
		t.Fatal(e)
	}
	s.disk, e = b.stdioDisk(ref.ExecutionID)
	if e != nil {
		t.Fatal(e)
	}
	return s
}

func TestStdioRemoteControlStatusPolicy(t *testing.T) {
	for _, status := range []string{"disabled", "connecting", "connected", "errored"} {
		t.Run(status, func(t *testing.T) {
			s := testStdioReceiver(t)
			raw, _ := json.Marshal(map[string]any{"method": "remoteControl/status/changed", "params": map[string]any{"status": status, "installationId": "fixture", "serverName": "fixture"}})
			s.receiveLine(append(raw, '\n'))
			if len(s.state.Records) != 1 {
				t.Fatal("remote status not retained")
			}
			event := s.state.Records[0].Event
			if status == "disabled" {
				if s.state.Pending || event.Kind != ps.EventNotice || event.Reason != ps.ReasonNone {
					t.Fatal("disabled metadata blocked")
				}
			} else if !s.state.Pending || event.Kind != ps.EventUnsupported || event.Reason != ps.ReasonUnsupported {
				t.Fatal("native remote control status accepted")
			}
		})
	}
}

func TestStdioConnectedRemoteControlRefusesPublication(t *testing.T) {
	b, spec := testStdio(t, "")
	ref := launchStdioFixture(t, b, spec)
	o := readyStdio(t, b, ref)
	if e := os.WriteFile(filepath.Join(spec.Environment.Home, "remote-connected"), []byte("yes"), 0600); e != nil {
		t.Fatal(e)
	}
	waitStdioFile(t, filepath.Join(spec.Environment.Home, "remote-emitted"))
	deadline := time.Now().Add(3 * time.Second)
	for {
		pending, e := b.RetainedStdio(context.Background(), ref, o.Through)
		if e == nil && pending.Snapshot.Pending {
			ackStdio(t, b, ref, pending)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("remote control blocker missing", e)
		}
		time.Sleep(5 * time.Millisecond)
	}
	called := false
	q := ps.Quiescence{Cursor: o.Through, SnapshotSHA256: o.Snapshot.SnapshotSHA256}
	if b.WithStdioQuiescence(context.Background(), ref, q, func(context.Context) error { called = true; return nil }) == nil || called {
		t.Fatal("remote control publication admitted")
	}
	if b.StopStdio(context.Background(), ref, ps.StopRequest{OperationID: domain.NewID("stop"), Quiescence: q}) == nil {
		t.Fatal("remote control stop admitted")
	}
	if len(stdioMethods(t, spec)) != 7 {
		t.Fatal("remote control blocker caused native writes")
	}
}
