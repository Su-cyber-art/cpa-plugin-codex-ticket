package ticket

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// CallbackHost discards raw host errors, which may contain paths or credentials.
type CallbackHost struct {
	Call func(string, []byte) ([]byte, error)
}

func (h CallbackHost) call(ctx context.Context, method string, req, out any) error {
	if ctx.Err() != nil || h.Call == nil {
		return errors.New("host_unavailable")
	}
	b, _ := json.Marshal(req)
	raw, err := h.Call(method, b)
	if err != nil || len(raw) > 16<<20 {
		return errors.New("host_callback_failed")
	}
	var env pluginabi.Envelope
	if json.Unmarshal(raw, &env) != nil || !env.OK || json.Unmarshal(env.Result, out) != nil {
		return errors.New("host_callback_failed")
	}
	if ctx.Err() != nil {
		return errors.New("canceled")
	}
	return nil
}
func (h CallbackHost) List(ctx context.Context) ([]pluginapi.HostAuthFileEntry, error) {
	var r struct {
		Files []pluginapi.HostAuthFileEntry `json:"files"`
	}
	err := h.call(ctx, pluginabi.MethodHostAuthList, struct{}{}, &r)
	return r.Files, err
}
func (h CallbackHost) Get(ctx context.Context, index string) (pluginapi.HostAuthGetResponse, error) {
	var r pluginapi.HostAuthGetResponse
	err := h.call(ctx, pluginabi.MethodHostAuthGet, pluginapi.HostAuthGetRequest{AuthIndex: index}, &r)
	return r, err
}
func (h CallbackHost) Runtime(ctx context.Context, index string) (pluginapi.HostAuthFileEntry, error) {
	var r pluginapi.HostAuthGetRuntimeResponse
	err := h.call(ctx, pluginabi.MethodHostAuthGetRuntime, pluginapi.HostAuthGetRequest{AuthIndex: index}, &r)
	return r.Auth, err
}
func envelope(v any) []byte {
	b, _ := json.Marshal(v)
	out, _ := json.Marshal(pluginabi.Envelope{OK: true, Result: b})
	return out
}
func registration() any {
	fields := []pluginapi.ConfigField{}
	add := func(name string, t pluginapi.ConfigFieldType, d string) {
		fields = append(fields, pluginapi.ConfigField{Name: name, Type: t, Description: d})
	}
	for _, n := range []string{"enabled", "harvest_enabled", "inject_enabled", "replace_existing"} {
		add(n, pluginapi.ConfigFieldTypeBoolean, "Explicit opt-in; false by default.")
	}
	add("proxy_file", pluginapi.ConfigFieldTypeString, "Private proxy-selection file. Configure the proxy in the Codex 采票代理 page; do not put credentials in this field.")
	add("host_config_file", pluginapi.ConfigFieldTypeString, "Absolute read-only host YAML path for enable and request-log safety gates.")
	add("models", pluginapi.ConfigFieldTypeString, "Comma-separated final upstream models; default gpt-6-astra,gpt-5.6-sol.")
	for _, n := range []string{"target_length", "ttl_seconds", "refresh_before_seconds", "scan_interval_seconds", "timeout_seconds", "max_concurrency", "retry_base_seconds", "retry_max_seconds"} {
		add(n, pluginapi.ConfigFieldTypeInteger, "Bounded experimental harvester setting; see README defaults.")
	}
	return struct {
		SchemaVersion uint32             `json:"schema_version"`
		Metadata      pluginapi.Metadata `json:"metadata"`
		Capabilities  map[string]bool    `json:"capabilities"`
	}{pluginabi.SchemaVersion, pluginapi.Metadata{Name: Name, Version: "0.2.0", Author: "Yuesaki", GitHubRepository: "https://github.com/Su-cyber-art/cpa-plugin-codex-ticket", ConfigFields: fields}, map[string]bool{"request_interceptor": true, "request_lifecycle_plugin": true, "response_interceptor": true, "response_stream_interceptor": true, "management_api": true}}
}

