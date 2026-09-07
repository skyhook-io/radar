package prometheus

import (
	"strconv"
	"strings"
)

// Matcher is one label matcher inside a vector selector's braces.
type Matcher struct {
	Label string `json:"label"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

// Selector is one vector selector a PromQL query reads. Metric is empty for a
// bare matcher block such as {__name__=~"node_.*"}; Matchers is empty for a
// bare metric name.
type Selector struct {
	Metric   string    `json:"metric"`
	Matchers []Matcher `json:"matchers"`
}

// ExtractSelectors lists the vector selectors a PromQL query reads, so a
// consumer can decide whether every series the query touches is scoped to a
// resource (the frontend's target-versus-broader rule for metric evidence).
//
// It is a tokenizer, not a parser, and fails closed: string literals are
// skipped, every identifier not followed by "(" and not a PromQL keyword,
// aggregator, number or duration is a selector, and anything it cannot
// account for (unbalanced braces or quotes, an unexpected character, a
// matcher that is not label-op-string, a malformed number or range) returns
// unknown=true with no selectors. A consumer must treat unknown as "could be
// anything".
func ExtractSelectors(query string) (selectors []Selector, unknown bool) {
	s := selectorScanner{src: query, sels: []Selector{}}
	if !s.scan() {
		return []Selector{}, true
	}
	return s.sels, false
}

// promModifierKeywords never name a metric: PromQL's grammar does not admit
// them as metric identifiers. inf/nan are number literals.
var promModifierKeywords = map[string]bool{
	"on": true, "ignoring": true, "group_left": true, "group_right": true,
	"bool": true, "inf": true, "nan": true,
}

// promLabelListKeywords take an optional parenthesised label list whose
// entries are label names, not selectors.
var promLabelListKeywords = map[string]bool{
	"by": true, "without": true, "on": true, "ignoring": true,
	"group_left": true, "group_right": true,
}

// promBinaryKeywords are binary operators after an operand and metric names
// otherwise: the grammar admits `and`, `or` and `unless` as bare metric
// identifiers, so `foo / or` reads the metric named "or".
var promBinaryKeywords = map[string]bool{
	"and": true, "or": true, "unless": true, "atan2": true,
}

// promAggregators are aggregation operators when followed by "(" or a
// by/without clause, and metric names otherwise (the grammar admits them too).
var promAggregators = map[string]bool{
	"sum": true, "avg": true, "min": true, "max": true, "group": true,
	"stddev": true, "stdvar": true, "count": true, "count_values": true,
	"bottomk": true, "topk": true, "quantile": true,
	"limitk": true, "limit_ratio": true,
}

// prevToken is what the scanner last consumed, enough to tell whether the
// next identifier can be a binary operator or clause keyword (after an
// operand or an aggregator) or must be a metric name.
type prevToken int

const (
	prevNone prevToken = iota
	prevOperand
	prevAggregator
)

type selectorScanner struct {
	src  string
	pos  int
	sels []Selector
	prev prevToken
}

func isPromSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
func isPromDigit(c byte) bool { return c >= '0' && c <= '9' }
func isPromLetter(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
func isPromMetricStart(c byte) bool { return isPromLetter(c) || c == ':' }
func isPromMetricChar(c byte) bool  { return isPromMetricStart(c) || isPromDigit(c) }
func isPromLabelChar(c byte) bool   { return isPromLetter(c) || isPromDigit(c) }

func (s *selectorScanner) eof() bool { return s.pos >= len(s.src) }

func (s *selectorScanner) skipSpace() {
	for !s.eof() && isPromSpace(s.src[s.pos]) {
		s.pos++
	}
}

func (s *selectorScanner) peekNonSpace() byte {
	i := s.pos
	for i < len(s.src) && isPromSpace(s.src[i]) {
		i++
	}
	if i >= len(s.src) {
		return 0
	}
	return s.src[i]
}

func (s *selectorScanner) peekWord() string {
	i := s.pos
	for i < len(s.src) && isPromSpace(s.src[i]) {
		i++
	}
	start := i
	for i < len(s.src) && isPromMetricChar(s.src[i]) {
		i++
	}
	return s.src[start:i]
}

func (s *selectorScanner) scan() bool {
	parenDepth := 0
	for !s.eof() {
		c := s.src[s.pos]
		switch {
		case isPromSpace(c):
			s.pos++
		case c == '#':
			for !s.eof() && s.src[s.pos] != '\n' && s.src[s.pos] != '\r' {
				s.pos++
			}
		case c == '"' || c == '\'' || c == '`':
			if _, ok := s.stringLiteral(); !ok {
				return false
			}
			s.prev = prevOperand
		case c == '{':
			m, ok := s.matchers()
			if !ok {
				return false
			}
			s.sels = append(s.sels, Selector{Metric: "", Matchers: m})
			s.prev = prevOperand
		case c == '[':
			if !s.rangeBracket() {
				return false
			}
			s.prev = prevOperand
		case c == '(':
			parenDepth++
			s.pos++
			s.prev = prevNone
		case c == ')':
			parenDepth--
			if parenDepth < 0 {
				return false
			}
			s.pos++
			s.prev = prevOperand
		case isPromDigit(c) || (c == '.' && s.pos+1 < len(s.src) && isPromDigit(s.src[s.pos+1])):
			end, ok := scanPromNumber(s.src, s.pos)
			if !ok {
				return false
			}
			s.pos = end
			s.prev = prevOperand
		case isPromMetricStart(c):
			if !s.identifier() {
				return false
			}
		case strings.IndexByte("+-*/%^=!<>,@", c) >= 0:
			s.pos++
			s.prev = prevNone
		default:
			return false
		}
	}
	return parenDepth == 0
}

