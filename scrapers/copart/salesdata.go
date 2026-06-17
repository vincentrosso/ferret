package copart

// salesdata.go — bulk Copart "Sales Data" CSV download + filter.
//
// Copart members can download a nationwide CSV of every lot on the upcoming
// sales calendar at /downloadSalesData. One file beats paginating the Angular
// results grid (the RunSearch / from-url path): a single round-trip carries the
// full inventory, with none of the per-page proxy-nav cost that dominates a
// 25-page scrape. We pull it through the logged-in browser (so Incapsula clears
// it like any member click), parse it HEADER-FIRST — Copart reorders and renames
// these columns over time, the same lesson the search table's buildColMap
// encodes — filter to the hail-arb playbook, and emit the same []Lot the search
// path does so everything downstream (detail → score → store) is unchanged.
//
// The exact CSV column set is not pinned here on purpose: the matcher keys on
// substrings we're confident about, and DownloadSalesData logs the real header
// row it sees, so the first live run is self-documenting and the matcher can be
// tuned from the journal rather than guessed blind.

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// salesDataPath is the member endpoint that serves the bulk sales CSV as a
// Content-Disposition attachment. Overridable from the CLI in case Copart moves it.
const salesDataPath = "/downloadSalesData/"

// SalesFilter narrows the nationwide sales file to the hail-arb playbook. Zero
// values mean "don't filter on this field" (except HailOnly, which defaults on
// at the CLI layer — the whole point is hail inventory).
type SalesFilter struct {
	Makes     []string // upper-case allow-list; empty = all makes
	YearMin   int      // inclusive; 0 = no floor
	YearMax   int      // inclusive; 0 = no ceiling
	OdoMax    int      // max odometer; 0 = no cap
	RetailMax float64  // max Copart Est. Retail / ACV (budget gate); 0 = no cap
	HailOnly  bool     // require "hail" in the damage description
	CleanOnly bool     // drop rows whose title reads salvage
}

func (f SalesFilter) makeSet() map[string]bool {
	if len(f.Makes) == 0 {
		return nil
	}
	m := make(map[string]bool, len(f.Makes))
	for _, mk := range f.Makes {
		mk = strings.ToUpper(strings.TrimSpace(mk))
		if mk != "" {
			m[mk] = true
		}
	}
	return m
}

// DownloadSalesData triggers the member CSV download into dir and returns the
// saved file path. rawURL overrides the default endpoint when non-empty.
//
// We navigate the member homepage first so the browser carries the logged-in
// cookies + Incapsula clearance, then navigate to the attachment URL — Chrome
// turns a Content-Disposition:attachment response into a download rather than a
// render, so the page.Navigate is expected to hang/error; the WaitDownload hook
// is what actually resolves. A timeout means we got an Incapsula challenge page
// (or the account isn't member-gated for this export) instead of the file.
func (s *Scraper) DownloadSalesData(ctx context.Context, dir, rawURL string) (string, error) {
	if rawURL == "" {
		rawURL = baseURL + salesDataPath
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("make download dir: %w", err)
	}

	page, err := s.br.NewPage(baseURL + "/")
	if err != nil {
		return "", fmt.Errorf("open copart homepage: %w", err)
	}
	defer page.Close()          //nolint:errcheck
	time.Sleep(2 * time.Second) // let the session settle + Incapsula clear

	dest := filepath.Join(dir, "copart-salesdata.csv")

	slog.Info("sales-data: requesting bulk CSV", "url", rawURL, "dir", dir)
	wait := s.br.Rod().WaitDownload(dir)
	ch := make(chan *proto.PageDownloadWillBegin, 1)
	go func() { ch <- wait() }()
	// If the response is an attachment, Chrome fires a download and the navigate
	// hangs; if it's inline (text/csv with no Content-Disposition, or HTML) the
	// navigate resolves and the body holds the answer. Cover both.
	go func() { _ = page.Navigate(rawURL) }()

	select {
	case info := <-ch:
		if info == nil {
			return "", fmt.Errorf("download began but info was nil")
		}
		src := filepath.Join(dir, info.GUID)
		if err := os.Rename(src, dest); err != nil {
			if _, statErr := os.Stat(dest); statErr == nil {
				return dest, nil
			}
			return src, nil // keep the GUID name rather than lose the file
		}
		return dest, nil
	case <-time.After(90 * time.Second):
		// No attachment download fired. The URL likely rendered inline — peek the
		// page: if it's CSV, save it from the body; else surface the head so we can
		// see exactly what Copart returned (challenge / 404 / login redirect).
		body, finalURL := peekBody(page)
		head := body
		if len(head) > 600 {
			head = head[:600]
		}
		if looksCSV(body) {
			if err := os.WriteFile(dest, []byte(body), 0o644); err != nil {
				return "", fmt.Errorf("save inline CSV: %w", err)
			}
			slog.Info("sales-data: saved inline CSV (no attachment)", "url", finalURL, "bytes", len(body))
			return dest, nil
		}
		slog.Warn("sales-data: no download + non-CSV response", "finalURL", finalURL, "head", strings.ReplaceAll(head, "\n", "\\n"))
		return "", fmt.Errorf("no CSV after 90s (finalURL=%s) — Incapsula challenge, login redirect, or wrong endpoint", finalURL)
	}
}

