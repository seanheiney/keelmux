package tui

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/seanheiney/keelmux/internal/channels"
	"github.com/seanheiney/keelmux/internal/clientstate"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
)

type channelSnapshotMsg struct {
	thread   domain.ID
	epoch    uint64
	bindings []protocol.ChannelStatus
	history  protocol.ChannelHistory
	err      error
}
type channelSendMsg struct {
	intent   clientstate.ChannelIntent
	binding  protocol.ChannelStatus
	response protocol.ChannelSendResponse
	err      error
}
type channelReceiptMsg struct {
	intent         clientstate.ChannelIntent
	binding        protocol.ChannelStatus
	delivery       channels.Delivery
	err            error
	retry          bool
	resolvePending bool
	epoch          uint64
}

func wireBinding(b protocol.ChannelStatus) channels.Binding {
	return channels.Binding{ID: b.ID, ThreadID: b.ThreadID, Provider: b.Provider, Endpoint: b.Endpoint, RealmID: b.RealmID, SelfID: b.SelfID, ChannelID: b.ChannelID, RootID: b.RootID}
}
func intentMessage(p clientstate.ChannelIntent) channels.Message {
	return channels.Message{ID: p.MessageID, ThreadID: p.ThreadID, BindingID: p.BindingID, AuthorID: "local-operator", Text: p.Text}
}
func validStatus(b protocol.ChannelStatus) bool {
	if b.SemanticDispatch || (b.Provider != channels.Mattermost && b.Provider != channels.Slack && b.Provider != channels.IRC) {
		return false
	}
	for _, value := range []string{string(b.ID), string(b.ThreadID), b.Endpoint, b.RealmID, b.SelfID, b.ChannelID} {
		if !channels.ValidID(value) {
			return false
		}
	}
	return (b.Provider == channels.IRC && b.RootID == "") || (b.Provider != channels.IRC && channels.ValidID(b.RootID))
}
func sameBinding(a, b protocol.ChannelStatus) bool {
	return reflect.DeepEqual(wireBinding(a), wireBinding(b)) && a.SessionID == b.SessionID && a.ActivationCutoff.Equal(b.ActivationCutoff) && reflect.DeepEqual(a.Allowlist, b.Allowlist) && a.SemanticDispatch == b.SemanticDispatch
}
func exactBinding(all []protocol.ChannelStatus, thread, id domain.ID) (protocol.ChannelStatus, error) {
	var found protocol.ChannelStatus
	n := 0
	for _, b := range all {
		if b.ID == id {
			if b.ThreadID != thread || !validStatus(b) {
				return found, errors.New("binding scope mismatch")
			}
			found = b
			n++
		}
	}
	if n != 1 {
		return found, errors.New("binding unavailable or ambiguous")
	}
	return found, nil
}
func validReceipt(p clientstate.ChannelIntent, b protocol.ChannelStatus, d channels.Delivery) bool {
	if b.ID != p.BindingID || b.ThreadID != p.ThreadID || !validStatus(b) || d.ID != p.MessageID || d.MessageID != p.MessageID || d.BindingID != p.BindingID || d.PayloadHash != channels.PayloadHash(wireBinding(b), intentMessage(p)) || !channels.ValidCode(d.Code) {
		return false
	}
	switch d.State {
	case channels.Queued, channels.Attempted, channels.Delivered, channels.Failed, channels.Uncertain:
		return d.State != channels.Delivered || channels.ValidID(d.RemoteID)
	}
	return false
}

