package ticket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type fakeHost struct {
	mu       sync.Mutex
	accounts map[string]string
	disabled map[string]bool
}

func (h *fakeHost) List(ctx context.Context) ([]pluginapi.HostAuthFileEntry, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []pluginapi.HostAuthFileEntry
	for id := range h.accounts {
		out = append(out, h.entry(id))
	}
	return out, nil
}
func (h *fakeHost) entry(id string) pluginapi.HostAuthFileEntry {
	return pluginapi.HostAuthFileEntry{ID: id, AuthIndex: id, Provider: "codex", Status: "active", Source: "file", Path: "/auth/" + id + ".json", Size: 200, Disabled: h.disabled[id]}
}
func (h *fakeHost) Runtime(ctx context.Context, id string) (pluginapi.HostAuthFileEntry, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.accounts[id]; !ok {
		return pluginapi.HostAuthFileEntry{}, errors.New("missing")
	}
	return h.entry(id), nil
}
func (h *fakeHost) Get(ctx context.Context, id string) (pluginapi.HostAuthGetResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	a, ok := h.accounts[id]
	if !ok {
		return pluginapi.HostAuthGetResponse{}, errors.New("missing")
	}
	raw, _ := json.Marshal(map[string]any{"type": "codex", "access_token": "test-token-SECRET", "account_id": a})
	return pluginapi.HostAuthGetResponse{AuthIndex: id, JSON: raw}, nil
}
func (h *fakeHost) bind(id, account string) { h.mu.Lock(); h.accounts[id] = account; h.mu.Unlock() }
func fixture(t *testing.T) (*Engine, Config, *fakeHost) {
	t.Helper()
	d := t.TempDir()
	if err := os.Chmod(d, 0700); err != nil {
		t.Fatal(err)
	}
	c := DefaultConfig()
	c.Enabled = true
	c.HarvestEnabled = true
	c.InjectEnabled = true
	c.ProxyFile = filepath.Join(d, "proxy")
	c.HostConfigFile = filepath.Join(d, "host.yaml")
	c.ScanIntervalSeconds = 30
	c.TimeoutSeconds = 2
	if err := os.WriteFile(c.ProxyFile, []byte("socks5h://user:proxy-SECRET@127.0.0.1:9\n"), 0600); err != nil {
		t.Fatal(err)
	}
	writeGate(t, c, true, true, true, false)
	h := &fakeHost{accounts: map[string]string{"a": "account-a"}, disabled: map[string]bool{}}
	e := New(h)
	e.cfg = c
	t.Cleanup(e.Shutdown)
	return e, c, h
}
func writeGate(t *testing.T, c Config, global, enabled, harvest, rawLog bool) {
	t.Helper()
	s := fmt.Sprintf("request-log: %t\ncommercial-mode: %t\nplugins:\n  enabled: %t\n  configs:\n    codex-ticket:\n      enabled: %t\n      harvest_enabled: %t\n      inject_enabled: true\n", rawLog, !rawLog, global, enabled, harvest)
	if err := os.WriteFile(c.HostConfigFile, []byte(s), 0600); err != nil {
		t.Fatal(err)
	}
}
func fakeTicket() string { return "gAAAAA" + strings.Repeat("B", 286) }
func seed(t *testing.T, e *Engine, index, model string, expiry time.Time) {
	t.Helper()
	c, why := e.load(context.Background(), index)
	if why != "" {
		t.Fatal(why)
	}
	e.mu.Lock()
	e.cache[cacheKey{index, c.identity, model}] = &entry{ticket: fakeTicket(), expiry: expiry}
	e.mu.Unlock()
}
func request(index, model string) pluginapi.RequestInterceptRequest {
	return pluginapi.RequestInterceptRequest{RequestID: "r", ToFormat: "codex", Model: model, Headers: http.Header{}, Metadata: map[string]any{"selected_auth_index": index}}
}
func waitFor(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition timeout")
}

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type bodySpy struct{ reads, closes atomic.Int32 }

func (b *bodySpy) Read([]byte) (int, error) { b.reads.Add(1); return 0, io.EOF }
func (b *bodySpy) Close() error             { b.closes.Add(1); return nil }

