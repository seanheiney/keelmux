#!/usr/bin/env python3
"""Build Keelmux reference pages from canonical guides, with stdlib only.

This deliberately supports the Markdown constructs used by the guides, escapes
raw HTML, and refuses unsafe links. It is not a general-purpose Markdown engine.
"""
import argparse
import hashlib
import html
import json
import posixpath
import re
import shutil
import subprocess
from pathlib import Path
from urllib.parse import quote, urlsplit

ROOT = Path(__file__).resolve().parents[2]
HERE = Path(__file__).resolve().parent
REPO = "https://github.com/seanheiney/keelmux"
TERMINAL_CAPTURE = "docs/site/evidence/runtime-ux-e81ddbd"
PAGES = [
    ("quickstart", "Your first session", "Start here", "docs/user/quickstart.md"),
    ("install", "Installation", "Start here", "docs/user/install.md"),
    ("daily-use", "Daily use", "Work", "docs/user/daily-use.md"),
    ("channels", "Channels & replies", "Work", "docs/user/channels.md"),
    ("managed-irc", "Managed IRC", "Work", "docs/user/managed-irc.md"),
    ("feature-expansion", "Next source features", "Work", "docs/user/feature-expansion.md"),
    ("groups", "Company & product groups", "Work", "docs/user/groups.md"),
    ("usage", "Usage & evidence", "Work", "docs/user/usage.md"),
    ("skills", "Skills & linking", "Work", "docs/user/skills.md"),
    ("operations", "Operations & recovery", "Operate", "docs/user/operations.md"),
    ("configuration", "Configuration", "Operate", "docs/user/configuration.md"),
    ("private-entry", "Private entry", "Operate", "docs/user/private-entry.md"),
    ("ssh-peers", "SSH peer inventory", "Operate", "docs/user/ssh-peers.md"),
    ("diagnostics", "Read-only diagnostics", "Operate", "docs/user/diagnostics.md"),
    ("troubleshooting", "Troubleshooting", "Operate", "docs/user/troubleshooting.md"),
    ("cli", "CLI reference", "Reference", "docs/user/cli.md"),
    ("architecture", "Architecture & trust", "Reference", "docs/user/architecture.md"),
    ("support-matrix", "Support & evidence", "Reference", "docs/user/support-matrix.md"),
    ("roadmap", "Roadmap & full vision", "Reference", "docs/user/roadmap.md"),
    ("security", "Security", "Project", "SECURITY.md"),
    ("support", "Get support", "Project", "SUPPORT.md"),
    ("contributing", "Contributing", "Project", "CONTRIBUTING.md"),
    ("changelog", "Changelog", "Project", "CHANGELOG.md"),
]
SLUGS = {source: slug for slug, _, _, source in PAGES}


def esc(value):
    return html.escape(str(value), quote=True)


def slugify(text):
    text = re.sub(r"[`*_]", "", text).lower()
    return re.sub(r"[^\w\- ]", "", text).strip().replace(" ", "-")


def base_path(value):
    if not re.fullmatch(r"/(?:[a-zA-Z0-9_-]+/)*", value):
        raise ValueError("base path must be an absolute path with a trailing slash")
    return value


def href(target, source, base, commit):
    """Keep known guide links local; pin all other repository files to source."""
    target = html.unescape(target)
    parsed = urlsplit(target)
    if parsed.scheme:
        if parsed.scheme not in ("https", "http", "mailto"):
            raise ValueError("unsafe link scheme")
        if parsed.scheme in ("http", "https") and not parsed.netloc:
            raise ValueError("missing link host")
        return target
    if target.startswith("//") or "\\" in target or any(ord(c) < 32 for c in target):
        raise ValueError("unsafe local link")
    if target.startswith("#"):
        return target
    path = posixpath.normpath(posixpath.join(posixpath.dirname(source), parsed.path))
    if path.startswith("../") or path.startswith("/"):
        raise ValueError("link escapes repository")
    fragment = "#" + parsed.fragment if parsed.fragment else ""
    if path in SLUGS:
        return base + SLUGS[path] + "/" + fragment
    return REPO + "/blob/" + commit + "/" + quote(path, safe="/") + fragment


