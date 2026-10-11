package store

import (
	"context"
	"database/sql"
	"strings"

	c "github.com/seanheiney/keelmux/internal/captain"
	"github.com/seanheiney/keelmux/internal/domain"
)

func captainMutation(a *captainAuthority, caller c.AuthCaller, m c.Mutation, kind string, id domain.ID, desired any) (*captainCommand, error) {
	if !c.ValidID(m.RequestID, c.RequestIDKind) {
		return nil, c.ErrConflict
	}
	if cmd, ok := a.commands[m.RequestID]; ok {
		var result any
		switch kind {
		case "principal":
			result = cmd.Principal
		case "grant":
			result = cmd.Grant
		case "assignment", "control":
			result = cmd.Assignment
		}
		if cmd.Actor != caller.PrincipalID || cmd.Kind != kind || cmd.ObjectID != id || cmd.Revision != m.ExpectedRevision+1 || !captainSame(result, desired) {
			return nil, c.ErrConflict
		}
		return &cmd, nil
	}
	return nil, nil
}

func insertCaptainCommand(ctx context.Context, tx *sql.Tx, a *captainAuthority, cmd captainCommand) error {
	if len(a.commands) >= c.MaxCommands {
		return c.ErrCapacity
	}
	var used int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM captain_entries WHERE id=?`, cmd.ID).Scan(&used); err != nil {
		return err
	}
	if used != 0 {
		return c.ErrConflict
	}
	cmd.Version = c.Version
	cmd.PreviousDigest = a.latest[cmd.ObjectID].Digest
	cmd.Digest = commandDigest(cmd)
	raw := encoded(cmd)
	if len(raw) > c.MaxRecordBytes || uint64(len(raw)) > c.MaxCommandBytes-a.commandBytes {
		return c.ErrCapacity
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO captain_commands(id,object_id,revision,kind,data) VALUES(?,?,?,?,?)`, cmd.ID, cmd.ObjectID, cmd.Revision, cmd.Kind, raw)
	return err
}

// BootstrapCaptainOwner is a trusted local initialization hook, deliberately
// absent from Service/wire. It neither discovers an owner nor authenticates one.
// Even an exact retry requires the original sole initial owner to remain active.
func (s *Store) BootstrapCaptainOwner(ctx context.Context, id domain.ID) (c.Principal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return c.Principal{}, err
	}
	defer tx.Rollback()
	a, err := loadCaptainAuthority(ctx, tx)
	if err != nil {
		return c.Principal{}, err
	}
	if !c.ValidID(id, c.PrincipalIDKind) {
		return c.Principal{}, c.ErrConflict
	}
	if len(a.principals) > 0 {
		p, ok := a.principals[id]
		if !ok || !c.HasRole(p, c.Owner) || a.latest[id].Kind != "bootstrap" {
			return c.Principal{}, c.ErrDenied
		}
		return p, nil
	}
	var entries int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM captain_entries`).Scan(&entries); err != nil {
		return c.Principal{}, err
	}
	if len(a.commands) != 0 || entries != 0 {
		return c.Principal{}, c.ErrCorrupt
	}
	p := c.Principal{Version: c.Version, Ref: c.Ref{ID: id, Revision: 1}, Roles: []c.Role{c.Owner}, Active: true}
	requestID := domain.ID(c.RequestIDKind + "_" + string(id)[strings.LastIndex(string(id), "_")+1:])
	cmd := captainCommand{ID: requestID, ObjectID: id, Revision: 1, Kind: "bootstrap", Actor: id, ActorRevision: 1, Principal: &p}
	if err = insertCaptainCommand(ctx, tx, a, cmd); err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO captain_principals VALUES(?,?,?,?,?)`, id, 1, 1, requestID, encoded(p))
	}
	if err != nil {
		return c.Principal{}, err
	}
	return p, tx.Commit()
}

