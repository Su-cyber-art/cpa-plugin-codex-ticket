package ticket

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type proxyHost struct {
	*fakeHost
	proxyURL string
}

func (h *proxyHost) Get(ctx context.Context, id string) (pluginapi.HostAuthGetResponse, error) {
	r, err := h.fakeHost.Get(ctx, id)
	if err != nil {
		return r, err
	}
	var obj map[string]any
	json.Unmarshal(r.JSON, &obj)
	obj["proxy_url"] = h.proxyURL
	r.JSON, _ = json.Marshal(obj)
	return r, nil
}
func addCoreProxy(t *testing.T, c Config, s string) {
	t.Helper()
	raw, err := os.ReadFile(c.HostConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte("proxy-url: "+s+"\n")...)
	if err := os.WriteFile(c.HostConfigFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
}
func TestCustomProxySaveAndRedaction(t *testing.T) {
	for _, scheme := range []string{"http", "https", "socks5", "socks5h"} {
		t.Run(scheme, func(t *testing.T) {
			e, c, _ := fixture(t)
			e.Pause(true)
			seed(t, e, "a", "gpt-6-astra", time.Now().Add(time.Hour))
			raw, _ := json.Marshal(proxySelection{Mode: "custom", URL: scheme + "://secret-user:secret-pass@example.test:1234"})
			if err := e.SaveProxy(raw); err != nil {
				t.Fatal(err)
			}
			u, err := e.resolveProxy(c)
			if err != nil || u.Scheme != scheme {
				t.Fatal(err)
			}
			b, _ := json.Marshal(e.ProxySettings())
			if strings.Contains(string(b), "secret-user") || strings.Contains(string(b), "secret-pass") {
				t.Fatal("secret leaked")
			}
			if !e.ProxySettings().Current.Configured || e.Status().CachedCount != 0 {
				t.Fatal("state not switched")
			}
			st, _ := os.Stat(c.ProxyFile)
			if st.Mode().Perm() != 0600 {
				t.Fatal("permissions")
			}
			before, _ := os.ReadFile(c.ProxyFile)
			if err := e.SaveProxy([]byte(`{"mode":"custom","url":""}`)); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(c.ProxyFile)
			if string(before) != string(after) {
				t.Fatal("blank update rewrote secret")
			}
		})
	}
}
func TestCoreProxySelectionLiveAndRemoval(t *testing.T) {
	e, c, h := fixture(t)
	e.Pause(true)
	addCoreProxy(t, c, "socks5h://user:secret@global.test:1080")
	e.host = &proxyHost{h, "http://account-user:account-secret@account.test:8080"}
	s := e.ProxySettings()
	if len(s.Options) != 2 {
		t.Fatal(s)
	}
	for _, o := range s.Options {
		if strings.Contains(o.Label, "secret") || strings.Contains(o.Label, "user:") {
			t.Fatal("leaked credentials")
		}
	}
	if err := e.SaveProxy([]byte(`{"mode":"core","ref":"core:global"}`)); err != nil {
		t.Fatal(err)
	}
	u, err := e.getProxy(c)
	if err != nil || u.Hostname() != "global.test" {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(c.ProxyFile)
	if strings.Contains(string(b), "secret") || strings.Contains(string(b), "global.test") {
		t.Fatal("selection copied secret rather than reference")
	}
	seed(t, e, "a", "gpt-6-astra", time.Now().Add(time.Hour))
	raw, _ := os.ReadFile(c.HostConfigFile)
	raw = []byte(strings.Replace(string(raw), "global.test", "updated.test", 1))
	os.WriteFile(c.HostConfigFile, raw, 0600)
	u, err = e.getProxy(c)
	if err != nil || u.Hostname() != "updated.test" || e.Status().CachedCount != 0 {
		t.Fatal("core update not followed")
	}
	writeGate(t, c, true, true, true, false)
	if _, err = e.resolveProxy(c); err == nil {
		t.Fatal("missing selected source fell back")
	}
	if e.ProxySettings().Current.Reason != "selected_proxy_unavailable" {
		t.Fatal("wrong reason")
	}
	if err := e.SaveProxy([]byte(`{"mode":"core","ref":"core:auth:a"}`)); err != nil {
		t.Fatal(err)
	}
	u, err = e.resolveProxy(c)
	if err != nil || u.Hostname() != "account.test" {
		t.Fatal(err)
	}
	h.mu.Lock()
	h.disabled["a"] = true
	h.mu.Unlock()
	e.proxyListMu.Lock()
	e.proxyListUntil = time.Time{}
	e.proxyListMu.Unlock()
	if _, err = e.resolveProxy(c); err == nil {
		t.Fatal("disabled core account still selectable")
	}
}
func TestProxyValidationDoesNotDestroySavedSelection(t *testing.T) {
	e, c, _ := fixture(t)
	e.Pause(true)
	before, _ := os.ReadFile(c.ProxyFile)
	for _, s := range []string{`{"mode":"custom","url":"file:///etc/passwd"}`, `{"mode":"custom","url":"http://host/evil"}`, `{"mode":"core","ref":"missing"}`, `{"mode":"custom","url":"http://host","extra":"secret"}`, `{"mode":"direct"}`, `{"mode":"custom","url":"http://host"} {}`, `{"mode":"custom","url":"http://host?"}`} {
		if err := e.SaveProxy([]byte(s)); err == nil {
			t.Fatal("accepted", s)
		}
		after, _ := os.ReadFile(c.ProxyFile)
		if string(before) != string(after) {
			t.Fatal("bad save mutated selection")
		}
	}
	link := filepath.Join(filepath.Dir(c.ProxyFile), "symlink")
	os.Symlink(c.ProxyFile, link)
	if err := atomicSelection(link, proxySelection{Mode: "custom", URL: "http://host"}); err == nil {
		t.Fatal("symlink write accepted")
	}
}
func TestProxyOptionsEnumeratesProviderEntries(t *testing.T) {
	e, c, _ := fixture(t)
	e.Pause(true)
	raw, _ := os.ReadFile(c.HostConfigFile)
	raw = append(raw, []byte("openai-compatibility:\n  - name: example\n    api-key-entries:\n      - api-key: NOT_RETURNED\n        proxy-url: https://user:NOT_RETURNED@provider.test:443\n")...)
	os.WriteFile(c.HostConfigFile, raw, 0600)
	s := e.ProxySettings()
	if len(s.Options) != 1 || s.Options[0].Source != "config" {
		t.Fatal(s)
	}
	b, _ := json.Marshal(s)
	if strings.Contains(string(b), "NOT_RETURNED") {
		t.Fatal("secret leaked")
	}
	body, _ := json.Marshal(proxySelection{Mode: "core", Ref: s.Options[0].ID})
	if err := e.SaveProxy(body); err != nil {
		t.Fatal(err)
	}
}
func TestPublicPageContainsNoRuntimeData(t *testing.T) {
	e, _, _ := fixture(t)
	r, _ := json.Marshal(pluginapi.ManagementRequest{Method: "GET", Path: "/v0/resource/plugins/codex-ticket/settings"})
	var env pluginabiEnvelopeForTest
	json.Unmarshal(e.Dispatch("management.handle", r), &env)
	var response pluginapi.ManagementResponse
	json.Unmarshal(env.Result, &response)
	if response.StatusCode != 200 || !strings.Contains(response.Headers.Get("Content-Type"), "text/html") || !strings.Contains(response.Headers.Get("Content-Security-Policy"), "sha256-") {
		t.Fatal("invalid static shell response")
	}
	for _, s := range []string{"test-token-SECRET", "proxy-SECRET", "account-a"} {
		if strings.Contains(string(response.Body), s) {
			t.Fatal("runtime data leaked")
		}
	}
}

type pluginabiEnvelopeForTest struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
}
