package market

import (
	"context"
	"fmt"
	"time"

	"github.com/vincentrosso/ferret/internal/browser"
)

type carsComSource struct {
	br  *browser.Browser
	cfg Config
}

func newCarsComSource(br *browser.Browser, cfg Config) *carsComSource {
	return &carsComSource{br: br, cfg: cfg}
}

func (c *carsComSource) Name() string { return "cars_com" }
func (c *carsComSource) Weight() int  { return 1 }

func (c *carsComSource) Scrape(_ context.Context, makeSlug, modelSlug string) ([]int, error) {
	modelKey := makeSlug + "-" + modelSlug
	u := fmt.Sprintf(
		"https://www.cars.com/shopping/results/?makes[]=%s&models[]=%s"+
			"&zip=%s&maximum_distance=%d&mileage_max=%d&year_min=%d&year_max=2026&stock_type=used",
		makeSlug, modelKey, c.cfg.Zip, c.cfg.Radius, c.cfg.MaxMiles, c.cfg.MinYear,
	)

	page, err := c.br.NewPage(u)
	if err != nil {
		return nil, fmt.Errorf("open page: %w", err)
	}
	defer page.MustClose()

	page.WaitLoad()
	time.Sleep(3 * time.Second)

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
	if err := result.Value.Unmarshal(&rawPrices); err != nil {
		return nil, fmt.Errorf("unmarshal prices: %w", err)
	}
	if len(rawPrices) < 3 {
		return nil, fmt.Errorf("only %d prices found", len(rawPrices))
	}
	return rawPrices, nil
}
