// Package shell implements the interactive REPL launched when proxyscraper is
// run with no subcommand. It keeps scrape settings as session state you change
// with `set`, and dispatches to the same cli functions as the direct commands.
package shell

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/zxcv616/proxy-scraper/internal/cli"
	"github.com/zxcv616/proxy-scraper/internal/sources"
	"github.com/zxcv616/proxy-scraper/internal/validate"
)

// Shell holds session state.
type Shell struct {
	cfg cli.Config
	in  *bufio.Scanner
}

// New builds a shell seeded with the given config.
func New(cfg cli.Config) *Shell {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	return &Shell{cfg: cfg, in: sc}
}

// Run renders the banner and enters the read-eval loop. Returns an exit code.
func (s *Shell) Run() int {
	if useColor() {
		fmt.Print("\x1b[2J\x1b[3J\x1b[H") // clear screen + scrollback
	}
	fmt.Println(banner(s.cfg))
	fmt.Println()

	for {
		fmt.Print(s.prompt())
		if !s.in.Scan() {
			fmt.Println()
			return 0 // EOF (Ctrl-D)
		}
		line := strings.TrimSpace(s.in.Text())
		if line == "" {
			continue
		}
		if quit := s.dispatch(line); quit {
			return 0
		}
	}
}

func (s *Shell) prompt() string {
	if useColor() {
		return fmt.Sprintf("\x1b[%smpxy ❯\x1b[0m ", accent)
	}
	return "pxy> "
}

func (s *Shell) dispatch(line string) (quit bool) {
	fields := strings.Fields(line)
	cmd, args := strings.ToLower(fields[0]), fields[1:]
	switch cmd {
	case "scrape", "run":
		s.doScrape(args)
	case "list", "results", "ls":
		s.doList(args)
	case "get":
		s.doGet(args)
	case "sources":
		s.doSources()
	case "set":
		s.doSet(args)
	case "show", "config":
		s.doShow()
	case "help", "?":
		s.doHelp()
	case "exit", "quit", "q":
		return true
	default:
		fmt.Printf("unknown command: %q. type 'help'.\n", cmd)
	}
	return false
}

// --- commands ---------------------------------------------------------------

func (s *Shell) doScrape(args []string) {
	cfg := s.cfg
	if len(args) > 0 { // one-off protocol override, e.g. `scrape socks5 http`
		ps, err := cli.ParseProtocols(strings.Join(args, ","))
		if err != nil {
			fmt.Printf("error: %v\n", err)
			return
		}
		cfg.Protocols = ps
	}

	spin := newSpinner("aggregating source lists")
	spin.Start()
	printedProgress := false
	hooks := cli.Hooks{
		FetchDone: func(unique int, errs map[string]error) {
			spin.Stop()
			for name, err := range errs {
				fmt.Fprintf(os.Stderr, "  %s! source %s failed:%s %v\n", c(dim), name, c(reset), err)
			}
			fmt.Printf("Collected %s%d%s unique candidates.\n", c(bold), unique, c(reset))
		},
		ValidateStart: func(total, workers int) {
			fmt.Printf("Validating with %d workers (timeout %s)...\n", workers, cfg.CheckTO)
		},
		Validate: func(done, total int) {
			printedProgress = true
			fmt.Fprintf(os.Stderr, "\r  checked %d/%d", done, total)
			if done == total {
				fmt.Fprintln(os.Stderr)
			}
		},
	}

	sum, err := cli.RunScrape(context.Background(), cfg, hooks)
	if err != nil {
		if printedProgress {
			fmt.Fprintln(os.Stderr)
		}
		fmt.Printf("error: %v\n", err)
		return
	}
	fmt.Printf("\n%sDone:%s %d working / %d checked (%.1f%% live)\n",
		c(bold), c(reset), len(sum.Live), sum.Checked, sum.LiveRate())
	fmt.Printf("  %s%s%s\n  %s%s%s\n", c(dim), sum.JSONPath, c(reset), c(dim), sum.TextPath, c(reset))
	if len(sum.Live) > 0 {
		n := min(5, len(sum.Live))
		fmt.Printf("Top %d by latency:\n", n)
		printTable(sum.Live[:n])
	}
}

func (s *Shell) doList(args []string) {
	proto, limit := parseProtoLimit(args, 20)
	rows, err := cli.LoadResults(s.cfg.OutDir, proto, limit)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("no data yet in %s — run 'scrape' first.\n", s.cfg.OutDir)
			return
		}
		fmt.Printf("error reading results: %v\n", err)
		return
	}
	if len(rows) == 0 {
		fmt.Println("no matching proxies.")
		return
	}
	printTable(rows)
}

func (s *Shell) doGet(args []string) {
	proto, _ := parseProtoLimit(args, 1)
	rows, err := cli.LoadResults(s.cfg.OutDir, proto, 1)
	if err != nil {
		fmt.Printf("no data yet in %s — run 'scrape' first.\n", s.cfg.OutDir)
		return
	}
	if len(rows) == 0 {
		fmt.Println("no matching proxies.")
		return
	}
	// Plain, pipe-friendly output.
	fmt.Printf("%s://%s\n", rows[0].Protocol, rows[0].Addr())
}

