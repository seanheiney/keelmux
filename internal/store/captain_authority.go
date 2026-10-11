package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"unicode/utf8"

	c "github.com/seanheiney/keelmux/internal/captain"
	"github.com/seanheiney/keelmux/internal/domain"
)

type captainQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type captainCommand struct {
	Version        uint32        `json:"version"`
	ID             domain.ID     `json:"id"`
	ObjectID       domain.ID     `json:"object_id"`
	Revision       uint64        `json:"revision"`
	Kind           string        `json:"kind"`
	Actor          domain.ID     `json:"actor"`
	ActorRevision  uint64        `json:"actor_revision"`
	PreviousDigest string        `json:"previous_digest"`
	Principal      *c.Principal  `json:"principal"`
	Grant          *c.ReadGrant  `json:"grant"`
	Assignment     *c.Assignment `json:"assignment"`
	Digest         string        `json:"digest"`
}

type captainAuthority struct {
	principals   map[domain.ID]c.Principal
	grants       map[domain.ID]c.ReadGrant
	assignments  map[domain.ID]c.Assignment
	commands     map[domain.ID]captainCommand
	latest       map[domain.ID]captainCommand
	commandBytes uint64
}

func captainDecode[T any](raw []byte) (out T, err error) {
	if len(raw) == 0 || len(raw) > c.MaxRecordBytes || !utf8.Valid(raw) || json.Unmarshal(raw, &out) != nil || !bytes.Equal(encoded(out), raw) {
		return out, c.ErrCorrupt
	}
	return out, nil
}

func captainSame(a, b any) bool { return bytes.Equal(encoded(a), encoded(b)) }

func commandDigest(v captainCommand) string { v.Digest = ""; return c.Digest("captain.command.v1", v) }

func newCaptainAuthority() *captainAuthority {
	return &captainAuthority{principals: map[domain.ID]c.Principal{}, grants: map[domain.ID]c.ReadGrant{}, assignments: map[domain.ID]c.Assignment{}, commands: map[domain.ID]captainCommand{}, latest: map[domain.ID]captainCommand{}}
}

// Source lengths are checked in SQLite before a blob can enter Go memory.
func captainRows(ctx context.Context, q captainQuery, query string, limit int, maxBytes uint64, visit func(*sql.Rows, []byte) error, args ...any) error {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	var total uint64
	for rows.Next() {
		count++
		if count > limit {
			return c.ErrCapacity
		}
		// Each fixed SELECT returns indexed columns, length, and bounded data.
		cols, err := rows.Columns()
		if err != nil {
			return err
		}
		values := make([]any, len(cols))
		holders := make([]any, len(cols))
		for i := range values {
			holders[i] = &values[i]
		}
		if err = rows.Scan(holders...); err != nil {
			return err
		}
		n, ok := values[len(values)-2].(int64)
		if !ok || n <= 0 || n > c.MaxRecordBytes {
			return c.ErrCorrupt
		}
		if uint64(n) > maxBytes-total {
			return c.ErrCapacity
		}
		total += uint64(n)
		raw, ok := values[len(values)-1].([]byte)
		if !ok || int64(len(raw)) != n {
			return c.ErrCorrupt
		}
		// Re-scan is supported for the current row and keeps fixed typed mirrors.
		if err = visit(rows, raw); err != nil {
			return err
		}
	}
	return rows.Err()
}

