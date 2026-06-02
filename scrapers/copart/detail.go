package copart

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// DetailResult holds every field extractable from a Copart lot detail page.
type DetailResult struct {
	LotNumber          string       `json:"lot_number"`
	LotURL             string       `json:"lot_url"`
	Year               int          `json:"year,omitempty"`
	Make               string       `json:"make,omitempty"`
	Model              string       `json:"model,omitempty"`
	VIN                string       `json:"vin,omitempty"`
	DamagePrimary      string       `json:"damage_primary,omitempty"`
	DamageSecondary    string       `json:"damage_secondary,omitempty"`
	RunAndDrive        string       `json:"run_and_drive,omitempty"`
	ConditionGrade     string       `json:"condition_grade,omitempty"`
	EngineType         string       `json:"engine_type,omitempty"`
	Transmission       string       `json:"transmission,omitempty"`
	Color              string       `json:"color,omitempty"`
	BodyStyle          string       `json:"body_style,omitempty"`
	Odometer           int          `json:"odometer,omitempty"`
	DriveType          string       `json:"drive_type,omitempty"`
	FuelType           string       `json:"fuel_type,omitempty"`
	LossType           string       `json:"loss_type,omitempty"`
	KeysPresent        string       `json:"keys_present,omitempty"`
	AirbagsDeployed    string       `json:"airbags_deployed,omitempty"`
	ExteriorCondition  []DamageItem `json:"exterior_condition,omitempty"`
	VehicleDetailsText string       `json:"vehicle_details_text,omitempty"`
	ImageURLs          []string     `json:"image_urls,omitempty"`
	ImageZip           string       `json:"image_zip,omitempty"`
	CurrentBid         float64      `json:"current_bid,omitempty"`
	FinalBid           float64      `json:"final_bid,omitempty"`
	SaleStatus         string       `json:"sale_status,omitempty"`
	SaleDate           string       `json:"sale_date,omitempty"`
	IsBIN              bool         `json:"is_bin,omitempty"`
	BuyNowAmount       float64      `json:"buy_now_amount,omitempty"`
	ScrapedAt          time.Time    `json:"scraped_at"`
}

// DamageItem is one entry from the "Exterior condition" panel.
type DamageItem struct {
	Panel  string `json:"panel"`
	Damage string `json:"damage"`
	Count  string `json:"count"`
}

// ── compiled regexes ──────────────────────────────────────────────────────

var (
	reVIN          = regexp.MustCompile(`(?i)VIN[:\s]+([A-HJ-NPR-Z0-9]{17})`)
	reGrade        = regexp.MustCompile(`(?i)(?:Condition\s+)?Grade[:\s]*([\d.]+)`)
	reRunDrive     = regexp.MustCompile(`(?i)Run\s*and\s*Drive[:\s]*(Yes|No)`)
	reDriveType    = regexp.MustCompile(`(?i)\b(FWD|RWD|AWD|4WD|4x4|All[\s-]Wheel|Front[\s-]Wheel|Rear[\s-]Wheel)\b`)
	reFuelType     = regexp.MustCompile(`(?i)Fuel[:\s]*(Gasoline|Gas|Diesel|Electric|Hybrid|Plug-in|PHEV|EV)`)
	reLossType     = regexp.MustCompile(`(?i)Loss\s*Type[:\s]*([A-Za-z &/]+?)(?:\n|$)`)
	reKeys         = regexp.MustCompile(`(?i)Keys?[:\s]*(Yes|No|Available|Not Available|Present|Missing)`)
	reAirbags      = regexp.MustCompile(`(?i)Airbags?[:\s]*(Deployed|Not Deployed|Yes|No)`)
	reSaleDate     = regexp.MustCompile(`(\d{2}/\d{2}/\d{4})`)
	reBIN          = regexp.MustCompile(`(?i)buy\s*(?:it\s*)?now`)
	reBINAmount    = regexp.MustCompile(`(?i)buy\s*(?:it\s*)?now[:\s$]*([\d,]+)`)
	reCurrentBid   = regexp.MustCompile(`(?i)Current\s*Bid[:\s$]*([\d,]+)`)
	reSoldFor      = regexp.MustCompile(`(?i)Sold\s+for\s*\$?\s*([\d,]+)`)
	reOdoDetail    = regexp.MustCompile(`(?i)(?:Odometer|Miles|Mileage)[:\s]*([\d,]+)`)
	reExteriorSect = regexp.MustCompile(`(?is)Exterior\s+condition\s*\n(.*?)(?:\nFront\s+(?:left|right)|\nInterior|\nNote:|\nFull\s+vehicle|\z)`)
	reDamageItem   = regexp.MustCompile(`^(.+?)\s*[-–]\s*(.+?)\s*\(([^)]+)\)\s*$`)
	reStripNonDigit = regexp.MustCompile(`[^\d]`)
)

