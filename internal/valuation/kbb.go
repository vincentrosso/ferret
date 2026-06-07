package valuation

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/vincentrosso/ferret/internal/browser"
)

// KBBTrim is one trim row from the KBB value table.
type KBBTrim struct {
	Trim         string `json:"trim"`
	TradeIn      int    `json:"trade_in"`
	PrivateParty int    `json:"private_party"`
	FairPurchase int    `json:"fair_purchase"`
}

// KBBResult holds Kelley Blue Book values for a year/make/model (no VIN needed).
type KBBResult struct {
	Year      int    `json:"year"`
	Make      string `json:"make"`
	Model     string `json:"model"`
	ModelSlug string `json:"model_slug"`

	// Values for the matched trim (our best signal for resale).
	MatchedTrim  string `json:"matched_trim,omitempty"`
	TradeIn      int    `json:"trade_in,omitempty"`
	PrivateParty int    `json:"private_party,omitempty"`
	FairPurchase int    `json:"fair_purchase,omitempty"`

	Trims     []KBBTrim `json:"trims,omitempty"`
	Source    string    `json:"source"` // "kbb"
	ScrapedAt string    `json:"scraped_at"`
	Error     string    `json:"error,omitempty"`
}

// KBB value rows look like: "XLE Sport Utility 4D $23,300 $25,500 $27,300"
// (columns: Trade-In | Private Party | Fair Purchase). The body-style keyword
// anchors the end of the trim name so we don't grab unrelated $ triples.
var reKBBRow = regexp.MustCompile(
	`([A-Z][A-Za-z0-9 /'\-]*?(?:Sport Utility|Sedan|Coupe|Hatchback|Minivan|Wagon|Convertible|Pickup|Crew Cab|Extended Cab|Double Cab|Regular Cab|Cargo Van|Passenger Van|Club Cab|Quad Cab|Mega Cab|King Cab|Access Cab|SuperCab|SuperCrew|Van|Truck|4D|2D)[A-Za-z0-9 ]*?)\s+\$([0-9,]+)\s+\$([0-9,]+)\s+\$([0-9,]+)`)

// ScrapeKBB pulls Kelley Blue Book trade-in / private-party / fair-purchase
// values for a year/make/model. No VIN required. proxyURL is optional (needed
// from datacenter IPs). Matches our trim where possible.
func ScrapeKBB(makeName, model string, year int, trim, proxyURL string) (*KBBResult, error) {
	res := &KBBResult{
		Year: year, Make: strings.ToUpper(makeName), Model: strings.ToUpper(model),
		Source: "kbb", ScrapedAt: time.Now().UTC().Format(time.RFC3339),
	}

	// KBB's value table is flaky through residential-proxy IPs: some get
	// challenged and the table never renders. Each attempt pins a FRESH sticky
	// session (one stable IP for that whole render — mid-render IP rotation is
	// what trips the challenge); a failed attempt draws a new IP. Up to 4
	// tries/slug. The 52s budget is the deliberate exception to the 60s scrape
	// rule; the caller's 65s subprocess timeout accommodates it.
	deadline := time.Now().Add(52 * time.Second)
	const navTimeout = 10 * time.Second
	const maxAttempts = 4
	makeSlug := slugify(makeName)

	// Try progressively longer model slugs (base model first): "rav4", then
	// "grand-cherokee", etc. First page with a value table wins.
	var trims []KBBTrim
outer:
	for _, slug := range modelSlugCandidates(model) {
		pageURL := fmt.Sprintf("https://www.kbb.com/%s/%s/%d/", makeSlug, slug, year)
		for attempt := 0; attempt < maxAttempts; attempt++ {
			// Cap each step to the time left so the 52s deadline is a near-hard
			// cap — a fresh attempt's 10s timeouts can't stack past the budget
			// and overrun the caller's 65s subprocess timeout.
			step := navTimeout
			if rem := time.Until(deadline); rem <= 0 {
				break outer
			} else if rem < step {
				step = rem
			}
			// Fresh sticky IP per attempt (no-op for non-Smartproxy proxies).
			sess := fmt.Sprintf("kbb%d%x", attempt, time.Now().UnixNano())
			body, notFound, err := fetchKBBBody(pageURL, browser.StickyProxy(proxyURL, sess), step)
			if err != nil {
				continue // launch/render error → retry on a new IP
			}
			if notFound {
				break // wrong slug (404) — won't appear on retry; next candidate
			}
			if body == "" {
				continue // no value table on this IP → retry fresh
			}
			if parsed := parseKBBTrims(body); len(parsed) > 0 {
				res.ModelSlug = slug
				trims = parsed
				break outer
			}
		}
	}

	if len(trims) == 0 {
		res.Error = fmt.Sprintf("no KBB value table for %d %s %s", year, makeName, model)
		return res, nil
	}
	res.Trims = trims

	// Match our trim; else fall back to the median trim by private-party value.
	match := matchKBBTrim(trims, trim)
	res.MatchedTrim = match.Trim
	res.TradeIn = match.TradeIn
	res.PrivateParty = match.PrivateParty
	res.FairPurchase = match.FairPurchase
	return res, nil
}

