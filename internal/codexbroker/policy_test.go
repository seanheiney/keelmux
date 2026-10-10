package codexbroker

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/seanheiney/agent-backplane/internal/domain"
)

func policyFixture() ConnectionBinding {
	return ConnectionBinding{ID: "connection_A", Role: NativeLocal,
		Agent:          AgentBinding{HostID: "host_A", SessionID: "session_A", ExecutionID: "execution_A", Generation: 1, AppServerIncarnation: "server_A", AppServerPID: 101, AppServerBootID: "boot_A", AppServerProcessStart: "server_start_A", ProtocolVersion: "0.159.0", NativeThreadID: "native_A"},
		Writer:         WriterRef{Lease: domain.Lease{SessionID: "session_A", ExecutionID: "execution_A", Generation: 1, OperationID: "acquire_A", ClientID: "client_A", Token: "fixture_token"}, PrincipalID: "principal_A", Kind: NativeWriter, Epoch: 7},
		NativeLaunchID: "launch_A", EndpointIncarnation: "endpoint_A", NativePID: 102, NativeProcessStart: "native_start_A"}
}

func policyFrame(t *testing.T, p Policy, raw string) Frame {
	t.Helper()
	f, err := p.Decode([]byte(raw))
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return f
}

func outputFixture(b ConnectionBinding, request Frame) OutputContext {
	return OutputContext{Recipient: b, UpstreamChannelID: "channel_A", OriginalRequest: &request, OriginOperationID: "operation_A"}
}

func TestPolicyDecodeTypedIDsAndActualShapes(t *testing.T) {
	p := NewPolicy()
	for _, id := range []string{`7`, `"7"`, `9007199254740993`, `9223372036854775807`, `-9223372036854775808`, `"emoji-\ud83d\ude00"`} {
		f := policyFrame(t, p, `{"id":`+id+`,"method":"thread/read","params":{"threadId":"native_A"}}`)
		if id == `"emoji-\ud83d\ude00"` {
			if string(f.ID) != `"emoji-😀"` {
				t.Fatalf("surrogate pair: %s", f.ID)
			}
		} else if string(f.ID) != id {
			t.Fatalf("lost ID precision/type: %s != %s", f.ID, id)
		}
	}
	f := policyFrame(t, p, `{"jsonrpc":"2.0","id":-0,"method":"configRequirements/read"}`)
	if string(f.ID) != "0" || f.Payload != nil {
		t.Fatalf("canonical ID/absent params: %#v", f)
	}
	f = policyFrame(t, p, `{"method":"initialized"}`)
	if f.Kind != Notification || f.ID != nil || f.Payload != nil {
		t.Fatalf("native notification changed: %#v", f)
	}
}

func TestPolicyDecodeMalformedEnvelopes(t *testing.T) {
	p := NewPolicy()
	bad := []string{
		`[]`, `[{"id":1,"method":"thread/read"}]`,
		`{"id":null,"method":"thread/read","params":{}}`,
		`{"id":true,"method":"thread/read","params":{}}`,
		`{"id":1.5,"method":"thread/read","params":{}}`,
		`{"id":1e0,"method":"thread/read","params":{}}`,
		`{"id":9223372036854775808,"method":"thread/read","params":{}}`,
		`{"id":"\ud800","method":"thread/read","params":{}}`,
		`{"id":"\udc00","method":"thread/read","params":{}}`,
		`{"id":"\ud800\u0041","method":"thread/read","params":{}}`,
		`{"jsonrpc":"1.0","id":1,"method":"thread/read","params":{}}`,
		`{"id":1,"id":2,"method":"thread/read","params":{}}`,
		`{"id":1,"method":"thread/read","params":{"threadId":"native_A","thread\u0049d":"native_B"}}`,
		`{"id":1,"method":"thread/read","params":{"nested":[{"x":1,"x":2}]}}`,
		`{"id":1,"method":"thread/read","params":{},"result":{}}`,
		`{"id":1,"result":{},"error":{"code":1,"message":"bad"}}`,
		`{"id":1,"result":{},"params":{}}`,
		`{"id":1,"method":"thread/read","params":{},"principal":"forged"}`,
		`{"id":1,"method":"thread/read","params":[]}`,
		`{"id":1,"method":"thread/read","params":{}} {}`,
		`{"id":1,"method":"thread/read","params":{"deep":` + strings.Repeat(`[`, 40) + `0` + strings.Repeat(`]`, 40) + `}}`,
		`{"id":1,"method":"thread/read","params":{"nodes":[` + strings.Repeat(`0,`, policyMaxNodes) + `0]}}`,
		strings.Repeat(" ", policyMaxBytes+1),
		string([]byte{'{', '"', 'i', 'd', '"', ':', '"', 0xff, '"', '}'}),
	}
	for i, raw := range bad {
		if _, err := p.Decode([]byte(raw)); err == nil {
			t.Fatalf("accepted malformed frame %d", i)
		}
	}
}

