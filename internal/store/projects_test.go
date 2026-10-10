package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/channels"
	"github.com/seanheiney/keelmux/internal/domain"
)

func projectSession(t *testing.T, s *Store, host domain.ID, workspace string) domain.Session {
	t.Helper()
	v := domain.NewSession(host, "Project thread", workspace, "fixture", "v1")
	op := domain.Operation{ID: domain.NewID("op"), Kind: "launch", SessionID: v.ID, ExecutionID: v.Execution.ID}
	if _, _, err := s.Record(context.Background(), op, []byte(`{"argv":["fixture"]}`), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func projectFixture(t *testing.T) (*Store, string, domain.Project, domain.Session) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state", "registry.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	host, workspace := domain.NewID("host"), t.TempDir()
	v := projectSession(t, s, host, workspace)
	p, err := s.CreateProject(context.Background(), NewProject(host, domain.NewID("project"), "Launch Café", workspace))
	if err != nil {
		t.Fatal(err)
	}
	return s, path, p, v
}

func projectBinding(t *testing.T, s *Store, p domain.Project, v domain.Session) channels.Binding {
	t.Helper()
	if _, err := s.BindProjectThread(context.Background(), p.ID, v.ID); err != nil {
		t.Fatal(err)
	}
	nick, err := channels.IRCThreadNick(v.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	b := channels.Binding{ID: domain.NewID("binding"), SessionID: v.ID, ThreadID: v.ThreadID, WorkID: v.WorkID, HostID: v.HostID,
		Provider: channels.IRC, Endpoint: "ircs://irc.example:6697", RealmID: "irc.example:6697", SelfID: nick,
		ChannelID: "#launch-cafe", CredentialRef: "local_thread_secret", Allowlist: []string{"alice"}, ActivationCutoff: time.Unix(100, 0).UTC(), ProjectID: p.ID, IRCAddressed: true}
	b, err = s.BindChannel(context.Background(), b, channelProof(b))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestProjectMembershipIsExplicitImmutableAndDurable(t *testing.T) {
	s, path, p, v := projectFixture(t)
	ctx := context.Background()
	var before int
	if err := s.db.QueryRow("SELECT count(*) FROM project_threads").Scan(&before); err != nil || before != 0 {
		t.Fatal("creation inferred membership", before, err)
	}
	retry := p
	retry.CreatedAt = p.CreatedAt.Add(time.Hour)
	if got, err := s.CreateProject(ctx, retry); err != nil || got != p {
		t.Fatal("retry changed immutable project", got, err)
	}
	changed := p
	changed.Name = "Replacement"
	if _, err := s.CreateProject(ctx, changed); err == nil {
		t.Fatal("project name/config replaced")
	}
	if got, err := s.BindProjectThread(ctx, p.ID, v.ID); err != nil || got != p {
		t.Fatal(got, err)
	}
	if _, err := s.BindProjectThread(ctx, p.ID, v.ID); err != nil {
		t.Fatal("identical membership retry", err)
	}
	p2, err := s.CreateProject(ctx, NewProject(p.HostID, domain.NewID("project"), "Another", p.Workspace))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.BindProjectThread(ctx, p2.ID, v.ID); err == nil {
		t.Fatal("thread moved to another project")
	}
	for name, other := range map[string]domain.Session{
		"foreign-home":      projectSession(t, s, domain.NewID("host"), p.Workspace),
		"foreign-workspace": projectSession(t, s, p.HostID, t.TempDir()),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.BindProjectThread(ctx, p.ID, other.ID); err == nil {
				t.Fatal("foreign thread adopted")
			}
		})
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got, err := reopened.BindProjectThread(ctx, p.ID, v.ID); err != nil || got != p {
		t.Fatal("membership lost after reopen", got, err)
	}
	if all, err := reopened.Projects(ctx); err != nil || len(all) != 2 {
		t.Fatal("project directory lost", all, err)
	}
}

func TestProjectChannelAllowsDistinctAddressedThreadBots(t *testing.T) {
	s, path, p, v := projectFixture(t)
	b1 := projectBinding(t, s, p, v)
	v2 := projectSession(t, s, p.HostID, p.Workspace)
	b2 := projectBinding(t, s, p, v2)
	if b1.ChannelID != b2.ChannelID || b1.SelfID == b2.SelfID {
		t.Fatal("threads did not get distinct bots in one project channel")
	}
	duplicate := b1
	duplicate.ID = domain.NewID("binding")
	if _, err := s.BindChannel(context.Background(), duplicate, channelProof(duplicate)); err == nil {
		t.Fatal("duplicate active thread bot adopted")
	}
	unaddressed := b1
	unaddressed.ID, unaddressed.ProjectID, unaddressed.IRCAddressed, unaddressed.SelfID = domain.NewID("binding"), "", false, "shared-bot"
	if _, err := s.BindChannel(context.Background(), unaddressed, channelProof(unaddressed)); err == nil {
		t.Fatal("unaddressed consumer can steal project channel sources")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for _, b := range []channels.Binding{b1, b2} {
		got, _, err := reopened.ChannelBinding(context.Background(), b.ID)
		if err != nil || got.SelfID != b.SelfID || got.ProjectID != p.ID || !got.IRCAddressed {
			t.Fatal("addressed identity lost", got, err)
		}
	}
}

func TestAddressedBindingRequiresOriginalProjectMembership(t *testing.T) {
	s, _, p, v := projectFixture(t)
	nick, _ := channels.IRCThreadNick(v.ThreadID)
	b := channels.Binding{ID: domain.NewID("binding"), SessionID: v.ID, ThreadID: v.ThreadID, WorkID: v.WorkID, HostID: v.HostID, Provider: channels.IRC, Endpoint: "ircs://irc.example:6697", RealmID: "irc.example:6697", SelfID: nick, ChannelID: "#project", CredentialRef: "local", Allowlist: []string{"alice"}, ActivationCutoff: time.Now().UTC(), ProjectID: p.ID, IRCAddressed: true}
	if _, err := s.BindChannel(context.Background(), b, channelProof(b)); err == nil {
		t.Fatal("binding invented membership")
	}
	if _, err := s.BindProjectThread(context.Background(), p.ID, v.ID); err != nil {
		t.Fatal(err)
	}
	wrong := b
	wrong.SelfID = "shared-bot"
	if _, err := s.BindChannel(context.Background(), wrong, channelProof(wrong)); err == nil {
		t.Fatal("binding replaced derived thread identity")
	}
	if _, err := s.BindChannel(context.Background(), b, channelProof(b)); err != nil {
		t.Fatal(err)
	}
}

// Make a genuine v3 layout without touching historical authored JSON, payload
// hashes, receipts or seed provenance. New authorities migrate it additively.
func downgradeProjectFixture(t *testing.T, s *Store) {
	t.Helper()
	_, err := s.db.Exec(`DROP TABLE project_threads; DROP TABLE projects; DROP INDEX channel_active_endpoint;
ALTER TABLE channel_bindings DROP COLUMN recipient_id;
CREATE UNIQUE INDEX channel_active_endpoint ON channel_bindings(provider,endpoint,realm_id,channel_id,root_id) WHERE active=1;
PRAGMA user_version=3;`)
	if err != nil {
		t.Fatal(err)
	}
}

func TestProjectMigrationPreservesHistoricalCanonicalBytes(t *testing.T) {
	x := newChannelFixture(t)
	m, d := x.queue(t, "historical_payload")
	var binding, payload, receipt []byte
	if err := x.store.db.QueryRow("SELECT data FROM channel_bindings WHERE id=?", x.binding.ID).Scan(&binding); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(binding, []byte("ProjectID")) || bytes.Contains(binding, []byte("IRCAddressed")) {
		t.Fatal("zero extension changed historical JSON")
	}
	if err := x.store.db.QueryRow("SELECT payload_hash,data FROM channel_deliveries WHERE id=?", d.ID).Scan(&payload, &receipt); err != nil {
		t.Fatal(err)
	}
	downgradeProjectFixture(t, x.store)
	x.store.Close()
	s, err := Open(x.path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var b2, p2, d2 []byte
	if err = s.db.QueryRow("SELECT data FROM channel_bindings WHERE id=?", x.binding.ID).Scan(&b2); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow("SELECT payload_hash,data FROM channel_deliveries WHERE id=?", d.ID).Scan(&p2, &d2); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(binding, b2) || !bytes.Equal(payload, p2) || !bytes.Equal(receipt, d2) {
		t.Fatal("migration rewrote historical authority bytes")
	}
	if got, err := s.ChannelDelivery(x.ctx, d.ID); err != nil || got != d {
		t.Fatal("historical claim lost", got, err)
	}
	if _, _, _, claimed, err := s.ClaimChannelDelivery(x.ctx, m.ID); err != nil || !claimed {
		t.Fatal("migrated original queued operation", claimed, err)
	}
}

func TestProjectMigrationCollisionRollsBackWithoutAdoption(t *testing.T) {
	for _, name := range []string{"projects", "project_threads", "project_thread_directory"} {
		t.Run(name, func(t *testing.T) {
			x := newChannelFixture(t)
			downgradeProjectFixture(t, x.store)
			if _, err := x.store.db.Exec("CREATE TABLE " + name + " (foreign_owned TEXT)"); err != nil {
				t.Fatal(err)
			}
			x.store.Close()
			if s, err := Open(x.path); err == nil {
				s.Close()
				t.Fatal("adopted colliding namespace")
			}
			db, err := sql.Open("sqlite", x.path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var version int
			if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 3 {
				t.Fatal("partial migration advanced version", version, err)
			}
			var definition string
			if err = db.QueryRow("SELECT sql FROM sqlite_master WHERE name=?", name).Scan(&definition); err != nil || !strings.Contains(definition, "foreign_owned") {
				t.Fatal("collision changed", definition, err)
			}
			var recipient int
			if err = db.QueryRow("SELECT count(*) FROM pragma_table_info('channel_bindings') WHERE name='recipient_id'").Scan(&recipient); err != nil || recipient != 0 {
				t.Fatal("partial recipient migration", recipient, err)
			}
		})
	}
}

func TestProjectEstablishedSchemaAndMembershipFailClosed(t *testing.T) {
	for name, corrupt := range map[string]func(*Store, domain.Project, domain.Session, channels.Binding) error{
		"missing-table": func(s *Store, p domain.Project, v domain.Session, b channels.Binding) error {
			_, e := s.db.Exec("DROP TABLE project_threads")
			return e
		},
		"missing-index": func(s *Store, p domain.Project, v domain.Session, b channels.Binding) error {
			_, e := s.db.Exec("DROP INDEX project_thread_directory")
			return e
		},
		"lost-member": func(s *Store, p domain.Project, v domain.Session, b channels.Binding) error {
			_, e := s.db.Exec("DELETE FROM project_threads")
			return e
		},
		"recipient-mirror": func(s *Store, p domain.Project, v domain.Session, b channels.Binding) error {
			_, e := s.db.Exec("UPDATE channel_bindings SET recipient_id='other'")
			return e
		},
		"project-host-mirror": func(s *Store, p domain.Project, v domain.Session, b channels.Binding) error {
			_, e := s.db.Exec("UPDATE projects SET host_id=?", domain.NewID("host"))
			return e
		},
		"duplicate-json": func(s *Store, p domain.Project, v domain.Session, b channels.Binding) error {
			raw := encoded(p)
			raw = append(raw[:len(raw)-1], []byte(`,"name":"Replacement"}`)...)
			_, e := s.db.Exec("UPDATE projects SET data=?", raw)
			return e
		},
		"unknown-json": func(s *Store, p domain.Project, v domain.Session, b channels.Binding) error {
			raw := encoded(p)
			raw = append(raw[:len(raw)-1], []byte(`,"authority":true}`)...)
			_, e := s.db.Exec("UPDATE projects SET data=?", raw)
			return e
		},
		"seed-workspace": func(s *Store, p domain.Project, v domain.Session, b channels.Binding) error {
			v.Workspace = filepath.Join(p.Workspace, "other")
			raw, _ := json.Marshal(v)
			_, e := s.db.Exec("UPDATE sessions SET data=? WHERE id=?", raw, v.ID)
			return e
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, path, p, v := projectFixture(t)
			b := projectBinding(t, s, p, v)
			if err := corrupt(s, p, v, b); err != nil {
				t.Fatal(err)
			}
			s.Close()
			if reopened, err := Open(path); err == nil {
				reopened.Close()
				t.Fatal("corrupt project authority reconstructed")
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var version int
			if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 4 {
				t.Fatal("refusal rewrote version", version, err)
			}
		})
	}
}
