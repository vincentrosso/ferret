// Package fb scrapes Facebook Marketplace vehicle listings — the private-party
// exit market the auction side sells into.
//
// NO PASSWORD EVER TOUCHES THIS CODE. Facebook checkpoints scripted logins fast
// (a typed password from a fresh browser is exactly what its integrity system
// looks for), so the session is established by a HUMAN once — `ferret fb login`
// opens a visible window and waits for them — and every scrape after that reuses
// the saved cookies, the same shape as data/copart-session.json.
//
// The authoritative session signal is the COOKIE PAIR, never the DOM: `c_user`
// says WHO, `xs` says the session is still live. (Two separate Copart outages
// came from scoring a session by a page element, then by a cookie that outlives
// the session — see scrapers/copart IsLoggedIn / ProbeLoggedIn.)
package fb

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/vincentrosso/ferret/internal/browser"
)

const (
	DefaultCookiePath = "data/fb-session.json"
	loginURL          = "https://www.facebook.com/login"
	marketplaceURL    = "https://www.facebook.com/marketplace"
)

type Scraper struct {
	br         *browser.Browser
	cookiePath string
}

func New(br *browser.Browser, cookiePath string) *Scraper {
	if cookiePath == "" {
		cookiePath = DefaultCookiePath
	}
	return &Scraper{br: br, cookiePath: cookiePath}
}

// Listing is one Marketplace vehicle listing. Card fields come from search
// results; the detail fields (Miles, Title…) only from Item.
type Listing struct {
	ID        string `json:"id"`
	URL       string `json:"url"`
	Title     string `json:"title"`
	Year      int    `json:"year,omitempty"`
	Price     int    `json:"price"`
	PrevPrice int    `json:"prev_price,omitempty"` // struck-through price = a price cut
	Location  string `json:"location"`

	Detailed     bool   `json:"detailed"`
	Miles        int    `json:"miles,omitempty"`
	TitleStatus  string `json:"title_status,omitempty"`
	Transmission string `json:"transmission,omitempty"`
	Owners       int    `json:"owners,omitempty"`
	Colors       string `json:"colors,omitempty"`
	ListedAgo    string `json:"listed_ago,omitempty"`
	Description  string `json:"description,omitempty"`
	Sold         bool   `json:"sold,omitempty"`
	Gone         bool   `json:"gone,omitempty"` // item page no longer renders a listing

	ScrapedAt time.Time `json:"scraped_at"`
}

// ---- session ----

type savedCookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain"`
	Path     string  `json:"path"`
	Expires  float64 `json:"expires"`
	HTTPOnly bool    `json:"httpOnly"`
	Secure   bool    `json:"secure"`
	SameSite string  `json:"sameSite,omitempty"`
}

// SessionState reads the jar without a browser.
type SessionState struct {
	Exists  bool
	Live    bool
	UserID  string
	Expires time.Time // xs expiry
	Reason  string
}

func ReadSession(path string) SessionState {
	b, err := os.ReadFile(path)
	if err != nil {
		return SessionState{Reason: "no cookie file"}
	}
	var raw []savedCookie
	if err := json.Unmarshal(b, &raw); err != nil {
		return SessionState{Exists: true, Reason: "unreadable cookie file"}
	}
	st := SessionState{Exists: true}
	var xs *savedCookie
	for i := range raw {
		switch raw[i].Name {
		case "c_user":
			st.UserID = raw[i].Value
		case "xs":
			xs = &raw[i]
		}
	}
	switch {
	case st.UserID == "":
		st.Reason = "no c_user cookie"
	case xs == nil:
		st.Reason = "no xs cookie"
	default:
		// Expires <= 0 means a browser-session cookie: it has no date to go stale by.
		if xs.Expires > 0 {
			st.Expires = time.Unix(int64(xs.Expires), 0)
			if time.Now().After(st.Expires) {
				st.Reason = "xs cookie expired " + st.Expires.UTC().Format(time.RFC3339)
				return st
			}
		}
		st.Live = true
	}
	return st
}

