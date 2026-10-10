"""Acceptance controls for canonical static reference output (stdlib only)."""
import hashlib
import importlib.util
import json
import re
import tempfile
import unittest
from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import unquote, urlsplit

HERE=Path(__file__).resolve().parents[1]
spec=importlib.util.spec_from_file_location('site_build',HERE/'build.py')
site=importlib.util.module_from_spec(spec);spec.loader.exec_module(site)


class Document(HTMLParser):
    def __init__(self,text):
        super().__init__(convert_charrefs=True);self.ids=[];self.links=[];self.assets=[];self.h1=0;self.scripts=[];self.feed(text)
    def handle_starttag(self,tag,attributes):
        attrs=dict(attributes)
        if 'id' in attrs:self.ids.append(attrs['id'])
        if tag=='h1':self.h1+=1
        if tag=='a':self.links.append(attrs.get('href',''))
        if tag in ('img','script'):self.assets.append(attrs.get('src',''))
        if tag=='source':self.assets.append(attrs.get('srcset',''))
        if tag=='img' and not attrs.get('alt'):raise AssertionError('image without text equivalent')
        if tag=='script':self.scripts.append(attrs.get('src'))
        if tag=='link':self.assets.append(attrs.get('href',''))
        if any(key.startswith('on') for key in attrs):raise AssertionError('inline event handler')