func (s *Store) PutCaptainPrincipal(ctx context.Context, caller c.AuthCaller, m c.Mutation, p c.Principal) (c.Principal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return c.Principal{}, err
	}
	defer tx.Rollback()
	a, err := loadCaptainAuthority(ctx, tx)
	if err != nil {
		return c.Principal{}, err
	}
	if err = captainAuthenticate(a, caller, c.Owner); err != nil {
		return c.Principal{}, err
	}
	if c.ValidatePrincipal(p) != nil || p.Ref.Revision != m.ExpectedRevision+1 {
		return c.Principal{}, c.ErrConflict
	}
	if old, err := captainMutation(a, caller, m, "principal", p.Ref.ID, &p); err != nil {
		return c.Principal{}, err
	} else if old != nil {
		return *old.Principal, nil
	}
	previous, exists := a.principals[p.Ref.ID]
	if !exists && m.ExpectedRevision != 0 || exists && c.CheckRevision(previous.Ref.Revision, m.ExpectedRevision) != nil {
		return c.Principal{}, c.ErrConflict
	}
	if !exists && len(a.principals) >= c.MaxPrincipals {
		return c.Principal{}, c.ErrCapacity
	}
	cmd := captainCommand{ID: m.RequestID, ObjectID: p.Ref.ID, Revision: p.Ref.Revision, Kind: "principal", Actor: caller.PrincipalID, ActorRevision: caller.RoleRevision, Principal: &p}
	if err = insertCaptainCommand(ctx, tx, a, cmd); err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO captain_principals VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision,active=excluded.active,last_command=excluded.last_command,data=excluded.data`, p.Ref.ID, p.Ref.Revision, p.Active, m.RequestID, encoded(p))
	}
	if err != nil {
		return c.Principal{}, err
	}
	return p, tx.Commit()
}

func (s *Store) PutCaptainGrant(ctx context.Context, caller c.AuthCaller, m c.Mutation, g c.ReadGrant) (c.ReadGrant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return c.ReadGrant{}, err
	}
	defer tx.Rollback()
	a, err := loadCaptainAuthority(ctx, tx)
	if err != nil {
		return c.ReadGrant{}, err
	}
	if err = captainAuthenticate(a, caller, c.Owner); err != nil {
		return c.ReadGrant{}, err
	}
	if c.ValidateGrant(g) != nil || g.Ref.Revision != m.ExpectedRevision+1 || g.OwnerPrincipal != caller.PrincipalID {
		return c.ReadGrant{}, c.ErrConflict
	}
	if old, err := captainMutation(a, caller, m, "grant", g.Ref.ID, &g); err != nil {
		return c.ReadGrant{}, err
	} else if old != nil {
		return *old.Grant, nil
	}
	previous, exists := a.grants[g.Ref.ID]
	if !exists && m.ExpectedRevision != 0 || exists && c.CheckRevision(previous.Ref.Revision, m.ExpectedRevision) != nil {
		return c.ReadGrant{}, c.ErrConflict
	}
	if exists {
		old := previous
		old.Ref = g.Ref
		old.Active = g.Active
		if !captainSame(old, g) || !previous.Active && g.Active {
			return c.ReadGrant{}, c.ErrConflict
		}
	} else if !g.Active {
		return c.ReadGrant{}, c.ErrConflict
	}
	if !exists && len(a.grants) >= c.MaxGrants {
		return c.ReadGrant{}, c.ErrCapacity
	}
	recipient, ok := a.principals[g.RecipientPrincipal]
	if !ok || !recipient.Active {
		return c.ReadGrant{}, c.ErrDenied
	}
	if g.Active {
		if err = captainTarget(ctx, tx, g.Source.Target); err != nil {
			return c.ReadGrant{}, err
		}
	}
	cmd := captainCommand{ID: m.RequestID, ObjectID: g.Ref.ID, Revision: g.Ref.Revision, Kind: "grant", Actor: caller.PrincipalID, ActorRevision: caller.RoleRevision, Grant: &g}
	if err = insertCaptainCommand(ctx, tx, a, cmd); err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO captain_read_grants VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision,active=excluded.active,last_command=excluded.last_command,data=excluded.data`, g.Ref.ID, g.OwnerPrincipal, g.RecipientPrincipal, g.Source.Target.SessionID, g.Ref.Revision, g.Active, m.RequestID, encoded(g))
	}
	if err != nil {
		return c.ReadGrant{}, err
	}
	return g, tx.Commit()
}

