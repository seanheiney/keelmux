#!/usr/bin/env python3
"""Actual native Codex and Claude rendering through one owned local bpg entry.

Both executable homes are on one computer. No native command or model prompt is
submitted, no account/login changes are made, and no remote host is implied.
"""
import argparse
import hashlib
import json
import re
from pathlib import Path
import shutil
import subprocess
import sys

from gateway_acceptance import GatewayAcceptance
from native_acceptance import wait_for


class NativeGateway(GatewayAcceptance):
    def __init__(self, root, report, native_workspace=None, only="all"):
        super().__init__(root, report)
        self.only = only
        self.report["gateway_harness_sha256"] = self.report["harness_sha256"]
        self.report["harness_sha256"] = hashlib.sha256(Path(__file__).read_bytes()).hexdigest()
        self.report["kind"] = "native_programs_through_same_computer_executable_gateway"
        self.report["providers"] = self.homes[0].report["providers"]
        self.report["auth_mode"] = "existing native realm; account/model unknown; no model prompt submitted"
        self.report["provider_journeys_requested"] = only
        self.report["missing_evidence"] = ["independent remote hosts and enrollment/authentication", "native skill instruction loading/invocation", "authenticated model turn and verified effective account/model", "display backpressure stress", "host reboot/native resume"]
        if native_workspace:
            for home in self.homes:
                home.native_workspace = Path(native_workspace).resolve()

    def prepare_native(self):
        for provider, home in zip(("codex", "claude"), self.homes):
            home.start_daemon()
            receipt = home.temp / (provider+".link.json")
            native_args = ["--sandbox", "read-only", "--config", "check_for_update_on_startup=false"] if provider == "codex" else []
            argv = [sys.executable, home.script, "--child", home.bp, str(home.state), str(receipt), provider, *native_args]
            s, _ = home.launch(provider+" gateway acceptance", provider, argv, workspace=home.native_workspace)
            linked = wait_for(lambda: json.loads(receipt.read_text()) if receipt.exists() else None)
            assert linked["linked"] and linked["session_id"] == s["id"] and linked["execution_id"] == s["execution"]["id"], "native gateway inherited link did not match exact child"
            assert Path(linked["workspace"]).resolve() == home.native_workspace, "native gateway child entered wrong workspace"
            self.work.append(s)
        self.config()
        self.start_entry()
        d = self.api("sessions")
        assert sorted(s["id"] for s in d["sessions"]) == sorted(s["id"] for s in self.work), "native entry directory omitted exact granted session"
        assert len(d["places"]) == 2 and all(p["state"] == "reachable" for p in d["places"]), "native execution homes not reachable"
        assert all(not any(s["execution"]["runtime"].values()) for s in d["sessions"]), "native entry exposed a home locator"
        return {"actual_programs": ["codex", "claude"], "separate_executable_homes": 2, "same_computer": True, "exact_inherited_link_before_exec": True, "native_workspace": str(self.homes[0].native_workspace), "workspace_trust": "existing_native_trust", "login_changes": False, "upgrades": False, "native_skill_invocation": "not_run"}

    def native_attach(self, t, n):
        home, s = self.homes[n], self.work[n]
        provider = s["execution"]["provider"]
        marks = ("›", "»") if provider == "codex" else ("❯",)
        t.output.clear()
        t.send("\r")  # Enter belongs to the backplane TUI, never native mode.
        def writer_ready():
            t.pump(0.05)
            return home.tmux("list-clients", "-F", "#{client_readonly}") == "0"
        wait_for(writer_ready)
        def composer():
            t.pump(0.05)
            text = home.pane(s)
            setup = any(word in text.lower() for word in ("update now", "do you trust", "trust this", "choose a theme"))
            return next((mark for mark in marks if mark in text), None) if not setup else None
        try:
            mark = wait_for(composer, timeout=30)
        except AssertionError:
            pane = home.pane(s)
            self.report.setdefault("native_startup_diagnostics", []).append({
                "provider": provider,
                "native_prompt_mark_present": any(mark in pane for mark in marks),
                "known_startup_phrases": [phrase for phrase in ("update now", "do you trust", "trust this", "choose a theme", "sign in", "log in", "error", "press enter", "press return", "loading", "update available", "coding agent", "input") if phrase in pane.lower()],
                "pane_dead": home.tmux("display-message", "-p", "-t", s["execution"]["runtime"]["pane"], "#{pane_dead}") == "1",
                "native_output_bytes_received": len(t.output),
                "native_nonblank_lines": [
                    {"row": row+1, "characters": len(line),
                     "alphanumeric_characters": sum(c.isalnum() for c in line),
                     "safe_prompt_glyphs": "".join(c for c in line if c in "›»❯>$#")}
                    for row, line in enumerate(pane.splitlines()) if line.strip()
                ],
                "terminal_queries_received": {repr(query): bytes(t.output).count(query)
                    for query in (b"\x1b[6n", b"\x1b[?6n", b"\x1b[c", b"\x1b[>c", b"\x1b[?u", b"\x1b]10;?", b"\x1b]11;?")},
                "ansi_control_sequence_count": len(re.findall(rb"\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07]*(?:\x07|\x1b\\))", bytes(t.output))),
            })
            raise
        t.expect(mark)  # Actual vendor output crossed the gateway to this PTY.

    def detach(self, t, n):
        t.output.clear()
        t.send(b"\x07")
        t.send(b"d")
        # Keep consuming vendor rendering while waiting for transport cleanup.
        t.expect("Returned to the same work and group", timeout=20)
        wait_for(lambda: not self.homes[n].tmux("list-clients", "-F", "#{client_readonly}"))
        wait_for(lambda: not self.place().get("pending_attachment"))
        assert self.place()["places"][""]["session_id"] == self.work[n]["id"], "native gateway detach lost exact selected work"

    def native_journey(self, n):
        provider = self.work[n]["execution"]["provider"]
        home, s = self.homes[n], self.work[n]
        t = self.ui()
        self.select(t, n)
        self.native_attach(t, n)
        t.send("gateway café λ 🙂")
        def draft_visible():
            t.pump(0.05)
            text = home.pane(s)
            return "café" in text and "λ" in text
        wait_for(draft_visible)
        t.send(" escape test")
        t.send(b"\x1b")
        t.pump(0.7)
        t.resize(91, 28)
        def resized():
            t.pump(0.05)
            return home.tmux("display-message", "-p", "-t", s["execution"]["runtime"]["pane"], "#{pane_width}x#{pane_height}") == "91x28"
        wait_for(resized)
        self.detach(t, n)
        assert self.place()["places"][""]["filter"] == s["name"], "native gateway detach lost the selected filter"
        self.stable(n)
        t.send("q")
        t.close()
        self.terminals.remove(t)
        t = self.ui()
        assert self.place()["places"][""]["session_id"] == s["id"], "native gateway TUI reopen lost the exact session"
        self.native_attach(t, n)
        self.stable(n)
        self.detach(t, n)
        t.send("q")
        t.close()
        self.terminals.remove(t)
        return {"provider": provider, "native_composer_output_received_via_gateway": True, "unicode_draft_visible_in_native_composer": True, "native_escape_sent": True, "resize": "91x28", "ctrl_g_d_returns_actual_tui": True, "lease_release_confirmed": True, "selection_filter_all_work_group_preserved": True, "tui_reopen_native_reconnect": True, "same_home_execution_pid_pane": True, "native_enter_sent": False, "model_prompt_submitted": False, "authenticated_turn": "not_run", "display_reader": "continuously_pumped_PTY"}

    def run(self):
        try:
            self.check("actual native homes through executable entry", self.prepare_native)
            for n in range(2):
                if self.only != "all" and self.work[n]["execution"]["provider"] != self.only:
                    continue
                self.check(self.work[n]["execution"]["provider"]+" native gateway attach/detach/reconnect", lambda n=n: self.native_journey(n))
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
    parser.add_argument("--native-workspace", help="repository already trusted by both native CLIs")
    parser.add_argument("--only", choices=("codex", "claude", "all"), default="all")
    args = parser.parse_args()
    NativeGateway(args.root, args.report, args.native_workspace, args.only).run()
