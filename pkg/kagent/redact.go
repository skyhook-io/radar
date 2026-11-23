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
	sensitiveFlag  = regexp.MustCompile(`(?i)^(--?[a-z0-9_.-]*(?:token|key|secret|passw|credential|auth)[a-z0-9_.-]*)(=.*)?$`)
	assignment     = regexp.MustCompile(`(?i)^([a-z_][a-z0-9_]*)=.+$`)
	credentialWord = regexp.MustCompile(`(?i)token|key|secret|passw|credential|auth`)
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
// that names a credential: --api-key=x, --token x, API_TOKEN=x, TOKEN=x. An
// argument holding a shell payload (sh -c "serve --api-key=x") is read word by
// word with the same rules.
func RedactArgs(args []string) []string {
	out := make([]string, len(args))
	blankNext := false
	for i, a := range args {
		if blankNext && !strings.HasPrefix(a, "-") {
			out[i], blankNext = Redacted, false
			continue
		}
		out[i], blankNext = redactArg(a)
		if strings.ContainsAny(a, " \t") {
			out[i], blankNext = redactShellWords(a), false
		}
	}
	return out
}

// redactArg redacts one word, and reports whether the word is a credential
// flag whose value is the next word.
func redactArg(a string) (string, bool) {
	if m := sensitiveFlag.FindStringSubmatch(a); m != nil {
		if m[2] != "" {
			return m[1] + "=" + Redacted, false
		}
		return a, true
	}
	if m := assignment.FindStringSubmatch(a); m != nil && credentialWord.MatchString(m[1]) {
		return m[1] + "=" + Redacted, false
	}
	return RedactURLsInText(a), false
}

func redactShellWords(payload string) string {
	words := strings.Fields(payload)
	blankNext := false
	for i, w := range words {
		if blankNext && !strings.HasPrefix(w, "-") {
			words[i], blankNext = Redacted, false
			continue
		}
		words[i], blankNext = redactArg(w)
	}
	return strings.Join(words, " ")
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
