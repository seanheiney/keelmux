package usage

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"sort"
	"strings"
	"time"
)

// Authorization is supplied by the caller after its current grant checks.
// Empty scope denies all records. Hosts and Sessions form an explicit allowlist
// union. Account and money visibility are independent of usage visibility.
type Authorization struct {
	AllContexts    bool     `json:"all_contexts"`
	Hosts          []string `json:"hosts,omitempty"`
	Sessions       []string `json:"sessions,omitempty"`
	AccountDetails bool     `json:"account_details"`
	MoneyDetails   bool     `json:"money_details"`
}
type Query struct {
	Start           time.Time     `json:"start"`
	End             time.Time     `json:"end"`
	Timezone        string        `json:"timezone"`
	Now             time.Time     `json:"now"`
	MaxAge          time.Duration `json:"max_age"`
	Filter          Context       `json:"filter"`
	Provider        string        `json:"provider,omitempty"`
	ActualModel     string        `json:"actual_model,omitempty"`
	Authorization   Authorization `json:"authorization"`
	ExpectedSources []string      `json:"expected_sources,omitempty"`
}

// Value retains provenance and conflicts. Nil never means measured zero.
type Value struct {
	Value      *string   `json:"value"`
	Quality    Quality   `json:"quality,omitempty"`
	SourceIDs  []string  `json:"source_ids"`
	ObservedAt time.Time `json:"observed_at,omitempty"`
	Conflicts  []string  `json:"conflicts,omitempty"`
}
type TokenValue struct {
	Dimension  string    `json:"dimension"`
	Count      *int64    `json:"count"`
	IncludedIn string    `json:"included_in,omitempty"`
	Conflict   bool      `json:"conflict"`
	Quality    Quality   `json:"quality,omitempty"`
	Conflicts  []Token   `json:"conflicts,omitempty"`
	SourceIDs  []string  `json:"source_ids"`
	ObservedAt time.Time `json:"observed_at,omitempty"`
}
type AttemptView struct {
	Key        AttemptKey       `json:"key"`
	Fields     map[string]Value `json:"fields"`
	Tokens     []TokenValue     `json:"tokens"`
	Sources    []Source         `json:"sources"`
	Coverage   Coverage         `json:"coverage"`
	Observed   bool             `json:"observed"`
	Stale      bool             `json:"stale"`
	Conflicted bool             `json:"conflicted"`
	Final      bool             `json:"final"`
}
type Counts struct {
	ProviderCalls          int    `json:"provider_calls"`
	DeclaredCalls          int    `json:"declared_calls"`
	LogicalRequests        int    `json:"logical_requests"`
	UnknownLogicalRequests int    `json:"unknown_logical_requests"`
	NativeTurns            int    `json:"native_turns"`
	UnknownNativeTurns     int    `json:"unknown_native_turns"`
	Retries                int    `json:"retries"`
	ChildCalls             int    `json:"child_calls"`
	Succeeded              int    `json:"succeeded"`
	Failed                 int    `json:"failed"`
	Uncertain              int    `json:"uncertain"`
	UnknownOutcome         int    `json:"unknown_outcome"`
	UnknownActualModel     int    `json:"unknown_actual_model"`
	ToolCalls              *int64 `json:"tool_calls"`
	UnknownToolCalls       int    `json:"unknown_tool_calls"`
	UnknownTokenCalls      int    `json:"unknown_token_calls"`
}
type TokenTotal struct {
	Provider           string `json:"provider"`
	ActualModel        string `json:"actual_model,omitempty"`
	Dimension          string `json:"dimension"`
	IncludedIn         string `json:"included_in,omitempty"`
	ObservedCount      *int64 `json:"observed_count"`
	KnownAttempts      int    `json:"known_attempts"`
	UnknownAttempts    int    `json:"unknown_attempts"`
	ConflictedAttempts int    `json:"conflicted_attempts"`
}
type ModelTotal struct {
	Provider    string `json:"provider"`
	ActualModel string `json:"actual_model,omitempty"`
	Counts      Counts `json:"counts"`
}
type MoneyView struct {
	Provider string           `json:"provider"`
	RecordID string           `json:"record_id"`
	Fields   map[string]Value `json:"fields"`
	Sources  []Source         `json:"sources"`
	Stale    bool             `json:"stale"`
}
type MoneyTotal struct {
	Kind               MoneyKind `json:"kind"`
	Currency           string    `json:"currency"`
	Cadence            string    `json:"cadence,omitempty"`
	ObservedDecimal    *string   `json:"observed_decimal"`
	KnownRecords       int       `json:"known_records"`
	UnknownRecords     int       `json:"unknown_records"`
	ConflictedRecords  int       `json:"conflicted_records"`
	UserEnteredRecords int       `json:"user_entered_records"`
	Coverage           Coverage  `json:"coverage"`
}
type QuotaView struct {
	Provider string           `json:"provider"`
	BucketID string           `json:"bucket_id"`
	WindowID string           `json:"window_id"`
	Fields   map[string]Value `json:"fields"`
	Sources  []Source         `json:"sources"`
	Stale    bool             `json:"stale"`
}
type CoverageSummary struct {
	// Scope is deliberately unknown: observing calls does not establish a full
	// period/provider ledger or coverage of other devices and unrelated clients.
	Scope             Coverage `json:"scope"`
	CompleteRecords   int      `json:"complete_records"`
	PartialRecords    int      `json:"partial_records"`
	UnknownRecords    int      `json:"unknown_records"`
	ConflictedRecords int      `json:"conflicted_records"`
	StaleRecords      int      `json:"stale_records"`
	MissingSources    []string `json:"missing_sources"`
}
type Projection struct {
	Start                  time.Time       `json:"start"`
	End                    time.Time       `json:"end"`
	Timezone               string          `json:"timezone"`
	Counts                 Counts          `json:"counts"`
	ActualModels           []string        `json:"actual_models"`
	ModelTotals            []ModelTotal    `json:"model_totals"`
	Attempts               []AttemptView   `json:"attempts"`
	Tokens                 []TokenTotal    `json:"tokens"`
	Money                  []MoneyView     `json:"money"`
	MoneyTotals            []MoneyTotal    `json:"money_totals"`
	Quotas                 []QuotaView     `json:"quotas"`
	Coverage               CoverageSummary `json:"coverage"`
	MoneyVisible           bool            `json:"money_visible"`
	UnknownMoneyRecords    int             `json:"unknown_money_records"`
	ConflictedMoneyRecords int             `json:"conflicted_money_records"`
}

