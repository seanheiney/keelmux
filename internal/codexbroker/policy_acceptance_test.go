package codexbroker_test

// These tests are independent policy-unit acceptance evidence for the bounded
// Codex 0.159.0 zero-turn profile. The bootstrap fixture preserves 21 observed
// native client request shapes across 16 methods, with synthetic identities and
// paths. No test starts a native CLI, submits a model turn, exercises a real
// relay, verifies process provenance or proves a live authority/revocation gate.
// In particular, every downstream approval/auth/tool reply remains denied;
// successful callback forwarding belongs to a later actor/relay profile.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	cb "github.com/seanheiney/keelmux/internal/codexbroker"
	"github.com/seanheiney/keelmux/internal/domain"
)

func acceptanceBinding(role cb.Role) cb.ConnectionBinding {
	return cb.ConnectionBinding{
		ID: "connection_acceptance", Role: role,
		Agent: cb.AgentBinding{
			HostID: "host_acceptance", SessionID: "session_acceptance", ExecutionID: "execution_acceptance",
			Generation: 1, AppServerIncarnation: "app_acceptance", AppServerPID: 1001,
			AppServerBootID: "boot_fixture", AppServerProcessStart: "start_fixture",
			ProtocolVersion: "0.159.0", NativeThreadID: "native_A",
		},
		Writer: cb.WriterRef{
			Lease: domain.Lease{
				SessionID: "session_acceptance", ExecutionID: "execution_acceptance", Generation: 1,
				OperationID: "acquisition_fixture", ClientID: "client_fixture", Token: "fixture_only_not_a_real_token",
			},
			PrincipalID: "principal_acceptance", Kind: cb.NativeWriter, Epoch: 1,
		},
		NativeLaunchID: "launch_fixture", EndpointIncarnation: "endpoint_fixture",
		NativePID: 1002, NativeProcessStart: "native_start_fixture",
	}
}

func acceptanceBaseline() cb.Baseline {
	return cb.DefaultBaseline("/owned/workspace", "/private/owned/workspace")
}

func acceptanceJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func acceptanceDecode(t *testing.T, p cb.Policy, raw string) cb.Frame {
	t.Helper()
	f, err := p.Decode([]byte(raw))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	return f
}

func acceptanceRequest(t *testing.T, method string, params any) cb.Frame {
	t.Helper()
	return acceptanceDecode(t, cb.NewPolicy(), string(acceptanceJSON(t, map[string]any{
		"id": "upstream-42", "method": method, "params": params,
	})))
}

func acceptanceContext(t *testing.T, role cb.Role, method string, params any) cb.OutputContext {
	t.Helper()
	f := acceptanceRequest(t, method, params)
	return cb.OutputContext{
		Recipient: acceptanceBinding(role), UpstreamChannelID: "upstream_channel_fixture",
		OriginalRequest: &f,
	}
}

func acceptanceResult(t *testing.T, result any) cb.Frame {
	t.Helper()
	return cb.Frame{Kind: cb.Result, ID: json.RawMessage(`"upstream-42"`), Payload: acceptanceJSON(t, result)}
}

func acceptanceObject(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("projected payload is not an object: %v", err)
	}
	return v
}

func acceptanceAllowed(t *testing.T, p cb.Policy, b cb.ConnectionBinding, f cb.Frame, baseline cb.Baseline) cb.EffectClass {
	t.Helper()
	class, err := p.Classify(b, f, baseline)
	if err != nil || (class != cb.NativeBootstrap && class != cb.ScopedRead && class != cb.ViewerLocal) {
		t.Fatalf("expected bounded bootstrap/read acceptance, got class=%q err=%v", class, err)
	}
	return class
}

func acceptanceDenied(t *testing.T, p cb.Policy, b cb.ConnectionBinding, raw []byte, baseline cb.Baseline) {
	t.Helper()
	f, err := p.Decode(raw)
	if err != nil {
		return // Syntax/schema refusal is also an effective denial.
	}
	class, err := p.Classify(b, f, baseline)
	if err == nil && class != cb.Denied {
		t.Fatalf("forbidden request accepted as %q: %s", class, raw)
	}
}

func acceptanceOutput(t *testing.T, p cb.Policy, c cb.OutputContext, f cb.Frame) cb.Frame {
	t.Helper()
	// A production reply follows request classification. This also supplies the
	// home-verified workspace baseline used to project native cwd fields. Known
	// unsolicited notifications follow an already classified connection request.
	request := acceptanceRequest(t, "thread/read", map[string]any{"threadId": c.Recipient.Agent.NativeThreadID})
	if c.OriginalRequest != nil {
		request = *c.OriginalRequest
	}
	acceptanceAllowed(t, p, c.Recipient, request, acceptanceBaseline())
	out, ok, err := p.NativeOutput(c, f)
	if err != nil || !ok {
		t.Fatalf("expected useful scoped projection: delivered=%v err=%v", ok, err)
	}
	if out.Kind != f.Kind || !bytes.Equal(out.ID, f.ID) {
		t.Fatalf("projection changed frame kind or typed upstream ID: %+v", out)
	}
	return out
}

func acceptanceWithheld(t *testing.T, p cb.Policy, c cb.OutputContext, f cb.Frame) {
	t.Helper()
	_, ok, err := p.NativeOutput(c, f)
	if ok {
		t.Fatalf("forbidden output delivered, err=%v", err)
	}
}

func acceptanceBootstrapFrames(t *testing.T) []json.RawMessage {
	t.Helper()
	var fixture struct {
		ProtocolVersion string            `json:"protocol_version"`
		Frames          []json.RawMessage `json:"frames"`
	}
	raw, err := os.ReadFile("testdata/codex_0159_bootstrap.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.ProtocolVersion != "0.159.0" || len(fixture.Frames) != 21 {
		t.Fatal("saved capture fixture version/count drift")
	}
	return fixture.Frames
}

func TestPolicyAcceptanceObservedNativeBootstrap(t *testing.T) {
	methods := map[string]bool{}
	for i, raw := range acceptanceBootstrapFrames(t) {
		f := acceptanceDecode(t, cb.NewPolicy(), string(raw))
		methods[f.Method] = true
		t.Run(fmt.Sprintf("%02d_%s", i, f.Method), func(t *testing.T) {
			p := cb.NewPolicy()
			acceptanceAllowed(t, p, acceptanceBinding(cb.NativeLocal), f, acceptanceBaseline())
		})
	}
	if len(methods) != 16 {
		t.Fatalf("expected 16 distinct observed methods, got %d", len(methods))
	}
}

func TestPolicyAcceptanceScopedReadRoles(t *testing.T) {
	for _, role := range []cb.Role{cb.NativeLocal, cb.TypedObserver, cb.TypedWriter} {
		for _, method := range []string{"thread/read", "thread/turns/list", "thread/items/list", "thread/goal/get"} {
			t.Run(string(role)+"/"+method, func(t *testing.T) {
				p := cb.NewPolicy()
				f := acceptanceRequest(t, method, map[string]any{"threadId": "native_A"})
				if class := acceptanceAllowed(t, p, acceptanceBinding(role), f, acceptanceBaseline()); class != cb.ScopedRead {
					t.Fatalf("thread-scoped read classified %q", class)
				}
			})
		}
	}
	nativeMethods := map[string]bool{"initialize": true, "initialized": true, "thread/resume": true, "account/read": true, "config/read": true, "configRequirements/read": true, "model/list": true, "collaborationMode/list": true, "skills/list": true, "hooks/list": true}
	for _, role := range []cb.Role{cb.TypedObserver, cb.TypedWriter} {
		for i, raw := range acceptanceBootstrapFrames(t) {
			f := acceptanceDecode(t, cb.NewPolicy(), string(raw))
			if !nativeMethods[f.Method] {
				continue
			}
			t.Run(fmt.Sprintf("deny_native_privilege/%s/%02d_%s", role, i, f.Method), func(t *testing.T) {
				// The exact same wire frame is first accepted for NativeLocal;
				// malformed parameters cannot make this role check pass vacuously.
				p := cb.NewPolicy()
				acceptanceAllowed(t, p, acceptanceBinding(cb.NativeLocal), f, acceptanceBaseline())
				class, err := p.Classify(acceptanceBinding(role), f, acceptanceBaseline())
				if err == nil && class != cb.Denied {
					t.Fatalf("native privilege granted to %s as %s", role, class)
				}
			})
		}
	}
}

