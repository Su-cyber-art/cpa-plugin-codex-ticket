package ticket

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

const upstreamEndpoint = "https://chatgpt.com/backend-api/codex/responses"

func parseProxy(raw []byte) (*url.URL, error) {
	fail := errors.New("proxy_invalid")
	s := strings.TrimSpace(string(raw))
	if s == "" || len(s) > 4096 || strings.ContainsAny(s, "\r\n\t ") {
		return nil, fail
	}
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fail
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, fail
	}
	if u.Port() != "" {
		p, err := strconv.Atoi(u.Port())
		if err != nil || p < 1 || p > 65535 {
			return nil, fail
		}
	}
	if strings.ContainsAny(u.Hostname(), "\x00\r\n\t ") {
		return nil, fail
	}
	return u, nil
}
func loadProxy(path string) (*url.URL, error) {
	raw, err := readBounded(path, 4096, true)
	if err != nil {
		return nil, errors.New("proxy_unavailable")
	}
	return parseProxy(raw)
}
func makeClient(u *url.URL, timeout time.Duration) (*http.Client, error) {
	if u == nil {
		return nil, errors.New("proxy_unavailable")
	}
	// Do not clone DefaultTransport (environment proxy or HTTP/2 settings).
	d := &net.Dialer{Timeout: timeout, KeepAlive: -1}
	t := &http.Transport{DialContext: d.DialContext, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}}, DisableKeepAlives: true, ForceAttemptHTTP2: false, TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{}, TLSHandshakeTimeout: timeout, ResponseHeaderTimeout: timeout, MaxResponseHeaderBytes: 64 << 10, DisableCompression: true}
	switch u.Scheme {
	case "http", "https":
		t.Proxy = http.ProxyURL(u)
	case "socks5", "socks5h":
		addr := u.Host
		if u.Port() == "" {
			addr = net.JoinHostPort(u.Hostname(), "1080")
		}
		var auth *proxy.Auth
		if u.User != nil {
			password, _ := u.User.Password()
			auth = &proxy.Auth{User: u.User.Username(), Password: password}
		}
		sd, err := proxy.SOCKS5("tcp", addr, auth, d)
		if err != nil {
			return nil, errors.New("proxy_invalid")
		}
		cd, ok := sd.(proxy.ContextDialer)
		if !ok {
			return nil, errors.New("proxy_context_unsupported")
		}
		// x/net SOCKS5 sends the hostname to the proxy for BOTH schemes and supports
		// cancellation during dialing/handshake. Never fall back to local DNS/direct.
		t.DialContext = cd.DialContext
	default:
		return nil, errors.New("proxy_invalid")
	}
	return &http.Client{Transport: t, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

type probeResult struct {
	ticket, reason string
	status, length int
	retryAfter     time.Duration
}

// endpoint is unexported and fixed in production; tests may substitute localhost.
func probe(ctx context.Context, client *http.Client, endpoint string, c credential, model string, cfg Config) probeResult {
	// Model names are validated, but use JSON marshaling for safety.
	body := probeBody(model)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return probeResult{reason: "request_invalid"}
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("ChatGPT-Account-ID", c.account)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	var session [16]byte
	if _, err := cryptorand.Read(session[:]); err != nil {
		return probeResult{reason: "random_unavailable"}
	}
	req.Header.Set("session_id", hex.EncodeToString(session[:]))
	req.Close = true
	req.Header.Set("Version", "0.153.4")
	req.Header.Set("Originator", "codex_cli_rs")
	req.Header.Set("User-Agent", "codex_cli_rs/0.153.4")
	resp, err := client.Do(req)
	if err != nil {
		reason := "transport_error"
		if ctx.Err() != nil {
			reason = "canceled"
		}
		return probeResult{reason: reason}
	}
	// Close immediately after response headers. Intentionally NO Read or drain.
	_ = resp.Body.Close()
	ticket := resp.Header.Get(Header)
	r := probeResult{status: resp.StatusCode, length: len(ticket), reason: "http_status"}
	if resp.StatusCode == 429 || resp.StatusCode == 503 {
		r.retryAfter = retryAfter(resp.Header.Get("Retry-After"), time.Now(), time.Duration(cfg.RetryMaxSeconds)*time.Second)
	}
	if resp.StatusCode != http.StatusOK {
		return r
	}
	if len(resp.Header.Values(Header)) != 1 || len(ticket) != cfg.TargetLength {
		r.reason = "length_mismatch"
		return r
	}
	if !strings.HasPrefix(ticket, "gAAAAA") {
		r.reason = "prefix_mismatch"
		return r
	}
	for _, b := range []byte(ticket) {
		if b < 33 || b > 126 {
			r.reason = "header_invalid"
			return r
		}
	}
	r.ticket = ticket
	r.reason = "candidate_cached" // NOT a quality/success guarantee.
	return r
}
func retryAfter(s string, now time.Time, cap time.Duration) time.Duration {
	var d time.Duration
	if seconds, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		if seconds > int64(cap/time.Second) {
			return cap
		}
		d = time.Duration(seconds) * time.Second
	} else if at, err := http.ParseTime(s); err == nil {
		d = at.Sub(now)
	}
	if d < 0 {
		return 0
	}
	if d > cap {
		return cap
	}
	return d
}
func backoff(failures int, cfg Config, retry time.Duration) time.Duration {
	d := time.Duration(cfg.RetryBaseSeconds) * time.Second
	cap := time.Duration(cfg.RetryMaxSeconds) * time.Second
	for n := 1; n < failures && d < cap; n++ {
		d *= 2
	}
	if d > cap {
		d = cap
	}
	// Positive jitter prevents retrying before Retry-After or base delay.
	d += time.Duration(rand.Int64N(int64(d/4) + 1))
	if d < retry {
		d = retry
	}
	if d > cap {
		d = cap
	}
	return d
}
