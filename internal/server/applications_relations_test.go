package server

import (
	"context"
	"reflect"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
)

type appCountingSession struct {
	relations.Session
	reads map[appCountedRead]int
}

type appCountedRead struct {
	kind          resourceid.GroupKind
	allNamespaces bool
}

func (s *appCountingSession) Indexed(kind resourceid.GroupKind, index string, keys []string, scope ownership.SearchScope) relations.IndexedRead {
	s.reads[appCountedRead{kind, scope.AllNamespaces}]++
	return s.Session.Indexed(kind, index, keys, scope)
}

func addApplicationTypedDiscovery() {
	for _, kl := range k8score.AllKindListers() {
		if b, ok := resourceid.BuiltinForKind(kl.Kind()); ok {
			k8s.GetResourceDiscovery().AddAPIResource(k8s.APIResource{Group: b.Group, Version: b.Version, Kind: b.Kind, Name: b.Resource, Namespaced: b.Namespaced, Verbs: []string{"get", "list", "watch"}})
		}
	}
}

func TestApplicationsBatchAttachmentParity(t *testing.T) {
	labels := map[string]string{"app": "api"}
	meta := func(name string) metav1.ObjectMeta { return metav1.ObjectMeta{Namespace: "prod", Name: name} }
	deployment := &appsv1.Deployment{ObjectMeta: meta("api"), Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{Volumes: []corev1.Volume{
		{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "settings"}}}},
		{Name: "credentials", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "sealed"}}},
		{Name: "storage", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}}},
	}}}}}
	service := &corev1.Service{ObjectMeta: meta("api"), Spec: corev1.ServiceSpec{Selector: labels}}
	ingress := &networkingv1.Ingress{ObjectMeta: meta("edge"), Spec: networkingv1.IngressSpec{DefaultBackend: &networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "api", Port: networkingv1.ServiceBackendPort{Number: 80}}}}}
	hpa := &autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: meta("scale"), Spec: autoscalingv2.HorizontalPodAutoscalerSpec{ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "api"}}}
	pdb := &policyv1.PodDisruptionBudget{ObjectMeta: meta("budget"), Spec: policyv1.PodDisruptionBudgetSpec{Selector: &metav1.LabelSelector{MatchLabels: labels}}}
	np := &networkingv1.NetworkPolicy{ObjectMeta: meta("network"), Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: labels}}}
	// istiod outside the request's namespaces still sets the mesh domain.
	istiod := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "istio-system", Name: "istiod", Labels: map[string]string{"app": "istiod"}}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "discovery", Args: []string{"discovery", "--domain=mesh.example"}}}}}}}
	useTestResourceCache(t, fake.NewClientset(append(testNamespaces("prod", "istio-system"), deployment, istiod, service, ingress, hpa, pdb, np, &networkingv1.NetworkPolicy{ObjectMeta: meta("all-pods")}, &corev1.ConfigMap{ObjectMeta: meta("settings")}, &corev1.PersistentVolumeClaim{ObjectMeta: meta("data")})...))
	route := func(kind, apiVersion, name string, spec map[string]any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": map[string]any{"namespace": "prod", "name": name}, "spec": spec}}
	}
	routes := []runtime.Object{
		route("CiliumNetworkPolicy", "cilium.io/v2", "cilium", map[string]any{"endpointSelector": map[string]any{"matchLabels": map[string]any{"app": "api"}, "matchExpressions": []any{map[string]any{"key": "tier", "operator": "In", "values": []any{"backend"}}}}}),
		route("SealedSecret", "bitnami.com/v1alpha1", "sealed", map[string]any{}),
		route("Gateway", "gateway.networking.k8s.io/v1", "gateway", map[string]any{}),
		route("HTTPRoute", "gateway.networking.k8s.io/v1", "http", map[string]any{"parentRefs": []any{map[string]any{"name": "gateway"}}, "rules": []any{map[string]any{"backendRefs": []any{map[string]any{"name": "api", "port": int64(80)}}}}}),
		route("IngressRoute", "traefik.io/v1alpha1", "traefik", map[string]any{"routes": []any{map[string]any{"services": []any{map[string]any{"name": "api", "port": int64(80)}}}}}),
		route("VirtualService", "networking.istio.io/v1", "istio", map[string]any{"http": []any{map[string]any{"route": []any{map[string]any{"destination": map[string]any{"host": "api"}}}}}}),
		route("VirtualService", "networking.istio.io/v1", "istio-mesh", map[string]any{"http": []any{map[string]any{"route": []any{map[string]any{"destination": map[string]any{"host": "api.prod.svc.mesh.example"}}}}}}),
		route("HTTPProxy", "projectcontour.io/v1", "contour", map[string]any{"routes": []any{map[string]any{"services": []any{map[string]any{"name": "api", "port": int64(80)}}}}}),
	}
	var resources []k8s.APIResource
	kinds := map[schema.GroupVersionResource]string{}
	for _, obj := range routes {
		u := obj.(*unstructured.Unstructured)
		gv := u.GroupVersionKind().GroupVersion()
		plural := map[string]string{"CiliumNetworkPolicy": "ciliumnetworkpolicies", "SealedSecret": "sealedsecrets", "Gateway": "gateways", "HTTPRoute": "httproutes", "IngressRoute": "ingressroutes", "VirtualService": "virtualservices", "HTTPProxy": "httpproxies"}[u.GetKind()]
		gvr := gv.WithResource(plural)
		kinds[gvr] = u.GetKind() + "List"
		resources = append(resources, k8s.APIResource{Group: gv.Group, Version: gv.Version, Name: plural, Kind: u.GetKind(), Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}})
	}
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), kinds, routes...)
	// The fake tracker guesses "gatewaies" from Gateway; use the served plural.
	if err := dynamicClient.Tracker().Create(schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"}, routes[2], "prod"); err != nil {
		t.Fatal(err)
	}
	if err := k8s.InitTestDynamicResourceCache(dynamicClient, resources); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	addApplicationTypedDiscovery()
	for gvr := range kinds {
		dc := k8s.GetDynamicResourceCache()
		if err := dc.EnsureWatching(gvr); err != nil || !dc.WaitForSync(gvr, 2*time.Second) {
			t.Fatalf("watch %v: %v", gvr, err)
		}
	}
	borrowed := map[*unstructured.Unstructured]*unstructured.Unstructured{}
	for gvr := range kinds {
		items, err := k8s.GetDynamicResourceCache().ListReadOnlyContext(context.Background(), gvr, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range items {
			borrowed[item] = item.DeepCopy()
		}
	}
	session, err := k8s.BeginOwnershipRead(k8s.GetResourceCache())
	if err != nil {
		t.Fatal(err)
	}
	counted := &appCountingSession{Session: session.(relations.Session), reads: map[appCountedRead]int{}}
	batch := buildAppRelationBatch(context.Background(), counted, []string{"prod"})
	for kind, count := range counted.reads {
		if count != 1 {
			t.Fatalf("batch scanned %+v %d times", kind, count)
		}
	}
	ref := func(group, kind, name string) topology.ResourceRef {
		return topology.ResourceRef{Group: group, Kind: kind, Namespace: "prod", Name: name}
	}
	want := &appRelationships{
		Services: []string{"api"}, Ingresses: []string{"edge"}, Routes: []string{"HTTPProxy/contour", "HTTPRoute/http", "IngressRoute/traefik", "VirtualService/istio", "VirtualService/istio-mesh"},
		Configs: 2, Scalers: 1, Storage: 1, PDBs: 1, NetworkPolicies: 2,
		serviceRefs:       refsByKey([]topology.ResourceRef{ref("", "Service", "api")}),
		ingressRefs:       refsByKey([]topology.ResourceRef{ref("networking.k8s.io", "Ingress", "edge")}),
		routeRefs:         refsByKey([]topology.ResourceRef{ref("gateway.networking.k8s.io", "HTTPRoute", "http"), ref("traefik.io", "IngressRoute", "traefik"), ref("networking.istio.io", "VirtualService", "istio"), ref("networking.istio.io", "VirtualService", "istio-mesh"), ref("projectcontour.io", "HTTPProxy", "contour")}),
		configRefs:        refsByKey([]topology.ResourceRef{ref("", "ConfigMap", "settings"), ref("bitnami.com", "SealedSecret", "sealed")}),
		scalerRefs:        refsByKey([]topology.ResourceRef{ref("autoscaling", "HorizontalPodAutoscaler", "scale")}),
		storageRefs:       refsByKey([]topology.ResourceRef{ref("", "PersistentVolumeClaim", "data")}),
		pdbRefs:           refsByKey([]topology.ResourceRef{ref("policy", "PodDisruptionBudget", "budget")}),
		networkPolicyRefs: refsByKey([]topology.ResourceRef{ref("networking.k8s.io", "NetworkPolicy", "network"), ref("cilium.io", "CiliumNetworkPolicy", "cilium")}),
	}
	got := batch.relationshipsForRef("apps", "Deployment", "prod", "api")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attachments changed: got=%#v want=%#v", got, want)
	}
	// No row is a Gateway or a route, so the batch evaluates nothing for them.
	if got := batch.relationshipsForRef("gateway.networking.k8s.io", "Gateway", "prod", "gateway"); got != nil {
		t.Fatalf("Gateway attachments=%#v want nil", got)
	}
	if got := batch.relationshipsForRef("gateway.networking.k8s.io", "HTTPRoute", "prod", "http"); got != nil {
		t.Fatalf("HTTPRoute attachments=%#v want nil", got)
	}

	clearApplicationsCache()
	t.Cleanup(clearApplicationsCache)
	if _, err := listApplicationsForRequest(context.Background(), []string{"prod"}, false); err != nil {
		t.Fatal(err)
	}
	for item, before := range borrowed {
		if !reflect.DeepEqual(item, before) {
			t.Fatalf("Applications mutated %s/%s", item.GetKind(), item.GetName())
		}
	}

}

