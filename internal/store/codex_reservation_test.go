package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	cb "github.com/seanheiney/agent-backplane/internal/codexbroker"
	"github.com/seanheiney/agent-backplane/internal/domain"
)

func codexReservationFixture(t *testing.T, s *Store) (domain.Session, cb.Grant, cb.ConnectionBinding, cb.Intent) {
	t.Helper()
	ctx := context.Background()
	v, _ := recordTest(t, s)
	a := cb.AgentBinding{HostID: v.HostID, SessionID: v.ID, ExecutionID: v.Execution.ID, Generation: v.Execution.Generation, AppServerIncarnation: "primary", AppServerPID: 1, AppServerBootID: "boot", AppServerProcessStart: "start", ProtocolVersion: "v1", NativeThreadID: "thread"}
	g, err := s.CodexAcquire(ctx, cb.WriterRequest{Agent: a, OperationID: "original", PrincipalID: "alice", ClientID: "original-client", Kind: cb.NativeWriter})
	if err != nil {
		t.Fatal(err)
	}
	c := cb.ConnectionBinding{ID: "connection", Agent: a, Writer: g.WriterRef, Role: cb.NativeLocal, NativeLaunchID: "launch", EndpointIncarnation: "endpoint", NativePID: 2, NativeProcessStart: "start"}
	if _, err = s.CodexBind(ctx, c); err != nil {
		t.Fatal(err)
	}
	in := cb.Intent{OperationID: "pending", Origin: c, UpstreamChannelID: "channel", DownstreamRequestID: json.RawMessage("2"), Frame: cb.Frame{Kind: cb.Request, ID: json.RawMessage("1"), Method: "fixture/read", Payload: json.RawMessage("{}")}}
	return v, g, c, in
}