func TestPolicyObservedNotificationTimestampDoesNotGrantScope(t *testing.T) {
	p := NewPolicy()
	b := policyFixture()
	for _, stamp := range []string{"0", "1791658123000", "9223372036854775807"} {
		raw := `{"method":"remoteControl/status/changed","params":{"serverId":"PRIVATE_GLOBAL_FIXTURE","installId":"PRIVATE_INSTALL_FIXTURE"},"emittedAtMs":` + stamp + `}`
		f := policyFrame(t, p, raw)
		if f.Kind != Notification || f.Method != "remoteControl/status/changed" || len(f.ID) != 0 {
			t.Fatal("observed timestamp changed notification identity")
		}
		if _, visible, err := p.NativeOutput(OutputContext{Recipient: b, UpstreamChannelID: "channel_A"}, f); err != nil || visible {
			t.Fatal("timestamp admitted global status disclosure", err)
		}
		encoded, err := Encode(f)
		if err != nil || strings.Contains(string(encoded), "emittedAtMs") {
			t.Fatal("untrusted timestamp survived canonical frame encoding", err)
		}
	}
	for _, raw := range []string{
		`{"id":1,"method":"thread/read","params":{"threadId":"native_A"},"emittedAtMs":1}`,
		`{"id":null,"method":"initialized","emittedAtMs":1}`,
		`{"id":1,"result":{},"emittedAtMs":1}`,
		`{"id":1,"error":{"code":-32000,"message":"fixture"},"emittedAtMs":1}`,
		`{"method":"initialized","emittedAtMs":null}`,
		`{"method":"initialized","emittedAtMs":true}`,
		`{"method":"initialized","emittedAtMs":"1"}`,
		`{"method":"initialized","emittedAtMs":-1}`,
		`{"method":"initialized","emittedAtMs":1.5}`,
		`{"method":"initialized","emittedAtMs":1e3}`,
		`{"method":"initialized","emittedAtMs":9223372036854775808}`,
		`{"method":"initialized","emittedAtMs":1,"emittedAtMs":2}`,
		`{"method":"initialized","emittedAtMs":1,"unexpected":true}`,
	} {
		if _, err := p.Decode([]byte(raw)); err == nil {
			t.Fatal("invalid timestamp envelope passed pinned decoder")
		}
	}
}

