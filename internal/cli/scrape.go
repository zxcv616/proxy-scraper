package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"github.com/zxcv616/proxy-scraper/internal/output"
	"github.com/zxcv616/proxy-scraper/internal/sources"
	"github.com/zxcv616/proxy-scraper/internal/validate"
)

// Hooks lets callers observe progress without cli knowing about the terminal.
// Any field may be nil.
type Hooks struct {
	FetchStart    func(numSources int)
	FetchDone     func(unique int, errs map[string]error)
	ValidateStart func(total, workers int)
	Validate      func(done, total int)
}

// Summary reports the outcome of a scrape.
type Summary struct {
	Checked  int
	Live     []validate.Result
	JSONPath string
	TextPath string
}

// LiveRate returns the working percentage of checked candidates.
func (s Summary) LiveRate() float64 {
	if s.Checked == 0 {
		return 0
	}
	return 100 * float64(len(s.Live)) / float64(s.Checked)
}

// RunScrape executes the full pipeline: fetch -> dedupe -> validate -> write.
// It returns the sorted live proxies and the paths written.
func RunScrape(ctx context.Context, cfg Config, h Hooks) (Summary, error) {
	srcs := cfg.FilterSources()
	if h.FetchStart != nil {
		h.FetchStart(len(srcs))
	}

	cands, errs := sources.FetchAll(ctx, srcs, cfg.FetchTO)
	if h.FetchDone != nil {
		h.FetchDone(len(cands), errs)
	}
	if cfg.Limit > 0 && len(cands) > cfg.Limit {
		cands = cands[:cfg.Limit]
	}

	if h.ValidateStart != nil {
		h.ValidateStart(len(cands), cfg.Concurrency)
	}
	live := validate.Run(ctx, cands, cfg.Concurrency, cfg.CheckTO, h.Validate)
	sort.Slice(live, func(i, j int) bool { return live[i].Latency() < live[j].Latency() })

	sum := Summary{
		Checked:  len(cands),
		Live:     live,
		JSONPath: filepath.Join(cfg.OutDir, "proxies.json"),
		TextPath: filepath.Join(cfg.OutDir, "proxies.txt"),
	}
	if err := output.WriteJSON(sum.JSONPath, live); err != nil {
		return sum, err
	}
	if err := output.WriteText(sum.TextPath, live); err != nil {
		return sum, err
	}
	return sum, nil
}

// LoadResults reads a previously written proxies.json from outDir, optionally
// filtered by protocol, capped to limit (0 = all). Results stay latency-sorted
// as they were written.
func LoadResults(outDir string, protocol sources.Protocol, limit int) ([]validate.Result, error) {
	path := filepath.Join(outDir, "proxies.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rep output.Report
	if err := json.Unmarshal(b, &rep); err != nil {
		return nil, err
	}
	out := rep.Proxies
	if protocol != "" {
		filtered := out[:0:0]
		for _, r := range out {
			if r.Protocol == protocol {
				filtered = append(filtered, r)
			}
		}
		out = filtered
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
