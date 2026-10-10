#!/usr/bin/env python3
"""Opt-in silent-home latency and surviving TUI acceptance on one computer.

Reuse GatewayAcceptance's two owned executable raw-fixture homes. SIGSTOP only
the second owned bpd; always SIGCONT it before cleanup. No native model turn,
external home, existing terminal, or user configuration is touched.
"""
import argparse
import hashlib
import json
from pathlib import Path
import shutil
import signal
import subprocess
import time

from gateway_acceptance import GatewayAcceptance
from native_acceptance import command, wait_for


FROZEN_IMPLEMENTATION = "4b36f4d773a31388c7ec8ade146eb1929a8d2a19"


class LatencyAcceptance(GatewayAcceptance):
    def __init__(self, root, report, expected_sha, deadline):
        super().__init__(root, report)
        self.expected_sha = expected_sha
        self.deadline = deadline
        self.paused_home = None
        self.report["gateway_harness_sha256"] = self.report["harness_sha256"]
        self.report["harness_sha256"] = hashlib.sha256(Path(__file__).read_bytes()).hexdigest()
        self.report["harness_commit"] = command(["git", "rev-parse", "HEAD"], cwd=Path(__file__).resolve().parent)
        self.report["repository_sha_at_start"] = self.report["tested_sha"]
        self.report["tested_sha"] = expected_sha
        self.report["claim"] = "one bounded executable directory fetch preserves healthy work while a distinct owned home is silently stopped; actual TUI remains usable"
        self.report["directory_deadline_seconds"] = deadline
        self.report["fault"] = "SIGSTOP on the second owned bpd; SIGCONT before cleanup"
        self.report["boundary"] = "same-computer raw terminal fixtures with distinct private state directories, host identities, and tmux sockets; no remote enrollment or native model-turn claim"
        self.report["cleanup"] = {}
        self.report["missing_evidence"].extend(["independent-user TUI usability", "real remote packet-loss or blackhole latency", "repeated latency distribution under host load"])

    def binary_identity(self):
        builds = {}
        for name in ("bp", "bpd", "bpg"):
            settings = {}
            for line in command(["go", "version", "-m", str(self.root / "bin" / name)]).splitlines():
                parts = line.strip().split(None, 1)
                if len(parts) == 2 and parts[0] == "build" and "=" in parts[1]:
                    key, value = parts[1].split("=", 1)
                    settings[key] = value
            assert settings.get("vcs.revision") == self.expected_sha, "binary revision differs from the expected frozen implementation"
            assert settings.get("vcs.modified") == "false", "binary was built from a modified checkout"
            builds[name] = {key: settings.get(key) for key in ("vcs.revision", "vcs.modified", "GOOS", "GOARCH")}
        self.report["binary_build"] = builds
        return {"all_binary_revisions": self.expected_sha, "all_binary_worktrees_clean_at_build": True}

    def stopped(self):
        assert self.paused_home is not None and self.paused_home.daemon.poll() is None, "owned paused home exited"
        state = command(["ps", "-o", "stat=", "-p", str(self.paused_home.daemon.pid)])
        return "T" in state

    def resume_home(self):
        if self.paused_home is not None:
            home = self.paused_home
            home.daemon.send_signal(signal.SIGCONT)
            self.report["cleanup"]["paused_home_sigcont_sent"] = True
            wait_for(lambda: "T" not in command(["ps", "-o", "stat=", "-p", str(home.daemon.pid)]), timeout=3)
            self.report["cleanup"]["paused_home_resumed"] = True
            self.paused_home = None

    def silent_directory(self):
        self.paused_home = self.homes[1]
        assert self.paused_home.daemon.poll() is None, "second owned home exited before pause"
        self.paused_home.daemon.send_signal(signal.SIGSTOP)
        wait_for(self.stopped, timeout=3)
        argv = [self.bp, "--state", str(self.client), "--entry", str(self.socket), "directory"]
        start = time.monotonic()
        result = subprocess.run(argv, capture_output=True, text=True, timeout=self.deadline)
        elapsed = time.monotonic() - start
        assert result.returncode == 0, "actual bp directory failed during a silent-home fetch"
        assert elapsed <= self.deadline, "actual bp directory exceeded the bounded deadline"
        directory = json.loads(result.stdout)
        assert [s["id"] for s in directory["sessions"]] == [self.work[0]["id"]], "partial directory did not return exactly the surviving granted session"
        places = {p["host_id"]: p["state"] for p in directory["places"]}
        assert places == {self.work[0]["host_id"]: "reachable", self.work[1]["host_id"]: "unreachable"}, "partial directory confused the silent home with the healthy home or entry"
        assert all(not any(s["execution"]["runtime"].values()) for s in directory["sessions"]), "partial entry inventory exposed home runtime locators"
        assert not (self.client / "control.sock").exists(), "partial entry client started an unintended local home"
        assert self.stopped(), "silent home resumed before the directory assertion"
        return {"actual_bp_directory_invocations_during_this_check": 1, "elapsed_seconds": round(elapsed, 6), "maximum_seconds": self.deadline, "healthy_sessions": 1, "distinct_unreachable_homes": 1, "entry_returned_success": True, "silent_home_still_stopped": True, "home_runtime_locators_scrubbed": True}

    def surviving_tui(self):
        assert self.stopped(), "silent home resumed before TUI exercise"
        t = self.ui()
        t.expect("1 execution homes unreachable; entry remains connected.")
        visible = t.output.decode("utf-8", "replace")
        assert "OFFLINE / last observed inventory" not in visible and "Connection offline" not in visible, "TUI treated a silent home as an entry outage"
        self.select(t, 0)
        self.attach(t, 0)
        probe = b"silent-home surviving work usable"
        t.send(probe)
        wait_for(lambda: probe in bytes.fromhex(self.keys(0)["hex"]))
        other = self.homes[1].temp / "keys.json"
        assert not other.exists() or probe not in bytes.fromhex(json.loads(other.read_text())["hex"]), "surviving-home input crossed into the paused home"
        self.detach(t, 0)
        assert not self.place().get("pending_attachment"), "surviving-home detach left an unresolved acquisition"
        self.stable(0)
        silent = self.work[1]["execution"]["runtime"]
        assert self.homes[1].tmux("display-message", "-p", "-t", silent["pane"], "#{pane_pid}") == str(silent["pid"]), "silent home execution changed while its daemon was stopped"
        assert self.stopped(), "silent home resumed before TUI attach/input/detach completed"
        t.send("q")
        t.close()
        self.terminals.remove(t)
        return {"actual_tui_partial_home_label": True, "entry_offline_label": False, "healthy_work_selected": True, "actual_healthy_home_native_attach": True, "raw_fixture_input_received_at_only_the_healthy_home": True, "detach_returns_same_selection_and_clears_lease": True, "silent_home_stopped_for_entire_tui_exercise": True, "both_native_pane_pids_preserved": True}

    def restored_directory(self):
        self.resume_home()
        directory = wait_for(lambda: self.api("sessions"))
        assert sorted(s["id"] for s in directory["sessions"]) == sorted(s["id"] for s in self.work), "resumed home did not restore both original sessions"
        assert len(directory["places"]) == 2 and all(p["state"] == "reachable" for p in directory["places"]), "resumed home remained unavailable"
        for n in range(2):
            self.stable(n)
        return {"sigcont_restores_both_original_sessions": True, "both_homes_reachable": True, "original_execution_runtime_and_pids_preserved": True}

    def binary_unchanged(self):
        final = {name: hashlib.sha256((self.root / "bin" / name).read_bytes()).hexdigest() for name in ("bp", "bpd", "bpg")}
        assert final == self.report["binary_sha256"], "an executable changed during the acceptance run"
        self.report["binary_sha256_at_end"] = final
        self.report["repository_sha_at_end"] = command(["git", "rev-parse", "HEAD"], cwd=self.root)
        return {"all_binary_digests_unchanged": True}

    def run(self):
        try:
            self.check("frozen executable build identity", self.binary_identity)
            self.check("two owned executable homes and scoped entry setup", self.directory)
            self.check("single bounded directory fetch with SIGSTOP home", self.silent_directory)
            self.check("actual surviving-home TUI attach input and detach while second home stays stopped", self.surviving_tui)
            self.check("SIGCONT restores exact original home executions", self.restored_directory)
            self.check("executable digests unchanged throughout acceptance", self.binary_unchanged)
        finally:
            try:
                self.resume_home()
            finally:
                for t in self.terminals:
                    t.close()
                if self.gateway and self.gateway.poll() is None:
                    self.gateway.terminate()
                    try:
                        self.gateway.wait(timeout=5)
                    except subprocess.TimeoutExpired:
                        self.gateway.kill()
                        self.gateway.wait(timeout=5)
                for home in self.homes:
                    if home.daemon and home.daemon.poll() is None:
                        home.daemon.terminate()
                        try:
                            home.daemon.wait(timeout=5)
                        except subprocess.TimeoutExpired:
                            home.daemon.kill()
                            home.daemon.wait(timeout=5)
                    try:
                        home.tmux("kill-server")
                    except subprocess.CalledProcessError:
                        pass
                    shutil.rmtree(home.temp)
                self.report["cleanup"]["owned_terminal_home_and_entry_processes_stopped"] = True
                shutil.rmtree(self.temp)
                self.report["cleanup"]["owned_temporary_directories_removed"] = True
                self.report_path.parent.mkdir(parents=True, exist_ok=True)
                self.report_path.write_text(json.dumps(self.report, indent=2)+"\n")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", default=str(Path(__file__).resolve().parents[1]))
    parser.add_argument("--report", required=True)
    parser.add_argument("--expected-sha", default=FROZEN_IMPLEMENTATION)
    parser.add_argument("--deadline", type=float, default=4.5)
    args = parser.parse_args()
    assert 0 < args.deadline <= 4.5, "latency gate must stay bounded at 4.5 seconds or less"
    LatencyAcceptance(args.root, args.report, args.expected_sha, args.deadline).run()
