package upgrade

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/upgradereadiness"
)

func TestRunUpgradeReadinessWithoutCachePreservesCollectedEvidence(t *testing.T) {
	apiServices := []*unstructured.Unstructured{}
	results, err := RunFromCache(nil, []string{}, Options{
		CurrentVersion:        "1.34",
		TargetVersion:         "1.35",
		DeprecatedAPIRequests: make([]upgradereadiness.DeprecatedAPIRequest, 0),
		APIServices:           apiServices,
	})
	if err != nil {
		t.Fatalf("RunFromCache() error = %v", err)
	}
	for _, check := range results.Checks {
		if check.ID == "deprecated-api-requests" {
			if check.Status != upgradereadiness.CheckPassed {
				t.Fatalf("deprecated API check status = %q, want %q", check.Status, upgradereadiness.CheckPassed)
			}
			return
		}
	}
	t.Fatal("deprecated API check not found")
}

func TestRunUpgradeReadinessWithoutCachePreservesAPIServiceEvidence(t *testing.T) {
	apiService := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiregistration.k8s.io/v1",
		"kind":       "APIService",
		"metadata":   map[string]any{"name": "v1.example.io"},
		"spec":       map[string]any{"service": map[string]any{"namespace": "default", "name": "example"}},
		"status":     map[string]any{"conditions": []any{map[string]any{"type": "Available", "status": "True"}}},
	}}
	results, err := RunFromCache(nil, nil, Options{
		CurrentVersion: "1.35",
		TargetVersion:  "1.36",
		APIServices:    []*unstructured.Unstructured{apiService},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range results.Checks {
		if check.ID == "aggregated-apiservice-readiness" {
			if check.Status != upgradereadiness.CheckPassed || check.Inspected != 1 {
				t.Fatalf("APIService check = %+v, want one passed inspection", check)
			}
			return
		}
	}
	t.Fatal("APIService check not found")
}

func TestRunUpgradeReadinessWithCachePreservesDirectSourceEvidence(t *testing.T) {
	if err := k8s.InitTestResourceCache(fake.NewSimpleClientset()); err != nil {
		t.Fatalf("InitTestResourceCache() error = %v", err)
	}
	t.Cleanup(k8s.ResetTestState)
	lastApplied := `{"apiVersion":"networking.k8s.io/v1beta1","kind":"Ingress","metadata":{"name":"web","namespace":"default"}}`
	source := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "Ingress",
		"metadata": map[string]any{"name": "web", "namespace": "default", "annotations": map[string]any{
			"kubectl.kubernetes.io/last-applied-configuration": lastApplied,
		}},
	}}

	results, err := RunFromCache(k8s.GetResourceCache(), nil, Options{
		CurrentVersion: "1.24",
		TargetVersion:  "1.25",
		SourceObjects:  []metav1.Object{source},
	})
	if err != nil {
		t.Fatalf("RunFromCache() error = %v", err)
	}
	for _, check := range results.Checks {
		if check.ID == "manifest-api-compatibility" {
			if check.Status != upgradereadiness.CheckBlocked || len(check.Findings) != 1 || check.Findings[0].Resource.Kind != "Ingress" {
				t.Fatalf("source manifest check = %+v, want one removed Ingress API blocker", check)
			}
			return
		}
	}
	t.Fatal("source manifest API check not found")
}

func TestFilterPersistentVolumesForNamespaces(t *testing.T) {
	namespaced := func(name, namespace string) *corev1.PersistentVolume {
		return &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: corev1.PersistentVolumeSpec{
				ClaimRef: &corev1.ObjectReference{Namespace: namespace, Name: "claim"},
			},
		}
	}
	a := namespaced("a", "team-a")
	b := namespaced("b", "team-b")
	unbound := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "unbound"}}
	volumes := []*corev1.PersistentVolume{a, b, unbound}

	all := filterPersistentVolumesForNamespaces(volumes, nil)
	if len(all) != 3 {
		t.Fatalf("cluster-wide volume count = %d, want 3", len(all))
	}

	teamA := filterPersistentVolumesForNamespaces(volumes, []string{"team-a"})
	if len(teamA) != 1 || teamA[0].Name != "a" {
		t.Fatalf("team-a volumes = %#v, want only a", teamA)
	}

	none := filterPersistentVolumesForNamespaces(volumes, []string{})
	if len(none) != 0 {
		t.Fatalf("empty namespace scope returned %d volumes, want 0", len(none))
	}
}

func TestRunUpgradeReadinessDoesNotTreatTargetedWebhookServicesAsFullServiceCoverage(t *testing.T) {
	results, err := RunFromCache(nil, nil, Options{
		CurrentVersion: "1.35",
		TargetVersion:  "1.36",
		WebhookServices: []*corev1.Service{{
			ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "policy-system"},
			Spec:       corev1.ServiceSpec{ExternalIPs: []string{"192.0.2.1"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range results.Checks {
		if check.ID == "service-externalips-deprecated" {
			if check.Status != upgradereadiness.CheckUnknown || len(check.Findings) != 0 {
				t.Fatalf("Service check = %+v, want unavailable full-Service evidence", check)
			}
			return
		}
	}
	t.Fatal("Service externalIPs check not found")
}

func TestSameNamespaceSet(t *testing.T) {
	if !sameNamespaceSet([]string{"team-b", "team-a"}, []string{"team-a", "team-b"}) {
		t.Fatal("equal namespace sets should ignore ordering")
	}
	if sameNamespaceSet(nil, []string{}) || sameNamespaceSet([]string{"team-a"}, []string{"team-b"}) {
		t.Fatal("cluster-wide, empty, and distinct scopes must remain different")
	}
}
