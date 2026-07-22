package shell

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/zxcv616/proxy-scraper/internal/cli"

	"golang.org/x/term"
)

// Accent and text styles (256-color codes). Accent is violet to give the tool
// its own identity next to sibling scrapers.
const (
	accent = "38;5;141" // violet
	dim    = "2"
	bold   = "1"
	reset  = "0"
)

var ansiRE = regexp.MustCompile("\x1b\\[[0-9;]*m")

// useColor reports whether styled output should be emitted.
func useColor() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	return term.IsTerminal(int(os.Stdout.Fd()))
}

// c wraps an ANSI code, or returns "" when color is disabled.
func c(code string) string {
	if useColor() {
		return "\x1b[" + code + "m"
	}
	return ""
}

// visLen is the display width of s, ignoring ANSI color codes. It counts
// runes (not bytes) so multi-byte box-drawing/braille glyphs measure as 1.
func visLen(s string) int {
	return utf8.RuneCountInString(ansiRE.ReplaceAllString(s, ""))
}

// termWidth returns the usable width, capped at 80 like the sibling tools.
func termWidth() int {
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w <= 0 {
		w = 80
	}
	if w > 80 {
		w = 80
	}
	return w
}

// Block-letter "PXY" (pxy) in the same box-drawing font as the sibling CLI.
var logo = strings.Split(strings.Trim(`
 ██████╗  ██╗  ██╗ ██╗   ██╗
 ██╔══██╗ ╚██╗██╔╝ ╚██╗ ██╔╝
 ██████╔╝  ╚███╔╝   ╚████╔╝
 ██╔═══╝   ██╔██╗    ╚██╔╝
 ██║      ██╔╝ ██╗    ██║
 ╚═╝      ╚═╝  ╚═╝    ╚═╝   `, "\n"), "\n")

// banner renders the boxed startup banner.
func banner(cfg cli.Config) string {
	width := termWidth()
	inner := width - 2
	o, d, b, r := c(accent), c(dim), c(bold), c(reset)

	title := fmt.Sprintf("%s%spxy%s%s v%s%s", b, o, r, d, cli.Version, r)
	right := []string{
		fmt.Sprintf("%sproxyscraper%s", b, r),
		fmt.Sprintf("%sfree proxy aggregator + validator%s", d, r),
		fmt.Sprintf("%sby zxcv616%s", d, r),
	}

	// Pad every logo line to a uniform width so the right-hand labels align.
	logoW := 0
	for _, line := range logo {
		if w := utf8.RuneCountInString(line); w > logoW {
			logoW = w
		}
	}
	rows := []string{""}
	for i, line := range logo {
		var rlabel string
		if i >= 1 && i <= len(right) {
			rlabel = right[i-1]
		}
		line += strings.Repeat(" ", logoW-utf8.RuneCountInString(line))
		rows = append(rows, fmt.Sprintf("  %s%s%s   %s", o, line, r, rlabel))
	}
	rows = append(rows,
		"",
		fmt.Sprintf("  %sprotocols%s %s    %soutput%s %s", d, r, cfg.ProtocolsString(), d, r, cfg.OutDir),
		fmt.Sprintf("  %scommands%s scrape · list · get · exec · sources · set · show · exit", d, r),
		"",
	)

	top := fmt.Sprintf("%s╭─%s %s %s%s╮%s", o, r, title, o, strings.Repeat("─", max(0, inner-visLen(title)-3)), r)
	out := []string{top}
	for _, row := range rows {
		pad := max(0, inner-visLen(row))
		out = append(out, fmt.Sprintf("%s│%s%s%s%s│%s", o, r, row, strings.Repeat(" ", pad), o, r))
	}
	out = append(out, fmt.Sprintf("%s╰%s╯%s", o, strings.Repeat("─", inner), r))
	return strings.Join(out, "\n")
}

// spinner animates a braille frame on stderr until Stop is called. It is a
// no-op when not attached to a color TTY.
type spinner struct {
	label string
	stop  chan struct{}
	done  chan struct{}
	mu    sync.Mutex
	on    bool
}

var spinFrames = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

func newSpinner(label string) *spinner {
	return &spinner{label: label, stop: make(chan struct{}), done: make(chan struct{})}
}

func (s *spinner) Start() {
	if !useColor() {
		return
	}
	s.on = true
	go func() {
		defer close(s.done)
		i := 0
		t := time.NewTicker(80 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-t.C:
				s.mu.Lock()
				fmt.Fprintf(os.Stderr, "\r\x1b[%sm%c\x1b[0m %s", accent, spinFrames[i%len(spinFrames)], s.label)
				s.mu.Unlock()
				i++
			}
		}
	}()
}

// Stop halts the animation and clears the line.
func (s *spinner) Stop() {
	if !s.on {
		return
	}
	s.on = false
	close(s.stop)
	<-s.done
	s.mu.Lock()
	fmt.Fprint(os.Stderr, "\r\x1b[2K")
	s.mu.Unlock()
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
