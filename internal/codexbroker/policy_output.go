package codexbroker

import (
	"bytes"
	"encoding/json"
)

func (p *nativePolicy) NativeOutput(ctx OutputContext, f Frame) (Frame, bool, error) {
	b := ctx.Recipient
	if !scopedBinding(b) || ctx.UpstreamChannelID == "" {
		return Frame{}, false, errPolicy
	}
	if f.Kind == Notification {
		if ctx.OriginalRequest != nil || len(f.ID) != 0 {
			return Frame{}, false, errPolicy
		}
		return p.notification(b, f)
	}
	// No server callbacks are delegated by this zero-turn profile.
	if (f.Kind != Result && f.Kind != RPCError) || f.Method != "" || ctx.OriginalRequest == nil {
		return Frame{}, false, errPolicy
	}
	original := *ctx.OriginalRequest
	id, ok := validatedID(f.ID)
	expected, expectedOK := validatedID(original.ID)
	if !ok || !expectedOK || !bytes.Equal(id, expected) || original.Kind != Request {
		return Frame{}, false, errPolicy
	}
	params, ok := payloadObject(original, original.Method == "configRequirements/read")
	if !ok || !outputMethod(original.Method, b, params) {
		return Frame{}, false, errPolicy
	}
	if f.Kind == RPCError {
		if _, ok := payloadObject(f, false); !ok {
			return Frame{}, false, errPolicy
		}
		return Frame{Kind: RPCError, ID: id, Payload: json.RawMessage(`{"code":-32000,"message":"Codex request failed"}`)}, true, nil
	}
	m, ok := payloadObject(f, false)
	if !ok {
		return Frame{}, false, errPolicy
	}
	var out any
	var err error
	switch original.Method {
	case "thread/read", "thread/resume":
		allowTurns := original.Method == "thread/read" && params["includeTurns"] == true
		var thread map[string]any
		thread, err = p.projectThread(b, m["thread"], allowTurns)
		out = map[string]any{"thread": thread}
		if err == nil && original.Method == "thread/resume" {
			out, err = p.projectResume(b, m, params, thread)
		}
	case "thread/list", "thread/loaded/list":
		out, err = p.projectInventory(b, m, original.Method == "thread/loaded/list")
	case "thread/turns/list", "thread/items/list":
		out, err = p.projectHistory(b, m, params, original.Method == "thread/items/list")
	case "thread/goal/get":
		if !onlyKeys(m, "goal") || !mHas(m, "goal") {
			err = errPolicy
			break
		}
		var goal any
		if m["goal"] != nil {
			goal, err = projectGoal(m["goal"], b.Agent.NativeThreadID)
		}
		out = map[string]any{"goal": goal}
	default:
		out, err = p.projectBootstrap(b, original.Method, params, m)
	}
	if err != nil {
		return Frame{}, false, err
	}
	raw, err := json.Marshal(out)
	if err != nil || len(raw) > policyMaxBytes {
		return Frame{}, false, errPolicy
	}
	return Frame{Kind: Result, ID: id, Payload: raw}, true, nil
}

func outputMethod(method string, b ConnectionBinding, params map[string]any) bool {
	switch method {
	case "thread/read", "thread/turns/list", "thread/items/list", "thread/goal/get":
		return threadTarget(params, b)
	case "thread/list":
		return params["useStateDbOnly"] == true
	case "thread/loaded/list":
		return true
	case "thread/resume":
		return nativeBinding(b) && threadTarget(params, b) && resumeConfig(params["config"]) && params["excludeTurns"] == true && nullOnly(params, "history", "path", "model", "modelProvider", "runtimeWorkspaceRoots", "approvalPolicy", "approvalsReviewer", "sandbox", "permissions", "baseInstructions", "developerInstructions", "personality", "initialTurnsPage")
	case "account/read":
		return nativeBinding(b) && onlyKeys(params, "refreshToken") && (!mHas(params, "refreshToken") || params["refreshToken"] == false)
	case "initialize", "config/read", "configRequirements/read", "hooks/list", "skills/list", "model/list", "collaborationMode/list":
		return nativeBinding(b)
	}
	return false
}

