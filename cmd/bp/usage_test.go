package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/usage"
)

var usageTestNow = time.Date(2026, 10, 10, 17, 0, 0, 0, time.UTC)

func usageTestNumber(n int64) *int64    { return &n }
func usageTestDecimal(s string) *string { return &s }
func usageTestObservation(id string) usage.Observation {
	return usage.Observation{ID: id, Source: usage.Source{ID: "authored-form", Kind: "manual", Quality: usage.UserEntered, ObservedAt: usageTestNow, Sequence: 1, Coverage: usage.Complete}, Attempt: &usage.Attempt{Key: usage.AttemptKey{Provider: "vendor", Namespace: "authored-namespace", RequestID: id, AttemptID: "attempt-1"}, At: usageTestNow.Add(-time.Minute), Context: usage.Context{Host: "host-a", Session: "session-a", Project: "project-a", Task: "task-a", Harness: "native-cli", Account: "authored-account"}, LogicalRequest: "logical-a", NativeThread: "native-a", NativeTurn: "turn-a", ActualModel: "authored-model", RequestedModel: "alias", Outcome: "succeeded", ToolCalls: usageTestNumber(3), Tokens: []usage.Token{{Dimension: "input", Count: usageTestNumber(100)}}, Final: true}}
}
func usageTestDocument(t *testing.T, observations ...usage.Observation) []byte {
	t.Helper()
	b, err := json.Marshal(usageImportDocument{Version: 1, Observations: observations})
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func usageTestRun(t *testing.T, state string, args []string, input []byte) string {
	t.Helper()
	var out bytes.Buffer
	if err := usageRun(context.Background(), state, "", args, bytes.NewReader(input), &out, usageTestNow); err != nil {
		t.Fatal(err)
	}
	return out.String()
}
func usageTestView(t *testing.T, state string, args ...string) usageViewOutput {
	t.Helper()
	args = append(args, "--json")
	var v usageViewOutput
	if err := json.Unmarshal([]byte(usageTestRun(t, state, args, nil)), &v); err != nil {
		t.Fatal(err)
	}
	return v
}
func usageTestNoState(t *testing.T, state string) {
	t.Helper()
	if _, err := os.Lstat(state); !os.IsNotExist(err) {
		t.Fatalf("invalid/read-only operation created state: %v", err)
	}
}

func TestUsageMissingViewIsUnknownWithoutCreatingState(t *testing.T) {
	state := filepath.Join(t.TempDir(), "missing", "state")
	v := usageTestView(t, state, "view", "--host", "host-a", "--session", "session-a", "--provider", "vendor", "--expected-source", "expected")
	if v.LedgerPresent || v.Projection.Coverage.Scope != usage.Unknown || v.Projection.Counts.ToolCalls != nil || !reflect.DeepEqual(v.Projection.Coverage.MissingSources, []string{"expected"}) || !strings.Contains(v.Notice, "observed subset") {
		t.Fatalf("missing ledger overclaimed coverage: %+v", v)
	}
	usageTestNoState(t, state)
	text := usageTestRun(t, state, nil, nil)
	for _, phrase := range []string{"No usage ledger exists", "coverage is unknown", "user-entered calls: 0", "Token usage: unknown", "Actual models observed: unknown"} {
		if !strings.Contains(text, phrase) {
			t.Fatalf("empty view omitted %q: %s", phrase, text)
		}
	}
	usageTestNoState(t, state)
}

func TestUsageMissingViewStillRejectsInvalidQueriesAndRemoteEntry(t *testing.T) {
	for _, args := range [][]string{{"--timezone", "made/up"}, {"--from", "yesterday"}, {"--from", "2026-10-11T00:00:00Z", "--to", "2026-10-10T00:00:00Z"}, {"--freshness", "0s"}, {"--provider", "unknown"}, {"unexpected-command"}} {
		state := filepath.Join(t.TempDir(), "state")
		var out bytes.Buffer
		if err := usageRun(context.Background(), state, "", args, strings.NewReader(""), &out, usageTestNow); err == nil {
			t.Fatalf("invalid view accepted: %v", args)
		}
		usageTestNoState(t, state)
	}
	state := filepath.Join(t.TempDir(), "state")
	var out bytes.Buffer
	if err := usageRun(context.Background(), state, "/never/contact.sock", nil, nil, &out, usageTestNow); err == nil {
		t.Fatal("remote entry was silently local")
	}
	usageTestNoState(t, state)
	if err := usageRun(context.Background(), state, "/never/contact.sock", []string{"--help"}, nil, &out, usageTestNow); err != nil || !strings.Contains(out.String(), "Usage:") {
		t.Fatal("help depends on state/daemon")
	}
	usageTestNoState(t, state)
}

func TestUsageManualImportAndReplayCannotEstablishProviderUsage(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	a := usageTestObservation("declared")
	m := usageTestObservation("estimate")
	m.Attempt = nil
	m.Money = &usage.Money{Provider: "vendor", Namespace: "authored-namespace", RecordID: "estimate", Context: usage.Context{Host: "host-a", Session: "session-a", Account: "authored-account"}, At: usageTestNow.Add(-time.Minute), Kind: usage.Estimate, Currency: "USD", Decimal: usageTestDecimal("0.300000000")}
	doc := usageTestDocument(t, a, m)
	var receipt usageImportOutput
	if err := json.Unmarshal([]byte(usageTestRun(t, state, []string{"import", "-"}, doc)), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Imported != 2 || receipt.Duplicates != 0 || receipt.Provenance != usage.UserEntered || receipt.ScopeCoverage != usage.Unknown {
		t.Fatalf("manual receipt overclaimed: %+v", receipt)
	}
	v := usageTestView(t, state, "--money", "--accounts")
	if !v.LedgerPresent || v.Projection.Counts.ProviderCalls != 0 || v.Projection.Counts.DeclaredCalls != 1 || len(v.Projection.ActualModels) != 0 || len(v.Projection.Tokens) != 0 || len(v.Projection.MoneyTotals) != 1 || v.Projection.MoneyTotals[0].UserEnteredRecords != 1 || *v.Projection.MoneyTotals[0].ObservedDecimal != "0.3" {
		t.Fatalf("manual input became observed provider evidence: %+v", v)
	}
	for _, record := range v.Projection.Attempts {
		if !strings.HasPrefix(record.Key.Namespace, manualUsagePrefix) || record.Sources[0].Quality != usage.UserEntered || record.Sources[0].Coverage != usage.Unknown || record.Fields["account"].Quality != usage.UserEntered {
			t.Fatalf("manual provenance laundered: %+v", record)
		}
	}
	before, _ := os.ReadFile(filepath.Join(state, "usage.db"))
	if err := json.Unmarshal([]byte(usageTestRun(t, state, []string{"import", "-"}, doc)), &receipt); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(state, "usage.db"))
	if receipt.Imported != 0 || receipt.Duplicates != 2 || !bytes.Equal(before, after) {
		t.Fatal("repeated manual import was not byte-preserving/idempotent")
	}
	text := usageTestRun(t, state, []string{"--money"}, nil)
	if !strings.Contains(text, "user-entered calls: 1") || !strings.Contains(text, "1 user-entered records; coverage unknown") {
		t.Fatal("text view hid authored money/calls")
	}
	if _, err := os.Lstat(filepath.Join(state, "control.sock")); !os.IsNotExist(err) {
		t.Fatal("usage started a daemon")
	}
	if _, err := os.Lstat(filepath.Join(state, "registry.sqlite")); !os.IsNotExist(err) {
		t.Fatal("usage created product registry")
	}
}

func TestUsageManualNamespaceCannotEnrichTrustedNativeAttempt(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	l, err := usage.Open(filepath.Join(state, "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	native := usageTestObservation("same-request")
	native.Source = usage.Source{ID: "native-hook", Kind: "runtime_hook", Quality: usage.AdapterObserved, ObservedAt: usageTestNow, Sequence: 1, Coverage: usage.Partial}
	native.Attempt.ActualModel = "actual-A"
	if _, err := l.Ingest(context.Background(), native); err != nil {
		t.Fatal(err)
	}
	l.Close()
	manual := usageTestObservation("same-request")
	manual.Attempt.ActualModel = "fabricated-B"
	usageTestRun(t, state, []string{"import", "-"}, usageTestDocument(t, manual))
	v := usageTestView(t, state)
	if v.Projection.Counts.ProviderCalls != 1 || v.Projection.Counts.DeclaredCalls != 1 || !reflect.DeepEqual(v.Projection.ActualModels, []string{"vendor:actual-A"}) || v.Projection.Coverage.ConflictedRecords != 0 {
		t.Fatalf("manual key contaminated native record: %+v", v)
	}
	v = usageTestView(t, state, "--model", "actual-A")
	if v.Projection.Counts.ProviderCalls != 1 || v.Projection.Counts.DeclaredCalls != 0 {
		t.Fatal("model filter accepted manual authority")
	}
	v = usageTestView(t, state, "--model", "alias")
	if len(v.Projection.Attempts) != 0 {
		t.Fatal("requested alias became actual model")
	}
}

func TestUsageImportAtomicExistingConflictAndInvalidInputCreateNoState(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	existing := usageTestObservation("existing")
	usageTestRun(t, state, []string{"import", "-"}, usageTestDocument(t, existing))
	beforeFile, _ := os.ReadFile(filepath.Join(state, "usage.db"))
	beforeView := usageTestView(t, state)
	first := usageTestObservation("new")
	conflict := usageTestObservation("existing")
	conflict.Attempt.ActualModel = "changed"
	var out bytes.Buffer
	if err := usageRun(context.Background(), state, "", []string{"import", "-"}, bytes.NewReader(usageTestDocument(t, first, conflict)), &out, usageTestNow); err == nil || out.Len() != 0 {
		t.Fatal("mixed failure printed partial success")
	}
	afterFile, _ := os.ReadFile(filepath.Join(state, "usage.db"))
	afterView := usageTestView(t, state)
	if !bytes.Equal(beforeFile, afterFile) || !reflect.DeepEqual(beforeView, afterView) {
		t.Fatal("failed import left a hidden partial record")
	}
	newState := filepath.Join(t.TempDir(), "state")
	first = usageTestObservation("identical-id")
	conflict = usageTestObservation("identical-id")
	conflict.Attempt.ActualModel = "changed"
	if err := usageRun(context.Background(), newState, "", []string{"import", "-"}, bytes.NewReader(usageTestDocument(t, first, conflict)), &out, usageTestNow); err == nil {
		t.Fatal("intrinsic conflicting batch accepted")
	}
	usageTestNoState(t, newState)
}

func TestUsageImportRejectsAuthorityQuotaChargeAndUnknownFields(t *testing.T) {
	cases := map[string]func(*usage.Observation){
		"provider-verified": func(o *usage.Observation) { o.Source.Quality = usage.ProviderVerified },
		"adapter-observed":  func(o *usage.Observation) { o.Source.Quality = usage.AdapterObserved },
		"runtime-hook":      func(o *usage.Observation) { o.Source.Kind = "runtime_hook" },
		"actual-charge": func(o *usage.Observation) {
			o.Attempt = nil
			o.Money = &usage.Money{Provider: "vendor", Namespace: "namespace", RecordID: "charge", At: usageTestNow, Kind: usage.Charge, Currency: "USD", Decimal: usageTestDecimal("1")}
		},
		"shared-quota":    func(o *usage.Observation) { o.Attempt = nil; o.Quota = &usage.Quota{} },
		"negative-tokens": func(o *usage.Observation) { o.Attempt.Tokens[0].Count = usageTestNumber(-1) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			state := filepath.Join(t.TempDir(), "state")
			o := usageTestObservation(name)
			mutate(&o)
			var out bytes.Buffer
			if err := usageRun(context.Background(), state, "", []string{"import", "-"}, bytes.NewReader(usageTestDocument(t, o)), &out, usageTestNow); err == nil {
				t.Fatal("unsafe manual import accepted")
			}
			usageTestNoState(t, state)
		})
	}
	valid := usageTestDocument(t, usageTestObservation("valid"))
	unknown := bytes.Replace(valid, []byte(`"version":1`), []byte(`"version":1,"AccountVerified":true`), 1)
	for _, bad := range [][]byte{unknown, []byte(`{"version":1,"version":1,"observations":[]}`), append(append([]byte{}, valid...), valid...), []byte(`{"version":2,"observations":[]}`), []byte(`{"version":1,"observations":[]}`), bytes.Repeat([]byte(" "), usageImportBytes+1), append([]byte{0xff}, valid...)} {
		state := filepath.Join(t.TempDir(), "state")
		var out bytes.Buffer
		if err := usageRun(context.Background(), state, "", []string{"import", "-"}, bytes.NewReader(bad), &out, usageTestNow); err == nil {
			t.Fatal("malformed/ambiguous JSON accepted")
		}
		usageTestNoState(t, state)
	}
	deep := []byte(strings.Repeat("[", 65) + "0" + strings.Repeat("]", 65))
	if _, err := decodeManualUsage(bytes.NewReader(deep)); err == nil {
		t.Fatal("unbounded JSON nesting accepted")
	}
}

func TestUsageRegularFileImportAndReadonlyFilters(t *testing.T) {
	parent := t.TempDir()
	state := filepath.Join(parent, "state")
	file := filepath.Join(parent, "observations.json")
	if err := os.WriteFile(file, usageTestDocument(t, usageTestObservation("file")), 0600); err != nil {
		t.Fatal(err)
	}
	usageTestRun(t, state, []string{"import", file}, nil)
	before, _ := os.ReadFile(filepath.Join(state, "usage.db"))
	filesBefore, _ := os.ReadDir(state)
	v := usageTestView(t, state, "view", "--host", "host-a", "--session", "session-a", "--project", "project-a", "--task", "task-a", "--harness", "native-cli", "--provider", "vendor", "--from", "2026-10-10T16:00:00Z", "--to", "2026-10-10T17:00:00Z", "--timezone", "America/New_York")
	if v.Projection.Counts.DeclaredCalls != 1 || v.Projection.Timezone != "America/New_York" {
		t.Fatal("context/time filtering failed")
	}
	if v := usageTestView(t, state, "--host", "other"); len(v.Projection.Attempts) != 0 {
		t.Fatal("host filter ignored")
	}
	if v := usageTestView(t, state, "--from", "2026-10-10T16:00:00Z", "--to", "2026-10-10T16:59:00Z"); len(v.Projection.Attempts) != 0 {
		t.Fatal("end-exclusive period became inclusive")
	}
	after, _ := os.ReadFile(filepath.Join(state, "usage.db"))
	filesAfter, _ := os.ReadDir(state)
	if !bytes.Equal(before, after) || !reflect.DeepEqual(filesBefore, filesAfter) {
		t.Fatal("view wrote a lock, journal or state")
	}
	if _, ok := v.Projection.Attempts[0].Fields["account"]; ok {
		t.Fatal("account details visible by default")
	}
	link := filepath.Join(parent, "linked.json")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	newState := filepath.Join(parent, "no-state")
	var out bytes.Buffer
	if err := usageRun(context.Background(), newState, "", []string{"import", link}, nil, &out, usageTestNow); err == nil {
		t.Fatal("import followed symlink")
	}
	usageTestNoState(t, newState)
	fifo := filepath.Join(parent, "input.fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if err := usageRun(context.Background(), newState, "", []string{"import", fifo}, nil, &out, usageTestNow); err == nil {
		t.Fatal("import accepted special file")
	}
	usageTestNoState(t, newState)
}

type usageFailedWriter struct{}

func (usageFailedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestUsageCommittedImportReportsOutputFailureAndCanReplay(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	doc := usageTestDocument(t, usageTestObservation("lost-output"))
	err := usageRun(context.Background(), state, "", []string{"import", "-"}, bytes.NewReader(doc), usageFailedWriter{}, usageTestNow)
	if err == nil || !strings.Contains(err.Error(), "import committed") {
		t.Fatalf("lost output hid durable effect: %v", err)
	}
	var receipt usageImportOutput
	if err := json.Unmarshal([]byte(usageTestRun(t, state, []string{"import", "-"}, doc)), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Imported != 0 || receipt.Duplicates != 1 {
		t.Fatal("lost output led to duplicate record")
	}
}
