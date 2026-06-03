package browser

import (
	"fmt"
	"net/url"
	"strings"
)

// StickyProxy injects a fresh Smartproxy sticky session into proxyURL's username
// (e.g. "smart-exoprox" → "smart-exoprox_area-US_life-5_session-ID"), so one
// browser launch holds a single residential IP for the whole render instead of
// rotating per request. Rotating IPs mid-render are what get challenged/blocked
// on KBB and Copart detail pages; a stable IP fixes it. Pass a new sessionID per
// attempt/worker to draw a new IP. No-op for empty URLs or non-Smartproxy
// proxies, so it's safe to call on any proxy string.
func StickyProxy(proxyURL, sessionID string) string {
	if proxyURL == "" {
		return proxyURL
	}
	u, err := url.Parse(proxyURL)
	if err != nil || u.User == nil || !strings.Contains(u.Host, "smartproxy") {
		return proxyURL
	}
	user := u.User.Username()
	pw, _ := u.User.Password()
	// Strip any session modifiers we manage, then re-add a fresh set.
	for _, m := range []string{"_session-", "_life-", "_area-"} {
		if i := strings.Index(user, m); i >= 0 {
			user = user[:i]
		}
	}
	user = fmt.Sprintf("%s_area-US_life-5_session-%s", user, sessionID)
	u.User = url.UserPassword(user, pw)
	return u.String()
}