def inline(text, source, base, commit):
    # Tokenize code before links, so executable-looking text always stays text.
    out = []
    pattern = r"(`+)(.+?)\1|\[([^\]]+)\]\(([^\s)]+)\)|\*\*(.+?)\*\*|(?<!\*)\*([^*]+)\*(?!\*)|\\([\\`*{}\[\]()#+\-.!|])"
    cursor = 0
    for m in re.finditer(pattern, text):
        out.append(esc(text[cursor:m.start()]))
        if m[1]:
            out.append("<code>" + esc(m[2]) + "</code>")
        elif m[3] is not None:
            out.append('<a href="' + esc(href(m[4], source, base, commit)) + '">' + inline(m[3], source, base, commit) + "</a>")
        elif m[5] is not None:
            out.append("<strong>" + inline(m[5], source, base, commit) + "</strong>")
        elif m[6] is not None:
            out.append("<em>" + inline(m[6], source, base, commit) + "</em>")
        else:
            out.append(esc(m[7]))
        cursor = m.end()
    out.append(esc(text[cursor:]))
    return "".join(out)


def table_cells(line):
    # The corpus escapes a literal pipe inside inline CLI code.
    return [x.strip().replace("\\|", "|") for x in re.split(r"(?<!\\)\|", line.strip().strip("|"))]


def markdown(text, source, base, commit):
    lines = text.splitlines()
    out, headings, used = [], [], {}
    i = 0
    while i < len(lines):
        line = lines[i]
        if not line.strip():
            i += 1
            continue
        if line.startswith("```"):
            language = line[3:].strip()
            body = []
            i += 1
            while i < len(lines) and not lines[i].startswith("```"):
                body.append(lines[i]); i += 1
            if i == len(lines):
                raise ValueError("unclosed code fence in " + source)
            label = "Diagram source" if language == "mermaid" else language or "text"
            code = "\n".join(body)
            copy = '' if language == "mermaid" else '<button class="copy" type="button" aria-label="Copy code">Copy</button>'
            out.append('<div class="code-block"><div class="code-top"><span>' + esc(label) + '</span>' + copy + '</div><pre tabindex="0"><code>' + esc(code) + '</code></pre></div>')
            i += 1
            continue
        heading = re.match(r"^(#{1,6})\s+(.+)$", line)
        if heading:
            level, title = len(heading[1]), heading[2]
            ident = slugify(title)
            count = used.get(ident, 0); used[ident] = count + 1
            if count: ident += "-" + str(count)
            if level > 1:
                headings.append((level, title, ident))
                out.append(f'<h{level} id="{esc(ident)}">{inline(title, source, base, commit)}<a class="anchor" href="#{esc(ident)}" aria-label="Link to {esc(title)}">#</a></h{level}>')
            # The document template owns the single H1, from the actual source.
            i += 1
            continue
        if i + 1 < len(lines) and re.match(r"^\|?\s*:?-{3,}", lines[i+1]) and "|" in line:
            header = table_cells(line); rows = []; i += 2
            while i < len(lines) and lines[i].strip().startswith("|"):
                cells = table_cells(lines[i])
                if len(cells) != len(header): raise ValueError("ragged Markdown table: " + source)
                rows.append(cells); i += 1
            out.append('<div class="table-wrap" role="region" aria-label="Reference table" tabindex="0"><table><thead><tr>' + ''.join('<th scope="col">' + inline(x, source, base, commit) + '</th>' for x in header) + '</tr></thead><tbody>' + ''.join('<tr>' + ''.join('<td>' + inline(x, source, base, commit) + '</td>' for x in row) + '</tr>' for row in rows) + '</tbody></table></div>')
            continue
        match = re.match(r"^(?:[-*] |\d+\. )(.+)$", line)
        if match:
            ordered = bool(re.match(r"^\d+\.", line)); tag = 'ol' if ordered else 'ul'; items = []
            while i < len(lines):
                item = re.match(r"^(?:\d+\. |[-*] )(.+)$", lines[i])
                if not item: break
                items.append('<li>' + inline(item[1], source, base, commit) + '</li>'); i += 1
            out.append('<' + tag + '>' + ''.join(items) + '</' + tag + '>')
            continue
        if line.startswith(">"):
            body=[]
            while i<len(lines) and lines[i].startswith(">"):
                body.append(lines[i].lstrip("> "));i+=1
            out.append('<blockquote><p>'+inline(' '.join(body),source,base,commit)+'</p></blockquote>');continue
        para = [line]; i += 1
        while i < len(lines) and lines[i].strip() and not re.match(r"^(#|```|\||[-*] |\d+\. |>)", lines[i]):
            para.append(lines[i]); i += 1
        out.append('<p>' + inline(' '.join(para), source, base, commit) + '</p>')
    return "\n".join(out), headings


