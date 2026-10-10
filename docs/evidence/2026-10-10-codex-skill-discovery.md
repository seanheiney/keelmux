# Native Codex skill catalogue and provider discovery

Tested implementation: `e975d17`, with only documentation changes pending during these commands. The actual native executables were Codex CLI 0.159.0 and Claude Code 2.1.296. No executable upgrade, login change, skill invocation or model turn occurred.

```sh
BP_NATIVE_SKILL_DISCOVERY=1 go test ./internal/skills -run '^TestNativeCodexSkillCatalogueReceipt$' -v -count=1
BP_NATIVE_DISCOVERY=1 go test ./internal/adapters/providers -run '^TestNativeDiscoveryReceipt$' -v -count=1
```

Both commands passed. The first installed the pinned Codex project bundle in an owned disposable directory, launched the real native app-server with isolated `HOME` and `CODEX_HOME`, initialized it and issued `skills/list`. Its receipt contained an enabled `backplane` skill with the exact owned project `SKILL.md` path. It created no native thread or turn and copied no credentials. Native instruction loading and invocation remain unknown. Owned temporary state was removed by the test harness.

The second used only native version and read-only authentication-status commands, recording whitelisted fields. Codex reported `credentials_present` with method `chatgpt`; Claude reported `credentials_present` with method `claude.ai`. Both principals remain unknown. Credential presence does not prove model eligibility or authenticated-turn success. Grok was installed, but version, authentication and supported semantic mapping remained unknown; no Grok auth workflow was invoked.

The ordinary source suite skips these native opt-in checks. `make check` also passed at this implementation SHA. Existing race tests did not cover the independently reproduced gateway authority and display-cancellation defects; those require separate hardening and regression evidence.
