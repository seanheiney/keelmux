package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/seanheiney/keelmux/internal/channels"
	"github.com/seanheiney/keelmux/internal/domain"
)

func validChannelBindingRow(b channels.Binding, id, sid, tid domain.ID, provider, endpoint, realm, channel, root string, active int) bool {
	n, err := channels.NormalizeBinding(b)
	return err == nil && bytes.Equal(encoded(n), encoded(b)) && b.ID == id && b.SessionID == sid && b.ThreadID == tid && string(b.Provider) == provider && b.Endpoint == endpoint && b.RealmID == realm && b.ChannelID == channel && b.RootID == root && (active == 0 || active == 1) && b.Active == (active == 1)
}

func channelBinding(ctx context.Context, q queryer, id domain.ID) (channels.Binding, error) {
	var b channels.Binding
	var sid, tid domain.ID
	var provider, endpoint, realm, channel, root string
	var active int
	var data []byte
	err := q.QueryRowContext(ctx, "SELECT session_id,thread_id,provider,endpoint,realm_id,channel_id,root_id,active,data FROM channel_bindings WHERE id=?", id).Scan(&sid, &tid, &provider, &endpoint, &realm, &channel, &root, &active, &data)
	if err != nil {
		return b, missing(err)
	}
	if json.Unmarshal(data, &b) != nil || !bytes.Equal(encoded(b), data) || !validChannelBindingRow(b, id, sid, tid, provider, endpoint, realm, channel, root, active) {
		return b, repair("invalid immutable channel binding")
	}
	return b, nil
}

func channelMessage(ctx context.Context, q queryer, id domain.ID) (channels.Message, error) {
	var m channels.Message
	var tid, bid domain.ID
	var seq int64
	var remote string
	var data []byte
	err := q.QueryRowContext(ctx, "SELECT thread_id,sequence,binding_id,remote_id,data FROM channel_messages WHERE id=?", id).Scan(&tid, &seq, &bid, &remote, &data)
	if err != nil {
		return m, missing(err)
	}
	if json.Unmarshal(data, &m) != nil || !bytes.Equal(encoded(m), data) || m.ID != id || m.ThreadID != tid || m.BindingID != bid || m.Sequence != seq || m.RemoteID != remote || !validChannelMessage(m) {
		return m, repair("invalid authored message metadata")
	}
	return m, nil
}

func validChannelMessage(m channels.Message) bool {
	return channels.ValidID(string(m.ID)) && channels.ValidID(string(m.ThreadID)) && channels.ValidID(string(m.BindingID)) && channels.ValidID(m.AuthorID) && channels.ValidText(m.Text) && m.Sequence > 0 && !m.CreatedAt.IsZero() && (m.RemoteID == "" || channels.ValidID(m.RemoteID)) && (m.RemoteID != "" || !m.Historical)
}

func channelPayloadHash(b channels.Binding, m channels.Message) [32]byte {
	return channels.PayloadHash(b, m)
}

func channelDelivery(ctx context.Context, q queryer, id domain.ID) (channels.Delivery, error) {
	var d channels.Delivery
	var bid domain.ID
	var state string
	var hash, data, proof []byte
	var evidence channels.Evidence
	err := q.QueryRowContext(ctx, "SELECT binding_id,state,payload_hash,data,proof FROM channel_deliveries WHERE id=?", id).Scan(&bid, &state, &hash, &data, &proof)
	if err != nil {
		return d, missing(err)
	}
	if json.Unmarshal(data, &d) != nil || !bytes.Equal(encoded(d), data) || json.Unmarshal(proof, &evidence) != nil || !bytes.Equal(encoded(evidence), proof) || d.ID != id || d.MessageID != id || d.BindingID != bid || d.State != channels.DeliveryState(state) || !bytes.Equal(hash, d.PayloadHash[:]) || !validChannelDelivery(d) {
		return d, repair("invalid durable channel delivery")
	}
	b, err := channelBinding(ctx, q, bid)
	if err != nil || !validChannelDeliveryProof(d, b, evidence) {
		return d, repair("delivery lost original outcome evidence")
	}
	return d, nil
}

