package providers

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// These contracts describe reviewed argv syntax, not runtime identity, login,
// model acceptance, terminal fidelity or native resume success. Unknown versions
// remain discoverable but cannot inherit a reviewed parser contract implicitly.
type nativeContract struct {
	provider string
	version  string
	flags    map[string]argumentKind
	commands map[string]bool
}

type argumentKind int

const (
	booleanArgument argumentKind = iota
	valueArgument
	optionalArgument
)

var grokVersionPattern = regexp.MustCompile(`^grok (\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)? \([0-9a-f]{12}\) \[(?:stable|alpha)\])$`)

var nativeContracts = map[string]*nativeContract{
	"gemini": newContract("gemini", "0.52.0",
		"-d --debug -s --sandbox --screen-reader",
		"-m --model -i --prompt-interactive --approval-mode --policy --admin-policy --allowed-mcp-server-names -e --extensions --session-file --session-id --include-directories",
		"-r --resume",
		"mcp extensions extension skills skill hooks hook gemma update"),
	"grok": newContract("grok", "1.0.50 (c58f321264ba) [stable]",
		"-c --continue --debug --disable-web-search --fork-session --fullscreen --minimal --no-alt-screen --no-plan --no-subagents",
		"--agent --agents --debug-file --deny --disallowedTools --disallowed-tools -m --model --max-turns --permission-mode --reasoning-effort --effort --rules --append-system-prompt -s --session-id --sandbox --system-prompt-override --system-prompt --tools",
		"-r --resume",
		"agent clone completions cursor-worker dashboard doctor du disk-usage export inspect leader login logout mcp memory models plugin sessions setup trace update usage version v worktree wrap import"),
	"opencode": newContract("opencode", "1.18.21",
		"--print-logs --pure -c --continue --fork --mini --no-replay",
		"--log-level -m --model -s --session --prompt --agent --replay-limit",
		"",
		"completion acp mcp attach run debug providers auth agent upgrade uninstall serve web models stats export import github pr session plugin plug db"),
}

func newContract(provider, version, booleans, values, optional, commands string) *nativeContract {
	c := &nativeContract{provider: provider, version: version, flags: map[string]argumentKind{}, commands: map[string]bool{}}
	for kind, list := range map[argumentKind]string{booleanArgument: booleans, valueArgument: values, optionalArgument: optional} {
		for _, flag := range strings.Fields(list) {
			c.flags[flag] = kind
		}
	}
	// Management words remain reserved even after --. An initial prompt can use
	// the provider's explicit interactive prompt flag or a full Grok sentence.
	for _, command := range strings.Fields(commands + " help login logout auth install update upgrade") {
		c.commands[command] = true
	}
	return c
}

func expandedContract(provider string) *nativeContract { return nativeContracts[provider] }

func qualifiedVersion(provider, version string) bool {
	if c := expandedContract(provider); c != nil {
		return c.version == version
	}
	return provider == "codex" || provider == "claude"
}

// ValidateManagedEnvironment must run at the actual launch boundary using that
// child's inherited environment. Argv preparation in a client cannot prove a
// daemon's later environment. The managed PTY must also supply TTY stdin/stdout.
// No values are changed or included in errors. Gemini 0.52.0's installed
// isHeadlessMode uses exact, case-sensitive "true" comparisons for these keys.
func ValidateManagedEnvironment(provider string, lookup func(string) (string, bool)) error {
	if provider != "codex" && provider != "claude" && expandedContract(provider) == nil {
		return errors.New("unknown managed native provider")
	}
	if provider != "gemini" {
		return nil
	}
	if lookup == nil {
		return errors.New("Gemini managed launch requires an explicit environment lookup")
	}
	isTrue := func(key string) bool {
		value, present := lookup(key)
		return present && value == "true"
	}
	if !isTrue("GEMINI_CLI_INTEGRATION_TEST") && (isTrue("CI") || isTrue("GITHUB_ACTIONS")) {
		return errors.New("Gemini native environment selects headless mode; managed launch requires an interactive environment")
	}
	return nil
}

