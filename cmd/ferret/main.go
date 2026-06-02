package main

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/vincentrosso/ferret/internal/browser"
	"github.com/vincentrosso/ferret/internal/damage"
	"github.com/vincentrosso/ferret/internal/report"
	"github.com/vincentrosso/ferret/internal/scoring"
	"github.com/vincentrosso/ferret/internal/server"
	"github.com/vincentrosso/ferret/internal/valuation"
	"github.com/vincentrosso/ferret/scrapers/copart"
	"github.com/vincentrosso/ferret/scrapers/market"
	"github.com/vincentrosso/ferret/store"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	if len(os.Args) < 3 && os.Args[1] != "serve" {
		usage()
		os.Exit(1)
	}

	loadDotEnv(".env")

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if os.Args[1] == "serve" {
		runServe(ctx, os.Args[2:])
		return
	}

	switch os.Args[1] + " " + os.Args[2] {
	case "copart login":
		runCopartLogin(ctx, os.Args[3:])
	case "copart check":
		runCopartCheck(ctx, os.Args[3:])
	case "copart search":
		runCopartSearch(ctx, os.Args[3:])
	case "copart from-url":
		runCopartFromURL(ctx, os.Args[3:])
	case "copart detail":
		runCopartDetail(ctx, os.Args[3:])
	case "copart bid":
		runCopartBid(ctx, os.Args[3:])
	case "value carfax":
		runValueCarfax(os.Args[3:])
	case "value auction-history":
		runValueAuctionHistory(os.Args[3:])
	case "value kbb":
		runValueKBB(os.Args[3:])
	case "copart analyze":
		runCopartAnalyze(ctx, os.Args[3:])
	case "copart report":
		runCopartReport(os.Args[3:])
	case "market scrape":
		runMarketScrape(ctx, os.Args[3:])
	default:
		usage()
		os.Exit(1)
	}
}

func runCopartLogin(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("copart login", flag.ExitOnError)
	headless := fs.Bool("headless", false, "headless browser (default false — show window for CAPTCHA)")
	cookiePath := fs.String("cookies", copart.DefaultCookiePath, "cookie file path")
	fs.Parse(args)

	email := mustEnv("COPART_EMAIL")
	password := mustEnv("COPART_PASSWORD")

	br, err := browser.New(browser.Options{
		Headless: *headless,
		UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
			"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
	})
	if err != nil {
		fatal("launch browser", err)
	}
	defer br.Close()

	sc := copart.New(br, email, password, *cookiePath)
	if err := sc.Login(ctx); err != nil {
		fatal("login", err)
	}
	fmt.Println("✓ logged in, session saved to", *cookiePath)
}

func runCopartSearch(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("copart search", flag.ExitOnError)
	makes := fs.String("makes", "TOYOTA,HONDA,LEXUS", "comma-separated makes")
	yearMin := fs.Int("year-min", 0, "minimum model year (default: 4 years ago)")
	yearMax := fs.Int("year-max", 0, "maximum model year (default: current year + 2)")
	odoMax := fs.Int("odo-max", 85_000, "max odometer miles")
	daysAhead := fs.Int("days", 5, "auction date window: today + N days")
	damage := fs.String("damage", "DAMAGECODE_HL", "damage type code")
	title := fs.String("title", "C", "title groups: C (clean), S (salvage), or C,S for both")
	maxPages := fs.Int("pages", 0, "max pages to scrape (0 = unlimited)")
	cookiePath := fs.String("cookies", copart.DefaultCookiePath, "cookie file path")
	outFile := fs.String("out", "", "write JSON results to file (default: stdout)")
	fs.Parse(args)

	makeList := strings.Split(strings.ToUpper(*makes), ",")

	br, err := browser.New(browser.Options{
		Headless: true,
		UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
			"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
	})
	if err != nil {
		fatal("launch browser", err)
	}
	defer br.Close()

	email := mustEnv("COPART_EMAIL")
	password := mustEnv("COPART_PASSWORD")
	sc := copart.New(br, email, password, *cookiePath)

	if err := sc.LoadSession(ctx); err != nil {
		slog.Warn("no saved session — run: ferret copart login", "err", err)
	}

	params := copart.SearchParams{
		Makes:      makeList,
		YearMin:    *yearMin,
		YearMax:    *yearMax,
		OdoMax:     *odoMax,
		DateTo:     time.Now().AddDate(0, 0, *daysAhead),
		DamageCode:  *damage,
		TitleGroups: strings.Split(*title, ","),
		MaxPages:   *maxPages,
	}

	lots, err := sc.RunSearch(ctx, params)
	if err != nil {
		fatal("search", err)
	}

	ranked := scoring.RankAll(lots)
	slog.Info("search complete", "total_lots", len(ranked))

	out := os.Stdout
	if *outFile != "" {
		f, err := os.Create(*outFile)
		if err != nil {
			fatal("create output file", err)
		}
		defer f.Close()
		out = f
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	enc.Encode(ranked)
	if *outFile != "" {
		fmt.Fprintf(os.Stderr, "wrote %d ranked lots to %s\n", len(ranked), *outFile)
	}
}

func runServe(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.String("port", "7777", "port to listen on")
	dataDir := fs.String("data", "data", "data directory")
	fs.Parse(args)

	srv, err := server.New(*dataDir)
	if err != nil {
		fatal("init server", err)
	}

	addr := ":" + *port
	slog.Info("serving", "url", "http://localhost:"+*port)

	httpSrv := &http.Server{Addr: addr, Handler: srv.Handler()}
	go func() {
		<-ctx.Done()
		httpSrv.Shutdown(context.Background()) //nolint:errcheck
	}()

	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fatal("listen", err)
	}
}

