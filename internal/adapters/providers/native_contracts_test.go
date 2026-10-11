package providers

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var expandedVersions = map[string]string{
	"gemini":   "0.52.0",
	"grok":     "grok 1.0.50 (c58f321264ba) [stable]",
	"opencode": "1.18.21",
}

func versionOnlyFixture(t *testing.T, dir, name, version string) string {
	t.Helper()
	t.Setenv("CI", "")
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("GEMINI_CLI_INTEGRATION_TEST", "")
	return executable(t, dir, name, `printf '%s\n' "$*" >> "$PROVIDER_TEST_LOG"
if [ "$#" = 1 ] && [ "$1" = --version ]; then
  printf '%s\n' '`+version+`'
else
  printf '%s\n' 'PRIVATE_NATIVE_CONTENT_MUST_NOT_ESCAPE'
  exit 9
fi
`)
}

func TestExpandedDiscoveryOnlyProbesVersionAndKeepsAuthUnknown(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	t.Setenv("PATH", dir)
	t.Setenv("PROVIDER_TEST_LOG", log)
	for provider, version := range expandedVersions {
		versionOnlyFixture(t, dir, provider, version)
	}
	records, err := Discover(context.Background())
	if err != nil || len(records) != 5 {
		t.Fatalf("discover: %v, %d records", err, len(records))
	}
	for _, record := range records[2:] {
		want := strings.TrimPrefix(expandedVersions[record.Name], "grok ")
		if !record.Installed || !record.NativeTerminal || record.Version.Value != want || record.Version.Source != record.Name+" --version" || record.Version.ObservedAt.IsZero() {
			t.Fatalf("missing qualified version evidence: %+v", record)
		}
		if record.Auth.State.Value != "unknown" || record.Auth.Method.Value != "unknown" || record.Auth.Principal.Value != "unknown" || record.SemanticPrompt {
			t.Fatalf("inferred auth or semantic support: %+v", record)
		}
	}
	b, err := os.ReadFile(log)
	if err != nil || string(b) != "--version\n--version\n--version\n" {
		t.Fatalf("unexpected native command: %q, %v", b, err)
	}
}

func TestExpandedLaunchPreservesArgvWithoutLaunchingIt(t *testing.T) {
	cases := []struct {
		provider string
		args     []string
	}{
		{"gemini", nil},
		{"gemini", []string{"--model", "example-model", "--approval-mode", "default", "-i", "explain $value `literal` 'quoted'\nUnicode é"}},
		{"gemini", []string{"-i", "please explain mcp", "--policy", "policy with spaces.toml", "--policy=other.toml", "--include-directories", "./login", "--allowed-mcp-server-names=a,b"}},
		{"gemini", []string{"--resume", "latest", "--screen-reader"}},
		{"gemini", []string{"--resume", "--sandbox"}},
		{"gemini", []string{"--resume=login"}},
		{"grok", nil},
		{"grok", []string{"--model=example-model", "--rules", "literal $value; `not a shell`", "--permission-mode", "plan", "please explain login"}},
		{"grok", []string{"--continue", "--fork-session", "--session-id", "native-session-id", "--fullscreen"}},
		{"grok", []string{"--resume", "session title with spaces", "--no-alt-screen"}},
		{"grok", []string{"--resume", "--no-subagents"}},
		{"grok", []string{"--agent", "./native agent.md", "--append-system-prompt=rule", "--deny", "Shell(rm *)"}},
		{"opencode", nil},
		{"opencode", []string{"--model", "openrouter/vendor/model:free", "--prompt", "explain $literal `quotes`"}},
		{"opencode", []string{"-m", "openrouter/auto", "--agent", "build", "--pure"}},
		{"opencode", []string{"--model=other-provider/model.v2", "--session", "native-session-id", "--fork"}},
		{"opencode", []string{"--mini", "--no-replay", "--replay-limit", "20", "--prompt", "please explain serve"}},
	}
	for _, tc := range cases {
		t.Run(tc.provider+"/"+strings.Join(tc.args, " "), func(t *testing.T) {
			dir := t.TempDir()
			log := filepath.Join(dir, "calls")
			t.Setenv("PATH", dir)
			t.Setenv("PROVIDER_TEST_LOG", log)
			path := versionOnlyFixture(t, dir, tc.provider, expandedVersions[tc.provider])
			original := append([]string(nil), tc.args...)
			argv, version, err := SupportedLaunch(context.Background(), tc.provider, tc.args)
			if err != nil || version != strings.TrimPrefix(expandedVersions[tc.provider], "grok ") || !reflect.DeepEqual(argv, append([]string{path}, tc.args...)) {
				t.Fatalf("launch contract: argv=%q version=%q err=%v", argv, version, err)
			}
			if !reflect.DeepEqual(original, tc.args) {
				t.Fatal("input argv mutated")
			}
			b, err := os.ReadFile(log)
			if err != nil || string(b) != "--version\n" {
				t.Fatalf("launch arguments executed or auth probed: %q, %v", b, err)
			}
		})
	}
}