func TestPolicyObservedBootstrapPositiveControls(t *testing.T) {
	p := NewPolicy()
	b := policyFixture()
	baseline := DefaultBaseline("/owned/workspace", "/private/owned/workspace")
	cases := []struct {
		raw   string
		class EffectClass
	}{
		{`{"id":"initialize","method":"initialize","params":{"clientInfo":{"name":"codex-tui","title":null,"version":"0.159.0"},"capabilities":{"experimentalApi":true,"requestAttestation":false,"optOutNotificationMethods":null}}}`, NativeBootstrap},
		{`{"method":"initialized"}`, NativeBootstrap},
		{`{"id":1,"method":"account/read","params":{}}`, NativeBootstrap},
		{`{"id":2,"method":"account/read","params":{"refreshToken":false}}`, NativeBootstrap},
		{`{"id":3,"method":"config/read","params":{"includeLayers":true,"cwd":"/private/owned/workspace"}}`, NativeBootstrap},
		{`{"id":4,"method":"configRequirements/read"}`, NativeBootstrap},
		{`{"id":5,"method":"hooks/list","params":{"cwds":["/owned/workspace"]}}`, NativeBootstrap},
		{`{"id":6,"method":"model/list","params":{"cursor":null,"limit":null,"includeHidden":true}}`, NativeBootstrap},
		{`{"id":7,"method":"collaborationMode/list","params":{}}`, NativeBootstrap},
		{`{"id":8,"method":"thread/read","params":{"threadId":"native_A"}}`, ScopedRead},
		{`{"id":9,"method":"thread/resume","params":{"threadId":"native_A","cwd":"/owned/workspace","history":null,"path":null,"model":null,"approvalPolicy":null,"excludeTurns":true,"config":{"features":{"plugins":false,"apps":false},"web_search":"cached"}}}`, NativeBootstrap},
		{`{"id":10,"method":"thread/turns/list","params":{"threadId":"native_A","cursor":null,"limit":5,"sortDirection":"desc","itemsView":"notLoaded"}}`, ScopedRead},
		{`{"id":11,"method":"thread/items/list","params":{"threadId":"native_A","turnId":null,"cursor":null,"limit":100,"sortDirection":"desc"}}`, ScopedRead},
		{`{"id":12,"method":"thread/loaded/list","params":{"cursor":null,"limit":null}}`, ScopedRead},
		{`{"id":13,"method":"thread/list","params":{"cursor":null,"limit":10,"sortKey":"recency_at","sortDirection":null,"modelProviders":[],"sourceKinds":["exec","appServer"],"originators":null,"archived":false,"cwd":null,"useStateDbOnly":true,"searchTerm":null,"parentThreadId":null,"ancestorThreadId":null}}`, ScopedRead},
		{`{"id":14,"method":"thread/goal/get","params":{"threadId":"native_A"}}`, ScopedRead},
		{`{"id":15,"method":"skills/list","params":{"cwds":["/owned/workspace"],"forceReload":true}}`, NativeBootstrap},
	}
	for _, tc := range cases {
		f := policyFrame(t, p, tc.raw)
		got, err := p.Classify(b, f, baseline)
		if err != nil || got != tc.class {
			t.Fatalf("%s: %s %v", f.Method, got, err)
		}
		if tc.class == ScopedRead {
			for _, role := range []Role{TypedObserver, TypedWriter} {
				viewer := b
				viewer.Role = role
				if class, err := p.Classify(viewer, f, baseline); err != nil || class != ScopedRead {
					t.Fatalf("%s %s: %s %v", role, f.Method, class, err)
				}
			}
		}
	}
	// The real renderer repeats safe native reads. One-shot lifecycle belongs to
	// authority and must not turn all bootstrap-only metadata into one-shot calls.
	f := policyFrame(t, p, `{"id":16,"method":"account/read","params":{}}`)
	for range 2 {
		if got, err := p.Classify(b, f, baseline); err != nil || got != NativeBootstrap {
			t.Fatalf("repeat account read: %s %v", got, err)
		}
	}
}