func TestApplicationsExistingControllerKeyParity(t *testing.T) {
	withOwnershipDiscovery(t)
	controller := true
	owner := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "owner", UID: "owner"}}
	inherited := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "inherited", UID: "inherited", Annotations: map[string]string{"meta.helm.sh/release-name": "other", "meta.helm.sh/release-namespace": "prod"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "owner", UID: "owner", Controller: &controller}}}}
	useTestResourceCache(t, fake.NewClientset(append(testNamespaces("prod"), owner, inherited)...))
	cache := k8s.GetResourceCache()
	session, err := k8s.BeginOwnershipRead(cache)
	if err != nil {
		t.Fatal(err)
	}
	view := ownership.NewView(session, ownership.Unrestricted())
	inventory := &applicationInventory{batch: buildAppRelationBatch(context.Background(), session.(relations.Session), []string{"prod"}), session: session.(relations.Session), owners: view}
	inputs := collectAppWorkloads(context.Background(), cache, []string{"prod"}, inventory, view, false)
	rows := groupApplications(inputs)
	if len(rows) != 1 || rows[0].Key != "prod/HelmRelease/other" {
		t.Fatalf("controller fixture keys changed: %+v", rows)
	}
}

func TestApplicationsSelectorBatchScopesAndPredicates(t *testing.T) {
	objects := testNamespaces("prod", "other")
	for _, ns := range []string{"prod", "other"} {
		for name, labels := range map[string]map[string]string{
			"api":          {"app": "api", "tier": "web"},
			"backend":      {"app": "api", "tier": "backend"},
			"missing-tier": {"app": "api"},
			"disabled":     {"app": "api", "tier": "web", "disabled": ""},
		} {
			objects = append(objects, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}}}})
		}
	}
	objects = append(objects,
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "api"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "api", "tier": "web", "disabled": ""}}},
		&policyv1.PodDisruptionBudget{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "budget"}, Spec: policyv1.PodDisruptionBudgetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}, MatchExpressions: []metav1.LabelSelectorRequirement{
			{Key: "tier", Operator: metav1.LabelSelectorOpIn, Values: []string{"web"}},
			{Key: "tier", Operator: metav1.LabelSelectorOpNotIn, Values: []string{"backend"}},
			{Key: "app", Operator: metav1.LabelSelectorOpExists},
			{Key: "disabled", Operator: metav1.LabelSelectorOpDoesNotExist},
		}}}},
	)
	useTestResourceCache(t, fake.NewClientset(objects...))
	gvr := schema.GroupVersionResource{Group: "projectcalico.org", Version: "v3", Resource: "globalnetworkpolicies"}
	policy := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "projectcalico.org/v3", "kind": "GlobalNetworkPolicy", "metadata": map[string]any{"name": "global"},
		"spec": map[string]any{"selector": "app == 'api' && tier == 'web'"},
	}}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "GlobalNetworkPolicyList"}, policy)
	if err := k8s.InitTestDynamicResourceCache(client, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: "GlobalNetworkPolicy", IsCRD: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	addApplicationTypedDiscovery()
	dc := k8s.GetDynamicResourceCache()
	if err := dc.EnsureWatching(gvr); err != nil || !dc.WaitForSync(gvr, 2*time.Second) {
		t.Fatalf("watch %v: %v", gvr, err)
	}
	session, err := k8s.BeginOwnershipRead(k8s.GetResourceCache())
	if err != nil {
		t.Fatal(err)
	}
	batch := buildAppRelationBatch(context.Background(), session.(relations.Session), nil)
	want := map[string]map[resourceid.Ref][]topology.ResourceRef{
		"Services": {resourceid.NewRef("apps", "Deployment", "prod", "disabled"): {{Kind: "Service", Namespace: "prod", Name: "api"}}},
		"PDBs":     {resourceid.NewRef("apps", "Deployment", "prod", "api"): {{Group: "policy", Kind: "PodDisruptionBudget", Namespace: "prod", Name: "budget"}}},
	}
	want["NetworkPolicies"] = map[resourceid.Ref][]topology.ResourceRef{}
	for _, ns := range []string{"prod", "other"} {
		for _, name := range []string{"api", "disabled"} {
			want["NetworkPolicies"][resourceid.NewRef("apps", "Deployment", ns, name)] = []topology.ResourceRef{{Group: gvr.Group, Kind: "GlobalNetworkPolicy", Name: "global"}}
		}
	}
	for field, refs := range want {
		if !reflect.DeepEqual(batch.fields[field], refs) {
			t.Fatalf("%s=%#v want=%#v", field, batch.fields[field], refs)
		}
	}
}

