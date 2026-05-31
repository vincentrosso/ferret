package market

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var reMoney = regexp.MustCompile(`\$(\d{1,3}),(\d{3})`)

type ebaySoldSource struct {
	cfg    Config
	client *http.Client
}

func newEbaySoldSource(cfg Config) *ebaySoldSource {
	return &ebaySoldSource{cfg: cfg, client: &http.Client{Timeout: 20 * time.Second}}
}

func (e *ebaySoldSource) Name() string   { return "ebay_sold" }
func (e *ebaySoldSource) Weight() int    { return 3 }

// Scrape fetches eBay Motors completed/sold listings for the given make+model.
// Uses multiple year queries to get more samples.
func (e *ebaySoldSource) Scrape(ctx context.Context, makeSlug, modelSlug string) ([]int, error) {
	query := fmt.Sprintf("%d-%d %s %s clean title",
		e.cfg.MinYear, time.Now().Year(), makeSlug, strings.ReplaceAll(modelSlug, "-", " "))

	u := "https://www.ebay.com/sch/6001/i.html?" + url.Values{
		"_nkw":         {query},
		"LH_Sold":      {"1"},
		"LH_Complete":  {"1"},
		"_ipg":         {"200"},
		"_sop":         {"12"}, // sort: newly listed
		"LH_ItemCondition": {"3000"}, // used
	}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return extractPrices(string(body), 8_000, 80_000), nil
}

// extractPrices pulls all dollar amounts from HTML within [minP, maxP].
func extractPrices(html string, minP, maxP int) []int {
	seen := map[int]bool{}
	var out []int
	for _, m := range reMoney.FindAllStringSubmatch(html, -1) {
		n, err := strconv.Atoi(m[1] + m[2])
		if err != nil || n < minP || n > maxP || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}