func runCopartDetail(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("copart detail", flag.ExitOnError)
	lotFlag := fs.String("lot", "", "single lot number (e.g. 86590735)")
	fromFile := fs.String("from", "", "JSON file of search results ([]copart.Lot) to detail-scrape")
	workers := fs.Int("workers", 2, "parallel browser workers when using -from")
	dataDir := fs.String("data", "data", "root directory for raw JSON + images")
	images := fs.Bool("images", true, "download lot images")
	cookiePath := fs.String("cookies", copart.DefaultCookiePath, "cookie file path")
	proxy := fs.String("proxy", "", "residential proxy (when datacenter IP is throttled)")
	fs.Parse(args)

	if *lotFlag == "" && *fromFile == "" {
		fmt.Fprintln(os.Stderr, "error: provide -lot <number> or -from <search.json>")
		os.Exit(1)
	}

	st := store.NewLocal(*dataDir)
	email := mustEnv("COPART_EMAIL")
	password := mustEnv("COPART_PASSWORD")

	// Collect lot URLs to scrape
	var lotURLs []string
	if *lotFlag != "" {
		lotURLs = append(lotURLs, "https://www.copart.com/lot/"+*lotFlag)
	} else {
		f, err := os.Open(*fromFile)
		if err != nil {
			fatal("open input file", err)
		}
		var lots []copart.Lot
		if err := json.NewDecoder(f).Decode(&lots); err != nil {
			fatal("parse input file", err)
		}
		f.Close()
		for _, l := range lots {
			lotURLs = append(lotURLs, l.LotURL)
		}
		slog.Info("loaded lots from file", "file", *fromFile, "count", len(lotURLs))
	}

	// Worker pool: each worker gets its own browser
	type job struct{ url string }
	jobs := make(chan job, len(lotURLs))
	for _, u := range lotURLs {
		jobs <- job{u}
	}
	close(jobs)

	var wg sync.WaitGroup
	var mu sync.Mutex
	var succeeded, failed int

	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			br, err := browser.New(browser.Options{
				Headless: true,
				UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
					"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
				ProxyURL: *proxy,
			})
			if err != nil {
				slog.Error("worker: launch browser", "err", err)
				return
			}
			defer br.Close()

			sc := copart.New(br, email, password, *cookiePath)
			if err := sc.LoadSession(ctx); err != nil {
				slog.Warn("worker: no saved session", "err", err)
			}

			for j := range jobs {
				imgDir := ""
				if *images {
					imgDir = filepath.Join(*dataDir, "images")
				}

				detail, err := sc.ScrapeDetail(ctx, j.url, imgDir)
				if err != nil {
					slog.Error("detail failed", "url", j.url, "err", err)
					mu.Lock()
					failed++
					mu.Unlock()
					continue
				}

				path, err := st.SaveJSON(detail.LotNumber, detail)
				if err != nil {
					slog.Error("save JSON", "lot", detail.LotNumber, "err", err)
				} else {
					slog.Info("saved", "lot", detail.LotNumber, "path", path,
						"vin", detail.VIN, "zip", detail.ImageZip)
				}

				mu.Lock()
				succeeded++
				mu.Unlock()
			}
		}()
	}

	wg.Wait()
	slog.Info("detail scrape complete", "ok", succeeded, "failed", failed)
}