func (p *nativePolicy) projectThread(b ConnectionBinding, v any, allowTurns bool) (map[string]any, error) {
	m, ok := v.(map[string]any)
	if !ok || m["id"] != b.Agent.NativeThreadID {
		return nil, errPolicy
	}
	out := map[string]any{"id": m["id"]}
	for _, key := range []string{"preview", "name", "model", "modelProvider", "reasoningEffort", "cliVersion", "historyMode"} {
		if value, exists := m[key]; exists {
			if value != nil && !stringValue(value, 32768, true) {
				return nil, errPolicy
			}
			out[key] = value
		}
	}
	for _, key := range []string{"createdAt", "updatedAt", "recencyAt", "sectionEnteredAt"} {
		if value, exists := m[key]; exists {
			if value != nil && !integer(value, 0, 1<<63-1) {
				return nil, errPolicy
			}
			out[key] = value
		}
	}
	for _, key := range []string{"ephemeral", "canAcceptDirectInput", "daybreakEnabled"} {
		if value, exists := m[key]; exists {
			if !boolOrNull(value) {
				return nil, errPolicy
			}
			out[key] = value
		}
	}
	if value, exists := m["status"]; exists {
		status, err := projectStatus(value)
		if err != nil {
			return nil, err
		}
		out["status"] = status
	}
	if value, exists := m["turns"]; exists {
		a, ok := value.([]any)
		if !ok || len(a) > 100 || (!allowTurns && len(a) != 0) {
			return nil, errProjection
		}
		turns := []any{}
		ids := []string{}
		for _, turn := range a {
			projected, err := projectTurn(turn)
			if err != nil {
				return nil, err
			}
			if b.Role != NativeLocal {
				projected = turnMetadata(projected)
			}
			turns = append(turns, projected)
			ids = append(ids, projected["id"].(string))
		}
		out["turns"] = turns
		p.rememberTurns(b, ids)
	}
	if b.Role == NativeLocal {
		// Preserve required native nullable fields without disclosing trees/home refs.
		out["sessionId"] = ""
		for _, key := range []string{"forkedFromId", "parentThreadId", "environments", "extra", "projectId", "section", "path", "gitInfo", "threadSource", "agentNickname", "agentRole"} {
			out[key] = nil
		}
		if cwd, exists := m["cwd"]; exists {
			if !p.outputPath(b, cwd, nil) {
				return nil, errPolicy
			}
			out["cwd"] = cwd
		}
		for _, key := range []string{"originator", "source"} {
			if value, exists := m[key]; exists {
				if value != nil && !stringValue(value, 256, true) {
					return nil, errProjection
				}
				out[key] = value
			}
		}
	}
	return out, nil
}

func projectStatus(v any) (map[string]any, error) {
	m, ok := v.(map[string]any)
	if !ok || !onlyKeys(m, "type", "activeFlags") || !enumOrNull(m["type"], "idle", "notLoaded", "systemError", "active") || m["type"] == nil {
		return nil, errPolicy
	}
	if m["type"] == "active" {
		if !stringsArray(m["activeFlags"], 2, "waitingOnApproval", "waitingOnUserInput") {
			return nil, errPolicy
		}
	} else if mHas(m, "activeFlags") {
		return nil, errPolicy
	}
	return m, nil
}

func (p *nativePolicy) projectInventory(b ConnectionBinding, m map[string]any, loaded bool) (any, error) {
	if !onlyKeys(m, "data", "nextCursor", "backwardsCursor") {
		return nil, errPolicy
	}
	a, ok := m["data"].([]any)
	if !ok || len(a) > 100 {
		return nil, errPolicy
	}
	data := []any{}
	for _, row := range a {
		if loaded {
			if !stringValue(row, 256, false) {
				return nil, errPolicy
			}
			if row == b.Agent.NativeThreadID {
				data = append(data, row)
			}
		} else {
			thread, ok := row.(map[string]any)
			if !ok || !stringValue(thread["id"], 256, false) {
				return nil, errPolicy
			}
			if thread["id"] != b.Agent.NativeThreadID {
				continue
			}
			projected, err := p.projectThread(b, thread, false)
			if err != nil {
				return nil, err
			}
			data = append(data, projected)
		}
	}
	if len(data) > 1 {
		return nil, errPolicy
	}
	if len(data) == 0 && m["nextCursor"] != nil {
		return nil, errProjection
	}
	out := map[string]any{"data": data, "nextCursor": nil}
	if !loaded {
		out["backwardsCursor"] = nil
	}
	return out, nil
}

