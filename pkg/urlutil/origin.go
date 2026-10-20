package urlutil

import (
	"net/url"
	"strconv"
	"strings"
)

// NormalizeOrigin returns an opaque comparison key, not a display URL.
// Host case and effective HTTP(S) ports do not distinguish origins.
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
