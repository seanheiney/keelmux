#!/usr/bin/env python3
"""Opt-in real bpg -> two executable bpd homes, on one computer.

Owned raw terminal fixture panes prove routing and recovery boundaries, not
native provider turns or independent remote enrollment/authentication.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time

from native_acceptance import Acceptance, Terminal, UnixHTTP, command, wait_for


class GatewayAcceptance:
    def __init__(self, root, report):
        self.root = Path(root).resolve()
        self.report_path = Path(report)
        self.temp = Path(tempfile.mkdtemp(prefix="bp-g-", dir="/tmp"))
        self.entry = self.temp / "e"
        self.client = self.temp / "c"
        self.configuration = self.temp / "peers.json"
        self.socket = self.entry / "control.sock"
        self.bp = str(self.root / "bin/bp")
        self.bpg = str(self.root / "bin/bpg")
        self.homes = [Acceptance(root, self.temp / ("home-"+str(n)+".json")) for n in range(2)]
        self.gateway = None
        self.terminals = []
        self.work = []
        self.report = {
            "schema": 1, "tested_sha": command(["git", "rev-parse", "HEAD"], cwd=self.root),
            "harness_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
            "native_core_harness_sha256": hashlib.sha256(Path(self.homes[0].script).read_bytes()).hexdigest(),
            "binary_sha256": {p: hashlib.sha256((self.root / "bin" / p).read_bytes()).hexdigest() for p in ("bp", "bpd", "bpg")},
            "utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "platform": command(["sw_vers"]), "go": command(["go", "version"]), "tmux": command(["tmux", "-V"]),
            "kind": "same_computer_executable_process_fixture", "model_prompt_submitted": False,
            "configuration": "owned600 principal+explicit home/session grants; startup reload on restart",
            "checks": [], "missing_evidence": ["independent remote hosts", "SSH or other remote enrollment/authentication", "native provider authenticated model turns", "live configuration revocation without entry restart", "host reboot and network transport faults"],
        }

    def api(self, path, body=None, status=200):
        c = UnixHTTP(self.socket)
        c.request("GET" if body is None else "POST", "/v1/"+path, json.dumps(body) if body is not None else None, {"X-Backplane-Protocol": "bp/1", "Content-Type": "application/json"})
        r = c.getresponse()
        data = r.read()
        c.close()
        allowed = status if isinstance(status, tuple) else (status,)
        assert r.status in allowed, f"entry {path}: expected HTTP {status}; got {r.status}"
        return json.loads(data) if data else None

    def config(self, revoked=None):
        value = {"principal": "owned-acceptance-principal", "peers": [], "grants": []}
        for n, (home, s) in enumerate(zip(self.homes, self.work)):
            value["peers"].append({"host_id": s["host_id"], "socket": str(home.state / "control.sock")})
            value["grants"].append({"host_id": s["host_id"], "sessions": [] if n == revoked else [s["id"]]})
        staging = self.temp / "peers.new"
        staging.write_text(json.dumps(value)+"\n")
        staging.chmod(0o600)
        staging.replace(self.configuration)

    def start_entry(self):
        log = open(self.temp / "entry.log", "ab")
        self.gateway = subprocess.Popen([self.bpg, "--state", str(self.entry), "--config", str(self.configuration), "--companion", self.bp], stdout=log, stderr=log)
        log.close()
        def ready():
            assert self.gateway.poll() is None, "owned entry exited before ready; inspect private entry log"
            return self.api("sessions")
        wait_for(ready)

    def kill_entry(self):
        self.gateway.kill()
        self.gateway.wait(timeout=5)

    def place(self):
        return json.loads((self.client / "place.json").read_text())

    def ui(self):
        t = Terminal([self.bp, "--state", str(self.client), "--entry", str(self.socket)], str(self.homes[0].workspace))
        self.terminals.append(t)
        t.expect("BACKPLANE")
        t.expect("Model:")
        return t

    def select(self, t, n):
        t.send(b"\x1b")
        t.send("/")
        t.send(self.work[n]["name"])
        t.send("\r")
        assert self.place()["places"][""]["session_id"] == self.work[n]["id"], "entry TUI selected the wrong home work"

    def attach(self, t, n):
        t.send("\r")
        wait_for(lambda: self.homes[n].tmux("list-clients", "-F", "#{client_readonly}") == "0")
        t.expect("FIXTURE READY")

    def detach(self, t, n):
        t.output.clear()
        t.send(b"\x07")
        t.send(b"d")
        wait_for(lambda: not self.homes[n].tmux("list-clients", "-F", "#{client_readonly}"))
        wait_for(lambda: not self.place().get("pending_attachment"))
        t.expect("Returned to the same work and group")
        assert self.place()["places"][""]["session_id"] == self.work[n]["id"], "entry detach lost selected work"

    def keys(self, n):
        return json.loads((self.homes[n].temp / "keys.json").read_text())

    def stable(self, n):
        s = next(s for s in self.homes[n].inventory() if s["id"] == self.work[n]["id"])
        assert s["execution"]["runtime"] == self.work[n]["execution"]["runtime"] and s["execution"]["state"] == "running", "entry fault changed the home execution"
        assert self.homes[n].tmux("display-message", "-p", "-t", s["execution"]["runtime"]["pane"], "#{pane_pid}") == str(s["execution"]["runtime"]["pid"]), "home native pane PID changed"

    def check(self, name, fn):
        try:
            detail = fn()
            self.report["checks"].append({"name": name, "result": "pass", "detail": detail})
            print("PASS", name, flush=True)
        except Exception as e:
            detail = {"error": str(e)}
            if self.terminals:
                visible = self.terminals[-1].output.decode("utf-8", "replace")
                detail["terminal_status_phrases"] = [phrase for phrase in ("OFFLINE", "Connection offline", "lease release pending", "x reconcile/release", "Waiting for native", "Returned to", "Saved selection is unavailable") if phrase in visible]
            try:
                pending = self.place().get("pending_attachment")
                detail["saved_pending_lease_present"] = bool(pending and pending.get("token"))
                detail["home_native_client_flags"] = [home.tmux("list-clients", "-F", "#{client_readonly}") for home in self.homes]
            except Exception:
                pass
            self.report["checks"].append({"name": name, "result": "fail", "detail": detail})
            print("FAIL", name, str(e), flush=True)
            raise

    def directory(self):
        for n, home in enumerate(self.homes):
            home.start_daemon()
            s, _ = home.launch("gateway home "+str(n+1), "fixture", [sys.executable, home.script, "--fixture", str(home.temp / "keys.json")])
            self.work.append(s)
        assert self.work[0]["host_id"] != self.work[1]["host_id"], "separate homes reused host identity"
        self.config()
        self.start_entry()
        d = self.api("sessions")
        assert sorted(s["id"] for s in d["sessions"]) == sorted(s["id"] for s in self.work), "entry directory did not unite exact granted work"
        assert len(d["places"]) == 2 and all(p["state"] == "reachable" for p in d["places"]), "reachable home inventory missing"
        assert all(not any(s["execution"]["runtime"].values()) for s in d["sessions"]), "home runtime locators leaked through entry"
        assert all(not s["capabilities"]["stop"] and not s["capabilities"]["semantic_prompt"] for s in d["sessions"]), "entry overclaims management or prompt delivery"
        listed = json.loads(command([self.bp, "--state", str(self.client), "--entry", str(self.socket), "directory"]))
        assert sorted(s["id"] for s in listed["sessions"]) == sorted(s["id"] for s in self.work), "executable bp directory diverged"
        assert not (self.client / "control.sock").exists(), "entry client silently started a local home"
        self.api("launch", {}, status=501)
        denied = subprocess.run([self.bp, "--state", str(self.client), "--entry", str(self.socket), "stop", self.work[0]["id"]], capture_output=True, text=True)
        assert denied.returncode != 0 and "execution home" in denied.stderr, "entry CLI allowed home management"
        return {"homes": 2, "separate_bpd_processes": True, "one_entry_cli_directory": True, "runtime_locators_scrubbed": True, "entry_management_refused": True, "automatic_local_home_started": False}

    def terminal_paths(self):
        t = self.ui()
        self.select(t, 0)
        self.attach(t, 0)
        t.send("home one café λ 🙂")
        t.send(b"\x1b")
        t.pump(0.7)
        t.send(b"\x03")
        t.send(b"\x07")
        t.send(b"\x07")
        t.resize(83, 27)
        wait_for(lambda: (self.keys(0)["columns"], self.keys(0)["rows"]) == (83, 27))
        raw = bytes.fromhex(self.keys(0)["hex"])
        assert "home one café λ 🙂".encode() in raw and all(b in raw for b in (b"\x1b", b"\x03", b"\x07")), "entry stream lost raw terminal bytes"
        self.api("attach", self.homes[0].lease(self.work[0], "entry-competitor"), status=409)
        self.homes[0].api("attach", self.homes[0].lease(self.work[0], "home-competitor"), status=409)
        ro_req = self.homes[0].lease(self.work[0], "entry-readonly", True)
        ro_grant = self.api("attach", ro_req)
        assert ro_grant["argv"][0] == self.bp and "drive" in ro_grant["argv"], "entry returned nonlocal native attach argv"
        ro = Terminal(ro_grant["argv"], str(self.homes[0].workspace))
        self.terminals.append(ro)
        wait_for(lambda: "1" in self.homes[0].tmux("list-clients", "-F", "#{client_readonly}"))
        ro.resize(20, 10)
        ro.send("READONLY-CANARY")
        ro.pump(0.7)
        assert b"READONLY-CANARY" not in bytes.fromhex(self.keys(0)["hex"]), "entry readonly viewer injected input"
        assert self.homes[0].tmux("display-message", "-p", "-t", self.work[0]["execution"]["runtime"]["pane"], "#{pane_width}x#{pane_height}") == "83x27", "entry readonly viewer changed writer size"
        ro.send(b"\x07")
        ro.send(b"d")
        wait_for(lambda: "1" not in self.homes[0].tmux("list-clients", "-F", "#{client_readonly}"))
        self.api("release", dict(ro_req, token=ro_grant["lease"]["token"]), status=204)
        ro.close()
        self.terminals.remove(ro)
        self.detach(t, 0)
        prior = self.keys(0)["hex"]
        self.select(t, 1)
        self.attach(t, 1)
        t.send("home two routing")
        wait_for(lambda: b"home two routing" in bytes.fromhex(self.keys(1)["hex"]))
        assert self.keys(0)["hex"] == prior, "second-home input crossed into the first home"
        self.detach(t, 1)
        self.ui_terminal = t
        return {"actual_tui_drive_attach_detach_both_homes": True, "unicode_escape_ctrl_c_literal_prefix": True, "resize": "83x27", "read_only_input_size_preserved": True, "entry_and_local_home_writer_contention_refused": True, "cross_home_input_leak": False}

    def entry_restart(self):
        t = self.ui_terminal
        self.attach(t, 1)
        pending = self.place()["pending_attachment"]
        t.output.clear()
        self.kill_entry()
        wait_for(lambda: not self.homes[1].tmux("list-clients", "-F", "#{client_readonly}"))
        t.expect("OFFLINE / last observed inventory")
        t.expect("x reconcile/release")
        assert self.place()["pending_attachment"] == pending, "entry crash erased the exact saved lease"
        self.start_entry()
        t.send("x")
        wait_for(lambda: not self.place().get("pending_attachment"))
        self.stable(0)
        self.stable(1)
        self.attach(t, 1)
        assert self.place()["pending_attachment"]["operation_id"] != pending["operation_id"], "released acquisition was reused as a fresh stream"
        t.send("after entry restart")
        wait_for(lambda: b"after entry restart" in bytes.fromhex(self.keys(1)["hex"]))
        self.detach(t, 1)
        return {"entry_sigkill_while_attached": True, "saved_exact_lease_retained": True, "restart_receipt_release_confirmed": True, "home_execution_pid_pane_stable": True, "fresh_stream_uses_new_intent": True}

    def revoked_restart(self):
        req = self.homes[0].lease(self.work[0], "revoked-receipt")
        grant = self.api("attach", req)
        self.kill_entry()
        self.config(revoked=0)
        self.start_entry()
        assert [s["id"] for s in self.api("sessions")["sessions"]] == [self.work[1]["id"]], "startup revoke returned cached unauthorized work"
        self.api("attach", req, status=403)
        self.api("attach", self.homes[0].lease(self.work[0], "new-revoked"), status=403)
        release = dict(req, token=grant["lease"]["token"])
        self.api("release", dict(release, token="forged"), status=403)
        self.api("release", release, status=204)
        self.api("release", release, status=204)
        direct = self.homes[0].lease(self.work[0], "home-after-revoke")
        home_grant = self.homes[0].api("attach", direct)
        self.homes[0].api("release", dict(direct, token=home_grant["lease"]["token"]), status=204)
        self.stable(0)
        self.kill_entry()
        self.config()
        self.start_entry()
        return {"revoke_mode": "changed_private_startup_config_then_entry_restart", "fresh_navigation_and_acquisition_refused": True, "loaded_receipt_exact_release_after_revoke": True, "forged_release_refused": True, "native_work_stayed_at_home": True, "live_config_reload": "not_implemented_not_claimed"}

    def partial_home(self):
        t = self.ui_terminal
        t.output.clear()
        self.homes[1].daemon.kill()
        self.homes[1].daemon.wait(timeout=5)
        d = wait_for(lambda: self.api("sessions") if len(self.api("sessions")["sessions"]) == 1 else None)
        assert sorted(p["state"] for p in d["places"]) == ["reachable", "unreachable"], "entry did not separate unreachable home from entry outage"
        t.send("r")
        t.expect("1 execution homes unreachable; entry remains connected.")
        t.expect("Saved selection is unavailable")
        assert self.place()["places"][""]["session_id"] == self.work[1]["id"], "partial inventory silently replaced saved selection"
        self.select(t, 0)
        self.attach(t, 0)
        t.send("surviving home usable")
        wait_for(lambda: b"surviving home usable" in bytes.fromhex(self.keys(0)["hex"]))
        self.detach(t, 0)
        self.homes[1].start_daemon()
        wait_for(lambda: len(self.api("sessions")["sessions"]) == 2)
        self.stable(1)
        self.select(t, 1)
        return {"idle_home_sigkill": True, "entry_still_connected": True, "availability_labels_and_saved_unavailable_selection": True, "surviving_home_attach_usable": True, "restarted_home_same_execution": True}

    def attached_home_partition(self):
        t = self.ui_terminal
        self.attach(t, 1)
        pending = self.place()["pending_attachment"]
        t.output.clear()
        self.homes[1].daemon.kill()
        self.homes[1].daemon.wait(timeout=5)
        t.expect("lease release pending")
        assert self.place()["pending_attachment"] == pending, "home partition erased unreleased writer intent"
        wait_for(lambda: not self.homes[1].tmux("list-clients", "-F", "#{client_readonly}"))
        self.homes[1].start_daemon()
        t.send("x")
        wait_for(lambda: not self.place().get("pending_attachment"))
        self.stable(1)
        self.attach(t, 1)
        t.send("after home repair")
        wait_for(lambda: b"after home repair" in bytes.fromhex(self.keys(1)["hex"]))
        self.detach(t, 1)
        t.send("q")
        t.close()
        self.terminals.remove(t)
        t = self.ui()
        assert self.place()["places"][""]["session_id"] == self.work[1]["id"], "TUI reopen lost home selection"
        t.send("q")
        t.close()
        self.terminals.remove(t)
        return {"attached_home_sigkill": True, "release_failure_retains_exact_grant": True, "home_restart_repair_and_native_reconnect": True, "same_home_execution": True, "tui_reopen_selection": True}

    def run(self):
        try:
            self.check("two executable homes and scoped entry directory", self.directory)
            self.check("actual TUI routed terminal bytes and read-only ownership", self.terminal_paths)
            self.check("entry crash and saved lease recovery", self.entry_restart)
            self.check("startup revocation and exact receipt release", self.revoked_restart)
            self.check("partial home availability with usable surviving home", self.partial_home)
            self.check("attached home partition and repair", self.attached_home_partition)
        finally:
            for t in self.terminals:
                t.close()
            if self.gateway and self.gateway.poll() is None:
                self.gateway.terminate()
                try:
                    self.gateway.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    self.gateway.kill()
                    self.gateway.wait()
            for home in self.homes:
                if home.daemon and home.daemon.poll() is None:
                    home.daemon.terminate()
                    try:
                        home.daemon.wait(timeout=5)
                    except subprocess.TimeoutExpired:
                        home.daemon.kill()
                        home.daemon.wait()
                try:
                    home.tmux("kill-server")
                except subprocess.CalledProcessError:
                    pass
                shutil.rmtree(home.temp)
            self.report_path.parent.mkdir(parents=True, exist_ok=True)
            self.report_path.write_text(json.dumps(self.report, indent=2)+"\n")
            shutil.rmtree(self.temp)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", default=str(Path(__file__).resolve().parents[1]))
    parser.add_argument("--report", required=True)
    args = parser.parse_args()
    GatewayAcceptance(args.root, args.report).run()
