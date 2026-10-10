package federation

import (
	"context"
	"errors"
	"github.com/seanheiney/keelmux/internal/domain"
	"testing"
)

type source struct {
	sessions []domain.Session
	err      error
	during   func()
}

func (s source) List(context.Context) ([]domain.Session, error) {
	if s.during != nil {
		s.during()
	}
	return s.sessions, s.err
}
func TestScopedOneEntryAndRevocation(t *testing.T) {
	a := domain.NewSession("host_a", "a", "/tmp", "codex", "unknown")
	hidden := domain.NewSession("host_a", "secret", "/secret", "claude", "unknown")
	b := domain.NewSession("host_b", "b", "/tmp", "claude", "unknown")
	g, err := New([]Peer{{"host_a", source{sessions: []domain.Session{a, hidden}}}, {"host_b", source{sessions: []domain.Session{b}}}}, []Grant{{"human", "host_a", []domain.ID{a.ID}}, {"human", "host_b", []domain.ID{b.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	d, err := g.Directory(context.Background(), "human")
	if err != nil || len(d.Sessions) != 2 {
		t.Fatal(d, err)
	}
	for _, s := range d.Sessions {
		if s.ID == hidden.ID {
			t.Fatal("private metadata leaked")
		}
		if s.Capabilities.Stop || s.Capabilities.SemanticPrompt || s.Capabilities.NativeResume {
			t.Fatal("entry advertised an unexposed home mutation")
		}
	}
	if !a.Capabilities.Stop {
		t.Fatal("directory projection changed the home capability")
	}
	_ = g.SetGrant(Grant{"human", "host_a", nil})
	d, err = g.Directory(context.Background(), "human")
	if err != nil || len(d.Sessions) != 1 || d.Sessions[0].ID != b.ID {
		t.Fatal("revocation not applied", d, err)
	}
	if _, err = g.Directory(context.Background(), "stranger"); err == nil {
		t.Fatal("unlinked identity gained access")
	}
}
func TestRevokeDuringFetchAndPartition(t *testing.T) {
	a := domain.NewSession("host_a", "a", "/tmp", "codex", "unknown")
	g, _ := New([]Peer{{"host_a", source{sessions: []domain.Session{a}}}, {"host_b", source{err: errors.New("partition")}}}, []Grant{{"human", "host_a", []domain.ID{a.ID}}, {"human", "host_b", []domain.ID{"session_b"}}})
	g.peers["host_a"] = source{sessions: []domain.Session{a}, during: func() { _ = g.SetGrant(Grant{"human", "host_a", nil}) }}
	d, err := g.Directory(context.Background(), "human")
	if err != nil || len(d.Sessions) != 0 || len(d.Places) != 1 || d.Places[0].State != "unreachable" {
		t.Fatal("revocation or partition misrepresented", d, err)
	}
}