func (m *model) openChannels() tea.Cmd {
	thread := m.place().ThreadID
	if s, ok := m.selected(); ok {
		thread = s.ThreadID
	}
	if thread == "" {
		m.status = "Select a stable thread before opening authored notes."
		return nil
	}
	m.channelEpoch++
	m.channelThread = thread
	m.channelBindings = nil
	m.channelMessages = nil
	m.channelBinding = nil
	m.channelIndex = 0
	m.channelScroll = 0
	m.channelNoteOffset = 0
	m.channelError = ""
	m.channelDraft = clientstate.ChannelDraft{}
	m.screen = "channels"
	m.status = ""
	return m.fetchChannels()
}
func (m *model) fetchChannels() tea.Cmd {
	m.channelEpoch++
	m.channelLoading = true
	thread, epoch := m.channelThread, m.channelEpoch
	return func() tea.Msg {
		bindings, err := m.client.Channels(m.ctx)
		var history protocol.ChannelHistory
		if err == nil {
			history, err = m.client.ChannelHistory(m.ctx, thread, 0, channels.MaxPageSize)
		}
		return channelSnapshotMsg{thread, epoch, bindings, history, err}
	}
}
func (m *model) acceptChannelSnapshot(msg channelSnapshotMsg) tea.Cmd {
	if msg.thread != m.channelThread || msg.epoch != m.channelEpoch || !strings.HasPrefix(m.screen, "channel") {
		return nil
	}
	m.channelLoading = false
	if msg.err != nil {
		m.channelError = "Channel inbox unavailable at this connection; r retries."
		return nil
	}
	if msg.history.ThreadID != msg.thread || msg.history.SemanticDispatch || !msg.history.SnapshotOnly {
		m.channelError = "Authored snapshot scope rejected; no reply can be sent."
		return nil
	}
	var bindings []protocol.ChannelStatus
	seen := map[domain.ID]bool{}
	for _, b := range msg.bindings {
		if b.ThreadID != msg.thread {
			continue
		}
		if !validStatus(b) || seen[b.ID] {
			m.channelError = "Destination snapshot rejected; repair required."
			return nil
		}
		seen[b.ID] = true
		bindings = append(bindings, b)
	}
	knownBindings := seen
	seen = map[domain.ID]bool{}
	var sequence int64
	for _, note := range msg.history.Messages {
		if note.ThreadID != msg.thread || !knownBindings[note.BindingID] || !channels.ValidID(string(note.ID)) || !channels.ValidID(string(note.BindingID)) || !channels.ValidID(note.AuthorID) || !channels.ValidText(note.Text) || note.Sequence <= sequence || seen[note.ID] {
			m.channelError = "Authored message scope rejected; no reply can be sent."
			return nil
		}
		sequence = note.Sequence
		seen[note.ID] = true
	}
	if len(msg.history.Messages) > channels.MaxPageSize {
		m.channelError = "Authored snapshot page rejected."
		return nil
	}
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].ID < bindings[j].ID })
	if m.channelBinding != nil {
		fresh, err := exactBinding(bindings, msg.thread, m.channelBinding.ID)
		if err != nil || !fresh.Active {
			m.channelBinding = nil
			m.channelError = ""
			m.status = "Destination disabled or unavailable; b chooses a destination."
		} else if !sameBinding(*m.channelBinding, fresh) {
			m.channelBinding = nil
			m.channelError = "Immutable destination changed; repair required."
		} else {
			m.channelBinding = &fresh
			m.channelError = ""
		}
	} else {
		m.channelError = ""
	}
	m.channelBindings = bindings
	m.channelMessages = append([]channels.Message{}, msg.history.Messages...)
	m.channelIndex = min(m.channelIndex, max(0, len(bindings)-1))
	m.channelScroll = min(m.channelScroll, max(0, len(m.channelMessages)-1))
	return nil
}

