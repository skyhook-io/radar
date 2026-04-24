package k8score

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// DropUnstructuredManagedFields is the SharedInformer transform used by
// the dynamic cache. The tests below pin its two invariants:
//   1. For any unstructured object, managedFields and the
//      last-applied-configuration annotation are gone.
//   2. For CustomResourceDefinitions, the heavy fields (versions[].schema,
//      conversion) are gone while list-view fields (name, served/storage,
//      additionalPrinterColumns, spec.group, spec.names) survive.

func TestDropUnstructuredManagedFields_NonCRD(t *testing.T) {
	u := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "cilium.io/v2",
			"kind":       "CiliumNetworkPolicy",
			"metadata": map[string]any{
				"name":      "allow-dns",
				"namespace": "default",
				"managedFields": []any{
					map[string]any{"manager": "kubectl"},
				},
				"annotations": map[string]any{
					"kubectl.kubernetes.io/last-applied-configuration": "{big blob}",
					"description": "allow DNS",
				},
			},
			"spec": map[string]any{"endpointSelector": map[string]any{}},
		},
	}

	out, err := DropUnstructuredManagedFields(u)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	got := out.(*unstructured.Unstructured)

	// managedFields gone
	if _, found, _ := unstructured.NestedSlice(got.Object, "metadata", "managedFields"); found {
		t.Error("managedFields should be stripped")
	}

	// last-applied-configuration gone, other annotations preserved
	annotations := got.GetAnnotations()
	if _, ok := annotations["kubectl.kubernetes.io/last-applied-configuration"]; ok {
		t.Error("last-applied-configuration annotation should be stripped")
	}
	if annotations["description"] != "allow DNS" {
		t.Errorf("other annotations should be preserved, got %v", annotations)
	}

	// Non-CRD spec untouched
	if _, found, _ := unstructured.NestedMap(got.Object, "spec", "endpointSelector"); !found {
		t.Error("non-CRD spec fields should be preserved")
	}
}

func TestDropUnstructuredManagedFields_CRD(t *testing.T) {
	u := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "apiextensions.k8s.io/v1",
			"kind":       "CustomResourceDefinition",
			"metadata": map[string]any{
				"name": "certificates.cert-manager.io",
				"managedFields": []any{
					map[string]any{"manager": "helm"},
				},
			},
			"spec": map[string]any{
				"group": "cert-manager.io",
				"names": map[string]any{
					"kind":     "Certificate",
					"plural":   "certificates",
					"singular": "certificate",
				},
				"scope": "Namespaced",
				"conversion": map[string]any{
					"strategy": "Webhook",
					"webhook": map[string]any{
						"clientConfig": map[string]any{
							"caBundle": "LS0tLS1CRUdJTi...a 4KB base64 blob...",
							"service": map[string]any{"name": "cert-manager-webhook"},
						},
					},
				},
				"versions": []any{
					map[string]any{
						"name":    "v1",
						"served":  true,
						"storage": true,
						"schema": map[string]any{
							"openAPIV3Schema": map[string]any{
								"description": "A 50KB blob describing every property of a Certificate...",
								"properties": map[string]any{
									"spec": map[string]any{"type": "object"},
								},
							},
						},
						"additionalPrinterColumns": []any{
							map[string]any{"name": "Ready", "jsonPath": ".status.conditions[?(@.type=='Ready')].status"},
						},
					},
					map[string]any{
						"name":   "v1alpha1",
						"served": false,
						"schema": map[string]any{"openAPIV3Schema": map[string]any{}},
					},
				},
			},
		},
	}

	out, err := DropUnstructuredManagedFields(u)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	got := out.(*unstructured.Unstructured)

	// managedFields gone
	if _, found, _ := unstructured.NestedSlice(got.Object, "metadata", "managedFields"); found {
		t.Error("managedFields should be stripped on CRDs")
	}

	// Conversion gone (caBundle lives inside — would leak into cache otherwise)
	if _, found, _ := unstructured.NestedMap(got.Object, "spec", "conversion"); found {
		t.Error("spec.conversion should be stripped on CRDs")
	}

	// Versions still there but schema stripped from each
	versions, found, _ := unstructured.NestedSlice(got.Object, "spec", "versions")
	if !found {
		t.Fatal("spec.versions should be preserved (list-view column hint)")
	}
	if len(versions) != 2 {
		t.Errorf("expected 2 versions, got %d", len(versions))
	}
	for i, v := range versions {
		vm := v.(map[string]any)
		if _, ok := vm["schema"]; ok {
			t.Errorf("versions[%d].schema should be stripped, got %v", i, vm["schema"])
		}
	}

	// Identity fields preserved — these are what fleet aggregation and
	// Radar's own resource list rely on.
	v0 := versions[0].(map[string]any)
	if v0["name"] != "v1" {
		t.Errorf("versions[0].name should be preserved, got %v", v0["name"])
	}
	if v0["served"] != true {
		t.Errorf("versions[0].served should be preserved, got %v", v0["served"])
	}
	if v0["storage"] != true {
		t.Errorf("versions[0].storage should be preserved, got %v", v0["storage"])
	}
	if _, ok := v0["additionalPrinterColumns"]; !ok {
		t.Error("versions[0].additionalPrinterColumns should be preserved (drives list-view columns)")
	}

	group, _, _ := unstructured.NestedString(got.Object, "spec", "group")
	if group != "cert-manager.io" {
		t.Errorf("spec.group should be preserved, got %q", group)
	}
	names, _, _ := unstructured.NestedMap(got.Object, "spec", "names")
	if names["kind"] != "Certificate" {
		t.Errorf("spec.names should be preserved, got %v", names)
	}
	scope, _, _ := unstructured.NestedString(got.Object, "spec", "scope")
	if scope != "Namespaced" {
		t.Errorf("spec.scope should be preserved, got %q", scope)
	}
}

func TestDropUnstructuredManagedFields_NonUnstructuredInput(t *testing.T) {
	// Defensive: transform should be a no-op (not error) on unexpected input.
	// A transform error is fatal for the informer — we'd rather leak a
	// typed object than halt a watch.
	type foo struct{ Name string }
	in := &foo{Name: "x"}

	out, err := DropUnstructuredManagedFields(in)
	if err != nil {
		t.Fatalf("should not error on unexpected input type: %v", err)
	}
	if out != in {
		t.Error("should return input unchanged")
	}
}

func TestDropUnstructuredManagedFields_CRDWithoutVersions(t *testing.T) {
	// Edge: a minimal CRD object (e.g. mid-reconcile) might have no
	// spec.versions yet. Transform must not panic.
	u := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "apiextensions.k8s.io/v1",
			"kind":       "CustomResourceDefinition",
			"metadata":   map[string]any{"name": "brand-new.example.com"},
			"spec":       map[string]any{"group": "example.com"},
		},
	}

	out, err := DropUnstructuredManagedFields(u)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if out == nil {
		t.Fatal("expected unchanged object, got nil")
	}
}
