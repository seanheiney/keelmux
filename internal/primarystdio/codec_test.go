package primarystdio

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

// Synthetic values, with required fields taken from the pinned generated schema.
// These fixtures are protocol inputs, not native execution or authority evidence.
func fixtureThread() map[string]any {
	return map[string]any{"id": "thread-A", "cwd": "/owned/workspace", "ephemeral": false, "status": map[string]any{"type": "idle"}, "cliVersion": "0.159.0", "createdAt": 1, "updatedAt": 1, "modelProvider": "fixture", "preview": "", "projectId": nil, "sessionId": "session-A", "source": "cli", "turns": []any{}}
}
func fixtureResume() map[string]any {
	return map[string]any{"thread": fixtureThread(), "cwd": "/owned/workspace", "model": "fixture-model", "modelProvider": "fixture", "approvalPolicy": "on-request", "approvalsReviewer": "user", "sandbox": map[string]any{"type": "readOnly"}}
}
func frame(t *testing.T, v any) Envelope {
	t.Helper()
	raw, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	out, e := (Codec{}).DecodeLine(append(raw, '\n'))
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func response(t *testing.T, v any) Envelope {
	return frame(t, map[string]any{"id": "req-1", "result": v})
}
func notice(t *testing.T, method string, v any) Envelope {
	return frame(t, map[string]any{"method": method, "params": v, "emittedAtMs": 42})
}
func TestFixedBuildersAndBoundaries(t *testing.T) {
	c := Codec{}
	thread := "thread-\"\\<>&雪"
	id := "req-\"\\<>&雪"
	tests := []struct {
		name, method string
		fn           func() ([]byte, error)
		params       map[string]any
	}{
		{"init", "initialize", func() ([]byte, error) { return c.Initialize(id) }, map[string]any{"clientInfo": map[string]any{"name": "keelmux_owned_stdio", "title": "Keelmux owned stdio", "version": "0.1.0"}, "capabilities": map[string]any{"experimentalApi": true, "requestAttestation": false, "explicitGatewayOauth": true, "optOutNotificationMethods": []any{}}}},
		{"resume", "thread/resume", func() ([]byte, error) { return c.Resume(id, thread) }, map[string]any{"threadId": thread, "excludeTurns": true}},
		{"loaded", "thread/loaded/list", func() ([]byte, error) { return c.Loaded(id) }, map[string]any{"limit": float64(2)}},
		{"read", "thread/read", func() ([]byte, error) { return c.Read(id, thread) }, map[string]any{"threadId": thread, "includeTurns": false}},
		{"queue", "thread/queue/list", func() ([]byte, error) { return c.Queue(id, thread) }, map[string]any{"threadId": thread, "limit": float64(1)}},
		{"background", "thread/backgroundTerminals/list", func() ([]byte, error) { return c.Background(id, thread) }, map[string]any{"threadId": thread, "limit": float64(1)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := tt.fn()
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Count(raw, []byte{'\n'}) != 1 || raw[len(raw)-1] != '\n' {
				t.Fatal("not one complete line")
			}
			var m map[string]any
			if json.Unmarshal(raw, &m) != nil || m["id"] != id || m["method"] != tt.method || len(m) != 3 {
				t.Fatal("wrong fixed envelope")
			}
			a, _ := json.Marshal(m["params"])
			b, _ := json.Marshal(tt.params)
			if !bytes.Equal(a, b) {
				t.Fatalf("unexpected params %s", a)
			}
		})
	}
	raw, _ := c.Initialized()
	if string(raw) != "{\"method\":\"initialized\"}\n" {
		t.Fatal("initialized must not request an ack")
	}
	if _, e := c.Initialize(strings.Repeat("a", 256)); e != nil {
		t.Fatal(e)
	}
	for _, s := range []string{"", strings.Repeat("a", 257), "x\n", "x\x00", string([]byte{255})} {
		if _, e := c.Initialize(s); !errors.Is(e, ErrInvalid) {
			t.Fatal("bad ID accepted")
		}
		if _, e := c.Resume("r", s); !errors.Is(e, ErrInvalid) {
			t.Fatal("bad thread accepted")
		}
	}
}
func TestEnvelopeVariantsAndCorrelation(t *testing.T) {
	c := Codec{}
	for _, raw := range []string{`{"id":"r","result":null}` + "\n", `{"jsonrpc":"2.0","id":-9223372036854775808,"error":{"code":-32600,"message":"private secret","data":{"x":1}}}` + "\r\n", `{"id":9223372036854775807,"method":"currentTime/read","params":{},"trace":{"traceparent":null}}` + "\n", `{"method":"thread/queue/changed","params":{"threadId":"t"},"emittedAtMs":0}` + "\n", `{"id":"","method":"callback"}` + "\n"} {
		if _, err := c.DecodeLine([]byte(raw)); err != nil {
			t.Fatalf("legal shape rejected: %v", err)
		}
	}
	e := response(t, map[string]any{"data": []any{}, "nextCursor": nil})
	if _, err := c.ParseLoaded(e, "other"); !errors.Is(err, ErrConflict) {
		t.Fatal("wrong ID accepted")
	}
	e = frame(t, map[string]any{"id": 1, "result": map[string]any{"data": []any{}}})
	if _, err := c.ParseLoaded(e, "1"); !errors.Is(err, ErrConflict) {
		t.Fatal("numeric/string ID conflated")
	}
	e = frame(t, map[string]any{"id": "req-1", "error": map[string]any{"code": -1, "message": "private-token-marker"}})
	_, err := c.ParseLoaded(e, "req-1")
	if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "private-token") {
		t.Fatal("native error leaked or became success")
	}
}
func TestMalformedEnvelopesAndUnicode(t *testing.T) {
	bad := []string{`{"id":"r","result":{}}`, `{"id":null,"result":{}}` + "\n", `{"id":true,"result":{}}` + "\n", `{"id":1.0,"result":{}}` + "\n", `{"id":9223372036854775808,"result":{}}` + "\n", `{"id":"r","result":{},"error":{}}` + "\n", `{"id":"r","result":{},"method":"m"}` + "\n", `{"method":"m","params":null}` + "\n", `{"id":"r","result":{},"emittedAtMs":1}` + "\n", `{"method":"m","emittedAtMs":-1}` + "\n", `{"method":"m","emittedAtMs":1.5}` + "\n", `{"method":"m","id":"r","emittedAtMs":1}` + "\n", `{"method":"m","params":{},"trace":null}` + "\n", `{"id":"r","result":{},"jsonrpc":"1.0"}` + "\n", `{"id":"r","result":{},"x":1}` + "\n", `{"id":"r","result":{"a":1,"\u0061":2}}` + "\n", `{"id":"r","id":"r","result":{}}` + "\n", `{"id":"\ud800","result":{}}` + "\n", `{"id":"\udc00","result":{}}` + "\n", `{"id":"\ud800\u0041","result":{}}` + "\n", "{\"id\":\"r\",\"result\":\"\xff\"}\n", "{}\n{}\n", `{"id":"r","result":` + strings.Repeat("[", 34) + strings.Repeat("]", 34) + "}\n", `{"id":"r","result":[` + strings.Repeat("0,", 65536) + "0]}\n"}
	for i, raw := range bad {
		if _, e := (Codec{}).DecodeLine([]byte(raw)); !errors.Is(e, ErrInvalid) {
			t.Fatalf("case %d accepted", i)
		}
	}
	if _, e := (Codec{}).DecodeLine([]byte(`{"id":"\ud83d\ude00","result":{}}` + "\n")); e != nil {
		t.Fatal("valid pair rejected", e)
	}
	base := `{"id":"r","result":null}`
	raw := []byte(base + strings.Repeat(" ", MaxFrameBytes-len(base)-1) + "\n")
	if _, e := (Codec{}).DecodeLine(raw); e != nil {
		t.Fatal("exact frame limit rejected", e)
	}
	raw = append(raw[:len(raw)-1], ' ', '\n')
	if _, e := (Codec{}).DecodeLine(raw); e == nil {
		t.Fatal("oversize accepted")
	}
}
func TestPinnedResultsPreserveEvidenceStates(t *testing.T) {
	c := Codec{}
	init := map[string]any{"userAgent": "native", "codexHome": "/owned/home", "platformFamily": "unix", "platformOs": "linux"}
	r, e := c.ParseInitialize(response(t, init), "req-1")
	if e != nil || r.Home != "/owned/home" {
		t.Fatal(r, e)
	}
	for _, state := range []string{"idle", "notLoaded", "systemError", "active"} {
		m := fixtureResume()
		th := m["thread"].(map[string]any)
		status := map[string]any{"type": state}
		if state == "active" {
			status["activeFlags"] = []string{"waitingOnApproval", "waitingOnUserInput"}
		}
		th["status"] = status
		got, e := c.ParseResume(response(t, m), "req-1")
		if e != nil || string(got.Status) != state || got.ResumeCWD != "/owned/workspace" {
			t.Fatal(got, e)
		}
		read, e := c.ParseRead(response(t, map[string]any{"thread": th}), "req-1")
		if e != nil || read.ResumeCWD != "" || string(read.Status) != state {
			t.Fatal(read, e)
		}
	}
	for _, entry := range []struct {
		state   PageCursorState
		value   any
		present bool
	}{{PageCursorMissing, nil, false}, {PageCursorNull, nil, true}, {PageCursorValue, "next", true}, {PageCursorValue, "", true}} {
		m := map[string]any{"data": []any{}}
		if entry.present {
			m["nextCursor"] = entry.value
		}
		env := response(t, m)
		loaded, e := c.ParseLoaded(env, "req-1")
		if e != nil || loaded.Cursor.State != entry.state {
			t.Fatal(loaded, e)
		}
		for _, parse := range []func(Envelope, string) (ListResult, error){c.ParseQueue, c.ParseBackground} {
			v, e := parse(env, "req-1")
			if e != nil || v.Count != 0 || v.Cursor.State != entry.state {
				t.Fatal(v, e)
			}
		}
	}
	loaded, e := c.ParseLoaded(response(t, map[string]any{"data": []string{"thread-A", "foreign"}, "nextCursor": nil}), "req-1")
	if e != nil || len(loaded.IDs) != 2 {
		t.Fatal("codec must not decide unique-thread readiness", e)
	}
	queued := map[string]any{"id": "q", "input": []any{}, "clientUserMessageId": "message"}
	q, e := c.ParseQueue(response(t, map[string]any{"data": []any{queued}, "nextCursor": nil}), "req-1")
	if e != nil || q.Count != 1 {
		t.Fatal(q, e)
	}
	bg := map[string]any{"itemId": "i", "processId": "p", "command": "private", "cwd": "/owned", "osPid": 42, "cpuPercent": 0.5, "rssKb": uint64(18446744073709551615)}
	q, e = c.ParseBackground(response(t, map[string]any{"data": []any{bg}}), "req-1")
	if e != nil || q.Count != 1 {
		t.Fatal(q, e)
	}
}
func TestPinnedSchemasRefuseMissingAndConflictingFields(t *testing.T) {
	c := Codec{}
	for _, key := range []string{"id", "cwd", "status", "ephemeral", "cliVersion", "createdAt", "updatedAt", "modelProvider", "preview", "projectId", "sessionId", "source", "turns"} {
		th := fixtureThread()
		delete(th, key)
		if _, e := c.ParseRead(response(t, map[string]any{"thread": th}), "req-1"); e == nil {
			t.Fatal("missing required thread field", key)
		}
	}
	for _, key := range []string{"thread", "cwd", "model", "modelProvider", "approvalPolicy", "approvalsReviewer", "sandbox"} {
		m := fixtureResume()
		delete(m, key)
		if _, e := c.ParseResume(response(t, m), "req-1"); e == nil {
			t.Fatal("missing required resume field", key)
		}
	}
	for _, change := range []func(map[string]any){func(m map[string]any) { m["createdAt"] = "1" }, func(m map[string]any) { m["createdAt"] = json.Number("9223372036854775808") }, func(m map[string]any) { m["status"] = map[string]any{"type": "active"} }, func(m map[string]any) {
		m["status"] = map[string]any{"type": "active", "activeFlags": []string{"invented"}}
	}, func(m map[string]any) { m["status"] = map[string]any{"type": "idle", "activeFlags": []string{}} }, func(m map[string]any) { m["invented"] = true }, func(m map[string]any) { m["turns"] = []any{map[string]any{}} }, func(m map[string]any) { m["source"] = map[string]any{"custom": "x", "subAgent": "x"} }} {
		m := fixtureThread()
		change(m)
		if _, e := c.ParseRead(response(t, map[string]any{"thread": m}), "req-1"); e == nil {
			t.Fatal("bad nested schema accepted")
		}
	}
	for _, m := range []any{map[string]any{}, map[string]any{"data": nil}, map[string]any{"data": []any{}, "nextCursor": false}, map[string]any{"data": []any{}, "extra": 1}} {
		if _, e := c.ParseLoaded(response(t, m), "req-1"); e == nil {
			t.Fatal("bad list accepted")
		}
	}
}
func TestNotificationClassificationNeverApproves(t *testing.T) {
	c := Codec{}
	cases := []struct {
		method string
		params any
		kind   NoticeKind
	}{{"thread/started", map[string]any{"thread": fixtureThread()}, NoticeThreadStarted}, {"thread/status/changed", map[string]any{"threadId": "foreign", "status": map[string]any{"type": "active", "activeFlags": []string{"waitingOnApproval"}}}, NoticeThreadStatus}, {"thread/queue/changed", map[string]any{"threadId": "thread-A"}, NoticeQueueChanged}, {"remoteControl/status/changed", map[string]any{"status": "connected", "serverName": "private", "installationId": "private"}, NoticeRemoteStatus}, {"serverRequest/resolved", map[string]any{"requestId": 1}, NoticeUnsupported}, {"future/notification", map[string]any{}, NoticeUnsupported}}
	for _, tt := range cases {
		n, e := c.ClassifyNotification(notice(t, tt.method, tt.params))
		if e != nil || n.Kind != tt.kind {
			t.Fatal(tt.method, n, e)
		}
	}
	if _, e := c.ClassifyNotification(notice(t, "thread/status/changed", map[string]any{"threadId": "t", "status": map[string]any{"type": "madeUp"}})); e == nil {
		t.Fatal("unknown status accepted")
	}
	for _, method := range []string{"item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/tool/requestUserInput", "mcpServer/elicitation/request", "item/permissions/requestApproval", "item/tool/call", "account/chatgptAuthTokens/refresh", "attestation/generate", "currentTime/read", "applyPatchApproval", "execCommandApproval"} {
		e := frame(t, map[string]any{"id": 1, "method": method, "params": map[string]any{}})
		if e.Kind != Request {
			t.Fatal("callback lost identity")
		}
		if _, err := c.ClassifyNotification(e); err == nil {
			t.Fatal("callback became notice")
		}
	}
}
func TestSchemaPinAndConcurrentStatelessUse(t *testing.T) {
	sum := sha256.Sum256([]byte(pinnedSchemaJSON))
	if hex.EncodeToString(sum[:]) != CodecSchemaSHA256 {
		t.Fatal("embedded schema changed without reviewed pin")
	}
	env := response(t, fixtureResume())
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				r, e := (Codec{}).ParseResume(env, "req-1")
				if e != nil || r.ID != "thread-A" {
					t.Error("stateless parse failed", e)
				}
			}
		}()
	}
	wg.Wait()
}
func FuzzCodecNeverPanics(f *testing.F) {
	for _, s := range []string{"{\"id\":\"r\",\"result\":{}}\n", "{\"method\":\"thread/status/changed\",\"params\":{}}\n", "{\"id\":0,\"method\":\"currentTime/read\"}\n"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		c := Codec{}
		e, err := c.DecodeLine(raw)
		if err != nil {
			return
		}
		_, _ = c.ParseInitialize(e, "r")
		_, _ = c.ParseResume(e, "r")
		_, _ = c.ParseRead(e, "r")
		_, _ = c.ParseLoaded(e, "r")
		_, _ = c.ParseQueue(e, "r")
		_, _ = c.ParseBackground(e, "r")
		_, _ = c.ClassifyNotification(e)
	})
}
