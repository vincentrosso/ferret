package copart

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/vincentrosso/ferret/internal/browser"
	"github.com/vincentrosso/ferret/internal/scraper"
)

const (
	baseURL           = "https://www.copart.com"
	loginURL          = "https://www.copart.com/login"
	DefaultCookiePath = "data/copart-session.json"
)

type Scraper struct {
	br         *browser.Browser
	email      string
	password   string
	cookiePath string
}

func New(br *browser.Browser, email, password, cookiePath string) *Scraper {
	if cookiePath == "" {
		cookiePath = DefaultCookiePath
	}
	return &Scraper{br: br, email: email, password: password, cookiePath: cookiePath}
}

func (s *Scraper) Name() string { return "copart" }

// Login follows the DevTools-recorded click-through flow:
// homepage → "Sign in" dropdown → "Member" → fill form → submit.
// Run with headless=false the first time in case of CAPTCHA.
func (s *Scraper) Login(ctx context.Context) error {
	page, err := s.br.NewPage(baseURL)
	if err != nil {
		return fmt.Errorf("open homepage: %w", err)
	}
	defer page.Close() //nolint:errcheck

	slog.Info("waiting for homepage to load...")
	if err := page.WaitLoad(); err != nil {
		return fmt.Errorf("homepage load: %w", err)
	}
	time.Sleep(1 * time.Second)

	// Click the "Sign in" dropdown button
	slog.Info("clicking Sign in...")
	signInBtn, err := page.Timeout(15 * time.Second).Element(`div.desktopScreen div.z-i-999 > button`)
	if err != nil {
		return fmt.Errorf("sign-in button not found: %w", err)
	}
	if err := signInBtn.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("click sign-in: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Click the "Member" link — this is an <a> tag that does a full navigation to /login
	slog.Info("clicking Member...")
	memberLink, err := page.Timeout(10 * time.Second).Element(`div.desktopScreen div.z-i-999 > div a:nth-of-type(1)`)
	if err != nil {
		return fmt.Errorf("member link not found: %w", err)
	}
	navWait := page.WaitNavigation(proto.PageLifecycleEventNameNetworkIdle)
	if err := memberLink.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("click member: %w", err)
	}
	navWait()
	time.Sleep(1 * time.Second) // give Angular time to render the form

	slog.Info("waiting for login form...")
	emailEl, err := page.Timeout(20 * time.Second).Element("#email-member-number")
	if err != nil {
		return fmt.Errorf("email input not found: %w", err)
	}

	slog.Info("filling credentials")
	// Click + Input triggers Angular's synthetic events more reliably than JS setter alone.
	if err := emailEl.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("focus email: %w", err)
	}
	if err := emailEl.Input(s.email); err != nil {
		return fmt.Errorf("fill email: %w", err)
	}
	time.Sleep(300 * time.Millisecond)

	pwEl, err := page.Element("#member-password")
	if err != nil {
		return fmt.Errorf("password input not found: %w", err)
	}
	if err := pwEl.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("focus password: %w", err)
	}
	if err := pwEl.Input(s.password); err != nil {
		return fmt.Errorf("fill password: %w", err)
	}
	time.Sleep(300 * time.Millisecond)

	// Submit button inside copart-signin component
	submitEl, err := page.Element(`copart-signin > div > div button`)
	if err != nil {
		submitEl, err = page.Element(`button[type="submit"]`)
		if err != nil {
			return fmt.Errorf("submit button not found: %w", err)
		}
	}

	slog.Info("submitting, waiting for redirect...")
	wait := page.WaitNavigation(proto.PageLifecycleEventNameNetworkIdle)
	if err := submitEl.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("click submit: %w", err)
	}
	wait()

	info, err := page.Info()
	if err != nil {
		return fmt.Errorf("get page info: %w", err)
	}
	if strings.Contains(info.URL, "/login") {
		return fmt.Errorf("login failed: still on login page (%s)", info.URL)
	}
	slog.Info("login succeeded", "url", info.URL)

	res, err := proto.StorageGetCookies{}.Call(page)
	if err != nil {
		return fmt.Errorf("get cookies: %w", err)
	}
	if err := saveCookies(res.Cookies, s.cookiePath); err != nil {
		return fmt.Errorf("save cookies: %w", err)
	}
	slog.Info("session saved", "path", s.cookiePath, "cookies", len(res.Cookies))
	return nil
}

