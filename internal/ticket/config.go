// Package ticket implements an experimental memory-only Codex ticket cache.
package ticket

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"gopkg.in/yaml.v3"
)

const Name = "codex-ticket"
const Header = "X-Codex-Turn-State"
const maxCredentialBytes = 1 << 20

// Models is a comma-separated string (also accepts a YAML list for hand editing).
type Models []string

func (m *Models) UnmarshalYAML(n *yaml.Node) error {
	var list []string
	if n.Kind == yaml.ScalarNode {
		list = strings.Split(n.Value, ",")
	} else if err := n.Decode(&list); err != nil {
		return errors.New("config_invalid")
	}
	*m = nil
	for _, s := range list {
		s = strings.TrimSpace(s)
		if s != "" {
			*m = append(*m, s)
		}
	}
	return nil
}

type Config struct {
	Enabled              bool   `yaml:"enabled"`
	HarvestEnabled       bool   `yaml:"harvest_enabled"`
	InjectEnabled        bool   `yaml:"inject_enabled"`
	ProxyFile            string `yaml:"proxy_file"`
	HostConfigFile       string `yaml:"host_config_file"`
	Models               Models `yaml:"models"`
	TargetLength         int    `yaml:"target_length"`
	TTLSeconds           int    `yaml:"ttl_seconds"`
	RefreshBeforeSeconds int    `yaml:"refresh_before_seconds"`
	ScanIntervalSeconds  int    `yaml:"scan_interval_seconds"`
	TimeoutSeconds       int    `yaml:"timeout_seconds"`
	MaxConcurrency       int    `yaml:"max_concurrency"`
	RetryBaseSeconds     int    `yaml:"retry_base_seconds"`
	RetryMaxSeconds      int    `yaml:"retry_max_seconds"`
	ReplaceExisting      bool   `yaml:"replace_existing"`
}

func DefaultConfig() Config {
	return Config{Models: Models{"gpt-6-astra", "gpt-5.6-sol"}, TargetLength: 292, TTLSeconds: 3600, RefreshBeforeSeconds: 600, ScanIntervalSeconds: 30, TimeoutSeconds: 25, MaxConcurrency: 2, RetryBaseSeconds: 60, RetryMaxSeconds: 1800}
}
func ParseConfig(raw []byte) (Config, error) {
	c := DefaultConfig()
	if len(raw) > 65536 || yaml.Unmarshal(raw, &c) != nil || c.validate() != nil {
		return DefaultConfig(), errors.New("config_invalid")
	}
	return c, nil
}
func (c Config) validate() error {
	if c.TargetLength < 6 || c.TargetLength > 8192 || c.TTLSeconds < 1 || c.TTLSeconds > 86400 || c.RefreshBeforeSeconds < 0 || c.RefreshBeforeSeconds >= c.TTLSeconds || c.ScanIntervalSeconds < 1 || c.ScanIntervalSeconds > 3600 || c.TimeoutSeconds < 1 || c.TimeoutSeconds > 120 || c.MaxConcurrency < 1 || c.MaxConcurrency > 16 || c.RetryBaseSeconds < 1 || c.RetryMaxSeconds < c.RetryBaseSeconds || c.RetryMaxSeconds > 86400 || len(c.Models) == 0 || len(c.Models) > 32 {
		return errors.New("config_invalid")
	}
	seen := map[string]bool{}
	for _, m := range c.Models {
		if len(m) > 128 || m == "" || strings.ContainsAny(m, " \r\n\t\x00") || seen[m] {
			return errors.New("config_invalid")
		}
		seen[m] = true
	}
	// Relative paths are intentionally rejected to avoid dependence on host cwd.
	if c.ProxyFile != "" && !filepath.IsAbs(c.ProxyFile) || c.HostConfigFile != "" && !filepath.IsAbs(c.HostConfigFile) {
		return errors.New("config_invalid")
	}
	return nil
}
func (c Config) allows(model string) bool {
	for _, m := range c.Models {
		if model == m {
			return true
		}
	}
	return false
}

// readBounded does not follow a final symlink and rejects devices/FIFOs before
// reading. Private files must be owned by this uid and exactly mode 0600.
func readBounded(path string, max int64, private bool) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("file_unavailable")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("file_unavailable")
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > max {
		return nil, errors.New("file_invalid")
	}
	if private {
		s, ok := st.Sys().(*syscall.Stat_t)
		if !ok || int(s.Uid) != os.Geteuid() || st.Mode().Perm() != 0600 || st.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return nil, errors.New("file_permissions")
		}
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil || int64(len(b)) > max {
		return nil, errors.New("file_invalid")
	}
	return b, nil
}

type gate struct {
	harvest, inject             bool
	harvestReason, injectReason string
}

// Re-read host YAML on every probe and injection, not only reconfigure. CPA can
// remove capabilities without sending a lifecycle callback when disabled.
func hostGate(c Config) gate {
	deny := func(s string) gate { return gate{harvestReason: s, injectReason: s} }
	if !c.Enabled {
		return deny("disabled")
	}
	b, err := readBounded(c.HostConfigFile, 4<<20, false)
	if err != nil {
		return deny("host_config_unavailable")
	}
	var h struct {
		Plugins struct {
			Enabled bool `yaml:"enabled"`
			Configs map[string]struct {
				Enabled bool `yaml:"enabled"`
				Harvest bool `yaml:"harvest_enabled"`
				Inject  bool `yaml:"inject_enabled"`
			} `yaml:"configs"`
		} `yaml:"plugins"`
		RequestLog     bool `yaml:"request-log"`
		CommercialMode bool `yaml:"commercial-mode"`
	}
	if yaml.NewDecoder(bytes.NewReader(b)).Decode(&h) != nil {
		return deny("host_config_invalid")
	}
	p, ok := h.Plugins.Configs[Name]
	if !ok || !h.Plugins.Enabled || !p.Enabled {
		return deny("host_disabled")
	}
	g := gate{harvest: c.HarvestEnabled && p.Harvest, inject: c.InjectEnabled && p.Inject, harvestReason: "harvest_disabled", injectReason: "inject_disabled"}
	if g.harvest {
		g.harvestReason = "ready"
	}
	if g.inject {
		g.injectReason = "ready"
	}
	// CPA retains deferred upstream headers for error-only logs even when
	// request-log is false. Only startup commercial-mode disables both paths.
	if !h.CommercialMode {
		g.inject = false
		g.injectReason = "log_unsafe"
	}
	return g
}
