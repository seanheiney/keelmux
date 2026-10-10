// Package skills installs only the selected provider mappings of a pinned
// connection skill. Installation is not evidence of native discovery/loading.
package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"sort"
)

const Revision = "backplane-connection/v1"
const Source = "https://github.com/seanheiney/agent-backplane/tree/main/internal/skills"
const UserScope = "user"
const ProjectScope = "project"

type File struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}
type Mapping struct {
	Provider     string `json:"provider"`
	Scope        string `json:"scope"`
	Directory    string `json:"directory"`
	Action       string `json:"action"`
	Detail       string `json:"detail"`
	Source       string `json:"source"`
	Revision     string `json:"revision"`
	Files        []File `json:"files,omitempty"`
	Invocation   string `json:"invocation,omitempty"`
	Reload       string `json:"reload"`
	Installed    string `json:"installed"`
	Discoverable string `json:"discoverable"`
	Loaded       string `json:"loaded"`
}
type Preview struct {
	Root     string    `json:"root"`
	Scope    string    `json:"scope"`
	Mappings []Mapping `json:"mappings"`
}
type Result = Mapping

func names(selected []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, name := range selected {
		if name != "codex" && name != "claude" && name != "grok" {
			return nil, errors.New("unknown provider selection")
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}
func digest(b []byte) string { d := sha256.Sum256(b); return hex.EncodeToString(d[:]) }
func directory(provider string) string {
	if provider == "codex" {
		return filepath.Join(".agents", "skills", "backplane")
	}
	return filepath.Join(".claude", "skills", "backplane")
}
func catalogue(provider string) map[string][]byte {
	front := "---\nname: backplane\ndescription: Explicitly link this exact managed native terminal to Agent Backplane, inspect its binding, or turn sharing off.\n"
	if provider == "claude" {
		front += "disable-model-invocation: true\n"
	}
	front += "---\n\n# Agent Backplane connection\n\nUse only when the user explicitly invokes this skill. Accept exactly no argument, `status`, or `off`; reject all other input without running a command.\n\nRun the bundled `scripts/link.sh` with `sh`, resolving its path relative to this skill directory. Pass only the single allowlisted argument, if any. Do not interpolate untrusted text into a shell command. Native tool permissions still apply.\n\nThe helper uses inherited private launch correlation. Do not print, inspect, copy, or change BP_LINK_TOKEN or other launch environment values. Do not invent session IDs, select the newest transcript, rotate logins, start another model turn, or take over another terminal.\n\nReturn the authoritative helper receipt and its stated supported next action. A refusal means the native context is unverified; explain the receipt and use the explicit managed launch or attach route. Repeated linking returns its existing binding. `off` changes declared sharing, not the process, login, history, or installed skill.\n"
	files := map[string][]byte{"SKILL.md": []byte(front), "scripts/link.sh": []byte(linkScript)}
	if provider == "codex" {
		files["agents/openai.yaml"] = []byte("interface:\n  display_name: \"Backplane\"\n  short_description: \"Link this exact native terminal\"\n  default_prompt: \"Use $backplane to link this managed terminal.\"\npolicy:\n  allow_implicit_invocation: false\n")
	}
	return files
}

const linkScript = `#!/bin/sh
set -eu
case "$#" in
  0) ;;
  1) case "$1" in status|off) ;; *) printf '%s\n' 'backplane: expected no argument, status, or off' >&2; exit 2 ;; esac ;;
  *) printf '%s\n' 'backplane: expected no argument, status, or off' >&2; exit 2 ;;
esac
if [ -z "${BP_SESSION_ID:-}" ] || [ -z "${BP_EXECUTION_ID:-}" ] || [ -z "${BP_OPERATION_ID:-}" ] || [ -z "${BP_LINK_TOKEN:-}" ] || [ -z "${BP_STATE_DIR:-}" ]; then
  printf '%s\n' 'backplane: no exact inherited managed context; attach the intended managed terminal first' >&2
  exit 2
fi
case "${BP_HELPER_PATH:-}" in
  /*) ;;
  *) printf '%s\n' 'backplane: an absolute inherited helper path is required' >&2; exit 2 ;;
esac
if [ ! -f "$BP_HELPER_PATH" ] || [ ! -x "$BP_HELPER_PATH" ]; then
  printf '%s\n' 'backplane: inherited helper executable is unavailable' >&2
  exit 2
fi
exec "$BP_HELPER_PATH" --state "$BP_STATE_DIR" link "$@"
`
