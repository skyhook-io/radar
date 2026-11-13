package helm

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestFluxHelmReleaseKeysIgnoresRemoteReleases(t *testing.T) {
	hr := func(name string, spec map[string]any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"metadata": map[string]any{"namespace": "flux-system", "name": name},
			"spec":     spec,
		}}
	}
	got := fluxHelmReleaseKeys([]*unstructured.Unstructured{
		hr("local", map[string]any{"releaseName": "web", "storageNamespace": "apps"}),
		hr("prod", map[string]any{
			"releaseName":      "api",
			"storageNamespace": "apps",
			"kubeConfig":       map[string]any{"secretRef": map[string]any{"name": "prod-kubeconfig"}},
		}),
	})
	if got["apps/web"] != "flux-system/local" {
		t.Errorf("local release owner = %q, want flux-system/local", got["apps/web"])
	}
	if owner, ok := got["apps/api"]; ok {
		t.Errorf("remote HelmRelease claimed local release apps/api as %q", owner)
	}
}

func TestFluxManagerNamespaceScope(t *testing.T) {
	useCrossNamespaceCache(t)
	rel := crossNamespaceRelease()
	client := fake.NewSimpleClientset(helmReleaseSecret(t, "team-a", rel, false))
	snapshot, err := helmReleaseStorageSnapshotWithClient(context.Background(), client, "team-a")
	if err != nil {
		t.Fatal(err)
	}
	manager := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
		"metadata": map[string]interface{}{"name": "private-manager", "namespace": "shared"},
		"spec":     map[string]interface{}{"releaseName": "payments", "storageNamespace": "team-a"},
	}}
	ownership := fluxHelmReleaseKeys([]*unstructured.Unstructured{manager})
	for _, tc := range []struct {
		name    string
		scope   StatusAccess
		wantRef bool
	}{
		{"admin", adminCaller(), true}, {"restricted", teamACaller(), false}, {"unchecked", NamespaceStatusScope{}, false}, {"internal", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := helmReleaseRowsFromStorageSnapshot(context.Background(), snapshot, ownership, "", tc.scope)
			if err != nil || len(rows) != 1 {
				t.Fatalf("rows=%d err=%v", len(rows), err)
			}
			if !rows[0].ManagedByFlux {
				t.Fatal("lost Flux ownership and direct-write protection")
			}
			ref := rows[0].ManagedByFluxHelmRelease
			if strings.Contains(ref, "private-manager") != tc.wantRef {
				t.Fatalf("manager reference=%q, caller should see it=%v", ref, tc.wantRef)
			}
			if !tc.wantRef && strings.Contains(mustJSON(t, rows[0]), "private-manager") {
				t.Fatal("hidden controller leaked in serialized response")
			}
			if rows[0].Name != rel.Name {
				t.Fatal("lost readable release")
			}
		})
	}
}

func TestFluxManagerListNamespaceScope(t *testing.T) {
	useCrossNamespaceCache(t)
	gvr := schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}
	owner := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease", "metadata": map[string]interface{}{"name": "manager", "namespace": "shared"}, "spec": map[string]interface{}{"releaseName": "payments", "storageNamespace": "team-a"}}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "HelmReleaseList"}, owner)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: "HelmRelease", Namespaced: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	cache := k8s.GetDynamicResourceCache()
	if err := cache.EnsureWatching(gvr); err != nil {
		t.Fatal(err)
	}
	if !cache.WaitForSync(gvr, 5*time.Second) {
		t.Fatal("controller cache did not sync")
	}
	rel := crossNamespaceRelease()
	client := fake.NewSimpleClientset(helmReleaseSecret(t, "team-a", rel, false))
	for _, tc := range []struct {
		name    string
		scope   StatusAccess
		wantRef string
	}{
		{"admin", adminCaller(), "shared/manager"},
		{"restricted", teamACaller(), ""},
		{"unchecked", NamespaceStatusScope{}, ""},
		{"internal", nil, ""},
	} {
		for _, health := range []bool{true, false} {
			name := tc.name + "/health-off"
			if health {
				name = tc.name + "/health-on"
			}
			t.Run(name, func(t *testing.T) {
				rows, err := listReleasesAcrossNamespacesWithClient(context.Background(), client, "", []string{"team-a"}, health, tc.scope)
				if err != nil || len(rows) != 1 {
					t.Fatalf("rows=%d err=%v", len(rows), err)
				}
				if !rows[0].ManagedByFlux {
					t.Fatal("lost controller ownership")
				}
				if rows[0].ManagedByFluxHelmRelease != tc.wantRef {
					t.Fatalf("reference=%q want %q", rows[0].ManagedByFluxHelmRelease, tc.wantRef)
				}
				if tc.wantRef == "" && strings.Contains(mustJSON(t, rows[0]), "shared/manager") {
					t.Fatal("hidden controller leaked")
				}
			})
		}
	}
}
