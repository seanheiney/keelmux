package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
)

var projectDefinitions = []struct{ name, kind, sql string }{
	{"projects", "table", `CREATE TABLE projects (id TEXT PRIMARY KEY, host_id TEXT NOT NULL, data BLOB NOT NULL)`},
	{"project_threads", "table", `CREATE TABLE project_threads (thread_id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), session_id TEXT NOT NULL REFERENCES sessions(id))`},
	{"project_thread_directory", "index", `CREATE INDEX project_thread_directory ON project_threads(project_id,thread_id)`},
}

func migrateProjects(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, d := range projectDefinitions {
		if _, err = tx.ExecContext(ctx, d.sql); err != nil {
			return repair("project schema creation refused")
		}
	}
	if _, err = tx.ExecContext(ctx, `DROP INDEX channel_active_endpoint;
ALTER TABLE channel_bindings ADD COLUMN recipient_id TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX channel_active_endpoint ON channel_bindings(provider,endpoint,realm_id,channel_id,root_id,recipient_id) WHERE active=1;
PRAGMA user_version=4;`); err != nil {
		return repair("addressed channel migration refused")
	}
	return tx.Commit()
}

func project(ctx context.Context, q queryer, id domain.ID) (domain.Project, error) {
	var p domain.Project
	var host domain.ID
	var raw []byte
	if err := q.QueryRowContext(ctx, "SELECT host_id,data FROM projects WHERE id=?", id).Scan(&host, &raw); err != nil {
		return p, missing(err)
	}
	if json.Unmarshal(raw, &p) != nil || !bytes.Equal(encoded(p), raw) || p.Validate() != nil || p.ID != id || p.HostID != host {
		return domain.Project{}, repair("invalid project identity")
	}
	return p, nil
}

func (s *Store) Project(ctx context.Context, id domain.ID) (domain.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return project(ctx, s.db, id)
}

// CreateProject is transactional and idempotent for its immutable ID/config.
// It never discovers sessions, creates a bot or contacts a remote service.
func (s *Store) CreateProject(ctx context.Context, p domain.Project) (domain.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.Validate() != nil {
		return domain.Project{}, errors.New("invalid project configuration")
	}
	p.CreatedAt = p.CreatedAt.UTC()
	if old, err := project(ctx, s.db, p.ID); err == nil {
		if old.HostID != p.HostID || old.Name != p.Name || old.Workspace != p.Workspace {
			return old, conflict("project ID already names another configuration")
		}
		return old, nil
	} else if !errors.Is(err, ErrNotFound) {
		return domain.Project{}, err
	}
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM projects").Scan(&count); err != nil {
		return domain.Project{}, err
	}
	if count >= 128 {
		return domain.Project{}, conflict("project capacity reached")
	}
	_, err := s.db.ExecContext(ctx, "INSERT INTO projects VALUES(?,?,?)", p.ID, p.HostID, encoded(p))
	return p, err
}

func (s *Store) Projects(ctx context.Context) ([]domain.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.QueryContext(ctx, "SELECT id,host_id,data FROM projects ORDER BY id")
	if err != nil {
		return nil, err
	}
	out := []domain.Project{}
	for rows.Next() {
		var p domain.Project
		var id, host domain.ID
		var raw []byte
		if err = rows.Scan(&id, &host, &raw); err != nil {
			break
		}
		if json.Unmarshal(raw, &p) != nil || !bytes.Equal(encoded(p), raw) || p.Validate() != nil || p.ID != id || p.HostID != host || len(out) >= 128 {
			err = repair("invalid project directory")
			break
		}
		out = append(out, p)
	}
	if err = closeChannelRows(rows, err); err != nil {
		return nil, err
	}
	return out, nil
}

// BindProjectThread explicitly fixes a stable thread's project before external
// provisioning. Execution replacement or a local navigation group cannot move
// it. A lost external registration keeps this membership for reconciliation.
func (s *Store) BindProjectThread(ctx context.Context, projectID, sessionID domain.ID) (domain.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Project{}, err
	}
	defer tx.Rollback()
	p, err := project(ctx, tx, projectID)
	if err != nil {
		return p, err
	}
	v, err := session(ctx, tx, sessionID)
	if err != nil {
		return p, err
	}
	if !domain.ValidObjectID(v.ThreadID, "thread") || v.HostID != p.HostID || v.Workspace != p.Workspace {
		return p, conflict("thread does not belong to the project's exact home and workspace")
	}
	var oldProject, oldSession domain.ID
	err = tx.QueryRowContext(ctx, "SELECT project_id,session_id FROM project_threads WHERE thread_id=?", v.ThreadID).Scan(&oldProject, &oldSession)
	if err == nil {
		if oldProject != projectID || oldSession != sessionID {
			return p, conflict("thread already belongs to another project or seed session")
		}
		return p, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return p, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO project_threads VALUES(?,?,?)", v.ThreadID, projectID, sessionID); err != nil {
		return p, err
	}
	return p, tx.Commit()
}

func validateProjectSchema(ctx context.Context, db *sql.DB) error {
	for _, d := range projectDefinitions {
		var kind, definition string
		if err := db.QueryRowContext(ctx, "SELECT type,sql FROM sqlite_master WHERE name=?", d.name).Scan(&kind, &definition); err != nil || kind != d.kind || normalizedChannelSQL(definition) != normalizedChannelSQL(d.sql) {
			return repair("missing or changed project schema")
		}
	}
	projects := map[domain.ID]domain.Project{}
	rows, err := db.QueryContext(ctx, "SELECT id,host_id,data FROM projects")
	if err != nil {
		return err
	}
	for rows.Next() {
		var p domain.Project
		var id, host domain.ID
		var raw []byte
		if err = rows.Scan(&id, &host, &raw); err != nil {
			break
		}
		if json.Unmarshal(raw, &p) != nil || !bytes.Equal(encoded(p), raw) || p.Validate() != nil || p.ID != id || p.HostID != host || len(projects) >= 128 {
			err = repair("invalid stored project")
			break
		}
		projects[id] = p
	}
	if err = closeChannelRows(rows, err); err != nil {
		return err
	}
	// Close the directory reader before provenance lookups: the authority uses
	// exactly one SQLite connection, and nested queries must never deadlock it.
	type member struct{ thread, project, session domain.ID }
	members := []member{}
	rows, err = db.QueryContext(ctx, "SELECT thread_id,project_id,session_id FROM project_threads")
	if err != nil {
		return err
	}
	for rows.Next() {
		var m member
		if err = rows.Scan(&m.thread, &m.project, &m.session); err != nil {
			break
		}
		members = append(members, m)
	}
	if err = closeChannelRows(rows, err); err != nil {
		return err
	}
	for _, m := range members {
		p, ok := projects[m.project]
		v, e := session(ctx, db, m.session)
		if !ok || e != nil || v.ThreadID != m.thread || !domain.ValidObjectID(m.thread, "thread") || v.HostID != p.HostID || v.Workspace != p.Workspace {
			return repair("project membership lost exact thread provenance")
		}
	}
	return nil
}

// NewProject leaves creation intent/time to the trusted local home surface.
func NewProject(host domain.ID, id domain.ID, name, workspace string) domain.Project {
	return domain.Project{ID: id, HostID: host, Name: name, Workspace: workspace, CreatedAt: time.Now().UTC()}
}
