package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/library"
	"github.com/seanheiney/keelmux/internal/protocol"
	"github.com/seanheiney/keelmux/internal/store"
)

func recordRetryLaunch(t *testing.T, e *env, in protocol.LaunchRequest) (domain.Operation, domain.Session) {
	t.Helper()
	v := domain.NewSession("host", in.Name, in.Workspace, in.Provider, in.ProviderVersion)
	if in.Library != nil {
		snapshot := *in.Library
		v.Library = &snapshot
	}
	op, created, err := e.store.Record(context.Background(), domain.Operation{ID: in.OperationID, Kind: "launch", SessionID: v.ID, ExecutionID: v.Execution.ID}, payload(in), &v)
	if err != nil || !created {
		t.Fatal("record original fixture intent", err)
	}
	return op, v
}

func retryFixtureDB(t *testing.T, e *env) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", e.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func rewriteRetryFixture(t *testing.T, e *env, query string, args ...any) {
	t.Helper()
	if _, err := retryFixtureDB(t, e).ExecContext(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func retryFixtureState(t *testing.T, e *env, op domain.Operation) [][]byte {
	t.Helper()
	db := retryFixtureDB(t, e)
	var data, raw, session []byte
	if err := db.QueryRow("SELECT data,payload FROM operations WHERE id=?", op.ID).Scan(&data, &raw); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT data FROM sessions WHERE id=?", op.SessionID).Scan(&session); err != nil {
		t.Fatal(err)
	}
	var operations, sessions, events int
	if err := db.QueryRow("SELECT COUNT(*) FROM operations").Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM events").Scan(&events); err != nil {
		t.Fatal(err)
	}
	return [][]byte{data, raw, session, payload([]int{operations, sessions, events})}
}