func TestPolicyAcceptanceDecodeTypedIDsAndNativeEnvelope(t *testing.T) {
	p := cb.NewPolicy()
	for _, id := range []string{`"7"`, `7`, `9007199254740993`, `9223372036854775807`, `-9223372036854775808`, `"native-request"`} {
		for _, marker := range []string{"", `"jsonrpc":"2.0",`} {
			t.Run(id+marker, func(t *testing.T) {
				f := acceptanceDecode(t, p, `{`+marker+`"id":`+id+`,"method":"thread/read","params":{"threadId":"native_A"}}`)
				if f.Kind != cb.Request || string(f.ID) != id {
					t.Fatalf("typed ID changed: want %s got %s kind %s", id, f.ID, f.Kind)
				}
				acceptanceAllowed(t, p, acceptanceBinding(cb.NativeLocal), f, acceptanceBaseline())
			})
		}
	}
	for _, raw := range []string{`{"method":"initialized"}`, `{"id":7,"method":"configRequirements/read"}`, `{"id":7,"method":"account/read","params":{}}`} {
		f := acceptanceDecode(t, p, raw)
		acceptanceAllowed(t, p, acceptanceBinding(cb.NativeLocal), f, acceptanceBaseline())
	}
	// Whitespace in a numeric ID must not become a separate authority identity.
	f := acceptanceDecode(t, p, `{"id": 7 ,"method":"thread/read","params":{"threadId":"native_A"}}`)
	if string(f.ID) != "7" {
		t.Fatalf("integer ID is not canonical: %q", f.ID)
	}
	for _, id := range []struct{ wire, canonical string }{
		{`"\u0037"`, `"7"`}, {`"\ud83d\ude00"`, `"😀"`}, {`-0`, `0`},
	} {
		f := acceptanceDecode(t, p, `{"id":`+id.wire+`,"method":"thread/read","params":{"threadId":"native_A"}}`)
		if string(f.ID) != id.canonical {
			t.Fatalf("equivalent ID is not canonical: want %s got %s", id.canonical, f.ID)
		}
	}
}

func TestPolicyAcceptanceRejectsAmbiguousWireFrames(t *testing.T) {
	cases := map[string][]byte{
		"batch":       []byte(`[{"id":1,"method":"thread/read","params":{"threadId":"native_A"}},{"id":2,"method":"turn/start","params":{"threadId":"native_A","input":[]}}]`),
		"empty_batch": []byte(`[]`), "null": []byte(`null`), "trailing_object": []byte(`{"method":"initialized"}{"method":"initialized"}`),
		"duplicate_id":             []byte(`{"id":1,"id":2,"method":"thread/read","params":{"threadId":"native_A"}}`),
		"duplicate_method":         []byte(`{"id":1,"method":"thread/read","method":"turn/start","params":{"threadId":"native_A"}}`),
		"duplicate_target":         []byte(`{"id":1,"method":"thread/read","params":{"threadId":"native_A","threadId":"native_B"}}`),
		"escaped_duplicate_target": []byte(`{"id":1,"method":"thread/read","params":{"threadId":"native_A","thread\u0049d":"native_B"}}`),
		"nested_config_duplicate":  []byte(`{"id":1,"method":"thread/resume","params":{"threadId":"native_A","config":{"features":{"apps":false,"apps":true}}}}`),
		"request_and_result":       []byte(`{"id":1,"method":"thread/read","params":{"threadId":"native_A"},"result":{}}`),
		"request_and_error":        []byte(`{"id":1,"method":"thread/read","error":{"code":-1,"message":"fixture"}}`),
		"result_and_error":         []byte(`{"id":1,"result":{},"error":{"code":-1,"message":"fixture"}}`),
		"params_with_result":       []byte(`{"id":1,"params":{},"result":{}}`),
		"unsupported_jsonrpc":      []byte(`{"jsonrpc":"1.0","id":1,"method":"thread/read","params":{"threadId":"native_A"}}`),
		"null_id":                  []byte(`{"id":null,"method":"thread/read","params":{"threadId":"native_A"}}`),
		"bool_id":                  []byte(`{"id":true,"method":"thread/read","params":{"threadId":"native_A"}}`),
		"fraction_id":              []byte(`{"id":1.5,"method":"thread/read","params":{"threadId":"native_A"}}`),
		"exponent_id":              []byte(`{"id":1e0,"method":"thread/read","params":{"threadId":"native_A"}}`),
		"integer_overflow":         []byte(`{"id":9223372036854775808,"method":"thread/read","params":{"threadId":"native_A"}}`),
		"unpaired_surrogate_id":    []byte(`{"id":"\ud800","method":"thread/read","params":{"threadId":"native_A"}}`),
		"unpaired_surrogate_key":   []byte(`{"id":1,"method":"thread/read","params":{"threadId":"native_A","\udc00":1}}`),
		"invalid_utf8":             append([]byte(`{"id":"`), append([]byte{0xff}, []byte(`","method":"thread/read","params":{"threadId":"native_A"}}`)...)...),
		"excessive_depth":          []byte(`{"id":1,"method":"thread/read","params":{"threadId":"native_A","extra":` + strings.Repeat(`{"x":`, 1000) + `0` + strings.Repeat(`}`, 1000) + `}}`),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := cb.NewPolicy().Decode(raw); err == nil {
				t.Fatal("ambiguous/malformed envelope was decoded")
			}
		})
	}
	for _, params := range []string{`[]`, `null`, `"native_A"`, `7`, `true`} {
		t.Run("invalid_params/"+params, func(t *testing.T) {
			acceptanceDenied(t, cb.NewPolicy(), acceptanceBinding(cb.NativeLocal), []byte(`{"id":1,"method":"thread/read","params":`+params+`}`), acceptanceBaseline())
		})
	}
}

func acceptanceResumeParams() map[string]any {
	return map[string]any{
		"threadId": "native_A", "history": nil, "path": nil, "cwd": "/owned/workspace", "excludeTurns": true,
		"config": map[string]any{"features": map[string]any{"plugins": false, "apps": false}, "web_search": "cached"},
	}
}

