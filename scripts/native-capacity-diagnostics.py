#!/usr/bin/env python3
"""Bounded exact-source capacity investigation; only public/ is uploadable.

Raw logs, executables, profiles, state and paths stay in private/. A failed
original gate remains a failed investigation even if later profiles complete.
No process census or detached-child cleanup proof is claimed by this driver.
"""
import argparse
import hashlib
import json
import math
import os
from pathlib import Path
import platform
import re
import resource
import shutil
import signal
import subprocess
import time

BASELINE = "732d9cc63521fda778ff657f829d079238ad451d"
BASELINE_TREE = "ee3b7d24ec224850444f6983c3fe0ed4d46103bc"
PACKAGE = "./internal/adapters/nativepty"
NATIVE = "internal/adapters/nativepty/"
GATE_NAMES = ("TestSlowReaderDoesNotBlockNativeOutput",
              "TestSlowViewerBurstTracksCompleteUnicodeTail")
GATES = "^(" + "|".join(GATE_NAMES) + ")$"
BENCHMARK = "BenchmarkScreenASCIIBurst"
ALLOWED_DIFF = frozenset(NATIVE + name for name in (
    "server.go", "backend_test.go", "throughput_test.go", "capacity_disabled.go",
    "capacity_diagnostics.go", "capacity_diagnostics_test.go",
    "capacity_fixture_disabled_test.go", "capacity_fixture_test.go")) | frozenset((
    ".github/workflows/native-capacity.yml", ".github/workflows/native-capacity-diagnostics.yml",
    "scripts/native-capacity-diagnostics.py", "docs/build/STATUS.md", "docs/build/TASKS.md"))
IDENTITY_FILES = ("go.mod", "go.sum", NATIVE + "screen.go", NATIVE + "filter.go",
                  NATIVE + "screen_test.go", NATIVE + "performance_test.go")
DIAG_KEYS = frozenset(("version", "original_failure", "parent_elapsed_ns",
    "stage_first_seen_ns", "marker", "pending", "telemetry", "telemetry_complete_records",
    "telemetry_last", "telemetry_partial_record", "post_failure_snapshot", "snapshot_cursor",
    "tail_present", "post_failure_inspect", "child_alive", "outcome", "bytes",
    "sequence", "elapsed_ns", "checkpoint", "read_calls", "read_bytes", "screen_calls",
    "screen_bytes", "screen_total_ns", "screen_max_ns", "replies_generated_bytes",
    "reply_write_calls", "reply_write_bytes", "reply_write_ns", "reply_error_class",
    "read_ns", "last_read_outcome", "screen_ns", "reply_attempted_bytes",
    "reply_successful_bytes", "last_reply_outcome"))
DIAG_ENUMS = frozenset(("start", "screen", "reply", "close", "none", "success", "timeout",
                       "canceled", "closed", "missing", "permission", "other", "invalid", "not_requested"))
ERROR_ENUMS = frozenset(("success", "timeout", "canceled", "closed", "missing", "permission", "other"))
RECORD_KEYS = frozenset(("version", "sequence", "elapsed_ns", "checkpoint", "read_calls", "read_bytes",
    "read_ns", "last_read_outcome", "screen_calls", "screen_bytes", "screen_ns", "screen_max_ns",
    "replies_generated_bytes", "reply_write_calls", "reply_attempted_bytes", "reply_successful_bytes",
    "reply_write_ns", "last_reply_outcome"))
DECIMAL = r"[0-9]+(?:\.[0-9]+)?"
PROFILE_REASONS = {"profile_top_row_unsupported": 1, "profile_path_in_symbol": 2,
    "profile_summary_missing_or_excess": 3, "numeric_column_invalid": 4,
    "numeric_column_not_finite": 5, "numeric_unit_column_invalid": 6,
    "profile_total_column_invalid": 7, "profile_shown_total_column_invalid": 8,
    "profile_total_invalid": 9}


def need(condition, code):
    if not condition:
        raise ValueError(code)


def profile_reason(error, phase_code):
    """Only fixed validator categories leave this boundary, never exception text."""
    if type(phase_code) is not int:
        return 0
    if phase_code in (1, 2):
        if type(error) is UnicodeDecodeError:
            return 10
        if type(error) is ValueError and len(error.args) == 1 and type(error.args[0]) is str:
            return PROFILE_REASONS.get(error.args[0], 0)
    if phase_code == 3 and type(error) is ValueError:
        return 11
    return 0


def sha(data):
    return hashlib.sha256(data).hexdigest()