// ScrapeDetail navigates to a lot detail page and extracts all available fields.
// If imageDir is non-empty, it clicks Copart's "Download Image" button and saves
// the resulting ZIP to imageDir/<lotNumber>.zip (unextracted).
func (s *Scraper) ScrapeDetail(ctx context.Context, lotURL string, imageDir string) (*DetailResult, error) {
	lotM := reLotNum.FindStringSubmatch(lotURL)
	if len(lotM) < 2 {
		return nil, fmt.Errorf("could not extract lot number from %s", lotURL)
	}

	page, err := s.br.NewPage(lotURL)
	if err != nil {
		return nil, fmt.Errorf("open lot page: %w", err)
	}
	defer page.Close() //nolint:errcheck

	slog.Info("scraping detail", "lot", lotM[1])

	// Wait for detail panel to appear
	if _, err := page.Timeout(20 * time.Second).Element(
		`dt, th, [class*='vehicle-detail'], [class*='label']`,
	); err != nil {
		return nil, fmt.Errorf("detail panel not found after 20s: %w", err)
	}

	// Scroll to trigger lazy sections
	page.MustEval(`() => window.scrollTo(0, document.body.scrollHeight * 0.5)`)
	time.Sleep(500 * time.Millisecond)
	page.MustEval(`() => window.scrollTo(0, document.body.scrollHeight)`)
	time.Sleep(600 * time.Millisecond)

	// Expand "Full vehicle details" — try multiple selector strategies
	expandSelectors := []string{
		`a[href*='vehicle-detail']`,
		`button[class*='vehicle-detail']`,
		`[data-uname*='vehicleDetail']`,
		`[data-uname*='fullDetails']`,
		`copart-vehicle-detail button`,
		`[class*='view-more']`,
		`[class*='show-more']`,
	}
	for _, sel := range expandSelectors {
		if el, err := page.Timeout(2 * time.Second).Element(sel); err == nil {
			el.Click(proto.InputMouseButtonLeft, 1) //nolint:errcheck
			time.Sleep(1 * time.Second)
			break
		}
	}
	// XPath fallback — any clickable element whose text mentions details/VIN
	for _, text := range []string{"Full vehicle details", "View full details", "More details", "Show VIN"} {
		if el, err := page.Timeout(1500 * time.Millisecond).ElementX(
			fmt.Sprintf(`//*[contains(text(),'%s')]`, text),
		); err == nil {
			el.Click(proto.InputMouseButtonLeft, 1) //nolint:errcheck
			time.Sleep(1 * time.Second)
			break
		}
	}
	// Give Angular time to render newly-visible sections
	time.Sleep(500 * time.Millisecond)

	bodyText, err := page.MustElement("body").Text()
	if err != nil {
		return nil, fmt.Errorf("get body text: %w", err)
	}

	res := &DetailResult{
		LotNumber: lotM[1],
		LotURL:    lotURL,
		ScrapedAt: time.Now(),
	}

	// ── regex-based extractions (fast, works on body text) ────────────────

	// VIN: three-layer extraction — DOM label → JS innerText scan → body regex
	res.VIN = strings.ToUpper(strings.TrimSpace(domValue(page, "vin", "vin number", "vehicle identification number")))
	if res.VIN == "" {
		// JavaScript scan: find any standalone 17-char VIN string in the rendered page
		if r, err := page.Eval(`() => {
			var m = (document.body.innerText || '').match(/\b[A-HJ-NPR-Z0-9]{17}\b/);
			return m ? m[0] : '';
		}`); err == nil {
			res.VIN = strings.TrimSpace(r.Value.Str())
		}
	}
	if res.VIN == "" {
		// Body text regex fallback
		res.VIN = reMatch(reVIN, bodyText, 1)
	}
	// Validate — VINs contain only A-H, J-N, P-R, S-Z, 0-9 (no I, O, Q)
	if res.VIN != "" && !regexp.MustCompile(`^[A-HJ-NPR-Z0-9]{17}$`).MatchString(res.VIN) {
		res.VIN = ""
	}
	res.ConditionGrade = reMatch(reGrade, bodyText, 1)
	res.RunAndDrive = reMatch(reRunDrive, bodyText, 1)
	if m := reDriveType.FindStringSubmatch(bodyText); len(m) >= 2 {
		res.DriveType = strings.ToUpper(m[1])
	}
	res.FuelType = reMatch(reFuelType, bodyText, 1)
	if lt := reMatch(reLossType, bodyText, 1); lt != "" {
		res.LossType = strings.TrimSpace(lt)
	}
	if k := reMatch(reKeys, bodyText, 1); k != "" {
		kl := strings.ToLower(k)
		if kl == "yes" || kl == "available" || kl == "present" {
			res.KeysPresent = "Yes"
		} else {
			res.KeysPresent = "No"
		}
	}
	if ab := reMatch(reAirbags, bodyText, 1); ab != "" {
		if strings.ToLower(ab) == "deployed" || strings.ToLower(ab) == "yes" {
			res.AirbagsDeployed = "Deployed"
		} else {
			res.AirbagsDeployed = "Not Deployed"
		}
	}
	if m := reCurrentBid.FindStringSubmatch(bodyText); len(m) >= 2 {
		res.CurrentBid = parseMoney(m[1])
	}
	if m := reSoldFor.FindStringSubmatch(bodyText); len(m) >= 2 {
		res.FinalBid = parseMoney(m[1])
		res.SaleStatus = "sold"
	}
	res.IsBIN = reBIN.MatchString(bodyText)
	if res.IsBIN {
		if m := reBINAmount.FindStringSubmatch(bodyText); len(m) >= 2 {
			res.BuyNowAmount = parseMoney(m[1])
		}
	}
	if m := reOdoDetail.FindStringSubmatch(bodyText); len(m) >= 2 {
		if n, err := strconv.Atoi(strings.ReplaceAll(m[1], ",", "")); err == nil {
			res.Odometer = n
		}
	}

	// ── DOM label→value for structured fields ─────────────────────────────

	res.Make  = strings.ToUpper(strings.TrimSpace(domValue(page, "make", "vehicle make")))
	res.Model = strings.TrimSpace(domValue(page, "model", "vehicle model"))
	if yStr := domValue(page, "year", "model year"); yStr != "" {
		if y, err := strconv.Atoi(strings.TrimSpace(yStr)); err == nil && y > 1980 {
			res.Year = y
		}
	}

	// Fallback: extract year + make + model from the vehicle title in body text.
	// Copart renders something like "2018 HONDA CIVIC LX" in the first ~1000 chars.
	if res.Year == 0 || res.Make == "" {
		snippet := bodyText
		if len(snippet) > 2000 {
			snippet = snippet[:2000]
		}
		// Match: 4-digit year followed by ALLCAPS make and model
		reTitleLine := regexp.MustCompile(`(?m)\b(20[012]\d)\s+([A-Z][A-Z]+)\s+([A-Z][A-Z0-9 \-]+)`)
		if m := reTitleLine.FindStringSubmatch(snippet); len(m) >= 4 {
			if res.Year == 0 {
				if y, _ := strconv.Atoi(m[1]); y > 1980 {
					res.Year = y
				}
			}
			if res.Make == "" {
				res.Make = m[2]
			}
			if res.Model == "" {
				res.Model = strings.TrimSpace(m[3])
			}
		}
	}

	res.DamagePrimary = domValue(page, "primary damage", "damage")
	res.DamageSecondary = domValue(page, "secondary damage")
	// Strip "Listen to engine" link text that appears inline on Copart engine cells
	eng := domValue(page, "engine type", "engine")
	res.EngineType = strings.TrimSpace(strings.Split(eng, "\n")[0])
	res.Transmission = domValue(page, "transmission")
	res.Color = domValue(page, "color", "exterior color")
	res.BodyStyle = domValue(page, "body style", "body type")

	if odoStr := domValue(page, "odometer", "miles", "mileage"); odoStr != "" {
		digits := reStripNonDigit.ReplaceAllString(odoStr, "")
		if n, err := strconv.Atoi(digits); err == nil && n > 0 {
			res.Odometer = n
		}
	}

	// Only accept a real date (MM/DD/YYYY); ignore status words like "Future"
	if sd := domValue(page, "sale date", "auction date", "sale information"); sd != "" {
		if strings.Contains(strings.ToLower(sd), "future") {
			res.SaleStatus = "future"
		} else if reSaleDate.MatchString(sd) {
			res.SaleDate = reMatch(reSaleDate, sd, 1)
		}
	}
	// No body-text fallback: reSaleDate against full body finds random dates (listing
	// history, repair estimates, etc.) that are unrelated to the auction schedule.

	// Fuel / loss fallback from DOM
	if res.FuelType == "" {
		res.FuelType = domValue(page, "fuel type", "fuel")
	}
	if res.LossType == "" {
		res.LossType = domValue(page, "loss type", "loss")
	}

	// ── structured / rich extractions ─────────────────────────────────────

	res.ExteriorCondition = parseExteriorCondition(bodyText)

	for _, marker := range []string{"exterior condition", "vehicle condition"} {
		if i := strings.Index(strings.ToLower(bodyText), marker); i >= 0 {
			end := i + 3000
			if end > len(bodyText) {
				end = len(bodyText)
			}
			res.VehicleDetailsText = strings.TrimSpace(bodyText[i:end])
			break
		}
	}

	// ── image download ────────────────────────────────────────────────────

	if imageDir != "" {
		if err := os.MkdirAll(imageDir, 0o755); err == nil {
			zipPath, err := clickDownloadImages(s.br.Rod(), page, imageDir, lotM[1])
			if err != nil {
				slog.Warn("button download failed, trying URL fallback", "lot", lotM[1], "err", err)
				urls := scrapeImageURLs(page)
				if len(urls) > 0 {
					if zipPath2, err2 := downloadURLsToZip(urls, imageDir, lotM[1]); err2 != nil {
						slog.Warn("URL image download also failed", "lot", lotM[1], "err", err2)
					} else {
						res.ImageZip = zipPath2
						slog.Info("images downloaded via URL fallback", "lot", lotM[1], "zip", zipPath2, "count", len(urls))
					}
				} else {
					slog.Warn("no image URLs found on page", "lot", lotM[1])
				}
			} else {
				res.ImageZip = zipPath
				slog.Info("images downloaded", "lot", lotM[1], "zip", zipPath)
			}
		}
	}

	return res, nil
}