// peekBody returns the current page's innerText and final URL, best-effort.
func peekBody(page *rod.Page) (string, string) {
	finalURL := ""
	if info, err := page.Info(); err == nil {
		finalURL = info.URL
	}
	body := ""
	if r, err := page.Timeout(8 * time.Second).Eval(`() => document.body ? document.body.innerText : document.documentElement.outerHTML`); err == nil {
		body = r.Value.Str()
	}
	return body, finalURL
}

// looksCSV reports whether s reads as comma-delimited tabular text rather than
// an HTML challenge/error page.
func looksCSV(s string) bool {
	t := strings.ToLower(strings.TrimSpace(s))
	if t == "" || strings.HasPrefix(t, "<!doctype") || strings.HasPrefix(t, "<html") || strings.Contains(t, "incapsula") {
		return false
	}
	first := strings.SplitN(t, "\n", 2)[0]
	return strings.Count(first, ",") >= 3
}

// isLikelyCSV guards against Copart's Incapsula challenge HTML being accepted as
// the CSV — mirrors detail.go's isValidZip. A real sales file is comma-delimited
// text; a challenge comes back as an <html> document.
func isLikelyCSV(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close() //nolint:errcheck
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	head := strings.ToLower(strings.TrimSpace(string(buf[:n])))
	if head == "" {
		return false
	}
	if strings.HasPrefix(head, "<!doctype") || strings.HasPrefix(head, "<html") ||
		strings.Contains(head, "<title>") || strings.Contains(head, "incapsula") {
		return false
	}
	return strings.Contains(head, ",")
}

// buildSalesColMap resolves logical fields to column indices from the CSV header
// row, by case-folded substring — robust to Copart's column reorders/renames.
// Order matters where headers overlap: "secondary damage" is checked before
// "damage", "est. retail"/"acv" before a bare "bid", "odometer brand" before a
// bare "odometer".
func buildSalesColMap(headers []string) colMap {
	m := colMap{}
	set := func(key string, i int) {
		if _, exists := m[key]; !exists { // first matching column wins
			m[key] = i
		}
	}
	for i, h := range headers {
		h = strings.ToLower(strings.TrimSpace(h))
		switch {
		case h == "":
			continue
		case strings.Contains(h, "lot") && (strings.Contains(h, "number") || strings.Contains(h, "lot #") || h == "lot"):
			set("lotnum", i)
		case h == "vin" || strings.Contains(h, "vin"):
			set("vin", i)
		case strings.Contains(h, "secondary") && strings.Contains(h, "damage"):
			set("damage2", i)
		case strings.Contains(h, "damage"):
			set("damage", i)
		case strings.Contains(h, "odometer") && strings.Contains(h, "brand"):
			set("odobrand", i)
		case strings.Contains(h, "odometer") || strings.Contains(h, "mileage"):
			set("odometer", i)
		case strings.Contains(h, "retail") || h == "acv" || strings.Contains(h, "est. retail") || strings.Contains(h, "estimated retail"):
			set("retail", i)
		case strings.Contains(h, "year"):
			set("year", i)
		case strings.Contains(h, "make"):
			set("make", i)
		case strings.Contains(h, "model"):
			set("model", i) // first model col (group/detail) wins
		case strings.Contains(h, "body"):
			set("body", i)
		case strings.Contains(h, "color"):
			set("color", i)
		case strings.Contains(h, "title"):
			set("title", i)
		case strings.Contains(h, "yard") && strings.Contains(h, "name"):
			set("yard", i)
		case strings.Contains(h, "location") && (strings.Contains(h, "city") || strings.Contains(h, "name")):
			set("yard", i)
		case strings.Contains(h, "sale") && strings.Contains(h, "date"):
			set("saledate", i)
		case strings.Contains(h, "buy") && strings.Contains(h, "now"):
			set("binprice", i)
		case strings.Contains(h, "high bid") || (strings.Contains(h, "current") && strings.Contains(h, "bid")):
			set("bid", i)
		case strings.Contains(h, "runs") || strings.Contains(h, "run and drive") || strings.Contains(h, "run & drive"):
			set("runs", i)
		case strings.Contains(h, "keys"):
			set("keys", i)
		case strings.Contains(h, "image") || strings.Contains(h, "thumbnail"):
			set("image", i)
		}
	}
	return m
}

