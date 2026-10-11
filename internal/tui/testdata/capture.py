#!/usr/bin/env python3
"""Capture real switcher PTY cells against owned deterministic Unix inventory.

PNG output is a cell rasterization, not a macOS Terminal screenshot. Pillow is
optional; text/raw captures use only the standard library. No runtime is launched.
"""
import argparse
import fcntl
import hashlib
import http.server
import json
import os
from pathlib import Path
import pty
import re
import select
import shutil
import signal
import socketserver
import struct
import tempfile
import termios
import threading
import time
import unicodedata


class Screen:
    def __init__(self, width, height):
        self.width, self.height, self.x, self.y = width, height, 0, 0
        self.cells = [[(' ', ()) for _ in range(width)] for _ in range(height)]
        self.style = ()

    def feed(self, text):
        for part in re.findall(r'\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07]*(?:\x07|\x1b\\)|[^\x1b]+', text):
            if part.startswith('\x1b['):
                code, raw = part[-1], part[2:-1]
                args = [int(n) if n else 0 for n in raw.lstrip('?').split(';')]
                n = args[0] or 1
                if code in 'Hf':
                    self.y = max(0, min(self.height-1, n-1))
                    self.x = max(0, min(self.width-1, (args[1] if len(args)>1 and args[1] else 1)-1))
                elif code == 'A': self.y = max(0, self.y-n)
                elif code == 'B': self.y = min(self.height-1, self.y+n)
                elif code == 'C': self.x = min(self.width-1, self.x+n)
                elif code == 'D': self.x = max(0, self.x-n)
                elif code == 'G': self.x = min(self.width-1, n-1)
                elif code == 'J' and args[0] in (2, 3): self.cells = [[(' ', ()) for _ in range(self.width)] for _ in range(self.height)]
                elif code == 'K':
                    start, end = (0, self.width) if args[0]==2 else ((0, self.x+1) if args[0]==1 else (self.x, self.width))
                    for x in range(start, end): self.cells[self.y][x] = (' ', ())
                elif code == 'm': self.style = () if args==[0] else tuple(args)
            elif not part.startswith('\x1b'):
                for ch in part:
                    if ch=='\r': self.x=0
                    elif ch=='\n': self.y=min(self.height-1,self.y+1)
                    elif ch=='\b': self.x=max(0,self.x-1)
                    elif not unicodedata.category(ch).startswith('C'):
                        if self.x>=self.width: self.x=0;self.y=min(self.height-1,self.y+1)
                        width=2 if unicodedata.east_asian_width(ch) in ('W','F') else 1
                        self.cells[self.y][self.x]=(ch,self.style)
                        if width==2 and self.x+1<self.width: self.cells[self.y][self.x+1]=('',self.style)
                        self.x+=width

    def save(self, path):
        path.with_suffix('.txt').write_text('\n'.join(''.join(ch for ch,_ in row).rstrip() for row in self.cells)+'\n')
        try:
            from PIL import Image, ImageDraw, ImageFont
        except ImportError:
            return
        font=ImageFont.truetype('/System/Library/Fonts/Menlo.ttc', 16)
        cw, ch, pad = 10, 23, 18
        image=Image.new('RGB',(self.width*cw+pad*2,self.height*ch+pad*2),'#15191e');draw=ImageDraw.Draw(image)
        for y,row in enumerate(self.cells):
            for x,(text,style) in enumerate(row):
                fg='#d7dee7'
                if 90 in style or 2 in style: fg='#7e8998'
                if 36 in style: fg='#79d4d8'
                if 33 in style: fg='#e6bd75'
                if 7 in style:
                    draw.rectangle((pad+x*cw,pad+y*ch,pad+(x+1)*cw-1,pad+(y+1)*ch-1),fill='#d7dee7');fg='#15191e'
                draw.text((pad+x*cw,pad+y*ch),text,font=font,fill=fg)
        image.save(path.with_suffix('.png'))


def sessions():
    names=['api / tests','billing / migration','cli / docs','data / import','design / review','edge / retries','gateway / reconnect','infra / deploy','search / indexing','worker / queues','zeta / cleanup']
    result=[]
    for i,name in enumerate(names):
        ident='session_%02d'%i
        result.append({'id':ident,'host_id':'host_local','work_id':'work_'+ident,'thread_id':'thread_'+ident,'lane_id':'lane_'+ident,'name':name,'workspace':'/work/keelmux/'+name.split(' / ')[0],'mode':'managed','linked':True,'capabilities':{'native_attach':i!=5,'read_only_attach':i!=5,'explanation':'Native attach supported; semantic delivery unavailable.'},'execution':{'id':'execution_'+ident,'generation':1,'provider':'codex' if i%2==0 else 'claude','state':'missing' if i==5 else 'running','actual_model':{'value':'','source':'unknown'},'account':{'value':'','source':'unknown'}}})
    return result


def join_child(pid, fd, seconds):
    deadline=time.monotonic()+seconds
    while time.monotonic()<deadline:
        try:
            exited,status=os.waitpid(pid,os.WNOHANG)
            if exited:
                return os.waitstatus_to_exitcode(status)==0
        except ChildProcessError:
            return True
        # Bubble Tea restores/redraws the screen on exit. A PTY owner must
        # keep reading; otherwise its own display buffer can block shutdown.
        if select.select([fd],[],[],0.05)[0]:
            try: os.read(fd,65536)
            except OSError: pass
    return False


