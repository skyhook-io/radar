// Package logsafe provides minimal helpers for embedding user-controlled
// values in structured log lines without inviting log forgery.
//
// The agent-context log paths (internal/mcp, internal/server) both write
// logfmt-style lines that include attacker-influenceable fields (MCP tool
// input, chi URL params). Without sanitization, a value like
// "Pod\nlevel=error fake=line" would inject a forged log entry that
// downstream scrapers parse as a separate event.
//
// This package intentionally lives at the internal/ root (not inside one
// of the consumers) because both internal/mcp and internal/server import
// it; internal subpackages can't import each other peer-to-peer.
package logsafe

import "strings"

// Sanitize strips newlines, carriage returns, and other control characters
// from s, replacing each with '_'. Replacement (rather than removal) keeps
// the untrusted value visibly present in the line so operators can still
// see what was attempted.
//
// This is intentionally narrow: callers should pass only the small set of
// genuinely user-controlled fields (kind, namespace, route pattern, etc.).
// Internally-set values (tool names, component identifiers) don't need it.
func Sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r < 0x20 || r == 0x7f {
			return '_'
		}
		return r
	}, s)
}
