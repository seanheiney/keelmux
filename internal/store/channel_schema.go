package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/seanheiney/keelmux/internal/channels"
	"github.com/seanheiney/keelmux/internal/domain"
)

var channelDefinitions = []struct{ name, kind, sql string }{
	{"chat_threads", "table", `CREATE TABLE chat_threads (thread_id TEXT PRIMARY KEY, work_id TEXT NOT NULL, host_id TEXT NOT NULL, next_sequence INTEGER NOT NULL CHECK(next_sequence>0))`},
	{"channel_bindings", "table", `CREATE TABLE channel_bindings (id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id), thread_id TEXT NOT NULL REFERENCES chat_threads(thread_id), provider TEXT NOT NULL, endpoint TEXT NOT NULL, realm_id TEXT NOT NULL, channel_id TEXT NOT NULL, root_id TEXT NOT NULL, active INTEGER NOT NULL CHECK(active IN (0,1)), data BLOB NOT NULL, proof BLOB NOT NULL)`},
	{"channel_active_endpoint", "index", `CREATE UNIQUE INDEX channel_active_endpoint ON channel_bindings(provider,endpoint,realm_id,channel_id,root_id) WHERE active=1`},
	{"channel_messages", "table", `CREATE TABLE channel_messages (id TEXT PRIMARY KEY, thread_id TEXT NOT NULL REFERENCES chat_threads(thread_id), sequence INTEGER NOT NULL CHECK(sequence>0), binding_id TEXT NOT NULL REFERENCES channel_bindings(id), remote_id TEXT NOT NULL, data BLOB NOT NULL, UNIQUE(thread_id,sequence))`},
	{"channel_deliveries", "table", `CREATE TABLE channel_deliveries (id TEXT PRIMARY KEY REFERENCES channel_messages(id), binding_id TEXT NOT NULL REFERENCES channel_bindings(id), state TEXT NOT NULL CHECK(state IN ('queued','attempted','delivered','failed','uncertain')), payload_hash BLOB NOT NULL CHECK(length(payload_hash)=32), data BLOB NOT NULL, proof BLOB NOT NULL)`},
	{"channel_inbox", "table", `CREATE TABLE channel_inbox (provider TEXT NOT NULL, endpoint TEXT NOT NULL, realm_id TEXT NOT NULL, channel_id TEXT NOT NULL, source_id TEXT NOT NULL, binding_id TEXT NOT NULL REFERENCES channel_bindings(id), source_hash BLOB NOT NULL CHECK(length(source_hash)=32), identity_hash BLOB NOT NULL CHECK(length(identity_hash)=32), message_id TEXT REFERENCES channel_messages(id), disposition TEXT NOT NULL CHECK(disposition IN ('accepted','self','unallowlisted','before_cutoff')), PRIMARY KEY(provider,endpoint,realm_id,channel_id,source_id))`},
	{"channel_cursors", "table", `CREATE TABLE channel_cursors (binding_id TEXT PRIMARY KEY REFERENCES channel_bindings(id), value TEXT NOT NULL)`},
}

// Only a migration invokes this helper. Preexisting objects are not adopted or
// repaired; an established v3 database is validated without recreating tables.
func createChannelSchema(ctx context.Context, tx *sql.Tx) error {
	for _, d := range channelDefinitions {
		if _, err := tx.ExecContext(ctx, d.sql); err != nil {
			return repair("channel schema creation refused")
		}
	}
	return nil
}

func normalizedChannelSQL(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), ""))
}

