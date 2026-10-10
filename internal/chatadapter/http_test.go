package chatadapter

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/channels"
)

func privateFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "private-credential")
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}
func testBinding(t *testing.T, provider channels.Provider) channels.Binding {
	t.Helper()
	b := channels.Binding{ID: "binding", SessionID: "session", ThreadID: "thread", WorkID: "work", HostID: "host", Provider: provider, RealmID: "realm", SelfID: "self", ChannelID: "channel", RootID: "root", CredentialRef: privateFile(t, "fixture-token"), ActivationCutoff: time.Unix(1700000000, 0).UTC(), Active: true, Allowlist: []string{"alice"}}
	if provider == channels.Slack {
		b.Endpoint = "https://slack.com/api"
		b.RootID = "1699999900.000001"
	}
	return b
}
func testDelivery(b channels.Binding) (channels.Delivery, channels.Message) {
	m := channels.Message{ID: "delivery", BindingID: b.ID, ThreadID: b.ThreadID, Text: "Explicit reply: hello 世界 🧭", AuthorID: "local", CreatedAt: time.Now().UTC()}
	d := channels.Delivery{ID: m.ID, MessageID: m.ID, BindingID: b.ID, State: channels.Attempted, PayloadHash: sha256.Sum256([]byte(m.Text))}
	return d, m
}
func jsonResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func dropResponse(w http.ResponseWriter) {
	c, _, err := w.(http.Hijacker).Hijack()
	if err == nil {
		c.Close()
	}
}

type mmFixture struct {
	mu                                                   sync.Mutex
	b                                                    channels.Binding
	posts                                                map[string]mmPost
	count                                                int
	drop, wrongAck, wrongRoot, wrongRealm, duplicateAuth bool
	rotate                                               func()
	tokens                                               []string
}

