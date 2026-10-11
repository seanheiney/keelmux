package native

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/seanheiney/keelmux/internal/library"
)

const maxRulesArg = 64 << 10
const maxEncodedRulesArg = 32 << 10

// Grok carries this literal text inside a JSON launch request. Count the same
// encoding, including quotes and escapes, and leave room for argv and metadata
// under the existing 64 KiB transport ceiling. The launcher checks the full
// request separately; this bound alone is not a request-size guarantee.
func rulesArgumentWithinBudget(text string) bool {
	if len(text) > maxRulesArg {
		return false
	}
	encoded, err := json.Marshal(text)
	return err == nil && len(encoded) <= maxEncodedRulesArg
}

var skillName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type mapping struct {
	files      map[string][]byte
	roots      []string
	guards     []string
	args       []string
	unmapped   []Unmapped
	collisions []Collision
}

func (m *mapping) add(p string, b []byte) error {
	if !relative(p) || len(b) > maxFileBytes || len(m.files) >= maxFiles {
		return ErrUnsafe
	}
	if _, ok := m.files[p]; ok {
		m.collisions = append(m.collisions, Collision{p, "two declarations map to the same native path"})
		return nil
	}
	m.files[p] = b
	return nil
}
func (m *mapping) unsupported(pack, kind, id, field, why string) {
	m.unmapped = append(m.unmapped, Unmapped{pack, kind, id, field, why})
}

