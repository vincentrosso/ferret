package copart

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/go-rod/rod/lib/proto"
)

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