func TestPolicyMutationsAndOverridesDenied(t *testing.T) {
	p := NewPolicy()
	b := policyFixture()
	baseline := DefaultBaseline("/owned/workspace")
	bad := []string{
		`{"id":1,"method":"future/new","params":{}}`,
		`{"id":1,"method":"thread/read/extra","params":{"threadId":"native_A"}}`,
		`{"id":1,"method":"turn/start","params":{"threadId":"native_A","input":[]}}`,
		`{"id":1,"method":"account/read","params":{"refreshToken":true}}`,
		`{"id":1,"method":"account/read","params":{"refreshToken":null}}`,
		`{"id":1,"method":"thread/read","params":{"threadId":"native_B"}}`,
		`{"id":1,"method":"thread/read","params":{"threadId":"native_A","includeTurns":null}}`,
		`{"id":1,"method":"thread/list","params":{}}`,
		`{"id":1,"method":"thread/list","params":{"useStateDbOnly":false}}`,
		`{"id":1,"method":"thread/list","params":{"useStateDbOnly":true,"ancestorThreadId":"native_A"}}`,
		`{"id":1,"method":"skills/list","params":{"cwds":[],"forceReload":true}}`,
		`{"id":1,"method":"skills/list","params":{"cwds":["/unapproved"],"forceReload":true}}`,
		`{"id":1,"method":"config/read","params":{"includeLayers":true,"cwd":"/owned/workspace/../other"}}`,
		`{"id":1,"method":"thread/items/list","params":{"threadId":"native_A","turnId":"foreign"}}`,
		`{"id":1,"method":"thread/items/list","params":{"threadId":"native_A","cursor":{"type":"item","itemId":"foreign"}}}`,
		`{"id":1,"method":"thread/turns/list","params":{"threadId":"native_A","cursor":"foreign_cursor"}}`,
		`{"id":7,"result":{"decision":"accept"}}`,
		`{"id":7,"error":{"code":-32000,"message":"cancel"}}`,
		`{"id":"auth","result":{"accessToken":"fixture-only"}}`,
	}
	for _, raw := range bad {
		f := policyFrame(t, p, raw)
		if got, err := p.Classify(b, f, baseline); err == nil || got != Denied {
			t.Fatalf("allowed %s: %s %v", raw, got, err)
		}
	}
	base := map[string]any{"threadId": "native_A", "cwd": "/owned/workspace", "excludeTurns": true, "config": map[string]any{"features": map[string]any{"plugins": false, "apps": false}, "web_search": "cached"}}
	for _, field := range []string{"history", "path", "model", "modelProvider", "runtimeWorkspaceRoots", "approvalPolicy", "approvalsReviewer", "sandbox", "permissions", "baseInstructions", "developerInstructions", "personality", "initialTurnsPage", "serviceTier"} {
		params := map[string]any{}
		for k, v := range base {
			params[k] = v
		}
		params[field] = "unreviewed"
		raw, _ := json.Marshal(map[string]any{"id": 1, "method": "thread/resume", "params": params})
		f := policyFrame(t, p, string(raw))
		if _, err := p.Classify(b, f, baseline); err == nil {
			t.Fatalf("allowed resume %s", field)
		}
	}
	for _, config := range []string{`{"features.apps":false,"web_search":"cached"}`, `{"features":{"plugins":false,"apps":false,"unknown":false},"web_search":"cached"}`, `{"features":{"plugins":false,"apps":true},"web_search":"cached"}`, `{"features":{"plugins":false,"apps":false},"web_search":"live"}`} {
		f := policyFrame(t, p, `{"id":1,"method":"thread/resume","params":{"threadId":"native_A","cwd":"/owned/workspace","excludeTurns":true,"config":`+config+`}}`)
		if _, err := p.Classify(b, f, baseline); err == nil {
			t.Fatal("allowed unreviewed config")
		}
	}
	f := policyFrame(t, p, `{"id":1,"method":"account/read","params":{}}`)
	for _, role := range []Role{TypedObserver, TypedWriter, Role("unknown")} {
		v := b
		v.Role = role
		if _, err := p.Classify(v, f, baseline); err == nil {
			t.Fatalf("bootstrap allowed for %s", role)
		}
	}
	if _, err := p.Classify(b, f, DefaultBaseline("relative")); err == nil {
		t.Fatal("resolved relative workspace")
	}
}