def file_binding(path):
    need(path.is_file() and not path.is_symlink(), "artifact_not_regular")
    data = path.read_bytes()
    return {"sha256": sha(data), "bytes": len(data)}


def write_json(path, value):
    path.write_text(json.dumps(value, sort_keys=True, indent=2, allow_nan=False) + "\n")


def finite_number(value):
    need(isinstance(value, str) and re.fullmatch(DECIMAL, value), "numeric_column_invalid")
    parsed = float(value)
    need(math.isfinite(parsed) and parsed >= 0, "numeric_column_not_finite")
    return parsed


def finite_unit_column(value):
    match = re.fullmatch("(" + DECIMAL + r")(?:ns|us|ms|s|B|kB|MB|GB)?", value)
    need(match is not None, "numeric_unit_column_invalid")
    finite_number(match[1])
    return value


def safe_text(value, limit=256):
    need(isinstance(value, str) and len(value.encode()) <= limit and
         re.fullmatch(r"[a-zA-Z0-9_.+ ():,/@=-]*", value) is not None, "metadata_text_invalid")
    return value


def parse_benchmark(data):
    rows = re.findall(rb"^BenchmarkScreenASCIIBurst(?:-(\d+))?\s+(\d+)\s+"
        rb"([0-9.]+) ns/op\s+([0-9.]+) MB/s\s+(\d+) B/op\s+(\d+) allocs/op\s*$",
        data, re.M)
    need(len(rows) == 1, "benchmark_row_missing_or_duplicate")
    cpu, count, ns, mb, space, allocs = rows[0]
    need(int(count) == 3 and math.isfinite(float(ns)) and math.isfinite(float(mb)) and
         float(ns) > 0 and float(mb) > 0, "benchmark_measurement_invalid")
    return {"name": BENCHMARK, "measured_iterations": 3, "bytes_per_iteration": 2097152,
        "ns_per_op": float(ns), "mb_per_second": float(mb), "bytes_per_op": int(space),
        "allocations_per_op": int(allocs), "benchmark_gomaxprocs": int(cpu) if cpu else None,
        "profile_scope": "process_including_calibration_setup_and_teardown"}


def parse_gates(data):
    result = []
    for name in GATE_NAMES:
        runs = re.findall(rb"^=== RUN   " + name.encode() + rb"\s*$", data, re.M)
        rows = re.findall(rb"^--- (PASS|FAIL|SKIP): " + name.encode() + rb" \((" + DECIMAL.encode() + rb")s\)\s*$", data, re.M)
        need(len(runs) == 1 and len(rows) == 1 and rows[0][0] != b"SKIP", "native_gate_missing_or_replayed")
        result.append({"name": name, "result": rows[0][0].decode().lower(),
            "test_wall_seconds": finite_number(rows[0][1].decode()), "original_payload_bytes": 2097152,
            "original_deadline_seconds": 8, "assertions": "unchanged_original_test"})
    return result


def validate_diagnostic(value, depth=0):
    need(depth <= 4, "diagnostic_depth")
    if isinstance(value, dict):
        need(len(value) <= 40 and all(k in DIAG_KEYS for k in value), "diagnostic_unknown_field")
        return {k: validate_diagnostic(v, depth + 1) for k, v in value.items()}
    if isinstance(value, list):
        need(len(value) <= 4, "diagnostic_array_bound")
        return [validate_diagnostic(v, depth + 1) for v in value]
    if value is None or isinstance(value, bool):
        return value
    if isinstance(value, int):
        need(0 <= value <= (1 << 63) - 1, "diagnostic_number_bound")
        return value
    need(isinstance(value, str) and value in DIAG_ENUMS, "diagnostic_non_numeric_content")
    return value


def parse_diagnostics(data):
    def unique(pairs):
        result = {}
        for key, value in pairs:
            need(key not in result, "diagnostic_duplicate_field")
            result[key] = value
        return result
    records = []
    for line in data.splitlines():
        prefix = b"native_capacity_diagnostic="
        if prefix in line:
            raw = line.split(prefix, 1)[1]
            need(len(raw) <= 65536, "diagnostic_record_bound")
            record = validate_diagnostic(json.loads(raw, object_pairs_hook=unique))
            validate_fixture_record(record)
            records.append(record)
    need(1 <= len(records) <= 4, "diagnostic_records_missing_or_excess")
    return records


