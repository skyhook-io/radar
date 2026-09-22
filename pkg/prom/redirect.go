package prom

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// NormalizeOrigin compares scheme, hostname and effective port, including the
// default HTTP(S) port when it is omitted. Paths are not part of an origin.
func NormalizeOrigin(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port != "" {
		number, err := strconv.ParseUint(port, 10, 16)
		if err != nil {
			return "", false
		}
		port = strconv.FormatUint(number, 10)
	} else {
		switch scheme {
		case "https":
			port = "443"
		case "http":
			port = "80"
		}
	}
	return scheme + "://" + host + ":" + port, true
}

// SameOriginRedirectClient copies a client, preserving its transport and stricter
// redirect policy while refusing redirects that could expose configured headers.
func SameOriginRedirectClient(client *http.Client) *http.Client {
	guarded := *client
	guarded.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if len(via) == 0 {
			return errors.New("redirect has no original request")
		}
		origin, ok := NormalizeOrigin(via[0].URL.String())
		check := func() error {
			destination, valid := NormalizeOrigin(next.URL.String())
			if !ok || !valid || destination != origin {
				return errors.New("cross-origin redirect refused: configure the final backend URL directly")
			}
			return nil
		}
		if err := check(); err != nil {
			return err
		}
		if client.CheckRedirect != nil {
			if err := client.CheckRedirect(next, via); err != nil {
				return err
			}
		}
		// A caller policy may rewrite the destination request.
		return check()
	}
	return &guarded
}
