#!/usr/bin/env python3
"""Opt-in Linux runtime acceptance against a checksum-pinned release archive.

Run only inside a disposable owned Linux container with Python 3.
This tests packaged native-supervisor/daemon/CLI behavior with a raw terminal fixture. It
does not run native Codex or Claude, model turns, remote hosts, or installers.
"""
import argparse
from concurrent.futures import ThreadPoolExecutor
import fcntl
import hashlib
import http.client
import json
import os
from pathlib import Path
import platform
import pty
import re
import select
import shutil
import signal
import socket
import sqlite3
import stat
import struct
import subprocess
import sys
import tarfile
import tempfile
import termios
import threading
import time
import uuid


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def command(argv, **kwargs):
    return subprocess.run(argv, text=True, capture_output=True, check=True,
                          timeout=30, **kwargs).stdout.strip()


def wait_for(fn, timeout=15):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            result = fn()
            if result:
                return result
        except (OSError, ValueError, http.client.HTTPException):
            pass
        time.sleep(0.05)
    raise AssertionError("authored fixture condition did not become true before deadline")


def process_start(pid):
    # Linux /proc stat's second field can contain spaces and parentheses.
    try:
        text = Path(f"/proc/{pid}/stat").read_text()
        tail = text[text.rfind(")") + 2:].split()
        return (tail[19], tail[0])
    except (FileNotFoundError, ProcessLookupError):
        return None


class UnixHTTP(http.client.HTTPConnection):
    def __init__(self, path):
        super().__init__("keelmux", timeout=25)
        self.path = str(path)

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.path)


class Terminal:
    def __init__(self, argv, cwd):
        self.pid, self.fd = pty.fork()
        if self.pid == 0:
            os.chdir(cwd)
            os.environ["TERM"] = "xterm-256color"
            os.execv(argv[0], argv)
        self.output = bytearray()
        self.open = True
        self.resize(100, 34)

    def resize(self, columns, rows):
        fcntl.ioctl(self.fd, termios.TIOCSWINSZ,
                    struct.pack("HHHH", rows, columns, 0, 0))
        os.kill(self.pid, signal.SIGWINCH)

    def pump(self, seconds=0.3):
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            ready, _, _ = select.select([self.fd], [], [],
                                       max(0, min(0.1, deadline - time.monotonic())))
            if ready:
                try:
                    data = os.read(self.fd, 65536)
                except OSError:
                    break
                if not data:
                    break
                self.output.extend(data)
                if b"\x1b[6n" in data:
                    os.write(self.fd, b"\x1b[1;1R")
        return self.output.decode("utf-8", "replace")

    def send(self, data):
        os.write(self.fd, data.encode() if isinstance(data, str) else data)
        self.pump()

    def expect(self, text, timeout=15):
        return wait_for(lambda: text in self.pump(0.1), timeout)

    def close(self, crash=False):
        if not self.open:
            return
        try:
            os.kill(self.pid, signal.SIGKILL if crash else signal.SIGTERM)
        except ProcessLookupError:
            pass
        deadline = time.monotonic() + 2
        while time.monotonic() < deadline:
            try:
                if os.waitpid(self.pid, os.WNOHANG)[0]:
                    break
            except ChildProcessError:
                break
            time.sleep(0.05)
        else:
            os.kill(self.pid, signal.SIGKILL)
            os.waitpid(self.pid, 0)
        os.close(self.fd)
        self.open = False


def fixture(path):
    raw = termios.tcgetattr(0)
    raw[3] &= ~(termios.ECHO | termios.ICANON | termios.ISIG)
    raw[6][termios.VMIN], raw[6][termios.VTIME] = 1, 0
    termios.tcsetattr(0, termios.TCSANOW, raw)
    data = bytearray()

    def record(*unused):
        cols, rows = shutil.get_terminal_size()
        target = Path(path)
        temporary = target.with_suffix(".new")
        temporary.write_text(json.dumps({"hex": data.hex(), "columns": cols,
                                         "rows": rows, "cwd": os.getcwd()}))
        temporary.replace(target)

    signal.signal(signal.SIGWINCH, record)
    record()
    os.write(1, b"\x1b[?1049hKEELMUX FIXTURE READY\r\n")
    while True:
        data.extend(os.read(0, 4096))
        record()


def child_link(args):
    binary, state, receipt, keylog = args
    deadline = time.monotonic() + 12
    while time.monotonic() < deadline:
        linked = subprocess.run([binary, "--state", state, "link"], text=True,
                                capture_output=True, timeout=20)
        if linked.returncode == 0:
            value = json.loads(linked.stdout)
            Path(receipt).write_text(json.dumps({"linked": value["linked"],
                "session_id": value["id"], "execution_id": value["execution"]["id"],
                "thread_id": value["thread_id"], "workspace": os.getcwd()}))
            return fixture(keylog)
        time.sleep(0.05)
    os._exit(123)


