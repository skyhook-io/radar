package prom

import (
	"net/url"
	"regexp"
	"strings"
)

// urlPattern matches a URL in free-form diagnostic text. It stops at the quote
// net/url's own errors wrap URLs in, so `Get "http://host/x": dial tcp` leaves
// the cause outside the match.
var urlPattern = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^\s"]*`)

// redactedURLPlaceholder stands in for the address. It names no host, so a
// message carrying it is safe to forward to an MCP client or an LLM prompt.
const redactedURLPlaceholder = "<redacted>"

// RedactURLs replaces the origin and query string of every URL in a diagnostic
// message, keeping the path. Errors from this package embed the request URL,
// which names the backend and can carry credentials two ways: in userinfo, as
// a URL passed to --prometheus-url can, and as a query parameter, which is how
// an auth-proxied endpoint is commonly configured (?token=...). These messages
// reach MCP clients and LLM prompts, so the address must not survive; the path
// still says which call failed.
func RedactURLs(message string) string {
	return urlPattern.ReplaceAllStringFunc(message, func(match string) string {
		// Trailing punctuation belongs to the sentence, not the URL: in
		// "error from <url>: bad_data" the colon separates the two, and
		// swallowing it would also make the address unparseable.
		address := strings.TrimRight(match, ".,;:")
		suffix := match[len(address):]
		parsed, err := url.Parse(address)
		if err != nil {
			return redactedURLPlaceholder + suffix
		}
		return redactedURLPlaceholder + parsed.EscapedPath() + suffix
	})
}

// SafeAddress reduces a configured URL to where the backend is: scheme, host
// and path. Unlike RedactURLs it keeps the host, because an address is shown
// to say WHICH backend answered — but a Prometheus behind an auth proxy is
// commonly configured with the credential in the query string (?token=…)
// rather than in userinfo, so stripping userinfo alone still discloses it. The
// fragment goes for the same reason.
func SafeAddress(address string) string {
	parsed, err := url.Parse(address)
	if err != nil {
		// An unparseable address can still carry a credential verbatim.
		return redactedURLPlaceholder
	}
	if parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" {
		return address
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.RawFragment = ""
	return parsed.String()
}
