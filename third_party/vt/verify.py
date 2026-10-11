#!/usr/bin/env python3
"""Verify the exact pinned VT copy and its single approved expression change."""
import argparse
import base64
import hashlib
import io
import json
from pathlib import Path
import stat
import zipfile

MODULE = "github.com/charmbracelet/x/vt"
VERSION = "v0.0.0-20261008172826-faa4adf95555"
COMMIT = "faa4adf95555331dfb439b716cc83767ecdadeb4"
ZIP_SHA = "33e779af4c1855a14128f1cb623dc11a35e28b5dadb20a3de5a7a23bf416e9f8"
ZIP_BYTES = 48426
MODULE_SUM = "h1:l0NChS3jadPx3cUmaUbFm7LYFCoORN26aKmBfW2EEkk="
MOD_SUM = "h1:u1LOIABor9JqY54oZdktK3TCRrgzP6tzHrDYx1nd3wY="
INFO_SHA = "8ee7c42a67ebeb1f5e3df9b92065962c9f6b66da13ebde8fd41ed5a107c1c190"
ORIGINAL_SHA = "5362209587478feb6c3dc5367ed5f02c7789a8094736c42d305848ee1aa15247"
CHANGED_SHA = "05154c7c3544764e8c7678c6b933bf582fb31ac092498ce00b97b47f348e560e"
BEFORE = b"e.handleGrapheme(string(e.grapheme[0]), 1)"
AFTER = b"e.handleGrapheme(string(e.grapheme[:1]), 1)"
AUTHORED = {"README.md", "verify.py", "verify_test.py"}
ORIGINALS = {"upstream/module.zip", "upstream/module.info", "upstream/utf8.go.orig"}


def require(value, message):
    if not value:
        raise ValueError(message)


def sha(data):
    return hashlib.sha256(data).hexdigest()


def read(path, bound=1 << 20):
    metadata = path.lstat()
    require(stat.S_ISREG(metadata.st_mode) and metadata.st_nlink == 1,
            "expected an ordinary regular file: " + path.name)
    require(metadata.st_mode & 0o111 == 0, "source executable mode refused")
    require(metadata.st_size <= bound, "source file too large")
    return path.read_bytes()


def strict_object(pairs):
    value = {}
    for key, item in pairs:
        require(key not in value, "duplicate provenance field")
        value[key] = item
    return value


def h1(files):
    summary = hashlib.sha256()
    for name, data in sorted(files.items()):
        require("\n" not in name, "invalid checksum filename")
        summary.update((sha(data) + "  " + name + "\n").encode())
    return "h1:" + base64.b64encode(summary.digest()).decode()


def inventory(directory):
    require(stat.S_ISDIR(directory.lstat().st_mode), "source root must be a real directory")
    found, pending = set(), [directory]
    while pending:
        current = pending.pop()
        for path in current.iterdir():
            name = str(path.relative_to(directory))
            require(len(found) < 64, "unexpected oversized source inventory")
            found.add(name)
            metadata = path.lstat()
            if stat.S_ISDIR(metadata.st_mode):
                pending.append(path)
            else:
                require(stat.S_ISREG(metadata.st_mode) and metadata.st_nlink == 1,
                        "source symlink or special/hardlinked file refused")
                require(metadata.st_mode & 0o111 == 0, "source executable mode refused")
    return found


def original_files(blob):
    require(len(blob) == ZIP_BYTES and sha(blob) == ZIP_SHA, "pinned upstream ZIP changed")
    prefix = MODULE + "@" + VERSION + "/"
    files, full = {}, {}
    with zipfile.ZipFile(io.BytesIO(blob)) as archive:
        require(len(archive.infolist()) == 33, "upstream file count changed")
        for member in archive.infolist():
            require(member.filename.startswith(prefix), "upstream module ZIP prefix changed")
            name = member.filename[len(prefix):]
            require(Path(name).name == name and name not in files and not member.is_dir()
                    and member.file_size <= 1 << 20, "upstream member invalid")
            data = archive.read(member)
            files[name] = data
            full[member.filename] = data
    require(h1(full) == MODULE_SUM and h1({"go.mod": files["go.mod"]}) == MOD_SUM,
            "pinned Go module checksums differ")
    require({"LICENSE", "go.mod", "go.sum", "utf8.go", "utf8_test.go", "emulator_test.go",
             "scrollback_test.go", "osc_test.go"} <= files.keys(), "upstream license/tests missing")
    require(sha(files["utf8.go"]) == ORIGINAL_SHA and files["utf8.go"].count(BEFORE) == 1,
            "upstream ASCII expression changed")
    return files


