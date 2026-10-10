// Package usage stores explicit, nonsecret structured observations. It never
// discovers credentials, scrapes terminals, calls providers, or changes billing.
package usage

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

type Quality string

const (
	ProviderVerified Quality = "provider_verified"
	AdapterObserved  Quality = "adapter_observed"
	UserEntered      Quality = "user_entered"
)

type Coverage string

const (
	Complete Coverage = "complete"
	Partial  Coverage = "partial"
	Unknown  Coverage = "unknown"
)

// ID identifies the original evidence stream, including on a peer replica.
// Kind describes a structured provider_api, runtime_hook, transcript, or manual
// source. Sequence orders cumulative snapshots within that original stream.
type Source struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	Quality    Quality   `json:"quality"`
	ObservedAt time.Time `json:"observed_at"`
	Sequence   uint64    `json:"sequence"`
	Coverage   Coverage  `json:"coverage"`
}

// Namespace must be established by the adapter from a verified native scope.
// Aliases, terminal output, and missing native IDs cannot establish this key.
// The package validates structure; the caller is responsible for that evidence.
type AttemptKey struct {
	Provider  string `json:"provider"`
	Namespace string `json:"namespace"`
	RequestID string `json:"request_id"`
	AttemptID string `json:"attempt_id"`
}
type Context struct {
	Federation     string `json:"federation,omitempty"`
	Host           string `json:"host,omitempty"`
	Project        string `json:"project,omitempty"`
	Task           string `json:"task,omitempty"`
	Thread         string `json:"thread,omitempty"`
	Lane           string `json:"lane,omitempty"`
	Session        string `json:"session,omitempty"`
	Execution      string `json:"execution,omitempty"`
	Account        string `json:"account,omitempty"`
	Subscription   string `json:"subscription,omitempty"`
	Harness        string `json:"harness,omitempty"`
	HarnessVersion string `json:"harness_version,omitempty"`
}

// IncludedIn names a dimension which already includes these tokens. Input,
// output, cache_read, cache_write, reasoning, and total are native names rather
// than universal interchangeable metrics. Unknown is represented by nil Count.
type Token struct {
	Dimension  string `json:"dimension"`
	Count      *int64 `json:"count"`
	IncludedIn string `json:"included_in,omitempty"`
}
type Attempt struct {
	Key            AttemptKey  `json:"key"`
	At             time.Time   `json:"at"`
	Context        Context     `json:"context"`
	LogicalRequest string      `json:"logical_request,omitempty"`
	NativeThread   string      `json:"native_thread,omitempty"`
	NativeTurn     string      `json:"native_turn,omitempty"`
	RequestedModel string      `json:"requested_model,omitempty"`
	ActualModel    string      `json:"actual_model,omitempty"`
	RetryOf        *AttemptKey `json:"retry_of,omitempty"`
	Parent         *AttemptKey `json:"parent,omitempty"`
	Outcome        string      `json:"outcome,omitempty"`
	ToolCalls      *int64      `json:"tool_calls"`
	Tokens         []Token     `json:"tokens,omitempty"`
	Final          bool        `json:"final"`
}

type MoneyKind string

const (
	Charge          MoneyKind = "actual_charge"
	Estimate        MoneyKind = "estimate"
	Commitment      MoneyKind = "subscription_commitment"
	Credit          MoneyKind = "credit"
	TopUp           MoneyKind = "top_up"
	TokenEquivalent MoneyKind = "token_equivalent"
)

// RecordID is an adapter-established invoice line/estimate/commitment identity,
// not an attempt counter. Decimal has up to 18 integer and 9 fractional digits;
// nil means unknown. Annual commitments remain annual; no price is inferred.
type Money struct {
	Provider    string      `json:"provider"`
	Namespace   string      `json:"namespace"`
	RecordID    string      `json:"record_id"`
	Context     Context     `json:"context"`
	At          time.Time   `json:"at"`
	Kind        MoneyKind   `json:"kind"`
	Currency    string      `json:"currency"`
	Decimal     *string     `json:"decimal"`
	Cadence     string      `json:"cadence,omitempty"`
	Attempt     *AttemptKey `json:"attempt,omitempty"`
	ActualModel string      `json:"actual_model,omitempty"`
}