// unreadableRef reads as Unavailable for ref, as an unloaded store would.
type unreadableRef struct {
	relations.Session
	ref resourceid.Ref
}

func (s unreadableRef) Borrow(ref resourceid.Ref) (runtime.Object, ownership.ObjectRead) {
	if ref == s.ref {
		return nil, ownership.ObjectRead{State: ownership.Unavailable, Gap: ownership.Gap{Kind: ref.GroupKind(), Namespace: ref.Namespace, Reason: readreason.KindSyncPending}}
	}
	return s.Session.Borrow(ref)
}

// A Rollout whose workloadRef target couldn't be read may be selected by a
// Service, so its exposure is unknown rather than a worker's.
func TestApplicationsBatchKeepsAnUndecidedSelectionUnknown(t *testing.T) {
	useTestResourceCache(t, fake.NewClientset(testNamespaces("prod")...))
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}
	rollout := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Rollout", "metadata": map[string]any{"namespace": "prod", "name": "web"},
		"spec": map[string]any{"workloadRef": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "web-template"}},
	}}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "RolloutList"}, rollout)
	if err := k8s.InitTestDynamicResourceCache(client, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: "Rollout", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	addApplicationTypedDiscovery()
	dc := k8s.GetDynamicResourceCache()
	if err := dc.EnsureWatching(gvr); err != nil || !dc.WaitForSync(gvr, 2*time.Second) {
		t.Fatalf("watch %v: %v", gvr, err)
	}
	session, err := k8s.BeginOwnershipRead(k8s.GetResourceCache())
	if err != nil {
		t.Fatal(err)
	}
	template := resourceid.NewRef("apps", "Deployment", "prod", "web-template")
	for _, tc := range []struct {
		name    string
		s       relations.Session
		unknown bool
	}{
		{"template absent", session.(relations.Session), false},
		{"template unreadable", unreadableRef{session.(relations.Session), template}, true},
	} {
		batch := buildAppRelationBatch(context.Background(), tc.s, nil)
		rel := batch.relationshipsForRef("argoproj.io", "Rollout", "prod", "web")
		if got := rel != nil && rel.exposureIncomplete; got != tc.unknown {
			t.Errorf("%s: exposure unknown = %v, want %v", tc.name, got, tc.unknown)
		}
		// The template's kind is still loading, so the rows aren't cached.
		if batch.loading != tc.unknown {
			t.Errorf("%s: loading = %v, want %v", tc.name, batch.loading, tc.unknown)
		}
	}
}

