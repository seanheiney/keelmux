#!/usr/bin/env python3
"""Owned offline drift controls; never execute a copied module or fixture."""
import importlib.util
import json
import os
from pathlib import Path
import shutil
import stat
import sys
import tempfile
import unittest

sys.dont_write_bytecode = True
SOURCE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("vt_verifier", SOURCE / "verify.py")
verifier = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verifier)


def snapshot(root):
    """Capture contents and file identities relevant to refusal without follows."""
    found = {}
    for current, directories, files in os.walk(root, followlinks=False):
        for name in directories + files:
            path = Path(current) / name
            metadata = path.lstat()
            payload = (path.read_bytes() if stat.S_ISREG(metadata.st_mode)
                       else os.readlink(path) if stat.S_ISLNK(metadata.st_mode) else None)
            found[str(path.relative_to(root))] = (metadata.st_mode, metadata.st_nlink, payload)
    return found


class VerifyTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="keelmux-vt-verify-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name) / "vt"
        shutil.copytree(SOURCE, self.root)

    def refuse(self):
        before = snapshot(self.root)
        with self.assertRaises((ValueError, KeyError, TypeError, OSError)):
            verifier.verify(self.root)
        self.assertEqual(snapshot(self.root), before, "refusal must not rewrite source")

    def edit_metadata(self, edit):
        path = self.root / "PROVENANCE.json"
        metadata = json.loads(path.read_bytes())
        edit(metadata)
        path.write_text(json.dumps(metadata))

    def test_exact_copy_and_separate_zip(self):
        before = snapshot(self.root)
        independent = Path(self.temporary.name) / "official.zip"
        shutil.copyfile(self.root / "upstream/module.zip", independent)
        result = verifier.verify(self.root, independent)
        self.assertEqual(result["upstream_files"], 33)
        self.assertEqual(result["unmodified_files"], 32)
        self.assertEqual(result["changed_files"], ["utf8.go"])
        self.assertTrue(result["separate_upstream_zip_compared"])
        self.assertEqual(snapshot(self.root), before)
        independent.write_bytes(independent.read_bytes() + b"drift")
        with self.assertRaises(ValueError):
            verifier.verify(self.root, independent)
        self.assertEqual(snapshot(self.root), before)

    def test_every_upstream_file_is_required_and_byte_exact(self):
        files = verifier.original_files((self.root / "upstream/module.zip").read_bytes())
        for name in sorted(files):
            with self.subTest(name=name, operation="change"):
                path = self.root / name
                original = path.read_bytes()
                path.write_bytes(original + b"\n")
                self.refuse()
                path.write_bytes(original)
            with self.subTest(name=name, operation="missing"):
                path.unlink()
                self.refuse()
                path.write_bytes(original)

    def test_guard_and_exactly_one_expression(self):
        path = self.root / "utf8.go"
        original = path.read_bytes()
        mutations = [
            original.replace(verifier.AFTER, verifier.BEFORE),
            original.replace(b"len(e.grapheme) == 1", b"len(e.grapheme) > 0"),
            original.replace(b"r < ansi.DEL", b"r <= ansi.DEL"),
            original.replace(b"string(e.grapheme[:1])", b"string(e.grapheme)"),
            original.replace(b"e.grapheme = e.grapheme[:0]", b"e.grapheme = nil"),
        ]
        for index, changed in enumerate(mutations):
            with self.subTest(index=index):
                self.assertNotEqual(changed, original)
                path.write_bytes(changed)
                self.refuse()
        path.write_bytes(original)

    def test_forged_file_hash_cannot_authorize_drift(self):
        path = self.root / "utf8.go"
        path.write_bytes(path.read_bytes().replace(b"len(e.grapheme) == 1", b"len(e.grapheme) > 0"))
        digest = verifier.sha(path.read_bytes())
        self.edit_metadata(lambda data: data["copied_files"]["utf8.go"].update(sha256=digest))
        self.refuse()

    def test_originals_are_required_and_byte_exact(self):
        for name in sorted(verifier.ORIGINALS):
            path = self.root / name
            original = path.read_bytes()
            with self.subTest(name=name, operation="change"):
                path.write_bytes(original + b"drift")
                self.refuse()
                path.write_bytes(original)
            with self.subTest(name=name, operation="missing"):
                path.unlink()
                self.refuse()
                path.write_bytes(original)

    def test_each_authored_file_is_bound(self):
        for name in sorted(verifier.AUTHORED):
            path = self.root / name
            original = path.read_bytes()
            path.write_bytes(original + b"\n")
            with self.subTest(name=name):
                self.refuse()
            path.write_bytes(original)

    def test_provenance_identity_checksums_patch_inventory_are_bound(self):
        path = self.root / "PROVENANCE.json"
        original = path.read_bytes()
        changes = [
            lambda data: data.update(schema=True),
            lambda data: data.update(extra="unapproved"),
            lambda data: data["upstream"].update(commit="0" * 40),
            lambda data: data["upstream"].update(module_sum="h1:forged"),
            lambda data: data["upstream"].update(go_mod_sum="h1:forged"),
            lambda data: data["upstream"].update(zip_sha256="0" * 64),
            lambda data: data["change"].update(after="arbitrary"),
            lambda data: data["copied_files"].pop("LICENSE"),
            lambda data: data["copied_files"]["go.sum"].update(bytes=0),
            lambda data: data["preserved_originals"].update({"upstream/utf8.go.orig": "0" * 64}),
            lambda data: data["authored_files"].update({"README.md": "0" * 64}),
        ]
        for index, edit in enumerate(changes):
            with self.subTest(index=index):
                path.write_bytes(original)
                self.edit_metadata(edit)
                self.refuse()
        path.write_bytes(original.replace(b'"schema": 1', b'"schema": 1, "schema": 1'))
        self.assertNotEqual(path.read_bytes(), original)
        self.refuse()

    def test_extra_entries_and_oversized_inventory(self):
        for name in ["unexpected.go", "upstream/extra", ".hidden"]:
            path = self.root / name
            path.write_bytes(b"extra")
            with self.subTest(name=name):
                self.refuse()
            path.unlink()
        empty = self.root / "empty"
        empty.mkdir()
        self.refuse()
        empty.rmdir()
        for index in range(65):
            (self.root / ("extra-%d" % index)).write_bytes(b"")
        self.refuse()

    def test_symlinks_hardlinks_special_and_executable_files(self):
        path = self.root / "emulator.go"
        original = path.read_bytes()
        outside = Path(self.temporary.name) / "outside.go"
        outside.write_bytes(original)
        path.unlink()
        path.symlink_to(outside)
        self.refuse()
        path.unlink()
        os.link(outside, path)
        self.refuse()
        path.unlink()
        os.mkfifo(path)
        self.refuse()
        path.unlink()
        path.write_bytes(original)
        path.chmod(0o755)
        self.refuse()

    def test_source_and_upstream_directory_symlinks(self):
        alias = Path(self.temporary.name) / "alias"
        alias.symlink_to(self.root, target_is_directory=True)
        before = snapshot(self.root)
        with self.assertRaises(ValueError):
            verifier.verify(alias)
        self.assertEqual(snapshot(self.root), before)
        real = Path(self.temporary.name) / "real-upstream"
        shutil.move(self.root / "upstream", real)
        (self.root / "upstream").symlink_to(real, target_is_directory=True)
        self.refuse()


if __name__ == "__main__":
    unittest.main()