// ParseSalesData reads the downloaded CSV, maps each row to a Lot, applies the
// filter, and returns the survivors. The header row is logged so the first live
// run reveals Copart's real column set.
func ParseSalesData(path string, filter SalesFilter) ([]Lot, error) {
	if !isLikelyCSV(path) {
		return nil, fmt.Errorf("%s is not a CSV (likely an Incapsula challenge page)", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1 // tolerate ragged rows
	r.LazyQuotes = true

	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	cols := buildSalesColMap(header)
	slog.Info("sales-data: parsed header", "columns", len(header), "headers", strings.Join(header, " | "), "resolved", len(cols))
	if _, ok := cols["lotnum"]; !ok {
		return nil, fmt.Errorf("no lot-number column found in header: %v", header)
	}

	makes := filter.makeSet()
	now := time.Now()
	var lots []Lot
	read, kept := 0, 0
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			slog.Warn("sales-data: skipping malformed row", "err", err)
			continue
		}
		read++
		lot, ok := salesRowToLot(rec, cols, now)
		if !ok {
			continue
		}
		if !filter.keep(lot, makes) {
			continue
		}
		lots = append(lots, lot)
		kept++
	}
	slog.Info("sales-data: parse complete", "rows", read, "kept", kept)
	return lots, nil
}

// keep applies the filter to a parsed lot.
func (f SalesFilter) keep(lot Lot, makes map[string]bool) bool {
	if f.HailOnly && !lot.IsHail {
		return false
	}
	if f.CleanOnly && lot.IsSalvage {
		return false
	}
	if f.YearMin > 0 && lot.Year > 0 && lot.Year < f.YearMin {
		return false
	}
	if f.YearMax > 0 && lot.Year > 0 && lot.Year > f.YearMax {
		return false
	}
	if f.OdoMax > 0 && lot.Odometer > f.OdoMax {
		return false
	}
	if f.RetailMax > 0 && lot.EstRetail > f.RetailMax {
		return false
	}
	if makes != nil && !makes[lot.Make] {
		return false
	}
	return true
}

// salesRowToLot maps one CSV record to a Lot via the header-derived column map.
// Returns false when the row carries no usable lot number / make.
func salesRowToLot(rec []string, cols colMap, now time.Time) (Lot, bool) {
	cell := func(key string) string {
		i, ok := cols[key]
		if !ok || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}

	lotNum := reStripNonDigit.ReplaceAllString(cell("lotnum"), "")
	if lotNum == "" {
		return Lot{}, false
	}

	lot := Lot{
		LotNumber: lotNum,
		LotURL:    baseURL + "/lot/" + lotNum,
		Make:      strings.ToUpper(cell("make")),
		Model:     cell("model"),
		ScrapedAt: now,
	}
	if lot.Make == "" {
		return Lot{}, false
	}

	if y, err := strconv.Atoi(cell("year")); err == nil && y > 1980 {
		lot.Year = y
	}
	if lot.Year > 0 {
		lot.Title = strings.TrimSpace(fmt.Sprintf("%d %s %s", lot.Year, lot.Make, lot.Model))
	}
	if odo := reStripNonDigit.ReplaceAllString(cell("odometer"), ""); odo != "" {
		if n, err := strconv.Atoi(odo); err == nil {
			lot.Odometer = n
		}
	}
	lot.DamagePrimary = cell("damage")
	// Title type, e.g. "CT" / "Cert Of Title" / "Salvage".
	if t := cell("title"); t != "" {
		lot.TitleType = strings.TrimSpace(strings.SplitN(t, "-", 2)[0])
	}
	if y := cell("yard"); y != "" {
		lot.YardName = strings.TrimRight(strings.TrimSpace(y), " -")
	}
	if sd := cell("saledate"); sd != "" {
		if d := parseCopartSaleDate(sd); d != "" {
			lot.SaleDate = d
		} else {
			lot.SaleDate = sd
		}
	}
	if r := cell("retail"); r != "" {
		lot.EstRetail = parseMoney(reMoney.FindString(r))
	}
	if b := cell("bid"); b != "" {
		lot.CurrentBid = parseMoney(reMoney.FindString(b))
	}
	if img := cell("image"); strings.HasPrefix(img, "http") {
		lot.ThumbnailURL = img
	}

	lot.IsHail = strings.Contains(strings.ToLower(lot.DamagePrimary), "hail")
	tl := strings.ToLower(cell("title"))
	lot.IsSalvage = strings.Contains(tl, "salvage") || strings.Contains(tl, "sv") || strings.Contains(tl, "junk")

	return lot, true
}
