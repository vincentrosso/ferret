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
	baseURL    = "https://www.copart.com"
	loginURL   = "https://www.copart.com/login"
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
	defer page.MustClose()

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
	defer page.MustClose()

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

// Search implements scraper.Scraper — stub until search is built.
func (s *Scraper) Search(_ context.Context, _ *rod.Page, _ scraper.Query) ([]string, error) {
	return nil, fmt.Errorf("Search not yet implemented")
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
