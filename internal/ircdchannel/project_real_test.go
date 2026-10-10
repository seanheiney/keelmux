package ircdchannel

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/channels"
	"github.com/seanheiney/keelmux/internal/chatadapter"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/ircd"
)

// Real pinned wire/registration proof using only an owned foreground supervisor
// fixture. The integrated CLI/native product gate remains separately recorded.
func TestRealPinnedProjectThreadRecipients(t *testing.T) {
	archive := os.Getenv("KEELMUX_ERGO_ARCHIVE")
	if archive == "" {
		t.Skip("explicit owned pinned Ergo archive required")
	}
	raw, e := os.ReadFile(archive)
	if e != nil {
		t.Fatal(e)
	}
	dir := filepath.Join(t.TempDir(), "server")
	supervisor := &ownedSupervisor{dir: dir, processes: map[domain.ID]*ownedProcess{}}
	t.Cleanup(supervisor.cleanup)
	m, e := ircd.NewWithSupervisor(dir, supervisor)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if _, e = m.InstallArchive(ctx, bytes.NewReader(raw)); e != nil {
		t.Fatal(e)
	}
	cfg, e := m.Init(ctx, ircd.InitOptions{})
	if e != nil {
		t.Fatal(e)
	}
	started, e := m.Start(ctx, domain.NewID("operation"))
	if e != nil {
		t.Fatal(e)
	}
	registrar, e := New(m)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { registrar.Close() })
	p := Project{ID: domain.NewID("project"), HostID: domain.NewID("host"), Name: "Shared owned project", Workspace: dir}
	other := p
	other.ID = domain.NewID("project")
	other.Name = "Other owned project"
	projects := []Project{p, p, other}
	threads := []domain.ID{domain.NewID("thread"), domain.NewID("thread"), domain.NewID("thread")}
	routes := make([]Route, 3)
	bindings := make([]channels.Binding, 3)
	bots := make([]*chatadapter.IRC, 3)
	for i := range routes {
		routes[i], e = registrar.EnsureProject(ctx, projects[i], threads[i], domain.NewID("operation"))
		if e != nil {
			t.Fatal("real project registration/thread account", e)
		}
		r := routes[i]
		bots[i] = chatadapter.NewIRCWithDialer(m.Dial)
		t.Cleanup(func() { bots[i].Close() })
		bindings[i] = channels.Binding{ID: domain.NewID("binding"), ProjectID: r.ProjectID, ThreadID: r.ThreadID, IRCAddressed: true, Provider: channels.IRC, Endpoint: r.Endpoint, RealmID: r.RealmID, SelfID: r.Account, ChannelID: r.Channel, CredentialRef: r.CredentialRef, Active: true, ActivationCutoff: time.Now().UTC()}
		if proof, e := bots[i].Probe(ctx, bindings[i]); e != nil || proof.SelfID != r.Account {
			t.Fatal("real stable thread SASL/nick", e)
		}
	}
	if routes[0].Channel != routes[1].Channel || routes[0].Account == routes[1].Account || routes[2].Channel == routes[0].Channel {
		t.Fatal("real project identities merged")
	}
	collision := chatadapter.NewIRCWithDialer(m.Dial)
	t.Cleanup(func() { collision.Close() })
	secondConnection := bindings[0]
	secondConnection.ID = domain.NewID("binding")
	if _, e = collision.Probe(ctx, secondConnection); e == nil {
		t.Fatal("occupied stable nick adopted or random fallback")
	}
	human, e := m.EnsureAccount(ctx, "alice")
	if e != nil {
		t.Fatal(e)
	}
	peer := chatadapter.NewIRCWithDialer(m.Dial)
	t.Cleanup(func() { peer.Close() })
	peerBinding := channels.Binding{ID: domain.NewID("binding"), Provider: channels.IRC, Endpoint: cfg.Endpoint, RealmID: cfg.RealmID, SelfID: human.Account, ChannelID: routes[0].Channel, CredentialRef: human.CredentialRef, Active: true, ActivationCutoff: time.Now().UTC()}
	if _, e = peer.Probe(ctx, peerBinding); e != nil {
		t.Fatal(e)
	}
	send := func(text string) {
		t.Helper()
		msg := channels.Message{ID: domain.NewID("message"), BindingID: peerBinding.ID, Text: text}
		delivery := channels.Delivery{ID: msg.ID, MessageID: msg.ID, BindingID: peerBinding.ID, State: channels.Attempted, PayloadHash: channels.PayloadHash(peerBinding, msg)}
		r, e := peer.Send(ctx, peerBinding, delivery, msg)
		if e != nil || r.State != channels.Delivered {
			t.Fatal("real human echo", e)
		}
	}
	one, two := routes[0].Nick+": only first", "@"+routes[1].Nick+" only second"
	send("unaddressed inert broadcast")
	send(one)
	send(two)
	send(routes[2].Nick + ": wrong project")
	// Wait for a bounded live receipt for each exact recipient; a quiet third
	// connection is subsequently inspected after all shared-channel frames.
	for i, want := range []string{one, two} {
		deadline := time.Now().Add(3 * time.Second)
		var fetched channels.FetchResult
		for time.Now().Before(deadline) {
			fetched, e = bots[i].Fetch(ctx, bindings[i], "", 100)
			if e == nil && len(fetched.Messages) > 0 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if e != nil || len(fetched.Messages) != 1 || fetched.Messages[0].Text != want || fetched.Messages[0].ActorID != "alice" {
			t.Fatal("real shared channel fanned out or lost exact originaltext", e)
		}
	}
	time.Sleep(100 * time.Millisecond)
	fetched, e := bots[2].Fetch(ctx, bindings[2], "", 100)
	if e != nil || len(fetched.Messages) != 0 {
		t.Fatal("real other project received shared-channel note", e)
	}
	for _, a := range bots {
		a.Close()
	}
	peer.Close()
	collision.Close()
	replacement, e := m.Restart(ctx, started.Instance, domain.NewID("operation"))
	if e != nil || !replacement.Alive {
		t.Fatal("owned Ergo replacement", e)
	}
	reopened, e := ircd.NewWithSupervisor(dir, supervisor)
	if e != nil {
		t.Fatal(e)
	}
	r2, e := New(reopened)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { r2.Close() })
	for i, old := range routes {
		current, e := r2.EnsureProject(ctx, projects[i], threads[i], domain.NewID("operation"))
		if e != nil || current.Account != old.Account || current.Nick != old.Nick || current.CredentialRef != old.CredentialRef || current.Channel != old.Channel || current.RegisteredAt != old.RegisteredAt || current.OperationID != old.OperationID {
			t.Fatal("real restart changed original route/account/nick", e)
		}
		a := chatadapter.NewIRCWithDialer(reopened.Dial)
		proof, e := a.Probe(ctx, bindings[i])
		a.Close()
		if e != nil || proof.SelfID != old.Account {
			t.Fatal("real restarted stable nick unavailable", e)
		}
	}
}
