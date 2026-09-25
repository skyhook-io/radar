package k8s

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/pkg/topology"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	"helm.sh/helm/v3/pkg/strvals"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

type integrationReadEntry struct {
	Group      string   `json:"group"`
	Resources  []string `json:"resources"`
	Scope      string   `json:"scope"`
	Collection string   `json:"collection"`
	Decision   string   `json:"decision"`
	Reason     string   `json:"reason"`
	Source     string   `json:"source"`
}

func integrationReadBaseline(t *testing.T) []integrationReadEntry {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "helm", "radar", "files", "integration-read-baseline.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var table struct {
		Entries []integrationReadEntry `json:"entries"`
	}
	if err := yaml.UnmarshalStrict(raw, &table); err != nil {
		t.Fatal(err)
	}
	if len(table.Entries) == 0 {
		t.Fatal("empty integration read policy")
	}
	return table.Entries
}

func TestIntegrationReadBaselineCatalogCoverage(t *testing.T) {
	entries := map[string]integrationReadEntry{}
	for _, entry := range integrationReadBaseline(t) {
		if !slices.Contains([]string{"grant", "existing", "withhold"}, entry.Decision) ||
			!slices.Contains([]string{"Namespaced", "Cluster"}, entry.Scope) ||
			entry.Reason == "" || entry.Source == "" || len(entry.Resources) == 0 {
			t.Errorf("incomplete policy decision: %+v", entry)
		}
		if entry.Decision == "grant" && (entry.Collection == "" || entry.Group == "" || strings.Contains(entry.Group, "*") || entry.Group == "rbac.authorization.k8s.io") {
			t.Errorf("invalid grant: %+v", entry)
		}
		for _, resource := range entry.Resources {
			key := entry.Group + "/" + resource
			if _, exists := entries[key]; exists {
				t.Errorf("duplicate decision for %s", key)
			}
			if resource == "" || strings.ContainsAny(resource, "*/") || resource == "secrets" {
				t.Errorf("unexpected resource in integration policy: %s", key)
			}
			entries[key] = entry
		}
	}
	check := func(group, resource, scope string) {
		t.Helper()
		entry, ok := entries[group+"/"+resource]
		if !ok {
			t.Errorf("supported %s/%s needs an explicit grant/existing/withhold decision", group, resource)
		} else if entry.Scope != scope {
			t.Errorf("%s/%s scope = %s, want %s", group, resource, entry.Scope, scope)
		}
	}
	for _, candidate := range supportedCRDFallbacks {
		scope := "Cluster"
		if candidate.Namespaced {
			scope = "Namespaced"
		}
		check(candidate.Group, candidate.Resource, scope)
	}
	for _, candidate := range topology.ClusterScopedKinds {
		check(candidate.Group, candidate.Resource, "Cluster")
	}
	for _, family := range reportGroups {
		for _, gvr := range family.namespaced {
			check(gvr.Group, gvr.Resource, "Namespaced")
		}
		for _, gvr := range family.cluster {
			check(gvr.Group, gvr.Resource, "Cluster")
		}
	}
	for _, key := range []string{
		"argoproj.io/analysisruns", "argoproj.io/analysistemplates", "argoproj.io/clusteranalysistemplates",
		"argoproj.io/workflows", "argoproj.io/workflowtemplates", "argoproj.io/clusterworkflowtemplates", "argoproj.io/cronworkflows",
		"infrastructure.cluster.x-k8s.io/azuremachines", "infrastructure.cluster.x-k8s.io/azuremachinetemplates",
		"infrastructure.cluster.x-k8s.io/gcpmachines", "infrastructure.cluster.x-k8s.io/gcpmachinetemplates",
		"traefik.containo.us/serverstransporttcps",
		"external-secrets.io/secretstores", "external-secrets.io/clustersecretstores",
		"keda.sh/triggerauthentications", "keda.sh/clustertriggerauthentications", "kyverno.io/updaterequests",
		"bootstrap.cluster.x-k8s.io/kubeadmconfigs", "bootstrap.cluster.x-k8s.io/kubeadmconfigtemplates",
		"controlplane.cluster.x-k8s.io/kubeadmcontrolplanes", "kubernetes.crossplane.io/objects",
		"controlplane.cluster.x-k8s.io/kubeadmcontrolplanetemplates",
		"kubernetes.crossplane.io/providerconfigs", "helm.crossplane.io/providerconfigs", "helm.crossplane.io/releases",
		"aquasecurity.github.io/exposedsecretreports",
	} {
		if entries[key].Decision != "withhold" {
			t.Errorf("sensitive or pending-review tuple %s must be withheld", key)
		}
	}
}