def validate_fixture_record(record):
    required = {"version", "original_failure", "parent_elapsed_ns", "stage_first_seen_ns", "marker",
        "pending", "telemetry", "telemetry_complete_records", "telemetry_partial_record",
        "post_failure_snapshot", "snapshot_cursor", "tail_present", "post_failure_inspect", "child_alive"}
    need(isinstance(record, dict) and required <= set(record) <= required | {"telemetry_last"}, "diagnostic_fields_invalid")
    need(type(record["version"]) is int and record["version"] == 1 and
         type(record["original_failure"]) is bool and type(record["telemetry_partial_record"]) is bool,
         "diagnostic_header_invalid")
    def integer(value, upper=(1 << 63) - 1):
        need(type(value) is int and 0 <= value <= upper, "diagnostic_integer_invalid")
    integer(record["parent_elapsed_ns"])
    need(isinstance(record["stage_first_seen_ns"], list) and len(record["stage_first_seen_ns"]) == 4, "diagnostic_stages_invalid")
    for value in record["stage_first_seen_ns"]: integer(value)
    for name, bound in (("marker", 8), ("pending", 8), ("telemetry", (128 << 10) + 1)):
        value = record[name]
        need(isinstance(value, dict) and set(value) == {"outcome", "bytes"} and
             type(value["outcome"]) is str and value["outcome"] in ERROR_ENUMS | {"invalid"},
             "diagnostic_file_observation_invalid")
        integer(value["bytes"], bound)
    integer(record["telemetry_complete_records"], 128)
    need((record["telemetry_complete_records"] > 0) == ("telemetry_last" in record), "diagnostic_counter_presence_invalid")
    if "telemetry_last" in record:
        last = record["telemetry_last"]
        need(isinstance(last, dict) and set(last) == RECORD_KEYS, "diagnostic_counter_fields_invalid")
        for key, value in last.items():
            if key not in ("checkpoint", "last_read_outcome", "last_reply_outcome"): integer(value)
        need(last["version"] == 1 and last["sequence"] == record["telemetry_complete_records"] and
             type(last["checkpoint"]) is str and last["checkpoint"] in {"start", "screen", "reply", "close"} and
             type(last["last_read_outcome"]) is str and last["last_read_outcome"] in ERROR_ENUMS | {"none"} and
             type(last["last_reply_outcome"]) is str and last["last_reply_outcome"] in ERROR_ENUMS | {"none"},
             "diagnostic_counter_invalid")
    for name in ("post_failure_snapshot", "post_failure_inspect"):
        need(type(record[name]) is str and record[name] in ERROR_ENUMS | {"not_requested"},
             "diagnostic_observation_outcome_invalid")
    cursor, tail, alive = record["snapshot_cursor"], record["tail_present"], record["child_alive"]
    if cursor is not None: integer(cursor)
    need(tail is None or type(tail) is bool, "diagnostic_tail_type_invalid")
    need(alive is None or type(alive) is bool, "diagnostic_alive_type_invalid")
    need((cursor is not None and tail is not None) if record["post_failure_snapshot"] == "success"
         else (cursor is None and tail is None), "diagnostic_missing_snapshot_invalid")
    need((record["post_failure_inspect"] == "success") == (alive is not None), "diagnostic_missing_inspect_invalid")


def pprof_summary(data):
    """Regenerate only numeric top-table rows; never copy raw headers/paths."""
    rows = []
    number = DECIMAL + r"(?:ns|us|ms|s|B|kB|MB|GB)?"
    pattern = re.compile(r"^\s*(" + number + r")\s+(" + DECIMAL + r")%\s+(" + DECIMAL + r")%\s+(" +
        number + r")\s+(" + DECIMAL + r")%\s+([a-zA-Z0-9_./*<>\[\]()+:-]+)(?: \(inline\))?\s*$")
    for line in data.decode("utf-8", errors="strict").splitlines():
        match = pattern.fullmatch(line)
        if re.match(r"^\s*\S+\s+\S+%\s+\S+%\s+\S+\s+\S+%\s+\S", line):
            need(match is not None, "profile_top_row_unsupported")
        if match:
            flat, flatpc, sumpc, cumulative, cumpc, function = match.groups()
            need(not function.startswith("/") and ".." not in function and
                 ":" not in function.replace("::", "") and len(function) <= 512, "profile_path_in_symbol")
            rows.append({"flat": finite_unit_column(flat), "flat_percent": finite_number(flatpc),
                "sum_percent": finite_number(sumpc), "cumulative": finite_unit_column(cumulative),
                "cumulative_percent": finite_number(cumpc), "function": function})
    need(1 <= len(rows) <= 64, "profile_summary_missing_or_excess")
    return rows