def capture(binary, output, width, height, mode, prepare_state=None):
    root=Path(tempfile.mkdtemp(prefix='km-tui-',dir='/tmp')).resolve();root.chmod(0o700)
    sock=root/'fixture.sock'
    requests=[]
    class Handler(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            requests.append(('GET',self.path))
            if mode=='offline':
                self.send_response(503);payload={'error':'Owned fixture unavailable'}
            else:
                self.send_response(200);payload={'version':'bp/1','sessions':[] if mode=='empty' else sessions()}
            self.send_header('Content-Type','application/json');self.end_headers();self.wfile.write(json.dumps(payload).encode())
        def do_POST(self):
            requests.append(('POST',self.path));self.send_error(405)
        def log_message(self,*args): pass
    class Server(socketserver.UnixStreamServer): pass
    server=Server(str(sock),Handler);sock.chmod(0o600)
    threading.Thread(target=server.serve_forever,daemon=True).start()
    state=root/'state';state.mkdir(mode=0o700)
    place=state/'place.json'
    place.write_text(json.dumps({'version':1,'client_id':'client_owned_capture','places':{},'groups':[{'id':'group_api','name':'API','members':[{'kind':'workspace','workspace':'/work/keelmux/api'}]},{'id':'group_infra','name':'Infrastructure','members':[{'kind':'workspace','workspace':'/work/keelmux/infra'}]}]}));place.chmod(0o600)
    if prepare_state is not None:
        prepare_state(state)
    pid,fd=pty.fork()
    if pid==0:
        fcntl.ioctl(0,termios.TIOCSWINSZ,struct.pack('HHHH',height,width,0,0))
        os.environ['TERM']='xterm-256color';os.environ.pop('NO_COLOR',None)
        os.execv(binary,[binary,'--state',str(root/'state'),'--entry',str(sock)])
    raw=bytearray();deadline=time.monotonic()+12;last_data=time.monotonic();forced_shutdown=False
    try:
        edited=False;text_entered=False
        expected={'inventory':'api / tests','empty':'No sessions yet.','offline':'OFFLINE','groups':'groups','draft':'local draft','usage':'User-entered calls: 1'}[mode]
        while time.monotonic()<deadline:
            if select.select([fd],[],[],0.1)[0]:
                data=os.read(fd,65536);raw.extend(data);last_data=time.monotonic()
                if b'\x1b[6n' in data: os.write(fd,b'\x1b[1;1R')
                if not edited and b'api / tests' in raw:
                    if mode=='groups': os.write(fd,b'g')
                    elif mode=='draft': os.write(fd,b'p')
                    elif mode=='usage': os.write(fd,b'u')
                    edited=True
                if mode=='draft' and not text_entered and b'local draft' in raw:
                    os.write(fd,b'Local addressed draft; no delivery.');text_entered=True
            screen=Screen(width,height);screen.feed(raw.decode('utf-8','replace'))
            visible='\n'.join(''.join(ch for ch,_ in row) for row in screen.cells)
            if expected in visible and (mode!='draft' or 'Local addressed' in visible) and time.monotonic()-last_data>0.25:
                break
        screen=Screen(width,height);screen.feed(raw.decode('utf-8','replace'))
        visible='\n'.join(''.join(ch for ch,_ in row) for row in screen.cells)
        if expected not in visible:
            failed=output.with_name(output.name+'-failed');screen.save(failed);failed.with_suffix('.ansi').write_bytes(raw)
            raise AssertionError('actual PTY did not show requested owned fixture mode: '+mode)
        screen.save(output)
        output.with_suffix('.ansi').write_bytes(raw)
        if mode in ('groups','draft','usage'):
            os.write(fd,b'\x1b');time.sleep(0.15)
        os.write(fd,b'q')
        if not join_child(pid,fd,3):
            forced_shutdown=True
            os.kill(pid,signal.SIGTERM)
            if not join_child(pid,fd,2): os.kill(pid,signal.SIGKILL);os.waitpid(pid,0)
    finally:
        try:
            if os.waitpid(pid,os.WNOHANG)[0]==0:
                os.kill(pid,signal.SIGTERM)
                if not join_child(pid,fd,2): os.kill(pid,signal.SIGKILL);os.waitpid(pid,0)
        except ChildProcessError:
            pass
        os.close(fd);server.shutdown();server.server_close();shutil.rmtree(root)
    if forced_shutdown: raise AssertionError('owned TUI did not exit cleanly from q')
    if not requests or any(row!=('GET','/v1/sessions') for row in requests):
        raise AssertionError('visual fixture unexpectedly used a mutation or other route')
    return {'size':[width,height],'mode':mode,'binary_sha256':hashlib.sha256(Path(binary).read_bytes()).hexdigest(),'raw_sha256':hashlib.sha256(raw).hexdigest(),'capture':'Actual PTY; PNG rasterizes interpreted cells, not native Terminal screenshot','owned_state_removed':not root.exists(),'tui_quit_exit_zero_without_signals':True,'actual_inventory_gets':len(requests),'actual_mutating_requests':0}


if __name__=='__main__':
    parser=argparse.ArgumentParser();parser.add_argument('--binary',required=True);parser.add_argument('--output',required=True);args=parser.parse_args()
    output=Path(args.output);output.mkdir(parents=True,exist_ok=True)
    receipt=[]
    for width,height,mode in ((80,24,'inventory'),(120,32,'inventory'),(40,14,'inventory'),(80,24,'empty'),(80,24,'offline'),(80,24,'groups'),(80,24,'draft')):
        receipt.append(capture(str(Path(args.binary).resolve()),output/('%s-%dx%d'%(mode,width,height)),width,height,mode))
    (output/'receipt.json').write_text(json.dumps(receipt,indent=2)+'\n')
    print(json.dumps({'captures':len(receipt),'output':str(output)}))