// gappedKind adds a gap to every indexed read of kind.
type gappedKind struct {
	relations.Session
	kind   resourceid.GroupKind
	reason readreason.Code
}

func (s gappedKind) Indexed(kind resourceid.GroupKind, index string, keys []string, scope ownership.SearchScope) relations.IndexedRead {
	read := s.Session.Indexed(kind, index, keys, scope)
	if kind == s.kind {
		read.Gaps = append(read.Gaps, ownership.Gap{Kind: kind, Reason: s.reason})
	}
	return read
}

// Only a kind still loading keeps a build from the cache: a gap that lasts,
// such as a kind Radar doesn't collect, would keep every build out.
func TestApplicationsBatchNotesKindsStillLoading(t *testing.T) {
	useTestResourceCache(t, fake.NewClientset(append(testNamespaces("prod"),
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "api"}},
		&autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "scale"}, Spec: autoscalingv2.HorizontalPodAutoscalerSpec{ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "api"}}},
	)...))
	restore, err := k8s.SetTestResourceDiscovery(fake.NewClientset().Discovery())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	addApplicationTypedDiscovery()
	session, err := k8s.BeginOwnershipRead(k8s.GetResourceCache())
	if err != nil {
		t.Fatal(err)
	}
	hpa := resourceid.GroupKind{Group: "autoscaling", Kind: "HorizontalPodAutoscaler"}
	for _, tc := range []struct {
		name    string
		s       relations.Session
		loading bool
	}{
		{"loaded", session.(relations.Session), false},
		{"autoscalers loading", gappedKind{session.(relations.Session), hpa, readreason.KindSyncPending}, true},
		{"autoscalers not collected", gappedKind{session.(relations.Session), hpa, readreason.RadarKindNotCollected}, false},
	} {
		if got := buildAppRelationBatch(context.Background(), tc.s, []string{"prod"}).loading; got != tc.loading {
			t.Errorf("%s: loading = %v, want %v", tc.name, got, tc.loading)
		}
	}
}

