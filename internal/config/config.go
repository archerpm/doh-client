// Package config holds the persistent configuration of the DoH client.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Mode selects how the system is pointed at the resolver.
type Mode string

const (
	ModeProxy Mode = "proxy" // local DNS server + system DNS -> 127.0.0.1
	ModeHosts Mode = "hosts" // resolve rule domains once and write them to the hosts file
)

// Provider is one DoH upstream.
type Provider struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	URL       string   `json:"url"`                 // https://host/dns-query
	Bootstrap []string `json:"bootstrap,omitempty"` // IPs of the DoH host (avoid resolving it through ourselves)
	Enabled   bool     `json:"enabled"`
	Builtin   bool     `json:"builtin"`
	Geo       bool     `json:"geo"` // provider returns proxy IPs for geo-blocked services
	Note      string   `json:"note,omitempty"`
	UseGET    bool     `json:"use_get,omitempty"`
	// Services names what the provider itself says it unblocks (informational).
	Services []string `json:"services,omitempty"`
	// Tested lists the services whose hosts really resolved to this provider's
	// proxy addresses in our own test run on TestedAt (see tools/probesupport).
	Tested   []string `json:"tested,omitempty"`
	TestedAt string   `json:"tested_at,omitempty"`
	// Lists are domain lists the provider itself publishes. Only built-in providers
	// have them: Normalize drops them from custom ones, so a saved config can never
	// make the service fetch an arbitrary URL.
	Lists []ProviderList `json:"lists,omitempty"`
}

// ProviderList is one published list of the domains a provider proxies.
type ProviderList struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"` // https, plain text: one domain per line (hosts-file lines are accepted too)
}

// Rule routes a domain (and its subdomains) to a specific provider chain.
type Rule struct {
	Domain    string   `json:"domain"`    // "claude.ai" matches claude.ai and *.claude.ai
	Providers []string `json:"providers"` // provider IDs tried in order; empty = default chain
	Enabled   bool     `json:"enabled"`
}

// Config is the full persistent state.
type Config struct {
	Enabled          bool       `json:"enabled"` // proxy active (persisted across reboots)
	Mode             Mode       `json:"mode"`
	Listen           []string   `json:"listen"`          // e.g. 127.0.0.1:53
	ActiveProvider   string     `json:"active_provider"` // the single DoH provider in use, chosen by the user
	DefaultChain     []string   `json:"default_chain"`   // always [ActiveProvider]; kept for config compatibility
	BootstrapDNS     []string   `json:"bootstrap_dns"`   // plain DNS for DoH bootstrap and diagnostic references
	Providers        []Provider `json:"providers"`
	Rules            []Rule     `json:"rules"`
	BlockAAAAForRule bool       `json:"block_aaaa_for_rules"` // suppress IPv6 for rule domains (IPv6 leaks bypass the proxy)
	BlockHTTPSRR     bool       `json:"block_https_rr_for_rules"`
	CacheSize        int        `json:"cache_size"`
	MinTTL           uint32     `json:"min_ttl"`
	MaxTTL           uint32     `json:"max_ttl"`
	TimeoutMS        int        `json:"timeout_ms"`
	HealthIntervalS  int        `json:"health_interval_s"`
	LogSize          int        `json:"log_size"`
}

// Timeout returns the per-upstream timeout.
func (c *Config) Timeout() time.Duration { return time.Duration(c.TimeoutMS) * time.Millisecond }

// BuiltinProviders are the known providers. Verified 2026-10-13; the support
// results come from the last run of tools/probesupport.
func BuiltinProviders() []Provider {
	ps := builtinProviders()
	for i := range ps {
		if t, ok := testedSupport[ps[i].ID]; ok {
			ps[i].Tested = append([]string(nil), t...)
			ps[i].TestedAt = SupportTestedAt
		}
	}
	return ps
}

