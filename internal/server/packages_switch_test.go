package server

import (
	"context"
	"errors"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
)

// clusterWithHelmWorkload is a cluster whose only package is one Helm-labelled
// Deployment, so each side of a switch reports a chart that exists only there.
func clusterWithHelmWorkload(chart string) *fake.Clientset {
	return fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "demo"}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
			Name: chart, Namespace: "demo",
			Labels:      map[string]string{"helm.sh/chart": chart + "-1.0.0"},
			Annotations: map[string]string{"meta.helm.sh/release-name": chart, "meta.helm.sh/release-namespace": "demo"},
		}},
	)
}

func packageCharts(resp PackagesResponse) []string {
	var charts []string
	for _, row := range resp.Packages {
		charts = append(charts, row.Chart)
	}
	return charts
}

// A package list computed from the cluster the user is leaving can finish
// after the context switch has cleared the cache, because nothing cancels it.
// Restoring that entry after the switch stands for that late store. It must
// not be what a request on the new cluster gets.
func TestListPackages_LateStoreFromPreviousClusterAfterSwitch(t *testing.T) {
	withCleanCache(t, func() {
		useTestResourceCache(t, clusterWithHelmWorkload("old-cluster-chart"))
		s := newTestServer(t)
		params := ListPackagesParams{Namespaces: []string{"demo"}}

		before, err := ListPackages(context.Background(), params)
		if err != nil {
			t.Fatalf("seed ListPackages: %v", err)
		}
		if charts := packageCharts(before); len(charts) != 1 || charts[0] != "old-cluster-chart" {
			t.Fatalf("seed: want [old-cluster-chart], got %v", charts)
		}
		packagesCacheMu.Lock()
		lateStore := make(map[string]packagesCacheEntry, len(packagesCache))
		for k, e := range packagesCache {
			lateStore[k] = e
		}
		packagesCacheMu.Unlock()

		useTestResourceCache(t, clusterWithHelmWorkload("new-cluster-chart"))
		s.finalizePostContextSwitch()
		packagesCacheMu.Lock()
		for k, e := range lateStore {
			packagesCache[k] = e
		}
		packagesCacheMu.Unlock()

		after, err := ListPackages(context.Background(), params)
		if err != nil {
			t.Fatalf("post-switch ListPackages: %v", err)
		}
		if charts := packageCharts(after); len(charts) != 1 || charts[0] != "new-cluster-chart" {
			t.Errorf("post-switch package list describes the previous cluster: got %v, want [new-cluster-chart]", charts)
		}
	})
}

// Mid-switch there is no resource cache, so there is no generation to look
// a cached list up under. A list cached before the switch must not be
// served in that gap.
func TestListPackages_NoResourceCacheIsNotReady(t *testing.T) {
	withCleanCache(t, func() {
		useTestResourceCache(t, clusterWithHelmWorkload("old-cluster-chart"))
		params := ListPackagesParams{Namespaces: []string{"demo"}}
		if _, err := ListPackages(context.Background(), params); err != nil {
			t.Fatalf("seed ListPackages: %v", err)
		}
		k8s.ResetResourceCache()
		resp, err := ListPackages(context.Background(), params)
		if !errors.Is(err, errResourceCacheUnavailable) {
			t.Errorf("with no resource cache: got err %v and charts %v, want errResourceCacheUnavailable", err, packageCharts(resp))
		}
	})
}
