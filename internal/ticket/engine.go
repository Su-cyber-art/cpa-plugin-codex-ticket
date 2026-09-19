package ticket

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type cacheKey struct{ index, identity, model string }
type entry struct {
	ticket                         string
	expiry, next                   time.Time
	lastHTTP, lastLength, failures int
	reason                         string
	injected                       uint64
	inflight                       bool
}

// Engine owns all memory state. Lifecycle operations stop AND join the old
// generation before starting a new one, including scans and HTTP probes.
type Engine struct {
	life             sync.Mutex
	mu               sync.Mutex
	host             Host
	cfg              Config
	generation       uint64
	cancel           context.CancelFunc
	done             chan struct{}
	wake             chan struct{}
	paused, quiesced bool
	cache            map[cacheKey]*entry
	identities       map[string]string
	active           map[string]struct{}
	reason           string
	endpoint         string
	proxySignature   string
	proxyTestBusy    bool
	lastProxyTest    time.Time
	proxyListMu      sync.Mutex
	proxyList        []pluginapi.HostAuthFileEntry
	proxyListUntil   time.Time
	// Test-only internal factory. Production always uses the mandatory proxy.
	clientFactory func(Config) (*http.Client, error)
}

func New(host Host) *Engine {
	return &Engine{host: host, cfg: DefaultConfig(), cache: map[cacheKey]*entry{}, identities: map[string]string{}, active: map[string]struct{}{}, wake: make(chan struct{}, 1), reason: "disabled", endpoint: upstreamEndpoint}
}
func probeBody(model string) []byte {
	b, _ := json.Marshal(map[string]any{"model": model, "store": false, "stream": true, "instructions": "pong", "input": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "ping"}}}}})
	return b
}
func (e *Engine) stopWorker() {
	e.mu.Lock()
	cancel, done := e.cancel, e.done
	e.cancel = nil
	e.done = nil
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}
func (e *Engine) startWorker() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.paused || e.quiesced {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	e.done = make(chan struct{})
	cfg, gen, done := e.cfg, e.generation, e.done
	go e.run(ctx, cfg, gen, done)
}
func (e *Engine) Configure(c Config, reason string) {
	e.life.Lock()
	defer e.life.Unlock()
	// Inhibit concurrent injection before joining the old worker.
	e.mu.Lock()
	e.quiesced = true
	e.generation++
	e.mu.Unlock()
	e.stopWorker()
	if c.validate() != nil {
		c = DefaultConfig()
		reason = "config_invalid"
	}
	e.mu.Lock()
	e.cfg = c
	e.quiesced = false
	e.cache = map[cacheKey]*entry{}
	e.identities = map[string]string{}
	e.reason = reason
	e.mu.Unlock()
	e.startWorker()
}
func (e *Engine) Quiesce() {
	e.life.Lock()
	defer e.life.Unlock()
	e.mu.Lock()
	e.quiesced = true
	e.generation++
	e.reason = "quiesced"
	e.mu.Unlock()
	e.stopWorker()
}
func (e *Engine) Shutdown() {
	e.Quiesce()
	e.mu.Lock()
	e.cache = map[cacheKey]*entry{}
	e.identities = map[string]string{}
	e.active = map[string]struct{}{}
	e.mu.Unlock()
}
func (e *Engine) Pause(paused bool) {
	e.life.Lock()
	defer e.life.Unlock()
	e.mu.Lock()
	e.paused = paused
	e.generation++
	e.mu.Unlock()
	e.stopWorker()
	e.startWorker()
}
func (e *Engine) Refresh() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}
func (e *Engine) snapshot() (Config, uint64, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg, e.generation, !e.paused && !e.quiesced
}
func (e *Engine) setReason(s string) { e.mu.Lock(); e.reason = s; e.mu.Unlock() }
func (e *Engine) run(ctx context.Context, cfg Config, gen uint64, done chan struct{}) {
	defer close(done)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var batchDone chan struct{}
	var cancelBatch context.CancelFunc
	defer func() {
		if cancelBatch != nil {
			cancelBatch()
		}
		if batchDone != nil {
			<-batchDone
		}
	}()
	next := time.Time{}
	for {
		g := hostGate(cfg)
		enabled := g.harvest
		reason := g.harvestReason
		if enabled {
			if _, err := e.getProxy(cfg); err != nil {
				enabled = false
				reason = err.Error()
			}
		}
		e.setReason(reason)
		if !enabled && cancelBatch != nil {
			cancelBatch()
		}
		if enabled && batchDone == nil && !time.Now().Before(next) {
			work, cancelWork := context.WithCancel(ctx)
			cancelBatch = cancelWork
			batchDone = make(chan struct{})
			go func(ch chan struct{}) {
				defer close(ch)
				defer cancelWork()
				e.scan(work, cfg, gen)
			}(batchDone)
			next = time.Now().Add(time.Duration(cfg.ScanIntervalSeconds) * time.Second)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-e.wake:
			next = time.Time{}
		case <-batchDone:
			batchDone = nil
			cancelBatch = nil
		}
	}
}
func (e *Engine) invalidate(index string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.identities, index)
	for k := range e.cache {
		if k.index == index {
			delete(e.cache, k)
		}
	}
}
func (e *Engine) bind(index, identity string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.identities[index] != identity {
		for k := range e.cache {
			if k.index == index {
				delete(e.cache, k)
			}
		}
		e.identities[index] = identity
	}
}
func (e *Engine) load(ctx context.Context, index string) (credential, string) {
	if ctx.Err() != nil || e.host == nil {
		return credential{}, "host_unavailable"
	}
	a, err := e.host.Runtime(ctx, index)
	if err != nil || a.AuthIndex != index || !eligible(a) {
		e.invalidate(index)
		return credential{}, "auth_inactive"
	}
	r, err := e.host.Get(ctx, index)
	if err != nil || r.AuthIndex != index {
		e.invalidate(index)
		return credential{}, "credential_unavailable"
	}
	c, err := parseCredential(r.JSON, a)
	if err != nil {
		e.invalidate(index)
		return credential{}, err.Error()
	}
	e.bind(index, c.identity)
	return c, ""
}
func (e *Engine) scan(ctx context.Context, cfg Config, gen uint64) {
	if e.host == nil || ctx.Err() != nil || !hostGate(cfg).harvest {
		return
	}
	list, err := e.host.List(ctx)
	if err != nil {
		e.setReason("host_unavailable")
		return
	}
	if len(list) > 10000 {
		e.setReason("auth_limit")
		return
	}
	valid := map[string]bool{}
	for _, a := range list {
		if eligible(a) {
			valid[a.AuthIndex] = true
		}
	}
	e.mu.Lock()
	for k := range e.cache {
		if !valid[k.index] {
			delete(e.cache, k)
		}
	}
	for index := range e.identities {
		if !valid[index] {
			delete(e.identities, index)
		}
	}
	e.mu.Unlock()
	type job struct{ index, model string }
	jobs := make(chan job)
	var wg sync.WaitGroup
	for i := 0; i < cfg.MaxConcurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				if ctx.Err() == nil {
					e.harvest(ctx, cfg, gen, j.index, j.model)
				}
			}
		}()
	}
	// Sorted dispatch gives deterministic coverage; due/backoff skips are cheap.
	indices := make([]string, 0, len(valid))
	for i := range valid {
		indices = append(indices, i)
	}
	sort.Strings(indices)
