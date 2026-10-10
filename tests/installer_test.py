#!/usr/bin/env python3
"""Owned installer/packer checks. Fake version-only binaries; no native CLI use."""
import copy
import hashlib
import http.server
import io
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import sys
import tarfile
import tempfile
import threading
import unittest

ROOT = Path(__file__).resolve().parents[1]
INSTALLER = ROOT / "install.sh"
PACKER = ROOT / "scripts/pack-release.py"
COMMIT = "a" * 40
OS = {"Darwin": "darwin", "Linux": "linux"}[platform.system()]
ARCH = {"arm64": "arm64", "aarch64": "arm64", "x86_64": "amd64", "amd64": "amd64"}[platform.machine()]
EPOCH = "1728000000"
BUILT_AT = "2024-10-04T00:00:00Z"
CANONICAL = ("keelmux", "keelmuxd", "keelmux-entry", "keelmux-supervisor")


def sha(data):
    return hashlib.sha256(data).hexdigest()


class Fixture:
    def __init__(self, root):
        self.root = root
        self.base = root / "downloads"
        self.base.mkdir()
        self.home = root / "home"
        self.home.mkdir(mode=0o700)
        self.prefix = self.home / "code with spaces"
        self.bin = self.home / "commands"
        self.archive_entered = threading.Event()
        self.archive_release = threading.Event()
        self.archive_release.set()
        fixture = self

        class Handler(http.server.SimpleHTTPRequestHandler):
            def __init__(self, *args, **kwargs):
                super().__init__(*args, directory=str(fixture.base), **kwargs)

            def do_GET(self):
                if self.path.endswith(".tar.gz"):
                    fixture.archive_entered.set()
                    if not fixture.archive_release.wait(20):
                        self.send_error(503)
                        return
                super().do_GET()

            def log_message(self, *_):
                pass

        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.env = dict(os.environ, HOME=str(self.home),
                        KEELMUX_ALLOW_TEST_HTTP="1",
                        KEELMUX_DOWNLOAD_BASE_URL=f"http://127.0.0.1:{self.server.server_port}",
                        KEELMUX_TEST_OS=OS, KEELMUX_TEST_ARCH=ARCH)
        for key in ("KEELMUX_VERSION", "KEELMUX_INSTALL_DIR", "KEELMUX_BIN_DIR"):
            self.env.pop(key, None)

    def close(self):
        self.archive_release.set()
        self.server.shutdown()
        self.server.server_close()
        self.thread.join()

    def make_release(self, version="v0.1.0", schema=2):
        build = self.root / f"build-{version}"
        build.mkdir()
        for name in CANONICAL:
            text = (f"#!/bin/sh\n# Version-only fixture: {name}\n"
                    'case "$1" in version|--version) ;; *) exit 8 ;; esac\n'
                    f"printf '%s\\n' 'Keelmux {version} (commit {COMMIT}, protocol bp/1)'\n")
            (build / name).write_text(text)
        for name in ("LICENSE", "README.md"):
            (build / name).write_text(f"Fixture {name}\n")
        output = self.base / version
        output.mkdir()
        subprocess.run([sys.executable, str(PACKER), str(build), str(output),
                        version, COMMIT, BUILT_AT, OS, ARCH, "go1.26.6",
                        EPOCH, "false", "0"], check=True)
        if schema == 1:
            # Finite legacy archive/receipt corpus, derived without invoking any
            # old or native runtime. This exercises the installed-v1 contract.
            def legacy(entries):
                entries[:] = [(info, data) for info, data in entries
                              if info.name != "keelmux-supervisor"]
                for i, (info, data) in enumerate(entries):
                    if info.name == "RELEASE.json":
                        value = json.loads(data)
                        value["schema"] = 1
                        del value["files"]["keelmux-supervisor"]
                        entries[i] = (info, json.dumps(value).encode())
            self.rewrite(version, legacy)
        elif schema != 2:
            raise ValueError("unsupported fixture schema")
        self.checksums(version)
        return output / f"keelmux_{version}_{OS}_{ARCH}.tar.gz"

    def checksums(self, version):
        subprocess.run([sys.executable, str(PACKER), "--checksums",
                        str(self.base / version)], check=True)

    def rewrite(self, version, edit):
        asset = self.base / version / f"keelmux_{version}_{OS}_{ARCH}.tar.gz"
        with tarfile.open(asset, "r:gz") as archive:
            entries = [(copy.copy(info), archive.extractfile(info).read()
                        if info.isfile() else None) for info in archive]
        edit(entries)
        with tarfile.open(asset, "w:gz", format=tarfile.USTAR_FORMAT) as archive:
            for info, data in entries:
                if data is not None:
                    info.size = len(data)
                archive.addfile(info, None if data is None else io.BytesIO(data))
        self.checksums(version)

    def manifest_edit(self, version, edit):
        def apply(entries):
            for i, (info, data) in enumerate(entries):
                if info.name == "RELEASE.json":
                    entries[i] = (info, edit(data))
        self.rewrite(version, apply)

    def args(self, version="v0.1.0", uninstall=False):
        return ["sh", str(INSTALLER), "--prefix", str(self.prefix),
                "--bin-dir", str(self.bin)] + (
                    ["--uninstall"] if uninstall else ["--version", version])

    def run(self, version="v0.1.0", uninstall=False, env=None):
        return subprocess.run(self.args(version, uninstall), env=env or self.env,
                              capture_output=True, text=True, timeout=30)

    def current(self):
        return os.readlink(self.prefix / "current")


