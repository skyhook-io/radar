package server

import (
	"context"
	"fmt"
	"testing"

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