func newMMFixture(t *testing.T) (*mmFixture, *Mattermost, channels.Binding) {
	t.Helper()
	b := testBinding(t, channels.Mattermost)
	f := &mmFixture{b: b, posts: map[string]mmPost{}}
	f.posts[b.RootID] = mmPost{ID: b.RootID, UserID: "alice", ChannelID: b.ChannelID, Text: "Existing root", CreatedAt: b.ActivationCutoff.UnixMilli() - 1000}
	s := httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(s.Close)
	t.Cleanup(func() { f.mu.Lock(); f.rotate = nil; f.mu.Unlock() })
	b.Endpoint = s.URL
	f.b = b
	a := NewMattermost(s.Client())
	t.Cleanup(func() { a.Close() })
	return f, a, b
}
func (f *mmFixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = append(f.tokens, r.Header.Get("Authorization"))
	switch {
	case r.URL.Path == "/api/v4/users/me":
		if f.rotate != nil {
			fn := f.rotate
			f.rotate = nil
			fn()
		}
		if f.duplicateAuth {
			fmt.Fprint(w, `{"id":"self","id":"other"}`)
			return
		}
		id := f.b.SelfID
		if r.Header.Get("Authorization") == "Bearer changed-token" {
			id = "other"
		}
		jsonResponse(w, 200, map[string]string{"id": id})
	case r.URL.Path == "/api/v4/channels/"+f.b.ChannelID:
		realm := f.b.RealmID
		if f.wrongRealm {
			realm = "other-team"
		}
		jsonResponse(w, 200, map[string]any{"id": f.b.ChannelID, "team_id": realm, "type": "O"})
	case r.Method == http.MethodPost && r.URL.Path == "/api/v4/posts":
		var body struct {
			Text    string                     `json:"message"`
			Channel string                     `json:"channel_id"`
			Root    string                     `json:"root_id"`
			Pending string                     `json:"pending_post_id"`
			Props   map[string]json.RawMessage `json:"props"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			jsonResponse(w, 400, nil)
			return
		}
		f.count++
		p := mmPost{ID: fmt.Sprintf("reply%d", f.count), UserID: f.b.SelfID, ChannelID: body.Channel, RootID: body.Root, Text: body.Text, CreatedAt: f.b.ActivationCutoff.UnixMilli() + int64(f.count), Props: body.Props}
		f.posts[p.ID] = p
		if f.drop {
			dropResponse(w)
			return
		}
		if f.wrongAck {
			p.RootID = "other-root"
		}
		jsonResponse(w, 201, p)
	case r.URL.Path == "/api/v4/posts/"+f.b.RootID+"/thread":
		limit, _ := strconv.Atoi(r.URL.Query().Get("perPage"))
		if limit < 1 || limit > 100 {
			jsonResponse(w, 400, nil)
			return
		}
		at, _ := strconv.ParseInt(r.URL.Query().Get("fromCreateAt"), 10, 64)
		anchor := r.URL.Query().Get("fromPost")
		direction := r.URL.Query().Get("direction")
		var posts []mmPost
		for _, p := range f.posts {
			if p.ID == f.b.RootID {
				continue
			}
			if at > 0 && (p.CreatedAt < at || p.CreatedAt == at && p.ID <= anchor) {
				continue
			}
			posts = append(posts, p)
		}
		sort.Slice(posts, func(i, j int) bool {
			if posts[i].CreatedAt == posts[j].CreatedAt {
				if direction == "up" {
					return posts[i].ID > posts[j].ID
				}
				return posts[i].ID < posts[j].ID
			}
			if direction == "up" {
				return posts[i].CreatedAt > posts[j].CreatedAt
			}
			return posts[i].CreatedAt < posts[j].CreatedAt
		})
		hasMore := len(posts) > limit
		if hasMore {
			posts = posts[:limit]
		}
		root := f.posts[f.b.RootID]
		list := mmList{Order: []string{root.ID}, Posts: map[string]mmPost{root.ID: root}, HasNext: &hasMore}
		for _, p := range posts {
			list.Order = append(list.Order, p.ID)
			list.Posts[p.ID] = p
		}
		jsonResponse(w, 200, list)
	case strings.HasPrefix(r.URL.Path, "/api/v4/posts/"):
		id := strings.TrimPrefix(r.URL.Path, "/api/v4/posts/")
		p, ok := f.posts[id]
		if !ok {
			jsonResponse(w, 404, nil)
			return
		}
		if f.wrongRoot && id == f.b.RootID {
			p.ChannelID = "foreign"
		}
		jsonResponse(w, 200, p)
	default:
		jsonResponse(w, 404, nil)
	}
}

func TestMattermostLostACKReconcilesWithoutRepeatingPost(t *testing.T) {
	f, a, b := newMMFixture(t)
	d, m := testDelivery(b)
	f.drop = true
	r, err := a.Send(context.Background(), b, d, m)
	if err != nil || r.State != channels.Uncertain || !r.Evidence.Matches(b) {
		t.Fatalf("send state=%s err=%v", r.State, err)
	}
	r, err = a.Reconcile(context.Background(), b, d, m)
	if err != nil || r.State != channels.Delivered || r.RemoteID != "reply1" || f.count != 1 {
		t.Fatalf("reconcile state=%s count=%d err=%v", r.State, f.count, err)
	}
	f.mu.Lock()
	p := f.posts["reply1"]
	p.UserID = "foreign"
	f.posts[p.ID] = p
	f.mu.Unlock()
	r, _ = a.Reconcile(context.Background(), b, d, m)
	if r.State != channels.Uncertain || f.count != 1 {
		t.Fatal("foreign author marker became a delivery proof")
	}
}

func TestMattermostSameLoadedTokenAndIdentityPreflight(t *testing.T) {
	f, a, b := newMMFixture(t)
	d, m := testDelivery(b)
	f.rotate = func() {
		if err := os.WriteFile(b.CredentialRef, []byte("changed-token"), 0600); err != nil {
			t.Error(err)
		}
	}
	r, _ := a.Send(context.Background(), b, d, m)
	if r.State != channels.Delivered {
		t.Fatalf("first send=%s", r.State)
	}
	for _, token := range f.tokens {
		if token != "Bearer fixture-token" {
			t.Fatal("secret reloaded during one send")
		}
	}
	d.ID = "delivery2"
	d.MessageID = d.ID
	m.ID = d.ID
	r, _ = a.Send(context.Background(), b, d, m)
	if r.State != channels.Failed || r.Code != "remote_rejected" || r.Evidence.Source != "" || f.count != 1 {
		t.Fatal("credential rotation switched immutable self or posted after failed preflight")
	}
}

func TestMattermostScopeAndAcknowledgementFailures(t *testing.T) {
	for _, name := range []string{"realm", "root", "duplicate-auth", "wrong-ack"} {
		t.Run(name, func(t *testing.T) {
			f, a, b := newMMFixture(t)
			d, m := testDelivery(b)
			switch name {
			case "realm":
				f.wrongRealm = true
			case "root":
				f.wrongRoot = true
			case "duplicate-auth":
				f.duplicateAuth = true
			case "wrong-ack":
				f.wrongAck = true
			}
			r, _ := a.Send(context.Background(), b, d, m)
			if name == "wrong-ack" {
				if r.State != channels.Uncertain || f.count != 1 {
					t.Fatal("wrong root ACK was trusted")
				}
			} else if r.State != channels.Failed || f.count != 0 {
				t.Fatal("preflight failure emitted a post")
			}
		})
	}
}

func TestMattermostBoundedThreadPaginationAndCursorScope(t *testing.T) {
	f, a, b := newMMFixture(t)
	at := b.ActivationCutoff.UnixMilli() + 1
	for _, id := range []string{"a", "b", "c"} {
		f.posts[id] = mmPost{ID: id, UserID: "alice", ChannelID: b.ChannelID, RootID: b.RootID, CreatedAt: at, Text: "Unicode 世界 " + id}
	}
	cursor := ""
	for i, id := range []string{"a", "b", "c"} {
		r, err := a.Fetch(context.Background(), b, cursor, 1)
		if err != nil || len(r.Messages) != 1 || r.Messages[0].ID != id || r.HasMore != (i < 2) || !r.Messages[0].Matches(b) {
			t.Fatalf("page %d got %+v err=%v", i, r, err)
		}
		cursor = r.NextCursor
	}
	other := b
	other.ID = "different-binding"
	if _, err := a.Fetch(context.Background(), other, cursor, 1); err == nil {
		t.Fatal("cursor crossed binding")
	}
	if _, err := a.Fetch(context.Background(), b, cursor, 101); err == nil {
		t.Fatal("unbounded fetch accepted")
	}
}

type fixtureTransport struct {
	base      *url.URL
	transport http.RoundTripper
}

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	u := *r.URL
	u.Scheme = f.base.Scheme
	u.Host = f.base.Host
	copy.URL = &u
	copy.Host = f.base.Host
	return f.transport.RoundTrip(copy)
}

type slackFixture struct {
	mu                                       sync.Mutex
	b                                        channels.Binding
	messages                                 []slackMessage
	count, replyCalls                        int
	drop, wrongAck, wrongAuth, expiredCursor bool
	errorCode                                string
	rotate                                   func()
	tokens                                   []string
}

func newSlackFixture(t *testing.T) (*slackFixture, *Slack, channels.Binding) {
	t.Helper()
	b := testBinding(t, channels.Slack)
	f := &slackFixture{b: b, messages: []slackMessage{{Type: "message", TS: b.RootID, User: "alice", Text: "Existing root"}}}
	s := httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(s.Close)
	base, _ := url.Parse(s.URL)
	a := NewSlack(&http.Client{Transport: fixtureTransport{base: base, transport: s.Client().Transport}})
	t.Cleanup(func() { a.Close() })
	return f, a, b
}
func (f *slackFixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = append(f.tokens, r.Header.Get("Authorization"))
	switch r.URL.Path {
	case "/api/auth.test":
		if f.rotate != nil {
			fn := f.rotate
			f.rotate = nil
			fn()
		}
		self := f.b.SelfID
		if f.wrongAuth || r.Header.Get("Authorization") == "Bearer changed-token" {
			self = "foreign"
		}
		jsonResponse(w, 200, slackAuth{OK: true, Team: f.b.RealmID, User: self, Bot: "BSELF"})
	case "/api/chat.postMessage":
		var body struct {
			Text     string     `json:"text"`
			Channel  string     `json:"channel"`
			Root     string     `json:"thread_ts"`
			Metadata *slackMeta `json:"metadata"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.count++
		if f.errorCode != "" {
			jsonResponse(w, 200, map[string]any{"ok": false, "error": f.errorCode})
			return
		}
		p := slackMessage{Type: "message", TS: fmt.Sprintf("1700000001.%06d", f.count), ThreadTS: body.Root, User: f.b.SelfID, Text: body.Text, Metadata: body.Metadata}
		f.messages = append(f.messages, p)
		if f.drop {
			dropResponse(w)
			return
		}
		if f.wrongAck {
			p.ThreadTS = "wrong-root"
		}
		jsonResponse(w, 200, map[string]any{"ok": true, "channel": body.Channel, "ts": p.TS, "message": p})
	case "/api/conversations.replies":
		f.replyCalls++
		q := r.URL.Query()
		if q.Get("channel") != f.b.ChannelID || q.Get("ts") != f.b.RootID {
			jsonResponse(w, 200, map[string]any{"ok": false, "error": "thread_not_found"})
			return
		}
		if f.expiredCursor && q.Get("cursor") != "" {
			f.expiredCursor = false
			jsonResponse(w, 200, map[string]any{"ok": false, "error": "invalid_cursor"})
			return
		}
		limit, _ := strconv.Atoi(q.Get("limit"))
		if limit < 1 || limit > 15 {
			jsonResponse(w, 400, nil)
			return
		}
		var messages []slackMessage
		for _, p := range f.messages {
			if p.TS == f.b.RootID && q.Get("oldest") != "" {
				continue
			}
			if q.Get("oldest") != "" && p.TS < q.Get("oldest") || q.Get("latest") != "" && p.TS > q.Get("latest") {
				continue
			}
			messages = append(messages, p)
		}
		start := 0
		if q.Get("cursor") == "page2" {
			start = 1
		}
		if start > len(messages) {
			start = len(messages)
		}
		messages = messages[start:]
		more := len(messages) > limit
		next := ""
		if more {
			next = "page2"
			messages = messages[:limit]
		}
		jsonResponse(w, 200, map[string]any{"ok": true, "messages": messages, "has_more": more, "response_metadata": map[string]string{"next_cursor": next}})
	default:
		jsonResponse(w, 404, nil)
	}
}

func TestSlackLostACKBoundedReconciliationAndOneRootFetch(t *testing.T) {
	f, a, b := newSlackFixture(t)
	d, m := testDelivery(b)
	if e, err := a.Probe(context.Background(), b); err != nil || !e.Matches(b) {
		t.Fatal("probe", err)
	}
	if f.replyCalls != 1 {
		t.Fatal("initial root was not verified once")
	}
	f.drop = true
	r, _ := a.Send(context.Background(), b, d, m)
	if r.State != channels.Uncertain || f.count != 1 || f.replyCalls != 1 {
		t.Fatal("send repeated root read or lost ACK was trusted")
	}
	r, _ = a.Reconcile(context.Background(), b, d, m)
	if r.State != channels.Delivered || f.count != 1 || f.replyCalls != 2 {
		t.Fatal("reconcile repeated send or failed to verify own exact marker")
	}
	page, err := a.Fetch(context.Background(), b, "", 15)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].ID != "1700000001.000001" || !page.Messages[0].Matches(b) || f.replyCalls != 3 {
		t.Fatalf("fetch %+v %v", page, err)
	}
	f.mu.Lock()
	f.messages[1].User = "foreign"
	f.mu.Unlock()
	r, _ = a.Reconcile(context.Background(), b, d, m)
	if r.State != channels.Uncertain || f.count != 1 {
		t.Fatal("foreign author marker became proof")
	}
}