// BucketID names a provider's shared native bucket. WindowID prevents distinct
// allowance windows from collapsing. Scope must be verified, not a local alias.
// Units and dimensions remain native and are never summed across buckets.
type Quota struct {
	Provider    string     `json:"provider"`
	Namespace   string     `json:"namespace"`
	BucketID    string     `json:"bucket_id"`
	WindowID    string     `json:"window_id"`
	ScopeKind   string     `json:"scope_kind"`
	ScopeID     string     `json:"scope_id"`
	Unit        string     `json:"unit"`
	Dimensions  []string   `json:"dimensions,omitempty"`
	WindowStart time.Time  `json:"window_start"`
	WindowEnd   time.Time  `json:"window_end"`
	ResetAt     *time.Time `json:"reset_at,omitempty"`
	Limit       *string    `json:"limit"`
	Used        *string    `json:"used"`
	Remaining   *string    `json:"remaining"`
	Context     Context    `json:"context"`
}

// Exactly one payload is required. Observation ID is immutable within Source.ID.
// A replica preserves both original IDs; a replica-specific ID is another
// observation of the same canonical key, never another provider call.
type Observation struct {
	ID      string   `json:"id"`
	Source  Source   `json:"source"`
	Attempt *Attempt `json:"attempt,omitempty"`
	Money   *Money   `json:"money,omitempty"`
	Quota   *Quota   `json:"quota,omitempty"`
}

