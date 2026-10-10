package usage

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var testNow = time.Date(2026, 10, 10, 17, 0, 0, 0, time.UTC)

func num(n int64) *int64   { return &n }
func dec(s string) *string { return &s }
func testContext() Context {
	return Context{Federation: "fed", Host: "host-a", Project: "project", Task: "task", Thread: "thread", Lane: "lane", Session: "session", Execution: "epoch-1", Account: "account", Subscription: "subscription", Harness: "native-cli", HarnessVersion: "1"}
}
func attempt(id string) Observation {
	return Observation{ID: id, Source: Source{ID: "stream", Kind: "runtime_hook", Quality: AdapterObserved, ObservedAt: testNow, Sequence: 1, Coverage: Partial}, Attempt: &Attempt{Key: AttemptKey{Provider: "vendor", Namespace: "opaque-scope", RequestID: id, AttemptID: "1"}, At: testNow.Add(-time.Minute), Context: testContext(), LogicalRequest: "logical-1", NativeThread: "native-thread", NativeTurn: "turn-1", RequestedModel: "alias", ActualModel: "A", Outcome: "succeeded", ToolCalls: num(0), Tokens: []Token{{Dimension: "input", Count: num(100)}, {Dimension: "output", Count: num(20)}}, Final: true}}
}
func query() Query {
	return Query{Start: testNow.Add(-time.Hour), End: testNow.Add(time.Hour), Timezone: "America/New_York", Now: testNow, MaxAge: 15 * time.Minute, Authorization: Authorization{AllContexts: true, AccountDetails: true, MoneyDetails: true}}
}
func openTest(t *testing.T) (*Ledger, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state", "usage.db")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, path
}
func ingest(t *testing.T, l *Ledger, o Observation) IngestResult {
	t.Helper()
	v, err := l.Ingest(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func project(t *testing.T, l *Ledger, q Query) Projection {
	t.Helper()
	p, err := l.Project(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCanonicalRequestsEnrichmentAndRestart(t *testing.T) {
	l, path := openTest(t)
	main := attempt("main")
	main.Attempt.ActualModel = ""
	main.Attempt.Context.Account = ""
	main.Attempt.Final = false
	main.Attempt.Tokens[0].Count = num(25)
	ingest(t, l, main)
	final := attempt("main-final")
	final.Attempt.Key = main.Attempt.Key
	final.Source.Sequence = 2
	final.Source.Coverage = Complete
	final.Attempt.Tokens = []Token{{Dimension: "total", Count: num(120)}, {Dimension: "input", Count: num(100), IncludedIn: "total"}, {Dimension: "output", Count: num(20), IncludedIn: "total"}, {Dimension: "cache_read", Count: num(10), IncludedIn: "input"}, {Dimension: "reasoning", Count: num(5), IncludedIn: "output"}}
	ingest(t, l, final)
	transcript := final
	transcript.ID = "transcript-main"
	transcript.Source.ID = "native-transcript"
	transcript.Source.Kind = "transcript"
	transcript.Source.Sequence = 1
	ingest(t, l, transcript)
	if !ingest(t, l, transcript).Duplicate {
		t.Fatal("replica replay not idempotent")
	}
	old := main
	old.ID = "late-old-snapshot"
	old.Source.Sequence = 0
	old.Source.ObservedAt = testNow.Add(-time.Minute)
	ingest(t, l, old)
	retry := attempt("retry")
	retry.Attempt.RetryOf = &main.Attempt.Key
	retry.Attempt.Tokens = []Token{{Dimension: "input", Count: num(2), IncludedIn: "total"}, {Dimension: "output", Count: num(1), IncludedIn: "total"}, {Dimension: "total", Count: num(3)}}
	ingest(t, l, retry)
	child := attempt("child")
	child.Attempt.ActualModel = "B"
	child.Attempt.Parent = &main.Attempt.Key
	child.Attempt.NativeTurn = "child-turn"
	ingest(t, l, child)
	q := query()
	q.ExpectedSources = []string{"stream", "native-transcript", "missing-provider"}
	p := project(t, l, q)
	if p.Counts.ProviderCalls != 3 || p.Counts.LogicalRequests != 1 || p.Counts.Retries != 1 || p.Counts.ChildCalls != 1 || p.Counts.NativeTurns != 2 || p.Counts.Succeeded != 3 || p.Counts.DeclaredCalls != 0 {
		t.Fatalf("inflated or collapsed counts: %+v", p.Counts)
	}
	if !reflect.DeepEqual(p.ActualModels, []string{"vendor:A", "vendor:B"}) {
		t.Fatal(p.ActualModels)
	}
	if len(p.ModelTotals) != 2 || p.ModelTotals[0].ActualModel != "A" || p.ModelTotals[0].Counts.ProviderCalls != 2 || p.ModelTotals[0].Counts.LogicalRequests != 1 || p.ModelTotals[0].Counts.NativeTurns != 1 || p.ModelTotals[0].Counts.Retries != 1 || p.ModelTotals[1].Counts.ChildCalls != 1 {
		t.Fatalf("model counts mixed requested aliases or retries: %+v", p.ModelTotals)
	}
	if p.Coverage.Scope != Unknown || !reflect.DeepEqual(p.Coverage.MissingSources, []string{"missing-provider"}) {
		t.Fatalf("scope coverage claimed: %+v", p.Coverage)
	}
	for _, v := range p.Attempts {
		if v.Key == main.Attempt.Key {
			if value(v.Fields, "actual_model") != "A" || value(v.Fields, "account") != "account" || !v.Final || v.Coverage != Complete {
				t.Fatalf("enrichment lost: %+v", v)
			}
		}
	}
	found := false
	for _, v := range p.Tokens {
		if v.ActualModel == "A" && v.Dimension == "input" {
			found = true
			if v.ObservedCount == nil || *v.ObservedCount != 102 || v.KnownAttempts != 2 || v.IncludedIn != "total" {
				t.Fatalf("snapshots summed: %+v", v)
			}
		}
		if v.ActualModel == "A" && v.Dimension == "cache_read" && v.UnknownAttempts != 1 {
			t.Fatalf("absent dimension treated as zero: %+v", v)
		}
	}
	if !found {
		t.Fatal("input dimension absent")
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	again := project(t, reopened, q)
	if !reflect.DeepEqual(p, again) {
		t.Fatal("restart changed deterministic projection")
	}
	if !ingest(t, reopened, transcript).Duplicate {
		t.Fatal("restart lost source identity")
	}
}

func TestConflictsRemainUnknownAndInsertionOrderIndependent(t *testing.T) {
	a := attempt("first")
	a.Source.Coverage = Complete
	b := attempt("second")
	b.Attempt.Key = a.Attempt.Key
	b.Source.ID = "provider-receipt"
	b.Source.Kind = "provider_api"
	b.Source.Quality = ProviderVerified
	b.Source.Coverage = Complete
	b.Attempt.ActualModel = "B"
	b.Attempt.Tokens[0].Count = num(101)
	l, _ := openTest(t)
	ingest(t, l, a)
	ingest(t, l, b)
	p := project(t, l, query())
	if p.Counts.ProviderCalls != 1 || p.Counts.UnknownActualModel != 1 || len(p.ActualModels) != 0 || !p.Attempts[0].Conflicted || p.Attempts[0].Fields["actual_model"].Value != nil || len(p.Attempts[0].Fields["actual_model"].Conflicts) != 2 {
		t.Fatalf("arbitrarily chosen actual model: %+v", p)
	}
	for _, v := range p.Attempts[0].Tokens {
		if v.Dimension == "input" && (v.Count != nil || !v.Conflict) {
			t.Fatalf("conflicting tokens selected: %+v", v)
		}
		if v.Dimension == "input" && len(v.Conflicts) != 2 {
			t.Fatalf("token conflict evidence lost: %+v", v)
		}
	}
	l2, _ := openTest(t)
	ingest(t, l2, b)
	ingest(t, l2, a)
	if !reflect.DeepEqual(p, project(t, l2, query())) {
		t.Fatal("insertion order decided truth")
	}
	changed := a
	changed.Attempt = &Attempt{}
	*changed.Attempt = *a.Attempt
	changed.Attempt.ActualModel = "different"
	if _, err := l.Ingest(context.Background(), changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("immutable replay accepted: %v", err)
	}
	changed = a
	changed.ID = "another-id"
	if _, err := l.Ingest(context.Background(), changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("same stream sequence reassigned: %v", err)
	}
}

func TestModelOutcomesKnownZeroAndFutureEvidence(t *testing.T) {
	l, _ := openTest(t)
	for _, outcome := range []string{"succeeded", "failed", "uncertain", "pending"} {
		o := attempt(outcome)
		o.Attempt.Outcome = outcome
		o.Attempt.LogicalRequest = outcome
		o.Attempt.NativeTurn = outcome
		o.Attempt.Tokens[0].Count = num(0)
		o.Attempt.Tokens[1].Count = nil
		if outcome == "pending" {
			o.Attempt.ActualModel = ""
			o.Source.ObservedAt = testNow.Add(time.Minute)
		}
		ingest(t, l, o)
	}
	p := project(t, l, query())
	if p.Counts.ProviderCalls != 4 || p.Counts.Succeeded != 1 || p.Counts.Failed != 1 || p.Counts.Uncertain != 1 || p.Counts.UnknownOutcome != 1 || p.Counts.UnknownActualModel != 1 || p.Coverage.StaleRecords != 1 || len(p.ModelTotals) != 2 {
		t.Fatalf("outcomes or freshness collapsed: %+v", p)
	}
	for _, v := range p.Tokens {
		if v.Dimension == "input" && (v.ObservedCount == nil || *v.ObservedCount != 0) {
			t.Fatalf("measured zero made unknown: %+v", v)
		}
		if v.Dimension == "output" && v.ObservedCount != nil {
			t.Fatalf("unknown output made zero: %+v", v)
		}
	}
	q := query()
	q.ActualModel = "A"
	filtered := project(t, l, q)
	if filtered.Counts.ProviderCalls != 3 || filtered.Counts.UnknownActualModel != 0 || len(filtered.ModelTotals) != 1 {
		t.Fatalf("unknown or requested aliases matched actual model: %+v", filtered.Counts)
	}
	q.ActualModel = "alias"
	if project(t, l, q).Counts.ProviderCalls != 0 {
		t.Fatal("requested model became actual model")
	}
}

func money(id string, kind MoneyKind, currency, amount string) Observation {
	var amountPointer *string
	if amount != "" {
		amountPointer = dec(amount)
	}
	m := &Money{Provider: "vendor", Namespace: "opaque-scope", RecordID: id, Context: testContext(), At: testNow, Kind: kind, Currency: currency, Decimal: amountPointer, ActualModel: "A"}
	if kind == Commitment {
		m.Cadence = "annual"
	}
	return Observation{ID: id, Source: Source{ID: "billing", Kind: "provider_api", Quality: ProviderVerified, ObservedAt: testNow, Sequence: 1, Coverage: Complete}, Money: m}
}
func TestExactMoneyCurrencyKindsAndCommitments(t *testing.T) {
	l, _ := openTest(t)
	for _, o := range []Observation{money("charge-a", Charge, "USD", "0.1"), money("charge-b", Charge, "USD", "0.2"), money("charge-eur", Charge, "EUR", "1.20"), money("estimate", Estimate, "USD", "99"), money("unknown-estimate", Estimate, "USD", ""), money("plan", Commitment, "USD", "120"), money("credit", Credit, "USD", "5"), money("topup", TopUp, "USD", "20"), money("equivalent", TokenEquivalent, "USD", "3")} {
		ingest(t, l, o)
	}
	if !ingest(t, l, money("charge-a", Charge, "USD", "0.100000000")).Duplicate {
		t.Fatal("equivalent decimal changed immutable record")
	}
	p := project(t, l, query())
	if len(p.MoneyTotals) != 7 || p.UnknownMoneyRecords != 1 {
		t.Fatalf("money categories combined: %+v", p.MoneyTotals)
	}
	for _, total := range p.MoneyTotals {
		if total.Kind == Charge && total.Currency == "USD" {
			if total.ObservedDecimal == nil || *total.ObservedDecimal != "0.3" || total.KnownRecords != 2 {
				t.Fatalf("inexact or doubled charges: %+v", total)
			}
		}
		if total.Kind == Commitment && (total.Cadence != "annual" || *total.ObservedDecimal != "120") {
			t.Fatalf("commitment converted into spend: %+v", total)
		}
		if total.Kind == Estimate && (total.UnknownRecords != 1 || *total.ObservedDecimal != "99") {
			t.Fatalf("unknown estimate became zero: %+v", total)
		}
	}
	q := query()
	q.ActualModel = "A"
	if len(project(t, l, q).Money) != 9 {
		t.Fatal("explicit actual-model money filter failed")
	}
	q.Authorization.MoneyDetails = false
	p = project(t, l, q)
	if p.MoneyVisible || len(p.Money) != 0 || len(p.MoneyTotals) != 0 || p.UnknownMoneyRecords != 0 {
		t.Fatal("money detail grant bypassed")
	}
}

func TestMoneyConflictsAndDeclaredValues(t *testing.T) {
	l, _ := openTest(t)
	a := money("invoice", Charge, "USD", "2")
	b := a
	b.ID = "another-receipt"
	b.Source.ID = "billing-replica"
	m := *a.Money
	m.Decimal = dec("3")
	b.Money = &m
	ingest(t, l, a)
	ingest(t, l, b)
	p := project(t, l, query())
	if p.ConflictedMoneyRecords != 1 || p.UnknownMoneyRecords != 1 || p.MoneyTotals[0].ObservedDecimal != nil || p.MoneyTotals[0].ConflictedRecords != 1 {
		t.Fatalf("conflicting charge summed: %+v", p)
	}
	manual := money("manual-plan", Commitment, "EUR", "88")
	manual.Source.ID = "manual"
	manual.Source.Kind = "manual"
	manual.Source.Quality = UserEntered
	ingest(t, l, manual)
	p = project(t, l, query())
	found := false
	for _, v := range p.MoneyTotals {
		if v.Currency == "EUR" {
			found = true
			if v.UserEnteredRecords != 1 {
				t.Fatal("user commitment lost source qualification")
			}
		}
	}
	if !found {
		t.Fatal("declared commitment lost")
	}
	manual = money("manual-charge", Charge, "USD", "1")
	manual.Source.ID = "manual"
	manual.Source.Kind = "manual"
	manual.Source.Quality = UserEntered
	if _, err := l.Ingest(context.Background(), manual); err == nil {
		t.Fatal("manual entry established actual charge")
	}
}

func quota(id, host string) Observation {
	ctx := testContext()
	ctx.Host = host
	return Observation{ID: id, Source: Source{ID: "capacity-" + host, Kind: "provider_api", Quality: ProviderVerified, ObservedAt: testNow, Sequence: 1, Coverage: Partial}, Quota: &Quota{Provider: "vendor", Namespace: "opaque-scope", BucketID: "shared-account-bucket", WindowID: "window-1", ScopeKind: "account", ScopeID: "verified-account", Unit: "requests", Dimensions: []string{"model:A", "tool:code"}, WindowStart: testNow.Add(-time.Hour), WindowEnd: testNow.Add(time.Hour), Limit: dec("100"), Remaining: dec("30"), Context: ctx}}
}
func TestSharedQuotaWindowsFreshnessAndPermissions(t *testing.T) {
	l, _ := openTest(t)
	a, b := quota("host-a-observation", "host-a"), quota("host-b-observation", "host-b")
	ingest(t, l, a)
	ingest(t, l, b)
	expired := quota("expired-window", "host-a")
	qv := *expired.Quota
	qv.WindowID = "prior-window"
	qv.WindowStart = testNow.Add(-2 * time.Hour)
	qv.WindowEnd = testNow.Add(-time.Hour)
	expired.Quota = &qv
	ingest(t, l, expired)
	q := query()
	p := project(t, l, q)
	if len(p.Quotas) != 1 || value(p.Quotas[0].Fields, "remaining") != "30" || value(p.Quotas[0].Fields, "unit") != "requests" || value(p.Quotas[0].Fields, "used") != "" {
		t.Fatalf("quota duplicated or unknown used invented: %+v", p.Quotas)
	}
	update := b
	update.ID = "quota-update"
	update.Source.Sequence = 2
	update.Source.ObservedAt = testNow.Add(-20 * time.Minute)
	ingest(t, l, update)
	p = project(t, l, q)
	if !p.Quotas[0].Stale {
		t.Fatal("stale peer capacity hidden")
	}
	q.Authorization = Authorization{Hosts: []string{"host-a"}}
	p = project(t, l, q)
	if len(p.Quotas) != 1 || p.Quotas[0].Stale || len(p.Quotas[0].Sources) != 1 || value(p.Quotas[0].Fields, "scope_id") != "" || value(p.Quotas[0].Fields, "account") != "" {
		t.Fatalf("unauthorized peer/account details exposed: %+v", p.Quotas)
	}
	q.Filter.Account = "account"
	if _, err := l.Project(context.Background(), q); err == nil {
		t.Fatal("restricted account filter accepted")
	}
	q = query()
	q.Authorization = Authorization{}
	p = project(t, l, q)
	if len(p.Quotas) != 0 || len(p.Attempts) != 0 || len(p.Money) != 0 {
		t.Fatal("empty authorization was global access")
	}
	manual := quota("manual", "host-a")
	manual.Source = Source{ID: "manual", Kind: "manual", Quality: UserEntered, ObservedAt: testNow, Coverage: Unknown}
	if _, err := l.Ingest(context.Background(), manual); err == nil {
		t.Fatal("user alias established shared quota")
	}
}

func TestUnknownCoveragePeriodFiltersAndDeclaredCalls(t *testing.T) {
	l, _ := openTest(t)
	a := attempt("no-measurement")
	a.Attempt.Tokens = nil
	a.Attempt.ToolCalls = nil
	a.Attempt.ActualModel = ""
	a.Attempt.LogicalRequest = ""
	a.Attempt.NativeTurn = ""
	a.Attempt.Outcome = ""
	a.Source.Coverage = Unknown
	a.Source.ObservedAt = testNow.Add(-time.Hour)
	ingest(t, l, a)
	manual := attempt("declared")
	manual.Source = Source{ID: "manual", Kind: "manual", Quality: UserEntered, ObservedAt: testNow, Coverage: Unknown}
	ingest(t, l, manual)
	q := query()
	q.ExpectedSources = []string{"stream", "missing"}
	p := project(t, l, q)
	if p.Counts.ProviderCalls != 1 || p.Counts.DeclaredCalls != 1 || p.Counts.UnknownLogicalRequests != 1 || p.Counts.UnknownActualModel != 1 || p.Counts.UnknownNativeTurns != 1 || p.Counts.UnknownTokenCalls != 1 || p.Counts.UnknownToolCalls != 1 || p.Counts.ToolCalls != nil || len(p.Tokens) != 0 || len(p.ActualModels) != 0 || p.Coverage.StaleRecords != 1 {
		t.Fatalf("unknown treated as zero/verified: %+v", p)
	}
	q.Filter.Project = "other"
	if project(t, l, q).Counts.ProviderCalls != 0 {
		t.Fatal("project filter ignored")
	}
	q = query()
	q.Filter.Task = "task"
	q.Filter.Harness = "native-cli"
	q.Provider = "vendor"
	if project(t, l, q).Counts.ProviderCalls != 1 {
		t.Fatal("contextual filter failed")
	}
	q = query()
	q.End = a.Attempt.At
	q.Start = q.End.Add(-time.Hour)
	if project(t, l, q).Counts.ProviderCalls != 0 {
		t.Fatal("period end was inclusive")
	}
	q = query()
	q.Start = a.Attempt.At
	if project(t, l, q).Counts.ProviderCalls != 1 {
		t.Fatal("period start was exclusive")
	}
	q = query()
	q.Timezone = "not/a/timezone"
	if _, err := l.Project(context.Background(), q); err == nil {
		t.Fatal("invalid timezone accepted")
	}
	q = query()
	q.MaxAge = 0
	if _, err := l.Project(context.Background(), q); err == nil {
		t.Fatal("unbounded freshness accepted")
	}
}

func TestMalformedObservationsFailBeforeCommit(t *testing.T) {
	l, _ := openTest(t)
	tests := map[string]func(*Observation){
		"missing native request": func(o *Observation) { o.Attempt.Key.RequestID = "" },
		"unknown namespace":      func(o *Observation) { o.Attempt.Key.Namespace = "unknown" },
		"terminal source":        func(o *Observation) { o.Source.Kind = "terminal_output" },
		"unqualified source":     func(o *Observation) { o.Source.Quality = "guessed" },
		"negative token":         func(o *Observation) { o.Attempt.Tokens[0].Count = num(-1) },
		"duplicate dimension":    func(o *Observation) { o.Attempt.Tokens = append(o.Attempt.Tokens, o.Attempt.Tokens[0]) },
		"inclusion cycle": func(o *Observation) {
			o.Attempt.Tokens[0].IncludedIn = "output"
			o.Attempt.Tokens[1].IncludedIn = "input"
		},
		"self retry": func(o *Observation) { o.Attempt.RetryOf = &o.Attempt.Key },
		"cross namespace child": func(o *Observation) {
			k := o.Attempt.Key
			k.Namespace = "other"
			k.AttemptID = "2"
			o.Attempt.Parent = &k
		},
		"control character":   func(o *Observation) { o.Attempt.Context.Host = "host\n" },
		"oversized dimension": func(o *Observation) { o.Attempt.Tokens[0].Dimension = strings.Repeat("a", 257) },
		"two payloads":        func(o *Observation) { o.Money = money("charge", Charge, "USD", "1").Money },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			o := attempt(name)
			mutate(&o)
			if _, err := l.Ingest(context.Background(), o); err == nil {
				t.Fatal("malformed observation committed")
			}
		})
	}
	for _, amount := range []string{"NaN", "1e3", "-1", ".1", "01", "1.1234567890", strings.Repeat("1", 19)} {
		o := money("bad-"+amount, Charge, "USD", amount)
		if _, err := l.Ingest(context.Background(), o); err == nil {
			t.Fatalf("invalid decimal %q accepted", amount)
		}
	}
	if p := project(t, l, query()); len(p.Attempts) != 0 || len(p.Money) != 0 {
		t.Fatal("failed ingestion mutated ledger")
	}
}

func TestPartialInclusionUnionAndOverflowFailClosed(t *testing.T) {
	l, _ := openTest(t)
	a := attempt("root")
	a.Attempt.Tokens = []Token{{Dimension: "input", Count: num(5), IncludedIn: "output"}}
	b := a
	b.ID = "other"
	b.Source.ID = "transcript"
	b.Source.Kind = "transcript"
	bb := *a.Attempt
	bb.Tokens = []Token{{Dimension: "output", Count: num(5), IncludedIn: "input"}}
	b.Attempt = &bb
	ingest(t, l, a)
	ingest(t, l, b)
	p := project(t, l, query())
	for _, v := range p.Attempts[0].Tokens {
		if v.Count != nil || !v.Conflict {
			t.Fatal("cross-source inclusion cycle made additive")
		}
	}
	l2, _ := openTest(t)
	for _, id := range []string{"one", "two"} {
		o := attempt(id)
		o.Attempt.Tokens[0].Count = num(math.MaxInt64)
		ingest(t, l2, o)
	}
	if _, err := l2.Project(context.Background(), query()); !errors.Is(err, ErrCapacity) {
		t.Fatalf("token overflow accepted: %v", err)
	}
}

func TestPrivateFilesAuthoritySymlinksAndRestartCorruption(t *testing.T) {
	l, path := openTest(t)
	ingest(t, l, attempt("durable"))
	if _, err := Open(path); err == nil {
		t.Fatal("second cooperating authority opened")
	}
	for _, p := range []string{path, path + ".lock"} {
		info, err := os.Stat(p)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("nonprivate ledger: %s %v", p, err)
		}
	}
	if _, err := Open("relative.db"); err == nil {
		t.Fatal("relative path accepted")
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Ingest(context.Background(), attempt("closed")); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if _, err := l.Project(context.Background(), query()); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(root, "private")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(private, "usage.db")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(link); err == nil {
		t.Fatal("symlink database followed")
	}
	body, _ := os.ReadFile(target)
	if string(body) != "unchanged" {
		t.Fatal("symlink target mutated")
	}
	fifo := filepath.Join(private, "fifo.db")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(fifo); err == nil {
		t.Fatal("nonregular database accepted")
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.db.Exec(`UPDATE usage_observations SET data='{}'`); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Project(context.Background(), query()); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("corrupt active data accepted: %v", err)
	}
	reopened.Close()
	if _, err := Open(path); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("corrupt restart accepted: %v", err)
	}
}

func TestRecordBoundAndConcurrentReplay(t *testing.T) {
	l, _ := openTest(t)
	o := attempt("concurrent")
	var wg sync.WaitGroup
	errCh := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := l.Ingest(context.Background(), o); errCh <- err }()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if p := project(t, l, query()); p.Counts.ProviderCalls != 1 {
		t.Fatal("concurrent replay inflated calls")
	}
	for i := 2; i <= MaxAttemptObservations; i++ {
		next := o
		next.Source.Sequence = uint64(i)
		next.ID = "snapshot-" + stringJSON(i)
		ingest(t, l, next)
	}
	next := o
	next.ID = "capacity-overflow"
	next.Source.Sequence = MaxAttemptObservations + 1
	if _, err := l.Ingest(context.Background(), next); !errors.Is(err, ErrCapacity) {
		t.Fatalf("unbounded attempt snapshots: %v", err)
	}
	if !ingest(t, l, o).Duplicate {
		t.Fatal("capacity blocked an idempotent replay")
	}
}

func TestSourceIdentityCannotUpgradeManualAuthority(t *testing.T) {
	l, _ := openTest(t)
	o := attempt("manual")
	o.Source = Source{ID: "fixed-source", Kind: "manual", Quality: UserEntered, ObservedAt: testNow, Coverage: Partial, Sequence: 1}
	ingest(t, l, o)
	upgrade := attempt("upgrade")
	upgrade.Source.ID = o.Source.ID
	if _, err := l.Ingest(context.Background(), upgrade); !errors.Is(err, ErrConflict) {
		t.Fatalf("source authority changed: %v", err)
	}
}

func TestProjectionContainsNoTranscriptAndStableJSON(t *testing.T) {
	l, _ := openTest(t)
	ingest(t, l, attempt("native"))
	q := query()
	q.Authorization = Authorization{Sessions: []string{"session"}}
	p := project(t, l, q)
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"account"`) || strings.Contains(string(data), `"subscription"`) {
		t.Fatalf("account detail leaked: %s", data)
	}
	if p.Counts.ToolCalls == nil || *p.Counts.ToolCalls != 0 {
		t.Fatal("measured zero lost")
	}
	if p.Counts.ProviderCalls != 1 {
		t.Fatal("usage incorrectly requires transcript access")
	}
}
