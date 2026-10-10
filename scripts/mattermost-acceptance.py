#!/usr/bin/env python3
"""Owned, opt-in Mattermost/SQLite/adapter acceptance. Never uses an existing server."""
import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import re
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
RUNTIME_IMAGE = "golang:1.26.6-bookworm@sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36"
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
        self.names = {"network": "keelmux-mm-" + self.nonce, "postgres": "keelmux-mm-pg-" + self.nonce, "server": "keelmux-mm-server-" + self.nonce, "runtime": "keelmux-mm-runtime-" + self.nonce}
        self.ids = {}
        self.allow_dirty = allow_dirty
        self.secrets = []
        self.receipt = {"schema": 1, "scope": "owned real Mattermost service/adapter acceptance", "passed": False, "failure_kind": None,
                        "mattermost_image": MM_IMAGE, "postgres_image": PG_IMAGE,
                        "excluded": ["packaged release/TUI/customer deployment", "Slack/IRC live servers", "model dispatch", "native terminal input", "exactly-once posts", "database downgrade"], "cleanup": {}}

    def run(self, argv, *, data=None, timeout=30, env=None):
        try:
            result = subprocess.run(argv, cwd=ROOT, input=data, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout, env=env)
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
        self.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-sha256", "-nodes", "-days", "1", "-subj", "/CN=Keelmux Owned Acceptance CA", "-addext", "basicConstraints=critical,CA:TRUE", "-addext", "keyUsage=critical,keyCertSign,cRLSign", "-addext", "subjectKeyIdentifier=hash", "-keyout", str(ca_key), "-out", str(ca)])
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
                return json.loads(raw), {k.lower(): v for k, v in response.headers.items()}
        except Exception as exc:
            # Original diagnostic stays private and is never interpolated into a receipt.
            private(self.state / "http-error.txt", repr(exc))
            private(self.state / "http-error-request.json", json.dumps({"method": method, "path": path}))
            if isinstance(exc, urllib.error.HTTPError):
                private(self.state / "http-error-body.txt", exc.read(1 << 20))
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
        self.receipt["go"] = self.run(["go", "version"]).decode().strip()
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
                    ready = True; break
            except Failure as exc:
                cause = exc.__cause__
                if isinstance(cause, urllib.error.URLError) and isinstance(cause.reason, ssl.SSLCertVerificationError):
                    raise Failure("fixture_tls_validation_failed") from exc
            time.sleep(1)
        if not ready:
            raise Failure("mattermost_not_ready")
        # The observed ping response does not disclose a version header.
        # Query the exact live owned container's installed server binary instead.
        version_output = self.run(["docker", "exec", self.ids["server"], "/mattermost/bin/mattermost", "version"]).decode()
        version = re.search(r"(?m)^Version:\s*(\d+\.\d+\.\d+)\s*$", version_output)
        self.receipt["mattermost_version"] = version.group(1) if version else "unverified"
        if self.receipt["mattermost_version"] != "11.11.1":
            raise Failure("mattermost_version_mismatch")
        self.receipt["runtime"] = {"image_os": "linux", "image_arch": "amd64", "engine_arch": engine["Arch"], "emulated": engine["Arch"] != "amd64", "server_uid": uid, "tls_verified": True, "network_internal": False, "egress_isolation_proven": False}
        self.ca = ca

    def fixtures(self):
        def user(name):
            pw = secrets.token_urlsafe(24) + "Aa1!"
            u, _ = self.request("POST", "/users", body={"email": name + "@keelmux.invalid", "username": name, "password": pw})
            _, headers = self.request("POST", "/users/login", body={"login_id": name, "password": pw})
            token = headers.get("token")
            if not token:
                raise Failure("fixture_login_missing_token")
            return u["id"], token
        admin_id, admin = user("owned_admin")
        human_id, human = user("owned_human")
        outsider_id, outsider = user("owned_outsider")
        bot, _ = self.request("POST", "/bots", admin, {"username": "owned_bot", "display_name": "Owned Keelmux Fixture"})
        bot_id = bot["user_id"]
        access, _ = self.request("POST", "/users/" + bot_id + "/tokens", admin, {"description": "Owned isolated fixture"})
        team, _ = self.request("POST", "/teams", admin, {"name": "ownedfixture", "display_name": "Owned Fixture", "type": "O"})
        team_id = team["id"]
        for uid in (human_id, outsider_id, bot_id):
            self.request("POST", "/teams/" + team_id + "/members", admin, {"team_id": team_id, "user_id": uid})
        channel, _ = self.request("POST", "/channels", admin, {"team_id": team_id, "name": "ownedchannel", "display_name": "Owned Channel", "type": "O"})
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
            except BaseException:
                if proc.poll() is None:
                    os.killpg(proc.pid, signal.SIGTERM)
                    try:
                        proc.wait(timeout=5)
                    except subprocess.TimeoutExpired:
                        os.killpg(proc.pid, signal.SIGKILL); proc.wait(timeout=5)
                raise
        path = self.state / "result.json"
        if path.exists():
            self.receipt["acceptance"] = json.loads(path.read_text())
        if proc.returncode or not self.receipt.get("acceptance", {}).get("passed"):
            raise Failure("acceptance_failed")

    def daemon_phase(self):
        # Linux Go honors SSL_CERT_FILE. Darwin uses system roots, so the
        # production daemon runs in this separately owned Linux child instead
        # of introducing an insecure product override or changing system trust.
        image = self.metadata("image", RUNTIME_IMAGE)
        if image.get("Os") != "linux" or image.get("Architecture") != "arm64":
            raise Failure("source_runtime_image_mismatch")
        self.remember("runtime", self.run(["docker", "create", "--pull", "never", "--name", self.names["runtime"], "--label", LABEL + "=" + self.nonce,
                                             "--network", "container:" + self.ids["server"], RUNTIME_IMAGE, "sleep", "1200"]).decode())
        self.run(["docker", "start", self.ids["runtime"]])
        runtime = self.metadata("container", self.ids["runtime"])
        if runtime.get("Mounts") or runtime["HostConfig"]["NetworkMode"] != "container:" + self.ids["server"]:
            raise Failure("source_runtime_isolation_mismatch")
        # Only this disposable fixture container receives a dependency. It has
        # no user mounts, native credentials, or generated bot credentials yet.
        self.run(["docker", "exec", self.ids["runtime"], "apt-get", "update", "-qq"], timeout=120)
        self.run(["docker", "exec", "-e", "DEBIAN_FRONTEND=noninteractive", self.ids["runtime"], "apt-get", "install", "-y", "-qq", "--no-install-recommends", "tmux"], timeout=120)
        tmux_version = self.run(["docker", "exec", self.ids["runtime"], "tmux", "-V"]).decode().strip()
        if not re.fullmatch(r"tmux \d+\.\d+[a-z]?", tmux_version):
            raise Failure("source_runtime_tmux_version_unknown")
        outputs = self.state / "source-binaries"; outputs.mkdir(mode=0o700)
        buildenv = dict(os.environ, CGO_ENABLED="0", GOOS="linux", GOARCH="arm64")
        stamp = self.run(["git", "show", "-s", "--format=%cI", "HEAD"]).decode().strip()
        flags = "-X github.com/seanheiney/keelmux/internal/buildinfo.Version=development -X github.com/seanheiney/keelmux/internal/buildinfo.Commit=" + self.receipt["source"] + " -X github.com/seanheiney/keelmux/internal/buildinfo.BuiltAt=" + stamp
        for name, package in (("keelmux", "./cmd/bp"), ("keelmuxd", "./cmd/bpd")):
            self.run(["go", "build", "-trimpath", "-ldflags", flags, "-o", str(outputs / name), package], timeout=120, env=buildenv)
        self.run(["go", "test", "-c", "-trimpath", "-o", str(outputs / "acceptance.test"), "./internal/channelservice"], timeout=120, env=buildenv)
        original = json.loads(self.fixture.read_text())
        original.update(endpoint="https://127.0.0.1:8065", state="/tmp/keelmux-fixture", ca="/tmp/keelmux-fixture/ca.pem",
                        root=original["other_root"], other_root=original["root"], token_paths={key: "/tmp/keelmux-fixture/" + key + "-token" for key in original["token_paths"]})
        payload = io.BytesIO()
        with tarfile.open(fileobj=payload, mode="w") as archive:
            for name in ("keelmux-bin", "keelmux-fixture"):
                info = tarfile.TarInfo(name); info.type = tarfile.DIRTYPE; info.mode = 0o700; archive.addfile(info)
            files = [("keelmux-bin/" + name, path.read_bytes(), 0o700) for name, path in ((n, outputs / n) for n in ("keelmux", "keelmuxd", "acceptance.test"))]
            files += [("keelmux-fixture/ca.pem", self.ca.read_bytes(), 0o600), ("keelmux-fixture/fixture.json", json.dumps(original).encode(), 0o600)]
            files += [("keelmux-fixture/" + key + "-token", Path(path).read_bytes(), 0o600) for key, path in json.loads(self.fixture.read_text())["token_paths"].items()]
            for name, data, mode in files:
                info = tarfile.TarInfo(name); info.size = len(data); info.mode = mode; archive.addfile(info, io.BytesIO(data))
        self.run(["docker", "cp", "-a", "-", self.ids["runtime"] + ":/tmp"], data=payload.getvalue())
        self.receipt["source_daemon_runtime"] = {"image": RUNTIME_IMAGE, "os": "linux", "arch": "arm64", "uid": 0, "tmux": tmux_version,
            "network_namespace": "exact owned Mattermost container", "endpoint": "https://127.0.0.1:8065", "ssl_cert_file_child_only": True,
            "no_host_mounts": True, "binary_sha256": {name: hashlib.sha256((outputs / name).read_bytes()).hexdigest() for name in ("keelmux", "keelmuxd", "acceptance.test")},
            "functional_test_not_race_instrumented": True}
        command = ["docker", "exec", "-e", "KEELMUX_MM_ACCEPTANCE_FIXTURE=/tmp/keelmux-fixture/fixture.json", "-e", "KEELMUX_MM_ACCEPTANCE_BIN_DIR=/tmp/keelmux-bin", "-e", "KEELMUX_MM_ACCEPTANCE_SOURCE=" + self.receipt["source"], self.ids["runtime"],
                   "/tmp/keelmux-bin/acceptance.test", "-test.run", "^(TestMattermostOwnedDaemonAcceptance|TestMMAcceptancePrivateFixtureFiles)$", "-test.timeout", "120s", "-test.v"]
        try:
            private(self.state / "daemon-test-private.log", self.run(command, timeout=150))
        finally:
            # Never extract a provider-controlled tree on the host. Preserve the
            # whole owned runtime diagnostics as a private opaque tar instead.
            raw = self.run(["docker", "cp", self.ids["runtime"] + ":/tmp/keelmux-fixture", "-"])
            private(self.state / "daemon-private.tar", raw)
            with tarfile.open(fileobj=io.BytesIO(raw)) as archive:
                result = [entry for entry in archive.getmembers() if entry.name == "keelmux-fixture/daemon-result.json" and entry.isfile() and entry.size <= 32768]
                if len(result) == 1:
                    self.receipt["source_daemon_acceptance"] = json.loads(archive.extractfile(result[0]).read())
        result = self.receipt.get("source_daemon_acceptance", {})
        if not result.get("passed") or result.get("stable_source_commit") != self.receipt["source"]:
            raise Failure("source_daemon_acceptance_failed")
        if self.run(["git", "rev-parse", "HEAD"]).decode().strip() != self.receipt["source"] or hashlib.sha256(self.run(["git", "diff", "HEAD", "--", "cmd", "internal", "go.mod", "go.sum", "scripts/mattermost-acceptance.py"])).hexdigest() != self.receipt["dirty_diff_sha256"]:
            raise Failure("source_changed_during_acceptance")

    def cleanup(self):
        # Only exact recorded handles whose immutable label/name still match.
        failures = []
        for kind in ("runtime", "server", "postgres"):
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
        self.receipt["cleanup"]["host_secret_files_removed"] = all(not p.exists() for p in self.secrets)
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
        h.start(); h.fixtures(); h.acceptance(); h.daemon_phase(); h.receipt["passed"] = True
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