// unloadedKind reads kind as a store still loading: no objects, one gap.
type unloadedKind struct {
	relations.Session
	kind resourceid.GroupKind
}

func (s unloadedKind) Indexed(kind resourceid.GroupKind, index string, keys []string, scope ownership.SearchScope) relations.IndexedRead {
	if kind == s.kind {
		return relations.IndexedRead{Gaps: []ownership.Gap{{Kind: kind, Reason: readreason.KindSyncPending}}}
	}
	return s.Session.Indexed(kind, index, keys, scope)
}

// A workload the batch couldn't read can't be matched to the Services that
// select it, so a row built from the workload as its own lister reads it has
// undecided exposure, not a worker's.
func TestApplicationsUnreadWorkloadKindLeavesExposureUndecided(t *testing.T) {
	labels := map[string]string{"app": "api"}
	useTestResourceCache(t, fake.NewClientset(append(testNamespaces("prod"),
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "api"}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}}}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "api"}, Spec: corev1.ServiceSpec{Selector: labels}},
	)...))
	restore, err := k8s.SetTestResourceDiscovery(fake.NewClientset().Discovery())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	addApplicationTypedDiscovery()
	cache := k8s.GetResourceCache()
	for _, tc := range []struct {
		name    string
		loading bool
		want    string
	}{{"Deployments loaded", false, "service"}, {"Deployments loading", true, "unknown"}} {
		session, err := k8s.BeginOwnershipRead(cache)
		if err != nil {
			t.Fatal(err)
		}
		batchSession := session.(relations.Session)
		if tc.loading {
			batchSession = unloadedKind{batchSession, resourceid.GroupKind{Group: "apps", Kind: "Deployment"}}
		}
		view := ownership.NewView(session, ownership.Unrestricted())
		inventory := &applicationInventory{batch: buildAppRelationBatch(context.Background(), batchSession, []string{"prod"}), session: session.(relations.Session), owners: view}
		rows := groupApplications(collectAppWorkloads(context.Background(), cache, []string{"prod"}, inventory, view, false))
		if row := rowByName(rows, "api"); row == nil || row.WorkloadClass != tc.want || row.Workloads[0].WorkloadClass != tc.want {
			t.Errorf("%s: row = %+v, want class %s", tc.name, row, tc.want)
		}
	}
}

