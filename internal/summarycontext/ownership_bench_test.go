package summarycontext

import (
	"context"
	"fmt"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/ownership"
)

// BenchmarkSummaryPageOfOwnerlessDeployments is one list page whose rows all
// have no controller, so each row also asks for residual spec managers.
func BenchmarkSummaryPageOfOwnerlessDeployments(b *testing.B) {
	const rows = 1000
	objs := make([]runtime.Object, 0, rows)
	deployments := make([]*appsv1.Deployment, 0, rows)
	for i := range rows {
		d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: fmt.Sprintf("web-%d", i), UID: types.UID(fmt.Sprintf("uid-%d", i))}}
		d.SetGroupVersionKind(appsv1.SchemeGroupVersion.WithKind("Deployment"))
		objs = append(objs, d)
		deployments = append(deployments, d)
	}
	if err := k8s.InitTestResourceCache(fake.NewClientset(objs...)); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(k8s.ResetResourceCache)
	var resources []k8s.APIResource
	for _, r := range []struct{ group, kind, name string }{{"", "Pod", "pods"}, {"apps", "Deployment", "deployments"}, {"apps", "ReplicaSet", "replicasets"}, {"", "Secret", "secrets"}, {"", "ConfigMap", "configmaps"}} {
		resources = append(resources, k8s.APIResource{Group: r.group, Version: "v1", Kind: r.kind, Name: r.name, Namespaced: true, Verbs: metav1.Verbs{"get", "list", "watch", "delete"}})
	}
	if err := k8s.InitTestDynamicResourceCache(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), resources); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(k8s.ResetTestDynamicState)

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		ctx, cancel := context.WithCancel(context.Background())
		owners := k8s.NewAgentOwnership(func(*ownership.Catalog) ownership.Access { return ownership.Unrestricted() })
		build := BuilderFromIndexes(ctx, nil, nil, nil, owners)
		for _, d := range deployments {
			if got := build(d, nil, "apps", "Deployment", d.Namespace, d.Name); got == nil || len(got.Omitted) != 0 {
				b.Fatalf("summary %+v", got)
			}
		}
		cancel()
	}
}
