// Package market scrapes AutoTrader for OC used-car listing prices
// to build a local comp database for resale value estimation.
package market

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/vincentrosso/ferret/internal/browser"
)

const (
	zip      = "92618" // Irvine, CA (central OC)
	radius   = 50      // miles
	maxMiles = 60_000
	minYear  = 2020
)

// CompEntry is the resale data for one make/model bucket.
type CompEntry struct {
	MedianPrice int       `json:"median_price"`
	AvgPrice    int       `json:"avg_price"`
	MinPrice    int       `json:"min_price"`
	MaxPrice    int       `json:"max_price"`
	SampleSize  int       `json:"sample_size"`
	ScrapedAt   time.Time `json:"scraped_at"`
}

// Comps is the full comp database keyed by "MAKE|MODEL" (e.g. "TOYOTA|RAV4").
type Comps map[string]CompEntry

// Load reads comps from a JSON file. Returns empty map if file doesn't exist.
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

// Save writes comps to a JSON file.
func (c Comps) Save(path string) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// Lookup returns the median price for a make/model key, or 0 if not found.
func (c Comps) Lookup(make, modelPrefix string) int {
	key := strings.ToUpper(make) + "|" + strings.ToUpper(modelPrefix)
	if e, ok := c[key]; ok {
		return e.MedianPrice
	}
	return 0
}

// Target is one make/model to scrape.
type Target struct {
	Make        string // e.g. "toyota"
	Model       string // e.g. "rav4" (URL slug)
	ModelKey    string // e.g. "RAV4" (comp map key)
}

// DefaultTargets are the models we care about.
var DefaultTargets = []Target{
	{"toyota", "rav4", "RAV4"},
	{"toyota", "camry", "CAMRY"},
	{"toyota", "corolla", "COROLLA"},
	{"toyota", "highlander", "HIGHLANDER"},
	{"toyota", "tacoma", "TACOMA"},
	{"honda", "cr-v", "CR-V"},
	{"honda", "accord", "ACCORD"},
	{"honda", "civic", "CIVIC"},
	{"honda", "pilot", "PILOT"},
	{"lexus", "rx", "RX"},
	{"lexus", "es", "ES"},
	{"lexus", "nx", "NX"},
}

// Scraper fetches listing prices from AutoTrader.
type Scraper struct {
	br *browser.Browser
}

func New(br *browser.Browser) *Scraper {
	return &Scraper{br: br}
}

// ScrapeAll scrapes all DefaultTargets and returns an updated Comps map.
func (s *Scraper) ScrapeAll(existing Comps) Comps {
	if existing == nil {
		existing = Comps{}
	}
	for _, t := range DefaultTargets {
		entry, err := s.scrapeTarget(t)
		if err != nil {
			slog.Warn("market scrape failed", "make", t.Make, "model", t.Model, "err", err)
			continue
		}
		key := strings.ToUpper(t.Make) + "|" + t.ModelKey
		existing[key] = *entry
		slog.Info("market comp", "key", key,
			"median", entry.MedianPrice, "n", entry.SampleSize)
		time.Sleep(2 * time.Second) // polite delay
	}
	return existing
}

func (s *Scraper) scrapeTarget(t Target) (*CompEntry, error) {
	// Cars.com model slug: "toyota-rav4", "honda-cr-v", etc.
	modelSlug := t.Make + "-" + t.Model
	url := fmt.Sprintf(
		"https://www.cars.com/shopping/results/?makes[]=%s&models[]=%s"+
			"&zip=%s&maximum_distance=%d&mileage_max=%d&year_min=%d&year_max=2026&stock_type=used",
		t.Make, modelSlug, zip, radius, maxMiles, minYear,
	)

	page, err := s.br.NewPage(url)
	if err != nil {
		return nil, fmt.Errorf("open page: %w", err)
	}
	defer page.MustClose()

	// Wait for page load then give JS time to render listings
	page.WaitLoad()
	time.Sleep(3 * time.Second)

	// Extract prices via text node scan — Cars.com renders prices as "$XX,XXX"
	result, err := page.Eval(`() => {
		const prices = [];
		const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
		const re = /\$(\d{2,3}),(\d{3})(?!\d)/g;
		let node;
		while ((node = walker.nextNode())) {
			let m;
			while ((m = re.exec(node.textContent)) !== null) {
				const n = parseInt(m[1] + m[2]);
				if (n >= 8000 && n <= 80000) prices.push(n);
			}
		}
		return [...new Set(prices)];
	}`)
	if err != nil {
		return nil, fmt.Errorf("js eval: %w", err)
	}

	var rawPrices []int
	if err := result.Value.Unmarshal(&rawPrices); err != nil || len(rawPrices) < 3 {
		return nil, fmt.Errorf("too few prices extracted: %d", len(rawPrices))
	}

	sort.Ints(rawPrices)
	median := rawPrices[len(rawPrices)/2]
	sum := 0
	for _, p := range rawPrices {
		sum += p
	}

	return &CompEntry{
		MedianPrice: median,
		AvgPrice:    sum / len(rawPrices),
		MinPrice:    rawPrices[0],
		MaxPrice:    rawPrices[len(rawPrices)-1],
		SampleSize:  len(rawPrices),
		ScrapedAt:   time.Now(),
	}, nil
}
