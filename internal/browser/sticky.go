package browser

import (
	"time"

	"github.com/vincentrosso/proxypool"
)

// StickyProxy injects a fresh Smartproxy sticky session into proxyURL's username
// (e.g. "smart-exoprox" → "smart-exoprox_area-US_life-5_session-ID"), so one
// browser launch holds a single residential IP for the whole render instead of
// rotating per request. Rotating IPs mid-render are what get challenged/blocked
// on KBB and Copart detail pages; a stable IP fixes it. Pass a new sessionID per
// attempt/worker to draw a new IP. No-op for empty URLs or non-Smartproxy
// proxies, so it's safe to call on any proxy string.
//
// Thin wrapper over the shared proxypool module so every scraper (ferret + the
// hammer watcher) formats sticky sessions identically — one source of truth.
func StickyProxy(proxyURL, sessionID string) string {
	return proxypool.Sticky(proxyURL, "US", 5*time.Minute, sessionID)
}