// Dispatch always fails open on business hooks, including malformed input.
// Empty response modifications mean no header or body changes in CPA schema 6.
func (e *Engine) Dispatch(method string, raw []byte) (result []byte) {
	defer func() {
		if recover() != nil {
			result = envelope(struct{}{})
		}
	}()
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		var r struct {
			ConfigYAML    []byte `json:"config_yaml"`
			SchemaVersion uint32 `json:"schema_version"`
		}
		reason := "configured"
		c := DefaultConfig()
		if json.Unmarshal(raw, &r) != nil || r.SchemaVersion != pluginabi.SchemaVersion {
			reason = "schema_invalid"
		} else {
			var err error
			c, err = ParseConfig(r.ConfigYAML)
			if err != nil {
				reason = "config_invalid"
			}
		}
		e.Configure(c, reason)
		return envelope(registration())
	case pluginabi.MethodPluginQuiesce:
		e.Quiesce()
	case pluginabi.MethodPluginShutdown:
		e.Shutdown()
	case pluginabi.MethodRequestInterceptBefore:
		return envelope(pluginapi.RequestInterceptResponse{})
	case pluginabi.MethodRequestInterceptAfter:
		var r pluginapi.RequestInterceptRequest
		if json.Unmarshal(raw, &r) == nil {
			return envelope(e.Intercept(r))
		}
		return envelope(pluginapi.RequestInterceptResponse{})
	case pluginabi.MethodRequestComplete:
		var r pluginapi.RequestCompletion
		if json.Unmarshal(raw, &r) == nil {
			e.Complete(r.RequestID)
		}
	case pluginabi.MethodResponseInterceptAfter:
		var r pluginapi.ResponseInterceptRequest
		var clear []string
		if json.Unmarshal(raw, &r) == nil && !websocket(r.RequestHeaders, r.Metadata) {
			clear = e.ClearTicketHeaders(r.RequestID)
		}
		return envelope(pluginapi.ResponseInterceptResponse{ClearHeaders: clear})
	case pluginabi.MethodResponseInterceptStreamChunk:
		var r pluginapi.StreamChunkInterceptRequest
		var clear []string
		if json.Unmarshal(raw, &r) == nil && r.ChunkIndex == pluginapi.StreamChunkHeaderInitIndex && !websocket(r.RequestHeaders, r.Metadata) {
			clear = e.ClearTicketHeaders(r.RequestID)
		}
		return envelope(pluginapi.StreamChunkInterceptResponse{ClearHeaders: clear})
	case pluginabi.MethodManagementRegister:
		// Public resource serves static HTML only. All data/mutations require
		// CPA Management API authentication on the separate routes below.
		type route struct {
			Method string
			Path   string
		}
		type resource struct {
			Path        string
			Menu        string
			Description string
		}
		routes := []route{{"GET", "/v0/management/codex-ticket/status"}, {"POST", "/v0/management/codex-ticket/refresh"}, {"POST", "/v0/management/codex-ticket/pause"}, {"POST", "/v0/management/codex-ticket/resume"}, {"GET", "/v0/management/codex-ticket/proxy-options"}, {"POST", "/v0/management/codex-ticket/proxy"}, {"POST", "/v0/management/codex-ticket/proxy-test"}}
		return envelope(struct {
			Routes    []route    `json:"routes"`
			Resources []resource `json:"resources"`
		}{routes, []resource{{"/settings", "Codex 采票代理", "自定义 HTTP/SOCKS5 代理或选择 CPA 已配置的代理；需要 CPA 管理密钥。"}}})
	case pluginabi.MethodManagementHandle:
		var r pluginapi.ManagementRequest
		if json.Unmarshal(raw, &r) != nil {
			return managementResponse(400, map[string]string{"reason": "invalid_request"})
		}
		// No query parameters accepted, including tokens; built-in management auth only.
		if len(r.Query) != 0 {
			return managementResponse(400, map[string]string{"reason": "query_not_supported"})
		}
		switch {
		case r.Method == "GET" && r.Path == "/v0/resource/plugins/codex-ticket/settings":
			return pageResponse()
		case r.Method == "GET" && r.Path == "/v0/management/codex-ticket/proxy-options":
			return managementResponse(200, e.ProxySettings())
		case r.Method == "POST" && r.Path == "/v0/management/codex-ticket/proxy":
			if err := e.SaveProxy(r.Body); err != nil {
				return managementResponse(400, map[string]string{"reason": err.Error()})
			}
			return managementResponse(200, e.ProxySettings())
		case r.Method == "POST" && r.Path == "/v0/management/codex-ticket/proxy-test":
			return managementResponse(200, e.TestProxy())
		case r.Method == "GET" && r.Path == "/v0/management/codex-ticket/status":
			return managementResponse(200, e.Status())
		case r.Method == "POST" && r.Path == "/v0/management/codex-ticket/refresh":
			e.Refresh()
			return managementResponse(202, map[string]string{"reason": "scan_requested_backoff_preserved"})
		case r.Method == "POST" && r.Path == "/v0/management/codex-ticket/pause":
			e.Pause(true)
			return managementResponse(200, e.Status())
		case r.Method == "POST" && r.Path == "/v0/management/codex-ticket/resume":
			e.Pause(false)
			return managementResponse(200, e.Status())
		default:
			return managementResponse(404, map[string]string{"reason": "not_found"})
		}
	}
	return envelope(struct{}{})
}
func managementResponse(status int, v any) []byte {
	b, _ := json.Marshal(v)
	return envelope(pluginapi.ManagementResponse{StatusCode: status, Headers: http.Header{"Content-Type": {"application/json"}, "Cache-Control": {"no-store"}}, Body: b})
}