func TestProxyValidationAndPrivateFile(t *testing.T) {
	for _, s := range []string{"http://localhost:8080", "https://user:p%40ss@example.com:443", "socks5h://u:p@host:1080", "socks5://[::1]:1080"} {
		if _, err := parseProxy([]byte(s)); err != nil {
			t.Fatal(s, err)
		}
	}
	for _, s := range []string{"", "ftp://host", "http://host/path", "socks5h://host:0", "http://host:65536", "http://host?q=secret", "http://host#x", "http://host\nX: y"} {
		if _, err := parseProxy([]byte(s)); err == nil {
			t.Fatalf("accepted %q", s)
		} else if strings.Contains(err.Error(), s) && s != "" {
			t.Fatal("leaked proxy")
		}
	}
	e, c, _ := fixture(t)
	_ = e
	if _, err := loadProxy(c.ProxyFile); err != nil {
		t.Fatal(err)
	}
	os.Chmod(c.ProxyFile, 0644)
	if _, err := loadProxy(c.ProxyFile); err == nil {
		t.Fatal("accepted public secret")
	}
	link := filepath.Join(t.TempDir(), "link")
	os.Symlink(c.ProxyFile, link)
	if _, err := loadProxy(link); err == nil {
		t.Fatal("followed symlink")
	}
	if _, err := makeClient(nil, time.Second); err == nil {
		t.Fatal("nil proxy must not fallback")
	}
}
func TestConfigDefaultsAndBounds(t *testing.T) {
	c, err := ParseConfig(nil)
	if err != nil || c.Enabled || c.HarvestEnabled || c.InjectEnabled {
		t.Fatal(c, err)
	}
	for _, raw := range []string{"ttl_seconds: 100\nrefresh_before_seconds: 100", "max_concurrency: 0", "models: [a,a]", "proxy_file: relative", "target_length: 9000", "models: []"} {
		if _, err := ParseConfig([]byte(raw)); err == nil {
			t.Fatal("accepted", raw)
		}
	}
	c, err = ParseConfig([]byte("models: gpt-6-astra,gpt-5.6-sol"))
	if err != nil || len(c.Models) != 2 {
		t.Fatal(c, err)
	}
}
func TestHostEnableAndLoggingGates(t *testing.T) {
	e, c, _ := fixture(t)
	seed(t, e, "a", "gpt-6-astra", time.Now().Add(time.Hour))
	r := request("a", "gpt-6-astra")
	if e.Intercept(r).Headers.Get(Header) == "" {
		t.Fatal("not injected")
	}
	for _, tc := range []struct {
		name                              string
		global, enabled, harvest, logging bool
		reason                            string
	}{{"global_off", false, true, true, false, "host_disabled"}, {"plugin_off", true, false, true, false, "host_disabled"}, {"raw_log", true, true, true, true, "log_unsafe"}} {
		t.Run(tc.name, func(t *testing.T) {
			writeGate(t, c, tc.global, tc.enabled, tc.harvest, tc.logging)
			if e.Intercept(r).Headers.Get(Header) != "" {
				t.Fatal("unsafe injection")
			}
			if hostGate(c).injectReason != tc.reason {
				t.Fatal(hostGate(c))
			}
		})
	}
	os.Remove(c.HostConfigFile)
	if e.Intercept(r).Headers.Get(Header) != "" {
		t.Fatal("injected without config")
	}
}
func TestInjectionIsolationExpiryAndPassThrough(t *testing.T) {
	e, c, h := fixture(t)
	h.bind("b", "account-b")
	seed(t, e, "a", "gpt-6-astra", time.Now().Add(time.Hour))
	for _, tc := range []struct {
		name string
		req  pluginapi.RequestInterceptRequest
		want bool
	}{{"selected", request("a", "gpt-6-astra"), true}, {"other_account", request("b", "gpt-6-astra"), false}, {"other_model", request("a", "gpt-5.6-sol"), false}, {"unlisted", request("a", "other"), false}, {"missing_auth", request("missing", "gpt-6-astra"), false}} {
		t.Run(tc.name, func(t *testing.T) {
			o := e.Intercept(tc.req)
			if (o.Headers.Get(Header) != "") != tc.want || o.Terminate || len(o.Body) > 0 {
				t.Fatal("wrong injection/pass-through")
			}
		})
	}
	r := request("a", "gpt-6-astra")
	r.Headers.Set(Header, "client-ticket")
	if e.Intercept(r).Headers.Get(Header) != "" {
		t.Fatal("overrode client ticket by default")
	}
	e.mu.Lock()
	e.cfg.ReplaceExisting = true
	e.mu.Unlock()
	if e.Intercept(r).Headers.Get(Header) == "" {
		t.Fatal("explicit replacement failed")
	}
	for _, kind := range []string{"upgrade", "key", "session", "protocol"} {
		rr := request("a", "gpt-6-astra")
		switch kind {
		case "upgrade":
			rr.Headers.Set("Upgrade", "websocket")
		case "key":
			rr.Headers.Set("Sec-Websocket-Key", "fake")
		case "session":
			rr.Metadata["execution_session_id"] = "s"
		case "protocol":
			rr.ToFormat = "openai"
		}
		if e.Intercept(rr).Headers.Get(Header) != "" {
			t.Fatal("did not skip", kind)
		}
	}
	seed(t, e, "a", "gpt-6-astra", time.Now().Add(-time.Second))
	if e.Intercept(request("a", "gpt-6-astra")).Headers.Get(Header) != "" {
		t.Fatal("expired injection")
	}
	seed(t, e, "a", "gpt-6-astra", time.Now().Add(time.Hour))
	h.bind("a", "account-rebound")
	if e.Intercept(request("a", "gpt-6-astra")).Headers.Get(Header) != "" {
		t.Fatal("cross identity injection")
	}
	if e.Status().CachedCount != 0 {
		t.Fatal("old identity retained")
	}
	os.Remove(c.ProxyFile)
	if e.Intercept(request("a", "gpt-6-astra")).Terminate {
		t.Fatal("fail-open violated")
	}
}
func TestCredentialTypeAndTokenRotation(t *testing.T) {
	_, _, h := fixture(t)
	a, _ := h.Runtime(context.Background(), "a")
	r, _ := h.Get(context.Background(), "a")
	c, err := parseCredential(r.JSON, a)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(r.JSON, &m)
	m["access_token"] = "new-token"
	raw, _ := json.Marshal(m)
	newc, err := parseCredential(raw, a)
	if err != nil || newc.identity != c.identity {
		t.Fatal("refresh changed identity")
	}
	for _, k := range []string{"api_key", "account_type", "disabled", "account_id"} {
		copy := map[string]any{}
		for a, b := range m {
			copy[a] = b
		}
		switch k {
		case "api_key":
			copy[k] = "api-secret"
		case "account_type":
			copy[k] = "api_key"
		case "disabled":
			copy[k] = true
		case "account_id":
			copy[k] = "bad\nvalue"
		}
		raw, _ = json.Marshal(copy)
		if _, err := parseCredential(raw, a); err == nil {
			t.Fatal("accepted", k)
		}
	}
}
func TestProbeHeaderOnlyAndRejectsInvalid(t *testing.T) {
	cfg := DefaultConfig()
	for _, tc := range []struct {
		name   string
		code   int
		ticket string
		want   bool
	}{{"good", 200, fakeTicket(), true}, {"not_status_292", 292, fakeTicket(), false}, {"overload", 503, fakeTicket(), false}, {"bad_length", 200, "gAAAAA", false}, {"bad_prefix", 200, strings.Repeat("X", 292), false}, {"missing", 200, "", false}} {
		t.Run(tc.name, func(t *testing.T) {
			b := &bodySpy{}
			client := &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("Authorization") != "Bearer fake" || r.Header.Get("ChatGPT-Account-ID") != "account" || !r.Close || r.Header.Get("session_id") == "" {
					t.Error("missing probe identity/close")
				}
				return &http.Response{StatusCode: tc.code, Header: http.Header{Header: {tc.ticket}}, Body: b}, nil
			})}
			got := probe(context.Background(), client, "https://example.test", credential{token: "fake", account: "account"}, "gpt-6-astra", cfg)
			if (got.ticket != "") != tc.want || b.reads.Load() != 0 || b.closes.Load() != 1 {
				t.Fatal("bad header-only result", got.reason)
			}
		})
	}
}
func TestTransportAndBackoff(t *testing.T) {
	u, _ := parseProxy([]byte("socks5h://host:1080"))
	client, err := makeClient(u, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	tr := client.Transport.(*http.Transport)
	if !tr.DisableKeepAlives || tr.ForceAttemptHTTP2 || tr.Proxy != nil {
		t.Fatal("unsafe transport")
	}
	if client.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("redirects enabled")
	}
	cfg := DefaultConfig()
	for n := 1; n < 20; n++ {
		d := backoff(n, cfg, 300*time.Second)
		if d < 300*time.Second || d > time.Duration(cfg.RetryMaxSeconds)*time.Second {
			t.Fatal("backoff bounds", d)
		}
	}
	if retryAfter("9999999999", time.Now(), time.Minute) != time.Minute {
		t.Fatal("retry cap")
	}
	if retryAfter("-1", time.Now(), time.Minute) != 0 {
		t.Fatal("negative retry")
	}
}
func TestHarvestCacheAndStatusRedaction(t *testing.T) {
	e, c, _ := fixture(t)
	var calls atomic.Int32
	e.clientFactory = func(Config) (*http.Client, error) {
		return &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{StatusCode: 200, Header: http.Header{Header: {fakeTicket()}}, Body: io.NopCloser(strings.NewReader("unused"))}, nil
		})}, nil
	}
	e.harvest(context.Background(), c, 0, "a", "gpt-6-astra")
	e.harvest(context.Background(), c, 0, "a", "gpt-6-astra")
	if calls.Load() != 1 || e.Status().CachedCount != 1 {
		t.Fatal("no cache dedup")
	}
	e.Intercept(request("a", "gpt-6-astra"))
	raw, _ := json.Marshal(e.Status())
	for _, s := range []string{fakeTicket(), "test-token-SECRET", "proxy-SECRET", "account-a"} {
		if strings.Contains(string(raw), s) {
			t.Fatal("status leaked secret")
		}
	}
	if e.Status().Entries[0].InjectedCount != 1 {
		t.Fatal("missing count")
	}
	if len(e.ClearTicketHeaders("r")) != 1 || len(e.ClearTicketHeaders("not-owned")) != 0 {
		t.Fatal("header cleanup ownership")
	}
	e.Complete("r")
	if len(e.ClearTicketHeaders("r")) != 0 {
		t.Fatal("completion cleanup")
	}
}
func TestBackoffPreservedAndNoDirectFallback(t *testing.T) {
	e, c, _ := fixture(t)
	var calls atomic.Int32
	e.clientFactory = func(Config) (*http.Client, error) {
		return &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": {"600"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
		})}, nil
	}
	e.harvest(context.Background(), c, 0, "a", "gpt-6-astra")
	e.harvest(context.Background(), c, 0, "a", "gpt-6-astra")
	if calls.Load() != 1 || e.Status().Entries[0].BackoffSeconds < 598 {
		t.Fatal("retry gate")
	}
	os.Remove(c.ProxyFile)
	e.harvest(context.Background(), c, 0, "a", "gpt-5.6-sol")
	if calls.Load() != 1 {
		t.Fatal("missing proxy made request")
	}
}
func TestBoundedConcurrencyAndCancellation(t *testing.T) {
	e, c, h := fixture(t)
	for i := 0; i < 8; i++ {
		h.bind(fmt.Sprint(i), fmt.Sprint("account", i))
	}
	var active, peak atomic.Int32
	started := make(chan struct{}, 20)
	e.clientFactory = func(Config) (*http.Client, error) {
		return &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			n := active.Add(1)
			defer active.Add(-1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			started <- struct{}{}
			<-r.Context().Done()
			return nil, r.Context().Err()
		})}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.scan(ctx, c, 0); close(done) }()
	<-started
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scan cancellation stuck")
	}
	if peak.Load() > int32(c.MaxConcurrency) || active.Load() != 0 {
		t.Fatal("concurrency bound")
	}
}
func TestPauseAndHostDisableCancelLiveProbe(t *testing.T) {
	for _, mode := range []string{"pause", "host-disable", "quiesce"} {
		t.Run(mode, func(t *testing.T) {
			e, c, _ := fixture(t)
			var active atomic.Int32
			started := make(chan struct{}, 4)
			e.clientFactory = func(Config) (*http.Client, error) {
				return &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
					active.Add(1)
					defer active.Add(-1)
					started <- struct{}{}
					<-r.Context().Done()
					return nil, r.Context().Err()
				})}, nil
			}
			e.Configure(c, "test")
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("probe not started")
			}
			switch mode {
			case "pause":
				e.Pause(true)
			case "host-disable":
				writeGate(t, c, true, false, true, false)
			case "quiesce":
				e.Quiesce()
			}
			waitFor(t, func() bool { return active.Load() == 0 })
			if e.Intercept(request("a", "gpt-6-astra")).Headers.Get(Header) != "" {
				t.Fatal("inactive injected")
			}
		})
	}
}
func TestRPCRegistrationAndManagement(t *testing.T) {
	e, _, _ := fixture(t)
	raw := e.Dispatch(pluginabi.MethodManagementRegister, []byte("{}"))
	var env pluginabi.Envelope
	if json.Unmarshal(raw, &env) != nil || !env.OK {
		t.Fatal("bad envelope")
	}
	var routes map[string]any
	json.Unmarshal(env.Result, &routes)
	if len(routes["resources"].([]any)) != 1 || len(routes["routes"].([]any)) != 7 {
		t.Fatal("unexpected routes")
	}
	if routes["resources"].([]any)[0].(map[string]any)["Path"] != "/settings" {
		t.Fatal("only static settings shell may be public")
	}
	raw = e.Dispatch(pluginabi.MethodRequestInterceptAfter, []byte("invalid"))
	json.Unmarshal(raw, &env)
	if !env.OK {
		t.Fatal("malformed request not fail open")
	}
	b, _ := json.Marshal(pluginapi.ManagementRequest{Method: "GET", Path: "/v0/management/codex-ticket/status"})
	raw = e.Dispatch(pluginabi.MethodManagementHandle, b)
	json.Unmarshal(raw, &env)
	var r pluginapi.ManagementResponse
	json.Unmarshal(env.Result, &r)
	if r.StatusCode != 200 || !strings.Contains(string(r.Body), "quality_guaranteed\":false") {
		t.Fatal("status output")
	}
}
