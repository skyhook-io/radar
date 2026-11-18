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
	"k8s.io/apimachinery/pkg/types"
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
	useApplicationsBenchCache(b, objects)
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

func useApplicationsBenchCache(b *testing.B, objects []runtime.Object) {
	b.Helper()
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
}

// The batch beside what a real cluster also holds: each Deployment's
// ReplicaSets and Pods, and the ConfigMaps, Secrets and ServiceAccounts they
// mount. every_source evaluates every source of a family a row could use;
// rows evaluates only the sources rows read.
func BenchmarkApplicationsBatch1000WorkloadsWithPodsAndConfig(b *testing.B) {
	objects := testNamespaces("bench")
	controller := true
	for i := range 1000 {
		name := fmt.Sprintf("app-%d", i)
		selector := map[string]string{"app": name}
		spec := corev1.PodSpec{
			ServiceAccountName: name,
			Volumes: []corev1.Volume{
				{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: name}}}},
				{Name: "secret", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: name}}},
			},
			Containers: []corev1.Container{{Name: "app", Env: []corev1.EnvVar{{Name: "MODE", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: name + "-flags"}, Key: "mode"}}}}}},
		}
		template := corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: selector}, Spec: spec}
		uid := types.UID(name)
		objects = append(objects,
			&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "bench", UID: uid}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: selector}, Template: template}},
			&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "bench"}},
			&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name + "-flags", Namespace: "bench"}},
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "bench"}},
			&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "bench"}},
		)
		for r := range 10 {
			rs := fmt.Sprintf("%s-%d", name, r)
			objects = append(objects, &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: rs, Namespace: "bench", UID: types.UID(rs), OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: name, UID: uid, Controller: &controller}}}, Spec: appsv1.ReplicaSetSpec{Selector: &metav1.LabelSelector{MatchLabels: selector}, Template: template}})
			if r == 9 {
				for p := range 3 {
					objects = append(objects, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%d", rs, p), Namespace: "bench", Labels: selector, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: rs, UID: types.UID(rs), Controller: &controller}}}, Spec: spec})
				}
			}
		}
	}
	for i := range 5000 {
		objects = append(objects, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("service-%d", i), Namespace: "bench"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": fmt.Sprintf("app-%d", i%1000)}}})
	}
	useApplicationsBenchCache(b, objects)
	for _, plan := range []struct {
		name string
		plan func([]*relations.Family, []*relations.Composition) appEvaluation
	}{{"every_source", appEverySourceEvaluation}, {"rows", appRowEvaluation}} {
		b.Run("batch_only/cold_session/"+plan.name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				b.StopTimer()
				session, err := k8s.BeginOwnershipRead(k8s.GetResourceCache())
				if err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				batch := buildPlannedAppRelationBatch(context.Background(), session.(relations.Session), []string{"bench"}, plan.plan)
				if len(batch.fields["Services"]) != 1000 || len(batch.fields["ConfigRefs"]) < 1000 {
					b.Fatalf("batch workloads=%d configured=%d", len(batch.fields["Services"]), len(batch.fields["ConfigRefs"]))
				}
			}
		})
	}
	b.Run("cold_rows_cold_batch", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			b.StopTimer()
			clearApplicationsCache()
			b.StartTimer()
			resp, err := listApplicationsForRequest(context.Background(), []string{"bench"}, false)
			if err != nil || len(resp.Applications) != 1000 {
				b.Fatalf("rows=%v err=%v", resp, err)
			}
		}
	})
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