func TestPolicyAcceptanceResumeIsParameterBound(t *testing.T) {
	// This observed nonempty configuration is a positive control: denying all
	// resume configuration would hide the override checks behind blanket refusal.
	for _, cwd := range []string{"/owned/workspace", "/private/owned/workspace"} {
		p := acceptanceResumeParams()
		p["cwd"] = cwd
		acceptanceAllowed(t, cb.NewPolicy(), acceptanceBinding(cb.NativeLocal), acceptanceRequest(t, "thread/resume", p), acceptanceBaseline())
	}
	cases := map[string]any{
		"threadId": "native_B", "history": []any{map[string]any{"type": "fixture"}}, "path": "/other/rollout.jsonl",
		"model": "unreviewed-model", "modelProvider": "unreviewed-provider", "runtimeWorkspaceRoots": []string{"/ungranted"},
		"approvalPolicy": "never", "approvalsReviewer": "unreviewed-reviewer", "sandbox": "danger-full-access",
		"permissions": "unreviewed-profile", "baseInstructions": "fixture override", "developerInstructions": "fixture override",
		"personality": "unreviewed", "initialTurnsPage": map[string]any{"limit": 1}, "excludeTurns": false,
		"cwd": "/owned/workspace/../other", "unknownFutureOverride": true,
	}
	for field, value := range cases {
		t.Run(field, func(t *testing.T) {
			p := acceptanceResumeParams()
			p[field] = value
			acceptanceDenied(t, cb.NewPolicy(), acceptanceBinding(cb.NativeLocal), acceptanceJSON(t, map[string]any{"id": 1, "method": "thread/resume", "params": p}), acceptanceBaseline())
		})
	}
	for name, config := range map[string]any{
		"arbitrary":       map[string]any{"approval_policy": "never"},
		"dotted":          map[string]any{"features.apps": true},
		"apps_enabled":    map[string]any{"features": map[string]any{"plugins": false, "apps": true}, "web_search": "cached"},
		"plugins_enabled": map[string]any{"features": map[string]any{"plugins": true, "apps": false}, "web_search": "cached"},
		"web_live":        map[string]any{"features": map[string]any{"plugins": false, "apps": false}, "web_search": "live"},
		"unknown_nested":  map[string]any{"features": map[string]any{"plugins": false, "apps": false, "future": true}, "web_search": "cached"},
	} {
		t.Run("config/"+name, func(t *testing.T) {
			p := acceptanceResumeParams()
			p["config"] = config
			acceptanceDenied(t, cb.NewPolicy(), acceptanceBinding(cb.NativeLocal), acceptanceJSON(t, map[string]any{"id": 1, "method": "thread/resume", "params": p}), acceptanceBaseline())
		})
	}
	for _, role := range []cb.Role{cb.TypedObserver, cb.TypedWriter} {
		acceptanceDenied(t, cb.NewPolicy(), acceptanceBinding(role), acceptanceJSON(t, map[string]any{"id": 1, "method": "thread/resume", "params": acceptanceResumeParams()}), acceptanceBaseline())
	}
}

func TestPolicyAcceptanceReadNamesDoNotGrantEffects(t *testing.T) {
	cases := []struct {
		name, method string
		params       any
	}{
		{"unknown", "future/newMethod", map[string]any{}},
		{"prefix", "thread/read/extra", map[string]any{"threadId": "native_A"}},
		{"missing_thread", "thread/read", map[string]any{}},
		{"other_thread", "thread/read", map[string]any{"threadId": "native_B"}},
		{"unknown_read_flag", "thread/read", map[string]any{"threadId": "native_A", "followOtherThread": true}},
		{"auth_refresh", "account/read", map[string]any{"refreshToken": true}},
		{"config_other_cwd", "config/read", map[string]any{"cwd": "/ungranted", "includeLayers": true}},
		{"list_scan_repair", "thread/list", map[string]any{"useStateDbOnly": false}},
		{"list_implicit_scan_repair", "thread/list", map[string]any{}},
		{"list_foreign_cursor", "thread/list", map[string]any{"useStateDbOnly": true, "cursor": "foreign_cursor"}},
		{"loaded_foreign_cursor", "thread/loaded/list", map[string]any{"cursor": "foreign_cursor"}},
		{"turns_foreign_cursor", "thread/turns/list", map[string]any{"threadId": "native_A", "cursor": "foreign_cursor"}},
		{"items_foreign_cursor", "thread/items/list", map[string]any{"threadId": "native_A", "turnId": nil, "cursor": "foreign_cursor"}},
		{"items_unproved_anchor", "thread/items/list", map[string]any{"threadId": "native_A", "turnId": nil, "cursor": map[string]any{"type": "item", "itemId": "item_A"}}},
		{"models_foreign_cursor", "model/list", map[string]any{"cursor": "foreign_cursor", "includeHidden": true}},
		{"skills_other_cwd", "skills/list", map[string]any{"cwds": []string{"/ungranted"}, "forceReload": true}},
		{"skills_mixed_cwds", "skills/list", map[string]any{"cwds": []string{"/owned/workspace", "/ungranted"}, "forceReload": true}},
		{"hooks_other_cwd", "hooks/list", map[string]any{"cwds": []string{"/ungranted"}}},
		{"turn_start", "turn/start", map[string]any{"threadId": "native_A", "input": []any{map[string]any{"type": "text", "text": "fixture-only-do-not-execute"}}}},
		{"turn_steer", "turn/steer", map[string]any{"threadId": "native_A", "expectedTurnId": "turn_A", "input": []any{}}},
		{"shell_command", "thread/shellCommand", map[string]any{"threadId": "native_A", "command": "fixture-only"}},
		{"command", "command/exec", map[string]any{"command": []string{"fixture-only"}}},
		{"config_write", "config/value/write", map[string]any{"keyPath": "fixture", "value": true, "mergeStrategy": "replace"}},
		{"account_login", "account/login/start", map[string]any{"type": "chatgpt"}},
		{"account_logout", "account/logout", map[string]any{}},
		{"skills_mutation", "skills/config/write", map[string]any{"path": "/owned/workspace/fixture", "enabled": true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			acceptanceDenied(t, cb.NewPolicy(), acceptanceBinding(cb.NativeLocal), acceptanceJSON(t, map[string]any{"id": 1, "method": c.method, "params": c.params}), acceptanceBaseline())
		})
	}
	for name, capabilities := range map[string]any{
		"oauth":       map[string]any{"experimentalApi": true, "requestAttestation": false, "explicitGatewayOauth": true},
		"attestation": map[string]any{"experimentalApi": true, "requestAttestation": true},
		"unknown":     map[string]any{"experimentalApi": true, "requestAttestation": false, "futureCapability": true},
	} {
		t.Run("initialize/"+name, func(t *testing.T) {
			acceptanceDenied(t, cb.NewPolicy(), acceptanceBinding(cb.NativeLocal), acceptanceJSON(t, map[string]any{"id": 1, "method": "initialize", "params": map[string]any{"clientInfo": map[string]any{"name": "codex-tui", "version": "0.159.0"}, "capabilities": capabilities}}), acceptanceBaseline())
		})
	}
	for _, change := range []string{"binding_version", "baseline_version", "empty_selected_thread"} {
		t.Run(change, func(t *testing.T) {
			b, baseline := acceptanceBinding(cb.NativeLocal), acceptanceBaseline()
			switch change {
			case "binding_version":
				b.Agent.ProtocolVersion = "future"
			case "baseline_version":
				baseline.ProtocolVersion = "future"
			case "empty_selected_thread":
				b.Agent.NativeThreadID = ""
			}
			acceptanceDenied(t, cb.NewPolicy(), b, []byte(`{"id":1,"method":"thread/read","params":{"threadId":"native_A"}}`), baseline)
		})
	}
	b := acceptanceBinding(cb.Role("unreviewed_future_role"))
	acceptanceDenied(t, cb.NewPolicy(), b, []byte(`{"id":1,"method":"thread/read","params":{"threadId":"native_A"}}`), acceptanceBaseline())
}

func TestPolicyAcceptanceAllDownstreamRepliesDenied(t *testing.T) {
	// Decode both response variants so this assertion exercises classification,
	// rather than treating every methodless frame as malformed. This first
	// profile grants no callback-response capability, including an apparently
	// valid, current command approval. Actor attribution/replay gates are later.
	frames := []string{
		`{"id":7,"result":{"decision":"accept"}}`,
		`{"id":"7","result":{"decision":"accept"}}`,
		`{"id":7,"result":{"decision":"acceptForSession"}}`,
		`{"id":7,"result":{"decision":{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["fixture"]}}}}`,
		`{"id":7,"result":{"permissions":{"network":{"enabled":true}},"scope":"session"}}`,
		`{"id":"auth_fixture","result":{"accessToken":"NON_SECRET_FIXTURE"}}`,
		`{"id":7,"result":{"decision":true}}`,
		`{"id":7,"error":{"code":-32000,"message":"fixture cancellation"}}`,
	}
	for _, role := range []cb.Role{cb.NativeLocal, cb.TypedObserver, cb.TypedWriter} {
		for i, raw := range frames {
			t.Run(fmt.Sprintf("%s/%d", role, i), func(t *testing.T) {
				p := cb.NewPolicy()
				f := acceptanceDecode(t, p, raw)
				if f.Kind != cb.Result && f.Kind != cb.RPCError {
					t.Fatalf("wrong response kind %s", f.Kind)
				}
				class, err := p.Classify(acceptanceBinding(role), f, acceptanceBaseline())
				if err == nil && class != cb.Denied {
					t.Fatalf("downstream native response accepted as %q", class)
				}
			})
		}
	}
}

