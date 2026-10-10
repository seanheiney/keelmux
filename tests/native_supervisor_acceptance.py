#!/usr/bin/env python3
"""Opt-in real native-default Keelmux acceptance; never submits native Enter.

The exact source binaries must already be built. This harness freezes sibling
binaries into a private directory, launches installed vendor CLIs in an explicitly
selected existing trusted workspace, and exercises product transport and TUI.
It does not build source, install anything, invoke tmux, or change credentials.
Raw output and detailed errors remain in the private temporary directory.
"""
import argparse
import fcntl
import hashlib
import http.client
import json
import os
from pathlib import Path
import platform
import pty
import re
import select
import shutil
import signal
import socket
import stat
import struct
import subprocess
import sys
import tempfile
import termios
import threading
import time
import uuid


def run(argv, **kw):
    return subprocess.run(argv, check=True, capture_output=True, text=True, timeout=15, **kw).stdout.strip()


def digest(data):
    return hashlib.sha256(data).hexdigest()


def wait(fn, label, timeout=15):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            answer = fn()
            if answer:
                return answer
        except (OSError, ValueError, http.client.HTTPException):
            pass
        time.sleep(0.05)
    raise AssertionError(label)


def plain(data):
    text = data.decode('utf-8', 'replace') if isinstance(data, (bytes, bytearray)) else data
    text = re.sub(r'\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)', '', text)
    return re.sub(r'\x1b(?:\[[0-?]*[ -/]*[@-~]|[ -/]*[@-~])', '', text)


def lifetime(pid):
    if platform.system() == 'Linux':
        try:
            fields = Path('/proc/%d/stat' % pid).read_text().rsplit(')', 1)[1].split()
            return (fields[19], fields[0])
        except FileNotFoundError:
            return None
    result = subprocess.run(['/bin/ps', '-p', str(pid), '-o', 'lstart=', '-o', 'stat='],
                            capture_output=True, text=True, timeout=2, env=dict(os.environ, LC_ALL='C'))
    if result.returncode == 1 and not result.stdout.strip():
        return None
    assert result.returncode == 0, 'owned process lifetime lookup failed'
    fields = result.stdout.split()
    assert len(fields) == 6, 'owned process lifetime lookup malformed'
    return (' '.join(fields[:5]), fields[5])


def alive(pid, original):
    current = lifetime(pid)
    return current is not None and current[0] == original[0] and 'Z' not in current[1]


class UnixHTTP(http.client.HTTPConnection):
    def __init__(self, path):
        super().__init__('keelmux', timeout=15)
        self.path = str(path)

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.path)


class Terminal:
    def __init__(self, argv, cwd, capture, columns=100, rows=34):
        self.output = bytearray()
        self.capture = capture
        self.status = None
        self.closed = False
        self.pid, self.fd = pty.fork()
        if self.pid == 0:
            os.chdir(cwd)
            os.environ['TERM'] = 'xterm-256color'
            os.execv(argv[0], argv)
        self.resize(columns, rows)
        self.reader_error = None
        self.reading = True
        self.reader = threading.Thread(target=self.drain, daemon=True)
        self.reader.start()

    def resize(self, columns, rows):
        fcntl.ioctl(self.fd, termios.TIOCSWINSZ, struct.pack('HHHH', rows, columns, 0, 0))
        os.kill(self.pid, signal.SIGWINCH)

    def drain(self):
        # Drain every open terminal concurrently. Letting an unrelated observer
        # fill the original writer's OS PTY would test slow-consumer eviction,
        # not healthy simultaneous native writer/read-only attachment.
        try:
            while self.reading:
                ready, _, _ = select.select([self.fd], [], [], .05)
                if not ready:
                    continue
                try:
                    data = os.read(self.fd, 65536)
                except OSError:
                    break
                if not data:
                    break
                if len(self.output) + len(data) >= 16 << 20:
                    self.reader_error = 'private terminal capture exceeded bound'
                    break
                self.output.extend(data)
                # No query answers: the helper owns provider terminal queries.
        except Exception:
            self.reader_error = 'private terminal capture failed'

    def pump(self, seconds=0.15):
        time.sleep(seconds)
        assert self.reader_error is None, self.reader_error
        return plain(bytes(self.output))

    def send(self, data, tui_enter=False):
        data = data.encode() if isinstance(data, str) else data
        assert tui_enter or (b'\r' not in data and b'\n' not in data), 'native Enter is prohibited'
        os.write(self.fd, data)
        self.pump()

    def poll(self):
        if self.status is None:
            pid, status = os.waitpid(self.pid, os.WNOHANG)
            if pid:
                self.status = os.waitstatus_to_exitcode(status)
        return self.status

    def exited(self):
        self.pump()
        return self.poll() is not None

    def save(self):
        self.capture.write_bytes(bytes(self.output))
        self.capture.chmod(0o600)

    def close(self):
        if self.closed:
            return
        self.pump()
        if self.poll() is None:
            os.kill(self.pid, signal.SIGTERM)
            deadline = time.monotonic()+2
            while time.monotonic() < deadline and self.poll() is None:
                self.pump(.05)
            if self.poll() is None:
                os.kill(self.pid, signal.SIGKILL)
                _, status = os.waitpid(self.pid, 0)
                self.status = os.waitstatus_to_exitcode(status)
        self.reading = False
        self.reader.join(timeout=1)
        self.save()
        os.close(self.fd)
        self.closed = True


