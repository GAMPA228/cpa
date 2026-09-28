"""Exercise the real C ABI using fake host callbacks. No network access."""
import base64
import ctypes as c
import json
import pathlib
import sys
import tempfile
import time


class Buffer(c.Structure):
    _fields_ = [("ptr", c.c_void_p), ("len", c.c_size_t)]


Call = c.CFUNCTYPE(c.c_int, c.c_char_p, c.c_void_p, c.c_size_t, c.POINTER(Buffer))
Free = c.CFUNCTYPE(None, c.c_void_p, c.c_size_t)
Shutdown = c.CFUNCTYPE(None)
HostCall = c.CFUNCTYPE(c.c_int, c.c_void_p, c.c_char_p, c.c_void_p, c.c_size_t, c.POINTER(Buffer))


class Host(c.Structure):
    _fields_ = [("version", c.c_uint32), ("ctx", c.c_void_p), ("call", HostCall), ("free", Free)]


class Plugin(c.Structure):
    _fields_ = [("version", c.c_uint32), ("call", Call), ("free", Free), ("shutdown", Shutdown)]


buffers = {}
methods = []


@HostCall
def callback(ctx, method, payload, size, out):
    method = method.decode()
    methods.append(method)
    if method == "host.auth.list":
        result = {"files": [{"id": "account-a", "auth_index": "index-a", "name": "test.json", "provider": "codex"}]}
    elif method == "host.auth.get":
        result = {"json": {"type": "codex", "proxy_url": "", "access_token": "never-leak"}}
    elif method == "host.http.do":
        request = json.loads(c.string_at(payload, size))
        assert request["url"] == "https://ipwho.is/"
        assert not request.get("headers") and not request.get("body")
        body = json.dumps({"success": True, "ip": "203.0.113.7", "timezone": {"id": "Asia/Tokyo"}}).encode()
        result = {"StatusCode": 200, "Body": base64.b64encode(body).decode()}
    else:
        return 1
    data = json.dumps({"ok": True, "result": result}).encode()
    buf = c.create_string_buffer(data)
    address = c.addressof(buf)
    buffers[address] = buf
    out.contents.ptr, out.contents.len = address, len(data)
    return 0


@Free
def free(ptr, size):
    buffers.pop(ptr, None)


library = c.CDLL(str(pathlib.Path(sys.argv[1]).resolve()))
host = Host(1, None, callback, free)
plugin = Plugin()
library.cliproxy_plugin_init.argtypes = [c.POINTER(Host), c.POINTER(Plugin)]
assert library.cliproxy_plugin_init(c.byref(host), c.byref(plugin)) == 0


def call(method, request):
    data = json.dumps(request).encode()
    out = Buffer()
    code = plugin.call(method.encode(), data, len(data), c.byref(out))
    raw = c.string_at(out.ptr, out.len)
    plugin.free(out.ptr, out.len)
    value = json.loads(raw)
    assert code == 0 and value["ok"], value
    return value["result"]


def manage(method, path, body=None):
    request = {"Method": method, "Path": path}
    if body is not None:
        request["Body"] = base64.b64encode(json.dumps(body).encode()).decode()
    result = call("management.handle", request)
    assert result["StatusCode"] in (200, 202), result
    return json.loads(base64.b64decode(result["Body"]))


with tempfile.TemporaryDirectory() as directory:
    config = f'data_file: "{directory}/settings.json"\n'.encode()
    registration = call("plugin.register", {"config_yaml": base64.b64encode(config).decode(), "schema_version": 7})
    assert registration["capabilities"]["request_interceptor"]
    menu = call("management.register", {})
    assert len(menu["resources"]) == 1 and len(menu["routes"]) == 3
    path = "/v0/management/plugins/codex-timezone/settings"
    try:
        manage("PUT", path, {"enabled": True, "default": {"mode": "auto", "timezone": ""}, "accounts": {}})
        deadline = time.monotonic() + 10
        while True:
            state = manage("GET", path)
            if state["accounts"] and state["accounts"][0]["observation"].get("timezone"):
                break
            assert time.monotonic() < deadline, state
            time.sleep(0.01)
        assert "never-leak" not in json.dumps(state)
        body = {"model": "gpt-6", "input": [{"role": "user", "content": "<environment_context><current_date>2001-01-01</current_date><timezone>Asia/Shanghai</timezone></environment_context>"}]}
        req = {"ToFormat": "codex", "Body": base64.b64encode(json.dumps(body).encode()).decode(), "Metadata": {"selected_auth_id": "account-a"}}
        response = call("request.intercept_after", req)
        output = base64.b64decode(response["Body"]).decode()
        assert "Asia/Tokyo" in output and "2001-01-01" in output
        assert methods.count("host.http.do") == 1, methods
        manage("PUT", path, {"enabled": False, "default": {"mode": "auto", "timezone": ""}, "accounts": {}})
        assert not call("request.intercept_after", req).get("Body")
        assert pathlib.Path(directory, "settings.json").exists()
    finally:
        call("plugin.quiesce", {})
        plugin.shutdown()
print("PASS: native ABI registration, menu, persistence, account callback, cached egress, rewrite, disable, quiesce")