class StaticReferenceAcceptance(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp=tempfile.TemporaryDirectory();cls.out=Path(cls.tmp.name)
        cls.receipt=site.build(cls.out,'/keelmux/')
        cls.docs={p:Document(p.read_text()) for p in cls.out.rglob('*.html')}
    @classmethod
    def tearDownClass(cls):cls.tmp.cleanup()

    def test_every_local_link_asset_and_fragment_exists_on_subpath(self):
        for path,doc in self.docs.items():
            self.assertEqual(doc.h1,1,str(path));self.assertEqual(len(doc.ids),len(set(doc.ids)),str(path))
            for target in doc.links+doc.assets:
                parsed=urlsplit(target)
                if parsed.scheme:
                    self.assertIn(parsed.scheme,('http','https','mailto'));continue
                if not parsed.path:destination=path
                else:
                    self.assertTrue(parsed.path.startswith('/keelmux/'),target)
                    destination=self.out/unquote(parsed.path[len('/keelmux/'):])
                    if destination.is_dir():destination/='index.html'
                self.assertTrue(destination.is_file(),(path,target))
                if parsed.fragment and destination.suffix=='.html':self.assertIn(parsed.fragment,self.docs[destination].ids,(path,target))

    def test_guide_hashes_and_search_body_are_exact_current_sources(self):
        manifest=json.loads((self.out/'build-manifest.json').read_text())
        for name,digest in manifest['canonical_sources'].items():self.assertEqual(digest,hashlib.sha256((site.ROOT/name).read_bytes()).hexdigest())
        for name,digest in manifest['builder_sources'].items():self.assertEqual(digest,hashlib.sha256((site.ROOT/name).read_bytes()).hexdigest())
        search=json.loads((self.out/'search-index.json').read_text())
        self.assertEqual(len(search),len(site.PAGES))
        for slug,title,_,source in site.PAGES:
            if not (site.ROOT/source).exists():continue
            expected=re.sub(r'[`#*|]','',(site.ROOT/source).read_text())
            actual=next(p for p in search if p['url']=='/keelmux/'+slug+'/')
            self.assertEqual(actual['body'],expected)

    def test_canonical_install_command_and_qualification_are_visible(self):
        text=(self.out/'index.html').read_text()
        self.assertIn('curl -fsSL https://raw.githubusercontent.com/seanheiney/keelmux/main/install.sh | sh',text)
        self.assertIn('The installer selects the latest published release',text)
        self.assertIn('Historical v0.1.0 uses tmux',text)
        self.assertIn('0.2 qualification and historical 0.1 evidence',text)
        self.assertIn('Development UI',text);self.assertIn('Interpreted cell raster',text)
        self.assertIn('Planned',text)
        self.assertIn('Terminal core · tmux adapter',text)
        self.assertIn('optional legacy compatibility',text)
        self.assertIn('/keelmux/managed-irc/',text)
        self.assertIn('/LICENSE',text)
        channel=(self.out/'channels/index.html').read_text()
        self.assertIn('does not include channel bindings',channel)
        managed=(self.out/'managed-irc/index.html').read_text()
        if not (site.ROOT/'docs/user/managed-irc.md').exists():self.assertIn('No owned-server commands are claimed',managed)
        else:
            for command in ('keelmux ircd init','keelmux ircd start','keelmux ircd bind','keelmux ircd restart'):
                self.assertIn(command,managed)
        daily=(self.out/'daily-use/index.html').read_text()
        for phrase in ('Saved attach','currently selected row','Runtime','unknown'):
            self.assertIn(phrase,daily)

    def test_full_vision_build_plan_and_evidence_remain_reachable(self):
        text=(self.out/'roadmap/index.html').read_text()
        self.assertIn('docs/product/PRODUCT_SPEC.md',text)
        # The managed-server reference also exposes the build plan, unabridged.
        all_links=' '.join(link for d in self.docs.values() for link in d.links)
        self.assertIn('docs/build/BUILD_PLAN.md',all_links)
        self.assertIn('docs/evidence/',all_links)
        for link in all_links.split():
            prefix=site.REPO+'/blob/'+self.receipt['source_commit']+'/'
            if link.startswith(prefix):self.assertTrue((site.ROOT/unquote(urlsplit(link[len(prefix):]).path)).is_file(),link)

    def test_no_remote_runtime_assets_or_inline_javascript(self):
        for path,doc in self.docs.items():
            self.assertEqual(doc.scripts,['/keelmux/assets/site.js'])
            self.assertTrue(all(a.startswith('/keelmux/') for a in doc.assets))
            self.assertNotIn('<script>',path.read_text())
        js=(HERE/'assets/site.js').read_text()
        self.assertNotIn('innerHTML',js);self.assertNotIn('eval(',js)
        self.assertIn('textContent',js)

    def test_actual_terminal_assets_are_unmodified_and_have_text_equivalents(self):
        manifest=json.loads((self.out/'build-manifest.json').read_text())
        for name,digest in manifest['image_hashes'].items():self.assertEqual(digest,hashlib.sha256((self.out/'assets'/name).read_bytes()).hexdigest())
        receipt=json.loads((self.out/'terminal-evidence.json').read_text())
        self.assertEqual(manifest['terminal_receipt_sha256'],hashlib.sha256((self.out/'terminal-evidence.json').read_bytes()).hexdigest())
        self.assertEqual(set(manifest['image_sources'].values()),{receipt['source_commit']})
        self.assertEqual(receipt['native_provider_launches'],0)
        self.assertEqual(receipt['external_service_posts'],0)
        self.assertEqual(receipt['model_turns'],0)
        for suffix in ('.png','.txt'):
            self.assertEqual(manifest['image_hashes']['switcher'+suffix],receipt['files_sha256']['native-80x24'+suffix])
        compact=(self.out/'assets/switcher-compact.txt').read_text()
        for text in ('Runtime: native / running','Enter attach','› api / tests'):
            self.assertIn(text,compact)
        repair=(self.out/'assets/runtime-repair.txt').read_text()
        for text in ('Runtime: native / uncertain','Outcome unknown','› api / tests','↑/↓ select'):
            self.assertIn(text,repair)
        self.assertIn('Provider', (self.out/'assets/switcher.txt').read_text().title())
        text=(self.out/'assets/channel-reply.txt').read_text()
        for field in ('mattermost / channel_owned','Root: root_owned','Bot: bot_owned'):self.assertIn(field,text)

    def test_cli_pipe_table_cell_and_multiline_code_are_preserved(self):
        text=(self.out/'cli/index.html').read_text()
        self.assertIn('codex|claude',text)
        fragment,_=site.markdown('''# Test\n\n```sh\nprintf '<tag>&x'\nsecond line\n```\n\n| Key | Meaning |\n| --- | --- |\n| `a\\|b` | <img src=x onerror=x> |\n''','docs/user/cli.md','/keelmux/','0'*40)
        self.assertIn("printf &#x27;&lt;tag&gt;&amp;x&#x27;\nsecond line",fragment)
        self.assertIn('<code>a|b</code>',fragment)
        self.assertNotIn('<img',fragment);self.assertIn('&lt;img',fragment)

    def test_unsafe_schemes_and_escaping_repo_are_refused(self):
        for target in ('javascript:alert(1)','data:text/html,x','//evil.invalid/x','../../../outside.md','bad\\path.md','x\x00y'):
            with self.subTest(target=target),self.assertRaises(ValueError):site.href(target,'docs/user/cli.md','/keelmux/','0'*40)
        result=site.inline('<script>alert(1)</script> **safe**','docs/user/cli.md','/keelmux/','0'*40)
        self.assertNotIn('<script>',result);self.assertIn('&lt;script&gt;',result)

    def test_duplicate_heading_anchors_resolve_and_fences_fail_closed(self):
        rendered,headings=site.markdown('# Title\n## Same\n## Same\n','docs/user/cli.md','/keelmux/','0'*40)
        self.assertEqual([h[2] for h in headings],['same','same-1'])
        with self.assertRaises(ValueError):site.markdown('# Title\n```sh\nunclosed','docs/user/cli.md','/keelmux/','0'*40)

    def test_root_path_and_invalid_deployment_prefix_controls(self):
        self.assertEqual(site.href('daily-use.md#channel-inbox','docs/user/quickstart.md','/','0'*40),'/daily-use/#channel-inbox')
        for value in ('keelmux/','/keelmux','//','/../','/foo?bar/'):
            with self.subTest(value=value),self.assertRaises(ValueError):site.base_path(value)
        with self.assertRaises(ValueError):site.build(site.ROOT,'/keelmux/')


if __name__=='__main__':unittest.main(verbosity=2)