class Acceptance:
    def __init__(self, args):
        self.args = args
        self.temp = Path(tempfile.mkdtemp(prefix='km-n-', dir='/tmp')).resolve()
        self.temp.chmod(0o700)
        self.state = self.temp / 's'
        self.state.mkdir(mode=0o700)
        self.bin = self.temp / 'bin'
        self.bin.mkdir(mode=0o700)
        self.terminals, self.sessions, self.owned = [], [], {}
        self.daemon = None
        self.metadata = {}
        for name in ('keelmux', 'keelmuxd', 'keelmux-supervisor'):
            shutil.copy2(Path(args.bin_dir) / name, self.bin / name)
            self.metadata[name] = {'sha256': digest((self.bin / name).read_bytes()),
                                   'version': run([str(self.bin / name), 'version'])}
        info = json.loads(run([str(self.bin / 'keelmux'), 'version', '--json']))
        assert info['commit'] == args.expected_source, 'frozen binary source metadata mismatch'
        for value in self.metadata.values():
            assert args.expected_source in value['version'], 'sibling binary source metadata mismatch'
        self.providers = {p: shutil.which(p) for p in args.providers}
        assert all(self.providers.values()), 'requested native CLI is not installed'
        self.report = {'schema': 1, 'source': info, 'harness_sha256': digest(Path(__file__).read_bytes()),
                       'utc': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()),
                       'platform': platform.platform(), 'binaries': self.metadata,
                       'providers': {p: run([path, '--version']) for p, path in self.providers.items()},
                       'qualification': ('diagnostic only: known outer client queries allowed' if args.allow_known_client_probe else 'source-built development binaries with installed native CLIs'),
                       'diagnostic_client_probe_exception': args.allow_known_client_probe,
                       'model_prompt_submitted': False, 'native_enter_submitted': False,
                       'credentials': 'existing native realm; no credential/config/trust changes by harness',
                       'harness_invokes_tmux': False, 'checks': [],
                       'missing_evidence': ['authenticated model turn and verified effective account/model',
                                            'installed release package and installer for this source',
                                            'host reboot/native resume', 'remote transport and Linux native CLIs',
                                            'native skill invocation and prompt delivery']}
        print('Private captures:', self.temp, flush=True)

    def api(self, path, body=None, expected=200):
        c = UnixHTTP(self.state / 'control.sock')
        c.request('GET' if body is None else 'POST', '/v1/'+path,
                  json.dumps(body) if body is not None else None,
                  {'X-Backplane-Protocol': 'bp/1', 'Content-Type': 'application/json'})
        response = c.getresponse()
        data = response.read()
        c.close()
        assert response.status == expected, '%s returned HTTP %d, expected %d' % (path, response.status, expected)
        return json.loads(data) if data else None

    def start(self):
        log = open(self.temp / 'daemon.log', 'ab')
        os.chmod(log.name, 0o600)
        self.daemon = subprocess.Popen([str(self.bin / 'keelmuxd'), '--state', str(self.state)], stdout=log, stderr=log)
        log.close()
        def ready():
            assert self.daemon.poll() is None, 'owned daemon exited before ready'
            return self.api('daemon')
        result = wait(ready, 'owned daemon did not become ready')
        assert result['pid'] == self.daemon.pid, 'unexpected daemon owns test socket'
        return result

    def inventory(self):
        return self.api('sessions')['sessions']

    def session(self, s):
        return next(v for v in self.inventory() if v['id'] == s['id'])

    def lease(self, s, ro=False):
        e = s['execution']
        return {'session_id': s['id'], 'execution_id': e['id'], 'generation': e['generation'],
                'operation_id': 'attach_'+uuid.uuid4().hex, 'client_id': 'native-acceptance-'+uuid.uuid4().hex,
                'read_only': ro}

    def receipt(self, s):
        for path in (self.state / 'native').glob('*.json'):
            value = json.loads(path.read_bytes())
            if value['ref']['execution_id'] == s['execution']['id']:
                return value
        raise AssertionError('owned native receipt not found')

    def inspect(self, s):
        r = self.receipt(s)
        path = Path(r['ref']['socket'])
        st = path.lstat()
        assert stat.S_ISSOCK(st.st_mode) and st.st_uid == os.getuid() and st.st_mode & 0o077 == 0, 'native socket ownership invalid'
        data = json.dumps({'op': 'inspect', 'auth': r['auth'], 'ref': r['ref']}).encode()
        with socket.socket(socket.AF_UNIX) as conn:
            conn.settimeout(3)
            conn.connect(str(path))
            peer = struct.unpack('i', conn.getsockopt(0, 2, 4))[0] if platform.system() == 'Darwin' else struct.unpack('3i', conn.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12))[0]
            assert peer == r['supervisor_pid'], 'native socket peer is not recorded helper'
            conn.sendall(struct.pack('!I', len(data))+data)
            def receive(n):
                out = bytearray()
                while len(out) < n:
                    chunk = conn.recv(n-len(out))
                    assert chunk, 'native inspect closed early'
                    out.extend(chunk)
                return bytes(out)
            size = struct.unpack('!I', receive(4))[0]
            assert 0 < size <= 2 << 20, 'native inspect response exceeded bound'
            reply = json.loads(receive(size))
        assert not reply.get('error') and reply.get('alive'), 'owned native child is not confirmed alive'
        assert reply['ref'] == s['execution']['runtime'], 'native exact execution reference changed'
        return reply

    def terminal(self, argv, label, columns=100, rows=34):
        t = Terminal(argv, self.args.workspace, self.temp / (label+'-'+uuid.uuid4().hex[:8]+'.bin'), columns, rows)
        t.label = label
        self.terminals.append(t)
        return t

    def drive(self, s, ro=False, columns=100, rows=34):
        req = self.lease(s, ro)
        before_clients = {(c['pid'], c['start']) for c in self.inspect(s).get('clients', [])}
        argv = [str(self.bin / 'keelmux'), '--entry', str(self.state / 'control.sock'), 'drive',
                '--host', s['host_id'], '--session', s['id'], '--execution', s['execution']['id'],
                '--generation', str(s['execution']['generation']), '--operation', req['operation_id'],
                '--client', req['client_id']]
        if ro:
            argv.append('--read-only')
        t = self.terminal(argv, s['execution']['provider']+('-ro' if ro else '-writer'), columns, rows)
        # Observe a newly registered exact native helper, not another viewer
        # already present. Startup query bytes are not rendered-screen evidence.
        def ready():
            t.pump()
            assert t.poll() is None, 'product drive exited before native attachment'
            clients = self.inspect(s).get('clients', [])
            attached = [c for c in clients if c['read_only'] == ro and (c['pid'], c['start']) not in before_clients]
            if attached and b'\x1b[2J\x1b[H' in t.output:
                t.native_client = attached[0]
                t.lease_request = req
                return True
            return False
        wait(ready, 'product drive did not reach native attachment readiness')
        return t

    def detach(self, t, s):
        assert t.poll() is None, 'attachment exited before detach was requested'
        t.send(b'\x07d')
        wait(t.exited, 'product drive did not detach: '+t.label)
        assert t.status == 0, 'product drive detach exited nonzero'
        t.close()
        return self.inspect(s)

    def snapshot(self, s):
        t = self.drive(s, True)
        t.pump(.3)
        text = plain(t.output)
        self.detach(t, s)
        return text

    def dimensions(self, s):
        pid = s['execution']['runtime']['pid']
        tty = run(['/bin/ps', '-p', str(pid), '-o', 'tty='])
        assert tty and tty not in ('??', '?') and re.fullmatch(r'[a-zA-Z0-9/]+', tty), 'owned native process has no observable PTY'
        path = '/dev/'+tty
        fd = os.open(path, os.O_RDONLY | os.O_NOCTTY | os.O_NONBLOCK)
        try:
            rows, cols, _, _ = struct.unpack('HHHH', fcntl.ioctl(fd, termios.TIOCGWINSZ, bytes(8)))
            return cols, rows
        finally:
            os.close(fd)

    def assert_same(self, s):
        now = self.session(s)
        assert now['execution']['runtime'] == s['execution']['runtime'] and now['execution']['state'] == 'running', 'recovery changed native execution reference/state'
        self.inspect(s)
        for pid, original in self.owned.items():
            assert alive(pid, original), 'owned child or supervisor lifetime changed'

    def check(self, label, fn):
        print('RUN', label, flush=True)
        try:
            result = fn()
            if label == 'native_default_daemon_start':
                result = {k: v for k, v in result.items() if k != 'instance'}
            self.report['checks'].append({'name': label, 'result': 'pass', 'detail': result})
            print('PASS', label, flush=True)
        except Exception as error:
            (self.temp / 'failure.txt').write_text(repr(error))
            (self.temp / 'failure.txt').chmod(0o600)
            self.report['checks'].append({'name': label, 'result': 'fail', 'failure': str(error) if isinstance(error, AssertionError) else type(error).__name__})
            print('FAIL', label, type(error).__name__, flush=True)
            raise

    def journey(self, provider):
        def step(name):
            print('STEP', provider, name, flush=True)
        argv = [self.providers[provider]]
        if provider == 'codex':
            argv += ['--sandbox', 'read-only', '--config', 'check_for_update_on_startup=false']
        request = {'operation_id': 'op_'+uuid.uuid4().hex, 'name': provider+' native qualification',
                   'workspace': self.args.workspace, 'provider': provider,
                   'provider_version': self.report['providers'][provider], 'argv': argv}
        launched = self.api('launch', request)
        assert launched['operation']['outcome'] == 'completed', 'native launch was not confirmed'
        s = launched['session']
        self.sessions.append(s)
        r = self.receipt(s)
        assert r['ref']['backend'] == 'native', 'default backend was not native'
        for pid in (r['ref']['pid'], r['supervisor_pid']):
            self.owned[pid] = lifetime(pid)
            assert self.owned[pid] and 'Z' not in self.owned[pid][1], 'launched native process missing'
        time.sleep(1)  # Queries emitted before any downstream exists must work.
        writer = self.drive(s)
        marks = ('›', '»') if provider == 'codex' else ('❯',)
        def composer():
            text = writer.pump(.2)
            lower = text.lower()
            for phrase in ('do you trust', 'trust this folder', 'trust this workspace', 'choose a theme', 'update now', 'login required', 'sign in to continue'):
                assert phrase not in lower, 'native onboarding/permission blocker: '+phrase
            return any(mark in text for mark in marks)
        wait(composer, 'native composer did not become visible without input', 25)
        # keelmux imports the current TUI renderer, whose initialization emits
        # this exact local-terminal probe before drive starts. Record this
        # explicitly; it is not a replay of the provider's historical query.
        local_probe = b'\x1b]11;?\x1b\\\x1b[6n'
        native_output = bytes(writer.output)
        had_local_probe = native_output.startswith(local_probe)
        assert not had_local_probe or self.args.allow_known_client_probe, 'outer client emitted a terminal query before native attachment'
        if had_local_probe:
            native_output = native_output[len(local_probe):]
        assert b'\x1b[6n' not in native_output and b'\x1b]52;' not in native_output, 'terminal query/clipboard escaped supervisor authority'
        print('READY', provider, 'composer without native input', flush=True)
        writer.send('native draft café λ 🙂')
        def unicode_visible():
            screen = self.snapshot(s)
            return all(x in screen for x in ('café', 'λ', '🙂'))
        wait(unicode_visible, 'Unicode draft not visible in current rendered native screen')
        step('unicode_snapshot')
        writer.send(b'\x1b')
        writer.pump(.4)
        writer.resize(92, 29)
        wait(lambda: self.dimensions(s) == (92, 29), 'writer resize not applied to provider PTY')
        readonly = self.drive(s, True, 35, 12)
        readonly.send('READONLY_NATIVE_CHECK')
        readonly.resize(30, 10)
        readonly.pump(.2)
        assert self.dimensions(s) == (92, 29), 'read-only attachment resized provider PTY'
        assert 'READONLY_NATIVE_CHECK' not in self.snapshot(s), 'read-only attachment injected provider input'
        self.api('attach', self.lease(s), expected=409)
        self.detach(readonly, s)
        step('read_only_input_resize_and_detach')
        self.detach(writer, s)
        step('writer_detach')
        wait(lambda: not self.inspect(s).get('clients'), 'detach left a native attachment')
        self.assert_same(s)
        writer = self.drive(s)
        step('writer_reconnect')
        assert all(x in writer.pump(.4) for x in ('café', 'λ', '🙂')), 'reconnect did not restore visible Unicode draft'
        before_daemon = self.api('daemon')
        self.daemon.kill()
        self.daemon.wait(timeout=5)
        after_daemon = self.start()
        assert before_daemon['instance'] != after_daemon['instance'], 'daemon restart did not replace daemon instance'
        self.api('daemon/stop', {'instance': before_daemon['instance']}, expected=409)
        assert self.api('daemon')['instance'] == after_daemon['instance'], 'stale daemon stop changed replacement daemon'
        self.assert_same(s)
        step('daemon_restart_and_stale_instance')
        repeated = self.api('launch', request)
        assert repeated['session']['execution']['runtime'] == s['execution']['runtime'], 'same-operation recovery changed native process'
        assert len([v for v in self.inventory() if v['id'] == s['id']]) == 1, 'recovery duplicated registry session'
        writer.close()  # A broken transport may already have returned EOF.
        wait(lambda: not self.inspect(s).get('clients'), 'daemon loss left a managed attachment')
        # A killed authority cannot durably acknowledge releasing the previous
        # stream. The caller retains its exact operation/client request, recovers
        # that grant and explicitly releases its token after absence is observed.
        self.api('attach', self.lease(s), expected=409)
        recovered = self.api('attach', writer.lease_request)
        self.api('release', dict(writer.lease_request, token=recovered['lease']['token']), expected=204)
        writer = self.drive(s)
        step('writer_reconnect_after_daemon_restart')
        assert all(x in writer.pump(.4) for x in ('café', 'λ', '🙂')), 'daemon restart lost visible native draft'
        self.detach(writer, s)
        self.tui_journey(s)
        self.assert_same(s)
        return {'default_backend': 'native', 'composer_before_native_input': True,
                'outer_client_terminal_probe_observed': had_local_probe,
                'unsubmitted_unicode_draft': True, 'escape_forwarded': True,
                'writer_resize': [92, 29], 'read_only_input_and_resize_blocked': True,
                'second_writer_refused': True, 'ctrl_g_d_detach_exit_zero': True,
                'same_execution_after_reconnect_and_daemon_sigkill': True,
                'same_operation_no_relaunch': True, 'stale_daemon_stop_refused': True,
                'orphan_attach_grant_reconciled_and_explicitly_released': True,
                'tui_detach_and_saved_selection': True, 'ref_sha256': digest(json.dumps(r['ref'], sort_keys=True).encode()),
                'child_pid': r['ref']['pid'], 'child_process_start': r['ref']['process_start'],
                'supervisor_pid': r['supervisor_pid'], 'supervisor_process_start': r['supervisor_start'],
                'native_enter_count': 0, 'model_turn_count': 0}

    def tui_journey(self, s):
        ui = self.terminal([str(self.bin / 'keelmux'), '--state', str(self.state)], s['execution']['provider']+'-tui')
        wait(lambda: 'Keelmux' in ui.pump() and 'Model:' in plain(ui.output), 'TUI inventory did not load')
        assert self.inspect(s).get('writable_clients', 0) == 0, 'TUI Enter cannot be safely sent while native writer exists'
        ui.send(b'\x1b')
        ui.send('/')
        ui.send(s['execution']['provider'])
        ui.send(b'\r', tui_enter=True)
        wait(lambda: json.loads((self.state / 'place.json').read_bytes())['places']['']['filter'] == s['execution']['provider'], 'TUI filter was not saved')
        assert self.inspect(s).get('writable_clients', 0) == 0, 'search unexpectedly entered native mode'
        ui.send(b'\r', tui_enter=True)
        wait(lambda: (ui.pump() is not None) and self.inspect(s).get('writable_clients') == 1, 'TUI did not acquire actual native writer')
        ui.pump(.4)
        start = len(ui.output)
        ui.send(b'\x07d')
        wait(lambda: 'Returned to the same work and group' in plain(ui.output[start:]) or 'Returned to the same work and group' in ui.pump(), 'TUI detach did not return to inventory')
        place = json.loads((self.state / 'place.json').read_bytes())
        assert not place.get('pending_attachment'), 'TUI detach retained pending attachment'
        assert place['places']['']['session_id'] == s['id'], 'TUI detach lost exact selection'
        assert self.inspect(s).get('writable_clients', 0) == 0, 'TUI detach retained writer'
        ui.send('q')
        wait(ui.exited, 'TUI did not exit from browse mode')
        assert ui.status == 0, 'TUI exited nonzero'
        ui.close()

    def cleanup(self):
        issues = []
        for terminal in self.terminals:
            try:
                terminal.close()
            except Exception:
                issues.append('owned_terminal_cleanup_failed')
        if self.sessions and (self.daemon is None or self.daemon.poll() is not None):
            try:
                self.start()
            except Exception:
                issues.append('owned_daemon_recovery_for_cleanup_failed')
        for s in self.sessions:
            try:
                body = {'session_id': s['id'], 'execution_id': s['execution']['id'],
                        'generation': s['execution']['generation'], 'operation_id': 'stop_'+uuid.uuid4().hex}
                self.api('stop', body, expected=204)
            except Exception:
                issues.append('owned_native_exact_stop_failed')
        if self.daemon and self.daemon.poll() is None:
            try:
                status = self.api('daemon')
                self.api('daemon/stop', {'instance': status['instance']}, expected=204)
                self.daemon.wait(timeout=5)
            except Exception:
                self.daemon.terminate()
                self.daemon.wait(timeout=5)
                issues.append('owned_daemon_exact_stop_failed')
        deadline = time.monotonic()+5
        while time.monotonic() < deadline and any(alive(pid, original) for pid, original in self.owned.items()):
            time.sleep(.05)
        if any(alive(pid, original) for pid, original in self.owned.items()):
            issues.append('owned_native_process_remains_live')
        self.report['cleanup'] = {'owned_process_count': len(self.owned), 'owned_processes_no_longer_live': not issues,
                                  'exact_product_stop_requested': True, 'no_saved_pid_signals': True,
                                  'private_captures_retained': True, 'issues': issues}
        self.report['result'] = 'pass' if self.report['checks'] and all(c['result'] == 'pass' for c in self.report['checks']) and not issues else 'fail'
        self.args.report.parent.mkdir(parents=True, exist_ok=True)
        self.args.report.write_text(json.dumps(self.report, indent=2)+'\n')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--bin-dir', required=True)
    parser.add_argument('--expected-source', required=True)
    parser.add_argument('--workspace', required=True)
    parser.add_argument('--providers', nargs='+', choices=('codex', 'claude'), default=['codex', 'claude'])
    parser.add_argument('--report', type=Path, required=True)
    parser.add_argument('--allow-known-client-probe', action='store_true',
                        help='diagnostic only: record known pre-drive query prefix; default rejects it')
    args = parser.parse_args()
    args.workspace = str(Path(args.workspace).resolve(strict=True))
    test = Acceptance(args)
    failed = False
    try:
        test.check('native_default_daemon_start', test.start)
        for provider in args.providers:
            try:
                test.check(provider+'_native_transport_tui', lambda p=provider: test.journey(p))
            except Exception:
                failed = True
                # Each provider is independently qualified; no dialog is answered.
                for terminal in test.terminals:
                    terminal.close()
    finally:
        test.cleanup()
    return 1 if failed or test.report['result'] != 'pass' else 0


if __name__ == '__main__':
    sys.exit(main())
