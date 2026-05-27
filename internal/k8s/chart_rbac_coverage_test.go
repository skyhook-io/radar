package k8s

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/pkg/topology"
)

// TestChartGrantsEveryWatchedCRDGroup guards against the drift where Radar
// watches an API group the Helm chart never grants. That failure is silent: the
// informer's list call 403s, the reflector backs off, and the resource simply
// never appears in the UI — no error, no log most operators would notice. The
// drift recurs because the (group, resource, kind) tuples live in several
// independent places that must agree and nothing else cross-checks them.
//
// Scope: GROUP-level coverage for the CRD / extension watch lists
// (supportedCRDFallbacks + topology.ClusterScopedKinds), which is exactly where
// the drift happens. It deliberately does NOT verify resource-level grants
// inside core/typed groups (e.g. ingressclasses under networking.k8s.io) —
// those rules enumerate resources explicitly on a small, stable surface.
//
// Opt-in wildcard rules (rbac.helm, rbac.crdGroups.all) are ignored: they're
// off by default, so the guard insists every watched group has its own explicit
// per-group rule rather than leaning on a broad wildcard a deployment may not enable.
func TestChartGrantsEveryWatchedCRDGroup(t *testing.T) {
	granted := chartGrantedAPIGroups(t)

	// group -> a human-readable pointer to where Radar watches it, for the failure message.
	watched := map[string]string{}
	for _, c := range supportedCRDFallbacks {
		if c.Group != "" {
			watched[c.Group] = "supportedCRDFallbacks (internal/k8s/dynamic_cache.go)"
		}
	}
	for _, e := range topology.ClusterScopedKinds {
		if e.Group != "" {
			watched[e.Group] = "topology.ClusterScopedKinds (pkg/topology/cluster_scoped_kinds.go)"
		}
	}

	for group, source := range watched {
		if !granted[group] {
			t.Errorf("API group %q is watched by Radar (%s) but no rule in "+
				"deploy/helm/radar/templates/clusterrole.yaml grants it. The informer "+
				"will 403 and the resource will silently never appear. Add a per-group "+
				"rule (and values.yaml toggle) granting get/list/watch on %q.",
				group, source, group)
		}
	}
}

// chartGrantedAPIGroups extracts the set of API groups the ClusterRole template
// can grant. It walks the raw template rather than rendering it (the file is Helm,
// not valid YAML), skipping comments and {{ }} directives so groups named in prose
// don't count, and reading both inline (apiGroups: ["a","b"]) and block-style
// (apiGroups:\n  - "a") rules. Conditional per-group rules are counted as granted
// because their toggles default to true; the wildcard "*" is excluded on purpose.
func chartGrantedAPIGroups(t *testing.T) map[string]bool {
	t.Helper()
	path := filepath.Join("..", "..", "deploy", "helm", "radar", "templates", "clusterrole.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read chart ClusterRole template: %v", err)
	}

	quoted := regexp.MustCompile(`"([^"]*)"`)
	add := func(set map[string]bool, line string) {
		for _, m := range quoted.FindAllStringSubmatch(line, -1) {
			if m[1] != "*" {
				set[m[1]] = true
			}
		}
	}

	groups := map[string]bool{}
	inAPIGroups := false
	for line := range strings.SplitSeq(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "{{") {
			continue // preserve inAPIGroups across comments / template directives
		}
		switch {
		case strings.Contains(trimmed, "apiGroups:"):
			add(groups, trimmed)
			// Inline arrays close on the same line; block style spills onto "- " items.
			inAPIGroups = !strings.Contains(trimmed, "]")
		case inAPIGroups && strings.HasPrefix(trimmed, "- "):
			add(groups, trimmed)
		default:
			inAPIGroups = false
		}
	}
	return groups
}