func TestExpandedLaunchRejectsUnsafeOrAmbiguousArgvBeforeProbe(t *testing.T) {
	cases := map[string][][]string{
		"gemini": {
			{"ordinary positional query"}, {"--", "query"}, {"--model", "example", "query"},
			{"mcp", "add"}, {"--model", "example", "extensions", "install"}, {"skill", "install"}, {"hooks"}, {"gemma"}, {"--", "mcp"},
			{"--prompt", "do work"}, {"-pwork"}, {"-dp", "work"}, {"--acp"}, {"--experimental-acp=true"}, {"--output-format=json"},
			{"--list-sessions"}, {"--delete-session", "1"}, {"--list-extensions"}, {"--worktree", "branch"},
			{"--yolo"}, {"-y"}, {"--approval-mode=auto_edit"}, {"--approval-mode", "yolo"}, {"--skip-trust"}, {"--allowed-tools", "Shell(*)"},
			{"--resume", "mcp"}, {"--policy", "one.toml", "mcp"}, {"--model", "--prompt", "work"}, {"--model"}, {"--model="},
			{"--model", "a", "-m", "b"}, {"--screen-reader=false"}, {"--unknown-future-option", "mcp"}, {"--model", "bad\x00value"},
		},
		"grok": {
			{"--cwd", "/other/workspace"}, {"--cwd=/other/workspace"},
			{"login"}, {"--model", "example", "agent", "stdio"}, {"--", "leader"}, {"models"}, {"sessions", "delete"}, {"trace"}, {"setup"},
			{"cursor-worker"}, {"wrap", "sh"}, {"disk-usage"}, {"v"}, {"import"}, {"mcp"}, {"plugin"}, {"worktree"},
			{"-p", "work"}, {"-cp", "work"}, {"--single=work"}, {"--prompt-file", "input.txt"}, {"--prompt-json", "{}"}, {"--json-schema", "{}"},
			{"--output-format", "json"}, {"--always-approve"}, {"--yolo"}, {"--dangerously-skip-permissions"}, {"--allow", "*"},
			{"--permission-mode", "bypassPermissions"}, {"--permission-mode=acceptEdits"}, {"--permission-mode=auto"}, {"--permission-mode=dontAsk"},
			{"--oauth"}, {"--leader-socket", "elsewhere"}, {"--restore-code"}, {"--worktree"}, {"--resume", "login"},
			{"--model", "a", "--model=b"}, {"--cwd"}, {"--unknown", "value", "login"}, {"one prompt", "login"},
		},
		"opencode": {
			{"/other/workspace"}, {"./auth"}, {"--", "/other/workspace"}, {"--model=openrouter/a", "/other/workspace"},
			{"providers", "login"}, {"auth", "login"}, {"run", "work"}, {"serve"}, {"web"}, {"attach", "http://example.invalid"},
			{"--model", "openrouter/vendor/model", "mcp"}, {"--", "plugin"}, {"plug", "module"}, {"db"}, {"pr", "1"}, {"session", "delete"},
			{"--auto"}, {"--hostname", "0.0.0.0"}, {"--mdns"}, {"--port", "4096"}, {"--cors", "*"},
			{"--model", "openrouter/"}, {"--model", "model-only"}, {"--model", "openrouter//model"}, {"--model", "openrouter/../model"},
			{"--model", "openrouter/a b"}, {"--model", "openrouter/a\nsecret"}, {"--model", "openrouter/$(command)"},
			{"--model=openrouter/a", "-m", "openrouter/b"}, {"-mopenrouter/a"}, {"--model"}, {"--model="},
			{"--unknown", "value", "providers"}, {"one-project", "two-projects"}, {"--help"}, {"--version"},
		},
	}
	for provider, cases := range cases {
		for _, args := range cases {
			t.Run(provider+"/"+strings.Join(args, " "), func(t *testing.T) {
				dir := t.TempDir()
				log := filepath.Join(dir, "calls")
				t.Setenv("PATH", dir)
				t.Setenv("PROVIDER_TEST_LOG", log)
				versionOnlyFixture(t, dir, provider, expandedVersions[provider])
				if argv, _, err := SupportedLaunch(context.Background(), provider, args); err == nil || argv != nil {
					t.Fatalf("unsafe argv accepted: %q", args)
				}
				if _, err := os.Stat(log); !os.IsNotExist(err) {
					t.Fatalf("rejected argv caused a native probe: %v", err)
				}
			})
		}
	}
}

