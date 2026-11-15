package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	b.Cleanup(func() {
		clearApplicationsCache()
		k8s.ResetResourceCache()
		if err := k8s.InitTestResourceCache(testFakeClient); err != nil {
			b.Fatal(err)
		}
	})
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
	for _, mode := range []string{"cold_rows_cold_graph", "cold_rows_warm_graph", "warm_rows"} {
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
					if mode == "cold_rows_cold_graph" {
						entry.graph = nil
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