func (s *Store) CreateCaptainAssignment(ctx context.Context, caller c.AuthCaller, m c.Mutation, v c.Assignment) (c.Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return c.Status{}, err
	}
	defer tx.Rollback()
	a, err := loadCaptainAuthority(ctx, tx)
	if err != nil {
		return c.Status{}, err
	}
	if err = captainAuthenticate(a, caller, c.Owner); err != nil {
		return c.Status{}, err
	}
	if c.ValidateAssignment(v) != nil || m.ExpectedRevision != 0 || v.Ref.Revision != 1 || v.Control != c.Active || v.OwnerPrincipal != caller.PrincipalID {
		return c.Status{}, c.ErrConflict
	}
	if old, err := captainMutation(a, caller, m, "assignment", v.Ref.ID, &v); err != nil {
		return c.Status{}, err
	} else if old != nil {
		st, _, err := loadCaptainLedger(ctx, tx, a, v.Ref.ID)
		if err != nil {
			return c.Status{}, err
		}
		return st, nil
	}
	if _, exists := a.assignments[v.Ref.ID]; exists {
		return c.Status{}, c.ErrConflict
	}
	if len(a.assignments) >= c.MaxAssignments {
		return c.Status{}, c.ErrCapacity
	}
	inputs, err := captainScopes(ctx, tx, a, v)
	if err != nil {
		return c.Status{}, err
	}
	if err = captainAudience(ctx, tx, a, v, inputs, ""); err != nil {
		return c.Status{}, err
	}
	st := c.Status{Assignment: v, Cursors: []c.Position{}, Coverage: c.EmptyCoverage(), EntryDigest: c.EntryGenesis(v)}
	for _, g := range inputs {
		st.Cursors = append(st.Cursors, c.Position{Grant: g.Ref, Through: g.Source.FirstSequence - 1, AnchorDigest: c.Genesis(g)})
	}
	cmd := captainCommand{ID: m.RequestID, ObjectID: v.Ref.ID, Revision: 1, Kind: "assignment", Actor: caller.PrincipalID, ActorRevision: caller.RoleRevision, Assignment: &v}
	if err = insertCaptainCommand(ctx, tx, a, cmd); err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO captain_assignments VALUES(?,?,?,?,?,?,?,?)`, v.Ref.ID, v.OwnerPrincipal, v.RunnerPrincipal, v.Captain.SessionID, v.Ref.Revision, v.Control, m.RequestID, encoded(st))
	}
	if err != nil {
		return c.Status{}, err
	}
	return st, tx.Commit()
}

func (s *Store) SetCaptainControl(ctx context.Context, caller c.AuthCaller, m c.Mutation, ref c.Ref, state c.Control) (c.Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return c.Status{}, err
	}
	defer tx.Rollback()
	a, err := loadCaptainAuthority(ctx, tx)
	if err != nil {
		return c.Status{}, err
	}
	if err = captainAuthenticate(a, caller, c.Owner); err != nil {
		return c.Status{}, err
	}
	st, _, err := loadCaptainLedger(ctx, tx, a, ref.ID)
	if err != nil {
		return c.Status{}, err
	}
	if st.Assignment.OwnerPrincipal != caller.PrincipalID || ref.Revision != m.ExpectedRevision {
		return c.Status{}, c.ErrDenied
	}
	desired := st.Assignment
	desired.Ref.Revision = ref.Revision + 1
	desired.Control = state
	if c.ValidateAssignment(desired) != nil {
		return c.Status{}, c.ErrConflict
	}
	if old, err := captainMutation(a, caller, m, "control", ref.ID, &desired); err != nil {
		return c.Status{}, err
	} else if old != nil {
		return st, nil
	}
	if c.CheckRevision(st.Assignment.Ref.Revision, ref.Revision) != nil || st.Assignment.Control == c.Revoked {
		return c.Status{}, c.ErrConflict
	}
	if state == c.Active {
		inputs, err := captainScopes(ctx, tx, a, desired)
		if err != nil {
			return c.Status{}, err
		}
		if err = captainAudience(ctx, tx, a, desired, inputs, ""); err != nil {
			return c.Status{}, err
		}
	}
	st.Assignment = desired
	cmd := captainCommand{ID: m.RequestID, ObjectID: ref.ID, Revision: desired.Ref.Revision, Kind: "control", Actor: caller.PrincipalID, ActorRevision: caller.RoleRevision, Assignment: &desired}
	if err = insertCaptainCommand(ctx, tx, a, cmd); err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE captain_assignments SET revision=?,control=?,last_command=?,data=? WHERE id=?`, desired.Ref.Revision, state, m.RequestID, encoded(st), ref.ID)
	}
	if err != nil {
		return c.Status{}, err
	}
	return st, tx.Commit()
}