func validChannelDeliveryProof(d channels.Delivery, b channels.Binding, e channels.Evidence) bool {
	empty := e == (channels.Evidence{})
	switch d.State {
	case channels.Queued, channels.Attempted:
		return empty
	case channels.Delivered:
		return e.Matches(b)
	case channels.Uncertain:
		return empty || e.Matches(b)
	case channels.Failed:
		return e.Matches(b) || (empty && (d.Code == "remote_rejected" || d.Code == "binding_disabled"))
	}
	return false
}

func validChannelDelivery(d channels.Delivery) bool {
	if !channels.ValidID(string(d.ID)) || d.ID != d.MessageID || !channels.ValidID(string(d.BindingID)) || d.UpdatedAt.IsZero() || !channels.ValidCode(d.Code) {
		return false
	}
	switch d.State {
	case channels.Queued, channels.Attempted:
		return d.RemoteID == "" && d.Code == ""
	case channels.Delivered:
		return channels.ValidID(d.RemoteID) && d.Code == ""
	case channels.Failed:
		return d.RemoteID == "" && (d.Code == "remote_rejected" || d.Code == "unsupported" || d.Code == "binding_disabled")
	case channels.Uncertain:
		return d.RemoteID == "" && (d.Code == "transport_uncertain" || d.Code == "unconfirmed" || d.Code == "recovered_uncertain")
	}
	return false
}

// BindChannel seeds an independent authored thread from the exact selected
// session. The trusted home caller owns network authentication and Probe.
func (s *Store) BindChannel(ctx context.Context, b channels.Binding, proof channels.Evidence) (channels.Binding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b.Active = true
	b, err := channels.NormalizeBinding(b)
	if err != nil || !proof.Matches(b) || len(b.Allowlist) == 0 {
		return channels.Binding{}, channels.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return b, err
	}
	defer tx.Rollback()
	if old, e := channelBinding(ctx, tx, b.ID); e == nil {
		if !bytes.Equal(encoded(old), encoded(b)) {
			return old, conflict("channel binding identity already names immutable configuration")
		}
		return old, nil
	} else if !errors.Is(e, ErrNotFound) {
		return b, e
	}
	v, err := session(ctx, tx, b.SessionID)
	if err != nil {
		return b, err
	}
	if v.ThreadID != b.ThreadID || v.WorkID != b.WorkID || v.HostID != b.HostID {
		return b, conflict("channel binding differs from exact seed session")
	}
	var work, host domain.ID
	err = tx.QueryRowContext(ctx, "SELECT work_id,host_id FROM chat_threads WHERE thread_id=?", b.ThreadID).Scan(&work, &host)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, "INSERT INTO chat_threads VALUES(?,?,?,1)", b.ThreadID, b.WorkID, b.HostID)
	} else if err == nil && (work != b.WorkID || host != b.HostID) {
		return b, conflict("authored thread belongs to another work or home")
	}
	if err != nil {
		return b, err
	}
	var existing domain.ID
	err = tx.QueryRowContext(ctx, "SELECT id FROM channel_bindings WHERE provider=? AND endpoint=? AND realm_id=? AND channel_id=? AND root_id=? AND active=1", b.Provider, b.Endpoint, b.RealmID, b.ChannelID, b.RootID).Scan(&existing)
	if err == nil {
		return b, conflict("external channel root already has an active binding")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return b, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO channel_bindings VALUES(?,?,?,?,?,?,?,?,?,?,?)", b.ID, b.SessionID, b.ThreadID, b.Provider, b.Endpoint, b.RealmID, b.ChannelID, b.RootID, 1, encoded(b), encoded(proof))
	if err != nil {
		return b, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO channel_cursors VALUES(?, '')", b.ID); err != nil {
		return b, err
	}
	return b, tx.Commit()
}