func TestPolicyRequestBoundProjectionAndSafeInventory(t *testing.T) {
	p := NewPolicy()
	b := policyFixture()
	req := policyFrame(t, p, `{"id":9007199254740993,"method":"thread/read","params":{"threadId":"native_A"}}`)
	ctx := outputFixture(b, req)
	for _, raw := range []string{`{"id":"9007199254740993","result":{"thread":{"id":"native_A"}}}`, `{"id":9007199254740993,"result":{"thread":{"id":"native_B","nested":{"id":"native_A"}}}}`} {
		if _, send, _ := p.NativeOutput(ctx, policyFrame(t, p, raw)); send {
			t.Fatal("wrong typed ID/thread disclosed")
		}
	}
	response := policyFrame(t, p, `{"id":9007199254740993,"result":{"thread":{"id":"native_A","preview":"permitted","model":"configured","path":"SECRET_PATH","sessionId":"SECRET_TREE","parentThreadId":"SECRET_PARENT","extra":{"secret":"SECRET_EXTRA"},"unknown":"SECRET_UNKNOWN"},"unknown":"SECRET_TOP"}}`)
	out, send, err := p.NativeOutput(ctx, response)
	if err != nil || !send || string(out.ID) != "9007199254740993" || strings.Contains(string(out.Payload), "SECRET_") {
		t.Fatalf("thread projection: %s %v", out.Payload, err)
	}
	if !strings.Contains(string(out.Payload), `"preview":"permitted"`) {
		t.Fatal("vacuous metadata projection")
	}
	list := policyFrame(t, p, `{"id":2,"method":"thread/list","params":{"useStateDbOnly":true}}`)
	f := policyFrame(t, p, `{"id":2,"result":{"data":[{"id":"native_B","unknown":{"id":"native_A"}},{"id":"native_A","name":"selected"}],"nextCursor":"SECRET_CURSOR","backwardsCursor":"SECRET_REVERSE"}}`)
	out, send, err = p.NativeOutput(outputFixture(b, list), f)
	if err != nil || !send || strings.Contains(string(out.Payload), "SECRET") || strings.Contains(string(out.Payload), "native_B") || !strings.Contains(string(out.Payload), "selected") {
		t.Fatalf("list scope: %s %v", out.Payload, err)
	}
	f = policyFrame(t, p, `{"id":2,"result":{"data":[],"nextCursor":"not_complete"}}`)
	if _, send, err = p.NativeOutput(outputFixture(b, list), f); send || err == nil {
		t.Fatal("invented complete inventory")
	}
	errFrame := policyFrame(t, p, `{"id":9007199254740993,"error":{"code":-32000,"message":"SECRET_HOME_PATH","data":{"token":"SECRET_TOKEN"}}}`)
	out, send, err = p.NativeOutput(ctx, errFrame)
	if err != nil || !send || out.Kind != RPCError || strings.Contains(string(out.Payload), "SECRET") {
		t.Fatalf("error projection: %s %v", out.Payload, err)
	}
}

func TestPolicyBootstrapProjectionStaysNativeLocal(t *testing.T) {
	p := NewPolicy()
	b := policyFixture()
	cases := []struct{ request, response string }{
		{`{"id":1,"method":"account/read","params":{}}`, `{"id":1,"result":{"account":{"type":"chatgpt","email":"SECRET_EMAIL","planType":"pro","token":"SECRET_TOKEN"},"requiresOpenaiAuth":false,"workspaceRouting":{"chatgptAccountId":"SECRET_ACCOUNT","backendOrigin":"SECRET_ORIGIN"}}}`},
		{`{"id":1,"method":"config/read","params":{"includeLayers":true,"cwd":"/owned/workspace"}}`, `{"id":1,"result":{"config":{"model":"configured","sandbox_mode":"read-only","instructions":"SECRET_INSTRUCTIONS","api_key":"SECRET_TOKEN","mcp_servers":{"secret":"SECRET_MCP"}},"origins":{"secret":"SECRET_HOME"},"layers":[{"config":{"secret":"SECRET_LAYER"}}]}}`},
	}
	for _, tc := range cases {
		req := policyFrame(t, p, tc.request)
		response := policyFrame(t, p, tc.response)
		out, send, err := p.NativeOutput(outputFixture(b, req), response)
		if err != nil || !send || strings.Contains(string(out.Payload), "SECRET_") {
			t.Fatalf("native public projection: %s %v", out.Payload, err)
		}
		for _, role := range []Role{TypedObserver, TypedWriter} {
			view := b
			view.Role = role
			if _, send, _ := p.NativeOutput(outputFixture(view, req), response); send {
				t.Fatalf("%s bootstrap leak", role)
			}
		}
	}
}

