package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/seanheiney/keelmux/internal/channels"
	"github.com/seanheiney/keelmux/internal/clientstate"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
)

func channelBinding(id, thread string) protocol.ChannelStatus {
	return protocol.ChannelStatus{ID: domain.ID(id), ThreadID: domain.ID(thread), Provider: channels.Mattermost, Endpoint: "https://chat.example.test", RealmID: "realm", SelfID: "bot", ChannelID: "channel", RootID: "root", Active: true}
}
func channelSnapshot(m *model, bindings ...protocol.ChannelStatus) channelSnapshotMsg {
	return channelSnapshotMsg{thread: m.channelThread, epoch: m.channelEpoch, after: m.channelAfter, bindings: bindings, history: protocol.ChannelHistory{ThreadID: m.channelThread, SnapshotOnly: true}}
}
func readyChannelModel(t *testing.T) (*model, protocol.ChannelStatus) {
	m := testModel(t, clientstate.New())
	m.sessions = []domain.Session{fixtureSession("a", "A"), fixtureSession("b", "B")}
	m.selectSession(m.sessions[0])
	m.openChannels()
	b := channelBinding("binding_a", "thread_a")
	m.acceptChannelSnapshot(channelSnapshot(m, b, channelBinding("binding_b", "thread_b")))
	m.chooseChannelBinding()
	m.channelDraft.Text = "reply\nsecond line"
	m.state.SetChannelDraft(m.channelDraft)
	m.screen = "channelreply"
	return m, b
}
func receipt(p clientstate.ChannelIntent, b protocol.ChannelStatus, state channels.DeliveryState) channels.Delivery {
	d := channels.Delivery{ID: p.MessageID, MessageID: p.MessageID, BindingID: p.BindingID, State: state, PayloadHash: channels.PayloadHash(wireBinding(b), intentMessage(p)), UpdatedAt: time.Now().UTC()}
	if state == channels.Delivered {
		d.RemoteID = "remote-exact"
	}
	return d
}

type channelHTTPFixture struct {
	mu                   sync.Mutex
	bindings             []protocol.ChannelStatus
	posts                []protocol.ChannelSendRequest
	deliveries           map[domain.ID]channels.Delivery
	messages             []channels.Message
	afterRequests        []int64
	lost                 bool
	durableBeforeRequest bool
	reconciles           int
	statePath            string
}

func newChannelHTTPFixture(t *testing.T, m *model, b protocol.ChannelStatus) *channelHTTPFixture {
	f := &channelHTTPFixture{bindings: []protocol.ChannelStatus{b, channelBinding("other", "other_thread")}, deliveries: map[domain.ID]channels.Delivery{}, statePath: m.statePath}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/channels":
			json.NewEncoder(w).Encode(f.bindings)
		case "/v1/channels/history":
			after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
			f.afterRequests = append(f.afterRequests, after)
			var notes []channels.Message
			for _, note := range f.messages {
				if note.Sequence > after {
					notes = append(notes, note)
					if len(notes) == channels.MaxPageSize {
						break
					}
				}
			}
			json.NewEncoder(w).Encode(protocol.ChannelHistory{ThreadID: domain.ID(r.URL.Query().Get("thread")), SnapshotOnly: true, Messages: notes})
		case "/v1/channels/send":
			var p protocol.ChannelSendRequest
			if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			f.posts = append(f.posts, p)
			loaded, err := clientstate.Load(f.statePath)
			if err == nil && loaded.PendingChannel != nil && loaded.PendingChannel.MessageID == p.ID && loaded.PendingChannel.BindingID == p.BindingID && loaded.PendingChannel.Text == p.Text {
				f.durableBeforeRequest = true
			}
			intent := clientstate.ChannelIntent{MessageID: p.ID, BindingID: p.BindingID, ThreadID: b.ThreadID, Text: p.Text}
			d := receipt(intent, b, channels.Queued)
			f.deliveries[p.ID] = d
			if f.lost {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					conn.Close()
				}
				return
			}
			note := intentMessage(intent)
			note.Sequence = 1
			note.CreatedAt = time.Now().UTC()
			json.NewEncoder(w).Encode(protocol.ChannelSendResponse{Message: note, Delivery: d})
		case "/v1/channels/delivery":
			d, ok := f.deliveries[domain.ID(r.URL.Query().Get("id"))]
			if !ok {
				w.WriteHeader(404)
				return
			}
			json.NewEncoder(w).Encode(d)
		case "/v1/channels/reconcile":
			f.reconciles++
			var p struct {
				ID domain.ID `json:"message_id"`
			}
			json.NewDecoder(r.Body).Decode(&p)
			json.NewEncoder(w).Encode(f.deliveries[p.ID])
		default:
			t.Errorf("unexpected channel call %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(s.Close)
	m.client = &protocol.Client{HTTP: s.Client(), BaseURL: s.URL}
	return f
}