// clickDownloadImages clicks the "Download Image" button (and any overlay that appears),
// waits for the ZIP download to complete, and renames it to <lotNumber>.zip.
func clickDownloadImages(br *rod.Browser, page *rod.Page, dir, lotNumber string) (string, error) {
	btn, err := page.Timeout(10 * time.Second).Element(`#downloadImageBtn`)
	if err != nil {
		return "", fmt.Errorf("download button not found: %w", err)
	}

	// Scroll the button into view first
	btn.MustScrollIntoView()
	time.Sleep(300 * time.Millisecond)

	// First click — may open an overlay/dropdown with options
	if err := btn.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return "", fmt.Errorf("click download button: %w", err)
	}
	time.Sleep(800 * time.Millisecond)

	// Look for a "Download" or "All" option inside the overlay that appeared
	overlaySelectors := []string{
		`[class*="download-op"] li`,
		`[class*="download-op"] a`,
		`[class*="download-op"] button`,
		`.p-overlaypanel li`,
		`.p-overlaypanel a`,
		`[styleclass="download-op-block"] li`,
	}
	for _, sel := range overlaySelectors {
		items, err := page.Elements(sel)
		if err != nil || len(items) == 0 {
			continue
		}
		// Register download handler before triggering the actual download
		wait := br.WaitDownload(dir)
		// Click the first item (usually "Download All" or the only option)
		if err := items[0].Click(proto.InputMouseButtonLeft, 1); err != nil {
			continue
		}
		// Wait with a 60s ceiling via a channel race
		type result struct {
			info *proto.PageDownloadWillBegin
		}
		ch := make(chan result, 1)
		go func() { ch <- result{wait()} }()

		select {
		case r := <-ch:
			if r.info == nil {
				return "", fmt.Errorf("download completed but info was nil")
			}
			downloaded := filepath.Join(dir, r.info.GUID)
			dest := filepath.Join(dir, lotNumber+".zip")
			if err := os.Rename(downloaded, dest); err != nil {
				if _, statErr := os.Stat(dest); statErr == nil {
					return dest, nil
				}
				return downloaded, nil
			}
			return dest, nil
		case <-time.After(60 * time.Second):
			return "", fmt.Errorf("download timed out after 60s")
		}
	}

	// Fallback: maybe the first click DID trigger the download directly (no overlay)
	wait := br.WaitDownload(dir)
	// Re-click the button
	btn.Click(proto.InputMouseButtonLeft, 1) //nolint:errcheck

	ch := make(chan *proto.PageDownloadWillBegin, 1)
	go func() { ch <- wait() }()
	select {
	case info := <-ch:
		if info == nil {
			return "", fmt.Errorf("fallback download: nil info")
		}
		downloaded := filepath.Join(dir, info.GUID)
		dest := filepath.Join(dir, lotNumber+".zip")
		os.Rename(downloaded, dest) //nolint:errcheck
		return dest, nil
	case <-time.After(30 * time.Second):
		return "", fmt.Errorf("fallback download timed out")
	}
}

