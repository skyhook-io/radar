package k8score

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

func newDiscoveryWith(t *testing.T, resources ...APIResource) *ResourceDiscovery {
	t.Helper()
	d := &ResourceDiscovery{
		resourceMap: make(map[string]APIResource),
		gvrMap:      make(map[string]schema.GroupVersionResource),
	}
	for _, r := range resources {
		d.AddAPIResource(r)
	}
	return d
}

func legacyClusterPolicy() APIResource {
	return APIResource{
		Group: "kyverno.io", Version: "v1", Kind: "ClusterPolicy",
		Name: "clusterpolicies", IsCRD: true, Verbs: []string{"get", "list", "watch"},
	}
}

func modernValidatingPolicy() APIResource {
	return APIResource{
		Group: "policies.kyverno.io", Version: "v1", Kind: "ValidatingPolicy",
		Name: "validatingpolicies", IsCRD: true, Verbs: []string{"get", "list", "watch"},
	}
}

// The regression this whole ticket exists for: a cluster that has migrated
// to (or was installed fresh on) the modern policies.kyverno.io family must
// still be detected as running Kyverno. Reporting not-installed here makes
// WarmupKyvernoPolicyReports skip the PolicyReport index entirely, so every
// policy finding silently disappears from Radar even though the reports are
// sitting in the cluster.
func TestIsKyvernoInstalledDetectsModernOnlyInstall(t *testing.T) {
	d := newDiscoveryWith(t, modernValidatingPolicy())

	if !d.IsKyvernoInstalled() {
		t.Fatal("modern-only Kyverno install reported as not installed; the PolicyReport index would be dropped")
	}
}

// Any kind in policies.kyverno.io is sufficient — the group belongs to
// Kyverno alone, so detection must not hinge on one nominated sentinel kind
// being present.
func TestIsKyvernoInstalledDetectsModernFamilyViaAnyKind(t *testing.T) {
	for _, kind := range []struct{ kind, plural string }{
		{"MutatingPolicy", "mutatingpolicies"},
		{"ImageValidatingPolicy", "imagevalidatingpolicies"},
		{"DeletingPolicy", "deletingpolicies"},
		{"NamespacedValidatingPolicy", "namespacedvalidatingpolicies"},
	} {
		t.Run(kind.kind, func(t *testing.T) {
			d := newDiscoveryWith(t, APIResource{
				Group: "policies.kyverno.io", Version: "v1", Kind: kind.kind,
				Name: kind.plural, IsCRD: true, Verbs: []string{"get", "list", "watch"},
			})
			if !d.IsKyvernoInstalled() {
				t.Fatalf("%s alone did not signal Kyverno installed", kind.kind)
			}
		})
	}
}

func TestIsKyvernoInstalledStillDetectsLegacyOnlyInstall(t *testing.T) {
	d := newDiscoveryWith(t, legacyClusterPolicy())

	if !d.IsKyvernoInstalled() {
		t.Fatal("legacy-only Kyverno install reported as not installed")
	}
}

func TestIsKyvernoInstalledDetectsBothFamiliesDuringMigration(t *testing.T) {
	d := newDiscoveryWith(t, legacyClusterPolicy(), modernValidatingPolicy())

	if !d.IsKyvernoInstalled() {
		t.Fatal("cluster serving both Kyverno API families reported as not installed")
	}
}

// Detection keys on Kyverno's own policy CRDs, never on the report CRDs —
// wgpolicyk8s.io is written by Trivy, Falco adapters and others, so a
// Trivy-only cluster must not be mistaken for a Kyverno one.
func TestIsKyvernoInstalledIgnoresReportCRDsAlone(t *testing.T) {
	d := newDiscoveryWith(t,
		APIResource{
			Group: "wgpolicyk8s.io", Version: "v1alpha2", Kind: "PolicyReport",
			Name: "policyreports", IsCRD: true, Verbs: []string{"get", "list", "watch"},
		},
		APIResource{
			Group: "aquasecurity.github.io", Version: "v1alpha1", Kind: "VulnerabilityReport",
			Name: "vulnerabilityreports", IsCRD: true, Verbs: []string{"get", "list", "watch"},
		},
	)

	if d.IsKyvernoInstalled() {
		t.Fatal("PolicyReport CRDs alone must not imply Kyverno is installed")
	}
}

func TestIsKyvernoInstalledFalseOnCleanCluster(t *testing.T) {
	d := newDiscoveryWith(t, APIResource{
		Version: "v1", Kind: "Pod", Name: "pods", Verbs: []string{"get", "list", "watch"},
	})

	if d.IsKyvernoInstalled() {
		t.Fatal("cluster with no Kyverno CRDs reported as installed")
	}
}

func TestIsKyvernoInstalledNilSafe(t *testing.T) {
	var d *ResourceDiscovery
	if d.IsKyvernoInstalled() {
		t.Fatal("nil discovery must report not installed")
	}
}

func TestHasGroup(t *testing.T) {
	d := newDiscoveryWith(t, modernValidatingPolicy())

	if !d.HasGroup("policies.kyverno.io") {
		t.Error("HasGroup missed a registered group")
	}
	// Must be exact: policies.kyverno.io is a distinct group from
	// kyverno.io, not a version of it.
	if d.HasGroup("kyverno.io") {
		t.Error("HasGroup matched a group that is only a suffix of a registered one")
	}
	var nilD *ResourceDiscovery
	if nilD.HasGroup("policies.kyverno.io") {
		t.Error("nil discovery must report no groups")
	}
}
