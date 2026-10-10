package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	cb "github.com/seanheiney/keelmux/internal/codexbroker"
	"github.com/seanheiney/keelmux/internal/domain"
)

func repair(detail string) error {
	return fmt.Errorf("store schema/authority repair required: %s", detail)
}

// An established v2 database must not silently recreate lost broker metadata.
// That would turn a reserved broker lease into a legacy native-only grant.
func validateCodexSchema(ctx context.Context, db *sql.DB) error {
	tables := map[string][]string{
		"sessions":          {"id", "thread_id", "data"},
		"operations":        {"id", "session_id", "generation", "data", "payload"},
		"events":            {"thread_id", "sequence", "data"},
		"grants":            {"operation_id", "session_id", "execution_id", "read_only", "active", "data"},
		"codex_agents":      {"session_id", "execution_id", "data"},
		"codex_epochs":      {"session_id", "value"},
		"codex_grants":      {"operation_id", "data", "agent"},
		"codex_connections": {"id", "data", "retired"},
		"codex_receipts":    {"operation_id", "effect_class", "data"},
		"codex_callbacks":   {"id", "data"},
	}
	keys := map[string][]string{
		"sessions": {"id"}, "operations": {"id"}, "events": {"thread_id", "sequence"},
		"grants": {"operation_id"}, "codex_agents": {"session_id", "execution_id"},
		"codex_epochs": {"session_id"}, "codex_grants": {"operation_id"},
		"codex_connections": {"id"}, "codex_receipts": {"operation_id"}, "codex_callbacks": {"id"},
	}
	for table, columns := range tables {
		var kind string
		if err := db.QueryRowContext(ctx, "SELECT type FROM sqlite_master WHERE name=?", table).Scan(&kind); err != nil || kind != "table" {
			return repair("missing table " + table)
		}
		rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
		if err != nil {
			return err
		}
		type column struct {
			kind             string
			notnull, primary int
		}
		found := map[string]column{}
		for rows.Next() {
			var cid, notnull, pk int
			var name, ctype string
			var defaultValue any
			if err = rows.Scan(&cid, &name, &ctype, &notnull, &defaultValue, &pk); err != nil {
				rows.Close()
				return err
			}
			found[name] = column{strings.ToUpper(ctype), notnull, pk}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, column := range columns {
			c, ok := found[column]
			expectedType := "TEXT"
			switch column {
			case "data", "agent", "payload":
				expectedType = "BLOB"
			case "generation", "sequence", "read_only", "active", "value", "retired":
				expectedType = "INTEGER"
			}
			expectedPK := 0
			for n, key := range keys[table] {
				if key == column {
					expectedPK = n + 1
				}
			}
			expectedNotNull := 1
			if expectedPK != 0 && len(keys[table]) == 1 {
				expectedNotNull = 0
			}
			if !ok || c.kind != expectedType || c.primary != expectedPK || c.notnull != expectedNotNull {
				return repair("missing or changed column " + table + "." + column)
			}
		}
	}
	var definition string
	if err := db.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='index' AND name='one_writer' AND tbl_name='grants'").Scan(&definition); err != nil {
		return repair("missing writer constraint")
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(definition), ""))
	normalized = strings.ReplaceAll(normalized, "ifnotexists", "")
	if normalized != "createuniqueindexone_writerongrants(session_id,execution_id)whereactive=1andread_only=0" {
		return repair("changed writer constraint")
	}
	rows, err := db.QueryContext(ctx, `SELECT g.operation_id,g.data,g.read_only,g.active,o.data,c.data,c.agent,a.data,e.value
FROM grants g JOIN operations o ON o.id=g.operation_id
LEFT JOIN codex_grants c ON c.operation_id=g.operation_id
LEFT JOIN codex_agents a ON a.session_id=g.session_id AND a.execution_id=g.execution_id
LEFT JOIN codex_epochs e ON e.session_id=g.session_id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id domain.ID
		var base, opBytes, meta, agent, registered []byte
		var ro bool
		var active int
		var epoch sql.NullInt64
		if err = rows.Scan(&id, &base, &ro, &active, &opBytes, &meta, &agent, &registered, &epoch); err != nil {
			rows.Close()
			return err
		}
		var op domain.Operation
		var g Grant
		if json.Unmarshal(opBytes, &op) != nil || json.Unmarshal(base, &g) != nil {
			rows.Close()
			return repair("invalid durable grant/operation")
		}
		broker := op.Kind == "codex.acquire" || op.Kind == "codex.handover"
		if !broker && len(meta) == 0 {
			continue
		}
		if !broker || len(meta) == 0 || len(agent) == 0 || !bytes.Equal(agent, registered) {
			rows.Close()
			return repair("broker grant missing original authority metadata")
		}
		var view cb.Grant
		var a cb.AgentBinding
		if json.Unmarshal(meta, &view) != nil || json.Unmarshal(agent, &a) != nil || view.Lease != g.Lease || view.ReadOnly != ro || view.Lease.OperationID != id || view.PrincipalID == "" || view.Epoch == 0 ||
			(active != 0 && active != 1) || view.Active != (active == 1) || (view.Admission == cb.Retired) != !view.Active ||
			view.Lease.Token == "" || view.Lease.ClientID == "" || view.Lease.SessionID != op.SessionID || view.Lease.ExecutionID != op.ExecutionID || op.ID != id ||
			(view.Admission != cb.Admitting && view.Admission != cb.Fenced && view.Admission != cb.Retired) ||
			(view.Kind != cb.NativeWriter && view.Kind != cb.SemanticWriter) || a.SessionID != g.Lease.SessionID || a.ExecutionID != g.Lease.ExecutionID || a.Generation != g.Lease.Generation ||
			!epoch.Valid || epoch.Int64 < 0 || uint64(epoch.Int64) < view.Epoch {
			rows.Close()
			return repair("broker grant authority metadata inconsistent")
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// Metadata must not refer to absent master grants or operations.
	var orphans int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM codex_grants c LEFT JOIN grants g ON g.operation_id=c.operation_id WHERE g.operation_id IS NULL`).Scan(&orphans); err != nil {
		return err
	}
	if orphans != 0 {
		return repair("orphan broker grant metadata")
	}
	// RPC intent without its receipt would erase an attempted/unknown effect.
	rows, err = db.QueryContext(ctx, `SELECT o.data,r.data,o.payload FROM operations o LEFT JOIN codex_receipts r ON r.operation_id=o.id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var opBytes, receiptBytes, request []byte
		if err = rows.Scan(&opBytes, &receiptBytes, &request); err != nil {
			return err
		}
		var op domain.Operation
		if err = json.Unmarshal(opBytes, &op); err != nil {
			return repair("invalid operation metadata")
		}
		if op.Kind != "codex.rpc" {
			continue
		}
		var r cb.Receipt
		if len(receiptBytes) == 0 || json.Unmarshal(receiptBytes, &r) != nil || r.Intent.OperationID != op.ID || !bytes.Equal(encoded(r.Intent), request) {
			return repair("broker RPC missing immutable attempted receipt")
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// Callback request IDs can be absent only in durable, non-actionable
	// quarantine. JSON null is the stored representation of an absent raw ID.
	rows, err = db.QueryContext(ctx, `SELECT data FROM codex_callbacks`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var b []byte
		var c cb.CallbackRecord
		if err = rows.Scan(&b); err != nil {
			rows.Close()
			return err
		}
		if json.Unmarshal(b, &c) != nil || !validCallbackQuarantine(c) {
			rows.Close()
			return repair("callback without native request ID is not quarantined")
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	return nil
}
