#!/usr/bin/env python3
"""Opt-in installed native terminal proof. Never builds product source or submits a model prompt."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import socket
import stat
import struct
import subprocess
import tempfile
import time

from native_acceptance import Acceptance, Terminal, command, wait_for
from native_gateway import NativeGateway


ALIASES = {"bp": "keelmux", "bpd": "keelmuxd", "bpg": "keelmux-entry"}


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def verify_package(directory, source):
    directory = directory.resolve()
    manifest = json.loads((directory / "RELEASE.json").read_bytes())
    target = {"arm64": "arm64", "aarch64": "arm64", "x86_64": "amd64"}[platform.machine().lower()]
    assert manifest["schema"] == 1 and manifest["product"] == "Keelmux", "installed manifest identity differs"
    assert manifest["commit"] == source and re.fullmatch(r"v\d+\.\d+\.\d+", manifest["version"]), "installed source/version differs"
    assert manifest["source_clean"] is True and manifest["development_test"] is False, "installed package is not a clean release artifact"
    assert manifest["os"] == platform.system().lower() and manifest["arch"] == target, "installed package target differs"
    assert manifest["aliases"] == ALIASES, "installed alias manifest differs"
    hashes = {}
    assert set(manifest["files"]) == {"keelmux", "keelmuxd", "keelmux-entry", "LICENSE", "README.md"}, "installed file manifest differs"
    for name, expected in manifest["files"].items():
        path = directory / name
        info = path.lstat()
        assert stat.S_ISREG(info.st_mode) and not path.is_symlink(), "installed canonical file is not regular"
        assert sha(path) == expected, "installed canonical digest differs"
        hashes[name] = expected
    for alias, canonical in ALIASES.items():
        path = directory / alias
        assert path.is_symlink() and os.readlink(path) == canonical, "installed alias target differs"
    metadata = json.loads(command([str(directory / "keelmux"), "version", "--json"]))
    assert metadata["name"] == "Keelmux" and metadata["protocol"] == "bp/1", "installed CLI product/protocol differs"
    for field in ("version", "commit", "built_at", "os", "arch", "go_version"):
        assert metadata[field] == manifest[field], "installed CLI provenance differs"
    return directory, {"source_sha": source, "version": manifest["version"],
                       "manifest_sha256": sha(directory / "RELEASE.json"),
                       "installed_file_sha256": hashes, "clean_release_source": True}


class PackageHome(Acceptance):
    def __init__(self, root, report, workspace, package, identity):
        super().__init__(root, report, workspace)
        self.bp, self.bpd = str(package / "keelmux"), str(package / "keelmuxd")
        self.report["harness_checkout_sha"] = self.report.pop("tested_sha")
        self.report["imported_core_harness_sha256"] = self.report["harness_sha256"]
        self.report["harness_sha256"] = sha(Path(__file__).resolve())
        self.report["installed_artifact"] = identity
        self.report["execution_source"] = "Installed canonical release binaries; no product build is invoked."
        self.tmux_receipt = None

    def launch(self, *args, **kwargs):
        reply = super().launch(*args, **kwargs)
        if self.tmux_receipt is None:
            path = self.state / "tmux.sock"
            info = path.lstat()
            with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as peer:
                peer.connect(str(path))
                if platform.system() == "Darwin":
                    pid = struct.unpack("i", peer.getsockopt(0, 2, 4))[0]
                else:
                    pid = struct.unpack("3i", peer.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12))[0]
            assert int(self.tmux("display-message", "-p", "#{pid}")) == pid, "owned tmux peer/renderer PID differs"
            started = command(["/bin/ps", "-p", str(pid), "-o", "lstart="])
            self.tmux_receipt = (path, info.st_dev, info.st_ino, pid, started)
        return reply

    def ui(self):
        terminal = Terminal([self.bp, "--state", str(self.state)], str(self.workspace))
        self.terminals.append(terminal)
        wait_for(lambda: any(title in terminal.pump(0.1) for title in ("BACKPLANE", "KEELMUX", "Keelmux")))
        terminal.expect("Model:")
        return terminal

    def check(self, name, function, kind="integrated"):
        try:
            detail = function()
            self.report["checks"].append({"name": name, "kind": kind, "result": "pass", "detail": detail})
        except Exception as error:
            private = self.temp / "failure.txt"
            private.write_text(str(error))
            private.chmod(0o600)
            # Arbitrary subprocess/native errors are private. Assertions in the
            # imported tests are authored conditions rather than native text.
            tag = str(error) if isinstance(error, AssertionError) else type(error).__name__
            self.report["checks"].append({"name": name, "kind": kind, "result": "fail", "failure": tag})
            raise

    def cleanup(self, embedded=False):
        issues = []
        for n, terminal in enumerate(self.terminals):
            path = self.temp / ("terminal-%d.bin" % n)
            path.write_bytes(terminal.output)
            path.chmod(0o600)
            terminal.close()
        self.terminals.clear()
        if self.daemon and self.daemon.poll() is None:
            self.daemon.terminate()
            try:
                self.daemon.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.daemon.kill()
                self.daemon.wait(timeout=5)
        if self.tmux_receipt:
            path, device, inode, pid, started = self.tmux_receipt
            try:
                current = command(["/bin/ps", "-p", str(pid), "-o", "lstart="])
                if command(["/bin/ps", "-p", str(pid), "-o", "stat="]).startswith("Z"):
                    current = None  # Recorded process is dead, awaiting reaping.
            except subprocess.CalledProcessError:
                current = None
            if current == started:
                info = path.lstat()
                if (info.st_dev, info.st_ino) == (device, inode) and int(self.tmux("display-message", "-p", "#{pid}")) == pid:
                    self.tmux("kill-server")
                else:
                    issues.append("tmux_cleanup_identity_changed")
            elif current is not None:
                issues.append("tmux_cleanup_process_changed")
        self.report["cleanup_issues"] = issues
        checked = bool(self.report["checks"]) or embedded
        self.report["qualification"] = ("Embedded execution home; native journey verdict is in the gateway receipt."
                                        if embedded else "Installed binaries with actual native terminal programs; no model prompt submitted.")
        self.report["result"] = "pass" if not issues and checked and all(c["result"] == "pass" for c in self.report["checks"]) else "fail"
        self.report_path.parent.mkdir(parents=True, exist_ok=True)
        self.report_path.write_text(json.dumps(self.report, indent=2) + "\n")
        if self.report["result"] == "pass":
            shutil.rmtree(self.temp)

    def run(self):
        try:
            self.start_daemon()
            for provider in ("codex", "claude"):
                self.check(provider+" native launch/link/TUI/reconnect/restart", lambda p=provider: self.native_journey(p), "native_program")
            self.check("lost attach acknowledgement and writer arbitration", self.lost_attach_ack)
            self.check("raw terminal keys and ten-session grouped place/drafts", self.fixtures_and_place, "fixture")
            self.check("lost stop acknowledgement and stale generation", self.lost_stop_ack)
            self.check("native Ctrl-C behavior separate from uninterrupted recovery", self.native_interrupts, "native_program")
            self.check("durable registry/journal", self.journal)
            self.check("tmux loss", self.tmux_loss)
        finally:
            self.cleanup()


class PackageGateway(NativeGateway):
    def __init__(self, root, report, workspace, package, identity):
        # Construct with verified installed binaries directly. The legacy base
        # constructor hashes root/bin, which could be a source-build substitute.
        self.root = Path(root).resolve()
        self.report_path = Path(report)
        self.temp = Path(tempfile.mkdtemp(prefix="km-ng-", dir="/tmp"))
        self.temp.chmod(0o700)
        self.entry, self.client = self.temp / "e", self.temp / "c"
        self.configuration = self.temp / "peers.json"
        self.socket = self.entry / "control.sock"
        self.bp, self.bpg = str(package / "keelmux"), str(package / "keelmux-entry")
        self.homes = [PackageHome(root, self.temp / ("home-%d.json" % n), workspace, package, identity) for n in range(2)]
        self.gateway = None
        self.terminals, self.work = [], []
        self.only = "all"
        self.report = {"schema": 1, "harness_checkout_sha": command(["git", "rev-parse", "HEAD"], cwd=root),
                       "installed_artifact": identity, "harness_sha256": sha(Path(__file__).resolve()),
                       "imported_harness_sha256": {name: sha(Path(__file__).parent / name) for name in ("native_acceptance.py", "native_gateway.py", "gateway_acceptance.py")},
                       "utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                       "platform": platform.platform(), "tmux": command(["tmux", "-V"]),
                       "providers": self.homes[0].report["providers"], "checks": [],
                       "kind": "installed_native_programs_through_same_computer_executable_gateway",
                       "model_prompt_submitted": False, "native_enter_submitted": False,
                       "execution_source": "Installed canonical release binaries; no product build is invoked.",
                       "missing_evidence": ["independent remote hosts and enrollment/authentication", "native skill loading/invocation", "authenticated model turns and verified account/model", "host reboot/native resume", "upgrade/downgrade schema compatibility"]}

    def ui(self):
        terminal = Terminal([self.bp, "--state", str(self.client), "--entry", str(self.socket)], str(self.homes[0].workspace))
        self.terminals.append(terminal)
        wait_for(lambda: any(title in terminal.pump(0.1) for title in ("BACKPLANE", "KEELMUX", "Keelmux")))
        terminal.expect("Model:")
        return terminal

    def check(self, name, function):
        PackageHome.check(self, name, function, "native_program")

    def run(self):
        try:
            self.check("actual native homes through installed executable entry", self.prepare_native)
            for n in range(2):
                self.check(("codex", "claude")[n]+" installed native gateway attach/detach/reconnect", lambda n=n: self.native_journey(n))
        finally:
            for n, terminal in enumerate(self.terminals):
                path = self.temp / ("terminal-%d.bin" % n)
                path.write_bytes(terminal.output)
                path.chmod(0o600)
                terminal.close()
            if self.gateway and self.gateway.poll() is None:
                self.gateway.terminate()
                try:
                    self.gateway.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    self.gateway.kill()
                    self.gateway.wait(timeout=5)
            for home in self.homes:
                home.cleanup(embedded=True)
            issues = [issue for home in self.homes for issue in home.report.get("cleanup_issues", [])]
            self.report["cleanup_issues"] = issues
            self.report["result"] = "pass" if not issues and self.report["checks"] and all(c["result"] == "pass" for c in self.report["checks"]) else "fail"
            self.report_path.parent.mkdir(parents=True, exist_ok=True)
            self.report_path.write_text(json.dumps(self.report, indent=2) + "\n")
            if self.report["result"] == "pass":
                shutil.rmtree(self.temp)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", default=str(Path(__file__).resolve().parents[1]))
    parser.add_argument("--package-dir", required=True, type=Path, help="verified installer immutable version directory containing RELEASE.json")
    parser.add_argument("--artifact-source-sha", required=True)
    parser.add_argument("--native-workspace", required=True, type=Path, help="existing owned repository already trusted by both native CLIs")
    parser.add_argument("--journey", required=True, choices=("core", "gateway"))
    parser.add_argument("--report", required=True, type=Path)
    args = parser.parse_args()
    assert re.fullmatch(r"[a-f0-9]{40}", args.artifact_source_sha), "full artifact source SHA required"
    package, identity = verify_package(args.package_dir, args.artifact_source_sha)
    assert args.native_workspace.is_dir(), "trusted native workspace must exist"
    harness_type = PackageHome if args.journey == "core" else PackageGateway
    harness = harness_type(args.root, args.report, args.native_workspace.resolve(), package, identity)
    try:
        harness.run()
    except Exception:
        print(json.dumps({"result": "fail", "report": str(args.report), "private_diagnostics": str(harness.temp)}))
        return 1
    print(json.dumps({"result": harness.report["result"], "report": str(args.report)}))
    return 0 if harness.report["result"] == "pass" else 1


if __name__ == "__main__":
    raise SystemExit(main())