// fetchKBBBody launches a browser through proxyURL, loads a KBB model page, and
// returns its body text. notFound is true when KBB served a 404 ("page not
// found") — a wrong slug that won't appear on retry. An empty body with
// notFound=false means no value table rendered on this IP (retry on a fresh
// one). A fresh browser per call is deliberate: the proxy (hence sticky IP) is
// fixed at launch, so a per-attempt launch is how each retry gets a new IP.
func fetchKBBBody(pageURL, proxyURL string, step time.Duration) (body string, notFound bool, err error) {
	br, err := browser.New(browser.Options{
		Headless: true,
		UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
			"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
		ProxyURL:       proxyURL,
		BlockResources: true, // value table is text/JSON — skip images/fonts/media
	})
	if err != nil {
		return "", false, fmt.Errorf("launch browser: %w", err)
	}
	defer br.Close()

	page, err := br.NewPage("about:blank")
	if err != nil {
		return "", false, fmt.Errorf("open page: %w", err)
	}
	_ = page.Timeout(step).Navigate(pageURL)
	_ = page.Timeout(step).WaitLoad()
	time.Sleep(3 * time.Second)
	page.Eval(`() => window.scrollTo(0, 1400)`) //nolint:errcheck — nudge lazy content
	time.Sleep(1500 * time.Millisecond)

	bodyEl, err := page.Timeout(step).Element("body")
	if err != nil {
		return "", false, err
	}
	text, _ := bodyEl.Text()
	if strings.Contains(strings.ToLower(text), "page not found") {
		return "", true, nil
	}
	if !strings.Contains(text, "Private Party Value") && !strings.Contains(text, "Trade-In Value") {
		return "", false, nil // challenged/slow IP — no table; caller retries
	}
	return text, false, nil
}

// parseKBBTrims extracts trim rows from the value table region of the page text.
func parseKBBTrims(body string) []KBBTrim {
	// Normalize whitespace so tab/newline-separated cells become single spaces.
	norm := strings.Join(strings.Fields(strings.ReplaceAll(strings.ReplaceAll(body, "\t", " "), "\n", " ")), " ")
	// Limit to the value-table region to avoid stray triples elsewhere.
	if i := strings.Index(norm, "Trade-In Value"); i >= 0 {
		end := i + 2500
		if end > len(norm) {
			end = len(norm)
		}
		norm = norm[i:end]
	}
	var out []KBBTrim
	seen := map[string]bool{}
	for _, m := range reKBBRow.FindAllStringSubmatch(norm, -1) {
		trim := strings.TrimSpace(m[1])
		if seen[trim] || strings.Contains(strings.ToLower(trim), "value") {
			continue
		}
		seen[trim] = true
		out = append(out, KBBTrim{
			Trim:         trim,
			TradeIn:      atoiComma(m[2]),
			PrivateParty: atoiComma(m[3]),
			FairPurchase: atoiComma(m[4]),
		})
	}
	return out
}

// matchKBBTrim finds the trim row best matching our trim string; falls back to
// the median private-party trim when there's no match.
func matchKBBTrim(trims []KBBTrim, want string) KBBTrim {
	want = strings.ToUpper(strings.TrimSpace(want))
	if want != "" {
		// Prefer a row whose first word(s) equal our trim (e.g. "XLE" →
		// "XLE Sport Utility 4D", not "XLE Premium …").
		firstWord := strings.Fields(want)[0]
		var prefixMatch *KBBTrim
		for i := range trims {
			label := strings.ToUpper(trims[i].Trim)
			if strings.HasPrefix(label, want+" ") || label == want {
				return trims[i]
			}
			if prefixMatch == nil && strings.HasPrefix(label, firstWord+" ") {
				prefixMatch = &trims[i]
			}
		}
		if prefixMatch != nil {
			return *prefixMatch
		}
	}
	// Median by private-party value — a stable middle-of-lineup estimate.
	sorted := append([]KBBTrim(nil), trims...)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].PrivateParty < sorted[j-1].PrivateParty; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	return sorted[len(sorted)/2]
}

// modelSlugCandidates yields base-model-first slug guesses from a model string.
// "RAV4 XLE" → ["rav4", "rav4-xle"]; "GRAND CHEROKEE L" → ["grand-cherokee",
// "grand-cherokee-l", "grand"]. Single tokens first since most base models are
// one word; two-word base models (Grand Cherokee) caught on the second try.
func modelSlugCandidates(model string) []string {
	words := strings.Fields(strings.ToLower(strings.TrimSpace(model)))
	if len(words) == 0 {
		return nil
	}
	var cands []string
	add := func(s string) {
		s = slugify(s)
		for _, c := range cands {
			if c == s {
				return
			}
		}
		if s != "" {
			cands = append(cands, s)
		}
	}
	// 1 word, 2 words, 3 words (base-model-first ordering)
	for n := 1; n <= len(words) && n <= 3; n++ {
		add(strings.Join(words[:n], " "))
	}
	return cands
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
