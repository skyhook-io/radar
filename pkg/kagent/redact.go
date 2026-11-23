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
	urlInText      = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s"'<>]+`)
	sensitiveQuery = regexp.MustCompile(`(?i)(?:^|[?&#;])[^=&#]*(?:key|token|sig|secret|passw|auth|credential|session)[^=&#]*=`)
	// A bare flag naming a credential, whose value is the next element.
	credentialFlag = regexp.MustCompile(`(?i)^--?[a-z0-9_.-]*(?:token|key|secret|passw|credential|auth)[a-z0-9_.-]*$`)
	// Words that mark an element as possibly holding a credential.
	credentialText = regexp.MustCompile(`(?i)token|secret|password|passwd|apikey|api_key|api-key|authorization|bearer|credential|cookie|private_key|access_key`)
	urlUserInfo    = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^/\s?#]*@`)
	// user:password@host with no scheme, at the start of a word: the user has
	// no slash or colon, the password no slash, so a registry path or digest
	// does not match.
	schemelessUserInfo = regexp.MustCompile(`(^|[\s"'(=,<])[A-Za-z0-9._~%!$&*+-]+:[^\s@/"']+@`)
)

// RedactURLsInText blanks the user info of every URL in free text, and its
// query when the query names a credential (?api_key=, &sig=, ?token=). Free
// text, such as a condition message quoting a discovery error, keeps
// ordinary query strings. A scheme-less user:password@host (an Ollama host
// can be written so) loses its user info too; it needs the colon, so an
// e-mail address or an image digest (repo@sha256:…) is left alone.
func RedactURLsInText(text string) string {
	if strings.Contains(text, "://") {
		text = urlInText.ReplaceAllStringFunc(text, func(match string) string {
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
	if strings.Contains(text, "@") {
		text = schemelessUserInfo.ReplaceAllString(text, "${1}"+Redacted+"@")
	}
	return text
}

// ArgRedacted replaces a command or argument element that may hold a
// credential.
const ArgRedacted = "[redacted: may hold a credential]"

// RedactArgs redacts a Harness's or SandboxTemplate's workload command and
// args, element by element, without parsing shell text: a shell's -c payload
// can nest, quote and chain commands, and a parser that misses one form leaks
// it. An element that mentions a credential, or holds a URL with user info,
// is replaced whole; the element after a bare credential flag (--token x) is
// too; any other element keeps its text, its URLs without user info or query.
// Over-redaction is accepted: these elements reach the drawer and AI context
// only, and the object's YAML still shows them to whoever may read it.
func RedactArgs(args []string) []string {
	out := make([]string, len(args))
	blankNext := false
	for i, a := range args {
		switch {
		case blankNext:
			out[i] = ArgRedacted
		case credentialFlag.MatchString(a):
			out[i] = a
		case credentialText.MatchString(a) || urlUserInfo.MatchString(a):
			out[i] = ArgRedacted
		default:
			out[i] = urlInText.ReplaceAllStringFunc(a, func(match string) string {
				u := strings.TrimRight(match, ".,;:!?)]}")
				return StripURLCredentials(u) + match[len(u):]
			})
		}
		blankNext = credentialFlag.MatchString(a) && !blankNext
	}
	return out
}

// urlColumnPaths are printer-column JSONPaths that read a URL field, whose
// whole query and user info are suspect.
var urlColumnPaths = map[string]bool{".spec.url": true, ".spec.endpoint": true}

// RedactPrinterCell redacts one kagent printer-column cell, which the API
// server's table conversion copies verbatim from the object: a URL column
// (RemoteMCPServer's URL, ModelProviderConfig's endpoint) loses its user
// info, query and fragment, and any other text the user info of the URLs in
// it and a query naming a credential.
func RedactPrinterCell(jsonPath, value string) string {
	if urlColumnPaths[strings.TrimSpace(jsonPath)] {
		return StripURLCredentials(value)
	}
	return RedactURLsInText(value)
}