func (q Query) validate() error {
	if !validTime(q.Start) || !validTime(q.End) || !q.End.After(q.Start) || !validTime(q.Now) || q.MaxAge <= 0 || q.MaxAge > 365*24*time.Hour || !q.Filter.valid() || !ref(q.Provider, false) || !ref(q.ActualModel, false) {
		return errors.New("invalid usage projection query")
	}
	if _, err := time.LoadLocation(q.Timezone); err != nil || q.Timezone == "" {
		return errors.New("invalid usage timezone")
	}
	if !q.Authorization.AccountDetails && (q.Filter.Account != "" || q.Filter.Subscription != "") {
		return errors.New("account detail grant required for account filters")
	}
	if len(q.ExpectedSources) > 128 || len(q.Authorization.Hosts) > 128 || len(q.Authorization.Sessions) > 1024 {
		return ErrCapacity
	}
	for _, list := range [][]string{q.ExpectedSources, q.Authorization.Hosts, q.Authorization.Sessions} {
		for _, s := range list {
			if !ref(s, true) {
				return errors.New("invalid projection scope")
			}
		}
	}
	return nil
}
func allowed(c Context, a Authorization) bool {
	if a.AllContexts {
		return true
	}
	for _, v := range a.Hosts {
		if c.Host == v {
			return true
		}
	}
	for _, v := range a.Sessions {
		if c.Session == v {
			return true
		}
	}
	return false
}
func contextOf(o Observation) Context {
	if o.Attempt != nil {
		return o.Attempt.Context
	}
	if o.Money != nil {
		return o.Money.Context
	}
	return o.Quota.Context
}
func inPeriod(t time.Time, q Query) bool { return !t.Before(q.Start) && t.Before(q.End) }
func stringJSON(v any) string            { b, _ := json.Marshal(v); return string(b) }

// Empty attribution is omitted/enrichable. An unavailable current measurement
// supersedes the previous value from that original source.
const explicitUnknown = "\x00"