def profile_totals(data):
    text = data.decode("utf-8", errors="strict")
    units = {"ns": 1, "us": 1000, "ms": 1000000, "s": 1000000000}
    result = {}
    for field, regex in (("duration_ns", r"Duration: ([0-9.]+)(ns|us|ms|s),"),
                         ("sampled_cpu_ns", r"Total samples = ([0-9.]+)(ns|us|ms|s)")):
        match = re.search(regex, text)
        if ("Duration:" if field == "duration_ns" else "Total samples =") in text:
            need(match is not None, "profile_total_column_invalid")
        if match:
            value = finite_number(match[1])
            need(math.isfinite(value * units[match[2]]), "profile_total_invalid")
            result[field] = int(value * units[match[2]])
    match = re.search(r"Showing nodes accounting for ([0-9.]+(?:ns|us|ms|s|B|kB|MB|GB)?),\s*"
                      r"([0-9.]+)% of ([0-9.]+(?:ns|us|ms|s|B|kB|MB|GB)?) total", text)
    if "Showing nodes accounting for " in text:
        need(match is not None, "profile_shown_total_column_invalid")
    if match:
        result.update(shown_total=finite_unit_column(match[1]), shown_percent=finite_number(match[2]),
                      profile_total=finite_unit_column(match[3]))
    return result


