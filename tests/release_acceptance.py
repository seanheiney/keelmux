#!/usr/bin/env python3
"""Independent package/installer tests in owned temporary homes; no native turns."""
import argparse
import copy
import hashlib
import http.server
import io
import json
import os
from pathlib import Path
import platform
import re
import shutil
import signal
import socket
import stat
import struct
import subprocess
import tarfile
import tempfile
import threading
import time


NAMES = ("keelmux", "keelmuxd", "keelmux-entry", "bp", "bpd", "bpg")
ALIASES = {"bp": "keelmux", "bpd": "keelmuxd", "bpg": "keelmux-entry"}


def digest(data):
    return hashlib.sha256(data).hexdigest()


def need(condition, tag):
    if not condition:
        raise AssertionError(tag)


def snapshot(root):
    """Compare bytes/modes/link destinations without publishing their contents."""
    if not root.exists():
        return {}
    result = {}
    for path in sorted(root.rglob("*")):
        info = path.lstat()
        name = str(path.relative_to(root))
        if path.is_symlink():
            result[name] = ("link", os.readlink(path))
        elif stat.S_ISREG(info.st_mode):
            result[name] = ("file", stat.S_IMODE(info.st_mode), digest(path.read_bytes()))
        elif stat.S_ISDIR(info.st_mode):
            result[name] = ("dir", stat.S_IMODE(info.st_mode))
        else:
            result[name] = ("other", stat.S_IFMT(info.st_mode))
    return result


class Fixture:
    def __init__(self, files, version):
        self.files = dict(files)
        self.requests = []
        self.lock = threading.Lock()
        self.truncate = set()
        self.version = version
        owner = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                prefix = "/" + owner.version + "/"
                name = self.path.removeprefix(prefix) if self.path.startswith(prefix) else ""
                with owner.lock:
                    owner.requests.append(name)
                    body = owner.files.get(name)
                    truncated = name in owner.truncate
                if body is None:
                    self.send_error(404)
                    return
                self.send_response(200)
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body[: len(body) // 2] if truncated else body)
                self.close_connection = True

            def log_message(self, *_):
                pass

        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)

    def __enter__(self):
        self.thread.start()
        return self

    @property
    def url(self):
        return "http://127.0.0.1:%d" % self.server.server_port

    def __exit__(self, *_):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=2)