func runCopartFromURL(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("copart from-url", flag.ExitOnError)
	rawURL := fs.String("url", "", "any Copart results URL (search / saleListResult)")
	maxPages := fs.Int("pages", 0, "max pages (0 = all)")
	cookiePath := fs.String("cookies", copart.DefaultCookiePath, "cookie file path")
	outFile := fs.String("out", "", "write ranked JSON to file (default stdout)")
	proxy := fs.String("proxy", "", "residential proxy for Copart (when datacenter IP is throttled)")
	fs.Parse(args)

	if *rawURL == "" {
		fmt.Fprintln(os.Stderr, "usage: ferret copart from-url -url <copart URL> [-out lots.json] [-proxy URL]")
		os.Exit(1)
	}

	br, err := browser.New(browser.Options{
		Headless: true,
		UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
			"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
		ProxyURL: *proxy,
	})
	if err != nil {
		fatal("launch browser", err)
	}
	defer br.Close()

	email := mustEnv("COPART_EMAIL")
	password := mustEnv("COPART_PASSWORD")
	sc := copart.New(br, email, password, *cookiePath)
	if err := sc.LoadSession(ctx); err != nil {
		slog.Warn("no saved session — run: ferret copart login", "err", err)
	}

	lots, err := sc.RunSearchURL(ctx, *rawURL, *maxPages)
	if err != nil {
		fatal("scrape url", err)
	}
	ranked := scoring.RankAll(lots)
	slog.Info("from-url complete", "total_lots", len(ranked))

	out := os.Stdout
	if *outFile != "" {
		f, err := os.Create(*outFile)
		if err != nil {
			fatal("create output file", err)
		}
		defer f.Close()
		out = f
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	enc.Encode(ranked)
	if *outFile != "" {
		fmt.Fprintf(os.Stderr, "wrote %d ranked lots to %s\n", len(ranked), *outFile)
	}
}

func runCopartBid(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("copart bid", flag.ExitOnError)
	cookiePath := fs.String("cookies", copart.DefaultCookiePath, "cookie file path")
	fs.Parse(args)

	if len(fs.Args()) == 0 {
		fmt.Fprintln(os.Stderr, "usage: ferret copart bid <lot_number>")
		os.Exit(1)
	}
	lotNumber := fs.Args()[0]

	email := mustEnv("COPART_EMAIL")
	password := mustEnv("COPART_PASSWORD")

	br, err := browser.New(browser.Options{
		Headless: true,
		UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
			"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
	})
	if err != nil {
		fatal("launch browser", err)
	}
	defer br.Close()

	sc := copart.New(br, email, password, *cookiePath)
	if err := sc.LoadSession(ctx); err != nil {
		slog.Warn("no saved session", "err", err)
	}

	lotURL := "https://www.copart.com/lot/" + lotNumber
	detail, err := sc.ScrapeDetail(ctx, lotURL, "") // no image dir → skip download
	if err != nil {
		out, _ := json.Marshal(map[string]any{"error": err.Error(), "lot": lotNumber})
		fmt.Println(string(out))
		os.Exit(1)
	}

	out := map[string]any{
		"lot":         detail.LotNumber,
		"current_bid": detail.CurrentBid,
		"sale_date":   detail.SaleDate,
		"sale_status": detail.SaleStatus,
		"lot_url":     detail.LotURL,
		"fetched_at":  time.Now().UTC().Format(time.RFC3339),
	}
	b, _ := json.Marshal(out)
	fmt.Println(string(b))
}

func runCopartAnalyze(_ context.Context, args []string) {
	fs := flag.NewFlagSet("copart analyze", flag.ExitOnError)
	inFile := fs.String("in", "lots-ranked.json", "ranked lots JSON from 'copart search'")
	dataDir := fs.String("data", "data", "data directory (for raw/ and images/)")
	outFile := fs.String("out", "lots-analyzed.json", "output file with scores + damage")
	fs.Parse(args)

	apiKey := mustEnv("ANTHROPIC_API_KEY")
	az := damage.New(apiKey)

	f, err := os.Open(*inFile)
	if err != nil {
		fatal("open input", err)
	}
	var ranked []scoring.RankedLot
	if err := json.NewDecoder(f).Decode(&ranked); err != nil {
		fatal("decode input", err)
	}
	f.Close()

	type AnalyzedLot struct {
		scoring.RankedLot
		Damage *damage.Report `json:"damage,omitempty"`
	}

	var results []AnalyzedLot
	for _, lot := range ranked {
		// Skip lots marked "future" (not yet assigned to an auction date).
		detailPath := filepath.Join(*dataDir, "raw", lot.LotNumber, "detail.json")
		if f, err := os.Open(detailPath); err == nil {
			var d struct {
				SaleStatus string `json:"sale_status"`
				SaleDate   string `json:"sale_date"`
			}
			if json.NewDecoder(f).Decode(&d) == nil {
				if d.SaleStatus == "future" {
					slog.Info("skipping future lot", "lot", lot.LotNumber)
					f.Close()
					continue
				}
				if d.SaleDate != "" {
					lot.SaleDate = d.SaleDate
				}
			}
			f.Close()
		}

		zipPath := filepath.Join(*dataDir, "images", lot.LotNumber+".zip")
		reportPath := filepath.Join(*dataDir, "raw", lot.LotNumber, "damage_report.json")

		al := AnalyzedLot{RankedLot: lot}

		// Load cached report if present
		var report *damage.Report
		if rb, err := os.ReadFile(reportPath); err == nil {
			var r damage.Report
			if json.Unmarshal(rb, &r) == nil {
				report = &r
			}
		}

		// Run analysis if no cached report and zip exists
		if report == nil {
			if _, err := os.Stat(zipPath); err == nil {
				r, err := az.Analyze(lot.LotNumber, zipPath)
				if err != nil {
					slog.Warn("analyze failed", "lot", lot.LotNumber, "err", err)
				} else {
					report = r
					// Cache it
					if rb, err := json.MarshalIndent(r, "", "  "); err == nil {
						os.WriteFile(reportPath, rb, 0o644) //nolint:errcheck
					}
					slog.Info("analyzed", "lot", lot.LotNumber, "severity", r.Severity,
						"cost", fmt.Sprintf("$%d–$%d", r.RepairCostLow, r.RepairCostHigh))
				}
			} else {
				slog.Warn("no zip found, skipping vision", "lot", lot.LotNumber)
			}
		} else {
			slog.Info("cached report", "lot", lot.LotNumber, "severity", report.Severity)
		}

		al.Damage = report

		// Re-score with damage severity
		severity := ""
		if report != nil {
			severity = report.Severity
		}
		al.Score = scoring.RankWithDamage(lot.Lot, severity)
		results = append(results, al)
	}

	// Sort best-first by updated score
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score.Total > results[j].Score.Total
	})

	out, err := os.Create(*outFile)
	if err != nil {
		fatal("create output", err)
	}
	defer out.Close()
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	enc.Encode(results)
	fmt.Fprintf(os.Stderr, "wrote %d analyzed lots to %s\n", len(results), *outFile)
}

