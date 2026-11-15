package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/relations"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func BenchmarkApplications1000Workloads5000Services(b *testing.B) {
	objects := testNamespaces("bench")
	for i := range 1000 {
		selector := map[string]string{"app": fmt.Sprintf("app-%d", i)}
		objects = append(objects, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: selector["app"], Namespace: "bench"}, Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: selector}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: selector}},
		}})
	}
	for i := range 5000 {
		objects = append(objects, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("service-%d", i), Namespace: "bench"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": fmt.Sprintf("app-%d", i%1000)}}})
	}
	k8s.ResetResourceCache()
	if err := k8s.InitTestResourceCache(fake.NewClientset(objects...)); err != nil {
		b.Fatal(err)
	}
	restore, err := k8s.SetTestResourceDiscovery(fake.NewClientset().Discovery())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(restore)
	addApplicationTypedDiscovery()
	b.Cleanup(func() {
		clearApplicationsCache()
		k8s.ResetResourceCache()
		if err := k8s.InitTestResourceCache(testFakeClient); err != nil {
			b.Fatal(err)
		}
	})
	for _, mode := range []string{"cold_session", "warm_session"} {
		b.Run("batch_only/"+mode, func(b *testing.B) {
			session, err := k8s.BeginOwnershipRead(k8s.GetResourceCache())
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if mode == "cold_session" {
					b.StopTimer()
					session, err = k8s.BeginOwnershipRead(k8s.GetResourceCache())
					if err != nil {
						b.Fatal(err)
					}
					b.StartTimer()
				}
				batch := buildAppRelationBatch(context.Background(), session.(relations.Session), []string{"bench"})
				if len(batch.fields["Services"]) != 1000 {
					b.Fatalf("batch workloads=%d", len(batch.fields["Services"]))
				}
			}
		})
	}
	read := func() *applicationsResponse {
		resp, err := listApplicationsForRequest(context.Background(), []string{"bench"}, false)
		if err != nil {
			b.Fatal(err)
		}
		if len(resp.Applications) != 1000 {
			b.Fatalf("rows=%d", len(resp.Applications))
		}
		return resp
	}
	clearApplicationsCache()
	for _, row := range read().Applications {
		if row.Relationships == nil || len(row.Relationships.ServiceRefs) != 5 {
			b.Fatalf("row %s services=%+v", row.Key, row.Relationships)
		}
	}
	for _, mode := range []string{"cold_rows_cold_batch", "warm_rows"} {
		b.Run(mode, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				b.StopTimer()
				applicationsCacheMu.Lock()
				for key, entry := range applicationsCache {
					entry.at = time.Now()
					if mode != "warm_rows" {
						entry.at = time.Time{}
					}
					applicationsCache[key] = entry
				}
				applicationsCacheMu.Unlock()
				b.StartTimer()
				read()
			}
		})
	}
}

func BenchmarkApplicationIdentityReads1000Managers(b *testing.B) {
	kinds := map[schema.GroupVersionResource]string{}
	var objects []runtime.Object
	var resources []k8s.APIResource
	for _, kind := range []string{"Application", "Kustomization"} {
		group, version, resource := "argoproj.io", "v1alpha1", "applications"
		if kind == "Kustomization" {
			group, version, resource = "kustomize.toolkit.fluxcd.io", "v1", "kustomizations"
		}
		gvr := schema.GroupVersionResource{Group: group, Version: version, Resource: resource}
		kinds[gvr] = kind + "List"
		resources = append(resources, k8s.APIResource{Group: group, Version: version, Kind: kind, Name: resource, Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}})
		for i := range 500 {
			path := fmt.Sprintf("apps/app-%d/overlays/prod", i)
			objects = append(objects, &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": group + "/" + version, "kind": kind,
				"metadata": map[string]any{"name": fmt.Sprintf("app-%d", i), "namespace": "gitops"},
				"spec":     map[string]any{"path": path, "source": map[string]any{"path": path}, "destination": map[string]any{"server": "https://kubernetes.default.svc", "namespace": "bench"}},
				"status":   map[string]any{"resources": []any{map[string]any{"group": "apps", "kind": "Deployment", "namespace": "bench", "name": fmt.Sprintf("app-%d", i)}}},
			}})
		}
	}
	if err := k8s.InitTestDynamicResourceCache(dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), kinds, objects...), resources); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(k8s.ResetTestDynamicState)
	for gvr := range kinds {
		dc := k8s.GetDynamicResourceCache()
		if err := dc.EnsureWatching(gvr); err != nil || !dc.WaitForSync(gvr, 2*time.Second) {
			b.Fatalf("watch %v: %v", gvr, err)
		}
	}
	cache := k8s.GetResourceCache()
	for _, mode := range []string{"copied", "read_only"} {
		b.Run(mode, func(b *testing.B) {
			var lister resourceLister = cache
			if mode == "copied" {
				lister = copyingApplicationBenchmarkLister{cache}
			}
			b.ReportAllocs()
			for range b.N {
				_, _, items := argoApplicationFacts(context.Background(), lister)
				if len(items) != 500 {
					b.Fatalf("Applications=%d", len(items))
				}
				if facts := fluxKustomizationFacts(context.Background(), lister); len(facts) != 1000 {
					b.Fatalf("Kustomization facts=%d", len(facts))
				}
				addFluxKustomizationManagedSourceRefs(context.Background(), lister, map[string][]appSourceRef{})
			}
		})
	}
}

type copyingApplicationBenchmarkLister struct{ *k8s.ResourceCache }

func (c copyingApplicationBenchmarkLister) ListDynamicReadOnlyWithGroup(ctx context.Context, kind, namespace, group string) ([]*unstructured.Unstructured, error) {
	return c.ListDynamicWithGroup(ctx, kind, namespace, group)
}