func TestExpandedVersionDriftStaysVisibleButUnavailable(t *testing.T) {
	for provider, versions := range map[string][]string{
		"gemini":   {"0.53.0", "0.52.0-preview.1"},
		"grok":     {"grok 1.0.51 (c58f321264ba) [stable]", "grok 1.0.50 (aaaaaaaaaaaa) [stable]", "grok 1.0.50 (c58f321264ba) [alpha]"},
		"opencode": {"1.18.22", "2.0.0"},
	} {
		for _, version := range versions {
			t.Run(provider+"/"+version, func(t *testing.T) {
				dir := t.TempDir()
				t.Setenv("PATH", dir)
				t.Setenv("PROVIDER_TEST_LOG", filepath.Join(dir, "calls"))
				versionOnlyFixture(t, dir, provider, version)
				records, err := Discover(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				for _, record := range records {
					if record.Name == provider && (!record.Installed || record.Version.Value != strings.TrimPrefix(version, "grok ") || record.NativeTerminal || record.Auth.State.Value != "unknown") {
						t.Fatalf("version drift inherited capability: %+v", record)
					}
				}
				if argv, _, err := SupportedLaunch(context.Background(), provider, nil); err == nil || argv != nil {
					t.Fatal("unreviewed version launched")
				}
			})
		}
	}
}

func TestExpandedMalformedVersionNeverLeaksNativeOutput(t *testing.T) {
	for _, provider := range []string{"gemini", "grok", "opencode"} {
		t.Run(provider, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("PATH", dir)
			t.Setenv("PROVIDER_TEST_LOG", filepath.Join(dir, "calls"))
			versionOnlyFixture(t, dir, provider, expandedVersions[provider]+"\nPRIVATE_NATIVE_CONTENT")
			records, err := Discover(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(records)
			if strings.Contains(string(b), "PRIVATE_NATIVE_CONTENT") {
				t.Fatal("raw native output leaked")
			}
			for _, record := range records {
				if record.Name == provider && (record.NativeTerminal || record.Version.Value != "unknown") {
					t.Fatal("malformed version trusted")
				}
			}
			_, _, err = SupportedLaunch(context.Background(), provider, nil)
			if err == nil || strings.Contains(err.Error(), "PRIVATE_NATIVE_CONTENT") {
				t.Fatal("invalid launch version leaked or trusted")
			}
		})
	}
}

func TestExpandedLaunchProbeHonorsCancellation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	t.Setenv("CI", "")
	t.Setenv("GITHUB_ACTIONS", "")
	executable(t, dir, "gemini", `exec /bin/sleep 10`)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	start := time.Now()
	if argv, _, err := SupportedLaunch(ctx, "gemini", nil); err == nil || argv != nil {
		t.Fatal("cancelled probe authorized launch")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("version probe ignored cancellation")
	}
}