// domValue finds dt/th/label elements matching any key and returns the adjacent value.
func domValue(page *rod.Page, keys ...string) string {
	labels, err := page.Elements(`dt, th, [class*='label'], [class*='key'], [class*='field-name']`)
	if err != nil {
		return ""
	}
	for _, el := range labels {
		text, err := el.Text()
		if err != nil {
			continue
		}
		tl := strings.ToLower(strings.TrimSpace(text))
		for _, k := range keys {
			if strings.Contains(tl, k) {
				// Try following sibling first
				if sib, err := el.ElementX(`following-sibling::*[1]`); err == nil {
					if v, err := sib.Text(); err == nil && strings.TrimSpace(v) != "" {
						return strings.TrimSpace(v)
					}
				}
				// Try parent's next td
				if td, err := el.ElementX(`../td`); err == nil {
					if v, err := td.Text(); err == nil && strings.TrimSpace(v) != "" {
						return strings.TrimSpace(v)
					}
				}
			}
		}
	}
	return ""
}

// scrapeImageURLs extracts Copart CDN image URLs via JS + img attribute sweep.
func scrapeImageURLs(page *rod.Page) []string {
	// JS sweep: scan innerHTML for CDN URLs + check all img attributes
	result, err := page.Eval(`() => {
		var urls = new Set();
		var re = /https:\/\/cs\.copart\.com\/[^"'\s<>]+\.(?:jpg|jpeg|png|webp)/ig;
		var html = document.body.innerHTML;
		var m;
		while ((m = re.exec(html)) !== null) { urls.add(m[0]); }
		document.querySelectorAll('img').forEach(function(img) {
			['src','data-src','data-imgurl','data-lazy','data-original'].forEach(function(a) {
				var v = img.getAttribute(a);
				if (v && v.indexOf('cs.copart.com') > -1) urls.add(v);
			});
		});
		return Array.from(urls).slice(0, 40);
	}`)
	if err == nil && result != nil {
		var rawURLs []string
		if err := result.Value.Unmarshal(&rawURLs); err == nil {
			var out []string
			for _, u := range rawURLs {
				if !strings.Contains(strings.ToLower(u), "placeholder") {
					// Prefer full-size over thumbnails (_thb → _ful)
					full := strings.Replace(u, "_thb.", "_ful.", 1)
					out = append(out, full)
				}
			}
			if len(out) > 0 {
				return out
			}
		}
	}

	// Fallback: rod element sweep
	var out []string
	seen := map[string]bool{}
	for _, sel := range []string{
		`img[src*='cs.copart.com']`,
		`[class*='lot-img'] img`,
		`[class*='gallery'] img`,
		`[class*='photo'] img`,
	} {
		imgs, err := page.Elements(sel)
		if err != nil {
			continue
		}
		for _, img := range imgs {
			for _, attr := range []string{"src", "data-src"} {
				v, err := img.Attribute(attr)
				if err != nil || v == nil || *v == "" {
					continue
				}
				if strings.HasPrefix(*v, "http") && !strings.Contains(*v, "placeholder") && !seen[*v] {
					seen[*v] = true
					out = append(out, *v)
				}
			}
		}
	}
	return out
}