func (s *selectorScanner) identifier() bool {
	start := s.pos
	for !s.eof() && isPromMetricChar(s.src[s.pos]) {
		s.pos++
	}
	word := s.src[start:s.pos]
	next := s.peekNonSpace()
	prev := s.prev
	s.prev = prevNone

	if next == '{' {
		s.skipSpace()
		m, ok := s.matchers()
		if !ok {
			return false
		}
		s.sels = append(s.sels, Selector{Metric: word, Matchers: m})
		s.prev = prevOperand
		return true
	}

	lower := strings.ToLower(word)
	switch {
	case promModifierKeywords[lower]:
		if promLabelListKeywords[lower] && next == '(' {
			s.skipSpace()
			return s.labelList()
		}
		return true
	case (lower == "by" || lower == "without") && (prev == prevOperand || prev == prevAggregator):
		// A postfix clause (`sum(x) by (a)`) completes the operand it follows;
		// a prefix clause keeps waiting for the aggregator's argument.
		s.prev = prev
		if next == '(' {
			s.skipSpace()
			return s.labelList()
		}
		return true
	case lower == "offset" && prev == prevOperand:
		return true
	case promBinaryKeywords[lower] && prev == prevOperand:
		return true
	case promAggregators[lower]:
		if next == '(' {
			s.prev = prevAggregator
			return true
		}
		if following := strings.ToLower(s.peekWord()); following == "by" || following == "without" {
			s.prev = prevAggregator
			return true
		}
	case next == '(':
		return true
	}

	s.sels = append(s.sels, Selector{Metric: word, Matchers: []Matcher{}})
	s.prev = prevOperand
	return true
}

// labelList consumes the "(a, b)" after by/without/on/ignoring/group_*.
// Entries are label names (identifiers or, for UTF-8 names, strings).
func (s *selectorScanner) labelList() bool {
	s.pos++ // (
	for {
		s.skipSpace()
		if s.eof() {
			return false
		}
		switch c := s.src[s.pos]; {
		case c == ')':
			s.pos++
			return true
		case isPromLetter(c):
			for !s.eof() && isPromLabelChar(s.src[s.pos]) {
				s.pos++
			}
		case c == '"' || c == '\'' || c == '`':
			if _, ok := s.stringLiteral(); !ok {
				return false
			}
		default:
			return false
		}
		s.skipSpace()
		if s.eof() {
			return false
		}
		switch s.src[s.pos] {
		case ',':
			s.pos++
		case ')':
			s.pos++
			return true
		default:
			return false
		}
	}
}

// matchers consumes a "{label op "value", ...}" block. A block that is not a
// plain list of label-op-string entries (a quoted metric name, a bare value)
// is unknown territory.
func (s *selectorScanner) matchers() ([]Matcher, bool) {
	s.pos++ // {
	out := []Matcher{}
	for {
		s.skipSpace()
		if s.eof() {
			return nil, false
		}
		if s.src[s.pos] == '}' {
			s.pos++
			return out, true
		}
		if !isPromLetter(s.src[s.pos]) {
			return nil, false
		}
		start := s.pos
		for !s.eof() && isPromLabelChar(s.src[s.pos]) {
			s.pos++
		}
		label := s.src[start:s.pos]

		s.skipSpace()
		op := s.matchOp()
		if op == "" {
			return nil, false
		}

		s.skipSpace()
		if s.eof() {
			return nil, false
		}
		if q := s.src[s.pos]; q != '"' && q != '\'' && q != '`' {
			return nil, false
		}
		value, ok := s.stringLiteral()
		if !ok {
			return nil, false
		}
		out = append(out, Matcher{Label: label, Op: op, Value: value})

		s.skipSpace()
		if s.eof() {
			return nil, false
		}
		switch s.src[s.pos] {
		case ',':
			s.pos++
		case '}':
			s.pos++
			return out, true
		default:
			return nil, false
		}
	}
}