func TestChannelExplicitChoiceWrongThreadAndStaleRefresh(t *testing.T) {
	m, b := readyChannelModel(t)
	m.channelBinding = nil
	if m.sendChannelReply() != nil || m.state.PendingChannel != nil {
		t.Fatal("unchosen destination auto-selected for send")
	}
	if len(m.channelBindings) != 1 {
		t.Fatal("global binding leaked into thread choice")
	}
	prior := channelSnapshot(m, b)
	m.fetchChannels()
	fresh := channelSnapshot(m, b)
	fresh.history.Messages = []channels.Message{{ID: "note", ThreadID: m.channelThread, BindingID: b.ID, Sequence: 1, AuthorID: "user", Text: "fresh text"}}
	m.acceptChannelSnapshot(fresh)
	m.acceptChannelSnapshot(prior)
	if len(m.channelMessages) != 1 || m.channelMessages[0].Text != "fresh text" {
		t.Fatal("older refresh replaced newer authored snapshot")
	}
	m.channelThread = "thread_b"
	m.channelEpoch++
	m.acceptChannelSnapshot(fresh)
	if m.channelMessages[0].Text != "fresh text" {
		t.Fatal("stale fetch mutated current view")
	}
	for _, mutate := range []func(*channelSnapshotMsg){
		func(s *channelSnapshotMsg) { s.history.ThreadID = "wrong" },
		func(s *channelSnapshotMsg) { s.history.SemanticDispatch = true },
		func(s *channelSnapshotMsg) { s.history.SnapshotOnly = false },
		func(s *channelSnapshotMsg) {
			s.history.Messages = []channels.Message{{ID: "n", ThreadID: "wrong", BindingID: b.ID, Sequence: 1, AuthorID: "user", Text: "private other thread"}}
		},
		func(s *channelSnapshotMsg) { s.bindings = append(s.bindings, s.bindings[0]) },
		func(s *channelSnapshotMsg) {
			s.history.Messages = []channels.Message{{ID: "n", ThreadID: s.thread, BindingID: "other-thread-binding", Sequence: 1, AuthorID: "user", Text: "claimed wrong route"}}
		},
	} {
		m, b := readyChannelModel(t)
		s := channelSnapshot(m, b)
		mutate(&s)
		m.acceptChannelSnapshot(s)
		if m.channelError == "" || m.sendChannelReply() != nil {
			t.Fatal("invalid scope permitted first send")
		}
	}
}

func TestChannelBindingChangesAndDisabledPreflightDoNotPost(t *testing.T) {
	for _, change := range []func(*protocol.ChannelStatus){func(b *protocol.ChannelStatus) { b.Active = false }, func(b *protocol.ChannelStatus) { b.ThreadID = "wrong" }, func(b *protocol.ChannelStatus) { b.Endpoint = "https://different.example.test" }, func(b *protocol.ChannelStatus) { b.RootID = "another-root" }} {
		m, b := readyChannelModel(t)
		f := newChannelHTTPFixture(t, m, b)
		change(&f.bindings[0])
		cmd := m.sendChannelReply()
		if cmd == nil {
			t.Fatal("expected durable intent command")
		}
		m.Update(cmd())
		if len(f.posts) != 0 || m.state.PendingChannel == nil {
			t.Fatal("changed binding posted or erased immutable intent")
		}
	}
	m, b := readyChannelModel(t)
	b.Active = false
	m.acceptChannelSnapshot(channelSnapshot(m, b, channelBinding("replacement", "thread_a")))
	if m.channelBinding != nil || m.channelError != "" {
		t.Fatal("disabled old choice became fatal global destination refusal")
	}
	m.channelIndex = 1
	m.chooseChannelBinding()
	if m.channelBinding == nil || m.channelBinding.ID != "replacement" {
		t.Fatal("explicit new active destination unavailable")
	}
}