// A workload created or changed after the batch read its kind, and before the
// rows read it, has no selection the batch decided for it: its exposure is
// undecided and the build isn't cached, so a rebuild classifies it.
func TestApplicationsWorkloadChangedBetweenBatchAndRows(t *testing.T) {
	labels := map[string]string{"app": "api"}
	deployment := func(generation int64, labels map[string]string) *appsv1.Deployment {
		return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "api", UID: "api", Generation: generation}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}}}}
	}
	statusWritten := deployment(1, labels)
	statusWritten.Status.ReadyReplicas = 3
	for _, tc := range []struct {
		name      string
		before    *appsv1.Deployment
		after     *appsv1.Deployment
		wantClass string
	}{
		{"created", nil, deployment(1, labels), "unknown"},
		{"template relabelled", deployment(1, map[string]string{"app": "other"}), deployment(2, labels), "unknown"},
		// A status write leaves the spec the batch evaluated, and its answer.
		{"status written", deployment(1, labels), statusWritten, "service"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objects := append(testNamespaces("prod"), &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "api"}, Spec: corev1.ServiceSpec{Selector: labels}})
			if tc.before != nil {
				objects = append(objects, tc.before)
			}
			client := fake.NewClientset(objects...)
			useTestResourceCache(t, client)
			restore, err := k8s.SetTestResourceDiscovery(fake.NewClientset().Discovery())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(restore)
			addApplicationTypedDiscovery()
			cache := k8s.GetResourceCache()
			rows := func(batch *appRelationBatch, session ownership.ReadSession) ([]appRow, bool) {
				view := ownership.NewView(session, ownership.Unrestricted())
				inventory := &applicationInventory{batch: batch, session: session.(relations.Session), owners: view}
				return groupApplications(collectAppWorkloads(context.Background(), cache, []string{"prod"}, inventory, view, false)), inventory.cacheable()
			}
			session, err := k8s.BeginOwnershipRead(cache)
			if err != nil {
				t.Fatal(err)
			}
			batch := buildAppRelationBatch(context.Background(), session.(relations.Session), []string{"prod"})

			if tc.before == nil {
				_, err = client.AppsV1().Deployments("prod").Create(context.Background(), tc.after, metav1.CreateOptions{})
			} else {
				_, err = client.AppsV1().Deployments("prod").Update(context.Background(), tc.after, metav1.UpdateOptions{})
			}
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				d, err := cache.Deployments().Deployments("prod").Get("api")
				if err == nil && d.Generation == tc.after.Generation && d.Status.ReadyReplicas == tc.after.Status.ReadyReplicas && d.Spec.Template.Labels["app"] == "api" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("the Deployment never reached the lister: %v", err)
				}
				time.Sleep(10 * time.Millisecond)
			}

			got, cacheable := rows(batch, session)
			if row := rowByName(got, "api"); row == nil || row.WorkloadClass != tc.wantClass || cacheable != (tc.wantClass != "unknown") {
				t.Fatalf("rows over a batch read before the change: row %+v cacheable %v, want %s", row, cacheable, tc.wantClass)
			}
			fresh, err := k8s.BeginOwnershipRead(cache)
			if err != nil {
				t.Fatal(err)
			}
			got, cacheable = rows(buildAppRelationBatch(context.Background(), fresh.(relations.Session), []string{"prod"}), fresh)
			if row := rowByName(got, "api"); row == nil || row.WorkloadClass != "service" || !cacheable {
				t.Fatalf("rebuilt rows: row %+v cacheable %v, want service and cacheable", row, cacheable)
			}
		})
	}
}

// changeBetweenBatchAndRows builds a batch over the current cache, applies
// change, waits until settled reports the cache has it, and returns rows over
// that batch and whether the build is cacheable, then over a fresh batch.
func changeBetweenBatchAndRows(t *testing.T, change func(), settled func() bool) (before []appRow, beforeCacheable bool, fresh []appRow, freshCacheable bool) {
	t.Helper()
	cache := k8s.GetResourceCache()
	rows := func(session ownership.ReadSession, batch *appRelationBatch) ([]appRow, bool) {
		view := ownership.NewView(session, ownership.Unrestricted())
		inventory := &applicationInventory{batch: batch, session: session.(relations.Session), owners: view}
		return groupApplications(collectAppWorkloads(context.Background(), cache, []string{"prod"}, inventory, view, false)), inventory.cacheable()
	}
	session, err := k8s.BeginOwnershipRead(cache)
	if err != nil {
		t.Fatal(err)
	}
	batch := buildAppRelationBatch(context.Background(), session.(relations.Session), []string{"prod"})
	change()
	for deadline := time.Now().Add(5 * time.Second); !settled(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the change never reached the cache")
		}
	}
	before, beforeCacheable = rows(session, batch)
	next, err := k8s.BeginOwnershipRead(cache)
	if err != nil {
		t.Fatal(err)
	}
	fresh, freshCacheable = rows(next, buildAppRelationBatch(context.Background(), next.(relations.Session), []string{"prod"}))
	return before, beforeCacheable, fresh, freshCacheable
}

