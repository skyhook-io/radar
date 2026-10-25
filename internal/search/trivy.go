package search

import (
	"context"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
)

// trivyGroup is the API group Trivy Operator writes its reports in.
const trivyGroup = aicontext.TrivyGroup

// maxTrivyFieldBytes bounds one indexed report entry. IDs and versions are
// short; the cap only guards against a malformed report.
const maxTrivyFieldBytes = 256

// trivyFinding is one searchable report entry. Only identifiers are
// searchable: CVE IDs with package and versions, SBOM component names and
// versions, failed check IDs and secret rule IDs. Severity words, titles and
// descriptions are left out because generic tokens ("high", "memory", "root")
// would otherwise match hundreds of reports and drown ordinary searches;
// severity stays reachable through the `report` CEL binding. A secret
// finding's `match` holds the matched line and is never searchable. Entries
// are not capped: a capped report would silently miss the CVE a caller is
// looking for.
type trivyFinding struct {
	prefix string
	index  int
	parts  [5]string
	n      int
}

func (f *trivyFinding) set(prefix string, index int, parts ...string) {
	f.prefix, f.index, f.n = prefix, index, 0
	for _, p := range parts {
		if p != "" {
			f.parts[f.n] = p
			f.n++
		}
	}
}

func (f *trivyFinding) path() string {
	return f.prefix + "[" + strconv.Itoa(f.index) + "]"
}

func (f *trivyFinding) value() string {
	return joinField(f.parts[:f.n]...)
}

// match scores a lowercase token against the finding the way a content field
// is scored, without building the joined value: a token without spaces can
// only fall inside one part.
func (f *trivyFinding) match(low string) int {
	if f.n == 0 {
		return 0
	}
	if strings.ContainsRune(low, ' ') {
		v := strings.ToLower(f.value())
		switch {
		case v == low:
			return scoreContentExact
		case strings.Contains(v, low):
			return scoreContentSubstr
		}
		return 0
	}
	if f.n == 1 && strings.EqualFold(f.parts[0], low) {
		return scoreContentExact
	}
	for _, p := range f.parts[:f.n] {
		if containsFoldASCII(p, low) {
			return scoreContentSubstr
		}
	}
	return 0
}

// eachTrivyFinding calls fn for every searchable entry of a report until fn
// returns false. It reuses one trivyFinding, so a walk allocates nothing per
// entry: search walks every report on every free-text query. Every
// findingsCheckEvery entries, searchable or not, it stops if findings says
// the request ended or the budget is spent (nil findings: never).
func eachTrivyFinding(report map[string]any, findings *findingsScan, fn func(*trivyFinding) bool) {
	if report == nil {
		return
	}
	var f trivyFinding
	walked := 0
	keepGoing := func() bool {
		walked++
		return walked%findingsCheckEvery != 0 || findings.within()
	}
	for i, item := range asSlice(report["vulnerabilities"]) {
		if !keepGoing() {
			return
		}
		v := asMap(item)
		id := asString(v["vulnerabilityID"])
		if id == "" {
			continue
		}
		fixed := asString(v["fixedVersion"])
		if fixed == "" {
			fixed = "unfixed"
		}
		f.set("report.vulnerabilities", i, id, asString(v["resource"]), asString(v["installedVersion"]), "→", fixed)
		if !fn(&f) {
			return
		}
	}
	for i, item := range asSlice(asMap(report["components"])["components"]) {
		if !keepGoing() {
			return
		}
		c := asMap(item)
		f.set("report.components.components", i, asString(c["name"]), asString(c["version"]))
		if f.n > 0 && !fn(&f) {
			return
		}
	}
	for i, item := range asSlice(report["checks"]) {
		if !keepGoing() {
			return
		}
		c := asMap(item)
		if success, ok := c["success"].(bool); !ok || success {
			continue
		}
		f.set("report.checks", i, asString(c["checkID"]))
		if f.n > 0 && !fn(&f) {
			return
		}
	}
	for i, item := range asSlice(report["secrets"]) {
		if !keepGoing() {
			return
		}
		s := asMap(item)
		f.set("report.secrets", i, asString(s["ruleID"]), asString(s["target"]))
		if f.n > 0 && !fn(&f) {
			return
		}
	}
}

// trivyReportContent lists a report's searchable entries as content fields.
func trivyReportContent(report map[string]any) []ContentField {
	var out []ContentField
	eachTrivyFinding(report, nil, func(f *trivyFinding) bool {
		out = append(out, ContentField{Path: f.path(), Value: f.value()})
		return true
	})
	return out
}

// containsFoldASCII reports whether s contains the lowercase substring low,
// ignoring ASCII case, without allocating.
func containsFoldASCII(s, low string) bool {
	n := len(low)
	if n == 0 {
		return true
	}
	for i := 0; i+n <= len(s); i++ {
		j := 0
		for ; j < n; j++ {
			c := s[i+j]
			if 'A' <= c && c <= 'Z' {
				c += 'a' - 'A'
			}
			if c != low[j] {
				break
			}
		}
		if j == n {
			return true
		}
	}
	return false
}

