// Package tools distinguishes a declared MCP tool from an available executable,
// a successful health handshake, and an actual tool invocation. Check never
// invokes tools. Callers must resolve and verify the selected library pin first.
package tools

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/seanheiney/keelmux/internal/library"
)

type Options struct {
	Health           bool
	WorkingDirectory string
	Timeout          time.Duration               // Default 5 seconds, maximum 15 seconds; cleanup has a separate bound.
	LookupEnv        func(string) (string, bool) // Defaults to os.LookupEnv; values never enter receipts.
}

type Receipt struct {
	PackageID       string    `json:"package_id,omitempty"`
	PackageDigest   string    `json:"package_digest,omitempty"`
	ToolID          string    `json:"tool_id,omitempty"`
	Transport       string    `json:"transport,omitempty"`
	ObservedAt      time.Time `json:"observed_at"`
	Declared        bool      `json:"declared"`
	Availability    string    `json:"availability"` // available, unavailable, unknown
	Health          string    `json:"health"`       // not_checked, healthy, unhealthy, unsupported
	Started         bool      `json:"started"`
	Initialized     bool      `json:"initialized"`
	ToolsListed     bool      `json:"tools_listed"`
	ToolCount       int       `json:"tool_count"`
	ProtocolVersion string    `json:"protocol_version,omitempty"`
	Invoked         bool      `json:"invoked"` // Always false: no tools/call request exists in this package.
	Cleanup         string    `json:"cleanup"` // not_needed, complete, failed
	Code            string    `json:"code"`    // Finite local codes; never native error strings.
}

var identifier = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// Check's zero Options inspects filesystem/environment availability only. Health
// explicitly authorizes starting the selected command, whose own code remains
// trusted by the caller. Private HOME/cache/temp are not an operating-system
// sandbox. HTTPS is unqualified and never contacted, even for explicit health.
func Check(ctx context.Context, selected library.ResolvedTool, options Options) Receipt {
	r := Receipt{ObservedAt: time.Now().UTC(), Availability: "unknown", Health: "not_checked", Cleanup: "not_needed", Code: "invalid_declaration"}
	if !validDeclaration(selected) {
		return r
	}
	r.PackageID, r.PackageDigest, r.ToolID, r.Transport = selected.PackageID, selected.PackageDigest, selected.Tool.ID, selected.Tool.Transport
	r.Declared = true
	if ctx.Err() != nil {
		r.Code = "cancelled"
		return r
	}
	if selected.Tool.Transport == "https" {
		r.Code = "https_unqualified"
		if options.Health {
			r.Health = "unsupported"
		}
		return r
	}
	lookup := options.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	pathValue, _ := lookup("PATH")
	if len(pathValue) > 64<<10 || strings.ContainsRune(pathValue, '\x00') {
		r.Code = "environment_invalid"
		return r
	}
	path := findExecutable(selected.Tool.Command[0], pathValue, options.WorkingDirectory)
	if path == "" {
		r.Availability, r.Code = "unavailable", "executable_unavailable"
		return r
	}
	// Resolve only explicitly named values, plus PATH for executable lookup.
	// Even an available value says nothing about credential validity or quota.
	env := map[string]string{"PATH": pathValue}
	bytes := len(pathValue)
	for _, name := range selected.Tool.EnvRefs {
		value, present := lookup(name)
		if !present || value == "" {
			r.Availability, r.Code = "unavailable", "environment_missing"
			return r
		}
		bytes += len(value)
		if len(value) > 64<<10 || strings.ContainsRune(value, '\x00') || bytes > 256<<10 {
			r.Code = "environment_invalid"
			return r
		}
		env[name] = value
	}
	r.Availability, r.Code = "available", "availability_only"
	if !options.Health {
		return r
	}
	r.Health = "unhealthy"
	if !filepath.IsAbs(options.WorkingDirectory) {
		r.Code = "working_directory_required"
		return r
	}
	if info, err := os.Stat(options.WorkingDirectory); err != nil || !info.IsDir() {
		r.Code = "working_directory_unavailable"
		return r
	}
	if options.Timeout < 0 || options.Timeout > 15*time.Second {
		r.Code = "timeout_invalid"
		return r
	}
	if options.Timeout == 0 {
		options.Timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()
	return checkStdio(ctx, selected.Tool.Command, path, env, options.WorkingDirectory, r)
}

func validDeclaration(s library.ResolvedTool) bool {
	t := s.Tool
	if !identifier.MatchString(s.PackageID) || !digestPattern.MatchString(s.PackageDigest) || !identifier.MatchString(t.ID) || len(t.EnvRefs) > 64 {
		return false
	}
	seen := map[string]bool{}
	for _, name := range t.EnvRefs {
		if !envName.MatchString(name) || seen[name] || privateEnvironmentKey(name) {
			return false
		}
		seen[name] = true
	}
	switch t.Transport {
	case "stdio":
		if len(t.Command) == 0 || len(t.Command) > 64 || t.URL != "" {
			return false
		}
		for _, arg := range t.Command {
			if arg == "" || len(arg) > 4096 || !utf8.ValidString(arg) {
				return false
			}
			for _, c := range arg {
				if unicode.IsControl(c) {
					return false
				}
			}
		}
		return true
	case "https":
		u, err := url.Parse(t.URL)
		return err == nil && len(t.URL) <= 4096 && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && len(t.Command) == 0
	default:
		return false
	}
}

func privateEnvironmentKey(name string) bool {
	switch name {
	case "HOME", "TMPDIR", "TMP", "TEMP", "XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA":
		return true
	}
	return false
}

func findExecutable(command, pathValue, dir string) string {
	check := func(path string) string {
		if !filepath.IsAbs(path) {
			return ""
		}
		if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0111 != 0 {
			return filepath.Clean(path)
		}
		return ""
	}
	if filepath.IsAbs(command) {
		return check(command)
	}
	if strings.ContainsRune(command, os.PathSeparator) {
		if !filepath.IsAbs(dir) {
			return ""
		}
		return check(filepath.Join(dir, command))
	}
	for _, directory := range filepath.SplitList(pathValue) {
		if !filepath.IsAbs(directory) {
			continue
		}
		if path := check(filepath.Join(directory, command)); path != "" {
			return path
		}
	}
	return ""
}