func runMarketScrape(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("market scrape", flag.ExitOnError)
	dataDir := fs.String("data", "data", "data directory")
	fs.Parse(args)

	compsPath := filepath.Join(*dataDir, "market-comps.json")
	existing := market.Load(compsPath)

	br, err := browser.New(browser.Options{
		Headless:  true,
		UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
	})
	if err != nil {
		fatal("launch browser", err)
	}
	defer br.Close()

	sc := market.New(br, market.DefaultConfig)
	updated := sc.ScrapeAll(ctx, existing)

	if err := updated.Save(compsPath); err != nil {
		fatal("save comps", err)
	}
	fmt.Fprintf(os.Stderr, "saved %d comp entries to %s\n", len(updated), compsPath)
}

func runCopartReport(args []string) {
	fs := flag.NewFlagSet("copart report", flag.ExitOnError)
	inFile := fs.String("in", "lots-analyzed.json", "analyzed lots JSON from 'copart analyze'")
	outDir := fs.String("out-dir", "reports", "directory to write HTML reports")
	dataDir := fs.String("data", "data", "data directory (for market-comps.json)")
	topN := fs.Int("top", 10, "number of top lots to include")
	upcomingPath := fs.String("upcoming", "", "path to write upcoming.html watchlist (e.g. /var/www/autoarb/upcoming.html)")
	fs.Parse(args)

	comps := market.Load(filepath.Join(*dataDir, "market-comps.json"))

	f, err := os.Open(*inFile)
	if err != nil {
		fatal("open input", err)
	}
	defer f.Close()

	type analyzedLot struct {
		scoring.RankedLot
		Damage *damage.Report `json:"damage,omitempty"`
	}
	var raw []analyzedLot
	if err := json.NewDecoder(f).Decode(&raw); err != nil {
		fatal("decode input", err)
	}

	// Build report.Lot for ALL analyzed lots (not just top-N) so the upcoming
	// page — the curated soonest-deals list — can consider every lot. The HTML
	// report itself still shows only the top-N.
	allLots := make([]report.Lot, len(raw))
	for i, al := range raw {
		bid := int(al.CurrentBid)
		repairMid := 0
		if al.Damage != nil {
			repairMid = (al.Damage.RepairCostLow + al.Damage.RepairCostHigh) / 2
		}

		var compsArg scoring.Comps
		if len(comps) > 0 {
			compsArg = comps
		}
		resale := scoring.EstimateResale(al.Year, al.Make, al.Model, al.Odometer, compsArg)
		maxBid := scoring.MaxBid(resale, repairMid, 0.50)
		roiAt80 := scoring.ROIPercent(maxBid*80/100, repairMid, resale)

		allLots[i] = report.Lot{
			RankedLot:  al.RankedLot,
			Damage:     al.Damage,
			EstResale:  resale,
			MaxBid:     maxBid,
			ROIAt80Pct: roiAt80,
		}
		if al.Damage != nil {
			allLots[i].TotalCostLow = bid + al.Damage.RepairCostLow
			allLots[i].TotalCostHigh = bid + al.Damage.RepairCostHigh
		}
	}

	// Backfill thumbnails from zip for lots the search page lazy-loaded.
	for i := range allLots {
		if allLots[i].ThumbnailURL == "" {
			zipPath := filepath.Join(*dataDir, "images", allLots[i].LotNumber+".zip")
			if img, err := firstImageFromZip(zipPath); err == nil {
				allLots[i].ThumbnailURL = "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(img)
			}
		}
	}

	date := time.Now().Format("2006-01-02")
	reportFile := date + ".html"

	// Deep-dive page for every analyzed lot; also backfill SaleDate from detail.json.
	for i := range allLots {
		dp := buildDetailPage(allLots[i], *dataDir, reportFile)
		if dp.SaleDate != "" {
			allLots[i].SaleDate = dp.SaleDate
		}
		detailPath := filepath.Join(*outDir, "lot-"+allLots[i].LotNumber+".html")
		if err := report.GenerateDetail(dp, detailPath); err != nil {
			slog.Warn("detail page failed", "lot", allLots[i].LotNumber, "err", err)
		} else {
			slog.Info("detail page written", "lot", allLots[i].LotNumber)
		}
	}

	// HTML report: top-N only.
	n := *topN
	if n > len(allLots) {
		n = len(allLots)
	}
	outPath := filepath.Join(*outDir, reportFile)
	if err := report.Generate(allLots[:n], outPath); err != nil {
		fatal("generate report", err)
	}
	fmt.Fprintf(os.Stderr, "report saved to %s\n", outPath)
	fmt.Println(outPath)

	// Upcoming = curated soonest-deals list, built from ALL analyzed lots.
	if *upcomingPath != "" {
		var upcoming []report.UpcomingLot
		for _, lot := range allLots {
			upcoming = append(upcoming, report.UpcomingLot{
				Lot:       lot,
				DetailURL: "/ferret/lot-" + lot.LotNumber + ".html",
			})
		}
		if err := report.GenerateUpcoming(upcoming, *upcomingPath); err != nil {
			slog.Warn("upcoming page failed", "err", err)
		} else {
			fmt.Fprintf(os.Stderr, "upcoming page written to %s\n", *upcomingPath)
		}
	}
}