func (s *Scraper) saveCookies(page *rod.Page) (int, error) {
	res, err := proto.StorageGetCookies{}.Call(page)
	if err != nil {
		return 0, err
	}
	var out []savedCookie
	for _, c := range res.Cookies {
		if !strings.Contains(c.Domain, "facebook.com") {
			continue
		}
		out = append(out, savedCookie{
			Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path,
			Expires: float64(c.Expires), HTTPOnly: c.HTTPOnly, Secure: c.Secure,
			SameSite: string(c.SameSite),
		})
	}
	if err := os.MkdirAll(filepath.Dir(s.cookiePath), 0o755); err != nil {
		return 0, err
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return len(out), os.WriteFile(s.cookiePath, b, 0o600)
}

func (s *Scraper) LoadSession() error {
	st := ReadSession(s.cookiePath)
	if !st.Live {
		return fmt.Errorf("fb session not usable (%s) — run: ferret fb login", st.Reason)
	}
	b, _ := os.ReadFile(s.cookiePath)
	var raw []savedCookie
	_ = json.Unmarshal(b, &raw)
	var params []*proto.NetworkCookieParam
	for _, c := range raw {
		params = append(params, &proto.NetworkCookieParam{
			Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path,
			HTTPOnly: c.HTTPOnly, Secure: c.Secure, Expires: proto.TimeSinceEpoch(c.Expires),
		})
	}
	if err := (proto.StorageSetCookies{Cookies: params}).Call(s.br.Rod()); err != nil {
		return fmt.Errorf("set cookies: %w", err)
	}
	slog.Info("fb session loaded", "user", st.UserID, "cookies", len(params))
	return nil
}

// Login opens Facebook in a VISIBLE browser and waits for a human to sign in.
// It never fills a field. It returns once the c_user+xs pair appears, then
// saves the jar.
func (s *Scraper) Login(ctx context.Context, wait time.Duration) error {
	page, err := s.br.NewPage(loginURL)
	if err != nil {
		return fmt.Errorf("open login page: %w", err)
	}
	fmt.Fprintln(os.Stderr, "→ sign in to Facebook in the browser window (waiting up to", wait, ")")
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
		res, err := proto.StorageGetCookies{}.Call(page)
		if err != nil {
			continue
		}
		var user, xs bool
		for _, c := range res.Cookies {
			user = user || (c.Name == "c_user" && c.Value != "")
			xs = xs || (c.Name == "xs" && c.Value != "")
		}
		if user && xs {
			time.Sleep(3 * time.Second) // let the post-login redirects settle the jar
			n, err := s.saveCookies(page)
			if err != nil {
				return fmt.Errorf("save cookies: %w", err)
			}
			slog.Info("fb session saved", "path", s.cookiePath, "cookies", n)
			return nil
		}
	}
	return fmt.Errorf("no login within %s", wait)
}

// ---- scraping ----

// SearchParams maps onto Marketplace's own query string.
type SearchParams struct {
	Query    string
	Location string // city slug, e.g. "irvine"; empty = the account's saved location
	MinYear  int
	MaxYear  int
	MinPrice int
	MaxPrice int
	Scrolls  int    // each scroll loads ~24 more cards
	RawURL   string // overrides everything above except Query (used for the title filter)
}

func (p SearchParams) URL() string {
	if p.RawURL != "" {
		return p.RawURL
	}
	base := marketplaceURL
	if p.Location != "" {
		base += "/" + url.PathEscape(p.Location)
	}
	q := url.Values{}
	q.Set("query", p.Query)
	q.Set("exact", "false")
	for k, v := range map[string]int{"minYear": p.MinYear, "maxYear": p.MaxYear, "minPrice": p.MinPrice, "maxPrice": p.MaxPrice} {
		if v > 0 {
			q.Set(k, strconv.Itoa(v))
		}
	}
	return base + "/search/?" + q.Encode()
}

var ErrLoggedOut = fmt.Errorf("facebook served a logged-out page — run: ferret fb login")

