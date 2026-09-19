package ticket

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestErrorOnlyLoggingAlsoBlocksInjection(t *testing.T) {
	e, c, _ := fixture(t)
	seed(t, e, "a", "gpt-6-astra", time.Now().Add(time.Hour))
	raw, err := os.ReadFile(c.HostConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), "commercial-mode: true", "commercial-mode: false", 1))
	if err := os.WriteFile(c.HostConfigFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	g := hostGate(c)
	if g.inject || g.injectReason != "log_unsafe" || !g.harvest {
		t.Fatal("error-only logging safety gate", g)
	}
	if e.Intercept(request("a", "gpt-6-astra")).Headers.Get(Header) != "" {
		t.Fatal("ticket could leak to error logs")
	}
}
