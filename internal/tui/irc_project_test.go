package tui

import (
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/channels"
	"github.com/seanheiney/keelmux/internal/clientstate"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
)

func TestAddressedIRCReceiptPreservesExactProjectBotDestination(t *testing.T) {
	thread := domain.NewID("thread")
	nick, _ := channels.IRCThreadNick(thread)
	b := protocol.ChannelStatus{ID: domain.NewID("binding"), ThreadID: thread, Provider: channels.IRC, Endpoint: "ircs://irc.example:6697", RealmID: "irc.example:6697", ChannelID: "#project", SelfID: nick, ProjectID: domain.NewID("project"), IRCAddressed: true, Active: true}
	p := clientstate.ChannelIntent{MessageID: domain.NewID("message"), BindingID: b.ID, ThreadID: thread, Text: "Exact authored reply"}
	d := channels.Delivery{ID: p.MessageID, MessageID: p.MessageID, BindingID: b.ID, State: channels.Delivered, RemoteID: "server-exact", UpdatedAt: time.Now().UTC(), PayloadHash: channels.PayloadHash(channels.Binding{ID: b.ID, ThreadID: thread, Provider: b.Provider, Endpoint: b.Endpoint, RealmID: b.RealmID, SelfID: nick, ChannelID: b.ChannelID, ProjectID: b.ProjectID, IRCAddressed: true}, intentMessage(p))}
	if !validReceipt(p, b, d) || rootLabel(b) != "project channel (IRC)" {
		t.Fatal("exact addressed receipt not verified or rendered")
	}
	for name, mutate := range map[string]func(*protocol.ChannelStatus){
		"project":     func(v *protocol.ChannelStatus) { v.ProjectID = domain.NewID("project") },
		"unaddressed": func(v *protocol.ChannelStatus) { v.ProjectID = ""; v.IRCAddressed = false },
		"shared-bot":  func(v *protocol.ChannelStatus) { v.SelfID = "keelmux-bot" },
	} {
		t.Run(name, func(t *testing.T) {
			other := b
			mutate(&other)
			if sameBinding(b, other) || validReceipt(p, other, d) {
				t.Fatal("recovered reply adopted another project or recipient")
			}
		})
	}
}