def verify(directory, upstream_zip=None):
    actual = inventory(directory)
    metadata = json.loads(read(directory / "PROVENANCE.json", 1 << 16), object_pairs_hook=strict_object)
    require(set(metadata) == {"schema", "upstream", "copied_files", "change", "preserved_originals", "authored_files"}
            and type(metadata["schema"]) is int and metadata["schema"] == 1, "provenance schema changed")
    require(metadata["upstream"] == {"module": MODULE, "version": VERSION,
        "repository": "https://github.com/charmbracelet/x", "subdirectory": "vt", "commit": COMMIT,
        "module_sum": MODULE_SUM, "go_mod_sum": MOD_SUM, "zip_sha256": ZIP_SHA,
        "zip_bytes": ZIP_BYTES, "info_sha256": INFO_SHA}, "pinned upstream identity changed")
    originals = {"upstream/module.zip": ZIP_SHA, "upstream/module.info": INFO_SHA,
                 "upstream/utf8.go.orig": ORIGINAL_SHA}
    require(metadata["preserved_originals"] == originals, "original source receipt changed")
    info = read(directory / "upstream/module.info", 4096)
    require(sha(info) == INFO_SHA, "pinned module origin receipt changed")
    require(json.loads(info, object_pairs_hook=strict_object) == {"Version": VERSION,
        "Time": "2026-10-08T17:28:26Z", "Origin": {"VCS": "git",
        "URL": "https://github.com/charmbracelet/x", "Subdir": "vt", "Hash": COMMIT}},
        "full upstream commit binding changed")
    files = original_files(read(directory / "upstream/module.zip", ZIP_BYTES))
    changed = files["utf8.go"].replace(BEFORE, AFTER)
    require(sha(changed) == CHANGED_SHA, "approved patch identity changed")
    require(metadata["change"] == {"path": "utf8.go", "before": BEFORE.decode(), "after": AFTER.decode(),
        "original_sidecar": "upstream/utf8.go.orig", "original_sha256": ORIGINAL_SHA,
        "changed_sha256": CHANGED_SHA}, "approved expression receipt changed")
    require(read(directory / "upstream/utf8.go.orig") == files["utf8.go"], "original sidecar changed")
    expected = {}
    for name, original in files.items():
        wanted = changed if name == "utf8.go" else original
        expected[name] = {"upstream_sha256": sha(original), "sha256": sha(wanted), "bytes": len(wanted)}
        require(read(directory / name) == wanted, "unapproved source change: " + name)
    require(metadata["copied_files"] == expected, "upstream file inventory/hash receipt changed")
    authored = metadata["authored_files"]
    require(set(authored) == AUTHORED, "authored source inventory changed")
    for name in AUTHORED:
        require(authored[name] == sha(read(directory / name)), "authored file drift: " + name)
    require(actual == set(files) | AUTHORED | ORIGINALS | {"PROVENANCE.json", "upstream"},
            "extra or missing source inventory entry")
    if upstream_zip is not None:
        require(read(upstream_zip, ZIP_BYTES) == read(directory / "upstream/module.zip", ZIP_BYTES),
                "separately supplied official ZIP differs")
    return {"result": "pass", "upstream_version": VERSION, "upstream_commit": COMMIT,
            "upstream_files": len(files), "unmodified_files": len(files) - 1,
            "changed_files": ["utf8.go"], "original_zip_sha256": ZIP_SHA,
            "original_info_sha256": INFO_SHA,
            "original_utf8_sha256": ORIGINAL_SHA, "changed_utf8_sha256": CHANGED_SHA,
            "separate_upstream_zip_compared": upstream_zip is not None}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, default=Path(__file__).resolve().parent)
    parser.add_argument("--upstream-zip", type=Path)
    args = parser.parse_args()
    try:
        result = verify(args.source, args.upstream_zip)
    except (ValueError, KeyError, TypeError, OSError, zipfile.BadZipFile) as error:
        raise SystemExit(str(error)) from error
    print(json.dumps(result, sort_keys=True))
