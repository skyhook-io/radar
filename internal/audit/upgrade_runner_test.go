package audit

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/skyhook-io/radar/pkg/upgradereadiness"
)

func TestRunUpgradeReadinessWithoutCachePreservesCollectedEvidence(t *testing.T) {
	results, err := RunUpgradeReadinessFromCache(nil, []string{}, UpgradeReadinessOptions{
		CurrentVersion:        "1.34",
		TargetVersion:         "1.35",
		DeprecatedAPIRequests: make([]upgradereadiness.DeprecatedAPIRequest, 0),
	})
	if err != nil {
		t.Fatalf("RunUpgradeReadinessFromCache() error = %v", err)
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