func TestSlackSameTokenAndDefinitiveVersusAmbiguousReplies(t *testing.T) {
	for _, code := range []string{"", "thread_not_found", "fatal_error", "internal_error"} {
		t.Run("code="+code, func(t *testing.T) {
			f, a, b := newSlackFixture(t)
			d, m := testDelivery(b)
			f.errorCode = code
			f.rotate = func() { os.WriteFile(b.CredentialRef, []byte("changed-token"), 0600) }
			r, _ := a.Send(context.Background(), b, d, m)
			want := channels.Uncertain
			if code == "" {
				want = channels.Delivered
			}
			if code == "thread_not_found" {
				want = channels.Failed
			}
			if r.State != want || f.count != 1 {
				t.Fatalf("state=%s want=%s", r.State, want)
			}
			for _, token := range f.tokens {
				if token != "Bearer fixture-token" {
					t.Fatal("token changed between auth and POST")
				}
			}
			d.ID = "second"
			d.MessageID = d.ID
			m.ID = d.ID
			r, _ = a.Send(context.Background(), b, d, m)
			if r.State != channels.Failed || r.Evidence.Source != "" || f.count != 1 {
				t.Fatal("switched authenticated self")
			}
		})
	}
	f, a, b := newSlackFixture(t)
	f.wrongAck = true
	d, m := testDelivery(b)
	r, _ := a.Send(context.Background(), b, d, m)
	if r.State != channels.Uncertain {
		t.Fatal("wrong thread acknowledgement trusted")
	}
}