func loadCaptainAuthority(ctx context.Context, q captainQuery) (*captainAuthority, error) {
	a := newCaptainAuthority()
	err := captainRows(ctx, q, `SELECT id,object_id,revision,kind,length(data),CASE WHEN length(data)<=524288 THEN data END FROM captain_commands ORDER BY object_id,revision LIMIT 4097`, c.MaxCommands, c.MaxCommandBytes, func(rows *sql.Rows, raw []byte) error {
		var id, obj domain.ID
		var revision uint64
		var kind string
		var size int64
		var ignored []byte
		if err := rows.Scan(&id, &obj, &revision, &kind, &size, &ignored); err != nil {
			return err
		}
		cmd, err := captainDecode[captainCommand](raw)
		if err != nil {
			return err
		}
		if cmd.Version != c.Version || !c.ValidID(id, c.RequestIDKind) || cmd.ID != id || cmd.ObjectID != obj || cmd.Revision != revision || revision == 0 || revision > math.MaxInt64 || cmd.Kind != kind || cmd.Digest != commandDigest(cmd) || !c.ValidDigest(cmd.Digest) {
			return c.ErrCorrupt
		}
		previous, exists := a.latest[obj]
		if exists {
			if revision != previous.Revision+1 || cmd.PreviousDigest != previous.Digest {
				return c.ErrCorrupt
			}
		} else if revision != 1 || cmd.PreviousDigest != "" {
			return c.ErrCorrupt
		}
		parts := 0
		if cmd.Principal != nil {
			parts++
			p := *cmd.Principal
			if c.ValidatePrincipal(p) != nil || p.Ref != (c.Ref{ID: obj, Revision: revision}) || kind != "principal" && kind != "bootstrap" {
				return c.ErrCorrupt
			}
			a.principals[obj] = p
		}
		if cmd.Grant != nil {
			parts++
			g := *cmd.Grant
			if c.ValidateGrant(g) != nil || g.Ref != (c.Ref{ID: obj, Revision: revision}) || kind != "grant" {
				return c.ErrCorrupt
			}
			if exists {
				old := *previous.Grant
				old.Ref = g.Ref
				old.Active = g.Active
				if !captainSame(old, g) || !previous.Grant.Active && g.Active {
					return c.ErrCorrupt
				}
			}
			a.grants[obj] = g
		}
		if cmd.Assignment != nil {
			parts++
			v := *cmd.Assignment
			if c.ValidateAssignment(v) != nil || v.Ref != (c.Ref{ID: obj, Revision: revision}) || kind != "assignment" && kind != "control" {
				return c.ErrCorrupt
			}
			if exists {
				if previous.Assignment == nil {
					return c.ErrCorrupt
				}
				old := *previous.Assignment
				old.Ref = v.Ref
				old.Control = v.Control
				if !captainSame(old, v) || previous.Assignment.Control == c.Revoked && v.Control != c.Revoked {
					return c.ErrCorrupt
				}
			} else if kind != "assignment" || v.Control != c.Active {
				return c.ErrCorrupt
			}
			a.assignments[obj] = v
		}
		if parts != 1 || exists && (previous.Principal != nil) != (cmd.Principal != nil) {
			return c.ErrCorrupt
		}
		a.commands[id] = cmd
		a.latest[obj] = cmd
		a.commandBytes += uint64(len(raw))
		return nil
	})
	if err != nil {
		return nil, err
	}
	bootstrap := 0
	for _, cmd := range a.commands {
		if cmd.Kind == "bootstrap" {
			bootstrap++
			if cmd.Revision != 1 || cmd.Actor != cmd.ObjectID || cmd.ActorRevision != 1 || cmd.Principal == nil || !c.HasRole(*cmd.Principal, c.Owner) {
				return nil, c.ErrCorrupt
			}
			continue
		}
		owner, ok := a.principals[cmd.Actor]
		if !ok {
			return nil, c.ErrCorrupt
		}
		var historical *c.Principal
		for _, v := range a.commands {
			if v.ObjectID == owner.Ref.ID && v.Revision == cmd.ActorRevision {
				historical = v.Principal
				break
			}
		}
		if historical == nil || !c.HasRole(*historical, c.Owner) {
			return nil, c.ErrCorrupt
		}
		if cmd.Grant != nil {
			g := *cmd.Grant
			if g.OwnerPrincipal != cmd.Actor || a.principals[g.RecipientPrincipal].Ref.ID == "" {
				return nil, c.ErrCorrupt
			}
		}
		if cmd.Assignment != nil {
			v := *cmd.Assignment
			if v.OwnerPrincipal != cmd.Actor || a.principals[v.RunnerPrincipal].Ref.ID == "" {
				return nil, c.ErrCorrupt
			}
		}
	}
	if len(a.commands) > 0 && bootstrap != 1 {
		return nil, c.ErrCorrupt
	}
	if len(a.principals) > c.MaxPrincipals || len(a.grants) > c.MaxGrants || len(a.assignments) > c.MaxAssignments {
		return nil, c.ErrCapacity
	}
	if err = validateCaptainMirrors(ctx, q, a); err != nil {
		return nil, err
	}
	return a, nil
}

