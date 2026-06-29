// Package logsafe provides minimal helpers for embedding user-controlled
// values in structured (logfmt-style) log lines without inviting log forgery.
//
// The agent-context log paths (internal/mcp, internal/server) both write
// logfmt-style lines that include attacker-influenceable fields (MCP tool
// input, chi URL params). Two attack vectors must be neutralized:
//
//  1. Multi-line injection: a value like "Pod\nlevel=error fake=line"
//     splits onto a new line that scrapers parse as a separate event.
//  2. Same-line field injection: a value like "Pod level=error fake=line"
//     introduces NEW logfmt fields on the SAME line — "level=error" would
//     override the legitimate level= field earlier on the line.
//
// Sanitize neutralizes both: it replaces newlines, carriage returns, other
// control characters, AND the two logfmt field separators (space and "=")
// with '_'. Legitimate values (Kubernetes kind names, RFC 1123 namespace
// names, chi route patterns) never contain these characters; attacker-
// crafted values do.
//
// This package intentionally lives at the internal/ root (not inside one
// of the consumers) because both internal/mcp and internal/server import
// it; internal subpackages can't import each other peer-to-peer.
package logsafe

import "strings"

// Sanitize replaces dangerous runes in s with '_'. Replacement (rather
// than removal) keeps the untrusted value visibly present in the line so
// operators can still see what was attempted, while preventing the value
// from being parsed as logfmt structure.
//
// Replaced runes:
//   - '\n', '\r' — multi-line injection
//   - control chars (<0x20), DEL (0x7f) — malformed-output evasion
//   - ' ' (space) — logfmt field separator; prevents same-line injection
//   - '=' — logfmt key/value separator; prevents same-line injection
//
// This is intentionally narrow: callers should pass only the small set of
// genuinely user-controlled fields (kind, namespace, route pattern, etc.).
// Internally-set values (tool names, component identifiers) don't need it.
func Sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r < 0x20 || r == 0x7f || r == ' ' || r == '=' {
			return '_'
		}
		return r
	}, s)
}

// EstimateTokens converts a response byte count into a rough token estimate
// using an English-JSON heuristic of ~4 characters per token. Cheap,
// deterministic, and good enough for "is this response 200 tokens or 200k
// tokens?" budgeting decisions. Real tokenization happens client-side; this
// is only a budget signal.
//
// Lives here (alongside Sanitize) so MCP and REST log emitters share a single
// definition. Without this, divergence between the two log-line `est_tokens`
// fields is one heuristic change away.
func EstimateTokens(bytes int) int {
	return bytes / 4
}
