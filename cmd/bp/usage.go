package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/seanheiney/keelmux/internal/config"
	"github.com/seanheiney/keelmux/internal/usage"
)

const usageHelp = `Usage: keelmux [--state DIR] usage [view] [filters]
       keelmux [--state DIR] usage import FILE|-

Inspect the local explicit-observation ledger without starting a daemon.
An absent ledger produces an empty view with unknown observation coverage.
Import accepts a bounded version-1 JSON document containing observations.
Only manual, user-entered attempt/estimate/commitment/credit records are allowed;
imports cannot establish provider authority, actual charges or shared quota.

View filters:
  --host ID             Exact execution host
  --session ID          Exact product session
  --project ID          Exact project
  --task ID             Exact task
  --harness NAME        Exact harness
  --provider NAME       Exact provider
  --model NAME          Trusted actual model; requested aliases stay separate
  --from RFC3339        Inclusive absolute start (default: 24 hours before now)
  --to RFC3339          Exclusive absolute end (default: now)
  --timezone NAME       Display timezone (default: UTC)
  --freshness DURATION  Age beyond which source evidence is stale (default: 15m)
  --expected-source ID  Expected original source; may repeat
  --accounts            Include local account/subscription detail
  --money               Include local money detail, separate by kind and currency
  --json                Emit the complete qualified projection as JSON

Remote entry, native RPC, credentials, billing controls and model calls are
outside this local command. Manual records remain separate from observed calls.
`

const usageImportBytes = 1 << 20
const manualUsagePrefix = "manual_import:"

type usageViewOutput struct {
	LedgerPresent bool             `json:"ledger_present"`
	Notice        string           `json:"notice"`
	Projection    usage.Projection `json:"projection"`
}
type usageImportDocument struct {
	Version      int                 `json:"version"`
	Observations []usage.Observation `json:"observations"`
}
type usageImportOutput struct {
	Imported      int            `json:"imported"`
	Duplicates    int            `json:"duplicates"`
	Provenance    usage.Quality  `json:"provenance"`
	ScopeCoverage usage.Coverage `json:"scope_coverage"`
}
type usageSources []string

func (s *usageSources) String() string     { return strings.Join(*s, ",") }
func (s *usageSources) Set(v string) error { *s = append(*s, v); return nil }

