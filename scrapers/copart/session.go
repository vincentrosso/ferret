package copart

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/go-rod/rod/lib/proto"
)

// The two cookies that decide, authoritatively, whether the saved session is a live
// MEMBER session. Login() already gates on memberCookie and says so in its own comment
// ("the authoritative signal is the SESSION COOKIE, never the URL") — these constants
// exist so IsLoggedIn can hold to the same rule instead of guessing from the DOM.
//
// Both are required, and neither is sufficient alone:
//   - memberCookie carries the member NAME and lives ~30 DAYS. It proves the session was
//     a member session; it long outlives the session itself, so it can never date it.
//   - sessionCookie is the actual server-side session and expires in ~8h. It dates the
//     session but says nothing about who it belongs to.
const (
	sessionCookie = "g2usersessionid"
	memberCookie  = "g2app.logged-in-member-name"
)

// SessionState is what the cookie jar on disk says about our session, with no browser and
// no network. Fast (a file read) and authoritative about EXPIRY — it cannot see a
// server-side invalidation, which is why callers still probe periodically.
type SessionState struct {
	Member    string
	ExpiresAt time.Time
	Live      bool
	Reason    string
}

// SessionFileState reports whether the cookie file at path holds an unexpired member
// session. Live is false with a human Reason on every negative, so a caller can log WHY it
// is about to spend an Incapsula-exposed login.
func SessionFileState(path string) SessionState {
	b, err := os.ReadFile(path)
	if err != nil {
		return SessionState{Reason: fmt.Sprintf("no session file (%v)", err)}
	}
	var raw []savedCookie
	if err := json.Unmarshal(b, &raw); err != nil {
		return SessionState{Reason: fmt.Sprintf("unreadable session file: %v", err)}
	}
	var member string
	var exp float64
	var haveSession bool
	for _, c := range raw {
		switch c.Name {
		case memberCookie:
			member = c.Value
		case sessionCookie:
			haveSession = true
			if c.Expires > exp {
				exp = c.Expires
			}
		}
	}
	if member == "" {
		return SessionState{Reason: "no member cookie — session is anonymous, not logged in"}
	}
	if !haveSession {
		return SessionState{Member: member, Reason: "no " + sessionCookie + " cookie"}
	}
	if exp <= 0 {
		// A pure session cookie (no expiry) can't be dated from disk. Refuse the fast
		// path rather than assume — the caller falls through to a real probe.
		return SessionState{Member: member, Reason: sessionCookie + " has no expiry — cannot date from disk"}
	}
	at := time.Unix(int64(exp), 0).UTC()
	if time.Now().After(at) {
		return SessionState{Member: member, ExpiresAt: at,
			Reason: "session cookie expired at " + at.Format(time.RFC3339)}
	}
	return SessionState{Member: member, ExpiresAt: at, Live: true}
}

type savedCookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain"`
	Path     string  `json:"path"`
	Expires  float64 `json:"expires,omitempty"`
	HTTPOnly bool    `json:"httpOnly"`
	Secure   bool    `json:"secure"`
	SameSite string  `json:"sameSite,omitempty"`
}

func saveCookies(cookies []*proto.NetworkCookie, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var out []savedCookie
	for _, c := range cookies {
		out = append(out, savedCookie{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   string(c.Domain),
			Path:     c.Path,
			Expires:  float64(c.Expires),
			HTTPOnly: c.HTTPOnly,
			Secure:   c.Secure,
			SameSite: string(c.SameSite),
		})
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func loadCookies(path string) ([]*proto.NetworkCookieParam, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw []savedCookie
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	var out []*proto.NetworkCookieParam
	for _, c := range raw {
		cp := &proto.NetworkCookieParam{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   c.Domain,
			Path:     c.Path,
			HTTPOnly: c.HTTPOnly,
			Secure:   c.Secure,
			Expires:  proto.TimeSinceEpoch(c.Expires),
		}
		out = append(out, cp)
	}
	return out, nil
}
