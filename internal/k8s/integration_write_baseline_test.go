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
	Group     string   `json:"group"`
	Resources []string `json:"resources"`
	Verbs     []string `json:"verbs"`

	Collection string `json:"collection"`
	Capability string `json:"capability"`
}

type integrationWritePolicy struct {
	Capabilities map[string]struct {
		Default *bool `json:"default"`
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
		if capability.Default == nil {
			t.Fatalf("capability %s must declare a boolean default", name)
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
		if entry.Capability == "" || len(entry.Resources) == 0 || len(entry.Verbs) == 0 {
			t.Errorf("incomplete write grant: %+v", entry)
		}
		for _, verb := range entry.Verbs {
			if !slices.Contains(allowedVerbs, verb) {
				t.Errorf("verb %q in %s is not an object write/read verb", verb, entry.Group)
			}
		}
		if strings.Contains(entry.Group, "*") || entry.Group == "rbac.authorization.k8s.io" {
			t.Errorf("invalid write group: %+v", entry)
		}
		if _, ok := policy.Capabilities[entry.Capability]; !ok {
			t.Errorf("grant row %s needs a declared capability switch, got %q", entry.Group, entry.Capability)
		}
		if _, ok := collections[entry.Collection]; !ok || entry.Collection == "all" || entry.Group == "" {
			t.Errorf("grant row %s needs a collection flag, got %q", entry.Group, entry.Collection)
		}
		for _, resource := range entry.Resources {
			key := entry.Group + "/" + resource
			if seen[key] {
				t.Errorf("duplicate grant for %s", key)
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
		if name == "cnpg" {
			if len(switches) != 0 {
				t.Errorf("values.yaml integrationWrite.cnpg must leave owner unset for the role-dependent default, got %v", switches)
			}
		} else if got, ok := switches["owner"].(bool); !ok || got != *capability.Default || len(switches) != 1 {
			t.Errorf("values.yaml integrationWrite.%s = %v, want only owner: %t", name, switches, *capability.Default)
		}
		node, _ := schemaCapabilities[name].(map[string]interface{})
		owner, _ := node["properties"].(map[string]interface{})["owner"].(map[string]interface{})
		if node["additionalProperties"] != false || len(node["properties"].(map[string]interface{})) != 1 ||
			owner["type"] != "boolean" {
			t.Errorf("schema for integrationWrite.%s must accept only a boolean owner switch, got %v", name, node)
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
		if entry.Capability == "argocd" {
			want = append(want, rbacv1.PolicyRule{APIGroups: []string{entry.Group}, Resources: entry.Resources, Verbs: entry.Verbs})
		}
	}
	if len(docs) != 2 {
		t.Fatalf("rendered %d documents, want role + owner binding", len(docs))
	}
	role, binding := docs[0], docs[1]
	if role.Kind != "ClusterRole" || role.Name != "radar-owner-access-argocd" || !reflect.DeepEqual(role.Rules, want) {
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
		{Kind: "Group", Name: "radar:owner-access:argocd", APIGroup: rbacv1.GroupName},
	}
	if binding.Kind != "ClusterRoleBinding" || binding.Name != "radar-owner-access-argocd" ||
		binding.RoleRef != (rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "radar-owner-access-argocd"}) ||
		!reflect.DeepEqual(binding.Subjects, wantSubjects) {
		t.Errorf("unexpected write binding: %+v", binding)
	}
}

func TestIntegrationWriteOwnerSwitch(t *testing.T) {
	policy := integrationWriteBaseline(t)
	dropMap := func(rbac map[string]interface{}) { delete(rbac, "integrationWrite") }
	dropCapability := func(rbac map[string]interface{}) {
		delete(rbac["integrationWrite"].(map[string]interface{}), "argocd")
	}
	dropKey := func(rbac map[string]interface{}) {
		delete(rbac["integrationWrite"].(map[string]interface{})["argocd"].(map[string]interface{}), "owner")
	}
	for _, tc := range []struct {
		name, overrides string
		editDefaults    func(map[string]interface{})
		granted         bool
	}{
		{name: "default with stock admin", granted: true},
		{name: "default with cluster-admin", overrides: "cloud.defaultRbac.ownerClusterRole=cluster-admin", granted: true},
		{name: "default with a custom owner role", overrides: "cloud.defaultRbac.ownerClusterRole=platform-owner", granted: true},
		{name: "true with a custom owner role", overrides: "cloud.defaultRbac.ownerClusterRole=platform-owner,cloud.defaultRbac.integrationWrite.argocd.owner=true", granted: true},
		{name: "true with stock admin", overrides: "cloud.defaultRbac.integrationWrite.argocd.owner=true", granted: true},
		{name: "false", overrides: "cloud.defaultRbac.integrationWrite.argocd.owner=false"},
		{name: "false with cluster-admin", overrides: "cloud.defaultRbac.ownerClusterRole=cluster-admin,cloud.defaultRbac.integrationWrite.argocd.owner=false"},
		{name: "old values missing map", editDefaults: dropMap, granted: true},
		{name: "old values missing map, custom owner role", editDefaults: dropMap, overrides: "cloud.defaultRbac.ownerClusterRole=platform-owner", granted: true},
		{name: "old values missing capability", editDefaults: dropCapability, granted: true},
		{name: "old values missing key", editDefaults: dropKey, granted: true},
		{name: "old values missing map, explicit false", editDefaults: dropMap, overrides: "cloud.defaultRbac.integrationWrite.argocd.owner=false"},
		{name: "old values missing map, explicit true", editDefaults: dropMap, overrides: "cloud.defaultRbac.ownerClusterRole=platform-owner,cloud.defaultRbac.integrationWrite.argocd.owner=true", granted: true},
		{name: "customer-managed RBAC", overrides: "cloud.defaultRbac.create=false,cloud.defaultRbac.integrationWrite.argocd.owner=true"},
		{name: "owner tier off", overrides: "cloud.defaultRbac.owner=false,cloud.defaultRbac.integrationWrite.argocd.owner=true"},
		{name: "OSS", overrides: "cloud.enabled=false,cloud.defaultRbac.integrationWrite.argocd.owner=true"},
		{name: "RBAC disabled", overrides: "rbac.create=false,cloud.defaultRbac.integrationWrite.argocd.owner=true"},
		{name: "Argo collection off", overrides: "rbac.crdGroups.argo=false,cloud.defaultRbac.integrationWrite.argocd.owner=true"},
		{name: "Argo collection off, all on", overrides: "rbac.crdGroups.argo=false,rbac.crdGroups.all=true,cloud.defaultRbac.integrationWrite.argocd.owner=true", granted: true},
		{name: "member and viewer tiers off", overrides: "cloud.defaultRbac.member=false,cloud.defaultRbac.viewer=false,cloud.defaultRbac.integrationWrite.argocd.owner=true", granted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			all := renderIntegrationWriteChart(t, tc.overrides, tc.editDefaults)
			var docs []integrationRBACDocument
			for _, doc := range all {
				if doc.Name == "radar-owner-access-argocd" {
					docs = append(docs, doc)
				}
			}
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

func TestIntegrationWriteCNPGOwnerApproval(t *testing.T) {
	const customOwner = "cloud.defaultRbac.ownerClusterRole=custom-operator"
	const approveCNPG = "cloud.defaultRbac.integrationWrite.cnpg.owner=true"
	dropMap := func(rbac map[string]interface{}) { delete(rbac, "integrationWrite") }
	for _, tc := range []struct {
		name, overrides string
		editDefaults    func(map[string]interface{})
		wantGrants      string
	}{
		{name: "stock admin", wantGrants: "argocd,cnpg,velero"},
		{name: "cluster-admin", overrides: "cloud.defaultRbac.ownerClusterRole=cluster-admin", wantGrants: "argocd,cnpg,velero"},
		{name: "stock admin scoped to namespaces", overrides: "rbac.namespaces={databases}", wantGrants: "argocd,cnpg"},
		{name: "stock admin explicit opt-out", overrides: "cloud.defaultRbac.integrationWrite.cnpg.owner=false", wantGrants: "argocd,velero"},
		{name: "custom role", overrides: customOwner, wantGrants: "argocd"},
		{name: "custom role explicit opt-in", overrides: customOwner + "," + approveCNPG, wantGrants: "argocd,cnpg"},
		{name: "old stock values missing map", editDefaults: dropMap, wantGrants: "argocd,cnpg,velero"},
		{name: "old custom values missing map", editDefaults: dropMap, overrides: customOwner, wantGrants: "argocd"},
		{name: "old custom values explicit opt-in", editDefaults: dropMap, overrides: customOwner + "," + approveCNPG, wantGrants: "argocd,cnpg"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rendered := renderCloudChart(t, tc.overrides, tc.editDefaults)
			wantCNPG := slices.Contains(strings.Split(tc.wantGrants, ","), "cnpg")
			var hasCNPG bool
			for _, doc := range decodeRBACDocuments(t, rendered["radar/templates/cloud-rbac-integration-write.yaml"]) {
				for _, subject := range doc.Subjects {
					if subject.Kind == "Group" && subject.Name == "radar:owner-access:cnpg" {
						hasCNPG = true
					}
				}
			}
			if hasCNPG != wantCNPG {
				t.Errorf("CNPG group binding = %t, want %t", hasCNPG, wantCNPG)
			}
			wantEnv := "- name: RADAR_CLOUD_OWNER_ACCESS\n              value: " + fmt.Sprintf("%q", tc.wantGrants)
			if !strings.Contains(rendered["radar/templates/deployment.yaml"], wantEnv) {
				t.Errorf("deployment does not advertise %q", tc.wantGrants)
			}
		})
	}
}

func TestIntegrationWriteDoesNotChangeExistingManifests(t *testing.T) {
	on := renderCloudChart(t, "rbac.helm=true,cloud.defaultRbac.integrationWrite.argocd.owner=true", nil)
	off := renderCloudChart(t, "rbac.helm=true,cloud.defaultRbac.integrationWrite.argocd.owner=false", nil)
	for name, rendered := range on {
		if name == "radar/templates/deployment.yaml" {
			advertised := `value: "argocd,cnpg,velero"`
			if strings.Count(rendered, advertised) != 1 {
				t.Fatal("deployment must advertise all three approved default grants")
			}
			rendered = strings.Replace(rendered, advertised, `value: "cnpg,velero"`, 1)
			const onSetup = `{"blocked":{"velero-delete":"helm","velero-download":"helm"},"mode":"standard"}`
			const offSetup = `{"blocked":{"argocd":"helm","velero-delete":"helm","velero-download":"helm"},"mode":"standard"}`
			if !strings.Contains(rendered, fmt.Sprintf("%q", onSetup)) {
				t.Fatal("deployment must advertise standard Owner setup")
			}
			rendered = strings.Replace(rendered, fmt.Sprintf("%q", onSetup), fmt.Sprintf("%q", offSetup), 1)
		}
		if name != "radar/templates/cloud-rbac-integration-write.yaml" && !bytes.Equal([]byte(rendered), []byte(off[name])) {
			t.Errorf("integration write add-on changed existing manifest %s", name)
		}
	}
}

func TestIntegrationWriteInstallGuidance(t *testing.T) {
	for _, tc := range []struct {
		name, overrides, mode string
		blocked               map[string]string
	}{
		{name: "custom Kubernetes RBAC", overrides: "cloud.defaultRbac.create=false", mode: "kubernetes", blocked: map[string]string{}},
		{name: "Owner role disabled", overrides: "cloud.defaultRbac.owner=false", mode: "owner-disabled", blocked: map[string]string{}},
		{name: "custom Owner base role", overrides: "cloud.defaultRbac.ownerClusterRole=platform-owner",
			mode: "custom-owner", blocked: map[string]string{"cnpg": "helm", "velero": "velero-scope", "velero-download": "velero-scope", "velero-delete": "velero-scope"}},
		{name: "Argo collection disabled", overrides: "rbac.crdGroups.argo=false",
			mode: "standard", blocked: map[string]string{"argocd": "collection", "velero-delete": "helm", "velero-download": "helm"}},
		{name: "scoped Velero", overrides: "rbac.namespaces={databases}",
			mode: "standard", blocked: map[string]string{"velero": "velero-scope", "velero-download": "velero-scope", "velero-delete": "velero-scope"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setup, err := json.Marshal(map[string]interface{}{"mode": tc.mode, "blocked": tc.blocked})
			if err != nil {
				t.Fatal(err)
			}
			want := "- name: RADAR_CLOUD_OWNER_ACCESS_SETUP\n              value: " + fmt.Sprintf("%q", setup)
			if got := renderCloudChart(t, tc.overrides, nil)["radar/templates/deployment.yaml"]; !strings.Contains(got, want) {
				t.Errorf("deployment did not advertise setup %s", setup)
			}
		})
	}
}