func runCopartCheck(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("copart check", flag.ExitOnError)
	cookiePath := fs.String("cookies", copart.DefaultCookiePath, "cookie file path")
	fs.Parse(args)

	email := mustEnv("COPART_EMAIL")
	password := mustEnv("COPART_PASSWORD")

	br, err := browser.New(browser.Options{
		Headless: true,
		UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
			"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
	})
	if err != nil {
		fatal("launch browser", err)
	}
	defer br.Close()

	sc := copart.New(br, email, password, *cookiePath)
	if err := sc.LoadSession(ctx); err != nil {
		slog.Warn("no saved session, will require login", "err", err)
	}

	ok, err := sc.IsLoggedIn(ctx)
	if err != nil {
		fatal("check session", err)
	}
	if ok {
		fmt.Println("✓ session is valid")
	} else {
		fmt.Println("✗ session expired — run: ferret copart login")
		os.Exit(1)
	}
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		fmt.Fprintf(os.Stderr, "error: %s is not set (add to .env or export it)\n", key)
		os.Exit(1)
	}
	return v
}

func fatal(msg string, err error) {
	slog.Error(msg, "err", err)
	os.Exit(1)
}

func usage() {
	fmt.Fprintln(os.Stderr, `ferret — scrape engine

Usage:
  ferret copart login   [-headless] [-cookies path]                     login and save session
  ferret copart check   [-cookies path]                                 verify saved session
  ferret copart search  [-makes A,B] [-year-min N] [-odo-max N]        search lots
                        [-days N] [-damage CODE] [-title CODE]
                        [-pages N] [-out file.json]
  ferret copart detail  -lot NUMBER | -from search.json                 scrape lot details
                        [-workers N] [-data dir] [-images=false]
  ferret copart analyze [-in lots-ranked.json] [-data dir] [-out lots-analyzed.json]

Credentials are read from env vars or .env file:
  COPART_EMAIL, COPART_PASSWORD`)
}