func builtinProviders() []Provider {
	return []Provider{
		{ID: "comss", Name: "Comss.one", URL: "https://dns.comss.one/dns-query",
			Bootstrap: []string{"195.133.25.16", "83.220.169.155", "212.109.195.93"}, Enabled: true, Builtin: true, Geo: true,
			Note: "AI-сервисы, обновления Windows, игры; фильтрация рекламы",
			// comss.ru/page.php?id=7315; the provider publishes no domain list
			Services: []string{"ChatGPT и Sora", "Microsoft Copilot", "GitHub Copilot", "xAI Grok", "Claude", "Gemini", "Brawl Stars", "Doom Eternal"}},
		{ID: "geohide", Name: "GeoHide", URL: "https://dns.geohide.ru/dns-query",
			Bootstrap: []string{"37.230.192.51"}, Enabled: true, Builtin: true, Geo: true,
			Note: "Notion, Canva, Autodesk, ChatGPT, Grok и др.",
			// github.com/Internet-Helper/GeoHideDNS publishes its lists (updated daily)
			Services: []string{"AI: ChatGPT, Claude, Gemini, Grok, NotebookLM, Manus, Meta AI", "AI-разработка: Cursor, Windsurf, Trae, Groq, OpenRouter",
				"Работа: Notion, Canva, Autodesk, Adobe, Atlassian, Slack, Zapier", "Игры: все игры Supercell", "Музыка и видео: Spotify, Crunchyroll"},
			Lists: []ProviderList{
				{ID: "google-ai", Name: "Google AI", URL: "https://raw.githubusercontent.com/Internet-Helper/GeoHideDNS/main/lists/domains-google.txt"},
				{ID: "services", Name: "Остальные сервисы", URL: "https://raw.githubusercontent.com/Internet-Helper/GeoHideDNS/main/lists/domains.txt"},
				{ID: "supercell", Name: "Supercell", URL: "https://raw.githubusercontent.com/Internet-Helper/GeoHideDNS/main/lists/supercell.txt"},
			}},
		{ID: "malw", Name: "dns.malw.link", URL: "https://dns.malw.link/dns-query",
			Bootstrap: []string{"179.254.166.171", "164.5.224.81"}, Enabled: true, Builtin: true, Geo: true,
			Note:     "Широкий список: AI, Spotify, Twitch, Xbox, JetBrains…; только HTTP/2",
			Services: []string{"AI-сервисы", "Spotify", "Twitch", "Xbox", "JetBrains", "Linear, Patreon"}},
		{ID: "malw-cf", Name: "dns.malw.link (Cloudflare)", URL: "https://5u35p8m9i7.cloudflare-gateway.com/dns-query",
			Bootstrap: []string{"162.159.36.5", "162.159.36.20"}, Enabled: true, Builtin: true, Geo: true,
			Note:     "Тот же malw.link через Cloudflare Gateway; некоторые провайдеры блокируют CF",
			Services: []string{"AI-сервисы", "Spotify", "Twitch", "Xbox", "JetBrains", "Linear, Patreon"}},
		{ID: "dnsai", Name: "DNS-AI", URL: "https://dns.dns-ai.ru/dns-query",
			Bootstrap: []string{"192.144.59.14", "186.246.49.127"}, Enabled: true, Builtin: true, Geo: true,
			Note: "Только AI-сервисы; работает только для IP из РФ/РБ",
			// dns-ai.ru: the AI list changes with the vendors' addresses and is deliberately not published
			Services: []string{"AI-сервисы (Anthropic, ChatGPT и др.); список не публикуется"}},
		{ID: "mafioznik", Name: "Mafioznik", URL: "https://dns.mafioznik.xyz/dns-query",
			Bootstrap: []string{"103.27.157.38", "103.27.157.100"}, Enabled: false, Builtin: true, Geo: true,
			Note:     "Только HTTP/2; ответы нестабильны: часть доменов (chatgpt.com, notion.so) отдаёт 0.0.0.0",
			Services: []string{"xAI Grok", "Linear (частично)"}},
		{ID: "bezmezhau", Name: "Bezmezhau", URL: "https://dns.bezmezhau.com/dns-query",
			Bootstrap: []string{"143.20.64.55"}, Enabled: false, Builtin: true, Geo: true,
			Note: "ChatGPT, Spotify, YouTube, Netflix и др.; сервис для Беларуси (bezmezhau.com)",
			// bezmezhau.com/supported-services
			Services: []string{"ChatGPT", "Claude", "Gemini", "Brawl Stars", "Spotify", "Netflix", "YouTube", "Instagram и Facebook", "Discord", "Twitter / X", "TikTok", "Reddit"}},
		{ID: "cloudflare", Name: "Cloudflare", URL: "https://cloudflare-dns.com/dns-query",
			Bootstrap: []string{"1.1.1.1", "1.0.0.1"}, Enabled: true, Builtin: true,
			Note: "Обычный DoH без обхода геоблокировок"},
		{ID: "google", Name: "Google", URL: "https://dns.google/dns-query",
			Bootstrap: []string{"8.8.8.8", "8.8.4.4"}, Enabled: true, Builtin: true,
			Note: "Обычный DoH без обхода геоблокировок"},
	}
}