func TestChannelEnterIncomingTextAndNativeDraftRemainInert(t *testing.T) {
	m, b := readyChannelModel(t)
	f := newChannelHTTPFixture(t, m, b)
	m.state.SetDraft(clientstate.Draft{ThreadID: "thread_a", RecipientID: "lane_a", Text: "native untouched"})
	old := m.channelDraft.Text
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil || m.channelDraft.Text != old+"\n" || len(f.posts) != 0 {
		t.Fatal("Enter submitted reply")
	}
	m.channelMessages = []channels.Message{{ID: "incoming", ThreadID: "thread_a", BindingID: b.ID, Sequence: 1, AuthorID: "external", Text: "/run $(touch never)\x1b[2J", RemoteID: "remote"}}
	m.screen = "channels"
	for _, k := range []string{"d", "u"} {
		_, cmd = m.Update(key(k))
		if cmd != nil {
			t.Fatal("incoming text received delivery action")
		}
	}
	if d, _ := m.state.Draft("thread_a", "lane_a"); d.Text != "native untouched" {
		t.Fatal("channel editor changed native lane draft")
	}
	if strings.Contains(m.View(), "\x1b[2J") {
		t.Fatal("authored text supplied terminal control")
	}
}

func TestChannelLostAcknowledgementHasDurableExactIntentAndNoSecondPost(t *testing.T) {
	m, b := readyChannelModel(t)
	f := newChannelHTTPFixture(t, m, b)
	f.lost = true
	cmd := m.sendChannelReply()
	p := *m.state.PendingChannel
	m.Update(cmd())
	f.mu.Lock()
	durable, postCount := f.durableBeforeRequest, len(f.posts)
	f.mu.Unlock()
	if !durable || postCount != 1 || m.state.PendingChannel == nil {
		t.Fatal("lost ack not fenced by persisted intent")
	}
	m.Update(key("edited"))
	if m.channelDraft.Text != p.Text || *m.state.PendingChannel != p {
		t.Fatal("lost-ack intent edited")
	}
	cmd = m.sendChannelReply()
	if cmd == nil {
		t.Fatal("explicit retry should query receipt")
	}
	m.Update(cmd())
	if len(f.posts) != 1 || m.state.PendingChannel != nil || m.channelDraft.Text != "" || !strings.Contains(m.status, "Queued locally") {
		t.Fatal("known durable queue ack replayed external send or claimed delivery")
	}
	loaded, err := clientstate.Load(m.statePath)
	if err != nil || loaded.PendingChannel != nil {
		t.Fatal("queue acknowledgement not durably cleared")
	}
}

func TestChannelAbsentReceiptRetriesSameIDOnlyAfterExplicitControl(t *testing.T) {
	m, b := readyChannelModel(t)
	f := newChannelHTTPFixture(t, m, b)
	p := clientstate.ChannelIntent{MessageID: "frozen-id", BindingID: b.ID, ThreadID: b.ThreadID, Text: m.channelDraft.Text}
	m.state.PendingChannel = &p
	m.save()
	cmd := m.sendChannelReply()
	msg := cmd()
	next := m.acceptChannelReceipt(msg.(channelReceiptMsg))
	if len(f.posts) != 0 || next == nil {
		t.Fatal("read-only lookup sent effect or failed absent-ack recovery")
	}
	m.Update(next())
	if len(f.posts) != 1 || f.posts[0].ID != p.MessageID || f.posts[0].Text != p.Text || f.posts[0].BindingID != p.BindingID || !f.durableBeforeRequest {
		t.Fatal("retry changed frozen identity/content")
	}
	// Merely visiting a different work item cannot redirect that retry.
	m, b = readyChannelModel(t)
	f = newChannelHTTPFixture(t, m, b)
	m.state.PendingChannel = &p
	m.save()
	cmd = m.sendChannelReply()
	m.channelThread = "thread_b"
	m.channelBinding = nil
	if m.acceptChannelReceipt(cmd().(channelReceiptMsg)) != nil || len(f.posts) != 0 || m.state.PendingChannel == nil {
		t.Fatal("selection change redirected immutable reply")
	}
}

