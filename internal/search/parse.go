package search

import (
	"net/netip"
	"strings"
	"unicode"
)

// Parse parses a query string into a Query.
//
// Tokens are split on whitespace. Modifiers are recognized by a "<key>:" prefix
// where key is one of: kind, k, ns, n, namespace, cluster, c, label, l, image.
// Quoted segments ("foo bar") are kept as a single token. A token that is an
// IP address, ip:port or [v6]:port becomes the canonical address and matches
// whole addresses only (see parseIPToken).
//
// Examples:
//
//	"redis"                          → {Tokens: [redis]}
//	"kind:Pod redis"                 → {KindFilter: [Pod], Tokens: [redis]}
//	"l:app=foo image:nginx"          → {LabelFilter: [{app,foo}], ImageFilter: [nginx]}
//	"\"my service\" ns:prod"         → {NSFilter: [prod], Tokens: [my service]}
func Parse(q string) Query {
	out := Query{Raw: q}
	for _, tok := range tokenize(q) {
		if addr, ok := parseIPToken(tok); ok {
			ip := addr.String()
			if out.ips == nil {
				out.ips = map[string]netip.Addr{}
			}
			out.ips[ip] = addr
			out.Tokens = append(out.Tokens, ip)
			continue
		}
		key, val, ok := splitModifier(tok)
		if !ok {
			out.Tokens = append(out.Tokens, tok)
			continue
		}
		switch key {
		case "kind", "k":
			out.KindFilter = append(out.KindFilter, val)
		case "ns", "n", "namespace":
			out.NSFilter = append(out.NSFilter, val)
		case "cluster", "c":
			out.Cluster = val
		case "label", "l":
			out.LabelFilter = append(out.LabelFilter, parseLabelEq(val))
		case "image", "img":
			out.ImageFilter = append(out.ImageFilter, val)
		default:
			// Unknown modifier — keep the original token so the user sees
			// it landed somewhere instead of silently disappearing.
			out.Tokens = append(out.Tokens, tok)
		}
	}
	return out
}

// splitModifier splits "key:value" once at the first colon.
// Only treats it as a modifier if the key is non-empty and contains only
// letters (so URLs and names with colons aren't accidentally parsed).
func splitModifier(s string) (key, val string, ok bool) {
	i := strings.IndexByte(s, ':')
	if i <= 0 || i == len(s)-1 {
		return "", "", false
	}
	k := s[:i]
	for _, r := range k {
		if !unicode.IsLetter(r) {
			return "", "", false
		}
	}
	return strings.ToLower(k), s[i+1:], true
}

// IPTokens returns the free tokens Parse read as IP addresses, canonical.
func (q Query) IPTokens() []string {
	var out []string
	for _, tok := range q.Tokens {
		if _, ok := q.ips[tok]; ok {
			out = append(out, tok)
		}
	}
	return out
}

func parseLabelEq(v string) LabelEq {
	k, val, ok := strings.Cut(v, "=")
	if !ok {
		return LabelEq{Key: v}
	}
	return LabelEq{Key: k, Value: val}
}

// tokenize splits on whitespace, respecting double-quoted segments.
func tokenize(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
		case unicode.IsSpace(r) && !inQuote:
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}
