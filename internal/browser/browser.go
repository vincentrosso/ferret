package browser

import (
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

type Options struct {
	Headless   bool
	UserAgent  string
	Timeout    time.Duration
	CookieFile string
}

type Browser struct {
	rod  *rod.Browser
	opts Options
}

func New(opts Options) (*Browser, error) {
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Second
	}

	u := launcher.New().
		Headless(opts.Headless).
		MustLaunch()

	b := rod.New().ControlURL(u).MustConnect()
	b.MustIgnoreCertErrors(true)

	if opts.UserAgent != "" {
		b.MustSetCookies() // clear before setting UA via page override
	}

	return &Browser{rod: b, opts: opts}, nil
}

func (b *Browser) NewPage(url string) (*rod.Page, error) {
	page, err := b.rod.Page(proto.TargetCreateTarget{URL: url})
	if err != nil {
		return nil, err
	}
	page = page.Timeout(b.opts.Timeout)

	if b.opts.UserAgent != "" {
		if err := page.SetUserAgent(&proto.NetworkSetUserAgentOverride{
			UserAgent: b.opts.UserAgent,
		}); err != nil {
			return nil, err
		}
	}

	return page, nil
}

func (b *Browser) Close() {
	b.rod.MustClose()
}
