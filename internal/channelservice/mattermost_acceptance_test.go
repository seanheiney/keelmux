package channelservice

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/channels"
	"github.com/seanheiney/keelmux/internal/chatadapter"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
	"github.com/seanheiney/keelmux/internal/store"
)

type mmAcceptanceFixture struct {
	Schema               int    `json:"schema"`
	Endpoint, CA, State  string // local 0600 fixture only
	Team, Channel, Root  string
	OtherRoot            string `json:"other_root"`
	Bot, Human, Outsider string
	TokenPaths           map[string]string `json:"token_paths"`
}

type mmAcceptancePost struct {
	ID, UserID, ChannelID, RootID, Message string
	Props                                  map[string]string
}

func (p *mmAcceptancePost) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID        string            `json:"id"`
		UserID    string            `json:"user_id"`
		ChannelID string            `json:"channel_id"`
		RootID    string            `json:"root_id"`
		Message   string            `json:"message"`
		Props     map[string]string `json:"props"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	p.ID, p.UserID, p.ChannelID, p.RootID, p.Message, p.Props = raw.ID, raw.UserID, raw.ChannelID, raw.RootID, raw.Message, raw.Props
	return nil
}

// An actual verified server response is consumed, then lost before the adapter
// sees it. The counter measures real product POST calls, not fixture setup.
type mmAcceptanceTransport struct {
	base            *http.Transport
	mu              sync.Mutex
	posts, verified int
	dropID          domain.ID
	dropped         mmAcceptancePost
	drops           int
}

func (w *mmAcceptanceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	w.mu.Lock()
	drop := false
	if req.Method == http.MethodPost && req.URL.Path == "/api/v4/posts" {
		w.posts++
		if req.GetBody != nil {
			body, err := req.GetBody()
			if err == nil {
				var posted struct {
					Props map[string]string `json:"props"`
				}
				_ = json.NewDecoder(body).Decode(&posted)
				_ = body.Close()
				drop = w.dropID != "" && posted.Props["keelmux_delivery_id"] == string(w.dropID)
			}
		}
	}
	w.mu.Unlock()
	response, err := w.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	w.mu.Lock()
	if response.TLS != nil && len(response.TLS.VerifiedChains) > 0 {
		w.verified++
	}
	w.mu.Unlock()
	if !drop {
		return response, nil
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	_ = response.Body.Close()
	var p mmAcceptancePost
	if err != nil || len(data) > 1<<20 || response.StatusCode != http.StatusCreated || json.Unmarshal(data, &p) != nil || p.ID == "" {
		return nil, errors.New("owned loss fixture did not obtain actual post acknowledgement")
	}
	w.mu.Lock()
	w.dropID, w.dropped = "", p
	w.drops++
	w.mu.Unlock()
	return nil, errors.New("owned test discarded an actual server acknowledgement")
}

func (w *mmAcceptanceTransport) counters() (int, int, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.posts, w.verified, w.drops
}

// Opt-in only: the Python owner creates all Docker resources, TLS material and
// accounts. The normal Go suite never contacts a provider.
func TestMattermostOwnedAcceptance(t *testing.T) {
	fixturePath := os.Getenv("KEELMUX_MM_ACCEPTANCE_FIXTURE")
	if fixturePath == "" {
		t.Skip("requires scripts/mattermost-acceptance.py owned fixture")
	}
	var fixture mmAcceptanceFixture
	data, err := os.ReadFile(fixturePath)
	if err != nil || json.Unmarshal(data, &fixture) != nil || fixture.Schema != 1 {
		t.Fatal("owned_fixture_invalid")
	}
	// Fixture endpoints must be loopback HTTPS; this test cannot be pointed at a
	// customer server through an ordinary environment override.
	u, err := url.Parse(fixture.Endpoint)
	if err != nil || u.Scheme != "https" || !net.ParseIP(u.Hostname()).IsLoopback() || u.Path != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		t.Fatal("owned_endpoint_invalid")
	}
	info, err := os.Stat(fixturePath)
	if err != nil || info.Mode().Perm() != 0600 || filepath.Dir(fixturePath) != fixture.State {
		t.Fatal("owned_fixture_permissions_invalid")
	}
	stateInfo, err := os.Stat(fixture.State)
	if err != nil || stateInfo.Mode().Perm() != 0700 {
		t.Fatal("owned_state_permissions_invalid")
	}
	ca, err := os.ReadFile(fixture.CA)
	pool := x509.NewCertPool()
	if err != nil || !pool.AppendCertsFromPEM(ca) {
		t.Fatal("owned_ca_invalid")
	}
	base := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, Proxy: nil}
	defer base.CloseIdleConnections()
	wire := &mmAcceptanceTransport{base: base}
	client := &http.Client{Transport: wire, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	// Setup requests use an independent trusted client and do not inflate the
	// adapter's real POST counter.
	adminClient := &http.Client{Transport: base, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	tokens := map[string]string{}
	for _, who := range []string{"admin", "human", "outsider", "bot"} {
		secret, e := os.ReadFile(fixture.TokenPaths[who])
		if e != nil || len(secret) == 0 || filepath.Dir(fixture.TokenPaths[who]) != fixture.State {
			t.Fatal("owned_secret_invalid")
		}
		tokens[who] = strings.TrimSpace(string(secret))
	}
	checks := map[string]bool{}
	result := map[string]any{"passed": false, "checks": checks, "scope": "actual Mattermost REST v4, injected trusted TLS client, real local channel service and SQLite"}
	t.Cleanup(func() {
		posts, verified, drops := wire.counters()
		result["actual_adapter_posts"], result["verified_tls_responses"], result["dropped_actual_acknowledgements"] = posts, verified, drops
		result["passed"] = !t.Failed() && len(checks) == 10 && posts == 2 && verified > 0 && drops == 1
		hash := sha256.Sum256(ca)
		result["ca_sha256"] = hex.EncodeToString(hash[:])
		b, _ := json.MarshalIndent(result, "", "  ")
		if e := os.WriteFile(filepath.Join(fixture.State, "result.json"), append(b, '\n'), 0600); e != nil {
			t.Error("owned_result_write_failed")
		}
	})
	request := func(who, method, path string, body any, into any) {
		t.Helper()
		var reader io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			reader = bytes.NewReader(b)
		}
		r, e := http.NewRequestWithContext(ctx, method, fixture.Endpoint+"/api/v4"+path, reader)
		if e != nil {
			t.Fatal("fixture_request_invalid")
		}
		r.Header.Set("Authorization", "Bearer "+tokens[who])
		r.Header.Set("Content-Type", "application/json")
		resp, e := adminClient.Do(r)
		if e != nil {
			t.Fatal("fixture_request_failed")
		}
		defer resp.Body.Close()
		b, e := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
		if e != nil || len(b) > 1<<20 || resp.StatusCode < 200 || resp.StatusCode >= 300 || (into != nil && json.Unmarshal(b, into) != nil) {
			t.Fatal("fixture_response_invalid")
		}
	}
	post := func(who, root, text string) mmAcceptancePost {
		t.Helper()
		var p mmAcceptancePost
		request(who, http.MethodPost, "/posts", map[string]string{"channel_id": fixture.Channel, "root_id": root, "message": text}, &p)
		return p
	}
	dbPath := filepath.Join(fixture.State, "db", "registry.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal("owned_store_open_failed")
	}
	defer func() { _ = db.Close() }()
	seed := domain.NewSession("owned-fixture-home", "Owned Mattermost fixture", fixture.State, "fixture", "zero-model-activity")
	if _, _, err = db.Record(ctx, domain.Operation{ID: domain.NewID("op"), Kind: "launch", SessionID: seed.ID, ExecutionID: seed.Execution.ID}, []byte("owned channel fixture; no terminal process"), &seed); err != nil {
		t.Fatal("owned_seed_failed")
	}
	factory := func(provider channels.Provider) (channels.Adapter, error) {
		if provider != channels.Mattermost {
			return nil, errors.New("owned_fixture_provider_invalid")
		}
		return chatadapter.NewMattermost(client), nil
	}
	service, err := New(ctx, db, factory)
	if err != nil {
		t.Fatal("owned_service_open_failed")
	}
	defer func() { _ = service.Close() }()
	bindRequest := protocol.ChannelBindRequest{ID: domain.NewID("binding"), SessionID: seed.ID, Provider: channels.Mattermost, Endpoint: fixture.Endpoint, ChannelID: fixture.Channel, RootID: fixture.Root, CredentialRef: fixture.TokenPaths["bot"], Allowlist: []string{fixture.Human}}
	bound, err := service.Bind(ctx, bindRequest)
	if err != nil || bound.RealmID != fixture.Team || bound.SelfID != fixture.Bot || bound.RootID != fixture.Root || bound.ThreadID != seed.ThreadID {
		t.Fatal("exact_verified_binding_failed")
	}
	checks["exact_verified_bot_team_root_binding"] = true
	// The test CA has not been installed globally: an ordinary client must fail.
	probeBinding, _, err := db.ChannelBinding(ctx, bound.ID)
	if err != nil || probeBinding.WorkID != seed.WorkID || probeBinding.HostID != seed.HostID || probeBinding.ThreadID != seed.ThreadID || probeBinding.Endpoint != fixture.Endpoint {
		t.Fatal("bound_route_read_failed")
	}
	if _, err = chatadapter.NewMattermost(&http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second}).Probe(ctx, probeBinding); err == nil {
		t.Fatal("untrusted_ca_was_accepted")
	}
	checks["verified_tls_and_no_global_ca_change"] = true
	// A reply cannot be presented as the root of a new native thread binding.
	reply := post("human", fixture.Root, "Owned preflight nested reply")
	bad := bindRequest
	bad.ID, bad.RootID = domain.NewID("binding"), reply.ID
	if _, err = service.Bind(ctx, bad); err == nil {
		t.Fatal("nested_reply_root_accepted")
	}
	checks["nested_reply_is_not_root"] = true
	// Move beyond the server's millisecond timestamp rounding after activation.
	time.Sleep(20 * time.Millisecond)
	allowed := post("human", fixture.Root, "Owned allowed note: café 🐚; text is inert")
	outsider := post("outsider", fixture.Root, "Owned unallowlisted note")
	self := post("bot", fixture.Root, "Owned bot echo")
	otherRoot := post("human", fixture.OtherRoot, "Owned other-root note")
	reset := func() {
		// Deterministic explicit polling, not a production cadence assertion.
		service.mu.Lock()
		service.lastFetch[bound.ID], service.lastSend[bound.ID] = time.Time{}, time.Time{}
		service.mu.Unlock()
	}
	reset()
	if _, err = service.Sync(ctx, bound.ID); err != nil {
		t.Fatal("scoped_inbound_sync_failed")
	}
	notes, err := db.ChannelMessages(ctx, bound.ThreadID, 0, 100)
	seen := map[string]bool{}
	for _, m := range notes {
		seen[m.RemoteID] = true
	}
	if err != nil || !seen[allowed.ID] || seen[outsider.ID] || seen[self.ID] || seen[otherRoot.ID] {
		t.Fatal("inbound_scope_or_allowlist_failed")
	}
	if calls, _, _ := wire.counters(); calls != 0 {
		t.Fatal("inbound_note_caused_post")
	}
	checks["allowed_inbound_self_outsider_other_root_exclusion"] = true
	// More than one bounded service page must remain complete and deduplicated.
	paged := map[string]bool{}
	for i := 0; i < 19; i++ {
		p := post("human", fixture.Root, "Owned pagination note "+string(rune('A'+i)))
		paged[p.ID] = true
	}
	more := false
	for i := 0; i < 5; i++ {
		reset()
		synced, e := service.Sync(ctx, bound.ID)
		if e != nil || synced.Imported > 15 {
			t.Fatal("bounded_pagination_failed")
		}
		more = more || synced.HasMore
		if !synced.HasMore {
			break
		}
	}
	notes, err = db.ChannelMessages(ctx, bound.ThreadID, 0, 100)
	for _, m := range notes {
		delete(paged, m.RemoteID)
	}
	if err != nil || len(paged) != 0 || !more {
		t.Fatal("pagination_dropped_note_or_lacked_positive_page_control")
	}
	checks["bounded_thread_pagination"] = true
	// Edits are intentionally not projected. Refetching the original page must
	// preserve its first authored snapshot while intake continues.
	request("human", http.MethodPut, "/posts/"+allowed.ID, map[string]string{"id": allowed.ID, "message": "Owned edited text"}, nil)
	adapter := chatadapter.NewMattermost(client)
	page, err := adapter.Fetch(ctx, probeBinding, "", 100)
	if err != nil {
		t.Fatal("edited_snapshot_refetch_failed")
	}
	_, current, err := db.ChannelBinding(ctx, bound.ID)
	if err != nil {
		t.Fatal("snapshot_cursor_read_failed")
	}
	page.NextCursor = current // Revalidation snapshot, not a cursor rewind.
	if _, err = db.ImportChannelMessages(ctx, bound.ID, current, page); err != nil {
		t.Fatal("edited_snapshot_import_failed")
	}
	notes, err = db.ChannelMessages(ctx, bound.ThreadID, 0, 100)
	foundOriginal := false
	for _, m := range notes {
		if m.RemoteID == allowed.ID {
			foundOriginal = m.Text == allowed.Message
		}
	}
	if err != nil || !foundOriginal {
		t.Fatal("edit_replaced_first_snapshot")
	}
	checks["remote_edit_preserves_first_snapshot"] = true
	intent := protocol.ChannelSendRequest{ID: domain.NewID("message"), BindingID: bound.ID, Text: "Owned explicit outbound: café 🐚\nOne post only."}
	if _, err = service.Send(ctx, intent); err != nil {
		t.Fatal("explicit_outgoing_queue_failed")
	}
	reset()
	synced, err := service.Sync(ctx, bound.ID)
	if err != nil || synced.Delivery == nil || synced.Delivery.State != channels.Delivered {
		t.Fatal("actual_outgoing_ack_failed")
	}
	var accepted mmAcceptancePost
	request("admin", http.MethodGet, "/posts/"+synced.Delivery.RemoteID, nil, &accepted)
	if accepted.ID != synced.Delivery.RemoteID || accepted.UserID != fixture.Bot || accepted.ChannelID != fixture.Channel || accepted.RootID != fixture.Root || accepted.Message != intent.Text || accepted.Props["keelmux_delivery_id"] != string(intent.ID) || accepted.Props["keelmux_payload_hash"] != hex.EncodeToString(synced.Delivery.PayloadHash[:]) {
		t.Fatal("get_did_not_confirm_exact_post")
	}
	if _, err = service.Send(ctx, intent); err != nil {
		t.Fatal("same_intent_retry_failed")
	}
	reset()
	if _, err = service.Sync(ctx, bound.ID); err != nil {
		t.Fatal("same_intent_sync_failed")
	}
	if calls, _, _ := wire.counters(); calls != 1 {
		t.Fatal("same_operation_retry_repeated_post")
	}
	checks["explicit_post_ack_get_and_idempotent_retry"] = true
	lost := protocol.ChannelSendRequest{ID: domain.NewID("message"), BindingID: bound.ID, Text: "Owned actual POST with discarded acknowledgement"}
	wire.mu.Lock()
	wire.dropID = lost.ID
	wire.mu.Unlock()
	if _, err = service.Send(ctx, lost); err != nil {
		t.Fatal("lost_ack_queue_failed")
	}
	reset()
	synced, err = service.Sync(ctx, bound.ID)
	if err != nil || synced.Delivery == nil || synced.Delivery.State != channels.Uncertain {
		t.Fatal("lost_actual_ack_not_uncertain")
	}
	wire.mu.Lock()
	dropped := wire.dropped
	wire.mu.Unlock()
	request("admin", http.MethodGet, "/posts/"+dropped.ID, nil, &accepted)
	if accepted.ID != dropped.ID || accepted.Message != lost.Text || accepted.RootID != fixture.Root || accepted.UserID != fixture.Bot {
		t.Fatal("lost_response_had_no_actual_effect")
	}
	checks["discarded_actual_post_ack_is_uncertain"] = true
	if err = service.Close(); err != nil {
		t.Fatal("service_close_failed")
	}
	if err = db.Close(); err != nil {
		t.Fatal("store_close_failed")
	}
	db, err = store.Open(dbPath)
	if err != nil {
		t.Fatal("durable_store_reopen_failed")
	}
	service, err = New(ctx, db, factory)
	if err != nil {
		t.Fatal("durable_service_reopen_failed")
	}
	if _, err = service.Send(ctx, lost); err != nil {
		t.Fatal("lost_operation_retry_failed")
	}
	reset()
	if _, err = service.Sync(ctx, bound.ID); err != nil {
		t.Fatal("restart_sync_failed")
	}
	if d, e := db.ChannelDelivery(ctx, lost.ID); e != nil || d.State != channels.Uncertain {
		t.Fatal("restart_lost_uncertainty")
	}
	if calls, _, _ := wire.counters(); calls != 2 {
		t.Fatal("restart_replayed_uncertain_post")
	}
	checks["reopen_and_retry_never_resend_uncertain"] = true
	if err = service.Disable(ctx, bound.ID); err != nil {
		t.Fatal("owned_disable_failed")
	}
	if _, err = service.Send(ctx, protocol.ChannelSendRequest{ID: domain.NewID("message"), BindingID: bound.ID, Text: "must never post"}); err == nil {
		t.Fatal("disabled_binding_admitted_new_intent")
	}
	d, err := service.Reconcile(ctx, lost.ID)
	if err != nil || d.State != channels.Delivered || d.RemoteID != dropped.ID {
		t.Fatal("disabled_historical_readonly_reconcile_failed")
	}
	if calls, _, _ := wire.counters(); calls != 2 {
		t.Fatal("readonly_reconcile_posted_again")
	}
	checks["disabled_historical_reconcile_is_readonly"] = true
}
