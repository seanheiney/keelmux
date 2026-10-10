package peers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/federation"
)

type transportFunc func(context.Context, Config) (Snapshot, error)

func (f transportFunc) Fetch(ctx context.Context, c Config) (Snapshot, error) { return f(ctx, c) }

func openFixture(t *testing.T, tr Transport) *Registry {
	t.Helper()
	r, err := Open(filepath.Join(t.TempDir(), "peers"), tr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func TestEnrollmentPreservesIdentitiesScopesAndPrivateRestart(t *testing.T) {
	c, s := fixture(t)
	dir := filepath.Join(t.TempDir(), "peers")
	r, err := Open(dir, transportFunc(func(context.Context, Config) (Snapshot, error) { return s, nil }))
	if err != nil {
		t.Fatal(err)
	}
	v, err := r.Enroll(context.Background(), c)
	if err != nil || v.State != "available" || len(v.Sessions) != 1 || v.Sessions[0].WorkID != s.Sessions[0].WorkID || v.Sessions[0].ThreadID != s.Sessions[0].ThreadID {
		t.Fatal(v, err)
	}
	if len(v.Workspaces) != 1 || v.Workspaces[0].State != "recorded" || v.Sessions[0].Execution.Runtime != (domain.RuntimeRef{}) || v.Sessions[0].Capabilities.NativeAttach {
		t.Fatal("observation acquired action authority", v)
	}
	if _, err := r.Enroll(context.Background(), c); err != nil {
		t.Fatal("idempotent enrollment failed", err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	r, err = Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	v, err = r.View(c.HostID, "local")
	if err != nil || len(v.Sessions) != 1 {
		t.Fatal("cache did not recover", v, err)
	}
	for _, name := range []string{"peers.json", "peers.lock"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("nonprivate peer state", name, err)
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, "peers.json"))
	if strings.Contains(string(b), "private-hidden") || strings.Contains(string(b), "native.sock") {
		t.Fatal("unpermitted metadata persisted")
	}
	if _, err := r.View(c.HostID, "other-human"); !errors.Is(err, ErrGrant) {
		t.Fatal("another principal read inventory", err)
	}
}

func TestStaleFailureExpiryAndRecoveryAreTruthful(t *testing.T) {
	c, s := fixture(t)
	now := s.CapturedAt
	var failed error
	r := openFixture(t, transportFunc(func(context.Context, Config) (Snapshot, error) {
		if failed != nil {
			return Snapshot{}, failed
		}
		s.CapturedAt = now
		return s, nil
	}))
	r.now = func() time.Time { return now }
	if _, err := r.Enroll(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		err    error
		reason string
	}{{ErrUnavailable, "transport_unavailable"}, {ErrHostKey, "host_key_rejected"}, {ErrMalformed, "malformed_inventory"}, {ErrIdentity, "host_identity_changed"}, {ErrTimeout, "timeout"}, {ErrAuthentication, "authentication_unavailable"}} {
		failed = test.err
		v, err := r.Refresh(context.Background(), c.HostID, "local")
		if err != nil || v.State != "stale" || v.Reason != test.reason || len(v.Sessions) != 1 {
			t.Fatal("failure discarded known state or claimed fresh", v, err)
		}
		if _, err := (Source{r, c.HostID, "local"}).List(context.Background()); !errors.Is(err, ErrStale) {
			t.Fatal("stale directory admitted as ready", err)
		}
	}
	now = now.Add(101 * time.Second)
	v, err := r.View(c.HostID, "local")
	if err != nil || v.State != "unavailable" || v.Reason != "cache_expired" || len(v.Sessions) != 0 || len(v.Workspaces) != 0 {
		t.Fatal("expired cache remained available", v, err)
	}
	failed = nil
	v, err = r.Refresh(context.Background(), c.HostID, "local")
	if err != nil || v.State != "available" || len(v.Sessions) != 1 {
		t.Fatal("valid pinned refresh did not recover", v, err)
	}
	now = now.Add(11 * time.Second)
	v, _ = r.View(c.HostID, "local")
	if v.State != "stale" || v.Reason != "freshness_expired" {
		t.Fatal("TTL not enforced", v)
	}
}

func TestRevocationWhileFetchingDropsLateMetadataAndSurvivesOtherProcess(t *testing.T) {
	c, s := fixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	block := false
	r := openFixture(t, transportFunc(func(ctx context.Context, c Config) (Snapshot, error) {
		mu.Lock()
		wait := block
		mu.Unlock()
		if wait {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return Snapshot{}, ctx.Err()
			}
		}
		return s, nil
	}))
	if _, err := r.Enroll(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	other, err := Open(r.directory.Name(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	mu.Lock()
	block = true
	mu.Unlock()
	finished := make(chan View, 1)
	go func() {
		v, err := r.Refresh(context.Background(), c.HostID, "local")
		if err != nil {
			t.Error(err)
		}
		finished <- v
	}()
	<-entered
	if err := other.Revoke(c.HostID, "local"); err != nil {
		t.Fatal(err)
	}
	close(release)
	v := <-finished
	if v.State != "revoked" || len(v.Sessions) != 0 || len(v.Workspaces) != 0 {
		t.Fatal("late revoked metadata leaked", v)
	}
	v, err = r.Refresh(context.Background(), c.HostID, "local")
	if err != nil || v.State != "revoked" {
		t.Fatal("revoked peer contacted", v, err)
	}
	b, _ := os.ReadFile(filepath.Join(r.directory.Name(), "peers.json"))
	if strings.Contains(string(b), "permitted") || strings.Contains(string(b), string(s.Sessions[0].ID)) {
		t.Fatal("revoked cache kept private metadata")
	}
}

func TestGrantReductionAndExpansionNeverRestoreUnseenMetadata(t *testing.T) {
	c, s := fixture(t)
	r := openFixture(t, transportFunc(func(context.Context, Config) (Snapshot, error) { return s, nil }))
	if _, err := r.Enroll(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	grant := c.Grant
	grant.Sessions = []domain.ID{s.Sessions[1].ID}
	if err := r.SetGrant(grant); err != nil {
		t.Fatal(err)
	}
	v, _ := r.View(c.HostID, "local")
	if v.State != "stale" || len(v.Sessions) != 0 {
		t.Fatal("broader/replacement grant resurrected hidden cache", v)
	}
	v, err := r.Refresh(context.Background(), c.HostID, "local")
	if err != nil || v.State != "available" || len(v.Sessions) != 1 || v.Sessions[0].ID != s.Sessions[1].ID {
		t.Fatal("new exact grant not honored", v, err)
	}
	grant.Sessions = nil
	if err := r.SetGrant(grant); err != nil {
		t.Fatal(err)
	}
	v, _ = r.View(c.HostID, "local")
	if v.State != "revoked" || len(v.Sessions) != 0 {
		t.Fatal("empty grant exposed cache", v)
	}
	grant.Principal = "other"
	if err := r.SetGrant(grant); !errors.Is(err, ErrGrant) {
		t.Fatal("principal moved authority", err)
	}
}

func TestWrongHostClockSkewReplayAndMalformedCannotReplaceKnownIdentity(t *testing.T) {
	c, s := fixture(t)
	now := s.CapturedAt
	next := s
	r := openFixture(t, transportFunc(func(context.Context, Config) (Snapshot, error) { return next, nil }))
	r.now = func() time.Time { return now }
	if _, err := r.Enroll(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"host", "future", "expired", "replay", "malformed", "duplicate", "transitive", "thread-reuse", "old-generation", "reused-generation"} {
		next = s
		switch mode {
		case "host":
			next.HostID = domain.NewID("host")
		case "future":
			next.CapturedAt = now.Add(31 * time.Second)
		case "expired":
			next.CapturedAt = now.Add(-101 * time.Second)
		case "replay":
			next.CapturedAt = now.Add(-time.Second)
		case "duplicate":
			next.Sessions = []domain.Session{s.Sessions[0], s.Sessions[0]}
		case "malformed":
			next.Sessions = append([]domain.Session{}, s.Sessions...)
			next.Sessions[0].ThreadID = "forged"
		case "transitive":
			next.Sessions = append([]domain.Session{}, s.Sessions...)
			next.Sessions[1].HostID = domain.NewID("host")
		case "thread-reuse":
			next.Sessions = append([]domain.Session{}, s.Sessions...)
			next.Sessions[0].ThreadID = domain.NewID("thread")
		case "old-generation":
			next.Sessions = append([]domain.Session{}, s.Sessions...)
			next.Sessions[0].Execution.Generation = 0
		case "reused-generation":
			next.Sessions = append([]domain.Session{}, s.Sessions...)
			next.Sessions[0].Execution.ID = domain.NewID("execution")
		}
		v, err := r.Refresh(context.Background(), c.HostID, "local")
		if err != nil || v.State != "stale" || len(v.Sessions) != 1 || v.Sessions[0].ThreadID != s.Sessions[0].ThreadID || v.LastSeenAt != now {
			t.Fatal("invalid identity/replay replaced known cache", mode, v, err)
		}
	}
	now = now.Add(-31 * time.Second)
	v, _ := r.View(c.HostID, "local")
	if v.State != "unavailable" || v.Reason != "local_clock_changed" {
		t.Fatal("clock rollback extended ready state", v)
	}
}

func TestEnrollmentConflictsAndFailedIdentityNeverSave(t *testing.T) {
	c, s := fixture(t)
	r := openFixture(t, transportFunc(func(context.Context, Config) (Snapshot, error) { return s, nil }))
	wrong := c
	wrong.HostID, wrong.Grant.HostID = domain.NewID("host"), domain.NewID("host")
	wrong.Grant.HostID = wrong.HostID
	if _, err := r.Enroll(context.Background(), wrong); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
	d, _ := r.Directory(context.Background(), "local", false)
	if len(d.Places) != 0 {
		t.Fatal("failed enrollment saved authority", d)
	}
	if _, err := r.Enroll(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	changed := c
	changed.RemoteSocket = "/different/home.sock"
	if _, err := r.Enroll(context.Background(), changed); !errors.Is(err, ErrConflict) {
		t.Fatal("route silently repinned", err)
	}
	changed = c
	changed.Grant.Sessions = nil
	if _, err := r.Enroll(context.Background(), changed); !errors.Is(err, ErrConflict) {
		t.Fatal("active grant changed through re-enroll", err)
	}
}

func TestDirectoryFinalGrantProjectionAndPartialHomeFailure(t *testing.T) {
	c1, s1 := fixture(t)
	c2, s2 := fixture(t)
	c2.Alias = "second-home"
	var r *Registry
	var enabled bool
	var once sync.Once
	r = openFixture(t, transportFunc(func(ctx context.Context, c Config) (Snapshot, error) {
		if c.HostID == c1.HostID {
			return s1, nil
		}
		if enabled {
			once.Do(func() {
				if err := r.Revoke(c1.HostID, "local"); err != nil {
					t.Error(err)
				}
			})
			return Snapshot{}, ErrUnavailable
		}
		return s2, nil
	}))
	if _, err := r.Enroll(context.Background(), c1); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Enroll(context.Background(), c2); err != nil {
		t.Fatal(err)
	}
	enabled = true
	d, err := r.Directory(context.Background(), "local", true)
	if err != nil || len(d.Places) != 2 || len(d.Sessions) != 1 || d.Sessions[0].HostID != c2.HostID {
		t.Fatal("final revoke or partial stale inventory lost", d, err)
	}
	for _, p := range d.Places {
		if p.HostID == c1.HostID && p.State != "revoked" || p.HostID == c2.HostID && p.State != "stale" {
			t.Fatal("incorrect availability", p)
		}
	}
}

func TestRegistryRejectsUnsafeFilesAndMalformedAuthority(t *testing.T) {
	c, s := fixture(t)
	for _, mode := range []string{"symlink", "fifo", "public", "malformed", "foreign-host", "action-capability"} {
		t.Run(mode, func(t *testing.T) {
			r := openFixture(t, transportFunc(func(context.Context, Config) (Snapshot, error) { return s, nil }))
			if _, err := r.Enroll(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(r.directory.Name(), "peers.json")
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "symlink":
				_ = os.Remove(path)
				_ = os.Symlink(filepath.Join(t.TempDir(), "target"), path)
			case "fifo":
				_ = os.Remove(path)
				makeFIFO(t, path)
			case "public":
				_ = os.Chmod(path, 0644)
			case "malformed":
				_ = os.WriteFile(path, []byte("{}{}"), 0600)
			default:
				var rs records
				_ = json.Unmarshal(b, &rs)
				if mode == "foreign-host" {
					rs.Peers[0].Snapshot.HostID = domain.NewID("host")
				} else {
					rs.Peers[0].Snapshot.Sessions[0].Capabilities.NativeAttach = true
				}
				b, _ = json.Marshal(rs)
				_ = os.WriteFile(path, b, 0600)
			}
			if _, err := r.View(c.HostID, "local"); err == nil {
				t.Fatal("unsafe registry accepted", mode)
			}
		})
	}
}

func TestSourceDoesNotImplementStreamAuthority(t *testing.T) {
	var source federation.InventorySource = Source{}
	if _, ok := source.(federation.StreamSource); ok {
		t.Fatal("inventory acquired terminal authority")
	}
}

func TestExecutionGenerationAdvancesButOlderAttemptCannotReturn(t *testing.T) {
	c, s := fixture(t)
	next := s
	r := openFixture(t, transportFunc(func(context.Context, Config) (Snapshot, error) { return next, nil }))
	if _, err := r.Enroll(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	next.Sessions = append([]domain.Session{}, s.Sessions...)
	next.Sessions[0].Execution.ID = domain.NewID("execution")
	next.Sessions[0].Execution.Generation = 2
	v, err := r.Refresh(context.Background(), c.HostID, "local")
	if err != nil || v.State != "available" || v.Sessions[0].Execution.Generation != 2 {
		t.Fatal("new home generation lost", v, err)
	}
	next = s
	next.CapturedAt = time.Now().UTC()
	v, err = r.Refresh(context.Background(), c.HostID, "local")
	if err != nil || v.State != "stale" || v.Reason != "stale_advertisement" || v.Sessions[0].Execution.Generation != 2 {
		t.Fatal("delayed older generation replaced current observation", v, err)
	}
}

func TestDirectoryCancellationMarksSilentAndUnattemptedHomesStale(t *testing.T) {
	var mu sync.Mutex
	active := false
	states := map[domain.ID]Snapshot{}
	r := openFixture(t, transportFunc(func(ctx context.Context, c Config) (Snapshot, error) {
		mu.Lock()
		block := active
		s := states[c.HostID]
		mu.Unlock()
		if block {
			<-ctx.Done()
			return Snapshot{}, ctx.Err()
		}
		return s, nil
	}))
	var hosts []domain.ID
	for i := 0; i < 6; i++ {
		c, s := fixture(t)
		c.Alias = fmt.Sprintf("home-%d", i)
		states[c.HostID] = s
		hosts = append(hosts, c.HostID)
		if _, err := r.Enroll(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	active = true
	mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := r.Directory(ctx, "local", true); !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatal("directory ignored cancellation", err)
	}
	for _, host := range hosts {
		v, err := r.View(host, "local")
		if err != nil || v.State != "stale" || v.Reason != "timeout" {
			t.Fatal("silent/unattempted home claimed fresh", v, err)
		}
	}
}
