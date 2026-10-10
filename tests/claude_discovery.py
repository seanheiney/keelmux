#!/usr/bin/env python3
"""Opt-in native Claude slash-selector proof; never submits a command.

Requires a separately authorized project skill installation in a workspace
already trusted by Claude. Installation and safe removal are separate steps.
"""
import argparse
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import sys
import time

from native_acceptance import Acceptance, Terminal, wait_for


def run(root, report, workspace):
    a = Acceptance(root, report, workspace)
    script = Path(__file__).resolve()
    a.report.update({
        "harness_sha256": hashlib.sha256(script.read_bytes()).hexdigest(),
        "native_core_harness_sha256": hashlib.sha256(Path(a.script).read_bytes()).hexdigest(),
        "claim": "Claude project skill discoverable in native slash selector only",
        "missing_evidence": ["native skill instruction loading and invocation", "authenticated model turn and verified effective account/model", "user-scope skill discovery"],
    })
    bundle = a.native_workspace / ".claude/skills/backplane"
    description = "Explicitly link this exact managed native terminal"
    try:
        assert bundle.is_dir(), "authorized project bundle must already be installed"
        skill = (bundle / "SKILL.md").read_text()
        assert "disable-model-invocation: true" in skill, "explicit-only Claude skill metadata missing"
        a.report["bundle"] = {
            "scope": "project", "workspace": str(a.native_workspace),
            "files": {str(p.relative_to(bundle)): hashlib.sha256(p.read_bytes()).hexdigest() for p in (bundle / "SKILL.md", bundle / "scripts/link.sh")},
        }
        a.start_daemon()
        receipt = a.temp / "claude.link.json"
        s, request = a.launch("Claude selector acceptance", "claude", [sys.executable, a.script, "--child", a.bp, str(a.state), str(receipt), "claude"], workspace=a.native_workspace)
        linked = wait_for(lambda: json.loads(receipt.read_text()) if receipt.exists() else None)
        assert linked["session_id"] == s["id"] and linked["execution_id"] == s["execution"]["id"], "inherited exact link did not match launched terminal"
        req = a.lease(s, "claude-selector")
        grant = a.api("attach", req)
        t = Terminal(grant["argv"], str(a.native_workspace))
        a.terminals.append(t)
        def composer():
            text = a.pane(s)
            setup = any(word in text.lower() for word in ("do you trust", "trust this", "choose a theme"))
            return text if "❯" in text and not setup else None
        wait_for(composer)
        t.send("/backplane")
        selected = wait_for(lambda: a.pane(s) if description in a.pane(s) else None)
        skill_rows = [line.strip() for line in selected.splitlines() if "/backplane" in line or description in line]
        assert any("/backplane" in row for row in skill_rows), "native skill suggestion absent"
        a.report["checks"].append({"name": "Claude native project skill selector", "kind": "native_program", "result": "pass", "detail": {"selector_rows": skill_rows, "discoverable": "observed_in_native_selector", "instruction_loaded": "not_proven", "invoked": "not_run", "enter_sent": False, "model_prompt_submitted": False, "link_method": "inherited_helper_before_native_exec", "native_login_config_changes": False}})
        t.send(b"\x1b")
        t.pump(0.7)
        t.send(b"\x03")
        wait_for(composer)
        t.send("/rc")
        t.pump(2)
        native = a.pane(s)
        rc_rows = [line.strip() for line in native.splitlines() if "/rc" in line or "remote control" in line.lower()]
        a.report["checks"].append({"name": "native rc autocomplete observation", "kind": "native_program", "result": "observed", "detail": {"selector_rows": rc_rows, "remote_control_menu_description_visible": "remote control" in native.lower(), "alias_invocation": "not_run_unknown", "enter_sent": False, "external_connectivity_opened": False}})
        executable = Path(shutil.which("claude")).resolve()
        native_bytes = executable.read_bytes()
        a.report["native_alias_definition"] = {
            "kind": "installed_native_code_observation",
            "executable": str(executable),
            "sha256": hashlib.sha256(native_bytes).hexdigest(),
            "remote_control_rc_alias_present": b'name:"remote-control",aliases:["rc"]' in native_bytes,
            "runtime_eligibility_and_alias_invocation": "unknown_not_run",
        }
        t.send(b"\x1b")
        t.pump(0.7)
        t.send(b"\x03")
        t.send(b"\x07")
        t.send(b"d")
        wait_for(lambda: not a.tmux("list-clients", "-F", "#{client_readonly}"))
        a.api("release", dict(req, token=grant["lease"]["token"]), status=204)
        print("PASS Claude native project skill selector; rc alias invocation remains unknown", flush=True)
    except Exception as e:
        a.report["checks"].append({"name": "Claude native project skill and rc selector", "kind": "native_program", "result": "fail", "detail": {"error": str(e)}})
        # Retain only menu-related lines for diagnosis; no native transcript.
        if a.sessions:
            try:
                lines = a.pane(a.sessions[0]).splitlines()
                print(json.dumps({"menu_rows": [line.strip() for line in lines if "backplane" in line.lower() or "/rc" in line or "remote control" in line.lower()]}), flush=True)
            except Exception:
                pass
        raise
    finally:
        for t in a.terminals:
            t.close()
        if a.daemon:
            a.daemon.terminate()
            try:
                a.daemon.wait(timeout=5)
            except subprocess.TimeoutExpired:
                a.daemon.kill()
                a.daemon.wait()
        try:
            a.tmux("kill-server")
        except subprocess.CalledProcessError:
            pass
        a.report_path.parent.mkdir(parents=True, exist_ok=True)
        a.report_path.write_text(json.dumps(a.report, indent=2)+"\n")
        shutil.rmtree(a.temp)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", default=str(Path(__file__).resolve().parents[1]))
    parser.add_argument("--report", required=True)
    parser.add_argument("--native-workspace", required=True, help="already trusted workspace with authorized project bundle")
    args = parser.parse_args()
    run(args.root, args.report, args.native_workspace)
