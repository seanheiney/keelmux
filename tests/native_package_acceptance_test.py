#!/usr/bin/env python3
"""Provider-free regression tests for package cancellation and command authority."""
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
from types import SimpleNamespace
import unittest

from native_package_acceptance import check_public_commands, run_native
from release_acceptance import ALIASES, CANONICAL, NAMES, digest


# Exercise the actual child main/signal/checkpoint/finally/cleanup functions.
# Only its authority and session fixture are fake; no product/provider starts.
CANCELLABLE_FIXTURE = r'''
import json, sys, time
from pathlib import Path
sys.path.insert(0, sys.argv[1])
import native_supervisor_acceptance as native

class Daemon:
    def poll(self): return None
    def wait(self, timeout): return 0

class Fixture(native.Acceptance):
    def __init__(self, args):
        self.args = args
        self.temp = args.report.parent
        self.terminals = []
        self.sessions = []
        self.owned = {}
        self.launches = {}
        self.providers = {'codex': '/unused-provider-fixture'}
        self.daemon = Daemon()
        self.requests = []
        self.report = {'checks': [], 'providers': {'codex': 'provider-free fixture'}}
    def start(self):
        return {'instance': 'owned-daemon-instance'}
    def journey(self, provider):
        # Simulate a launch acknowledgement arriving after cancellation. The
        # signal handler must let its ownership bookkeeping finish first.
        (self.temp / 'ready').write_text('ready')
        time.sleep(1.3)
        self.sessions.append({'id': 'owned-session', 'execution': {'id': 'owned-execution', 'generation': 7}})
        native.wait(lambda: False, 'fixture wait', timeout=30)
    def api(self, path, body=None, expected=200):
        self.requests.append({'path': path, 'body': body, 'expected': expected})
        (self.temp / 'requests.json').write_text(json.dumps(self.requests))
        return {'instance': 'owned-daemon-instance'} if path == 'daemon' else None

native.Acceptance = Fixture
sys.argv = ['fixture', '--bin-dir', '.', '--workspace', sys.argv[2],
            '--expected-source', 'fixture', '--providers', 'codex', '--report', sys.argv[3]]
raise SystemExit(native.main())
'''

LOST_ACK_FIXTURE = CANCELLABLE_FIXTURE.replace('native.Acceptance = Fixture', r'''
Fixture.journey = native.Acceptance.journey
original_api = Fixture.api
def lost_ack(self, path, body=None, expected=200):
    if path == 'launch':
        assert body['operation_id'] in self.launches
        (self.temp / 'launch-intent.json').write_text(json.dumps(self.launches[body['operation_id']]))
        raise TimeoutError('simulated acknowledgement loss after possible effect')
    return original_api(self, path, body, expected)
Fixture.api = lost_ack
native.Acceptance = Fixture
''')


class PackageAcceptanceTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='km-package-regression-')
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.harness = SimpleNamespace(state=self.root, private=self.root, commands=0)

    def test_timeout_finishes_exact_authority_cleanup(self):
        report = self.root / 'native.json'
        result = run_native(self.harness,
                            [sys.executable, '-c', CANCELLABLE_FIXTURE,
                             str(Path(__file__).parent), str(self.root), str(report)],
                            dict(os.environ), timeout=1, cleanup_timeout=3)
        self.assertTrue((self.root / 'ready').is_file())
        self.assertTrue(result['timed_out'])
        self.assertTrue(result['graceful_cancel_requested'])
        self.assertFalse(result['cleanup_wait_expired'])
        self.assertEqual(result['returncode'], 1)
        receipt = json.loads(report.read_text())
        self.assertEqual(receipt['result'], 'fail')
        self.assertTrue(receipt['cleanup']['owned_processes_no_longer_live'])
        self.assertEqual(receipt['cleanup']['issues'], [])
        requests = json.loads((self.root / 'requests.json').read_text())
        stop = requests[0]
        self.assertEqual(stop['path'], 'stop')
        self.assertEqual(stop['expected'], 204)
        self.assertEqual({k: v for k, v in stop['body'].items() if k != 'operation_id'},
                         {'session_id': 'owned-session', 'execution_id': 'owned-execution', 'generation': 7})
        self.assertEqual(requests[-1], {'path': 'daemon/stop',
                                        'body': {'instance': 'owned-daemon-instance'}, 'expected': 204})

    def test_unresponsive_child_is_bounded_and_cleanup_is_unresolved(self):
        code = 'import signal,time; signal.signal(signal.SIGTERM, signal.SIG_IGN); time.sleep(30)'
        result = run_native(self.harness, [sys.executable, '-c', code], dict(os.environ),
                            timeout=1, cleanup_timeout=.2)
        self.assertTrue(result['timed_out'])
        self.assertTrue(result['cleanup_wait_expired'])
        self.assertEqual(result['returncode'], -9)
        self.assertFalse((self.root / 'native.json').exists())

    def test_lost_launch_ack_cannot_claim_zero_process_cleanup(self):
        report = self.root / 'native.json'
        result = run_native(self.harness,
                            [sys.executable, '-c', LOST_ACK_FIXTURE,
                             str(Path(__file__).parent), str(self.root), str(report)],
                            dict(os.environ), timeout=3, cleanup_timeout=1)
        self.assertEqual(result['returncode'], 1)
        self.assertFalse(result['timed_out'])
        intent = json.loads((self.root / 'launch-intent.json').read_text())
        self.assertFalse(intent.pop('confirmed'))
        self.assertTrue(intent['operation_id'].startswith('op_'))
        self.assertIsNone(intent['session_id'])
        self.assertIsNone(intent['execution_id'])
        receipt = json.loads(report.read_text())
        self.assertEqual(receipt['result'], 'fail')
        cleanup = receipt['cleanup']
        self.assertEqual(cleanup['owned_process_count'], 0)
        self.assertFalse(cleanup['owned_processes_no_longer_live'])
        self.assertEqual(cleanup['unresolved_launches'], [intent])
        self.assertIn('owned_launch_effect_unresolved', cleanup['issues'])
        self.assertTrue(cleanup['daemon_retained_for_reconciliation'])
        self.assertFalse(cleanup['exact_product_stop_requested'])
        self.assertTrue(cleanup['private_captures_retained'])
        self.assertFalse((self.root / 'requests.json').exists(), 'unknown identity must not trigger guessed stops')

    def test_completed_child_needs_no_signal(self):
        result = run_native(self.harness, [sys.executable, '-c', 'print("finite fixture")'],
                            dict(os.environ), timeout=3, cleanup_timeout=1)
        self.assertEqual(result, {'timed_out': False, 'cancel_requested': False, 'graceful_cancel_requested': False,
                                  'cleanup_wait_expired': False, 'returncode': 0})
        self.assertEqual((self.root / 'command-1.stdout').read_text(), 'finite fixture\n')

    def test_wrapper_cancellation_waits_for_child_cleanup(self):
        for cancellation in (signal.SIGINT, signal.SIGTERM):
            with self.subTest(signal=cancellation):
                root = self.root / str(cancellation)
                root.mkdir()
                fixture = root / 'fixture.py'
                fixture.write_text(CANCELLABLE_FIXTURE)
                code = r'''
import json, os, sys
from pathlib import Path
from types import SimpleNamespace
sys.path.insert(0, sys.argv[1])
from native_package_acceptance import run_native
root = Path(sys.argv[2])
harness = SimpleNamespace(state=root, private=root, commands=0)
result = run_native(harness, [sys.executable, str(root / 'fixture.py'), sys.argv[1],
                             str(root), str(root / 'native.json')], dict(os.environ),
                    timeout=30, cleanup_timeout=3)
(root / 'wrapper.json').write_text(json.dumps(result))
'''
                wrapper = subprocess.Popen([sys.executable, '-c', code,
                                            str(Path(__file__).parent), str(root)],
                                           stdout=subprocess.PIPE, stderr=subprocess.PIPE)
                try:
                    deadline = time.monotonic() + 5
                    while not (root / 'ready').exists() and time.monotonic() < deadline:
                        self.assertIsNone(wrapper.poll(), 'wrapper exited before owned child was ready')
                        time.sleep(.02)
                    self.assertTrue((root / 'ready').exists())
                    wrapper.send_signal(cancellation)
                    stdout, stderr = wrapper.communicate(timeout=5)
                    self.assertEqual(wrapper.returncode, 0, (stdout, stderr))
                    result = json.loads((root / 'wrapper.json').read_text())
                    self.assertTrue(result['cancel_requested'])
                    self.assertTrue(result['graceful_cancel_requested'])
                    self.assertFalse(result['timed_out'])
                    self.assertFalse(result['cleanup_wait_expired'])
                    receipt = json.loads((root / 'native.json').read_text())
                    self.assertEqual(receipt['result'], 'fail')
                    self.assertTrue(receipt['cleanup']['owned_processes_no_longer_live'])
                    self.assertEqual(receipt['cleanup']['issues'], [])
                    requests = json.loads((root / 'requests.json').read_text())
                    self.assertEqual(requests[0]['body']['execution_id'], 'owned-execution')
                    self.assertEqual(requests[-1]['body']['instance'], 'owned-daemon-instance')
                finally:
                    if wrapper.poll() is None:
                        wrapper.terminate()
                        wrapper.communicate(timeout=5)

    def test_public_links_must_select_current_archive(self):
        prefix, bindir = self.root / 'install', self.root / 'bin'
        selected = prefix / 'versions' / 'new'
        selected.mkdir(parents=True)
        bindir.mkdir()
        (prefix / 'current').symlink_to('versions/new')
        hashes = {}
        for name in CANONICAL:
            path = selected / name
            path.write_bytes(('qualified ' + name).encode())
            hashes[name] = digest(path.read_bytes())
        for name in NAMES:
            (bindir / name).symlink_to(prefix / 'current' / ALIASES.get(name, name))
        check_public_commands(prefix, bindir, selected, hashes)
        old = prefix / 'versions' / 'old'
        old.mkdir()
        (old / 'keelmux').write_bytes(b'stale command')
        (bindir / 'keelmux').unlink()
        (bindir / 'keelmux').symlink_to(old / 'keelmux')
        with self.assertRaisesRegex(AssertionError, 'public_command_does_not_select_qualified_archive'):
            check_public_commands(prefix, bindir, selected, hashes)


if __name__ == '__main__':
    unittest.main()