// Disable cancels only queued posts. Already admitted effects retain their
// original immutable route and may be reconciled, never requeued. The service
// holds its per-binding bounded IO gate around Disable and send admission.
func (s *Store) DisableChannel(ctx context.Context, id domain.ID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	b, err := channelBinding(ctx, tx, id)
	if err != nil || !b.Active {
		return err
	}
	b.Active = false
	if _, err = tx.ExecContext(ctx, "UPDATE channel_bindings SET active=0,data=? WHERE id=?", encoded(b), id); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id FROM channel_deliveries WHERE binding_id=? AND state='queued'", id)
	if err != nil {
		return err
	}
	var ids []domain.ID
	for rows.Next() {
		var id domain.ID
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err = closeChannelRows(rows, err); err != nil {
		return err
	}
	for _, id := range ids {
		d, e := channelDelivery(ctx, tx, id)
		if e != nil {
			return e
		}
		d.State, d.Code, d.UpdatedAt = channels.Failed, "binding_disabled", time.Now().UTC()
		if err = saveChannelDelivery(ctx, tx, d); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ChannelBindings(ctx context.Context) ([]channels.Binding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.QueryContext(ctx, "SELECT id FROM channel_bindings ORDER BY id")
	if err != nil {
		return nil, err
	}
	var ids []domain.ID
	for rows.Next() {
		var id domain.ID
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err = closeChannelRows(rows, err); err != nil {
		return nil, err
	}
	result := []channels.Binding{}
	for _, id := range ids {
		b, e := channelBinding(ctx, s.db, id)
		if e != nil {
			return nil, e
		}
		result = append(result, b)
	}
	return result, nil
}

func (s *Store) ChannelBinding(ctx context.Context, id domain.ID) (channels.Binding, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := channelBinding(ctx, s.db, id)
	if err != nil {
		return b, "", err
	}
	var cursor string
	if err = s.db.QueryRowContext(ctx, "SELECT value FROM channel_cursors WHERE binding_id=?", id).Scan(&cursor); err != nil || !channels.ValidCursor(cursor) {
		return b, "", repair("channel binding lost valid cursor")
	}
	return b, cursor, nil
}

func (s *Store) ChannelMessage(ctx context.Context, id domain.ID) (channels.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return channelMessage(ctx, s.db, id)
}

func (s *Store) ChannelMessages(ctx context.Context, thread domain.ID, after int64, limit int) ([]channels.Message, error) {
	if after < 0 || limit < 1 || limit > channels.MaxPageSize {
		return nil, channels.ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.QueryContext(ctx, "SELECT id FROM channel_messages WHERE thread_id=? AND sequence>? ORDER BY sequence LIMIT ?", thread, after, limit)
	if err != nil {
		return nil, err
	}
	ids := []domain.ID{}
	for rows.Next() {
		var id domain.ID
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err = closeChannelRows(rows, err); err != nil {
		return nil, err
	}
	result := []channels.Message{}
	for _, id := range ids {
		m, e := channelMessage(ctx, s.db, id)
		if e != nil {
			return nil, e
		}
		result = append(result, m)
	}
	return result, nil
}

func insertChannelMessage(ctx context.Context, tx *sql.Tx, m channels.Message) (channels.Message, error) {
	if err := tx.QueryRowContext(ctx, "SELECT next_sequence FROM chat_threads WHERE thread_id=?", m.ThreadID).Scan(&m.Sequence); err != nil {
		return m, missing(err)
	}
	if m.Sequence <= 0 || m.Sequence == int64(^uint64(0)>>1) {
		return m, repair("authored sequence exhausted or invalid")
	}
	if _, err := tx.ExecContext(ctx, "UPDATE chat_threads SET next_sequence=next_sequence+1 WHERE thread_id=?", m.ThreadID); err != nil {
		return m, err
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO channel_messages VALUES(?,?,?,?,?,?)", m.ID, m.ThreadID, m.Sequence, m.BindingID, m.RemoteID, encoded(m))
	return m, err
}

// The home authenticates the local AuthorID. A retry ID compares the exact
// authored content and explicit binding; it never creates fanout or a model turn.
func (s *Store) QueueChannelMessage(ctx context.Context, m channels.Message) (channels.Message, channels.Delivery, error) {
	if !channels.ValidID(string(m.ID)) || !channels.ValidID(string(m.BindingID)) || !channels.ValidID(m.AuthorID) || !channels.ValidText(m.Text) || m.RemoteID != "" || m.Historical {
		return m, channels.Delivery{}, channels.ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return m, channels.Delivery{}, err
	}
	defer tx.Rollback()
	b, err := channelBinding(ctx, tx, m.BindingID)
	if err != nil {
		return m, channels.Delivery{}, err
	}
	if m.ThreadID != "" && m.ThreadID != b.ThreadID {
		return m, channels.Delivery{}, conflict("message names another authored thread")
	}
	m.ThreadID = b.ThreadID
	if old, e := channelMessage(ctx, tx, m.ID); e == nil {
		if old.BindingID != m.BindingID || old.ThreadID != m.ThreadID || old.AuthorID != m.AuthorID || old.Text != m.Text || old.RemoteID != "" {
			return old, channels.Delivery{}, conflict("message ID already names different author content or binding")
		}
		d, e := channelDelivery(ctx, tx, old.ID)
		return old, d, e
	} else if !errors.Is(e, ErrNotFound) {
		return m, channels.Delivery{}, e
	}
	if !b.Active {
		return m, channels.Delivery{}, conflict("channel binding is disabled")
	}
	m.CreatedAt, m.Sequence = time.Now().UTC(), 0
	m, err = insertChannelMessage(ctx, tx, m)
	if err != nil {
		return m, channels.Delivery{}, err
	}
	d := channels.Delivery{ID: m.ID, MessageID: m.ID, BindingID: b.ID, State: channels.Queued, PayloadHash: channelPayloadHash(b, m), UpdatedAt: time.Now().UTC()}
	_, err = tx.ExecContext(ctx, "INSERT INTO channel_deliveries VALUES(?,?,?,?,?,?)", d.ID, d.BindingID, d.State, d.PayloadHash[:], encoded(d), encoded(channels.Evidence{}))
	if err != nil {
		return m, d, err
	}
	return m, d, tx.Commit()
}

func (s *Store) ChannelDelivery(ctx context.Context, id domain.ID) (channels.Delivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return channelDelivery(ctx, s.db, id)
}

func (s *Store) PendingChannelDeliveries(ctx context.Context, binding domain.ID, limit int) ([]channels.Delivery, error) {
	if limit < 1 || limit > channels.MaxPageSize {
		return nil, channels.ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.QueryContext(ctx, "SELECT d.id FROM channel_deliveries d JOIN channel_messages m ON m.id=d.id WHERE d.binding_id=? AND d.state='queued' ORDER BY m.sequence LIMIT ?", binding, limit)
	if err != nil {
		return nil, err
	}
	ids := []domain.ID{}
	for rows.Next() {
		var id domain.ID
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err = closeChannelRows(rows, err); err != nil {
		return nil, err
	}
	result := []channels.Delivery{}
	for _, id := range ids {
		d, e := channelDelivery(ctx, s.db, id)
		if e != nil {
			return nil, e
		}
		result = append(result, d)
	}
	return result, nil
}

func saveChannelDelivery(ctx context.Context, tx *sql.Tx, d channels.Delivery) error {
	_, err := tx.ExecContext(ctx, "UPDATE channel_deliveries SET state=?,data=? WHERE id=?", d.State, encoded(d), d.ID)
	return err
}

// claimed=true is the only permission for the service's one bounded Send.
// No SQL transaction covers that network IO; Disable is serialized by service.
func (s *Store) ClaimChannelDelivery(ctx context.Context, id domain.ID) (channels.Binding, channels.Message, channels.Delivery, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b channels.Binding
	var m channels.Message
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return b, m, channels.Delivery{}, false, err
	}
	defer tx.Rollback()
	d, err := channelDelivery(ctx, tx, id)
	if err != nil {
		return b, m, d, false, err
	}
	b, err = channelBinding(ctx, tx, d.BindingID)
	if err == nil {
		m, err = channelMessage(ctx, tx, d.MessageID)
	}
	if err != nil {
		return b, m, d, false, err
	}
	if m.BindingID != b.ID || m.ThreadID != b.ThreadID || m.RemoteID != "" || d.PayloadHash != channelPayloadHash(b, m) {
		return b, m, d, false, repair("delivery lost immutable authored target")
	}
	if d.State != channels.Queued {
		return b, m, d, false, nil
	}
	if !b.Active {
		return b, m, d, false, repair("inactive binding retains queued delivery")
	}
	d.State, d.UpdatedAt = channels.Attempted, time.Now().UTC()
	if err = saveChannelDelivery(ctx, tx, d); err != nil {
		return b, m, d, false, err
	}
	if err = tx.Commit(); err != nil {
		return b, m, d, false, err
	}
	return b, m, d, true, nil
}

// Finish is called with the trusted adapter outcome. Only exact scoped post
// acknowledgement resolves delivery; read-only reconciliation never resubmits.
func (s *Store) FinishChannelDelivery(ctx context.Context, id domain.ID, result channels.SendResult) (channels.Delivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return channels.Delivery{}, err
	}
	defer tx.Rollback()
	d, err := channelDelivery(ctx, tx, id)
	if err != nil {
		return d, err
	}
	b, err := channelBinding(ctx, tx, d.BindingID)
	if err != nil {
		return d, err
	}
	m, err := channelMessage(ctx, tx, d.MessageID)
	if err != nil || m.BindingID != b.ID || m.ThreadID != b.ThreadID || m.RemoteID != "" || d.PayloadHash != channelPayloadHash(b, m) {
		return d, repair("post outcome lost original authored target")
	}
	reject := func() (channels.Delivery, error) {
		if d.State == channels.Attempted || d.State == channels.Uncertain {
			d.State, d.RemoteID, d.Code, d.UpdatedAt = channels.Uncertain, "", "unconfirmed", time.Now().UTC()
			if e := saveChannelDelivery(ctx, tx, d); e != nil {
				return d, e
			}
			if _, e := tx.ExecContext(ctx, "UPDATE channel_deliveries SET proof=? WHERE id=?", encoded(channels.Evidence{}), id); e != nil {
				return d, e
			}
			if e := tx.Commit(); e != nil {
				return d, e
			}
		}
		return d, conflict("post outcome lacks exact original target evidence")
	}
	emptyProof := result.Evidence == (channels.Evidence{})
	if !channels.ValidCode(result.Code) {
		return reject()
	}
	switch result.State {
	case channels.Delivered:
		if !channels.ValidID(result.RemoteID) || result.Code != "" || !result.Evidence.Matches(b) {
			return reject()
		}
	case channels.Failed:
		// The trusted adapter may explicitly prove a preflight rejection with
		// no POST invoked; no authenticated network proof is invented for it.
		if result.RemoteID != "" || (result.Code != "remote_rejected" && result.Code != "unsupported") || (!result.Evidence.Matches(b) && !(emptyProof && result.Code == "remote_rejected")) {
			return reject()
		}
	case channels.Uncertain:
		if result.RemoteID != "" || (!emptyProof && !result.Evidence.Matches(b)) || (result.Code != "" && result.Code != "transport_uncertain" && result.Code != "unconfirmed") {
			return reject()
		}
		if result.Code == "" {
			result.Code = "transport_uncertain"
		}
	default:
		return reject()
	}
	if d.State == channels.Delivered || d.State == channels.Failed {
		if d.State != result.State || d.RemoteID != result.RemoteID || d.Code != result.Code {
			return d, conflict("terminal post receipt cannot be replaced")
		}
		return d, nil
	}
	if d.State != channels.Attempted && d.State != channels.Uncertain {
		return d, conflict("post outcome requires the original durable attempt")
	}
	d.State, d.RemoteID, d.Code, d.UpdatedAt = result.State, result.RemoteID, result.Code, time.Now().UTC()
	if err = saveChannelDelivery(ctx, tx, d); err != nil {
		return d, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE channel_deliveries SET proof=? WHERE id=?", encoded(result.Evidence), id); err != nil {
		return d, err
	}
	return d, tx.Commit()
}

func allowedChannelActor(b channels.Binding, actor string) bool {
	n := sort.SearchStrings(b.Allowlist, actor)
	return n < len(b.Allowlist) && b.Allowlist[n] == actor
}

func channelSourceHash(m channels.RemoteMessage) [32]byte {
	// History/live transport provenance can differ for the same authored post.
	m.Historical = false
	m.CreatedAt = m.CreatedAt.UTC()
	return sha256.Sum256(encoded(m))
}

func channelSourceIdentityHash(m channels.RemoteMessage) [32]byte {
	m.Text, m.Historical = "", false
	m.CreatedAt = m.CreatedAt.UTC()
	return sha256.Sum256(encoded(m))
}

func remoteFromMessage(b channels.Binding, m channels.Message) channels.RemoteMessage {
	return channels.RemoteMessage{ID: m.RemoteID, ActorID: m.AuthorID, RealmID: b.RealmID, ChannelID: b.ChannelID, RootID: b.RootID, Text: m.Text, CreatedAt: m.CreatedAt, Historical: m.Historical}
}

// One page commits source dedup, authored notes and cursor CAS together. A
// replay, bot echo or imported command-like text can never cause model work.
func (s *Store) ImportChannelMessages(ctx context.Context, id domain.ID, expectedCursor string, result channels.FetchResult) (int, error) {
	if !channels.ValidCursor(expectedCursor) || !channels.ValidCursor(result.NextCursor) || len(result.Messages) > channels.MaxPageSize || (result.HasMore && result.NextCursor == expectedCursor) || (expectedCursor != "" && result.NextCursor == "") {
		return 0, channels.ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	b, err := channelBinding(ctx, tx, id)
	if err != nil {
		return 0, err
	}
	if !b.Active || !result.Evidence.Matches(b) {
		return 0, conflict("channel intake requires active exact authenticated binding")
	}
	var current string
	if err = tx.QueryRowContext(ctx, "SELECT value FROM channel_cursors WHERE binding_id=?", id).Scan(&current); err != nil {
		return 0, repair("channel binding lost intake cursor")
	}
	if current != expectedCursor {
		return 0, conflict("channel cursor changed before import")
	}
	added := 0
	for _, remote := range result.Messages {
		if !remote.Matches(b) {
			return 0, conflict("remote message differs from exact channel root or schema")
		}
		hash, identityHash := channelSourceHash(remote), channelSourceIdentityHash(remote)
		var previousIdentity []byte
		err = tx.QueryRowContext(ctx, "SELECT identity_hash FROM channel_inbox WHERE provider=? AND endpoint=? AND realm_id=? AND channel_id=? AND source_id=?", b.Provider, b.Endpoint, b.RealmID, b.ChannelID, remote.ID).Scan(&previousIdentity)
		if err == nil {
			if !bytes.Equal(previousIdentity, identityHash[:]) {
				return 0, conflict("external source ID names different immutable author or root")
			}
			// Initial authored notes preserve the first snapshot. Changed text
			// is not an edit projection, delivery, action or new message.
			continue // Includes another old binding; never retarget a source post.
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		disposition := "accepted"
		switch {
		case remote.ActorID == b.SelfID:
			disposition = "self"
		case !allowedChannelActor(b, remote.ActorID):
			disposition = "unallowlisted"
		case !remote.CreatedAt.After(b.ActivationCutoff):
			disposition = "before_cutoff"
		}
		var messageID any
		if disposition == "accepted" {
			m := channels.Message{ID: domain.NewID("message"), ThreadID: b.ThreadID, BindingID: b.ID, AuthorID: remote.ActorID, Text: remote.Text, CreatedAt: remote.CreatedAt.UTC(), RemoteID: remote.ID, Historical: remote.Historical}
			m, err = insertChannelMessage(ctx, tx, m)
			if err != nil {
				return 0, err
			}
			messageID = m.ID
			added++
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO channel_inbox VALUES(?,?,?,?,?,?,?,?,?,?)", b.Provider, b.Endpoint, b.RealmID, b.ChannelID, remote.ID, b.ID, hash[:], identityHash[:], messageID, disposition); err != nil {
			return 0, err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE channel_cursors SET value=? WHERE binding_id=?", result.NextCursor, id); err != nil {
		return 0, err
	}
	return added, tx.Commit()
}

// Startup recovers unknown attempts, without making them eligible for Send.
func (s *Store) RecoverChannelDeliveries(ctx context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT id FROM channel_deliveries WHERE state='attempted'")
	if err != nil {
		return 0, err
	}
	ids := []domain.ID{}
	for rows.Next() {
		var id domain.ID
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err = closeChannelRows(rows, err); err != nil {
		return 0, err
	}
	for _, id := range ids {
		d, e := channelDelivery(ctx, tx, id)
		if e != nil {
			return 0, e
		}
		d.State, d.Code, d.UpdatedAt = channels.Uncertain, "recovered_uncertain", time.Now().UTC()
		if err = saveChannelDelivery(ctx, tx, d); err != nil {
			return 0, err
		}
	}
	return len(ids), tx.Commit()
}