func acceptanceThread(id string) map[string]any {
	return map[string]any{
		"id": id, "preview": "fixture visible text", "ephemeral": false, "historyMode": "full",
		"modelProvider": "fixture-provider", "model": "fixture-model", "reasoningEffort": nil,
		"createdAt": 1, "updatedAt": 2, "recencyAt": 2, "status": map[string]any{"type": "idle"},
		"cwd": "/owned/workspace", "cliVersion": "0.159.0", "source": "cli", "name": "fixture name", "turns": []any{},
		"path": "PRIVATE_PATH_SENTINEL", "environments": []any{map[string]any{"id": "PRIVATE_ENV_SENTINEL"}},
		"extra": map[string]any{"unknown": "PRIVATE_EXTRA_SENTINEL"}, "sessionId": "PRIVATE_SESSION_SENTINEL",
		"projectId": "PRIVATE_PROJECT_SENTINEL", "parentThreadId": "PRIVATE_PARENT_SENTINEL", "forkedFromId": "PRIVATE_FORK_SENTINEL",
		"gitInfo": map[string]any{"repositoryUrl": "PRIVATE_GIT_SENTINEL"}, "futureField": "PRIVATE_UNKNOWN_SENTINEL",
	}
}

func acceptanceNoStrings(t *testing.T, payload json.RawMessage, forbidden ...string) {
	t.Helper()
	for _, s := range forbidden {
		if bytes.Contains(payload, []byte(s)) {
			t.Fatalf("projection leaked %q: %s", s, payload)
		}
	}
}

func acceptanceNoThreadRealm(t *testing.T, payload json.RawMessage) {
	t.Helper()
	acceptanceNoStrings(t, payload, "PRIVATE_", "native_B")
}

func TestPolicyAcceptanceExactResponseAndThreadProjection(t *testing.T) {
	p := cb.NewPolicy()
	c := acceptanceContext(t, cb.NativeLocal, "thread/read", map[string]any{"threadId": "native_A"})
	f := acceptanceResult(t, map[string]any{"thread": acceptanceThread("native_A"), "futureResultField": "PRIVATE_RESULT_SENTINEL"})
	out := acceptanceOutput(t, p, c, f)
	acceptanceNoThreadRealm(t, out.Payload)
	root := acceptanceObject(t, out.Payload)
	thread, ok := root["thread"].(map[string]any)
	if !ok || thread["id"] != "native_A" || thread["preview"] != "fixture visible text" || thread["model"] != "fixture-model" {
		t.Fatalf("lost useful native thread metadata: %s", out.Payload)
	}
	for name, mutate := range map[string]func(*cb.OutputContext, *cb.Frame){
		"string_vs_integer": func(_ *cb.OutputContext, f *cb.Frame) { f.ID = json.RawMessage(`42`) },
		"downstream_id":     func(_ *cb.OutputContext, f *cb.Frame) { f.ID = json.RawMessage(`"downstream-42"`) },
		"missing_original":  func(c *cb.OutputContext, _ *cb.Frame) { c.OriginalRequest = nil },
		"wrong_original_target": func(c *cb.OutputContext, _ *cb.Frame) {
			r := acceptanceRequest(t, "thread/read", map[string]any{"threadId": "native_B"})
			c.OriginalRequest = &r
		},
		"wrong_original_method": func(c *cb.OutputContext, _ *cb.Frame) {
			r := acceptanceRequest(t, "future/read", map[string]any{"threadId": "native_A"})
			c.OriginalRequest = &r
		},
		"wrong_result_thread": func(_ *cb.OutputContext, f *cb.Frame) {
			f.Payload = acceptanceJSON(t, map[string]any{"thread": acceptanceThread("native_B")})
		},
		"missing_result_thread": func(_ *cb.OutputContext, f *cb.Frame) { f.Payload = json.RawMessage(`{"thread":{}}`) },
		"wrong_payload_shape":   func(_ *cb.OutputContext, f *cb.Frame) { f.Payload = json.RawMessage(`[]`) },
		"unreviewed_role":       func(c *cb.OutputContext, _ *cb.Frame) { c.Recipient.Role = cb.Role("unreviewed_future_role") },
	} {
		t.Run(name, func(t *testing.T) {
			cc, ff := c, f
			mutate(&cc, &ff)
			acceptanceWithheld(t, p, cc, ff)
		})
	}
	// Same textual digits remain distinct across a trusted upstream map.
	c.OriginalRequest = &cb.Frame{Kind: cb.Request, ID: json.RawMessage(`7`), Method: "thread/read", Payload: json.RawMessage(`{"threadId":"native_A"}`)}
	f.ID = json.RawMessage(`"7"`)
	acceptanceWithheld(t, p, c, f)
	f.ID = json.RawMessage(`7`)
	acceptanceOutput(t, p, c, f)
	// The policy cannot prove a live grant. No revoke-while-read test is claimed.
}

func TestPolicyAcceptanceSchemaKnownContainersDoNotBypassProjection(t *testing.T) {
	// Known top-level field names are insufficient: source/status/config values
	// also need finite typed projection. The source union can carry ancestry IDs
	// and paths even though it is a schema-known Thread field.
	c := acceptanceContext(t, cb.NativeLocal, "thread/read", map[string]any{"threadId": "native_A"})
	for _, change := range []string{"source", "status"} {
		t.Run("thread/"+change, func(t *testing.T) {
			p := cb.NewPolicy()
			acceptanceOutput(t, p, c, acceptanceResult(t, map[string]any{"thread": acceptanceThread("native_A")}))
			thread := acceptanceThread("native_A")
			if change == "source" {
				thread["source"] = map[string]any{"subAgent": map[string]any{"thread_spawn": map[string]any{"parent_thread_id": "native_B", "depth": 1, "agent_path": "PRIVATE_SOURCE_PATH", "agent_nickname": nil, "agent_role": nil}}}
			} else {
				thread["status"] = map[string]any{"type": "active", "activeFlags": []string{"waitingOnApproval"}, "futureField": "PRIVATE_STATUS_SENTINEL"}
			}
			out, ok, _ := p.NativeOutput(c, acceptanceResult(t, map[string]any{"thread": thread}))
			if ok {
				acceptanceNoThreadRealm(t, out.Payload)
			}
		})
	}
	ctx := acceptanceContext(t, cb.NativeLocal, "config/read", map[string]any{"cwd": "/owned/workspace", "includeLayers": true})
	for key, value := range map[string]any{
		"model":                   map[string]any{"future": "PRIVATE_WRONG_MODEL_TYPE"},
		"approval_policy":         map[string]any{"future": "PRIVATE_UNKNOWN_APPROVAL_VARIANT"},
		"sandbox_workspace_write": map[string]any{"writable_roots": []string{"PRIVATE_WRITABLE_ROOT"}, "network_access": true, "exclude_tmpdir_env_var": false, "exclude_slash_tmp": false},
	} {
		t.Run("config/"+key, func(t *testing.T) {
			p := cb.NewPolicy()
			acceptanceOutput(t, p, ctx, acceptanceResult(t, map[string]any{"config": map[string]any{"model": "fixture-model", "sandbox_mode": "read-only"}, "origins": map[string]any{}, "layers": nil}))
			out, ok, _ := p.NativeOutput(ctx, acceptanceResult(t, map[string]any{"config": map[string]any{key: value}, "origins": map[string]any{}, "layers": nil}))
			if ok {
				acceptanceNoStrings(t, out.Payload, "PRIVATE_")
			}
		})
	}
}

