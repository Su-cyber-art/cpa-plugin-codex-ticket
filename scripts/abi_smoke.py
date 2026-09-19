#!/usr/bin/env python3
"""Exercise the plugin's exported C ABI with a secret-free fake host.
This is a mock-host ABI test, not evidence of real upstream ticket validity.
"""
import argparse
import ctypes as C
import json
import pathlib
import tempfile

class Buffer(C.Structure):
    _fields_ = [("ptr", C.c_void_p), ("len", C.c_size_t)]

HOSTCALL = C.CFUNCTYPE(C.c_int, C.c_void_p, C.c_char_p, C.c_void_p, C.c_size_t, C.POINTER(Buffer))
FREE = C.CFUNCTYPE(None, C.c_void_p, C.c_size_t)
CALL = C.CFUNCTYPE(C.c_int, C.c_char_p, C.c_void_p, C.c_size_t, C.POINTER(Buffer))
SHUTDOWN = C.CFUNCTYPE(None)

class HostAPI(C.Structure):
    _fields_ = [("abi_version", C.c_uint32), ("host_ctx", C.c_void_p), ("call", HOSTCALL), ("free_buffer", FREE)]

class PluginAPI(C.Structure):
    _fields_ = [("abi_version", C.c_uint32), ("call", CALL), ("free_buffer", FREE), ("shutdown", SHUTDOWN)]

parser = argparse.ArgumentParser()
parser.add_argument("library")
args = parser.parse_args()
allocations = {}
host_calls = []

@HOSTCALL
def hostcall(ctx, method, request, length, response):
    name = method.decode()
    host_calls.append(name)
    if name == "host.auth.list":
        value = {"ok": True, "result": {"files": []}}
    elif name == "host.log":
        value = {"ok": True, "result": {}}
    else:
        value = {"ok": False, "error": {"code": "mock_unavailable", "message": "No credentials in ABI test"}}
    raw = json.dumps(value).encode()
    buf = C.create_string_buffer(raw)
    ptr = C.addressof(buf)
    allocations[ptr] = buf
    response.contents.ptr = ptr
    response.contents.len = len(raw)
    return 0

@FREE
def hostfree(ptr, length):
    allocations.pop(ptr, None)

host = HostAPI(1, None, hostcall, hostfree)
plugin = PluginAPI()
lib = C.CDLL(str(pathlib.Path(args.library).resolve()))
lib.cliproxy_plugin_init.argtypes = [C.POINTER(HostAPI), C.POINTER(PluginAPI)]
lib.cliproxy_plugin_init.restype = C.c_int
assert lib.cliproxy_plugin_init(C.byref(host), C.byref(plugin)) == 0
assert plugin.abi_version == 1

def rpc(method, value):
    raw = json.dumps(value).encode()
    req = C.create_string_buffer(raw)
    response = Buffer()
    code = plugin.call(method.encode(), C.cast(req, C.c_void_p), len(raw), C.byref(response))
    try:
        data = json.loads(C.string_at(response.ptr, response.len))
    finally:
        if response.ptr:
            plugin.free_buffer(response.ptr, response.len)
    assert code == 0 and data.get("ok"), (method, code, data)
    return data.get("result")

with tempfile.TemporaryDirectory(prefix="ticket-abi-") as temp:
    config_file = pathlib.Path(temp) / "config.yaml"
    config_file.write_text("request-log: false\nplugins:\n  enabled: true\n  configs:\n    codex-ticket:\n      enabled: true\n      harvest_enabled: false\n      inject_enabled: true\n")
    config = f"enabled: true\nharvest_enabled: false\ninject_enabled: true\nhost_config_file: {config_file}\nmodels:\n  - gpt-6-astra\n"
    import base64
    registration = rpc("plugin.register", {"schema_version": 6, "config_yaml": base64.b64encode(config.encode()).decode()})
    assert registration["metadata"]["Name"] == "codex-ticket", registration
    assert registration["capabilities"]["request_interceptor"] is True
    routes = rpc("management.register", {})
    assert len(routes.get("resources", [])) == 1
    assert routes["resources"][0]["Path"] == "/settings", "Only the static settings shell may be public"
    request = {"RequestID": "abi-mock-request", "SourceFormat": "openai-response", "ToFormat": "codex", "Model": "gpt-6-astra", "Headers": {"X-Test": ["unchanged"]}, "Metadata": {}, "Body": "e30="}
    before = rpc("request.intercept_before", request)
    after = rpc("request.intercept_after", request)
    assert not before.get("Terminate") and not after.get("Terminate")
    assert not any(k.lower() == "x-codex-turn-state" for k in (after.get("Headers") or {}))
    result = {"abi": plugin.abi_version, "metadata": registration["metadata"], "registered_routes": [{k: r.get(k) for k in ("Method", "Path")} for r in routes.get("routes", [])], "fail_open_without_ticket": True, "public_static_shell_only": True}
    plugin.shutdown()
    result["shutdown_completed"] = True
    result["host_callbacks"] = sorted(set(host_calls))
    print(json.dumps(result, ensure_ascii=False))