outer:
	for _, i := range indices {
		for _, m := range cfg.Models {
			select {
			case jobs <- job{i, m}:
			case <-ctx.Done():
				break outer
			}
		}
	}
	close(jobs)
	wg.Wait()
}
func (e *Engine) harvest(ctx context.Context, cfg Config, gen uint64, index, model string) {
	if ctx.Err() != nil || !hostGate(cfg).harvest {
		return
	}
	c, why := e.load(ctx, index)
	if why != "" {
		return
	}
	k := cacheKey{index, c.identity, model}
	now := time.Now()
	e.mu.Lock()
	if e.generation != gen || e.quiesced || e.paused {
		e.mu.Unlock()
		return
	}
	en := e.cache[k]
	if en == nil {
		if len(e.cache) >= 50000 {
			e.mu.Unlock()
			return
		}
		en = &entry{}
		e.cache[k] = en
	}
	if en.inflight || now.Before(en.next) || (en.ticket != "" && now.Add(time.Duration(cfg.RefreshBeforeSeconds)*time.Second).Before(en.expiry)) {
		e.mu.Unlock()
		return
	}
	en.inflight = true
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		if e.cache[k] == en {
			en.inflight = false
		}
		e.mu.Unlock()
	}()
	pctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutSeconds)*time.Second)
	defer cancel()
	var result probeResult
	u, err := e.getProxy(cfg)
	if err != nil {
		result.reason = err.Error()
	} else if !hostGate(cfg).harvest || pctx.Err() != nil {
		result.reason = "canceled"
	} else {
		var client *http.Client
		if e.clientFactory != nil {
			client, err = e.clientFactory(cfg)
		} else {
			client, err = makeClient(u, time.Duration(cfg.TimeoutSeconds)*time.Second)
		}
		if err != nil {
			result.reason = "proxy_invalid"
		} else {
			// Reload credentials immediately before the real network request.
			fresh, reason := e.load(pctx, index)
			if reason != "" || fresh.identity != c.identity {
				result.reason = "identity_changed"
			} else if !hostGate(cfg).harvest || pctx.Err() != nil {
				result.reason = "canceled"
			} else {
				result = probe(pctx, client, e.endpoint, fresh, model, cfg)
			}
			client.CloseIdleConnections()
		}
	}
	// Reject results belonging to a removed/rebound/disabled credential.
	if result.ticket != "" {
		fresh, reason := e.load(ctx, index)
		if reason != "" || fresh.identity != c.identity {
			return
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.generation != gen || e.cache[k] != en || e.identities[index] != c.identity || ctx.Err() != nil {
		return
	}
	en.lastHTTP = result.status
	en.lastLength = result.length
	en.reason = result.reason
	if result.ticket != "" && hostGate(cfg).harvest {
		en.ticket = result.ticket
		en.expiry = time.Now().Add(time.Duration(cfg.TTLSeconds) * time.Second)
		en.next = time.Time{}
		en.failures = 0
	} else {
		en.failures++
		en.next = time.Now().Add(backoff(en.failures, cfg, result.retryAfter))
	}
}
func hasHeader(h http.Header, key string) bool {
	for k := range h {
		if strings.EqualFold(k, key) {
			return true
		}
	}
	return false
}
func websocket(h http.Header, m map[string]any) bool {
	if hasHeader(h, "Upgrade") || hasHeader(h, "Sec-WebSocket-Key") {
		return true
	}
	for _, k := range []string{"execution_session_id", "execution_session"} {
		if v, ok := m[k]; ok && v != nil && v != "" {
			return true
		}
	}
	return false
}
func (e *Engine) Intercept(req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	pass := pluginapi.RequestInterceptResponse{}
	cfg, gen, running := e.snapshot()
	if !running || req.RequestID == "" || req.ToFormat != "codex" || !cfg.allows(req.Model) || websocket(req.Headers, req.Metadata) {
		return pass
	}
	if hasHeader(req.Headers, Header) && !cfg.ReplaceExisting {
		return pass
	}
	g := hostGate(cfg)
	if !g.inject {
		return pass
	}
	if _, err := e.getProxy(cfg); err != nil {
		return pass
	}
	index, _ := req.Metadata["selected_auth_index"].(string)
	if index == "" {
		return pass
	}
	// The host callback API is synchronous; it does not accept a cancellation
	// token. Timeout applies around the Go interface but cannot interrupt C.
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.TimeoutSeconds)*time.Second)
	defer cancel()
	c, reason := e.load(ctx, index)
	if reason != "" {
		return pass
	}
	if !hostGate(cfg).inject {
		return pass
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.generation != gen || e.paused || e.quiesced || len(e.active) >= 100000 || e.identities[index] != c.identity {
		return pass
	}
	en := e.cache[cacheKey{index, c.identity, req.Model}]
	if en == nil || en.ticket == "" || !time.Now().Before(en.expiry) {
		return pass
	}
	en.injected++
	e.active[req.RequestID] = struct{}{}
	return pluginapi.RequestInterceptResponse{Headers: http.Header{Header: []string{en.ticket}}, ClearHeaders: []string{Header}}
}