func (p *nativePolicy) projectHistory(b ConnectionBinding, m, params map[string]any, items bool) (any, error) {
	if !onlyKeys(m, "data", "nextCursor", "backwardsCursor") || !nullOnly(m, "nextCursor", "backwardsCursor") {
		return nil, errProjection
	}
	a, ok := m["data"].([]any)
	if !ok || len(a) > 100 {
		return nil, errPolicy
	}
	data := []any{}
	ids := []string{}
	for _, row := range a {
		if !items {
			turn, err := projectTurn(row)
			if err != nil {
				return nil, err
			}
			if b.Role != NativeLocal {
				turn = turnMetadata(turn)
			}
			data = append(data, turn)
			ids = append(ids, turn["id"].(string))
			continue
		}
		entry, ok := row.(map[string]any)
		if !ok || !onlyKeys(entry, "turnId", "item", "startedAtMs", "completedAtMs") || !stringValue(entry["turnId"], 256, false) || (params["turnId"] != nil && params["turnId"] != entry["turnId"]) {
			return nil, errPolicy
		}
		item, err := projectItem(entry["item"])
		if err != nil {
			return nil, err
		}
		if b.Role != NativeLocal {
			item = map[string]any{"id": item["id"], "type": item["type"]}
		}
		out := map[string]any{"turnId": entry["turnId"], "item": item}
		for _, key := range []string{"startedAtMs", "completedAtMs"} {
			if entry[key] != nil && !integer(entry[key], 0, 1<<63-1) {
				return nil, errPolicy
			}
			out[key] = entry[key]
		}
		data = append(data, out)
		ids = append(ids, entry["turnId"].(string))
	}
	p.rememberTurns(b, ids)
	return map[string]any{"data": data, "nextCursor": nil, "backwardsCursor": nil}, nil
}

func projectTurn(v any) (map[string]any, error) {
	m, ok := v.(map[string]any)
	if !ok || !onlyKeys(m, "id", "items", "itemsView", "status", "error", "startedAt", "completedAt", "durationMs") || !stringValue(m["id"], 256, false) {
		return nil, errPolicy
	}
	out := map[string]any{"id": m["id"]}
	if mHas(m, "status") {
		if !enumOrNull(m["status"], "completed", "interrupted", "failed", "inProgress") || m["status"] == nil {
			return nil, errPolicy
		}
		out["status"] = m["status"]
	}
	if mHas(m, "itemsView") {
		if !enumOrNull(m["itemsView"], "notLoaded", "summary", "full") || m["itemsView"] == nil {
			return nil, errPolicy
		}
		out["itemsView"] = m["itemsView"]
	}
	if m["error"] != nil {
		return nil, errProjection
	}
	out["error"] = nil
	for _, key := range []string{"startedAt", "completedAt", "durationMs"} {
		if m[key] != nil && !integer(m[key], 0, 1<<63-1) {
			return nil, errPolicy
		}
		out[key] = m[key]
	}
	items := []any{}
	if mHas(m, "items") {
		a, ok := m["items"].([]any)
		if !ok || len(a) > 100 {
			return nil, errPolicy
		}
		for _, item := range a {
			projected, err := projectItem(item)
			if err != nil {
				return nil, err
			}
			items = append(items, projected)
		}
	}
	out["items"] = items
	return out, nil
}

func turnMetadata(turn map[string]any) map[string]any {
	turn["items"] = []any{}
	turn["itemsView"] = "notLoaded"
	return turn
}

