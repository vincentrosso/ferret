package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/vincentrosso/ferret/internal/browser"
	"github.com/vincentrosso/ferret/scrapers/copart"
	"github.com/vincentrosso/ferret/store"
)

func main() {
	if len(os.Args) < 3 {
		usage()
		os.Exit(1)
	}

	loadDotEnv(".env")

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	switch os.Args[1] + " " + os.Args[2] {
	case "copart login":
		runCopartLogin(ctx, os.Args[3:])
	case "copart check":
		runCopartCheck(ctx, os.Args[3:])
	case "copart search":
		runCopartSearch(ctx, os.Args[3:])
	case "copart detail":
		runCopartDetail(ctx, os.Args[3:])
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
	makes := fs.String("makes", "HONDA,TOYOTA,NISSAN,CHEVROLET,ACURA", "comma-separated makes")
	yearMin := fs.Int("year-min", 0, "minimum model year (default: 4 years ago)")
	yearMax := fs.Int("year-max", 0, "maximum model year (default: current year + 2)")
	odoMax := fs.Int("odo-max", 85_000, "max odometer miles")
	daysAhead := fs.Int("days", 14, "auction date window: today + N days")
	damage := fs.String("damage", "DAMAGECODE_HL", "damage type code")
	title := fs.String("title", "TITLEGROUP_C", "title group code")
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
		DamageCode: *damage,
		TitleGroup: *title,
		MaxPages:   *maxPages,
	}

	lots, err := sc.RunSearch(ctx, params)
	if err != nil {
		fatal("search", err)
	}
	slog.Info("search complete", "total_lots", len(lots))

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	out := os.Stdout
	if *outFile != "" {
		f, err := os.Create(*outFile)
		if err != nil {
			fatal("create output file", err)
		}
		defer f.Close()
		out = f
		enc = json.NewEncoder(out)
		enc.SetIndent("", "  ")
	}
	enc.Encode(lots)
	if *outFile != "" {
		fmt.Fprintf(os.Stderr, "wrote %d lots to %s\n", len(lots), *outFile)
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
				detail, err := sc.ScrapeDetail(ctx, j.url)
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
						"vin", detail.VIN, "images", len(detail.ImageURLs))
				}

				if *images && len(detail.ImageURLs) > 0 {
					paths, err := st.DownloadImages(ctx, detail.LotNumber, detail.ImageURLs)
					if err != nil {
						slog.Warn("image download partial", "lot", detail.LotNumber, "err", err)
					}
					slog.Info("images downloaded", "lot", detail.LotNumber, "count", len(paths))
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

Credentials are read from env vars or .env file:
  COPART_EMAIL, COPART_PASSWORD`)
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
