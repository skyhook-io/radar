// Package traefik holds pure rules about Traefik routes, shared with Radar Hub.
package traefik

import (
	"sort"
	"strings"
	"unicode"
)

// RuleHosts returns the hosts an IngressRoute match expression names in
// Host(...) and HostRegexp(...) calls: Host values lower-cased, regexps
// verbatim, de-duplicated, in order. It reads only those calls, not the full
// rule grammar, and returns nil when one is malformed or none is present, so
// a caller shows the raw rule instead. HostSNI is TCP and isn't read.
func RuleHosts(match string) []string {
	var out []string
	seen := map[string]bool{}
	for i := 0; i < len(match); {
		name, args, next, ok := nextHostCall(match, i)
		if next < 0 {
			break
		}
		if !ok {
			return nil
		}
		for _, a := range args {
			if name == "Host" {
				a = strings.ToLower(a)
			}
			if !seen[a] {
				seen[a] = true
				out = append(out, a)
			}
		}
		i = next
	}
	return out
}

// NormalizeMatch makes equivalent match expressions compare equal for
// duplicate detection: whitespace outside quotes removed, the arguments of
// each Host/HostRegexp call sorted, and top-level ||-terms sorted.
func NormalizeMatch(match string) string {
	terms := splitTopLevel(stripSpace(match), "||")
	for i, term := range terms {
		terms[i] = sortHostArgs(term)
	}
	sort.Strings(terms)
	return strings.Join(terms, "||")
}

// nextHostCall finds the next Host( or HostRegexp( at or after i. next is -1
// when there is none; ok is false when the call is malformed.
func nextHostCall(s string, i int) (name string, args []string, next int, ok bool) {
	for ; i < len(s); i++ {
		if c := s[i]; c == '`' || c == '"' {
			end := strings.IndexByte(s[i+1:], c)
			if end < 0 {
				return "", nil, i, false
			}
			i += end + 1
			continue
		}
		if i > 0 && (unicode.IsLetter(rune(s[i-1])) || unicode.IsDigit(rune(s[i-1]))) {
			continue
		}
		for _, n := range []string{"HostRegexp(", "Host("} {
			if strings.HasPrefix(s[i:], n) {
				args, end, ok := readArgs(s, i+len(n))
				return strings.TrimSuffix(n, "("), args, end, ok
			}
		}
	}
	return "", nil, -1, false
}

// readArgs reads quoted, comma-separated arguments from j up to ')'.
func readArgs(s string, j int) ([]string, int, bool) {
	var args []string
	for {
		for j < len(s) && unicode.IsSpace(rune(s[j])) {
			j++
		}
		if j >= len(s) || (s[j] != '`' && s[j] != '"') {
			return nil, j, false
		}
		q := s[j]
		end := strings.IndexByte(s[j+1:], q)
		if end < 0 {
			return nil, len(s), false
		}
		args = append(args, s[j+1:j+1+end])
		j += end + 2
		for j < len(s) && unicode.IsSpace(rune(s[j])) {
			j++
		}
		if j < len(s) && s[j] == ',' {
			j++
			continue
		}
		if j < len(s) && s[j] == ')' {
			return args, j + 1, true
		}
		return nil, j, false
	}
}

func stripSpace(s string) string {
	var b strings.Builder
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '`' || c == '"':
			quote = c
		case unicode.IsSpace(rune(c)):
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// splitTopLevel splits on sep outside quotes and parentheses.
func splitTopLevel(s, sep string) []string {
	var out []string
	depth, start := 0, 0
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '`' || c == '"':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			depth--
		case depth == 0 && strings.HasPrefix(s[i:], sep):
			out = append(out, s[start:i])
			start = i + len(sep)
			i += len(sep) - 1
		}
	}
	return append(out, s[start:])
}

// sortHostArgs sorts the arguments of a term that is exactly one Host or
// HostRegexp call; any other term is returned unchanged.
func sortHostArgs(term string) string {
	name, args, next, ok := nextHostCall(term, 0)
	if !ok || next != len(term) || !strings.HasPrefix(term, name+"(") {
		return term
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = "`" + a + "`"
	}
	sort.Strings(quoted)
	return name + "(" + strings.Join(quoted, ",") + ")"
}
