// Package cli holds the shared command logic used by both the direct
// subcommand interface and the interactive shell, so behavior is identical
// either way.
package cli

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/zxcv616/proxy-scraper/internal/sources"
)

// Version is the tool version, shown in the banner and `--version`.
const Version = "0.1.0"

// Config is the full set of scrape settings. The shell keeps one of these as
// mutable session state; the direct CLI builds one from flags.
type Config struct {
	Protocols   []sources.Protocol
	Concurrency int
	FetchTO     time.Duration
	CheckTO     time.Duration
	Limit       int
	OutDir      string
}

// DefaultConfig returns the baseline settings.
func DefaultConfig() Config {
	return Config{
		Protocols:   []sources.Protocol{sources.HTTP, sources.HTTPS, sources.SOCKS4, sources.SOCKS5},
		Concurrency: 300,
		FetchTO:     20 * time.Second,
		CheckTO:     8 * time.Second,
		Limit:       0,
		OutDir:      "./data",
	}
}

// ProtocolsString renders the protocol set as "http,https,...".
func (c Config) ProtocolsString() string {
	parts := make([]string, len(c.Protocols))
	for i, p := range c.Protocols {
		parts[i] = string(p)
	}
	return strings.Join(parts, ",")
}

// ParseProtocols parses "http,socks5" into a validated protocol slice.
func ParseProtocols(s string) ([]sources.Protocol, error) {
	var out []sources.Protocol
	seen := map[sources.Protocol]bool{}
	for _, raw := range strings.Split(s, ",") {
		p := sources.Protocol(strings.TrimSpace(strings.ToLower(raw)))
		if p == "" {
			continue
		}
		switch p {
		case sources.HTTP, sources.HTTPS, sources.SOCKS4, sources.SOCKS5:
		default:
			return nil, fmt.Errorf("unknown protocol %q (want http, https, socks4, socks5)", p)
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no valid protocols given")
	}
	return out, nil
}

// ParseDuration accepts "8s", "500ms", "2m", or a bare number of seconds.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		return time.Duration(n) * time.Second, nil
	}
	return 0, fmt.Errorf("invalid duration %q (e.g. 8s, 500ms, 2m)", s)
}

// wantedSet returns the protocol set as a lookup map.
func (c Config) wantedSet() map[sources.Protocol]bool {
	m := map[sources.Protocol]bool{}
	for _, p := range c.Protocols {
		m[p] = true
	}
	return m
}

// FilterSources selects the upstream lists relevant to the wanted protocols.
// HTTP sources also serve HTTPS-capable proxies, so include them when either
// is requested.
func (c Config) FilterSources() []sources.Source {
	wanted := c.wantedSet()
	var out []sources.Source
	for _, s := range sources.DefaultSources {
		if wanted[s.Protocol] || (s.Protocol == sources.HTTP && wanted[sources.HTTPS]) {
			out = append(out, s)
		}
	}
	return out
}