def logo():
    return '<svg class="logo" viewBox="0 0 28 28" fill="none" aria-hidden="true"><path d="M5 5v18M6 14L21 5M6 14l15 9M4 23h20" stroke="currentColor" stroke-width="2.3" stroke-linecap="square"/></svg>'


def header(base, home=False):
    return f'''<header class="site-header{' dark' if home else ''}"><a class="wordmark" href="{base}" aria-label="Keelmux home">{logo()}<span>Keelmux</span></a><nav class="top-nav" aria-label="Main"><a href="{base}quickstart/">Documentation</a><a href="{base}roadmap/">Roadmap</a><a href="{REPO}">GitHub <span aria-hidden="true">↗</span></a></nav><div class="header-actions"><button class="search-trigger" type="button" aria-haspopup="dialog">Search <kbd>/</kbd></button><button class="menu-trigger" type="button" aria-expanded="false" aria-controls="guide-nav">Guides</button></div></header>'''


def nav(base, current):
    out = ['<nav class="guide-nav" id="guide-nav" aria-label="Documentation">']; prev = None
    for slug, title, group, _ in PAGES:
        if group != prev: out.append('<p class="nav-group">' + group + '</p>'); prev = group
        selected = ' aria-current="page"' if slug == current else ''
        out.append(f'<a href="{base}{slug}/"{selected}>{esc(title)}</a>')
    out.append('</nav>'); return ''.join(out)


def search_dialog():
    return '''<dialog class="search-dialog" aria-labelledby="search-title"><div class="search-heading"><h2 id="search-title">Find a guide</h2><button type="button" class="search-close" aria-label="Close search">Esc</button></div><label class="sr-only" for="search-input">Search documentation</label><input id="search-input" type="search" autocomplete="off" placeholder="Try detach, delivery, or backup…"><p id="search-status" role="status" aria-live="polite">Type to search this documentation.</p><ol id="search-results"></ol></dialog>'''


def footer(base, commit):
    return f'''<footer class="site-footer"><a class="wordmark" href="{base}">{logo()}Keelmux</a><p>Real terminals. Durable work. Your machine.</p><div><a href="{base}security/">Security</a><a href="{base}support/">Support</a><a href="{REPO}/blob/{commit}/LICENSE">MIT</a><a href="{REPO}/tree/{commit}">Docs source {commit[:7]}</a></div></footer>'''


def shell(title, body, base, commit, home=False):
    return f'''<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta name="color-scheme" content="light"><meta name="description" content="Keelmux: real agent terminals, one switcher, durable work. Installation, guides, architecture and exact support boundaries."><title>{esc(title)} · Keelmux</title><link rel="icon" href="{base}assets/favicon.svg" type="image/svg+xml"><link rel="stylesheet" href="{base}assets/site.css"><script src="{base}assets/site.js" defer data-base="{base}"></script><noscript><style>.search-trigger,.menu-trigger,.copy{{display:none}}@media(max-width:700px){{.guide-nav{{display:block;position:static;max-height:none;padding:0 0 25px}}}}</style></noscript></head><body class="{'home' if home else 'guide'}"><a class="skip-link" href="#main">Skip to content</a>{header(base,home)}{body}{footer(base,commit)}{search_dialog()}<div id="copy-status" class="sr-only" role="status" aria-live="polite"></div></body></html>'''


def terminal(base, filename, label, caption, width=836, height=588):
    return f'<figure class="terminal-figure"><a href="{base}assets/{filename}.png" aria-label="Open full-size {esc(label)}"><img src="{base}assets/{filename}.png" alt="{esc(label)}" width="{width}" height="{height}" loading="lazy"></a><figcaption>{caption} <a href="{base}assets/{filename}.txt">Text equivalent</a></figcaption></figure>'


