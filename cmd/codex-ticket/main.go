package main

/*
#include <stdint.h>
#include <stdlib.h>
#include <pthread.h>

typedef struct { void* ptr; size_t len; } cliproxy_buffer;
typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);
typedef struct {
 uint32_t abi_version;
 void* host_ctx;
 cliproxy_host_call_fn call;
 cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;
typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);
typedef struct {
 uint32_t abi_version;
 cliproxy_plugin_call_fn call;
 cliproxy_plugin_free_fn free_buffer;
 cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;
extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

// Copy the callback table: never retain the caller's temporary table pointer.
static cliproxy_host_api stored_host;
static pthread_mutex_t host_mu = PTHREAD_MUTEX_INITIALIZER;
static void store_host(const cliproxy_host_api* host) {
 pthread_mutex_lock(&host_mu); stored_host = *host; pthread_mutex_unlock(&host_mu);
}
static int call_host(const char* method, const uint8_t* request, size_t n, cliproxy_buffer* response) {
 pthread_mutex_lock(&host_mu); cliproxy_host_api h = stored_host; pthread_mutex_unlock(&host_mu);
 if (!h.call || !h.free_buffer) return 1;
 return h.call(h.host_ctx, method, request, n, response);
}
static void free_host(void* ptr, size_t n) {
 pthread_mutex_lock(&host_mu); cliproxy_host_api h = stored_host; pthread_mutex_unlock(&host_mu);
 if (ptr && h.free_buffer) h.free_buffer(ptr, n);
}
*/
import "C"

import (
	"errors"
	"github.com/Su-cyber-art/cpa-plugin-codex-ticket/internal/ticket"
	"unsafe"
)

var engine = ticket.New(ticket.CallbackHost{Call: hostCall})

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, out *C.cliproxy_plugin_api) C.int {
	if host == nil || out == nil || host.abi_version != 1 || host.call == nil || host.free_buffer == nil {
		return 1
	}
	C.store_host(host)
	out.abi_version = 1
	out.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	out.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	out.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, req *C.uint8_t, n C.size_t, out *C.cliproxy_buffer) C.int {
	if out == nil {
		return 1
	}
	out.ptr = nil
	out.len = 0
	raw := []byte(`{"ok":true,"result":{}}`)
	// Bound the C->Go copy; oversized business hook input is a no-op.
	if method != nil && n <= 32<<20 && (req != nil || n == 0) {
		var b []byte
		if n > 0 {
			b = C.GoBytes(unsafe.Pointer(req), C.int(n))
		}
		raw = engine.Dispatch(C.GoString(method), b)
	}
	out.ptr = C.CBytes(raw)
	out.len = C.size_t(len(raw))
	if out.ptr == nil {
		return 1
	}
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, n C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() { engine.Shutdown() }
func hostCall(method string, raw []byte) ([]byte, error) {
	cm := C.CString(method)
	defer C.free(unsafe.Pointer(cm))
	payload := C.CBytes(raw)
	defer C.free(payload)
	var out C.cliproxy_buffer
	code := C.call_host(cm, (*C.uint8_t)(payload), C.size_t(len(raw)), &out)
	if out.ptr != nil {
		defer C.free_host(out.ptr, out.len)
	}
	if code != 0 || out.ptr == nil || out.len == 0 || out.len > 16<<20 {
		return nil, errors.New("host_callback_failed")
	}
	return C.GoBytes(out.ptr, C.int(out.len)), nil
}
