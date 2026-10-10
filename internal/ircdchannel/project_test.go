package ircdchannel

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/ircd"
)

type projectFixture struct {
	*fixtureRuntime
	projects       map[domain.ID]ircd.ProjectChannelOperation
	accounts       map[domain.ID]ircd.Credential
	accountCreates int
	projectProof   func(*ircd.ProjectChannelOperation)
}

func newProjectFixture(t *testing.T) (*projectFixture, *Service, Project) {
	t.Helper()
	legacy, _ := fixture(t)
	r := &projectFixture{fixtureRuntime: legacy, projects: map[domain.ID]ircd.ProjectChannelOperation{}, accounts: map[domain.ID]ircd.Credential{}}
	r.script = registration(legacy, true)
	s, e := New(r)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	p := Project{ID: domain.NewID("project"), HostID: domain.NewID("host"), Name: "Owned project", Workspace: filepath.Dir(r.cfg.CredentialRef)}
	return r, s, p
}
func (r *projectFixture) BeginProjectChannel(ctx context.Context, ch string, p, op domain.ID) (ircd.ProjectChannelOperation, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if saved, ok := r.projects[p]; ok {
		if saved.Channel != ch {
			return saved, false, ircd.ErrConflict
		}
		return saved, false, nil
	}
	r.registered = false
	o := ircd.ProjectChannelOperation{ID: op, ServerID: r.cfg.ID, ProjectID: p, Channel: ch, Owner: r.cfg.Account, State: "attempted", UpdatedAt: time.Now().UTC()}
	r.projects[p] = o
	r.op = ircd.ChannelOperation{Channel: ch}
	return o, true, nil
}
func (r *projectFixture) ConfirmProjectChannel(ctx context.Context, ch string, p domain.ID) (ircd.ProjectChannelOperation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.confirms++
	o, ok := r.projects[p]
	if !ok || o.Channel != ch {
		return o, ircd.ErrConflict
	}
	if r.confirmErr != nil {
		return o, r.confirmErr
	}
	if !r.registered {
		return o, ircd.ErrUncertain
	}
	o.State = "confirmed"
	o.RegisteredAt = "2026-10-10T20:00:00Z"
	o.UpdatedAt = time.Now().UTC()
	r.projects[p] = o
	if r.projectProof != nil {
		r.projectProof(&o)
	}
	return o, nil
}
func (r *projectFixture) EnsureThreadAccount(ctx context.Context, thread domain.ID) (ircd.Credential, error) {
	if c, ok := r.accounts[thread]; ok {
		return c, nil
	}
	account, e := ThreadAccount(thread)
	if e != nil {
		return ircd.Credential{}, e
	}
	ref := filepath.Join(filepath.Dir(r.cfg.CredentialRef), "accounts", account, "credential.json")
	if e = os.MkdirAll(filepath.Dir(ref), 0700); e != nil {
		return ircd.Credential{}, e
	}
	raw, _ := json.Marshal(map[string]string{"account": account, "password": strings.Repeat("a", 64), "nick": account})
	if e = os.WriteFile(ref, raw, 0600); e != nil {
		return ircd.Credential{}, e
	}
	c := ircd.Credential{Account: account, CredentialRef: ref}
	r.accounts[thread] = c
	r.accountCreates++
	return c, nil
}