func (s *selectorScanner) matchOp() string {
	for _, op := range []string{"=~", "!~", "!=", "="} {
		if strings.HasPrefix(s.src[s.pos:], op) {
			s.pos += len(op)
			return op
		}
	}
	return ""
}

// rangeBracket consumes "[5m]", "[1h:1m]", "[1h:]". The contents are
// durations, never selectors; anything else (a templating variable, a
// duration expression, an identifier) is unknown territory.
func (s *selectorScanner) rangeBracket() bool {
	end := strings.IndexByte(s.src[s.pos:], ']')
	if end < 0 {
		return false
	}
	body := s.src[s.pos+1 : s.pos+end]
	parts := strings.Split(body, ":")
	if len(parts) > 2 {
		return false
	}
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			if i == 0 {
				return false
			}
			continue
		}
		if n, ok := scanPromNumber(part, 0); !ok || n != len(part) {
			return false
		}
	}
	s.pos += end + 1
	return true
}

// scanPromNumber consumes a number or duration literal starting at pos (1,
// 1_000, 1.5, .5, 1e-3, 0x1f, 5m, 1h30m) and returns where it ends. A literal
// that runs straight into identifier characters ("123garbage", "5mfoo") is
// not a literal at all.
func scanPromNumber(src string, pos int) (int, bool) {
	i := pos
	digits := func() int {
		start := i
		for i < len(src) && (isPromDigit(src[i]) || src[i] == '_') {
			i++
		}
		return i - start
	}
	hexDigits := func() int {
		start := i
		for i < len(src) && (isPromDigit(src[i]) || src[i] == '_' || (src[i] >= 'a' && src[i] <= 'f') || (src[i] >= 'A' && src[i] <= 'F')) {
			i++
		}
		return i - start
	}

	if strings.HasPrefix(src[i:], "0x") || strings.HasPrefix(src[i:], "0X") {
		i += 2
		if hexDigits() == 0 {
			return i, false
		}
	} else {
		whole := digits()
		frac := 0
		if i < len(src) && src[i] == '.' {
			i++
			frac = digits()
		}
		if whole == 0 && frac == 0 {
			return i, false
		}
		switch {
		case i < len(src) && (src[i] == 'e' || src[i] == 'E'):
			i++
			if i < len(src) && (src[i] == '+' || src[i] == '-') {
				i++
			}
			if digits() == 0 {
				return i, false
			}
		case i < len(src) && isPromLetter(src[i]):
			for {
				if !scanDurationUnit(src, &i) {
					return i, false
				}
				if i < len(src) && isPromDigit(src[i]) {
					digits()
					continue
				}
				break
			}
		}
	}
	if i < len(src) && isPromMetricChar(src[i]) {
		return i, false
	}
	return i, true
}

func scanDurationUnit(src string, i *int) bool {
	for _, unit := range []string{"ms", "s", "m", "h", "d", "w", "y"} {
		if strings.HasPrefix(src[*i:], unit) {
			*i += len(unit)
			return true
		}
	}
	return false
}

// stringLiteral consumes a double-, single- or backtick-quoted string at pos
// and returns its unescaped value.
func (s *selectorScanner) stringLiteral() (string, bool) {
	q := s.src[s.pos]
	start := s.pos + 1
	if q == '`' {
		end := strings.IndexByte(s.src[start:], '`')
		if end < 0 {
			return "", false
		}
		s.pos = start + end + 1
		return s.src[start : start+end], true
	}
	i := start
	for i < len(s.src) {
		c := s.src[i]
		if c == '\\' {
			i += 2
			continue
		}
		if c == '\n' {
			return "", false
		}
		if c == q {
			break
		}
		i++
	}
	if i >= len(s.src) {
		return "", false
	}
	raw := s.src[start:i]
	s.pos = i + 1
	return unquotePromString(q, raw)
}

// unquotePromString applies PromQL's Go-style escapes. A single-quoted string
// is rewritten into its double-quoted equivalent so strconv.Unquote can decode
// both forms.
func unquotePromString(q byte, raw string) (string, bool) {
	if q == '\'' {
		var b strings.Builder
		for i := 0; i < len(raw); i++ {
			switch c := raw[i]; {
			case c == '\\' && i+1 < len(raw):
				i++
				if raw[i] == '\'' {
					b.WriteByte('\'')
				} else {
					b.WriteByte('\\')
					b.WriteByte(raw[i])
				}
			case c == '"':
				b.WriteString(`\"`)
			default:
				b.WriteByte(c)
			}
		}
		raw = b.String()
	}
	v, err := strconv.Unquote(`"` + raw + `"`)
	if err != nil {
		return "", false
	}
	return v, true
}
