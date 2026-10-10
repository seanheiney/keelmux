#!/usr/bin/env python3
"""Qualify installed schema-2 archives against real native CLIs, without turns.

The loopback download fixture proves package/installer behavior only. It does
not replace the anonymous public-download gate. The provider harness uses the
existing native credential realm and an explicitly selected trusted workspace.
"""
import argparse
import datetime
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys

from release_acceptance import ALIASES, NAMES, Fixture, Harness, digest, need, snapshot


def check_public_commands(prefix, bindir, selected, hashes):
    """Prove the commands users invoke select the qualified archive bytes."""
    for name in NAMES:
        path = bindir / name
        canonical = ALIASES.get(name, name)
        need(path.is_symlink()
             and os.readlink(path) == str(prefix / 'current' / canonical)
             and path.resolve(strict=True) == (selected / canonical).resolve(strict=True)
             and digest(path.read_bytes()) == hashes[canonical],
             'public_command_does_not_select_qualified_archive')


def run_native(h, argv, env, timeout=300, cleanup_timeout=90):
    """Cancel only the direct child harness, giving its exact cleanup time to run."""
    h.commands += 1
    paths = [h.private / ('command-%d.%s' % (h.commands, stream))
             for stream in ('stdout', 'stderr')]
    status = {'timed_out': False, 'graceful_cancel_requested': False,
              'cleanup_wait_expired': False}
    with paths[0].open('wb') as out, paths[1].open('wb') as err:
        for path in paths:
            path.chmod(0o600)
        with subprocess.Popen(argv, env=env, cwd=h.state, stdout=out, stderr=err) as child:
            try:
                child.wait(timeout=timeout)
            except subprocess.TimeoutExpired:
                status['timed_out'] = True
                status['graceful_cancel_requested'] = True
                child.terminate()
                try:
                    child.wait(timeout=cleanup_timeout)
                except subprocess.TimeoutExpired:
                    status['cleanup_wait_expired'] = True
                    # This is the unreaped direct Python child, never a saved
                    # provider/helper PID. Runtime cleanup remains unconfirmed.
                    child.kill()
                    child.wait(timeout=5)
            status['returncode'] = child.returncode
    return status


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--installer", type=Path, required=True)
    parser.add_argument("--assets", type=Path, required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--artifact-source-sha", required=True)
    parser.add_argument("--workspace", type=Path, required=True)
    parser.add_argument("--providers", nargs="+", choices=("codex", "claude"),
                        default=["codex", "claude"])
    parser.add_argument("--report", type=Path, required=True)
    args = parser.parse_args()
    need(bool(re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+", args.version)), "invalid_version")
    need(bool(re.fullmatch(r"[a-f0-9]{40}", args.artifact_source_sha)), "invalid_source")
    need(len(set(args.providers)) == len(args.providers), "duplicate_provider")
    args.installer = args.installer.resolve(strict=True)
    args.assets = args.assets.resolve(strict=True)
    args.workspace = args.workspace.resolve(strict=True)
    need(args.workspace.is_dir(), "workspace_not_directory")
    args.harness_checkout_sha = None
    h = Harness(args)
    native_harness = Path(__file__).with_name("native_supervisor_acceptance.py").resolve()
    report = {"schema": 1, "utc": datetime.datetime.now(datetime.timezone.utc).isoformat(),
              "artifact_source_commit": args.artifact_source_sha,
              "installer_sha256": digest(args.installer.read_bytes()),
              "wrapper_sha256": digest(Path(__file__).read_bytes()),
              "native_harness_sha256": digest(native_harness.read_bytes()),
              "qualification": "Installed clean native archive, owned loopback downloads and temporary installation; actual existing native CLIs with zero model turns.",
              "public_download_verified": False, "checks": [], "result": "fail"}
    child_report = h.private / "native.json"
    installed = None
    passed = False
    try:
        artifact = h.artifact()
        need(h.manifest["schema"] == 2, "native_schema2_required")
        report.update(archive=artifact, manifest=h.manifest)
        _, home, prefix, bindir, env = h.case("native_package")
        untouched = snapshot(home)
        installed = (prefix, bindir, env)
        with Fixture(h.files, args.version) as fixture:
            need(h.install(fixture, prefix, bindir, env).returncode == 0, "package_install_failed")
        hashes = h.check_installed(prefix, bindir)
        report["installed_executable_sha256"] = hashes
        selected = (prefix / "current").resolve(strict=True)
        check_public_commands(prefix, bindir, selected, hashes)
        for name in ("keelmux", "keelmuxd", "keelmux-entry", "keelmux-supervisor"):
            result = h.run([str(selected / name), "--version"], env)
            expected = ("Keelmux %s (commit %s, protocol bp/1)" %
                        (args.version, args.artifact_source_sha)).encode()
            need(result.returncode == 0 and result.stdout.strip() == expected,
                 "loaded_package_version_mismatch")
        need(snapshot(home) == untouched, "installer_changed_native_or_shell_sentinels")
        report["checks"].append({"name": "installed_archive_and_loaded_versions", "result": "pass"})
        # Actual native CLI paths/realm remain inherited. Only the owned tmux
        # denial probe is prepended; the harness cannot borrow a host adapter.
        native_env = dict(os.environ, PATH=str(h.tools) + ":" + os.environ.get("PATH", ""))
        native_run = run_native(h, [sys.executable, str(native_harness), "--bin-dir", str(selected),
                        "--expected-source", args.artifact_source_sha,
                        "--workspace", str(args.workspace), "--providers", *args.providers,
                        "--report", str(child_report)], native_env)
        report['native_harness_process'] = native_run
        report['native_cleanup_confirmed'] = False
        need(not native_run['cleanup_wait_expired'], 'native_cleanup_grace_expired_unresolved')
        need(child_report.is_file(), "native_receipt_missing")
        child = json.loads(child_report.read_text())
        # Retain failure/cancellation cleanup evidence too; do not lose it when
        # a qualification assertion below fails.
        report["native_receipt"] = child
        report["native_receipt_sha256"] = digest(child_report.read_bytes())
        cleanup = child.get("cleanup", {})
        report['native_cleanup_confirmed'] = (cleanup.get('owned_processes_no_longer_live') is True
                                               and cleanup.get('issues') == [])
        need(report['native_cleanup_confirmed'], 'native_cleanup_unconfirmed')
        need(not native_run['timed_out'], 'native_harness_timed_out_after_cleanup')
        # The child freezes sibling bytes for its isolated process lifetime.
        # Prove these are exactly the binaries activated by the installer.
        for name in ("keelmux", "keelmuxd", "keelmux-supervisor"):
            need(child.get("binaries", {}).get(name, {}).get("sha256") == hashes[name],
                 "native_harness_binary_differs_from_installed_archive")
        source = child.get("source", {})
        need(source.get("commit") == args.artifact_source_sha and source.get("version") == args.version,
             "native_loaded_provenance_mismatch")
        need(child.get("diagnostic_client_probe_exception") is False
             and child.get("model_prompt_submitted") is False
             and child.get("native_enter_submitted") is False,
             "native_no_turn_or_strict_terminal_gate_failed")
        expected_checks = {"native_default_daemon_start"} | {
            p + "_native_transport_tui" for p in args.providers}
        checks = child.get("checks", [])
        need(len(checks) == len(expected_checks)
             and {c.get("name") for c in checks} == expected_checks
             and all(c.get("result") == "pass" for c in checks), "native_scenarios_failed")
        need(native_run['returncode'] == 0 and child.get("result") == "pass", "native_harness_failed")
        need(not (h.state / "tmux-invoked").exists(), "native_package_invoked_tmux")
        report["checks"].append({"name": "installed_native_provider_lifecycle", "result": "pass"})
        uninstall = h.run(["/bin/sh", str(args.installer), "--prefix", str(prefix),
                           "--bin-dir", str(bindir), "--uninstall"], env)
        # The installer intentionally retains its private ownership scaffold
        # so a concurrent reinstall never encounters an unowned directory.
        need(uninstall.returncode == 0 and not os.path.lexists(prefix / "current")
             and not any((prefix / "versions").iterdir())
             and not any(bindir.iterdir()), "owned_package_uninstall_failed")
        need(snapshot(home) == untouched, "uninstall_changed_native_or_shell_sentinels")
        report["checks"].append({"name": "owned_uninstall_and_preservation", "result": "pass"})
        installed = None
        passed = True
        report["result"] = "pass"
    except Exception as exc:
        report["checks"].append({"name": "qualification", "result": "fail",
                                 "failure": str(exc) if isinstance(exc, AssertionError) else type(exc).__name__})
    finally:
        report["model_turns_submitted"] = 0
        report["native_enter_submitted"] = False
        report["tmux_invocations_observed"] = (h.state / "tmux-invoked").exists()
        report["owned_installation_retained"] = installed is not None
        report["missing_evidence"] = ["anonymous public download of these exact release bytes",
                                      "authenticated model turn and effective account/model",
                                      "host reboot/native resume", "remote transport and Linux native CLIs"]
        args.report.parent.mkdir(parents=True, exist_ok=True)
        args.report.write_text(json.dumps(report, indent=2) + "\n")
        if passed:
            shutil.rmtree(h.state)
        print(json.dumps({"result": report["result"], "report": str(args.report),
                          "checks": len(report["checks"])}))
    return 0 if passed else 1


if __name__ == "__main__":
    raise SystemExit(main())
