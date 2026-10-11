package store

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/seanheiney/keelmux/internal/domain"
)

func captainV4Fixture(t *testing.T) (string, domain.Session, []byte, []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "home", "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	session := domain.NewSession(domain.NewID("host"), "Existing work", "/work/project", "fixture", "test")
	op := domain.Operation{ID: domain.NewID("op"), Kind: "launch", SessionID: session.ID, ExecutionID: session.Execution.ID}
	if _, _, err = s.Record(context.Background(), op, []byte(`{"original":"payload"}`), &session); err != nil {
		t.Fatal(err)
	}
	var original, payload []byte
	if err = s.db.QueryRow("SELECT data FROM sessions WHERE id=?", session.ID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow("SELECT payload FROM operations WHERE id=?", op.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	// A v4 fixture has no captain metadata and retains its addressed-channel schema.
	if _, err = s.db.Exec(`DROP TABLE captain_entries; DROP TABLE captain_assignments; DROP TABLE captain_read_grants; DROP TABLE captain_principals; DROP TABLE captain_commands; PRAGMA user_version=4;`); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	return path, session, original, payload
}

func TestCaptainV4UpgradeAddsNoAuthorityAndPreservesWorkerBytes(t *testing.T) {
	path, session, original, payload := captainV4Fixture(t)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var version int
	if err = s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 5 {
		t.Fatal("captain migration did not commit v5", version, err)
	}
	for _, table := range []string{"captain_principals", "captain_read_grants", "captain_assignments", "captain_entries", "captain_commands"} {
		var count int
		if err = s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("migration invented captain authority", table, count, err)
		}
	}
	var got, gotPayload []byte
	if err = s.db.QueryRow("SELECT data FROM sessions WHERE id=?", session.ID).Scan(&got); err != nil || !bytes.Equal(got, original) {
		t.Fatal("worker identity bytes changed", err)
	}
	if err = s.db.QueryRow("SELECT payload FROM operations WHERE session_id=?", session.ID).Scan(&gotPayload); err != nil || !bytes.Equal(gotPayload, payload) {
		t.Fatal("original operation payload changed", err)
	}
	if err = validateChannelSchema(context.Background(), s.db); err != nil {
		t.Fatal("v5 lost addressed channel schema", err)
	}
}

func TestCaptainMigrationRefusesPreexistingObjectsWithoutPartialCommit(t *testing.T) {
	path, _, _, _ := captainV4Fixture(t)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE captain_commands (sentinel TEXT); INSERT INTO captain_commands VALUES ('unowned');`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if s, err := Open(path); err == nil {
		s.Close()
		t.Fatal("adopted unverified captain object")
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	var sentinel string
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 4 {
		t.Fatal("failed migration advanced version", version, err)
	}
	if err = db.QueryRow("SELECT sentinel FROM captain_commands").Scan(&sentinel); err != nil || sentinel != "unowned" {
		t.Fatal("failed migration replaced preexisting object", err)
	}
	var count int
	if err = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name='captain_principals'").Scan(&count); err != nil || count != 0 {
		t.Fatal("failed migration left partial authority tables", count, err)
	}
}
