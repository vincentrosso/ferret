package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/vincentrosso/ferret/internal/browser"
	"github.com/vincentrosso/ferret/scrapers/fb"
)

func fbBrowser(headless bool, proxy string) *browser.Browser {
	br, err := browser.New(browser.Options{
		Headless: headless,
		ProxyURL: proxy,
		UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
			"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
	})
	if err != nil {
		fatal("launch browser", err)
	}
	return br
}

func emitJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// runFBLogin never takes credentials: it opens a visible window and waits for a
// person to sign in. See scrapers/fb for why.
func runFBLogin(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("fb login", flag.ExitOnError)
	cookiePath := fs.String("cookies", fb.DefaultCookiePath, "cookie file path")
	wait := fs.Duration("wait", 10*time.Minute, "how long to wait for the human to sign in")
	proxy := fs.String("proxy", "", "proxy (use the SAME egress the scrapes will use)")
	fs.Parse(args)

	br := fbBrowser(false, *proxy)
	defer br.Close()
	if err := fb.New(br, *cookiePath).Login(ctx, *wait); err != nil {
		fatal("fb login", err)
	}
	fmt.Println("✓ facebook session saved to", *cookiePath)
}

func runFBCheck(args []string) {
	fs := flag.NewFlagSet("fb check", flag.ExitOnError)
	cookiePath := fs.String("cookies", fb.DefaultCookiePath, "cookie file path")
	fs.Parse(args)
	st := fb.ReadSession(*cookiePath)
	if !st.Live {
		fmt.Println("✗ fb session not usable:", st.Reason, "— run: ferret fb login")
		os.Exit(1)
	}
	exp := "browser-session cookie"
	if !st.Expires.IsZero() {
		exp = "xs expires " + st.Expires.UTC().Format(time.RFC3339)
	}
	fmt.Printf("✓ fb session valid (user %s, %s)\n", st.UserID, exp)
}

// exitLoggedOut uses a distinct exit code so the Python tracker can tell "the
// session died" (alarm) from "a scrape flaked" (retry tomorrow).
func exitLoggedOut(err error) {
	if errors.Is(err, fb.ErrLoggedOut) {
		slog.Error("fb", "err", err)
		os.Exit(3)
	}
}

func runFBSearch(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("fb search", flag.ExitOnError)
	cookiePath := fs.String("cookies", fb.DefaultCookiePath, "cookie file path")
	query := fs.String("query", "", "search text, e.g. \"toyota avalon\"")
	location := fs.String("location", "", "city slug (default: the account's saved location)")
	minYear := fs.Int("min-year", 0, "")
	maxYear := fs.Int("max-year", 0, "")
	minPrice := fs.Int("min-price", 0, "")
	maxPrice := fs.Int("max-price", 0, "")
	scrolls := fs.Int("scrolls", 6, "infinite-scroll pages to load")
	proxy := fs.String("proxy", "", "residential proxy")
	rawURL := fs.String("url", "", "exact Marketplace search URL (overrides the filter flags)")
	fs.Parse(args)
	if *query == "" {
		fmt.Fprintln(os.Stderr, "fb search: -query is required")
		os.Exit(2)
	}

	br := fbBrowser(true, *proxy)
	defer br.Close()
	sc := fb.New(br, *cookiePath)
	if err := sc.LoadSession(); err != nil {
		slog.Error("fb", "err", err)
		os.Exit(3)
	}
	res, err := sc.Search(ctx, fb.SearchParams{
		Query: *query, Location: *location, MinYear: *minYear, MaxYear: *maxYear,
		MinPrice: *minPrice, MaxPrice: *maxPrice, Scrolls: *scrolls, RawURL: *rawURL,
	})
	if err != nil {
		exitLoggedOut(err)
		fatal("fb search", err)
	}
	emitJSON(res)
}

func runFBItem(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("fb item", flag.ExitOnError)
	cookiePath := fs.String("cookies", fb.DefaultCookiePath, "cookie file path")
	ids := fs.String("ids", "", "comma-separated listing ids")
	proxy := fs.String("proxy", "", "residential proxy")
	fs.Parse(args)

	br := fbBrowser(true, *proxy)
	defer br.Close()
	sc := fb.New(br, *cookiePath)
	if err := sc.LoadSession(); err != nil {
		slog.Error("fb", "err", err)
		os.Exit(3)
	}
	var out []fb.Listing
	for i, id := range strings.Split(*ids, ",") {
		id = strings.TrimSpace(id)
		if id == "" || ctx.Err() != nil {
			continue
		}
		if i > 0 {
			fb.Pace()
		}
		l := fb.Listing{ID: id, URL: "https://www.facebook.com/marketplace/item/" + id + "/"}
		if err := sc.Item(ctx, &l); err != nil {
			exitLoggedOut(err)
			// One flaky item must not cost the batch — report it and carry on.
			slog.Warn("fb item failed", "id", id, "err", err)
			continue
		}
		out = append(out, l)
	}
	emitJSON(out)
}
