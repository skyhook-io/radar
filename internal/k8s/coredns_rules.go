package k8s

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
)

const (
	// MaxCoreDNSRuleExcerpts caps how many rules one response carries.
	MaxCoreDNSRuleExcerpts = 5
	// maxCoreDNSRuleChars caps one rule's text, in bytes.
	maxCoreDNSRuleChars = 400
	coreDNSCorefileKey  = "Corefile"
)

// CoreDNSRuleExcerpt is a rewrite or template rule copied from a stored CoreDNS
// Corefile. It is text from a ConfigMap, not a statement about what CoreDNS
// does: Radar does not know whether the rule runs or whether CoreDNS loaded
// this version of the file.
type CoreDNSRuleExcerpt struct {
	Namespace string
	Name      string
	Key       string
	// Line is the 1-based line of the rule's first line within the key.
	Line int
	Text string
	// Truncated is set when Text was cut at the length cap or the rule's block
	// never closed.
	Truncated bool
}

// CoreDNSRuleExcerpts returns the rewrite and template rules stored in
// kube-system CoreDNS ConfigMaps, ordered by ConfigMap name and then file
// order, capped at MaxCoreDNSRuleExcerpts. omitted is how many more exist.
//
// Callers show these only where DNS is already the subject (observed DNS
// symptoms, a recent CoreDNS change) and gate them on the caller's access to
// kube-system ConfigMaps.
func CoreDNSRuleExcerpts(cache *ResourceCache) (excerpts []CoreDNSRuleExcerpt, omitted int) {
	if cache == nil || cache.ConfigMaps() == nil {
		return nil, 0
	}
	cms, err := cache.ConfigMaps().ConfigMaps("kube-system").List(labels.Everything())
	if err != nil {
		logConfigListError("ConfigMap", "kube-system", err)
		return nil, 0
	}
	var coreDNS []*corev1.ConfigMap
	for _, cm := range cms {
		if cm != nil && strings.Contains(strings.ToLower(cm.Name), "coredns") {
			coreDNS = append(coreDNS, cm)
		}
	}
	sort.Slice(coreDNS, func(i, j int) bool { return coreDNS[i].Name < coreDNS[j].Name })
	for _, cm := range coreDNS {
		for _, rule := range corefileRules(cm.Data[coreDNSCorefileKey]) {
			if len(excerpts) == MaxCoreDNSRuleExcerpts {
				omitted++
				continue
			}
			excerpts = append(excerpts, CoreDNSRuleExcerpt{
				Namespace: cm.Namespace,
				Name:      cm.Name,
				Key:       coreDNSCorefileKey,
				Line:      rule.line,
				Text:      rule.text,
				Truncated: rule.truncated,
			})
		}
	}
	return excerpts, omitted
}

type corefileRule struct {
	line      int
	text      string
	truncated bool
}

