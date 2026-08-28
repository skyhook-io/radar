package upgrade

import (
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
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

func TestRunUpgradeReadinessDoesNotExposeUnauthorizedCached137Evidence(t *testing.T) {
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "kubeadm-config", Namespace: "kube-system"},
		Data:       map[string]string{"ClusterConfiguration": "apiVersion: kubeadm.k8s.io/v1beta3\nkind: ClusterConfiguration\n"},
	}
	event := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "selinux-conflict", Namespace: "secret-team"},
		Reason:         "SELinuxLabelConflict",
		Message:        "sensitive volume conflict detail",
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "secret-team", Name: "private"},
	}
	if err := k8s.InitTestResourceCache(fake.NewSimpleClientset(configMap, event)); err != nil {
		t.Fatalf("InitTestResourceCache() error = %v", err)
	}
	t.Cleanup(k8s.ResetTestState)

	results, err := RunFromCache(k8s.GetResourceCache(), nil, Options{
		CurrentVersion:                  "1.36",
		TargetVersion:                   "1.37",
		ConfigMapNamespaces:             []string{},
		PersistentVolumeClaimNamespaces: []string{},
		EventNamespaces:                 []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range results.Checks {
		if check.ID == "kubeadm-config-v1beta3" && (check.Status != upgradereadiness.CheckUnknown || len(check.Findings) != 0) {
			t.Fatalf("unauthorized kubeadm evidence = %+v, want unknown without findings", check)
		}
		for _, finding := range check.Findings {
			if finding.Evidence.Source == "event" {
				t.Fatalf("unauthorized Event evidence leaked through %s: %+v", check.ID, finding)
			}
		}
	}
	for _, kind := range []string{"configmaps", "persistentvolumeclaims", "events"} {
		if !slices.Contains(results.Coverage.UnavailableKinds, kind) {
			t.Fatalf("unavailable kinds = %v, want %s", results.Coverage.UnavailableKinds, kind)
		}
	}
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

func TestIntersectNamespaceScopes(t *testing.T) {
	for _, tc := range []struct {
		name string
		a    []string
		b    []string
		want []string
	}{
		{name: "two cluster-wide scopes", want: nil},
		{name: "scan ceiling", a: []string{"team-a"}, want: []string{"team-a"}},
		{name: "authorization ceiling", b: []string{"team-b"}, want: []string{"team-b"}},
		{name: "intersection", a: []string{"team-a", "team-b"}, b: []string{"team-b", "team-c"}, want: []string{"team-b"}},
		{name: "explicit no access", a: []string{}, b: []string{"team-b"}, want: []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := intersectNamespaceScopes(tc.a, tc.b)
			if !sameNamespaceSet(got, tc.want) {
				t.Fatalf("intersection = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestUpgradeCacheScopeResourcesFollowCrossedReleases(t *testing.T) {
	for _, tc := range []struct {
		name           string
		current        string
		target         string
		wantEvents     bool
		wantConfigMaps bool
		wantPVCs       bool
	}{
		{name: "crosses 1.35", current: "1.34", target: "1.36", wantEvents: true},
		{name: "crosses only 1.36", current: "1.35", target: "1.36"},
		{name: "crosses 1.37", current: "1.36", target: "1.37", wantEvents: true, wantConfigMaps: true, wantPVCs: true},
		{name: "kube-proxy review persists after 1.37", current: "1.37", target: "1.38", wantConfigMaps: true},
		{name: "kube-proxy review ends at 1.40", current: "1.40", target: "1.41"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resources := upgradeCacheScopeResources(tc.current, tc.target)
			if got := slices.Contains(resources, string(k8score.Events)); got != tc.wantEvents {
				t.Fatalf("Events included = %t, want %t in %v", got, tc.wantEvents, resources)
			}
			if got := slices.Contains(resources, string(k8score.ConfigMaps)); got != tc.wantConfigMaps {
				t.Fatalf("ConfigMaps included = %t, want %t in %v", got, tc.wantConfigMaps, resources)
			}
			if got := slices.Contains(resources, string(k8score.PersistentVolumeClaims)); got != tc.wantPVCs {
				t.Fatalf("PersistentVolumeClaims included = %t, want %t in %v", got, tc.wantPVCs, resources)
			}
		})
	}
}

func TestRunUpgradeReadinessDoesNotAssertKubeadmAbsenceFromColdConfigMapCache(t *testing.T) {
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "kubeadm-config", Namespace: "kube-system"},
		Data:       map[string]string{"ClusterConfiguration": "apiVersion: kubeadm.k8s.io/v1beta3\nkind: ClusterConfiguration\n"},
	}
	client := fake.NewSimpleClientset(configMap)
	listStarted := make(chan struct{})
	releaseList := make(chan struct{})
	client.PrependReactor("list", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		select {
		case <-listStarted:
		default:
			close(listStarted)
		}
		<-releaseList
		return false, nil, nil
	})

	coreCache, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:        client,
		ResourceTypes: map[string]bool{string(k8score.ConfigMaps): true},
		DeferredTypes: map[string]bool{string(k8score.ConfigMaps): true},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(coreCache.Stop)
	t.Cleanup(func() { close(releaseList) })
	select {
	case <-listStarted:
	case <-time.After(time.Second):
		t.Fatal("ConfigMap informer did not start")
	}

	results, err := RunFromCache(&k8s.ResourceCache{ResourceCache: coreCache}, nil, Options{
		CurrentVersion:      "1.36",
		TargetVersion:       "1.37",
		ConfigMapNamespaces: nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range results.Checks {
		if check.ID == "kubeadm-config-v1beta3" {
			if check.Status != upgradereadiness.CheckUnknown || len(check.Findings) != 0 {
				t.Fatalf("cold ConfigMap evidence = %+v, want unknown without findings", check)
			}
			return
		}
	}
	t.Fatal("kubeadm configuration check not found")
}