func validateCaptainMirrors(ctx context.Context, q captainQuery, a *captainAuthority) error {
	seen := map[domain.ID]bool{}
	err := captainRows(ctx, q, `SELECT id,revision,active,last_command,length(data),CASE WHEN length(data)<=524288 THEN data END FROM captain_principals LIMIT 65`, c.MaxPrincipals, c.MaxCommandBytes, func(rows *sql.Rows, raw []byte) error {
		var id, last domain.ID
		var rev uint64
		var active int
		var n int64
		var b []byte
		if err := rows.Scan(&id, &rev, &active, &last, &n, &b); err != nil {
			return err
		}
		p, e := captainDecode[c.Principal](raw)
		if e != nil {
			return e
		}
		if !captainSame(p, a.principals[id]) || p.Ref.Revision != rev || (active == 1) != p.Active || active < 0 || active > 1 || a.latest[id].ID != last {
			return c.ErrCorrupt
		}
		seen[id] = true
		return nil
	})
	if err != nil {
		return err
	}
	if len(seen) != len(a.principals) {
		return c.ErrCorrupt
	}
	seen = map[domain.ID]bool{}
	err = captainRows(ctx, q, `SELECT id,owner_id,recipient_id,session_id,revision,active,last_command,length(data),CASE WHEN length(data)<=524288 THEN data END FROM captain_read_grants LIMIT 257`, c.MaxGrants, c.MaxCommandBytes, func(rows *sql.Rows, raw []byte) error {
		var id, owner, recipient, sessionID, last domain.ID
		var rev uint64
		var active int
		var n int64
		var b []byte
		if err := rows.Scan(&id, &owner, &recipient, &sessionID, &rev, &active, &last, &n, &b); err != nil {
			return err
		}
		g, e := captainDecode[c.ReadGrant](raw)
		if e != nil {
			return e
		}
		if !captainSame(g, a.grants[id]) || g.OwnerPrincipal != owner || g.RecipientPrincipal != recipient || g.Source.Target.SessionID != sessionID || g.Ref.Revision != rev || (active == 1) != g.Active || active < 0 || active > 1 || a.latest[id].ID != last {
			return c.ErrCorrupt
		}
		seen[id] = true
		return nil
	})
	if err != nil {
		return err
	}
	if len(seen) != len(a.grants) {
		return c.ErrCorrupt
	}
	seen = map[domain.ID]bool{}
	err = captainRows(ctx, q, `SELECT id,owner_id,runner_id,session_id,revision,control,last_command,length(data),CASE WHEN length(data)<=524288 THEN data END FROM captain_assignments LIMIT 65`, c.MaxAssignments, c.MaxCommandBytes, func(rows *sql.Rows, raw []byte) error {
		var id, owner, runner, sessionID, last domain.ID
		var rev uint64
		var control string
		var n int64
		var b []byte
		if err := rows.Scan(&id, &owner, &runner, &sessionID, &rev, &control, &last, &n, &b); err != nil {
			return err
		}
		st, e := captainDecode[c.Status](raw)
		if e != nil {
			return e
		}
		v := st.Assignment
		if !captainSame(v, a.assignments[id]) || v.OwnerPrincipal != owner || v.RunnerPrincipal != runner || v.Captain.SessionID != sessionID || v.Ref.Revision != rev || string(v.Control) != control || a.latest[id].ID != last {
			return c.ErrCorrupt
		}
		seen[id] = true
		return nil
	})
	if err != nil {
		return err
	}
	if len(seen) != len(a.assignments) {
		return c.ErrCorrupt
	}
	return nil
}

func captainAuthenticate(a *captainAuthority, caller c.AuthCaller, role c.Role) error {
	p, ok := a.principals[caller.PrincipalID]
	if !ok || caller.Role != role || caller.RoleRevision != p.Ref.Revision || !c.HasRole(p, role) {
		return c.ErrDenied
	}
	return nil
}