func TestSlackPaginationWatermarkAndExpiredCursorRecovery(t *testing.T) {
	f, a, b := newSlackFixture(t)
	for _, ts := range []string{"1700000001.000001", "1700000001.000002"} {
		f.messages = append(f.messages, slackMessage{Type: "message", User: "alice", TS: ts, ThreadTS: b.RootID, Text: "authored 世界"})
	}
	first, err := a.Fetch(context.Background(), b, "", 1)
	if err != nil || len(first.Messages) != 1 || !first.HasMore || f.replyCalls != 1 {
		t.Fatal("first page", err)
	}
	second, err := a.Fetch(context.Background(), b, first.NextCursor, 1)
	if err != nil || len(second.Messages) != 1 || second.Messages[0].ID != "1700000001.000002" || second.HasMore {
		t.Fatalf("second page %+v %v", second, err)
	}
	f.expiredCursor = true
	recovered, err := a.Fetch(context.Background(), b, first.NextCursor, 1)
	if err != nil || len(recovered.Messages) != 1 || recovered.Messages[0].ID != first.Messages[0].ID || f.replyCalls != 4 {
		t.Fatal("expired cursor did not restart bounded inclusive overlap", err)
	}
	other := b
	other.ID = "other-binding"
	if _, err := a.Fetch(context.Background(), other, first.NextCursor, 1); err == nil {
		t.Fatal("cursor crossed binding")
	}
}