// LoadSession restores a saved cookie session into the browser.
func (s *Scraper) LoadSession(ctx context.Context) error {
	params, err := loadCookies(s.cookiePath)
	if err != nil {
		return fmt.Errorf("load cookies from %s: %w", s.cookiePath, err)
	}
	setCookies := proto.StorageSetCookies{Cookies: params}
	if err := setCookies.Call(s.br.Rod()); err != nil {
		return fmt.Errorf("set cookies: %w", err)
	}
	slog.Info("session loaded", "cookies", len(params))
	return nil
}

// IsLoggedIn navigates to the dashboard and checks whether we're authenticated.
// It waits for either a member-only element or a redirect to /login.
func (s *Scraper) IsLoggedIn(ctx context.Context) (bool, error) {
	page, err := s.br.NewPage(baseURL + "/dashboard")
	if err != nil {
		return false, err
	}
	defer page.Close() //nolint:errcheck

	// Wait up to 15s for the page to settle, then check URL.
	page.Timeout(15 * time.Second).WaitLoad() //nolint: errcheck — timeout is fine
	info, err := page.Info()
	if err != nil {
		return false, err
	}
	if strings.Contains(info.URL, "/login") {
		return false, nil
	}
	// Confirm an authenticated element exists (guards against soft-redirects).
	_, err = page.Timeout(5 * time.Second).Element(`copart-dashboard, [class*="dashboard"]`)
	return err == nil, nil
}

// RunSearch navigates to a pre-filtered Copart search URL and paginates through results.
func (s *Scraper) RunSearch(ctx context.Context, params SearchParams) ([]Lot, error) {
	searchURL, err := params.BuildURL()
	if err != nil {
		return nil, fmt.Errorf("build url: %w", err)
	}
	slog.Info("search", "makes", params.Makes, "yearMin", params.YearMin, "odoMax", params.OdoMax)
	return s.RunSearchURL(ctx, searchURL, params.MaxPages)
}

// RunSearchURL scrapes lots from ANY pre-built Copart results URL — a search,
// a saleListResult, or a member-saved filter. Paginates through all pages.
func (s *Scraper) RunSearchURL(ctx context.Context, rawURL string, maxPages int) ([]Lot, error) {
	page, err := s.br.NewPage(rawURL)
	if err != nil {
		return nil, fmt.Errorf("open search page: %w", err)
	}
	defer page.Close() //nolint:errcheck

	// Wait for Angular to populate the table. Through a (slow, rotating)
	// residential proxy the first load can be slow or land on a throttled IP,
	// so allow more time and reload once before giving up.
	rowSel := `table tbody tr`
	if _, err := page.Timeout(40 * time.Second).Element(rowSel); err != nil {
		slog.Warn("results table slow — reloading once", "err", err)
		_ = page.Navigate(rawURL)
		if _, err := page.Timeout(40 * time.Second).Element(rowSel); err != nil {
			return nil, fmt.Errorf("no results table after 2×40s — may be throttled/login: %w", err)
		}
	}
	time.Sleep(1500 * time.Millisecond) // let all rows render

	// Bump the page size to 100 up front: a ~500-lot search becomes ~5 pages
	// instead of ~25, and the per-page nav cost through the residential proxy
	// dominates, so fewer pages is the single biggest speedup (and what was
	// blowing the scrape past its timeout).
	setRowsPerPage(page, 100)

	var lots []Lot
	seen := map[string]bool{}

	for pageNum := 1; ; pageNum++ {
		if maxPages > 0 && pageNum > maxPages {
			break
		}

		// Scroll through the table to trigger lazy-loaded row thumbnails, then
		// return to top — otherwise rows below the fold have no img src yet.
		for y := 0; y < 9000; y += 700 {
			page.Eval(`(y) => window.scrollTo(0, y)`, y) //nolint:errcheck
			time.Sleep(120 * time.Millisecond)
		}
		page.Eval(`() => window.scrollTo(0, 0)`) //nolint:errcheck
		time.Sleep(700 * time.Millisecond)

		pageLots, err := extractRows(ctx, page, seen)
		if err != nil {
			slog.Warn("row extraction error", "page", pageNum, "err", err)
		}
		slog.Info("page scraped", "page", pageNum, "lots", len(pageLots), "total", len(lots)+len(pageLots))
		lots = append(lots, pageLots...)

		// Stop as soon as a page yields no new rows — Copart's next button stays
		// enabled even on the last page, so we can't rely on it being disabled.
		if len(pageLots) == 0 {
			break
		}

		prevFirst := firstLotHref(page)
		if !nextPage(page) {
			break
		}
		// Wait for the server-side redraw to actually swap the rows in. Through
		// a slow residential proxy the repaint can take 30s+; a fixed short wait
		// re-reads the old page, which looks like "no new rows" and ends the
		// scrape after page 1. On the true last page the (always-enabled) next
		// click changes nothing — the timeout is what detects that.
		if !waitRowsChanged(page, prevFirst, 40*time.Second) {
			slog.Info("rows unchanged after next click — last page", "page", pageNum)
			break
		}
	}

	return lots, nil
}