func TestCodexReservationStateCorruptionRefusedWithoutRewritingEvidence(t *testing.T) {
	for _, damage := range []string{"lost-master-active", "retired-master-active", "inactive-fenced", "active-retired", "invalid-master-active"} {
		t.Run(damage, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state", "registry.db")
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			_, g, _, in := codexReservationFixture(t, s)
			if _, _, err = s.CodexAttempt(context.Background(), in, cb.ScopedRead); err != nil {
				t.Fatal(err)
			}
			active := 1
			switch damage {
			case "lost-master-active":
				active = 0
			case "retired-master-active":
				g.Active, g.Admission = false, cb.Retired
			case "inactive-fenced":
				active = 0
				g.Active, g.Admission = false, cb.Fenced
			case "active-retired":
				g.Admission = cb.Retired
			case "invalid-master-active":
				active = 2
			}
			if _, err = s.db.Exec(`UPDATE grants SET active=? WHERE operation_id='original'`, active); err != nil {
				t.Fatal(err)
			}
			if _, err = s.db.Exec(`UPDATE codex_grants SET data=? WHERE operation_id='original'`, encoded(g)); err != nil {
				t.Fatal(err)
			}
			queries := map[string]string{
				"master":  "SELECT data FROM grants WHERE operation_id='original'",
				"meta":    "SELECT data FROM codex_grants WHERE operation_id='original'",
				"op":      "SELECT data FROM operations WHERE id='pending'",
				"payload": "SELECT payload FROM operations WHERE id='pending'",
				"receipt": "SELECT data FROM codex_receipts WHERE operation_id='pending'",
			}
			original := map[string][]byte{}
			for key, query := range queries {
				var b []byte
				if err = s.db.QueryRow(query).Scan(&b); err != nil {
					t.Fatal(err)
				}
				original[key] = b
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			if reopened, err := Open(path); err == nil {
				reopened.Close()
				t.Fatal("damaged reservation reopened as usable authority")
			} else if !strings.Contains(err.Error(), "repair required") {
				t.Fatal("repair refusal was not actionable", err)
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			for key, query := range queries {
				var b []byte
				if err = db.QueryRow(query).Scan(&b); err != nil || !bytes.Equal(b, original[key]) {
					t.Fatal("repair refusal rewrote original "+key, err)
				}
			}
			var got int
			if err = db.QueryRow(`SELECT active FROM grants WHERE operation_id='original'`).Scan(&got); err != nil || got != active {
				t.Fatal("repair refusal changed the master reservation", err)
			}
		})
	}
}

func TestCodexNewAcquisitionCannotLeapUnresolvedRetiredEffects(t *testing.T) {
	for _, effect := range []string{"receipt", "callback"} {
		for _, target := range []string{"broker-current", "broker-replacement", "legacy-replacement"} {
			t.Run(effect+"/"+target, func(t *testing.T) {
				s := openTest(t)
				ctx := context.Background()
				v, g, c, in := codexReservationFixture(t, s)
				var receipt cb.Receipt
				var err error
				if effect == "receipt" {
					receipt, _, err = s.CodexAttempt(ctx, in, cb.ScopedRead)
					if err == nil {
						receipt.State = cb.Uncertain
						receipt, err = s.CodexSettle(ctx, receipt)
					}
				} else {
					_, err = s.CodexCallback(ctx, cb.CallbackRecord{ID: "quarantine", Origin: c, Key: cb.CallbackKey{UpstreamChannelID: "channel"}, Payload: json.RawMessage(`{"Quarantine":"malformed owned frame"}`), DecisionState: cb.Uncertain})
				}
				if err != nil {
					t.Fatal(err)
				}
				// Expose the defensive boundary with a coherent retired row, even
				// though the public handover path refuses retirement while blocked.
				tx, err := s.db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = fenceCodex(ctx, tx, g.WriterRef, true); err != nil {
					tx.Rollback()
					t.Fatal(err)
				}
				if err = tx.Commit(); err != nil {
					t.Fatal(err)
				}
				a := c.Agent
				if target != "broker-current" {
					v.Execution.ID = "replacement-execution"
					v.Execution.Generation++
					a.ExecutionID, a.Generation, a.AppServerIncarnation = v.Execution.ID, v.Execution.Generation, "replacement-primary"
					if _, err = s.db.Exec("UPDATE sessions SET data=? WHERE id=?", encoded(v), v.ID); err != nil {
						t.Fatal(err)
					}
				}
				acquire := func() error {
					if target == "legacy-replacement" {
						_, e := s.Acquire(ctx, domain.Lease{SessionID: v.ID, ExecutionID: v.Execution.ID, Generation: v.Execution.Generation, OperationID: "next", ClientID: "new-client", Token: "private-fixture-token"}, false, []byte(`{"fixture":"replacement"}`))
						return e
					}
					_, e := s.CodexAcquire(ctx, cb.WriterRequest{Agent: a, OperationID: "next", PrincipalID: "alice", ClientID: "new-client", Kind: cb.SemanticWriter})
					return e
				}
				var collision domain.ConflictError
				if err = acquire(); !errors.As(err, &collision) {
					t.Fatal("ordinary acquisition leapt unresolved original effect", err)
				}
				if _, _, err = s.Operation(ctx, "next"); !errors.Is(err, ErrNotFound) {
					t.Fatal("blocked acquisition left a new operation", err)
				}
				if effect == "receipt" {
					receipt.State = cb.Completed
					if _, err = s.CodexSettle(ctx, receipt); err != nil {
						t.Fatal(err)
					}
					if err = acquire(); err != nil {
						t.Fatal("exact historical settlement did not clear its effect blocker", err)
					}
				}
			})
		}
	}
}

func TestCodexMissingCallbackIDCannotAcquireAttribution(t *testing.T) {
	for _, damage := range []string{"attributed", "resolved", "allowed", "display", "decision", "state"} {
		t.Run(damage, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state", "registry.db")
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			_, _, origin, _ := codexReservationFixture(t, s)
			c := cb.CallbackRecord{ID: "quarantine", Origin: origin, Key: cb.CallbackKey{UpstreamChannelID: "channel"}, Payload: json.RawMessage(`{"Quarantine":"malformed owned frame"}`), DecisionState: cb.Uncertain}
			if _, err = s.CodexCallback(context.Background(), c); err != nil {
				t.Fatal("non-actionable missing-ID quarantine refused", err)
			}
			switch damage {
			case "attributed":
				c.Attributed = true
			case "resolved":
				c.Resolved = true
			case "allowed":
				c.Allowed = []cb.DecisionAction{cb.Accept}
			case "display":
				c.DisplayConnectionID = origin.ID
			case "decision":
				c.DecisionOperationID = "forged-decision"
			case "state":
				c.DecisionState = cb.Completed
			}
			// Use a fresh record ID so immutable-ID collision cannot mask this
			// missing-native-ID admission check.
			c.ID = "new-quarantine"
			var collision domain.ConflictError
			if _, err = s.CodexCallback(context.Background(), c); !errors.As(err, &collision) {
				t.Fatal("missing-ID callback acquired decision authority", err)
			}
			c.ID = "quarantine"
			bad := encoded(c)
			if _, err = s.db.Exec(`UPDATE codex_callbacks SET data=? WHERE id='quarantine'`, bad); err != nil {
				t.Fatal(err)
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			if reopened, err := Open(path); err == nil {
				reopened.Close()
				t.Fatal("established v2 promoted missing-ID quarantine")
			} else if !strings.Contains(err.Error(), "repair required") {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var got []byte
			if err = db.QueryRow(`SELECT data FROM codex_callbacks WHERE id='quarantine'`).Scan(&got); err != nil || !bytes.Equal(got, bad) {
				t.Fatal("refusal rewrote quarantined source evidence", err)
			}
		})
	}
}