func TestPolicyAcceptanceUnknownOutputFieldsAreProjectedOrWithheld(t *testing.T) {
	// The public contract permits safe refusal of unreviewed output variants.
	// Every case first passes its exact known-schema positive control. The added
	// unknown data may be dropped or cause explicit withholding, never leak.
	for _, method := range []string{"thread/turns/list", "thread/items/list", "thread/goal/get", "configRequirements/read", "model/list"} {
		t.Run("result/"+method, func(t *testing.T) {
			params := map[string]any{"threadId": "native_A"}
			payload := map[string]any{"data": []any{}, "nextCursor": nil, "backwardsCursor": nil}
			switch method {
			case "thread/goal/get":
				payload = map[string]any{"goal": nil}
			case "configRequirements/read":
				params = map[string]any{}
				payload = map[string]any{"requirements": nil}
			case "model/list":
				params = map[string]any{"includeHidden": true}
				payload = map[string]any{"data": []any{}, "nextCursor": nil}
			}
			c := acceptanceContext(t, cb.NativeLocal, method, params)
			p := cb.NewPolicy()
			acceptanceOutput(t, p, c, acceptanceResult(t, payload))
			payload["futureField"] = "PRIVATE_UNKNOWN_SENTINEL"
			out, ok, _ := p.NativeOutput(c, acceptanceResult(t, payload))
			if ok {
				acceptanceNoStrings(t, out.Payload, "PRIVATE_")
			}
		})
	}
	for method, payload := range map[string]map[string]any{
		"thread/status/changed": {"threadId": "native_A", "status": map[string]any{"type": "idle"}},
		"thread/goal/cleared":   {"threadId": "native_A"},
		"thread/started":        {"thread": acceptanceThread("native_A")},
	} {
		t.Run("notification/"+method, func(t *testing.T) {
			c := cb.OutputContext{Recipient: acceptanceBinding(cb.NativeLocal), UpstreamChannelID: "upstream_channel_fixture"}
			p := cb.NewPolicy()
			acceptanceOutput(t, p, c, cb.Frame{Kind: cb.Notification, Method: method, Payload: acceptanceJSON(t, payload)})
			payload["futureField"] = "PRIVATE_UNKNOWN_SENTINEL"
			out, ok, _ := p.NativeOutput(c, cb.Frame{Kind: cb.Notification, Method: method, Payload: acceptanceJSON(t, payload)})
			if ok {
				acceptanceNoStrings(t, out.Payload, "PRIVATE_")
			}
		})
	}
}

func TestPolicyAcceptanceGlobalListsBecomeExactSelectedPage(t *testing.T) {
	for _, role := range []cb.Role{cb.NativeLocal, cb.TypedObserver, cb.TypedWriter} {
		t.Run(string(role)+"/thread/list", func(t *testing.T) {
			p := cb.NewPolicy()
			c := acceptanceContext(t, role, "thread/list", map[string]any{"useStateDbOnly": true})
			f := acceptanceResult(t, map[string]any{"data": []any{acceptanceThread("native_B"), acceptanceThread("native_A")}, "nextCursor": "PRIVATE_GLOBAL_CURSOR", "backwardsCursor": "PRIVATE_BACK_CURSOR"})
			out := acceptanceOutput(t, p, c, f)
			acceptanceNoThreadRealm(t, out.Payload)
			v := acceptanceObject(t, out.Payload)
			data, ok := v["data"].([]any)
			if !ok || len(data) != 1 || data[0].(map[string]any)["id"] != "native_A" {
				t.Fatalf("not an exact selected page: %s", out.Payload)
			}
			if v["nextCursor"] != nil || v["backwardsCursor"] != nil {
				t.Fatalf("global cursor retained: %s", out.Payload)
			}
		})
		t.Run(string(role)+"/thread/loaded/list", func(t *testing.T) {
			p := cb.NewPolicy()
			c := acceptanceContext(t, role, "thread/loaded/list", map[string]any{})
			out := acceptanceOutput(t, p, c, acceptanceResult(t, map[string]any{"data": []string{"native_B", "native_A"}, "nextCursor": "PRIVATE_GLOBAL_CURSOR"}))
			acceptanceNoThreadRealm(t, out.Payload)
			v := acceptanceObject(t, out.Payload)
			data, ok := v["data"].([]any)
			if !ok || len(data) != 1 || data[0] != "native_A" || v["nextCursor"] != nil {
				t.Fatalf("not an exact loaded page: %s", out.Payload)
			}
		})
	}
	for _, method := range []string{"thread/list", "thread/loaded/list"} {
		for _, paged := range []bool{false, true} {
			t.Run(fmt.Sprintf("target_absent/%s/paged=%t", method, paged), func(t *testing.T) {
				params := map[string]any{}
				if method == "thread/list" {
					params["useStateDbOnly"] = true
				}
				c := acceptanceContext(t, cb.NativeLocal, method, params)
				data := any([]any{acceptanceThread("native_B")})
				if method == "thread/loaded/list" {
					data = []string{"native_B"}
				}
				var cursor any
				if paged {
					cursor = "PRIVATE_GLOBAL_CURSOR"
				}
				f := acceptanceResult(t, map[string]any{"data": data, "nextCursor": cursor})
				p := cb.NewPolicy()
				acceptanceAllowed(t, p, c.Recipient, *c.OriginalRequest, acceptanceBaseline())
				out, ok, err := p.NativeOutput(c, f)
				if paged {
					if ok || err == nil {
						t.Fatalf("target absent in partial global page needs explicit unsupported outcome: delivered=%v err=%v", ok, err)
					}
				} else {
					if err != nil || !ok {
						t.Fatalf("complete empty scoped inventory should be useful: delivered=%v err=%v", ok, err)
					}
					v := acceptanceObject(t, out.Payload)
					if data, ok := v["data"].([]any); !ok || len(data) != 0 {
						t.Fatalf("unselected row survived: %s", out.Payload)
					}
				}
			})
		}
	}
}

func TestPolicyAcceptanceHistoryResponsesAreRequestBound(t *testing.T) {
	p := cb.NewPolicy()
	for _, method := range []string{"thread/turns/list", "thread/items/list"} {
		c := acceptanceContext(t, cb.NativeLocal, method, map[string]any{"threadId": "native_A", "cursor": nil})
		f := acceptanceResult(t, map[string]any{"data": []any{}, "nextCursor": nil, "backwardsCursor": nil})
		out := acceptanceOutput(t, p, c, f)
		acceptanceNoThreadRealm(t, out.Payload)
		wrong := acceptanceRequest(t, method, map[string]any{"threadId": "native_B"})
		c.OriginalRequest = &wrong
		acceptanceWithheld(t, p, c, f)
		c.OriginalRequest = nil
		acceptanceWithheld(t, p, c, f)
	}
}

