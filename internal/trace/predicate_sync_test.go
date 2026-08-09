package trace

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// diagnoseKindPredicatePath holds the TS mirror of trace.IsEntryKind. The
// Diagnose tab in the UI gates on isDiagnoseKind(); we mirror that allowlist
// in TS rather than fetch-to-decide. Mirror cost: this file fails CI if the
// two drift.
const diagnoseKindPredicatePath = "../../packages/k8s-ui/src/components/workload/WorkloadView.tsx"

// kindLiteralInPredicate extracts the quoted kind tokens out of the
// isDiagnoseKind body. The function body declares each accepted kind as
// `k === 'service' || k === 'services' || ...`, so a simple regex over
// single-quoted tokens between `isDiagnoseKind` and its closing brace gives
// the same set the runtime predicate consults.
var kindLiteralInPredicate = regexp.MustCompile(`'([a-zA-Z][a-zA-Z0-9_-]*)'`)

// TestIsEntryKindMatchesUIPredicate fails when the Go IsEntryKind and the TS
// isDiagnoseKind diverge on which kinds the Diagnose surface supports. A
// kind added in Go but not TS renders the API endpoint with no UI affordance;
// a kind added in TS but not Go shows the tab on a resource the server
// rejects with a 400.
func TestIsEntryKindMatchesUIPredicate(t *testing.T) {
	raw, err := os.ReadFile(diagnoseKindPredicatePath)
	if err != nil {
		t.Fatalf("read %s: %v", diagnoseKindPredicatePath, err)
	}
	body := extractIsDiagnoseKindBody(string(raw))
	if body == "" {
		t.Fatalf("isDiagnoseKind body not found in %s - did the function move or get renamed?", diagnoseKindPredicatePath)
	}
	tsKinds := map[string]bool{}
	for _, m := range kindLiteralInPredicate.FindAllStringSubmatch(body, -1) {
		tsKinds[m[1]] = true
	}
	if len(tsKinds) == 0 {
		t.Fatalf("no kind literals parsed from isDiagnoseKind body - predicate format may have changed")
	}

	// The Go side accepts singular, capitalized canonical, and plural forms.
	// Build the expected set from the canonical predicate so the test
	// describes intent, not implementation: every form Go accepts must also
	// be accepted by TS, and vice versa.
	goKinds := map[string]bool{}
	for _, form := range []string{
		"service", "services",
		"ingress", "ingresses",
		"httproute", "httproutes",
		"grpcroute", "grpcroutes",
		"gateway", "gateways",
	} {
		if !IsEntryKind(form) {
			t.Fatalf("test setup bug: Go IsEntryKind(%q) should be true", form)
		}
		goKinds[form] = true
	}

	var missingFromTS, extraInTS []string
	for k := range goKinds {
		if !tsKinds[k] {
			missingFromTS = append(missingFromTS, k)
		}
	}
	for k := range tsKinds {
		if !goKinds[k] {
			extraInTS = append(extraInTS, k)
		}
	}
	sort.Strings(missingFromTS)
	sort.Strings(extraInTS)
	if len(missingFromTS) > 0 || len(extraInTS) > 0 {
		t.Fatalf(
			"isDiagnoseKind (TS) and IsEntryKind (Go) drift:\n  missing from TS: %v\n  extra in TS:     %v\nUpdate %s OR internal/trace/trace.go so both predicates accept the same kind set.",
			missingFromTS, extraInTS, diagnoseKindPredicatePath,
		)
	}
}

// extractIsDiagnoseKindBody slices the TS function body between the opening
// `export function isDiagnoseKind` and the next standalone `}` at the
// matching brace depth. Simple-minded brace counting is enough - the body
// is straightforward boolean logic with no nested functions.
func extractIsDiagnoseKindBody(src string) string {
	const marker = "export function isDiagnoseKind"
	start := strings.Index(src, marker)
	if start == -1 {
		return ""
	}
	open := strings.Index(src[start:], "{")
	if open == -1 {
		return ""
	}
	open += start
	depth := 0
	for i := open; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[open : i+1]
			}
		}
	}
	return ""
}

// missingRefPrefixTSPath holds the TS module that mirrors missingRefCodePrefix
// to exclude a sibling route's broken backend from an upstream entry's findings
// (a sibling break must not paint a healthy subject's verdict red).
const missingRefPrefixTSPath = "../../packages/k8s-ui/src/components/trace/reachVerdict.ts"

// TestMissingRefPrefixMirrored fails when the UI's missing-ref prefix drifts
// from the Go missingRefCodePrefix. Both partition an upstream entry's findings
// into "reaches this subject" vs "sibling route"; a mismatch would let the UI
// fold a sibling break into the subject verdict (or vice versa) while the Go
// verdict excludes it.
func TestMissingRefPrefixMirrored(t *testing.T) {
	raw, err := os.ReadFile(missingRefPrefixTSPath)
	if err != nil {
		t.Fatalf("read %s: %v", missingRefPrefixTSPath, err)
	}
	want := "startsWith('" + missingRefCodePrefix + "')"
	if !strings.Contains(string(raw), want) {
		t.Fatalf("TS missing-ref prefix out of sync with Go missingRefCodePrefix (%q).\nExpected %s to contain:\n  %s\nUpdate the TS literal OR internal/trace/trace.go so both use the same code prefix.",
			missingRefCodePrefix, missingRefPrefixTSPath, want)
	}
}