func TestProjectChannelFullIdentityAndStableThreadAccounts(t *testing.T) {
	r, s, p := newProjectFixture(t)
	t1, t2 := domain.NewID("thread"), domain.NewID("thread")
	op := domain.NewID("operation")
	first, e := s.EnsureProject(context.Background(), p, t1, op)
	if e != nil {
		t.Fatal(e)
	}
	second, e := s.EnsureProject(context.Background(), p, t2, domain.NewID("operation"))
	if e != nil {
		t.Fatal(e)
	}
	if first.ProjectID != p.ID || first.Channel != second.Channel || first.Account == second.Account || first.Nick != first.Account || second.Nick != second.Account || first.OperationID != op || second.OperationID != op || r.accountCreates != 2 {
		t.Fatal("project/thread identity was not immutable")
	}
	dials, mutations := r.counts()
	if dials != 1 || mutations != 1 {
		t.Fatal("two threads re-registered their project")
	}
	again, e := s.EnsureProject(context.Background(), p, t1, domain.NewID("operation"))
	if e != nil || again.Account != first.Account || again.CredentialRef != first.CredentialRef || r.accountCreates != 2 {
		t.Fatal("thread identity changed on retry", e)
	}
	renamed := p
	renamed.Name = "Different name"
	if _, e = s.EnsureProject(context.Background(), renamed, t1, domain.NewID("operation")); !errors.Is(e, ircd.ErrConflict) {
		t.Fatal("project slug was rewritten", e)
	}
	other := p
	other.ID = domain.NewID("project")
	third, e := s.EnsureProject(context.Background(), other, domain.NewID("thread"), domain.NewID("operation"))
	if e != nil || third.Channel == first.Channel {
		t.Fatal("same display name merged explicit projects", e)
	}
}
func TestProjectUnknownRegistrationOnlyReadOnlyConfirmation(t *testing.T) {
	r, s, p := newProjectFixture(t)
	r.script = registration(r.fixtureRuntime, false)
	r.confirmErr = ircd.ErrUnavailable
	thread, op := domain.NewID("thread"), domain.NewID("operation")
	for _, id := range []domain.ID{op, op, domain.NewID("operation")} {
		if _, e := s.EnsureProject(context.Background(), p, thread, id); !errors.Is(e, ErrUncertain) {
			t.Fatal("unknown registration published route", e)
		}
	}
	if d, m := r.counts(); d != 1 || m != 1 || r.accountCreates != 0 {
		t.Fatal("unknown project registration replayed or granted thread account")
	}
	r.confirmErr = nil
	route, e := s.EnsureProject(context.Background(), p, thread, domain.NewID("operation"))
	if e != nil || route.OperationID != op || r.accountCreates != 1 {
		t.Fatal("read-only original confirmation did not recover", e)
	}
}
func TestProjectRegistrationProofRejectsScopeDrift(t *testing.T) {
	changes := []func(*ircd.ProjectChannelOperation){func(o *ircd.ProjectChannelOperation) { o.ProjectID = domain.NewID("project") }, func(o *ircd.ProjectChannelOperation) { o.Owner = "foreign" }, func(o *ircd.ProjectChannelOperation) { o.ID = domain.NewID("operation") }, func(o *ircd.ProjectChannelOperation) { o.Channel = "#foreign" }, func(o *ircd.ProjectChannelOperation) { o.RegisteredAt = "" }}
	for i, change := range changes {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			r, s, p := newProjectFixture(t)
			r.projectProof = change
			if _, e := s.EnsureProject(context.Background(), p, domain.NewID("thread"), domain.NewID("operation")); !errors.Is(e, ErrScope) || r.accountCreates != 0 {
				t.Fatal("unverified project ownership granted credentials", e)
			}
		})
	}
}
func TestCanonicalProjectChannelAndThreadNick(t *testing.T) {
	p := Project{ID: "project_0123456789abcdef0123456789abcdef", HostID: domain.NewID("host"), Name: strings.TrimSpace(strings.Repeat("Long name! ", 10)), Workspace: t.TempDir()}
	ch, e := ProjectChannelName(p)
	if e != nil || len(ch) > 61 || !strings.HasSuffix(ch, "-0123456789abcdef0123456789abcdef") {
		t.Fatal("project identity truncated", ch, e)
	}
	p.Name = "世界"
	ch, e = ProjectChannelName(p)
	if e != nil || !strings.HasPrefix(ch, "#km-project-") {
		t.Fatal("nonASCII display name leaked into address")
	}
	nick, e := ThreadAccount("thread_00000000000000000000000000000000")
	if e != nil || nick != "kt-aaaaaaaaaaaaaaaaaaaaaaaaaa" || len(nick) != 29 {
		t.Fatal("full128 thread encoding", nick, e)
	}
	p.HostID = "host_fake"
	if _, e = ProjectChannelName(p); e == nil {
		t.Fatal("inferred/invalid host accepted")
	}
}