func TestPolicyAcceptanceNonemptyHistoryKeepsOnlyKnownScopedData(t *testing.T) {
	item := map[string]any{"type": "agentMessage", "id": "item_A", "text": "fixture agent text", "phase": nil, "memoryCitation": nil, "delivery": nil, "questions": nil}
	turn := map[string]any{"id": "turn_A", "items": []any{item}, "itemsView": "full", "status": "completed", "error": nil, "startedAt": 1, "completedAt": 2, "durationMs": 1000}
	entry := map[string]any{"turnId": "turn_A", "item": item, "startedAtMs": 1000, "completedAtMs": 2000}
	for method, data := range map[string][]any{"thread/turns/list": {turn}, "thread/items/list": {entry}} {
		for _, role := range []cb.Role{cb.NativeLocal, cb.TypedObserver} {
			t.Run(string(role)+"/"+method, func(t *testing.T) {
				c := acceptanceContext(t, role, method, map[string]any{"threadId": "native_A", "cursor": nil})
				out := acceptanceOutput(t, cb.NewPolicy(), c, acceptanceResult(t, map[string]any{"data": data, "nextCursor": nil, "backwardsCursor": nil}))
				acceptanceNoThreadRealm(t, out.Payload)
				if !bytes.Contains(out.Payload, []byte("turn_A")) {
					t.Fatalf("nonempty history lost its scoped turn metadata: %s", out.Payload)
				}
				if role == cb.NativeLocal && !bytes.Contains(out.Payload, []byte("fixture agent text")) {
					t.Fatalf("native text projection is empty: %s", out.Payload)
				}
				if role == cb.TypedObserver {
					acceptanceNoStrings(t, out.Payload, "fixture agent text")
				}
			})
		}
	}
}

func TestPolicyAcceptanceTurnMembershipDoesNotCrossBindings(t *testing.T) {
	p := cb.NewPolicy()
	b := acceptanceBinding(cb.NativeLocal)
	items := acceptanceRequest(t, "thread/items/list", map[string]any{"threadId": "native_A", "turnId": "turn_A", "cursor": nil})
	raw := acceptanceJSON(t, map[string]any{"id": "upstream-42", "method": items.Method, "params": json.RawMessage(items.Payload)})
	acceptanceDenied(t, p, b, raw, acceptanceBaseline())
	c := acceptanceContext(t, cb.NativeLocal, "thread/turns/list", map[string]any{"threadId": "native_A"})
	turn := map[string]any{"id": "turn_A", "items": []any{}, "itemsView": "notLoaded", "status": "completed", "error": nil, "startedAt": nil, "completedAt": nil, "durationMs": nil}
	acceptanceOutput(t, p, c, acceptanceResult(t, map[string]any{"data": []any{turn}, "nextCursor": nil, "backwardsCursor": nil}))
	if class := acceptanceAllowed(t, p, b, items, acceptanceBaseline()); class != cb.ScopedRead {
		t.Fatalf("known scoped turn read classified %s", class)
	}
	acceptanceDenied(t, p, b, []byte(`{"id":1,"method":"thread/items/list","params":{"threadId":"native_A","turnId":"turn_B"}}`), acceptanceBaseline())
	for _, change := range []string{"connection", "epoch", "generation", "incarnation", "principal"} {
		t.Run(change, func(t *testing.T) {
			other := b
			switch change {
			case "connection":
				other.ID = "other_connection"
			case "epoch":
				other.Writer.Epoch++
			case "generation":
				other.Agent.Generation++
				other.Writer.Lease.Generation++
			case "incarnation":
				other.Agent.AppServerIncarnation = "other_app_incarnation"
			case "principal":
				other.Writer.PrincipalID = "other_principal"
			}
			acceptanceDenied(t, p, other, raw, acceptanceBaseline())
		})
	}
}

func TestPolicyAcceptanceGoalIdentityIsExplicit(t *testing.T) {
	c := acceptanceContext(t, cb.NativeLocal, "thread/goal/get", map[string]any{"threadId": "native_A"})
	goal := map[string]any{"threadId": "native_A", "objective": "fixture objective", "status": "active", "tokenBudget": nil, "tokensUsed": 0, "timeUsedSeconds": 0, "createdAt": 1, "updatedAt": 2}
	f := acceptanceResult(t, map[string]any{"goal": goal})
	out := acceptanceOutput(t, cb.NewPolicy(), c, f)
	acceptanceNoThreadRealm(t, out.Payload)
	if !bytes.Contains(out.Payload, []byte("native_A")) {
		t.Fatalf("goal lost exact identity: %s", out.Payload)
	}
	goal["threadId"] = "native_B"
	f = acceptanceResult(t, map[string]any{"goal": goal})
	acceptanceWithheld(t, cb.NewPolicy(), c, f)
	acceptanceOutput(t, cb.NewPolicy(), c, acceptanceResult(t, map[string]any{"goal": nil}))
}