// supportedSkill deliberately accepts a narrow portable frontmatter subset.
// Unknown executable/permission/native metadata is not silently stripped or
// activated. Content and companion assets remain byte-identical when mapped.
func supportedSkill(b []byte, id string) (string, bool) {
	if !utf8.Valid(b) || len(id) > 64 || !skillName.MatchString(id) {
		return "native skill names require lower-case alphanumeric words separated by single hyphens", false
	}
	s := string(b)
	if !strings.HasPrefix(s, "---\n") {
		return "portable SKILL.md frontmatter is required", false
	}
	end := strings.Index(s[4:], "\n---\n")
	if end < 0 {
		return "portable SKILL.md frontmatter is required", false
	}
	name, desc := "", ""
	for _, line := range strings.Split(s[4:4+end], "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			return "multiline or structured frontmatter is outside this mapper's portable subset", false
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			return "invalid portable frontmatter", false
		}
		v = strings.TrimSpace(v)
		switch k {
		case "name":
			name = v
		case "description":
			desc = v
		case "license", "compatibility":
		default:
			return "unmapped frontmatter field: " + k, false
		}
	}
	if name != id || desc == "" || len(desc) > 1024 {
		return "frontmatter name must equal asset ID and description must be 1–1024 bytes", false
	}
	return "", true
}
func readPackages(ctx context.Context, reg library.Registry, r library.Resolution) (map[string]map[string][]byte, error) {
	out := map[string]map[string][]byte{}
	total, count := 0, 0
	for _, p := range r.Packages {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		if !identifier.MatchString(p.Manifest.ID) || !digestPattern.MatchString(p.Digest) {
			return nil, ErrUnsafe
		}
		if _, ok := out[p.Manifest.ID]; ok {
			return nil, ErrUnsafe
		}
		root, e := reg.PackageRoot(ctx, p.Manifest.ID, p.Digest)
		if e != nil {
			return nil, e
		}
		t, e := openTree(root)
		if e != nil {
			return nil, e
		}
		files := map[string][]byte{}
		for path, want := range p.Manifest.Files {
			if !relative(path) || !digestPattern.MatchString(want) {
				t.close()
				return nil, ErrUnsafe
			}
			b, present, e := t.read(path, maxFileBytes)
			if e != nil {
				t.close()
				return nil, e
			}
			if !present || hash(b) != want {
				t.close()
				return nil, fmt.Errorf("%w: package file %s", ErrDrift, path)
			}
			count++
			total += len(b)
			if count > maxFiles || total > maxTotalBytes {
				t.close()
				return nil, ErrUnsafe
			}
			files[path] = b
		}
		if e = t.bound(); e != nil {
			t.close()
			return nil, e
		}
		t.close()
		out[p.Manifest.ID] = files
	}
	return out, nil
}
func buildMapping(ctx context.Context, reg library.Registry, r library.Resolution, t Target) (mapping, error) {
	m := mapping{files: map[string][]byte{}}
	packs, e := readPackages(ctx, reg, r)
	if e != nil {
		return m, e
	}
	pins := map[string]string{}
	for _, p := range r.Packages {
		pins[p.Manifest.ID] = p.Digest
	}
	base := appPath(t, "generated")
	var rules, index strings.Builder
	for _, a := range r.Skills {
		if pins[a.PackageID] != a.PackageDigest || !relative(a.Asset.Path) {
			return m, ErrUnsafe
		}
		files := packs[a.PackageID]
		skill := files[filepath.Join(a.Asset.Path, "SKILL.md")]
		if why, ok := supportedSkill(skill, a.Asset.ID); !ok {
			m.unsupported(a.PackageID, "skill", a.Asset.ID, "frontmatter", why)
			continue
		}
		var root string
		switch t.Harness {
		case "codex":
			root = filepath.Join(".agents/skills", a.Asset.ID)
		case "claude":
			root = filepath.Join(".claude/skills", a.Asset.ID)
			m.guards = append(m.guards, filepath.Join(".agents/skills", a.Asset.ID, "SKILL.md"))
		case "gemini":
			root = filepath.Join(".gemini/skills", a.Asset.ID)
			m.guards = append(m.guards, filepath.Join(".agents/skills", a.Asset.ID, "SKILL.md"))
		case "opencode":
			root = filepath.Join(".opencode/skills", a.Asset.ID)
			m.guards = append(m.guards, filepath.Join(".agents/skills", a.Asset.ID, "SKILL.md"), filepath.Join(".claude/skills", a.Asset.ID, "SKILL.md"))
		case "grok":
			root = filepath.Join(base, "skills", a.PackageID, a.Asset.ID)
		}
		m.roots = append(m.roots, root)
		paths := make([]string, 0, len(files))
		for path := range files {
			if strings.HasPrefix(path, a.Asset.Path+"/") {
				paths = append(paths, path)
			}
		}
		sort.Strings(paths)
		for _, path := range paths {
			if e = m.add(filepath.Join(root, strings.TrimPrefix(path, a.Asset.Path+"/")), files[path]); e != nil {
				return m, e
			}
		}
		if t.Harness == "grok" {
			fmt.Fprintf(&index, "- %s: read the applied skill instructions at %s when that capability is needed.\n", a.Asset.ID, canonical(filepath.Join(t.Workspace, root, "SKILL.md")))
		}
	}
	for _, a := range r.Rules {
		if pins[a.PackageID] != a.PackageDigest || !relative(a.Asset.Path) || !identifier.MatchString(a.Asset.ID) {
			return m, ErrUnsafe
		}
		b, ok := packs[a.PackageID][a.Asset.Path]
		if !ok {
			return m, ErrUnsafe
		}
		if !utf8.Valid(b) || strings.IndexByte(string(b), 0) >= 0 {
			m.unsupported(a.PackageID, "rule", a.Asset.ID, "content", "rule must be UTF-8 text without NUL")
			continue
		}
		if t.Harness == "claude" {
			e = m.add(filepath.Join(".claude/rules", "keelmux-"+t.ApplicationID+"-"+a.PackageID+"-"+a.Asset.ID+".md"), b)
			if e != nil {
				return m, e
			}
		} else {
			fmt.Fprintf(&rules, "\n<!-- %s/%s -->\n%s\n", a.PackageID, a.Asset.ID, b)
		}
	}
	servers := map[string]any{}
	var toml strings.Builder
	for _, a := range r.Tools {
		tool := a.Tool
		if pins[a.PackageID] != a.PackageDigest || !identifier.MatchString(tool.ID) {
			return m, ErrUnsafe
		}
		if t.Harness == "grok" {
			m.unsupported(a.PackageID, "tool", tool.ID, "transport", "installed Grok help does not establish an MCP configuration hook")
			continue
		}
		refs := map[string]string{}
		bad := false
		for _, name := range tool.EnvRefs {
			if !envName.MatchString(name) {
				return m, ErrUnsafe
			}
			if _, ok := refs[name]; ok {
				return m, ErrUnsafe
			}
			if t.Harness == "opencode" {
				refs[name] = "{env:" + name + "}"
			} else {
				refs[name] = "${" + name + "}"
			}
		}
		if tool.Transport == "https" {
			u, e := url.Parse(tool.URL)
			if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
				m.unsupported(a.PackageID, "tool", tool.ID, "url", "HTTPS endpoint must have no embedded authentication, query or fragment")
				bad = true
			}
			if len(tool.EnvRefs) > 0 {
				m.unsupported(a.PackageID, "tool", tool.ID, "env_refs", "HTTPS environment names do not specify an authentication header mapping")
				bad = true
			}
		}
		if tool.Transport == "stdio" {
			if len(tool.Command) == 0 || len(tool.Command) > 128 {
				return m, ErrUnsafe
			}
			for _, arg := range tool.Command {
				if !validText(arg) || len(arg) > 65536 {
					return m, ErrUnsafe
				}
			}
		} else if tool.Transport != "https" {
			m.unsupported(a.PackageID, "tool", tool.ID, "transport", "unsupported transport")
			bad = true
		}
		if bad {
			continue
		}
		name := "km-" + t.ApplicationID + "-" + a.PackageID + "-" + tool.ID
		if _, ok := servers[name]; ok {
			m.collisions = append(m.collisions, Collision{name, "duplicate MCP server name"})
			continue
		}
		conf := map[string]any{}
		if t.Harness == "codex" {
			fmt.Fprintf(&toml, "\n[mcp_servers.%s]\n", string(canonical(name)))
			if tool.Transport == "stdio" {
				fmt.Fprintf(&toml, "command = %s\nargs = %s\n", canonical(tool.Command[0]), canonical(append([]string{}, tool.Command[1:]...)))
				if len(tool.EnvRefs) > 0 {
					fmt.Fprintf(&toml, "env_vars = %s\n", canonical(tool.EnvRefs))
				}
			} else {
				fmt.Fprintf(&toml, "url = %s\n", canonical(tool.URL))
			}
		} else if t.Harness == "opencode" {
			if tool.Transport == "stdio" {
				conf["type"] = "local"
				conf["command"] = tool.Command
				if len(refs) > 0 {
					conf["environment"] = refs
				}
			} else {
				conf["type"] = "remote"
				conf["url"] = tool.URL
			}
		} else {
			if tool.Transport == "stdio" {
				conf["command"] = tool.Command[0]
				conf["args"] = append([]string{}, tool.Command[1:]...)
				if len(refs) > 0 {
					conf["env"] = refs
				}
			} else if t.Harness == "gemini" {
				conf["httpUrl"] = tool.URL
			} else {
				conf["type"] = "http"
				conf["url"] = tool.URL
			}
		}
		servers[name] = conf
	}
	switch t.Harness {
	case "codex":
		if rules.Len() > 0 {
			e = m.add("AGENTS.md", []byte(rules.String()))
		}
		if e == nil && toml.Len() > 0 {
			e = m.add(".codex/config.toml", []byte(toml.String()))
		}
	case "gemini":
		if rules.Len() > 0 {
			e = m.add("GEMINI.md", []byte(rules.String()))
		}
		if e == nil && len(servers) > 0 {
			e = m.add(".gemini/settings.json", canonical(map[string]any{"mcpServers": servers}))
		}
	case "claude":
		if len(servers) > 0 {
			path := filepath.Join(base, "mcp.json")
			e = m.add(path, canonical(map[string]any{"mcpServers": servers}))
			m.args = []string{"--mcp-config", filepath.Join(t.Workspace, path)}
			m.guards = append(m.guards, ".mcp.json")
		}
	case "opencode":
		conf := map[string]any{}
		if rules.Len() > 0 {
			path := filepath.Join(base, "rules.md")
			e = m.add(path, []byte(rules.String()))
			conf["instructions"] = []string{path}
		}
		if len(servers) > 0 {
			conf["mcp"] = servers
		}
		if e == nil && len(conf) > 0 {
			e = m.add("opencode.json", canonical(conf))
			m.guards = append(m.guards, "opencode.jsonc")
		}
	case "grok":
		text := rules.String()
		if index.Len() > 0 {
			text += "\n## Skill instruction index\nInstruction compatibility mapping; this is not native skill discovery. Loading and invocation remain unknown.\n" + index.String()
		}
		if !rulesArgumentWithinBudget(text) {
			return m, fmt.Errorf("%w: Grok rules/index exceeds 32 KiB JSON-encoded argument budget", ErrUnsupported)
		}
		if text != "" {
			e = m.add(filepath.Join(base, "rules.md"), []byte(text))
			m.args = []string{"--rules", text}
		}
	}
	if e != nil {
		return m, e
	}
	total := 0
	for _, b := range m.files {
		total += len(b)
	}
	if total > maxTotalBytes {
		return m, ErrUnsafe
	}
	sort.Slice(m.unmapped, func(i, j int) bool { return string(canonical(m.unmapped[i])) < string(canonical(m.unmapped[j])) })
	return m, nil
}
