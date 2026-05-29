package browser

import (
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

type Options struct {
	Headless  bool
	UserAgent string
	Timeout   time.Duration
	// ProxyURL sets a per-browser HTTP proxy (e.g. "http://user:pass@host:port").
	// Required when running on VPS IPs blocked by Imperva/Incapsula (IAAI, sometimes Copart).
	// TODO: wire up via COPART_PROXY_URL / IAAI_PROXY_URL env vars in cmd/ferret.
	ProxyURL string
}

type Browser struct {
	rod  *rod.Browser
	opts Options
}

func New(opts Options) (*Browser, error) {
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.UserAgent == "" {
		opts.UserAgent = defaultUA
	}

	l := launcher.New().
		Headless(opts.Headless).
		// Remove the "--enable-automation" flag Chrome adds by default.
		// This flag is the primary signal anti-bot systems check.
		Set("disable-blink-features", "AutomationControlled").
		Delete("enable-automation")

	if opts.ProxyURL != "" {
		l = l.Proxy(opts.ProxyURL)
	}

	u := l.MustLaunch()

	b := rod.New().ControlURL(u).MustConnect()
	b.MustIgnoreCertErrors(true)

	// Clear any stale cookies from previous sessions
	b.MustSetCookies()

	return &Browser{rod: b, opts: opts}, nil
}

func (b *Browser) NewPage(url string) (*rod.Page, error) {
	page, err := b.rod.Page(proto.TargetCreateTarget{URL: url})
	if err != nil {
		return nil, err
	}
	// No blanket timeout — scrapers are long-running; use per-operation timeouts.

	// Override UA at the network layer
	if err := page.SetUserAgent(&proto.NetworkSetUserAgentOverride{
		UserAgent:         b.opts.UserAgent,
		AcceptLanguage:    "en-US,en;q=0.9",
		Platform:          "MacIntel",
	}); err != nil {
		return nil, err
	}

	// Stealth JS — runs before page scripts so the patches are in place
	page.MustEval(stealthJS)

	return page, nil
}

// Rod returns the underlying *rod.Browser for protocol-level calls.
func (b *Browser) Rod() *rod.Browser { return b.rod }

func (b *Browser) Close() {
	b.rod.MustClose()
}

const defaultUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
	"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

// stealthJS patches the page's JS environment to remove automation signals.
// Mirrors the key patches from undetected-chromedriver / puppeteer-extra-stealth.
const stealthJS = `() => {
	// 1. Hide navigator.webdriver
	Object.defineProperty(navigator, 'webdriver', {
		get: () => undefined,
		configurable: true,
	});

	// 2. Restore navigator.plugins to a realistic non-empty list
	if (navigator.plugins.length === 0) {
		Object.defineProperty(navigator, 'plugins', {
			get: () => {
				const plugins = [
					{ name: 'Chrome PDF Plugin', filename: 'internal-pdf-viewer', description: 'Portable Document Format' },
					{ name: 'Chrome PDF Viewer', filename: 'mhjfbmdgcfjbbpaeojofohoefgiehjai', description: '' },
					{ name: 'Native Client', filename: 'internal-nacl-plugin', description: '' },
				];
				plugins.__proto__ = PluginArray.prototype;
				return plugins;
			},
		});
	}

	// 3. Realistic language settings
	Object.defineProperty(navigator, 'languages', {
		get: () => ['en-US', 'en'],
	});

	// 4. Remove Chrome's automation-specific properties from window.chrome
	if (window.chrome && window.chrome.app && window.chrome.app.isInstalled === false) {
		window.chrome.app.isInstalled = undefined;
	}

	// 5. Spoof permissions.query to not reveal headless state
	if (navigator.permissions && navigator.permissions.query) {
		const origQuery = navigator.permissions.query.bind(navigator.permissions);
		navigator.permissions.query = (params) => {
			if (params.name === 'notifications') {
				return Promise.resolve({ state: 'denied', onchange: null });
			}
			return origQuery(params);
		};
	}
}`
