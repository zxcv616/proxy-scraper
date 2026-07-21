// Package sources fetches pre-aggregated free-proxy lists from public
// endpoints and normalizes them into deduplicated candidates.
//
// The design principle: we do NOT scrape proxy *websites* (brittle HTML that
// breaks constantly). Instead we aggregate the raw output of dozens of
// scrapers that already publish results to GitHub / public APIs every few
// minutes. Our value-add is dedup + validation (see internal/validate).
package sources

import (
	"bufio"
	"context"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Protocol identifies how a proxy is reached.
type Protocol string

const (
	HTTP   Protocol = "http"
	HTTPS  Protocol = "https"
	SOCKS4 Protocol = "socks4"
	SOCKS5 Protocol = "socks5"
)

// Candidate is a single proxy endpoint before validation.
type Candidate struct {
	IP       string   `json:"ip"`
	Port     string   `json:"port"`
	Protocol Protocol `json:"protocol"`
}

// Addr returns the "ip:port" form.
func (c Candidate) Addr() string { return c.IP + ":" + c.Port }

// Source is a single upstream list.
type Source struct {
	Name     string
	URL      string
	Protocol Protocol // protocol these entries speak
}

// DefaultSources are the aggregators researched as the strongest free options
// for 2026. All return plain "ip:port" lines (one per row) over HTTPS.
var DefaultSources = []Source{
	// proxifly — refreshed every 5 min, served via jsDelivr CDN.
	{"proxifly-http", "https://cdn.jsdelivr.net/gh/proxifly/free-proxy-list@main/proxies/protocols/http/data.txt", HTTP},
	{"proxifly-socks4", "https://cdn.jsdelivr.net/gh/proxifly/free-proxy-list@main/proxies/protocols/socks4/data.txt", SOCKS4},
	{"proxifly-socks5", "https://cdn.jsdelivr.net/gh/proxifly/free-proxy-list@main/proxies/protocols/socks5/data.txt", SOCKS5},
	// ProxyScraper — refreshed ~30 min, raw GitHub.
	{"proxyscraper-http", "https://raw.githubusercontent.com/ProxyScraper/ProxyScraper/main/http.txt", HTTP},
	{"proxyscraper-socks4", "https://raw.githubusercontent.com/ProxyScraper/ProxyScraper/main/socks4.txt", SOCKS4},
	{"proxyscraper-socks5", "https://raw.githubusercontent.com/ProxyScraper/ProxyScraper/main/socks5.txt", SOCKS5},
	// TheSpeedX/PROXY-List — long-standing, high-volume, refreshed frequently.
	{"speedx-http", "https://raw.githubusercontent.com/TheSpeedX/PROXY-List/master/http.txt", HTTP},
	{"speedx-socks4", "https://raw.githubusercontent.com/TheSpeedX/PROXY-List/master/socks4.txt", SOCKS4},
	{"speedx-socks5", "https://raw.githubusercontent.com/TheSpeedX/PROXY-List/master/socks5.txt", SOCKS5},
	// monosans — validated lists, refreshed frequently.
	{"monosans-http", "https://raw.githubusercontent.com/monosans/proxy-list/main/proxies/http.txt", HTTP},
	{"monosans-socks4", "https://raw.githubusercontent.com/monosans/proxy-list/main/proxies/socks4.txt", SOCKS4},
	{"monosans-socks5", "https://raw.githubusercontent.com/monosans/proxy-list/main/proxies/socks5.txt", SOCKS5},
	// ProxyScrape public API (protocol-scoped).
	{"proxyscrape-http", "https://api.proxyscrape.com/v4/free-proxy-list/get?request=display_proxies&protocol=http&proxy_format=ipport&format=text", HTTP},
	{"proxyscrape-socks4", "https://api.proxyscrape.com/v4/free-proxy-list/get?request=display_proxies&protocol=socks4&proxy_format=ipport&format=text", SOCKS4},
	{"proxyscrape-socks5", "https://api.proxyscrape.com/v4/free-proxy-list/get?request=display_proxies&protocol=socks5&proxy_format=ipport&format=text", SOCKS5},
}

// ipPortRe matches "1.2.3.4:8080", tolerating a scheme prefix and surrounding
// junk so we can also parse lightly-formatted lists.
var ipPortRe = regexp.MustCompile(`(?:^|[^0-9])(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}):(\d{1,5})`)

// FetchAll pulls every source concurrently and returns a deduplicated slice.
// Fetch errors are collected and returned alongside whatever succeeded, so one
// dead source never sinks the run.
func FetchAll(ctx context.Context, srcs []Source, timeout time.Duration) ([]Candidate, map[string]error) {
	client := &http.Client{Timeout: timeout}
	var (
		mu    sync.Mutex
		seen  = map[string]struct{}{}
		out   []Candidate
		errs  = map[string]error{}
		wg    sync.WaitGroup
	)

	for _, s := range srcs {
		wg.Add(1)
		go func(s Source) {
			defer wg.Done()
			cands, err := fetch(ctx, client, s)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs[s.Name] = err
				return
			}
			for _, c := range cands {
				key := string(c.Protocol) + "|" + c.Addr()
				if _, ok := seen[key]; ok {
					continue
				}
				seen[key] = struct{}{}
				out = append(out, c)
			}
		}(s)
	}
	wg.Wait()
	return out, errs
}

func fetch(ctx context.Context, client *http.Client, s Source) ([]Candidate, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "proxyscraper/1.0 (+https://github.com)")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var cands []Candidate
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		m := ipPortRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		cands = append(cands, Candidate{IP: m[1], Port: m[2], Protocol: s.Protocol})
	}
	return cands, sc.Err()
}
