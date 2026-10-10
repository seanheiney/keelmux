#!/usr/bin/env python3
"""Opt-in Linux runtime acceptance against a checksum-pinned release archive.

Run only inside a disposable owned Linux container with tmux and Python 3.
This tests packaged daemon/CLI/tmux behavior with a raw terminal fixture. It
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
        self.owned_panes = {}
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
            "tmux": command(["tmux", "-V"]),
            "packages": command(["dpkg-query", "-W", "-f=${Package} ${Version}\n",
                                   "tmux", "python3", "lsof", "procps"]),
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
        files = {"keelmux", "keelmuxd", "keelmux-entry", "LICENSE", "README.md", "RELEASE.json"}
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
                    target.chmod(0o755 if member.name in ("keelmux", "keelmuxd", "keelmux-entry") else 0o644)
        manifest = json.loads((self.package / "RELEASE.json").read_text())
        assert manifest["product"] == "Keelmux" and manifest["schema"] == 1, "unexpected release manifest"
        assert manifest["commit"] == self.args.source_sha, "manifest source SHA differs"
        assert manifest["source_clean"] and not manifest["development_test"], "unreviewed development archive"
        assert manifest["os"] == "linux" and manifest["arch"] == "arm64", "unexpected target"
        assert set(manifest["files"]) == files - {"RELEASE.json"}, "manifest file set differs"
        for name, sha in manifest["files"].items():
            assert digest(self.package / name) == sha, "archive binary or documentation digest differs"
        version = json.loads(command([self.binary, "version", "--json"]))
        assert version["commit"] == self.args.source_sha and version["version"] == manifest["version"], "executable identity differs"
        assert version["os"] == "linux" and version["arch"] == "arm64" and version["protocol"] == "bp/1", "executable runtime differs"
        for name in ("keelmuxd", "keelmux-entry", "bp", "bpd", "bpg"):
            visible = command([str(self.package / name), "--version"])
            assert self.args.source_sha in visible and manifest["version"] in visible and "Keelmux" in visible, "executable alias version differs"
        help_text = command([self.binary, "help"])
        assert "keelmux" in help_text.lower(), "release help missing product name"
        self.manifest = manifest
        self.report["release"] = manifest
        self.report["executable_buildinfo"] = version
        self.report["binary_sha256"] = {n: digest(self.package / n) for n in ("keelmux", "keelmuxd", "keelmux-entry")}
        return {"checksum_pinned_archive": True, "all_binary_manifest_digests": True,
                "clean_source_manifest": True, "canonical_and_alias_versions_match": True}

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

    def tmux(self, *args):
        return command(["tmux", "-S", str(self.state / "tmux.sock"), *args])

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
        self.owned_panes[ref["pid"]] = process_start(ref["pid"])[0]
        return session, request

    def stable(self, session):
        now = next(s for s in self.inventory() if s["id"] == session["id"])
        before = session["execution"]
        assert now["execution"]["id"] == before["id"] and now["thread_id"] == session["thread_id"], "durable identity changed"
        assert now["execution"]["runtime"] == before["runtime"] and now["execution"]["state"] == "running", "runtime identity changed"
        assert self.tmux("display-message", "-p", "-t", before["runtime"]["pane"], "#{pane_pid}") == str(before["runtime"]["pid"]), "pane PID changed"
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
        terminal.expect("KEELMUX")
        terminal.expect("Model:")
        return terminal

    def place(self, name="place.json"):
        return json.loads((self.state / name).read_text())

    def detach(self, terminal):
        terminal.send(b"\x07")
        terminal.send(b"d")
        wait_for(lambda: not self.tmux("list-clients", "-F", "#{client_readonly}"))

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
        return {"actual_packaged_daemon_and_tmux": True, "exact_inherited_link": True,
            "forged_link_refused": True, "dropped_reply_retry_exact_identity": True,
            "same_session_thread_execution_pid_pane_after_daemon_SIGKILL": True}

    def controls(self):
        terminal = self.terminal([self.binary, "--state", str(self.state), "attach", self.oracle["id"]])
        terminal.expect("KEELMUX FIXTURE READY")
        wait_for(lambda: self.tmux("list-clients", "-F", "#{client_readonly}") == "0")
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
        wait_for(lambda: "1" in self.tmux("list-clients", "-F", "#{client_readonly}"))
        viewer.resize(20, 10)
        viewer.send("READONLY-CANARY")
        viewer.pump(0.7)
        assert b"READONLY-CANARY" not in bytes.fromhex(json.loads(self.keylog.read_text())["hex"]), "readonly viewer injected input"
        assert self.tmux("display-message", "-p", "-t", self.oracle["execution"]["runtime"]["pane"], "#{pane_width}x#{pane_height}") == "76x23", "readonly viewer resized writer"
        viewer.send(b"\x07")
        viewer.send(b"d")
        wait_for(lambda: "1" not in self.tmux("list-clients", "-F", "#{client_readonly}"))
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
        wait_for(lambda: self.tmux("list-clients", "-F", "#{client_readonly}") == "0")
        self.restart()
        self.stable(self.oracle)
        self.api("attach", self.lease(self.oracle, "restart-competitor"), status=409)
        terminal.close(crash=True)
        wait_for(lambda: not self.tmux("list-clients", "-F", "#{client_readonly}"))
        recovered = self.ui()
        wait_for(lambda: not self.place().get("pending_attachment"))
        assert self.place()["places"][""]["session_id"] == self.oracle["id"], "TUI selected identity lost"
        assert any(d["text"] == "Linux saved draft café λ" for d in self.place()["drafts"]), "local TUI draft lost"
        recovered.send("\r")
        wait_for(lambda: self.tmux("list-clients", "-F", "#{client_readonly}") == "0")
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
                "private_authority_and_client_state_modes": True, "native_auth_realm_untouched": True}

    def runtime_loss(self):
        self.tmux("kill-server")
        self.api("reconcile", {}, status=204)
        before = self.inventory()
        assert all(s["execution"]["state"] in ("missing", "stopped") for s in before), "runtime loss not projected"
        assert any(s["execution"]["state"] == "missing" for s in before), "runtime loss did not mark live fixture missing"
        self.api("launch", self.launch_request)
        self.restart()
        after = self.inventory()
        assert len(after) == len(before), "runtime loss replay created work"
        missing = next(s for s in after if s["id"] == self.oracle["id"])
        assert missing["execution"]["state"] == "missing" and missing["execution"]["id"] == self.oracle["execution"]["id"], "runtime loss silently replaced execution"
        assert process_start(self.oracle["execution"]["runtime"]["pid"]) is None or process_start(self.oracle["execution"]["runtime"]["pid"])[1] == "Z", "owned fixture survived owned tmux destruction"
        return {"owned_tmux_loss_projects_missing": True, "launch_retry_and_daemon_restart_do_not_replay": True,
                "original_execution_and_stopped_tombstone_preserved": True}

    def cleanup(self):
        errors = []
        for terminal in self.terminals:
            terminal.close()
        if self.daemon and self.daemon.poll() is None:
            self.daemon.terminate()
            try:
                self.daemon.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.daemon.kill()
                self.daemon.wait(timeout=5)
        if (self.state / "tmux.sock").exists():
            try:
                self.tmux("kill-server")
            except subprocess.CalledProcessError:
                errors.append("owned tmux cleanup returned failure")
        # tmux acknowledges shutdown before its native pane has necessarily
        # exited. Wait only for the recorded owned process lifetimes.
        deadline = time.monotonic() + 3
        while time.monotonic() < deadline:
            live = [pid for pid, start in self.owned_panes.items()
                    if (current := process_start(pid)) and current[0] == start
                    and current[1] != "Z"]
            if not live:
                break
            time.sleep(0.05)
        for pid, start in self.owned_panes.items():
            current = process_start(pid)
            if current and current[0] == start and current[1] != "Z":
                errors.append("owned pane remains live after cleanup")
        if any(p.poll() is None for p in self.daemon_processes):
            errors.append("owned daemon remains live after cleanup")
        if self.original_home is not None:
            os.environ["HOME"] = self.original_home
        else:
            os.environ.pop("HOME", None)
        if not errors:
            shutil.rmtree(self.temp)
        self.report["cleanup"] = {"owned_terminal_clients_reaped": all(not t.open for t in self.terminals),
            "owned_daemons_reaped": all(p.poll() is not None for p in self.daemon_processes),
            "owned_panes_not_live": not errors, "owned_temp_removed": not self.temp.exists(), "errors": errors}
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
            self.check("tmux loss and no automatic execution replay", self.runtime_loss)
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
    parser.add_argument("--report", required=True)
    args = parser.parse_args()
    LinuxAcceptance(args).run()


if __name__ == "__main__":
    main()
