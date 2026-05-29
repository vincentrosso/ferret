package scraper

import (
	"context"

	"github.com/go-rod/rod"
)

// Result is a single scraped item — scrapers return a slice of these.
type Result map[string]any

// Scraper defines the contract for a site-specific scraper.
type Scraper interface {
	// Name returns the scraper's identifier (e.g. "copart", "iaai").
	Name() string

	// Search navigates to a search/listing page and returns result URLs or IDs.
	Search(ctx context.Context, page *rod.Page, query Query) ([]string, error)

	// Detail fetches a single item's full data given its URL or ID.
	Detail(ctx context.Context, page *rod.Page, target string) (Result, error)
}

// Query carries search parameters passed to Search.
type Query struct {
	Keywords string
	Filters  map[string]string
	MaxPages int
}
