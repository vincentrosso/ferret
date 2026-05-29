package main

import (
	"context"
	"encoding/json"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/vincentrosso/ferret/internal/browser"
	"github.com/vincentrosso/ferret/internal/scraper"
)

func main() {
	headless := flag.Bool("headless", true, "run browser headless")
	concurrency := flag.Int("concurrency", 3, "parallel detail pages")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	br, err := browser.New(browser.Options{
		Headless: *headless,
		UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
			"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
	})
	if err != nil {
		slog.Error("launch browser", "err", err)
		os.Exit(1)
	}
	defer br.Close()

	_ = *concurrency // pipeline wired up when first scraper is added

	// Smoke-test: open example.com and print title
	page, err := br.NewPage("https://example.com")
	if err != nil {
		slog.Error("new page", "err", err)
		os.Exit(1)
	}
	defer page.MustClose()

	if err := page.WaitLoad(); err != nil {
		slog.Error("wait load", "err", err)
		os.Exit(1)
	}

	title := page.MustInfo().Title
	result := scraper.Result{"title": title, "url": "https://example.com"}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(result)

	<-ctx.Done()
}