func (m *model) chooseChannelBinding() {
	if len(m.channelBindings) == 0 || m.channelLoading || m.channelError != "" {
		m.status = "Refresh a valid destination snapshot first."
		return
	}
	b := m.channelBindings[m.channelIndex]
	if !b.Active || b.ThreadID != m.channelThread {
		m.status = "Disabled destinations cannot receive a new reply."
		return
	}
	m.channelBinding = &b
	d, ok := m.state.ChannelDraft(m.channelThread, b.ID)
	if !ok {
		d = clientstate.ChannelDraft{ThreadID: m.channelThread, BindingID: b.ID}
	}
	m.channelDraft = d
	m.screen = "channels"
	m.status = "Destination chosen explicitly. p composes a local channel reply."
}
func (m *model) channelKey(key tea.KeyMsg) tea.Cmd {
	if key.String() == "esc" {
		if m.screen == "channels" {
			m.screen = ""
			m.channelEpoch++
			m.status = "Returned to the same work; authored notes never become model input."
		} else {
			m.screen = "channels"
		}
		return nil
	}
	if m.screen == "channelhelp" {
		return nil
	}
	if m.screen == "channelreply" {
		if key.String() == "ctrl+s" {
			return m.sendChannelReply()
		}
		if p := m.state.PendingChannel; p != nil && p.ThreadID == m.channelDraft.ThreadID && p.BindingID == m.channelDraft.BindingID {
			m.status = "Saved reply is immutable; Ctrl-S checks its exact receipt; Esc then r refreshes."
			return nil
		}
		draft := m.channelDraft
		if key.String() == "enter" {
			draft.Text += "\n"
		} else {
			draft.Text = edit(draft.Text, key)
		}
		if err := m.state.SetChannelDraft(draft); err != nil {
			m.status = "Channel draft exceeds its safe text limit."
			return nil
		}
		m.channelDraft = draft
		m.save()
		return nil
	}
	if m.screen == "channelbinding" {
		switch key.String() {
		case "up", "k":
			m.channelIndex = max(0, m.channelIndex-1)
		case "down", "j":
			m.channelIndex = min(max(0, len(m.channelBindings)-1), m.channelIndex+1)
		case "enter":
			m.chooseChannelBinding()
		}
		return nil
	}
	switch key.String() {
	case "b":
		m.screen = "channelbinding"
		m.status = "Choose one active destination; Enter confirms."
	case "p":
		if m.channelBinding == nil {
			m.status = "b chooses one active destination before composing."
			return nil
		}
		m.screen = "channelreply"
		m.status = "Local reply · Enter adds a newline; Ctrl-S queues chat only."
	case "up", "k":
		m.channelScroll = max(0, m.channelScroll-1)
		m.channelNoteOffset = 0
	case "down", "j":
		m.channelScroll = min(max(0, len(m.channelMessages)-1), m.channelScroll+1)
		m.channelNoteOffset = 0
	case "pgup":
		m.channelNoteOffset = max(0, m.channelNoteOffset-max(1, m.height/2))
	case "pgdown":
		m.channelNoteOffset += max(1, m.height/2)
	case "?":
		m.screen = "channelhelp"
		m.status = ""
	case "r":
		cmd := m.fetchChannels()
		if m.state.PendingChannel != nil {
			return tea.Batch(cmd, m.channelReceiptCommand(*m.state.PendingChannel, false, false))
		}
		return cmd
	case "d", "u":
		if len(m.channelMessages) == 0 {
			return nil
		}
		note := m.channelMessages[m.channelScroll]
		if note.RemoteID != "" || note.Historical || note.AuthorID != "local-operator" {
			m.status = "Incoming authored notes are inert; no delivery action applies."
			return nil
		}
		p := clientstate.ChannelIntent{MessageID: note.ID, BindingID: note.BindingID, ThreadID: note.ThreadID, Text: note.Text}
		return m.channelReceiptCommand(p, false, key.String() == "u")
	}
	return nil
}

