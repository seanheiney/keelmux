#!/usr/bin/env python3
"""Verify the finite, recorded upstream source subset without network access."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import stat


def require(condition, message):
    if not condition:
        raise ValueError(message)


def file_digest(path):
    require(stat.S_ISREG(path.lstat().st_mode), "source must be a regular file")
    return hashlib.sha256(path.read_bytes()).hexdigest()


def verify(directory, upstream=None):
    manifest = json.loads((directory / "PROVENANCE.json").read_text())
    require(manifest["schema"] == 1, "unsupported provenance schema")
    source = manifest["upstream"]
    require(source["module"] == "github.com/charmbracelet/bubbletea" and
            source["version"] == "v1.3.10" and
            source["repository"] == "https://github.com/charmbracelet/bubbletea" and
            source["commit"] == "9edf69c677c7353eca5fae6d3ea3986af39717b7",
            "unexpected pinned upstream")
    copied = manifest["copied_files"]
    removed = manifest["removed_files"]
    require(set(removed) == {"tea_init.go"}, "unexpected upstream modification")
    require(not (directory / "tea_init.go").exists() and
            not (directory / "tea_init.go").is_symlink(), "eager initialization returned")
    authored = set(manifest["authored_files"])
    require(authored == {"README.md", "PROVENANCE.json", "verify.py"},
            "unexpected local source addition")
    actual = {str(p.relative_to(directory)) for p in directory.rglob("*")}
    require(actual == set(copied) | authored, "extra or missing dependency file")
    origins = set()
    for name, receipt in copied.items():
        origin = receipt["upstream_path"]
        require(Path(name).name == name and Path(origin).name == origin,
                "source map is not finite and flat")
        require(origin not in origins, "duplicate upstream source")
        origins.add(origin)
        require(re.fullmatch(r"[0-9a-f]{64}", receipt["sha256"]) is not None,
                "invalid source digest")
        require(file_digest(directory / name) == receipt["sha256"],
                "copied upstream file changed")
        if upstream is not None:
            require(file_digest(upstream / origin) == receipt["sha256"],
                    "official downloaded source differs")
    require({"LICENSE", "go.mod", "go.sum", "README.md"} <= origins,
            "upstream license/module documentation missing")
    exclusions = manifest["excluded_upstream_files"]
    require(len(exclusions) == len(set(exclusions)) and
            not set(exclusions) & (origins | set(removed)),
            "exclusion map overlaps retained or removed source")
    require(not any("/" not in p and p.endswith(".go") for p in exclusions),
            "unrecorded production source omission")
    if upstream is not None:
        original_init = removed["tea_init.go"]["sha256"]
        require(file_digest(upstream / "tea_init.go") == original_init,
                "deleted initialization source differs")
        official_root_go = {p.name for p in upstream.glob("*.go")}
        require(official_root_go == {p for p in origins if p.endswith(".go")} | set(removed),
                "official root Go source set differs")
    return {"result": "pass", "upstream_version": source["version"],
            "upstream_commit": source["commit"], "copied_files": len(copied),
            "removed_files": sorted(removed), "excluded_paths": len(exclusions),
            "downloaded_upstream_compared": upstream is not None}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, default=Path(__file__).resolve().parent,
                        help="source copy to validate, useful for adverse fixtures")
    parser.add_argument("--upstream", type=Path,
                        help="separately downloaded official v1.3.10 Go module")
    args = parser.parse_args()
    try:
        result = verify(args.source, args.upstream)
    except (ValueError, KeyError, OSError) as error:
        raise SystemExit(str(error)) from error
    print(json.dumps(result, sort_keys=True))


if __name__ == "__main__":
    main()