func TestPolicyHistoryMembershipAndTypedMetadata(t *testing.T) {
	p := NewPolicy()
	b := policyFixture()
	baseline := DefaultBaseline("/owned/workspace")
	req := policyFrame(t, p, `{"id":1,"method":"thread/turns/list","params":{"threadId":"native_A","cursor":null}}`)
	response := policyFrame(t, p, `{"id":1,"result":{"data":[{"id":"turn_A","status":"completed","itemsView":"full","items":[{"type":"agentMessage","id":"item_A","text":"permitted 😀","phase":null,"memoryCitation":null,"delivery":null,"questions":null}],"error":null}],"nextCursor":null,"backwardsCursor":null}}`)
	out, send, err := p.NativeOutput(outputFixture(b, req), response)
	if err != nil || !send || !strings.Contains(string(out.Payload), "permitted 😀") {
		t.Fatalf("native text history: %s %v", out.Payload, err)
	}
	items := policyFrame(t, p, `{"id":2,"method":"thread/items/list","params":{"threadId":"native_A","turnId":"turn_A","cursor":null}}`)
	if _, err := p.Classify(b, items, baseline); err != nil {
		t.Fatal("visible turn not usable")
	}
	fresh := b
	fresh.ID = "new_connection"
	fresh.Writer.Epoch++
	if _, err := p.Classify(fresh, items, baseline); err == nil {
		t.Fatal("turn membership crossed incarnation/epoch")
	}
	view := b
	view.Role = TypedObserver
	out, send, err = p.NativeOutput(outputFixture(view, req), response)
	if err != nil || !send || strings.Contains(string(out.Payload), "permitted") || !strings.Contains(string(out.Payload), "turn_A") || !strings.Contains(string(out.Payload), "notLoaded") {
		t.Fatalf("typed metadata: %s %v", out.Payload, err)
	}
	wrong := policyFrame(t, p, `{"id":2,"result":{"data":[{"turnId":"turn_B","item":{"type":"plan","id":"item_B","text":"foreign"}}],"nextCursor":null,"backwardsCursor":null}}`)
	if _, send, _ := p.NativeOutput(outputFixture(b, items), wrong); send {
		t.Fatal("wrong-turn item delivered")
	}
	unknown := policyFrame(t, p, `{"id":2,"result":{"data":[{"turnId":"turn_A","item":{"type":"collabAgentToolCall","id":"item_B","receiverThreadIds":["native_B"]}}],"nextCursor":null}}`)
	if _, send, err := p.NativeOutput(outputFixture(b, items), unknown); send || err == nil {
		t.Fatal("unreviewed item projected")
	}
}

func TestPolicyFiniteNotificationScope(t *testing.T) {
	p := NewPolicy()
	b := policyFixture()
	ctx := OutputContext{Recipient: b, UpstreamChannelID: "channel_A"}
	for _, raw := range []string{`{"method":"thread/status/changed","params":{"threadId":"native_A","status":{"type":"idle"}}}`, `{"method":"thread/goal/cleared","params":{"threadId":"native_A"}}`} {
		if _, send, err := p.NativeOutput(ctx, policyFrame(t, p, raw)); err != nil || !send {
			t.Fatalf("known notification: %v", err)
		}
	}
	for _, raw := range []string{
		`{"method":"remoteControl/status/changed","params":{"serverName":"SECRET_SERVER","installationId":"SECRET_INSTALL","status":"disabled"}}`,
		`{"method":"future/event","params":{"threadId":"native_A"}}`,
		`{"method":"thread/status/changed","params":{"threadId":"native_B","status":{"type":"idle","nested":{"threadId":"native_A"}}}}`,
		`{"method":"thread/started","params":{"thread":{"id":"native_B","nested":{"id":"native_A"}}}}`,
		`{"id":7,"method":"item/commandExecution/requestApproval","params":{"threadId":"native_A"}}`,
	} {
		if _, send, _ := p.NativeOutput(ctx, policyFrame(t, p, raw)); send {
			t.Fatal("global/foreign/unknown notification or callback delivered")
		}
	}
}

func TestPolicyConcurrentReadProjection(t *testing.T) {
	p := NewPolicy()
	b := policyFixture()
	base := DefaultBaseline("/owned/workspace")
	req := policyFrame(t, p, `{"id":1,"method":"thread/read","params":{"threadId":"native_A"}}`)
	response := policyFrame(t, p, `{"id":1,"result":{"thread":{"id":"native_A","cwd":"/owned/workspace","turns":[]}}}`)
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				if _, err := p.Classify(b, req, base); err != nil {
					t.Error(err)
					return
				}
				if _, send, err := p.NativeOutput(outputFixture(b, req), response); err != nil || !send {
					t.Errorf("projection: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