// Default returns a sensible starting configuration.
func Default() *Config {
	return &Config{
		Mode:             ModeProxy,
		Listen:           []string{"127.0.0.1:53", "[::1]:53"},
		DefaultChain:     []string{"comss", "malw", "geohide", "cloudflare"},
		BootstrapDNS:     []string{"77.88.8.8:53", "1.1.1.1:53", "8.8.8.8:53"},
		Providers:        BuiltinProviders(),
		Rules:            nil,
		BlockAAAAForRule: true,
		BlockHTTPSRR:     true,
		CacheSize:        4096,
		MinTTL:           30,
		MaxTTL:           3600,
		TimeoutMS:        3000,
		HealthIntervalS:  60,
		LogSize:          1000,
	}
}

// Normalize fills defaults, merges new builtin providers and validates.
func (c *Config) Normalize() error {
	d := Default()
	if c.Mode == "" {
		c.Mode = ModeProxy
	}
	if c.Mode != ModeProxy && c.Mode != ModeHosts {
		return fmt.Errorf("unknown mode %q", c.Mode)
	}
	if len(c.Listen) == 0 {
		c.Listen = d.Listen
	}
	if c.Rules == nil {
		c.Rules = []Rule{} // JSON "[]" not null: the UI iterates it directly
	}
	if len(c.BootstrapDNS) == 0 {
		c.BootstrapDNS = d.BootstrapDNS
	}
	if c.CacheSize <= 0 {
		c.CacheSize = d.CacheSize
	}
	if c.MaxTTL == 0 {
		c.MaxTTL = d.MaxTTL
	}
	if c.MinTTL > c.MaxTTL {
		c.MinTTL = c.MaxTTL
	}
	if c.TimeoutMS < 200 || c.TimeoutMS > 30000 {
		c.TimeoutMS = d.TimeoutMS
	}
	if c.HealthIntervalS < 10 {
		c.HealthIntervalS = d.HealthIntervalS
	}
	if c.LogSize <= 0 || c.LogSize > 100000 {
		c.LogSize = d.LogSize
	}
	// Merge builtins: add missing ones, refresh URL/bootstrap/notes of existing builtins
	// while keeping the user's Enabled flag.
	idx := map[string]int{}
	for i, p := range c.Providers {
		idx[p.ID] = i
	}
	for _, b := range BuiltinProviders() {
		if i, ok := idx[b.ID]; ok {
			en := c.Providers[i].Enabled
			if c.Providers[i].Builtin {
				c.Providers[i] = b
				c.Providers[i].Enabled = en
			}
		} else {
			c.Providers = append(c.Providers, b)
		}
	}
	seen := map[string]bool{}
	for i := range c.Providers {
		p := &c.Providers[i]
		if !p.Builtin {
			p.Lists = nil // only built-in providers may name a list URL
		}
		if err := ValidateProvider(p); err != nil {
			return err
		}
		if seen[p.ID] {
			return fmt.Errorf("duplicate provider id %q", p.ID)
		}
		seen[p.ID] = true
	}
	for _, a := range c.Listen {
		if _, err := netip.ParseAddrPort(a); err != nil {
			return fmt.Errorf("listen address %q: %w", a, err)
		}
	}
	for _, a := range c.BootstrapDNS {
		if _, err := netip.ParseAddrPort(a); err != nil {
			return fmt.Errorf("bootstrap dns %q: %w", a, err)
		}
	}
	c.resolveActive(seen, d)
	for i := range c.Rules {
		r := &c.Rules[i]
		dom, err := NormalizeDomain(r.Domain)
		if err != nil {
			return fmt.Errorf("rule %q: %w", r.Domain, err)
		}
		r.Domain = dom
		r.Providers = []string{} // one tunnel for everything: rules never pick providers
	}
	return nil
}