// RequestedModel returns only the explicit model declaration from a reviewed
// interactive argv contract. It does not inspect native defaults, credentials,
// registries or accepted models, and performs no process or filesystem work.
// Display limits apply here without narrowing the native launch validator.
func RequestedModel(provider, version string, args []string) (string, error) {
	c := expandedContract(provider)
	if c == nil || c.version != version {
		return "", errors.New("provider version is outside the reviewed model declaration contract")
	}
	model, err := c.inspect(args)
	if err != nil {
		return "", err
	}
	if len(model) > 256 || !utf8.ValidString(model) || strings.ContainsFunc(model, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
	}) {
		return "", errors.New("requested native model is outside the display declaration limits")
	}
	return model, nil
}

func (c *nativeContract) validate(args []string) error {
	_, err := c.inspect(args)
	return err
}

func (c *nativeContract) inspect(args []string) (string, error) {
	for _, arg := range args {
		if strings.ContainsRune(arg, '\x00') {
			return "", errors.New("native argv contains a NUL byte")
		}
	}
	positional, modelSeen, literal := 0, false, false
	requestedModel := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" && !literal {
			literal = true
			continue
		}
		if literal || !strings.HasPrefix(arg, "-") {
			if c.commands[arg] {
				return "", errors.New("native management commands require the provider's separate explicit workflow")
			}
			if c.provider == "gemini" {
				return "", errors.New("Gemini positional prompts can select headless mode; use --prompt-interactive for an explicit interactive initial prompt")
			}
			if c.provider == "opencode" {
				return "", errors.New("select the OpenCode project with the managed workspace option, not a native positional path")
			}
			positional++
			if positional > 1 {
				return "", errors.New("this native interactive launch accepts at most one positional argument")
			}
			continue
		}
		flag, value, attached := strings.Cut(arg, "=")
		kind, ok := c.flags[flag]
		if !ok {
			return "", fmt.Errorf("%s option is outside the reviewed interactive argv contract; use separate documented flags", c.provider)
		}
		// No short clusters/attached short values, abbreviations, camelCase
		// aliases, or implicit booleans. Their parser boundaries are unqualified.
		if attached && !strings.HasPrefix(flag, "--") {
			return "", errors.New("use separate short option values or a long option with =")
		}
		if kind == booleanArgument {
			if attached {
				return "", errors.New("native boolean options must be provided without a value")
			}
			continue
		}
		if !attached {
			if i+1 == len(args) || strings.HasPrefix(args[i+1], "-") {
				if kind == optionalArgument {
					continue
				}
				return "", errors.New("native option requires a separate value or a long option with =")
			}
			i++
			value = args[i]
			if kind == optionalArgument && c.commands[value] {
				return "", errors.New("ambiguous resume value matches a native management command; use --resume=value")
			}
		}
		if value == "" {
			return "", errors.New("native option value must not be empty")
		}
		if flag == "--model" || flag == "-m" {
			if modelSeen {
				return "", errors.New("specify the requested native model only once")
			}
			modelSeen = true
			requestedModel = value
			if c.provider == "opencode" && !validOpenCodeModel(value) {
				return "", errors.New("OpenCode model must be an explicit provider/model identifier; OpenRouter uses openrouter/<model>")
			}
		}
		if flag == "--approval-mode" || flag == "--permission-mode" {
			if value != "default" && value != "plan" {
				return "", errors.New("managed native launch supports default or plan approval mode; approval bypass belongs in a separate native workflow")
			}
		}
	}
	return requestedModel, nil
}

// Validate only the routing syntax. Native registries/credentials determine
// whether the requested provider and model exist and may be used. Nested model
// IDs (openrouter/vendor/model:variant) are preserved, never rewritten.
func validOpenCodeModel(value string) bool {
	parts := strings.Split(value, "/")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false
		}
		for _, r := range part {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._:-", r)) {
				return false
			}
		}
	}
	return true
}
