// Package channels defines authored chat and external-post delivery. It grants
// no terminal, model-turn, approval or remote-principal authority.
package channels

import (
	"context"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
)

type Provider string

const (
	Mattermost Provider = "mattermost"
	Slack      Provider = "slack"
	IRC        Provider = "irc"
)

// Binding is private home configuration, not an inventory/wire DTO. Probe must
// prove its endpoint, realm, authenticated self and channel/root before Bind.
// All fields except Active are immutable; replacement requires a fresh ID.
type Binding struct {
	ID, SessionID, ThreadID, WorkID, HostID                     domain.ID
	Provider                                                    Provider
	Endpoint, RealmID, SelfID, ChannelID, RootID, CredentialRef string
	Allowlist                                                   []string
	ActivationCutoff                                            time.Time
	Active                                                      bool
}

// Sequence orders authored messages only, independently of terminal events.
// Imported text remains a note, including text resembling a command/approval.
type Message struct {
	ID, ThreadID, BindingID domain.ID
	Sequence                int64
	AuthorID, Text          string
	CreatedAt               time.Time
	RemoteID                string
	Historical              bool
}

type DeliveryState string

const (
	Queued    DeliveryState = "queued"
	Attempted DeliveryState = "attempted"
	Delivered DeliveryState = "delivered"
	Failed    DeliveryState = "failed"
	Uncertain DeliveryState = "uncertain"
)

// Delivered means an exact external post acknowledgement, not reader receipt,
// provider acceptance or model completion. ID equals its local MessageID.
type Delivery struct {
	ID, MessageID, BindingID domain.ID
	State                    DeliveryState
	PayloadHash              [32]byte
	RemoteID, Code           string
	UpdatedAt                time.Time
}

type RemoteMessage struct {
	ID, ActorID, RealmID, ChannelID, RootID, Text string
	CreatedAt                                     time.Time
	Historical                                    bool
}

// Evidence is produced by the trusted configured adapter, never inbound text.
// Matching these fields is necessary; it does not itself authenticate a server.
type Evidence struct {
	Provider                                             Provider
	Endpoint, RealmID, SelfID, ChannelID, RootID, Source string
	ObservedAt                                           time.Time
}

type FetchResult struct {
	Messages   []RemoteMessage
	NextCursor string
	HasMore    bool
	Evidence   Evidence
}

type SendResult struct {
	State          DeliveryState
	RemoteID, Code string
	Evidence       Evidence
}

// Adapter must bound IO and suppress raw server/credential diagnostics. Send
// is called only after the durable claim; invoked errors stay Uncertain.
// Reconcile is read-only: absence is not permission to repeat a post.
type Adapter interface {
	Probe(context.Context, Binding) (Evidence, error)
	Fetch(context.Context, Binding, string, int) (FetchResult, error)
	Send(context.Context, Binding, Delivery, Message) (SendResult, error)
	Reconcile(context.Context, Binding, Delivery, Message) (SendResult, error)
	Close() error
}
