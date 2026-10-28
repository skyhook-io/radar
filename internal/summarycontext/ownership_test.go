package summarycontext

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
)

type summaryAccess struct {
	ownership.Access
	denied resourceid.GroupKind
}

func (a summaryAccess) CanRead(kind resourceid.GroupKind, ns string) ownership.Decision {
	if kind == a.denied {
		return ownership.Denied
	}
	return a.Access.CanRead(kind, ns)
}

func TestOwnershipSummariesUseCanonicalAnswers(t *testing.T) {
	yes := true
	owner := func(group, kind, name, uid string) metav1.OwnerReference {
		version := "v1"
		if group != "" {
			version = group + "/v1"
		}
		return metav1.OwnerReference{APIVersion: version, Kind: kind, Name: name, UID: types.UID(uid), Controller: &yes}
	}
	cr := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.io/v1", "kind": "Widget", "metadata": map[string]any{"namespace": "shop", "name": "db", "uid": "uid-cr"}}}
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application", "metadata": map[string]any{"namespace": "argocd", "name": "web", "uid": "uid-app"},
		"spec":   map[string]any{"destination": map[string]any{"server": "https://kubernetes.default.svc", "namespace": "shop"}},
		"status": map[string]any{"reconciledAt": "now", "resources": []any{map[string]any{"group": "apps", "kind": "Deployment", "namespace": "shop", "name": "standalone"}}},
	}}
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web", UID: "uid-dep", OwnerReferences: []metav1.OwnerReference{owner("example.io", "Widget", "db", "uid-cr")}}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web-a", UID: "uid-pod", OwnerReferences: []metav1.OwnerReference{owner("apps", "Deployment", "web", "uid-dep")}, Labels: map[string]string{"argocd.argoproj.io/instance": "copied"}}}
	standalone := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "standalone", UID: "uid-root"}}
	missing := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "gap", UID: "uid-gap", OwnerReferences: []metav1.OwnerReference{owner("unknown.io", "Missing", "root", "uid-missing")}}}
	managedGap := missing.DeepCopy()
	managedGap.Name, managedGap.UID = "managed-gap", "uid-managed-gap"
	_ = unstructured.SetNestedSlice(app.Object, []any{
		map[string]any{"group": "apps", "kind": "Deployment", "namespace": "shop", "name": "standalone"},
		map[string]any{"kind": "Pod", "namespace": "shop", "name": "managed-gap"},
	}, "status", "resources")
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "tls", UID: "uid-tls"}}
	sealed := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "sealed", UID: "uid-sealed"}}
	unmanaged := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "unmanaged", UID: "uid-unmanaged"}}
	cert := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "cert-manager.io/v1", "kind": "Certificate", "metadata": map[string]any{"namespace": "shop", "name": "tls", "uid": "uid-cert"}, "spec": map[string]any{"secretName": "tls"}}}
	sealedSpec := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "bitnami.com/v1alpha1", "kind": "SealedSecret", "metadata": map[string]any{"namespace": "shop", "name": "sealed", "uid": "uid-sealed-spec"}}}
	if err := k8s.InitTestResourceCache(fake.NewClientset(deployment, pod, standalone, missing, managedGap, secret, sealed, unmanaged)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetResourceCache)
	widgetGVR := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	appGVR := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	certGVR := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
	sealedGVR := schema.GroupVersionResource{Group: "bitnami.com", Version: "v1alpha1", Resource: "sealedsecrets"}
	dynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{widgetGVR: "WidgetList", appGVR: "ApplicationList", certGVR: "CertificateList", sealedGVR: "SealedSecretList"}, cr, app, cert, sealedSpec)
	var resources []k8s.APIResource
	for _, r := range []struct{ group, version, kind, name string }{{"", "v1", "Pod", "pods"}, {"apps", "v1", "Deployment", "deployments"}, {"example.io", "v1", "Widget", "widgets"}, {"argoproj.io", "v1alpha1", "Application", "applications"}, {"cert-manager.io", "v1", "Certificate", "certificates"}, {"bitnami.com", "v1alpha1", "SealedSecret", "sealedsecrets"}, {"", "v1", "Secret", "secrets"}, {"", "v1", "ConfigMap", "configmaps"}} {
		resources = append(resources, k8s.APIResource{Group: r.group, Version: r.version, Kind: r.kind, Name: r.name, Namespaced: true, Verbs: metav1.Verbs{"get", "list", "watch", "delete"}})
	}
	if err := k8s.InitTestDynamicResourceCache(dynamic, resources); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	for _, gvr := range []schema.GroupVersionResource{widgetGVR, appGVR, certGVR, sealedGVR} {
		cache := k8s.GetDynamicResourceCache()
		if err := cache.EnsureWatching(gvr); err != nil {
			t.Fatal(err)
		}
		if !cache.WaitForSync(gvr, 5*time.Second) {
			t.Fatal("dynamic cache did not sync")
		}
	}
	for _, tc := range []struct {
		name   string
		obj    runtime.Object
		kind   string
		access ownership.Access
		want   string
		note   resourcecontext.OmittedReason
		graph  bool
	}{
		{"excluded Event", &corev1.Event{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "event", UID: "uid-event"}}, "Event", ownership.Unrestricted(), "", "", false},
		{"operator beats copied labels", pod, "Pod", ownership.Unrestricted(), "Widget", "", false},
		{"denied top", pod, "Pod", summaryAccess{ownership.Unrestricted(), resourceid.GroupKind{Group: "example.io", Kind: "Widget"}}, "Deployment", resourcecontext.OmittedRBACDenied, false},
		{"recorded without metadata", standalone, "Deployment", ownership.Unrestricted(), "Application", "", true},
		{"budget during graph", standalone, "Deployment", ownership.Unrestricted(), "Application", resourcecontext.OmittedBudgetExceeded, true},
		{"recorded without graph", standalone, "Deployment", ownership.Unrestricted(), "Application", resourcecontext.OmittedUnavailable, true},
		{"unserved owner", missing, "Pod", ownership.Unrestricted(), "", resourcecontext.OmittedUnavailable, false},
		{"GitOps lead beside unknown owner", managedGap, "Pod", ownership.Unrestricted(), "Application", resourcecontext.OmittedUnavailable, false},
		{"certificate spec", secret, "Secret", ownership.Unrestricted(), "Certificate", "", true},
		{"sealed secret spec", sealed, "Secret", ownership.Unrestricted(), "SealedSecret", "", true},
		{"no graph or manager", unmanaged, "ConfigMap", ownership.Unrestricted(), "", resourcecontext.OmittedUnavailable, true},
		{"GitOps edge is not a fallback", unmanaged, "ConfigMap", ownership.Unrestricted(), "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			loaded := false
			graph := func() IndexedTopology {
				loaded = true
				if tc.name == "budget during graph" {
					cancel()
					return IndexedTopology{}
				}
				if tc.name == "recorded without graph" || tc.name == "no graph or manager" {
					return IndexedTopology{}
				}
				nodes := []topology.Node{
					{ID: "deployment/shop/standalone", Kind: topology.KindDeployment, Name: "standalone", Data: map[string]any{"namespace": "shop", "apiVersion": "apps/v1"}},
					{ID: "secret/shop/tls", Kind: topology.KindSecret, Name: "tls", Data: map[string]any{"namespace": "shop"}},
					{ID: "secret/shop/sealed", Kind: topology.KindSecret, Name: "sealed", Data: map[string]any{"namespace": "shop"}},
					{ID: "configmap/shop/unmanaged", Kind: topology.KindConfigMap, Name: "unmanaged", Data: map[string]any{"namespace": "shop"}},
					{ID: "certificate/shop/tls", Kind: topology.KindCertificate, Name: "tls", Data: map[string]any{"namespace": "shop", "apiVersion": "cert-manager.io/v1"}},
					{ID: "sealedsecret/shop/sealed", Kind: topology.KindSealedSecret, Name: "sealed", Data: map[string]any{"namespace": "shop", "apiVersion": "bitnami.com/v1alpha1"}},
					{ID: "application/argocd/web", Kind: topology.KindApplication, Name: "web", Data: map[string]any{"namespace": "argocd", "apiVersion": "argoproj.io/v1alpha1"}},
				}
				value := &topology.Topology{Nodes: nodes, Edges: []topology.Edge{
					{Source: "certificate/shop/tls", Target: "secret/shop/tls", Type: topology.EdgeManages},
					{Source: "sealedsecret/shop/sealed", Target: "secret/shop/sealed", Type: topology.EdgeManages},
					{Source: "application/argocd/web", Target: "configmap/shop/unmanaged", Type: topology.EdgeManages},
				}}
				return IndexedTopology{Topology: value, Index: topology.IndexByResource(value)}
			}
			owners := k8s.NewAgentOwnership(func(*ownership.Catalog) ownership.Access { return tc.access })
			build := BuilderFromIndexes(ctx, graph, nil, nil, nil, owners)
			meta := tc.obj.(metav1.Object)
			group := ""
			if tc.kind == "Deployment" {
				group = "apps"
			}
			got := build(tc.obj, nil, group, tc.kind, meta.GetNamespace(), meta.GetName())
			if loaded != tc.graph {
				t.Fatalf("loaded graph=%v, want %v", loaded, tc.graph)
			}
			if got == nil {
				if tc.want == "" && tc.note == "" {
					return
				}
				t.Fatal("no summary")
			}
			if tc.want == "" {
				if got.ManagedBy != nil {
					t.Fatalf("unexpected pointer %+v", got.ManagedBy)
				}
			} else if got.ManagedBy == nil || got.ManagedBy.Kind != tc.want {
				t.Fatalf("pointer %+v, want %s", got.ManagedBy, tc.want)
			}
			if loaded != tc.graph {
				t.Fatalf("loaded graph=%v, want %v", loaded, tc.graph)
			}
			if tc.note == "" {
				if len(got.Omitted) != 0 {
					t.Fatalf("unexpected omissions %+v", got.Omitted)
				}
			} else if len(got.Omitted) != 1 || got.Omitted[0].Reason != tc.note {
				t.Fatalf("omissions %+v, want %s", got.Omitted, tc.note)
			}
		})
	}
}
