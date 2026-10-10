# Native-key fixture repair for the optional tmux adapter

The additional main source workflow at `85879183` failed Ubuntu's detach assertion. Nativepty and transport passed on both hosted platforms. Independent unchanged Linux tests reproduced four failures in five runs and captured the final Ctrl-G+d as literal pane input. The [tmux 3.5a source](https://raw.githubusercontent.com/tmux/tmux/3.5a/server-client.c) and [manual](https://raw.githubusercontent.com/tmux/tmux/3.5a/tmux.1) describe the timing-based paste classification that bypasses key bindings.

The test now sends each native Ctrl-G through `script` stdin and waits for the actual server's key table and exact pane bytes before sending the next typed key. It checks root state before the final prefix, then requires zero writable clients and unchanged final pane bytes. Both literal acknowledgements share the original three-second byte bound; final prefix acknowledgement and writer release share the original three-second detach bound. The ten-second wrapper, process survival and reconnect checks remain. Production options and runtime sources are unchanged.

The exact work-only repair passed five Linux arm64 runs at default paste setting 1 ms and ten macOS arm64 race runs. Breaking detach and literal-prefix bindings still failed within the original bounds. Sleep-only and final-prefix-only variants were rejected. Full integrated source race/vet passed with cache allowed; an independent diff review found no substantive blocker. All five fresh owned containers are absent.

The [finite receipt](tmux-native-key-fixture-2026-10-10.json) binds source/log hashes and limits. Fresh exact-source hosted, package and public installer gates remain required. The v0.2.0 tag and draft remain unpublished and unchanged.