// A Rollout whose workloadRef names a Deployment is selected by that
// Deployment's template, so the Deployment changing between the passes leaves
// the Rollout's exposure undecided, though the Rollout itself didn't change.
func TestApplicationsRolloutTemplateChangedBetweenBatchAndRows(t *testing.T) {
	labels := map[string]string{"app": "api"}
	template := func(generation int64, labels map[string]string) *appsv1.Deployment {
		return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "web-template", UID: "web-template", Generation: generation}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}}}}
	}
	client := fake.NewClientset(append(testNamespaces("prod"), template(1, map[string]string{"app": "other"}), &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "api"}, Spec: corev1.ServiceSpec{Selector: labels}})...)
	useTestResourceCache(t, client)
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}
	rollout := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Rollout", "metadata": map[string]any{"namespace": "prod", "name": "web", "uid": "web"},
		"spec": map[string]any{"workloadRef": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "web-template"}},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "RolloutList"}, rollout)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: "Rollout", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	addApplicationTypedDiscovery()
	dc := k8s.GetDynamicResourceCache()
	if err := dc.EnsureWatching(gvr); err != nil || !dc.WaitForSync(gvr, 2*time.Second) {
		t.Fatalf("watch %v: %v", gvr, err)
	}
	before, beforeCacheable, fresh, freshCacheable := changeBetweenBatchAndRows(t, func() {
		if _, err := client.AppsV1().Deployments("prod").Update(context.Background(), template(2, labels), metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	}, func() bool {
		d, err := k8s.GetResourceCache().Deployments().Deployments("prod").Get("web-template")
		return err == nil && d.Generation == 2
	})
	if row := rowByName(before, "web"); row == nil || row.WorkloadClass != "unknown" || beforeCacheable {
		t.Fatalf("rows over a batch read before the template changed: row %+v cacheable %v, want unknown and not cacheable", row, beforeCacheable)
	}
	if row := rowByName(fresh, "web"); row == nil || row.WorkloadClass != "service" || !freshCacheable {
		t.Fatalf("rebuilt rows: row %+v cacheable %v, want service and cacheable", row, freshCacheable)
	}
}

