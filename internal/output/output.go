// Package output writes validated proxies to disk as JSON and plain text.
package output

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/zxcv616/proxy-scraper/internal/validate"
)

// Report is the top-level JSON document.
type Report struct {
	GeneratedAt time.Time          `json:"generated_at"`
	Count       int                `json:"count"`
	Proxies     []validate.Result  `json:"proxies"`
}

// WriteJSON writes the full structured report.
func WriteJSON(path string, live []validate.Result) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	rep := Report{GeneratedAt: time.Now().UTC(), Count: len(live), Proxies: live}
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// WriteText writes one "protocol://ip:port" per line, sorted as given.
func WriteText(path string, live []validate.Result) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, r := range live {
		if _, err := f.WriteString(string(r.Protocol) + "://" + r.Addr() + "\n"); err != nil {
			return err
		}
	}
	return nil
}
