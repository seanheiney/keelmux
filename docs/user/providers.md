# Additional providers

Keelmux's development source adds reviewed interactive launch contracts for Gemini CLI, Grok and OpenCode. Install and authenticate each CLI using its native workflow. Keelmux retains its native login, terminal and permission controls.

**Development source:** published v0.2.1 supports the qualified Codex/Claude terminal workflows. These additional adapters have local help/version and argv conformance evidence. Their authenticated model turns, native attachment fidelity and resume/reboot lifecycle remain separate unfinished gates.

| Harness | Reviewed installed version | Launch |
| --- | --- | --- |
| Gemini CLI | 0.52.0 | `keelmux run --workspace /path/to/project gemini` |
| Grok | 1.0.50 (c58f321264ba) [stable] | `keelmux run --workspace /path/to/project grok` |
| OpenCode | 1.18.21 | `keelmux run --workspace /path/to/project opencode` |

`keelmux providers` reports each independently. A recognized executable/version is evidence of installation and the reviewed launch contract. It does not prove account eligibility, subscription, quota or an accepted model. Unknown versions remain launch-unavailable until qualified. New provider account status remains unknown.

## OpenRouter through OpenCode

Connect OpenRouter through OpenCode's native provider setup, then select an explicit native model identifier:

```sh
keelmux run --workspace /path/to/project opencode --model openrouter/anthropic/claude-sonnet-4.5
```

This illustrates the reviewed `provider/model` syntax. Choose an available model using OpenCode's current native registry; this example is not a model availability or account-access check. Credentials stay in the native provider's local account store. Keelmux neither copies them into a profile nor makes a direct OpenRouter request. The requested identifier is distinct from a provider-accepted model and actual usage.

[OpenCode provider documentation](https://opencode.ai/docs/providers/) describes native OpenRouter configuration. [OpenRouter documentation](https://openrouter.ai/developers) describes its API; a direct API session adapter is outside this initial native integration.

## Managed interactive arguments

Use Keelmux's `--workspace` before the provider to select the execution directory. The new adapters refuse alternate native workspace selectors, management commands, headless/background modes, unknown flags and permission-bypass arguments. This keeps the managed session's declared workspace and interactive contract concrete.

Gemini's positional query workflow can become headless. Use an explicit native `--prompt-interactive` argument if you intend an initial interactive prompt. Gemini also treats `CI=true` or `GITHUB_ACTIONS=true` as headless unless its documented native integration-test override is active. Keelmux checks that condition at both the client and execution home and refuses before a launch effect; it does not rewrite those environment variables.

Native instructions and shared capabilities are optional. Follow [reusable capabilities](plugins.md) to preview/apply the same skill/rule/tool pack, then choose `run --profile ID`. Applying native files does not prove that a process loaded them, that a tool is healthy or that a skill was invoked.

[Native contract evidence](../evidence/native-provider-contracts-2026-10-10.md) records the exact help/version probes and limitations. [Support matrix](support-matrix.md) remains the release evidence reference.
