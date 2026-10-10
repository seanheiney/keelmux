package ircd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seanheiney/keelmux/internal/domain"
)

func TestThreadNamespaceCannotBeCreatedByGenericAccounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent-manager")
	m, e := New(path)
	if e != nil {
		t.Fatal(e)
	}
	for _, account := range []string{"kt-aaaaaaaaaaaaaaaaaaaaaaaaaa", "kt-human", "kt-"} {
		if _, e = m.EnsureAccount(context.Background(), account); e != ErrConflict {
			t.Fatal("reserved thread account reached generic admission", e)
		}
	}
	if _, e = os.Lstat(path); !os.IsNotExist(e) {
		t.Fatal("invalid account created manager state")
	}
	for _, id := range []domain.ID{"thread_bad", "thread_0000000000000000000000000000000A", "session_00000000000000000000000000000000"} {
		if _, e = ThreadAccount(id); e != ErrConflict {
			t.Fatal("invalid immutable thread identity accepted")
		}
	}
}

func TestProjectChannelGrammarAndHistoricalRecordSeparation(t *testing.T) {
	project := domain.ID("project_0123456789abcdef0123456789abcdef")
	good := "#km-readable-project-0123456789abcdef0123456789abcdef"
	if !validProjectChannel(good, project) {
		t.Fatal("full project identity denied")
	}
	for _, channel := range []string{"#km-0123456789abcdef0123456789abcdef", "#km--0123456789abcdef0123456789abcdef", "#km-UPPER-0123456789abcdef0123456789abcdef", "#km-" + strings.Repeat("a", 25) + "-0123456789abcdef0123456789abcdef", good + "extra"} {
		if validProjectChannel(channel, project) {
			t.Fatal("noncanonical or lossy project channel accepted", channel)
		}
	}
	m, e := New(filepath.Join(t.TempDir(), "manager"))
	if e != nil {
		t.Fatal(e)
	}
	if m.projectChannelPath(project) == m.channelPath(good) || !strings.HasSuffix(m.projectChannelPath(project), string(project)+".json") {
		t.Fatal("project journal adopted historical channel digest")
	}
}