func retryLibraryFixture(t *testing.T) (library.Registry, library.Package) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "pack")
	if err = os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	rule := []byte("Preserve the original launch selection.")
	if err = os.WriteFile(filepath.Join(source, "careful.md"), rule, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(rule)
	manifest := library.Manifest{Schema: 1, ID: "retry-pack", Version: "1.0.0", Files: map[string]string{"careful.md": hex.EncodeToString(sum[:])}, Rules: []library.Asset{{ID: "careful", Path: "careful.md"}}}
	if err = os.WriteFile(filepath.Join(source, library.ManifestFilename), payload(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := library.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Import(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.SelectPackage(context.Background(), "default", p.Manifest.ID, p.Digest, true); err != nil {
		t.Fatal(err)
	}
	return r, p
}

func TestRetryLaunchOriginalSnapshotAfterRestartAndProfileToggle(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	r, p := retryLibraryFixture(t)
	selected, err := r.Resolve(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	in := e.launchRequest("original-profile-launch")
	in.Provider = "gemini"
	in.Argv = []string{"fixture-original-cli", "--instructions", "original rule"}
	in.Library = &domain.LibrarySnapshot{ProfileID: selected.ProfileID, ProfileRevision: selected.ProfileRevision,
		ResolutionDigest: selected.Digest, ApplicationID: "original", ApplicationDigest: strings.Repeat("b", 64),
		Source: "client-declaration", Loaded: "unknown"}
	t.Setenv("CI", "")
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("GEMINI_CLI_INTEGRATION_TEST", "")
	first, err := e.service.Launch(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	e.restart(t)
	if _, err = r.SelectComponent(ctx, "default", p.Manifest.ID, "rule/careful", false); err != nil {
		t.Fatal(err)
	}
	current, err := r.Resolve(ctx, "default")
	if err != nil || current.Digest == selected.Digest || current.ProfileRevision == selected.ProfileRevision || len(current.Rules) != 0 {
		t.Fatal("profile toggle control did not change current selection", err)
	}
	// A completed retry must not need the current workspace, native binary,
	// profile resolution, inherited Gemini environment or runtime inspection.
	if err = os.RemoveAll(e.workspace); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CI", "true")
	e.backend.lookupErr = errors.New("private lookup must not run")
	e.backend.inspectErrors[first.Session.Execution.ID] = errors.New("private inspection must not run")
	before := retryFixtureState(t, e, first.Operation)
	again, err := e.service.RetryLaunch(ctx, in.OperationID)
	if err != nil || !reflect.DeepEqual(first, again) {
		t.Fatal("operation-only retry changed the original response", err)
	}
	if !reflect.DeepEqual(before, retryFixtureState(t, e, first.Operation)) {
		t.Fatal("completed retry changed durable intent or outcome")
	}
	if n, _ := e.backend.counts(); n != 1 {
		t.Fatal("completed retry replayed launch", n)
	}
}

func TestRetryLaunchRecordedConcurrentClaimUsesOriginalRequest(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	in := e.launchRequest("recorded-original")
	in.Argv = []string{"fixture-original", "original argument"}
	in.Library = &domain.LibrarySnapshot{ProfileID: "default", ProfileRevision: 1, ResolutionDigest: strings.Repeat("a", 64), Source: "client-declaration", Loaded: "unknown"}
	op, v := recordRetryLaunch(t, e, in)
	// Restart the service without the separate global recovery policy, which
	// intentionally fails unattempted intents. Explicit RetryLaunch may claim
	// an original intent that is still Recorded; it never creates one.
	if err := e.store.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	e.store, err = store.Open(e.path)
	if err != nil {
		t.Fatal(err)
	}
	e.service, err = New(e.store, e.backend, "host")
	if err != nil {
		t.Fatal(err)
	}
	e.backend.beforeLaunch = func(spec domain.LaunchSpec) {
		stored, _, err := e.store.Operation(ctx, op.ID)
		if err != nil || stored.Outcome != domain.Attempted || spec.OperationID != op.ID || spec.Session.ID != v.ID ||
			spec.Session.Execution.ID != v.Execution.ID || !reflect.DeepEqual(spec.Argv, in.Argv) || !sameLaunchLibrary(spec.Session.Library, in.Library) {
			t.Error("fixture effect was not the original durably attempted launch", err)
		}
	}
	var wg sync.WaitGroup
	results := make(chan protocol.LaunchResponse, 24)
	for range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := e.service.RetryLaunch(ctx, op.ID)
			if err != nil {
				t.Error(err)
				return
			}
			results <- got
		}()
	}
	wg.Wait()
	close(results)
	for got := range results {
		if got.Operation.ID != op.ID || got.Operation.Outcome != domain.Completed || got.Session.ID != v.ID || got.Session.Execution.ID != v.Execution.ID {
			t.Fatal("concurrent retry changed operation or execution identity")
		}
	}
	if n, _ := e.backend.counts(); n != 1 {
		t.Fatal("concurrent recorded retry replayed launch", n)
	}
	operations, err := e.store.Operations(ctx)
	if err != nil || len(operations) != 1 {
		t.Fatal("retry created another intent", err)
	}
}

func TestRetryLaunchAttemptedAndUncertainNeverReplay(t *testing.T) {
	for _, outcome := range []domain.Outcome{domain.Attempted, domain.Uncertain} {
		for _, state := range []string{"absent", "alive", "unavailable", "conflicting"} {
			t.Run(string(outcome)+"/"+state, func(t *testing.T) {
				e := setup(t)
				ctx := context.Background()
				in := e.launchRequest("uncertain-original")
				op, v := recordRetryLaunch(t, e, in)
				if _, claimed, err := e.store.Attempt(ctx, op.ID); err != nil || !claimed {
					t.Fatal(err)
				}
				if outcome == domain.Uncertain {
					if _, err := e.store.Resolve(ctx, op.ID, outcome, "fixture lost acknowledgement", nil); err != nil {
						t.Fatal(err)
					}
				}
				if state == "alive" || state == "conflicting" {
					if _, err := e.backend.Launch(ctx, domain.LaunchSpec{Session: v, OperationID: op.ID, Argv: in.Argv}); err != nil {
						t.Fatal(err)
					}
					if state == "conflicting" {
						ref := e.backend.refs[v.Execution.ID]
						ref.ProcessStart = ""
						e.backend.refs[v.Execution.ID] = ref
					}
				}
				if state == "unavailable" {
					e.backend.lookupErr = errors.New("private runtime lookup diagnostic")
				}
				before, _ := e.backend.counts()
				for range 2 {
					got, err := e.service.RetryLaunch(ctx, op.ID)
					if state == "unavailable" || state == "conflicting" {
						var issue runtimeIssue
						if !errors.As(err, &issue) || strings.Contains(err.Error(), "private runtime") {
							t.Fatal("uncertain retry did not preserve sanitized refusal", err)
						}
					} else {
						want := domain.Uncertain
						if state == "alive" {
							want = domain.Completed
						}
						if err != nil || got.Operation.Outcome != want || got.Session.ID != v.ID {
							t.Fatal("uncertain retry did not reconcile original operation", err)
						}
					}
				}
				if after, _ := e.backend.counts(); after != before {
					t.Fatal("uncertain retry replayed launch", before, after)
				}
			})
		}
	}
}

func TestRetryLaunchMissingAndInvalidIDNeverRecordsIntent(t *testing.T) {
	e := setup(t)
	for _, id := range []domain.ID{"", "missing-original", domain.ID(strings.Repeat("x", protocol.MaxControlRequestBytes+1))} {
		if _, err := e.service.RetryLaunch(context.Background(), id); err == nil {
			t.Fatal("invalid or absent operation accepted")
		} else if id == "missing-original" && !errors.Is(err, store.ErrNotFound) {
			t.Fatal("missing operation lost finite not-found error", err)
		}
	}
	operations, err := e.store.Operations(context.Background())
	if err != nil || len(operations) != 0 {
		t.Fatal("retry created an intent", err)
	}
	if n, _ := e.backend.counts(); n != 0 {
		t.Fatal("missing retry launched a runtime", n)
	}
}

func TestRetryLaunchRefusesInvalidStoredPayloadBeforeEffects(t *testing.T) {
	canonicalEdit := func(edit func(*protocol.LaunchRequest)) func(protocol.LaunchRequest) []byte {
		return func(in protocol.LaunchRequest) []byte { edit(&in); return payload(in) }
	}
	cases := []struct {
		name string
		raw  func(protocol.LaunchRequest) []byte
	}{
		{"empty", func(protocol.LaunchRequest) []byte { return []byte{} }},
		{"null", func(protocol.LaunchRequest) []byte { return []byte("null") }},
		{"malformed", func(protocol.LaunchRequest) []byte { return []byte(`{"argv":"private-saved-value"`) }},
		{"duplicate", func(in protocol.LaunchRequest) []byte {
			return append([]byte(`{"operation_id":"private-saved-value",`), payload(in)[1:]...)
		}},
		{"case-alias", func(in protocol.LaunchRequest) []byte {
			return bytes.Replace(payload(in), []byte(`"operation_id"`), []byte(`"Operation_ID"`), 1)
		}},
		{"unknown", func(in protocol.LaunchRequest) []byte {
			return append([]byte(`{"private-saved-value":true,`), payload(in)[1:]...)
		}},
		{"omitted-field", func(in protocol.LaunchRequest) []byte {
			return bytes.Replace(payload(in), []byte(`"provider_version":"v1",`), nil, 1)
		}},
		{"whitespace", func(in protocol.LaunchRequest) []byte { return append([]byte(" "), payload(in)...) }},
		{"second-value", func(in protocol.LaunchRequest) []byte {
			return append(payload(in), []byte(` {"private-saved-value":true}`)...)
		}},
		{"oversized", func(protocol.LaunchRequest) []byte { return bytes.Repeat([]byte(" "), protocol.MaxControlRequestBytes) }},
		{"wrong-operation", canonicalEdit(func(in *protocol.LaunchRequest) { in.OperationID = "private-saved-value" })},
		{"wrong-name", canonicalEdit(func(in *protocol.LaunchRequest) { in.Name = "private-saved-value" })},
		{"wrong-provider", canonicalEdit(func(in *protocol.LaunchRequest) { in.Provider = "private-saved-value" })},
		{"wrong-version", canonicalEdit(func(in *protocol.LaunchRequest) { in.ProviderVersion = "private-saved-value" })},
		{"wrong-workspace", canonicalEdit(func(in *protocol.LaunchRequest) { in.Workspace = "/private-saved-value" })},
		{"relative-workspace", canonicalEdit(func(in *protocol.LaunchRequest) { in.Workspace = "private-saved-value" })},
		{"nul-workspace", canonicalEdit(func(in *protocol.LaunchRequest) { in.Workspace += "\x00private-saved-value" })},
		{"empty-argv", canonicalEdit(func(in *protocol.LaunchRequest) { in.Argv = nil })},
		{"empty-executable", canonicalEdit(func(in *protocol.LaunchRequest) { in.Argv = []string{""} })},
		{"nul-argv", canonicalEdit(func(in *protocol.LaunchRequest) { in.Argv = []string{"fixture", "\x00private-saved-value"} })},
		{"invalid-library", canonicalEdit(func(in *protocol.LaunchRequest) { in.Library.Loaded = "private-saved-value" })},
		{"changed-library", canonicalEdit(func(in *protocol.LaunchRequest) { in.Library.ResolutionDigest = strings.Repeat("c", 64) })},
		{"missing-library", canonicalEdit(func(in *protocol.LaunchRequest) { in.Library = nil })},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := setup(t)
			in := e.launchRequest("invalid-original")
			in.Library = &domain.LibrarySnapshot{ProfileID: "default", ProfileRevision: 1, ResolutionDigest: strings.Repeat("a", 64), Source: "client-declaration", Loaded: "unknown"}
			op, _ := recordRetryLaunch(t, e, in)
			rewriteRetryFixture(t, e, "UPDATE operations SET payload=? WHERE id=?", tc.raw(in), op.ID)
			before := retryFixtureState(t, e, op)
			_, err := e.service.RetryLaunch(context.Background(), op.ID)
			assertConflict(t, err)
			if strings.Contains(err.Error(), "private-saved-value") {
				t.Fatal("saved payload leaked in refusal")
			}
			if !reflect.DeepEqual(before, retryFixtureState(t, e, op)) {
				t.Fatal("invalid retry changed original store")
			}
			if n, _ := e.backend.counts(); n != 0 {
				t.Fatal("invalid stored payload reached runtime", n)
			}
		})
	}
}

func TestRetryLaunchRefusesStoredIdentityMismatchBeforeEffects(t *testing.T) {
	cases := []struct {
		name    string
		op      func(*domain.Operation)
		session func(*domain.Session)
	}{
		{name: "operation-ID", op: func(op *domain.Operation) { op.ID = "private-saved-value" }},
		{name: "not-launch", op: func(op *domain.Operation) { op.Kind = "stop" }},
		{name: "missing-session", op: func(op *domain.Operation) { op.SessionID = "" }},
		{name: "absent-session", op: func(op *domain.Operation) { op.SessionID = "private-saved-value" }},
		{name: "missing-execution", op: func(op *domain.Operation) { op.ExecutionID = "" }},
		{name: "wrong-execution", op: func(op *domain.Operation) { op.ExecutionID = "private-saved-value" }},
		{name: "unknown-outcome", op: func(op *domain.Operation) { op.Outcome = "private-saved-value" }},
		{name: "session-ID", session: func(v *domain.Session) { v.ID = "private-saved-value" }},
		{name: "foreign-home", session: func(v *domain.Session) { v.HostID = "private-saved-value" }},
		{name: "replacement-execution", session: func(v *domain.Session) { v.Execution.ID = "private-saved-value" }},
		{name: "zero-generation", session: func(v *domain.Session) { v.Execution.Generation = 0 }},
		{name: "stale-generation", session: func(v *domain.Session) { v.Execution.Generation++ }},
		{name: "missing-work", session: func(v *domain.Session) { v.WorkID = "" }},
		{name: "missing-thread", session: func(v *domain.Session) { v.ThreadID = "" }},
		{name: "missing-lane", session: func(v *domain.Session) { v.LaneID = "" }},
		{name: "observed-session", session: func(v *domain.Session) { v.Mode = domain.Observed }},
		{name: "session-library", session: func(v *domain.Session) { v.Library = &domain.LibrarySnapshot{} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := setup(t)
			op, v := recordRetryLaunch(t, e, e.launchRequest("identity-original"))
			if tc.op != nil {
				changed := op
				tc.op(&changed)
				rewriteRetryFixture(t, e, "UPDATE operations SET data=? WHERE id=?", payload(changed), op.ID)
			}
			if tc.session != nil {
				changed := v
				tc.session(&changed)
				rewriteRetryFixture(t, e, "UPDATE sessions SET data=? WHERE id=?", payload(changed), v.ID)
			}
			before := retryFixtureState(t, e, op)
			_, err := e.service.RetryLaunch(context.Background(), op.ID)
			assertConflict(t, err)
			if strings.Contains(err.Error(), "private-saved-value") || !reflect.DeepEqual(before, retryFixtureState(t, e, op)) {
				t.Fatal("invalid binding leaked private data or changed store")
			}
			if n, _ := e.backend.counts(); n != 0 {
				t.Fatal("invalid stored identity reached runtime", n)
			}
		})
	}
}

func TestRetryLaunchUnreadableStoredMetadataIsSanitized(t *testing.T) {
	for _, table := range []string{"operations", "sessions"} {
		t.Run(table, func(t *testing.T) {
			e := setup(t)
			op, v := recordRetryLaunch(t, e, e.launchRequest("corrupt-original"))
			id := op.ID
			if table == "sessions" {
				id = v.ID
			}
			rewriteRetryFixture(t, e, "UPDATE "+table+" SET data=? WHERE id=?", []byte(`{"id":{"private-saved-value":"secret-invalid-shape"}}`), id)
			before := retryFixtureState(t, e, op)
			_, err := e.service.RetryLaunch(context.Background(), op.ID)
			assertConflict(t, err)
			if strings.Contains(err.Error(), "private-saved-value") || strings.Contains(err.Error(), "secret-invalid-shape") || !reflect.DeepEqual(before, retryFixtureState(t, e, op)) {
				t.Fatal("corrupt metadata leaked or changed durable records")
			}
			if n, _ := e.backend.counts(); n != 0 {
				t.Fatal("corrupt metadata reached runtime", n)
			}
		})
	}
}

func TestRetryLaunchEveryOutcomeRefusesDifferentIndexedLifetime(t *testing.T) {
	for _, outcome := range []domain.Outcome{domain.Recorded, domain.Attempted, domain.Uncertain, domain.Completed, domain.Failed} {
		for _, mutation := range []string{"session-generation", "indexed-generation", "indexed-session"} {
			for _, entry := range []string{"operation-only", "exact-payload"} {
				t.Run(string(outcome)+"/"+mutation+"/"+entry, func(t *testing.T) {
					e := setup(t)
					in := e.launchRequest("lifetime-original")
					op, v := recordRetryLaunch(t, e, in)
					changedOp := op
					changedOp.Outcome = outcome
					rewriteRetryFixture(t, e, "UPDATE operations SET data=? WHERE id=?", payload(changedOp), op.ID)
					switch mutation {
					case "session-generation":
						v.Execution.Generation++
						rewriteRetryFixture(t, e, "UPDATE sessions SET data=? WHERE id=?", payload(v), v.ID)
					case "indexed-generation":
						rewriteRetryFixture(t, e, "UPDATE operations SET generation=generation+1 WHERE id=?", op.ID)
					case "indexed-session":
						otherRequest := e.launchRequest("other-lifetime")
						otherRequest.Name = "Other"
						_, other := recordRetryLaunch(t, e, otherRequest)
						rewriteRetryFixture(t, e, "UPDATE operations SET session_id=? WHERE id=?", other.ID, op.ID)
					}
					before := retryFixtureState(t, e, op)
					var err error
					if entry == "operation-only" {
						_, err = e.service.RetryLaunch(context.Background(), op.ID)
					} else {
						_, err = e.service.Launch(context.Background(), in)
					}
					assertConflict(t, err)
					if !reflect.DeepEqual(before, retryFixtureState(t, e, op)) {
						t.Fatal("refused lifetime changed durable state")
					}
					if launches, stops := e.backend.counts(); launches != 0 || stops != 0 {
						t.Fatal("different original lifetime reached runtime", launches, stops)
					}
				})
			}
		}
	}
}

func TestRetryLaunchLegacyOriginalAndFailedTerminalCompatibility(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy-completed", true: "legacy-failed"}[failed], func(t *testing.T) {
			e := setup(t)
			ctx := context.Background()
			in := e.launchRequest("legacy-original")
			op, _ := recordRetryLaunch(t, e, in)
			// This is the original pre-library payload shape and field order.
			legacy := struct {
				OperationID     domain.ID `json:"operation_id"`
				Name            string    `json:"name"`
				Workspace       string    `json:"workspace"`
				Provider        string    `json:"provider"`
				ProviderVersion string    `json:"provider_version"`
				Argv            []string  `json:"argv"`
			}{in.OperationID, in.Name, in.Workspace, in.Provider, in.ProviderVersion, in.Argv}
			if !bytes.Equal(payload(legacy), payload(in)) {
				t.Fatal("legacy payload control no longer matches original encoding")
			}
			rewriteRetryFixture(t, e, "UPDATE operations SET payload=? WHERE id=?", payload(legacy), op.ID)
			if failed {
				if _, err := e.store.Resolve(ctx, op.ID, domain.Failed, "original intent explicitly failed", nil); err != nil {
					t.Fatal(err)
				}
			}
			first, err := e.service.RetryLaunch(ctx, op.ID)
			want := domain.Completed
			launches := 1
			if failed {
				want, launches = domain.Failed, 0
			}
			if err != nil || first.Operation.Outcome != want {
				t.Fatal("legacy original request refused", err)
			}
			again, err := e.service.RetryLaunch(ctx, op.ID)
			if err != nil || !reflect.DeepEqual(first, again) {
				t.Fatal("terminal retry changed original response", err)
			}
			if n, _ := e.backend.counts(); n != launches {
				t.Fatal("terminal retry replayed launch", n)
			}
		})
	}
}

