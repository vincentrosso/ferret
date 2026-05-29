package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/vincentrosso/ferret/internal/browser"
	"github.com/vincentrosso/ferret/internal/scraper"
)

// Pipeline orchestrates search → detail scraping with concurrency control.
type Pipeline struct {
	br          *browser.Browser
	sc          scraper.Scraper
	concurrency int
}

func New(br *browser.Browser, sc scraper.Scraper, concurrency int) *Pipeline {
	if concurrency <= 0 {
		concurrency = 3
	}
	return &Pipeline{br: br, sc: sc, concurrency: concurrency}
}

// Run executes a full search+detail pass and streams results to out.
func (p *Pipeline) Run(ctx context.Context, q scraper.Query, out chan<- scraper.Result) error {
	searchPage, err := p.br.NewPage("")
	if err != nil {
		return fmt.Errorf("open search page: %w", err)
	}
	defer searchPage.MustClose()

	targets, err := p.sc.Search(ctx, searchPage, q)
	if err != nil {
		return fmt.Errorf("search: %w", err)
	}
	slog.Info("search complete", "scraper", p.sc.Name(), "targets", len(targets))

	sem := make(chan struct{}, p.concurrency)
	var wg sync.WaitGroup

	for _, target := range targets {
		target := target
		wg.Add(1)
		sem <- struct{}{}

		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			page, err := p.br.NewPage("")
			if err != nil {
				slog.Error("open detail page", "err", err, "target", target)
				return
			}
			defer page.MustClose()

			result, err := p.sc.Detail(ctx, page, target)
			if err != nil {
				slog.Error("detail scrape", "err", err, "target", target)
				return
			}

			select {
			case out <- result:
			case <-ctx.Done():
			}
		}()
	}

	wg.Wait()
	return nil
}
