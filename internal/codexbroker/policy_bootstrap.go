package codexbroker

func (p *nativePolicy) projectBootstrap(b ConnectionBinding, method string, params, m map[string]any) (any, error) {
	switch method {
	case "initialize":
		out := map[string]any{}
		for _, key := range []string{"userAgent", "codexHome", "platformFamily", "platformOs"} {
			if !stringValue(m[key], 4096, false) {
				return nil, errPolicy
			}
			out[key] = m[key]
		}
		if !cleanAbsolute(m["codexHome"].(string)) {
			return nil, errPolicy
		}
		// This home path is solely for the owned native client, never typed viewers.
		return out, nil
	case "account/read":
		return projectAccount(m)
	case "config/read":
		return projectConfig(m)
	case "configRequirements/read":
		if !onlyKeys(m, "requirements") || !mHas(m, "requirements") || m["requirements"] != nil {
			return nil, errProjection
		}
		return map[string]any{"requirements": nil}, nil
	case "collaborationMode/list":
		a, ok := m["data"].([]any)
		if !ok || len(a) > 16 {
			return nil, errPolicy
		}
		data := []any{}
		for _, value := range a {
			mode, ok := value.(map[string]any)
			if !ok || !onlyKeys(mode, "name", "mode", "model", "reasoning_effort") || !stringValue(mode["name"], 128, false) || !enumOrNull(mode["mode"], "plan", "default") || (mode["model"] != nil && !stringValue(mode["model"], 256, false)) || (mode["reasoning_effort"] != nil && !stringValue(mode["reasoning_effort"], 64, false)) {
				return nil, errPolicy
			}
			data = append(data, mode)
		}
		return map[string]any{"data": data}, nil
	case "model/list":
		return projectModels(m)
	case "skills/list", "hooks/list":
		return p.projectDiscovery(b, method, params, m)
	}
	return nil, errPolicy
}

func projectAccount(m map[string]any) (any, error) {
	auth, ok := m["requiresOpenaiAuth"].(bool)
	if !ok || !mHas(m, "account") {
		return nil, errPolicy
	}
	var account any
	if m["account"] != nil {
		a, ok := m["account"].(map[string]any)
		if !ok {
			return nil, errPolicy
		}
		out := map[string]any{"type": a["type"]}
		switch a["type"] {
		case "apiKey":
		case "chatgpt":
			if !enumOrNull(a["planType"], "free", "go", "plus", "pro", "prolite", "promax", "team", "self_serve_business_prolite", "self_serve_business_usage_based", "business", "ent26", "enterprise_cbp_automation", "enterprise_cbp_usage_based", "enterprise", "edu", "edu_plus", "edu_pro", "unknown") || a["planType"] == nil {
				return nil, errPolicy
			}
			out["planType"] = a["planType"]
			out["email"] = nil
		case "amazonBedrock":
			if _, ok := a["usesCodexManagedCredentials"].(bool); !ok {
				return nil, errPolicy
			}
			out["usesCodexManagedCredentials"] = a["usesCodexManagedCredentials"]
		default:
			return nil, errProjection
		}
		account = out
	}
	return map[string]any{"account": account, "requiresOpenaiAuth": auth, "workspaceRouting": nil}, nil
}

func approvalPolicy(v any) bool {
	if enumOrNull(v, "untrusted", "on-request", "never") && v != nil {
		return true
	}
	m, ok := v.(map[string]any)
	if !ok || len(m) != 1 {
		return false
	}
	g, ok := m["granular"].(map[string]any)
	if !ok || len(g) != 5 || !onlyKeys(g, "sandbox_approval", "rules", "skill_approval", "request_permissions", "mcp_elicitations") {
		return false
	}
	for _, v := range g {
		if _, ok := v.(bool); !ok {
			return false
		}
	}
	return true
}