// Argo's Workflow CRD has no status subresource, so its generation moves with
// every status write. A running Workflow's status written between the passes
// keeps the batch's answer; a spec change doesn't.
func TestApplicationsWorkflowWrittenBetweenBatchAndRows(t *testing.T) {
	useTestResourceCache(t, fake.NewClientset(testNamespaces("prod")...))
	workflows := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "workflows"}
	templates := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "workflowtemplates"}
	workflow := func(generation int64, template, phase string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "argoproj.io/v1alpha1", "kind": "Workflow", "metadata": map[string]any{"namespace": "prod", "name": "release", "uid": "release", "generation": generation},
			"spec":   map[string]any{"templates": []any{map[string]any{"name": "main", "steps": []any{[]any{map[string]any{"name": "build", "templateRef": map[string]any{"name": template}}}}}}},
			"status": map[string]any{"phase": phase},
		}}
	}
	build := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "WorkflowTemplate", "metadata": map[string]any{"namespace": "prod", "name": "build", "uid": "build"}}}
	other := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "WorkflowTemplate", "metadata": map[string]any{"namespace": "prod", "name": "other", "uid": "other"}}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{workflows: "WorkflowList", templates: "WorkflowTemplateList"}, workflow(1, "build", "Running"), build, other)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{
		{Group: workflows.Group, Version: workflows.Version, Name: workflows.Resource, Kind: "Workflow", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
		{Group: templates.Group, Version: templates.Version, Name: templates.Resource, Kind: "WorkflowTemplate", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	addApplicationTypedDiscovery()
	dc := k8s.GetDynamicResourceCache()
	for _, gvr := range []schema.GroupVersionResource{workflows, templates} {
		if err := dc.EnsureWatching(gvr); err != nil || !dc.WaitForSync(gvr, 2*time.Second) {
			t.Fatalf("watch %v: %v", gvr, err)
		}
	}
	write := func(next *unstructured.Unstructured) (func(), func() bool) {
		return func() {
				if _, err := dyn.Resource(workflows).Namespace("prod").Update(context.Background(), next, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			}, func() bool {
				items, err := dc.ListReadOnlyContext(context.Background(), workflows, "prod")
				return err == nil && len(items) == 1 && items[0].GetGeneration() == next.GetGeneration()
			}
	}
	configured := func(row *appRow, template string) bool {
		return row != nil && row.Relationships != nil && len(row.Relationships.ConfigRefs) == 1 && row.Relationships.ConfigRefs[0].Name == template
	}

	// The API server bumps this CRD's generation for a status write too.
	change, settled := write(workflow(2, "build", "Succeeded"))
	before, beforeCacheable, _, _ := changeBetweenBatchAndRows(t, change, settled)
	if row := rowByName(before, "release"); !configured(row, "build") || !beforeCacheable {
		t.Fatalf("a status write between the passes: row %+v cacheable %v, want the batch's answer, cacheable", row, beforeCacheable)
	}
	change, settled = write(workflow(3, "other", "Succeeded"))
	before, beforeCacheable, fresh, freshCacheable := changeBetweenBatchAndRows(t, change, settled)
	if row := rowByName(before, "release"); row == nil || row.Relationships != nil && len(row.Relationships.ConfigRefs) != 0 || beforeCacheable {
		t.Fatalf("a spec change between the passes: row %+v cacheable %v, want no answer from the batch, not cacheable", row, beforeCacheable)
	}
	if row := rowByName(fresh, "release"); !configured(row, "other") || !freshCacheable {
		t.Fatalf("rebuilt rows: row %+v cacheable %v, want the new template, cacheable", row, freshCacheable)
	}
}

// A Rollout's workloadRef may name a core PodTemplate, which keeps its pod
// template at the root rather than under spec: relabelling it between the
// passes leaves the Rollout's exposure undecided too.
func TestApplicationsRolloutPodTemplateChangedBetweenBatchAndRows(t *testing.T) {
	labels := map[string]string{"app": "api"}
	useTestResourceCache(t, fake.NewClientset(append(testNamespaces("prod"), &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "api"}, Spec: corev1.ServiceSpec{Selector: labels}})...))
	rollouts := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}
	podTemplates := schema.GroupVersionResource{Version: "v1", Resource: "podtemplates"}
	podTemplate := func(app string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1", "kind": "PodTemplate", "metadata": map[string]any{"namespace": "prod", "name": "web-template", "uid": "web-template"},
			"template": map[string]any{"metadata": map[string]any{"labels": map[string]any{"app": app}}},
		}}
	}
	rollout := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Rollout", "metadata": map[string]any{"namespace": "prod", "name": "web", "uid": "web"},
		"spec": map[string]any{"workloadRef": map[string]any{"apiVersion": "v1", "kind": "PodTemplate", "name": "web-template"}},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{rollouts: "RolloutList", podTemplates: "PodTemplateList"}, rollout, podTemplate("other"))
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{
		{Group: rollouts.Group, Version: rollouts.Version, Name: rollouts.Resource, Kind: "Rollout", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
		{Version: podTemplates.Version, Name: podTemplates.Resource, Kind: "PodTemplate", Namespaced: true, Verbs: []string{"get", "list", "watch"}},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	addApplicationTypedDiscovery()
	dc := k8s.GetDynamicResourceCache()
	for _, gvr := range []schema.GroupVersionResource{rollouts, podTemplates} {
		if err := dc.EnsureWatching(gvr); err != nil || !dc.WaitForSync(gvr, 2*time.Second) {
			t.Fatalf("watch %v: %v", gvr, err)
		}
	}
	before, beforeCacheable, fresh, freshCacheable := changeBetweenBatchAndRows(t, func() {
		if _, err := dyn.Resource(podTemplates).Namespace("prod").Update(context.Background(), podTemplate("api"), metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	}, func() bool {
		items, err := dc.ListReadOnlyContext(context.Background(), podTemplates, "prod")
		if err != nil || len(items) != 1 {
			return false
		}
		app, _, _ := unstructured.NestedString(items[0].Object, "template", "metadata", "labels", "app")
		return app == "api"
	})
	if row := rowByName(before, "web"); row == nil || row.WorkloadClass != "unknown" || beforeCacheable {
		t.Fatalf("rows over a batch read before the PodTemplate changed: row %+v cacheable %v, want unknown and not cacheable", row, beforeCacheable)
	}
	if row := rowByName(fresh, "web"); row == nil || row.WorkloadClass != "service" || !freshCacheable {
		t.Fatalf("rebuilt rows: row %+v cacheable %v, want service and cacheable", row, freshCacheable)
	}
}