func TestRetryLaunchCanceledRequestHasNoEffect(t *testing.T) {
	e := setup(t)
	op, _ := recordRetryLaunch(t, e, e.launchRequest("canceled-original"))
	before := retryFixtureState(t, e, op)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.service.RetryLaunch(ctx, op.ID); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled retry did not preserve cancellation", err)
	}
	if !reflect.DeepEqual(before, retryFixtureState(t, e, op)) {
		t.Fatal("canceled retry changed original intent")
	}
	if n, _ := e.backend.counts(); n != 0 {
		t.Fatal("canceled retry reached runtime", n)
	}
}

func TestRetryLaunchMaximumOriginalEncodedRequest(t *testing.T) {
	e := setup(t)
	in := e.launchRequest("maximum-original")
	in.Argv = []string{"fixture", ""}
	// Launch's encoder adds one newline; the durable canonical payload does
	// not. Preserve compatibility at that exact existing control limit.
	in.Argv[1] = strings.Repeat("x", protocol.MaxControlRequestBytes-len(payload(in))-1)
	if len(payload(in)) != protocol.MaxControlRequestBytes-1 || protocol.ValidateLaunchRequestSize(in) != nil {
		t.Fatal("encoded maximum positive control is invalid")
	}
	op, _ := recordRetryLaunch(t, e, in)
	got, err := e.service.RetryLaunch(context.Background(), op.ID)
	if err != nil || got.Operation.Outcome != domain.Completed {
		t.Fatal("maximum original request refused", err)
	}
	if n, _ := e.backend.counts(); n != 1 {
		t.Fatal("maximum original was not attempted exactly once", n)
	}
}
