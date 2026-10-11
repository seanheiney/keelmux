package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	cb "github.com/seanheiney/keelmux/internal/codexbroker"
	cc "github.com/seanheiney/keelmux/internal/codexcontroller"
	"github.com/seanheiney/keelmux/internal/domain"
	ps "github.com/seanheiney/keelmux/internal/primarystdio"
)

func stdioStoreFixture(t *testing.T, claim bool) (*Store, domain.Session, cc.LifecycleRequest, ps.Channel, string) {
	t.Helper()
	s, v, req, path := primaryStartStore(t)
	if claim {
		if fresh, err := s.ClaimCodexPrimaryStart(context.Background(), req); err != nil || !fresh {
			t.Fatal(fresh, err)
		}
	}
	e := ps.CleanEnvironment{Home: "/owned/home", CodexHome: "/owned/codex", XDGConfigHome: "/owned/config", XDGCacheHome: "/owned/cache", XDGDataHome: "/owned/data", TempDir: "/owned/tmp", Path: "/usr/bin:/bin", Locale: "C.UTF-8"}
	b := ps.Binding{HostID: v.HostID, SessionID: v.ID, ExecutionID: v.Execution.ID, Generation: v.Execution.Generation, StartOperationID: req.OperationID, PrimaryIncarnation: req.StartIncarnation, NativeThreadID: req.NativeThreadID, RealmRevision: req.RealmRevision}
	c := ps.Channel{Version: ps.Version, Spec: ps.LaunchSpec{Binding: b, Workspace: v.Workspace, Binary: "/owned/codex-native", BinarySHA256: strings.Repeat("1", 64), SchemaSHA256: strings.Repeat("2", 64), Profile: ps.Codex0159, Environment: e}, Ref: ps.ChannelRef{PrimaryIncarnation: b.PrimaryIncarnation, ChannelIncarnation: "abcdef0123456789abcdef0123456789", Runtime: domain.RuntimeRef{Backend: "native", Socket: "/owned/supervisor.sock", Session: "supervisor-inc", PID: 123, BootID: "boot", ProcessStart: "start", ExecutionID: v.Execution.ID, OperationID: req.OperationID}}}
	return s, v, req, c, path
}
func stdioBatch(t *testing.T, c ps.Channel, id domain.ID, from ps.Cursor, events []ps.Event, change func(*ps.Observation)) ps.ImportBatch {
	t.Helper()
	o := ps.Observation{From: from, Through: from, Events: append([]ps.Event(nil), events...)}
	for i := range o.Events {
		var err error
		o.Events[i].Cursor, err = ps.AdvanceCursor(o.Through, o.Events[i])
		if err != nil {
			t.Fatal(err)
		}
		o.Through = o.Events[i].Cursor
	}
	o.Snapshot = ps.Snapshot{Channel: c.Ref, Binding: c.Spec.Binding, Cursor: o.Through, Continuity: ps.ContinuityComplete, Bootstrap: ps.BootstrapComplete, ProcessAlive: true, ExactImage: true, ExactDirectories: true, ExactLoadedThread: true, Idle: true, EmptyQueue: true, EmptyBackground: true}
	if change != nil {
		change(&o)
	}
	o.Snapshot.SnapshotSHA256 = ps.SnapshotDigest(o.Snapshot)
	o.BatchSHA256 = ps.BatchDigest(o)
	return ps.ImportBatch{ImportID: id, Channel: c, Observation: o}
}
func stdioResult() ps.Event {
	return ps.Event{Kind: ps.EventResult, Reason: ps.ReasonNone, FrameBytes: 12, FrameSHA256: strings.Repeat("3", 64), RequestID: ps.NativeID{Kind: "string", Text: "original-request"}}
}
func stdioAgent(c ps.Channel) cb.AgentBinding {
	b, r := c.Spec.Binding, c.Ref.Runtime
	return cb.AgentBinding{HostID: b.HostID, SessionID: b.SessionID, ExecutionID: b.ExecutionID, Generation: b.Generation, AppServerIncarnation: b.PrimaryIncarnation, AppServerPID: r.PID, AppServerBootID: r.BootID, AppServerProcessStart: r.ProcessStart, ProtocolVersion: cc.ProtocolVersion, NativeThreadID: b.NativeThreadID}
}
func bindStdio(t *testing.T, s *Store, c ps.Channel) ps.Ledger {
	t.Helper()
	l, err := s.BindCodexPrimaryStdio(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	return l
}
func importStdio(t *testing.T, s *Store, b ps.ImportBatch) ps.ImportReceipt {
	t.Helper()
	r, err := s.ImportCodexPrimaryStdio(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func stdioDurableBytes(t *testing.T, s *Store) []byte {
	t.Helper()
	rows, err := s.db.Query(`SELECT id,generation,data,payload FROM operations ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out bytes.Buffer
	for rows.Next() {
		var id string
		var gen uint64
		var data, payload []byte
		if err := rows.Scan(&id, &gen, &data, &payload); err != nil {
			t.Fatal(err)
		}
		out.Write(encoded(struct {
			ID            string
			Gen           uint64
			Data, Payload []byte
		}{id, gen, data, payload}))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestPrimaryStdioBindingRequiresClaimAndCannotPublishGenesis(t *testing.T) {
	s, v, req, c, _ := stdioStoreFixture(t, false)
	before := stdioDurableBytes(t, s)
	if _, err := s.BindCodexPrimaryStdio(context.Background(), c); err == nil {
		t.Fatal("unclaimed start bound")
	}
	if !bytes.Equal(before, stdioDurableBytes(t, s)) {
		t.Fatal("rejected binding mutated intent")
	}
	if fresh, err := s.ClaimCodexPrimaryStart(context.Background(), req); err != nil || !fresh {
		t.Fatal(fresh, err)
	}
	if l, err := s.CodexPrimaryStdio(context.Background(), c.Spec.Binding); err != nil || l.Bound {
		t.Fatal("absent ledger not distinct", err)
	}
	l := bindStdio(t, s, c)
	if !l.Bound || l.ImportCount != 0 || l.Cursor != ps.Genesis(c.Ref.ChannelIncarnation) || l.Continuity != ps.ContinuityUnknown {
		t.Fatal("genesis claimed observations", l)
	}
	before = stdioDurableBytes(t, s)
	if again, err := s.BindCodexPrimaryStdio(context.Background(), c); err != nil || !bytes.Equal(encoded(again), encoded(l)) {
		t.Fatal("bind retry changed receipt", err)
	}
	if !bytes.Equal(before, stdioDurableBytes(t, s)) {
		t.Fatal("bind retry mutated journal")
	}
	if err := s.PublishCodexPrimary(context.Background(), stdioAgent(c)); err == nil {
		t.Fatal("genesis published ready")
	}
	for _, readonly := range []bool{false, true} {
		if _, err := s.CodexAcquire(context.Background(), cb.WriterRequest{Agent: stdioAgent(c), OperationID: domain.NewID("acquire"), PrincipalID: "owner", ClientID: "client", ReadOnly: readonly, Kind: cb.NativeWriter}); err == nil {
			t.Fatal("stdio grant admitted", readonly)
		}
	}
	if now, err := s.Session(context.Background(), v.ID); err != nil || now.Execution.State != "primary_reserved" {
		t.Fatal("rejected publication mutated session", err)
	}
}

func TestPrimaryStdioImportReceiptSurvivesRestartAndLateRetryWithoutMutation(t *testing.T) {
	s, _, _, c, path := stdioStoreFixture(t, true)
	l := bindStdio(t, s, c)
	b := stdioBatch(t, c, "import-one", l.Cursor, []ps.Event{stdioResult()}, nil)
	receipt := importStdio(t, s, b)
	second := stdioBatch(t, c, "import-two", receipt.Through, []ps.Event{stdioResult()}, nil)
	importStdio(t, s, second)
	before := stdioDurableBytes(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.ImportCodexPrimaryStdio(context.Background(), b)
	if err != nil || got != receipt || !bytes.Equal(before, stdioDurableBytes(t, s)) {
		t.Fatal("lost ack replay changed immutable receipt", err)
	}
	l, err = s.CodexPrimaryStdio(context.Background(), c.Spec.Binding)
	if err != nil || l.ImportCount != 2 || l.Cursor != second.Observation.Through || l.Snapshot != second.Observation.Snapshot || !primaryStdioReady(l) {
		t.Fatal("restart lost complete frontier", err)
	}
	if err = s.PublishCodexPrimary(context.Background(), stdioAgent(c)); err != nil {
		t.Fatal("complete structural snapshot refused", err)
	}
	for _, ro := range []bool{false, true} {
		if _, err = s.CodexAcquire(context.Background(), cb.WriterRequest{Agent: stdioAgent(c), OperationID: domain.NewID("acquire"), PrincipalID: "owner", ClientID: "client", Kind: cb.NativeWriter, ReadOnly: ro}); err == nil {
			t.Fatal("historical ready became stdio grant", ro)
		}
	}
}

func TestPrimaryStdioIdentityAndOverlappingImportsRefuseWithoutMutation(t *testing.T) {
	s, _, _, c, _ := stdioStoreFixture(t, true)
	l := bindStdio(t, s, c)
	b := stdioBatch(t, c, "import-one", l.Cursor, []ps.Event{stdioResult()}, nil)
	importStdio(t, s, b)
	before := stdioDurableBytes(t, s)
	for _, change := range []func(*ps.Channel){
		func(c *ps.Channel) { c.Ref.ChannelIncarnation = "11111111111111111111111111111111" },
		func(c *ps.Channel) { c.Ref.Runtime.PID++ },
		func(c *ps.Channel) { c.Spec.SchemaSHA256 = strings.Repeat("4", 64) },
		func(c *ps.Channel) { c.Spec.Binding.NativeThreadID = "foreign" },
		func(c *ps.Channel) { c.Spec.Binding.Generation++ },
		func(c *ps.Channel) { c.Spec.Binding.RealmRevision = "foreign" },
	} {
		x := c
		change(&x)
		if _, err := s.BindCodexPrimaryStdio(context.Background(), x); err == nil {
			t.Fatal("changed binding accepted")
		}
	}
	copy := b
	copy.ImportID = "duplicate-range"
	if _, err := s.ImportCodexPrimaryStdio(context.Background(), copy); err == nil {
		t.Fatal("overlapping range accepted with new ID")
	}
	changed := stdioBatch(t, c, b.ImportID, b.Observation.From, []ps.Event{stdioResult()}, func(o *ps.Observation) { o.Snapshot.EmptyQueue = false })
	if _, err := s.ImportCodexPrimaryStdio(context.Background(), changed); err == nil {
		t.Fatal("changed retry accepted")
	}
	wrong := c.Spec.Binding
	wrong.HostID = "foreign"
	if _, err := s.CodexPrimaryStdio(context.Background(), wrong); err == nil {
		t.Fatal("foreign read accepted")
	}
	if !bytes.Equal(before, stdioDurableBytes(t, s)) {
		t.Fatal("identity refusals mutated ledger")
	}
}

func TestPrimaryStdioEmptyPendingAndContinuityAreSticky(t *testing.T) {
	for _, kind := range []string{"pending", "unknown", "broken"} {
		t.Run(kind, func(t *testing.T) {
			s, _, _, c, _ := stdioStoreFixture(t, true)
			l := bindStdio(t, s, c)
			first := stdioBatch(t, c, "empty-fault", l.Cursor, nil, func(o *ps.Observation) {
				switch kind {
				case "pending":
					o.Snapshot.Pending = true
				case "unknown":
					o.Snapshot.Continuity = ps.ContinuityUnknown
				case "broken":
					o.Snapshot.Continuity = ps.ContinuityBroken
				}
			})
			importStdio(t, s, first)
			importStdio(t, s, stdioBatch(t, c, "later-healthy", first.Observation.Through, nil, nil))
			l, err := s.CodexPrimaryStdio(context.Background(), c.Spec.Binding)
			if err != nil || primaryStdioReady(l) || kind == "pending" && !l.Pending || kind == "unknown" && l.Continuity != ps.ContinuityUnknown || kind == "broken" && l.Continuity != ps.ContinuityBroken {
				t.Fatal("fault cleared by empty healthy snapshot", l, err)
			}
			if err = s.PublishCodexPrimary(context.Background(), stdioAgent(c)); err == nil {
				t.Fatal("sticky fault published ready")
			}
		})
	}
}

func TestPrimaryStdioMoreFrontierCanDrainWithoutClearingRealBlockers(t *testing.T) {
	s, _, _, c, _ := stdioStoreFixture(t, true)
	l := bindStdio(t, s, c)
	e := stdioResult()
	first := stdioBatch(t, c, "prefix", l.Cursor, []ps.Event{e}, func(o *ps.Observation) { o.More = true; o.Snapshot.Cursor, _ = ps.AdvanceCursor(o.Through, e) })
	importStdio(t, s, first)
	if err := s.PublishCodexPrimary(context.Background(), stdioAgent(c)); err == nil {
		t.Fatal("partial frontier published")
	}
	second := stdioBatch(t, c, "suffix", first.Observation.Through, []ps.Event{e}, nil)
	importStdio(t, s, second)
	l, err := s.CodexPrimaryStdio(context.Background(), c.Spec.Binding)
	if err != nil || l.Pending || !primaryStdioReady(l) {
		t.Fatal("ordinary bounded prefix made permanent pending", l, err)
	}
}

func TestPrimaryStdioGrantlessCallbackAndMalformedGapInvalidatePublication(t *testing.T) {
	for _, e := range []ps.Event{
		{Kind: ps.EventCallback, Reason: ps.ReasonPendingCallback, FrameBytes: 42, FrameSHA256: strings.Repeat("a", 64), RequestID: ps.NativeID{Kind: "string", Text: "foreign-native-request"}},
		{Kind: ps.EventMalformed, Reason: ps.ReasonMalformed, FrameBytes: 3, FrameSHA256: strings.Repeat("b", 64)},
		{Kind: ps.EventGap, Reason: ps.ReasonStorage},
	} {
		t.Run(string(e.Kind), func(t *testing.T) {
			s, v, _, c, _ := stdioStoreFixture(t, true)
			l := bindStdio(t, s, c)
			ready := stdioBatch(t, c, "ready", l.Cursor, nil, nil)
			importStdio(t, s, ready)
			if err := s.PublishCodexPrimary(context.Background(), stdioAgent(c)); err != nil {
				t.Fatal(err)
			}
			fault := stdioBatch(t, c, "blocking", ready.Observation.Through, []ps.Event{e}, nil)
			importStdio(t, s, fault)
			l, err := s.CodexPrimaryStdio(context.Background(), c.Spec.Binding)
			if err != nil || !l.Pending || primaryStdioReady(l) {
				t.Fatal("grantless fault disappeared", err)
			}
			if e.Kind != ps.EventCallback && l.Continuity != ps.ContinuityBroken {
				t.Fatal("malformed/gap still complete")
			}
			if err = s.PublishCodexPrimary(context.Background(), stdioAgent(c)); err == nil {
				t.Fatal("fault republished")
			}
			if blocked, err := s.CodexBlocked(context.Background(), stdioAgent(c)); err != nil || !blocked {
				t.Fatal("common predicate missed primary", err)
			}
			current, err := s.Session(context.Background(), v.ID)
			if err != nil || current.Execution.State != "primary_uncertain" {
				t.Fatal("saved ready survived fault", err)
			}
			var grants, callbacks int
			s.db.QueryRow(`SELECT COUNT(*) FROM grants`).Scan(&grants)
			s.db.QueryRow(`SELECT COUNT(*) FROM codex_callbacks`).Scan(&callbacks)
			if grants != 0 || callbacks != 0 {
				t.Fatal("primary evidence fabricated broker authority")
			}
		})
	}
}

func TestPrimaryStdioAtomicImportFailureAndMalformedTailLeaveOriginalBytes(t *testing.T) {
	s, _, _, c, _ := stdioStoreFixture(t, true)
	l := bindStdio(t, s, c)
	before := stdioDurableBytes(t, s)
	batch := stdioBatch(t, c, "atomic", l.Cursor, []ps.Event{stdioResult(), stdioResult()}, nil)
	bad := batch
	bad.Observation.Events = append([]ps.Event(nil), batch.Observation.Events...)
	bad.Observation.Events[1].Cursor.Seq++
	bad.Observation.BatchSHA256 = ps.BatchDigest(bad.Observation)
	if _, err := s.ImportCodexPrimaryStdio(context.Background(), bad); err == nil {
		t.Fatal("invalid final item accepted")
	}
	if !bytes.Equal(before, stdioDurableBytes(t, s)) {
		t.Fatal("partial malformed import persisted")
	}
	if _, err := s.db.Exec(`CREATE TRIGGER fail_stdio_anchor BEFORE UPDATE OF payload ON operations BEGIN SELECT RAISE(ABORT,'fixture transaction failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportCodexPrimaryStdio(context.Background(), batch); err == nil {
		t.Fatal("transaction failure returned ack")
	}
	if !bytes.Equal(before, stdioDurableBytes(t, s)) {
		t.Fatal("insert before failed anchor update survived rollback")
	}
	if _, _, err := s.Operation(context.Background(), batch.ImportID); !errors.Is(err, ErrNotFound) {
		t.Fatal("hidden receipt survived", err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER fail_stdio_anchor`); err != nil {
		t.Fatal(err)
	}
	importStdio(t, s, batch)
}

func TestPrimaryStdioMissingOrChangedJournalRefusesReadAndRestart(t *testing.T) {
	for _, damage := range []string{"channel", "interior", "tail", "generation", "count", "snapshot", "orphan", "anchor", "row-id", "reserve-generation"} {
		t.Run(damage, func(t *testing.T) {
			s, _, _, c, path := stdioStoreFixture(t, true)
			l := bindStdio(t, s, c)
			for _, id := range []domain.ID{"one", "two", "three"} {
				b := stdioBatch(t, c, id, l.Cursor, []ps.Event{stdioResult()}, nil)
				importStdio(t, s, b)
				l.Cursor = b.Observation.Through
			}
			r, err := s.CodexPrimaryReservation(context.Background(), c.Spec.Binding.SessionID, c.Spec.Binding.ExecutionID)
			if err != nil {
				t.Fatal(err)
			}
			switch damage {
			case "channel":
				_, err = s.db.Exec(`DELETE FROM operations WHERE id=?`, r.Stdio.ChannelOperationID)
			case "interior":
				_, err = s.db.Exec(`DELETE FROM operations WHERE id='two'`)
			case "tail":
				_, err = s.db.Exec(`DELETE FROM operations WHERE id='three'`)
			case "generation":
				_, err = s.db.Exec(`UPDATE operations SET generation=generation+1 WHERE id='two'`)
			case "reserve-generation":
				_, err = s.db.Exec(`UPDATE operations SET generation=generation+1 WHERE id=?`, r.Request.OperationID)
			case "count":
				r.Stdio.Ledger.ImportCount--
				_, err = s.db.Exec(`UPDATE operations SET payload=? WHERE id=?`, encoded(r), r.Request.OperationID)
			case "snapshot":
				r.Stdio.Ledger.Snapshot.Pending = true
				_, err = s.db.Exec(`UPDATE operations SET payload=? WHERE id=?`, encoded(r), r.Request.OperationID)
			case "anchor":
				r.Stdio = nil
				_, err = s.db.Exec(`UPDATE operations SET payload=? WHERE id=?`, encoded(r), r.Request.OperationID)
			case "row-id":
				_, err = s.db.Exec(`UPDATE operations SET id='changed' WHERE id='two'`)
			case "orphan":
				op := domain.Operation{ID: "orphan", Kind: primaryStdioImportKind, SessionID: c.Spec.Binding.SessionID, ExecutionID: c.Spec.Binding.ExecutionID, Outcome: domain.Completed}
				_, err = s.db.Exec(`INSERT INTO operations(id,session_id,generation,data,payload) VALUES(?,?,?,?,?)`, op.ID, op.SessionID, c.Spec.Binding.Generation, encoded(op), []byte(`{}`))
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.CodexPrimaryStdio(context.Background(), c.Spec.Binding); err == nil {
				t.Fatal("damaged ledger read healthy")
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			fileBefore, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if reopened, err := Open(path); err == nil {
				reopened.Close()
				t.Fatal("missing evidence recreated on Open")
			} else if !strings.Contains(err.Error(), "repair required") {
				t.Fatal(err)
			}
			fileAfter, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(fileBefore, fileAfter) {
				t.Fatal("repair refusal rewrote database", err)
			}
		})
	}
}

func TestPrimaryStdioCallbackAfterStopFenceRetainedWithoutUnfencingOrReplay(t *testing.T) {
	s, v, req, c, _ := stdioStoreFixture(t, true)
	l := bindStdio(t, s, c)
	ready := stdioBatch(t, c, "ready", l.Cursor, nil, nil)
	importStdio(t, s, ready)
	a := stdioAgent(c)
	if err := s.PublishCodexPrimary(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	stop := cc.LifecycleRequest{Action: cc.StopPrimary, OperationID: "stop", Target: req.Target, Agent: a, Workspace: req.Workspace, NativeThreadID: req.NativeThreadID, RealmRevision: req.RealmRevision}
	if err := s.FenceCodexPrimaryStop(context.Background(), stop); err != nil {
		t.Fatal(err)
	}
	fault := stdioBatch(t, c, "late-callback", ready.Observation.Through, []ps.Event{{Kind: ps.EventCallback, Reason: ps.ReasonPendingCallback, FrameBytes: 1, FrameSHA256: strings.Repeat("4", 64)}}, nil)
	importStdio(t, s, fault)
	if err := s.PublishCodexPrimary(context.Background(), a); err == nil {
		t.Fatal("stop fence cleared")
	}
	if fresh, err := s.ClaimCodexPrimaryStart(context.Background(), req); err != nil || fresh {
		t.Fatal("matching claim replayed", fresh, err)
	}
	if err := s.CompleteCodexPrimaryStop(context.Background(), cc.StopRequest{OperationID: "stop", Target: req.Target, Incarnation: req.StartIncarnation}); err != nil {
		t.Fatal(err)
	}
	current, err := s.Session(context.Background(), v.ID)
	if err != nil || current.Execution.State != "primary_stopped" {
		t.Fatal(err)
	}
	l, err = s.CodexPrimaryStdio(context.Background(), c.Spec.Binding)
	if err != nil || !l.Pending {
		t.Fatal("stop completion cleared callback", err)
	}
	if same, err := s.BindCodexPrimaryStdio(context.Background(), c); err != nil || !same.Pending {
		t.Fatal("stopped channel receipt could not be read", err)
	}
}

func TestPrimaryStdioConcurrentDuplicateImportsReturnOneReceipt(t *testing.T) {
	s, _, _, c, _ := stdioStoreFixture(t, true)
	l := bindStdio(t, s, c)
	b := stdioBatch(t, c, "same", l.Cursor, []ps.Event{stdioResult()}, nil)
	var wg sync.WaitGroup
	results := make(chan ps.ImportReceipt, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.ImportCodexPrimaryStdio(context.Background(), b)
			results <- r
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	var receipt ps.ImportReceipt
	for r := range results {
		if receipt.ImportID == "" {
			receipt = r
		}
		if r != receipt {
			t.Fatal("duplicate response differs")
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	l, err := s.CodexPrimaryStdio(context.Background(), c.Spec.Binding)
	if err != nil || l.ImportCount != 1 {
		t.Fatal("duplicate count advanced", err)
	}
}

func TestPrimaryStdioBlockingStorageCeilingAllowsFaultButRejectsExtraEvidence(t *testing.T) {
	for _, byBytes := range []bool{false, true} {
		t.Run(map[bool]string{false: "records", true: "bytes"}[byBytes], func(t *testing.T) {
			s, _, _, c, _ := stdioStoreFixture(t, true)
			l := bindStdio(t, s, c)
			count, size := ps.MaxEvents, uint32(1)
			if byBytes {
				count = 8
				size = ps.MaxFrameBytes
			}
			events := make([]ps.Event, count)
			for i := range events {
				events[i] = ps.Event{Kind: ps.EventCallback, Reason: ps.ReasonPendingCallback, FrameBytes: size, FrameSHA256: strings.Repeat("4", 64)}
			}
			full := stdioBatch(t, c, "full", l.Cursor, events, nil)
			importStdio(t, s, full)
			marker := stdioBatch(t, c, "overflow-marker", full.Observation.Through, []ps.Event{{Kind: ps.EventGap, Reason: ps.ReasonOverflow}}, nil)
			importStdio(t, s, marker)
			before := stdioDurableBytes(t, s)
			extra := stdioBatch(t, c, "excess", marker.Observation.Through, []ps.Event{{Kind: ps.EventCallback, Reason: ps.ReasonPendingCallback, FrameBytes: 1, FrameSHA256: strings.Repeat("4", 64)}}, nil)
			if _, err := s.ImportCodexPrimaryStdio(context.Background(), extra); !errors.Is(err, ps.ErrUnavailable) {
				t.Fatal("excess evidence admitted", err)
			}
			if !bytes.Equal(before, stdioDurableBytes(t, s)) {
				t.Fatal("over-limit import partly mutated")
			}
			l, err := s.CodexPrimaryStdio(context.Background(), c.Spec.Binding)
			if err != nil || !l.Pending || l.Continuity != ps.ContinuityBroken {
				t.Fatal("fault marker lost", err)
			}
		})
	}
}

func TestPrimaryStdioLifetimeBoundsRejectBeforeMutation(t *testing.T) {
	_, _, _, c, _ := stdioStoreFixture(t, true)
	for _, kind := range []string{"imports", "events", "bytes"} {
		t.Run(kind, func(t *testing.T) {
			a := initialPrimaryStdioAnchor(c)
			b := stdioBatch(t, c, "boundary", a.Ledger.Cursor, []ps.Event{stdioResult()}, nil)
			record := primaryStdioImport{Batch: b, Receipt: ps.ImportReceipt{ImportID: b.ImportID, Channel: c.Ref, From: b.Observation.From, Through: b.Observation.Through, BatchSHA256: b.Observation.BatchSHA256}}
			switch kind {
			case "imports":
				a.Ledger.ImportCount = MaxPrimaryStdioImports - 1
			case "events":
				a.EventCount = MaxPrimaryStdioEvents - 1
			case "bytes":
				a.StoredBytes = MaxPrimaryStdioBytes - uint64(len(encoded(record)))
			}
			at, err := primaryStdioAppend(a, record)
			if err != nil {
				t.Fatal("exact lifetime boundary refused", err)
			}
			before := encoded(at)
			next := stdioBatch(t, c, "overflow", at.Ledger.Cursor, []ps.Event{stdioResult()}, nil)
			r := primaryStdioImport{Batch: next, Receipt: ps.ImportReceipt{ImportID: next.ImportID, Channel: c.Ref, From: next.Observation.From, Through: next.Observation.Through, BatchSHA256: next.Observation.BatchSHA256}, PreviousImportID: at.Ledger.LastImportID, PreviousImportSHA256: at.LastImportSHA256}
			after, err := primaryStdioAppend(at, r)
			if !errors.Is(err, ps.ErrUnavailable) || !bytes.Equal(before, encoded(after)) {
				t.Fatal("lifetime overflow changed anchor", err)
			}
		})
	}
}

// Make raw-ID/privacy tests use the stored private DTO, never a broker callback.
func TestPrimaryStdioImportDoesNotNormalizeInvalidUTF8Identity(t *testing.T) {
	s, _, _, c, _ := stdioStoreFixture(t, true)
	l := bindStdio(t, s, c)
	b := stdioBatch(t, c, "valid", l.Cursor, nil, nil)
	b.ImportID = domain.ID(string([]byte{0xff}))
	before := stdioDurableBytes(t, s)
	if _, err := s.ImportCodexPrimaryStdio(context.Background(), b); !errors.Is(err, ps.ErrInvalid) {
		t.Fatal("identity normalization accepted", err)
	}
	if !bytes.Equal(before, stdioDurableBytes(t, s)) {
		t.Fatal("invalid UTF8 mutated rows")
	}
}

func TestPrimaryStdioSnapshotReadinessRequiresEveryDimensionAndAcceptsTerminalEvidence(t *testing.T) {
	for _, kind := range []string{"alive", "image", "directories", "loaded", "idle", "queue", "background", "bootstrap"} {
		t.Run(kind, func(t *testing.T) {
			s, _, _, c, _ := stdioStoreFixture(t, true)
			l := bindStdio(t, s, c)
			batch := stdioBatch(t, c, "incomplete", l.Cursor, nil, func(o *ps.Observation) {
				switch kind {
				case "alive":
					o.Snapshot.ProcessAlive = false
				case "image":
					o.Snapshot.ExactImage = false
				case "directories":
					o.Snapshot.ExactDirectories = false
				case "loaded":
					o.Snapshot.ExactLoadedThread = false
				case "idle":
					o.Snapshot.Idle = false
				case "queue":
					o.Snapshot.EmptyQueue = false
				case "background":
					o.Snapshot.EmptyBackground = false
				case "bootstrap":
					o.Snapshot.Bootstrap = ps.BootstrapAttempted
				}
			})
			importStdio(t, s, batch)
			if err := s.PublishCodexPrimary(context.Background(), stdioAgent(c)); err == nil {
				t.Fatal("partial/terminal snapshot published", kind)
			}
			got, err := s.CodexPrimaryStdio(context.Background(), c.Spec.Binding)
			if err != nil || got.Snapshot != batch.Observation.Snapshot {
				t.Fatal("negative/terminal evidence was discarded", err)
			}
		})
	}
}
