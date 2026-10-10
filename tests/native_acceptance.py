#!/usr/bin/env python3
"""Opt-in real bp/bpd acceptance. No model prompt is submitted.

Uses only an owned private /tmp directory and dedicated tmux socket. Native
logins remain native; link is a deterministic inherited-context helper before
exec, not evidence of vendor skill invocation or authenticated model work.
"""
import argparse
from concurrent.futures import ThreadPoolExecutor
import fcntl
import hashlib
import http.client
import json
import os
from pathlib import Path
import pty
import select
import shutil
import signal
import socket
import sqlite3
import struct
import subprocess
import sys
import tempfile
import termios
import threading
import time
import uuid


def command(argv, **kwargs):
    return subprocess.run(argv, text=True, capture_output=True, check=True, **kwargs).stdout.strip()


def wait_for(fn, timeout=12):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            value = fn()
            if value:
                return value
        except (OSError, ValueError, http.client.HTTPException):
            pass
        time.sleep(0.05)
    raise AssertionError("condition did not become true within deadline")


class UnixHTTP(http.client.HTTPConnection):
    def __init__(self, path):
        super().__init__("backplane", timeout=25)
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
        self.resize(100, 34)

    def resize(self, columns, rows):
        fcntl.ioctl(self.fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, columns, 0, 0))
        os.kill(self.pid, signal.SIGWINCH)

    def pump(self, seconds=0.3):
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            ready, _, _ = select.select([self.fd], [], [], min(0.1, deadline-time.monotonic()))
            if ready:
                try:
                    data = os.read(self.fd, 65536)
                except OSError:
                    break
                if not data:
                    break
                self.output.extend(data)
                # Answer cursor-position queries exactly as a terminal would.
                if b"\x1b[6n" in data:
                    os.write(self.fd, b"\x1b[1;1R")
        return self.output.decode("utf-8", "replace")

    def send(self, data):
        os.write(self.fd, data.encode() if isinstance(data, str) else data)
        self.pump()

    def expect(self, text, timeout=12):
        return wait_for(lambda: text in self.pump(0.1), timeout)

    def close(self):
        try:
            os.kill(self.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        deadline = time.monotonic()+2
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

    def crash(self):
        os.kill(self.pid, signal.SIGKILL)
        os.waitpid(self.pid, 0)
        os.close(self.fd)


def child_link_then_exec(args):
    bp, state, receipt, provider, *native_args = args
    p = subprocess.run([bp, "--state", state, "link"], capture_output=True, text=True)
    if p.returncode:
        Path(receipt).write_text(json.dumps({"linked": False, "returncode": p.returncode}))
        os._exit(123)
    # Public session JSON contains no inherited nonce. Retain only exact IDs.
    s = json.loads(p.stdout)
    Path(receipt).write_text(json.dumps({"linked": s["linked"], "session_id": s["id"], "execution_id": s["execution"]["id"], "thread_id": s["thread_id"], "workspace": os.getcwd()}))
    os.execv(shutil.which(provider), [provider, *native_args])


def terminal_fixture(path):
    # A raw-key oracle is fixture evidence, never native-provider proof.
    tty = termios.tcgetattr(0)
    raw = termios.tcgetattr(0)
    raw[3] &= ~(termios.ECHO | termios.ICANON | termios.ISIG)
    raw[6][termios.VMIN] = 1
    raw[6][termios.VTIME] = 0
    termios.tcsetattr(0, termios.TCSANOW, raw)
    data = bytearray()
    def record(*unused):
        cols, rows = shutil.get_terminal_size()
        Path(path).write_text(json.dumps({"hex": data.hex(), "columns": cols, "rows": rows}))
    signal.signal(signal.SIGWINCH, record)
    os.write(1, b"\x1b[?1049hFIXTURE READY\r\n")
    try:
        while True:
            data.extend(os.read(0, 4096))
            record()
    finally:
        termios.tcsetattr(0, termios.TCSANOW, tty)


class Acceptance:
    def __init__(self, root, report, native_workspace=None):
        self.root = Path(root)
        self.report_path = Path(report)
        self.temp = Path(tempfile.mkdtemp(prefix="bp-a-", dir="/tmp"))
        self.state = self.temp / "s"
        self.workspace = self.temp / "w"
        self.workspace.mkdir(mode=0o700)
        common = Path(command(["git", "rev-parse", "--path-format=absolute", "--git-common-dir"], cwd=self.root))
        self.native_workspace = Path(native_workspace).resolve() if native_workspace else common.parent
        self.bp = str(self.root / "bin/bp")
        self.bpd = str(self.root / "bin/bpd")
        self.script = str(Path(__file__).resolve())
        self.daemon = None
        self.terminals = []
        self.sessions = []
        self.report = {"schema": 1, "tested_sha": command(["git", "rev-parse", "HEAD"], cwd=self.root), "harness_sha256": hashlib.sha256(Path(self.script).read_bytes()).hexdigest(), "utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "platform": command(["sw_vers"]), "go": command(["go", "version"]), "tmux": command(["tmux", "-V"]), "providers": {p: command([p, "--version"]) for p in ("codex", "claude")}, "auth_mode": "existing native realm; effective account/model unknown; no model prompt submitted", "socket": "private owned /tmp/bp-a-*/s/tmux.sock", "checks": [], "missing_evidence": ["authenticated model turn and verified effective account/model", "native skill invocation and preload", "real independent remote servers", "host reboot/native resume", "cross-provider continuity/model change", "Mattermost/IRC delivery", "independent-user speed/accessibility pilot"]}

    def check(self, name, fn, kind="integrated"):
        try:
            detail = fn()
            self.report["checks"].append({"name": name, "kind": kind, "result": "pass", "detail": detail})
            print("PASS", name, flush=True)
            return detail
        except Exception as e:
            # Exception strings are authored conditions, never raw CLI output.
            detail = {"error": str(e)}
            try:
                detail["runtime_health"] = [{"provider": s["execution"]["provider"], "pane_dead": self.tmux("display-message", "-p", "-t", s["execution"]["runtime"]["pane"], "#{pane_dead}")} for s in self.sessions]
                detail["writable_client_flags"] = self.tmux("list-clients", "-F", "#{client_readonly}")
                phrases = ["Attach refused", "runtime is missing", "outcome unknown", "Input", "stale", "already held", "Returned to", "Saved selection", "Acquiring", "OFFLINE"]
                detail["terminal_status_phrases"] = [p for p in phrases if self.terminals and p in self.terminals[-1].output.decode("utf-8", "replace")]
            except Exception:
                pass
            self.report["checks"].append({"name": name, "kind": kind, "result": "fail", "detail": detail})
            print("FAIL", name, str(e), flush=True)
            raise

    def api(self, path, body=None, status=200, drop_response=False):
        c = UnixHTTP(self.state / "control.sock")
        method = "GET" if body is None else "POST"
        c.request(method, "/v1/"+path, json.dumps(body) if body is not None else None, {"X-Backplane-Protocol": "bp/1", "Content-Type": "application/json"})
        if drop_response:
            # Wait until the authority has replied, without consuming any
            # response. Then discard it. An earlier disconnect may cancel the
            # request before its intent is committed, so cannot prove a lost
            # acknowledgement after an external effect.
            assert c.sock.recv(1, socket.MSG_PEEK), "server did not reply before response loss"
            c.sock.shutdown(socket.SHUT_RD)
            c.close()
            return None
        r = c.getresponse()
        data = r.read()
        c.close()
        allowed = status if isinstance(status, tuple) else (status,)
        assert r.status in allowed, f"{path}: expected HTTP {status}; got {r.status}"
        return json.loads(data) if data else None

    def start_daemon(self):
        self.state.mkdir(mode=0o700, exist_ok=True)
        log = open(self.temp / "daemon.log", "ab")
        self.daemon = subprocess.Popen([self.bpd, "--state", str(self.state)], stdout=log, stderr=log)
        log.close()
        def ready():
            assert self.daemon.poll() is None, "owned daemon exited before ready; inspect its private log"
            return self.api("sessions")
        wait_for(ready)

    def restart(self):
        self.daemon.kill()
        self.daemon.wait(timeout=5)
        self.start_daemon()

    def inventory(self):
        return self.api("sessions")["sessions"]

    def launch(self, name, provider, argv, drop=False, workspace=None):
        request = {"operation_id": "op_"+uuid.uuid4().hex, "name": name, "workspace": str(workspace or self.workspace), "provider": provider, "provider_version": self.report["providers"].get(provider, "fixture"), "argv": argv}
        self.api("launch", request, drop_response=drop)
        if drop:
            wait_for(lambda: any(s["name"] == name for s in self.inventory()))
        receipt = self.api("launch", request)
        assert receipt["operation"]["outcome"] == "completed", "native launch did not complete"
        self.sessions.append(receipt["session"])
        return receipt["session"], request

    def lease(self, s, client="acceptance", readonly=False):
        return {"session_id": s["id"], "execution_id": s["execution"]["id"], "generation": s["execution"]["generation"], "operation_id": "attach_"+uuid.uuid4().hex, "client_id": client, "read_only": readonly}

    def tmux(self, *args):
        return command(["tmux", "-S", str(self.state / "tmux.sock"), *args])

    def pane(self, s):
        return self.tmux("capture-pane", "-p", "-t", s["execution"]["runtime"]["pane"])

    def ui(self):
        t = Terminal([self.bp, "--state", str(self.state)], str(self.workspace))
        self.terminals.append(t)
        t.expect("BACKPLANE")
        t.expect("Model:")  # initial render may precede the inventory response
        return t

    def native_journey(self, provider):
        receipt = self.temp / (provider+".link.json")
        native_args = []
        if provider == "codex":
            # Use existing native trust and login, with a read-only sandbox.
            # Suppress only this invocation's update selector; never upgrade.
            native_args = ["--sandbox", "read-only", "--config", "check_for_update_on_startup=false"]
        s, request = self.launch(provider+" acceptance", provider, [sys.executable, self.script, "--child", self.bp, str(self.state), str(receipt), provider, *native_args], drop=True, workspace=self.native_workspace)
        linked = wait_for(lambda: json.loads(receipt.read_text()) if receipt.exists() else None)
        assert linked["linked"] and linked["session_id"] == s["id"] and linked["execution_id"] == s["execution"]["id"], "inherited exact link failed"
        assert Path(linked["workspace"]).resolve() == Path(s["workspace"]).resolve(), "managed native child entered the wrong workspace"
        assert not s["capabilities"]["semantic_prompt"], "native managed mode falsely declares semantic delivery"
        self.api("link", {"session_id": s["id"], "execution_id": s["execution"]["id"], "operation_id": request["operation_id"], "token": "forged"}, status=409)
        self.api("link", {"session_id": s["id"], "execution_id": s["execution"]["id"], "operation_id": request["operation_id"], "token": ""}, status=409)
        t = self.ui()
        t.send(b"\x1b")
        t.send("/")
        t.send(provider)
        t.send("\r")
        saved = json.loads((self.state / "place.json").read_text())
        assert saved["places"][""]["filter"] == provider, "TUI search did not persist filter"
        t.send("\r")
        wait_for(lambda: self.tmux("list-clients", "-F", "#{client_readonly}") == "0")
        before = s["execution"]["runtime"]
        def composer():
            text = self.pane(s)
            mark = "›" if provider == "codex" else "❯"
            setup = any(word in text.lower() for word in ("update now", "do you trust", "trust this", "choose a theme"))
            return text if mark in text and not setup else None
        native_text = wait_for(composer)
        assert "Traceback" not in native_text, "native child raised an exception"
        # Never press Enter in native mode. Draft characters are a terminal
        # check, not an agent prompt. The current screen may be onboarding.
        t.send("unicode café λ 🙂")
        rendered = self.pane(s)
        unicode_seen = "café" in rendered and "λ" in rendered
        assert unicode_seen, "Unicode draft was not visible in the native composer"
        t.send("escape test")
        t.send(b"\x1b")
        t.pump(0.7)  # tmux's default escape-time must finish before prefix input.
        t.resize(92, 29)
        wait_for(lambda: self.tmux("display-message", "-p", "-t", before["pane"], "#{pane_width}x#{pane_height}") == "92x29")
        t.send(b"\x07")
        t.send(b"d")
        t.expect("Returned to the same work and group")
        place = json.loads((self.state / "place.json").read_text())
        assert place["places"][""]["session_id"] == s["id"] and place["places"][""]["filter"] == provider, "native detach lost TUI selection/filter"
        assert not place.get("pending_attachment"), "native detach did not release durable lease"
        t.send("q")
        t.close()
        self.terminals.remove(t)
        t = self.ui()
        t.send("\r")
        wait_for(lambda: self.tmux("list-clients", "-F", "#{client_readonly}") == "0")
        t.crash()
        self.terminals.remove(t)
        wait_for(lambda: not self.tmux("list-clients", "-F", "#{client_readonly}"))
        t = self.ui()
        wait_for(lambda: not json.loads((self.state / "place.json").read_text()).get("pending_attachment"))
        t.send("\r")
        wait_for(lambda: self.tmux("list-clients", "-F", "#{client_readonly}") == "0")
        self.restart()
        now = next(v for v in self.inventory() if v["id"] == s["id"])
        assert now["execution"]["runtime"] == before and now["execution"]["state"] == "running", "daemon restart changed native execution"
        self.api("attach", self.lease(s, "competitor"), status=409)
        self.api("launch", request)
        assert len([v for v in self.inventory() if v["id"] == s["id"]]) == 1, "lost launch acknowledgement created duplicate registry row"
        assert self.tmux("display-message", "-p", "-t", before["pane"], "#{pane_pid}") == str(before["pid"]), "native process changed after reconnect"
        t.send(b"\x07")
        t.send(b"d")
        t.expect("Returned to the same work and group")
        t.send("q")
        t.close()
        self.terminals.remove(t)
        return {"provider": provider, "native_workspace": str(self.native_workspace), "exact_child_workspace": True, "stable_session_execution_pid_pane": True, "exact_inherited_link": True, "link_nonce_forgery_refused": True, "lost_launch_ack_retry": True, "tui_detach_reconnect": True, "tui_crash_recovery": True, "daemon_restart_while_attached": True, "native_resize": True, "unicode_draft_visible": unicode_seen, "ctrl_c_unsubmitted_draft": "separate_interrupt_probe", "workspace_trust": "existing_native_trust", "authenticated_turn": "not_run"}

    def lost_attach_ack(self):
        s = self.sessions[0]
        req = self.lease(s)
        self.api("attach", req, drop_response=True)
        time.sleep(0.2)
        grant = self.api("attach", req)
        self.restart()
        recovered = self.api("attach", req)
        assert grant["lease"] == recovered["lease"], "lost attach response/restart replaced grant"
        other = self.lease(s, "another")
        self.api("attach", other, status=409)
        conflict = dict(req, client_id="another")
        self.api("attach", conflict, status=409)
        release = dict(req, token=grant["lease"]["token"])
        self.api("release", dict(release, token="forged"), status=409)
        self.api("release", release, status=204)
        self.api("release", release, status=204)
        self.api("attach", req, status=409)
        contenders = [self.lease(s, "racing-"+str(n)) for n in range(2)]
        barrier = threading.Barrier(2)
        def compete(request):
            barrier.wait(timeout=5)
            return request, self.api("attach", request, status=(200, 409))
        with ThreadPoolExecutor(max_workers=2) as pool:
            futures = [pool.submit(compete, request) for request in contenders]
            replies = [future.result(timeout=30) for future in futures]
        winners = [(request, reply) for request, reply in replies if "lease" in reply]
        assert len(winners) == 1, "racing cooperating clients did not receive exactly one writer grant"
        winner_req, winner_reply = winners[0]
        self.api("release", dict(winner_req, token=winner_reply["lease"]["token"]), status=204)
        return "same durable grant after dropped response/restart; collision/forged release refused; exactly one writer grant in concurrent two-client race"

    def journal(self):
        db = sqlite3.connect(self.state / "registry.sqlite")
        rows = db.execute("SELECT thread_id,sequence,data FROM events ORDER BY thread_id,sequence").fetchall()
        per_thread = {}
        kinds = []
        for thread, sequence, data in rows:
            if isinstance(data, bytes):
                data = data.decode()
            per_thread.setdefault(thread, []).append(sequence)
            event = json.loads(data)
            assert event["thread_id"] == thread and event["sequence"] == sequence, "journal projection mismatch"
            assert "BP_LINK_TOKEN" not in data and "link_token" not in data, "private correlation leaked into journal"
            kinds.append(event["kind"])
        db.close()
        assert all(values == list(range(1, len(values)+1)) for values in per_thread.values()), "noncontiguous per-thread journal"
        assert all(kind in kinds for kind in ("launch.recorded", "launch.attempted", "launch.completed", "link.on", "attach.granted", "attach.released")), "journal omitted lifecycle evidence"
        return {"events": len(rows), "threads": len(per_thread), "contiguous_per_thread": True, "durable_intents_outcomes": True}

    def fixtures_and_place(self):
        keylog = self.temp / "keys.json"
        fixture, _ = self.launch("key oracle", "fixture", [sys.executable, self.script, "--fixture", str(keylog)])
        for n in range(7):
            self.launch("auxiliary "+str(n), "fixture", [sys.executable, self.script, "--fixture", str(self.temp / ("aux-"+str(n)+".json"))])
        assert len(self.inventory()) == 10, "ten-session inventory missing"
        t = self.ui()
        t.send(b"\x1b")  # clear saved native-provider filter
        t.send("/")
        t.send("key oracle")
        t.send("\r")
        # Add one optional company and a product; pin the exact selected work.
        t.send("g")
        t.send("n")
        t.send("Acceptance company")
        t.send("\r")
        t.send("c")
        t.send("Acceptance product")
        t.send("\r")
        t.send("s")
        t.send("\r")
        place = json.loads((self.state / "place.json").read_text())
        active = place["active_group"]
        assert place["places"][active]["session_id"] == fixture["id"], "group failed to retain exact selected session"
        t.send("p")
        t.send("local draft café λ")
        t.send(b"\x1b")
        t.send("\r")
        wait_for(lambda: "0" in self.tmux("list-clients", "-F", "#{client_readonly}"))
        t.send("café λ 🙂")
        t.send(b"\x1b")
        t.pump(0.7)
        t.send(b"\x03")
        t.send(b"\x07")
        t.send(b"\x07")
        t.resize(76, 23)
        observed = wait_for(lambda: json.loads(keylog.read_text()) if keylog.exists() else None)
        wait_for(lambda: json.loads(keylog.read_text())["columns"] == 76)
        observed = json.loads(keylog.read_text())
        data = bytes.fromhex(observed["hex"])
        assert "café λ 🙂".encode() in data and b"\x1b" in data and b"\x03" in data and b"\x07" in data, "native tmux path lost raw Unicode/Escape/Ctrl-C/doubled-prefix bytes"
        assert (observed["columns"], observed["rows"]) == (76, 23), "raw fixture lost terminal resize"
        ro_req = self.lease(fixture, "read-only-observer", True)
        ro_grant = self.api("attach", ro_req)
        ro = Terminal(ro_grant["argv"], str(self.workspace))
        self.terminals.append(ro)
        ro.resize(30, 10)
        wait_for(lambda: "1" in self.tmux("list-clients", "-F", "#{client_readonly}"))
        ro.send("READONLY")
        ro.pump(0.7)
        assert b"READONLY" not in bytes.fromhex(json.loads(keylog.read_text())["hex"]), "read-only observer injected native input"
        assert self.tmux("display-message", "-p", "-t", fixture["execution"]["runtime"]["pane"], "#{pane_width}x#{pane_height}") == "76x23", "read-only observer changed writer terminal size"
        ro.send(b"\x07")
        ro.send(b"d")
        wait_for(lambda: "1" not in self.tmux("list-clients", "-F", "#{client_readonly}"))
        self.api("release", dict(ro_req, token=ro_grant["lease"]["token"]), status=204)
        ro.close()
        self.terminals.remove(ro)
        t.send(b"\x07")
        t.send(b"d")
        t.expect("Returned to the same work and group")
        restored = json.loads((self.state / "place.json").read_text())
        assert restored["active_group"] == active and restored["places"][active]["session_id"] == fixture["id"], "detach lost group/product/session place"
        assert any(d["text"] == "local draft café λ" for d in restored["drafts"]), "detach lost saved draft"
        t.send("q")
        t.close()
        self.terminals.remove(t)
        t = self.ui()
        t.expect("Acceptance company / Acceptance product")
        t.send("q")
        t.close()
        self.terminals.remove(t)
        return {"sessions": 10, "native_providers": 2, "fixture_processes": 8, "raw_unicode_escape_ctrl_c_literal_prefix_resize": True, "read_only_input_and_size_preserved": True, "company_product_selection_draft_restore": True, "human_speed_pilot": "not_run"}

    def lost_stop_ack(self):
        s = next(v for v in self.sessions if v["execution"]["provider"] == "fixture")
        req = {"session_id": s["id"], "execution_id": s["execution"]["id"], "generation": s["execution"]["generation"], "operation_id": "stop_"+uuid.uuid4().hex}
        self.api("stop", dict(req, generation=req["generation"]+1), status=409)
        self.api("stop", req, status=204, drop_response=True)
        self.api("stop", req, status=204)
        self.restart()
        self.api("stop", req, status=204)
        self.api("stop", dict(req, execution_id="execution_conflicting"), status=409)
        v = next(v for v in self.inventory() if v["id"] == s["id"])
        assert v["execution"]["state"] == "stopped", "confirmed stop lost after restart"
        return "stale generation refused; same lost-response stop idempotent across restart; conflicting reused operation refused"

    def native_interrupts(self):
        results = []
        for s in self.sessions[:2]:
            req = self.lease(s, "interrupt-probe")
            grant = self.api("attach", req)
            t = Terminal(grant["argv"], str(self.workspace))
            self.terminals.append(t)
            t.send("interrupt probe")
            before = self.pane(s)
            t.send(b"\x03")
            t.pump(0.7)
            after = self.pane(s)
            dead = self.tmux("display-message", "-p", "-t", s["execution"]["runtime"]["pane"], "#{pane_dead}") == "1"
            assert before != after or dead, "native interrupt produced no observed terminal effect"
            results.append({"provider": s["execution"]["provider"], "ctrl_c_sent_via_native_attach": True, "native_screen_changed": before != after, "native_process_exited": dead, "model_prompt_submitted": False})
            t.send(b"\x07")
            t.send(b"d")
            wait_for(lambda: not self.tmux("list-clients", "-F", "#{client_readonly}"))
            self.api("release", dict(req, token=grant["lease"]["token"]), status=204)
            t.close()
            self.terminals.remove(t)
        self.api("reconcile", {}, status=204)
        return results

    def tmux_loss(self):
        self.tmux("kill-server")
        self.api("reconcile", {}, status=204)
        assert all(v["execution"]["state"] in ("missing", "stopped") for v in self.inventory()), "tmux loss was not reported missing"
        assert any(v["execution"]["state"] == "missing" for v in self.inventory()), "no missing execution after tmux loss"
        return "owned tmux server loss reports missing execution; no replacement is launched"

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
            for t in self.terminals:
                t.close()
            if self.daemon:
                self.daemon.terminate()
                try:
                    self.daemon.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    self.daemon.kill()
                    self.daemon.wait()
            try:
                self.tmux("kill-server")
            except subprocess.CalledProcessError:
                pass
            self.report_path.parent.mkdir(parents=True, exist_ok=True)
            self.report_path.write_text(json.dumps(self.report, indent=2)+"\n")
            shutil.rmtree(self.temp)


if __name__ == "__main__":
    if len(sys.argv) > 1 and sys.argv[1] == "--child":
        child_link_then_exec(sys.argv[2:])
    elif len(sys.argv) > 1 and sys.argv[1] == "--fixture":
        terminal_fixture(sys.argv[2])
    else:
        parser = argparse.ArgumentParser(description=__doc__)
        parser.add_argument("--root", default=str(Path(__file__).resolve().parents[1]))
        parser.add_argument("--report", required=True)
        parser.add_argument("--native-workspace", help="repository already trusted by both native CLIs; defaults to the primary repository checkout")
        args = parser.parse_args()
        Acceptance(args.root, args.report, args.native_workspace).run()