func captainTarget(ctx context.Context, q captainQuery, t c.Target) error {
	if c.ValidateTarget(t) != nil {
		return c.ErrConflict
	}
	var raw []byte
	var thread domain.ID
	var n int64
	if err := q.QueryRowContext(ctx, `SELECT thread_id,length(data),CASE WHEN length(data)<=524288 THEN data END FROM sessions WHERE id=?`, t.SessionID).Scan(&thread, &n, &raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return c.ErrDenied
		}
		return err
	}
	if n <= 0 || n > c.MaxRecordBytes {
		return c.ErrCorrupt
	}
	v, err := captainDecode[domain.Session](raw)
	if err != nil {
		return err
	}
	if v.ID != t.SessionID || v.ThreadID != thread || v.HostID != t.HostID || v.ThreadID != t.ThreadID || v.Execution.ID != t.ExecutionID || v.Execution.Generation != t.Generation {
		return c.ErrDenied
	}
	return nil
}

func captainGrant(ctx context.Context, q captainQuery, a *captainAuthority, r c.Ref, recipient domain.ID) (c.ReadGrant, error) {
	g, ok := a.grants[r.ID]
	if !ok || g.Ref != r || !g.Active || g.RecipientPrincipal != recipient || !c.HasRole(a.principals[g.OwnerPrincipal], c.Owner) {
		return g, c.ErrDenied
	}
	if err := captainTarget(ctx, q, g.Source.Target); err != nil {
		return g, err
	}
	return g, nil
}

func captainScopes(ctx context.Context, q captainQuery, a *captainAuthority, v c.Assignment) ([]c.ReadGrant, error) {
	if !c.HasRole(a.principals[v.OwnerPrincipal], c.Owner) || !c.HasRole(a.principals[v.RunnerPrincipal], c.Runner) {
		return nil, c.ErrDenied
	}
	if err := captainTarget(ctx, q, v.Captain); err != nil {
		return nil, err
	}
	out := []c.ReadGrant{}
	seen := map[domain.ID]bool{}
	for _, r := range v.Grants {
		g, err := captainGrant(ctx, q, a, r, v.RunnerPrincipal)
		if err != nil {
			return nil, err
		}
		if g.Source.Target.HostID != v.Captain.HostID || g.Source.Target.SessionID == v.Captain.SessionID || g.Source.Target.ThreadID == v.Captain.ThreadID || seen[g.Source.Target.ThreadID] {
			return nil, c.ErrDenied
		}
		seen[g.Source.Target.ThreadID] = true
		out = append(out, g)
	}
	return out, nil
}

func captainAudience(ctx context.Context, q captainQuery, a *captainAuthority, v c.Assignment, inputs []c.ReadGrant, readerID domain.ID) error {
	for _, r := range v.Readers {
		if readerID != "" && r.PrincipalID != readerID {
			continue
		}
		if !c.HasRole(a.principals[r.PrincipalID], c.Reader) || len(r.Grants) != len(inputs) {
			return c.ErrDenied
		}
		seen := map[domain.ID]bool{}
		for _, ref := range r.Grants {
			g, err := captainGrant(ctx, q, a, ref, r.PrincipalID)
			if err != nil {
				return err
			}
			found := false
			for _, input := range inputs {
				if g.Source.Target == input.Source.Target && g.Source.FirstSequence <= input.Source.FirstSequence {
					found = true
					break
				}
			}
			if !found || seen[g.Source.Target.ThreadID] {
				return c.ErrDenied
			}
			seen[g.Source.Target.ThreadID] = true
		}
		if readerID != "" {
			return nil
		}
	}
	if readerID != "" {
		return c.ErrDenied
	}
	return nil
}

func captainRunner(ctx context.Context, q captainQuery, a *captainAuthority, caller c.AuthCaller, v c.Assignment) ([]c.ReadGrant, error) {
	if err := captainAuthenticate(a, caller, c.Runner); err != nil {
		return nil, err
	}
	if caller.PrincipalID != v.RunnerPrincipal || caller.RunnerTarget != v.Captain || v.Control != c.Active {
		return nil, c.ErrDenied
	}
	return captainScopes(ctx, q, a, v)
}