func runValueCarfax(args []string) {
	fs := flag.NewFlagSet("value carfax", flag.ExitOnError)
	zip := fs.String("zip", "60601", "zip code for regional pricing")
	fs.Parse(args)
	if len(fs.Args()) == 0 {
		fmt.Fprintln(os.Stderr, "usage: ferret value carfax <VIN> [-zip XXXXX]")
		os.Exit(1)
	}
	vin := fs.Args()[0]
	result, err := valuation.ScrapeCarfaxValue(vin, *zip)
	if err != nil {
		b, _ := json.Marshal(map[string]string{"error": err.Error(), "vin": vin})
		fmt.Println(string(b))
		os.Exit(1)
	}
	b, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(b))
}

func runValueAuctionHistory(args []string) {
	fs := flag.NewFlagSet("value auction-history", flag.ExitOnError)
	makeName := fs.String("make", "", "vehicle make, e.g. TOYOTA")
	model := fs.String("model", "", "vehicle model, e.g. RAV4")
	year := fs.Int("year", 0, "model year")
	proxy := fs.String("proxy", os.Getenv("SALESHISTORY_PROXY"),
		"residential proxy URL (required from datacenter IPs; Cloudflare)")
	fs.Parse(args)

	if *makeName == "" || *model == "" || *year == 0 {
		fmt.Fprintln(os.Stderr, "usage: ferret value auction-history -make TOYOTA -model RAV4 -year 2022 [-proxy URL]")
		os.Exit(1)
	}

	result, err := valuation.ScrapeAuctionHistory(*makeName, *model, *year, *proxy)
	if err != nil {
		b, _ := json.Marshal(map[string]string{"error": err.Error()})
		fmt.Println(string(b))
		os.Exit(1)
	}
	b, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(b))
}

func runValueKBB(args []string) {
	fs := flag.NewFlagSet("value kbb", flag.ExitOnError)
	makeName := fs.String("make", "", "vehicle make, e.g. TOYOTA")
	model := fs.String("model", "", "vehicle model, e.g. RAV4")
	year := fs.Int("year", 0, "model year")
	trim := fs.String("trim", "", "trim, e.g. XLE (optional)")
	proxy := fs.String("proxy", os.Getenv("KBB_PROXY"),
		"residential proxy URL (defaults to SALESHISTORY_PROXY if unset)")
	fs.Parse(args)

	if *proxy == "" {
		*proxy = os.Getenv("SALESHISTORY_PROXY")
	}
	if *makeName == "" || *model == "" || *year == 0 {
		fmt.Fprintln(os.Stderr, "usage: ferret value kbb -make TOYOTA -model RAV4 -year 2022 [-trim XLE] [-proxy URL]")
		os.Exit(1)
	}

	result, err := valuation.ScrapeKBB(*makeName, *model, *year, *trim, *proxy)
	if err != nil {
		b, _ := json.Marshal(map[string]string{"error": err.Error()})
		fmt.Println(string(b))
		os.Exit(1)
	}
	b, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(b))
}

