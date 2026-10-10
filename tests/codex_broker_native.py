#!/usr/bin/env python3
"""Opt-in owned Codex broker proof. Never submits terminal user input/model work."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import tempfile
import time


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--native-binary", required=True, type=Path)
    parser.add_argument("--report", required=True, type=Path)
    args = parser.parse_args()
    root = Path(__file__).resolve().parent.parent
    binary = args.native_binary
    if not binary.is_absolute() or str(binary) != os.path.normpath(str(binary)):
        parser.error("native binary must be a clean absolute path")
    state = Path(tempfile.mkdtemp(prefix="bp-broker-", dir="/tmp")).resolve()
    os.chmod(state, 0o700)
    report = {"schema": 1, "tested_sha": subprocess.check_output(
        ["git", "rev-parse", "HEAD"], cwd=root, text=True).strip(),
        "utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "platform": subprocess.check_output(["uname", "-sr"], text=True).strip(),
        "harness_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
        "go_helper_sha256": hashlib.sha256((root / "tests/codexbrokerprobe/main.go").read_bytes()).hexdigest(),
        "go_helper_files": {str(p.relative_to(root)): hashlib.sha256(p.read_bytes()).hexdigest()
                            for p in sorted((root / "tests/codexbrokerprobe").glob("*.go"))},
        "working_tree_dirty": bool(subprocess.check_output(["git", "status", "--porcelain"], cwd=root, text=True).strip()),
        "native_binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
        "auth_mode": "isolated HOME/CODEX_HOME, no copied auth, no-auth loopback port 1 provider",
        "model_turns_submitted": 0,
        "missing_evidence": ["product bpd app-server launcher", "authenticated model work",
                             "nonempty history", "native approvals", "restart recovery",
                             "remote hosts", "writer handover"]}
    report.update(source_fingerprint(root))
    try:
        subprocess.run(["go", "build", "-race", "-o", str(state / "probe"),
                        "./tests/codexbrokerprobe"], cwd=root, check=True,
                       capture_output=True, text=True, timeout=90)
        child = subprocess.Popen([str(state / "probe"), "-native", str(binary),
                                  "-state", str(state)], cwd=root, stdout=subprocess.PIPE,
                                 stderr=subprocess.PIPE, text=True, start_new_session=True)
        try:
            stdout, stderr = child.communicate(timeout=70)
        except subprocess.TimeoutExpired:
            child.send_signal(signal.SIGTERM)  # Go cancels and runs owned cleanup.
            try:
                stdout, stderr = child.communicate(timeout=5)
            except subprocess.TimeoutExpired:
                cleanup_owned_children(child, state)
                if child.poll() is None:
                    os.killpg(child.pid, signal.SIGKILL)
                child.communicate(timeout=5)
                raise
        # The helper emits only finite, sanitized evidence. Raw screens/RPCs remain
        # in this owned private state; neither subprocess stderr nor paths are published.
        (state / "helper.stderr").write_text(stderr)
        os.chmod(state / "helper.stderr", 0o600)
        result = json.loads(stdout)
        report["proof"] = result
        report["result"] = "pass" if child.returncode == 0 and result.get("passed") else "fail"
    except (subprocess.SubprocessError, ValueError, OSError) as exc:
        report.update(result="fail", harness_error=type(exc).__name__)
    args.report.parent.mkdir(parents=True, exist_ok=True)
    args.report.write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps({"result": report["result"], "report": str(args.report),
                      "private_failure_state": str(state), "model_turns_submitted": 0}))
    return 0 if report["result"] == "pass" else 1


def cleanup_owned_children(probe, state):
    # Only while the exact spawned parent is still unreaped/alive can a receipt
    # plus live parent PID prove an owned child. Never kill by a saved PID alone.
    if probe.poll() is not None:
        return
    for receipt in state.glob("owned-child-*.json"):
        try:
            saved = json.loads(receipt.read_text())
            pid = saved["pid"]
            current = subprocess.check_output(["/bin/ps", "-p", str(pid), "-o", "ppid=",
                                               "-o", "lstart=", "-o", "stat="],
                                              text=True, env={"PATH": "/usr/bin:/bin", "LC_ALL": "C"}).split()
            if (saved["parent_pid"] != probe.pid or len(current) != 7 or
                    int(current[0]) != probe.pid or "Z" in current[-1] or
                    saved["start"] != "darwin-lstart:" + " ".join(current[1:6])):
                continue
            os.kill(pid, signal.SIGKILL)
        except (OSError, ValueError, KeyError, subprocess.SubprocessError):
            pass


def source_fingerprint(root):
    # Fingerprint actual compiled/test source, including allowed new helpers.
    # A dirty flag alone cannot attribute dirty shared implementation changes.
    names = subprocess.check_output(["git", "ls-files", "-z", "--cached", "--others",
                                     "--exclude-standard"], cwd=root).decode().split("\0")
    source = {}
    for name in sorted(set(names)):
        if name and (name.endswith(".go") or (name.startswith("tests/") and name.endswith(".py"))
                     or name in ("go.mod", "go.sum", "Makefile")):
            source[name] = hashlib.sha256((root / name).read_bytes()).hexdigest()
    digest = hashlib.sha256(json.dumps(source, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
    dirty = subprocess.check_output(["git", "diff", "--binary", "HEAD", "--", "cmd", "internal",
                                     "tests", "go.mod", "go.sum", "Makefile"], cwd=root)
    return {"source_tree_sha256": digest, "source_file_count": len(source),
            "dirty_tracked_source_diff_sha256": hashlib.sha256(dirty).hexdigest()}


if __name__ == "__main__":
    raise SystemExit(main())