func TestChannelReceiptHashIDsAndStatesAreContentAuthority(t *testing.T) {
	for _, change := range []func(*channels.Delivery){
		func(d *channels.Delivery) { d.PayloadHash[0] ^= 1 }, func(d *channels.Delivery) { d.ID = "wrong" }, func(d *channels.Delivery) { d.MessageID = "wrong" }, func(d *channels.Delivery) { d.BindingID = "wrong" }, func(d *channels.Delivery) { d.State = "accepted-model" }, func(d *channels.Delivery) { d.Code = "unknown-private-error" },
	} {
		m, b := readyChannelModel(t)
		p := clientstate.ChannelIntent{MessageID: "message", BindingID: b.ID, ThreadID: b.ThreadID, Text: m.channelDraft.Text}
		m.state.PendingChannel = &p
		m.save()
		d := receipt(p, b, channels.Queued)
		change(&d)
		m.acceptChannelReceipt(channelReceiptMsg{intent: p, binding: b, delivery: d, resolvePending: true})
		if m.state.PendingChannel == nil || m.channelDraft.Text != p.Text {
			t.Fatal("untrusted receipt cleared immutable intent")
		}
	}
	for _, state := range []channels.DeliveryState{channels.Queued, channels.Attempted, channels.Uncertain, channels.Delivered, channels.Failed} {
		m, b := readyChannelModel(t)
		f := newChannelHTTPFixture(t, m, b)
		p := clientstate.ChannelIntent{MessageID: "message", BindingID: b.ID, ThreadID: b.ThreadID, Text: m.channelDraft.Text}
		m.state.PendingChannel = &p
		m.save()
		f.bindings[0].Active = false
		f.deliveries[p.MessageID] = receipt(p, b, state)
		m.Update(m.channelReceiptCommand(p, true, false)())
		if m.state.PendingChannel != nil || len(f.posts) != 0 {
			t.Fatal("existing durable receipt not resolved, or uncertain/disabled receipt retried")
		}
		if state == channels.Delivered && !strings.Contains(m.status, "no model acceptance") {
			t.Fatal("external delivery became model acceptance")
		}
	}
}

func TestChannelRestartQueriesOnlyExactReceiptAndPreservesCurrentSelection(t *testing.T) {
	m, b := readyChannelModel(t)
	f := newChannelHTTPFixture(t, m, b)
	p := clientstate.ChannelIntent{MessageID: "message", BindingID: b.ID, ThreadID: b.ThreadID, Text: m.channelDraft.Text}
	m.state.PendingChannel = &p
	m.save()
	s, err := clientstate.Load(m.statePath)
	if err != nil {
		t.Fatal(err)
	}
	restarted := newModel(m.ctx, m.client, m.statePath, s)
	restarted.sessions = []domain.Session{fixtureSession("b", "B")}
	restarted.selectSession(restarted.sessions[0])
	f.deliveries[p.MessageID] = receipt(p, b, channels.Uncertain)
	restarted.Update(restarted.channelReceiptCommand(*restarted.state.PendingChannel, false, false)())
	if len(f.posts) != 0 || restarted.state.PendingChannel != nil || restarted.place().SessionID != "b" || !strings.Contains(restarted.status, "uncertain") {
		t.Fatal("restart replayed/retargeted pending reply")
	}
}

func TestChannelSaveFailureCannotSendOrEraseKnownQueueReceipt(t *testing.T) {
	m, b := readyChannelModel(t)
	f := newChannelHTTPFixture(t, m, b)
	blocked := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocked, []byte("not directory"), 0600)
	m.statePath = filepath.Join(blocked, "place.json")
	if m.sendChannelReply() != nil || len(f.posts) != 0 {
		t.Fatal("first effect before durable save")
	}
	p := *m.state.PendingChannel
	m.acknowledgeChannelIntent(p, receipt(p, b, channels.Queued))
	if m.state.PendingChannel == nil || *m.state.PendingChannel != p || m.channelDraft.Text != p.Text {
		t.Fatal("failed local ack save erased immutable recovery")
	}
}