// resolveActive enforces the "exactly one DoH tunnel" invariant. The active
// provider is the user's choice; for older configs it is migrated from the
// first still-known enabled provider of the old default chain, then the first enabled one.
func (c *Config) resolveActive(known map[string]bool, d *Config) {
	pick := ""
	if known[c.ActiveProvider] {
		pick = c.ActiveProvider
	}
	if pick == "" {
		for _, id := range c.DefaultChain {
			if p, ok := c.Provider(id); known[id] && ok && p.Enabled {
				pick = id
				break
			}
		}
	}
	if pick == "" {
		for _, p := range c.Providers {
			if p.Enabled {
				pick = p.ID
				break
			}
		}
	}
	if pick == "" {
		pick = d.DefaultChain[0]
	}
	c.ActiveProvider = pick
	c.DefaultChain = []string{pick}
	for i := range c.Providers {
		c.Providers[i].Enabled = c.Providers[i].ID == pick
	}
}

func filterKnown(ids []string, known map[string]bool) []string {
	out := make([]string, 0, len(ids))
	dup := map[string]bool{}
	for _, id := range ids {
		if known[id] && !dup[id] {
			out = append(out, id)
			dup[id] = true
		}
	}
	return out
}

// ValidateProvider checks a provider definition.
func ValidateProvider(p *Provider) error {
	p.ID = strings.TrimSpace(p.ID)
	if p.ID == "" || strings.ContainsAny(p.ID, " \t/\\") {
		return fmt.Errorf("invalid provider id %q", p.ID)
	}
	u, err := url.Parse(strings.TrimSpace(p.URL))
	if err != nil {
		return fmt.Errorf("provider %s: bad url: %w", p.ID, err)
	}
	if u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("provider %s: url must be https://host/path", p.ID)
	}
	if u.Path == "" {
		u.Path = "/dns-query"
	}
	p.URL = u.String()
	if p.Name == "" {
		p.Name = u.Hostname()
	}
	for _, ip := range p.Bootstrap {
		if net.ParseIP(ip) == nil {
			return fmt.Errorf("provider %s: bad bootstrap ip %q", p.ID, ip)
		}
	}
	return nil
}

// NormalizeDomain lowercases, trims dots and a leading "*." and validates a domain.
func NormalizeDomain(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "*.")
	s = strings.Trim(s, ".")
	if s == "" || len(s) > 253 {
		return "", errors.New("empty or too long domain")
	}
	for _, l := range strings.Split(s, ".") {
		if l == "" || len(l) > 63 {
			return "", fmt.Errorf("bad label in %q", s)
		}
		for _, r := range l {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
				return "", fmt.Errorf("bad character %q in %q (use punycode for IDN)", r, s)
			}
		}
	}
	return s, nil
}

// Clone deep-copies the config.
func (c *Config) Clone() *Config {
	b, _ := json.Marshal(c)
	var n Config
	_ = json.Unmarshal(b, &n)
	return &n
}

// Provider returns the provider by id.
func (c *Config) Provider(id string) (Provider, bool) {
	for _, p := range c.Providers {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// Store persists the config atomically.
type Store struct {
	mu   sync.Mutex
	path string
}

func NewStore(path string) *Store { return &Store{path: path} }

func (s *Store) Path() string { return s.path }

// Load reads the config; a missing file yields defaults.
func (s *Store) Load() (*Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		c := Default()
		// First run: the target services are intercepted out of the box. An
		// existing config is never touched; the Rules tab adds presets on demand.
		c.Rules = DefaultRules()
		return c, c.Normalize()
	}
	if err != nil {
		return nil, err
	}
	c := Default()
	c.Providers = nil
	if err := json.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.path, err)
	}
	return c, c.Normalize()
}

// Save writes the config atomically (tmp + rename).
func (s *Store) Save(c *Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	// A planted tmp (symlink) must not be followed: drop it, then create exclusively.
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, s.path)
}
