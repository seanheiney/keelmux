package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	cb "github.com/seanheiney/keelmux/internal/codexbroker"
	"github.com/seanheiney/keelmux/internal/domain"
)

func legacyFixture(t *testing.T, failMigration bool) (string, domain.Lease, map[string][]byte) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "registry.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`
CREATE TABLE sessions(id TEXT PRIMARY KEY,thread_id TEXT NOT NULL,data BLOB NOT NULL);
CREATE TABLE operations(id TEXT PRIMARY KEY,session_id TEXT NOT NULL REFERENCES sessions(id),generation INTEGER NOT NULL,data BLOB NOT NULL,payload BLOB NOT NULL);
CREATE TABLE events(thread_id TEXT NOT NULL,sequence INTEGER NOT NULL,data BLOB NOT NULL,PRIMARY KEY(thread_id,sequence));
CREATE TABLE grants(operation_id TEXT PRIMARY KEY REFERENCES operations(id),session_id TEXT NOT NULL REFERENCES sessions(id),execution_id TEXT NOT NULL,read_only INTEGER NOT NULL,active INTEGER NOT NULL,data BLOB NOT NULL);
CREATE UNIQUE INDEX one_writer ON grants(session_id,execution_id) WHERE active=1 AND read_only=0;
PRAGMA user_version=1;`)
	if err != nil {
		t.Fatal(err)
	}
	v := domain.NewSession("host", "Legacy", dir, "fixture", "v1")
	l := domain.Lease{SessionID: v.ID, ExecutionID: v.Execution.ID, Generation: v.Execution.Generation, OperationID: "legacy-acquisition", ClientID: "legacy-client", Token: "exact-private-legacy-token"}
	op := domain.Operation{ID: l.OperationID, Kind: "attach", SessionID: v.ID, ExecutionID: v.Execution.ID, Outcome: domain.Completed, CreatedAt: time.Unix(7, 0).UTC(), UpdatedAt: time.Unix(8, 0).UTC(), Detail: "legacy opaque provenance"}
	// Whitespace and unknown fields prove byte preservation, beyond decoding.
	state := append(encoded(v), byte(' '))
	operationBytes := append(encoded(op), []byte("\n  ")...)
	leaseBytes := append(encoded(Grant{Lease: l, Active: true}), []byte(" \n")...)
	request := []byte(" { \"legacy_payload\" : [\"private\", 17], \"future\" : true }\n")
	eventBytes := encoded(domain.Event{ThreadID: v.ThreadID, SessionID: v.ID, OperationID: l.OperationID, Sequence: 1, Kind: "attach.granted", At: time.Unix(8, 0).UTC()})
	if _, err = db.Exec("INSERT INTO sessions VALUES(?,?,?)", v.ID, v.ThreadID, state); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO operations VALUES(?,?,?,?,?)", op.ID, v.ID, v.Execution.Generation, operationBytes, request); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO grants VALUES(?,?,?,?,?,?)", op.ID, v.ID, v.Execution.ID, 0, 1, leaseBytes); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO events VALUES(?,?,?)", v.ThreadID, 1, eventBytes); err != nil {
		t.Fatal(err)
	}
	if failMigration {
		if _, err = db.Exec("CREATE INDEX codex_epochs ON sessions(id)"); err != nil {
			t.Fatal(err)
		}
	}
	return path, l, map[string][]byte{"sessions": state, "operations": operationBytes, "grants": leaseBytes, "events": eventBytes, "payload": request}
}
func assertLegacyBytes(t *testing.T, db *sql.DB, original map[string][]byte) {
	t.Helper()
	for table, want := range original {
		query := "SELECT data FROM " + table
		if table == "payload" {
			query = "SELECT payload FROM operations"
		}
		var got []byte
		if err := db.QueryRow(query).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("migration rewrote %s bytes", table)
		}
	}
}
func TestCodexV1MigrationPreservesLegacyBytesWithoutPromotion(t *testing.T) {
	path, l, original := legacyFixture(t, false)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	assertLegacyBytes(t, s.db, original)
	var version, n int
	if err = s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 4 {
		t.Fatal("wrong schema", version, err)
	}
	if err = s.db.QueryRow("SELECT COUNT(*) FROM codex_grants").Scan(&n); err != nil || n != 0 {
		t.Fatal("legacy grant was promoted", err)
	}
	if err = s.db.QueryRow("SELECT COUNT(*) FROM codex_agents").Scan(&n); err != nil || n != 0 {
		t.Fatal("legacy primary invented", err)
	}
	g, err := s.Grant(context.Background(), l.OperationID)
	if err != nil || g.Lease != l || !g.Active {
		t.Fatal("legacy lease changed", err)
	}
	if err = s.Release(context.Background(), l); err != nil {
		t.Fatal("legacy exact-token release broke", err)
	}
}
func TestCodexMigrationFailureRollsBackAllMetadata(t *testing.T) {
	path, _, original := legacyFixture(t, true)
	if s, err := Open(path); err == nil {
		s.Close()
		t.Fatal("broken metadata namespace accepted")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	assertLegacyBytes(t, db, original)
	var version, n int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		t.Fatal("failed migration advanced version", version, err)
	}
	if err = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name='codex_agents'").Scan(&n); err != nil || n != 0 {
		t.Fatal("partial migration survived", err)
	}
}
func TestCodexFutureSchemaRefused(t *testing.T) {
	path, _, _ := legacyFixture(t, false)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("PRAGMA user_version=5"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if s, err := Open(path); err == nil {
		s.Close()
		t.Fatal("future schema silently downgraded")
	}
}
func TestCodexAttemptCrashAndHistoricalGenerationSettlement(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state", "registry.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	v, _ := recordTest(t, s)
	a := cb.AgentBinding{HostID: v.HostID, SessionID: v.ID, ExecutionID: v.Execution.ID, Generation: v.Execution.Generation, AppServerIncarnation: "owned-primary", AppServerPID: 1, AppServerBootID: "boot", AppServerProcessStart: "start", ProtocolVersion: "v1", NativeThreadID: "thread"}
	req := cb.WriterRequest{Agent: a, OperationID: "grant", PrincipalID: "alice", ClientID: "client", Kind: cb.NativeWriter}
	g, err := s.CodexAcquire(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	c := cb.ConnectionBinding{ID: "connection", Agent: a, Writer: g.WriterRef, Role: cb.NativeLocal, NativeLaunchID: "launch", EndpointIncarnation: "endpoint", NativePID: 2, NativeProcessStart: "start"}
	if _, err = s.CodexBind(ctx, c); err != nil {
		t.Fatal(err)
	}
	in := cb.Intent{OperationID: "rpc", Origin: c, UpstreamChannelID: "channel", DownstreamRequestID: json.RawMessage("2"), Frame: cb.Frame{Kind: cb.Request, ID: json.RawMessage("1"), Method: "fixture/read", Payload: json.RawMessage("{}")}}
	r, claimed, err := s.CodexAttempt(ctx, in, cb.ScopedRead)
	if err != nil || !claimed || r.State != cb.Attempted {
		t.Fatal("no durable attempt", err)
	}
	// Recover the precise crash boundary before any sender outcome persisted.
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecoverCodex(ctx); err != nil {
		t.Fatal(err)
	}
	r, err = s.CodexReceipt(ctx, in.OperationID)
	if err != nil || r.State != cb.Uncertain {
		t.Fatal("attempt became replayable", err)
	}
	g, _, err = s.CodexGrant(ctx, g.Lease.OperationID)
	if err != nil || !g.Active || g.Admission != cb.Fenced {
		t.Fatal("crash released writer", err)
	}
	replacement := v
	replacement.Execution.ID = "new-execution"
	replacement.Execution.Generation++
	if _, err = s.db.Exec("UPDATE sessions SET data=? WHERE id=?", encoded(replacement), v.ID); err != nil {
		t.Fatal(err)
	}
	r.State = cb.Completed
	r.Detail = "trusted historical fixture receipt"
	if _, err = s.CodexSettle(ctx, r); err != nil {
		t.Fatal("historical settlement rejected", err)
	}
	current, err := s.Session(ctx, v.ID)
	if err != nil || current.Execution.ID != replacement.Execution.ID || current.Execution.Generation != replacement.Execution.Generation {
		t.Fatal("historical receipt projected onto replacement", err)
	}
	original, _, err := s.CodexGrant(ctx, g.Lease.OperationID)
	if err != nil || !original.Active || original.Admission != cb.Fenced {
		t.Fatal("historical receipt freed old reservation", err)
	}
}

func TestCodexV2CorruptionRequiresRepairWithoutReclassification(t *testing.T) {
	for _, damage := range []string{"table", "column", "writer-index", "grant-row", "agent-row", "epoch-row", "rpc-receipt"} {
		t.Run(damage, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state", "registry.db")
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			v, _ := recordTest(t, s)
			a := cb.AgentBinding{HostID: v.HostID, SessionID: v.ID, ExecutionID: v.Execution.ID, Generation: v.Execution.Generation, AppServerIncarnation: "primary", AppServerPID: 1, AppServerBootID: "boot", AppServerProcessStart: "start", ProtocolVersion: "v1", NativeThreadID: "thread"}
			g, err := s.CodexAcquire(context.Background(), cb.WriterRequest{Agent: a, OperationID: "grant", PrincipalID: "alice", ClientID: "client", Kind: cb.NativeWriter})
			if err != nil {
				t.Fatal(err)
			}
			c := cb.ConnectionBinding{ID: "conn", Agent: a, Writer: g.WriterRef, Role: cb.NativeLocal, NativeLaunchID: "launch", EndpointIncarnation: "endpoint", NativePID: 2, NativeProcessStart: "start"}
			if _, err = s.CodexBind(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			in := cb.Intent{OperationID: "rpc", Origin: c, UpstreamChannelID: "channel", DownstreamRequestID: json.RawMessage("2"), Frame: cb.Frame{Kind: cb.Request, ID: json.RawMessage("1"), Method: "fixture/read", Payload: json.RawMessage("{}")}}
			if _, _, err = s.CodexAttempt(context.Background(), in, cb.ScopedRead); err != nil {
				t.Fatal(err)
			}
			query := map[string]string{
				"table":        "DROP TABLE codex_grants",
				"column":       "ALTER TABLE codex_connections DROP COLUMN retired",
				"writer-index": "DROP INDEX one_writer; CREATE INDEX one_writer ON grants(session_id,execution_id) WHERE active=1 AND read_only=0",
				"grant-row":    "DELETE FROM codex_grants",
				"agent-row":    "DELETE FROM codex_agents",
				"epoch-row":    "DELETE FROM codex_epochs",
				"rpc-receipt":  "DELETE FROM codex_receipts",
			}[damage]
			if _, err = s.db.Exec(query); err != nil {
				t.Fatal(err)
			}
			if damage == "grant-row" {
				if err = s.Release(context.Background(), g.Lease); err == nil {
					t.Fatal("lost metadata let legacy release free broker slot")
				}
				raw, err := s.Grant(context.Background(), g.Lease.OperationID)
				if err != nil || !raw.Active {
					t.Fatal("reservation changed", err)
				}
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			if reopened, err := Open(path); err == nil {
				reopened.Close()
				t.Fatal("damaged v2 authority was silently healed or reclassified")
			} else if !strings.Contains(err.Error(), "repair required") {
				t.Fatal("no actionable repair error", err)
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var version, active int
			if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 4 {
				t.Fatal("repair changed version", err)
			}
			if err = db.QueryRow("SELECT active FROM grants WHERE operation_id='grant'").Scan(&active); err != nil || active != 1 {
				t.Fatal("repair changed master reservation", err)
			}
			if damage == "table" {
				var n int
				if err = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name='codex_grants'").Scan(&n); err != nil || n != 0 {
					t.Fatal("lost metadata table recreated", err)
				}
			}
		})
	}
}
func TestStoreSpecialFilesRefusedWithoutBlocking(t *testing.T) {
	for _, shape := range []string{"database-fifo", "lock-fifo", "database-directory", "lock-directory"} {
		t.Run(shape, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "registry.db")
			target := path
			if strings.HasPrefix(shape, "lock") {
				target += ".lock"
			}
			var err error
			if strings.HasSuffix(shape, "fifo") {
				err = syscall.Mkfifo(target, 0600)
			} else {
				err = os.Mkdir(target, 0700)
			}
			if err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() {
				s, err := Open(path)
				if s != nil {
					_ = s.Close()
				}
				result <- err
			}()
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("nonregular authority inode accepted")
				}
			case <-time.After(time.Second):
				t.Fatal("special file stalled authority open")
			}
		})
	}
}