func projectConfig(m map[string]any) (any, error) {
	c, ok := m["config"].(map[string]any)
	if !ok {
		return nil, errPolicy
	}
	out := map[string]any{}
	// Every generated required Config field is represented; arbitrary TOML/JSON
	// keys, auth/provider definitions, paths and instruction text are not copied.
	for _, key := range []string{"model", "review_model", "model_context_window", "model_auto_compact_token_limit", "model_auto_compact_token_limit_scope", "model_provider", "approval_policy", "approvals_reviewer", "sandbox_mode", "sandbox_workspace_write", "forced_chatgpt_workspace_id", "forced_login_method", "web_search", "tools", "instructions", "developer_instructions", "compact_prompt", "model_reasoning_effort", "model_reasoning_summary", "model_verbosity", "service_tier", "analytics", "apps", "browser_use", "computer_use", "desktop"} {
		out[key] = nil
	}
	for _, key := range []string{"model", "review_model", "model_provider", "service_tier", "model_reasoning_effort", "model_reasoning_summary", "model_verbosity", "model_auto_compact_token_limit_scope"} {
		if c[key] != nil && !stringValue(c[key], 256, false) {
			return nil, errPolicy
		}
		out[key] = c[key]
	}
	for _, key := range []string{"model_context_window", "model_auto_compact_token_limit"} {
		if c[key] != nil && !integer(c[key], 0, 1<<63-1) {
			return nil, errPolicy
		}
		out[key] = c[key]
	}
	if c["approval_policy"] != nil && !approvalPolicy(c["approval_policy"]) {
		return nil, errPolicy
	}
	out["approval_policy"] = c["approval_policy"]
	if !enumOrNull(c["sandbox_mode"], "read-only", "workspace-write", "danger-full-access") || !enumOrNull(c["approvals_reviewer"], "user", "auto_review", "guardian_subagent") || !enumOrNull(c["web_search"], "disabled", "cached", "live") {
		return nil, errPolicy
	}
	for _, key := range []string{"sandbox_mode", "approvals_reviewer", "web_search"} {
		out[key] = c[key]
	}
	if c["sandbox_workspace_write"] != nil {
		return nil, errProjection
	}
	if c["features"] != nil {
		f, ok := c["features"].(map[string]any)
		if !ok || (f["plugins"] != nil && f["plugins"] != false) || (f["apps"] != nil && f["apps"] != false) {
			return nil, errProjection
		}
		features := map[string]any{}
		for _, key := range []string{"plugins", "apps"} {
			if f[key] != nil {
				features[key] = f[key]
			}
		}
		out["features"] = features
	}
	return map[string]any{"config": out, "origins": map[string]any{}, "layers": nil}, nil
}

func projectModels(m map[string]any) (any, error) {
	if !onlyKeys(m, "data", "nextCursor") || m["nextCursor"] != nil {
		return nil, errProjection
	}
	a, ok := m["data"].([]any)
	if !ok || len(a) > 100 {
		return nil, errPolicy
	}
	data := []any{}
	for _, value := range a {
		model, ok := value.(map[string]any)
		if !ok || !stringValue(model["id"], 256, false) || !stringValue(model["model"], 256, false) {
			return nil, errPolicy
		}
		out := map[string]any{}
		for _, key := range []string{"id", "model", "displayName", "description", "defaultReasoningEffort"} {
			if !stringValue(model[key], 8192, true) {
				return nil, errPolicy
			}
			out[key] = model[key]
		}
		for _, key := range []string{"upgrade", "modelSpecialty", "multiAgentVersion", "defaultServiceTier"} {
			if model[key] != nil && !stringValue(model[key], 256, false) {
				return nil, errPolicy
			}
			out[key] = model[key]
		}
		for _, key := range []string{"hidden", "supportsPersonality", "isDefault"} {
			if _, ok := model[key].(bool); !ok {
				return nil, errPolicy
			}
			out[key] = model[key]
		}
		for _, key := range []string{"inputModalities", "additionalSpeedTiers"} {
			if !stringsArray(model[key], 16) {
				return nil, errPolicy
			}
			out[key] = model[key]
		}
		for _, spec := range []struct {
			key    string
			fields []string
		}{{"supportedReasoningEfforts", []string{"reasoningEffort", "description"}}, {"serviceTiers", []string{"id", "name", "description"}}} {
			rows, ok := model[spec.key].([]any)
			if !ok || len(rows) > 16 {
				return nil, errPolicy
			}
			projected := []any{}
			for _, row := range rows {
				entry, ok := row.(map[string]any)
				if !ok {
					return nil, errPolicy
				}
				public := map[string]any{}
				for _, field := range spec.fields {
					if !stringValue(entry[field], 8192, true) {
						return nil, errPolicy
					}
					public[field] = entry[field]
				}
				projected = append(projected, public)
			}
			out[spec.key] = projected
		}
		for _, key := range []string{"upgradeInfo", "availabilityNux", "availableAccessPrograms"} {
			out[key] = nil
		}
		data = append(data, out)
	}
	return map[string]any{"data": data, "nextCursor": nil}, nil
}

