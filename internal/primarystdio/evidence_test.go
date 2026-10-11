package primarystdio

import (
	"math"
	"strings"
	"testing"

	"github.com/seanheiney/keelmux/internal/domain"
)

func evidenceChannel() Channel {
	b := Binding{HostID: "host", SessionID: "session", ExecutionID: "execution", Generation: 1, StartOperationID: "start", PrimaryIncarnation: "0123456789abcdef0123456789abcdef", NativeThreadID: "thread", RealmRevision: "realm"}
	return Channel{Version: Version, Spec: LaunchSpec{Binding: b, Workspace: "/owned/workspace", Binary: "/owned/native", BinarySHA256: strings.Repeat("1", 64), SchemaSHA256: strings.Repeat("2", 64), Profile: Codex0159, Environment: CleanEnvironment{Home: "/owned/home", CodexHome: "/owned/codex", XDGConfigHome: "/owned/config", XDGCacheHome: "/owned/cache", XDGDataHome: "/owned/data", TempDir: "/owned/tmp", Path: "/usr/bin:/bin", Locale: "C.UTF-8"}}, Ref: ChannelRef{PrimaryIncarnation: b.PrimaryIncarnation, ChannelIncarnation: "abcdef0123456789abcdef0123456789", Runtime: domain.RuntimeRef{Backend: "native", Socket: "/owned/helper.sock", Session: "supervisor", PID: 123, BootID: "boot", ProcessStart: "lifetime", ExecutionID: b.ExecutionID, OperationID: b.StartOperationID}}}
}

func evidenceObservation(c Channel, event *Event) Observation {
	o := Observation{From: Genesis(c.Ref.ChannelIncarnation)}
	o.Through = o.From
	if event != nil {
		event.Cursor, _ = AdvanceCursor(o.From, *event)
		o.Events = []Event{*event}
		o.Through = event.Cursor
	}
	o.Snapshot = Snapshot{Channel: c.Ref, Binding: c.Spec.Binding, Cursor: o.Through, Continuity: ContinuityUnknown, Bootstrap: BootstrapAttempted, Pending: true}
	o.Snapshot.SnapshotSHA256 = SnapshotDigest(o.Snapshot)
	o.BatchSHA256 = BatchDigest(o)
	return o
}

func TestEvidenceUnknownPendingIsValidAndDigestsBindAllFields(t *testing.T) {
	c := evidenceChannel()
	e := Event{Kind: EventCallback, Reason: ReasonPendingCallback, FrameSHA256: strings.Repeat("3", 64), FrameBytes: 22, RequestID: NativeID{Kind: "int64", Text: "7"}}
	o := evidenceObservation(c, &e)
	if err := ValidateObservation(c, o); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Observation){
		func(o *Observation) { o.Events[0].FrameBytes++ },
		func(o *Observation) { o.Events[0].RequestID.Kind = "string" },
		func(o *Observation) { o.Snapshot.Pending = false },
		func(o *Observation) { o.Snapshot.Binding.Generation++ },
		func(o *Observation) { o.Through.ChainSHA256 = strings.Repeat("4", 64) },
	} {
		x := o
		x.Events = append([]Event(nil), o.Events...)
		mutate(&x)
		if ValidateObservation(c, x) == nil {
			t.Fatal("altered evidence accepted")
		}
	}
}

func TestEvidenceChainAndMoreCannotSkipFramesOrOverflow(t *testing.T) {
	c := evidenceChannel()
	e := Event{Kind: EventResult, Reason: ReasonNone, FrameSHA256: strings.Repeat("3", 64), FrameBytes: 12}
	o := evidenceObservation(c, &e)
	if err := ValidateObservation(c, o); err != nil {
		t.Fatal(err)
	}
	o.More = true
	o.Snapshot.Cursor, _ = AdvanceCursor(o.Through, e)
	o.Snapshot.SnapshotSHA256 = SnapshotDigest(o.Snapshot)
	o.BatchSHA256 = BatchDigest(o)
	if err := ValidateObservation(c, o); err != nil {
		t.Fatal("bounded prefix rejected", err)
	}
	o.Events = nil
	o.BatchSHA256 = BatchDigest(o)
	if ValidateObservation(c, o) == nil {
		t.Fatal("missing prefix accepted")
	}
	cur := Genesis(c.Ref.ChannelIncarnation)
	cur.Seq = math.MaxUint64
	if _, err := AdvanceCursor(cur, e); err == nil {
		t.Fatal("sequence wrapped")
	}
	cur = Genesis(c.Ref.ChannelIncarnation)
	cur.ChainSHA256 = strings.Repeat("1", 64)
	if ValidateCursor(cur) == nil {
		t.Fatal("invented genesis accepted")
	}
}

func TestEvidenceRejectsUnsafeIdentityAndPreservesNativeIDKinds(t *testing.T) {
	c := evidenceChannel()
	for _, mutate := range []func(*Channel){
		func(c *Channel) { c.Spec.Binding.HostID = domain.ID(string([]byte{0xff})) },
		func(c *Channel) { c.Spec.Environment.Path = "/usr/bin:." },
		func(c *Channel) { c.Ref.Runtime.OperationID = "other" },
		func(c *Channel) { c.Ref.Runtime.Pane = "legacy" },
		func(c *Channel) { c.Spec.BinarySHA256 = strings.Repeat("A", 64) },
	} {
		x := c
		mutate(&x)
		if ValidateChannel(x) == nil {
			t.Fatal("invalid identity accepted")
		}
	}
	e := Event{Kind: EventResult, Reason: ReasonNone, FrameBytes: 1, FrameSHA256: strings.Repeat("1", 64), RequestID: NativeID{Kind: "int64", Text: "1"}}
	a, err := AdvanceCursor(Genesis(c.Ref.ChannelIncarnation), e)
	if err != nil {
		t.Fatal(err)
	}
	e.RequestID.Kind = "string"
	b, err := AdvanceCursor(Genesis(c.Ref.ChannelIncarnation), e)
	if err != nil || a == b {
		t.Fatal("native ID types collapsed", err)
	}
	e.RequestID = NativeID{Kind: "int64", Text: "01"}
	if _, err := AdvanceCursor(Genesis(c.Ref.ChannelIncarnation), e); err == nil {
		t.Fatal("noncanonical integer accepted")
	}
}