// usageCommand must be dispatched before config.Ensure/ensureDaemon in main.go.
// This local same-user command grants its own filesystem view; these projection
// flags are not a security boundary or authorization for a remote principal.
func usageCommand(stateDir, entry string, args []string) error {
	return usageRun(context.Background(), stateDir, entry, args, os.Stdin, os.Stdout, time.Now())
}
func usageRun(ctx context.Context, stateDir, entry string, args []string, in io.Reader, out io.Writer, now time.Time) error {
	if len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		_, err := io.WriteString(out, usageHelp)
		return err
	}
	if entry != "" {
		return errors.New("usage reads and imports belong to the local execution home; remote entry is unsupported")
	}
	if len(args) > 0 && args[0] == "import" {
		if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
			_, err := io.WriteString(out, usageHelp)
			return err
		}
		if len(args) != 2 {
			return errors.New("usage import requires one JSON file or - for stdin")
		}
		return usageImport(ctx, stateDir, args[1], in, out)
	}
	if len(args) > 0 && args[0] == "view" {
		args = args[1:]
	}
	return usageView(ctx, stateDir, args, out, now)
}
func usagePath(stateDir string) (string, error) {
	if stateDir == "" {
		p, err := config.Resolve("")
		if err != nil {
			return "", err
		}
		stateDir = p.Dir
	}
	abs, err := filepath.Abs(stateDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(abs, "usage.db"), nil
}
func usageView(ctx context.Context, stateDir string, args []string, out io.Writer, now time.Time) error {
	fs := flag.NewFlagSet("usage view", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() { _, _ = io.WriteString(out, usageHelp) }
	host := fs.String("host", "", "execution host")
	session := fs.String("session", "", "product session")
	project := fs.String("project", "", "project")
	task := fs.String("task", "", "task")
	harness := fs.String("harness", "", "harness")
	provider := fs.String("provider", "", "provider")
	model := fs.String("model", "", "actual model")
	from := fs.String("from", "", "inclusive RFC3339 start")
	to := fs.String("to", "", "exclusive RFC3339 end")
	zone := fs.String("timezone", "UTC", "display timezone")
	age := fs.Duration("freshness", 15*time.Minute, "maximum evidence age")
	accounts := fs.Bool("accounts", false, "show local account detail")
	money := fs.Bool("money", false, "show local money detail")
	asJSON := fs.Bool("json", false, "emit complete projection")
	var expected usageSources
	fs.Var(&expected, "expected-source", "expected original source ID")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if len(fs.Args()) != 0 {
		return errors.New("usage view accepts only filters; use usage import FILE for explicit import")
	}
	start, end := now.Add(-24*time.Hour), now
	if *from != "" {
		var err error
		start, err = time.Parse(time.RFC3339, *from)
		if err != nil {
			return errors.New("--from must be an absolute RFC3339 timestamp")
		}
	}
	if *to != "" {
		var err error
		end, err = time.Parse(time.RFC3339, *to)
		if err != nil {
			return errors.New("--to must be an absolute RFC3339 timestamp")
		}
	}
	q := usage.Query{Start: start, End: end, Timezone: *zone, Now: now, MaxAge: *age, Filter: usage.Context{Host: *host, Session: *session, Project: *project, Task: *task, Harness: *harness}, Provider: *provider, ActualModel: *model, ExpectedSources: expected, Authorization: usage.Authorization{AllContexts: true, AccountDetails: *accounts, MoneyDetails: *money}}
	path, err := usagePath(stateDir)
	if err != nil {
		return err
	}
	p, present, err := usage.ReadProjection(ctx, path, q)
	if err != nil {
		return err
	}
	result := usageViewOutput{LedgerPresent: present, Notice: "Observation coverage is unknown; measured totals cover the observed subset. User-entered records do not establish provider usage.", Projection: p}
	if *asJSON {
		return writeUsageJSON(out, result)
	}
	return writeUsageView(out, result)
}

func writeUsageJSON(out io.Writer, v any) error {
	e := json.NewEncoder(out)
	e.SetIndent("", "  ")
	return e.Encode(v)
}
func writeUsageView(out io.Writer, v usageViewOutput) error {
	p := v.Projection
	loc, err := time.LoadLocation(p.Timezone)
	if err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Usage · %s to %s (%s; end exclusive)\n", p.Start.In(loc).Format(time.RFC3339), p.End.In(loc).Format(time.RFC3339), p.Timezone)
	if !v.LedgerPresent {
		b.WriteString("No usage ledger exists. No state was created.\n")
	}
	b.WriteString(v.Notice + "\n")
	fmt.Fprintf(&b, "Observed provider calls: %d · user-entered calls: %d\n", p.Counts.ProviderCalls, p.Counts.DeclaredCalls)
	fmt.Fprintf(&b, "Known logical requests: %d (%d calls unattributed) · native turns: %d (%d calls unattributed)\n", p.Counts.LogicalRequests, p.Counts.UnknownLogicalRequests, p.Counts.NativeTurns, p.Counts.UnknownNativeTurns)
	fmt.Fprintf(&b, "Retries: %d · child calls: %d · succeeded: %d · failed: %d · uncertain: %d · outcome unknown: %d\n", p.Counts.Retries, p.Counts.ChildCalls, p.Counts.Succeeded, p.Counts.Failed, p.Counts.Uncertain, p.Counts.UnknownOutcome)
	tools := "unknown"
	if p.Counts.ToolCalls != nil {
		tools = fmt.Sprint(*p.Counts.ToolCalls)
	}
	fmt.Fprintf(&b, "Measured tool calls: %s (%d calls have unknown tool usage)\n", tools, p.Counts.UnknownToolCalls)
	if len(p.ActualModels) == 0 {
		b.WriteString("Actual models observed: unknown\n")
	} else {
		fmt.Fprintf(&b, "Actual models observed: %s (%d calls have unknown actual model)\n", strings.Join(p.ActualModels, ", "), p.Counts.UnknownActualModel)
	}
	fmt.Fprintf(&b, "Record coverage: %d source-claimed complete · %d partial · %d unknown · %d conflicted\n", p.Coverage.CompleteRecords, p.Coverage.PartialRecords, p.Coverage.UnknownRecords, p.Coverage.ConflictedRecords)
	staleMoney, staleQuota := 0, 0
	for _, m := range p.Money {
		if m.Stale {
			staleMoney++
		}
	}
	for _, q := range p.Quotas {
		if q.Stale {
			staleQuota++
		}
	}
	fmt.Fprintf(&b, "Source freshness: %d attempts, %d money records, %d quota windows stale or future-dated\n", p.Coverage.StaleRecords, staleMoney, staleQuota)
	if len(p.Coverage.MissingSources) > 0 {
		fmt.Fprintf(&b, "Missing sources: %s\n", strings.Join(p.Coverage.MissingSources, ", "))
	}
	if len(p.Tokens) == 0 {
		b.WriteString("Token usage: unknown (no measured token dimensions)\n")
	}
	for _, t := range p.Tokens {
		count := "unknown"
		if t.ObservedCount != nil {
			count = fmt.Sprint(*t.ObservedCount)
		}
		included := ""
		if t.IncludedIn != "" {
			included = "; included in " + t.IncludedIn
		}
		model := t.ActualModel
		if model == "" {
			model = "unknown actual model"
		}
		fmt.Fprintf(&b, "Tokens %s/%s %s: %s (%d known, %d unknown, %d conflicted attempts%s)\n", t.Provider, model, t.Dimension, count, t.KnownAttempts, t.UnknownAttempts, t.ConflictedAttempts, included)
	}
	if !p.MoneyVisible {
		b.WriteString("Money detail hidden; add --money to inspect local observations.\n")
	} else if len(p.Money) == 0 {
		b.WriteString("Money: unknown (no observations in this period)\n")
	}
	for _, m := range p.MoneyTotals {
		amount := "unknown"
		if m.ObservedDecimal != nil {
			amount = *m.ObservedDecimal
		}
		cadence := ""
		if m.Cadence != "" {
			cadence = " " + m.Cadence
		}
		fmt.Fprintf(&b, "Money %s %s%s: %s (%d known, %d unknown, %d conflicted, %d user-entered records; coverage unknown)\n", m.Currency, m.Kind, cadence, amount, m.KnownRecords, m.UnknownRecords, m.ConflictedRecords, m.UserEnteredRecords)
	}
	if p.ConflictedMoneyRecords > 0 {
		fmt.Fprintf(&b, "Money conflicts: %d records require source reconciliation\n", p.ConflictedMoneyRecords)
	}
	fmt.Fprintf(&b, "Current native quota windows: %d (capacity is separate from spend; stale or conflicting evidence cannot establish readiness)\n", len(p.Quotas))
	_, err = io.WriteString(out, b.String())
	return err
}

func usageImport(ctx context.Context, stateDir, file string, in io.Reader, out io.Writer) error {
	var reader io.Reader = in
	if file != "-" {
		fd, err := syscall.Open(file, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
		if err != nil {
			return err
		}
		f := os.NewFile(uintptr(fd), file)
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("usage import requires a regular JSON file or stdin")
		}
		if info.Size() > usageImportBytes {
			return errors.New("usage import exceeds the 1 MiB input limit")
		}
		reader = f
	}
	observations, err := decodeManualUsage(reader)
	if err != nil {
		return err
	}
	path, err := usagePath(stateDir)
	if err != nil {
		return err
	}
	l, err := usage.Open(path)
	if err != nil {
		return err
	}
	results, err := l.IngestBatch(ctx, observations)
	closeErr := l.Close()
	if err != nil {
		return fmt.Errorf("usage import rejected atomically: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("usage import committed but ledger close failed: %w", closeErr)
	}
	result := usageImportOutput{Provenance: usage.UserEntered, ScopeCoverage: usage.Unknown}
	for _, r := range results {
		if r.Duplicate {
			result.Duplicates++
		} else {
			result.Imported++
		}
	}
	if err := writeUsageJSON(out, result); err != nil {
		return fmt.Errorf("usage import committed; output failed (replaying identical input is safe): %w", err)
	}
	return nil
}

func decodeManualUsage(in io.Reader) ([]usage.Observation, error) {
	if in == nil {
		return nil, errors.New("usage import requires a JSON input stream")
	}
	data, err := io.ReadAll(io.LimitReader(in, usageImportBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > usageImportBytes {
		return nil, errors.New("usage import exceeds the 1 MiB input limit")
	}
	if !utf8.Valid(data) {
		return nil, errors.New("usage import JSON must be valid UTF-8")
	}
	if err := usageStrictJSON(data); err != nil {
		return nil, err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	var document usageImportDocument
	if err := d.Decode(&document); err != nil {
		return nil, fmt.Errorf("invalid usage import JSON: %w", err)
	}
	if document.Version != 1 {
		return nil, errors.New("usage import requires version 1")
	}
	if len(document.Observations) == 0 || len(document.Observations) > usage.MaxBatchObservations {
		return nil, errors.New("usage import requires 1 to 128 observations")
	}
	for i, o := range document.Observations {
		if o.Source.Kind != "" && o.Source.Kind != "manual" {
			return nil, errors.New("usage import accepts only manual sources")
		}
		if o.Source.Quality != "" && o.Source.Quality != usage.UserEntered {
			return nil, errors.New("usage import cannot establish provider-verified or adapter-observed authority")
		}
		if o.Quota != nil {
			return nil, errors.New("usage import cannot establish a shared provider quota bucket")
		}
		o.Source.Kind = "manual"
		o.Source.Quality = usage.UserEntered
		o.Source.Coverage = usage.Unknown
		if err := o.Validate(); err != nil {
			return nil, fmt.Errorf("invalid manual usage observation %d: %w", i+1, err)
		}
		o.Source.ID = manualUsagePrefix + usageOpaque(o.Source.ID)
		if o.Attempt != nil {
			a := *o.Attempt
			a.Key = manualUsageKey(a.Key)
			if a.Parent != nil {
				k := manualUsageKey(*a.Parent)
				a.Parent = &k
			}
			if a.RetryOf != nil {
				k := manualUsageKey(*a.RetryOf)
				a.RetryOf = &k
			}
			o.Attempt = &a
		}
		if o.Money != nil {
			m := *o.Money
			m.Namespace = manualUsagePrefix + usageOpaque(m.Provider+"\x00"+m.Namespace)
			if m.Attempt != nil {
				k := manualUsageKey(*m.Attempt)
				m.Attempt = &k
			}
			o.Money = &m
		}
		document.Observations[i] = o
	}
	if err := usage.ValidateBatch(document.Observations); err != nil {
		return nil, err
	}
	return document.Observations, nil
}
func usageOpaque(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func manualUsageKey(k usage.AttemptKey) usage.AttemptKey {
	k.Namespace = manualUsagePrefix + usageOpaque(k.Provider+"\x00"+k.Namespace)
	return k
}

// encoding/json's struct decoder alone accepts duplicate keys. Reject them,
// concatenated documents, and excessive nesting before decoding typed records.
func usageStrictJSON(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 64 {
			return errors.New("usage import JSON nesting exceeds 64 levels")
		}
		token, err := d.Token()
		if err != nil {
			return errors.New("invalid usage import JSON")
		}
		delimiter, compound := token.(json.Delim)
		if !compound {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return errors.New("invalid usage import JSON")
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return errors.New("usage import JSON contains duplicate object keys")
				}
				seen[name] = true
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid usage import JSON")
		}
		closing, err := d.Token()
		if err != nil {
			return errors.New("invalid usage import JSON")
		}
		if delimiter == '{' && closing != json.Delim('}') || delimiter == '[' && closing != json.Delim(']') {
			return errors.New("invalid usage import JSON")
		}
		return nil
	}
	if err := walk(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("usage import accepts exactly one JSON document")
	}
	return nil
}