func (m *model) sendChannelReply() tea.Cmd {
	if m.channelSending {
		return nil
	}
	if old := m.state.PendingChannel; old != nil {
		if old.ThreadID != m.channelThread || old.BindingID != m.channelDraft.BindingID {
			m.status = "A saved reply to another destination needs its exact receipt; r checks it."
			return nil
		}
		if !m.save() {
			return nil
		}
		m.channelSending = true
		return m.channelReceiptCommand(*old, true, false)
	}
	if m.channelLoading || m.channelError != "" || m.channelBinding == nil || !m.channelBinding.Active || m.channelBinding.ThreadID != m.channelThread || m.channelDraft.ThreadID != m.channelThread || m.channelDraft.BindingID != m.channelBinding.ID || !channels.ValidText(m.channelDraft.Text) {
		m.status = "Choose an active destination and a nonempty reply from a valid snapshot."
		return nil
	}
	p := clientstate.ChannelIntent{MessageID: domain.NewID("message"), BindingID: m.channelBinding.ID, ThreadID: m.channelThread, Text: m.channelDraft.Text}
	m.state.PendingChannel = &p
	if !m.save() {
		return nil
	}
	m.channelSending = true
	m.status = "Saving authored reply to the local durable queue…"
	return m.channelSendCommand(p, *m.channelBinding)
}
func (m *model) channelSendCommand(p clientstate.ChannelIntent, b protocol.ChannelStatus) tea.Cmd {
	return func() tea.Msg {
		all, err := m.client.Channels(m.ctx)
		var fresh protocol.ChannelStatus
		if err == nil {
			fresh, err = exactBinding(all, p.ThreadID, p.BindingID)
		}
		if err == nil && (!fresh.Active || !sameBinding(b, fresh)) {
			err = errors.New("destination changed before queue request")
		}
		var response protocol.ChannelSendResponse
		if err == nil {
			response, err = m.client.SendChannelMessage(m.ctx, protocol.ChannelSendRequest{ID: p.MessageID, BindingID: p.BindingID, Text: p.Text})
		}
		return channelSendMsg{p, b, response, err}
	}
}
func (m *model) acceptChannelSend(msg channelSendMsg) tea.Cmd {
	if m.state.PendingChannel == nil || *m.state.PendingChannel != msg.intent {
		return nil
	}
	m.channelSending = false
	if msg.err != nil {
		m.status = "Channel reply acknowledgement unknown; r checks the saved receipt. No new ID is created."
		return nil
	}
	note := msg.response.Message
	p := msg.intent
	if note.ID != p.MessageID || note.ThreadID != p.ThreadID || note.BindingID != p.BindingID || note.Text != p.Text || note.AuthorID != "local-operator" || note.RemoteID != "" || note.Historical || note.Sequence <= 0 || !validReceipt(p, msg.binding, msg.response.Delivery) {
		m.status = "Channel queue reply rejected; immutable intent retained for reconciliation."
		return nil
	}
	m.acknowledgeChannelIntent(p, msg.response.Delivery)
	if m.channelThread == p.ThreadID && strings.HasPrefix(m.screen, "channel") {
		return m.fetchChannels()
	}
	return nil
}
func (m *model) channelReceiptCommand(p clientstate.ChannelIntent, retry, reconcile bool) tea.Cmd {
	resolve := m.state.PendingChannel != nil && *m.state.PendingChannel == p
	epoch := m.channelEpoch
	return func() tea.Msg {
		all, err := m.client.Channels(m.ctx)
		var b protocol.ChannelStatus
		if err == nil {
			b, err = exactBinding(all, p.ThreadID, p.BindingID)
		}
		var delivery channels.Delivery
		if err == nil {
			delivery, err = m.client.ChannelDelivery(m.ctx, p.MessageID)
		}
		if err == nil && reconcile && delivery.State == channels.Uncertain {
			if !validReceipt(p, b, delivery) {
				err = errors.New("receipt content mismatch")
			} else {
				delivery, err = m.client.ReconcileChannelDelivery(m.ctx, p.MessageID)
			}
		}
		return channelReceiptMsg{p, b, delivery, err, retry, resolve, epoch}
	}
}
func (m *model) acceptChannelReceipt(msg channelReceiptMsg) tea.Cmd {
	if !msg.resolvePending && (msg.epoch != m.channelEpoch || msg.intent.ThreadID != m.channelThread || !strings.HasPrefix(m.screen, "channel")) {
		return nil
	}
	if msg.resolvePending && (m.state.PendingChannel == nil || *m.state.PendingChannel != msg.intent) {
		return nil
	}
	if msg.retry {
		m.channelSending = false
	}
	if msg.err != nil {
		var httpErr *protocol.HTTPError
		if msg.retry && errors.As(msg.err, &httpErr) && httpErr.StatusCode == 404 && m.channelThread == msg.intent.ThreadID && m.channelBinding != nil && m.channelBinding.ID == msg.intent.BindingID && msg.binding.Active && sameBinding(*m.channelBinding, msg.binding) && !m.channelLoading && m.channelError == "" {
			if !m.save() {
				return nil
			}
			m.channelSending = true
			return m.channelSendCommand(msg.intent, msg.binding)
		}
		m.status = "Exact channel receipt unavailable; saved intent remains. No external post was retried."
		return nil
	}
	if !validReceipt(msg.intent, msg.binding, msg.delivery) {
		m.status = "Exact channel receipt content rejected; saved intent remains."
		return nil
	}
	if msg.resolvePending {
		m.acknowledgeChannelIntent(msg.intent, msg.delivery)
	} else {
		m.channelReceipts[msg.delivery.ID] = msg.delivery
		m.status = receiptStatus(msg.delivery)
	}
	return nil
}
func (m *model) acknowledgeChannelIntent(p clientstate.ChannelIntent, d channels.Delivery) {
	m.channelReceipts[d.ID] = d
	if m.state.PendingChannel == nil || *m.state.PendingChannel != p {
		return
	}
	before := m.state.ChannelDrafts
	m.state.ChannelDrafts = append([]clientstate.ChannelDraft{}, before...)
	m.state.PendingChannel = nil
	m.state.SetChannelDraft(clientstate.ChannelDraft{ThreadID: p.ThreadID, BindingID: p.BindingID})
	if !m.save() {
		m.state.PendingChannel = &p
		m.state.ChannelDrafts = before
		m.status = "Local queue receipt known; saving recovery state failed. r checks the same receipt."
		return
	}
	if m.channelDraft.ThreadID == p.ThreadID && m.channelDraft.BindingID == p.BindingID {
		m.channelDraft.Text = ""
	}
	m.status = receiptStatus(d)
}
func receiptStatus(d channels.Delivery) string {
	switch d.State {
	case channels.Queued:
		return "Queued locally · external delivery is not yet acknowledged."
	case channels.Attempted:
		return "External post attempted · no delivery acknowledgement yet."
	case channels.Uncertain:
		return "External post uncertain · never repeat it; u reconciles read-only."
	case channels.Delivered:
		return "Delivered · exact external post receipt; no model acceptance."
	case channels.Failed:
		return "External delivery failed · inspect its receipt; no automatic retry."
	}
	return fmt.Sprintf("Unsupported channel receipt %q", d.State)
}
