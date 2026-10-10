#!/usr/bin/env python3
"""Refresh owned TUI examples from a clean, frozen source build.

Uses the existing actual-PTY fixture harnesses. No daemon, native provider,
external service, or model is contacted. PNGs rasterize interpreted PTY cells.
Historical captures remain unchanged.
"""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
FIXTURES = ROOT / "internal/tui/testdata"


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def source_inputs():
    paths = subprocess.check_output(["git", "ls-files", "-z"], cwd=ROOT).split(b"\0")
    return {p.decode(): digest(ROOT / p.decode()) for p in paths if p and
            (p.endswith(b".go") or p in (b"go.mod", b"go.sum") or p.startswith(b"third_party/") or
             p in (b"docs/site/capture_runtime.py", b"internal/tui/testdata/capture.py", b"internal/tui/testdata/capture_channels.py"))}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    if subprocess.check_output(["git", "status", "--porcelain", "--untracked-files=no"], cwd=ROOT):
        raise SystemExit("commit source changes before refreshing examples")
    source = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
    before = source_inputs()
    binary, output = Path(args.binary).resolve(), Path(args.output).resolve()
    if not (ROOT / "docs/site/evidence") in output.parents:
        raise SystemExit("capture output must be beneath docs/site/evidence")
    if output.exists():
        raise SystemExit("use a fresh output directory; historical evidence is not overwritten")
    output.mkdir(parents=True)
    sys.path.insert(0, str(FIXTURES))
    import capture
    original = capture.sessions
    sizes = ((80, 24), (120, 32), (40, 14))
    receipts = []
    for scenario, backend, state in (
            ("native", "native", "running"), ("legacy", "tmux", "running"),
            ("missing", "native", "missing"), ("uncertain", "native", "outcome_uncertain"),
            ("unknown", "", "running")):
        def sessions():
            rows = original()
            for row in rows:
                row["execution"]["runtime"] = {"backend": "native"}
            rows[1]["execution"]["runtime"] = {"backend": "tmux"}
            rows[0]["execution"]["runtime"] = {"backend": backend}
            rows[0]["execution"]["state"] = state
            if state != "running":
                rows[0]["capabilities"]["native_attach"] = False
                rows[0]["capabilities"]["read_only_attach"] = False
            return rows
        capture.sessions = sessions
        for width, height in sizes:
            prefix = "%s-%dx%d" % (scenario, width, height)
            receipt = capture.capture(str(binary), output / prefix, width, height, "inventory")
            text = (output / (prefix + ".txt")).read_text()
            expected = "Runtime: " + ("legacy tmux" if backend == "tmux" else backend or "unknown") + " / " + ("uncertain" if state == "outcome_uncertain" else state)
            if expected not in text:
                raise AssertionError("runtime state absent from actual PTY: " + prefix)
            if not (output / (prefix + ".png")).is_file():
                raise AssertionError("Pillow is required to refresh PNG examples")
            receipt.update(scenario=scenario, prefix=prefix)
            receipts.append(receipt)
    # Same frozen executable also refreshes authored-inbox/destination/reply and
    # lost-local-response examples, without sending to a real external provider.
    capture.sessions = sessions  # Last scenario keeps missing backend explicit.
    import capture_channels
    channels = output / "channels"
    channels.mkdir()
    channel_receipts = [capture_channels.capture(str(binary), channels, w, h) for w, h in sizes]
    if before != source_inputs():
        raise AssertionError("source inputs changed during capture; evidence is invalid")
    files = {str(p.relative_to(output)): digest(p) for p in output.rglob("*") if p.is_file()}
    receipt = {
        "source_commit": source, "source_inputs_sha256": before,
        "binary_sha256": digest(binary), "capture_script_sha256": digest(Path(__file__)),
        "scope": "Actual owned PTY with deterministic fixture inventory/notes; PNGs rasterize interpreted cells, not native Terminal screenshots",
        "native_provider_launches": 0, "external_service_posts": 0, "model_turns": 0,
        "switcher_captures": receipts, "channel_captures": channel_receipts,
        "files_sha256": files,
        "missing_proof": ["Real native-provider behavior", "live external chat delivery", "packaged release acceptance", "saved attachment recovery PTY screenshot (covered by source tests)"],
    }
    (output / "receipt.json").write_text(json.dumps(receipt, indent=2) + "\n")
    print(json.dumps({"source_commit": source, "switcher_captures": len(receipts), "channel_captures": sum(len(r["captures"]) for r in channel_receipts), "output": str(output)}))


if __name__ == "__main__":
    main()
