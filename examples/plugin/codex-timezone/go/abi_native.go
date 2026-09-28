//go:build cgo

package main

/*
#include <stdint.h>
#include <stdlib.h>
typedef struct { void* ptr; size_t len; } buffer;
typedef int (*host_call_fn)(void*, const char*, const uint8_t*, size_t, buffer*);
typedef void (*host_free_fn)(void*, size_t);
typedef struct { uint32_t abi_version; void* host_ctx; host_call_fn call; host_free_fn free_buffer; } host_api;
typedef int (*plugin_call_fn)(char*, uint8_t*, size_t, buffer*);
typedef void (*plugin_free_fn)(void*, size_t);
typedef void (*plugin_shutdown_fn)(void);
typedef struct { uint32_t abi_version; plugin_call_fn call; plugin_free_fn free_buffer; plugin_shutdown_fn shutdown; } plugin_api;
extern int cliproxyPluginCall(char*, uint8_t*, size_t, buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
static host_api saved_host;
static void save_host(host_api* host) { saved_host = *host; }
static int host_call(const char* method, const uint8_t* data, size_t len, buffer* out) {
  if (!saved_host.call) return 1;
  return saved_host.call(saved_host.host_ctx, method, data, len, out);
}
static void host_free(void* ptr, size_t len) { if (ptr && saved_host.free_buffer) saved_host.free_buffer(ptr, len); }
*/
import "C"

import (
	"encoding/json"
	"errors"
	"unsafe"
)

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.host_api, plugin *C.plugin_api) C.int {
	if host == nil || plugin == nil || host.abi_version != 1 {
		return 1
	}
	C.save_host(host)
	active.host = callHost
	plugin.abi_version = 1
	plugin.call = C.plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, data *C.uint8_t, length C.size_t, out *C.buffer) (code C.int) {
	if out == nil {
		return 1
	}
	out.ptr, out.len = nil, 0
	defer func() {
		if recover() != nil {
			code = 1
		}
	}()
	if method == nil || length > 32<<20 || (length > 0 && data == nil) {
		return 1
	}
	var raw []byte
	if length > 0 {
		raw = C.GoBytes(unsafe.Pointer(data), C.int(length))
	}
	result, err := active.handle(C.GoString(method), raw)
	if err != nil {
		result = []byte(`{"ok":false,"error":{"code":"timezone_plugin_error","message":"Timezone plugin operation failed"}}`)
		code = 1
	}
	out.ptr, out.len = C.CBytes(result), C.size_t(len(result))
	return code
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, _ C.size_t) { C.free(ptr) }

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() { active.close() }

func callHost(method string, request, result any) error {
	raw, err := json.Marshal(request)
	if err != nil {
		return err
	}
	name, data := C.CString(method), C.CBytes(raw)
	defer C.free(unsafe.Pointer(name))
	defer C.free(data)
	var out C.buffer
	code := C.host_call(name, (*C.uint8_t)(data), C.size_t(len(raw)), &out)
	defer C.host_free(out.ptr, out.len)
	if code != 0 || out.ptr == nil || out.len > 32<<20 {
		return errors.New("host callback failed")
	}
	var env struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(C.GoBytes(out.ptr, C.int(out.len)), &env) != nil || !env.OK {
		return errors.New("host callback failed")
	}
	return json.Unmarshal(env.Result, result)
}
