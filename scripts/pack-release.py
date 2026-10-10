#!/usr/bin/env python3
"""Deterministic, finite Keelmux release archives. No publication side effects."""
import gzip
import hashlib
import io
import json
from pathlib import Path
import sys
import tarfile

CANONICAL = ("keelmux", "keelmuxd", "keelmux-entry")
FILES = CANONICAL + ("LICENSE", "README.md")
ALIASES = {"bp": "keelmux", "bpd": "keelmuxd", "bpg": "keelmux-entry"}


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    if len(sys.argv) == 3 and sys.argv[1] == "--checksums":
        directory = Path(sys.argv[2])
        assets = sorted(directory.glob("keelmux_*.tar.gz"))
        if not assets:
            raise ValueError("no release assets")
        (directory / "SHA256SUMS").write_text(
            "".join(f"{digest(asset)}  {asset.name}\n" for asset in assets))
        return
    if len(sys.argv) != 12:
        raise ValueError("pack-release expects eleven arguments")
    directory, output = map(Path, sys.argv[1:3])
    version, commit, built_at, target_os, arch, go_version = sys.argv[3:9]
    epoch = int(sys.argv[9])
    source_dirty, dev_test = sys.argv[10] == "true", sys.argv[11] == "1"
    manifest = {
        "schema": 1, "product": "Keelmux", "version": version, "commit": commit,
        "built_at": built_at, "os": target_os, "arch": arch,
        "go_version": go_version,
        "files": {name: digest(directory / name) for name in FILES},
        "aliases": ALIASES,
        "source_clean": not source_dirty,
        "development_test": dev_test,
    }
    metadata = (json.dumps(manifest, sort_keys=True, indent=2) + "\n").encode()
    asset = output / f"keelmux_{version}_{target_os}_{arch}.tar.gz"
    # Explicit format/order/uid/gid/modes/timestamps, including gzip's timestamp
    # and empty filename, make equal SOURCE_DATE_EPOCH builds reproducible.
    with asset.open("xb") as raw:
        with gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=epoch) as compressed:
            with tarfile.open(fileobj=compressed, mode="w", format=tarfile.USTAR_FORMAT) as archive:
                entries = {name: (directory / name).read_bytes() for name in FILES}
                entries["RELEASE.json"] = metadata
                for name in sorted(set(entries) | set(ALIASES)):
                    info = tarfile.TarInfo(name)
                    info.mtime, info.uid, info.gid = epoch, 0, 0
                    info.uname = info.gname = ""
                    if name in ALIASES:
                        info.type = tarfile.SYMTYPE
                        info.linkname = ALIASES[name]
                        info.mode = 0o777
                        archive.addfile(info)
                    else:
                        info.mode = 0o755 if name in CANONICAL else 0o644
                        info.size = len(entries[name])
                        archive.addfile(info, io.BytesIO(entries[name]))


if __name__ == "__main__":
    main()
