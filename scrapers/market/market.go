// Package market scrapes multiple listing sources to build OC used-car comp data.
//
// Sources and weights (higher = more influence on blended price):
//   ebay_sold   3  — actual completed transactions (ground truth)
//   craigslist  2  — private-party asking (closest to our resale channel)
//   cars_com    1  — dealer asking (upper bound)
package market

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/vincentrosso/ferret/internal/browser"
)

// Source is a single listing data provider.
type Source interface {
	Name() string
	Weight() int
	Scrape(ctx context.Context, make, model string) ([]int, error)
}

// SourceData captures per-source statistics.
type SourceData struct {
	Median     int `json:"median"`
	SampleSize int `json:"sample_size"`
	Weight     int `json:"weight"`
}

// CompEntry is the blended resale comp for one make/model.
type CompEntry struct {
	Blended   int                   `json:"blended"`   // weighted median across all sources
	Sources   map[string]SourceData `json:"sources"`
	ScrapedAt time.Time             `json:"scraped_at"`
}

// Comps is the full comp database keyed by "MAKE|MODEL" (e.g. "TOYOTA|RAV4").
type Comps map[string]CompEntry

func Load(path string) Comps {
	b, err := os.ReadFile(path)
	if err != nil {
		return Comps{}
	}
	var c Comps
	if err := json.Unmarshal(b, &c); err != nil {
		return Comps{}
	}
	return c
}

func (c Comps) Save(path string) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// Lookup returns the blended median for a make/model key, or 0 if not found.
func (c Comps) Lookup(make, modelPrefix string) int {
	key := upper(make) + "|" + upper(modelPrefix)
	if e, ok := c[key]; ok {
		return e.Blended
	}
	return 0
}

// Target is a make/model pair to scrape across all sources.
type Target struct {
	Make     string // "TOYOTA" — comp key
	Model    string // "RAV4"   — comp key
	MakeSlug string // "toyota" — URL slug
	ModelSlug string // "rav4"  — URL slug
}

var DefaultTargets = []Target{
	{"TOYOTA", "RAV4", "toyota", "rav4"},
	{"TOYOTA", "CAMRY", "toyota", "camry"},
	{"TOYOTA", "COROLLA", "toyota", "corolla"},
	{"TOYOTA", "HIGHLANDER", "toyota", "highlander"},
	{"TOYOTA", "TACOMA", "toyota", "tacoma"},
	{"HONDA", "CR-V", "honda", "cr-v"},
	{"HONDA", "ACCORD", "honda", "accord"},
	{"HONDA", "CIVIC", "honda", "civic"},
	{"HONDA", "PILOT", "honda", "pilot"},
	{"LEXUS", "RX", "lexus", "rx"},
	{"LEXUS", "ES", "lexus", "es"},
	{"LEXUS", "NX", "lexus", "nx"},
}

// Config holds shared scrape settings.
type Config struct {
	Zip     string // e.g. "92618" (Irvine, OC)
	Radius  int    // miles
	MaxMiles int
	MinYear int
}

var DefaultConfig = Config{
	Zip:      "92618",
	Radius:   50,
	MaxMiles: 60_000,
	MinYear:  2020,
}

// Scraper runs all sources for all targets and returns updated comps.
type Scraper struct {
	sources []Source
}

// New creates a Scraper with all available sources.
func New(br *browser.Browser, cfg Config) *Scraper {
	return &Scraper{sources: []Source{
		newEbaySoldSource(cfg),
		newCraigslistSource(cfg),
		newCarsComSource(br, cfg),
	}}
}

// ScrapeAll scrapes every target across every source and returns updated comps.
func (s *Scraper) ScrapeAll(ctx context.Context, existing Comps) Comps {
	if existing == nil {
		existing = Comps{}
	}
	for _, t := range DefaultTargets {
		entry := s.scrapeTarget(ctx, t)
		key := t.Make + "|" + t.Model
		if entry != nil {
			existing[key] = *entry
			slog.Info("comp updated", "key", key, "blended", entry.Blended, "sources", len(entry.Sources))
		} else {
			slog.Warn("all sources failed", "key", key)
		}
		select {
		case <-ctx.Done():
			return existing
		default:
		}
	}
	return existing
}

func (s *Scraper) scrapeTarget(ctx context.Context, t Target) *CompEntry {
	type result struct {
		source string
		weight int
		prices []int
	}
	var mu sync.Mutex
	var results []result
	var wg sync.WaitGroup

	for _, src := range s.sources {
		src := src
		wg.Add(1)
		go func() {
			defer wg.Done()
			prices, err := src.Scrape(ctx, t.MakeSlug, t.ModelSlug)
			if err != nil {
				slog.Warn("source failed", "source", src.Name(), "make", t.Make, "model", t.Model, "err", err)
				return
			}
			if len(prices) == 0 {
				return
			}
			mu.Lock()
			results = append(results, result{src.Name(), src.Weight(), prices})
			mu.Unlock()
		}()
	}
	wg.Wait()

	if len(results) == 0 {
		return nil
	}

	entry := &CompEntry{
		Sources:   make(map[string]SourceData),
		ScrapedAt: time.Now(),
	}

	// Build weighted price pool for blended median
	var pool []int
	for _, r := range results {
		trimmed := trimOutliers(r.prices)
		if len(trimmed) == 0 {
			continue
		}
		med := median(trimmed)
		entry.Sources[r.source] = SourceData{
			Median:     med,
			SampleSize: len(trimmed),
			Weight:     r.weight,
		}
		// Add each source's prices r.weight times to weight the pool
		for range r.weight {
			pool = append(pool, trimmed...)
		}
	}

	if len(pool) == 0 {
		return nil
	}

	entry.Blended = median(pool)
	return entry
}

// ── helpers ───────────────────────────────────────────────────────────────────

func median(prices []int) int {
	sorted := make([]int, len(prices))
	copy(sorted, prices)
	sort.Ints(sorted)
	return sorted[len(sorted)/2]
}

// trimOutliers removes bottom 10% and top 10% of prices.
func trimOutliers(prices []int) []int {
	if len(prices) < 5 {
		return prices
	}
	sorted := make([]int, len(prices))
	copy(sorted, prices)
	sort.Ints(sorted)
	cut := len(sorted) / 10
	if cut == 0 {
		cut = 1
	}
	return sorted[cut : len(sorted)-cut]
}

func upper(s string) string {
	out := make([]byte, len(s))
	for i := range s {
		c := s[i]
		if c >= 'a' && c <= 'z' {
			c -= 32
		}
		out[i] = c
	}
	return string(out)
}
