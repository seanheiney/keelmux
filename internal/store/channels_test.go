package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/channels"
	"github.com/seanheiney/keelmux/internal/domain"
)

type channelFixture struct {
	store   *Store
	path    string
	ctx     context.Context
	session domain.Session
	binding channels.Binding
	proof   channels.Evidence
}

func newChannelFixture(t *testing.T) channelFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state", "registry.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	v, _ := recordTest(t, s)
	b := channels.Binding{ID: "binding_a", SessionID: v.ID, ThreadID: v.ThreadID, WorkID: v.WorkID, HostID: v.HostID, Provider: channels.Mattermost, Endpoint: "https://chat.example", RealmID: "realm_a", SelfID: "bot_a", ChannelID: "channel_a", RootID: "root_a", CredentialRef: "local_bot_token", Allowlist: []string{"human_b", "human_a"}, ActivationCutoff: time.Unix(100, 0).UTC()}
	e := channelProof(b)
	b, err = s.BindChannel(context.Background(), b, e)
	if err != nil {
		t.Fatal(err)
	}
	return channelFixture{s, path, context.Background(), v, b, e}
}

func channelProof(b channels.Binding) channels.Evidence {
	return channels.Evidence{Provider: b.Provider, Endpoint: b.Endpoint, RealmID: b.RealmID, SelfID: b.SelfID, ChannelID: b.ChannelID, RootID: b.RootID, Source: "owned_adapter_fixture", ObservedAt: time.Unix(99, 0).UTC()}
}

func (x channelFixture) queue(t *testing.T, id domain.ID) (channels.Message, channels.Delivery) {
	t.Helper()
	m, d, err := x.store.QueueChannelMessage(x.ctx, channels.Message{ID: id, BindingID: x.binding.ID, AuthorID: "local_principal", Text: "Exact note: café 🐚\nNo terminal input."})
	if err != nil {
		t.Fatal(err)
	}
	return m, d
}

func TestChannelDurableClaimAndHistoricalAcknowledgement(t *testing.T) {
	x := newChannelFixture(t)
	m, d := x.queue(t, "message_a")
	if m.Sequence != 1 || d.ID != m.ID || d.State != channels.Queued {
		t.Fatalf("bad queued note: %+v %+v", m, d)
	}
	_, _, d, claimed, err := x.store.ClaimChannelDelivery(x.ctx, d.ID)
	if err != nil || !claimed || d.State != channels.Attempted {
		t.Fatalf("claim: %+v %v %v", d, claimed, err)
	}
	if _, _, _, claimed, err = x.store.ClaimChannelDelivery(x.ctx, d.ID); err != nil || claimed {
		t.Fatalf("second claim: %v %v", claimed, err)
	}
	if err = x.store.DisableChannel(x.ctx, x.binding.ID); err != nil {
		t.Fatal(err)
	}
	d, err = x.store.FinishChannelDelivery(x.ctx, d.ID, channels.SendResult{State: channels.Delivered, RemoteID: "posted_exact", Evidence: x.proof})
	if err != nil || d.State != channels.Delivered {
		t.Fatalf("historical ack: %+v %v", d, err)
	}
	if err = validateChannelSchema(x.ctx, x.store.db); err != nil {
		t.Fatal(err)
	}
}

