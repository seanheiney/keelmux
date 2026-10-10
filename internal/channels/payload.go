package channels

import (
	"crypto/sha256"
	"encoding/json"

	"github.com/seanheiney/keelmux/internal/domain"
)

// PayloadHash binds an authored operation to its exact immutable destination,
// thread, author and content. A private client may compare a recovered receipt
// with its frozen intent without possessing credential bytes or file paths.
// It is a local integrity check, never authentication of an external server.
func PayloadHash(b Binding, m Message) [32]byte {
	data, _ := json.Marshal(struct {
		BindingID, MessageID, ThreadID                     domain.ID
		Provider                                           Provider
		Endpoint, Realm, Self, Channel, Root, Author, Text string
		ProjectID                                          domain.ID `json:",omitempty"`
		IRCAddressed                                       bool      `json:",omitempty"`
	}{b.ID, m.ID, m.ThreadID, b.Provider, b.Endpoint, b.RealmID, b.SelfID, b.ChannelID, b.RootID, m.AuthorID, m.Text, b.ProjectID, b.IRCAddressed})
	return sha256.Sum256(data)
}