// firstImageFromZip returns the raw bytes of the first JPEG in a zip archive.
func firstImageFromZip(zipPath string) ([]byte, error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	for _, f := range r.File {
		name := strings.ToLower(f.Name)
		if strings.HasSuffix(name, ".jpg") || strings.HasSuffix(name, ".jpeg") {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			data, err := io.ReadAll(rc)
			rc.Close()
			return data, err
		}
	}
	return nil, fmt.Errorf("no jpeg found in %s", zipPath)
}

// allImagesFromZip reads all JPEGs from a zip and returns them as base64 data URIs.
func allImagesFromZip(zipPath string) []template.URL {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil
	}
	defer r.Close()

	var files []*zip.File
	for _, f := range r.File {
		name := strings.ToLower(f.Name)
		if strings.HasSuffix(name, ".jpg") || strings.HasSuffix(name, ".jpeg") {
			files = append(files, f)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })

	var out []template.URL
	for _, f := range files {
		rc, err := f.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			continue
		}
		out = append(out, template.URL("data:image/jpeg;base64,"+base64.StdEncoding.EncodeToString(data)))
	}
	return out
}

// detailJSON is the subset of fields we need from data/raw/<lot>/detail.json.
type detailJSON struct {
	VIN             string `json:"vin"`
	EngineType      string `json:"engine_type"`
	Transmission    string `json:"transmission"`
	Color           string `json:"color"`
	BodyStyle       string `json:"body_style"`
	DriveType       string `json:"drive_type"`
	FuelType        string `json:"fuel_type"`
	RunAndDrive     string `json:"run_and_drive"`
	KeysPresent     string `json:"keys_present"`
	AirbagsDeployed string `json:"airbags_deployed"`
	ConditionGrade  string `json:"condition_grade"`
	LossType        string `json:"loss_type"`
	SaleDate        string `json:"sale_date"`
	ExteriorCondition []struct {
		Panel  string `json:"panel"`
		Damage string `json:"damage"`
		Count  string `json:"count"`
	} `json:"exterior_condition"`
}

// buildDetailPage assembles a DetailPage for a single lot.
func buildDetailPage(lot report.Lot, dataDir, reportFile string) report.DetailPage {
	dp := report.DetailPage{
		Lot:        lot,
		ReportPath: reportFile,
	}

	// Load detail JSON
	detailPath := filepath.Join(dataDir, "raw", lot.LotNumber, "detail.json")
	if f, err := os.Open(detailPath); err == nil {
		var d detailJSON
		if err := json.NewDecoder(f).Decode(&d); err == nil {
			dp.VIN = d.VIN
			dp.EngineType = d.EngineType
			dp.Transmission = d.Transmission
			dp.Color = d.Color
			dp.BodyStyle = d.BodyStyle
			dp.DriveType = d.DriveType
			dp.FuelType = d.FuelType
			dp.RunAndDrive = d.RunAndDrive
			dp.KeysPresent = d.KeysPresent
			dp.AirbagsDeployed = d.AirbagsDeployed
			dp.ConditionGrade = d.ConditionGrade
			dp.LossType = d.LossType
			if d.SaleDate != "" {
				dp.SaleDate = d.SaleDate
			}
			for _, p := range d.ExteriorCondition {
				dp.ExteriorPanels = append(dp.ExteriorPanels, report.PanelItem{
					Panel:  p.Panel,
					Damage: p.Damage,
					Count:  p.Count,
				})
			}
		}
		f.Close()
	}

	// Load all images
	zipPath := filepath.Join(dataDir, "images", lot.LotNumber+".zip")
	dp.GalleryImages = allImagesFromZip(zipPath)

	return dp
}

// loadDotEnv reads key=value pairs from path into the process environment.
// Silently skips if the file doesn't exist.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(strings.Trim(v, `"`))
		if os.Getenv(k) == "" { // don't override existing env
			os.Setenv(k, v)
		}
	}
}
