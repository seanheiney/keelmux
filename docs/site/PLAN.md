# Documentation site direction

Visual thesis: an ink-and-paper technical publication, with one sea-green accent and an actual terminal as the visual anchor.

Content plan: a full-width brand/terminal hero leads to installation; a sequential launch → detach → return workflow leads to canonical quickstart; a compact reading index exposes daily operation and development channels; a feature-to-evidence section links the support matrix, full product specification and build plan; the final action returns to installation and GitHub.

Interaction thesis: a brief staggered hero entrance; sticky reading navigation and section position; a fast search dialog/menu transition with reduced-motion alternatives. No continuous decorative animation.

This is static reference material, never a product web dashboard. Markdown under docs/user plus SECURITY.md and SUPPORT.md is canonical. The builder reads those files on every run, rewrites local guide links, and directs research/evidence/spec/build links to exact-source GitHub. No CDN, analytics, remote fonts, external runtime scripts or build packages. Python standard library is the build dependency.

Deployment contract: `python3 docs/site/build.py --output /absolute/output --base-path /keelmux/`. Serve the output root at that prefix. The root captain owns GitHub Pages deployment. Do not publish this branch independently.