type integrationRBACDocument struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`
	Rules             []rbacv1.PolicyRule `json:"rules"`
	Subjects          []rbacv1.Subject    `json:"subjects"`
	RoleRef           rbacv1.RoleRef      `json:"roleRef"`
}

func renderIntegrationReadChart(t *testing.T, overrides string, removeMap, removeKey bool) (map[string]string, []integrationRBACDocument) {
	t.Helper()
	chart, err := loader.Load(filepath.Join("..", "..", "deploy", "helm", "radar"))
	if err != nil {
		t.Fatal(err)
	}
	if removeMap || removeKey {
		rbac := chart.Values["cloud"].(map[string]interface{})["defaultRbac"].(map[string]interface{})
		if removeMap {
			delete(rbac, "integrationRead")
		} else {
			delete(rbac["integrationRead"].(map[string]interface{}), "viewer")
		}
	}
	values := map[string]interface{}{}
	if err := strvals.ParseInto("cloud.enabled=true,cloud.token=dummy,cloud.url=wss://cloud.example,cloud.clusterName=test,"+overrides, values); err != nil {
		t.Fatal(err)
	}
	renderValues, err := chartutil.ToRenderValues(chart, values, chartutil.ReleaseOptions{Name: "radar", Namespace: "radar", IsUpgrade: true}, chartutil.DefaultCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := engine.Render(chart, renderValues)
	if err != nil {
		t.Fatal(err)
	}
	decoder := yamlutil.NewYAMLOrJSONDecoder(strings.NewReader(rendered["radar/templates/cloud-rbac-integration-read.yaml"]), 4096)
	var docs []integrationRBACDocument
	for {
		var doc integrationRBACDocument
		if err := decoder.Decode(&doc); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if doc.Kind != "" {
			docs = append(docs, doc)
		}
	}
	return rendered, docs
}

func TestIntegrationReadBindings(t *testing.T) {
	all := []string{"viewer", "member", "owner"}
	for _, tc := range []struct {
		name, overrides      string
		namespaced, cluster  []string
		removeMap, removeKey bool
		// radar:system is bound whenever cloud mode renders RBAC at all,
		// independent of the tier settings.
		noSystem bool
	}{
		{name: "defaults", namespaced: all, cluster: all},
		{name: "OSS", overrides: "cloud.enabled=false", noSystem: true},
		{name: "customer managed", overrides: "cloud.defaultRbac.create=false"},
		{name: "RBAC disabled", overrides: "rbac.create=false", noSystem: true},
		{name: "system off", overrides: "cloud.systemRbac=false", namespaced: all, cluster: all, noSystem: true},
		{name: "system and tiers off", overrides: "cloud.systemRbac=false,cloud.defaultRbac.create=false", noSystem: true},
		{name: "viewer disabled", overrides: "cloud.defaultRbac.viewer=false", namespaced: all[1:], cluster: all[1:]},
		{name: "member addon off", overrides: "cloud.defaultRbac.integrationRead.member=false", namespaced: []string{"viewer", "owner"}, cluster: []string{"viewer", "owner"}},
		{name: "owner addon off", overrides: "cloud.defaultRbac.integrationRead.owner=false", namespaced: all[:2], cluster: all[:2]},
		{name: "viewer addon off", overrides: "cloud.defaultRbac.integrationRead.viewer=false", namespaced: all[1:], cluster: all[1:]},
		{name: "viewer cluster off", overrides: "cloud.defaultRbac.clusterScopedRead.viewer=false", namespaced: all, cluster: all[1:]},
		{name: "all addons off", overrides: "cloud.defaultRbac.integrationRead.viewer=false,cloud.defaultRbac.integrationRead.member=false,cloud.defaultRbac.integrationRead.owner=false"},
		{name: "all tiers off", overrides: "cloud.defaultRbac.viewer=false,cloud.defaultRbac.member=false,cloud.defaultRbac.owner=false"},
		{name: "all cluster off", overrides: "cloud.defaultRbac.clusterScopedRead.viewer=false,cloud.defaultRbac.clusterScopedRead.member=false,cloud.defaultRbac.clusterScopedRead.owner=false", namespaced: all},
		{name: "custom base role", overrides: "cloud.defaultRbac.viewerClusterRole=custom", namespaced: all, cluster: all},
		{name: "old values missing map", removeMap: true, namespaced: all, cluster: all},
		{name: "old values missing key", removeKey: true, namespaced: all, cluster: all},
		{name: "old values explicit false", removeMap: true, overrides: "cloud.defaultRbac.integrationRead.viewer=false", namespaced: all[1:], cluster: all[1:]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, docs := renderIntegrationReadChart(t, tc.overrides, tc.removeMap, tc.removeKey)
			namespaced, cluster := tc.namespaced, tc.cluster
			if !tc.noSystem {
				namespaced = append(append([]string{}, namespaced...), "system")
				cluster = append(append([]string{}, cluster...), "system")
			}
			wantDocs := 0
			for scope, tiers := range map[string][]string{"namespaced": namespaced, "cluster": cluster} {
				if len(tiers) == 0 {
					continue
				}
				wantDocs += 1 + len(tiers)
				roleName := "radar-integration-read-" + scope
				foundRole := false
				for _, doc := range docs {
					if doc.Kind == "ClusterRole" && doc.Name == roleName {
						foundRole = len(doc.Rules) > 0
					}
				}
				if !foundRole {
					t.Errorf("missing nonempty role %s", roleName)
				}
				for _, tier := range tiers {
					found := false
					for _, doc := range docs {
						if doc.Kind != "ClusterRoleBinding" || doc.Name != "radar-cloud-"+tier+"-integration-read-"+scope {
							continue
						}
						found = true
						if doc.RoleRef != (rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: roleName}) || !reflect.DeepEqual(doc.Subjects, []rbacv1.Subject{
							{Kind: "Group", Name: "radar:" + tier, APIGroup: rbacv1.GroupName},
							{Kind: "Group", Name: "cloud:" + tier, APIGroup: rbacv1.GroupName},
						}) {
							t.Errorf("incorrect binding: %+v", doc)
						}
					}
					if !found {
						t.Errorf("missing %s %s binding", scope, tier)
					}
				}
			}
			if len(docs) != wantDocs {
				t.Errorf("rendered %d documents, want %d (unexpected grants/orphan roles)", len(docs), wantDocs)
			}
		})
	}
}

func assertIntegrationReadRules(t *testing.T, docs []integrationRBACDocument, enabled func(string) bool) {
	t.Helper()
	want := map[string]string{}
	for _, entry := range integrationReadBaseline(t) {
		if entry.Decision == "grant" && enabled(entry.Collection) {
			for _, resource := range entry.Resources {
				want[entry.Group+"/"+resource] = strings.ToLower(entry.Scope)
			}
		}
	}
	for _, doc := range docs {
		if doc.Kind != "ClusterRole" {
			continue
		}
		for label := range doc.Labels {
			if strings.HasPrefix(label, "rbac.authorization.k8s.io/aggregate-to-") {
				t.Errorf("role %s must not aggregate into shared roles", doc.Name)
			}
		}
		for _, rule := range doc.Rules {
			if !reflect.DeepEqual(rule.Verbs, []string{"get", "list", "watch"}) || len(rule.APIGroups) != 1 || len(rule.ResourceNames) != 0 || len(rule.NonResourceURLs) != 0 {
				t.Fatalf("unexpected rule: %+v", rule)
			}
			for _, resource := range rule.Resources {
				key := rule.APIGroups[0] + "/" + resource
				scope, ok := want[key]
				if !ok || doc.Name != "radar-integration-read-"+scope {
					t.Errorf("unapproved, duplicate or wrong-scope grant %s in %s", key, doc.Name)
				}
				delete(want, key)
			}
		}
	}
	if len(want) > 0 {
		t.Errorf("missing approved grants: %v", want)
	}
}

func TestIntegrationReadRulesAndCollectionGates(t *testing.T) {
	families := map[string]bool{}
	for _, entry := range integrationReadBaseline(t) {
		if entry.Decision == "grant" {
			families[entry.Collection] = true
		}
	}
	_, defaults := renderIntegrationReadChart(t, "", false, false)
	assertIntegrationReadRules(t, defaults, func(string) bool { return true })
	var off []string
	for family := range families {
		off = append(off, "rbac.crdGroups."+family+"=false")
		t.Run(family, func(t *testing.T) {
			_, docs := renderIntegrationReadChart(t, "rbac.crdGroups."+family+"=false", false, false)
			assertIntegrationReadRules(t, docs, func(f string) bool { return f != family })
		})
	}
	slices.Sort(off)
	_, none := renderIntegrationReadChart(t, strings.Join(off, ","), false, false)
	if len(none) != 0 {
		t.Fatal("all collections disabled should render no integration roles or bindings")
	}
	_, all := renderIntegrationReadChart(t, strings.Join(off, ",")+",rbac.crdGroups.all=true", false, false)
	assertIntegrationReadRules(t, all, func(string) bool { return true })
	_, additional := renderIntegrationReadChart(t, "rbac.additionalCrdGroups={customer.example}", false, false)
	assertIntegrationReadRules(t, additional, func(string) bool { return true })

	_, clusterOff := renderIntegrationReadChart(t, "cloud.defaultRbac.clusterScopedRead.viewer=false", false, false)
	for _, key := range []string{"monitoring.coreos.com/prometheusrules", "cilium.io/ciliumnetworkpolicies"} {
		found := false
		for _, doc := range clusterOff {
			if doc.Name != "radar-integration-read-namespaced" {
				continue
			}
			for _, rule := range doc.Rules {
				for _, resource := range rule.Resources {
					found = found || rule.APIGroups[0]+"/"+resource == key
				}
			}
		}
		if !found {
			t.Errorf("cluster-read opt-out must not remove namespaced baseline %s", key)
		}
	}
}

func TestIntegrationReadDoesNotChangeExistingManifests(t *testing.T) {
	on, _ := renderIntegrationReadChart(t, "rbac.helm=true", false, false)
	off, _ := renderIntegrationReadChart(t, "rbac.helm=true,cloud.defaultRbac.integrationRead.viewer=false,cloud.defaultRbac.integrationRead.member=false,cloud.defaultRbac.integrationRead.owner=false", false, false)
	for name, rendered := range on {
		if name != "radar/templates/cloud-rbac-integration-read.yaml" && !bytes.Equal([]byte(rendered), []byte(off[name])) {
			t.Errorf("integration addon changed existing manifest %s", name)
		}
	}
}

func TestIntegrationReadCollectionMatchesCollector(t *testing.T) {
	chart, err := loader.Load(filepath.Join("..", "..", "deploy", "helm", "radar"))
	if err != nil {
		t.Fatal(err)
	}
	flags := chart.Values["rbac"].(map[string]interface{})["crdGroups"].(map[string]interface{})
	var off []string
	for flag := range flags {
		off = append(off, "rbac.crdGroups."+flag+"=false")
	}
	slices.Sort(off)
	families := map[string][]integrationReadEntry{}
	for _, entry := range integrationReadBaseline(t) {
		if entry.Decision == "grant" {
			if _, exists := flags[entry.Collection]; !exists || entry.Collection == "all" {
				t.Fatalf("invalid collection flag for %+v", entry)
			}
			families[entry.Collection] = append(families[entry.Collection], entry)
		}
	}
	for family, entries := range families {
		t.Run(family, func(t *testing.T) {
			rendered, docs := renderIntegrationReadChart(t, strings.Join(off, ",")+",rbac.crdGroups."+family+"=true", false, false)
			assertIntegrationReadRules(t, docs, func(f string) bool { return f == family })
			var collector rbacv1.ClusterRole
			if err := yaml.Unmarshal([]byte(rendered["radar/templates/clusterrole.yaml"]), &collector); err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				for _, resource := range entry.Resources {
					for _, verb := range []string{"get", "list", "watch"} {
						allowed := false
						for _, rule := range collector.Rules {
							allowed = allowed || (slices.Contains(rule.APIGroups, entry.Group) &&
								(slices.Contains(rule.Resources, resource) || slices.Contains(rule.Resources, "*")) && slices.Contains(rule.Verbs, verb))
						}
						if !allowed {
							t.Errorf("%s doesn't enable collector %s %s/%s", family, verb, entry.Group, resource)
						}
					}
				}
			}
		})
	}
}
