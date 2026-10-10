#!/usr/bin/env python3
"""Executable restart-authority adverse fixtures on one owned computer.

A private proxy discards a completed home attach acknowledgement and counts
upstream requests. It never prints or stores the private lease token in evidence.
"""
import argparse
import hashlib
import http.server
import json
from pathlib import Path
import select
import shutil
import socket
import socketserver
import subprocess
import threading

from gateway_acceptance import GatewayAcceptance
from native_acceptance import Terminal, UnixHTTP, wait_for


class Proxy(socketserver.ThreadingUnixStreamServer):
    daemon_threads = True

    def __init__(self, path, home):
        self.home = home
        self.attach_calls = 0
        self.drop_next_attach = False
        self.dropped_home_grant = None
        self.lock = threading.Lock()
        super().__init__(str(path), Handler)
        Path(path).chmod(0o600)
        self.thread = threading.Thread(target=self.serve_forever, daemon=True)
        self.thread.start()


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *args):
        pass

    def do_GET(self):
        self.forward()

    def do_POST(self):
        self.forward()

    def forward(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        if self.path == "/v1/terminal":
            self.terminal(body)
            return
        drop = False
        if self.path == "/v1/attach":
            with self.server.lock:
                self.server.attach_calls += 1
                drop = self.server.drop_next_attach
                self.server.drop_next_attach = False
        c = UnixHTTP(self.server.home)
        c.request(self.command, self.path, body or None, {"Content-Type": "application/json", "X-Backplane-Protocol": "bp/1"})
        r = c.getresponse()
        data = r.read()
        status = r.status
        c.close()
        if drop:
            assert status == 200, "home did not complete the intentionally lost attach acknowledgement"
            self.server.dropped_home_grant = json.loads(data)
            self.close_connection = True
            self.connection.shutdown(socket.SHUT_RDWR)
            return
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(data)
        self.close_connection = True

    def terminal(self, body):
        # Relay the actual bp-terminal upgrade and frames without reinterpretation.
        home = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        home.connect(str(self.server.home))
        headers = "".join(name+": "+value+"\r\n" for name, value in self.headers.items())
        initial = (self.requestline+"\r\n"+headers+"\r\n").encode()+body
        home.sendall(initial)
        self.close_connection = True
        try:
            while True:
                ready, _, _ = select.select([home, self.connection], [], [], 5)
                for source in ready:
                    data = source.recv(65536)
                    if not data:
                        return
                    (self.connection if source is home else home).sendall(data)
        except (OSError, ConnectionError):
            pass
        finally:
            home.close()


class AuthorityAcceptance(GatewayAcceptance):
    def __init__(self, root, report, only="all"):
        super().__init__(root, report)
        self.report["gateway_harness_sha256"] = self.report["harness_sha256"]
        self.report["harness_sha256"] = hashlib.sha256(Path(__file__).read_bytes()).hexdigest()
        self.report["claim"] = "original principal retains uncertain and direct-open acquisition ownership across executable entry restart"
        self.report["selected_groups"] = only
        self.only = only
        self.proxy = None

    def configured(self, principal):
        value = json.loads(self.configuration.read_text())
        value["principal"] = principal
        value["peers"][0]["socket"] = str(self.temp / "proxy.sock")
        self.configuration.write_text(json.dumps(value)+"\n")
        self.configuration.chmod(0o600)

    def prepare(self):
        result = self.directory()
        self.kill_entry()
        self.proxy = Proxy(self.temp / "proxy.sock", self.homes[0].state / "control.sock")
        self.configured("original-principal")
        self.start_entry()
        return result

    def uncertain_ack(self):
        req = self.homes[0].lease(self.work[0], "public-client-id")
        self.proxy.drop_next_attach = True
        self.api("attach", req, status=503)
        grant = wait_for(lambda: self.proxy.dropped_home_grant)
        self.kill_entry()
        self.configured("different-principal")
        self.start_entry()
        before = self.proxy.attach_calls
        self.api("attach", req, status=409)
        assert self.proxy.attach_calls == before, "public acquisition replay reached the home under a different principal"
        self.kill_entry()
        self.configured("original-principal")
        self.start_entry()
        recovered = self.api("attach", req)
        assert recovered["lease"] == grant["lease"], "original uncertain acquisition did not recover its exact home grant"
        self.api("release", dict(req, token=recovered["lease"]["token"]), status=204)
        self.stable(0)
        return {"home_attach_completed_response_discarded": True, "entry_restarted_before_ack_recovery": True, "different_principal_public_ids_refused_before_home_call": True, "original_principal_same_grant_recovered": True, "token_in_report": False}

    def direct_open(self):
        req = self.homes[0].lease(self.work[0], "public-direct-client")
        s = self.work[0]
        argv = [self.bp, "--entry", str(self.socket), "drive", "--host", s["host_id"], "--session", req["session_id"], "--execution", req["execution_id"], "--generation", str(req["generation"]), "--operation", req["operation_id"], "--client", req["client_id"]]
        t = Terminal(argv, str(self.homes[0].workspace))
        self.terminals.append(t)
        wait_for(lambda: self.homes[0].tmux("list-clients", "-F", "#{client_readonly}") == "0")
        t.expect("FIXTURE READY")
        self.kill_entry()
        wait_for(lambda: not self.homes[0].tmux("list-clients", "-F", "#{client_readonly}"))
        t.close()
        self.terminals.remove(t)
        self.configured("different-principal")
        self.start_entry()
        before = self.proxy.attach_calls
        self.api("attach", req, status=409)
        assert self.proxy.attach_calls == before, "direct-open acquisition lost its principal claim after entry restart"
        self.stable(0)
        return {"actual_drive_without_prior_entry_attach": True, "entry_sigkill": True, "different_principal_public_ids_refused_before_home_call": True, "home_execution_stable": True}

    def run(self):
        try:
            self.check("executable homes and counted private proxy", self.prepare)
            if self.only in ("all", "uncertain"):
                self.check("uncertain home acknowledgement original-principal restart fence", self.uncertain_ack)
            if self.only in ("all", "direct"):
                self.check("direct native open original-principal restart fence", self.direct_open)
        finally:
            for t in self.terminals:
                t.close()
            if self.gateway and self.gateway.poll() is None:
                self.gateway.terminate()
                try:
                    self.gateway.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    self.gateway.kill()
                    self.gateway.wait()
            if self.proxy:
                self.proxy.shutdown()
                self.proxy.server_close()
                self.proxy.thread.join(timeout=5)
            for home in self.homes:
                if home.daemon and home.daemon.poll() is None:
                    home.daemon.terminate()
                    try:
                        home.daemon.wait(timeout=5)
                    except subprocess.TimeoutExpired:
                        home.daemon.kill()
                        home.daemon.wait()
                try:
                    home.tmux("kill-server")
                except subprocess.CalledProcessError:
                    pass
                shutil.rmtree(home.temp)
            self.report_path.parent.mkdir(parents=True, exist_ok=True)
            self.report_path.write_text(json.dumps(self.report, indent=2)+"\n")
            shutil.rmtree(self.temp)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", default=str(Path(__file__).resolve().parents[1]))
    parser.add_argument("--report", required=True)
    parser.add_argument("--only", choices=("all", "uncertain", "direct"), default="all")
    args = parser.parse_args()
    AuthorityAcceptance(args.root, args.report, args.only).run()