// Validate both schema and mirrored durable metadata. Missing receipts, source
// dedup, immutable routes or claims require repair; never reconstruct authority.
func validateChannelSchema(ctx context.Context, db *sql.DB) error {
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	for _, d := range channelDefinitions {
		if version == 4 && d.name == "channel_bindings" {
			d.sql = strings.TrimSuffix(d.sql, ")") + ", recipient_id TEXT NOT NULL DEFAULT '')"
		}
		if version == 4 && d.name == "channel_active_endpoint" {
			d.sql = `CREATE UNIQUE INDEX channel_active_endpoint ON channel_bindings(provider,endpoint,realm_id,channel_id,root_id,recipient_id) WHERE active=1`
		}
		var kind, definition string
		if err := db.QueryRowContext(ctx, "SELECT type,sql FROM sqlite_master WHERE name=?", d.name).Scan(&kind, &definition); err != nil || kind != d.kind || normalizedChannelSQL(definition) != normalizedChannelSQL(d.sql) {
			return repair("missing or changed channel schema " + d.name)
		}
	}
	bindings := map[domain.ID]channels.Binding{}
	recipientColumn := "''"
	if version == 4 {
		recipientColumn = "recipient_id"
	}
	rows, err := db.QueryContext(ctx, "SELECT id,session_id,thread_id,provider,endpoint,realm_id,channel_id,root_id,active,data,proof,"+recipientColumn+" FROM channel_bindings")
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, sessionID, threadID domain.ID
		var provider, endpoint, realm, channel, root, recipient string
		var active int
		var data, proof []byte
		var b channels.Binding
		var e channels.Evidence
		if err = rows.Scan(&id, &sessionID, &threadID, &provider, &endpoint, &realm, &channel, &root, &active, &data, &proof, &recipient); err != nil {
			break
		}
		if json.Unmarshal(data, &b) != nil || !bytes.Equal(encoded(b), data) || json.Unmarshal(proof, &e) != nil || !bytes.Equal(encoded(e), proof) || !validChannelBindingRow(b, id, sessionID, threadID, provider, endpoint, realm, channel, root, active, recipient) || !e.Matches(b) || len(b.Allowlist) == 0 {
			err = repair("invalid immutable channel binding")
			break
		}
		bindings[id] = b
	}
	if err = closeChannelRows(rows, err); err != nil {
		return err
	}
	type thread struct {
		work, host domain.ID
		next       int64
	}
	threads := map[domain.ID]thread{}
	rows, err = db.QueryContext(ctx, "SELECT thread_id,work_id,host_id,next_sequence FROM chat_threads")
	if err != nil {
		return err
	}
	for rows.Next() {
		var id domain.ID
		var t thread
		if err = rows.Scan(&id, &t.work, &t.host, &t.next); err != nil {
			break
		}
		if !channels.ValidID(string(id)) || !channels.ValidID(string(t.work)) || !channels.ValidID(string(t.host)) || t.next <= 0 {
			err = repair("invalid authored thread identity")
			break
		}
		threads[id] = t
	}
	if err = closeChannelRows(rows, err); err != nil {
		return err
	}
	for _, b := range bindings {
		t, ok := threads[b.ThreadID]
		if !ok || t.host != b.HostID || t.work != b.WorkID {
			return repair("channel thread provenance mismatch")
		}
		v, e := session(ctx, db, b.SessionID)
		if e != nil || v.HostID != b.HostID || v.WorkID != b.WorkID || v.ThreadID != b.ThreadID {
			return repair("channel seed session provenance mismatch")
		}
		if b.IRCAddressed {
			var projectID, seed domain.ID
			p, e := project(ctx, db, b.ProjectID)
			if e != nil || p.HostID != v.HostID || p.Workspace != v.Workspace || db.QueryRowContext(ctx, "SELECT project_id,session_id FROM project_threads WHERE thread_id=?", b.ThreadID).Scan(&projectID, &seed) != nil || projectID != b.ProjectID || seed != b.SessionID {
				return repair("addressed IRC binding lost project membership")
			}
		}
	}
	messages := map[domain.ID]channels.Message{}
	maxSeq := map[domain.ID]int64{}
	countSeq := map[domain.ID]int64{}
	rows, err = db.QueryContext(ctx, "SELECT id,thread_id,sequence,binding_id,remote_id,data FROM channel_messages")
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, tid, bid domain.ID
		var seq int64
		var remote string
		var data []byte
		var m channels.Message
		if err = rows.Scan(&id, &tid, &seq, &bid, &remote, &data); err != nil {
			break
		}
		b, ok := bindings[bid]
		if json.Unmarshal(data, &m) != nil || !bytes.Equal(encoded(m), data) || !ok || m.ID != id || m.ThreadID != tid || m.BindingID != bid || m.Sequence != seq || seq <= 0 || m.RemoteID != remote || m.ThreadID != b.ThreadID || !validChannelMessage(m) {
			err = repair("invalid authored message metadata")
			break
		}
		messages[id] = m
		countSeq[tid]++
		if seq > maxSeq[tid] {
			maxSeq[tid] = seq
		}
	}
	if err = closeChannelRows(rows, err); err != nil {
		return err
	}
	for id, t := range threads {
		if t.next != maxSeq[id]+1 || countSeq[id] != maxSeq[id] {
			return repair("authored thread sequence mismatch")
		}
	}
	deliveries := map[domain.ID]channels.Delivery{}
	rows, err = db.QueryContext(ctx, "SELECT id,binding_id,state,payload_hash,data,proof FROM channel_deliveries")
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, bid domain.ID
		var state string
		var hash, data, proof []byte
		var d channels.Delivery
		var e channels.Evidence
		if err = rows.Scan(&id, &bid, &state, &hash, &data, &proof); err != nil {
			break
		}
		m, mok := messages[id]
		b, bok := bindings[bid]
		if json.Unmarshal(data, &d) != nil || !bytes.Equal(encoded(d), data) || json.Unmarshal(proof, &e) != nil || !bytes.Equal(encoded(e), proof) || !mok || !bok || m.RemoteID != "" || d.ID != id || d.MessageID != id || d.BindingID != bid || m.BindingID != bid || d.State != channels.DeliveryState(state) || !bytes.Equal(hash, d.PayloadHash[:]) || d.PayloadHash != channelPayloadHash(b, m) || !validChannelDelivery(d) || !validChannelDeliveryProof(d, b, e) || (d.State == channels.Queued && !b.Active) || (d.Code == "binding_disabled" && b.Active) {
			err = repair("invalid durable channel delivery")
			break
		}
		deliveries[id] = d
	}
	if err = closeChannelRows(rows, err); err != nil {
		return err
	}
	accepted := map[domain.ID]bool{}
	rows, err = db.QueryContext(ctx, "SELECT provider,endpoint,realm_id,channel_id,source_id,binding_id,source_hash,identity_hash,message_id,disposition FROM channel_inbox")
	if err != nil {
		return err
	}
	for rows.Next() {
		var provider, endpoint, realm, channel, source, disposition string
		var bid domain.ID
		var mid sql.NullString
		var hash, identityHash []byte
		if err = rows.Scan(&provider, &endpoint, &realm, &channel, &source, &bid, &hash, &identityHash, &mid, &disposition); err != nil {
			break
		}
		b, ok := bindings[bid]
		if !ok || provider != string(b.Provider) || endpoint != b.Endpoint || realm != b.RealmID || channel != b.ChannelID || !channels.ValidID(source) || len(hash) != 32 || len(identityHash) != 32 || ((disposition == "accepted") != mid.Valid) {
			err = repair("invalid channel source dedup metadata")
			break
		}
		if mid.Valid {
			m, ok := messages[domain.ID(mid.String)]
			r := remoteFromMessage(b, m)
			h := channelSourceHash(r)
			ih := channelSourceIdentityHash(r)
			if !ok || m.BindingID != bid || m.RemoteID != source || m.AuthorID == b.SelfID || !allowedChannelActor(b, m.AuthorID) || !m.CreatedAt.After(b.ActivationCutoff) || !bytes.Equal(hash, h[:]) || !bytes.Equal(identityHash, ih[:]) {
				err = repair("authored remote message lost original source")
				break
			}
			accepted[m.ID] = true
		}
	}
	if err = closeChannelRows(rows, err); err != nil {
		return err
	}
	for id, m := range messages {
		if (m.RemoteID == "" && deliveries[id].ID != id) || (m.RemoteID != "" && !accepted[id]) {
			return repair("authored message lost delivery or source receipt")
		}
	}
	seenCursor := map[domain.ID]bool{}
	rows, err = db.QueryContext(ctx, "SELECT binding_id,value FROM channel_cursors")
	if err != nil {
		return err
	}
	for rows.Next() {
		var id domain.ID
		var value string
		if err = rows.Scan(&id, &value); err != nil {
			break
		}
		if _, ok := bindings[id]; !ok || !channels.ValidCursor(value) {
			err = repair("invalid channel cursor")
			break
		}
		seenCursor[id] = true
	}
	if err = closeChannelRows(rows, err); err != nil {
		return err
	}
	for id := range bindings {
		if !seenCursor[id] {
			return repair("channel binding lost intake cursor")
		}
	}
	return nil
}

func closeChannelRows(rows *sql.Rows, err error) error {
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	return err
}
