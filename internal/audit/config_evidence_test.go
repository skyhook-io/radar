package audit

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

type partialConfigDiscovery struct {
	discovery.DiscoveryInterface
	failedGroup string
}

func (d partialConfigDiscovery) ServerGroupsAndResources() ([]*metav1.APIGroup, []*metav1.APIResourceList, error) {
	return nil, []*metav1.APIResourceList{{GroupVersion: "v1", APIResources: []metav1.APIResource{{Name: "pods", Kind: "Pod", Namespaced: true}}}}, &discovery.ErrGroupDiscoveryFailed{Groups: map[schema.GroupVersion]error{{Group: d.failedGroup, Version: "v1"}: errors.New("unavailable")}}
}

func TestCollectedOrphanEvidenceScopeAndPermissions(t *testing.T) {
	source := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "source", Namespace: "app", Annotations: map[string]string{"reflector.v1.k8s.emberstack.com/reflection-allowed": "true"}}}
	mirror := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "mirror", Namespace: "edge", Annotations: map[string]string{"reflector.v1.k8s.emberstack.com/reflects": "app/source"}}}
	orphan := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "orphan", Namespace: "app"}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "consumer", Namespace: "edge"}, Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "tls", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "mirror"}}}}}}
	client := fake.NewClientset(source, mirror, orphan, pod)
	scopes := map[string]k8score.ResourceScope{}
	for _, kind := range append(append([]string{}, typedConfigConsumers...), "secrets", "configmaps") {
		scopes[kind] = k8score.ResourceScope{Enabled: true}
	}
	if err := k8s.InitScopedTestResourceCache(client, scopes); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	if err := k8s.InitTestDynamicResourceCache(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), []k8s.APIResource{{Version: "v1", Name: "pods", Kind: "Pod", Namespaced: true}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	cache := k8s.GetResourceCache()
	input := CollectTypedInput(cache, []string{"app"})
	if !collectConfigEvidence(cache, input, []string{"app"}, nil).ReflectionsComplete["Secret"] {
		t.Fatal("cluster-wide reflection evidence incomplete")
	}
	results := RunFromCache(cache, []string{"app"}, nil)
	seen := map[string]bool{}
	for _, f := range results.Findings {
		if f.CheckID == "orphanConfigMapSecret" {
			seen[f.Name] = true
		}
	}
	if !seen["orphan"] || seen["source"] || seen["mirror"] {
		t.Fatalf("view filter changed evidence: %v", seen)
	}
	limited := &ReadScope{Namespaces: []string{"app"}, SecretNamespaces: []string{"app"}}
	results = RunFromCache(cache, []string{"app"}, &RunOptions{Scope: limited})
	for _, f := range results.Findings {
		if f.CheckID == "orphanConfigMapSecret" && f.Name == "source" {
			t.Fatal("hidden mirror became orphan source")
		}
	}
	if !slices.Contains(results.MissingInputs, "secret-references") {
		t.Fatal("missing reflection coverage")
	}
	denied := &ReadScope{Namespaces: []string{"app"}, SecretNamespaces: []string{}}
	results = RunFromCache(cache, []string{"app"}, &RunOptions{Scope: denied})
	for _, f := range results.Findings {
		if f.Kind == "Secret" {
			t.Fatal("denied Secret subject exposed")
		}
	}
	if results.CheckCounts["orphanConfigMapSecret"].Evaluated != 0 {
		t.Fatal("denied Secrets counted")
	}
}

func TestInstalledUnwatchedCertificateIsIncomplete(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
	if err := k8s.InitTestResourceCache(fake.NewClientset()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "CertificateList"})
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: "Certificate", Namespaced: true, Verbs: []string{"list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	cache := k8s.GetResourceCache()
	if configReferencesComplete(cache, "Secret", "app", nil, discoverConfigDependencies(k8s.GetResourceDiscovery())) {
		t.Fatal("installed but unwatched Certificate considered absent")
	}
	dynamic := k8s.GetDynamicResourceCache()
	if err := dynamic.EnsureWatching(gvr); err != nil {
		t.Fatal(err)
	}
	if !dynamic.WaitForSync(gvr, 2*time.Second) {
		t.Fatal("sync timeout")
	}
	if !configReferencesComplete(cache, "Secret", "app", nil, discoverConfigDependencies(k8s.GetResourceDiscovery())) {
		t.Fatal("synced Certificate inventory remains incomplete")
	}
}

func TestTypedCoverageDoesNotBorrowNamespace(t *testing.T) {
	scopes := map[string]k8score.ResourceScope{}
	for _, kind := range append(append([]string{}, typedConfigConsumers...), "secrets", "configmaps") {
		scopes[kind] = k8score.ResourceScope{Enabled: true, Namespace: "app"}
	}
	if err := k8s.InitScopedTestResourceCache(fake.NewClientset(), scopes); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	cache := k8s.GetResourceCache()
	if !typedConfigCoverage(cache, "pods", "app") || typedConfigCoverage(cache, "pods", "edge") || typedConfigCoverage(cache, "pods", "") {
		t.Fatal("wrong per-namespace authority")
	}
}

