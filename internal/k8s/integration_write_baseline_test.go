package k8s

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/chart/loader"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

type integrationWriteEntry struct {
	Group      string   `json:"group"`
	Resources  []string `json:"resources"`
	Verbs      []string `json:"verbs"`
	Scope      string   `json:"scope"`
	Collection string   `json:"collection"`
	Capability string   `json:"capability"`
	Decision   string   `json:"decision"`
	Reason     string   `json:"reason"`
	Source     string   `json:"source"`
	Actions    []string `json:"actions"`
}

type integrationWritePolicy struct {
	Capabilities map[string]struct {
		Default string `json:"default"`
	} `json:"capabilities"`
	Entries []integrationWriteEntry `json:"entries"`
}

func integrationWriteBaseline(t *testing.T) integrationWritePolicy {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "helm", "radar", "files", "integration-write-baseline.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var policy integrationWritePolicy
	if err := yaml.UnmarshalStrict(raw, &policy); err != nil {
		t.Fatal(err)
	}
	if len(policy.Entries) == 0 || len(policy.Capabilities) == 0 {
		t.Fatal("empty integration write policy")
	}
	return policy
}

func TestIntegrationWriteBaselinePolicy(t *testing.T) {
	policy := integrationWriteBaseline(t)
	for name, capability := range policy.Capabilities {
		if !slices.Contains([]string{"auto", "true", "false"}, capability.Default) {
			t.Errorf("capability %s must declare default auto, true or false, got %q", name, capability.Default)
		}
	}
	chart, err := loader.Load(filepath.Join("..", "..", "deploy", "helm", "radar"))
	if err != nil {
		t.Fatal(err)
	}
	collections := chart.Values["rbac"].(map[string]interface{})["crdGroups"].(map[string]interface{})
	allowedVerbs := []string{"get", "list", "watch", "create", "update", "patch", "delete"}
	seen := map[string]bool{}
	for _, entry := range policy.Entries {
		if !slices.Contains([]string{"grant", "existing", "withhold"}, entry.Decision) ||
			!slices.Contains([]string{"Namespaced", "Cluster"}, entry.Scope) ||
			entry.Reason == "" || entry.Source == "" || entry.Capability == "" ||
			len(entry.Resources) == 0 || len(entry.Verbs) == 0 || len(entry.Actions) == 0 {
			t.Errorf("incomplete write decision: %+v", entry)
		}
		for _, verb := range entry.Verbs {
			if !slices.Contains(allowedVerbs, verb) {
				t.Errorf("verb %q in %s is not an object write/read verb", verb, entry.Group)
			}
		}
		if strings.Contains(entry.Group, "*") || entry.Group == "rbac.authorization.k8s.io" {
			t.Errorf("invalid write group: %+v", entry)
		}
		if entry.Decision == "grant" {
			if _, ok := policy.Capabilities[entry.Capability]; !ok {
				t.Errorf("grant row %s needs a declared capability switch, got %q", entry.Group, entry.Capability)
			}
			if _, ok := collections[entry.Collection]; !ok || entry.Collection == "all" || entry.Group == "" {
				t.Errorf("grant row %s needs a collection flag, got %q", entry.Group, entry.Collection)
			}
		}
		for _, resource := range entry.Resources {
			key := entry.Group + "/" + resource
			if seen[key] {
				t.Errorf("duplicate decision for %s", key)
			}
			seen[key] = true
			if resource == "" || strings.Contains(resource, "*") || resource == "secrets" {
				t.Errorf("unexpected resource in integration write policy: %s", key)
			}
		}
	}

	// values.yaml, the schema and the table must describe the same switches.
	writeValues, _ := chart.Values["cloud"].(map[string]interface{})["defaultRbac"].(map[string]interface{})["integrationWrite"].(map[string]interface{})
	var schema map[string]interface{}
	if err := json.Unmarshal(chart.Schema, &schema); err != nil {
		t.Fatal(err)
	}
	schemaNode := schema
	for _, key := range []string{"cloud", "defaultRbac", "integrationWrite"} {
		schemaNode = schemaNode["properties"].(map[string]interface{})[key].(map[string]interface{})
	}
	if schemaNode["additionalProperties"] != false {
		t.Error("schema must reject unknown integrationWrite capabilities")
	}
	schemaCapabilities := schemaNode["properties"].(map[string]interface{})
	var declared []string
	for name, capability := range policy.Capabilities {
		declared = append(declared, name)
		switches, _ := writeValues[name].(map[string]interface{})
		if got := fmt.Sprint(switches["owner"]); got != capability.Default || len(switches) != 1 {
			t.Errorf("values.yaml integrationWrite.%s = %v, want only owner: %s", name, switches, capability.Default)
		}
		node, _ := schemaCapabilities[name].(map[string]interface{})
		owner, _ := node["properties"].(map[string]interface{})["owner"].(map[string]interface{})
		if node["additionalProperties"] != false || len(node["properties"].(map[string]interface{})) != 1 ||
			!reflect.DeepEqual(owner["enum"], []interface{}{"auto", true, false}) {
			t.Errorf("schema for integrationWrite.%s must accept only owner: auto|true|false, got %v", name, node)
		}
	}
	sort.Strings(declared)
	if got := slices.Sorted(maps.Keys(writeValues)); !reflect.DeepEqual(got, declared) {
		t.Errorf("values.yaml integrationWrite capabilities = %v, table declares %v", got, declared)
	}
	if got := slices.Sorted(maps.Keys(schemaCapabilities)); !reflect.DeepEqual(got, declared) {
		t.Errorf("schema integrationWrite capabilities = %v, table declares %v", got, declared)
	}
}

