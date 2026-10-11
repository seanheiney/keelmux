#!/usr/bin/env python3
"""Two exact revisions, one runner, unchanged tests; raw job logs retain proof."""
import glob
import hashlib
import json
import os
from pathlib import Path
import platform
import stat
import subprocess
import sys
import tempfile
import threading
import time

REVISIONS = (
    ("parent", "1e429fa8991edc41df8002958b9b439b3dd69ab8", "41f91ab851c5cf0d89bf8d95f55b9a1ef5747256"),
    ("head", "56e80407cd5000f6bbc7dd48fac5b678002447e2", "d31caedf32f3890f8c3b314525198fc08a79d093"),
)
COMMAND = ["go", "test", "-race", "-count=1", "-p", "1", "-tags", "library_registry", "-v",
           "-run", "^Test(SlowReaderDoesNotBlockNativeOutput|SlowViewerBurstTracksCompleteUnicodeTail)$",
           "./internal/adapters/nativepty"]
FILES = ("screen.go", "filter.go", "ptyio.go", "server.go", "protocol.go",
         "screen_test.go", "throughput_test.go", "backend_test.go")


def command(argv, cwd=None):
    p = subprocess.run(argv, cwd=cwd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=True)
    return p.stdout.decode("utf-8").strip()


def candidates():
    return set(glob.glob(str(Path(tempfile.gettempdir()) / "km-helper-*" / "keelmux-supervisor")))


def watch_helpers(before, done, records):
    # One stable file hash per newly materialized TestMain helper. No helper
    # launch, replacement, environment change, process signal or copied binary.
    seen = set()
    while not done.wait(0.1):
        for name in candidates() - before - seen:
            try:
                a = os.lstat(name)
                if not stat.S_ISREG(a.st_mode) or a.st_uid != os.getuid() or a.st_size == 0:
                    continue
                h = hashlib.sha256()
                with open(name, "rb") as f:
                    for chunk in iter(lambda: f.read(1 << 20), b""):
                        h.update(chunk)
                b = os.lstat(name)
                identity = lambda s: (s.st_dev, s.st_ino, s.st_size, s.st_mtime_ns)
                if identity(a) != identity(b):
                    continue
                records.append({"sha256": h.hexdigest(), "bytes": b.st_size,
                                "stable_file_during_hash": True,
                                "scope": "TestMain helper path observation; not loaded-image attestation"})
                seen.add(name)
            except OSError:
                pass  # TestMain removes its own temporary binary on exit.


def clean_source(path, sha, tree):
    if command(["git", "rev-parse", "HEAD"], path) != sha:
        raise RuntimeError("checkout source SHA mismatch")
    if command(["git", "rev-parse", "HEAD^{tree}"], path) != tree:
        raise RuntimeError("checkout tracked tree mismatch")
    if command(["git", "status", "--porcelain"], path):
        raise RuntimeError("checkout is not clean")


def run_revision(path, label, sha, tree, output):
    record = {"label": label, "source_sha": sha, "tracked_tree": tree, "command": COMMAND}
    try:
        clean_source(path, sha, tree)
        record["source_file_sha256"] = {
            name: hashlib.sha256((path / "internal/adapters/nativepty" / name).read_bytes()).hexdigest()
            for name in FILES
        }
        state_before = set(glob.glob("/tmp/km-n-*"))
        helpers_before = candidates()
        directories_before = set(glob.glob(str(Path(tempfile.gettempdir()) / "km-helper-*")))
        done = threading.Event()
        record["helper_file_observations"] = []
        observer = threading.Thread(target=watch_helpers,
                                    args=(helpers_before, done, record["helper_file_observations"]), daemon=True)
        observer.start()
        print("PAIRED_PHASE_BEGIN=" + json.dumps({"label": label, "source_sha": sha, "tracked_tree": tree,
                                                 "command": COMMAND}), flush=True)
        started = time.monotonic()
        log = output / (label + ".log")
        digest = hashlib.sha256()
        total = 0
        try:
            p = subprocess.Popen(COMMAND, cwd=path, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
            with log.open("wb") as retained:
                while True:
                    data = p.stdout.read(4096)
                    if not data:
                        break
                    retained.write(data)
                    digest.update(data)
                    total += len(data)
                    sys.stdout.buffer.write(data)
                    sys.stdout.buffer.flush()
            record["exit_code"] = p.wait()
        finally:
            done.set()
            observer.join(timeout=1)
        record.update(elapsed_seconds=round(time.monotonic() - started, 3),
                      log_sha256=digest.hexdigest(), log_bytes=total)
        clean_source(path, sha, tree)
        record["source_unchanged"] = True
        record["cleanup"] = {
            "new_fixture_state_directories_remaining": len(set(glob.glob("/tmp/km-n-*")) - state_before),
            "new_helper_directories_remaining": len(set(glob.glob(str(Path(tempfile.gettempdir()) / "km-helper-*"))) - directories_before),
            "scope": "Unchanged tests own process/stream cleanup; directory absence observed, process exit not independently attested",
        }
        record["result"] = "pass" if record["exit_code"] == 0 else "fail"
    except Exception as e:
        record.update(result="fail", diagnostic_error=type(e).__name__ + ": " + str(e))
    print("PAIRED_PHASE_RESULT=" + json.dumps(record, sort_keys=True), flush=True)
    return record


def main():
    if len(sys.argv) != 3 or platform.system() != "Darwin":
        raise SystemExit("expected two owned checkouts on macOS")
    output = Path(tempfile.mkdtemp(prefix="keelmux-paired-throughput-", dir=os.environ["RUNNER_TEMP"]))
    metadata = output / "runtime.go"
    metadata.write_text('package main\nimport("encoding/json";"os";"runtime")\n'
                        'func main(){json.NewEncoder(os.Stdout).Encode(map[string]any{'
                        '"version":runtime.Version(),"goos":runtime.GOOS,"goarch":runtime.GOARCH,'
                        '"num_cpu":runtime.NumCPU(),"gomaxprocs":runtime.GOMAXPROCS(0)})}\n')
    report = {
        "version": 1, "scope": "Two unchanged finite provider-free PTY race tests on one macOS runner; no capacity or release qualification",
        "platform": platform.platform(), "uname": command(["uname", "-a"]),
        "cpu": {name: command(["sysctl", "-n", name]) for name in ("hw.model", "hw.ncpu", "hw.physicalcpu", "hw.logicalcpu")},
        "go_runtime": json.loads(command(["go", "run", "-race", str(metadata)])),
        "gomaxprocs_env": os.environ.get("GOMAXPROCS"),
        "observer_limit": "Passive helper file hashing adds a small unmeasured common diagnostic cost; hash is not a loaded image proof",
        "model_turns": 0, "provider_launches": 0,
    }
    print("PAIRED_RUNNER=" + json.dumps(report, sort_keys=True), flush=True)
    report["checks"] = [run_revision(Path(arg).resolve(), *identity, output)
                        for arg, identity in zip(sys.argv[1:], REVISIONS)]
    report["result"] = "pass" if all(x["result"] == "pass" for x in report["checks"]) else "fail"
    encoded = (json.dumps(report, indent=2, sort_keys=True) + "\n").encode()
    (output / "receipt.json").write_bytes(encoded)
    print("PAIRED_RECEIPT_SHA256=" + hashlib.sha256(encoded).hexdigest(), flush=True)
    print("PAIRED_SUMMARY=" + json.dumps(report, sort_keys=True), flush=True)
    return 0 if report["result"] == "pass" else 1


if __name__ == "__main__":
    raise SystemExit(main())