func fields(o Observation) map[string]string {
	f := contextOf(o).values()
	if a := o.Attempt; a != nil {
		f["at"] = a.At.Format(time.RFC3339Nano)
		f["logical_request"] = a.LogicalRequest
		f["native_thread"] = a.NativeThread
		f["native_turn"] = a.NativeTurn
		f["requested_model"] = a.RequestedModel
		f["actual_model"] = a.ActualModel
		f["outcome"] = a.Outcome
		if a.RetryOf != nil {
			f["retry_of"] = stringJSON(a.RetryOf)
		}
		if a.Parent != nil {
			f["parent"] = stringJSON(a.Parent)
		}
		if a.ToolCalls != nil {
			f["tool_calls"] = stringJSON(*a.ToolCalls)
		}
	}
	if m := o.Money; m != nil {
		f["at"] = m.At.Format(time.RFC3339Nano)
		f["kind"] = string(m.Kind)
		f["currency"] = m.Currency
		f["cadence"] = m.Cadence
		f["actual_model"] = m.ActualModel
		if m.Decimal != nil {
			f["decimal"] = *m.Decimal
		} else {
			f["decimal"] = explicitUnknown
		}
		if m.Attempt != nil {
			f["attempt"] = stringJSON(m.Attempt)
		}
	}
	if b := o.Quota; b != nil {
		f["scope_kind"] = b.ScopeKind
		f["scope_id"] = b.ScopeID
		f["unit"] = b.Unit
		f["dimensions"] = stringJSON(b.Dimensions)
		f["window_start"] = b.WindowStart.Format(time.RFC3339Nano)
		f["window_end"] = b.WindowEnd.Format(time.RFC3339Nano)
		if b.ResetAt != nil {
			f["reset_at"] = b.ResetAt.Format(time.RFC3339Nano)
		}
		if b.Limit != nil {
			f["limit"] = *b.Limit
		} else {
			f["limit"] = explicitUnknown
		}
		if b.Used != nil {
			f["used"] = *b.Used
		} else {
			f["used"] = explicitUnknown
		}
		if b.Remaining != nil {
			f["remaining"] = *b.Remaining
		} else {
			f["remaining"] = explicitUnknown
		}
		delete(f, "host")
		delete(f, "harness")
		delete(f, "harness_version")
	}
	return f
}