func TestHTTPBoundedUntrustedInputRedirectsAndRetryAfter(t *testing.T) {
	for _, input := range []string{`{"x":1,"x":2}`, `{"x":{"a":1,"a":2}}`, `{} {}`, strings.Repeat("[", 25) + "0" + strings.Repeat("]", 25)} {
		if validJSON([]byte(input)) {
			t.Fatal("unbounded/ambiguous JSON accepted")
		}
	}
	if !validJSON([]byte(`{"x":[1,"Unicode 世界",true,null]}`)) {
		t.Fatal("valid JSON rejected")
	}
	var mu sync.Mutex
	calls := 0
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.Header().Set("Retry-After", "60")
		jsonResponse(w, 429, map[string]string{"error": "private raw diagnostic"})
	}))
	defer s.Close()
	wire := newHTTP(s.Client())
	for i := 0; i < 2; i++ {
		status, err := wire.request(context.Background(), "private-sentinel-token", http.MethodPost, s.URL+"/send", map[string]string{"x": "y"}, nil)
		if err != nil || status != 429 {
			t.Fatal(status, err)
		}
	}
	if calls != 1 {
		t.Fatal("Retry-After cooldown was ignored")
	}
	redirectCalls := 0
	s.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/next" {
			redirectCalls++
		}
		http.Redirect(w, r, s.URL+"/next", 302)
	})
	status, err := wire.request(context.Background(), "private-sentinel-token", http.MethodGet, s.URL+"/redirect", nil, nil)
	if err != nil || status != 302 || redirectCalls != 0 {
		t.Fatal("authenticated redirect followed")
	}
	for _, raw := range []string{"http://example.test", "https://user:secret@example.test", "https://example.test/", "https://example.test?x=1"} {
		if _, err := endpoint(raw); err == nil {
			t.Fatal("noncanonical TLS endpoint", raw)
		}
	}
}