def home_page(base, commit, command, terminal_source):
    install = '<div class="install-command code-block"><div class="code-top"><span>Install latest published release</span><button type="button" class="copy" aria-label="Copy install command">Copy</button></div><pre tabindex="0"><code>'+esc(command)+'</code></pre></div>'
    body = f'''<main id="main"><section class="hero"><div class="hero-inner"><div class="hero-copy"><p class="eyebrow">The terminal bouncer for agent work</p><h1>Keelmux<span class="accent">.</span></h1><h2>Leave the terminal.<br>Keep your place.</h2><p>Real Codex and Claude Code sessions on your machine. One switcher to return to work.</p><div class="hero-actions"><a class="button primary" href="{base}quickstart/">Get started <span aria-hidden="true">→</span></a><a href="{base}support-matrix/">Read the support matrix</a></div></div><div class="hero-terminal"><div class="terminal-label"><span class="live-dot"></span> Inside the switcher <span>development preview</span></div><a href="{base}assets/switcher.png" aria-label="Open full-size switcher preview"><picture><source media="(max-width:700px)" srcset="{base}assets/switcher-compact.png" width="436" height="358"><img src="{base}assets/switcher.png" width="836" height="588" alt="Keelmux terminal switcher with owned fixture sessions, selected native runtime, unknown model/account evidence and contextual keyboard actions"></picture></a><p class="image-caption">Development UI. Actual owned PTY with fixture inventory. Interpreted cell raster, not a native Terminal screenshot. <a href="{base}terminal-evidence.json">Source {terminal_source[:7]} &amp; capture scope</a>; <a href="{base}assets/switcher.txt">text equivalent</a>.</p></div></div></section>
<section class="install-section section-inner"><div><p class="eyebrow">Bring your terminal</p><h2>A small install.<br>Your existing setup.</h2><p>Per-user installation keeps the provider’s own login and permissions. Keelmux 0.2 owns native supervision. Historical v0.1.0 uses tmux.</p><a class="text-link" href="{base}install/">Review, pin, verify, or uninstall <span aria-hidden="true">→</span></a></div><div>{install}<p class="fine">The installer selects the latest published release. See the support matrix for 0.2 qualification and historical 0.1 evidence.</p></div></section>
<section class="workflow section-inner"><div class="section-heading"><p class="eyebrow">A dependable return</p><h2>Launch. Leave. Pick up.</h2><a href="{base}quickstart/">Walk through your first session →</a></div><ol class="workflow-list"><li><span>01</span><h3>Launch owned work</h3><p><code>keelmux run --workspace /your/project codex</code></p></li><li><span>02</span><h3>Enter the real CLI</h3><p>Select work and press <kbd>Enter</kbd>. Native prompts and permissions stay native.</p></li><li><span>03</span><h3>Detach and return</h3><p><kbd>Ctrl-G</kbd> <kbd>d</kbd> returns to the switcher. <kbd>Enter</kbd> returns to the surviving terminal.</p></li></ol></section>
<section class="reading-section"><div class="section-inner"><div class="section-heading"><p class="eyebrow">The field guide</p><h2>Find your next step.</h2></div><div class="reading-grid"><a href="{base}daily-use/"><span>01 / Work</span><h3>Navigate without losing place <span aria-hidden="true">↗</span></h3><p>Switching, search, groups and local drafts.</p></a><a href="{base}channels/"><span>02 / Connect</span><h3>Address one explicit reply <span aria-hidden="true">↗</span></h3><p>Authored inboxes, immutable routes and truthful receipts.</p></a><a href="{base}operations/"><span>03 / Operate</span><h3>Recover with the original identity <span aria-hidden="true">↗</span></h3><p>Daemon lifecycle, backup, upgrade and uncertainty.</p></a><a href="{base}architecture/"><span>04 / Understand</span><h3>Know what owns the work <span aria-hidden="true">↗</span></h3><p>Stable threads, execution homes and the trust boundary.</p></a></div></div></section>
<section class="scope-section section-inner"><div><p class="eyebrow">Evidence before claims</p><h2>Useful today.<br>A larger direction.</h2><p>Keelmux 0.2 owns native PTYs and processes independently of tmux, with optional legacy compatibility. Authored channels and the optional managed IRC server have their own qualification. Independent-server enrollment, model continuity and captains remain on the roadmap.</p><a class="text-link" href="{base}support-matrix/">What has actually been tested →</a></div><div class="scope-list"><a href="{base}support-matrix/"><span>Historical / v0.1.0</span><strong>Terminal core · tmux adapter</strong><span>Scope & receipts ↗</span></a><a href="{base}channels/"><span>0.2</span><strong>Authored chat & explicit replies</strong><span>Read the guide ↗</span></a><a href="{base}architecture/"><span>v0.2.1 / published</span><strong>Independent native supervisor</strong><span>Ownership & recovery →</span></a><a href="{base}managed-irc/"><span>v0.2.1 / published</span><strong>Owned IRC service</strong><span>Read the canonical guide ↗</span></a><a href="{base}roadmap/"><span>Planned / full direction</span><strong>Continuity, federation & captains</strong><span>Roadmap & plans ↗</span></a></div></section>
<section class="final-cta"><div class="section-inner"><h2>Keep the work.<br>Come back when ready.</h2><a class="button primary" href="{base}quickstart/">Open the quickstart →</a><a href="{REPO}">Explore the source ↗</a></div></section></main>'''
    return shell('Real terminals. Durable work.', body, base, commit, True)