func projectItem(v any) (map[string]any, error) {
	m, ok := v.(map[string]any)
	if !ok || !stringValue(m["id"], 256, false) {
		return nil, errPolicy
	}
	out := map[string]any{"id": m["id"], "type": m["type"]}
	switch m["type"] {
	case "userMessage":
		if !onlyKeys(m, "id", "type", "clientId", "content") || (m["clientId"] != nil && !stringValue(m["clientId"], 256, false)) {
			return nil, errPolicy
		}
		a, ok := m["content"].([]any)
		if !ok || len(a) > 100 {
			return nil, errPolicy
		}
		content := []any{}
		for _, v := range a {
			text, ok := v.(map[string]any)
			if !ok || !onlyKeys(text, "type", "text", "text_elements") || text["type"] != "text" || !stringValue(text["text"], 256*1024, true) {
				return nil, errProjection
			}
			if elements, exists := text["text_elements"]; exists {
				if a, ok := elements.([]any); !ok || len(a) != 0 {
					return nil, errProjection
				}
			}
			content = append(content, map[string]any{"type": "text", "text": text["text"], "text_elements": []any{}})
		}
		out["clientId"] = m["clientId"]
		out["content"] = content
	case "agentMessage":
		if !onlyKeys(m, "id", "type", "text", "phase", "memoryCitation", "delivery", "questions") || !stringValue(m["text"], 256*1024, true) || !enumOrNull(m["phase"], "commentary", "final_answer") || !nullOnly(m, "memoryCitation", "delivery", "questions") {
			return nil, errProjection
		}
		out["text"] = m["text"]
		out["phase"] = m["phase"]
		out["memoryCitation"] = nil
		out["delivery"] = nil
		out["questions"] = nil
	case "plan":
		if !onlyKeys(m, "id", "type", "text") || !stringValue(m["text"], 256*1024, true) {
			return nil, errPolicy
		}
		out["text"] = m["text"]
	case "contextCompaction":
		if !onlyKeys(m, "id", "type") {
			return nil, errPolicy
		}
	default:
		return nil, errProjection
	}
	return out, nil
}

func projectGoal(v any, thread string) (any, error) {
	m, ok := v.(map[string]any)
	if !ok || !onlyKeys(m, "threadId", "objective", "status", "tokenBudget", "tokensUsed", "timeUsedSeconds", "createdAt", "updatedAt") || m["threadId"] != thread || !stringValue(m["objective"], 32768, true) || !stringValue(m["status"], 64, false) {
		return nil, errPolicy
	}
	for _, key := range []string{"tokenBudget", "tokensUsed", "timeUsedSeconds", "createdAt", "updatedAt"} {
		if m[key] != nil && !integer(m[key], 0, 1<<63-1) {
			return nil, errPolicy
		}
	}
	return m, nil
}

func (p *nativePolicy) notification(b ConnectionBinding, f Frame) (Frame, bool, error) {
	m, ok := payloadObject(f, false)
	if !ok {
		return Frame{}, false, errPolicy
	}
	var out any
	switch f.Method {
	case "thread/goal/cleared":
		if !onlyKeys(m, "threadId") || !threadTarget(m, b) {
			return Frame{}, false, errPolicy
		}
		out = m
	case "thread/status/changed":
		if !onlyKeys(m, "threadId", "status") || !threadTarget(m, b) {
			return Frame{}, false, errPolicy
		}
		status, err := projectStatus(m["status"])
		if err != nil {
			return Frame{}, false, err
		}
		out = map[string]any{"threadId": m["threadId"], "status": status}
	case "thread/name/updated":
		if !onlyKeys(m, "threadId", "threadName") || !threadTarget(m, b) || (mHas(m, "threadName") && !stringValue(m["threadName"], 512, true)) {
			return Frame{}, false, errPolicy
		}
		out = m
	case "thread/started":
		if !onlyKeys(m, "thread") {
			return Frame{}, false, errPolicy
		}
		thread, err := p.projectThread(b, m["thread"], false)
		if err != nil {
			return Frame{}, false, err
		}
		out = map[string]any{"thread": thread}
	default:
		// Includes global remoteControl/status/changed and serverRequest/resolved:
		// the latter needs the authority's exact callback map, absent in this slice.
		return Frame{}, false, nil
	}
	raw, _ := json.Marshal(out)
	return Frame{Kind: Notification, Method: f.Method, Payload: raw}, true, nil
}
