// Package providers inspects installed native programs without reading or
// copying their credential stores. Native terminal and semantic support differ.
package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/seanheiney/agent-backplane/internal/domain"
)

type Auth struct {
	State     domain.Evidence `json:"state"`
	Method    domain.Evidence `json:"method"`
	Principal domain.Evidence `json:"principal"`
	Coverage  string          `json:"coverage"`
}
type Record struct {
	Name           string          `json:"name"`
	Executable     string          `json:"executable,omitempty"`
	Installed      bool            `json:"installed"`
	Version        domain.Evidence `json:"version"`
	Auth           Auth            `json:"auth"`
	NativeTerminal bool            `json:"native_terminal"`
	SemanticPrompt bool            `json:"semantic_prompt"`
	Detail         string          `json:"detail"`
}

const authCoverage = "Native status only; credential presence is not model access, subscription eligibility, quota or per-session identity."

var versionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$`)

// Discover reports each provider independently. Native status commands make no
// model turn and never initiate sign-in, refresh, installation or an upgrade.
func Discover(ctx context.Context) ([]Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var records []Record
	for _, name := range []string{"codex", "claude", "grok"} {
		r := Record{Name: name, Version: domain.Unknown(), Auth: Auth{State: domain.Unknown(), Method: domain.Unknown(), Principal: domain.Unknown(), Coverage: authCoverage}, Detail: "Native launch support is unavailable for this provider."}
		path, err := exec.LookPath(name)
		if err != nil {
			r.Detail = "CLI unavailable on PATH; use the provider's native installation instructions."
			records = append(records, r)
			continue
		}
		path, err = filepath.Abs(path)
		if err != nil {
			records = append(records, r)
			continue
		}
		r.Executable, r.Installed = path, true
		if name == "grok" {
			r.Detail = "Installed; version, auth status and native mapping unverified. Grok is outside this initial support matrix."
			records = append(records, r)
			continue
		}
		version, err := inspectVersion(ctx, path, name)
		if err != nil {
			r.Detail = "Installed; native version probe failed or returned an unrecognized format."
			records = append(records, r)
			continue
		}
		r.Version = known(version, name+" --version")
		r.NativeTerminal = true
		r.Detail = "Native argv launch is available; attach fidelity requires the exact terminal-backend proof. Semantic delivery is unavailable."
		r.Auth = inspectAuth(ctx, path, name)
		records = append(records, r)
	}
	return records, ctx.Err()
}

// SupportedLaunch preserves explicit native arguments as argv. It probes only
// version; it neither changes authentication nor treats an account as eligible.
func SupportedLaunch(ctx context.Context, provider string, nativeArgs []string) ([]string, string, error) {
	if provider != "codex" && provider != "claude" {
		return nil, "", errors.New("supported native providers: codex, claude; Grok mapping remains unverified")
	}
	if len(nativeArgs) > 0 {
		for _, command := range []string{"login", "logout", "auth", "update", "upgrade", "install"} {
			if nativeArgs[0] == command {
				return nil, "", errors.New("native login and installation changes require the provider's separate explicit workflow")
			}
		}
	}
	for _, arg := range nativeArgs {
		if strings.ContainsRune(arg, '\x00') {
			return nil, "", errors.New("native argv contains a NUL byte")
		}
	}
	path, err := exec.LookPath(provider)
	if err != nil {
		return nil, "", fmt.Errorf("%s CLI unavailable; install it through native instructions", provider)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, "", err
	}
	version, err := inspectVersion(ctx, path, provider)
	if err != nil {
		return nil, "", err
	}
	return append([]string{path}, nativeArgs...), version, nil
}

func inspectVersion(ctx context.Context, path, provider string) (string, error) {
	out, err := probe(ctx, path, []string{"--version"}, true)
	if err != nil {
		return "", fmt.Errorf("%s native version probe failed", provider)
	}
	v := strings.TrimSpace(string(out))
	if provider == "codex" {
		v = strings.TrimPrefix(v, "codex-cli ")
	} else {
		v = strings.TrimSuffix(v, " (Claude Code)")
	}
	if !versionPattern.MatchString(v) {
		return "", fmt.Errorf("%s native version format is unverified", provider)
	}
	return v, nil
}
func known(value, source string) domain.Evidence {
	return domain.Evidence{Value: value, Source: source, ObservedAt: time.Now().UTC()}
}

func inspectAuth(ctx context.Context, path, provider string) Auth {
	a := Auth{State: domain.Unknown(), Method: domain.Unknown(), Principal: domain.Unknown(), Coverage: authCoverage}
	if provider == "codex" {
		out, err := probe(ctx, path, []string{"login", "status"}, true)
		if err == nil {
			a.State = known("credentials_present", "codex login status exit 0")
			// This command can include a key prefix. Never return raw status text.
			line := strings.TrimSpace(string(out))
			if line == "Logged in using ChatGPT" {
				a.Method = known("chatgpt", "codex login status")
			} else if strings.HasPrefix(line, "Logged in using an API key") {
				a.Method = known("api_key", "codex login status")
			}
		} else if strings.TrimSpace(string(out)) == "Not logged in" {
			a.State = known("unavailable", "codex login status")
		}
		return a
	}
	out, err := probe(ctx, path, []string{"auth", "status", "--json"}, false)
	var s struct {
		LoggedIn   *bool  `json:"loggedIn"`
		AuthMethod string `json:"authMethod"`
	}
	if json.Unmarshal(out, &s) != nil || s.LoggedIn == nil {
		return a
	}
	var exit *exec.ExitError
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1 || *s.LoggedIn) {
		return a
	}
	if *s.LoggedIn {
		a.State = known("credentials_present", "claude auth status --json")
	} else {
		a.State = known("unavailable", "claude auth status --json")
	}
	for _, method := range []string{"none", "claude.ai", "oauth_token", "api_key", "api_key_helper", "third_party"} {
		if s.AuthMethod == method {
			a.Method = known(method, "claude auth status --json")
			break
		}
	}
	return a
}

// Bounded capture discards overflow and stderr unless the documented status
// itself uses stderr (Codex). No raw subprocess output is placed in an error.
type bounded struct {
	bytes.Buffer
	overflow bool
}

func (b *bounded) Write(p []byte) (int, error) {
	n := len(p)
	remain := (64 << 10) - b.Len()
	if len(p) > remain {
		b.overflow = true
		p = p[:max(0, remain)]
	}
	b.Buffer.Write(p)
	return n, nil
}
func probe(ctx context.Context, path string, args []string, captureStderr bool) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdin = nil
	var output bounded
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	if captureStderr {
		cmd.Stderr = &output
	}
	err := cmd.Run()
	if output.overflow {
		return nil, errors.New("native probe output exceeded limit")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return output.Bytes(), err
}