// firstLotHref returns the first lot link href in the results table ("" if none).
func firstLotHref(page *rod.Page) string {
	el, err := page.Timeout(2 * time.Second).Element(`table tbody tr a[href*="/lot/"]`)
	if err != nil {
		return ""
	}
	href, err := el.Attribute("href")
	if err != nil || href == nil {
		return ""
	}
	return *href
}

// waitRowsChanged polls until the table's first lot href differs from prev
// (the server-side redraw landed) or the timeout elapses.
func waitRowsChanged(page *rod.Page, prev string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if h := firstLotHref(page); h != "" && h != prev {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// extractRows pulls all lot rows from the current page state.
func extractRows(_ context.Context, page *rod.Page, seen map[string]bool) ([]Lot, error) {
	rows, err := page.Elements(`table tbody tr`)
	if err != nil || len(rows) == 0 {
		// Fallback: any row containing a /lot/ link
		rows, err = page.Elements(`tr`)
		if err != nil {
			return nil, err
		}
	}

	// Resolve columns by header text — Copart reorders search columns periodically,
	// so fixed indices drift. Falls back to the known layout if the header is absent.
	var headers []string
	if ths, err := page.Elements(`table thead th`); err == nil {
		for _, th := range ths {
			if t, err := th.Text(); err == nil {
				headers = append(headers, t)
			}
		}
	}
	cols := buildColMap(headers)

	now := time.Now()
	var lots []Lot

	for _, row := range rows {
		link, err := row.Element(`a[href*="/lot/"]`)
		if err != nil {
			continue
		}
		lotURL, _ := link.Attribute("href")
		if lotURL == nil || *lotURL == "" {
			continue
		}
		if seen[*lotURL] {
			continue
		}
		seen[*lotURL] = true

		// Build absolute URL if needed
		href := *lotURL
		if !strings.HasPrefix(href, "http") {
			href = baseURL + href
		}

		// Collect cell texts
		cells, _ := row.Elements(`td`)
		cellTexts := make([]string, len(cells))
		for i, c := range cells {
			if t, err := c.Text(); err == nil {
				cellTexts[i] = t
			}
		}

		lot, ok := parseLotRow(href, cellTexts, cols, now)
		if !ok {
			continue
		}

		// Thumbnail
		if img, err := row.Element(`img`); err == nil {
			if src, err := img.Attribute("src"); err == nil && src != nil && *src != "" {
				lot.ThumbnailURL = *src
			} else if ds, err := img.Attribute("data-src"); err == nil && ds != nil {
				lot.ThumbnailURL = *ds
			}
		}

		lots = append(lots, lot)
	}
	return lots, nil
}

// setRowsPerPage sets Copart's classic-view results page size to n (e.g. 100).
//
// The nationwide hail search loads Copart's CLASSIC results grid — a jQuery
// DataTables table (#serverSideDataTable) whose page-size control is a native
// <select name="serverSideDataTable_length"> with options up to 100. (Confirmed
// by live DOM probe, 2026-06: the default view IS classic; the "New list view"
// toggle switches to a WORSE 6-column card grid with no size control, so we must
// NOT toggle.) Setting the select's value and firing a change event triggers a
// DataTables server-side redraw at the new size: a ~500-lot search becomes ~5
// pages instead of ~25 — the single biggest speedup through the residential proxy.
//
// Best-effort: if the control isn't found we stay at the default size — never fatal.
func setRowsPerPage(page *rod.Page, n int) {
	const sel = `select[name="serverSideDataTable_length"]`
	want := fmt.Sprintf("%d", n)

	// If we somehow landed on the modern card view (no native length select),
	// toggle back to classic so the control is present. The toggle button reads
	// "Classic view" only when we're currently on the modern view.
	if _, err := page.Timeout(2 * time.Second).Element(sel); err != nil {
		if btn, err := page.Timeout(2*time.Second).ElementR(`button.search_result_toggle_icon`, "Classic view"); err == nil {
			slog.Info("modern view detected — toggling to classic for page-size control")
			_ = btn.Click(proto.InputMouseButtonLeft, 1)
			time.Sleep(3 * time.Second)
		}
	}

	lenSel, err := page.Timeout(5 * time.Second).Element(sel)
	if err != nil {
		slog.Warn("classic rows-per-page <select> not present — default page size", "err", err)
		return
	}

	// Native <select>: set value + dispatch a bubbling change event so DataTables
	// picks it up and redraws server-side at the new length.
	if _, err := lenSel.Eval(`(want) => {
		this.value = want;
		this.dispatchEvent(new Event('change', { bubbles: true }));
		return this.value;
	}`, want); err != nil {
		slog.Warn("set rows-per-page failed", "err", err)
		return
	}
	slog.Info("rows per page set (classic select)", "n", n)
	time.Sleep(2500 * time.Millisecond) // DataTables server-side redraw at new size
}

func nextPage(page *rod.Page) bool {
	const lookupTimeout = 2 * time.Second

	// Classic DataTables next button: <a id="serverSideDataTable_next"
	// class="paginate_button next [disabled]">. DataTables flags the last page by
	// adding the "disabled" CLASS (not a disabled attribute), so check the class.
	if el, err := page.Timeout(lookupTimeout).Element(`#serverSideDataTable_next`); err == nil {
		if cls, _ := el.Attribute("class"); cls != nil && strings.Contains(*cls, "disabled") {
			return false
		}
		if el.Click(proto.InputMouseButtonLeft, 1) == nil {
			return true
		}
	}

	selectors := []string{
		`[aria-label="Next page"]`,
		`[aria-label="Next"]`,
		`li.next a`,
		`button[class*="next"]`,
		`a[class*="next"]`,
	}
	for _, sel := range selectors {
		el, err := page.Timeout(lookupTimeout).Element(sel)
		if err != nil {
			continue
		}
		disabled, _ := el.Attribute("disabled")
		ariaDisabled, _ := el.Attribute("aria-disabled")
		if disabled != nil || (ariaDisabled != nil && *ariaDisabled == "true") {
			return false
		}
		if err := el.Click(proto.InputMouseButtonLeft, 1); err == nil {
			return true
		}
	}
	// XPath fallback
	el, err := page.Timeout(lookupTimeout).ElementX(
		`//button[normalize-space(text())='Next'] | //a[normalize-space(text())='Next']`,
	)
	if err != nil {
		return false
	}
	disabled, _ := el.Attribute("disabled")
	if disabled != nil {
		return false
	}
	return el.Click(proto.InputMouseButtonLeft, 1) == nil
}

// Search implements scraper.Scraper (generic interface).
func (s *Scraper) Search(ctx context.Context, _ *rod.Page, q scraper.Query) ([]string, error) {
	params := SearchParams{MaxPages: q.MaxPages}
	lots, err := s.RunSearch(ctx, params)
	if err != nil {
		return nil, err
	}
	urls := make([]string, len(lots))
	for i, l := range lots {
		urls[i] = l.LotURL
	}
	return urls, nil
}

// Detail implements scraper.Scraper — stub until detail parsing is built.
func (s *Scraper) Detail(_ context.Context, _ *rod.Page, _ string) (scraper.Result, error) {
	return nil, fmt.Errorf("Detail not yet implemented")
}

// jsSetValue fills a React-controlled input via the native property setter,
// which triggers React's synthetic onChange without full keyboard simulation.
func jsSetValue(el *rod.Element, value string) error {
	_, err := el.Eval(`function(val) {
		var nativeSetter = Object.getOwnPropertyDescriptor(
			window.HTMLInputElement.prototype, 'value').set;
		nativeSetter.call(this, val);
		this.dispatchEvent(new Event('input', { bubbles: true }));
		this.dispatchEvent(new Event('change', { bubbles: true }));
	}`, value)
	return err
}
