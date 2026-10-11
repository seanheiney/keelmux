# Headroom capability pack

Optional MCP integration for the reusable Keelmux library. The native tool must already be installed; importing this pack runs no installer, server or wrapper.

Upstream contract: [Headroom server.json](https://github.com/headroomlabs-ai/headroom/blob/976aa714ef3e19174dc0cb586cee7b20d064e4ef/server.json), version 0.40.0. Its stdio entry point is `headroom mcp serve`. This package declares that command without a shell. Keelmux does not bundle upstream code or claim measured token savings.

```sh
keelmux plugins import ./plugins/headroom
keelmux plugins on headroom
keelmux plugins off headroom
```

Use `--profile ID` to select a different session profile and `--component tool/mcp` to change only this tool. The pack digest pins these declarations; the installed executable is a separate prerequisite. Verify its native version before explicit application. Applying native configuration and proving a live tool is healthy are separate steps.

No proxy is enabled. An optional proxy requires a separately reviewed exact upstream endpoint and owned lifecycle. Headroom's `wrap` is not invoked and native account stores are not copied. Changed selections require explicit apply plus a supported native reload or new session; they cannot remove context already read by a running model.
