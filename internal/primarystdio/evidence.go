package primarystdio

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/seanheiney/keelmux/internal/domain"
)

// Evidence helpers validate bounded identity/chain structure only. They do not
// inspect files, authenticate a provider, grant a writer or establish readiness.
func textOK(s string, max int) bool {
	return s != "" && len(s) <= max && utf8.ValidString(s) && !strings.ContainsAny(s, "\x00\r\n")
}
func pathOK(s string) bool { return textOK(s, 4096) && filepath.IsAbs(s) && filepath.Clean(s) == s }
func digestOK(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size && s == strings.ToLower(s)
}
func incarnationOK(s domain.ID) bool {
	b, err := hex.DecodeString(string(s))
	return err == nil && len(b) == 16 && string(s) == strings.ToLower(string(s))
}

func ValidateBinding(b Binding) error {
	for _, s := range []string{string(b.HostID), string(b.SessionID), string(b.ExecutionID), string(b.StartOperationID), b.NativeThreadID, b.RealmRevision} {
		if !textOK(s, 256) {
			return ErrInvalid
		}
	}
	if b.Generation == 0 || !incarnationOK(b.PrimaryIncarnation) {
		return ErrInvalid
	}
	return nil
}

func ValidateLaunchSpec(s LaunchSpec) error {
	if ValidateBinding(s.Binding) != nil || s.Profile != Codex0159 || !pathOK(s.Workspace) || !pathOK(s.Binary) || !digestOK(s.BinarySHA256) || !digestOK(s.SchemaSHA256) {
		return ErrInvalid
	}
	e := s.Environment
	for _, p := range []string{e.Home, e.CodexHome, e.XDGConfigHome, e.XDGCacheHome, e.XDGDataHome, e.TempDir} {
		if !pathOK(p) {
			return ErrInvalid
		}
	}
	if !textOK(e.Path, 4096) || !textOK(e.Locale, 128) {
		return ErrInvalid
	}
	for _, p := range filepath.SplitList(e.Path) {
		if !pathOK(p) {
			return ErrInvalid
		}
	}
	return nil
}

func ValidateChannel(c Channel) error {
	r, b := c.Ref.Runtime, c.Spec.Binding
	if c.Version != Version || ValidateLaunchSpec(c.Spec) != nil || c.Ref.PrimaryIncarnation != b.PrimaryIncarnation || !incarnationOK(c.Ref.ChannelIncarnation) || r.Backend != "native" || r.PID <= 0 || !pathOK(r.Socket) || !textOK(r.Session, 256) || r.Pane != "" || !textOK(r.BootID, 256) || !textOK(r.ProcessStart, 256) || r.ExecutionID != b.ExecutionID || r.OperationID != b.StartOperationID {
		return ErrInvalid
	}
	return nil
}

func ValidateCursor(c Cursor) error {
	if !incarnationOK(c.ChannelIncarnation) || !digestOK(c.ChainSHA256) || c.Seq == 0 && c != Genesis(c.ChannelIncarnation) {
		return ErrInvalid
	}
	return nil
}

