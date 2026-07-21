// Package validate checks candidate proxies for liveness by routing a request
// through each one to a judge endpoint and confirming the response reflects the
// proxy's IP (not ours). This is the step that turns a mostly-dead raw list
// into a usable one — free lists typically have only a 5-15% live rate.
package validate

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zxcv616/proxy-scraper/internal/sources"

	xproxy "golang.org/x/net/proxy"
)

// Result is a validated (or failed) proxy.
type Result struct {
	sources.Candidate
	OK        bool          `json:"ok"`
	LatencyMS int64         `json:"latency_ms"`
	ExitIP    string        `json:"exit_ip,omitempty"`
	Err       string        `json:"error,omitempty"`
	latency   time.Duration `json:"-"`
}

// judgeURL returns the tester's observed client IP as plain text.
const judgeURL = "https://api.ipify.org"

// Run validates candidates with a bounded worker pool and returns only the
// live ones, sorted by ascending latency by the caller.
func Run(ctx context.Context, cands []sources.Candidate, concurrency int, timeout time.Duration, progress func(done, total int)) []Result {
	jobs := make(chan sources.Candidate)
	results := make(chan Result)
	var wg sync.WaitGroup

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range jobs {
				results <- check(ctx, c, timeout)
			}
		}()
	}

	go func() {
		for _, c := range cands {
			select {
			case <-ctx.Done():
				close(jobs)
				return
			case jobs <- c:
			}
		}
		close(jobs)
	}()

	go func() { wg.Wait(); close(results) }()

	var live []Result
	total := len(cands)
	done := 0
	for r := range results {
		done++
		if progress != nil {
			progress(done, total)
		}
		if r.OK {
			live = append(live, r)
		}
	}
	return live
}

// check routes one request through the candidate proxy.
func check(ctx context.Context, c sources.Candidate, timeout time.Duration) Result {
	res := Result{Candidate: c}
	transport, err := transportFor(c, timeout)
	if err != nil {
		res.Err = err.Error()
		return res
	}
	client := &http.Client{Transport: transport, Timeout: timeout}

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(reqCtx, http.MethodGet, judgeURL, nil)
	req.Header.Set("User-Agent", "proxyscraper/1.0")

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		res.Err = err.Error()
		return res
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 128))
	if err != nil {
		res.Err = err.Error()
		return res
	}
	ip := strings.TrimSpace(string(body))
	if resp.StatusCode != http.StatusOK || net.ParseIP(ip) == nil {
		res.Err = fmt.Sprintf("bad response (status %d)", resp.StatusCode)
		return res
	}
	res.OK = true
	res.ExitIP = ip
	res.latency = time.Since(start)
	res.LatencyMS = res.latency.Milliseconds()
	return res
}

// Latency exposes the raw duration for sorting.
func (r Result) Latency() time.Duration { return r.latency }

// transportFor builds an http.Transport that dials through the given proxy.
func transportFor(c sources.Candidate, timeout time.Duration) (*http.Transport, error) {
	switch c.Protocol {
	case sources.HTTP, sources.HTTPS:
		u, err := url.Parse("http://" + c.Addr())
		if err != nil {
			return nil, err
		}
		return &http.Transport{
			Proxy:               http.ProxyURL(u),
			TLSHandshakeTimeout: timeout,
			DisableKeepAlives:   true,
		}, nil
	case sources.SOCKS5:
		d, err := xproxy.SOCKS5("tcp", c.Addr(), nil, &net.Dialer{Timeout: timeout})
		if err != nil {
			return nil, err
		}
		return &http.Transport{
			DialContext:         func(_ context.Context, network, addr string) (net.Conn, error) { return d.Dial(network, addr) },
			TLSHandshakeTimeout: timeout,
			DisableKeepAlives:   true,
		}, nil
	case sources.SOCKS4:
		return &http.Transport{
			DialContext:         socks4DialContext(c.Addr(), timeout),
			TLSHandshakeTimeout: timeout,
			DisableKeepAlives:   true,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported protocol %q", c.Protocol)
	}
}

// socks4DialContext returns a DialContext that establishes a SOCKS4 tunnel.
// SOCKS4 has no library dialer in x/net, so we implement the (small) CONNECT
// handshake directly. Only IPv4 destinations are supported by the protocol.
func socks4DialContext(proxyAddr string, timeout time.Duration) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, _, target string) (net.Conn, error) {
		host, portStr, err := net.SplitHostPort(target)
		if err != nil {
			return nil, err
		}
		port, err := strconv.Atoi(portStr)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
		if err != nil || len(ips) == 0 {
			return nil, fmt.Errorf("socks4: resolve %s: %v", host, err)
		}
		ip4 := ips[0].To4()
		if ip4 == nil {
			return nil, fmt.Errorf("socks4: no IPv4 for %s", host)
		}

		conn, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", proxyAddr)
		if err != nil {
			return nil, err
		}
		_ = conn.SetDeadline(time.Now().Add(timeout))

		// Request: VN=4, CD=1 (connect), DSTPORT, DSTIP, USERID="", null.
		req := []byte{0x04, 0x01}
		req = binary.BigEndian.AppendUint16(req, uint16(port))
		req = append(req, ip4...)
		req = append(req, 0x00) // empty user id
		if _, err := conn.Write(req); err != nil {
			conn.Close()
			return nil, err
		}

		// Reply: 8 bytes; resp[1]==0x5A means granted.
		reply := make([]byte, 8)
		if _, err := io.ReadFull(conn, reply); err != nil {
			conn.Close()
			return nil, err
		}
		if reply[1] != 0x5A {
			conn.Close()
			return nil, fmt.Errorf("socks4: request rejected (0x%02x)", reply[1])
		}
		_ = conn.SetDeadline(time.Time{})
		return conn, nil
	}
}
