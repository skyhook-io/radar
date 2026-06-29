package k8score

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// DropUnstructuredManagedFields is the SharedInformer transform used by
// the dynamic cache. The tests below pin its two invariants:
//   1. For any unstructured object, managedFields are gone but the
//      last-applied-configuration annotation is preserved (GitOps drift
//      detection depends on it — see pkg/gitops/insights/drift.go).
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

	// last-applied-configuration is stripped from the cache. GitOps drift
	// detection still works because the insights resolver pulls live objects
	// via a dedicated direct-GET path that preserves the annotation — keeping
	// it off the informer cache so we don't retain it cluster-wide for core
	// kinds (apps/Deployment, /v1/Service, …) just to power a per-page diff.
	annotations := got.GetAnnotations()
	if _, present := annotations["kubectl.kubernetes.io/last-applied-configuration"]; present {
		t.Errorf("last-applied-configuration should be stripped from cache; got %v", annotations)
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
							"service":  map[string]any{"name": "cert-manager-webhook"},
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

func TestStripUnstructuredFields_StripsLastAppliedForResponses(t *testing.T) {
	u := &unstructured.Unstructured{
		Object: map[string]any{
			"metadata": map[string]any{
				"managedFields": []any{map[string]any{"manager": "kubectl"}},
				"annotations": map[string]any{
					"kubectl.kubernetes.io/last-applied-configuration": "{desired manifest}",
					"description": "keep me",
				},
			},
		},
	}
	got := StripUnstructuredFields(u)
	if _, found, _ := unstructured.NestedSlice(got.Object, "metadata", "managedFields"); found {
		t.Fatal("managedFields should be stripped")
	}
	annotations := got.GetAnnotations()
	if _, ok := annotations["kubectl.kubernetes.io/last-applied-configuration"]; ok {
		t.Fatalf("last-applied should be stripped from outward responses, got %v", annotations)
	}
	if annotations["description"] != "keep me" {
		t.Fatalf("other annotations should be preserved, got %v", annotations)
	}
}

func TestStripUnstructuredFieldsPreserveLastApplied_ForDrift(t *testing.T) {
	u := &unstructured.Unstructured{
		Object: map[string]any{
			"metadata": map[string]any{
				"managedFields": []any{map[string]any{"manager": "kubectl"}},
				"annotations": map[string]any{
					"kubectl.kubernetes.io/last-applied-configuration": "{desired manifest}",
				},
			},
		},
	}
	got := StripUnstructuredFieldsPreserveLastApplied(u)
	if _, found, _ := unstructured.NestedSlice(got.Object, "metadata", "managedFields"); found {
		t.Fatal("managedFields should be stripped")
	}
	if got.GetAnnotations()["kubectl.kubernetes.io/last-applied-configuration"] != "{desired manifest}" {
		t.Fatalf("last-applied should be preserved for drift, got %v", got.GetAnnotations())
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

// TestDropManagedFields_TypedAsymmetricAnnotationHandling pins the intentional
// asymmetry between DropManagedFields (typed cache transform) and
// DropUnstructuredManagedFields (dynamic cache transform). The dynamic path
// preserves kubectl.kubernetes.io/last-applied-configuration for GitOps drift
// detection; the typed path strips it because typed-cache objects are not
// the path drift detection reads from.
//
// Both transforms remove managedFields. The mismatch is intentional. A
// "consistency cleanup" PR that aligned them in either direction would
// silently break one feature (drift) or regress memory (the strip exists
// for a reason); this test documents the contract so the next maintainer
// can see what's load-bearing.
func TestDropManagedFields_TypedStripsLastAppliedConfig(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "x",
			Namespace: "default",
			Annotations: map[string]string{
				"kubectl.kubernetes.io/last-applied-configuration": "{blob}",
				"description": "keep me",
			},
			ManagedFields: []metav1.ManagedFieldsEntry{{Manager: "kubectl"}},
		},
	}
	out, err := DropManagedFields(dep)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	got := out.(*appsv1.Deployment)
	if len(got.ManagedFields) != 0 {
		t.Errorf("managedFields should be stripped, got %d entries", len(got.ManagedFields))
	}
	if _, present := got.Annotations["kubectl.kubernetes.io/last-applied-configuration"]; present {
		t.Errorf("typed path must strip last-applied-configuration (asymmetric with unstructured path); got %v", got.Annotations)
	}
	if got.Annotations["description"] != "keep me" {
		t.Errorf("other annotations should survive, got %v", got.Annotations)
	}
}

// Sanity check the asymmetry rule extends to other typed kinds: corev1.Pod is
// in DropManagedFields' explicit kind list. If the switch ever loses Pod,
// this test breaks.
func TestDropManagedFields_TypedStripsLastAppliedFromPod(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "x", Namespace: "default",
			Annotations: map[string]string{
				"kubectl.kubernetes.io/last-applied-configuration": "{blob}",
			},
		},
	}
	out, _ := DropManagedFields(pod)
	if _, present := out.(*corev1.Pod).Annotations["kubectl.kubernetes.io/last-applied-configuration"]; present {
		t.Errorf("Pod should also have last-applied stripped")
	}
}
