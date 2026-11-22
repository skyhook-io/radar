package kagent

import (
	"regexp"
	"strings"
)

// Redacted replaces a value that may be a credential.
const Redacted = "[REDACTED]"

// StripURLCredentials blanks a URL's user info, query and fragment, keeping
// its scheme, host and path. kagent's URL fields mostly have no admission
// pattern, and the runtime rejects user info only on Secret-bound
// destinations (go/core/internal/translator/credentials.go:111), so a token
// can sit in any of them. A scheme-less value (an Ollama host such as
// user:pw@host:11434) is read the same way.
func StripURLCredentials(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return raw
	}
	scheme, rest := "", s
	if i := strings.Index(s, "://"); i > 0 {
		scheme, rest = s[:i+3], s[i+3:]
	}
	authority, tail := rest, ""
	if end := strings.IndexAny(rest, "/?#"); end >= 0 {
		authority, tail = rest[:end], rest[end:]
	}
	if at := strings.LastIndex(authority, "@"); at >= 0 {
		authority = Redacted + "@" + authority[at+1:]
	}
	if q := strings.IndexAny(tail, "?#"); q >= 0 {
		tail = tail[:q+1] + Redacted
	}
	return scheme + authority + tail
}

var (
	urlInText       = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s"'<>]+`)
	sensitiveQuery  = regexp.MustCompile(`(?i)(?:^|[?&#;])[^=&#]*(?:key|token|sig|secret|passw|auth|credential|session)[^=&#]*=`)
	sensitiveFlag   = regexp.MustCompile(`(?i)^(--?[a-z0-9_.-]*(?:token|key|secret|passw|credential|auth)[a-z0-9_.-]*)(=.*)?$`)
	sensitiveAssign = regexp.MustCompile(`(?i)^([a-z_][a-z0-9_]*(?:token|key|secret|passw|credential|auth)[a-z0-9_]*)=.+$`)
)

// RedactURLsInText blanks the user info of every URL in free text, and its
// query when the query names a credential (?api_key=, &sig=, ?token=). Free
// text, such as a condition message quoting a discovery error, keeps
// ordinary query strings.
func RedactURLsInText(text string) string {
	if !strings.Contains(text, "://") {
		return text
	}
	return urlInText.ReplaceAllStringFunc(text, func(match string) string {
		// Sentence punctuation after a URL is not part of it.
		u := strings.TrimRight(match, ".,;:!?)]}")
		trailing := match[len(u):]
		stripped := StripURLCredentials(u)
		if q := strings.IndexAny(u, "?#"); q >= 0 && !sensitiveQuery.MatchString(u[q:]) {
			// Keep an ordinary query: restore it after the user info is gone.
			if s := strings.IndexAny(stripped, "?#"); s >= 0 {
				stripped = stripped[:s] + u[q:]
			}
		}
		return stripped + trailing
	})
}

// RedactArgs blanks the values a command line passes to a flag or variable
// that names a credential: --api-key=x, --token x, API_TOKEN=x.
func RedactArgs(args []string) []string {
	out := make([]string, len(args))
	blankNext := false
	for i, a := range args {
		switch {
		case blankNext && !strings.HasPrefix(a, "-"):
			out[i] = Redacted
			blankNext = false
			continue
		case sensitiveFlag.MatchString(a):
			m := sensitiveFlag.FindStringSubmatch(a)
			if m[2] != "" {
				out[i] = m[1] + "=" + Redacted
			} else {
				out[i] = a
				blankNext = true
				continue
			}
		case sensitiveAssign.MatchString(a):
			out[i] = a[:strings.Index(a, "=")+1] + Redacted
		default:
			out[i] = RedactURLsInText(a)
		}
		blankNext = false
	}
	return out
}
