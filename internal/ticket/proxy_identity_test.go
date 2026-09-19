package ticket

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestCoreProxyReferenceSurvivesReorderButNotRemoval(t *testing.T) {
	e, c, _ := fixture(t)
	e.Pause(true)
	base, _ := os.ReadFile(c.HostConfigFile)
	entries := []string{"  - api-key: identity-A\n    proxy-url: http://a.test:8080\n", "  - api-key: identity-B\n    proxy-url: http://b.test:8080\n"}
	write := func(items ...string) {
		os.WriteFile(c.HostConfigFile, append(append([]byte{}, base...), []byte("codex-api-key:\n"+strings.Join(items, ""))...), 0600)
	}
	write(entries...)
	s := e.ProxySettings()
	var ref string
	for _, o := range s.Options {
		if o.Host == "a.test" {
			ref = o.ID
		}
	}
	if ref == "" {
		t.Fatal("missing option")
	}
	b, _ := json.Marshal(proxySelection{Mode: "core", Ref: ref})
	if err := e.SaveProxy(b); err != nil {
		t.Fatal(err)
	}
	write(entries[1], entries[0])
	u, err := e.resolveProxy(c)
	if err != nil || u.Hostname() != "a.test" {
		t.Fatal("reorder changed selection")
	}
	write(entries[1])
	if _, err = e.resolveProxy(c); err == nil {
		t.Fatal("removed entry silently selected another proxy")
	}
	write(entries[0], entries[0])
	if _, err = e.resolveProxy(c); err == nil {
		t.Fatal("ambiguous duplicates accepted")
	}
}

type countedProxyHost struct {
	*fakeHost
	lists, gets atomic.Int32
}

func (h *countedProxyHost) List(ctx context.Context) ([]pluginapi.HostAuthFileEntry, error) {
	h.lists.Add(1)
	return h.fakeHost.List(ctx)
}
func (h *countedProxyHost) Get(ctx context.Context, id string) (pluginapi.HostAuthGetResponse, error) {
	h.gets.Add(1)
	return h.fakeHost.Get(ctx, id)
}
func TestGlobalProxySaveIndependentOfBoundedAccountDiscovery(t *testing.T) {
	e, c, h := fixture(t)
	e.Pause(true)
	for i := 0; i < 1000; i++ {
		h.bind(fmt.Sprint(i), fmt.Sprint("account-", i))
	}
	wrapped := &countedProxyHost{fakeHost: h}
	e.host = wrapped
	addCoreProxy(t, c, "http://global.test:8080")
	if err := e.SaveProxy([]byte(`{"mode":"core","ref":"core:global"}`)); err != nil {
		t.Fatal(err)
	}
	if wrapped.lists.Load() != 0 || wrapped.gets.Load() != 0 {
		t.Fatal("global save scanned accounts")
	}
	s := e.ProxySettings()
	if !s.Current.Configured || s.DiscoveryReason != "proxy_discovery_partial" {
		t.Fatal("partial results discarded", s.Current, s.DiscoveryReason)
	}
	if wrapped.gets.Load() > 256 || wrapped.lists.Load() != 1 {
		t.Fatal("unbounded discovery")
	}
	if _, err := e.resolveProxy(c); err != nil {
		t.Fatal(err)
	}
	if wrapped.lists.Load() != 1 {
		t.Fatal("selected global resolution queried accounts")
	}
}