func loggedOut(page *rod.Page) bool {
	info, err := page.Info()
	if err == nil && (strings.Contains(info.URL, "/login") || strings.Contains(info.URL, "checkpoint")) {
		return true
	}
	r, err := page.Eval(`() => !!document.querySelector('form[action*="login"] input[name="pass"]')`)
	return err == nil && r.Value.Bool()
}

func pause(min, max time.Duration) {
	time.Sleep(min + time.Duration(rand.Int63n(int64(max-min)+1)))
}

const cardsJS = `() => {
  const out = [], seen = new Set();
  for (const a of document.querySelectorAll('a[href*="/marketplace/item/"]')) {
    const m = a.getAttribute('href').match(/\/marketplace\/item\/(\d+)/);
    if (!m || seen.has(m[1])) continue;
    seen.add(m[1]);
    out.push({id: m[1], lines: a.innerText.split('\n').map(s => s.trim()).filter(Boolean)});
  }
  return out;
}`

var (
	moneyRe = regexp.MustCompile(`^(?:US)?\$[\d,]+$`)
	yearRe  = regexp.MustCompile(`^((?:19|20)\d{2})\b`)
)

func parseMoney(s string) int {
	n, _ := strconv.Atoi(strings.NewReplacer("US", "", "$", "", ",", "").Replace(s))
	return n
}

// Search returns the listing cards for one query. Card lines are
// [price, (struck price), title, location, …]; the prices are the leading
// money-shaped lines, the title is the first line after them.
func (s *Scraper) Search(ctx context.Context, p SearchParams) ([]Listing, error) {
	page, err := s.br.NewPage(p.URL())
	if err != nil {
		return nil, err
	}
	defer page.Close()
	_ = page.Timeout(30 * time.Second).WaitLoad()
	pause(3*time.Second, 5*time.Second)
	if loggedOut(page) {
		return nil, ErrLoggedOut
	}
	// Collect after EVERY scroll, not once at the end: the results grid is
	// virtualized, so scrolling unmounts the cards above the fold. A single
	// extraction after scrolling saw mostly the "similar listings" padding that
	// loads below the real results — 3 Avalons out of 42 cards, on a page
	// showing a dozen.
	type card struct {
		ID    string   `json:"id"`
		Lines []string `json:"lines"`
	}
	var cards []card
	have := map[string]bool{}
	collect := func() error {
		r, err := page.Eval(cardsJS)
		if err != nil {
			return fmt.Errorf("extract cards: %w", err)
		}
		var batch []card
		if err := r.Value.Unmarshal(&batch); err != nil {
			return fmt.Errorf("decode cards: %w", err)
		}
		for _, c := range batch {
			if !have[c.ID] {
				have[c.ID] = true
				cards = append(cards, c)
			}
		}
		return nil
	}
	if err := collect(); err != nil {
		return nil, err
	}
	for i := 0; i < p.Scrolls && ctx.Err() == nil; i++ {
		_, _ = page.Eval(`() => window.scrollBy(0, window.innerHeight * 0.9)`)
		pause(2*time.Second, 4*time.Second)
		if err := collect(); err != nil {
			return nil, err
		}
	}

	now := time.Now().UTC()
	var out []Listing
	padded := 0
	for _, c := range cards {
		l := Listing{ID: c.ID, URL: marketplaceURL + "/item/" + c.ID + "/", ScrapedAt: now}
		i := 0
		for ; i < len(c.Lines) && (moneyRe.MatchString(c.Lines[i]) || strings.EqualFold(c.Lines[i], "free")); i++ {
			if i == 0 {
				l.Price = parseMoney(c.Lines[i])
			} else if l.PrevPrice == 0 {
				l.PrevPrice = parseMoney(c.Lines[i])
			}
		}
		if i < len(c.Lines) {
			l.Title = c.Lines[i]
		}
		if i+1 < len(c.Lines) {
			l.Location = c.Lines[i+1]
		}
		if m := yearRe.FindStringSubmatch(l.Title); m != nil {
			l.Year, _ = strconv.Atoi(m[1])
		}
		// Marketplace pads thin results with "similar" listings — a search for
		// "toyota avalon" comes back half Camrys. Keep a card only if its title
		// carries every query word; a Camry asking price is not an Avalon comp.
		if !titleMatches(l.Title, p.Query) {
			padded++
			continue
		}
		out = append(out, l)
	}
	slog.Info("fb search", "query", p.Query, "cards", len(out), "dropped_unmatched", padded)
	return out, nil
}

