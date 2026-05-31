package market

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// craigslistSubdomain maps common OC/SoCal zips to the CL subdomain.
// Falls back to orangecounty for unrecognized zips.
var clSubdomains = map[string]string{
	"92618": "orangecounty",
	"92660": "orangecounty",
	"90210": "losangeles",
	"90001": "losangeles",
	"92101": "sandiego",
}

type craigslistSource struct {
	cfg       Config
	subdomain string
	client    *http.Client
}

func newCraigslistSource(cfg Config) *craigslistSource {
	sub, ok := clSubdomains[cfg.Zip]
	if !ok {
		sub = "orangecounty"
	}
	return &craigslistSource{
		cfg:       cfg,
		subdomain: sub,
		client:    &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *craigslistSource) Name() string { return "craigslist" }
func (c *craigslistSource) Weight() int  { return 2 }

func (c *craigslistSource) Scrape(ctx context.Context, makeSlug, modelSlug string) ([]int, error) {
	query := makeSlug + " " + strings.ReplaceAll(modelSlug, "-", " ")

	// cto = cars & trucks by owner (private party only)
	u := fmt.Sprintf("https://%s.craigslist.org/search/cto?%s", c.subdomain,
		url.Values{
			"query":          {query},
			"min_price":      {"8000"},
			"max_price":      {"60000"},
			"auto_year_min":  {fmt.Sprintf("%d", c.cfg.MinYear)},
			"hasPic":         {"1"},
			"srchType":       {"T"},
			"auto_miles_max": {fmt.Sprintf("%d", c.cfg.MaxMiles)},
		}.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	prices := extractPrices(string(body), 8_000, 60_000)
	if len(prices) == 0 {
		return nil, fmt.Errorf("no prices found (may be blocked or zero listings)")
	}
	return prices, nil
}