class Harness:
    def __init__(self, args):
        self.args = args
        self.root = Path(__file__).resolve().parent.parent
        self.state = Path(tempfile.mkdtemp(prefix="km-r-", dir="/tmp")).resolve()
        os.chmod(self.state, 0o700)
        self.private = self.state / "diagnostics"
        self.private.mkdir(mode=0o700)
        self.commands = 0
        self.cases = 0
        self.results = []
        self.manifest = None
        self.cleanups = []
        self.target = platform.system().lower() + "_" + {
            "arm64": "arm64", "aarch64": "arm64", "x86_64": "amd64"
        }.get(platform.machine().lower(), platform.machine().lower())
        self.archive_name = "keelmux_%s_%s.tar.gz" % (args.version, self.target)
        self.archive = args.assets / self.archive_name
        self.files = {p.name: p.read_bytes() for p in args.assets.iterdir() if p.is_file()}

    def case(self, name):
        self.cases += 1
        # Short directories keep actual Darwin control/tmux sockets portable.
        base = self.state / ("c%02d" % self.cases)
        base.mkdir(mode=0o700)
        home = base / "home"
        home.mkdir(mode=0o700)
        for relative in (".codex/config.toml", ".codex/auth.json", ".claude/settings.json",
                         ".claude/.credentials.json", ".profile",
                         ".tmux.conf", ".backplane/removals/retained/sentinel"):
            path = home / relative
            path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
            path.write_bytes(b"owned-isolation-sentinel\n")
            path.chmod(0o600)
        prefix = base / "install"
        bindir = base / "bin"
        bindir.mkdir(mode=0o700)
        env = {"HOME": str(home), "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
               "LC_ALL": "C", "TMPDIR": str(base), "TERM": "xterm-256color"}
        return base, home, prefix, bindir, env

    def run(self, argv, env, timeout=30):
        self.commands += 1
        result = subprocess.run(argv, env=env, cwd=self.state,
                                capture_output=True, timeout=timeout)
        for stream in ("stdout", "stderr"):
            path = self.private / ("command-%d.%s" % (self.commands, stream))
            path.write_bytes(getattr(result, stream))
            path.chmod(0o600)
        return result

    def install(self, fixture, prefix, bindir, env, *extra):
        env = dict(env, KEELMUX_DOWNLOAD_BASE_URL=fixture.url,
                   KEELMUX_ALLOW_TEST_HTTP="1")
        return self.run(["/bin/sh", str(self.args.installer), "--version", self.args.version,
                         "--prefix", str(prefix), "--bin-dir", str(bindir), *extra], env)

    def check_installed(self, prefix, bindir):
        hashes = {}
        for name in NAMES:
            path = bindir / name
            need(path.exists() and os.access(path, os.X_OK), "installed_command_missing")
            resolved = path.resolve()
            need(resolved.is_relative_to(prefix.resolve()), "command_escapes_owned_prefix")
            hashes[name] = digest(resolved.read_bytes())
        for alias, canonical in ALIASES.items():
            need(hashes[alias] == hashes[canonical], "compatibility_alias_differs")
        return hashes

    def record(self, name, operation):
        started = time.monotonic()
        try:
            detail = operation() or {}
            self.results.append(dict(name=name, result="pass", seconds=round(time.monotonic()-started, 3), **detail))
        except Exception as exc:
            # Do not publish subprocess output, opaque exceptions or temp paths.
            tag = str(exc) if isinstance(exc, AssertionError) else type(exc).__name__
            self.results.append(dict(name=name, result="fail", failure=tag,
                                     seconds=round(time.monotonic()-started, 3)))

    def artifact(self):
        need(self.archive.is_file(), "native_archive_missing")
        expected = digest(self.archive.read_bytes())
        sums = self.files.get("SHA256SUMS", b"").decode("ascii")
        rows = {line.split()[-1].removeprefix("*"): line.split()[0]
                for line in sums.splitlines() if len(line.split()) == 2}
        need(rows.get(self.archive_name) == expected, "published_checksum_mismatch")
        with tarfile.open(self.archive, "r:gz") as package:
            raw_members = package.getmembers()
            members = {m.name.removeprefix("./"): m for m in raw_members}
            need(len(raw_members) == len(members), "duplicate_archive_member")
            for name, member in members.items():
                if name in ("", ".") and member.isdir():
                    continue
                need(not Path(name).is_absolute() and ".." not in Path(name).parts,
                     "unsafe_archive_path")
                need(len(Path(name).parts) == 1, "archive_not_flat")
                need(not member.isdev() and not member.isfifo(), "unsafe_archive_member")
            need(set(NAMES).issubset(members), "archive_command_missing")
            need("LICENSE" in members and "README.md" in members, "archive_documents_missing")
            need("RELEASE.json" in members, "archive_manifest_missing")
            manifest = json.loads(package.extractfile(members["RELEASE.json"]).read())
            self.manifest = manifest
            need(manifest.get("schema") == 1 and manifest.get("product") == "Keelmux"
                 and manifest.get("version") == self.args.version
                 and manifest.get("commit") == self.args.artifact_source_sha,
                 "archive_manifest_identity_mismatch")
            need(manifest.get("os") + "_" + manifest.get("arch") == self.target,
                 "archive_manifest_target_mismatch")
            need(manifest.get("aliases") == ALIASES, "archive_manifest_alias_mismatch")
            need(manifest.get("source_clean") is True and manifest.get("development_test") is False,
                 "artifact_is_not_clean_release_source")
            need(set(manifest.get("files", {})) == set(NAMES[:3]) | {"LICENSE", "README.md"},
                 "archive_manifest_digest_set_mismatch")
            for name, sha256 in manifest["files"].items():
                need(digest(package.extractfile(members[name]).read()) == sha256,
                     "archive_manifest_file_digest_mismatch")
            for name in NAMES[:3]:
                need(members[name].isfile() and members[name].mode & 0o111,
                     "canonical_command_not_executable")
            for alias, canonical in ALIASES.items():
                need(members[alias].issym() and members[alias].linkname == canonical,
                     "archive_alias_wrong_target")
        return {"archive_sha256": expected, "target": self.target,
                "manifest_source_commit_verified": True, "manifest_file_digests_verified": True}

    def http_optin(self):
        _, home, prefix, bindir, env = self.case("http_optin")
        before = (snapshot(home), snapshot(bindir))
        with Fixture(self.files, self.args.version) as fixture:
            env["KEELMUX_DOWNLOAD_BASE_URL"] = fixture.url
            result = self.run(["/bin/sh", str(self.args.installer), "--version", self.args.version,
                               "--prefix", str(prefix), "--bin-dir", str(bindir)], env)
            need(result.returncode != 0 and not fixture.requests, "http_download_without_explicit_optin")
        need((snapshot(home), snapshot(bindir)) == before, "denied_http_mutated_files")
        return {"no_download_without_test_optin": True}

    def spaced_paths(self):
        base, home, _, _, env = self.case("spaced_paths")
        prefix, bindir = base / "install with spaces", base / "bin with spaces"
        bindir.mkdir(mode=0o700)
        before = snapshot(home)
        with Fixture(self.files, self.args.version) as fixture:
            need(self.install(fixture, prefix, bindir, env).returncode == 0,
                 "installation_paths_with_spaces_failed")
            self.check_installed(prefix, bindir)
        need(snapshot(home) == before, "spaced_paths_install_mutated_native_files")
        return {"quoted_install_paths": True}

    def fresh_and_repeat(self):
        _, home, prefix, bindir, env = self.case("fresh")
        untouched = snapshot(home)
        with Fixture(self.files, self.args.version) as fixture:
            help_env = dict(env, KEELMUX_VERSION=self.args.version,
                            KEELMUX_DOWNLOAD_BASE_URL=fixture.url, KEELMUX_ALLOW_TEST_HTTP="1")
            help_result = self.run(["/bin/sh", str(self.args.installer), "--help"], help_env)
            need(help_result.returncode == 0 and not fixture.requests, "installer_help_has_effect")
            need(self.install(fixture, prefix, bindir, env).returncode == 0, "fresh_install_failed")
            first = self.check_installed(prefix, bindir)
            for name in ("keelmux", "bp"):
                version = self.run([str(bindir / name), "version"], env)
                need(version.returncode == 0 and self.args.version.encode() in version.stdout,
                     "installed_version_mismatch")
                need(self.args.artifact_source_sha.encode() in version.stdout,
                     "installed_source_commit_mismatch")
                provenance = self.run([str(bindir / name), "version", "--json"], env)
                need(provenance.returncode == 0, "installed_json_version_failed")
                metadata = json.loads(provenance.stdout)
                need(metadata.get("name") == "Keelmux" and metadata.get("version") == self.args.version
                     and metadata.get("commit") == self.args.artifact_source_sha
                     and metadata.get("built_at") == self.manifest["built_at"]
                     and metadata.get("protocol") == "bp/1", "installed_json_provenance_mismatch")
                help_result = self.run([str(bindir / name), "help"], env)
                need(help_result.returncode == 0 and b"keelmux" in help_result.stdout.lower(),
                     "installed_help_name_mismatch")
            for name in ("keelmuxd", "bpd", "keelmux-entry", "bpg"):
                version = self.run([str(bindir / name), "--version"], env)
                need(version.returncode == 0 and self.args.version.encode() in version.stdout
                     and self.args.artifact_source_sha.encode() in version.stdout,
                     "installed_daemon_or_entry_provenance_mismatch")
            need(self.install(fixture, prefix, bindir, env).returncode == 0, "owned_reinstall_failed")
            need(first == self.check_installed(prefix, bindir), "reinstall_changed_artifacts")
        need(snapshot(home) == untouched, "installer_mutated_native_or_shell_files")
        return {"installed_executable_sha256": first, "native_and_shell_sentinels_preserved": True}

    def bad_download(self, mode):
        _, home, prefix, bindir, env = self.case(mode)
        baseline = snapshot(home)
        with Fixture(self.files, self.args.version) as fixture:
            need(self.install(fixture, prefix, bindir, env).returncode == 0, "failure_baseline_install_failed")
            original = (snapshot(prefix), snapshot(bindir))
            fixture.requests.clear()
            if mode == "checksum_mismatch":
                fixture.files[self.archive_name] += b"corrupt"
            elif mode == "truncated_download":
                fixture.truncate.add(self.archive_name)
            elif mode == "missing_checksum":
                fixture.files.pop("SHA256SUMS", None)
            elif mode == "missing_archive":
                fixture.files.pop(self.archive_name, None)
            result = self.install(fixture, prefix, bindir, env)
            need(result.returncode != 0, "bad_download_was_accepted")
            need("SHA256SUMS" in fixture.requests, "integrity_fixture_was_not_downloaded")
            if mode != "missing_checksum":
                need(self.archive_name in fixture.requests, "bad_archive_fixture_was_not_downloaded")
            need((snapshot(prefix), snapshot(bindir)) == original, "failed_install_changed_active_tree")
        need(snapshot(home) == baseline, "failed_install_mutated_native_files")
        return {"accepted_install_preserved": True}

    def collisions(self):
        checked = 0
        for name in NAMES:
            _, home, prefix, bindir, env = self.case("collision_" + name)
            collision = bindir / name
            collision.write_bytes(b"unowned-command-sentinel\n")
            collision.chmod(0o755)
            before = (snapshot(home), snapshot(bindir))
            with Fixture(self.files, self.args.version) as fixture:
                need(self.install(fixture, prefix, bindir, env).returncode != 0,
                     "unowned_command_collision_accepted")
            need((snapshot(home), snapshot(bindir)) == before, "collision_mutated_unowned_files")
            checked += 1
        # An unowned link into this prefix is still not an ownership receipt.
        _, home, prefix, bindir, env = self.case("collision_symlink")
        prefix.mkdir(mode=0o700)
        target = prefix / "unowned"
        target.write_bytes(b"unowned-target\n")
        (bindir / "keelmux").symlink_to(target)
        before = (snapshot(home), snapshot(prefix), snapshot(bindir))
        with Fixture(self.files, self.args.version) as fixture:
            need(self.install(fixture, prefix, bindir, env).returncode != 0,
                 "unowned_symlink_collision_accepted")
        need((snapshot(home), snapshot(prefix), snapshot(bindir)) == before,
             "collision_mutated_unowned_symlink")
        return {"unowned_names_checked": checked, "unowned_symlink_preserved": True}

    def archive_boundaries(self):
        for mode in ("alias_traversal", "duplicate_command", "absolute_member", "hardlink_command"):
            base, home, prefix, bindir, env = self.case("archive_" + mode)
            outside = base / "outside-sentinel"
            outside.write_bytes(b"preserve-outside-install\n")
            outside.chmod(0o600)
            before = (snapshot(home), snapshot(bindir), outside.read_bytes())
            packed = io.BytesIO()
            with tarfile.open(self.archive, "r:gz") as original, tarfile.open(fileobj=packed, mode="w:gz") as changed:
                for original_member in original.getmembers():
                    member = copy.copy(original_member)
                    body = original.extractfile(original_member).read() if original_member.isfile() else None
                    if mode == "alias_traversal" and member.name == "bp":
                        member.linkname = "../outside-sentinel"
                    if mode == "absolute_member" and member.name == "README.md":
                        member.name = str(outside)
                    if mode == "hardlink_command" and member.name == "keelmux":
                        member.type, member.linkname, member.size = tarfile.LNKTYPE, "LICENSE", 0
                        body = None
                    changed.addfile(member, io.BytesIO(body) if body is not None else None)
                    if mode == "duplicate_command" and member.name == "keelmux":
                        changed.addfile(copy.copy(member), io.BytesIO(body))
            files = dict(self.files)
            files[self.archive_name] = packed.getvalue()
            rows = files["SHA256SUMS"].decode().splitlines()
            files["SHA256SUMS"] = ("\n".join(
                digest(packed.getvalue()) + "  " + self.archive_name
                if line.split()[-1].removeprefix("*") == self.archive_name else line
                for line in rows) + "\n").encode()
            with Fixture(files, self.args.version) as fixture:
                need(self.install(fixture, prefix, bindir, env).returncode != 0,
                     "unsafe_verified_archive_was_accepted")
                need("SHA256SUMS" in fixture.requests and self.archive_name in fixture.requests,
                     "unsafe_archive_fixture_was_not_downloaded")
            need((snapshot(home), snapshot(bindir), outside.read_bytes()) == before,
                 "unsafe_archive_wrote_outside_or_activated_commands")
        return {"verified_malformed_archive_cases": 4, "outside_sentinel_preserved": True}

    def unsupported(self):
        base, home, prefix, bindir, env = self.case("unsupported")
        shim = base / "shim"
        shim.mkdir(mode=0o700)
        uname = shim / "uname"
        uname.write_text("#!/bin/sh\ncase \"$1\" in -s) echo Plan9;; -m) echo arm64;; *) exit 1;; esac\n")
        uname.chmod(0o755)
        env["PATH"] = str(shim) + os.pathsep + env["PATH"]
        before = (snapshot(home), snapshot(bindir))
        with Fixture(self.files, self.args.version) as fixture:
            need(self.install(fixture, prefix, bindir, env).returncode != 0,
                 "unsupported_platform_accepted")
            need(not fixture.requests, "unsupported_platform_downloaded")
        need((snapshot(home), snapshot(bindir)) == before, "unsupported_install_mutated_files")
        return {"no_download": True}

    def concurrent(self):
        _, home, prefix, bindir, env = self.case("concurrent")
        before = snapshot(home)
        with Fixture(self.files, self.args.version) as fixture:
            actual_env = dict(env, KEELMUX_DOWNLOAD_BASE_URL=fixture.url, KEELMUX_ALLOW_TEST_HTTP="1")
            argv = ["/bin/sh", str(self.args.installer), "--version", self.args.version,
                    "--prefix", str(prefix), "--bin-dir", str(bindir)]
            children = [subprocess.Popen(argv, env=actual_env, cwd=self.state,
                                         stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                        for _ in range(2)]
            try:
                exits = [p.wait(timeout=35) for p in children]
            finally:
                for child in children:
                    if child.poll() is None:
                        child.kill()
                        child.wait(timeout=5)
            need(0 in exits, "no_concurrent_installer_completed")
            self.check_installed(prefix, bindir)
        need(snapshot(home) == before, "concurrent_install_mutated_native_files")
        return {"complete_installs": exits.count(0), "clean_refusals": len(exits)-exits.count(0)}

    def uninstall(self):
        _, home, prefix, bindir, env = self.case("uninstall")
        retained = home / ".local/state/keelmux"
        retained.mkdir(mode=0o700, parents=True)
        (retained / "registry-sentinel").write_bytes(b"retained-work\n")
        (retained / "registry-sentinel").chmod(0o600)
        native_before = snapshot(home)
        with Fixture(self.files, self.args.version) as fixture:
            need(self.install(fixture, prefix, bindir, env).returncode == 0, "uninstall_baseline_failed")
            (bindir / "bp").unlink()
            (bindir / "bp").write_bytes(b"unowned-replacement\n")
            replacement = snapshot(bindir)["bp"]
            result = self.install(fixture, prefix, bindir, env, "--uninstall")
            # A preserved collision can cause explicit refusal; it must never
            # be deleted. Matching owned commands must still be removable.
            need((bindir / "bp").exists() and snapshot(bindir)["bp"] == replacement,
                 "uninstall_deleted_unowned_replacement")
            need(snapshot(home) == native_before, "uninstall_deleted_state_or_native_files")
            (bindir / "bp").unlink()  # Our own counterfactual replacement only.
            result = self.install(fixture, prefix, bindir, env, "--uninstall")
            need(result.returncode == 0, "matching_uninstall_failed")
            need(not any(os.path.lexists(bindir / name) for name in NAMES), "owned_links_survived_uninstall")
            need(snapshot(home) == native_before, "matching_uninstall_mutated_retained_state")
            need(self.install(fixture, prefix, bindir, env).returncode == 0, "reinstall_after_uninstall_failed")
            self.check_installed(prefix, bindir)
        return {"retained_state_and_skill_archive": True, "unowned_replacement_preserved": True}

    def peer_pid(self, path):
        with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as peer:
            peer.settimeout(1)
            peer.connect(str(path))
            if platform.system() == "Darwin":
                # Pinned Darwin SOL_LOCAL/LOCAL_PEERPID, also used by the
                # reviewed installed-native ownership helper.
                return struct.unpack("i", peer.getsockopt(0, 2, 4))[0]
            need(platform.system() == "Linux", "runtime_platform_unreviewed")
            return struct.unpack("3i", peer.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12))[0]

    def process_identity(self, pid):
        env = {"PATH": "/usr/bin:/bin", "LC_ALL": "C"}
        started = subprocess.check_output(["/bin/ps", "-p", str(pid), "-o", "lstart="], env=env).strip()
        command = subprocess.check_output(["/bin/ps", "-p", str(pid), "-o", "command="], env=env).strip()
        return started, command

    def own_daemon(self, state, bindir):
        path = state / "control.sock"
        info = path.lstat()
        need(stat.S_ISSOCK(info.st_mode) and info.st_uid == os.geteuid() and
             stat.S_IMODE(info.st_mode) == 0o600, "daemon_socket_not_private")
        pid = self.peer_pid(path)
        started, command = self.process_identity(pid)
        package = (bindir / "keelmux").resolve().parent
        candidates = {str(package / n) for n in ("keelmuxd", "bpd")}
        candidates.update(str(bindir / n) for n in ("keelmuxd", "bpd"))
        need(command.decode() in {p + " --state " + str(state) for p in candidates},
             "socket_peer_not_owned_installed_daemon")
        receipt = (path, info.st_dev, info.st_ino, pid, started, command)
        self.cleanups.append(receipt)
        return receipt

    def stop_daemon(self, receipt):
        path, device, inode, pid, started, command = receipt
        info = path.lstat()
        need(info.st_dev == device and info.st_ino == inode and self.peer_pid(path) == pid,
             "daemon_cleanup_socket_proof_changed")
        need(self.process_identity(pid) == (started, command), "daemon_cleanup_process_proof_changed")
        os.kill(pid, signal.SIGTERM)
        deadline = time.monotonic() + 6
        while time.monotonic() < deadline:
            try:
                current = self.process_identity(pid)
            except subprocess.CalledProcessError:
                self.cleanups.remove(receipt)
                return
            if current != (started, command):
                self.cleanups.remove(receipt)
                return
            time.sleep(0.05)
        raise AssertionError("owned_daemon_cleanup_timed_out")

    def list_state(self, home, state, bindir, env, explicit=False, alias="keelmux"):
        argv = [str(bindir / alias)]
        if explicit:
            argv += ["--state", str(state)]
        argv += ["list"]
        receipt = None
        try:
            result = self.run(argv, env, timeout=15)
            if (state / "control.sock").exists():
                receipt = self.own_daemon(state, bindir)
            need(result.returncode == 0, "installed_daemon_cold_start_failed")
            sessions = json.loads(result.stdout)
            need(sessions is None or sessions == [], "disposable_home_has_unexpected_sessions")
            need(receipt is not None, "installed_daemon_socket_missing")
            host = (state / "host-id").read_text()
            need(bool(re.fullmatch(r"host_[a-f0-9]{32}", host)), "installed_host_identity_invalid")
            for name in ("host-id", "registry.sqlite", "daemon.lock"):
                info = (state / name).lstat()
                need(stat.S_ISREG(info.st_mode) and stat.S_IMODE(info.st_mode) == 0o600
                     and info.st_uid == os.geteuid(), "installed_state_file_not_private")
            need(stat.S_IMODE(state.stat().st_mode) == 0o700, "installed_state_directory_not_private")
            return host
        finally:
            if receipt is None and (state / "control.sock").exists():
                receipt = self.own_daemon(state, bindir)
            if receipt is not None:
                self.stop_daemon(receipt)

    def runtime_states(self):
        need(shutil.which("tmux") is not None, "runtime_tmux_prerequisite_missing")
        _, _, prefix, bindir, install_env = self.case("runtime_install")
        with Fixture(self.files, self.args.version) as fixture:
            need(self.install(fixture, prefix, bindir, install_env).returncode == 0,
                 "runtime_package_install_failed")
        scenarios = ("fresh", "legacy", "new_environment", "legacy_environment", "explicit")
        for scenario in scenarios:
            base, home, _, _, env = self.case("state_" + scenario)
            fresh = home / ".local/state/keelmux"
            legacy = home / ".local/state/agent-backplane"
            selected = fresh
            if scenario == "legacy":
                legacy.mkdir(mode=0o700, parents=True)
                (legacy / "host-id").write_text("host_" + "a" * 32)
                (legacy / "host-id").chmod(0o600)
                selected = legacy
            if scenario in ("new_environment", "explicit"):
                env["KEELMUX_STATE_DIR"] = str(base / "new-env")
                env["BP_STATE_DIR"] = str(base / "old-env")
                selected = Path(env["KEELMUX_STATE_DIR"])
            if scenario == "legacy_environment":
                env["BP_STATE_DIR"] = str(base / "old-env")
                selected = Path(env["BP_STATE_DIR"])
            if scenario == "explicit":
                selected = base / "state with space"
            sentinels = {p: p.read_bytes() for p in home.rglob("*") if p.is_file()}
            host = self.list_state(home, selected, bindir, env, scenario == "explicit")
            need(self.list_state(home, selected, bindir, env, scenario == "explicit", "bp") == host,
                 "compatibility_alias_created_second_authority")
            if scenario == "legacy":
                need(host == "host_" + "a" * 32 and not fresh.exists(), "legacy_authority_not_reused")
            elif scenario != "fresh":
                need(not fresh.exists() and not legacy.exists(), "state_override_created_default_authority")
            if scenario in ("new_environment", "explicit"):
                need(not (base / "old-env").exists(), "legacy_env_won_over_new_env")
            if scenario == "explicit":
                need(not (base / "new-env").exists(), "environment_won_over_explicit_state")
            need(all(p.read_bytes() == original for p, original in sentinels.items()),
                 "daemon_start_mutated_native_or_shell_files")
        for kind in ("malformed_host", "permissive_directory", "symlink_directory"):
            base, home, _, _, env = self.case("legacy_refuse_" + kind)
            legacy = home / ".local/state/agent-backplane"
            legacy.parent.mkdir(mode=0o700, parents=True)
            if kind == "symlink_directory":
                target = base / "unowned-target"
                target.mkdir(mode=0o700)
                (target / "sentinel").write_bytes(b"preserve\n")
                legacy.symlink_to(target)
                before = snapshot(target)
            else:
                legacy.mkdir(mode=0o700)
                (legacy / "host-id").write_text("bad" if kind == "malformed_host" else "host_" + "b"*32)
                (legacy / "host-id").chmod(0o600)
                if kind == "permissive_directory":
                    legacy.chmod(0o755)
            result = self.run([str(bindir / "keelmux"), "list"], env, timeout=15)
            for candidate in (legacy, home / ".local/state/keelmux"):
                if (candidate / "control.sock").exists():
                    receipt = self.own_daemon(candidate, bindir)
                    self.stop_daemon(receipt)
            need(result.returncode != 0, "unsafe_legacy_authority_accepted")
            need(not (home / ".local/state/keelmux").exists(), "unsafe_legacy_created_second_authority")
            if kind == "symlink_directory":
                need(snapshot(target) == before, "legacy_symlink_target_mutated")
        return {"canonical_and_legacy_cold_starts": 10, "state_precedence_cases": 5,
                "unsafe_legacy_refusals": 3, "owned_daemons_stopped": True,
                "native_processes_launched": 0}

    def execute(self):
        self.record("artifact_identity", self.artifact)
        if self.results[-1]["result"] == "pass":
            self.record("http_requires_explicit_test_optin", self.http_optin)
            self.record("fresh_install_and_reinstall", self.fresh_and_repeat)
            self.record("installation_paths_with_spaces", self.spaced_paths)
            for mode in ("checksum_mismatch", "truncated_download", "missing_checksum", "missing_archive"):
                self.record(mode, lambda mode=mode: self.bad_download(mode))
            self.record("unowned_collisions", self.collisions)
            self.record("verified_archive_boundaries", self.archive_boundaries)
            self.record("unsupported_platform", self.unsupported)
            self.record("concurrent_install", self.concurrent)
            self.record("owned_uninstall_preserves_state", self.uninstall)
            if self.args.runtime:
                self.record("installed_runtime_state_and_sibling_discovery", self.runtime_states)

    def report(self):
        sha = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=self.root, text=True).strip()
        source = subprocess.check_output(["git", "diff", "--binary", "HEAD", "--", "cmd", "internal",
                                          "go.mod", "go.sum", "Makefile", "install.sh", "scripts"], cwd=self.root)
        missing = ["installed two-provider native terminal workflow and recovery",
                   "upgrade/downgrade schema compatibility", "public release download",
                   "independent remote host", "authenticated model turns"]
        if not any(g["name"] == "installed_runtime_state_and_sibling_discovery" and g["result"] == "pass" for g in self.results):
            missing.insert(0, "installed runtime state selection and sibling daemon startup")
        return {"schema": 1, "artifact_source_sha": self.args.artifact_source_sha,
                "harness_checkout_sha": sha, "harness_checkout_implementation_diff_sha256": digest(source),
                "harness_sha256": digest(Path(__file__).read_bytes()),
                "installer_sha256": digest(self.args.installer.read_bytes()),
                "version": self.args.version, "platform": platform.platform(),
                "archive": self.archive_name, "model_turns_submitted": 0,
                "qualification": "Actual release binaries and installer with owned loopback assets and temporary HOME. Preservation uses owned sentinels, not real credentials. Optional runtime starts empty packaged daemons only; no vendor CLI or native model work.",
                "artifact_source_clean": self.manifest.get("source_clean") if self.manifest else None,
                "artifact_development_test": self.manifest.get("development_test") if self.manifest else None,
                "result": "pass" if self.results and all(r["result"] == "pass" for r in self.results) else "fail",
                "groups": self.results,
                "missing_evidence": missing}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--installer", required=True, type=Path)
    parser.add_argument("--assets", required=True, type=Path)
    parser.add_argument("--version", default="v0.1.0")
    parser.add_argument("--artifact-source-sha", required=True,
                        help="full source commit embedded in this release's version output")
    parser.add_argument("--report", required=True, type=Path)
    parser.add_argument("--runtime", action="store_true", help="also start and stop empty owned packaged daemons; needs tmux")
    args = parser.parse_args()
    need(bool(re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+", args.version)), "invalid_version")
    need(bool(re.fullmatch(r"[a-f0-9]{40}", args.artifact_source_sha)), "invalid_artifact_source_sha")
    args.installer, args.assets = args.installer.resolve(), args.assets.resolve()
    harness = Harness(args)
    try:
        harness.execute()
        report = harness.report()
        args.report.parent.mkdir(parents=True, exist_ok=True)
        args.report.write_text(json.dumps(report, indent=2) + "\n")
        print(json.dumps({"result": report["result"], "groups": len(report["groups"]),
                          "report": str(args.report), "private_diagnostics": str(harness.state)}))
        return 0 if report["result"] == "pass" else 1
    finally:
        for receipt in list(harness.cleanups):
            try:
                harness.stop_daemon(receipt)
            except (OSError, subprocess.SubprocessError, AssertionError):
                pass  # Preserve private state; do not signal without identity proof.
        # Keep private diagnostics for failures; no native processes are launched.
        if not harness.cleanups and harness.results and all(r["result"] == "pass" for r in harness.results):
            shutil.rmtree(harness.state)


if __name__ == "__main__":
    raise SystemExit(main())
