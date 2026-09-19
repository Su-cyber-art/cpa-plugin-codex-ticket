package ticket

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

type proxySelection struct {
	Mode string `json:"mode"`
	URL  string `json:"url,omitempty"`
	Ref  string `json:"ref,omitempty"`
}
type ProxyOption struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Source  string `json:"source"`
	Scheme  string `json:"scheme"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
	HasAuth bool   `json:"has_auth"`
}
type ProxyCurrent struct {
	Mode       string `json:"mode"`
	Ref        string `json:"ref,omitempty"`
	Configured bool   `json:"configured"`
	Label      string `json:"label"`
	Reason     string `json:"reason"`
}
type ProxySettings struct {
	Current         ProxyCurrent  `json:"current"`
	Options         []ProxyOption `json:"options"`
	DiscoveryReason string        `json:"discovery_reason,omitempty"`
}
type proxyCandidate struct {
	safe ProxyOption
	url  *url.URL
}

func readSelection(path string) (proxySelection, error) {
	b, err := readBounded(path, 8192, true)
	if err != nil {
		return proxySelection{}, errors.New("proxy_unavailable")
	}
	s := strings.TrimSpace(string(b))
	if strings.HasPrefix(s, "{") {
		var p proxySelection
		if json.Unmarshal(b, &p) != nil {
			return p, errors.New("proxy_invalid")
		}
		if p.Mode == "core" && p.Ref != "" && p.URL == "" {
			return p, nil
		}
		if p.Mode == "custom" && p.Ref == "" {
			if _, err := parseProxy([]byte(p.URL)); err == nil {
				return p, nil
			}
		}
		return p, errors.New("proxy_invalid")
	}
	if _, err := parseProxy(b); err != nil {
		return proxySelection{}, err
	}
	return proxySelection{Mode: "custom", URL: s}, nil
}
func redactedProxy(u *url.URL) string {
	s := u.Scheme + "://" + u.Host
	if u.User != nil {
		s += "（已配置认证）"
	}
	return s
}
func option(id, source, title string, u *url.URL) ProxyOption {
	port, _ := strconv.Atoi(u.Port())
	if port == 0 {
		switch u.Scheme {
		case "https":
			port = 443
		case "http":
			port = 80
		default:
			port = 1080
		}
	}
	return ProxyOption{ID: id, Label: title + " · " + redactedProxy(u), Source: source, Scheme: u.Scheme, Host: u.Hostname(), Port: port, HasAuth: u.User != nil}
}
func configProxyID(path []string) string {
	b, _ := json.Marshal(path)
	sum := sha256.Sum256(b)
	return "core:config:" + hex.EncodeToString(sum[:12])
}

// Array entries are identified by non-proxy content, never their position.
func entryIdentity(v any) string {
	var strip func(any) any
	strip = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			out := map[string]any{}
			for k, val := range x {
				if k != "proxy-url" && k != "proxy_url" {
					out[k] = strip(val)
				}
			}
			return out
		case []any:
			out := make([]any, len(x))
			for i, val := range x {
				out[i] = strip(val)
			}
			return out
		default:
			return v
		}
	}
	b, err := json.Marshal(strip(v))
	if err != nil || string(b) == "{}" || string(b) == "null" {
		return ""
	}
	h := sha256.Sum256(b)
	return "entry-" + hex.EncodeToString(h[:16])
}
func (e *Engine) proxyAuthList(ctx context.Context) ([]pluginapi.HostAuthFileEntry, error) {
	e.proxyListMu.Lock()
	defer e.proxyListMu.Unlock()
	if time.Now().Before(e.proxyListUntil) {
		return e.proxyList, nil
	}
	if e.host == nil {
		return nil, errors.New("host_unavailable")
	}
	list, err := e.host.List(ctx)
	if err != nil {
		return nil, errors.New("proxy_discovery_partial")
	}
	e.proxyList = list
	e.proxyListUntil = time.Now().Add(2 * time.Second)
	return list, nil
}
func (e *Engine) proxyAuthJSON(ctx context.Context, a pluginapi.HostAuthFileEntry) ([]byte, error) {
	// Host-auth Get scans all credentials internally. Read the trusted host path
	// directly with size/type/symlink checks instead of repeated whole-list clones.
	if _, ok := e.host.(CallbackHost); ok {
		return readBounded(a.Path, maxCredentialBytes, false)
	}
	r, err := e.host.Get(ctx, a.AuthIndex)
	if err != nil {
		return nil, err
	}
	return r.JSON, nil
}

// coreProxies returns no credential-bearing URLs to external callers.
// References use entry identities or auth indexes, never shifting positions.
func (e *Engine) coreProxies(ctx context.Context, c Config, includeAccounts bool) ([]proxyCandidate, error) {
	b, err := readBounded(c.HostConfigFile, 4<<20, false)
	if err != nil {
		return nil, errors.New("host_config_unavailable")
	}
	var root any
	if yaml.Unmarshal(b, &root) != nil {
		return nil, errors.New("host_config_invalid")
	}
	out := []proxyCandidate{}
	var walk func(any, []string)
	walk = func(v any, path []string) {
		if len(out) >= 256 || len(path) > 12 {
			return
		}
		switch x := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if len(path) == 0 && (k == "plugins" || k == "remote-management") {
					continue
				}
				p := append(append([]string{}, path...), k)
				if k == "proxy-url" || k == "proxy_url" {
					s, _ := x[k].(string)
					u, err := parseProxy([]byte(s))
					if err == nil {
						id, title, source := configProxyID(p), "核心配置 "+strings.Join(p, " / "), "config"
						if len(p) == 1 && k == "proxy-url" {
							id, title, source = "core:global", "CPA 全局代理", "global"
						}
						out = append(out, proxyCandidate{option(id, source, title, u), u})
					}
				} else {
					walk(x[k], p)
				}
			}
		case []any:
			ids := map[string]int{}
			for _, item := range x {
				if id := entryIdentity(item); id != "" {
					ids[id]++
				}
			}
			for _, item := range x {
				id := entryIdentity(item)
				if id == "" || ids[id] != 1 {
					continue
				}
				walk(item, append(append([]string{}, path...), id))
			}
		}
	}
	walk(root, nil)
	var discoveryErr error
	if includeAccounts && e.host != nil {
		list, err := e.proxyAuthList(ctx)
		if err != nil {
			discoveryErr = errors.New("proxy_discovery_partial")
		}
		if err == nil {
			for i, a := range list {
				if ctx.Err() != nil || i >= 256 || len(out) >= 512 {
					discoveryErr = errors.New("proxy_discovery_partial")
					break
				}
				if a.Disabled || a.RuntimeOnly || a.AuthIndex == "" || a.Size > maxCredentialBytes {
					continue
				}
				raw, err := e.proxyAuthJSON(ctx, a)
				if err != nil || len(raw) > maxCredentialBytes {
					continue
				}
				var obj map[string]any
				if json.Unmarshal(raw, &obj) != nil {
					continue
				}
				s := text(obj, "proxy_url")
				if s == "" {
					s = text(obj, "proxy-url")
				}
				u, err := parseProxy([]byte(s))
				if err != nil {
					continue
				}
				id := "core:auth:" + a.AuthIndex
				out = append(out, proxyCandidate{option(id, "account", "账号 "+a.AuthIndex+" 专用代理", u), u})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].safe.ID < out[j].safe.ID })
	return out, discoveryErr
}
func (e *Engine) resolveProxy(c Config) (*url.URL, error) {
	p, err := readSelection(c.ProxyFile)
	if err != nil {
		return nil, err
	}
	return e.resolveSelection(c, p)
}
func (e *Engine) resolveSelection(c Config, p proxySelection) (*url.URL, error) {
	if p.Mode == "custom" {
		return parseProxy([]byte(p.URL))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if strings.HasPrefix(p.Ref, "core:auth:") {
		index := strings.TrimPrefix(p.Ref, "core:auth:")
		if e.host == nil {
			return nil, errors.New("selected_proxy_unavailable")
		}
		list, err := e.proxyAuthList(ctx)
		var a pluginapi.HostAuthFileEntry
		for _, v := range list {
			if v.AuthIndex == index {
				a = v
				break
			}
		}
		if err != nil || a.AuthIndex != index || a.Disabled || a.RuntimeOnly || a.Size > maxCredentialBytes {
			return nil, errors.New("selected_proxy_unavailable")
		}
		raw, err := e.proxyAuthJSON(ctx, a)
		if err != nil || len(raw) > maxCredentialBytes {
			return nil, errors.New("selected_proxy_unavailable")
		}
		var obj map[string]any
		if json.Unmarshal(raw, &obj) != nil {
			return nil, errors.New("selected_proxy_unavailable")
		}
		if disabled, _ := obj["disabled"].(bool); disabled {
			return nil, errors.New("selected_proxy_unavailable")
		}
		s := text(obj, "proxy_url")
		if s == "" {
			s = text(obj, "proxy-url")
		}
		u, err := parseProxy([]byte(s))
		if err != nil {
			return nil, errors.New("selected_proxy_unavailable")
		}
		return u, nil
	}
	options, err := e.coreProxies(ctx, c, false)
	if err != nil {
		return nil, err
	}
	for _, o := range options {
		if o.safe.ID == p.Ref {
			return o.url, nil
		}
	}
	return nil, errors.New("selected_proxy_unavailable")
}
func (e *Engine) getProxy(c Config) (*url.URL, error) {
	u, err := e.resolveProxy(c)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(u.String()))
	sig := hex.EncodeToString(sum[:])
	e.mu.Lock()
	if e.proxySignature != "" && e.proxySignature != sig {
		e.cache = map[cacheKey]*entry{}
	}
	e.proxySignature = sig
	e.mu.Unlock()
	return u, nil
}
func (e *Engine) ProxySettings() ProxySettings {
	c, _, _ := e.snapshot()
	result := ProxySettings{Current: ProxyCurrent{Mode: "custom", Reason: "proxy_unavailable"}, Options: []ProxyOption{}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	candidates, err := e.coreProxies(ctx, c, true)
	for _, o := range candidates {
		result.Options = append(result.Options, o.safe)
	}
	if err != nil {
		result.DiscoveryReason = "proxy_discovery_partial"
	}
	p, err := readSelection(c.ProxyFile)
	if err != nil {
		return result
	}
	result.Current.Mode = p.Mode
	result.Current.Ref = p.Ref
	u, err := e.resolveSelection(c, p)
	if err != nil {
		result.Current.Reason = err.Error()
		return result
	}
	result.Current.Configured = true
	result.Current.Label = redactedProxy(u)
	result.Current.Reason = "configured"
	return result
}

// Save only in the existing private directory. Reject symlinks and unsafe files;
// commit by rename+fsync, never touch CPA global/account proxy configuration.
func atomicSelection(path string, p proxySelection) error {
	dir := filepath.Dir(path)
	d, err := os.Lstat(dir)
	if err != nil || !d.IsDir() || d.Mode()&os.ModeSymlink != 0 {
		return errors.New("proxy_storage_unavailable")
	}
	st, ok := d.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Geteuid() || d.Mode().Perm()&0077 != 0 {
		return errors.New("proxy_storage_permissions")
	}
	if _, err := os.Lstat(path); err == nil {
		if _, err := readBounded(path, 8192, true); err != nil {
			return errors.New("proxy_storage_permissions")
		}
	} else if !os.IsNotExist(err) {
		return errors.New("proxy_storage_unavailable")
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return errors.New("proxy_invalid")
	}
	f, err := os.CreateTemp(dir, ".proxy-selection-*")
	if err != nil {
		return errors.New("proxy_storage_unavailable")
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if f.Chmod(0600) != nil {
		f.Close()
		return errors.New("proxy_storage_unavailable")
	}
	if _, err = f.Write(append(raw, '\n')); err == nil {
		err = f.Sync()
	}
	cerr := f.Close()
	if err != nil || cerr != nil {
		return errors.New("proxy_storage_unavailable")
	}
	if os.Rename(tmp, path) != nil {
		return errors.New("proxy_storage_unavailable")
	}
	fd, err := os.Open(dir)
	if err != nil {
		return errors.New("proxy_committed_durability_uncertain")
	}
	err = fd.Sync()
	cerr = fd.Close()
	if err != nil || cerr != nil {
		return errors.New("proxy_committed_durability_uncertain")
	}
	return nil
}
func (e *Engine) SaveProxy(raw []byte) error {
	if len(raw) > 8192 {
		return errors.New("proxy_invalid")
	}
	var p proxySelection
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if dec.Decode(&p) != nil {
		return errors.New("proxy_invalid")
	}
	var tail any
	if dec.Decode(&tail) != io.EOF {
		return errors.New("proxy_invalid")
	}
	e.life.Lock()
	defer e.life.Unlock()
	c, _, _ := e.snapshot()
	switch p.Mode {
	case "custom":
		if p.Ref != "" {
			return errors.New("proxy_invalid")
		}
		if p.URL == "" {
			old, err := readSelection(c.ProxyFile)
			if err != nil || old.Mode != "custom" {
				return errors.New("proxy_required")
			}
			return nil
		}
		u, err := parseProxy([]byte(p.URL))
		if err != nil {
			return err
		}
		p.URL = u.String()
	case "core":
		if p.URL != "" || p.Ref == "" || len(p.Ref) > 128 {
			return errors.New("proxy_invalid")
		}
		if _, err := e.resolveSelection(c, p); err != nil {
			return err
		}
	default:
		return errors.New("proxy_invalid")
	}
	e.mu.Lock()
	e.quiesced = true
	e.generation++
	e.mu.Unlock()
	e.stopWorker()
	err := atomicSelection(c.ProxyFile, p)
	e.mu.Lock()
	if err == nil || err.Error() == "proxy_committed_durability_uncertain" {
		e.cache = map[cacheKey]*entry{}
		e.proxySignature = ""
	}
	e.quiesced = false
	e.mu.Unlock()
	e.startWorker()
	return err
}

type ProxyTestResult struct {
	OK         bool   `json:"ok"`
	HTTPStatus int    `json:"http_status"`
	LatencyMS  int64  `json:"latency_ms"`
	Reason     string `json:"reason"`
}

func (e *Engine) TestProxy() ProxyTestResult {
	e.mu.Lock()
	if e.proxyTestBusy || time.Since(e.lastProxyTest) < 5*time.Second {
		e.mu.Unlock()
		return ProxyTestResult{Reason: "test_rate_limited"}
	}
	e.proxyTestBusy = true
	e.lastProxyTest = time.Now()
	e.mu.Unlock()
	defer func() { e.mu.Lock(); e.proxyTestBusy = false; e.mu.Unlock() }()
	cfg, _, _ := e.snapshot()
	u, err := e.resolveProxy(cfg)
	if err != nil {
		return ProxyTestResult{Reason: err.Error()}
	}
	client, err := makeClient(u, 10*time.Second)
	if err != nil {
		return ProxyTestResult{Reason: "proxy_invalid"}
	}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", upstreamEndpoint, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Close = true
	start := time.Now()
	resp, err := client.Do(req)
	result := ProxyTestResult{LatencyMS: time.Since(start).Milliseconds(), Reason: "transport_error"}
	if err != nil {
		return result
	}
	resp.Body.Close()
	result.HTTPStatus = resp.StatusCode
	result.OK = resp.StatusCode >= 200 && resp.StatusCode < 500 && resp.StatusCode != 407
	result.Reason = "reachable"
	if !result.OK {
		result.Reason = fmt.Sprintf("http_%d", resp.StatusCode)
	}
	return result
}
