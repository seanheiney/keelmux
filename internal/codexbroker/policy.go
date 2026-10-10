package codexbroker

import (
	"crypto/sha256"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"
)

const nativePolicyVersion = "0.159.0"

type nativePolicy struct {
	mu        sync.Mutex
	baselines map[[32]byte]Baseline
	turns     map[[32]byte]map[string]bool
}

// NewPolicy returns the zero-turn profile. It has no writer authority or live
// delivery gate; the caller must enforce those separately.
func NewPolicy() Policy {
	return &nativePolicy{baselines: map[[32]byte]Baseline{}, turns: map[[32]byte]map[string]bool{}}
}

// DefaultBaseline accepts only lexical, already home-verified paths. It never
// resolves symlinks, cwd, or another host's paths on this machine.
func DefaultBaseline(workspace string, aliases ...string) Baseline {
	b := Baseline{ProtocolVersion: nativePolicyVersion, Workspace: workspace, WorkspaceAliases: append([]string(nil), aliases...), NativeBootstrap: map[string]json.RawMessage{}}
	b.NativeBootstrap["thread/resume"] = json.RawMessage(`{"features":{"plugins":false,"apps":false},"web_search":"cached"}`)
	return b
}

func bindingKey(b ConnectionBinding) [32]byte { raw, _ := json.Marshal(b); return sha256.Sum256(raw) }
func cleanAbsolute(s string) bool {
	return len(s) > 0 && len(s) <= 4096 && utf8.ValidString(s) && !strings.ContainsRune(s, 0) && filepath.IsAbs(s) && filepath.Clean(s) == s
}
func baselineValid(b Baseline) bool {
	if b.ProtocolVersion != nativePolicyVersion || !cleanAbsolute(b.Workspace) || len(b.WorkspaceAliases) > 16 {
		return false
	}
	for _, alias := range b.WorkspaceAliases {
		if !cleanAbsolute(alias) {
			return false
		}
	}
	v, err := readJSON(b.NativeBootstrap["thread/resume"])
	return err == nil && resumeConfig(v)
}
func approvedPath(v any, b Baseline) bool {
	s, ok := v.(string)
	if !ok || !cleanAbsolute(s) {
		return false
	}
	if s == b.Workspace {
		return true
	}
	for _, alias := range b.WorkspaceAliases {
		if s == alias {
			return true
		}
	}
	return false
}
func scopedBinding(b ConnectionBinding) bool {
	return b.ID != "" && b.Agent.HostID != "" && b.Agent.SessionID != "" && b.Agent.ExecutionID != "" && b.Agent.Generation > 0 && b.Agent.NativeThreadID != "" && b.Agent.AppServerIncarnation != "" && b.Agent.AppServerPID > 0 && b.Agent.AppServerBootID != "" && b.Agent.AppServerProcessStart != "" && b.Agent.ProtocolVersion == nativePolicyVersion && (b.Role == NativeLocal || b.Role == TypedObserver || b.Role == TypedWriter)
}
func nativeBinding(b ConnectionBinding) bool {
	l := b.Writer.Lease
	return b.Role == NativeLocal && b.Writer.Kind == NativeWriter && b.Writer.PrincipalID != "" && b.Writer.Epoch > 0 && l.ClientID != "" && l.OperationID != "" && l.Token != "" && l.SessionID == b.Agent.SessionID && l.ExecutionID == b.Agent.ExecutionID && l.Generation == b.Agent.Generation && b.NativeLaunchID != "" && b.EndpointIncarnation != "" && b.NativePID > 0 && b.NativeProcessStart != ""
}
func resumeConfig(v any) bool {
	m, ok := v.(map[string]any)
	if !ok || len(m) != 2 || !onlyKeys(m, "features", "web_search") || m["web_search"] != "cached" {
		return false
	}
	f, ok := m["features"].(map[string]any)
	return ok && len(f) == 2 && onlyKeys(f, "plugins", "apps") && f["plugins"] == false && f["apps"] == false
}
func threadTarget(m map[string]any, b ConnectionBinding) bool {
	return m["threadId"] == b.Agent.NativeThreadID
}
func firstPage(m map[string]any, max int64) bool {
	return m["cursor"] == nil && (m["limit"] == nil || integer(m["limit"], 1, max))
}
func approvedCwds(v any, b Baseline) bool {
	a, ok := v.([]any)
	if !ok || len(a) == 0 || len(a) > 16 {
		return false
	}
	for _, cwd := range a {
		if !approvedPath(cwd, b) {
			return false
		}
	}
	return true
}