func (p *nativePolicy) projectDiscovery(b ConnectionBinding, method string, params, m map[string]any) (any, error) {
	requested, ok := params["cwds"].([]any)
	if !ok || len(requested) == 0 {
		return nil, errPolicy
	}
	a, ok := m["data"].([]any)
	if !ok || len(a) > 16 {
		return nil, errPolicy
	}
	data := []any{}
	for _, value := range a {
		entry, ok := value.(map[string]any)
		if !ok {
			return nil, errPolicy
		}
		match := false
		for _, cwd := range requested {
			match = match || cleanPathEqual(entry["cwd"], cwd)
		}
		if !match {
			return nil, errPolicy
		}
		if errors, ok := entry["errors"].([]any); !ok || len(errors) != 0 {
			return nil, errProjection
		}
		out := map[string]any{"cwd": entry["cwd"], "errors": []any{}}
		if method == "hooks/list" {
			hooks, ok := entry["hooks"].([]any)
			warnings, warningsOK := entry["warnings"].([]any)
			if !ok || !warningsOK || len(hooks) != 0 || len(warnings) != 0 {
				return nil, errProjection
			}
			out["hooks"] = []any{}
			out["warnings"] = []any{}
		} else {
			a, ok := entry["skills"].([]any)
			if !ok || len(a) > 128 {
				return nil, errPolicy
			}
			skills := []any{}
			for _, value := range a {
				skill, ok := value.(map[string]any)
				if !ok || !stringValue(skill["name"], 256, false) || !stringValue(skill["description"], 32768, true) || !stringValue(skill["path"], 4096, false) || !cleanAbsolute(skill["path"].(string)) || !enumOrNull(skill["scope"], "user", "repo", "system", "admin") || skill["scope"] == nil {
					return nil, errPolicy
				}
				if _, ok := skill["enabled"].(bool); !ok {
					return nil, errPolicy
				}
				public := map[string]any{"name": skill["name"], "description": skill["description"], "path": skill["path"], "scope": skill["scope"], "enabled": skill["enabled"], "pluginId": nil}
				if skill["shortDescription"] != nil {
					if !stringValue(skill["shortDescription"], 1024, true) {
						return nil, errPolicy
					}
					public["shortDescription"] = skill["shortDescription"]
				}
				skills = append(skills, public)
			}
			out["skills"] = skills
		}
		data = append(data, out)
	}
	return map[string]any{"data": data}, nil
}

func (p *nativePolicy) projectResume(b ConnectionBinding, m, params, thread map[string]any) (any, error) {
	out := map[string]any{"thread": thread}
	for _, key := range []string{"model", "modelProvider", "serviceTier", "reasoningEffort", "multiAgentMode"} {
		if m[key] != nil && !stringValue(m[key], 256, false) {
			return nil, errPolicy
		}
		if mHas(m, key) {
			out[key] = m[key]
		}
	}
	if cwd, exists := m["cwd"]; exists {
		if !p.outputPath(b, cwd, params["cwd"]) {
			return nil, errPolicy
		}
		out["cwd"] = cwd
	}
	if mHas(m, "approvalPolicy") {
		if !approvalPolicy(m["approvalPolicy"]) {
			return nil, errPolicy
		}
		out["approvalPolicy"] = m["approvalPolicy"]
	}
	if mHas(m, "approvalsReviewer") {
		if !enumOrNull(m["approvalsReviewer"], "user", "auto_review", "guardian_subagent") || m["approvalsReviewer"] == nil {
			return nil, errPolicy
		}
		out["approvalsReviewer"] = m["approvalsReviewer"]
	}
	if mHas(m, "sandbox") {
		sandbox, ok := m["sandbox"].(map[string]any)
		if !ok {
			return nil, errPolicy
		}
		switch sandbox["type"] {
		case "readOnly":
			if !onlyKeys(sandbox, "type", "networkAccess") {
				return nil, errPolicy
			}
			if _, ok := sandbox["networkAccess"].(bool); !ok {
				return nil, errPolicy
			}
		case "dangerFullAccess":
			if !onlyKeys(sandbox, "type") {
				return nil, errPolicy
			}
		default:
			return nil, errProjection
		}
		out["sandbox"] = sandbox
	}
	if mHas(m, "runtimeWorkspaceRoots") {
		roots, ok := m["runtimeWorkspaceRoots"].([]any)
		if !ok || len(roots) > 16 {
			return nil, errPolicy
		}
		for _, root := range roots {
			if !p.outputPath(b, root, params["cwd"]) {
				return nil, errPolicy
			}
		}
		out["runtimeWorkspaceRoots"] = roots
	}
	if !nullOnly(m, "initialTurnsPage", "turnsBackwardsCursor", "itemsBackwardsCursor", "activePermissionProfile") {
		return nil, errProjection
	}
	for _, key := range []string{"initialTurnsPage", "turnsBackwardsCursor", "itemsBackwardsCursor", "activePermissionProfile", "collaborationMode"} {
		out[key] = nil
	}
	out["disabledPluginIds"] = []any{}
	out["instructionSources"] = []any{}
	return out, nil
}
