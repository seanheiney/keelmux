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
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/channels"
	"github.com/seanheiney/keelmux/internal/chatadapter"
	"github.com/seanheiney/keelmux/internal/config"
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

func mmAcceptancePrivateFile(path string) ([]byte, error) {
	info, err := os.Lstat(filepath.Dir(path))
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, errors.New("owned fixture directory invalid")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) {
		return nil, errors.New("owned fixture directory owner invalid")
	}
	return config.ReadPrivateSecret(path)
}

func TestMMAcceptancePrivateFixtureFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	regular := filepath.Join(dir, "regular")
	if err := os.WriteFile(regular, []byte("owned"), 0600); err != nil {
		t.Fatal(err)
	}
	if data, err := mmAcceptancePrivateFile(regular); err != nil || string(data) != "owned" {
		t.Fatal("private positive control failed")
	}
	symlink := filepath.Join(dir, "symlink")
	if err := os.Symlink(regular, symlink); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	public := filepath.Join(dir, "public")
	if err := os.WriteFile(public, []byte("public"), 0644); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{"symlink": symlink, "fifo": fifo, "public": public} {
		t.Run(name, func(t *testing.T) {
			if _, err := mmAcceptancePrivateFile(path); err == nil {
				t.Fatal("unsafe fixture file accepted")
			}
		})
	}
	if os.Geteuid() == 0 {
		foreign := filepath.Join(dir, "foreign")
		if err := os.WriteFile(foreign, []byte("foreign"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(foreign, 65534, 65534); err != nil {
			t.Fatal(err)
		}
		if _, err := mmAcceptancePrivateFile(foreign); err == nil {
			t.Fatal("foreign fixture file accepted")
		}
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := mmAcceptancePrivateFile(regular); err == nil {
		t.Fatal("public fixture directory accepted")
	}
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
	state           string
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
	index := w.verified
	w.mu.Unlock()
	data, readErr := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	_ = response.Body.Close()
	if readErr != nil || len(data) > 1<<20 {
		return nil, errors.New("owned trace response exceeded bound")
	}
	trace, _ := json.Marshal(struct {
		Method, Path, Query string
		Status              int
		Body                []byte
	}{req.Method, req.URL.Path, req.URL.RawQuery, response.StatusCode, data})
	if err := os.WriteFile(filepath.Join(w.state, "private-wire-"+strconv.Itoa(index)+".json"), trace, 0600); err != nil {
		return nil, errors.New("owned trace could not be retained")
	}
	response.Body = io.NopCloser(bytes.NewReader(data))
	if !drop {
		return response, nil
	}
	_ = response.Body.Close()
	var p mmAcceptancePost
	if response.StatusCode != http.StatusCreated || json.Unmarshal(data, &p) != nil || p.ID == "" {
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
	data, err := mmAcceptancePrivateFile(fixturePath)
	if err != nil || json.Unmarshal(data, &fixture) != nil || fixture.Schema != 1 {
		t.Fatal("owned_fixture_invalid")
	}
	// Fixture endpoints must be loopback HTTPS; this test cannot be pointed at a
	// customer server through an ordinary environment override.
	u, err := url.Parse(fixture.Endpoint)
	if err != nil || u.Scheme != "https" || !net.ParseIP(u.Hostname()).IsLoopback() || u.Path != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		t.Fatal("owned_endpoint_invalid")
	}
	info, err := os.Lstat(fixturePath)
	if err != nil || info.Mode().Perm() != 0600 || filepath.Dir(fixturePath) != fixture.State {
		t.Fatal("owned_fixture_permissions_invalid")
	}
	stateInfo, err := os.Lstat(fixture.State)
	if err != nil || stateInfo.Mode().Perm() != 0700 {
		t.Fatal("owned_state_permissions_invalid")
	}
	ca, err := mmAcceptancePrivateFile(fixture.CA)
	pool := x509.NewCertPool()
	if err != nil || !pool.AppendCertsFromPEM(ca) {
		t.Fatal("owned_ca_invalid")
	}
	base := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, Proxy: nil}
	defer base.CloseIdleConnections()
	wire := &mmAcceptanceTransport{base: base, state: fixture.State}
	client := &http.Client{Transport: wire, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	// Setup requests use an independent trusted client and do not inflate the
	// adapter's real POST counter.
	adminClient := &http.Client{Transport: base, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	tokens := map[string]string{}
	for _, who := range []string{"admin", "human", "outsider", "bot"} {
		secret, e := mmAcceptancePrivateFile(fixture.TokenPaths[who])
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
		result["passed"] = !t.Failed() && len(checks) == 11 && posts == 2 && verified > 0 && drops == 1
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
	// A copied marker or a remote edit cannot stand in for this frozen content.
	request("bot", http.MethodPut, "/posts/"+dropped.ID, map[string]string{"id": dropped.ID, "message": "Owned changed receipt text"}, nil)
	request("admin", http.MethodGet, "/posts/"+dropped.ID, nil, &accepted)
	original, e := db.ChannelDelivery(ctx, lost.ID)
	if e != nil || accepted.Message == lost.Text || accepted.Props["keelmux_delivery_id"] != string(lost.ID) || accepted.Props["keelmux_payload_hash"] != hex.EncodeToString(original.PayloadHash[:]) {
		t.Fatal("edited_receipt_control_invalid")
	}
	if d, e := service.Reconcile(ctx, lost.ID); e != nil || d.State != channels.Uncertain {
		t.Fatal("changed_remote_text_falsely_reconciled")
	}
	request("bot", http.MethodPut, "/posts/"+dropped.ID, map[string]string{"id": dropped.ID, "message": lost.Text}, nil)
	checks["edited_receipt_with_same_markers_remains_uncertain"] = true
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

// A separate phase runs exact source-built product binaries in the Python
// owner's fresh Linux container. SSL_CERT_FILE applies only to these children.
func TestMattermostOwnedDaemonAcceptance(t *testing.T) {
	fixturePath, binDir := os.Getenv("KEELMUX_MM_ACCEPTANCE_FIXTURE"), os.Getenv("KEELMUX_MM_ACCEPTANCE_BIN_DIR")
	if fixturePath == "" || binDir == "" {
		t.Skip("requires owned Linux source-binary fixture")
	}
	data, err := mmAcceptancePrivateFile(fixturePath)
	var f mmAcceptanceFixture
	if err != nil || json.Unmarshal(data, &f) != nil || f.Schema != 1 || f.Endpoint != "https://127.0.0.1:8065" || f.State != filepath.Dir(fixturePath) {
		t.Fatal("owned_daemon_fixture_invalid")
	}
	ca, err := mmAcceptancePrivateFile(f.CA)
	pool := x509.NewCertPool()
	if err != nil || !pool.AppendCertsFromPEM(ca) {
		t.Fatal("owned_daemon_ca_invalid")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, Proxy: nil}
	defer transport.CloseIdleConnections()
	adminClient := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	tokens := map[string]string{}
	for _, name := range []string{"admin", "human", "outsider", "bot"} {
		b, e := mmAcceptancePrivateFile(f.TokenPaths[name])
		if e != nil || filepath.Dir(f.TokenPaths[name]) != f.State {
			t.Fatal("owned_daemon_secret_invalid")
		}
		tokens[name] = string(b)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	for _, name := range []string{"keelmux", "keelmuxd"} {
		info, e := os.Lstat(filepath.Join(binDir, name))
		if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Mode().Perm()&0100 == 0 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
			t.Fatal("owned_source_binary_invalid")
		}
	}
	home, state, emptyCA := filepath.Join(f.State, "private-home"), filepath.Join(f.State, "daemon-state"), filepath.Join(f.State, "empty-ca-dir")
	for _, dir := range []string{home, state, emptyCA} {
		if err = os.Mkdir(dir, 0700); err != nil {
			t.Fatal("owned_daemon_directory_failed")
		}
	}
	paths, err := config.Resolve(state)
	if err != nil {
		t.Fatal("owned_daemon_paths_failed")
	}
	host, err := paths.HostID()
	if err != nil {
		t.Fatal("owned_daemon_host_failed")
	}
	db, err := store.Open(paths.DB)
	if err != nil {
		t.Fatal("owned_daemon_seed_open_failed")
	}
	seed := domain.NewSession(host, "Owned daemon channel fixture", home, "fixture", "no-native-process")
	if _, _, err = db.Record(ctx, domain.Operation{ID: domain.NewID("op"), Kind: "launch", SessionID: seed.ID, ExecutionID: seed.Execution.ID}, []byte("owned stable-session fixture; no native process"), &seed); err != nil {
		t.Fatal("owned_daemon_seed_record_failed")
	}
	if err = db.Close(); err != nil {
		t.Fatal("owned_daemon_seed_close_failed")
	}
	childEnv := []string{"PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin", "HOME=" + home, "SSL_CERT_FILE=" + f.CA, "SSL_CERT_DIR=" + emptyCA, "KEELMUX_STATE_DIR=" + state}
	checks := map[string]bool{}
	result := map[string]any{"passed": false, "scope": "source-built Linux arm64 product daemon and CLI through actual Unix control socket and ordinary production HTTP client", "checks": checks, "fixture_session_has_no_native_process": true, "global_ca_changed": false, "processes_joined": true}
	var daemon *exec.Cmd
	var wait chan error
	var instance string
	stop := func() bool {
		if daemon == nil {
			return true
		}
		select {
		case <-wait:
			daemon = nil
			return false // Unexpected loss before this owned stop was admitted.
		default:
		}
		pgid, e := syscall.Getpgid(daemon.Process.Pid)
		if e != nil || pgid != daemon.Process.Pid {
			return false
		}
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		select {
		case <-wait:
			daemon = nil
			return true
		case <-time.After(8 * time.Second):
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			select {
			case <-wait:
				daemon = nil
				return true
			case <-time.After(2 * time.Second):
				return false
			}
		}
	}
	t.Cleanup(func() {
		joined := stop()
		result["processes_joined"] = joined
		result["passed"] = !t.Failed() && joined && len(checks) == 7
		b, _ := json.MarshalIndent(result, "", "  ")
		if e := os.WriteFile(filepath.Join(f.State, "daemon-result.json"), append(b, '\n'), 0600); e != nil {
			t.Error("owned_daemon_result_failed")
		}
	})
	client := protocol.NewClient(paths.Control)
	start := func() protocol.DaemonStatus {
		t.Helper()
		log, e := config.OpenPrivateLog(filepath.Join(f.State, "daemon-private.log"))
		if e != nil {
			t.Fatal("owned_daemon_log_failed")
		}
		defer log.Close()
		daemon = exec.Command(filepath.Join(binDir, "keelmuxd"), "-state", state)
		daemon.Env, daemon.Stdout, daemon.Stderr = childEnv, log, log
		daemon.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if e = daemon.Start(); e != nil {
			t.Fatal("owned_daemon_launch_failed")
		}
		wait = make(chan error, 1)
		owned := daemon
		ownedWait := wait
		go func() { ownedWait <- owned.Wait() }()
		for i := 0; i < 100; i++ {
			var status protocol.DaemonStatus
			e = client.Call(ctx, http.MethodGet, "/v1/daemon", nil, &status)
			if e == nil && status.PID == owned.Process.Pid && status.Instance != "" && status.Protocol == protocol.Version {
				instance = status.Instance
				return status
			}
			select {
			case <-wait:
				daemon = nil
				t.Fatal("owned_daemon_exited_before_ready")
			case <-time.After(100 * time.Millisecond):
			}
		}
		t.Fatal("owned_daemon_ready_timeout")
		return protocol.DaemonStatus{}
	}
	cli := func(expectSuccess bool, args ...string) []byte {
		t.Helper()
		verifyForeground := func() {
			var current protocol.DaemonStatus
			if daemon == nil || client.Call(ctx, http.MethodGet, "/v1/daemon", nil, &current) != nil || current.PID != daemon.Process.Pid || current.Instance != instance {
				t.Fatal("source_cli_lost_owned_foreground_daemon")
			}
		}
		verifyForeground()
		cmd := exec.CommandContext(ctx, filepath.Join(binDir, "keelmux"), append([]string{"-state", state}, args...)...)
		cmd.Env = childEnv
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, e := cmd.Output()
		log, le := config.OpenPrivateLog(filepath.Join(f.State, "cli-private.log"))
		if le == nil {
			_, _ = log.Write(stderr.Bytes())
			_ = log.Close()
		}
		if (e == nil) != expectSuccess || le != nil {
			t.Fatal("owned_cli_command_outcome_failed")
		}
		verifyForeground()
		return out
	}
	request := func(who, method, path string, body any, into any) {
		t.Helper()
		var reader io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			reader = bytes.NewReader(b)
		}
		r, e := http.NewRequestWithContext(ctx, method, f.Endpoint+"/api/v4"+path, reader)
		if e != nil {
			t.Fatal("owned_daemon_fixture_request_invalid")
		}
		r.Header.Set("Authorization", "Bearer "+tokens[who])
		r.Header.Set("Content-Type", "application/json")
		response, e := adminClient.Do(r)
		if e != nil {
			t.Fatal("owned_daemon_fixture_request_failed")
		}
		defer response.Body.Close()
		b, e := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
		if e != nil || len(b) > 1<<20 || response.StatusCode < 200 || response.StatusCode >= 300 || (into != nil && json.Unmarshal(b, into) != nil) {
			t.Fatal("owned_daemon_fixture_response_failed")
		}
	}
	status := start()
	if source := os.Getenv("KEELMUX_MM_ACCEPTANCE_SOURCE"); len(source) != 40 || status.Build.Commit != source {
		t.Fatal("source_daemon_build_commit_mismatch")
	}
	if !bytes.Contains(cli(true, "channels", "--help"), []byte("semantic dispatch")) {
		t.Fatal("channel_cli_help_missing_scope")
	}
	checks["source_foreground_daemon_identity_and_cli_help"] = true
	bindID := domain.NewID("binding")
	var bound protocol.ChannelStatus
	if json.Unmarshal(cli(true, "channels", "bind", "--binding", string(bindID), "--provider", "mattermost", "--session", string(seed.ID), "--server", f.Endpoint, "--channel", f.Channel, "--root", f.Root, "--credential-file", f.TokenPaths["bot"], "--allow-user", f.Human), &bound) != nil || bound.ID != bindID || bound.ThreadID != seed.ThreadID || bound.RealmID != f.Team || bound.SelfID != f.Bot || bound.RootID != f.Root || bound.SemanticDispatch {
		t.Fatal("source_cli_exact_bind_failed")
	}
	var bindings []protocol.ChannelStatus
	if json.Unmarshal(cli(true, "channels", "list"), &bindings) != nil || len(bindings) != 1 || bindings[0].ID != bound.ID {
		t.Fatal("source_cli_list_failed")
	}
	checks["source_cli_bind_list_exact_route"] = true
	time.Sleep(20 * time.Millisecond)
	post := func(who, root, text string) mmAcceptancePost {
		var p mmAcceptancePost
		request(who, http.MethodPost, "/posts", map[string]string{"channel_id": f.Channel, "root_id": root, "message": text}, &p)
		return p
	}
	allowed := post("human", f.Root, "Owned source daemon inbound café 🐚")
	outsider, self, other := post("outsider", f.Root, "Owned source excluded outsider"), post("bot", f.Root, "Owned source bot echo"), post("human", f.OtherRoot, "Owned source other-root note")
	var history protocol.ChannelHistory
	intakeOK := false
	for i := 0; i < 70; i++ {
		cli(true, "channels", "sync", string(bound.ID))
		if json.Unmarshal(cli(true, "channels", "history", string(bound.ThreadID)), &history) != nil || !history.SnapshotOnly || history.SemanticDispatch {
			t.Fatal("source_cli_history_scope_failed")
		}
		seen := map[string]bool{}
		for _, m := range history.Messages {
			seen[m.RemoteID] = true
		}
		if seen[outsider.ID] || seen[self.ID] || seen[other.ID] {
			t.Fatal("source_daemon_inbound_scope_failed")
		}
		if seen[allowed.ID] {
			intakeOK = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !intakeOK {
		t.Fatal("source_daemon_inbound_timeout")
	}
	checks["source_daemon_actual_scoped_inbound_history"] = true
	operation := domain.NewID("message")
	text := "Owned source daemon outgoing café 🐚"
	cli(true, "channels", "send", "--binding", string(bound.ID), "--operation", string(operation), "--text", text)
	var delivery channels.Delivery
	for i := 0; i < 70; i++ {
		if json.Unmarshal(cli(true, "channels", "delivery", string(operation)), &delivery) != nil {
			t.Fatal("source_daemon_delivery_decode_failed")
		}
		if delivery.State == channels.Delivered {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	var posted mmAcceptancePost
	if delivery.State != channels.Delivered || delivery.RemoteID == "" {
		t.Fatal("source_daemon_actual_post_not_delivered")
	}
	request("admin", http.MethodGet, "/posts/"+delivery.RemoteID, nil, &posted)
	if posted.ID != delivery.RemoteID || posted.RootID != f.Root || posted.UserID != f.Bot || posted.ChannelID != f.Channel || posted.Message != text || posted.Props["keelmux_delivery_id"] != string(operation) || posted.Props["keelmux_payload_hash"] != hex.EncodeToString(delivery.PayloadHash[:]) {
		t.Fatal("source_daemon_get_did_not_match_post")
	}
	checks["source_cli_actual_post_ack_and_get"] = true
	count := func() int {
		var page struct {
			Posts map[string]mmAcceptancePost `json:"posts"`
		}
		request("admin", http.MethodGet, "/posts/"+f.Root+"/thread?perPage=100&direction=up", nil, &page)
		n := 0
		for _, p := range page.Posts {
			if p.Props["keelmux_delivery_id"] == string(operation) {
				n++
			}
		}
		return n
	}
	cli(true, "channels", "send", "--binding", string(bound.ID), "--operation", string(operation), "--text", text)
	var reconciled channels.Delivery
	if json.Unmarshal(cli(true, "channels", "reconcile", string(operation)), &reconciled) != nil || reconciled.State != channels.Delivered || reconciled.RemoteID != delivery.RemoteID || count() != 1 {
		t.Fatal("source_cli_retry_or_readonly_reconcile_failed")
	}
	checks["source_cli_same_operation_retry_and_reconcile_no_duplicate"] = true
	if !stop() {
		t.Fatal("source_daemon_first_process_not_joined")
	}
	newStatus := start()
	if newStatus.Instance == status.Instance {
		t.Fatal("source_daemon_restart_reused_instance")
	}
	if json.Unmarshal(cli(true, "channels", "delivery", string(operation)), &reconciled) != nil || reconciled.State != channels.Delivered || reconciled.RemoteID != delivery.RemoteID {
		t.Fatal("source_daemon_restart_lost_receipt")
	}
	cli(true, "channels", "send", "--binding", string(bound.ID), "--operation", string(operation), "--text", text)
	if count() != 1 {
		t.Fatal("source_daemon_restart_replayed_post")
	}
	checks["source_daemon_restart_preserves_binding_and_receipt"] = true
	cli(true, "channels", "disable", string(bound.ID))
	cli(false, "channels", "send", "--binding", string(bound.ID), "--operation", string(domain.NewID("message")), "--text", "Owned disabled intent must fail")
	if json.Unmarshal(cli(true, "channels", "reconcile", string(operation)), &reconciled) != nil || reconciled.State != channels.Delivered || count() != 1 {
		t.Fatal("source_daemon_disabled_historical_receipt_failed")
	}
	checks["source_cli_disable_refuses_new_intent"] = true
	result["actual_operation_posts"] = 1
	result["stable_source_commit"] = newStatus.Build.Commit
}