func TestChannelStaleReceiptAndReadOnlyReconciliation(t *testing.T) {
	m, b := readyChannelModel(t)
	f := newChannelHTTPFixture(t, m, b)
	p := clientstate.ChannelIntent{MessageID: "message", BindingID: b.ID, ThreadID: b.ThreadID, Text: m.channelDraft.Text}
	f.deliveries[p.MessageID] = receipt(p, b, channels.Uncertain)
	cmd := m.channelReceiptCommand(p, false, true)
	oldStatus := m.status
	m.channelEpoch++
	m.Update(cmd())
	if m.status != oldStatus || len(m.channelReceipts) != 0 || f.reconciles != 1 || len(f.posts) != 0 {
		t.Fatal("stale receipt touched new view or reconciliation repeated post")
	}
	// Malformed source response does not become a known queue acknowledgement.
	m.state.PendingChannel = &p
	m.save()
	m.acceptChannelSend(channelSendMsg{intent: p, binding: b, err: errors.New("private transport error")})
	if m.state.PendingChannel == nil || strings.Contains(m.status, "private transport error") {
		t.Fatal("opaque error leaked or intent erased")
	}
}

func TestChannelLayoutsResizeClipAndKeepExplicitAction(t *testing.T) {
	m, b := readyChannelModel(t)
	m.channelMessages = []channels.Message{{ID: "note", ThreadID: b.ThreadID, BindingID: b.ID, Sequence: 1, AuthorID: "user\x1b[2J", Text: "Unicode 界界\ntext \x1b[31m\u202e inert", RemoteID: "remote"}}
	for _, screen := range []string{"channels", "channelbinding", "channelreply", "channelhelp"} {
		for _, size := range [][2]int{{80, 24}, {120, 32}, {40, 14}, {20, 8}, {1, 1}} {
			m.screen = screen
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			m.color = true
			view := m.View()
			rows := strings.Split(view, "\n")
			if len(rows) > size[1] {
				t.Fatal("channel viewport exceeded rows")
			}
			for _, row := range rows {
				if ansi.StringWidth(row) > size[0] {
					t.Fatal("channel viewport exceeded cell width")
				}
			}
			if strings.Contains(view, "\x1b[2J") || strings.Contains(view, "\u202e") || strings.Contains(view, "\x1b[31m") {
				t.Fatal("untrusted authored controls painted")
			}
			if screen == "channelreply" && size[0] >= 40 && !strings.Contains(view, "Ctrl-S queue") {
				t.Fatal("explicit sending control hidden")
			}
		}
	}
	if !reflect.DeepEqual(m.place().SessionID, domain.ID("a")) {
		t.Fatal("channel resize lost native selection")
	}
}