class Driver:
    def __init__(self, baseline, diagnostic, output):
        self.baseline, self.diagnostic = baseline.resolve(), diagnostic.resolve()
        self.output = output.resolve()
        need(not self.output.exists(), "fresh_output_required")
        self.output.mkdir(mode=0o700, parents=True)
        self.private, self.public = self.output / "private", self.output / "public"
        self.private.mkdir(mode=0o700); self.public.mkdir(mode=0o700)
        self.started = time.monotonic()
        self.end = self.started + 12 * 60
        self.report = {"version": 1, "kind": "exact_source_native_capacity_investigation",
            "result": "fail", "stages": [], "failures": [], "sources": {}, "benchmarks": [],
            "native_tests": [], "diagnostics": [], "profile_errors": [], "artifacts": [], "model_calls": 0,
            "driver_sha256": sha(Path(__file__).read_bytes()),
            "limitations": ["isolated_diagnostic_does_not_clear_failed_source_ci",
                "no_provider_or_model_qualification", "no_detached_child_census",
                "profile_totals_include_calibration_setup_teardown",
                "sampled_profile_is_not_native_pipeline_cause"]}
        self.env = {k: os.environ[k] for k in ("PATH", "GOCACHE", "GOMODCACHE", "GOMAXPROCS",
                    "CC", "CXX", "SDKROOT") if k in os.environ}
        for name in ("home", "tmp"):
            (self.private / name).mkdir(mode=0o700)
        self.env.update(HOME=str(self.private / "home"), TMPDIR=str(self.private / "tmp"),
            GOENV="off", GOTOOLCHAIN="local", GOFLAGS="-mod=readonly", CGO_ENABLED="1",
            LC_ALL="C", LANG="C")
        self.go = shutil.which("go", path=self.env.get("PATH"))

    def fail(self, label):
        if label not in self.report["failures"]:
            self.report["failures"].append(label)

    def profile_error(self, mode, profile_code, phase_code, error):
        if len(self.report["profile_errors"]) >= 4:
            self.fail("profile_error_records_excess")
            return
        self.report["profile_errors"].append({"mode_code": 1 if mode == "normal" else 2,
            "profile_code": profile_code, "phase_code": phase_code,
            "reason_code": profile_reason(error, phase_code)})

    def run(self, label, argv, cwd, limit=60, extra=None):
        remaining = self.end - time.monotonic() - 10
        stage = {"name": label, "result": "fail", "outer_limit_seconds": limit,
                 "command": self.public_command(argv)}
        self.report["stages"].append(stage)
        if remaining <= 1:
            stage.update(result="skipped", reason="driver_budget_exhausted")
            self.fail(label + ":budget_exhausted")
            return stage, b""
        stdout, stderr = self.private / (label + ".stdout.log"), self.private / (label + ".stderr.log")
        started = time.monotonic()
        before = resource.getrusage(resource.RUSAGE_CHILDREN)
        process = None
        try:
            with stdout.open("xb") as out, stderr.open("xb") as err:
                process = subprocess.Popen(argv, cwd=cwd, env={**self.env, **(extra or {})},
                    stdin=subprocess.DEVNULL, stdout=out, stderr=err, start_new_session=True)
                try:
                    stage["exit_code"] = process.wait(timeout=min(limit, remaining))
                    stage["direct_child_reaped"] = True
                    stage["cleanup"] = "fixture_cleanup_path_returned_no_descendant_census"
                except subprocess.TimeoutExpired:
                    stage["timeout"] = True
                    # A fresh unreaped direct child pins this freshly created group.
                    # Helpers can setsid: this is NOT descendant cleanup proof.
                    try:
                        os.killpg(process.pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
                    try:
                        process.wait(timeout=5)
                        stage["direct_child_reaped"] = True
                    except subprocess.TimeoutExpired:
                        stage["direct_child_reaped"] = False
                    stage.update(exit_code=124, cleanup="detached_cleanup_unknown_failed")
        except OSError:
            stage.update(exit_code=126, cleanup="spawn_or_io_failure", direct_child_reaped=process is None)
        after = resource.getrusage(resource.RUSAGE_CHILDREN)
        stage.update(elapsed_ns=int((time.monotonic() - started) * 1e9),
            children_user_cpu_seconds=after.ru_utime-before.ru_utime,
            children_system_cpu_seconds=after.ru_stime-before.ru_stime)
        data = b""
        for name, path in (("stdout", stdout), ("stderr", stderr)):
            if path.is_file():
                stage[name] = file_binding(path)
                raw = path.read_bytes()
                need(len(raw) <= 16 << 20, "command_output_bound")
                data += raw + b"\n"
        if stage.get("exit_code") == 0 and stage.get("direct_child_reaped") and not stage.get("timeout"):
            stage["result"] = "pass"
        else:
            self.fail(label)
        return stage, data

    def public_command(self, argv):
        words = []
        for arg in map(str, argv):
            arg = arg.replace(str(self.baseline), "<baseline>").replace(str(self.diagnostic), "<diagnostic>")
            arg = arg.replace(str(self.output), "<output>")
            if self.go: arg = arg.replace(str(self.go), "go")
            words.append(arg)
        return words

    def git(self, source, *args):
        label = "git-" + str(len(self.report["stages"]))
        stage, data = self.run(label, ["git", *args], source)
        need(stage["result"] == "pass", "source_git_query_failed")
        return data.rstrip(b"\n").decode()

    def source(self, path, kind):
        need(path.is_dir() and self.git(path, "rev-parse", "--show-toplevel") == str(path), "source_root_differs")
        need(self.git(path, "status", "--porcelain=v1", "--untracked-files=normal") == "", "source_not_clean")
        head = self.git(path, "rev-parse", "HEAD")
        tree = self.git(path, "rev-parse", "HEAD^{tree}")
        need(re.fullmatch("[0-9a-f]{40}", head) and re.fullmatch("[0-9a-f]{40}", tree), "source_identity_invalid")
        if kind == "baseline":
            need(head == BASELINE and tree == BASELINE_TREE, "baseline_identity_differs")
        self.report["sources"][kind] = {"sha": head, "tree": tree,
            "files": {name: file_binding(path / name) for name in IDENTITY_FILES}}
        return head

    def sources(self):
        self.source(self.baseline, "baseline")
        diagnostic = self.source(self.diagnostic, "diagnostic")
        stage, _ = self.run("diagnostic-ancestry", ["git", "merge-base", "--is-ancestor", BASELINE, diagnostic], self.diagnostic)
        need(stage["result"] == "pass", "diagnostic_not_baseline_descendant")
        changed = self.git(self.diagnostic, "diff", "--name-only", BASELINE, diagnostic).splitlines()
        need(set(changed) <= ALLOWED_DIFF, "diagnostic_diff_outside_scope")
        for name in IDENTITY_FILES:
            need((self.baseline / name).read_bytes() == (self.diagnostic / name).read_bytes(), "original_identity_file_changed")
        self.report["sources"]["diagnostic"]["changed_paths"] = changed
        self.report["sources"]["diagnostic"]["changed_files"] = {
            name: file_binding(self.diagnostic / name) for name in changed if (self.diagnostic / name).exists()}
        stage, data = self.run("diagnostic-diff", ["git", "diff", "--binary", BASELINE, diagnostic], self.diagnostic)
        need(stage["result"] == "pass", "diagnostic_diff_failed")
        self.report["sources"]["diagnostic"]["diff_sha256"] = sha(data)
        self.report["sources"]["diagnostic"]["semantic_gate_equivalence"] = "requires_captain_review_not_inferred_from_path_allowlist"

    def metadata(self):
        meta = {"system": platform.system(), "kernel": safe_text(platform.release()),
            "architecture": safe_text(platform.machine()), "python_logical_cpu_count": os.cpu_count(),
            "go_runtime_num_cpu": None, "gate_effective_gomaxprocs": None}
        for name in ("RUNNER_OS", "RUNNER_ARCH", "ImageOS", "ImageVersion", "GITHUB_RUN_ID",
                     "GITHUB_RUN_ATTEMPT", "GITHUB_SHA", "KEELMUX_CAPACITY_RUNNER_LABEL"):
            if name in os.environ:
                meta[name.lower()] = safe_text(os.environ[name])
        need(meta["system"] in ("Darwin", "Linux"), "runner_platform_unsupported")
        if meta["system"] == "Darwin":
            for name, args in (("os_version", ["sw_vers", "-productVersion"]),
                ("os_build", ["sw_vers", "-buildVersion"]), ("memory_bytes", ["sysctl", "-n", "hw.memsize"])):
                stage, data = self.run("metadata-" + name, args, self.baseline)
                need(stage["result"] == "pass", "runner_metadata_failed")
                text = data.decode().strip()
                meta[name] = int(text) if name == "memory_bytes" else safe_text(text)
        else:
            release = Path("/etc/os-release").read_text()
            meta["os_version"] = dict(re.findall(r'^([A-Z_]+)="?([^"\n]*)"?$', release, re.M)).get("VERSION_ID")
            match = re.search(r"^MemTotal:\s+(\d+) kB$", Path("/proc/meminfo").read_text(), re.M)
            meta["memory_bytes"] = int(match[1]) * 1024 if match else None
        if "GOMAXPROCS" in self.env:
            need(re.fullmatch(r"[1-9][0-9]{0,3}", self.env["GOMAXPROCS"]), "gomaxprocs_invalid")
            meta["gomaxprocs_environment"] = int(self.env["GOMAXPROCS"])
        self.report["runner"] = meta
        # Reuse setup-go's existing caches, but keep all fixture HOME/TMP state
        # private. Query only named Go metadata; no environment dump/config load.
        cache_home = os.environ.get("HOME", str(self.private / "home"))
        stage, data = self.run("go-env", [self.go, "env", "-json", "GOOS", "GOARCH", "GOVERSION",
            "CGO_ENABLED", "GOROOT", "GOCACHE", "GOMODCACHE"], self.baseline, extra={"HOME": cache_home})
        need(stage["result"] == "pass", "go_metadata_failed")
        env = json.loads(data)
        need(env["GOVERSION"] == "go1.26.6" and env["CGO_ENABLED"] == "1", "toolchain_differs")
        compiler = Path(env.pop("GOROOT")) / "pkg/tool" / (env["GOOS"] + "_" + env["GOARCH"]) / "compile"
        for name in ("GOCACHE", "GOMODCACHE"):
            value = env.pop(name)
            need(isinstance(value, str) and Path(value).is_absolute(), "go_cache_path_invalid")
            self.env[name] = value
        self.report["toolchain"] = {**env, "go_executable": file_binding(Path(self.go).resolve()),
            "compiler": file_binding(compiler), "GOENV": "off", "GOTOOLCHAIN": "local", "GOFLAGS": "-mod=readonly"}
        source = self.private / "runtime-metadata.go"
        source.write_text('package main\nimport ("encoding/json"; "os"; "runtime")\n'
            'func main(){ _ = json.NewEncoder(os.Stdout).Encode(struct{NumCPU int `json:"num_cpu"`;'
            ' GOMAXPROCS int `json:"gomaxprocs"`}{runtime.NumCPU(),runtime.GOMAXPROCS(0)}) }\n')
        binary = self.private / "runtime-metadata"
        stage, _ = self.run("runtime-metadata-build", [self.go, "build", "-o", str(binary), str(source)], self.baseline, 180)
        need(stage["result"] == "pass", "runtime_metadata_build_failed")
        identity = self.binary(binary, "runtime-metadata")
        stage, data = self.run("runtime-metadata", [str(binary)], self.baseline, 60)
        need(stage["result"] == "pass", "runtime_metadata_failed")
        runtime = json.loads(data)
        need(set(runtime) == {"num_cpu", "gomaxprocs"} and all(type(v) is int and 1 <= v <= 65536
            for v in runtime.values()), "runtime_metadata_invalid")
        meta["standalone_go_runtime"] = {**runtime, "test_binary": identity,
            "source": file_binding(source), "basis": "separate_numeric_go_process_not_gate_or_helper"}

    def binary(self, path, label):
        result = file_binding(path)
        stage, data = self.run(label + "-binary-metadata", [self.go, "version", "-m", "-json", str(path)], self.baseline)
        need(stage["result"] == "pass", "binary_metadata_failed")
        info = json.loads(data)
        result["go_version"] = safe_text(info["GoVersion"])
        result["build_settings"] = {row["Key"]: safe_text(row["Value"], 512) for row in info.get("Settings", [])
            if row["Key"] in ("-race", "-trimpath", "-buildmode", "GOOS", "GOARCH", "CGO_ENABLED", "vcs.revision", "vcs.modified")}
        need(result["go_version"] == "go1.26.6", "binary_go_version_differs")
        return result

    def gates(self, path, label, tags, extra=None):
        binary = self.private / (label + ".test")
        argv = [self.go, "test", "-race", "-count=1", "-p", "1", "-tags", tags, "-v",
                "-timeout", "90s", "-o", str(binary), "-run", GATES, PACKAGE]
        stage, data = self.run(label, argv, path, 180, extra)
        stage["source"] = self.report["sources"]["baseline" if path == self.baseline else "diagnostic"]["sha"]
        stage["runtime_timeout_seconds"] = 90
        try:
            rows = parse_gates(data)
            for row in rows:
                row.update(mode=label, source=stage["source"], profiled=False)
                self.report["native_tests"].append(row)
                if row["result"] != "pass": self.fail(label + ":" + row["name"])
        except (ValueError, UnicodeError):
            self.fail(label + ":gate_accounting_failed")
        if binary.is_file():
            stage["test_binary"] = self.binary(binary, label)
        else:
            self.fail(label + ":binary_missing")
        if extra:
            try:
                self.report["diagnostics"] = parse_diagnostics(data)
            except (ValueError, TypeError, UnicodeError):
                self.fail(label + ":diagnostic_parser_failed")
        return binary

    def profile(self, mode):
        label = "profile-" + mode
        binary = self.private / (label + ".test")
        argv = [self.go, "test", "-c", "-p", "1", "-tags", "library_registry", "-trimpath", "-o", str(binary)]
        if mode == "race": argv.append("-race")
        stage, _ = self.run(label + "-build", argv + [PACKAGE], self.baseline, 180)
        if stage["result"] != "pass": return
        if not binary.is_file():
            self.fail(label + ":binary_missing"); return
        identity = self.binary(binary, label)
        cpu, alloc = self.private / (label + ".cpu.pprof"), self.private / (label + ".alloc.pprof")
        argv = [str(binary), "-test.run=^$", "-test.bench=^" + BENCHMARK + "$", "-test.benchtime=3x",
            "-test.count=1", "-test.benchmem", "-test.timeout=90s",
            "-test.cpuprofile=" + str(cpu), "-test.memprofile=" + str(alloc)]
        stage, data = self.run(label + "-benchmark", argv, self.baseline / NATIVE, 90)
        stage["test_binary"] = identity
        profile_code, phase_code = 0, 0
        try:
            result = parse_benchmark(data)
            result.update(mode=mode, source=BASELINE, test_binary=identity, profiles={})
            self.report["benchmarks"].append(result)
            for kind, path in (("cpu", cpu), ("alloc_space", alloc)):
                profile_code, phase_code = (1 if kind == "cpu" else 2), 0
                need(path.is_file() and 0 < path.stat().st_size <= 32 << 20, "profile_missing_or_size_invalid")
                result["profiles"][kind] = file_binding(path)
                argv = [self.go, "tool", "pprof", "-top", "-nodecount=25"]
                if kind == "alloc_space": argv.append("-sample_index=alloc_space")
                summary, raw = self.run(label + "-" + kind + "-top", argv + [str(binary), str(path)], self.baseline, 60)
                need(summary["result"] == "pass", "profile_tool_failed")
                phase_code = 1
                rows = pprof_summary(raw)
                phase_code = 0
                phase_code = 2
                totals = profile_totals(raw)
                phase_code = 0
                result["profiles"][kind]["totals"] = totals
                result["profiles"][kind]["top"] = rows
                public = self.public / (label + "-" + kind + "-top.txt")
                phase_code = 3
                text = "\n".join(json.dumps(row, sort_keys=True, allow_nan=False) for row in rows) + "\n"
                phase_code = 0
                public.write_text(text)
                self.report["artifacts"].append({"file": public.name, **file_binding(public),
                    "original_summary": {"sha256": sha(raw), "bytes": len(raw)}})
        except (ValueError, UnicodeError) as error:
            self.fail(label + ":profile_measurement_failed")
            self.profile_error(mode, profile_code, phase_code, error)

    def finish(self):
        try:
            for kind, path in (("baseline", self.baseline), ("diagnostic", self.diagnostic)):
                if kind in self.report["sources"]:
                    need(self.git(path, "rev-parse", "HEAD") == self.report["sources"][kind]["sha"] and
                         self.git(path, "status", "--porcelain=v1", "--untracked-files=normal") == "", "source_changed_during_run")
        except (ValueError, OSError):
            self.fail("post_source_check_failed")
        if len(self.report["benchmarks"]) != 2:
            self.fail("required_benchmark_missing")
        if sum(row["mode"] == "baseline-race-gates" for row in self.report["native_tests"]) != 2:
            self.fail("required_baseline_gate_missing")
        self.report["elapsed_ns"] = int((time.monotonic() - self.started) * 1e9)
        self.report["result"] = "pass" if not self.report["failures"] else "fail"
        log = self.public / "results.log"
        log.write_text("\n".join(json.dumps({"stage": row["name"], "result": row["result"],
            "exit_code": row.get("exit_code"), "timeout": row.get("timeout", False),
            "elapsed_ns": row.get("elapsed_ns"), "cleanup": row.get("cleanup")}, sort_keys=True, allow_nan=False)
            for row in self.report["stages"]) + "\n")
        self.report["artifacts"].append({"file": log.name, **file_binding(log), "kind": "numeric_stage_summary"})
        allowed = {row["file"] for row in self.report["artifacts"]} | {"receipt.json"}
        # The upload directory contains only authored summaries, never a copied
        # raw file, native output, profile, executable or fixture directory.
        if not all(p.name in allowed and p.is_file() and not p.is_symlink() and
                   p.suffix in (".json", ".log", ".txt") for p in self.public.iterdir()):
            self.fail("public_artifact_inventory_invalid")
            self.report["result"] = "fail"
            # Preserve the unexpected bytes privately; never upload them even
            # on a failed investigation's always-run artifact step.
            self.public.rename(self.private / "refused-public")
            self.public.mkdir(mode=0o700)
            self.report["artifacts"] = []
            self.report["public_inventory_quarantined"] = True
        private = self.private / "receipt.json"
        write_json(private, self.report)
        public = dict(self.report)
        public["private_original_receipt"] = file_binding(private)
        write_json(self.public / "receipt.json", public)
        return 0 if self.report["result"] == "pass" else 1

    def execute(self):
        try:
            need(self.go is not None, "go_missing")
            self.sources(); self.metadata()
        except (ValueError, OSError, KeyError, json.JSONDecodeError):
            self.fail("driver_preflight_error")
            return self.finish()
        steps = (("baseline-race-gates", lambda: self.gates(self.baseline, "baseline-race-gates", "library_registry")),
                 ("profile-normal", lambda: self.profile("normal")),
                 ("profile-race", lambda: self.profile("race")))
        for name, operation in steps:
            # Native assertion failure deliberately does not skip later profiles.
            try:
                operation()
            except (ValueError, OSError, KeyError, json.JSONDecodeError):
                self.fail(name + ":measurement_error")
        try:
            seam = self.diagnostic / NATIVE / "capacity_diagnostics.go"
            if seam.is_file():
                need("//go:build native_capacity" in seam.read_text(), "diagnostic_tag_missing")
                self.report["diagnostic_mode"] = "tagged_instrumented_not_baseline"
                self.gates(self.diagnostic, "tagged-diagnostic-race-gates", "library_registry,native_capacity",
                    {"KEELMUX_TEST_NATIVE_CAPACITY": "1"})
            else:
                self.report["diagnostic_mode"] = "not_available_not_claimed"
        except (ValueError, TypeError, OSError, KeyError, json.JSONDecodeError):
            self.fail("tagged_diagnostic_measurement_error")
        return self.finish()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--baseline", type=Path, required=True)
    parser.add_argument("--diagnostic", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    os.umask(0o077)
    return Driver(args.baseline, args.diagnostic, args.output).execute()


if __name__ == "__main__":
    raise SystemExit(main())
