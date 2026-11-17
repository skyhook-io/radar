package server

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/topology"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestApplicationsRetainAttachmentsAboveTopologyDisplayLimit(t *testing.T) {
	selector := map[string]string{"app": "api"}
	objects := testNamespaces("prod")
	objects = append(objects,
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "api"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: selector}, Spec: corev1.PodSpec{Volumes: []corev1.Volume{
			{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "settings"}}}},
			{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}}},
		}}}}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "api"}, Spec: corev1.ServiceSpec{Selector: selector}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "settings"}},
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "data"}},
	)
	for i := range topology.LargeClusterThreshold {
		objects = append(objects, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: fmt.Sprintf("idle-%d", i)}})
	}
	useTestResourceCache(t, fake.NewClientset(objects...))
	restore, err := k8s.SetTestResourceDiscovery(fake.NewClientset().Discovery())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	addApplicationTypedDiscovery()
	cache := k8s.GetResourceCache()
	display, err := topology.NewBuilder(k8s.NewTopologyResourceProvider(cache)).Build(topology.DefaultBuildOptions())
	if err != nil || !display.RequiresNamespaceFilter || len(display.Nodes) != 0 {
		t.Fatalf("display guard not exercised: topology=%+v err=%v", display, err)
	}
	clearApplicationsCache()
	t.Cleanup(clearApplicationsCache)
	for _, namespaces := range [][]string{nil, {"prod"}} {
		for range 2 {
			resp, err := listApplicationsForRequest(context.Background(), namespaces, false)
			if err != nil {
				t.Fatal(err)
			}
			if len(resp.Applications) != 1 {
				t.Fatalf("rows=%d", len(resp.Applications))
			}
			row := resp.Applications[0]
			rel := row.Relationships
			if rel == nil || len(rel.ServiceRefs) != 1 || rel.ServiceRefs[0].Name != "api" || rel.Configs != 1 || len(rel.ConfigRefs) != 1 || rel.ConfigRefs[0].Name != "settings" || rel.Storage != 1 || len(rel.StorageRefs) != 1 || rel.StorageRefs[0].Name != "data" {
				t.Fatalf("namespaces=%v attachments=%+v", namespaces, rel)
			}
			if row.WorkloadClass != "service" || row.Workloads[0].WorkloadClass != "service" {
				t.Fatalf("exposure classification=%s/%s", row.WorkloadClass, row.Workloads[0].WorkloadClass)
			}
		}
	}
}

func queuedApplicationsBuildFixture(t *testing.T) {
	t.Helper()
	objects := append(testNamespaces("prod"), &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "api"}})
	useTestResourceCache(t, fake.NewClientset(objects...))
	restore, err := k8s.SetTestResourceDiscovery(fake.NewClientset().Discovery())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	addApplicationTypedDiscovery()
	clearApplicationsCache()
	t.Cleanup(clearApplicationsCache)
}

// A cold build queued behind a slow one gets its whole budget once admitted.
func TestApplicationsQueuedBuildGetsFullBudget(t *testing.T) {
	queuedApplicationsBuildFixture(t)
	prev := applicationsBuildBudget
	applicationsBuildBudget = 300 * time.Millisecond
	t.Cleanup(func() { applicationsBuildBudget = prev })

	applicationsBuildSlot <- struct{}{} // a slow build holds the slot
	done := make(chan error, 1)
	go func() {
		_, err := listApplicationsForRequest(context.Background(), []string{"prod"}, false)
		done <- err
	}()
	time.Sleep(2 * applicationsBuildBudget)
	<-applicationsBuildSlot
	if err := <-done; err != nil {
		t.Fatalf("queued build failed: %v", err)
	}
}

// A request that ends while its build is still queued reads as a busy
// build, not an internal error.
func TestApplicationsQueuedBuildRequestEnds(t *testing.T) {
	queuedApplicationsBuildFixture(t)
	applicationsBuildSlot <- struct{}{}
	t.Cleanup(func() { <-applicationsBuildSlot })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := listApplicationsForRequest(ctx, []string{"prod"}, false); !errors.Is(err, errResourceCacheUnavailable) {
		t.Fatalf("err = %v, want errResourceCacheUnavailable", err)
	}
}
