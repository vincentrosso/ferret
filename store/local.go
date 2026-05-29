// Package store handles persisting scraped data locally.
// Directory layout mirrors what an S3 prefix structure would look like,
// so the paths are a drop-in replacement once we wire up S3.
//
//	data/raw/<lot_number>/detail.json
//	data/images/<lot_number>/<filename>
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// Local persists raw JSON and image bytes under a root directory.
type Local struct {
	root string
}

func NewLocal(root string) *Local {
	return &Local{root: root}
}

// SaveJSON writes v as indented JSON to data/raw/<lotNumber>/detail.json.
// Creates parent directories as needed.
func (s *Local) SaveJSON(lotNumber string, v any) (string, error) {
	dir := filepath.Join(s.root, "raw", lotNumber)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "detail.json")
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, b, 0o644)
}

// DownloadImages fetches each URL concurrently and saves to data/images/<lotNumber>/.
// Returns the local paths of successfully downloaded files.
func (s *Local) DownloadImages(ctx context.Context, lotNumber string, urls []string) ([]string, error) {
	if len(urls) == 0 {
		return nil, nil
	}
	dir := filepath.Join(s.root, "images", lotNumber)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	type result struct {
		path string
		err  error
	}
	ch := make(chan result, len(urls))

	client := &http.Client{Timeout: 30 * time.Second}

	for i, rawURL := range urls {
		rawURL := rawURL
		i := i
		go func() {
			ext := imageExt(rawURL)
			filename := fmt.Sprintf("%03d%s", i+1, ext)
			path := filepath.Join(dir, filename)

			// Skip if already downloaded
			if _, err := os.Stat(path); err == nil {
				ch <- result{path: path}
				return
			}

			req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
			if err != nil {
				ch <- result{err: fmt.Errorf("url %d: %w", i, err)}
				return
			}
			req.Header.Set("User-Agent", "Mozilla/5.0")
			req.Header.Set("Referer", "https://www.copart.com/")

			resp, err := client.Do(req)
			if err != nil {
				ch <- result{err: fmt.Errorf("fetch %d: %w", i, err)}
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				ch <- result{err: fmt.Errorf("url %d: HTTP %d", i, resp.StatusCode)}
				return
			}

			f, err := os.Create(path)
			if err != nil {
				ch <- result{err: err}
				return
			}
			defer f.Close()

			if _, err := io.Copy(f, resp.Body); err != nil {
				ch <- result{err: fmt.Errorf("write %d: %w", i, err)}
				return
			}
			ch <- result{path: path}
		}()
	}

	var paths []string
	var errs []error
	for range urls {
		r := <-ch
		if r.err != nil {
			errs = append(errs, r.err)
		} else if r.path != "" {
			paths = append(paths, r.path)
		}
	}
	if len(errs) > 0 {
		slog.Warn("some images failed", "lot", lotNumber, "failed", len(errs), "ok", len(paths))
	}
	return paths, nil
}

func imageExt(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ".jpg"
	}
	ext := filepath.Ext(u.Path)
	if ext == "" {
		return ".jpg"
	}
	return ext
}