func (s *Shell) doSources() {
	byProto := map[sources.Protocol][]sources.Source{}
	var order []sources.Protocol
	for _, src := range sources.DefaultSources {
		if _, ok := byProto[src.Protocol]; !ok {
			order = append(order, src.Protocol)
		}
		byProto[src.Protocol] = append(byProto[src.Protocol], src)
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	fmt.Printf("%d source lists across %d protocols:\n", len(sources.DefaultSources), len(order))
	for _, p := range order {
		fmt.Printf("\n  %s%s%s\n", c(bold), p, c(reset))
		for _, src := range byProto[p] {
			fmt.Printf("    %-22s %s%s%s\n", src.Name, c(dim), src.URL, c(reset))
		}
	}
}

func (s *Shell) doSet(args []string) {
	if len(args) < 2 {
		fmt.Println("usage: set <key> <value>   (see 'show' for keys)")
		return
	}
	key := strings.ToLower(args[0])
	val := strings.Join(args[1:], " ")
	switch key {
	case "protocols":
		ps, err := cli.ParseProtocols(val)
		if err != nil {
			fmt.Printf("error: %v\n", err)
			return
		}
		s.cfg.Protocols = ps
		fmt.Printf("protocols = %s\n", s.cfg.ProtocolsString())
	case "concurrency":
		n, err := strconv.Atoi(val)
		if err != nil || n <= 0 {
			fmt.Println("error: concurrency must be a positive integer")
			return
		}
		s.cfg.Concurrency = n
		fmt.Printf("concurrency = %d\n", n)
	case "limit":
		n, err := strconv.Atoi(val)
		if err != nil || n < 0 {
			fmt.Println("error: limit must be a non-negative integer (0 = no cap)")
			return
		}
		s.cfg.Limit = n
		fmt.Printf("limit = %d\n", n)
	case "check_timeout", "check-timeout":
		d, err := cli.ParseDuration(val)
		if err != nil {
			fmt.Printf("error: %v\n", err)
			return
		}
		s.cfg.CheckTO = d
		fmt.Printf("check_timeout = %s\n", d)
	case "fetch_timeout", "fetch-timeout":
		d, err := cli.ParseDuration(val)
		if err != nil {
			fmt.Printf("error: %v\n", err)
			return
		}
		s.cfg.FetchTO = d
		fmt.Printf("fetch_timeout = %s\n", d)
	case "out":
		s.cfg.OutDir = val
		fmt.Printf("out = %s\n", val)
	default:
		fmt.Printf("unknown setting %q. keys: protocols, concurrency, limit, check_timeout, fetch_timeout, out\n", key)
	}
}

func (s *Shell) doShow() {
	pairs := [][2]string{
		{"protocols", s.cfg.ProtocolsString()},
		{"concurrency", strconv.Itoa(s.cfg.Concurrency)},
		{"limit", strconv.Itoa(s.cfg.Limit)},
		{"check_timeout", s.cfg.CheckTO.String()},
		{"fetch_timeout", s.cfg.FetchTO.String()},
		{"out", s.cfg.OutDir},
	}
	for _, p := range pairs {
		fmt.Printf("  %s%-14s%s %s\n", c(dim), p[0], c(reset), p[1])
	}
}

func (s *Shell) doHelp() {
	lines := [][2]string{
		{"scrape [proto...]", "aggregate + validate; optional one-off protocol filter"},
		{"list [proto] [N]", "show recent working proxies (default 20)"},
		{"get [proto]", "print the single fastest working proxy"},
		{"sources", "list the upstream proxy lists"},
		{"set KEY VALUE", "change a setting (see 'show')"},
		{"show", "print current settings"},
		{"help", "this help"},
		{"exit", "quit"},
	}
	for _, l := range lines {
		fmt.Printf("  %s%-18s%s %s\n", c(accent), l[0], c(reset), l[1])
	}
}

// --- helpers ----------------------------------------------------------------

// parseProtoLimit reads optional "<protocol>" and "<N>" tokens in any order.
func parseProtoLimit(args []string, defLimit int) (sources.Protocol, int) {
	proto, limit := sources.Protocol(""), defLimit
	for _, a := range args {
		if n, err := strconv.Atoi(a); err == nil {
			limit = n
			continue
		}
		if ps, err := cli.ParseProtocols(a); err == nil {
			proto = ps[0]
		}
	}
	return proto, limit
}

func printTable(rows []validate.Result) {
	cols := []string{"protocol", "address", "latency", "exit_ip"}
	w := map[string]int{}
	for _, c := range cols {
		w[c] = len(c)
	}
	cell := func(r validate.Result) map[string]string {
		return map[string]string{
			"protocol": string(r.Protocol),
			"address":  r.Addr(),
			"latency":  strconv.FormatInt(r.LatencyMS, 10) + "ms",
			"exit_ip":  r.ExitIP,
		}
	}
	for _, r := range rows {
		for k, v := range cell(r) {
			if len(v) > w[k] {
				w[k] = len(v)
			}
		}
	}
	pad := func(s string, n int) string { return s + strings.Repeat(" ", n-len(s)) }

	hdr := make([]string, len(cols))
	sep := make([]string, len(cols))
	for i, col := range cols {
		hdr[i] = pad(col, w[col])
		sep[i] = strings.Repeat("-", w[col])
	}
	fmt.Printf("  %s%s%s\n", c(dim), strings.Join(hdr, "  "), c(reset))
	fmt.Printf("  %s%s%s\n", c(dim), strings.Join(sep, "  "), c(reset))
	for _, r := range rows {
		ce := cell(r)
		vals := make([]string, len(cols))
		for i, col := range cols {
			vals[i] = pad(ce[col], w[col])
		}
		fmt.Printf("  %s\n", strings.Join(vals, "  "))
	}
	fmt.Printf("\n%d prox%s\n", len(rows), plural(len(rows)))
}

func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