// ClearTicketHeaders is only called for HTTP response or SSE header-init, never
// WebSocket. Track ownership by host request id, not a client-forgeable header.
func (e *Engine) ClearTicketHeaders(id string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.active[id]; ok {
		return []string{Header}
	}
	return nil
}
func (e *Engine) Complete(id string) { e.mu.Lock(); delete(e.active, id); e.mu.Unlock() }

type EntryStatus struct {
	AuthIndex        string     `json:"auth_index"`
	Model            string     `json:"model"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
	RemainingSeconds int64      `json:"remaining_seconds"`
	LastHTTP         int        `json:"last_http"`
	LastLength       int        `json:"last_length"`
	Reason           string     `json:"reason"`
	BackoffSeconds   int64      `json:"backoff_seconds"`
	InjectedCount    uint64     `json:"injected_count"`
	Inflight         bool       `json:"inflight"`
}
type Status struct {
	Experimental      bool          `json:"experimental"`
	QualityGuaranteed bool          `json:"quality_guaranteed"`
	HarvestActive     bool          `json:"harvest_active"`
	InjectActive      bool          `json:"inject_active"`
	HarvestReason     string        `json:"harvest_reason"`
	InjectReason      string        `json:"inject_reason"`
	WorkerReason      string        `json:"worker_reason"`
	Paused            bool          `json:"paused"`
	Entries           []EntryStatus `json:"entries"`
	CachedCount       int           `json:"cached_count"`
	InflightCount     int           `json:"inflight_count"`
	TrackedRequests   int           `json:"tracked_requests"`
}

func (e *Engine) Status() Status {
	cfg, _, running := e.snapshot()
	g := hostGate(cfg)
	if _, err := e.getProxy(cfg); err != nil {
		g = gate{harvestReason: err.Error(), injectReason: err.Error()}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	s := Status{Experimental: true, HarvestActive: g.harvest && running, InjectActive: g.inject && running, HarvestReason: g.harvestReason, InjectReason: g.injectReason, WorkerReason: e.reason, Paused: !running, Entries: []EntryStatus{}, TrackedRequests: len(e.active)}
	now := time.Now()
	for k, en := range e.cache {
		row := EntryStatus{AuthIndex: k.index, Model: k.model, LastHTTP: en.lastHTTP, LastLength: en.lastLength, Reason: en.reason, InjectedCount: en.injected, Inflight: en.inflight}
		if en.inflight {
			s.InflightCount++
		}
		if !en.expiry.IsZero() {
			t := en.expiry
			row.ExpiresAt = &t
			row.RemainingSeconds = max(0, int64(en.expiry.Sub(now)/time.Second))
			if en.ticket != "" && now.Before(en.expiry) {
				s.CachedCount++
			}
		}
		row.BackoffSeconds = max(0, int64(en.next.Sub(now)/time.Second))
		s.Entries = append(s.Entries, row)
	}
	sort.Slice(s.Entries, func(i, j int) bool {
		a, b := s.Entries[i], s.Entries[j]
		if a.AuthIndex == b.AuthIndex {
			return a.Model < b.Model
		}
		return a.AuthIndex < b.AuthIndex
	})
	return s
}
