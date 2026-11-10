package k8s

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/pkg/topology"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	"helm.sh/helm/v3/pkg/strvals"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

func renderCollectorClusterRole(t *testing.T, overrides string) rbacv1.ClusterRole {
	t.Helper()
	chartPath := filepath.Join("..", "..", "deploy", "helm", "radar")
	chart, err := loader.Load(chartPath)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]interface{}{}
	if overrides != "" {
		if err := strvals.ParseInto(overrides, values); err != nil {
			t.Fatal(err)
		}
	}
	renderValues, err := chartutil.ToRenderValues(
		chart,
		values,
		chartutil.ReleaseOptions{Name: "radar", Namespace: "radar", IsUpgrade: true},
		chartutil.DefaultCapabilities,
	)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := engine.Render(chart, renderValues)
	if err != nil {
		t.Fatal(err)
	}
	var role rbacv1.ClusterRole
	if err := yaml.Unmarshal([]byte(rendered["radar/templates/clusterrole.yaml"]), &role); err != nil {
		t.Fatal(err)
	}
	return role
}

func admissionPolicyRule(role rbacv1.ClusterRole) *rbacv1.PolicyRule {
	for i := range role.Rules {
		rule := &role.Rules[i]
		if slices.Contains(rule.Resources, "validatingadmissionpolicies") ||
			slices.Contains(rule.Resources, "validatingadmissionpolicybindings") {
			return rule
		}
	}
	return nil
}

func TestAdmissionPolicyRBACGate(t *testing.T) {
	if rule := admissionPolicyRule(renderCollectorClusterRole(t, "")); rule != nil {
		t.Fatalf("default no-auth chart unexpectedly grants admission policy reads: %+v", *rule)
	}
	if rule := admissionPolicyRule(renderCollectorClusterRole(t, "rbac.viewWebhooks=true")); rule != nil {
		t.Fatalf("viewWebhooks must not grant admission policy reads: %+v", *rule)
	}
	for _, overrides := range []string{"rbac.viewAdmissionPolicies=true", "auth.mode=proxy", "cloud.enabled=true,cloud.token=test-token,cloud.url=wss://radar.example,cloud.clusterName=test-cluster"} {
		role := renderCollectorClusterRole(t, overrides)
		rule := admissionPolicyRule(role)
		if rule == nil {
			t.Fatalf("%s: admission policy rule is missing", overrides)
		}
		if !slices.Equal(rule.APIGroups, []string{"admissionregistration.k8s.io"}) ||
			!slices.Equal(rule.Resources, []string{"validatingadmissionpolicies", "validatingadmissionpolicybindings"}) ||
			!slices.Equal(rule.Verbs, []string{"get", "list", "watch"}) {
			t.Errorf("%s: admission policy rule = %+v", overrides, *rule)
		}
	}
}

func TestAdmissionPolicyRBACValueAndSchema(t *testing.T) {
	chartPath := filepath.Join("..", "..", "deploy", "helm", "radar")
	chart, err := loader.Load(chartPath)
	if err != nil {
		t.Fatal(err)
	}
	rbac := chart.Values["rbac"].(map[string]interface{})
	if value, exists := rbac["viewAdmissionPolicies"]; !exists || value != false {
		t.Fatalf("rbac.viewAdmissionPolicies default = %v, exists = %v; want false", value, exists)
	}

	raw, err := os.ReadFile(filepath.Join(chartPath, "values.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	properties := schema["properties"].(map[string]any)["rbac"].(map[string]any)["properties"].(map[string]any)
	entry, exists := properties["viewAdmissionPolicies"].(map[string]any)
	if !exists || entry["type"] != "boolean" {
		t.Fatalf("rbac.viewAdmissionPolicies schema = %#v", properties["viewAdmissionPolicies"])
	}
}

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
	// The NetworkPolicy deny-all check treats an unreadable policy API as
	// "may allow" and goes silent, so a missing grant hides the check, not
	// just a resource.
	for _, api := range otherPolicyAPIList() {
		watched[api.group] = "otherPolicyAPIList (internal/k8s/detect_netpol_onset.go)"
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
// (apiGroups:\n  - "a") rules. Conditional per-group rules are counted as available
// collection grants; most default to true. Flagger is an explicitly documented
// opt-in, with default-off/opt-in chart regression tests. This guard checks that
// each catalog group has a focused grant path, not that every path is enabled; the wildcard "*" is excluded on purpose.
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