func hash(label string, v any) string {
	b, _ := json.Marshal(struct {
		Domain  string
		Version int
		Value   any
	}{label, Version, v})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Genesis is a channel-scoped empty prefix, distinct from an absent ledger.
func Genesis(channelIncarnation domain.ID) Cursor {
	return Cursor{ChannelIncarnation: channelIncarnation, ChainSHA256: hash("keelmux.primary_stdio.genesis", channelIncarnation)}
}

func validEvent(e Event) bool {
	if e.FrameBytes > MaxFrameBytes || e.FrameBytes == 0 && e.FrameSHA256 != "" || e.FrameBytes != 0 && !digestOK(e.FrameSHA256) {
		return false
	}
	switch e.RequestID.Kind {
	case "":
		if e.RequestID.Text != "" {
			return false
		}
	case "string":
		if len(e.RequestID.Text) > 256 || !utf8.ValidString(e.RequestID.Text) {
			return false
		}
	case "int64":
		n, err := strconv.ParseInt(e.RequestID.Text, 10, 64)
		if err != nil || strconv.FormatInt(n, 10) != e.RequestID.Text {
			return false
		}
	default:
		return false
	}
	switch e.Kind {
	case EventResult, EventNotice:
		return e.Reason == ReasonNone && e.FrameBytes != 0
	case EventCallback:
		return e.Reason == ReasonPendingCallback && e.FrameBytes != 0
	case EventUnsupported:
		return (e.Reason == ReasonUnsupported || e.Reason == ReasonCorrelation) && e.FrameBytes != 0
	case EventMalformed:
		return e.Reason == ReasonMalformed || e.Reason == ReasonCorrelation || e.Reason == ReasonPartialEOF || e.Reason == ReasonOverflow
	case EventGap:
		return e.Reason == ReasonOverflow || e.Reason == ReasonStorage || e.Reason == ReasonPartialEOF || e.Reason == ReasonIdentity || e.Reason == ReasonTimeout || e.Reason == ReasonCorrelation
	}
	return false
}

// EventDigest excludes its derived cursor; the chain binds its position below.
func EventDigest(e Event) string { e.Cursor = Cursor{}; return hash("keelmux.primary_stdio.event", e) }

func AdvanceCursor(previous Cursor, event Event) (Cursor, error) {
	if ValidateCursor(previous) != nil || previous.Seq == math.MaxUint64 || !validEvent(event) {
		return Cursor{}, ErrInvalid
	}
	n := Cursor{ChannelIncarnation: previous.ChannelIncarnation, Seq: previous.Seq + 1}
	n.ChainSHA256 = hash("keelmux.primary_stdio.advance", struct {
		Previous    Cursor
		Sequence    uint64
		EventSHA256 string
	}{previous, n.Seq, EventDigest(event)})
	return n, nil
}

func SnapshotDigest(s Snapshot) string {
	s.SnapshotSHA256 = ""
	return hash("keelmux.primary_stdio.snapshot", s)
}
func BatchDigest(o Observation) string {
	o.BatchSHA256 = ""
	return hash("keelmux.primary_stdio.batch", o)
}

func ValidateObservation(c Channel, o Observation) error {
	if ValidateChannel(c) != nil || ValidateCursor(o.From) != nil || ValidateCursor(o.Through) != nil || o.From.ChannelIncarnation != c.Ref.ChannelIncarnation || o.Through.ChannelIncarnation != c.Ref.ChannelIncarnation || len(o.Events) > MaxExchangeFrames {
		return ErrInvalid
	}
	s := o.Snapshot
	if s.Channel != c.Ref || s.Binding != c.Spec.Binding || ValidateCursor(s.Cursor) != nil || s.Cursor.ChannelIncarnation != c.Ref.ChannelIncarnation || s.SnapshotSHA256 != SnapshotDigest(s) || o.BatchSHA256 != BatchDigest(o) {
		return ErrInvalid
	}
	switch s.Continuity {
	case ContinuityComplete, ContinuityUnknown, ContinuityBroken:
	default:
		return ErrInvalid
	}
	switch s.Bootstrap {
	case BootstrapUnsent, BootstrapAttempted, BootstrapComplete, BootstrapUncertain:
	default:
		return ErrInvalid
	}
	if !o.More && s.Cursor != o.Through || o.More && s.Cursor.Seq <= o.Through.Seq {
		return ErrInvalid
	}
	cur := o.From
	var bytes uint64
	for _, e := range o.Events {
		next, err := AdvanceCursor(cur, e)
		if err != nil || next != e.Cursor {
			return ErrInvalid
		}
		cur = next
		bytes += uint64(e.FrameBytes)
		if bytes > MaxExchangeBytes {
			return ErrInvalid
		}
	}
	if cur != o.Through {
		return ErrInvalid
	}
	return nil
}

func ValidateImport(b ImportBatch) error {
	if !textOK(string(b.ImportID), 256) {
		return ErrInvalid
	}
	return ValidateObservation(b.Channel, b.Observation)
}

// EventBlocks is conservative and never permits native callback resolution.
func EventBlocks(e Event) bool {
	return e.Kind != EventResult && e.Kind != EventNotice || e.Reason != ReasonNone
}