func TestChannelBindingProvenanceAndPermanentDisable(t *testing.T) {
	x := newChannelFixture(t)
	for name, change := range map[string]func(*channels.Binding){
		"thread":          func(b *channels.Binding) { b.ThreadID = "foreign_thread" },
		"work":            func(b *channels.Binding) { b.WorkID = "foreign_work" },
		"home":            func(b *channels.Binding) { b.HostID = "foreign_home" },
		"missing-root":    func(b *channels.Binding) { b.RootID = "" },
		"duplicate-actor": func(b *channels.Binding) { b.Allowlist = []string{"human_a", "human_a"} },
		"empty-allowlist": func(b *channels.Binding) { b.Allowlist = nil },
		"native-thread":   func(b *channels.Binding) { b.ThreadID = domain.ID(x.session.Execution.NativeThread.Value) },
	} {
		t.Run(name, func(t *testing.T) {
			b := x.binding
			b.ID = domain.ID("binding_" + name)
			change(&b)
			if _, err := x.store.BindChannel(x.ctx, b, channelProof(b)); err == nil {
				t.Fatal("accepted invalid provenance")
			}
		})
	}
	b := x.binding
	b.ID = "binding_other"
	if _, err := x.store.BindChannel(x.ctx, b, channelProof(b)); err == nil {
		t.Fatal("duplicate active endpoint")
	}
	b.Endpoint = "https://second.example"
	if _, err := x.store.BindChannel(x.ctx, b, x.proof); err == nil {
		t.Fatal("foreign endpoint proof")
	}
	if _, err := x.store.BindChannel(x.ctx, b, channelProof(b)); err != nil {
		t.Fatal("distinct endpoint collided:", err)
	}
	old, err := x.store.BindChannel(x.ctx, x.binding, x.proof)
	if err != nil || old.ID != x.binding.ID {
		t.Fatal("identical bind retry", err)
	}
	changed := x.binding
	changed.CredentialRef = "replacement_secret"
	if _, err = x.store.BindChannel(x.ctx, changed, x.proof); err == nil {
		t.Fatal("same binding config replacement")
	}
	if err = x.store.DisableChannel(x.ctx, x.binding.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = x.store.BindChannel(x.ctx, x.binding, x.proof); err == nil {
		t.Fatal("disabled binding rearmed")
	}
	fresh := x.binding
	fresh.ID = "fresh_binding"
	if _, err = x.store.BindChannel(x.ctx, fresh, channelProof(fresh)); err != nil {
		t.Fatal("fresh explicit binding:", err)
	}
	irc := x.binding
	irc.ID, irc.Provider, irc.RootID, irc.Endpoint = "irc_binding", channels.IRC, "", "ircs://irc.example:6697"
	if _, err = x.store.BindChannel(x.ctx, irc, channelProof(irc)); err != nil {
		t.Fatal("explicit IRC channel:", err)
	}
	irc.ID, irc.RootID = "irc_bad", "fake_root"
	if _, err = x.store.BindChannel(x.ctx, irc, channelProof(irc)); err == nil {
		t.Fatal("IRC root invented")
	}
}

func TestChannelLocalRetryIdentityAndOneExplicitDestination(t *testing.T) {
	x := newChannelFixture(t)
	m, d := x.queue(t, "local_retry")
	retry := channels.Message{ID: m.ID, BindingID: m.BindingID, AuthorID: m.AuthorID, Text: m.Text}
	got, again, err := x.store.QueueChannelMessage(x.ctx, retry)
	if err != nil || got != m || again != d {
		t.Fatal("retry changed original receipt", err)
	}
	for name, change := range map[string]func(*channels.Message){
		"author":            func(m *channels.Message) { m.AuthorID = "foreign_author" },
		"text":              func(m *channels.Message) { m.Text = "changed content" },
		"thread":            func(m *channels.Message) { m.ThreadID = "wrong_thread" },
		"remote-origin":     func(m *channels.Message) { m.RemoteID = "foreign_post" },
		"historical-origin": func(m *channels.Message) { m.Historical = true },
	} {
		t.Run(name, func(t *testing.T) {
			q := retry
			change(&q)
			if _, _, e := x.store.QueueChannelMessage(x.ctx, q); e == nil {
				t.Fatal("accepted reused message identity")
			}
		})
	}
	b := x.binding
	b.ID, b.RootID = "second_binding", "second_root"
	if _, err = x.store.BindChannel(x.ctx, b, channelProof(b)); err != nil {
		t.Fatal(err)
	}
	retry.BindingID = b.ID
	if _, _, err = x.store.QueueChannelMessage(x.ctx, retry); err == nil {
		t.Fatal("retry created fanout")
	}
	if pending, err := x.store.PendingChannelDeliveries(x.ctx, b.ID, 10); err != nil || len(pending) != 0 {
		t.Fatal("unexpected second destination", err)
	}
	for name, text := range map[string]string{"invalid-utf8": string([]byte{0xff}), "nul": "a\x00b", "huge": strings.Repeat("x", channels.MaxTextBytes+1), "empty": " \n"} {
		t.Run(name, func(t *testing.T) {
			if _, _, e := x.store.QueueChannelMessage(x.ctx, channels.Message{ID: domain.ID(name), BindingID: x.binding.ID, AuthorID: "owner", Text: text}); e == nil {
				t.Fatal("unsafe text accepted")
			}
		})
	}
	messages, err := x.store.ChannelMessages(x.ctx, x.binding.ThreadID, 0, 100)
	if err != nil || len(messages) != 1 {
		t.Fatal("retry changed history", err)
	}
	if _, err = x.store.ChannelMessages(x.ctx, x.binding.ThreadID, -1, 100); err == nil {
		t.Fatal("negative cursor")
	}
	if _, err = x.store.ChannelMessages(x.ctx, x.binding.ThreadID, 0, 101); err == nil {
		t.Fatal("unbounded history")
	}
}

func TestChannelConcurrentClaimAndRestartNeverReplay(t *testing.T) {
	x := newChannelFixture(t)
	m, _ := x.queue(t, "claim_once")
	var claimed atomic.Int32
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, d, ok, err := x.store.ClaimChannelDelivery(x.ctx, m.ID)
			if err != nil {
				errs <- err
			}
			if ok {
				if d.State != channels.Attempted {
					errs <- errors.New("claim not durable attempted")
				}
				claimed.Add(1)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if claimed.Load() != 1 {
		t.Fatalf("claimed %d times", claimed.Load())
	}
	if err := x.store.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(x.path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	before, err := s.ChannelDelivery(x.ctx, m.ID)
	if err != nil || before.State != channels.Attempted {
		t.Fatal("lost durable attempt", err)
	}
	if n, err := s.RecoverChannelDeliveries(x.ctx); err != nil || n != 1 {
		t.Fatal("recover", n, err)
	}
	if n, err := s.RecoverChannelDeliveries(x.ctx); err != nil || n != 0 {
		t.Fatal("repeated recovery", n, err)
	}
	if _, _, d, ok, err := s.ClaimChannelDelivery(x.ctx, m.ID); err != nil || ok || d.State != channels.Uncertain {
		t.Fatal("recovery replay", ok, d.State, err)
	}
	if pending, err := s.PendingChannelDeliveries(x.ctx, x.binding.ID, 100); err != nil || len(pending) != 0 {
		t.Fatal("uncertain pending replay", err)
	}
	got, d, err := s.QueueChannelMessage(x.ctx, channels.Message{ID: m.ID, BindingID: m.BindingID, AuthorID: m.AuthorID, Text: m.Text})
	if err != nil || got.ID != m.ID || d.State != channels.Uncertain {
		t.Fatal("retry requeued uncertain operation", err)
	}
}

func TestChannelDisableCancelsQueuePreservesAttemptAndHistory(t *testing.T) {
	x := newChannelFixture(t)
	first, _ := x.queue(t, "first")
	second, _ := x.queue(t, "second")
	if _, _, _, ok, err := x.store.ClaimChannelDelivery(x.ctx, first.ID); err != nil || !ok {
		t.Fatal("claim", err)
	}
	if err := x.store.DisableChannel(x.ctx, x.binding.ID); err != nil {
		t.Fatal(err)
	}
	if err := x.store.DisableChannel(x.ctx, x.binding.ID); err != nil {
		t.Fatal("disable retry", err)
	}
	d, err := x.store.ChannelDelivery(x.ctx, second.ID)
	if err != nil || d.State != channels.Failed || d.Code != "binding_disabled" {
		t.Fatal("queue not canceled", err)
	}
	if _, _, _, ok, err := x.store.ClaimChannelDelivery(x.ctx, second.ID); err != nil || ok {
		t.Fatal("disabled queue sent", err)
	}
	d, err = x.store.ChannelDelivery(x.ctx, first.ID)
	if err != nil || d.State != channels.Attempted {
		t.Fatal("in-flight effect fabricated", err)
	}
	if _, _, err := x.store.QueueChannelMessage(x.ctx, channels.Message{ID: "third", BindingID: x.binding.ID, AuthorID: "owner", Text: "new note"}); err == nil {
		t.Fatal("new disabled queue")
	}
	if _, err = x.store.SetState(x.ctx, x.session.ID, x.session.Execution.ID, "stopped"); err != nil {
		t.Fatal(err)
	}
	history, err := x.store.ChannelMessages(x.ctx, x.binding.ThreadID, 0, 100)
	if err != nil || len(history) != 2 || history[0].ID != first.ID || history[1].ID != second.ID {
		t.Fatal("session stop lost authored notes", err)
	}
	if err = validateChannelSchema(x.ctx, x.store.db); err != nil {
		t.Fatal(err)
	}
}

func TestChannelOutcomeRequiresOriginalScopedProof(t *testing.T) {
	for name, change := range map[string]func(*channels.SendResult){
		"endpoint":   func(r *channels.SendResult) { r.Evidence.Endpoint = "https://foreign.example" },
		"realm":      func(r *channels.SendResult) { r.Evidence.RealmID = "foreign_realm" },
		"self":       func(r *channels.SendResult) { r.Evidence.SelfID = "foreign_bot" },
		"channel":    func(r *channels.SendResult) { r.Evidence.ChannelID = "foreign_channel" },
		"root":       func(r *channels.SendResult) { r.Evidence.RootID = "foreign_root" },
		"provider":   func(r *channels.SendResult) { r.Evidence.Provider = channels.Slack },
		"no-proof":   func(r *channels.SendResult) { r.Evidence = channels.Evidence{} },
		"no-post-id": func(r *channels.SendResult) { r.RemoteID = "" },
		"raw-code":   func(r *channels.SendResult) { r.Code = "private-server-error" },
	} {
		t.Run(name, func(t *testing.T) {
			x := newChannelFixture(t)
			m, _ := x.queue(t, "outcome")
			if _, _, _, ok, err := x.store.ClaimChannelDelivery(x.ctx, m.ID); err != nil || !ok {
				t.Fatal(err)
			}
			r := channels.SendResult{State: channels.Delivered, RemoteID: "remote_id", Evidence: x.proof}
			change(&r)
			d, err := x.store.FinishChannelDelivery(x.ctx, m.ID, r)
			if err == nil || d.State != channels.Uncertain || d.Code != "unconfirmed" {
				t.Fatal("wrong target falsely settled", d, err)
			}
			if _, _, _, ok, err := x.store.ClaimChannelDelivery(x.ctx, m.ID); err != nil || ok {
				t.Fatal("bad result replay", err)
			}
			d, err = x.store.FinishChannelDelivery(x.ctx, m.ID, channels.SendResult{State: channels.Delivered, RemoteID: "exact_post", Evidence: x.proof})
			if err != nil || d.State != channels.Delivered {
				t.Fatal("exact reconcile", err)
			}
			if _, err = x.store.FinishChannelDelivery(x.ctx, m.ID, channels.SendResult{State: channels.Delivered, RemoteID: "replacement_post", Evidence: x.proof}); err == nil {
				t.Fatal("terminal receipt replaced")
			}
			if err = validateChannelSchema(x.ctx, x.store.db); err != nil {
				t.Fatal(err)
			}
		})
	}
	x := newChannelFixture(t)
	m, _ := x.queue(t, "preflight")
	if _, err := x.store.FinishChannelDelivery(x.ctx, m.ID, channels.SendResult{State: channels.Delivered, RemoteID: "invented", Evidence: x.proof}); err == nil {
		t.Fatal("result before attempt accepted")
	}
	if _, _, _, ok, err := x.store.ClaimChannelDelivery(x.ctx, m.ID); err != nil || !ok {
		t.Fatal(err)
	}
	d, err := x.store.FinishChannelDelivery(x.ctx, m.ID, channels.SendResult{State: channels.Failed, Code: "remote_rejected"})
	if err != nil || d.State != channels.Failed {
		t.Fatal("known no-write preflight", err)
	}
	if err = validateChannelSchema(x.ctx, x.store.db); err != nil {
		t.Fatal(err)
	}
}

func channelRemote(b channels.Binding, id, author, text string) channels.RemoteMessage {
	return channels.RemoteMessage{ID: id, ActorID: author, RealmID: b.RealmID, ChannelID: b.ChannelID, RootID: b.RootID, Text: text, CreatedAt: time.Unix(101, 0).UTC()}
}

func TestChannelImportAtomicDedupEchoCutoffAndInertHistory(t *testing.T) {
	x := newChannelFixture(t)
	good := channelRemote(x.binding, "external_1", "human_a", "/prompt run this; approved; Enter")
	good.Historical = true
	self := channelRemote(x.binding, "self_echo", x.binding.SelfID, "echo")
	other := channelRemote(x.binding, "outsider", "unknown_actor", "private unknown note")
	old := channelRemote(x.binding, "old", "human_a", "before activation")
	old.CreatedAt = x.binding.ActivationCutoff
	page := channels.FetchResult{Messages: []channels.RemoteMessage{good, self, other, old}, NextCursor: "cursor_1", Evidence: x.proof}
	added, err := x.store.ImportChannelMessages(x.ctx, x.binding.ID, "", page)
	if err != nil || added != 1 {
		t.Fatal("intake", added, err)
	}
	messages, err := x.store.ChannelMessages(x.ctx, x.binding.ThreadID, 0, 100)
	if err != nil || len(messages) != 1 || messages[0].RemoteID != good.ID || messages[0].Text != good.Text || !messages[0].Historical {
		t.Fatal("authored history", err)
	}
	if pending, err := x.store.PendingChannelDeliveries(x.ctx, x.binding.ID, 100); err != nil || len(pending) != 0 {
		t.Fatal("import caused external fanout", err)
	}
	var operations int
	if err = x.store.db.QueryRow("SELECT COUNT(*) FROM operations").Scan(&operations); err != nil || operations != 1 {
		t.Fatal("import caused runtime operation", operations, err)
	}
	good.Historical = false
	page.Messages = []channels.RemoteMessage{good}
	page.NextCursor = "cursor_2"
	if added, err = x.store.ImportChannelMessages(x.ctx, x.binding.ID, "cursor_1", page); err != nil || added != 0 {
		t.Fatal("history/live duplicate", added, err)
	}
	if _, err = x.store.ImportChannelMessages(x.ctx, x.binding.ID, "cursor_1", page); err == nil {
		t.Fatal("stale cursor accepted")
	}
	if err = validateChannelSchema(x.ctx, x.store.db); err != nil {
		t.Fatal(err)
	}
	// Ignored text is not retained as a canonical message or raw payload.
	var ignoredData []byte
	if err = x.store.db.QueryRow("SELECT source_hash FROM channel_inbox WHERE source_id='outsider'").Scan(&ignoredData); err != nil || len(ignoredData) != 32 || bytes.Contains(ignoredData, []byte(other.Text)) {
		t.Fatal("ignored content retained", err)
	}
}

func TestChannelSnapshotEditProgressButIdentityChangesReject(t *testing.T) {
	x := newChannelFixture(t)
	first := channelRemote(x.binding, "stable_post", "human_a", "original snapshot")
	if n, err := x.store.ImportChannelMessages(x.ctx, x.binding.ID, "", channels.FetchResult{Messages: []channels.RemoteMessage{first}, NextCursor: "1", Evidence: x.proof}); err != nil || n != 1 {
		t.Fatal(err)
	}
	edited := first
	edited.Text = "edited snapshot"
	newer := channelRemote(x.binding, "new_post", "human_b", "later useful note")
	if n, err := x.store.ImportChannelMessages(x.ctx, x.binding.ID, "1", channels.FetchResult{Messages: []channels.RemoteMessage{edited, newer}, NextCursor: "2", Evidence: x.proof}); err != nil || n != 1 {
		t.Fatal("edit blocked future intake", n, err)
	}
	msgs, err := x.store.ChannelMessages(x.ctx, x.binding.ThreadID, 0, 100)
	if err != nil || len(msgs) != 2 || msgs[0].Text != first.Text || msgs[1].Text != newer.Text {
		t.Fatal("snapshot overwritten", err)
	}
	for name, change := range map[string]func(*channels.RemoteMessage){"author": func(m *channels.RemoteMessage) { m.ActorID = "human_b" }, "created": func(m *channels.RemoteMessage) { m.CreatedAt = m.CreatedAt.Add(time.Second) }, "root": func(m *channels.RemoteMessage) { m.RootID = "other_root" }} {
		t.Run(name, func(t *testing.T) {
			bad := first
			change(&bad)
			validNew := channelRemote(x.binding, "rollback_"+name, "human_a", "must rollback")
			if _, e := x.store.ImportChannelMessages(x.ctx, x.binding.ID, "2", channels.FetchResult{Messages: []channels.RemoteMessage{validNew, bad}, NextCursor: "3", Evidence: x.proof}); e == nil {
				t.Fatal("source identity changed")
			}
			if _, cursor, e := x.store.ChannelBinding(x.ctx, x.binding.ID); e != nil || cursor != "2" {
				t.Fatal("cursor escaped rollback", cursor, e)
			}
		})
	}
	msgs, err = x.store.ChannelMessages(x.ctx, x.binding.ThreadID, 0, 100)
	if err != nil || len(msgs) != 2 {
		t.Fatal("partial failed page survived", err)
	}
}

func TestChannelGlobalSourceDedupSurvivesRebindButPinsEndpoint(t *testing.T) {
	x := newChannelFixture(t)
	remote := channelRemote(x.binding, "same_post", "human_a", "first canonical note")
	if n, err := x.store.ImportChannelMessages(x.ctx, x.binding.ID, "", channels.FetchResult{Messages: []channels.RemoteMessage{remote}, NextCursor: "1", Evidence: x.proof}); err != nil || n != 1 {
		t.Fatal(err)
	}
	if err := x.store.DisableChannel(x.ctx, x.binding.ID); err != nil {
		t.Fatal(err)
	}
	b := x.binding
	b.ID = "binding_replacement"
	if _, err := x.store.BindChannel(x.ctx, b, channelProof(b)); err != nil {
		t.Fatal(err)
	}
	if n, err := x.store.ImportChannelMessages(x.ctx, b.ID, "", channels.FetchResult{Messages: []channels.RemoteMessage{remote}, NextCursor: "new_cursor", Evidence: channelProof(b)}); err != nil || n != 0 {
		t.Fatal("rebind replayed source", n, err)
	}
	clone := b
	clone.ID = "cloned_server"
	clone.Endpoint = "https://cloned.example"
	if _, err := x.store.BindChannel(x.ctx, clone, channelProof(clone)); err != nil {
		t.Fatal("clone endpoint collision", err)
	}
	if n, err := x.store.ImportChannelMessages(x.ctx, clone.ID, "", channels.FetchResult{Messages: []channels.RemoteMessage{remote}, NextCursor: "clone_cursor", Evidence: channelProof(clone)}); err != nil || n != 1 {
		t.Fatal("different origin source collided", n, err)
	}
	if err := validateChannelSchema(x.ctx, x.store.db); err != nil {
		t.Fatal(err)
	}
}

func TestChannelImportCursorFailureRollsBackSourceAndSequence(t *testing.T) {
	x := newChannelFixture(t)
	if _, err := x.store.db.Exec("CREATE TRIGGER fail_channel_cursor BEFORE UPDATE ON channel_cursors BEGIN SELECT RAISE(ABORT,'fixture'); END"); err != nil {
		t.Fatal(err)
	}
	page := channels.FetchResult{Messages: []channels.RemoteMessage{channelRemote(x.binding, "rollback", "human_a", "new note")}, NextCursor: "1", Evidence: x.proof}
	if _, err := x.store.ImportChannelMessages(x.ctx, x.binding.ID, "", page); err == nil {
		t.Fatal("cursor failure unexpectedly committed")
	}
	if _, err := x.store.db.Exec("DROP TRIGGER fail_channel_cursor"); err != nil {
		t.Fatal(err)
	}
	if _, cursor, err := x.store.ChannelBinding(x.ctx, x.binding.ID); err != nil || cursor != "" {
		t.Fatal("cursor changed", err)
	}
	msgs, err := x.store.ChannelMessages(x.ctx, x.binding.ThreadID, 0, 100)
	if err != nil || len(msgs) != 0 {
		t.Fatal("partial note committed", err)
	}
	if n, err := x.store.ImportChannelMessages(x.ctx, x.binding.ID, "", page); err != nil || n != 1 {
		t.Fatal("source claim survived rollback", n, err)
	}
	msgs, err = x.store.ChannelMessages(x.ctx, x.binding.ThreadID, 0, 100)
	if err != nil || len(msgs) != 1 || msgs[0].Sequence != 1 {
		t.Fatal("sequence escaped rollback", err)
	}
}

func TestChannelDurableWriteFailuresNeverAdmitOrPartiallyRecover(t *testing.T) {
	x := newChannelFixture(t)
	if _, err := x.store.db.Exec("CREATE TRIGGER fail_channel_receipt BEFORE INSERT ON channel_deliveries BEGIN SELECT RAISE(ABORT,'fixture'); END"); err != nil {
		t.Fatal(err)
	}
	intent := channels.Message{ID: "write_failure", BindingID: x.binding.ID, AuthorID: "owner", Text: "unchanged intent"}
	if _, _, err := x.store.QueueChannelMessage(x.ctx, intent); err == nil {
		t.Fatal("queue ignored receipt write failure")
	}
	if notes, err := x.store.ChannelMessages(x.ctx, x.binding.ThreadID, 0, 100); err != nil || len(notes) != 0 {
		t.Fatal("queue published note without receipt", err)
	}
	if _, err := x.store.db.Exec("DROP TRIGGER fail_channel_receipt"); err != nil {
		t.Fatal(err)
	}
	first, _, err := x.store.QueueChannelMessage(x.ctx, intent)
	if err != nil || first.Sequence != 1 {
		t.Fatal("failed queue consumed identity or sequence", err)
	}
	if _, err := x.store.db.Exec("CREATE TRIGGER fail_channel_claim BEFORE UPDATE ON channel_deliveries WHEN NEW.state='attempted' BEGIN SELECT RAISE(ABORT,'fixture'); END"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok, err := x.store.ClaimChannelDelivery(x.ctx, first.ID); err == nil || ok {
		t.Fatal("failed durable claim admitted send", ok, err)
	}
	if got, err := x.store.ChannelDelivery(x.ctx, first.ID); err != nil || got.State != channels.Queued {
		t.Fatal("failed claim changed delivery", err)
	}
	if _, err := x.store.db.Exec("DROP TRIGGER fail_channel_claim"); err != nil {
		t.Fatal(err)
	}
	second, _ := x.queue(t, "second_recovery")
	for _, id := range []domain.ID{first.ID, second.ID} {
		if _, _, _, ok, err := x.store.ClaimChannelDelivery(x.ctx, id); err != nil || !ok {
			t.Fatal("claim after repaired fixture", err)
		}
	}
	if _, err := x.store.db.Exec("CREATE TRIGGER fail_channel_recovery BEFORE UPDATE ON channel_deliveries WHEN NEW.state='uncertain' AND NEW.id='second_recovery' BEGIN SELECT RAISE(ABORT,'fixture'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err := x.store.RecoverChannelDeliveries(x.ctx); err == nil {
		t.Fatal("recovery ignored durable failure")
	}
	for _, id := range []domain.ID{first.ID, second.ID} {
		if got, err := x.store.ChannelDelivery(x.ctx, id); err != nil || got.State != channels.Attempted {
			t.Fatal("partial recovery committed", got.State, err)
		}
	}
	if _, err := x.store.db.Exec("DROP TRIGGER fail_channel_recovery"); err != nil {
		t.Fatal(err)
	}
	if n, err := x.store.RecoverChannelDeliveries(x.ctx); err != nil || n != 2 {
		t.Fatal("recovery after failed transaction", n, err)
	}
}

func TestChannelMigrationLateCollisionLeavesNoPartialSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "registry.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"channel_inbox", "channel_deliveries", "channel_cursors", "channel_messages", "channel_bindings", "chat_threads"} {
		if _, err = s.db.Exec("DROP TABLE " + table); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.db.Exec("PRAGMA user_version=2"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("CREATE TABLE channel_cursors (foreign_owned TEXT)"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if reopened, err := Open(path); err == nil {
		reopened.Close()
		t.Fatal("late migration collision adopted")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version, partial int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 2 {
		t.Fatal("late collision advanced version", version, err)
	}
	if err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name IN ('chat_threads','channel_bindings','channel_active_endpoint','channel_messages','channel_deliveries','channel_inbox')").Scan(&partial); err != nil || partial != 0 {
		t.Fatal("late collision left partial schema", partial, err)
	}
	var original string
	if err = db.QueryRow("SELECT sql FROM sqlite_master WHERE name='channel_cursors'").Scan(&original); err != nil || !strings.Contains(original, "foreign_owned") {
		t.Fatal("collision metadata was replaced", original, err)
	}
}

func TestChannelMigrationCollisionRollsBackVersionAndPreservesLegacy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "registry.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := recordTest(t, s)
	lease := domain.Lease{SessionID: v.ID, ExecutionID: v.Execution.ID, Generation: v.Execution.Generation, OperationID: "legacy_attach", ClientID: "legacy_client", Token: "legacy_fixture_token"}
	if _, err = s.Acquire(context.Background(), lease, false, []byte("legacy opaque payload")); err != nil {
		t.Fatal(err)
	}
	var grantBefore, opBefore, payloadBefore []byte
	if err = s.db.QueryRow("SELECT data FROM grants WHERE operation_id=?", lease.OperationID).Scan(&grantBefore); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow("SELECT data,payload FROM operations WHERE id=?", lease.OperationID).Scan(&opBefore, &payloadBefore); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"channel_inbox", "channel_deliveries", "channel_cursors", "channel_messages", "channel_bindings", "chat_threads"} {
		if _, err = s.db.Exec("DROP TABLE " + table); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.db.Exec("PRAGMA user_version=2"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("CREATE TABLE chat_threads (unowned TEXT)"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if reopened, err := Open(path); err == nil {
		reopened.Close()
		t.Fatal("adopted migration collision")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 2 {
		t.Fatal("failed migration advanced version", version, err)
	}
	var grantAfter, opAfter, payloadAfter []byte
	if err = db.QueryRow("SELECT data FROM grants WHERE operation_id=?", lease.OperationID).Scan(&grantAfter); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("SELECT data,payload FROM operations WHERE id=?", lease.OperationID).Scan(&opAfter, &payloadAfter); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(grantBefore, grantAfter) || !bytes.Equal(opBefore, opAfter) || !bytes.Equal(payloadBefore, payloadAfter) {
		t.Fatal("legacy bytes modified")
	}
	if _, err = db.Exec("DROP TABLE chat_threads"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal("valid additive v2 migration", err)
	}
	defer s.Close()
	if err = s.db.QueryRow("SELECT data FROM grants WHERE operation_id=?", lease.OperationID).Scan(&grantAfter); err != nil || !bytes.Equal(grantBefore, grantAfter) {
		t.Fatal("valid migration rewrote legacy grant", err)
	}
}

func TestChannelEstablishedSchemaAndMetadataFailClosed(t *testing.T) {
	for name, corrupt := range map[string]func(channelFixture) error{
		"missing-table": func(x channelFixture) error { _, e := x.store.db.Exec("DROP TABLE channel_inbox"); return e },
		"nonunique-route": func(x channelFixture) error {
			if _, e := x.store.db.Exec("DROP INDEX channel_active_endpoint"); e != nil {
				return e
			}
			_, e := x.store.db.Exec("CREATE INDEX channel_active_endpoint ON channel_bindings(provider,endpoint,realm_id,channel_id,root_id) WHERE active=1")
			return e
		},
		"missing-check": func(x channelFixture) error {
			if _, e := x.store.db.Exec("DROP TABLE channel_deliveries"); e != nil {
				return e
			}
			ddl := strings.ReplaceAll(channelDefinitions[4].sql, " CHECK(length(payload_hash)=32)", "")
			_, e := x.store.db.Exec(ddl)
			return e
		},
		"missing-fk": func(x channelFixture) error {
			if _, e := x.store.db.Exec("DROP TABLE channel_deliveries"); e != nil {
				return e
			}
			ddl := strings.ReplaceAll(channelDefinitions[4].sql, " REFERENCES channel_messages(id)", "")
			_, e := x.store.db.Exec(ddl)
			return e
		},
		"missing-source-pk": func(x channelFixture) error {
			if _, e := x.store.db.Exec("DROP TABLE channel_inbox"); e != nil {
				return e
			}
			ddl := strings.ReplaceAll(channelDefinitions[5].sql, ", PRIMARY KEY(provider,endpoint,realm_id,channel_id,source_id)", "")
			_, e := x.store.db.Exec(ddl)
			return e
		},
		"binding-active-mirror": func(x channelFixture) error {
			_, e := x.store.db.Exec("UPDATE channel_bindings SET active=0 WHERE id=?", x.binding.ID)
			return e
		},
		"endpoint-mirror": func(x channelFixture) error {
			_, e := x.store.db.Exec("UPDATE channel_bindings SET endpoint='https://wrong.example' WHERE id=?", x.binding.ID)
			return e
		},
		"lost-cursor": func(x channelFixture) error {
			_, e := x.store.db.Exec("DELETE FROM channel_cursors WHERE binding_id=?", x.binding.ID)
			return e
		},
		"thread-sequence": func(x channelFixture) error {
			_, e := x.store.db.Exec("UPDATE chat_threads SET next_sequence=3 WHERE thread_id=?", x.binding.ThreadID)
			return e
		},
	} {
		t.Run(name, func(t *testing.T) {
			x := newChannelFixture(t)
			if err := corrupt(x); err != nil {
				t.Fatal(err)
			}
			x.store.Close()
			if s, err := Open(x.path); err == nil {
				s.Close()
				t.Fatal("established corruption recreated authority")
			}
			db, e := sql.Open("sqlite", x.path)
			if e != nil {
				t.Fatal(e)
			}
			defer db.Close()
			var v int
			if e = db.QueryRow("PRAGMA user_version").Scan(&v); e != nil || v != 3 {
				t.Fatal("corruption changed version", v, e)
			}
		})
	}
	for name, corrupt := range map[string]func(channelFixture, channels.Message, channels.Delivery) error{
		"lost-receipt": func(x channelFixture, m channels.Message, d channels.Delivery) error {
			_, e := x.store.db.Exec("DELETE FROM channel_deliveries WHERE id=?", d.ID)
			return e
		},
		"wrong-proof": func(x channelFixture, m channels.Message, d channels.Delivery) error {
			_, _, _, _, e := x.store.ClaimChannelDelivery(x.ctx, d.ID)
			if e != nil {
				return e
			}
			if _, e = x.store.FinishChannelDelivery(x.ctx, d.ID, channels.SendResult{State: channels.Delivered, RemoteID: "exact", Evidence: x.proof}); e != nil {
				return e
			}
			p := x.proof
			p.Endpoint = "https://wrong.example"
			_, e = x.store.db.Exec("UPDATE channel_deliveries SET proof=? WHERE id=?", encoded(p), d.ID)
			return e
		},
		"duplicate-json": func(x channelFixture, m channels.Message, d channels.Delivery) error {
			raw := encoded(d)
			raw = append(raw[:len(raw)-1], []byte(fmt.Sprintf(",\"ID\":%q}", d.ID))...)
			_, e := x.store.db.Exec("UPDATE channel_deliveries SET data=? WHERE id=?", raw, d.ID)
			return e
		},
		"hash-mismatch": func(x channelFixture, m channels.Message, d channels.Delivery) error {
			d.PayloadHash = [32]byte{}
			_, e := x.store.db.Exec("UPDATE channel_deliveries SET payload_hash=?,data=? WHERE id=?", d.PayloadHash[:], encoded(d), d.ID)
			return e
		},
	} {
		t.Run(name, func(t *testing.T) {
			x := newChannelFixture(t)
			m, d := x.queue(t, "corrupt")
			if e := corrupt(x, m, d); e != nil {
				t.Fatal(e)
			}
			x.store.Close()
			if s, e := Open(x.path); e == nil {
				s.Close()
				t.Fatal("corrupt receipt adopted")
			}
		})
	}
}
