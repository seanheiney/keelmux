#!/usr/bin/env python3
"""Owned, opt-in Mattermost/SQLite/adapter acceptance. Never uses an existing server."""
import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import secrets
import signal
import ssl
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.error
import urllib.request

MM_IMAGE = "mattermost/mattermost-team-edition@sha256:14a2de6b71fe5f60660fb71ef2f020c5758d8559fdd35a3fca429d301b781154"
PG_IMAGE = "postgres@sha256:16bc17c64a573ef34162af9298258d1aec548232985b33ed7b1eac33ba35c229"
LABEL = "io.keelmux.mattermost-acceptance"
ROOT = Path(__file__).resolve().parents[1]


class Failure(Exception):
    pass


def private(path, data):
    with open(path, "wb") as stream:
        os.chmod(path, 0o600)
        stream.write(data if isinstance(data, bytes) else data.encode())


class Harness:
    def __init__(self, allow_dirty):
        self.state = Path(tempfile.mkdtemp(prefix="keelmux-mm-", dir=ROOT / "work"))
        os.chmod(self.state, 0o700)
        self.nonce = secrets.token_hex(12)
        self.names = {"network": "keelmux-mm-" + self.nonce, "postgres": "keelmux-mm-pg-" + self.nonce, "server": "keelmux-mm-server-" + self.nonce}
        self.ids = {}
        self.allow_dirty = allow_dirty
        self.secrets = []
        self.receipt = {"schema": 1, "scope": "owned real Mattermost service/adapter acceptance", "passed": False, "failure_kind": None,
                        "mattermost_image": MM_IMAGE, "postgres_image": PG_IMAGE,
                        "excluded": ["product bpd/TUI/customer deployment", "Slack/IRC live servers", "model dispatch", "native terminal input", "exactly-once posts", "database downgrade"], "cleanup": {}}

    def run(self, argv, *, data=None, timeout=30):
        try:
            result = subprocess.run(argv, cwd=ROOT, input=data, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout)
        except (subprocess.TimeoutExpired, OSError) as exc:
            private(self.state / "command-exception.txt", repr(exc))
            raise Failure("owned_command_failed") from exc
        with open(self.state / "commands.log", "ab") as stream:
            os.chmod(stream.name, 0o600)
            stream.write(result.stderr + (result.stdout if result.returncode else b""))
        if result.returncode:
            raise Failure("owned_command_failed")
        return result.stdout

    def metadata(self, kind, ident):
        return json.loads(self.run(["docker", kind, "inspect", ident]))[0]

    def remember(self, kind, ident):
        self.ids[kind] = ident.strip()
        private(self.state / "owned-resources.json", json.dumps(self.ids))
        obj = self.metadata("network" if kind == "network" else "container", self.ids[kind])
        labels = obj.get("Labels", {}) if kind == "network" else obj["Config"].get("Labels", {})
        name = obj["Name"].lstrip("/")
        if labels.get(LABEL) != self.nonce or name != self.names[kind]:
            raise Failure("resource_ownership_mismatch")

    def tls(self):
        ca_key, ca, key, csr, cert = [self.state / name for name in ("ca-key.pem", "ca.pem", "server-key.pem", "server.csr", "server.pem")]
        self.secrets += [ca_key, key]
        for p in (ca_key, ca, key, csr, cert):
            private(p, b"")
        self.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-sha256", "-nodes", "-days", "1", "-subj", "/CN=Keelmux Owned Acceptance CA", "-keyout", str(ca_key), "-out", str(ca)])
        self.run(["openssl", "req", "-newkey", "rsa:2048", "-sha256", "-nodes", "-subj", "/CN=localhost", "-keyout", str(key), "-out", str(csr)])
        ext = self.state / "cert.ext"
        private(ext, "subjectAltName=IP:127.0.0.1,DNS:localhost\nkeyUsage=digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\n")
        self.run(["openssl", "x509", "-req", "-sha256", "-days", "1", "-in", str(csr), "-CA", str(ca), "-CAkey", str(ca_key), "-CAcreateserial", "-extfile", str(ext), "-out", str(cert)])
        for p in (ca_key, ca, key, csr, cert):
            os.chmod(p, 0o600)
        self.receipt["ca_sha256"] = hashlib.sha256(ca.read_bytes()).hexdigest()
        return ca, cert, key

    def request(self, method, path, token=None, body=None):
        data = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(self.endpoint + "/api/v4" + path, data=data, method=method, headers={"Content-Type": "application/json"})
        if token:
            req.add_header("Authorization", "Bearer " + token)
        try:
            with self.opener.open(req, timeout=15) as response:
                raw = response.read(1 << 20)
                return json.loads(raw), dict(response.headers)
        except Exception as exc:
            # Original diagnostic stays private and is never interpolated into a receipt.
            private(self.state / "http-error.txt", repr(exc))
            raise Failure("fixture_api_failed") from exc

    def start(self):
        self.receipt["source"] = self.run(["git", "rev-parse", "HEAD"]).decode().strip()
        dirty = self.run(["git", "status", "--porcelain"]).decode()
        self.receipt["source_clean"] = not dirty
        if dirty and not self.allow_dirty:
            raise Failure("source_not_clean")
        source = self.run(["git", "diff", "HEAD", "--", "cmd", "internal", "go.mod", "go.sum", "scripts/mattermost-acceptance.py"])
        self.receipt["dirty_diff_sha256"] = hashlib.sha256(source).hexdigest()
        self.receipt["harness_sha256"] = hashlib.sha256(Path(__file__).read_bytes()).hexdigest()
        self.receipt["test_sha256"] = hashlib.sha256((ROOT / "internal/channelservice/mattermost_acceptance_test.go").read_bytes()).hexdigest()
        engine = json.loads(self.run(["docker", "version", "--format", "{{json .Server}}"])); self.receipt["docker"] = {"version": engine["Version"], "os": engine["Os"], "arch": engine["Arch"]}
        for image in (MM_IMAGE, PG_IMAGE):
            self.run(["docker", "image", "inspect", image])  # Never implicitly pulls/upgrades.
        # Docker's internal-only bridges do not publish localhost ports on the
        # qualified engine. A dedicated bridge still isolates resource identity;
        # this harness makes no claim of server network-egress isolation.
        self.remember("network", self.run(["docker", "network", "create", "--driver", "bridge", "--label", LABEL + "=" + self.nonce, self.names["network"]]).decode())
        password = secrets.token_urlsafe(30)
        pg_env = self.state / "postgres.env"; self.secrets.append(pg_env)
        private(pg_env, "POSTGRES_USER=keelmux\nPOSTGRES_DB=keelmux\nPOSTGRES_PASSWORD=" + password + "\n")
        self.remember("postgres", self.run(["docker", "create", "--pull", "never", "--name", self.names["postgres"], "--label", LABEL + "=" + self.nonce,
                                               "--network", self.ids["network"], "--network-alias", "postgres", "--env-file", str(pg_env), PG_IMAGE]).decode())
        self.run(["docker", "start", self.ids["postgres"]])
        ready = False
        for _ in range(30):
            proc = subprocess.run(["docker", "exec", self.ids["postgres"], "pg_isready", "-U", "keelmux", "-d", "keelmux"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=10)
            if proc.returncode == 0:
                ready = True; break
            time.sleep(1)
        if not ready:
            raise Failure("postgres_not_ready")
        ca, cert, key = self.tls()
        mm_env = self.state / "mattermost.env"; self.secrets.append(mm_env)
        private(mm_env, "\n".join(["MM_SQLSETTINGS_DRIVERNAME=postgres", "MM_SQLSETTINGS_DATASOURCE=postgres://keelmux:" + password + "@postgres:5432/keelmux?sslmode=disable&connect_timeout=10",
             "MM_SERVICESETTINGS_LISTENADDRESS=:8065", "MM_SERVICESETTINGS_CONNECTIONSECURITY=TLS", "MM_SERVICESETTINGS_TLSCERTFILE=/tmp/keelmux-server.pem", "MM_SERVICESETTINGS_TLSKEYFILE=/tmp/keelmux-server-key.pem",
             "MM_SERVICESETTINGS_SITEURL=https://localhost", "MM_SERVICESETTINGS_ENABLEUSERACCESSTOKENS=true", "MM_SERVICESETTINGS_ENABLEBOTACCOUNTCREATION=true", "MM_SERVICESETTINGS_ENABLEDEVELOPER=false", "MM_SERVICESETTINGS_ENABLESECURITYFIXALERT=false",
             "MM_EMAILSETTINGS_REQUIREEMAILVERIFICATION=false", "MM_EMAILSETTINGS_SENDEMAILNOTIFICATIONS=false", "MM_TEAMSETTINGS_ENABLEOPENSERVER=true", "MM_PLUGINSETTINGS_ENABLE=false", "MM_LOGSETTINGS_ENABLEFILE=false", "MM_LOGSETTINGS_CONSOLELEVEL=ERROR",
             "MM_DIAGNOSTICSETTINGS_ENABLE=false", "MM_LOGSETTINGS_ENABLEDIAGNOSTICS=false", "MM_RATELIMITSETTINGS_ENABLERATELIMITER=false", ""]))
        self.remember("server", self.run(["docker", "create", "--pull", "never", "--platform", "linux/amd64", "--name", self.names["server"], "--label", LABEL + "=" + self.nonce,
                                             "--network", self.ids["network"], "-p", "127.0.0.1::8065", "--env-file", str(mm_env), MM_IMAGE]).decode())
        passwd_tar = self.run(["docker", "cp", self.ids["server"] + ":/etc/passwd", "-"])
        with tarfile.open(fileobj=io.BytesIO(passwd_tar)) as archive:
            passwd = archive.extractfile(archive.getmembers()[0]).read().decode()
        identities = [line.split(":") for line in passwd.splitlines() if line.startswith("mattermost:")]
        if len(identities) != 1:
            raise Failure("server_user_unproved")
        uid, gid = int(identities[0][2]), int(identities[0][3])
        payload = io.BytesIO()
        with tarfile.open(fileobj=payload, mode="w") as archive:
            for src, name in ((cert, "keelmux-server.pem"), (key, "keelmux-server-key.pem")):
                data = src.read_bytes(); info = tarfile.TarInfo(name); info.size = len(data); info.uid = uid; info.gid = gid; info.mode = 0o600
                archive.addfile(info, io.BytesIO(data))
        self.run(["docker", "cp", "-a", "-", self.ids["server"] + ":/tmp"], data=payload.getvalue())
        self.run(["docker", "start", self.ids["server"]])
        ports = None
        for _ in range(40):
            obj = self.metadata("container", self.ids["server"])
            if not obj["State"]["Running"]:
                private(self.state / "server-start-private.json", json.dumps(obj["State"]))
                raise Failure("mattermost_start_failed")
            ports = obj["NetworkSettings"]["Ports"].get("8065/tcp")
            if ports:
                break
            time.sleep(0.25)
        if not ports or len(ports) != 1 or ports[0]["HostIp"] != "127.0.0.1":
            raise Failure("server_port_scope_unproved")
        self.endpoint = "https://127.0.0.1:" + ports[0]["HostPort"]
        context = ssl.create_default_context(cafile=str(ca))
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPSHandler(context=context))
        ready = False
        for _ in range(120):
            try:
                ping, headers = self.request("GET", "/system/ping")
                if ping.get("status") == "OK":
                    ready = True; self.receipt["mattermost_version"] = ".".join(headers.get("X-Version", "unknown").split(".")[0:3]); break
            except Failure:
                pass
            time.sleep(1)
        if not ready:
            raise Failure("mattermost_not_ready")
        if self.receipt["mattermost_version"] != "11.11.1":
            raise Failure("mattermost_version_mismatch")
        self.receipt["runtime"] = {"image_os": "linux", "image_arch": "amd64", "engine_arch": engine["Arch"], "emulated": engine["Arch"] != "amd64", "server_uid": uid, "tls_verified": True, "network_internal": False, "egress_isolation_proven": False}
        self.ca = ca

    def fixtures(self):
        def user(name):
            pw = secrets.token_urlsafe(24) + "Aa1!"
            u, _ = self.request("POST", "/users", body={"email": name + "@keelmux.invalid", "username": name, "password": pw})
            _, headers = self.request("POST", "/users/login", body={"login_id": name, "password": pw})
            token = headers.get("Token")
            if not token:
                raise Failure("fixture_login_missing_token")
            return u["id"], token
        admin_id, admin = user("owned_admin")
        human_id, human = user("owned_human")
        outsider_id, outsider = user("owned_outsider")
        bot, _ = self.request("POST", "/bots", admin, {"username": "owned_bot", "display_name": "Owned Keelmux Fixture"})
        bot_id = bot["user_id"]
        access, _ = self.request("POST", "/users/" + bot_id + "/tokens", admin, {"description": "Owned isolated fixture"})
        team, _ = self.request("POST", "/teams", admin, {"name": "owned_fixture", "display_name": "Owned Fixture", "type": "O"})
        team_id = team["id"]
        for uid in (human_id, outsider_id, bot_id):
            self.request("POST", "/teams/" + team_id + "/members", admin, {"team_id": team_id, "user_id": uid})
        channel, _ = self.request("POST", "/channels", admin, {"team_id": team_id, "name": "owned_channel", "display_name": "Owned Channel", "type": "O"})
        channel_id = channel["id"]
        for uid in (human_id, outsider_id, bot_id):
            self.request("POST", "/channels/" + channel_id + "/members", admin, {"user_id": uid})
        root, _ = self.request("POST", "/posts", admin, {"channel_id": channel_id, "message": "Owned fixture selected root"})
        other, _ = self.request("POST", "/posts", admin, {"channel_id": channel_id, "message": "Owned fixture different root"})
        token_paths = {}
        for name, token in (("admin", admin), ("human", human), ("outsider", outsider), ("bot", access["token"])):
            path = self.state / (name + "-token"); private(path, token); self.secrets.append(path); token_paths[name] = str(path)
        fixture = {"schema": 1, "endpoint": self.endpoint, "ca": str(self.ca), "state": str(self.state), "team": team_id, "channel": channel_id, "root": root["id"], "other_root": other["id"],
                   "bot": bot_id, "human": human_id, "outsider": outsider_id, "token_paths": token_paths}
        self.fixture = self.state / "fixture.json"; private(self.fixture, json.dumps(fixture)); self.secrets.append(self.fixture)

    def acceptance(self):
        env = dict(os.environ, KEELMUX_MM_ACCEPTANCE_FIXTURE=str(self.fixture))
        with open(self.state / "go-test.json", "wb") as out:
            os.chmod(out.name, 0o600)
            proc = subprocess.Popen(["go", "test", "-race", "./internal/channelservice", "-run", "^TestMattermostOwnedAcceptance$", "-count=1", "-timeout", "180s", "-json"], cwd=ROOT, env=env, stdout=out, stderr=subprocess.STDOUT, start_new_session=True)
            try:
                proc.wait(timeout=210)
            except subprocess.TimeoutExpired as exc:
                os.killpg(proc.pid, signal.SIGTERM)
                try:
                    proc.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    os.killpg(proc.pid, signal.SIGKILL); proc.wait(timeout=5)
                raise Failure("acceptance_timeout") from exc
        path = self.state / "result.json"
        if path.exists():
            self.receipt["acceptance"] = json.loads(path.read_text())
        if proc.returncode or not self.receipt.get("acceptance", {}).get("passed"):
            raise Failure("acceptance_failed")

    def cleanup(self):
        # Only exact recorded handles whose immutable label/name still match.
        failures = []
        for kind in ("server", "postgres"):
            ident = self.ids.get(kind)
            if not ident:
                continue
            try:
                obj = self.metadata("container", ident)
                if obj["Id"] != ident or obj["Name"].lstrip("/") != self.names[kind] or obj["Config"]["Labels"].get(LABEL) != self.nonce:
                    raise Failure("cleanup_ownership_mismatch")
                volumes = [mount["Name"] for mount in obj.get("Mounts", []) if mount["Type"] == "volume"]
                logs = subprocess.run(["docker", "logs", ident], stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=10)
                private(self.state / (kind + "-private.log"), logs.stdout)
                self.run(["docker", "rm", "-f", "-v", ident])
                proc = subprocess.run(["docker", "container", "inspect", ident], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=10)
                self.receipt["cleanup"][kind + "_absent"] = proc.returncode != 0
                if proc.returncode == 0:
                    raise Failure("cleanup_resource_survived")
                self.receipt["cleanup"][kind + "_anonymous_volumes_absent"] = all(subprocess.run(["docker", "volume", "inspect", volume], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=10).returncode != 0 for volume in volumes)
                if not self.receipt["cleanup"][kind + "_anonymous_volumes_absent"]:
                    raise Failure("cleanup_volume_survived")
            except Exception:
                failures.append(kind)
        ident = self.ids.get("network")
        if ident:
            try:
                obj = self.metadata("network", ident)
                if obj["Id"] != ident or obj["Name"] != self.names["network"] or obj["Labels"].get(LABEL) != self.nonce:
                    raise Failure("cleanup_ownership_mismatch")
                self.run(["docker", "network", "rm", ident])
                proc = subprocess.run(["docker", "network", "inspect", ident], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=10)
                self.receipt["cleanup"]["network_absent"] = proc.returncode != 0
                if proc.returncode == 0:
                    raise Failure("cleanup_resource_survived")
            except Exception:
                failures.append("network")
        for path in self.secrets:
            path.unlink(missing_ok=True)
        self.receipt["cleanup"]["host_secrets_removed"] = all(not p.exists() for p in self.secrets)
        self.receipt["cleanup"]["failures"] = failures
        if failures:
            self.receipt["passed"] = False; self.receipt["failure_kind"] = "cleanup_incomplete"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--receipt", type=Path, default=ROOT / "work/mattermost-acceptance.json")
    parser.add_argument("--allow-dirty", action="store_true", help="qualify a development run; final proof requires clean source")
    args = parser.parse_args()
    (ROOT / "work").mkdir(exist_ok=True)
    os.umask(0o077)
    h = Harness(args.allow_dirty)
    def interrupted(_signum, _frame):
        raise Failure("interrupted")
    signal.signal(signal.SIGTERM, interrupted); signal.signal(signal.SIGINT, interrupted)
    try:
        h.start(); h.fixtures(); h.acceptance(); h.receipt["passed"] = True
    except Failure as exc:
        h.receipt["failure_kind"] = str(exc)
    except Exception as exc:
        private(h.state / "unexpected-private.txt", repr(exc)); h.receipt["failure_kind"] = "unexpected_harness_failure"
    finally:
        h.cleanup()
        private(args.receipt, json.dumps(h.receipt, indent=2) + "\n")
    print(json.dumps({"passed": h.receipt["passed"], "failure_kind": h.receipt["failure_kind"], "receipt": str(args.receipt), "private_diagnostics": str(h.state)}))
    return 0 if h.receipt["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
