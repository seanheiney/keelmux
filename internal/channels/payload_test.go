package channels

import "testing"

func TestPayloadHashFencesFrozenIntent(t *testing.T) {
	b := Binding{ID: "binding_a", Provider: Mattermost, Endpoint: "https://chat.example", RealmID: "team_a", SelfID: "bot_a", ChannelID: "channel_a", RootID: "post_a", CredentialRef: "/private/token", Active: true}
	m := Message{ID: "message_a", ThreadID: "thread_a", BindingID: b.ID, AuthorID: "local-operator", Text: "An explicit reply"}
	want := PayloadHash(b, m)
	for name, change := range map[string]func(*Binding, *Message){
		"binding":   func(b *Binding, m *Message) { b.ID = "binding_b" },
		"operation": func(b *Binding, m *Message) { m.ID = "message_b" },
		"thread":    func(b *Binding, m *Message) { m.ThreadID = "thread_b" },
		"endpoint":  func(b *Binding, m *Message) { b.Endpoint = "https://other.example" },
		"realm":     func(b *Binding, m *Message) { b.RealmID = "team_b" },
		"self":      func(b *Binding, m *Message) { b.SelfID = "bot_b" },
		"channel":   func(b *Binding, m *Message) { b.ChannelID = "channel_b" },
		"root":      func(b *Binding, m *Message) { b.RootID = "post_b" },
		"provider":  func(b *Binding, m *Message) { b.Provider = Slack },
		"author":    func(b *Binding, m *Message) { m.AuthorID = "someone-else" },
		"content":   func(b *Binding, m *Message) { m.Text += " changed" },
	} {
		t.Run(name, func(t *testing.T) {
			bb, mm := b, m
			change(&bb, &mm)
			if PayloadHash(bb, mm) == want {
				t.Fatal("changed immutable intent retained its hash")
			}
		})
	}
	b.CredentialRef = "/another/private/token"
	b.Active = false
	m.Sequence = 42
	if PayloadHash(b, m) != want {
		t.Fatal("receipt projection depends on private credentials or mutable status")
	}
}