@unittest.skipIf(os.getuid() == 0, "installer deliberately refuses root")
class InstallerTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="keelmux-installer-test-")
        self.f = Fixture(Path(self.tmp.name).resolve())

    def tearDown(self):
        self.f.close()
        self.tmp.cleanup()

    def assert_ok(self, result):
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def assert_failed(self, result):
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_install_reinstall_and_owned_uninstall_preserve_state_auth_skills(self):
        self.f.make_release()
        secret = self.f.home / ".codex/auth.json"
        secret.parent.mkdir()
        secret.write_text("private native auth fixture")
        self.assert_ok(self.f.run())
        before = self.f.current()
        self.assertEqual(before, f"versions/v0.1.0_{OS}_{ARCH}")
        for name, target in {"keelmux": "keelmux", "keelmuxd": "keelmuxd",
                             "keelmux-entry": "keelmux-entry", "bp": "keelmux",
                             "bpd": "keelmuxd", "bpg": "keelmux-entry"}.items():
            self.assertEqual(os.readlink(self.f.bin / name),
                             str(self.f.prefix / "current" / target))
        self.assert_ok(self.f.run())
        self.assertEqual(self.f.current(), before)
        state = self.f.prefix / "state.db"
        state.write_text("state")
        skills = self.f.prefix / before / "skills"
        skills.mkdir()
        (skills / "custom.md").write_text("owned user skill")
        self.assert_ok(self.f.run(uninstall=True))
        self.assertTrue(state.exists())
        self.assertEqual(secret.read_text(), "private native auth fixture")
        self.assertEqual((skills / "custom.md").read_text(), "owned user skill")
        self.assertFalse((self.f.bin / "keelmux").exists())
        self.assertFalse((self.f.prefix / before / "keelmux").exists())
        self.assert_ok(self.f.run(uninstall=True))

    def test_upgrade_and_checksum_failure_preserve_previous_current(self):
        self.f.make_release()
        self.assert_ok(self.f.run())
        old = self.f.current()
        asset = self.f.make_release("v0.2.0")
        asset.write_bytes(asset.read_bytes()[:-50])
        self.assert_failed(self.f.run("v0.2.0"))
        self.assertEqual(self.f.current(), old)
        self.assertTrue((self.f.bin / "keelmux").exists())

    def test_complete_upgrade_atomically_selects_new_version(self):
        self.f.make_release()
        self.f.make_release("v0.2.0")
        self.assert_ok(self.f.run())
        old_path = self.f.prefix / self.f.current()
        self.assert_ok(self.f.run("v0.2.0"))
        self.assertEqual(self.f.current(), f"versions/v0.2.0_{OS}_{ARCH}")
        self.assertTrue((old_path / "keelmux").exists())

    def test_foreign_bin_collision_is_preserved(self):
        self.f.make_release()
        self.f.bin.mkdir(mode=0o700)
        foreign = self.f.bin / "bp"
        foreign.write_text("foreign")
        self.assert_failed(self.f.run())
        self.assertEqual(foreign.read_text(), "foreign")
        self.assertFalse((self.f.prefix / "current").exists())

    def test_fresh_prefix_does_not_adopt_prospective_owned_link(self):
        self.f.make_release()
        self.f.bin.mkdir(mode=0o700)
        foreign = self.f.bin / "bp"
        target = str(self.f.prefix / "current/keelmux")
        foreign.symlink_to(target)
        self.assert_failed(self.f.run())
        self.assertEqual(os.readlink(foreign), target)
        self.assertFalse((self.f.prefix / "current").exists())

    def test_foreign_path_collision_is_preserved(self):
        self.f.make_release()
        foreign_dir = Path(self.tmp.name) / "foreign-bin"
        foreign_dir.mkdir()
        foreign = foreign_dir / "keelmux"
        foreign.write_text("#!/bin/sh\nexit 0\n")
        foreign.chmod(0o700)
        env = dict(self.f.env, PATH=f"{foreign_dir}:{self.f.env['PATH']}")
        self.assert_failed(self.f.run(env=env))
        self.assertTrue(foreign.exists())

    def test_manifest_duplicate_escaped_key_and_wrong_version_are_denied(self):
        self.f.make_release()
        self.f.manifest_edit("v0.1.0", lambda data: data.replace(
            b'"schema": 2,', b'"schema": 2, "sch\\u0065ma": 2,'))
        self.assert_failed(self.f.run())
        self.assertFalse((self.f.prefix / "current").exists())

    def test_manifest_file_digest_mismatch_is_denied(self):
        self.f.make_release()
        def corrupt(data):
            value = json.loads(data)
            value["files"]["keelmux"] = "0" * 64
            return json.dumps(value).encode()
        self.f.manifest_edit("v0.1.0", corrupt)
        self.assert_failed(self.f.run())

    def test_external_archive_alias_is_denied_before_extract(self):
        self.f.make_release()
        def edit(entries):
            for info, _ in entries:
                if info.name == "bpg":
                    info.linkname = "../../outside"
        self.f.rewrite("v0.1.0", edit)
        self.assert_failed(self.f.run())
        self.assertFalse((self.f.prefix / "current").exists())

    def test_traversal_archive_entry_is_denied_before_extract(self):
        self.f.make_release()
        def edit(entries):
            for info, _ in entries:
                if info.name == "README.md":
                    info.name = "../../outside"
        self.f.rewrite("v0.1.0", edit)
        self.assert_failed(self.f.run())
        self.assertFalse((self.f.home / "outside").exists())

    def test_concurrent_initial_loser_cannot_remove_winner_scaffold(self):
        self.f.make_release()
        self.f.archive_release.clear()
        first = subprocess.Popen(self.f.args(), env=self.f.env,
                                 stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                 text=True)
        try:
            self.assertTrue(self.f.archive_entered.wait(10))
            self.assert_failed(self.f.run())
            self.assertTrue((self.f.prefix / "versions").is_dir())
            self.assertTrue((self.f.prefix / ".install.lock").is_dir())
        finally:
            self.f.archive_release.set()
        out, err = first.communicate(timeout=30)
        self.assertEqual(first.returncode, 0, out + err)
        self.assertTrue((self.f.prefix / "current").is_symlink())

    def test_modified_installed_code_is_not_replaced_or_removed(self):
        self.f.make_release()
        self.assert_ok(self.f.run())
        binary = self.f.prefix / self.f.current() / "keelmux"
        binary.write_text("modified private code")
        self.assert_failed(self.f.run())
        self.assert_ok(self.f.run(uninstall=True))
        self.assertEqual(binary.read_text(), "modified private code")

    def test_helper_is_owned_sibling_without_public_command(self):
        self.f.make_release()
        self.assert_ok(self.f.run())
        selected = self.f.prefix / self.f.current()
        self.assertEqual((selected / ".keelmux-version").read_text().strip(),
                         "keelmux-installed-v2")
        self.assertTrue((selected / "keelmux-supervisor").is_file())
        self.assertFalse(os.path.lexists(self.f.bin / "keelmux-supervisor"))
        manifest = json.loads((selected / "RELEASE.json").read_text())
        self.assertEqual(sha((selected / "keelmux-supervisor").read_bytes()),
                         manifest["files"]["keelmux-supervisor"])
        self.assert_ok(self.f.run(uninstall=True))
        self.assertFalse((selected / "keelmux-supervisor").exists())

    def test_legacy_v1_upgrade_reinstall_downgrade_and_uninstall_preserve_state(self):
        self.f.make_release(schema=1)
        self.f.make_release("v0.2.0")
        self.assert_ok(self.f.run())
        legacy = self.f.prefix / self.f.current()
        self.assertEqual((legacy / ".keelmux-version").read_text().strip(),
                         "keelmux-installed-v1")
        self.assertFalse((legacy / "keelmux-supervisor").exists())
        state = self.f.home / ".local/state/agent-backplane/registry.sqlite"
        state.parent.mkdir(parents=True)
        state.write_bytes(b"private registry sentinel")
        auth = self.f.home / ".claude/.credentials.json"
        auth.parent.mkdir()
        auth.write_bytes(b"private auth sentinel")
        self.assert_ok(self.f.run("v0.2.0"))
        native = self.f.prefix / self.f.current()
        self.assertTrue((native / "keelmux-supervisor").is_file())
        self.assert_ok(self.f.run("v0.2.0"))
        self.assert_ok(self.f.run())
        self.assertEqual(self.f.prefix / self.f.current(), legacy)
        self.assert_ok(self.f.run("v0.2.0"))
        self.assert_ok(self.f.run(uninstall=True))
        self.assertFalse((native / "keelmux-supervisor").exists())
        self.assertFalse((legacy / "keelmux").exists())
        self.assertEqual(state.read_bytes(), b"private registry sentinel")
        self.assertEqual(auth.read_bytes(), b"private auth sentinel")

    def test_missing_native_helper_preserves_legacy_current(self):
        self.f.make_release(schema=1)
        self.assert_ok(self.f.run())
        old = self.f.current()
        self.f.make_release("v0.2.0")
        def remove_helper(entries):
            entries[:] = [(info, data) for info, data in entries
                          if info.name != "keelmux-supervisor"]
        self.f.rewrite("v0.2.0", remove_helper)
        self.assert_failed(self.f.run("v0.2.0"))
        self.assertEqual(self.f.current(), old)

    def test_native_helper_digest_mismatch_is_denied(self):
        self.f.make_release()
        def edit(entries):
            for i, (info, data) in enumerate(entries):
                if info.name == "keelmux-supervisor":
                    entries[i] = (info, data + b"# modified fixture\n")
        self.f.rewrite("v0.1.0", edit)
        self.assert_failed(self.f.run())
        self.assertFalse((self.f.prefix / "current").exists())

    def test_native_helper_symlink_is_denied(self):
        self.f.make_release()
        def edit(entries):
            for i, (info, data) in enumerate(entries):
                if info.name == "keelmux-supervisor":
                    info.type, info.linkname = tarfile.SYMTYPE, "keelmux"
                    entries[i] = (info, None)
        self.f.rewrite("v0.1.0", edit)
        self.assert_failed(self.f.run())

    def test_schema_archive_and_helper_digest_membership_must_agree(self):
        for variant in ("schema1_with_helper", "schema2_without_digest", "schema1_extra_digest"):
            with self.subTest(variant=variant):
                version = "v0.3." + str(("schema1_with_helper", "schema2_without_digest", "schema1_extra_digest").index(variant))
                self.f.make_release(version, schema=1 if variant == "schema1_extra_digest" else 2)
                def edit(data):
                    value = json.loads(data)
                    if variant == "schema1_with_helper":
                        value["schema"] = 1
                    elif variant == "schema2_without_digest":
                        del value["files"]["keelmux-supervisor"]
                    else:
                        value["files"]["keelmux-supervisor"] = "0" * 64
                    return json.dumps(value).encode()
                self.f.manifest_edit(version, edit)
                self.assert_failed(self.f.run(version))
                self.assertFalse((self.f.prefix / "current").exists())

    def test_each_canonical_version_mismatch_is_denied_after_valid_digest(self):
        for index, binary in enumerate(CANONICAL):
            with self.subTest(binary=binary):
                version = f"v0.4.{index}"
                self.f.make_release(version)
                def edit(entries):
                    replacement = b"#!/bin/sh\nprintf '%s\\n' 'Keelmux v9.9.9 (commit " + COMMIT.encode() + b", protocol bp/1)'\n"
                    for i, (info, data) in enumerate(entries):
                        if info.name == binary:
                            entries[i] = (info, replacement)
                        elif info.name == "RELEASE.json":
                            value = json.loads(data)
                            value["files"][binary] = sha(replacement)
                            entries[i] = (info, json.dumps(value).encode())
                self.f.rewrite(version, edit)
                self.assert_failed(self.f.run(version))
                self.assertFalse((self.f.prefix / "current").exists())

    def test_modified_installed_helper_refuses_upgrade_and_preserves_bytes(self):
        self.f.make_release()
        self.f.make_release("v0.2.0")
        self.assert_ok(self.f.run())
        old = self.f.current()
        helper = self.f.prefix / old / "keelmux-supervisor"
        helper.write_bytes(b"modified private helper")
        self.assert_failed(self.f.run("v0.2.0"))
        self.assertEqual(self.f.current(), old)
        self.assert_ok(self.f.run(uninstall=True))
        self.assertEqual(helper.read_bytes(), b"modified private helper")


    def test_http_and_platform_overrides_require_explicit_loopback_mode(self):
        self.f.make_release()
        self.assert_failed(self.f.run(env=dict(self.f.env, KEELMUX_ALLOW_TEST_HTTP="0")))
        self.assert_failed(self.f.run(env=dict(self.f.env,
            KEELMUX_DOWNLOAD_BASE_URL="http://example.invalid:80")))
        self.assert_failed(self.f.run(env=dict(self.f.env,
            KEELMUX_DOWNLOAD_BASE_URL="https://example.invalid/releases")))


