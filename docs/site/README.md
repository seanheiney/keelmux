# Keelmux reference site

Static reference material for GitHub Pages. The product stays terminal-first; this site has no product API, login, message composer, account settings or runtime controls.

Build with Python 3 (standard library only):

```sh
python3 docs/site/build.py --output docs/site/dist --base-path /keelmux/
python3 -m unittest discover -s docs/site/tests -v
```

Serve the generated output at the configured prefix. For a custom-domain root use `--base-path /`. All navigation, image, script, stylesheet and search paths follow this prefix. The captain owns `.github` deployment wiring. Upload only the generated output, not the source tree or private `work/` files. `.nojekyll` is generated.

`docs/user/*.md`, `SECURITY.md`, `SUPPORT.md`, `CONTRIBUTING.md` and `CHANGELOG.md` are the canonical reading content. The source hash manifest makes the build auditable. Known guides are linked locally; research, evidence, full product specification and build plan link to the exact repository source. No copied guide prose needs separate maintenance. The small Markdown renderer supports the current corpus (headings, links, emphasis, lists, blockquotes, tables and fenced code); raw HTML stays escaped. Unsupported future constructs should get a rendering acceptance case before use. Mermaid is presented as a copy of the canonical diagram source; no remote rendering script is loaded.

Search downloads one local JSON index only when opened; it uses literal text and creates results with text nodes. No analytics, remote fonts, cookies, external JavaScript, service worker or backend is needed. A search failure leaves the static navigation usable. Copy failure offers manual selection. Search supports `/`, Escape and ArrowDown into results. Mobile guide navigation supports Escape. Native dialog focus management and visible focus rings preserve keyboard access. Motion respects reduced-motion preferences.

Terminal images are unmodified captured PTY-cell rasters with deterministic inventory/notes. They are examples of the development TUI, not screenshots of a customer or real external chat, and not native Terminal screenshots. Captions and text equivalents preserve that distinction. `evidence/terminal-image-check.json` records byte-exact ANSI replay of the narrow reply image and verifies that every nonempty row is present in its PNG.

The current site images come from the new frozen-source receipt at `evidence/runtime-ux-e81ddbd/receipt.json`: 15 selected-runtime captures and 24 authored-channel captures across 80×24, 120×32 and 40×14. Native, legacy, missing, uncertain and unknown runtime examples use explicit fixture fields. The mobile hero uses the actual 40×14 raster. This receipt does not replace historical evidence or prove a real provider, external post, model turn or packaged release.

Refresh examples from a clean committed build with the bundled Python runtime containing Pillow:

```sh
make build
python3 docs/site/capture_runtime.py --binary bin/keelmux \
  --output docs/site/evidence/runtime-ux-NEW_SOURCE
```

The capture command refuses existing output and verifies source inputs before and after the run. Review the images, then point `TERMINAL_CAPTURE` in `build.py` at the new receipt. The builder checks image hashes against it, publishes a copy as `terminal-evidence.json`, and records both canonical guide hashes and builder/asset hashes in the build manifest. Captured TUI source and documentation-build source can differ; each has its own exact identity.

Published v0.2.1 runtime/channels/managed IRC and newer source-only groups/usage/SSH inventory/diagnostics stay explicitly distinct. The managed IRC page reads the canonical `docs/user/managed-irc.md` guide. Native and managed IRC development status follows the integrated source and its declared qualification gates. The site links the repository's MIT license.
