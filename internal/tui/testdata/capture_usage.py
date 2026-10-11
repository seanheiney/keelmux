#!/usr/bin/env python3
"""Capture actual usage-panel PTY cells with explicitly manual fixture evidence.

Uses the product's manual importer and deterministic owned inventory. No daemon,
provider, native account reader, external service or model is contacted. PNGs
rasterize interpreted terminal cells; they are not native Terminal screenshots.
"""
import argparse
import datetime
import hashlib
import json
from pathlib import Path
import subprocess

import capture


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--binary', required=True)
    p.add_argument('--output', required=True)
    args = p.parse_args()
    binary, output = Path(args.binary).resolve(), Path(args.output).resolve()
    if output.exists():
        raise SystemExit('use a fresh output directory')
    output.mkdir(parents=True)
    source = json.loads(subprocess.check_output([str(binary), 'version', '--json']))['commit']
    now = datetime.datetime.now(datetime.timezone.utc)
    stamp = lambda t: t.isoformat(timespec='seconds').replace('+00:00', 'Z')
    observation = {
        'id': 'owned-usage-capture',
        'source': {'id': 'owned-manual-capture', 'kind': 'manual', 'quality': 'user_entered', 'observed_at': stamp(now), 'sequence': 1, 'coverage': 'unknown'},
        'attempt': {'key': {'provider': 'codex', 'namespace': 'owned-fixture', 'request_id': 'authored-one', 'attempt_id': 'authored-one'}, 'at': stamp(now-datetime.timedelta(minutes=1)), 'context': {'host': 'host_local', 'session': 'session_00'}, 'actual_model': 'authored-label'},
    }

    def prepare(state):
        subprocess.run([str(binary), '--state', str(state), 'usage', 'import', '-'], input=json.dumps({'version': 1, 'observations': [observation]}), text=True, capture_output=True, check=True, timeout=10)

    receipts = []
    for width, height in ((80, 24), (120, 32), (40, 14)):
        prefix = output / ('usage-%dx%d' % (width, height))
        receipt = capture.capture(str(binary), prefix, width, height, 'usage', prepare_state=prepare)
        text = prefix.with_suffix('.txt').read_text()
        for expected in ('Coverage unknown', 'Provider calls observed: 0', 'User-entered calls: 1', 'Actual models observed: unknown'):
            if expected not in text:
                raise AssertionError('manual evidence lost its scope label: ' + expected)
        if not prefix.with_suffix('.png').is_file():
            raise AssertionError('Pillow is required to save the interpreted cells')
        receipts.append(receipt)
    files = {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in output.iterdir() if p.is_file()}
    receipt = {'schema': 1, 'source_commit': source, 'binary_sha256': hashlib.sha256(binary.read_bytes()).hexdigest(), 'scope': 'Actual owned PTY with deterministic fixture inventory and one manual imported observation; PNGs rasterize cells', 'model_turns': 0, 'provider_launches': 0, 'external_service_posts': 0, 'captures': receipts, 'files_sha256': files}
    (output / 'receipt.json').write_text(json.dumps(receipt, indent=2)+'\n')
    print(json.dumps({'source_commit': source, 'captures': len(receipts), 'output': str(output)}))


if __name__ == '__main__':
    main()