// downloadURLsToZip fetches image URLs one-by-one and writes them into a ZIP archive.
// Used as fallback when the Copart "Download Images" button is unavailable (e.g. VPS IPs).
func downloadURLsToZip(urls []string, dir, lotNumber string) (string, error) {
	dest := filepath.Join(dir, lotNumber+".zip")
	f, err := os.Create(dest)
	if err != nil {
		return "", err
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	defer zw.Close()

	client := &http.Client{Timeout: 20 * time.Second}
	written := 0
	for i, u := range urls {
		req, err := http.NewRequest(http.MethodGet, u, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36")
		req.Header.Set("Referer", "https://www.copart.com/")

		resp, err := client.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			if resp != nil {
				resp.Body.Close()
			}
			continue
		}

		name := fmt.Sprintf("%s_Image_%d.jpg", lotNumber, i+1)
		w, err := zw.Create(name)
		if err != nil {
			resp.Body.Close()
			continue
		}
		if _, err := io.Copy(w, resp.Body); err != nil {
			resp.Body.Close()
			continue
		}
		resp.Body.Close()
		written++
	}

	if written == 0 {
		os.Remove(dest)
		return "", fmt.Errorf("all %d image URLs failed to download", len(urls))
	}
	return dest, nil
}

// ── small helpers ─────────────────────────────────────────────────────────

func reMatch(re *regexp.Regexp, text string, group int) string {
	m := re.FindStringSubmatch(text)
	if len(m) > group {
		return strings.TrimSpace(m[group])
	}
	return ""
}

func parseMoney(s string) float64 {
	s = strings.ReplaceAll(s, ",", "")
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func parseExteriorCondition(text string) []DamageItem {
	m := reExteriorSect.FindStringSubmatch(text)
	if len(m) < 2 {
		return nil
	}
	var items []DamageItem
	for _, line := range strings.Split(m[1], "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lm := reDamageItem.FindStringSubmatch(line)
		if lm == nil {
			continue
		}
		count := lm[3]
		if regexp.MustCompile(`(?i)^\d+\s+or\s+more$`).MatchString(count) {
			count = "10+"
		}
		items = append(items, DamageItem{
			Panel:  strings.TrimSpace(lm[1]),
			Damage: strings.TrimSpace(lm[2]),
			Count:  count,
		})
	}
	return items
}
