#!/usr/bin/env python3
"""Actual release archive install smoke in a temporary HOME via loopback only."""
import argparse
import hashlib
import http.server
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tempfile
import threading

ROOT = Path(__file__).resolve().parents[1]


def run(*args, env):
    result = subprocess.run(args, env=env, check=True, capture_output=True,
                            text=True, timeout=180)
    return result.stdout.strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("assets", type=Path)
    parser.add_argument("version")
    args = parser.parse_args()
    if os.getuid() == 0:
        raise SystemExit("Smoke must run as a normal user; installer refuses root.")
    target_os = {"Darwin": "darwin", "Linux": "linux"}[platform.system()]
    arch = {"arm64": "arm64", "aarch64": "arm64", "x86_64": "amd64",
            "amd64": "amd64"}[platform.machine()]
    asset = f"keelmux_{args.version}_{target_os}_{arch}.tar.gz"
    if not (args.assets / asset).is_file():
        raise SystemExit(f"Missing host release archive: {asset}")
    with tempfile.TemporaryDirectory(prefix="keelmux-release-smoke-") as temporary:
        state = Path(temporary).resolve()
        served = state / "download" / args.version
        served.mkdir(parents=True)
        shutil.copyfile(args.assets / asset, served / asset)
        shutil.copyfile(args.assets / "SHA256SUMS", served / "SHA256SUMS")
        home = state / "home"
        home.mkdir(mode=0o700)
        prefix, commands = home / "code", home / "bin"
        auth = home / ".codex/auth.json"
        auth.parent.mkdir()
        auth.write_text("private smoke sentinel: never copied or loaded")
        sentinel_hash = hashlib.sha256(auth.read_bytes()).hexdigest()

        class Handler(http.server.SimpleHTTPRequestHandler):
            def __init__(self, *a, **kw):
                super().__init__(*a, directory=str(state / "download"), **kw)

            def log_message(self, *_):
                pass

        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        env = dict(os.environ, HOME=str(home), KEELMUX_ALLOW_TEST_HTTP="1",
                   KEELMUX_DOWNLOAD_BASE_URL=f"http://127.0.0.1:{server.server_port}",
                   KEELMUX_TEST_OS=target_os, KEELMUX_TEST_ARCH=arch)
        for key in ("KEELMUX_VERSION", "KEELMUX_INSTALL_DIR", "KEELMUX_BIN_DIR"):
            env.pop(key, None)
        installer = ["sh", str(ROOT / "install.sh"), "--prefix", str(prefix),
                     "--bin-dir", str(commands)]
        try:
            run(*installer, "--version", args.version, env=env)
            metadata = json.loads(run(str(commands / "keelmux"), "version", "--json", env=env))
            selected = prefix / os.readlink(prefix / "current")
            manifest = json.loads((selected / "RELEASE.json").read_text())
            for field in ("version", "commit", "built_at", "go_version", "os", "arch"):
                if metadata[field] != manifest[field]:
                    raise RuntimeError(f"Loaded metadata differs from manifest: {field}")
            if metadata["name"] != "Keelmux" or metadata["protocol"] != "bp/1":
                raise RuntimeError("Loaded product identity/protocol differs.")
            schema = manifest.get("schema")
            if schema not in (1, 2):
                raise RuntimeError("Unsupported package schema.")
            canonical = ("keelmux", "keelmuxd", "keelmux-entry") + (
                ("keelmux-supervisor",) if schema == 2 else ())
            expected_version = (f"Keelmux {manifest['version']} "
                                f"(commit {manifest['commit']}, protocol bp/1)")
            loaded = {}
            for name in canonical:
                path = selected / name
                actual = hashlib.sha256(path.read_bytes()).hexdigest()
                if not path.is_file() or path.is_symlink() or actual != manifest["files"][name]:
                    raise RuntimeError("Installed canonical binary differs from manifest.")
                if run(str(path), "--version", env=env) != expected_version:
                    raise RuntimeError("Loaded canonical binary version differs from manifest.")
                loaded[name] = actual
            if os.path.lexists(commands / "keelmux-supervisor"):
                raise RuntimeError("Internal helper appeared in the public command directory.")
            run(*installer, "--version", args.version, env=env)
            state_file = prefix / "state-sentinel"
            state_file.write_text("preserved state")
            run(*installer, "--uninstall", env=env)
            if any((commands / name).is_symlink() for name in
                   ("keelmux", "keelmuxd", "keelmux-entry", "bp", "bpd", "bpg")):
                raise RuntimeError("Owned command link remained after uninstall.")
            if not state_file.is_file() or hashlib.sha256(auth.read_bytes()).hexdigest() != sentinel_hash:
                raise RuntimeError("Installer changed native auth or state.")
            print(json.dumps({"passed": True, "version": args.version,
                              "os": target_os, "arch": arch, "commit": manifest["commit"],
                              "source_clean": manifest.get("source_clean"),
                              "development_test": manifest.get("development_test"),
                              "archive_schema": schema, "loaded_canonical_sha256": loaded,
                              "supervisor_is_internal_sibling": schema == 2,
                              "coverage": "actual archive install/reinstall/version/uninstall; no sessions or providers"}))
        finally:
            server.shutdown()
            server.server_close()
            thread.join()


if __name__ == "__main__":
    main()