func TestManagedGeminiEnvironmentMatchesInstalledHeadlessTruth(t *testing.T) {
	for _, tc := range []struct {
		name    string
		env     map[string]string
		allowed bool
	}{
		{"unset", nil, true},
		{"empty", map[string]string{"CI": "", "GITHUB_ACTIONS": ""}, true},
		{"CI true", map[string]string{"CI": "true"}, false},
		{"actions true", map[string]string{"GITHUB_ACTIONS": "true"}, false},
		{"both true", map[string]string{"CI": "true", "GITHUB_ACTIONS": "true"}, false},
		{"false", map[string]string{"CI": "false", "GITHUB_ACTIONS": "false"}, true},
		{"not truthy", map[string]string{"CI": "1", "GITHUB_ACTIONS": "TRUE"}, true},
		{"no trimming", map[string]string{"CI": "true ", "GITHUB_ACTIONS": " true"}, true},
		{"native test exception", map[string]string{"CI": "true", "GITHUB_ACTIONS": "true", "GEMINI_CLI_INTEGRATION_TEST": "true"}, true},
		{"test exception exact", map[string]string{"CI": "true", "GEMINI_CLI_INTEGRATION_TEST": "TRUE"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, _ := json.Marshal(tc.env)
			err := ValidateManagedEnvironment("gemini", func(key string) (string, bool) { v, ok := tc.env[key]; return v, ok })
			if (err == nil) != tc.allowed {
				t.Fatalf("environment result: %v", err)
			}
			after, _ := json.Marshal(tc.env)
			if string(before) != string(after) {
				t.Fatal("environment mutated")
			}
		})
	}
	if err := ValidateManagedEnvironment("gemini", nil); err == nil {
		t.Fatal("missing lookup accepted")
	}
	if err := ValidateManagedEnvironment("gemini", func(string) (string, bool) { return "true", false }); err != nil {
		t.Fatal("absent environment value treated as present")
	}
	if err := ValidateManagedEnvironment("unknown", nil); err == nil {
		t.Fatal("unknown provider accepted")
	}
	for _, provider := range []string{"codex", "claude", "grok", "opencode"} {
		if err := ValidateManagedEnvironment(provider, func(string) (string, bool) { t.Fatal("unqualified environment inference"); return "", false }); err != nil {
			t.Fatal(err)
		}
	}
}

func TestManagedGeminiHeadlessEnvironmentRejectsBeforeProbe(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	t.Setenv("PATH", dir)
	t.Setenv("PROVIDER_TEST_LOG", log)
	versionOnlyFixture(t, dir, "gemini", expandedVersions["gemini"])
	t.Setenv("CI", "true")
	argv, _, err := SupportedLaunch(context.Background(), "gemini", nil)
	if err == nil || argv != nil {
		t.Fatal("headless environment accepted")
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("rejected environment ran native probe")
	}
}

func TestExpandedNativeVersionReceipt(t *testing.T) {
	if os.Getenv("BP_NATIVE_PROVIDER_VERSION_PROBE") != "1" {
		t.Skip("explicit version-only native provider receipt")
	}
	for _, provider := range []string{"gemini", "grok", "opencode"} {
		// SupportedLaunch returns argv; it never executes that interactive argv.
		argv, version, err := SupportedLaunch(context.Background(), provider, nil)
		if err != nil || len(argv) != 1 || version != strings.TrimPrefix(expandedVersions[provider], "grok ") {
			t.Fatalf("%s installed version contract failed: %v", provider, err)
		}
		t.Logf("%s version=%s interactive-argv-prepared=true interactive-started=false auth=unknown model=unknown", provider, version)
	}
}
