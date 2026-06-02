package browser

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"runtime"
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
		Set("disable-blink-features", "AutomationControlled").
		Delete("enable-automation")

	// Use the system Chrome if present (installed via .deb on the server) instead
	// of letting go-rod download its own — the download cache (~/.cache/rod) isn't
	// writable when the FastAPI service runs us as www-data.
	if path, ok := launcher.LookPath(); ok {
		l = l.Bin(path)
	}

	// Required when running as root on Linux (VPS/server).
	if runtime.GOOS == "linux" {
		l = l.Set("no-sandbox", "")
	}

	// Authenticated proxy: Chrome can't auth an upstream proxy via --proxy-server
	// (407s, and the CDP Fetch/HandleAuth path stalls connections). The reliable
	// fix is a tiny local relay that injects Proxy-Authorization on every CONNECT
	// and tunnels to the upstream proxy; Chrome points at the relay with no creds.
	if opts.ProxyURL != "" {
		if pu, err := url.Parse(opts.ProxyURL); err == nil && pu.Host != "" && pu.User != nil {
			if local, err := startProxyRelay(pu); err == nil {
				l = l.Proxy("http://" + local)
			} else {
				l = l.Proxy(opts.ProxyURL) // fall back; may 407
			}
		} else {
			l = l.Proxy(opts.ProxyURL) // no creds — Chrome handles it
		}
	}

	u := l.MustLaunch()

	b := rod.New().ControlURL(u).MustConnect()
	b.MustIgnoreCertErrors(true)

	// Clear any stale cookies from previous sessions
	b.MustSetCookies()

	return &Browser{rod: b, opts: opts}, nil
}

// startProxyRelay listens on a local port and forwards Chrome's connections to
// the upstream proxy, injecting Proxy-Authorization on each CONNECT. Returns the
// local "127.0.0.1:PORT" address. The listener lives for the process lifetime.
func startProxyRelay(upstream *url.URL) (string, error) {
	user := upstream.User.Username()
	pass, _ := upstream.User.Password()
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
	upHost := upstream.Host

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go relayConn(c, upHost, auth)
		}
	}()
	return ln.Addr().String(), nil
}

func relayConn(client net.Conn, upHost, auth string) {
	defer client.Close()

	cr := bufio.NewReader(client)
	req, err := http.ReadRequest(cr)
	if err != nil {
		return
	}

	up, err := net.DialTimeout("tcp", upHost, 10*time.Second)
	if err != nil {
		return
	}
	defer up.Close()

	if req.Method == http.MethodConnect {
		// Forward the CONNECT to the upstream proxy with credentials.
		fmt.Fprintf(up, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: %s\r\n\r\n",
			req.Host, req.Host, auth)
		ur := bufio.NewReader(up)
		resp, err := http.ReadResponse(ur, req)
		if err != nil {
			return
		}
		if resp.StatusCode != 200 {
			fmt.Fprintf(client, "HTTP/1.1 %d %s\r\n\r\n", resp.StatusCode, resp.Status)
			return
		}
		fmt.Fprint(client, "HTTP/1.1 200 Connection established\r\n\r\n")
		// Drain any bytes the buffered readers already consumed, then tunnel raw.
		if n := cr.Buffered(); n > 0 {
			b, _ := cr.Peek(n)
			up.Write(b)
		}
		if n := ur.Buffered(); n > 0 {
			b, _ := ur.Peek(n)
			client.Write(b)
		}
		go io.Copy(up, client)
		io.Copy(client, up)
		return
	}

	// Plain HTTP — add auth and forward through the upstream proxy.
	req.Header.Set("Proxy-Authorization", auth)
	if err := req.WriteProxy(up); err != nil {
		return
	}
	go io.Copy(up, client)
	io.Copy(client, up)
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
