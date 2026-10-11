#!/usr/bin/env python3
"""Measure one compiler overlay against exact source, without adopting it.

Raw local receipts are retained privately. Only path-sanitized derivatives are
uploaded by the manual workflow. Every original capacity deadline is unchanged.
"""
import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import re
import signal
import subprocess
import tarfile
import time
import uuid

SOURCE = "4ef3c9afce086a07abd4646bd6c428abe8d43b90"
UTF8_SHA = "5362209587478feb6c3dc5367ed5f02c7789a8094736c42d305848ee1aa15247"
ORIGINAL = b"e.handleGrapheme(string(e.grapheme[0]), 1)"
REPLACEMENT = b"e.handleGrapheme(string(e.grapheme[:1]), 1)"
PACKAGE = "./internal/adapters/nativepty"
GATES = "^Test(SlowReaderDoesNotBlockNativeOutput|SlowViewerBurstTracksCompleteUnicodeTail)$"
CONTROLS = "^Test(ASCIIBatchMatchesBytewise|ASCIITailAfterWrapPreservingControls|ASCIITailCombinesAtActiveScrollMargin|CurrentScreenWithoutMainHistoryMatchesHistoryEnabled)$"


def digest(data):
    return hashlib.sha256(data).hexdigest()


def need(value, label):
    if not value:
        raise AssertionError(label)


def varint(data, offset):
    result = 0
    for shift in range(0, 70, 7):
        need(offset < len(data), "truncated_profile_varint")
        value = data[offset]
        offset += 1
        result |= (value & 127) << shift
        if not value & 128:
            return result, offset
    raise AssertionError("profile_varint_overflow")


def encode_varint(value):
    result = bytearray()
    while value > 127:
        result.append((value & 127) | 128)
        value >>= 7
    result.append(value)
    return bytes(result)


def sanitize_profile(data):
    # pprof Profile field6 is its shared string table. Rewrite absolute file
    # paths only; preserve every other protobuf field byte-for-byte, including
    # samples, values, stacks, labels, timing and string-table indices.
    with gzip.GzipFile(fileobj=io.BytesIO(data)) as stream:
        raw = stream.read((64 << 20) + 1)
    need(len(raw) <= 64 << 20, "profile_too_large")
    result, offset, rewrites, strings = bytearray(), 0, 0, 0
    while offset < len(raw):
        start = offset
        tag, offset = varint(raw, offset)
        field, wire = tag >> 3, tag & 7
        need(field > 0, "profile_invalid_field")
        if wire == 0:
            _, offset = varint(raw, offset)
        elif wire in (1, 5):
            offset += 8 if wire == 1 else 4
        elif wire == 2:
            size, offset = varint(raw, offset)
            end = offset + size
            need(end <= len(raw), "profile_truncated_field")
            if field == 6:
                strings += 1
                value = raw[offset:end].decode("utf-8")
                if value.startswith("/"):
                    value = "<owned-path>/" + PurePosixPath(value).name
                    replacement = value.encode()
                    result.extend(encode_varint(tag) + encode_varint(len(replacement)) + replacement)
                    rewrites += 1
                    offset = end
                    continue
            offset = end
        else:
            raise AssertionError("profile_unsupported_wire_type")
        need(offset <= len(raw), "profile_truncated_fixed_field")
        result.extend(raw[start:offset])
    need(strings > 0, "profile_string_table_missing")
    return gzip.compress(bytes(result), mtime=0), rewrites


def sanitized_log(data, root, output):
    text = data.decode("utf-8", errors="replace")
    for value, replacement in [(str(output), "<experiment>"), (str(root), "<checkout>"),
                               (os.environ.get("RUNNER_TEMP", ""), "<runner-temp>")]:
        if value:
            text = text.replace(value, replacement)
    text = re.sub(r"/(?:tmp|private|home|Users|Volumes)/[^\s\"'<>]+", "<owned-path>", text)
    return text.encode()


def owned_processes(nonce):
    """Read identity pins only for same-UID children carrying this fresh nonce."""
    marker = ("KEELMUX_OVERLAY_EXPERIMENT_NONCE=" + nonce).encode()
    found = {}
    for path in Path("/proc").iterdir():
        if not path.name.isdigit():
            continue
        try:
            if path.stat().st_uid != os.getuid():
                continue
            if marker not in (path / "environ").read_bytes().split(b"\0"):
                continue
            # field22 (starttime) pins against PID reuse; comm may contain spaces.
            fields = (path / "stat").read_text().rsplit(")", 1)[1].split()
            found[int(path.name)] = fields[19]
        except (FileNotFoundError, PermissionError, ProcessLookupError):
            continue
    return found


