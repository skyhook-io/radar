package server

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Host renderers live in the app, and the rule they have to keep is one the app
// cannot check for itself: web's tsconfig type-checks its own tests and carries
// no Node types, so a test that reads files off disk does not compile there, and
// the shared package must not reach up into the app to inspect it. Go already
// reads frontend source for exactly this reason — see TestSetupDialogCoversAllTools
// on the MCP tool catalogue — so the guard lives on this side of the line.
const hostRenderersDir = "../../web/src/components/resources/renderers"

const lookupHooks = `use(?:Resources|RBACSubject|RBACRole|RBACNamespace|PolicyCoverage|PolicyQueued|CNPGCatalogUsers)`

// `const { data, error } = useResources(...)` — bindings captured for inspection.
var lookupDestructure = regexp.MustCompile(`const\s*\{([^}]*)\}\s*=\s*` + lookupHooks + `\b`)

// `const clusters = useResources(...)` — the query object is kept whole, which
// is the dominant style in these files. Checking only the destructured form
// would leave most call sites unguarded while appearing to cover them, so this
// captures the identifier and the check below looks for `<identifier>.error`.
var lookupAssignment = regexp.MustCompile(`const\s+([A-Za-z_$][\w$]*)\s*=\s*` + lookupHooks + `\b`)

// Ratchet: the individual lookups that still discard their error, keyed by file
// and by the binding itself. Per call site rather than per file on purpose — a
// filename entry would forgive every future lookup added to that file, which is
// the file most likely to grow one.
//
// This set may SHRINK. It must never GROW.
var lookupErrorBaseline = map[string]string{
	"CNPGDeclarativeRenderer.tsx :: data": "the name resolver degrades to plain text instead of a link, " +
		"which is its documented fallback; the list beside it carries its own error note",
}

// commentPattern matches the comment forms these files use. A `.error` written
// in prose — including the comments this guard's own fixes leave behind —
// satisfied the check without a line of code reading the error.
var commentPattern = regexp.MustCompile(`(?s)/\*.*?\*/|//[^\n]*`)

// stripComments removes them so only real uses count.
func stripComments(s string) string {
	return commentPattern.ReplaceAllString(s, " ")
}

// normalizeBinding collapses whitespace so the baseline key is stable across
// formatting, while still naming the exact binding it forgives.
func normalizeBinding(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// TestReverseLookupsSurfaceTheirFailures keeps a failed or forbidden lookup from
// rendering as an established absence.
//
// A renderer that takes only `data` from its query throws the error away, and
// from that point an empty list, a denied list and a broken fetch all draw the
// same screen: "nothing here". That is a claim the page never earned, and it is
// invisible in review because the happy path looks perfect — which is why this
// is mechanical rather than a convention.
//
// When it fails because you added one: keep `error` from the hook and render
// <LookupFailureNote errors={[...]} what="..." />, passing `incomplete` when rows
// are already on screen. Hide an expected 403 only where the section is a bonus
// on someone else's page, as RBACErrorSection's callers do.
func TestReverseLookupsSurfaceTheirFailures(t *testing.T) {
	entries, err := os.ReadDir(hostRenderersDir)
	if err != nil {
		t.Fatalf("read host renderers: %v", err)
	}

	var offenders []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".tsx") || strings.HasSuffix(name, ".test.tsx") {
			continue
		}
		src, readErr := os.ReadFile(filepath.Join(hostRenderersDir, name))
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		text := stripComments(string(src))
		for _, m := range lookupDestructure.FindAllStringSubmatch(text, -1) {
			if !strings.Contains(m[1], "error") {
				offenders = append(offenders, name+" :: "+normalizeBinding(m[1]))
			}
		}
		for _, m := range lookupAssignment.FindAllStringSubmatch(text, -1) {
			// The query object is in hand, so the error is reachable; what
			// matters is whether anything ever reads it.
			if !strings.Contains(text, m[1]+".error") {
				offenders = append(offenders, name+" :: "+normalizeBinding(m[1]))
			}
		}
	}
	sort.Strings(offenders)

	var added []string
	found := map[string]bool{}
	for _, o := range offenders {
		found[o] = true
		if _, allowed := lookupErrorBaseline[o]; !allowed {
			added = append(added, o)
		}
	}
	if len(added) > 0 {
		t.Errorf("these keep only `data` from a lookup, so a failed or denied fetch renders as "+
			"\"nothing here\": %s", strings.Join(added, ", "))
	}

	// Without this the baseline quietly turns into a permanent allowance.
	var stale []string
	for name := range lookupErrorBaseline {
		if !found[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("fixed — remove from lookupErrorBaseline: %s", strings.Join(stale, ", "))
	}
}
