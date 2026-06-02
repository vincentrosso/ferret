package valuation

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/vincentrosso/ferret/internal/browser"
)

// AuctionHistoryResult holds real sold-price data from saleshistory.org.
type AuctionHistoryResult struct {
	Year      int    `json:"year"`
	Make      string `json:"make"`
	Model     string `json:"model"`
	ModelSlug string `json:"model_slug"`

	// Model-level sold-price aggregate — the key signal: real Copart/IAAI
	// hammer prices across many sold lots.
	SoldMedian int `json:"sold_median"`
	SoldLow    int `json:"sold_low"`
	SoldHigh   int `json:"sold_high"`
	SampleSize int `json:"sample_size"`

	// From the sample lot's detail page (Copart's own estimates)
	SampleVIN     string `json:"sample_vin,omitempty"`
	EstRetail     int    `json:"est_retail,omitempty"`
	EstRepairCost int    `json:"est_repair_cost,omitempty"`

	Source    string `json:"source"` // "saleshistory"
	ScrapedAt string `json:"scraped_at"`
	Error     string `json:"error,omitempty"`
}

var (
	reAggregate = regexp.MustCompile(
		`(?i)across\s+([\d,]+)\s+sold lots.*?median\s+\$([\d,]+),\s*typical range\s+\$([\d,]+)\s+to\s+\$([\d,]+)`)
	reEstRetail = regexp.MustCompile(`(?i)Estimated Retail Value\s+([\d,]+)`)
	reEstRepair = regexp.MustCompile(`(?i)Estimated Repair Cost\s+([\d,]+)`)
	reVINField  = regexp.MustCompile(`(?i)VIN:\s*([A-HJ-NPR-Z0-9]{17})`)
	reModelOpt  = regexp.MustCompile(`value="([^"]+)"[^>]*>([^<]+)<`)
)

// ScrapeAuctionHistory pulls the real sold-price aggregate for a year/make/model
// from saleshistory.org. proxyURL may be empty (works from a residential IP) or
// a residential proxy (required from datacenter IPs — saleshistory uses Cloudflare).
func ScrapeAuctionHistory(makeName, model string, year int, proxyURL string) (*AuctionHistoryResult, error) {
	res := &AuctionHistoryResult{
		Year: year, Make: strings.ToUpper(makeName), Model: strings.ToUpper(model),
		Source: "saleshistory", ScrapedAt: time.Now().UTC().Format(time.RFC3339),
	}

	br, err := browser.New(browser.Options{
		Headless: true,
		UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
			"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
		ProxyURL: proxyURL,
	})
	if err != nil {
		return nil, fmt.Errorf("launch browser: %w", err)
	}
	defer br.Close()

	// Hard 60s budget for the whole scrape — anything slower is treated as a
	// failure (a dead proxy / unsolved Cloudflare won't recover). Bounded
	// per-step timeouts ensure defer br.Close() runs and Chrome is cleaned up.
	deadline := time.Now().Add(28 * time.Second)
	const navTimeout = 10 * time.Second
	overBudget := func() bool { return time.Now().After(deadline) }

	page, err := br.NewPage("https://saleshistory.org/")
	if err != nil {
		return nil, fmt.Errorf("open homepage: %w", err)
	}
	_ = page.Timeout(navTimeout).WaitLoad() // partial load OK (Cloudflare/SPA)
	time.Sleep(2 * time.Second) // let Cloudflare clear

	brand := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(makeName), " ", "_"))

	// Resolve the model slug using the cleared browser session.
	slug := resolveModelSlug(page, brand, model)
	if slug == "" {
		slug = strings.ToLower(strings.Fields(model)[0]) // fallback: first token
	}
	res.ModelSlug = slug

	if overBudget() {
		res.Error = "timed out clearing Cloudflare (>60s) — proxy slow or challenged"
		return res, nil
	}

	catURL := fmt.Sprintf(
		"https://saleshistory.org/catalog/%s/%s/1/%d-%d/0-100000/",
		brand, slug, year-1, year+1,
	)
	_ = page.Timeout(navTimeout).Navigate(catURL)
	_ = page.Timeout(navTimeout).WaitLoad()
	time.Sleep(3 * time.Second) // client-side lot render

	vinHost := firstVINHost(page)
	if vinHost == "" {
		res.Error = fmt.Sprintf("no sold lots found for %d %s %s", year, makeName, model)
		return res, nil
	}

	if overBudget() {
		res.Error = "timed out before detail page (>60s)"
		return res, nil
	}

	_ = page.Timeout(navTimeout).Navigate("https://" + vinHost + "/")
	_ = page.Timeout(navTimeout).WaitLoad()
	time.Sleep(2 * time.Second)

	bodyEl, err := page.Timeout(navTimeout).Element("body")
	if err != nil {
		res.Error = "detail page body not found within timeout"
		return res, nil
	}
	body, err := bodyEl.Text()
	if err != nil {
		return nil, fmt.Errorf("read detail body: %w", err)
	}

	if m := reAggregate.FindStringSubmatch(body); len(m) >= 5 {
		res.SampleSize = atoiComma(m[1])
		res.SoldMedian = atoiComma(m[2])
		res.SoldLow = atoiComma(m[3])
		res.SoldHigh = atoiComma(m[4])
	}
	if m := reEstRetail.FindStringSubmatch(body); len(m) >= 2 {
		res.EstRetail = atoiComma(m[1])
	}
	if m := reEstRepair.FindStringSubmatch(body); len(m) >= 2 {
		res.EstRepairCost = atoiComma(m[1])
	}
	if m := reVINField.FindStringSubmatch(body); len(m) >= 2 {
		res.SampleVIN = m[1]
	}

	if res.SoldMedian == 0 {
		res.Error = "could not parse sold-price aggregate from detail page"
	}
	return res, nil
}

