// Command pxy (proxy-scraper) aggregates free proxies from public lists,
// validates them concurrently, and writes the working ones (ranked by latency).
//
// Run with no arguments to open the interactive shell. Or use a subcommand:
//
//	pxy scrape [flags]     aggregate + validate + write proxies.{json,txt}
//	pxy list [flags]       show recently scraped working proxies
//	pxy get [flags]        print the single fastest working proxy
//	pxy sources            list the upstream proxy lists
//	pxy version            print version
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/zxcv616/proxy-scraper/internal/cli"
	"github.com/zxcv616/proxy-scraper/internal/shell"
	"github.com/zxcv616/proxy-scraper/internal/sources"
	"github.com/zxcv616/proxy-scraper/internal/validate"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(argv []string) int {
	if len(argv) == 0 {
		return shell.New(cli.DefaultConfig()).Run()
	}
	cmd, rest := argv[0], argv[1:]
	switch cmd {
	case "scrape", "run":
		return cmdScrape(rest)
	case "list", "results":
		return cmdList(rest)
	case "get":
		return cmdGet(rest)
	case "sources":
		return cmdSources()
	case "exec":
		return cmdExec(rest)
	case "shell":
		return shell.New(cli.DefaultConfig()).Run()
	case "version", "--version", "-v":
		fmt.Printf("pxy (proxy-scraper) %s by zxcv616\n", cli.Version)
		return 0
	case "help", "--help", "-h":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `pxy — free proxy aggregator + validator

Usage:
  pxy                 open the interactive shell
  pxy scrape [flags]  aggregate + validate + write proxies.{json,txt}
  pxy list [flags]    show recently scraped working proxies
  pxy get [flags]     print the single fastest working proxy
  pxy exec [flags] -- <command> [args...]   run a command through the first working proxy
  pxy sources         list the upstream proxy lists
  pxy version         print version

Run 'pxy <command> -h' for command flags.
`)
}

// cmdScrape mirrors the shell's scrape but with flags and plain output.
func cmdScrape(argv []string) int {
	def := cli.DefaultConfig()
	fs := flag.NewFlagSet("scrape", flag.ContinueOnError)
	protocols := fs.String("protocols", def.ProtocolsString(), "comma-separated protocols to include")
	concurrency := fs.Int("concurrency", def.Concurrency, "concurrent validation workers")
	fetchTO := fs.Duration("fetch-timeout", def.FetchTO, "timeout for fetching each source list")
	checkTO := fs.Duration("check-timeout", def.CheckTO, "timeout for validating each proxy")
	limit := fs.Int("limit", def.Limit, "cap candidates before validation (0 = no cap)")
	outDir := fs.String("out", def.OutDir, "output directory for proxies.{json,txt}")
	quiet := fs.Bool("quiet", false, "suppress progress output")
	if err := fs.Parse(argv); err != nil {
		return 2
	}

	ps, err := cli.ParseProtocols(*protocols)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	cfg := cli.Config{
		Protocols:   ps,
		Concurrency: *concurrency,
		FetchTO:     *fetchTO,
		CheckTO:     *checkTO,
		Limit:       *limit,
		OutDir:      *outDir,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logf := func(format string, a ...any) {
		if !*quiet {
			fmt.Fprintf(os.Stderr, format, a...)
		}
	}
	last := time.Now()
	hooks := cli.Hooks{
		FetchStart: func(n int) { logf("Fetching %d source lists...\n", n) },
		FetchDone: func(unique int, errs map[string]error) {
			for name, e := range errs {
				logf("  ! source %s failed: %v\n", name, e)
			}
			logf("Collected %d unique candidates.\n", unique)
		},
		ValidateStart: func(total, workers int) {
			logf("Validating with %d workers (timeout %s)...\n", workers, cfg.CheckTO)
		},
	}
	if !*quiet {
		hooks.Validate = func(done, total int) {
			if done == total || time.Since(last) > 500*time.Millisecond {
				last = time.Now()
				fmt.Fprintf(os.Stderr, "\r  checked %d/%d", done, total)
				if done == total {
					fmt.Fprintln(os.Stderr)
				}
			}
		}
	}

	sum, err := cli.RunScrape(ctx, cfg, hooks)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nerror: %v\n", err)
		return 1
	}
	fmt.Printf("\nDone: %d working / %d checked (%.1f%% live)\n", len(sum.Live), sum.Checked, sum.LiveRate())
	fmt.Printf("  %s\n  %s\n", sum.JSONPath, sum.TextPath)
	if len(sum.Live) > 0 {
		f := sum.Live[0]
		fmt.Printf("Fastest: %s://%s (%dms)\n", f.Protocol, f.Addr(), f.LatencyMS)
	}
	return 0
}

