package channelservice

import (
	"context"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/channels"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
)

type gapAdapter struct {
	fixtureAdapter
	result channels.FetchResult
	err    error
}

func (a *gapAdapter) Fetch(_ context.Context, b channels.Binding, _ string, _ int) (channels.FetchResult, error) {
	r := a.result
	if r.Evidence == (channels.Evidence{}) {
		r.Evidence = proof(b)
	}
	return r, a.err
}

func TestIRCVerifiedGapCheckpointResumesLiveNotes(t *testing.T) {
	_, db, old, _ := serviceFixture(t)
	ctx := context.Background()
	b, _, err := db.ChannelBinding(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	a := &gapAdapter{}
	s, err := New(ctx, db, func(channels.Provider) (channels.Adapter, error) { return a, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	status, err := s.Bind(ctx, protocol.ChannelBindRequest{ID: domain.NewID("binding"), SessionID: b.SessionID, Provider: channels.IRC, Endpoint: "ircs://irc.invalid:6697", ChannelID: "#agents", CredentialRef: b.CredentialRef, Allowlist: b.Allowlist})
	if err != nil {
		t.Fatal(err)
	}
	resetPoll := func() { s.mu.Lock(); delete(s.lastFetch, status.ID); s.mu.Unlock() }
	a.result = channels.FetchResult{NextCursor: "old-generation"}
	if _, err = s.Sync(ctx, status.ID); err != nil {
		t.Fatal(err)
	}
	a.result = channels.FetchResult{NextCursor: "current-generation-zero", HasMore: true}
	a.err = channels.ErrHistoryGap
	resetPoll()
	if out, err := s.Sync(ctx, status.ID); err == nil || !out.HasMore {
		t.Fatalf("gap checkpoint: %+v %v", out, err)
	}
	b, cursor, err := db.ChannelBinding(ctx, status.ID)
	if err != nil || cursor != "current-generation-zero" || !s.status(b).HistoryGap {
		t.Fatalf("checkpoint not committed: %q %v", cursor, err)
	}
	a.err = nil
	a.result = channels.FetchResult{NextCursor: "current-generation-one", Messages: []channels.RemoteMessage{{ID: "fresh-live-id", ActorID: "allowed-human", RealmID: b.RealmID, ChannelID: b.ChannelID, Text: "Fresh note after reconnect", CreatedAt: time.Now().UTC()}}}
	resetPoll()
	if out, err := s.Sync(ctx, status.ID); err != nil || out.Imported != 1 {
		t.Fatalf("live intake: %+v %v", out, err)
	}
	notes, err := db.ChannelMessages(ctx, b.ThreadID, 0, 100)
	if err != nil || len(notes) != 1 || notes[0].Text != "Fresh note after reconnect" {
		t.Fatalf("fresh note unavailable: %+v %v", notes, err)
	}
	observed := s.status(b)
	if observed.Intake != "healthy" || !observed.HistoryGap || observed.Coverage != string(channels.CoverageHistoryGap) {
		t.Fatalf("lost history erased: %+v", observed)
	}
	if a.sends.Load() != 0 {
		t.Fatal("history recovery sent a post")
	}
	for _, bad := range []string{"empty", "same", "scope", "messages"} {
		t.Run(bad, func(t *testing.T) {
			a.result = channels.FetchResult{NextCursor: "untrusted-next"}
			switch bad {
			case "empty":
				a.result.NextCursor = ""
			case "same":
				a.result.NextCursor = "current-generation-one"
			case "scope":
				a.result.Evidence = proof(b)
				a.result.Evidence.SelfID = "different-self"
			case "messages":
				a.result.Messages = []channels.RemoteMessage{{ID: "should-not-import"}}
			}
			a.err = channels.ErrHistoryGap
			resetPoll()
			if _, err := s.Sync(ctx, b.ID); err == nil {
				t.Fatal("gap reported complete")
			}
			_, cursor, err := db.ChannelBinding(ctx, b.ID)
			if err != nil || cursor != "current-generation-one" {
				t.Fatalf("unverified gap advanced cursor: %q %v", cursor, err)
			}
		})
	}
}