// resolveModelSlug POSTs to /catalog/getmodels/ from inside the cleared browser
// session and picks the longest model label that is a prefix of our model string
// (e.g. "RAV4 XLE" → base "RAV4"; "GRAND CHEROKEE L" → "GRAND CHEROKEE").
func resolveModelSlug(page *rod.Page, brand, model string) string {
	r, err := page.Eval(`async (brand) => {
		const resp = await fetch('/catalog/getmodels/', {
			method: 'POST',
			headers: {'X-Requested-With':'XMLHttpRequest',
			          'Content-Type':'application/x-www-form-urlencoded'},
			body: 'brand=' + encodeURIComponent(brand),
		});
		const j = await resp.json();
		return j.list || '';
	}`, brand)
	if err != nil || r == nil {
		return ""
	}
	html := r.Value.Str()
	if html == "" {
		return ""
	}

	target := strings.ToUpper(strings.TrimSpace(model))
	bestSlug, bestLen := "", 0
	for _, m := range reModelOpt.FindAllStringSubmatch(html, -1) {
		slug, label := m[1], strings.ToUpper(strings.TrimSpace(m[2]))
		if slug == "" || label == "" {
			continue
		}
		// label must be a prefix of our model (so base models match trims)
		if strings.HasPrefix(target+" ", label+" ") || target == label {
			if len(label) > bestLen {
				bestLen, bestSlug = len(label), slug
			}
		}
	}
	return bestSlug
}

// firstVINHost returns the first "<vin>.saleshistory.org" host found among the
// catalog page's links (the per-lot detail pages live on VIN subdomains).
func firstVINHost(page *rod.Page) string {
	r, err := page.Eval(`() => {
		const re = /\/\/([a-z0-9]{11,17})\.saleshistory\.org/i;
		for (const a of document.querySelectorAll('a')) {
			const h = a.getAttribute('href') || '';
			const m = h.match(re);
			if (m) return m[1] + '.saleshistory.org';
		}
		return '';
	}`)
	if err != nil || r == nil {
		return ""
	}
	return r.Value.Str()
}

func atoiComma(s string) int {
	n, _ := strconv.Atoi(strings.ReplaceAll(s, ",", ""))
	return n
}