def build(output, base, root=ROOT, commit=None):
    base = base_path(base)
    commit = commit or subprocess.check_output(['git','rev-parse','HEAD'],cwd=root,text=True).strip()
    if not re.fullmatch('[0-9a-f]{40}',commit): raise ValueError('expected full source SHA')
    output=Path(output).resolve()
    if output==root or root in output.parents and not (HERE in output.parents):
        raise ValueError('output must not overwrite repository source')
    output.mkdir(parents=True,exist_ok=True);(output/'assets').mkdir(exist_ok=True)
    for file in ('site.css','site.js','favicon.svg'):
        shutil.copyfile(HERE/'assets'/file,output/'assets'/file)
    evidence_path = root / TERMINAL_CAPTURE / 'receipt.json'
    evidence = json.loads(evidence_path.read_text())
    terminal_source = evidence['source_commit']
    if not re.fullmatch('[0-9a-f]{40}', terminal_source): raise ValueError('expected full terminal capture source SHA')
    images = {'switcher': TERMINAL_CAPTURE + '/native-80x24', 'switcher-wide': TERMINAL_CAPTURE + '/native-120x32', 'switcher-compact': TERMINAL_CAPTURE + '/native-40x14', 'runtime-repair': TERMINAL_CAPTURE + '/uncertain-40x14', 'channel-inbox': TERMINAL_CAPTURE + '/channels/chosen-inbox-80x24', 'channel-reply': TERMINAL_CAPTURE + '/channels/multiline-reply-40x14'}
    image_hashes={}
    for name,path in images.items():
        for suffix in ('.png','.txt'):
            src=root/(path+suffix)
            digest=hashlib.sha256(src.read_bytes()).hexdigest()
            if evidence['files_sha256'][str(src.relative_to(root / TERMINAL_CAPTURE))] != digest:
                raise ValueError('terminal asset differs from frozen capture receipt: ' + path + suffix)
            shutil.copyfile(src,output/'assets'/(name+suffix));image_hashes[name+suffix]=digest
    shutil.copyfile(evidence_path,output/'terminal-evidence.json')
    install=(root/'docs/user/install.md').read_text()
    command=re.search(r'```sh\n(curl[^\n]+\| sh)\n```',install)
    if not command: raise ValueError('canonical one-line install command not found')
    (output/'index.html').write_text(home_page(base,commit,command[1],terminal_source))
    index=[]; source_hashes={}
    for slug,nav_title,group,source in PAGES:
        path=root/source
        if slug=='managed-irc' and not path.exists():
            source='docs/user/roadmap.md'
            text='# Managed IRC\n\n**Planned:** Keelmux-owned IRC lifecycle is not included in the published v0.1.0 installer or the current channel guide. No owned-server commands are claimed here.\n\nThe current [IRC binding guide](channels.md#irc) connects to an explicitly configured existing TLS server. Keep services-account identity, exact channel routing and connection-gap coverage distinct from managing that server.\n\n## Follow the full direction\n\nRead the canonical [roadmap](roadmap.md), [product specification](../product/PRODUCT_SPEC.md) and [build plan](../build/BUILD_PLAN.md). A managed server needs its own pinned runtime, private TLS, ownership/lifecycle and real-service qualification before installation instructions can be published.\n'
        else:
            text=path.read_text()
        title=re.search(r'^# (.+)$',text,re.M)
        if not title: raise ValueError('missing title: '+source)
        title=title[1];content,headings=markdown(text,source,base,commit)
        status = 'Keelmux 0.2 guide' if slug in ('channels','daily-use','architecture','configuration','cli','security') else 'Reference guide'
        if slug=='managed-irc':status='Keelmux 0.2 · managed server' if path.exists() else 'Planned · no released server lifecycle'
        if slug=='private-entry':status='Experiment · explicit local homes'
        scope=f'Development UI, source {terminal_source[:7]}. Actual owned PTY with deterministic fixtures; interpreted cells, not a native Terminal screenshot. No native provider, external post or model turn. <a href="{base}terminal-evidence.json">Exact capture scope</a>.'
        if slug=='channels':content=terminal(base,'channel-inbox','Thread inbox with one explicit destination, intake gap and authored fixture notes',scope)+content
        if slug=='daily-use':content=terminal(base,'switcher','Terminal work switcher with selected native runtime and unknown model/account evidence',scope+f' <a href="{base}assets/switcher-wide.png">120×32</a> · <a href="{base}assets/switcher-compact.png">40×14</a>.')+content
        if slug=='troubleshooting':content=terminal(base,'runtime-repair','40 by 14 terminal fixture retaining the selected work, uncertain runtime, repair guidance and navigation',scope,436,358)+content
        source_link=REPO+'/blob/'+commit+'/'+source
        toc=''.join(f'<a href="#{esc(ident)}">{esc(re.sub(r"[`*]", "",name))}</a>' for level,name,ident in headings if level==2)
        pager=''
        pos=[p[0] for p in PAGES].index(slug)
        if pos+1<len(PAGES):
            nxt=PAGES[pos+1];pager=f'<a class="next-guide" href="{base}{nxt[0]}/"><span>Continue reading</span><strong>{esc(nxt[1])} →</strong></a>'
        body=f'<div class="docs-layout">{nav(base,slug)}<main id="main" class="article"><div class="article-heading"><p class="eyebrow">{esc(group)} / {esc(status)}</p><h1>{esc(title)}</h1><a class="source-link" href="{source_link}">Canonical source ↗</a></div>{content}{pager}</main><aside class="toc" aria-label="On this page"><p>On this page</p>{toc}<div class="source-note">Built from repository guides.<br>Source <a href="{REPO}/tree/{commit}">{commit[:7]}</a><br><a href="{base}support-matrix/">Release scope & evidence ↗</a></div></aside></div>'
        destination=output/slug;destination.mkdir(exist_ok=True);(destination/'index.html').write_text(shell(title,body,base,commit))
        index.append({'title':nav_title,'url':base+slug+'/','group':group,'body':re.sub(r'[`#*|]','',text),'headings':[{'text':name,'url':base+slug+'/#'+ident} for _,name,ident in headings]})
        source_hashes[source]=hashlib.sha256((root/source).read_bytes()).hexdigest()
    (output/'search-index.json').write_text(json.dumps(index,ensure_ascii=False,separators=(',',':')))
    builder_sources = {str(path.relative_to(root)): hashlib.sha256(path.read_bytes()).hexdigest() for path in (HERE/'build.py', HERE/'assets/site.css', HERE/'assets/site.js', HERE/'assets/favicon.svg')}
    (output/'build-manifest.json').write_text(json.dumps({'source_commit':commit,'base_path':base,'canonical_sources':source_hashes,'builder_sources':builder_sources,'image_hashes':image_hashes,'image_sources':{name:terminal_source for name in images},'terminal_receipt_sha256':hashlib.sha256(evidence_path.read_bytes()).hexdigest(),'pages':len(PAGES)+1,'runtime':'static; local search; no analytics/CDN'},indent=2)+'\n')
    (output/'.nojekyll').touch()
    return {'output':str(output),'pages':len(PAGES)+1,'source_commit':commit,'base_path':base}


if __name__=='__main__':
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--output',default=str(HERE/'dist'));p.add_argument('--base-path',default='/keelmux/');args=p.parse_args()
    print(json.dumps(build(args.output,args.base_path)))
