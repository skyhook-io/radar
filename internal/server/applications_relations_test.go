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
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
)

type appCountingSession struct {
	relations.Session
	reads map[resourceid.GroupKind]int
}

func (s *appCountingSession) Indexed(kind resourceid.GroupKind, index string, keys []string, scope ownership.SearchScope) relations.IndexedRead {
	s.reads[kind]++
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
	useTestResourceCache(t, fake.NewClientset(append(testNamespaces("prod"), deployment, service, ingress, hpa, pdb, np, &networkingv1.NetworkPolicy{ObjectMeta: meta("all-pods")}, &corev1.ConfigMap{ObjectMeta: meta("settings")}, &corev1.PersistentVolumeClaim{ObjectMeta: meta("data")})...))
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
	counted := &appCountingSession{Session: session.(relations.Session), reads: map[resourceid.GroupKind]int{}}
	batch := buildAppRelationBatch(context.Background(), counted, []string{"prod"})
	for kind, count := range counted.reads {
		if count != 1 {
			t.Fatalf("batch scanned %s %d times", kind, count)
		}
	}
	ref := func(group, kind, name string) topology.ResourceRef {
		return topology.ResourceRef{Group: group, Kind: kind, Namespace: "prod", Name: name}
	}
	want := &appRelationships{
		Services: []string{"api"}, Ingresses: []string{"edge"}, Routes: []string{"HTTPProxy/contour", "HTTPRoute/http", "IngressRoute/traefik", "VirtualService/istio"},
		Configs: 2, Scalers: 1, Storage: 1, PDBs: 1, NetworkPolicies: 2,
		serviceRefs:       refsByKey([]topology.ResourceRef{ref("", "Service", "api")}),
		ingressRefs:       refsByKey([]topology.ResourceRef{ref("networking.k8s.io", "Ingress", "edge")}),
		routeRefs:         refsByKey([]topology.ResourceRef{ref("gateway.networking.k8s.io", "HTTPRoute", "http"), ref("traefik.io", "IngressRoute", "traefik"), ref("networking.istio.io", "VirtualService", "istio"), ref("projectcontour.io", "HTTPProxy", "contour")}),
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
	gateway := &appRelationships{Routes: []string{"HTTPRoute/http"}, routeRefs: refsByKey([]topology.ResourceRef{ref("gateway.networking.k8s.io", "HTTPRoute", "http")})}
	if got := batch.relationshipsForRef("gateway.networking.k8s.io", "Gateway", "prod", "gateway"); !reflect.DeepEqual(got, gateway) {
		t.Fatalf("Gateway attachments=%#v want=%#v", got, gateway)
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