// The latest nonempty claim per original source enriches earlier partial
// snapshots. Conflicting current sources have no chosen value. Ingestion order,
// wall-clock ties, source quality rankings, and peer count cannot decide truth.
func resolve(group []Observation, extract func(Observation) map[string]string) map[string]Value {
	type claim struct {
		text   string
		source Source
	}
	byField := map[string]map[string]claim{}
	for _, o := range group {
		for name, value := range extract(o) {
			if value == "" {
				continue
			}
			if byField[name] == nil {
				byField[name] = map[string]claim{}
			}
			old, ok := byField[name][o.Source.ID]
			if !ok || o.Source.Sequence > old.source.Sequence {
				byField[name][o.Source.ID] = claim{value, o.Source}
			}
		}
	}
	out := map[string]Value{}
	for name, claims := range byField {
		v := Value{SourceIDs: []string{}}
		distinct := map[string]bool{}
		v.Quality = ProviderVerified
		for id, c := range claims {
			distinct[c.text] = true
			v.SourceIDs = append(v.SourceIDs, id)
			if c.source.ObservedAt.After(v.ObservedAt) {
				v.ObservedAt = c.source.ObservedAt
			}
			if c.source.Quality == UserEntered {
				v.Quality = UserEntered
			} else if c.source.Quality == AdapterObserved && v.Quality != UserEntered {
				v.Quality = AdapterObserved
			}
		}
		sort.Strings(v.SourceIDs)
		values := keys(distinct)
		if len(values) == 1 && values[0] != explicitUnknown {
			s := values[0]
			v.Value = &s
		} else if len(values) > 1 {
			for i, s := range values {
				if s == explicitUnknown {
					values[i] = "unknown"
				}
			}
			sort.Strings(values)
			v.Conflicts = values
		}
		out[name] = v
	}
	return out
}
func sources(group []Observation) []Source {
	m := map[string]Source{}
	for _, o := range group {
		old, ok := m[o.Source.ID]
		if !ok || o.Source.Sequence > old.Sequence {
			m[o.Source.ID] = o.Source
		}
	}
	out := []Source{}
	for _, s := range m {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func stale(src []Source, q Query) bool {
	for _, s := range src {
		if s.ObservedAt.After(q.Now) || q.Now.Sub(s.ObservedAt) > q.MaxAge {
			return true
		}
	}
	return false
}
func value(f map[string]Value, k string) string {
	v := f[k]
	if v.Value == nil {
		return ""
	}
	return *v.Value
}
func trusted(f map[string]Value, k string) string {
	v := f[k]
	if v.Quality == UserEntered {
		return ""
	}
	return value(f, k)
}
func matches(f map[string]Value, provider string, q Query) bool {
	if q.Provider != "" && q.Provider != provider {
		return false
	}
	for k, want := range q.Filter.values() {
		if want != "" && value(f, k) != want {
			return false
		}
	}
	return q.ActualModel == "" || trusted(f, "actual_model") == q.ActualModel
}
func redact(f map[string]Value, a Authorization) {
	if !a.AccountDetails {
		delete(f, "account")
		delete(f, "subscription")
		delete(f, "scope_id")
	}
}
func keys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func tokenFields(o Observation) map[string]string {
	f := map[string]string{}
	if o.Attempt != nil {
		for _, t := range o.Attempt.Tokens {
			f[t.Dimension] = stringJSON(t)
		}
	}
	return f
}
func tokenValues(group []Observation) []TokenValue {
	// Final measurements supersede live cumulative snapshots across sources.
	final := []Observation{}
	for _, o := range group {
		if o.Attempt.Final && o.Source.Quality != UserEntered {
			final = append(final, o)
		}
	}
	if len(final) > 0 {
		group = final
	}
	f := resolve(group, tokenFields)
	dimensions := map[string]bool{}
	for _, o := range group {
		for _, t := range o.Attempt.Tokens {
			dimensions[t.Dimension] = true
		}
	}
	out := []TokenValue{}
	for _, d := range keys(dimensions) {
		v := f[d]
		t := TokenValue{Dimension: d, Conflict: len(v.Conflicts) > 0, Quality: v.Quality, SourceIDs: v.SourceIDs, ObservedAt: v.ObservedAt}
		for _, raw := range v.Conflicts {
			var tok Token
			_ = json.Unmarshal([]byte(raw), &tok)
			t.Conflicts = append(t.Conflicts, tok)
		}
		if v.Value != nil && v.Quality != UserEntered {
			var raw Token
			_ = json.Unmarshal([]byte(*v.Value), &raw)
			t.Count = raw.Count
			t.IncludedIn = raw.IncludedIn
		}
		out = append(out, t)
	}
	// A union of individually valid partial snapshots can form an inclusion
	// cycle or claim a subset larger than its known parent. Fail those dimensions
	// closed rather than offering a false additive count.
	index := map[string]int{}
	for i, t := range out {
		index[t.Dimension] = i
	}
	bad := map[int]bool{}
	for i, t := range out {
		seen := map[string]bool{t.Dimension: true}
		p := t.IncludedIn
		for p != "" {
			j, ok := index[p]
			if !ok {
				break
			}
			if seen[p] {
				for name := range seen {
					bad[index[name]] = true
				}
				break
			}
			seen[p] = true
			if t.Count != nil && out[j].Count != nil && *t.Count > *out[j].Count {
				bad[i] = true
				bad[j] = true
			}
			p = out[j].IncludedIn
		}
	}
	for i := range bad {
		out[i].Count = nil
		out[i].Conflict = true
	}
	return out
}

func (l *Ledger) Project(ctx context.Context, q Query) (Projection, error) {
	if err := q.validate(); err != nil {
		return Projection{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.db == nil {
		return Projection{}, ErrClosed
	}
	observations, err := l.observations(ctx)
	if err != nil {
		return Projection{}, err
	}
	p := Projection{Start: q.Start.UTC(), End: q.End.UTC(), Timezone: q.Timezone, ActualModels: []string{}, ModelTotals: []ModelTotal{}, Attempts: []AttemptView{}, Tokens: []TokenTotal{}, Money: []MoneyView{}, MoneyTotals: []MoneyTotal{}, Quotas: []QuotaView{}, Coverage: CoverageSummary{Scope: Unknown, MissingSources: []string{}}, MoneyVisible: q.Authorization.MoneyDetails}
	groups := map[string][]Observation{}
	for _, o := range observations {
		if !allowed(contextOf(o), q.Authorization) {
			continue
		}
		kind, key := canonical(o)
		if kind == "money" && !q.Authorization.MoneyDetails {
			continue
		}
		groups[kind+":"+key] = append(groups[kind+":"+key], o)
	}
	logical, turns, models, seenSources := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	totals := map[string]*TokenTotal{}
	modelCalls := map[string]int{}
	modelTotals := map[string]*ModelTotal{}
	modelLogical := map[string]map[string]bool{}
	modelTurns := map[string]map[string]bool{}
	moneyTotals := map[string]*MoneyTotal{}
	moneySums := map[string]*big.Int{}
	for _, key := range keys(groups) {
		group := groups[key]
		first := group[0]
		f := resolve(group, fields)
		src := sources(group)
		isStale := stale(src, q)
		if a := first.Attempt; a != nil {
			period := false
			for _, o := range group {
				if inPeriod(o.Attempt.At, q) {
					period = true
				}
			}
			if !period || !matches(f, a.Key.Provider, q) {
				continue
			}
			v := AttemptView{Key: a.Key, Fields: f, Tokens: tokenValues(group), Sources: src, Coverage: Unknown, Stale: isStale}
			for _, s := range src {
				if s.Quality != UserEntered {
					v.Observed = true
				}
				if s.Coverage == Complete {
					v.Coverage = Complete
				} else if s.Coverage == Partial && v.Coverage == Unknown {
					v.Coverage = Partial
				}
				seenSources[s.ID] = true
			}
			for _, o := range group {
				if o.Attempt.Final && o.Source.Quality != UserEntered {
					v.Final = true
				}
			}
			for _, e := range f {
				if len(e.Conflicts) > 0 {
					v.Conflicted = true
				}
			}
			for _, t := range v.Tokens {
				if t.Conflict {
					v.Conflicted = true
				}
			}
			if v.Conflicted {
				v.Coverage = Partial
				p.Coverage.ConflictedRecords++
			}
			if isStale {
				p.Coverage.StaleRecords++
			}
			switch v.Coverage {
			case Complete:
				p.Coverage.CompleteRecords++
			case Partial:
				p.Coverage.PartialRecords++
			default:
				p.Coverage.UnknownRecords++
			}
			if v.Observed {
				if err := countModel(&p.Counts, f, a.Key, logical, turns, len(v.Tokens) == 0); err != nil {
					return Projection{}, err
				}
				model := trusted(f, "actual_model")
				modelKey := stringJSON([]string{a.Key.Provider, model})
				modelCalls[modelKey]++
				mt := modelTotals[modelKey]
				if mt == nil {
					mt = &ModelTotal{Provider: a.Key.Provider, ActualModel: model}
					modelTotals[modelKey] = mt
					modelLogical[modelKey] = map[string]bool{}
					modelTurns[modelKey] = map[string]bool{}
				}
				if err := countModel(&mt.Counts, f, a.Key, modelLogical[modelKey], modelTurns[modelKey], len(v.Tokens) == 0); err != nil {
					return Projection{}, err
				}
				if model != "" {
					models[a.Key.Provider+":"+model] = true
				}
				for _, t := range v.Tokens {
					tk := stringJSON([]string{a.Key.Provider, model, t.Dimension, t.IncludedIn})
					total := totals[tk]
					if total == nil {
						total = &TokenTotal{Provider: a.Key.Provider, ActualModel: model, Dimension: t.Dimension, IncludedIn: t.IncludedIn}
						totals[tk] = total
					}
					if t.Count != nil {
						if total.ObservedCount == nil {
							x := int64(0)
							total.ObservedCount = &x
						}
						if *t.Count > math.MaxInt64-*total.ObservedCount {
							return Projection{}, ErrCapacity
						}
						*total.ObservedCount += *t.Count
						total.KnownAttempts++
					} else {
						total.UnknownAttempts++
					}
					if t.Conflict {
						total.ConflictedAttempts++
					}
				}
			} else {
				p.Counts.DeclaredCalls++
			}
			redact(f, q.Authorization)
			p.Attempts = append(p.Attempts, v)
		}
		if m := first.Money; m != nil {
			period := false
			for _, o := range group {
				if inPeriod(o.Money.At, q) {
					period = true
				}
			}
			if !period || !matches(f, m.Provider, q) {
				continue
			}
			for _, s := range src {
				seenSources[s.ID] = true
			}
			v := MoneyView{Provider: m.Provider, RecordID: m.RecordID, Fields: f, Sources: src, Stale: isStale}
			kind, currency, cadence := value(f, "kind"), value(f, "currency"), value(f, "cadence")
			conflict := false
			for _, v := range f {
				if len(v.Conflicts) > 0 {
					conflict = true
				}
			}
			if conflict {
				p.ConflictedMoneyRecords++
			}
			if f["decimal"].Value == nil {
				p.UnknownMoneyRecords++
			}
			if kind != "" && currency != "" && len(f["cadence"].Conflicts) == 0 {
				tk := stringJSON([]string{kind, currency, cadence})
				total := moneyTotals[tk]
				if total == nil {
					total = &MoneyTotal{Kind: MoneyKind(kind), Currency: currency, Cadence: cadence, Coverage: Unknown}
					moneyTotals[tk] = total
					moneySums[tk] = new(big.Int)
				}
				amount := f["decimal"]
				if amount.Value != nil {
					moneySums[tk].Add(moneySums[tk], decimalUnits(*amount.Value))
					total.KnownRecords++
				} else {
					total.UnknownRecords++
				}
				if conflict {
					total.ConflictedRecords++
				}
				if amount.Quality == UserEntered {
					total.UserEnteredRecords++
				}
			}
			redact(f, q.Authorization)
			p.Money = append(p.Money, v)
		}
		if b := first.Quota; b != nil {
			// Capacity is contextual state at Now, not spend in the selected period.
			if q.Now.Before(b.WindowStart) || !q.Now.Before(b.WindowEnd) || !matches(f, b.Provider, q) {
				continue
			}
			for _, s := range src {
				seenSources[s.ID] = true
			}
			redact(f, q.Authorization)
			p.Quotas = append(p.Quotas, QuotaView{Provider: b.Provider, BucketID: b.BucketID, WindowID: b.WindowID, Fields: f, Sources: src, Stale: isStale})
		}
	}
	p.Counts.LogicalRequests = len(logical)
	p.Counts.NativeTurns = len(turns)
	p.ActualModels = keys(models)
	for _, k := range keys(modelTotals) {
		mt := modelTotals[k]
		mt.Counts.LogicalRequests = len(modelLogical[k])
		mt.Counts.NativeTurns = len(modelTurns[k])
		p.ModelTotals = append(p.ModelTotals, *mt)
	}
	for _, k := range keys(totals) {
		total := totals[k]
		total.UnknownAttempts += modelCalls[stringJSON([]string{total.Provider, total.ActualModel})] - total.KnownAttempts - total.UnknownAttempts
		p.Tokens = append(p.Tokens, *totals[k])
	}
	for _, k := range keys(moneyTotals) {
		total := moneyTotals[k]
		if total.KnownRecords > 0 {
			s := formatUnits(moneySums[k])
			total.ObservedDecimal = &s
		}
		p.MoneyTotals = append(p.MoneyTotals, *total)
	}
	missing := map[string]bool{}
	for _, s := range q.ExpectedSources {
		if !seenSources[s] {
			missing[s] = true
		}
	}
	p.Coverage.MissingSources = keys(missing)
	return p, nil
}
func countModel(c *Counts, f map[string]Value, key AttemptKey, logical, turns map[string]bool, unknownTokens bool) error {
	c.ProviderCalls++
	if id := trusted(f, "logical_request"); id != "" {
		logical[id] = true
	} else {
		c.UnknownLogicalRequests++
	}
	if turn, thread := trusted(f, "native_turn"), trusted(f, "native_thread"); turn != "" && thread != "" {
		turns[stringJSON([]string{key.Provider, key.Namespace, thread, turn})] = true
	} else {
		c.UnknownNativeTurns++
	}
	if trusted(f, "retry_of") != "" {
		c.Retries++
	}
	if trusted(f, "parent") != "" {
		c.ChildCalls++
	}
	switch trusted(f, "outcome") {
	case "succeeded":
		c.Succeeded++
	case "failed":
		c.Failed++
	case "uncertain":
		c.Uncertain++
	default:
		c.UnknownOutcome++
	}
	if trusted(f, "actual_model") == "" {
		c.UnknownActualModel++
	}
	if unknownTokens {
		c.UnknownTokenCalls++
	}
	if text := trusted(f, "tool_calls"); text != "" {
		var n int64
		_ = json.Unmarshal([]byte(text), &n)
		if c.ToolCalls == nil {
			x := int64(0)
			c.ToolCalls = &x
		}
		if n > math.MaxInt64-*c.ToolCalls {
			return ErrCapacity
		}
		*c.ToolCalls += n
	} else {
		c.UnknownToolCalls++
	}
	return nil
}
func decimalUnits(s string) *big.Int {
	parts := strings.Split(s, ".")
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	fraction += strings.Repeat("0", 9-len(fraction))
	n := new(big.Int)
	n.SetString(parts[0]+fraction, 10)
	return n
}
func formatUnits(n *big.Int) string {
	s := n.String()
	if len(s) <= 9 {
		s = strings.Repeat("0", 10-len(s)) + s
	}
	whole, fraction := s[:len(s)-9], strings.TrimRight(s[len(s)-9:], "0")
	if fraction == "" {
		return whole
	}
	return whole + "." + fraction
}