class LinuxAcceptance:
    def __init__(self, args):
        assert platform.system() == "Linux", "Linux-only acceptance"
        assert re.fullmatch(r"[0-9a-f]{40}", args.source_sha), "exact source SHA required"
        assert re.fullmatch(r"[0-9a-f]{64}", args.archive_sha256), "archive checksum required"
        self.args = args
        self.report_path = Path(args.report).resolve()
        self.script = str(Path(__file__).resolve())
        self.temp = Path(tempfile.mkdtemp(prefix="km-l-", dir="/tmp"))
        self.state, self.workspace = self.temp / "s", self.temp / "w"
        self.package = self.temp / "p"
        self.workspace.mkdir(mode=0o700)
        self.package.mkdir(mode=0o700)
        home = self.temp / "h"
        home.mkdir(mode=0o700)
        # Do not borrow native configuration or authentication from the host.
        self.original_home = os.environ.get("HOME")
        os.environ["HOME"] = str(home)
        os.environ["TERM"] = "xterm-256color"
        os.environ["LANG"] = "C.UTF-8"
        self.daemon = None
        self.daemon_processes = []
        self.terminals = []
        self.sessions = []
        self.owned_children = {}
        self.owned_supervisors = {}
        self.runtime_receipts = {}
        self.displaced_paths = {}
        self.original_path = os.environ.get("PATH")
        tools = self.temp / "tools"
        tools.mkdir(mode=0o700)
        self.tmux_probe = self.temp / "tmux-invoked"
        denial = tools / "tmux"
        denial.write_text("#!/bin/sh\n/usr/bin/touch " + str(self.tmux_probe) + "\nexit 97\n")
        denial.chmod(0o700)
        os.environ["PATH"] = str(tools) + ":/usr/bin:/bin:/usr/sbin:/sbin"
        self.owned_daemon_instances = {}
        self.manifest = None
        self.binary = str(self.package / "keelmux")
        self.daemon_binary = str(self.package / "keelmuxd")
        self.keylog = self.temp / "keys.json"
        self.report = {
            "schema": 1, "tested_source_sha": args.source_sha,
            "archive_sha256": digest(args.archive), "harness_sha256": digest(self.script),
            "utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "platform": platform.platform(), "kernel": command(["uname", "-r"]),
            "machine": platform.machine(), "python": platform.python_version(),
            "effective_uid": os.geteuid(), "effective_gid": os.getegid(),
            "runtime": "native supervisor; tmux invocation denied and counted",
            "kind": "Linux_packaged_terminal_fixture",
            "provider_programs": "authored raw terminal fixture only",
            "model_turn_submitted": False, "native_auth_copied": False,
            "checks": [], "missing_evidence": [
                "Linux native Codex and Claude programs and authenticated model turns",
                "native skill invocation and verified account/model entitlement",
                "independent remote servers and SSH lifecycle",
                "host reboot/native conversation resume", "installer execution on Linux",
                "Linux amd64 runtime (arm64 runtime only)",
                "independent-user accessibility/speed pilot"],
        }
        if args.environment:
            self.report["container_environment"] = json.loads(Path(args.environment).read_text())

    def check(self, name, fn):
        try:
            detail = fn()
            self.report["checks"].append({"name": name, "result": "pass", "detail": detail})
            print("PASS", name, flush=True)
        except Exception as error:
            details = {"error": str(error)}
            if self.terminals:
                visible = self.terminals[-1].pump(0.1)
                details["terminal_status_phrases"] = [phrase for phrase in (
                    "local draft", "Draft saved locally", "Acquiring", "Attach refused",
                    "Saved selection is unavailable", "prior attachment", "Returned to",
                    "offline", "already held", "FIXTURE READY") if phrase in visible]
                if self.args.diagnostics:
                    private = Path(self.args.diagnostics)
                    private.mkdir(mode=0o700, parents=True, exist_ok=True)
                    screen = private / "fixture-screen.txt"
                    screen.write_text(visible)
                    screen.chmod(0o600)
            self.report["checks"].append({"name": name, "result": "fail",
                                         "detail": details})
            print("FAIL", name, str(error), flush=True)
            raise

    def provenance(self):
        assert self.report["archive_sha256"] == self.args.archive_sha256, "archive checksum differs"
        aliases = {"bp": "keelmux", "bpd": "keelmuxd", "bpg": "keelmux-entry"}
        canonical = ("keelmux", "keelmuxd", "keelmux-entry", "keelmux-supervisor")
        files = set(canonical) | {"LICENSE", "README.md", "RELEASE.json"}
        with tarfile.open(self.args.archive, "r:gz") as archive:
            members = archive.getmembers()
            assert len(members) == len(files) + len(aliases), "unexpected archive member count"
            assert {m.name for m in members} == files | set(aliases), "unexpected archive members"
            for member in members:
                assert "/" not in member.name and member.size < 200_000_000, "unsafe archive member"
                target = self.package / member.name
                if member.name in aliases:
                    assert member.issym() and member.linkname == aliases[member.name], "unsafe archive alias"
                    target.symlink_to(member.linkname)
                else:
                    assert member.isfile(), "archive contains unexpected special file"
                    target.write_bytes(archive.extractfile(member).read())
                    target.chmod(0o755 if member.name in canonical else 0o644)
        manifest = json.loads((self.package / "RELEASE.json").read_text())
        assert manifest["product"] == "Keelmux" and manifest["schema"] == 2, "unexpected release manifest"
        assert manifest["commit"] == self.args.source_sha, "manifest source SHA differs"
        assert manifest["source_clean"] and not manifest["development_test"], "unreviewed development archive"
        assert manifest["os"] == "linux" and manifest["arch"] == "arm64", "unexpected target"
        assert set(manifest["files"]) == files - {"RELEASE.json"}, "manifest file set differs"
        for name, sha in manifest["files"].items():
            assert digest(self.package / name) == sha, "archive binary or documentation digest differs"
        version = json.loads(command([self.binary, "version", "--json"]))
        assert version["commit"] == self.args.source_sha and version["version"] == manifest["version"], "executable identity differs"
        assert version["os"] == "linux" and version["arch"] == "arm64" and version["protocol"] == "bp/1", "executable runtime differs"
        for name in canonical + ("bp", "bpd", "bpg"):
            visible = command([str(self.package / name), "--version"])
            assert visible == f"Keelmux {manifest['version']} (commit {self.args.source_sha}, protocol bp/1)", "executable version differs"
        help_text = command([self.binary, "help"])
        assert "keelmux" in help_text.lower(), "release help missing product name"
        self.manifest = manifest
        self.report["release"] = manifest
        self.report["executable_buildinfo"] = version
        self.report["binary_sha256"] = {n: digest(self.package / n) for n in canonical}
        return {"checksum_pinned_archive": True, "all_binary_manifest_digests": True,
                "clean_source_manifest": True, "canonical_and_alias_versions_match": True,
                "archive_schema": 2, "internal_supervisor_digest_verified": True}

    def api(self, path, body=None, status=200, drop_response=False):
        connection = UnixHTTP(self.state / "control.sock")
        connection.request("GET" if body is None else "POST", "/v1/" + path,
            json.dumps(body) if body is not None else None,
            {"X-Backplane-Protocol": "bp/1", "Content-Type": "application/json"})
        if drop_response:
            assert connection.sock.recv(1, socket.MSG_PEEK), "authority never produced dropped reply"
            connection.sock.shutdown(socket.SHUT_RD)
            connection.close()
            return None
        reply = connection.getresponse()
        data = reply.read()
        connection.close()
        allowed = status if isinstance(status, tuple) else (status,)
        assert reply.status in allowed, f"{path}: expected HTTP {status}; got {reply.status}"
        return json.loads(data) if data else None

    def start_daemon(self):
        self.state.mkdir(mode=0o700, exist_ok=True)
        with (self.temp / "daemon.log").open("ab") as log:
            self.daemon = subprocess.Popen([self.daemon_binary, "--state", str(self.state)],
                                           stdout=log, stderr=log)
        self.daemon_processes.append(self.daemon)
        def ready():
            assert self.daemon.poll() is None, "owned packaged daemon exited before ready"
            return self.api("sessions")
        wait_for(ready)

    def restart(self):
        self.daemon.kill()
        self.daemon.wait(timeout=5)
        self.start_daemon()

    def inventory(self):
        return self.api("sessions")["sessions"]

    def private_receipt(self, ref):
        path = Path(ref["socket"]).with_suffix(".json")
        assert path.parent == self.state / "native", "runtime receipt escapes owned native state"
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        try:
            info = os.fstat(fd)
            assert stat.S_ISREG(info.st_mode) and info.st_uid == os.getuid() and stat.S_IMODE(info.st_mode) == 0o600, "runtime receipt is not private owned regular file"
            raw = os.read(fd, 2 * 1024 * 1024 + 1)
            assert len(raw) <= 2 * 1024 * 1024, "runtime receipt exceeds bound"
            receipt = json.loads(raw)
        finally:
            os.close(fd)
        assert receipt["ref"] == ref, "runtime receipt changed exact execution identity"
        return receipt

    def native_request(self, receipt, operation="inspect"):
        # Private launch correlation stays in memory/on owned 0600 files. It is
        # never passed in argv, placed in a public receipt, or printed.
        ref = receipt["ref"]
        start = process_start(receipt["supervisor_pid"])
        assert start and start[1] != "Z" and receipt["supervisor_start"] == "linux-ticks:" + start[0], "recorded supervisor lifetime changed"
        path = Path(ref["socket"])
        info = path.lstat()
        assert stat.S_ISSOCK(info.st_mode) and stat.S_IMODE(info.st_mode) == 0o600 and info.st_uid == os.getuid(), "runtime socket is not private owned Unix endpoint"
        with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as peer:
            peer.settimeout(3)
            peer.connect(str(path))
            pid, uid, unused_gid = struct.unpack("3i", peer.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12))
            assert pid == receipt["supervisor_pid"] and uid == os.getuid(), "socket does not name exact recorded owned supervisor"
            assert process_start(pid) == start, "supervisor lifetime changed during connection"
            assert digest(f"/proc/{pid}/exe") == self.manifest["files"]["keelmux-supervisor"], "live supervisor executable differs from archive"
            frame = json.dumps({"op": operation, "auth": receipt["auth"], "ref": ref}).encode()
            peer.sendall(struct.pack("!I", len(frame)) + frame)
            def exact(length):
                data = bytearray()
                while len(data) < length:
                    chunk = peer.recv(length - len(data))
                    assert chunk, "supervisor closed before complete response"
                    data.extend(chunk)
                return bytes(data)
            length = struct.unpack("!I", exact(4))[0]
            assert 0 < length <= 2 * 1024 * 1024, "supervisor response exceeds bound"
            reply = json.loads(exact(length))
        assert not reply.get("error") and reply.get("ref") == ref, "supervisor refused or changed exact runtime"
        return reply

    def native(self, session=None):
        session = session or self.oracle
        ref = session["execution"]["runtime"]
        return self.native_request(self.private_receipt(ref))

    def writer_count(self):
        return self.native().get("writable_clients", 0)

    def client_count(self, readonly):
        return sum(c["read_only"] == readonly for c in self.native().get("clients", []))

    def attached(self, terminal, readonly=False):
        for client in self.native().get("clients", []):
            if client["read_only"] != readonly:
                continue
            lifetime = process_start(client["pid"])
            if not lifetime or lifetime[1] == "Z":
                continue
            assert client["start"] == "linux-ticks:" + lifetime[0], "attached client lifetime differs"
            text = Path(f"/proc/{client['pid']}/stat").read_text()
            parent = int(text[text.rfind(")") + 2:].split()[1])
            if client["pid"] == terminal.pid or parent == terminal.pid:
                assert digest(f"/proc/{client['pid']}/exe") == self.manifest["files"]["keelmux-supervisor"], "attached executable differs from packaged helper"
                return True
        return False

    def capture_runtime(self, session):
        ref = session["execution"]["runtime"]
        assert ref["backend"] == "native" and not ref["pane"], "new runtime used compatibility backend"
        assert ref["execution_id"] == session["execution"]["id"] and ref["operation_id"], "native execution provenance absent"
        assert ref["boot_id"] == "linux:" + Path("/proc/sys/kernel/random/boot_id").read_text().strip(), "native boot identity differs"
        child = process_start(ref["pid"])
        assert child and child[1] != "Z" and ref["process_start"] == "linux-ticks:" + child[0], "native child lifetime differs"
        receipt = self.private_receipt(ref)
        self.native_request(receipt)
        self.owned_children[ref["pid"]] = child[0]
        self.owned_supervisors[receipt["supervisor_pid"]] = process_start(receipt["supervisor_pid"])[0]
        self.runtime_receipts[ref["execution_id"]] = receipt
        assert not (self.state / "tmux.sock").exists() and not self.tmux_probe.exists(), "new execution invoked tmux"

    def launch(self, name, argv, drop=False):
        request = {"operation_id": "op_" + uuid.uuid4().hex, "name": name,
            "workspace": str(self.workspace), "provider": "terminal-fixture",
            "provider_version": "authored-python-raw-oracle", "argv": argv}
        self.api("launch", request, drop_response=drop)
        reply = self.api("launch", request)
        assert reply["operation"]["outcome"] == "completed", "fixture launch did not complete"
        session = reply["session"]
        self.sessions.append(session)
        ref = session["execution"]["runtime"]
        self.capture_runtime(session)
        return session, request

    def stable(self, session):
        now = next(s for s in self.inventory() if s["id"] == session["id"])
        before = session["execution"]
        assert now["execution"]["id"] == before["id"] and now["thread_id"] == session["thread_id"], "durable identity changed"
        assert now["execution"]["runtime"] == before["runtime"] and now["execution"]["state"] == "running", "runtime identity changed"
        child = process_start(before["runtime"]["pid"])
        assert child and child[1] != "Z" and child[0] == self.owned_children[before["runtime"]["pid"]], "owned child lifetime changed"
        assert self.native(session).get("alive"), "exact native supervisor no longer owns live execution"
        return now

    def lease(self, session, client="linux-acceptance", readonly=False):
        return {"session_id": session["id"], "execution_id": session["execution"]["id"],
            "generation": session["execution"]["generation"], "operation_id": "attach_" + uuid.uuid4().hex,
            "client_id": client, "read_only": readonly}

    def terminal(self, argv):
        terminal = Terminal(argv, str(self.workspace))
        self.terminals.append(terminal)
        return terminal

    def ui(self):
        terminal = self.terminal([self.binary, "--state", str(self.state)])
        terminal.expect("Keelmux / ")
        terminal.expect("Model:")
        return terminal

    def place(self, name="place.json"):
        return json.loads((self.state / name).read_text())

    def detach(self, terminal):
        terminal.send(b"\x07")
        terminal.send(b"d")
        wait_for(lambda: self.writer_count() == 0)

    def launch_link(self):
        self.start_daemon()
        receipt = self.temp / "link.json"
        session, request = self.launch("Linux key oracle", [sys.executable, self.script,
            "--child", self.binary, str(self.state), str(receipt), str(self.keylog)], drop=True)
        link = wait_for(lambda: json.loads(receipt.read_text()) if receipt.exists() else None)
        assert link["linked"] and link["session_id"] == session["id"] and link["execution_id"] == session["execution"]["id"], "inherited owned link identity differs"
        assert link["workspace"] == str(self.workspace), "managed workspace differs"
        assert not session["capabilities"]["semantic_prompt"], "fixture falsely advertises semantic model capability"
        self.api("link", {"session_id": session["id"], "execution_id": session["execution"]["id"],
                          "operation_id": request["operation_id"], "token": "forged"}, status=409)
        self.oracle, self.launch_request = session, request
        self.restart()
        self.api("launch", request)
        assert len(self.inventory()) == 1, "dropped launch reply duplicated work"
        self.stable(session)
        assert command([self.binary, "--state", str(self.state), "inspect", session["id"]]), "packaged inspect failed"
        return {"actual_packaged_daemon_and_native_supervisor": True, "exact_inherited_link": True,
            "forged_link_refused": True, "dropped_reply_retry_exact_identity": True,
            "same_session_thread_execution_child_and_supervisor_after_daemon_SIGKILL": True}

    def controls(self):
        terminal = self.terminal([self.binary, "--state", str(self.state), "attach", self.oracle["id"]])
        terminal.expect("KEELMUX FIXTURE READY")
        wait_for(lambda: self.attached(terminal))
        terminal.send("café λ 🙂")
        terminal.send(b"\x1b")
        terminal.pump(0.7)
        terminal.send(b"\x03")
        terminal.send(b"\x07")
        terminal.send(b"\x07")
        terminal.resize(76, 23)
        wait_for(lambda: (json.loads(self.keylog.read_text())["columns"], json.loads(self.keylog.read_text())["rows"]) == (76, 23))
        raw = bytes.fromhex(json.loads(self.keylog.read_text())["hex"])
        assert "café λ 🙂".encode() in raw and all(key in raw for key in (b"\x1b", b"\x03", b"\x07")), "raw terminal controls differ"
        readonly = self.lease(self.oracle, "readonly", True)
        grant = self.api("attach", readonly)
        viewer = self.terminal(grant["argv"])
        wait_for(lambda: self.attached(viewer, True))
        viewer.resize(20, 10)
        viewer.send("READONLY-CANARY")
        viewer.pump(0.7)
        assert b"READONLY-CANARY" not in bytes.fromhex(json.loads(self.keylog.read_text())["hex"]), "readonly viewer injected input"
        assert (json.loads(self.keylog.read_text())["columns"], json.loads(self.keylog.read_text())["rows"]) == (76, 23), "readonly viewer resized writer"
        viewer.send(b"\x07")
        viewer.send(b"d")
        wait_for(lambda: self.client_count(True) == 0)
        self.api("release", dict(readonly, token=grant["lease"]["token"]), status=204)
        viewer.close()
        self.detach(terminal)
        wait_for(lambda: not self.place("cli-place.json").get("pending_attachment"))
        terminal.close()
        reconnect = self.terminal([self.binary, "--state", str(self.state), "attach", self.oracle["id"]])
        reconnect.expect("KEELMUX FIXTURE READY")
        self.stable(self.oracle)
        self.detach(reconnect)
        wait_for(lambda: not self.place("cli-place.json").get("pending_attachment"))
        reconnect.close()
        return {"actual_cli_attach_detach_reconnect": True, "raw_Unicode_Escape_CtrlC_literal_prefix": True,
                "resize": "76x23", "readonly_input_and_writer_size_preserved": True}

    def tui_recovery(self):
        terminal = self.ui()
        terminal.send("p")
        terminal.send("Linux saved draft café λ")
        terminal.send(b"\x1b")
        terminal.expect("Draft saved locally")
        terminal.send("\r")
        wait_for(lambda: self.attached(terminal))
        self.restart()
        self.stable(self.oracle)
        self.api("attach", self.lease(self.oracle, "restart-competitor"), status=409)
        terminal.close(crash=True)
        wait_for(lambda: self.writer_count() == 0)
        recovered = self.ui()
        wait_for(lambda: not self.place().get("pending_attachment"))
        assert self.place()["places"][""]["session_id"] == self.oracle["id"], "TUI selected identity lost"
        assert any(d["text"] == "Linux saved draft café λ" for d in self.place()["drafts"]), "local TUI draft lost"
        recovered.send("\r")
        wait_for(lambda: self.attached(recovered))
        self.stable(self.oracle)
        self.detach(recovered)
        recovered.expect("Returned to the same work and group")
        wait_for(lambda: not self.place().get("pending_attachment"))
        recovered.send("q")
        recovered.close()
        return {"actual_packaged_TUI": True, "daemon_SIGKILL_while_attached": True,
                "TUI_SIGKILL_and_saved_grant_release": True,
                "same_selected_work_and_local_draft_after_reconnect": True}

    def arbitration(self):
        request = self.lease(self.oracle)
        self.api("attach", request, drop_response=True)
        grant = self.api("attach", request)
        self.restart()
        assert self.api("attach", request)["lease"] == grant["lease"], "lost attach reply changed durable grant"
        self.api("attach", self.lease(self.oracle, "competitor"), status=409)
        self.api("attach", dict(request, client_id="forged-replay"), status=409)
        release = dict(request, token=grant["lease"]["token"])
        self.api("release", dict(release, token="forged"), status=409)
        self.api("release", release, status=204)
        self.api("release", release, status=204)
        self.api("attach", request, status=409)
        contenders = [self.lease(self.oracle, "race-" + str(n)) for n in range(2)]
        barrier = threading.Barrier(2)
        def compete(candidate):
            barrier.wait(timeout=5)
            return candidate, self.api("attach", candidate, status=(200, 409))
        with ThreadPoolExecutor(max_workers=2) as pool:
            replies = [f.result(timeout=30) for f in [pool.submit(compete, r) for r in contenders]]
        winners = [(r, g) for r, g in replies if "lease" in g]
        assert len(winners) == 1, "writer race granted other than one cooperating writer"
        winner, result = winners[0]
        self.api("release", dict(winner, token=result["lease"]["token"]), status=204)
        return {"dropped_attach_reply_and_restart_exact_grant": True,
                "forged_release_and_reused_retired_operation_refused": True,
                "concurrent_contenders": 2, "writer_grants": 1}

    def stop_recovery(self):
        session, _ = self.launch("stop fixture", [sys.executable, self.script, "--fixture", str(self.temp / "stop.json")])
        request = {"session_id": session["id"], "execution_id": session["execution"]["id"],
            "generation": session["execution"]["generation"], "operation_id": "stop_" + uuid.uuid4().hex}
        self.api("stop", dict(request, generation=request["generation"] + 1), status=409)
        self.api("stop", request, status=204, drop_response=True)
        self.api("stop", request, status=204)
        self.restart()
        self.api("stop", request, status=204)
        self.api("stop", dict(request, execution_id="execution_conflict"), status=409)
        stopped = next(s for s in self.inventory() if s["id"] == session["id"])
        assert stopped["execution"]["state"] == "stopped", "confirmed stop changed after restart"
        return {"stale_generation_refused": True, "dropped_stop_reply_idempotent_across_restart": True,
                "operation_target_conflict_refused": True}

    def durability(self):
        lifecycle = self.daemon_controls() if self.args.daemon_lifecycle else {"daemon_lifecycle_commands": "not_in_consumed_archive"}
        host = (self.state / "host-id").read_text()
        assert host == self.oracle["host_id"], "host identity differs"
        duplicate = subprocess.run([self.daemon_binary, "--state", str(self.state)],
            capture_output=True, text=True, timeout=15)
        assert duplicate.returncode != 0 and "already owns" in duplicate.stderr, "second daemon accepted same state"
        self.stable(self.oracle)
        with sqlite3.connect(self.state / "registry.sqlite") as database:
            rows = database.execute("SELECT thread_id,sequence,data FROM events ORDER BY thread_id,sequence").fetchall()
        per_thread, kinds = {}, set()
        for thread, sequence, raw in rows:
            text = raw.decode() if isinstance(raw, bytes) else raw
            value = json.loads(text)
            assert value["thread_id"] == thread and value["sequence"] == sequence, "journal identity projection differs"
            assert "BP_LINK_TOKEN" not in text and "link_token" not in text, "private link correlation leaked"
            per_thread.setdefault(thread, []).append(sequence)
            kinds.add(value["kind"])
        assert all(values == list(range(1, len(values) + 1)) for values in per_thread.values()), "journal sequence has gaps"
        assert {"launch.recorded", "launch.attempted", "launch.completed", "link.on", "attach.granted", "attach.released"} <= kinds, "journal omitted lifecycle outcome"
        for filename in ("registry.sqlite", "host-id", "daemon.lock", "place.json", "cli-place.json"):
            assert (self.state / filename).stat().st_mode & 0o777 == 0o600, "authority or client state is not private"
        assert self.state.stat().st_mode & 0o777 == 0o700, "state directory is not private"
        assert not (Path(os.environ["HOME"]) / ".codex").exists(), "native realm appeared in fixture HOME"
        return {"persisted_host_identity": True, "second_daemon_refused": True,
                "events": len(rows), "threads": len(per_thread), "contiguous_per_thread": True,
                "private_authority_and_client_state_modes": True, "native_auth_realm_untouched": True,
                **lifecycle}

    def daemon_status(self):
        reply = json.loads(command([self.binary, "--state", str(self.state), "daemon", "status"]))
        assert reply["running"], "packaged daemon status says not running"
        status = reply["daemon"]
        assert status["protocol"] == "bp/1" and status["build"]["commit"] == self.args.source_sha, "daemon status identity or build differs"
        lifetime = process_start(status["pid"])
        assert lifetime and lifetime[1] != "Z", "daemon status names absent process"
        self.owned_daemon_instances[status["instance"]] = (status["pid"], lifetime[0])
        return status

    def daemon_controls(self):
        absent = self.temp / "absent"
        status = json.loads(command([self.binary, "--state", str(absent), "daemon", "status"]))
        assert status == {"running": False} and not absent.exists(), "status created a daemon or private state"
        request = self.lease(self.oracle, "daemon-lifecycle-writer")
        grant = self.api("attach", request)
        before = self.daemon_status()
        refused = subprocess.run([self.binary, "--state", str(self.state), "daemon", "stop", "--instance", "daemon_forged"],
                                 capture_output=True, text=True, timeout=15)
        assert refused.returncode != 0 and self.daemon_status()["instance"] == before["instance"], "forged daemon instance stopped current daemon"
        command([self.binary, "--state", str(self.state), "daemon", "stop", "--instance", before["instance"]])
        self.daemon.wait(timeout=5)
        stopped = json.loads(command([self.binary, "--state", str(self.state), "daemon", "status"]))
        assert stopped == {"running": False}, "explicit daemon stop left authority running"
        ref = self.oracle["execution"]["runtime"]
        assert process_start(ref["pid"])[0] == self.owned_children[ref["pid"]], "daemon stop changed native fixture process"
        command([self.binary, "--state", str(self.state), "daemon", "restart"])
        current = self.daemon_status()
        assert current["instance"] != before["instance"], "restart reused daemon incarnation"
        self.stable(self.oracle)
        assert self.api("attach", request)["lease"] == grant["lease"], "daemon restart changed exact saved writer grant"
        stale = subprocess.run([self.binary, "--state", str(self.state), "daemon", "stop", "--instance", before["instance"]],
                               capture_output=True, text=True, timeout=15)
        assert stale.returncode != 0 and self.daemon_status()["instance"] == current["instance"], "stale stop intent affected replacement"
        for _ in range(10):
            command([self.binary, "--state", str(self.state), "daemon", "restart", "--instance", current["instance"]])
            replacement = self.daemon_status()
            assert replacement["instance"] != current["instance"], "restart reused current daemon incarnation"
            current = replacement
            self.stable(self.oracle)
            assert self.api("attach", request)["lease"] == grant["lease"], "restart stress replaced durable writer grant"
        command([self.binary, "--state", str(self.state), "daemon", "stop", "--instance", current["instance"]])
        self.start_daemon()
        self.stable(self.oracle)
        assert self.api("attach", request)["lease"] == grant["lease"], "explicit daemon startup changed durable grant"
        self.api("release", dict(request, token=grant["lease"]["token"]), status=204)
        return {"daemon_status_without_autostart": True, "forged_and_stale_instance_stop_refused": True,
                "daemon_stop_preserves_native_fixture": True, "packaged_daemon_restart_count": 11,
                "restart_preserves_exact_work_runtime_and_writer_grant": True}

    def runtime_loss(self):
        ref = self.oracle["execution"]["runtime"]
        receipt = self.runtime_receipts[ref["execution_id"]]
        counts = (len(self.inventory()), len(list((self.state / "native").glob("*.json"))))
        observations = []
        # Remove availability, not the owned processes. Restore each exact inode
        # in finally so normal authenticated product cleanup remains possible.
        # Neither persisted PID nor process-group signalling is a recovery tool.
        for kind, original in (("socket", Path(ref["socket"])),
                               ("authority_receipt", Path(ref["socket"]).with_suffix(".json"))):
            moved = original.with_name(original.name + ".acceptance-held")
            identity = (original.lstat().st_dev, original.lstat().st_ino)
            original.rename(moved)
            self.displaced_paths[original] = (moved, identity)
            try:
                self.api("reconcile", {}, status=204)
                unavailable = next(s for s in self.inventory() if s["id"] == self.oracle["id"])
                assert unavailable["execution"]["state"] == "runtime_unavailable", "unknown native runtime was asserted dead or replaced"
                assert unavailable["execution"]["runtime"] == ref, "unavailable projection changed exact runtime"
                self.api("attach", self.lease(self.oracle, "unavailable-" + kind), status=409)
                self.api("launch", self.launch_request)
                self.restart()
                unavailable = next(s for s in self.inventory() if s["id"] == self.oracle["id"])
                assert unavailable["execution"]["state"] == "runtime_unavailable" and unavailable["execution"]["runtime"] == ref, "daemon recovery replaced unknown execution"
                child = process_start(ref["pid"])
                helper = process_start(receipt["supervisor_pid"])
                assert child and child[0] == self.owned_children[ref["pid"]] and child[1] != "Z", "loss fixture changed live child"
                assert helper and helper[0] == self.owned_supervisors[receipt["supervisor_pid"]] and helper[1] != "Z", "loss fixture changed supervisor lifetime"
                assert (len(self.inventory()), len(list((self.state / "native").glob("*.json"))) + (kind == "authority_receipt")) == counts, "loss triggered replacement launch"
                observations.append(kind)
            finally:
                assert not os.path.lexists(original), "owned loss path was unexpectedly recreated"
                assert (moved.lstat().st_dev, moved.lstat().st_ino) == identity, "displaced owned inode changed"
                moved.rename(original)
                del self.displaced_paths[original]
            self.api("reconcile", {}, status=204)
            self.stable(self.oracle)
        assert not self.tmux_probe.exists(), "native runtime invoked tmux"
        return {"owned_unavailable_paths": observations, "unavailable_is_unknown_not_dead": True,
                "launch_retry_and_daemon_restart_do_not_replay": True,
                "restored_exact_authority_reconciles_same_live_execution": True,
                "tmux_invocations": 0}

    def cleanup(self):
        errors = []
        for terminal in self.terminals:
            terminal.close()
        if self.owned_daemon_instances:
            try:
                current = self.api("daemon")
                if current["instance"] in self.owned_daemon_instances:
                    pid, start = self.owned_daemon_instances[current["instance"]]
                    lifetime = process_start(pid)
                    assert current["pid"] == pid and lifetime and lifetime[0] == start and lifetime[1] != "Z", "daemon cleanup lifetime differs"
                    self.api("daemon/stop", {"instance": current["instance"]}, status=204)
            except (OSError, http.client.HTTPException, AssertionError):
                # The foreground handle below can still terminate its own
                # process. Replacement daemons are only stopped by exact API.
                if self.daemon is None or self.daemon.poll() is not None:
                    errors.append("replacement daemon exact cleanup unavailable")
        if self.daemon and self.daemon.poll() is None:
            self.daemon.terminate()
            try:
                self.daemon.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.daemon.kill()
                self.daemon.wait(timeout=5)
        # Stop each owned runtime through its authenticated, peer-verified
        # supervisor. No signals are sent to a saved child/helper PID.
        for execution, receipt in self.runtime_receipts.items():
            pid = receipt["supervisor_pid"]
            current = process_start(pid)
            if current and current[0] == self.owned_supervisors[pid] and current[1] != "Z":
                try:
                    self.native_request(receipt, "stop")
                except Exception:
                    errors.append("exact owned supervisor cleanup refused")
        deadline = time.monotonic() + 4
        while time.monotonic() < deadline:
            live = [pid for pid, start in (self.owned_children | self.owned_supervisors).items()
                    if (current := process_start(pid)) and current[0] == start and current[1] != "Z"]
            if not live:
                break
            time.sleep(0.05)
        for pid, start in self.owned_children.items():
            current = process_start(pid)
            if current and current[0] == start and current[1] != "Z":
                errors.append("owned direct child remains live after cleanup")
        for pid, start in self.owned_supervisors.items():
            current = process_start(pid)
            if current and current[0] == start and current[1] != "Z":
                errors.append("owned supervisor remains live after cleanup")
        if self.displaced_paths:
            errors.append("owned unavailable path remains displaced")
        native_dir = self.state / "native"
        # A failed first launch may have created a helper before an acknowledged
        # runtime could be captured. Retain state rather than claim empty cleanup.
        for path in native_dir.glob("*.json"):
            try:
                receipt = json.loads(path.read_text())
                if receipt.get("ref", {}).get("execution_id") not in self.runtime_receipts:
                    errors.append("uncaptured owned runtime receipt retained")
            except (OSError, ValueError):
                errors.append("unverified owned runtime state retained")
        if self.tmux_probe.exists():
            errors.append("forbidden tmux invocation observed")
        for pid, start in self.owned_daemon_instances.values():
            current = process_start(pid)
            if current and current[0] == start and current[1] != "Z":
                errors.append("owned daemon incarnation remains live after cleanup")
        if any(p.poll() is None for p in self.daemon_processes):
            errors.append("owned daemon remains live after cleanup")
        if self.original_home is not None:
            os.environ["HOME"] = self.original_home
        else:
            os.environ.pop("HOME", None)
        if self.original_path is not None:
            os.environ["PATH"] = self.original_path
        else:
            os.environ.pop("PATH", None)
        if not errors:
            shutil.rmtree(self.temp)
        self.report["cleanup"] = {"owned_terminal_clients_reaped": all(not t.open for t in self.terminals),
            "owned_daemons_reaped": all(p.poll() is not None for p in self.daemon_processes),
            "owned_children_and_supervisors_not_live": not errors,
            "native_stop_uses_verified_socket_authority": bool(self.runtime_receipts) and not errors,
            "owned_temp_removed": not self.temp.exists(), "errors": errors}
        assert not errors, "owned runtime cleanup failed"

    def run(self):
        try:
            self.check("release archive provenance and executable identity", self.provenance)
            self.check("owned launch/link and dropped launch acknowledgement", self.launch_link)
            self.check("CLI raw controls, readonly view, detach and reconnect", self.controls)
            self.check("TUI and daemon crash recovery with saved place and draft", self.tui_recovery)
            self.check("lost attach acknowledgement and one cooperating writer", self.arbitration)
            self.check("lost stop acknowledgement and stale generation", self.stop_recovery)
            self.check("durable private host registry and journal", self.durability)
            self.check("native authority/socket loss is unknown and never replayed", self.runtime_loss)
        finally:
            try:
                self.cleanup()
            finally:
                self.report_path.parent.mkdir(parents=True, exist_ok=True)
                self.report["result"] = "pass" if len(self.report["checks"]) == 8 and all(c["result"] == "pass" for c in self.report["checks"]) and not self.report.get("cleanup", {}).get("errors", ["missing"]) else "fail"
                self.report_path.write_text(json.dumps(self.report, indent=2) + "\n")


def main():
    if len(sys.argv) > 1 and sys.argv[1] == "--fixture":
        return fixture(sys.argv[2])
    if len(sys.argv) > 1 and sys.argv[1] == "--child":
        return child_link(sys.argv[2:])
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--archive", required=True)
    parser.add_argument("--archive-sha256", required=True)
    parser.add_argument("--source-sha", required=True)
    parser.add_argument("--environment", help="captain-supplied owned Docker environment metadata")
    parser.add_argument("--diagnostics", help="private fixture-only failure screen directory")
    parser.add_argument("--daemon-lifecycle", action="store_true", help="require packaged daemon status/stop/restart controls")
    parser.add_argument("--report", required=True)
    args = parser.parse_args()
    LinuxAcceptance(args).run()


if __name__ == "__main__":
    main()