func renderIntegrationWriteChart(t *testing.T, overrides string, editDefaults func(map[string]interface{})) []integrationRBACDocument {
	t.Helper()
	rendered := renderCloudChart(t, overrides, editDefaults)
	return decodeRBACDocuments(t, rendered["radar/templates/cloud-rbac-integration-write.yaml"])
}

func assertIntegrationWriteGrant(t *testing.T, policy integrationWritePolicy, docs []integrationRBACDocument) {
	t.Helper()
	var want []rbacv1.PolicyRule
	for _, entry := range policy.Entries {
		if entry.Decision == "grant" && entry.Capability == "gitops" {
			want = append(want, rbacv1.PolicyRule{APIGroups: []string{entry.Group}, Resources: entry.Resources, Verbs: entry.Verbs})
		}
	}
	if len(docs) != 2 {
		t.Fatalf("rendered %d documents, want role + owner binding", len(docs))
	}
	role, binding := docs[0], docs[1]
	if role.Kind != "ClusterRole" || role.Name != "radar-integration-write" || !reflect.DeepEqual(role.Rules, want) {
		t.Errorf("unexpected write role: %+v", role)
	}
	if !reflect.DeepEqual(want, []rbacv1.PolicyRule{{APIGroups: []string{"argoproj.io"}, Resources: []string{"applications"}, Verbs: []string{"get", "patch"}}}) {
		t.Errorf("grant rows changed to %+v; that expands every enabled install and needs review", want)
	}
	for label := range role.Labels {
		if strings.HasPrefix(label, "rbac.authorization.k8s.io/aggregate-to-") {
			t.Errorf("write role must not aggregate into shared roles")
		}
	}
	wantSubjects := []rbacv1.Subject{
		{Kind: "Group", Name: "radar:owner", APIGroup: rbacv1.GroupName},
		{Kind: "Group", Name: "cloud:owner", APIGroup: rbacv1.GroupName},
	}
	if binding.Kind != "ClusterRoleBinding" || binding.Name != "radar-cloud-owner-integration-write" ||
		binding.RoleRef != (rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "radar-integration-write"}) ||
		!reflect.DeepEqual(binding.Subjects, wantSubjects) {
		t.Errorf("unexpected write binding: %+v", binding)
	}
}

