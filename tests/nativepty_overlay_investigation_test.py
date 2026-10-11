#!/usr/bin/env python3
"""Finite sanitizer and authenticated-cleanup controls for the investigation."""
import gzip
import importlib.util
import os
from pathlib import Path
import signal
import subprocess
import sys
import unittest
import uuid

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("overlay_investigation", ROOT / "scripts/nativepty-overlay-investigation.py")
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class InvestigationControls(unittest.TestCase):
    def test_profile_rewrites_paths_and_preserves_sample_fields(self):
        encode = MODULE.encode_varint
        def field(number, value):
            return encode((number << 3) | 2) + encode(len(value)) + value
        sample = field(2, b"\x08\x02\x12\x03\x91\x82\x01")
        location = field(4, b"\x08\x01\x10\x02")
        strings = [b"", b"nativepty.(*terminalScreen).process", b"/home/private/source/filter.go"]
        original = sample + location + b"".join(field(6, item) for item in strings) + b"\x48\x8c\x01"
        changed, count = MODULE.sanitize_profile(gzip.compress(original, mtime=0))
        expected = sample + location + field(6, strings[0]) + field(6, strings[1]) + field(6, b"<owned-path>/filter.go") + b"\x48\x8c\x01"
        self.assertEqual(gzip.decompress(changed), expected)
        self.assertEqual(count, 1)
        self.assertEqual(MODULE.sanitize_profile(gzip.compress(original, mtime=0))[0], changed)

    def test_corrupt_profile_refused(self):
        for data in [b"\x32\x7fshort", b"\x00", b"\x33", b"\x32\x01\xff", b"\x08" + b"\xff" * 11]:
            with self.subTest(data=data), self.assertRaises((AssertionError, UnicodeDecodeError)):
                MODULE.sanitize_profile(gzip.compress(data))

    def test_log_removes_owned_paths_retains_original_gate_failure(self):
        log = b"FAIL TestSlowReaderDoesNotBlockNativeOutput 8.12s\n/home/runner/work/project/screen_test.go:277\n/tmp/fixture/state"
        result = MODULE.sanitized_log(log, Path("/home/runner/work/project"), Path("/tmp/experiment"))
        self.assertIn(b"8.12s", result)
        self.assertIn(b"screen_test.go:277", result)
        self.assertNotIn(b"/home/", result)
        self.assertNotIn(b"/tmp/", result)

    @unittest.skipUnless(sys.platform == "linux" and hasattr(os, "pidfd_open") and hasattr(signal, "pidfd_send_signal"), "real Linux pidfd cleanup requires Linux")
    def test_exact_nonce_cleanup_preserves_unrelated_control(self):
        nonce = uuid.uuid4().hex
        child_env = {**os.environ, "KEELMUX_OVERLAY_EXPERIMENT_NONCE": nonce}
        unrelated_env = {k:v for k,v in os.environ.items() if k != "KEELMUX_OVERLAY_EXPERIMENT_NONCE"}
        argv = [sys.executable, "-c", "import time; time.sleep(20)"]
        owned = subprocess.Popen(argv, env=child_env, start_new_session=True)
        unrelated = subprocess.Popen(argv, env=unrelated_env, start_new_session=True)
        try:
            self.assertEqual(set(MODULE.owned_processes(nonce)), {owned.pid})
            cleanup = MODULE.clean_owned_processes(nonce)
            self.assertEqual(cleanup["unexpected_owned_processes_before_cleanup"], 1)
            self.assertTrue(cleanup["exact_nonce_and_starttime_checked"])
            self.assertTrue(cleanup["no_owned_processes_remained"])
            self.assertLess(owned.wait(timeout=5), 0)
            self.assertIsNone(unrelated.poll())
        finally:
            for child in (owned, unrelated):
                if child.poll() is None:
                    child.kill()
                child.wait(timeout=5)


if __name__ == "__main__":
    unittest.main()