var ErrConflict = errors.New("usage observation identity conflict")
var ErrCapacity = errors.New("usage observation capacity reached")
var ErrCorrupt = errors.New("usage ledger requires repair")
var ErrClosed = errors.New("usage ledger closed")
var decimalPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,17})(\.[0-9]{1,9})?$`)
var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

func ref(s string, required bool) bool {
	if s == "" {
		return !required
	}
	if len(s) > 256 || strings.TrimSpace(s) != s || s == "unknown" {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func validCoverage(c Coverage) bool { return c == Complete || c == Partial || c == Unknown }
func (k AttemptKey) valid() bool {
	return ref(k.Provider, true) && ref(k.Namespace, true) && ref(k.RequestID, true) && ref(k.AttemptID, true)
}
func (c Context) valid() bool {
	for _, v := range c.values() {
		if !ref(v, false) {
			return false
		}
	}
	return true
}
func (c Context) values() map[string]string {
	return map[string]string{"federation": c.Federation, "host": c.Host, "project": c.Project, "task": c.Task, "thread": c.Thread, "lane": c.Lane, "session": c.Session, "execution": c.Execution, "account": c.Account, "subscription": c.Subscription, "harness": c.Harness, "harness_version": c.HarnessVersion}
}
func validDecimal(p *string) bool { return p == nil || decimalPattern.MatchString(*p) }
func validTime(t time.Time) bool  { return !t.IsZero() && t.Year() >= 1970 && t.Year() <= 9999 }

func (o Observation) Validate() error {
	bad := errors.New("invalid structured usage observation")
	if !ref(o.ID, true) || !ref(o.Source.ID, true) || !validTime(o.Source.ObservedAt) || !validCoverage(o.Source.Coverage) {
		return bad
	}
	switch o.Source.Kind {
	case "provider_api", "runtime_hook", "transcript":
		if o.Source.Quality != ProviderVerified && o.Source.Quality != AdapterObserved {
			return bad
		}
	case "manual":
		if o.Source.Quality != UserEntered {
			return bad
		}
	default:
		return bad
	}
	n := 0
	if a := o.Attempt; a != nil {
		n++
		if !a.Key.valid() || !validTime(a.At) || !a.Context.valid() || len(a.Tokens) > 32 || (a.ToolCalls != nil && *a.ToolCalls < 0) {
			return bad
		}
		for _, v := range []string{a.LogicalRequest, a.NativeThread, a.NativeTurn, a.RequestedModel, a.ActualModel} {
			if !ref(v, false) {
				return bad
			}
		}
		if a.Outcome != "" && a.Outcome != "pending" && a.Outcome != "succeeded" && a.Outcome != "failed" && a.Outcome != "uncertain" {
			return bad
		}
		for _, k := range []*AttemptKey{a.Parent, a.RetryOf} {
			if k != nil && (!k.valid() || *k == a.Key || k.Provider != a.Key.Provider || k.Namespace != a.Key.Namespace) {
				return bad
			}
		}
		seen := map[string]bool{}
		for _, t := range a.Tokens {
			if !ref(t.Dimension, true) || !ref(t.IncludedIn, false) || t.Dimension == t.IncludedIn || seen[t.Dimension] || (t.Count != nil && *t.Count < 0) {
				return bad
			}
			seen[t.Dimension] = true
		}
		for _, t := range a.Tokens {
			visited := map[string]bool{t.Dimension: true}
			parent := t.IncludedIn
			for parent != "" {
				if visited[parent] {
					return bad
				}
				visited[parent] = true
				found := false
				for _, p := range a.Tokens {
					if p.Dimension == parent {
						parent = p.IncludedIn
						found = true
						break
					}
				}
				if !found {
					break
				} // Missing parent stays unknown, never additive.
			}
		}
	}
	if m := o.Money; m != nil {
		n++
		if !ref(m.Provider, true) || !ref(m.Namespace, true) || !ref(m.RecordID, true) || !ref(m.ActualModel, false) || !m.Context.valid() || !validTime(m.At) || !currencyPattern.MatchString(m.Currency) || !validDecimal(m.Decimal) {
			return bad
		}
		switch m.Kind {
		case Charge, Estimate, Credit, TopUp, TokenEquivalent:
			if m.Cadence != "" {
				return bad
			}
		case Commitment:
			if m.Cadence != "monthly" && m.Cadence != "annual" && m.Cadence != "weekly" {
				return bad
			}
			if m.Context.Subscription == "" || m.Attempt != nil {
				return bad
			}
		default:
			return bad
		}
		if m.Attempt != nil && (!m.Attempt.valid() || m.Attempt.Provider != m.Provider || m.Attempt.Namespace != m.Namespace) {
			return bad
		}
		if m.Kind == Charge && o.Source.Quality != ProviderVerified {
			return errors.New("an actual charge requires provider-verified evidence")
		}
	}
	if q := o.Quota; q != nil {
		n++
		if !ref(q.Provider, true) || !ref(q.Namespace, true) || !ref(q.BucketID, true) || !ref(q.WindowID, true) || !ref(q.ScopeID, true) || !ref(q.Unit, true) || !q.Context.valid() || len(q.Dimensions) > 16 || !validTime(q.WindowStart) || !validTime(q.WindowEnd) || !q.WindowEnd.After(q.WindowStart) || !validDecimal(q.Limit) || !validDecimal(q.Used) || !validDecimal(q.Remaining) {
			return bad
		}
		if q.ScopeKind != "account" && q.ScopeKind != "organisation" && q.ScopeKind != "subscription" {
			return bad
		}
		if o.Source.Quality == UserEntered {
			return errors.New("user-entered quota cannot establish a shared provider bucket")
		}
		if q.ResetAt != nil && !validTime(*q.ResetAt) {
			return bad
		}
		seen := map[string]bool{}
		for _, d := range q.Dimensions {
			if !ref(d, true) || seen[d] {
				return bad
			}
			seen[d] = true
		}
	}
	if n != 1 {
		return fmt.Errorf("%w: exactly one payload required", bad)
	}
	return nil
}

func normalized(o Observation) Observation {
	o.Source.ObservedAt = o.Source.ObservedAt.UTC()
	if o.Attempt != nil {
		a := *o.Attempt
		a.At = a.At.UTC()
		a.Tokens = append([]Token(nil), a.Tokens...)
		sort.Slice(a.Tokens, func(i, j int) bool { return a.Tokens[i].Dimension < a.Tokens[j].Dimension })
		o.Attempt = &a
	}
	if o.Money != nil {
		m := *o.Money
		m.At = m.At.UTC()
		if m.Decimal != nil {
			s := formatUnits(decimalUnits(*m.Decimal))
			m.Decimal = &s
		}
		o.Money = &m
	}
	if o.Quota != nil {
		q := *o.Quota
		q.WindowStart = q.WindowStart.UTC()
		q.WindowEnd = q.WindowEnd.UTC()
		if q.ResetAt != nil {
			t := q.ResetAt.UTC()
			q.ResetAt = &t
		}
		q.Dimensions = append([]string(nil), q.Dimensions...)
		sort.Strings(q.Dimensions)
		for _, p := range []**string{&q.Limit, &q.Used, &q.Remaining} {
			if *p != nil {
				s := formatUnits(decimalUnits(**p))
				*p = &s
			}
		}
		o.Quota = &q
	}
	return o
}