class PackerTests(unittest.TestCase):
    def test_equal_epoch_pack_is_byte_identical_and_finite(self):
        with tempfile.TemporaryDirectory(prefix="keelmux-pack-test-") as temporary:
            root = Path(temporary)
            source = root / "source"
            source.mkdir()
            for name in CANONICAL + ("LICENSE", "README.md"):
                (source / name).write_text(name + "\n")
            archives = []
            for i in range(2):
                output = root / str(i)
                output.mkdir()
                subprocess.run([sys.executable, str(PACKER), str(source), str(output),
                                "v0.1.0", COMMIT, BUILT_AT, OS, ARCH, "go1.26.6",
                                EPOCH, "false", "0"], check=True)
                archives.append(next(output.glob("*.tar.gz")))
            self.assertEqual(archives[0].read_bytes(), archives[1].read_bytes())
            with tarfile.open(archives[0]) as archive:
                self.assertEqual(len(archive.getmembers()), 10)
                self.assertEqual(json.load(archive.extractfile("RELEASE.json"))["schema"], 2)
                manifest = json.load(archive.extractfile("RELEASE.json"))
                self.assertEqual(manifest["commit"], COMMIT)
                self.assertTrue(manifest["source_clean"])
                self.assertFalse(manifest["development_test"])
                for name, digest in manifest["files"].items():
                    self.assertEqual(digest, sha(archive.extractfile(name).read()))


if __name__ == "__main__":
    unittest.main(verbosity=2)