// corefileRules copies every rewrite and template directive that sits directly
// inside a server block (or snippet), with its block when it has one. Blocks are
// counted the way CoreDNS's parser counts them: every "{" token opens one and
// every "}" token closes one. It reads structure only and never interprets a
// rule.
func corefileRules(corefile string) []corefileRule {
	rawLines := strings.Split(corefile, "\n")
	excerpt := func(first, last int) (string, bool) {
		lines := make([]string, 0, last-first+1)
		for _, l := range rawLines[first-1 : last] {
			lines = append(lines, strings.TrimSpace(l))
		}
		text := strings.Join(lines, "\n")
		if len(text) <= maxCoreDNSRuleChars {
			return text, false
		}
		cut := maxCoreDNSRuleChars
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		return text[:cut], true
	}

	var (
		rules []corefileRule
		depth int
		// openLine is the first line of the rule being collected, 0 when none.
		openLine int
	)
	finish := func(lastLine int, truncated bool) {
		text, cut := excerpt(openLine, lastLine)
		rules = append(rules, corefileRule{line: openLine, text: text, truncated: truncated || cut})
		openLine = 0
	}
	tokens := corefileTokens(corefile)
	for i := 0; i < len(tokens); i++ {
		t := tokens[i]
		newLine := i == 0 || tokens[i-1].endLine < t.line
		if depth == 0 && newLine {
			if next := corefileBraceless(tokens, i); next > i {
				// The rest of the file is this server's directives.
				depth, i = 1, next-1
				continue
			}
		}
		// A directive ends at the first line break outside its own block.
		if openLine != 0 && newLine && depth == 1 {
			finish(tokens[i-1].endLine, false)
		}
		// A directive starts on a new line, or straight after the brace that
		// opened the server block.
		atStart := newLine || (i > 0 && tokens[i-1].text == "{")
		if openLine == 0 && atStart && depth == 1 && (t.text == "rewrite" || t.text == "template") {
			openLine = t.line
			continue
		}
		switch t.text {
		case "{":
			depth++
		case "}":
			if depth > 0 {
				depth--
			}
			if openLine != 0 && depth == 0 {
				// The server block closed on the rule's own line.
				finish(t.endLine, false)
			}
		}
	}
	if openLine != 0 {
		// Still inside the rule's block at the end of the file: it never closed.
		finish(tokens[len(tokens)-1].endLine, depth > 1)
	}
	return rules
}

// corefileBraceless handles the one form CoreDNS accepts without braces: a
// server block may list its addresses and then its directives with no "{" at
// all, in which case its directives run to the end of the file. Given the index
// of a top-level line, it returns the index of that server's first directive
// token, or from when the line opens a block the usual way or is an import.
func corefileBraceless(tokens []corefileToken, from int) int {
	if tokens[from].text == "import" || tokens[from].text == "}" {
		return from
	}
	// Addresses run to the end of their line, or on to the next while one ends
	// with a comma.
	i := from
	for i < len(tokens) && tokens[i].text != "{" {
		more := strings.HasSuffix(tokens[i].text, ",")
		i++
		if !more && i < len(tokens) && tokens[i-1].endLine < tokens[i].line {
			break
		}
	}
	if i >= len(tokens) || tokens[i].text == "{" {
		return from
	}
	return i
}

type corefileToken struct {
	text string
	// line is the 1-based line the token starts on; endLine is where it ends,
	// which differs only for quoted text that spans lines.
	line, endLine int
}

// corefileTokens splits a Corefile the way CoreDNS's own lexer does
// (github.com/coredns/caddy, caddyfile/lexer.go):
//   - whitespace separates tokens;
//   - a double quote at the start of a token opens quoted text, which may span
//     lines and ends the token at the closing quote; \" is a literal quote;
//   - "#" outside quotes starts a comment. At the start of a token it runs to
//     the end of the line; inside a token it runs to the next whitespace.
func corefileTokens(corefile string) []corefileToken {
	var (
		tokens                   []corefileToken
		val                      []rune
		line                     = 1
		start                    int
		comment, quoted, escaped bool
		started                  bool
	)
	emit := func() {
		tokens = append(tokens, corefileToken{text: string(val), line: start, endLine: line})
		val, started = val[:0], false
	}
	for _, ch := range corefile {
		if quoted {
			if !escaped {
				if ch == '\\' {
					escaped = true
					continue
				}
				if ch == '"' {
					quoted = false
					emit()
					continue
				}
			}
			if escaped && ch != '"' {
				val = append(val, '\\')
			}
			val = append(val, ch)
			escaped = false
			if ch == '\n' {
				line++
			}
			continue
		}
		if unicode.IsSpace(ch) {
			if ch == '\r' {
				continue
			}
			if started {
				// The lexer forgets a comment when it returns a token, so one
				// that began inside a token ends at the next whitespace.
				emit()
				comment = false
			}
			if ch == '\n' {
				line++
				comment = false
			}
			continue
		}
		if ch == '#' {
			comment = true
		}
		if comment {
			continue
		}
		if !started {
			started, start = true, line
			if ch == '"' {
				quoted = true
				continue
			}
		}
		val = append(val, ch)
	}
	if started {
		emit()
	}
	return tokens
}