func TestDependencyDiscoveryIgnoresUnrelatedFailures(t *testing.T) {
	for _, tc := range []struct {
		group      string
		cm, secret bool
	}{
		{"metrics.k8s.io", true, true}, {"cert-manager.io", true, false}, {"external-secrets.io", false, false},
	} {
		t.Run(tc.group, func(t *testing.T) {
			disc, err := k8score.NewResourceDiscovery(partialConfigDiscovery{failedGroup: tc.group})
			if err != nil {
				t.Fatal(err)
			}
			deps := discoverConfigDependencies(&k8s.ResourceDiscovery{ResourceDiscovery: disc})
			if deps.complete["ConfigMap"] != tc.cm || deps.complete["Secret"] != tc.secret {
				t.Fatalf("coverage: %v", deps.complete)
			}
		})
	}
}

func TestSecretOnlyDependenciesDoNotBlockConfigMaps(t *testing.T) {
	if err := k8s.InitTestResourceCache(fake.NewClientset()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	deps := configDependencies{complete: map[string]bool{"ConfigMap": true, "Secret": true}, resources: []k8s.APIResource{
		{Group: "gateway.networking.k8s.io", Version: "v1", Name: "gateways", Namespaced: true},
	}}
	scope := &ReadScope{Namespaces: []string{"app"}, SecretNamespaces: []string{"app"}}
	cache := k8s.GetResourceCache()
	if !configReferencesComplete(cache, "ConfigMap", "app", scope, deps) {
		t.Fatal("Secret-only Gateway blocks ConfigMaps")
	}
	if configReferencesComplete(cache, "Secret", "app", scope, deps) {
		t.Fatal("unreadable cross-namespace Gateway ignored")
	}
	deps.resources = []k8s.APIResource{{Group: "cert-manager.io", Version: "v1", Name: "clusterissuers"}}
	deps.clusterIssuerNamespaces = []string{"cert-manager"}
	if !configReferencesComplete(cache, "Secret", "app", scope, deps) {
		t.Fatal("known ClusterIssuer domain blocks unrelated namespace")
	}
	deps.clusterIssuerNamespaces = nil
	if configReferencesComplete(cache, "Secret", "app", scope, deps) {
		t.Fatal("unknown ClusterIssuer domain treated as absent")
	}
}

func TestClusterIssuerUnknownNamespaceCannotProveOrphan(t *testing.T) {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "issuer-key", Namespace: "custom-certs"}}
	controller := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "cert-manager", Namespace: "controllers", Labels: map[string]string{"app.kubernetes.io/name": "cert-manager"}}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "cert-manager", Args: []string{"--cluster-resource-namespace=custom-certs"}}}}}}}
	if err := k8s.InitTestResourceCache(fake.NewClientset(secret, controller)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	gvr := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "clusterissuers"}
	issuer := testUnstructured("cert-manager.io/v1", "ClusterIssuer", "", "issuer", map[string]any{"spec": map[string]any{"acme": map[string]any{"privateKeySecretRef": map[string]any{"name": "issuer-key"}}}})
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "ClusterIssuerList"}, issuer)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: "ClusterIssuer", Verbs: []string{"list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	dynamic := k8s.GetDynamicResourceCache()
	if err := dynamic.EnsureWatching(gvr); err != nil {
		t.Fatal(err)
	}
	if !dynamic.WaitForSync(gvr, 2*time.Second) {
		t.Fatal("sync timeout")
	}
	cache := k8s.GetResourceCache()
	for _, restricted := range []bool{true, false} {
		var opts *RunOptions
		if restricted {
			opts = &RunOptions{Scope: &ReadScope{Namespaces: []string{"custom-certs"}, SecretNamespaces: []string{"custom-certs"}, ClusterResources: map[string]bool{gvr.GroupResource().String(): true}}}
		}
		result := RunFromCache(cache, []string{"custom-certs"}, opts)
		for _, f := range result.Findings {
			if f.CheckID == "orphanConfigMapSecret" {
				t.Fatalf("restricted=%v false orphan: %+v", restricted, f)
			}
		}
		count := result.CheckCounts["orphanConfigMapSecret"]
		if restricted && (count.Evaluated != 0 || !slices.Contains(result.MissingInputs, "secret-references")) {
			t.Fatalf("unknown namespace counted complete: %+v", result)
		}
		if !restricted && (count.Evaluated != 1 || count.Passed != 1) {
			t.Fatalf("known namespace did not prove use: %+v", count)
		}
	}
	if err := dyn.Resource(gvr).Delete(context.Background(), "issuer", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		items, err := dynamic.ListWatched(gvr)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("issuer deletion not observed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	scope := &ReadScope{Namespaces: []string{"custom-certs"}, SecretNamespaces: []string{"custom-certs"}, ClusterResources: map[string]bool{gvr.GroupResource().String(): true}}
	result := RunFromCache(cache, []string{"custom-certs"}, &RunOptions{Scope: scope})
	if result.CheckCounts["orphanConfigMapSecret"].Evaluated != 1 {
		t.Fatal("empty authorized ClusterIssuer inventory suppressed orphan check")
	}

}

func TestEmptyDiscoveryDoesNotProveNoConfigConsumers(t *testing.T) {
	disc, err := k8score.NewResourceDiscovery(fake.NewClientset().Discovery())
	if err != nil {
		t.Fatal(err)
	}
	deps := discoverConfigDependencies(&k8s.ResourceDiscovery{ResourceDiscovery: disc})
	if deps.complete["Secret"] || deps.complete["ConfigMap"] {
		t.Fatal("empty discovery considered complete")
	}
}
