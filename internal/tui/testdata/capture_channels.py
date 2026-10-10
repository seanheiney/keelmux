#!/usr/bin/env python3
"""Exercise actual TUI PTYs against an owned Unix authored-channel fixture.

This fixture never contacts a chat provider, launches a native CLI or starts a
daemon. PNGs rasterize interpreted PTY cells using capture.py's limited parser.
"""
import argparse
import fcntl
import hashlib
import http.server
import json
import os
from pathlib import Path
import pty
import select
import shutil
import signal
import socketserver
import struct
import tempfile
import termios
import threading
import time

from capture import Screen, join_child, sessions


def capture(binary, output, width, height):
    root = Path(tempfile.mkdtemp(prefix='km-inbox-', dir='/tmp')).resolve()
    root.chmod(0o700)
    state = root/'state'
    state.mkdir(mode=0o700)
    place = state/'place.json'
    native = {'thread_id':'thread_session_00','recipient_id':'lane_session_00','text':'native draft preserved','execution_id':'execution_session_00','generation':1}
    place.write_text(json.dumps({'version':1,'client_id':'owned_inbox_capture','places':{},'drafts':[native]}))
    place.chmod(0o600)
    thread = 'thread_session_00'
    binding = {'binding_id':'binding_owned','thread_id':thread,'provider':'mattermost','endpoint':'https://chat.example.test','realm_id':'realm_owned','bot_identity':'bot_owned','channel_id':'channel_owned','root_id':'root_owned','active':True,'semantic_dispatch':False,'intake':'unavailable','coverage':'since_activation_snapshots','history_gap':True,'last_check':'2026-10-10T20:00:00Z'}
    unrelated = dict(binding, binding_id='binding_other', thread_id='thread_other', channel_id='other_channel')
    notes = [{'ID':'note_incoming','ThreadID':thread,'BindingID':binding['binding_id'],'Sequence':1,'AuthorID':'author_owned','Text':'Received note: /run $(touch never)\nInert authored text; no model work.','CreatedAt':'2026-10-10T20:00:00Z','RemoteID':'remote_owned','Historical':False}]
    for seq in range(2,102):
        notes.append({'ID':'note_%d'%seq,'ThreadID':thread,'BindingID':binding['binding_id'],'Sequence':seq,'AuthorID':'author_owned','Text':'Later authored note reached by paging' if seq==101 else 'Owned earlier note %d'%seq,'CreatedAt':'2026-10-10T20:00:00Z','RemoteID':'remote_%d'%seq,'Historical':False})
    calls, posts, receipts = [], [], {}
    observed = {'durable_before_post':False, 'enter_never_sent':False}
    guard = threading.Lock()
    class Handler(http.server.BaseHTTPRequestHandler):
        def reply(self, payload, status=200):
            self.send_response(status)
            self.send_header('Content-Type','application/json')
            self.end_headers()
            self.wfile.write(json.dumps(payload).encode())
        def do_GET(self):
            with guard:
                calls.append(('GET',self.path))
                if self.path == '/v1/sessions': self.reply({'version':'bp/1','sessions':sessions()})
                elif self.path == '/v1/channels': self.reply([binding, unrelated])
                elif self.path.startswith('/v1/channels/history?'):
                    from urllib.parse import parse_qs,urlparse
                    after=int(parse_qs(urlparse(self.path).query)['after'][0])
                    self.reply({'thread_id':thread,'messages':[n for n in notes if n['Sequence']>after][:100],'snapshot_only':True,'semantic_dispatch':False})
                elif self.path.startswith('/v1/channels/delivery?'):
                    from urllib.parse import parse_qs, urlparse
                    ident = parse_qs(urlparse(self.path).query)['id'][0]
                    self.reply(receipts[ident] if ident in receipts else {'error':'not found'}, 200 if ident in receipts else 404)
                else: self.reply({'error':'unexpected route'},405)
        def do_POST(self):
            with guard:
                calls.append(('POST',self.path))
                if self.path != '/v1/channels/send': self.reply({'error':'unexpected mutation'},405);return
                request = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
                posts.append(request)
                saved = json.loads(place.read_text())
                intent = saved['pending_channel_send']
                expected = {'message_id':request['message_id'],'binding_id':request['binding_id'],'thread_id':thread,'text':request['text']}
                observed['durable_before_post'] = intent == expected
                # Exact shared Go struct field order/casing; fixture text has no
                # characters requiring Go's HTML escaping. Public hash is content
                # integrity only, not authentication of an external service.
                data = {'BindingID':request['binding_id'],'MessageID':request['message_id'],'ThreadID':thread,'Provider':'mattermost','Endpoint':binding['endpoint'],'Realm':binding['realm_id'],'Self':binding['bot_identity'],'Channel':binding['channel_id'],'Root':binding['root_id'],'Author':'local-operator','Text':request['text']}
                digest = hashlib.sha256(json.dumps(data,ensure_ascii=False,separators=(',',':')).encode()).digest()
                receipts[request['message_id']] = {'ID':request['message_id'],'MessageID':request['message_id'],'BindingID':request['binding_id'],'State':'queued','PayloadHash':list(digest),'UpdatedAt':'2026-10-10T20:00:00Z'}
                notes.append({'ID':request['message_id'],'ThreadID':thread,'BindingID':request['binding_id'],'Sequence':len(notes)+1,'AuthorID':'local-operator','Text':request['text'],'CreatedAt':'2026-10-10T20:00:00Z','RemoteID':'','Historical':False})
                # Lose the acknowledgement after the owned durable fixture
                # accepted it. Actual reconnect must query, never post again.
                self.close_connection = True
        def log_message(self,*args): pass
    class Server(socketserver.UnixStreamServer): pass
    server = Server(str(root/'fixture.sock'),Handler)
    (root/'fixture.sock').chmod(0o600)
    threading.Thread(target=server.serve_forever,daemon=True).start()
    pid,fd = pty.fork()
    if pid == 0:
        fcntl.ioctl(0,termios.TIOCSWINSZ,struct.pack('HHHH',height,width,0,0))
        os.environ['TERM']='xterm-256color';os.environ.pop('NO_COLOR',None)
        os.execv(binary,[binary,'--state',str(state),'--entry',str(root/'fixture.sock')])
    raw = bytearray()
    captures = []
    def wait(predicate):
        deadline=time.monotonic()+8
        while time.monotonic()<deadline:
            if select.select([fd],[],[],0.08)[0]:
                data=os.read(fd,65536);raw.extend(data)
                if b'\x1b[6n' in data: os.write(fd,b'\x1b[1;1R')
            screen=Screen(width,height);screen.feed(raw.decode('utf-8','replace'))
            visible='\n'.join(''.join(ch for ch,_ in row) for row in screen.cells)
            if predicate(visible): return screen
        failed=output/('failed-%dx%d'%(width,height));screen.save(failed);failed.with_suffix('.ansi').write_bytes(raw)
        raise AssertionError('owned PTY semantic screen predicate did not appear')
    def record(label, screen):
        path=output/('%s-%dx%d'%(label,width,height));screen.save(path);path.with_suffix('.ansi').write_bytes(raw)
        captures.append({'label':label,'raw_sha256':hashlib.sha256(raw).hexdigest()})
    clean = False
    try:
        wait(lambda s:'api / tests' in s)
        os.write(fd,b'c')
        record('inbox',wait(lambda s:'Received note' in s))
        with guard:
            if posts: raise AssertionError('opening authored inbox caused effect')
        os.write(fd,b']')
        record('next-page',wait(lambda s:'Later authored note reached' in s))
        os.write(fd,b'[')
        record('previous-page',wait(lambda s:'Received note' in s))
        os.write(fd,b'b')
        record('destination',wait(lambda s:'choose destination' in s and 'binding_owned' in s))
        os.write(fd,b'\r')
        record('chosen-inbox',wait(lambda s:'authored inbox' in s and 'unchosen' not in s and 'history gap' in s))
        os.write(fd,b'p')
        wait(lambda s:'channel reply' in s)
        os.write(fd,b'first line\rsecond line')
        record('multiline-reply',wait(lambda s:'first line' in s and 'second line_' in s))
        saved=json.loads(place.read_text())
        with guard:
            observed['enter_never_sent']=not posts
        if saved['version']!=2 or saved['channel_drafts'][0]['text']!='first line\nsecond line' or saved['drafts']!=[native]:
            raise AssertionError('newlines or native draft/state migration changed')
        os.write(fd,b'\x13')  # Actual Ctrl-S; the sole first-effect control.
        record('pending',wait(lambda s:'Saved reply:' in s and 'unknown' in s))
        pending=json.loads(place.read_text())['pending_channel_send']
        os.write(fd,b'cannot edit frozen')
        wait(lambda s:'immutable' in s)
        if json.loads(place.read_text())['pending_channel_send']!=pending: raise AssertionError('frozen intent edited')
        os.write(fd,b'\x1b')
        wait(lambda s:'authored inbox' in s)
        os.write(fd,b'\x1b')
        wait(lambda s:'api / tests' in s)
        os.write(fd,b'q')
        if not join_child(pid,fd,3): raise AssertionError('first owned TUI did not quit without signals')
        os.close(fd);fd=None
        # Restart the same actual binary. Init may only query exact receipt.
        pid,fd=pty.fork()
        if pid==0:
            fcntl.ioctl(0,termios.TIOCSWINSZ,struct.pack('HHHH',height,width,0,0))
            os.environ['TERM']='xterm-256color';os.environ.pop('NO_COLOR',None)
            os.execv(binary,[binary,'--state',str(state),'--entry',str(root/'fixture.sock')])
        raw=bytearray()
        record('restarted-queued',wait(lambda s:'Queued locally' in s and 'api / tests' in s))
        saved=json.loads(place.read_text())
        with guard:
            if len(posts)!=1 or not observed['durable_before_post'] or not observed['enter_never_sent']:
                raise AssertionError('durable first effect or restart no-replay criterion failed')
        if saved.get('pending_channel_send') or saved['channel_drafts'][0]['text'] or saved['drafts']!=[native] or saved['places']['']['session_id']!='session_00':
            raise AssertionError('restart receipt failed exact draft clearing or native place preservation')
        os.write(fd,b'q')
        if not join_child(pid,fd,3): raise AssertionError('restarted owned TUI did not quit without signals')
        clean=True
    finally:
        # Signals only to this helper's still-owned child handle on failed
        # capture. A forced cleanup never satisfies the successful q predicate.
        try:
            if os.waitpid(pid,os.WNOHANG)[0]==0:
                os.kill(pid,signal.SIGTERM)
                if not join_child(pid,fd,2): os.kill(pid,signal.SIGKILL);os.waitpid(pid,0)
        except ChildProcessError: pass
        if fd is not None: os.close(fd)
        server.shutdown();server.server_close()
        if clean: shutil.rmtree(root)
    return {'size':[width,height],'binary_sha256':hashlib.sha256(Path(binary).read_bytes()).hexdigest(),'captures':captures,'actual_local_queue_posts':len(posts),'actual_external_posts':0,'durable_intent_before_local_request':observed['durable_before_post'],'enter_only_newline':observed['enter_never_sent'],'restart_recovery_queried_exact_receipt_without_repost':True,'later_note_101_and_previous_page_visible':True,'native_draft_and_selection_preserved':True,'owned_state_removed':not root.exists(),'tui_exit_zero_without_signals':clean,'capture_kind':'Actual PTY; cell rasterization, not native Terminal screenshot','calls':calls}


if __name__=='__main__':
    parser=argparse.ArgumentParser();parser.add_argument('--binary',required=True);parser.add_argument('--output',required=True);args=parser.parse_args()
    output=Path(args.output).resolve();output.mkdir(parents=True,exist_ok=True)
    receipt=[]
    for width,height in ((80,24),(120,32),(40,14)):
        receipt.append(capture(str(Path(args.binary).resolve()),output,width,height))
    (output/'receipt.json').write_text(json.dumps(receipt,indent=2)+'\n')
    print(json.dumps({'captures':sum(len(r['captures']) for r in receipt),'output':str(output),'status':'pass'}))
