package ticket

import (
	"context"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// This integration test uses a fake upstream and net/http client. It verifies
// the interceptor's returned header reaches the wire without exposing a real
// ticket; it does not exercise the CPA executor. Real CPA integration is tested
// separately in the isolated instance.
func TestCandidateWireHeaderAndFailOpen(t *testing.T) {
	e, c, _ := fixture(t)
	var seen atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(Header) == fakeTicket() {
			seen.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	seed(t, e, "a", "gpt-6-astra", time.Now().Add(time.Hour))
	req := request("a", "gpt-6-astra")
	mod := e.Intercept(req)
	httpReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, upstream.URL, strings.NewReader(`{}`))
	for k, v := range mod.Headers {
		httpReq.Header[k] = v
	}
	response, err := upstream.Client().Do(httpReq)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if seen.Load() != 1 {
		t.Fatal("candidate header not on wire")
	}
	c.InjectEnabled = false
	e.cfg = c
	mod = e.Intercept(pluginapi.RequestInterceptRequest{RequestID: "other", ToFormat: "codex", Model: "gpt-6-astra", Metadata: map[string]any{"selected_auth_index": "a"}})
	if mod.Terminate || mod.Headers.Get(Header) != "" {
		t.Fatal("disabled plugin changed traffic")
	}
}
