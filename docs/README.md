# Keelmux documentation

Keelmux keeps real agent terminals on your machine and gives you one switcher for returning to work. Native CLIs retain their own login, model interaction and permission controls.

Start with [your first session](user/quickstart.md). The current release candidate is a terminal product with an experimental private entry path. Cross-server enrollment, shared chat, account routing and structured Codex prompts have separate unfinished gates.

| I want to… | Read |
| --- | --- |
| Install or build Keelmux | [Installation](user/install.md) |
| Launch, attach, detach and return | [Quickstart](user/quickstart.md) |
| Switch work, organize groups and keep drafts | [Daily use](user/daily-use.md) |
| Install the connection skill without overwriting my setup | [Skills and linking](user/skills.md) |
| Find a command or flag | [CLI reference](user/cli.md) |
| Choose state paths and inspect local files | [Configuration](user/configuration.md) |
| Operate an explicitly configured local entry | [Private entry](user/private-entry.md) |
| Recover, back up, upgrade or remove the application | [Operations](user/operations.md) |
| Diagnose a refusal or missing terminal | [Troubleshooting](user/troubleshooting.md) |
| Check what has actually been tested | [Support matrix](user/support-matrix.md) |
| Understand identity, input ownership and trust | [Architecture](user/architecture.md) |
| Understand what comes next | [Roadmap](user/roadmap.md) |
| Review the selected name and collision checks | [Naming](user/naming.md) |

[Security](../SECURITY.md), [support](../SUPPORT.md), [contributing](../CONTRIBUTING.md) and the [changelog](../CHANGELOG.md) cover project operations. The [product specification](product/PRODUCT_SPEC.md) and [build plan](build/BUILD_PLAN.md) preserve the full direction; proposed commands there are not a command reference.

## Release status

The canonical commands are `keelmux`, `keelmuxd` and `keelmux-entry`; compatibility aliases remain available. [Release assets and qualification receipts](https://github.com/seanheiney/keelmux/releases) identify the tagged source, installed bytes and actual tested combinations.

Every release must link its own exact source and runtime receipts in the [support matrix](user/support-matrix.md). Evidence from an earlier source remains historical evidence.