// joinField joins the non-empty parts with spaces into one indexed value,
// capped at maxTrivyFieldBytes on a rune boundary. Search walks reports on
// every query, so this allocates once per value.
func joinField(parts ...string) string {
	n := 0
	for _, p := range parts {
		if p != "" {
			n += len(p) + 1
		}
	}
	if n == 0 {
		return ""
	}
	var b strings.Builder
	b.Grow(n)
	for _, p := range parts {
		if p == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(p)
	}
	v := b.String()
	if len(v) > maxTrivyFieldBytes {
		cut := maxTrivyFieldBytes
		for cut > 0 && !utf8.RuneStart(v[cut]) {
			cut--
		}
		v = v[:cut]
	}
	return v
}

// trivyReportProjection is what the `report` CEL binding exposes: the fields
// findings are identified and ranked by, in the report's own shape so an
// expression written against the object (report.components.components, …)
// works unchanged. It is a copy, never the cached object, and it leaves out
// every free-text field, in particular a secret finding's `match`, so a
// filter can't be used to probe secret values one predicate at a time.
func trivyReportProjection(report map[string]any, findings *findingsScan) (map[string]any, bool) {
	out := map[string]any{}
	if report == nil {
		return out, true
	}
	if ts := asString(report["updateTimestamp"]); ts != "" {
		out["updateTimestamp"] = ts
	}
	if v := pick(asMap(report["artifact"]), "repository", "tag", "digest"); len(v) > 0 {
		out["artifact"] = v
	}
	if v := pick(asMap(report["registry"]), "server"); len(v) > 0 {
		out["registry"] = v
	}
	if v := pick(asMap(report["os"]), "family", "name"); len(v) > 0 {
		out["os"] = v
	}
	if v := pick(asMap(report["scanner"]), "name", "version"); len(v) > 0 {
		out["scanner"] = v
	}
	if summary := asMap(report["summary"]); len(summary) > 0 {
		counts := make(map[string]any, len(summary))
		for k, v := range summary {
			switch v.(type) {
			case int64, float64, int:
				counts[k] = v
			}
		}
		out["summary"] = counts
	}
	if v := pickEach(report["vulnerabilities"], findings, "vulnerabilityID", "resource", "installedVersion", "fixedVersion", "severity", "score"); v != nil {
		out["vulnerabilities"] = v
	}
	if v := pickEach(asMap(report["components"])["components"], findings, "name", "version", "purl", "type"); v != nil {
		out["components"] = map[string]any{"components": v}
	}
	if v := pickEach(report["checks"], findings, "checkID", "severity", "success", "category"); v != nil {
		out["checks"] = v
	}
	if v := pickEach(report["secrets"], findings, "ruleID", "category", "severity", "target"); v != nil {
		out["secrets"] = v
	}
	if !findings.within() {
		return nil, false
	}
	return out, true
}

func pick(m map[string]any, keys ...string) map[string]any {
	out := map[string]any{}
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch v.(type) {
			case string, bool, int64, float64, int:
				out[k] = v
			}
		}
	}
	return out
}

// pickEach projects each entry, stopping early (nil) once the findings
// limit is reached; the caller then checks findings.within.
func pickEach(v any, findings *findingsScan, keys ...string) []any {
	items := asSlice(v)
	if items == nil {
		return nil
	}
	out := make([]any, 0, len(items))
	for i, item := range items {
		if i%findingsCheckEvery == 0 && !findings.within() {
			return nil
		}
		out = append(out, pick(asMap(item), keys...))
	}
	return out
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

// Searching findings walks every entry of every Trivy report, and the
// omnibar searches on every pause in typing. One search may spend at most
// findingsBudget on it, counted from its first report; past that it still
// returns everything else and reports findings as incomplete. A cancelled
// request stops at the next check.
var findingsBudget = time.Second

// findingsCheckEvery is how many entries a walk covers between checks of
// cancellation and the time budget.
const findingsCheckEvery = 256

// findingsScan is one search's findings budget. A nil scan is unlimited, for
// callers outside a search.
type findingsScan struct {
	ctx      context.Context
	deadline time.Time
	spent    bool
}

func newFindingsScan(ctx context.Context) *findingsScan {
	return &findingsScan{ctx: ctx}
}

// allow reports whether the next report's findings may be searched, starting
// the budget on first use.
func (f *findingsScan) allow() bool {
	if f != nil && f.deadline.IsZero() {
		f.deadline = time.Now().Add(findingsBudget)
	}
	return f.within()
}

// within reports whether a walk may continue: the request is still live and
// the budget isn't spent.
func (f *findingsScan) within() bool {
	if f == nil {
		return true
	}
	if f.spent {
		return false
	}
	if f.ctx.Err() != nil || (!f.deadline.IsZero() && time.Now().After(f.deadline)) {
		f.spent = true
		return false
	}
	return true
}

// exhausted reports whether the budget ran out, so a report searched since
// may have been searched only in part.
func (f *findingsScan) exhausted() bool {
	return f != nil && f.spent
}