def clean_owned_processes(nonce):
    initial = owned_processes(nonce)
    for sig in (signal.SIGTERM, signal.SIGKILL):
        for pid, start in initial.items():
            try:
                # pidfd binds the live process before the identity recheck;
                # signaling never uses a saved, potentially recycled PID.
                descriptor = os.pidfd_open(pid)
            except ProcessLookupError:
                continue
            try:
                if owned_processes(nonce).get(pid) == start:
                    try:
                        signal.pidfd_send_signal(descriptor, sig)
                    except ProcessLookupError:
                        pass
            finally:
                os.close(descriptor)
        deadline = time.monotonic() + 1
        while owned_processes(nonce) and time.monotonic() < deadline:
            time.sleep(0.02)
    return {"unexpected_owned_processes_before_cleanup": len(initial),
            "exact_nonce_and_starttime_checked": True,
            "no_owned_processes_remained": not owned_processes(nonce)}


def execute(args):
    root = Path(__file__).resolve().parents[1]
    output = args.output.resolve()
    need(not output.exists(), "fresh_output_required")
    output.mkdir(mode=0o700, parents=True)
    raw, public, source = output / "raw", output / "public", output / "source"
    for path in (raw, public, source):
        path.mkdir(mode=0o700)
    report = {"schema": 1, "kind": "exact_source_compiler_overlay_investigation",
              "source_sha": SOURCE, "result": "fail", "commands": [], "model_calls": 0,
              "production_or_shared_module_edits": False, "original_gate_deadline_seconds": 8,
              "scope": "Dedicated hosted Linux amd64 worker; focused package, not full-suite or release qualification",
              "earlier_negatives": {
                  "main_source_log_sha256": "959fa7c37afe7f2ce8567e05a79f8f19a30950e32e44a5fd42e607d1ef50145a",
                  "hosted_original_gate_seconds": 8.12, "hosted_unicode_gate_seconds": 8.19,
                  "qemu_compile_failed_before_runtime": True,
                  "invalid_margin_panic_independent_of_overlay": True,
                  "trace_qualifies_valid_margins_only": True}}
    env = {k: v for k, v in os.environ.items()
           if not k.startswith("KEELMUX_") and k not in
           {"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GITHUB_TOKEN", "GH_TOKEN"}}
    nonce = uuid.uuid4().hex
    env.update(HOME=str(output / "home"), GOCACHE=str(output / "gocache"),
               GOMODCACHE=str(output / "modcache"), GOTOOLCHAIN="local", GOENV="off",
               GOFLAGS="-mod=readonly", CGO_ENABLED="1", GOMAXPROCS="2",
               KEELMUX_OVERLAY_EXPERIMENT_NONCE=nonce)
    (output / "home").mkdir(mode=0o700)

    def command(label, argv, timeout=90, cwd=source, extra=None):
        record = {"label": label, "argv": [str(x).replace(str(output), "<experiment>") for x in argv],
                  "outer_timeout_seconds": timeout}
        report["commands"].append(record)
        started = time.monotonic()
        stdout, stderr = raw / (label + ".stdout"), raw / (label + ".stderr")
        with stdout.open("xb") as out, stderr.open("xb") as err:
            stdout.chmod(0o600)
            stderr.chmod(0o600)
            process = subprocess.Popen(argv, cwd=cwd, env={**env, **(extra or {})},
                                       stdout=out, stderr=err, start_new_session=True)
            try:
                record["returncode"] = process.wait(timeout=timeout)
            except subprocess.TimeoutExpired:
                # This exact child owns the process group; detached product
                # children are separately authenticated by nonce/starttime.
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                process.wait(timeout=10)
                record["timeout"] = True
                record["returncode"] = 124
        record.update(wall_seconds=round(time.monotonic() - started, 3),
                      direct_child_reaped=process.returncode is not None,
                      stdout_sha256=digest(stdout.read_bytes()), stderr_sha256=digest(stderr.read_bytes()))
        # Complete cleanup before the next measured command. Detached helper
        # sessions retain the nonce; a lingering child fails this measurement
        # even if authenticated cleanup then succeeds.
        record["owned_cleanup"] = clean_owned_processes(nonce)
        if record["owned_cleanup"]["unexpected_owned_processes_before_cleanup"] or not record["owned_cleanup"]["no_owned_processes_remained"]:
            record["cleanup_failed_measurement"] = True
            record["returncode"] = record["returncode"] or 125
        print(label, record["returncode"], record["wall_seconds"], flush=True)
        return record["returncode"], stdout.read_bytes()

    try:
        need(os.environ.get("RUNNER_OS") == "Linux" and os.environ.get("RUNNER_ARCH") == "X64",
             "hosted_linux_amd64_required")
        code, goenv = command("toolchain", ["go", "env", "-json", "GOOS", "GOARCH", "GOVERSION", "CGO_ENABLED"], cwd=root)
        toolchain = json.loads(goenv)
        need(code == 0 and toolchain == {"CGO_ENABLED": "1", "GOARCH": "amd64", "GOOS": "linux", "GOVERSION": "go1.26.6"}, "toolchain_differs")
        report["toolchain"] = toolchain
        report["investigation_commit"] = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root).decode().strip()
        report["script_sha256"] = digest(Path(__file__).read_bytes())
        report["fixture_sha256"] = digest((root / "scripts/fixtures/nativepty_overlay_trace_test.go.txt").read_bytes())
        archive = subprocess.check_output(["git", "archive", "--format=tar", SOURCE], cwd=root, timeout=60)
        report["exact_source_archive_sha256"] = digest(archive)
        files = {}
        with tarfile.open(fileobj=io.BytesIO(archive)) as stream:
            for member in stream.getmembers():
                path = PurePosixPath(member.name)
                need(not path.is_absolute() and ".." not in path.parts, "source_path_invalid")
                if member.isdir():
                    continue
                need(member.isfile() and member.name not in files, "source_member_invalid")
                data = stream.extractfile(member).read()
                destination = source / member.name
                destination.parent.mkdir(parents=True, exist_ok=True)
                destination.write_bytes(data)
                destination.chmod(member.mode)
                files[member.name] = {"sha256": digest(data), "mode": member.mode}
        report["gate_source_files"] = {name: files[name] for name in
             ["internal/adapters/nativepty/screen_test.go", "internal/adapters/nativepty/throughput_test.go", "internal/adapters/nativepty/performance_test.go", "go.mod", "go.sum"]}
        code, _ = command("owned-vendor", ["go", "mod", "vendor"], timeout=180)
        need(code == 0, "vendor_failed")
        utf8 = source / "vendor/github.com/charmbracelet/x/vt/utf8.go"
        original = utf8.read_bytes()
        need(digest(original) == UTF8_SHA and original.count(ORIGINAL) == 1, "pinned_upstream_differs")
        changed = original.replace(ORIGINAL, REPLACEMENT)
        overlay = output / "vt-utf8-overlay.go"
        overlay.write_bytes(changed)
        overlay_json = output / "overlay.json"
        overlay_json.write_text(json.dumps({"Replace": {str(utf8): str(overlay)}}) + "\n")
        report["overlay"] = {"original_sha256": digest(original), "changed_sha256": digest(changed),
                              "original_expression": ORIGINAL.decode(), "replacement_expression": REPLACEMENT.decode(),
                              "shared_module_cache_mutated": False}
        fixture = source / "internal/adapters/nativepty/overlay_experiment_test.go"
        fixture.write_bytes((root / "scripts/fixtures/nativepty_overlay_trace_test.go.txt").read_bytes())
        env["GOFLAGS"] = "-mod=vendor"
        package_dir = source / "internal/adapters/nativepty"
        for variant in ("baseline", "overlay"):
            binary = output / (variant + ".test")
            compile_args = ["go", "test", "-c", "-race", "-p", "1", "-o", str(binary)]
            if variant == "overlay":
                compile_args += ["-overlay", str(overlay_json)]
            code, _ = command("compile-" + variant, compile_args + [PACKAGE], timeout=240)
            need(code == 0, "compile_failed:" + variant)
            report.setdefault("binaries", {})[variant] = digest(binary.read_bytes())
        # Execute both variants despite test failures; retain every outcome.
        for variant in ("baseline", "overlay"):
            binary = output / (variant + ".test")
            extra = {"GOFLAGS": "-mod=vendor" + (" -overlay=" + str(overlay_json) if variant == "overlay" else "")}
            command(variant + "-bench", [str(binary), "-test.run=^$", "-test.bench=^BenchmarkScreenASCIIBurst$",
                    "-test.benchtime=1x", "-test.count=3", "-test.timeout=90s",
                    "-test.cpuprofile=" + str(raw / (variant + ".cpu")),
                    "-test.memprofile=" + str(raw / (variant + ".mem"))], 100, package_dir, extra)
            command(variant + "-trace", [str(binary), "-test.run=^TestOverlayEquivalenceTrace$",
                    "-test.timeout=90s", "-test.v"], 100, package_dir,
                    {**extra, "KEELMUX_OVERLAY_TRACE": str(raw / (variant + "-trace.jsonl"))})
            command(variant + "-controls", [str(binary), "-test.run=" + CONTROLS,
                    "-test.timeout=90s", "-test.v"], 100, package_dir, extra)
            command(variant + "-original-gates", [str(binary), "-test.run=" + GATES,
                    "-test.count=3", "-test.timeout=90s", "-test.v"], 100, package_dir, extra)
            for kind in ("cpu", "mem"):
                profile = raw / (variant + "." + kind)
                if profile.exists():
                    for view in (["cpu"] if kind == "cpu" else ["alloc_objects", "alloc_space"]):
                        argv = ["go", "tool", "pprof", "-top", "-nodecount=15"]
                        if view != "cpu":
                            argv += ["-" + view]
                        command(variant + "-profile-" + view, argv + [str(binary), str(profile)], 30, package_dir)
        first, second = raw / "baseline-trace.jsonl", raw / "overlay-trace.jsonl"
        report["trace_equal"] = first.exists() and second.exists() and first.read_bytes() == second.read_bytes()
        report["trace_records"] = len(first.read_text().splitlines()) if first.exists() else 0
        need(report["trace_equal"] and report["trace_records"] > 1000, "trace_equivalence_failed")
        report["original_tracked_bytes_and_modes_unchanged"] = all(
            digest((source / name).read_bytes()) == value["sha256"] and
            (source / name).stat().st_mode & 0o777 == value["mode"] for name, value in files.items())
        need(report["original_tracked_bytes_and_modes_unchanged"] and utf8.read_bytes() == original,
             "source_or_upstream_modified")
        report["result"] = "pass" if all(record["returncode"] == 0 for record in report["commands"]) else "fail"
    except BaseException as error:
        report["failure"] = str(error)[:500]
    finally:
        if Path("/proc").is_dir():
            try:
                report["cleanup"] = clean_owned_processes(nonce)
                if report["cleanup"]["unexpected_owned_processes_before_cleanup"] or not report["cleanup"]["no_owned_processes_remained"]:
                    report["result"] = "fail"
            except BaseException as error:
                report["result"] = "fail"
                report["cleanup_failure"] = type(error).__name__
        report["raw_files"] = {p.name: digest(p.read_bytes()) for p in raw.iterdir() if p.is_file()}
        report["sanitized_profiles"] = {}
        for path in raw.iterdir():
            if not path.is_file():
                continue
            if path.suffix in (".cpu", ".mem"):
                try:
                    data, rewrites = sanitize_profile(path.read_bytes())
                    (public / path.name).write_bytes(data)
                    report["sanitized_profiles"][path.name] = {"raw_sha256": digest(path.read_bytes()),
                        "sanitized_sha256": digest(data), "absolute_path_strings_rewritten": rewrites,
                        "all_non_string_fields_and_string_indices_preserved": True}
                except BaseException as error:
                    report["result"] = "fail"
                    report["sanitization_failure"] = type(error).__name__
            else:
                (public / path.name).write_bytes(sanitized_log(path.read_bytes(), root, output))
        report["limits"] = ["Compiler overlay experiment; no adopted dependency or production change",
                            "Focused dedicated-runner capacity results do not supersede the retained full-suite failure",
                            "Valid-margin trace only; separate invalid-margin crash remains retained",
                            "No provider models, customer channels, credentials or installed release operations"]
        (raw / "receipt.json").write_text(json.dumps(report, indent=2) + "\n")
        (raw / "receipt.json").chmod(0o600)
        public_report = {**report, "private_original_receipt_sha256": digest((raw / "receipt.json").read_bytes())}
        def scrub(value):
            if isinstance(value, str):
                return sanitized_log(value.encode(), root, output).decode()
            if isinstance(value, dict):
                return {key: scrub(item) for key, item in value.items()}
            if isinstance(value, list):
                return [scrub(item) for item in value]
            return value
        (public / "receipt.json").write_text(json.dumps(scrub(public_report), indent=2) + "\n")
        print("result", report["result"], flush=True)
    return 0 if report["result"] == "pass" else 1


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    raise SystemExit(execute(parser.parse_args()))