func TestPolicyAcceptanceAccountAndConfigAreFiniteNativeProjections(t *testing.T) {
	cases := []struct {
		method          string
		params, payload any
		check           func(*testing.T, map[string]any)
	}{
		{"account/read", map[string]any{}, map[string]any{
			"account":            map[string]any{"type": "chatgpt", "email": "PRIVATE_EMAIL_SENTINEL", "planType": "plus", "futureCredential": "PRIVATE_CREDENTIAL_SENTINEL"},
			"requiresOpenaiAuth": true, "workspaceRouting": map[string]any{"workspaceId": "PRIVATE_WORKSPACE_SENTINEL"}, "futureField": "PRIVATE_ACCOUNT_SENTINEL",
		}, func(t *testing.T, v map[string]any) {
			a, ok := v["account"].(map[string]any)
			if !ok || a["type"] != "chatgpt" || v["requiresOpenaiAuth"] != true {
				t.Fatalf("lost public account type: %+v", v)
			}
			if a["email"] != nil || v["workspaceRouting"] != nil {
				t.Fatalf("private account realm survived: %+v", v)
			}
		}},
		{"config/read", map[string]any{"cwd": "/owned/workspace", "includeLayers": true}, map[string]any{
			"config":  map[string]any{"model": "fixture-model", "sandbox_mode": "read-only", "approval_policy": "on-request", "web_search": "cached", "innocentArbitraryKey": "PRIVATE_ARBITRARY_SENTINEL", "instructions": "PRIVATE_INSTRUCTIONS_SENTINEL", "model_providers": map[string]any{"fixture": map[string]any{"env_key": "PRIVATE_SECRET_SENTINEL"}}},
			"origins": map[string]any{"model": map[string]any{"name": "PRIVATE_ORIGIN_SENTINEL"}},
			"layers":  []any{map[string]any{"config": map[string]any{"innocentArbitraryKey": "PRIVATE_LAYER_SENTINEL"}}}, "futureField": "PRIVATE_CONFIG_SENTINEL",
		}, func(t *testing.T, v map[string]any) {
			c, ok := v["config"].(map[string]any)
			if !ok || c["model"] != "fixture-model" || c["sandbox_mode"] != "read-only" {
				t.Fatalf("lost public config metadata: %+v", v)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.method, func(t *testing.T) {
			p := cb.NewPolicy()
			ctx := acceptanceContext(t, cb.NativeLocal, c.method, c.params)
			f := acceptanceResult(t, c.payload)
			out := acceptanceOutput(t, p, ctx, f)
			acceptanceNoStrings(t, out.Payload, "PRIVATE_")
			c.check(t, acceptanceObject(t, out.Payload))
			for _, role := range []cb.Role{cb.TypedObserver, cb.TypedWriter} {
				ctx.Recipient.Role = role
				acceptanceWithheld(t, p, ctx, f)
			}
		})
	}
	for method, payload := range map[string]any{
		"configRequirements/read": map[string]any{"requirements": nil},
		"model/list":              map[string]any{"data": []any{}, "nextCursor": nil},
		"collaborationMode/list":  map[string]any{"data": []any{}, "futureField": "PRIVATE_COLLAB_SENTINEL"},
		"skills/list":             map[string]any{"data": []any{map[string]any{"cwd": "/owned/workspace", "skills": []any{}, "errors": []any{}, "futureField": "PRIVATE_SKILL_SENTINEL"}}},
		"hooks/list":              map[string]any{"data": []any{map[string]any{"cwd": "/owned/workspace", "hooks": []any{}, "errors": []any{}, "warnings": []any{}, "futureField": "PRIVATE_HOOK_SENTINEL"}}},
	} {
		t.Run("native_metadata_withheld/"+method, func(t *testing.T) {
			params := map[string]any{}
			if method == "skills/list" || method == "hooks/list" {
				params["cwds"] = []string{"/owned/workspace"}
			}
			if method == "skills/list" {
				params["forceReload"] = true
			}
			if method == "model/list" {
				params["includeHidden"] = true
			}
			ctx := acceptanceContext(t, cb.NativeLocal, method, params)
			f := acceptanceResult(t, payload)
			out := acceptanceOutput(t, cb.NewPolicy(), ctx, f)
			acceptanceNoStrings(t, out.Payload, "PRIVATE_")
			for _, role := range []cb.Role{cb.TypedObserver, cb.TypedWriter} {
				ctx.Recipient.Role = role
				acceptanceWithheld(t, cb.NewPolicy(), ctx, f)
			}
		})
	}
}

func TestPolicyAcceptanceKnownScopedNotificationsAndGlobalWithholding(t *testing.T) {
	p := cb.NewPolicy()
	c := cb.OutputContext{Recipient: acceptanceBinding(cb.NativeLocal), UpstreamChannelID: "upstream_channel_fixture"}
	for _, f := range []cb.Frame{
		{Kind: cb.Notification, Method: "thread/status/changed", Payload: json.RawMessage(`{"threadId":"native_A","status":{"type":"idle"}}`)},
		{Kind: cb.Notification, Method: "thread/goal/cleared", Payload: json.RawMessage(`{"threadId":"native_A"}`)},
		{Kind: cb.Notification, Method: "thread/started", Payload: acceptanceJSON(t, map[string]any{"thread": acceptanceThread("native_A")})},
	} {
		t.Run("scoped/"+f.Method, func(t *testing.T) {
			out := acceptanceOutput(t, p, c, f)
			acceptanceNoThreadRealm(t, out.Payload)
			if !bytes.Contains(out.Payload, []byte("native_A")) {
				t.Fatalf("lost scoped notification identity: %s", out.Payload)
			}
		})
	}
	denied := []struct {
		method string
		params any
	}{
		{"remoteControl/status/changed", map[string]any{"status": "disabled", "installationId": "PRIVATE_INSTALL", "serverName": "PRIVATE_SERVER", "environmentId": nil}},
		{"account/updated", map[string]any{"threadId": "native_A", "authMode": "chatgpt"}},
		{"account/rateLimits/updated", map[string]any{"threadId": "native_A", "rateLimits": map[string]any{}}},
		{"future/notification", map[string]any{"threadId": "native_A"}},
		{"thread/status/changed", map[string]any{"threadId": "native_B", "status": map[string]any{"type": "idle", "nested": map[string]any{"threadId": "native_A"}}}},
		{"thread/status/changed", map[string]any{"status": map[string]any{"threadId": "native_A"}}},
		{"thread/started", map[string]any{"thread": acceptanceThread("native_B")}},
		{"process/outputDelta", map[string]any{"threadId": "native_A", "processHandle": "unowned", "stream": "stdout", "deltaBase64": "RklYVFVSRQ==", "capReached": false}},
		{"configWarning", map[string]any{"threadId": nil, "message": "PRIVATE_HOME_WARNING"}},
	}
	for _, d := range denied {
		t.Run("withheld/"+d.method, func(t *testing.T) {
			acceptanceWithheld(t, p, c, cb.Frame{Kind: cb.Notification, Method: d.method, Payload: acceptanceJSON(t, d.params)})
		})
	}
	// Callback display/attribution is authority-owned. An unknown callback must
	// never become a scoped event just because its params contain the target ID.
	acceptanceWithheld(t, p, c, cb.Frame{Kind: cb.Request, ID: json.RawMessage(`"server_unknown"`), Method: "future/approval", Payload: json.RawMessage(`{"threadId":"native_A"}`)})
}

func TestPolicyAcceptanceUpstreamErrorsCannotLeakNativeRealm(t *testing.T) {
	c := acceptanceContext(t, cb.NativeLocal, "thread/read", map[string]any{"threadId": "native_A"})
	f := cb.Frame{Kind: cb.RPCError, ID: json.RawMessage(`"upstream-42"`), Payload: json.RawMessage(`{"code":-32000,"message":"PRIVATE_ERROR_PATH native_B","data":{"threadId":"native_B","path":"PRIVATE_ERROR_PATH"}}`)}
	out, ok, _ := cb.NewPolicy().NativeOutput(c, f)
	if ok {
		acceptanceNoStrings(t, out.Payload, "PRIVATE_", "native_B")
		if out.Kind != cb.RPCError || !bytes.Equal(out.ID, f.ID) {
			t.Fatal("sanitized error lost response correlation")
		}
	}
	f.ID = json.RawMessage(`"unmatched-upstream"`)
	acceptanceWithheld(t, cb.NewPolicy(), c, f)
}

func TestPolicyAcceptanceNotificationTimestampCannotAuthorizeOrDisclose(t *testing.T) {
	// A source-observed additive notification envelope marker is disposable
	// metadata. Supporting it must not widen effect classes or output scope.
	for _, role := range []cb.Role{cb.NativeLocal, cb.TypedObserver} {
		for _, stamp := range []string{"0", "1760120506228"} {
			t.Run(string(role)+"/known_scoped/"+stamp, func(t *testing.T) {
				p := cb.NewPolicy()
				f := acceptanceDecode(t, p, `{"method":"thread/status/changed","params":{"threadId":"native_A","status":{"type":"idle"}},"emittedAtMs":`+stamp+`}`)
				if f.Kind != cb.Notification || len(f.ID) != 0 {
					t.Fatal("timestamp marker changed notification identity")
				}
				c := cb.OutputContext{Recipient: acceptanceBinding(role), UpstreamChannelID: "marker_source_fixture"}
				out := acceptanceOutput(t, p, c, f)
				encoded, err := cb.Encode(out)
				if err != nil || bytes.Contains(encoded, []byte("emittedAtMs")) || bytes.Contains(encoded, []byte("1760120506228")) {
					t.Fatal("discarded source timestamp reached a projected frame")
				}
				if !bytes.Contains(encoded, []byte("native_A")) || !bytes.Contains(encoded, []byte("idle")) {
					t.Fatal("known scoped control lost useful identity or status")
				}
			})
		}
		for _, method := range []string{"remoteControl/status/changed", "account/updated", "future/notification", "thread/status/changed"} {
			t.Run(string(role)+"/withheld/"+method, func(t *testing.T) {
				p := cb.NewPolicy()
				f := acceptanceDecode(t, p, string(acceptanceJSON(t, map[string]any{"method": method, "emittedAtMs": 1760120506228, "params": map[string]any{"threadId": "native_B", "status": map[string]any{"type": "idle"}, "installationId": "PRIVATE_MARKER_INSTALL", "workspaceRouting": "PRIVATE_MARKER_ROUTE"}})))
				c := cb.OutputContext{Recipient: acceptanceBinding(role), UpstreamChannelID: "marker_source_fixture"}
				acceptanceAllowed(t, p, c.Recipient, acceptanceRequest(t, "thread/read", map[string]any{"threadId": "native_A"}), acceptanceBaseline())
				acceptanceWithheld(t, p, c, f)
				class, err := p.Classify(c.Recipient, f, acceptanceBaseline())
				if err == nil && class != cb.Denied {
					t.Fatal("source timestamp converted global/unknown event into a client effect")
				}
			})
		}
	}
	for _, stamp := range []string{"-1", "9223372036854775808", "1.5", "1e3", "null", `"1760120506228"`, "true", "[]", "{}"} {
		t.Run("invalid_marker/"+stamp, func(t *testing.T) {
			if _, err := cb.NewPolicy().Decode([]byte(`{"method":"thread/status/changed","params":{"threadId":"native_A","status":{"type":"idle"}},"emittedAtMs":` + stamp + `}`)); err == nil {
				t.Fatal("invalid notification timestamp marker accepted")
			}
		})
	}
	for _, raw := range []string{
		`{"id":1,"method":"thread/read","params":{"threadId":"native_A"},"emittedAtMs":1760120506228}`,
		`{"id":1,"result":{"thread":{"id":"native_A"}},"emittedAtMs":1760120506228}`,
		`{"id":1,"error":{"code":-32000,"message":"fixture"},"emittedAtMs":1760120506228}`,
		`{"method":"thread/status/changed","params":{},"result":{},"emittedAtMs":1760120506228}`,
		`{"method":"thread/status/changed","params":{},"emittedAtMs":1,"emittedAtMs":2}`,
		`{"method":"thread/status/changed","params":{},"emittedAtMs":1,"emittedAt\u004ds":2}`,
		`{"method":"thread/status/changed","params":{},"emittedAtMs":1760120506228,"futureMarker":true}`,
	} {
		t.Run("nonnotification_or_ambiguous/"+raw, func(t *testing.T) {
			if _, err := cb.NewPolicy().Decode([]byte(raw)); err == nil {
				t.Fatal("timestamp support widened strict envelope acceptance")
			}
		})
	}
}

func TestPolicyAcceptanceProjectTrustUsesExactClassifiedHomeCwd(t *testing.T) {
	fixture := func(projects any) cb.Frame {
		return acceptanceResult(t, map[string]any{
			"config":  map[string]any{"model": "fixture-model", "projects": projects},
			"origins": map[string]any{"projects": map[string]any{"path": "PRIVATE_TRUST_ORIGIN"}},
			"layers":  []any{map[string]any{"config": map[string]any{"projects": map[string]any{"PRIVATE_TRUST_LAYER": map[string]any{"trust_level": "trusted"}}}}},
		})
	}
	noInventedTrust := func(t *testing.T, out cb.Frame) {
		t.Helper()
		config, ok := acceptanceObject(t, out.Payload)["config"].(map[string]any)
		if !ok {
			t.Fatal("native config control lost public config container")
		}
		if projects, present := config["projects"]; present {
			m, ok := projects.(map[string]any)
			if !ok || len(m) != 0 {
				t.Fatal("source-absent trust or unclassified realm was invented")
			}
		}
	}
	for _, cwd := range []string{"/owned/workspace", "/private/owned/workspace"} {
		other := "/private/owned/workspace"
		if cwd == other {
			other = "/owned/workspace"
		}
		for _, level := range []string{"trusted", "untrusted"} {
			t.Run("exact/"+cwd+"/"+level, func(t *testing.T) {
				projects := map[string]any{
					cwd:                         map[string]any{"trust_level": level, "model_provider": "PRIVATE_SELECTED_PROVIDER", "futureField": map[string]any{"path": "PRIVATE_SELECTED_PATH"}},
					other:                       map[string]any{"trust_level": "trusted", "privateField": "PRIVATE_ALIAS_RECORD"},
					"PRIVATE_OTHER_PROJECT_KEY": map[string]any{"trust_level": "unrecognized", "privateField": "PRIVATE_OTHER_PROJECT_VALUE"},
				}
				p := cb.NewPolicy()
				c := acceptanceContext(t, cb.NativeLocal, "config/read", map[string]any{"cwd": cwd, "includeLayers": true})
				f := fixture(projects)
				out := acceptanceOutput(t, p, c, f)
				acceptanceNoStrings(t, out.Payload, "PRIVATE_")
				cfg, ok := acceptanceObject(t, out.Payload)["config"].(map[string]any)
				if !ok || cfg["model"] != "fixture-model" {
					t.Fatal("bounded project trust lost known public config control")
				}
				selected, ok := cfg["projects"].(map[string]any)
				if !ok || len(selected) != 1 {
					t.Fatal("project trust exposed other workspace or alias keys")
				}
				record, ok := selected[cwd].(map[string]any)
				if !ok || len(record) != 1 || record["trust_level"] != level {
					t.Fatal("exact requested trust was lost, changed, or expanded")
				}
				for _, role := range []cb.Role{cb.TypedObserver, cb.TypedWriter} {
					c.Recipient.Role = role
					acceptanceWithheld(t, p, c, f)
				}
			})
		}
		for _, projects := range []any{
			map[string]any{},
			map[string]any{"/other/workspace": map[string]any{"trust_level": "trusted"}},
			map[string]any{other: map[string]any{"trust_level": "trusted"}},
		} {
			t.Run("selected_absent/"+cwd, func(t *testing.T) {
				c := acceptanceContext(t, cb.NativeLocal, "config/read", map[string]any{"cwd": cwd, "includeLayers": true})
				out := acceptanceOutput(t, cb.NewPolicy(), c, fixture(projects))
				noInventedTrust(t, out)
			})
		}
	}
	for _, selected := range []any{nil, true, 1, "trusted", []any{}, map[string]any{"trust_level": "TRUSTED"}, map[string]any{"trust_level": "unknown"}, map[string]any{"trust_level": " trusted"}, map[string]any{"trust_level": ""}, map[string]any{"trust_level": nil}, map[string]any{"trust_level": true}, map[string]any{"trust_level": []any{}}, map[string]any{"trust_level": map[string]any{}}} {
		t.Run("invalid_selected/"+fmt.Sprintf("%T_%v", selected, selected), func(t *testing.T) {
			p := cb.NewPolicy()
			c := acceptanceContext(t, cb.NativeLocal, "config/read", map[string]any{"cwd": "/owned/workspace", "includeLayers": true})
			acceptanceAllowed(t, p, c.Recipient, *c.OriginalRequest, acceptanceBaseline())
			acceptanceWithheld(t, p, c, fixture(map[string]any{"/owned/workspace": selected}))
		})
	}
	for _, record := range []any{map[string]any{}, map[string]any{"futureField": "PRIVATE_ABSENT_TRUST"}} {
		t.Run("selected_level_absent", func(t *testing.T) {
			p := cb.NewPolicy()
			c := acceptanceContext(t, cb.NativeLocal, "config/read", map[string]any{"cwd": "/owned/workspace", "includeLayers": true})
			acceptanceAllowed(t, p, c.Recipient, *c.OriginalRequest, acceptanceBaseline())
			out, visible, _ := p.NativeOutput(c, fixture(map[string]any{"/owned/workspace": record}))
			if visible {
				noInventedTrust(t, out)
			}
		})
	}
	for _, mode := range []string{"no_classification", "other_connection_cache", "forged_cwd_after_classification"} {
		t.Run("baseline_required/"+mode, func(t *testing.T) {
			p := cb.NewPolicy()
			c := acceptanceContext(t, cb.NativeLocal, "config/read", map[string]any{"cwd": "/owned/workspace", "includeLayers": true})
			cwd := "/owned/workspace"
			if mode != "no_classification" {
				acceptanceAllowed(t, p, c.Recipient, *c.OriginalRequest, acceptanceBaseline())
			}
			if mode == "other_connection_cache" {
				c.Recipient.ID = "other_native_connection"
			}
			if mode == "forged_cwd_after_classification" {
				cwd = "/ungranted/workspace"
				f := acceptanceRequest(t, "config/read", map[string]any{"cwd": cwd, "includeLayers": true})
				c.OriginalRequest = &f
			}
			out, visible, _ := p.NativeOutput(c, fixture(map[string]any{cwd: map[string]any{"trust_level": "trusted"}}))
			if visible {
				noInventedTrust(t, out)
			}
		})
	}
}
