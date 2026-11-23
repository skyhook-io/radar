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
	// A shell's command-string flag: -c, or combined like -ec or -lc.
	shellCommandFlag = regexp.MustCompile(`^-[a-z]*c$`)
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
// that names a credential: --api-key=x, --token x, API_TOKEN=x, TOKEN=x. The
// argument after a shell's -c (sh -c "serve --api-key=x") is a payload, read
// as shell words with quotes respected. Any other credential argument loses
// its whole value, spaces included ("--authorization=Bearer x"); another
// argument with spaces in it is read as a payload too, which only redacts more.
func RedactArgs(args []string) []string {
	out := make([]string, len(args))
	blankNext := false
	for i, a := range args {
		if blankNext && !strings.HasPrefix(a, "-") {
			out[i], blankNext = Redacted, false
			continue
		}
		blankNext = false
		if i > 0 && shellCommandFlag.MatchString(args[i-1]) {
			out[i] = redactShellPayload(a)
			continue
		}
		if red, next, ok := redactCredentialArg(a); ok {
			out[i], blankNext = red, next
			continue
		}
		if strings.ContainsAny(a, " \t\n") {
			out[i] = redactShellPayload(a)
			continue
		}
		out[i] = RedactURLsInText(a)
	}
	return out
}

// redactCredentialArg redacts a credential flag's or assignment's whole value.
// next reports a flag whose value is the following word; ok is false for any
// other argument.
func redactCredentialArg(a string) (red string, next, ok bool) {
	if m := sensitiveFlag.FindStringSubmatch(a); m != nil {
		if m[2] != "" {
			return m[1] + "=" + Redacted, false, true
		}
		return a, true, true
	}
	if m := assignment.FindStringSubmatch(a); m != nil && credentialWord.MatchString(m[1]) {
		return m[1] + "=" + Redacted, false, true
	}
	return "", false, false
}

// shellWord is one word of a shell payload: where it sits, and its text with
// quotes and escapes removed.
type shellWord struct {
	start, end int
	text       string
}

// shellWords splits a payload as a POSIX shell would, closely enough to find
// a credential's value: whitespace separates words, quotes and backslashes
// join them. An unclosed quote runs to the end, so its value is redacted in
// full rather than in part.
func shellWords(s string) []shellWord {
	var out []shellWord
	isSpace := func(c byte) bool { return c == ' ' || c == '\t' || c == '\n' }
	for i := 0; i < len(s); {
		for i < len(s) && isSpace(s[i]) {
			i++
		}
		if i >= len(s) {
			break
		}
		start := i
		var b strings.Builder
		var quote byte
		for i < len(s) {
			c := s[i]
			switch {
			case quote != 0 && c == quote:
				quote = 0
			case quote == '"' && c == '\\' && i+1 < len(s):
				i++
				b.WriteByte(s[i])
			case quote != 0:
				b.WriteByte(c)
			case c == '\'' || c == '"':
				quote = c
			case c == '\\' && i+1 < len(s):
				i++
				b.WriteByte(s[i])
			case isSpace(c):
				goto done
			default:
				b.WriteByte(c)
			}
			i++
		}
	done:
		out = append(out, shellWord{start: start, end: i, text: b.String()})
	}
	return out
}

func redactShellPayload(payload string) string {
	var b strings.Builder
	last := 0
	blankNext := false
	for _, w := range shellWords(payload) {
		b.WriteString(payload[last:w.start])
		last = w.end
		raw := payload[w.start:w.end]
		if blankNext && !strings.HasPrefix(w.text, "-") {
			b.WriteString(Redacted)
			blankNext = false
			continue
		}
		blankNext = false
		if red, next, ok := redactCredentialArg(w.text); ok {
			if next {
				red = raw
			}
			b.WriteString(red)
			blankNext = next
			continue
		}
		b.WriteString(RedactURLsInText(raw))
	}
	b.WriteString(payload[last:])
	return b.String()
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