func TestChannelIntakeIsDiagnosticAndLongNotesRemainReadable(t *testing.T) {
	m, b := readyChannelModel(t)
	f := newChannelHTTPFixture(t, m, b)
	now := time.Now().UTC()
	f.bindings[0].Intake = "unavailable"
	f.bindings[0].LastCheck = &now
	f.bindings[0].LastSuccess = &now
	f.bindings[0].HistoryGap = true
	f.bindings[0].MoreHistory = true
	f.bindings[0].Coverage = "live_connection_only"
	cmd := m.sendChannelReply()
	m.Update(cmd())
	if len(f.posts) != 1 || m.state.PendingChannel != nil {
		t.Fatal("transient intake changed immutable routing or receipt hash")
	}
	if !strings.Contains(intakeLabel(f.bindings[0]), "history gap") || strings.Contains(intakeLabel(protocol.ChannelStatus{}), "healthy") {
		t.Fatal("active/unobserved binding implied healthy history")
	}
	m.screen = "channels"
	m.channelLoading = false
	m.width = 40
	m.height = 14
	m.status = ""
	m.channelMessages = []channels.Message{{ID: "note", ThreadID: b.ThreadID, BindingID: b.ID, AuthorID: "user", Sequence: 1, RemoteID: "remote", Text: strings.Repeat("first row\n", 25) + "last authored line"}}
	m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	if m.channelNoteOffset == 0 {
		t.Fatal("long note had no text navigation")
	}
	for i := 0; i < 6; i++ {
		m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	}
	if !strings.Contains(m.View(), "last authored line") {
		t.Fatal("final authored text inaccessible")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	m.Update(key("?"))
	if !strings.Contains(m.View(), "u  Reconcile") || !strings.Contains(m.View(), "Ctrl-S") {
		t.Fatal("narrow viewport lost explicit receipt/send guidance")
	}
}

func TestChannelHistoryPagesReachLaterNotesAndFenceCursorResponses(t *testing.T) {
	m, b := readyChannelModel(t)
	f := newChannelHTTPFixture(t, m, b)
	for seq := int64(1); seq <= 102; seq++ {
		note := channels.Message{ID: domain.ID(fmt.Sprintf("note_%d", seq)), ThreadID: b.ThreadID, BindingID: b.ID, AuthorID: "external", Sequence: seq, Text: fmt.Sprintf("authored note %d", seq), RemoteID: fmt.Sprintf("remote_%d", seq)}
		if seq == 102 {
			note.AuthorID = "local-operator"
			note.RemoteID = ""
			note.Text = "later outgoing reply"
		}
		f.messages = append(f.messages, note)
	}
	last := f.messages[101]
	p := clientstate.ChannelIntent{MessageID: last.ID, BindingID: last.BindingID, ThreadID: last.ThreadID, Text: last.Text}
	f.deliveries[last.ID] = receipt(p, b, channels.Delivered)
	chosen := *m.channelBinding
	draft := m.channelDraft
	m.screen = "channels"
	m.Update(m.fetchChannels()())
	if len(m.channelMessages) != 100 || m.channelMessages[99].Sequence != 100 {
		t.Fatal("first bounded page not exact")
	}
	old := channelSnapshot(m, b)
	old.history.Messages = append([]channels.Message{}, m.channelMessages...)
	cmd := m.nextChannelPage()
	if cmd == nil || m.channelAfter != 100 || len(m.channelBack) != 1 {
		t.Fatal("explicit next page did not preserve cursor")
	}
	m.Update(cmd())
	if len(m.channelMessages) != 2 || m.channelMessages[0].Sequence != 101 || m.channelMessages[1].Text != "later outgoing reply" {
		t.Fatal("later incoming/outgoing authored notes inaccessible")
	}
	if !sameBinding(chosen, *m.channelBinding) || m.channelDraft != draft {
		t.Fatal("page change redirected chosen destination/draft")
	}
	m.acceptChannelSnapshot(old)
	if m.channelAfter != 100 || m.channelMessages[0].Sequence != 101 {
		t.Fatal("old page response overwrote later page")
	}
	m.channelScroll = 1
	_, cmd = m.Update(key("d"))
	if cmd == nil {
		t.Fatal("later outgoing receipt inaccessible")
	}
	m.Update(cmd())
	if m.channelReceipts[last.ID].State != channels.Delivered || len(f.posts) != 0 {
		t.Fatal("later receipt query missing or sent new effect")
	}
	m.Update(m.previousChannelPage()())
	if m.channelAfter != 0 || len(m.channelMessages) != 100 || len(m.channelBack) != 0 || !reflect.DeepEqual(f.afterRequests, []int64{0, 100, 0}) {
		t.Fatal("previous page did not restore exact cursor")
	}
	// Same thread/epoch with a wrong cursor cannot pass, and a response whose
	// first sequence precedes the requested after is not a valid local page.
	wrong := channelSnapshot(m, b)
	wrong.after = 100
	wrong.history.Messages = f.messages[100:]
	m.acceptChannelSnapshot(wrong)
	if m.channelMessages[0].Sequence != 1 {
		t.Fatal("wrong requested cursor accepted")
	}
	m.channelAfter = 100
	m.channelEpoch++
	wrong = channelSnapshot(m, b)
	wrong.history.Messages = f.messages[99:]
	m.acceptChannelSnapshot(wrong)
	if m.channelError == "" {
		t.Fatal("page accepted sequence <= requested after")
	}
	m.selectSession(m.sessions[1])
	m.openChannels()
	if m.channelAfter != 0 || len(m.channelBack) != 0 || m.channelThread != "thread_b" || m.channelBinding != nil {
		t.Fatal("thread switch retained old page or destination")
	}
}