func (p *nativePolicy) Classify(b ConnectionBinding, f Frame, baseline Baseline) (EffectClass, error) {
	deny := func() (EffectClass, error) { return Denied, errPolicy }
	if !scopedBinding(b) || !baselineValid(baseline) {
		return deny()
	}
	if f.Kind == Notification {
		if f.Method != "initialized" || len(f.ID) != 0 || len(f.Payload) != 0 || !nativeBinding(b) {
			return deny()
		}
		return NativeBootstrap, nil
	}
	if f.Kind != Request {
		return deny()
	} // Includes every methodless approval/auth reply.
	if _, ok := validatedID(f.ID); !ok {
		return deny()
	}
	m, ok := payloadObject(f, f.Method == "configRequirements/read")
	if !ok {
		return deny()
	}
	class := NativeBootstrap
	valid := false
	switch f.Method {
	case "initialize":
		ci, ciOK := m["clientInfo"].(map[string]any)
		cap, capOK := m["capabilities"].(map[string]any)
		valid = onlyKeys(m, "clientInfo", "capabilities") && ciOK && capOK && onlyKeys(ci, "name", "title", "version") && ci["name"] == "codex-tui" && ci["version"] == nativePolicyVersion && (ci["title"] == nil || stringValue(ci["title"], 128, true)) && onlyKeys(cap, "experimentalApi", "requestAttestation", "optOutNotificationMethods") && cap["experimentalApi"] == true && cap["requestAttestation"] == false && cap["optOutNotificationMethods"] == nil
	case "account/read":
		valid = onlyKeys(m, "refreshToken") && (!mHas(m, "refreshToken") || m["refreshToken"] == false)
	case "config/read":
		valid = onlyKeys(m, "includeLayers", "cwd") && m["includeLayers"] == true && approvedPath(m["cwd"], baseline)
	case "configRequirements/read", "collaborationMode/list":
		valid = len(m) == 0
	case "hooks/list":
		valid = onlyKeys(m, "cwds") && approvedCwds(m["cwds"], baseline)
	case "skills/list":
		valid = onlyKeys(m, "cwds", "forceReload") && approvedCwds(m["cwds"], baseline) && m["forceReload"] == true
	case "model/list":
		valid = onlyKeys(m, "cursor", "limit", "includeHidden") && firstPage(m, 100) && m["includeHidden"] == true
	case "thread/resume":
		valid = onlyKeys(m, "threadId", "history", "path", "model", "modelProvider", "cwd", "runtimeWorkspaceRoots", "approvalPolicy", "approvalsReviewer", "sandbox", "permissions", "config", "baseInstructions", "developerInstructions", "personality", "excludeTurns", "initialTurnsPage") && threadTarget(m, b) && approvedPath(m["cwd"], baseline) && m["excludeTurns"] == true && resumeConfig(m["config"]) && nullOnly(m, "history", "path", "model", "modelProvider", "runtimeWorkspaceRoots", "approvalPolicy", "approvalsReviewer", "sandbox", "permissions", "baseInstructions", "developerInstructions", "personality", "initialTurnsPage")
	case "thread/read":
		class = ScopedRead
		_, includeBool := m["includeTurns"].(bool)
		valid = onlyKeys(m, "threadId", "includeTurns") && threadTarget(m, b) && (!mHas(m, "includeTurns") || includeBool)
	case "thread/goal/get":
		class = ScopedRead
		valid = onlyKeys(m, "threadId") && threadTarget(m, b)
	case "thread/turns/list":
		class = ScopedRead
		valid = onlyKeys(m, "threadId", "cursor", "limit", "sortDirection", "itemsView") && threadTarget(m, b) && firstPage(m, 100) && enumOrNull(m["sortDirection"], "asc", "desc") && enumOrNull(m["itemsView"], "notLoaded", "summary", "full")
	case "thread/items/list":
		class = ScopedRead
		valid = onlyKeys(m, "threadId", "turnId", "cursor", "limit", "sortDirection") && threadTarget(m, b) && firstPage(m, 100) && enumOrNull(m["sortDirection"], "asc", "desc")
		if m["turnId"] != nil {
			turn, good := m["turnId"].(string)
			valid = valid && good && p.knownTurn(b, turn)
		}
	case "thread/loaded/list":
		class = ScopedRead
		valid = onlyKeys(m, "cursor", "limit") && firstPage(m, 100)
	case "thread/list":
		class = ScopedRead
		valid = onlyKeys(m, "cursor", "limit", "sortKey", "sortDirection", "modelProviders", "sourceKinds", "originators", "archived", "cwd", "useStateDbOnly", "searchTerm", "parentThreadId", "ancestorThreadId") && firstPage(m, 100) && enumOrNull(m["sortKey"], "recency_at") && enumOrNull(m["sortDirection"], "asc", "desc") && (m["modelProviders"] == nil || stringsArray(m["modelProviders"], 0)) && (m["sourceKinds"] == nil || stringsArray(m["sourceKinds"], 2, "exec", "appServer")) && boolOrNull(m["archived"]) && m["useStateDbOnly"] == true && nullOnly(m, "originators", "searchTerm", "parentThreadId", "ancestorThreadId") && (m["cwd"] == nil || approvedPath(m["cwd"], baseline))
	}
	if !valid || (class == NativeBootstrap && !nativeBinding(b)) {
		return deny()
	}
	p.mu.Lock()
	if len(p.baselines) >= 1024 {
		if _, exists := p.baselines[bindingKey(b)]; !exists {
			p.mu.Unlock()
			return deny()
		}
	}
	copyBaseline := baseline
	copyBaseline.WorkspaceAliases = append([]string(nil), baseline.WorkspaceAliases...)
	copyBaseline.NativeBootstrap = nil
	p.baselines[bindingKey(b)] = copyBaseline
	p.mu.Unlock()
	return class, nil
}

func (p *nativePolicy) knownTurn(b ConnectionBinding, turn string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return turn != "" && p.turns[bindingKey(b)][turn]
}
func (p *nativePolicy) rememberTurns(b ConnectionBinding, turns []string) {
	if len(turns) == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	k := bindingKey(b)
	if p.turns[k] == nil {
		if len(p.turns) >= 1024 {
			return
		}
		p.turns[k] = map[string]bool{}
	}
	for _, turn := range turns {
		if len(p.turns[k]) < 4096 {
			p.turns[k][turn] = true
		}
	}
}
func (p *nativePolicy) outputPath(b ConnectionBinding, v any, requested any) bool {
	p.mu.Lock()
	baseline, known := p.baselines[bindingKey(b)]
	p.mu.Unlock()
	if known {
		return approvedPath(v, baseline)
	}
	return cleanPathEqual(v, requested)
}
func cleanPathEqual(a, b any) bool {
	s, ok := a.(string)
	return ok && cleanAbsolute(s) && a == b
}