func TestIntegrationWriteOwnerSwitch(t *testing.T) {
	policy := integrationWriteBaseline(t)
	dropMap := func(rbac map[string]interface{}) { delete(rbac, "integrationWrite") }
	dropCapability := func(rbac map[string]interface{}) {
		delete(rbac["integrationWrite"].(map[string]interface{}), "gitops")
	}
	dropKey := func(rbac map[string]interface{}) {
		delete(rbac["integrationWrite"].(map[string]interface{})["gitops"].(map[string]interface{}), "owner")
	}
	for _, tc := range []struct {
		name, overrides string
		editDefaults    func(map[string]interface{})
		granted         bool
	}{
		{name: "auto with stock admin", granted: true},
		{name: "auto with cluster-admin", overrides: "cloud.defaultRbac.ownerClusterRole=cluster-admin", granted: true},
		{name: "auto with a custom owner role", overrides: "cloud.defaultRbac.ownerClusterRole=platform-owner"},
		{name: "auto matches the role name exactly", overrides: "cloud.defaultRbac.ownerClusterRole=admin-lite"},
		{name: "true with a custom owner role", overrides: "cloud.defaultRbac.ownerClusterRole=platform-owner,cloud.defaultRbac.integrationWrite.gitops.owner=true", granted: true},
		{name: "explicit auto", overrides: "cloud.defaultRbac.integrationWrite.gitops.owner=auto", granted: true},
		{name: "false", overrides: "cloud.defaultRbac.integrationWrite.gitops.owner=false"},
		{name: "false with cluster-admin", overrides: "cloud.defaultRbac.ownerClusterRole=cluster-admin,cloud.defaultRbac.integrationWrite.gitops.owner=false"},
		{name: "old values missing map", editDefaults: dropMap, granted: true},
		{name: "old values missing map, custom owner role", editDefaults: dropMap, overrides: "cloud.defaultRbac.ownerClusterRole=platform-owner"},
		{name: "old values missing capability", editDefaults: dropCapability, granted: true},
		{name: "old values missing key", editDefaults: dropKey, granted: true},
		{name: "old values missing map, explicit false", editDefaults: dropMap, overrides: "cloud.defaultRbac.integrationWrite.gitops.owner=false"},
		{name: "old values missing map, explicit true", editDefaults: dropMap, overrides: "cloud.defaultRbac.ownerClusterRole=platform-owner,cloud.defaultRbac.integrationWrite.gitops.owner=true", granted: true},
		{name: "customer-managed RBAC", overrides: "cloud.defaultRbac.create=false,cloud.defaultRbac.integrationWrite.gitops.owner=true"},
		{name: "owner tier off", overrides: "cloud.defaultRbac.owner=false,cloud.defaultRbac.integrationWrite.gitops.owner=true"},
		{name: "OSS", overrides: "cloud.enabled=false,cloud.defaultRbac.integrationWrite.gitops.owner=true"},
		{name: "RBAC disabled", overrides: "rbac.create=false,cloud.defaultRbac.integrationWrite.gitops.owner=true"},
		{name: "Argo collection off", overrides: "rbac.crdGroups.argo=false,cloud.defaultRbac.integrationWrite.gitops.owner=true"},
		{name: "Argo collection off, all on", overrides: "rbac.crdGroups.argo=false,rbac.crdGroups.all=true", granted: true},
		{name: "member and viewer tiers off", overrides: "cloud.defaultRbac.member=false,cloud.defaultRbac.viewer=false", granted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			docs := renderIntegrationWriteChart(t, tc.overrides, tc.editDefaults)
			if !tc.granted {
				if len(docs) != 0 {
					t.Fatalf("expected no write add-on, rendered %+v", docs)
				}
				return
			}
			assertIntegrationWriteGrant(t, policy, docs)
		})
	}
}

func TestIntegrationWriteDoesNotChangeExistingManifests(t *testing.T) {
	on := renderCloudChart(t, "rbac.helm=true,cloud.defaultRbac.integrationWrite.gitops.owner=true", nil)
	off := renderCloudChart(t, "rbac.helm=true,cloud.defaultRbac.integrationWrite.gitops.owner=false", nil)
	for name, rendered := range on {
		if name != "radar/templates/cloud-rbac-integration-write.yaml" && !bytes.Equal([]byte(rendered), []byte(off[name])) {
			t.Errorf("integration write add-on changed existing manifest %s", name)
		}
	}
}
