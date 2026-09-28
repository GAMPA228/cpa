"""Load the plugin in a real CPA binary using an isolated temporary configuration."""
import json
import os
import pathlib
import signal
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request


with tempfile.TemporaryDirectory() as directory:
    root = pathlib.Path(directory)
    (root / "auths").mkdir()
    (root / "auths" / "smoke.json").write_text(json.dumps({"type": "codex", "email": "smoke@example.test", "account_id": "mock-account", "access_token": "mock-upstream-token", "proxy_url": "direct"}))
    config = root / "config.yaml"
    config.write_text(f'''host: "127.0.0.1"
port: 18317
proxy-url: "http://127.0.0.1:18319"
auth-dir: "{root}/auths"
api-keys: ["timezone-local-test-api-key"]
remote-management:
  allow-remote: false
  secret-key: "timezone-local-test-management-key"
  disable-auto-update-panel: true
plugins:
  enabled: true
  dir: "{pathlib.Path(sys.argv[2]).resolve()}"
  configs:
    codex-timezone:
      enabled: true
      data_file: "{root}/timezone.json"
usage-statistics-enabled: false
''')
    cert, key = root / "cert.pem", root / "key.pem"
    subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", str(key), "-out", str(cert), "-days", "1", "-subj", "/CN=chatgpt.com", "-addext", "subjectAltName=DNS:chatgpt.com,DNS:ipwho.is"], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    executable = root / "fake-upstream"
    subprocess.run(["go", "build", "-buildvcs=false", "-o", str(executable), str(pathlib.Path(__file__).with_name("fake-upstream.go"))], check=True)
    received = root / "received.json"
    upstream = subprocess.Popen([str(executable), str(cert), str(key), str(received)])
    with (root / "server.log").open("wb") as log:
        process = subprocess.Popen([sys.argv[1], "--config", str(config), "--local-model"], cwd=root, stdout=log, stderr=log, env={**os.environ, "SSL_CERT_FILE": str(cert)})
        try:
            def request(path, method="GET", data=None, auth=True):
                headers = {"Content-Type": "application/json"}
                if auth:
                    headers["Authorization"] = "Bearer timezone-local-test-management-key"
                req = urllib.request.Request("http://127.0.0.1:18317" + path, headers=headers, method=method, data=json.dumps(data).encode() if data is not None else None)
                with urllib.request.urlopen(req, timeout=5) as response:
                    return response.status, response.read()

            deadline = time.monotonic() + 30
            while True:
                try:
                    _, raw = request("/v0/management/plugins")
                    break
                except (urllib.error.URLError, ConnectionError):
                    if process.poll() is not None or time.monotonic() > deadline:
                        raise RuntimeError((root / "server.log").read_text())
                    time.sleep(0.1)
            plugins = json.loads(raw)
            assert "codex-timezone" in raw.decode(), plugins
            print("Host plugin registration:", raw.decode())
            assert any(p.get("registered") for p in plugins["plugins"]), (root / "server.log").read_text()
            status, html = request("/v0/resource/plugins/codex-timezone/panel", auth=False)
            assert status == 200 and b"<!doctype html>" in html.lower()
            path = "/v0/management/plugins/codex-timezone/settings"
            try:
                request(path, auth=False)
                raise AssertionError("settings endpoint is public")
            except urllib.error.HTTPError as error:
                assert error.code == 401
            status, raw = request(path)
            assert status == 200 and not json.loads(raw)["settings"]["enabled"]
            status, raw = request(path, "PUT", {"enabled": True, "default": {"mode": "manual", "timezone": "Asia/Tokyo"}, "accounts": {}})
            assert status == 200 and json.loads(raw)["settings"]["default"]["timezone"] == "Asia/Tokyo"
            assert (root / "timezone.json").exists()
            request("/v0/management/plugins/codex-timezone/refresh", "POST", {})
            deadline = time.monotonic() + 10
            while not json.loads(request(path)[1])["accounts"]:
                assert time.monotonic() < deadline, "account callback did not discover test key"
                time.sleep(0.05)
            models_req = urllib.request.Request("http://127.0.0.1:18317/v1/models", headers={"Authorization": "Bearer timezone-local-test-api-key"})
            with urllib.request.urlopen(models_req, timeout=5) as response:
                models = json.loads(response.read())["data"]
            model = next(m["id"] for m in models if m["id"].startswith("gpt-6"))
            body = {"model": model, "stream": False, "input": [{"role": "user", "content": "<environment_context><current_date>2001-01-01</current_date><timezone>Asia/Shanghai</timezone></environment_context>"}]}
            req = urllib.request.Request("http://127.0.0.1:18317/v1/responses", method="POST", data=json.dumps(body).encode(), headers={"Authorization": "Bearer timezone-local-test-api-key", "Content-Type": "application/json"})
            try:
                with urllib.request.urlopen(req, timeout=10) as response:
                    assert response.status == 200
                    response.read()
            except urllib.error.HTTPError as error:
                raise RuntimeError(error.read().decode()) from error
            actual = received.read_text()
            assert "Asia/Tokyo" in actual and "Asia/Shanghai" not in actual and "2001-01-01" in actual, actual
            manage_auto = {"enabled": True, "default": {"mode": "auto", "timezone": ""}, "accounts": {}}
            request(path, "PUT", manage_auto)
            for proxy, expected in [("direct", "direct"), ("", "global"), ("http://127.0.0.1:18320", "account")]:
                auth_path = root / "auths" / "smoke.json"
                auth = json.loads(auth_path.read_text())
                auth["proxy_url"] = proxy
                auth_path.write_text(json.dumps(auth))
                request("/v0/management/plugins/codex-timezone/refresh", "POST", {})
                deadline = time.monotonic() + 10
                while True:
                    state = json.loads(request(path)[1])
                    rows = state["accounts"]
                    if rows and rows[0]["route"] == expected and rows[0]["observation"].get("timezone") == "America/New_York":
                        break
                    assert time.monotonic() < deadline, state
                    time.sleep(0.1)
                if expected != "direct":
                    port = "18319" if expected == "global" else "18320"
                    assert "ipwho.is:443" in pathlib.Path(str(received)+"."+port).read_text()
                with urllib.request.urlopen(req, timeout=10) as response:
                    assert response.status == 200
                    response.read()
                assert "America/New_York" in received.read_text(), received.read_text()
            print("PASS: real CPA v4.0.24, protected menu, persistence, manual rewrite, automatic direct/global/account proxy lookup, actual upstream body")
        finally:
            if sys.exc_info()[0] is not None:
                print("Host log:", (root / "server.log").read_text())
            process.send_signal(signal.SIGTERM)
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
                raise RuntimeError("CPA did not shut down cleanly")
            upstream.terminate()
            upstream.wait(timeout=5)
