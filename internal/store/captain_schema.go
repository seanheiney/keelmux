package store

// captainDefinitions is the additive v5 contract. Root owns Store.Open and
// migrateCaptains(ctx, db): one transaction calls createCaptainSchema(ctx, tx),
// then PRAGMA user_version=5. No roles/grants/assignments are seeded. Existing
// v5 databases must validateCaptainSchema(ctx, db), never recreate missing rows.
// The shared addressed-channel validator must select its v4 form for version>=4.
// Creation/validation implementations follow only after this contract freezes.
var captainDefinitions = []struct{ name, kind, sql string }{
	{"captain_commands", "table", `CREATE TABLE captain_commands (id TEXT PRIMARY KEY, object_id TEXT NOT NULL, revision INTEGER NOT NULL CHECK(revision>0), kind TEXT NOT NULL, data BLOB NOT NULL, UNIQUE(object_id,revision))`},
	{"captain_principals", "table", `CREATE TABLE captain_principals (id TEXT PRIMARY KEY, revision INTEGER NOT NULL CHECK(revision>0), active INTEGER NOT NULL CHECK(active IN (0,1)), last_command TEXT NOT NULL REFERENCES captain_commands(id), data BLOB NOT NULL)`},
	{"captain_read_grants", "table", `CREATE TABLE captain_read_grants (id TEXT PRIMARY KEY, owner_id TEXT NOT NULL REFERENCES captain_principals(id), recipient_id TEXT NOT NULL REFERENCES captain_principals(id), session_id TEXT NOT NULL REFERENCES sessions(id), revision INTEGER NOT NULL CHECK(revision>0), active INTEGER NOT NULL CHECK(active IN (0,1)), last_command TEXT NOT NULL REFERENCES captain_commands(id), data BLOB NOT NULL)`},
	{"captain_assignments", "table", `CREATE TABLE captain_assignments (id TEXT PRIMARY KEY, owner_id TEXT NOT NULL REFERENCES captain_principals(id), runner_id TEXT NOT NULL REFERENCES captain_principals(id), session_id TEXT NOT NULL REFERENCES sessions(id), revision INTEGER NOT NULL CHECK(revision>0), control TEXT NOT NULL CHECK(control IN ('active','paused','revoked')), last_command TEXT NOT NULL REFERENCES captain_commands(id), data BLOB NOT NULL)`},
	{"captain_entries", "table", `CREATE TABLE captain_entries (assignment_id TEXT NOT NULL REFERENCES captain_assignments(id), sequence INTEGER NOT NULL CHECK(sequence>0), id TEXT NOT NULL UNIQUE, kind TEXT NOT NULL CHECK(kind IN ('quiet','reserve','report','cancel')), reservation_id TEXT NOT NULL, report_id TEXT NOT NULL, data BLOB NOT NULL, PRIMARY KEY(assignment_id,sequence))`},
	{"captain_one_reservation", "index", `CREATE UNIQUE INDEX captain_one_reservation ON captain_entries(reservation_id) WHERE kind='reserve'`},
	{"captain_one_report", "index", `CREATE UNIQUE INDEX captain_one_report ON captain_entries(reservation_id) WHERE kind='report'`},
	{"captain_report_id", "index", `CREATE UNIQUE INDEX captain_report_id ON captain_entries(report_id) WHERE kind='report'`},
}