func cmdList(argv []string) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	protocol := fs.String("protocol", "", "filter by protocol (http, https, socks4, socks5)")
	limit := fs.Int("limit", 20, "max rows (0 = all)")
	outDir := fs.String("out", cli.DefaultConfig().OutDir, "directory holding proxies.json")
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	rows, err := cli.LoadResults(*outDir, sources.Protocol(strings.ToLower(*protocol)), *limit)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "no data in %s — run 'proxyscraper scrape' first.\n", *outDir)
			return 1
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	for _, r := range rows {
		fmt.Printf("%s://%s\t%dms\t%s\n", r.Protocol, r.Addr(), r.LatencyMS, r.ExitIP)
	}
	return 0
}

func cmdGet(argv []string) int {
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	protocol := fs.String("protocol", "", "filter by protocol")
	outDir := fs.String("out", cli.DefaultConfig().OutDir, "directory holding proxies.json")
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	rows, err := cli.LoadResults(*outDir, sources.Protocol(strings.ToLower(*protocol)), 1)
	if err != nil || len(rows) == 0 {
		fmt.Fprintf(os.Stderr, "no matching proxy in %s — run 'proxyscraper scrape' first.\n", *outDir)
		return 1
	}
	fmt.Printf("%s://%s\n", rows[0].Protocol, rows[0].Addr())
	return 0
}

func cmdExec(argv []string) int {
	fs := flag.NewFlagSet("exec", flag.ContinueOnError)
	protocol := fs.String("protocol", "http", "protocol filter (http, https, socks4, socks5)")
	checkTO := fs.Duration("check-timeout", 3*time.Second, "timeout per proxy check")
	outDir := fs.String("out", cli.DefaultConfig().OutDir, "directory holding proxies.json")
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	args := fs.Args()
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: pxy exec [flags] -- <command> [args...]")
		fs.PrintDefaults()
		return 2
	}

	ps, err := cli.ParseProtocols(*protocol)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	rows, err := cli.LoadResults(*outDir, ps[0], 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "no data in %s — run 'pxy scrape' first.\n", *outDir)
		return 1
	}
	if len(rows) == 0 {
		fmt.Fprintln(os.Stderr, "no proxies to try.")
		return 1
	}

	ctx := context.Background()
	live := findFirstLive(ctx, rows, *checkTO)
	if live == nil {
		fmt.Fprintln(os.Stderr, "no working proxy found.")
		return 1
	}

	proxyURL := string(live.Protocol) + "://" + live.Addr()
	fmt.Fprintf(os.Stderr, "using %s\n", proxyURL)

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()

	u := strings.TrimSuffix(proxyURL, "/")
	switch live.Protocol {
	case sources.HTTP:
		cmd.Env = append(cmd.Env, "HTTP_PROXY="+u, "http_proxy="+u)
	case sources.HTTPS:
		cmd.Env = append(cmd.Env, "HTTPS_PROXY="+u, "https_proxy="+u)
	case sources.SOCKS4, sources.SOCKS5:
		cmd.Env = append(cmd.Env, "ALL_PROXY="+u, "all_proxy="+u)
	}

	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

// findFirstLive quick-tests proxies in latency order and returns the first working one.
func findFirstLive(ctx context.Context, rows []validate.Result, timeout time.Duration) *validate.Result {
	for i := range rows {
		r := validate.CheckOne(ctx, rows[i].Candidate, timeout)
		if r.OK {
			return &rows[i]
		}
	}
	return nil
}

func cmdSources() int {
	byProto := map[sources.Protocol][]sources.Source{}
	var order []sources.Protocol
	for _, s := range sources.DefaultSources {
		if _, ok := byProto[s.Protocol]; !ok {
			order = append(order, s.Protocol)
		}
		byProto[s.Protocol] = append(byProto[s.Protocol], s)
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	fmt.Printf("%d source lists across %d protocols:\n", len(sources.DefaultSources), len(order))
	for _, p := range order {
		fmt.Printf("\n%s\n", p)
		for _, s := range byProto[p] {
			fmt.Printf("  %-22s %s\n", s.Name, s.URL)
		}
	}
	return 0
}