func titleMatches(title, query string) bool {
	t := strings.ToLower(title)
	for _, w := range strings.Fields(strings.ToLower(query)) {
		if !strings.Contains(t, w) {
			return false
		}
	}
	return true
}

const itemJS = `() => {
  const main = document.querySelector('[role="main"]') || document.body;
  return main.innerText.split('\n').map(s => s.trim()).filter(Boolean).slice(0, 80);
}`

var (
	milesRe  = regexp.MustCompile(`(?i)^Driven ([\d,]+) miles`)
	ownersRe = regexp.MustCompile(`(?i)^(\d+) owners?$`)
	listedRe = regexp.MustCompile(`(?i)^Listed (.+?) ago`)
)

// Item fills the detail fields from a listing's own page. Only the block
// above "Seller information" is read — below it Facebook renders unrelated
// suggested listings whose prices and mileage would contaminate this one.
func (s *Scraper) Item(ctx context.Context, l *Listing) error {
	page, err := s.br.NewPage(marketplaceURL + "/item/" + l.ID + "/")
	if err != nil {
		return err
	}
	defer page.Close()
	_ = page.Timeout(30 * time.Second).WaitLoad()
	pause(3*time.Second, 5*time.Second)
	if loggedOut(page) {
		return ErrLoggedOut
	}
	r, err := page.Eval(itemJS)
	if err != nil {
		return fmt.Errorf("extract item: %w", err)
	}
	var lines []string
	if err := r.Value.Unmarshal(&lines); err != nil {
		return err
	}
	l.ScrapedAt = time.Now().UTC()

	inDesc := false
	var desc []string
	found := false
	for _, ln := range lines {
		if strings.HasPrefix(ln, "Seller information") || strings.HasPrefix(ln, "Seller details") {
			break
		}
		switch {
		case inDesc && strings.Contains(ln, "Location is approximate"):
			inDesc = false // no description: the next block is the map caption
		case inDesc:
			if ln != "See more" && ln != "See less" {
				desc = append(desc, ln)
			}
		case ln == "Seller's description":
			inDesc = true
		case milesRe.MatchString(ln):
			l.Miles = parseMoney(milesRe.FindStringSubmatch(ln)[1])
			found = true
		case ownersRe.MatchString(ln):
			l.Owners, _ = strconv.Atoi(ownersRe.FindStringSubmatch(ln)[1])
		case strings.HasSuffix(strings.ToLower(ln), " title"):
			l.TitleStatus = ln
		case strings.HasSuffix(ln, "transmission"):
			l.Transmission = strings.TrimSuffix(ln, " transmission")
		case strings.HasPrefix(ln, "Exterior color"):
			l.Colors = ln
		case listedRe.MatchString(ln):
			l.ListedAgo = listedRe.FindStringSubmatch(ln)[1]
			found = true
		case strings.EqualFold(ln, "Sold"), strings.HasPrefix(ln, "Sold ·"):
			l.Sold = true
		case l.Price == 0 && moneyRe.MatchString(ln):
			l.Price = parseMoney(ln)
		}
	}
	if !found {
		// No "Listed … ago" and no mileage: the listing was removed (or never was
		// a vehicle). Reported, not guessed at — the tracker decides what it means.
		l.Gone = true
		return nil
	}
	l.Description = strings.Join(desc, "\n")
	l.Detailed = true
	return nil
}

// Pause between item fetches; exported so the command can pace a batch.
func Pace() { pause(4*time.Second, 9*time.Second) }
