package issues

import (
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/resourceid"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

type strimziOwnerAccess struct {
	ownership.Access
	denied resourceid.GroupKind
}

func (a strimziOwnerAccess) CanRead(kind resourceid.GroupKind, namespace string) ownership.Decision {
	if kind == a.denied {
		return ownership.Denied
	}
	return a.Access.CanRead(kind, namespace)
}

func TestCachedStrimziEvidenceReadsCurrentAuthorizedOwnerChain(t *testing.T) {
	for _, modern := range []bool{true, false} {
		name := "legacy deployment"
		if modern {
			name = "modern podset"
		}
		t.Run(name, func(t *testing.T) {
			k8s.ResetResourceCache()
			k8s.ResetTestDynamicState()
			t.Cleanup(k8s.ResetResourceCache)
			t.Cleanup(k8s.ResetTestDynamicState)
			connect := contextStrimzi("KafkaConnect", "app", "connect")
			connector := contextConnector("app", "sink", "connect")
			podset := contextStrimzi("StrimziPodSet", "app", "workers")
			podset.SetAPIVersion("core.strimzi.io/v1beta2")
			yes := true
			connectOwner := metav1.OwnerReference{APIVersion: connect.GetAPIVersion(), Kind: connect.GetKind(), Name: connect.GetName(), UID: connect.GetUID()}
			podset.SetOwnerReferences([]metav1.OwnerReference{connectOwner})
			deploy := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "workers", Namespace: "app", UID: "deploy-uid", OwnerReferences: []metav1.OwnerReference{connectOwner}}}
			rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "workers-rs", Namespace: "app", UID: "rs-uid", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: deploy.Name, UID: deploy.UID, Controller: &yes}}}}
			pod := &corev1.Pod{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}, ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "app", UID: "pod-uid"}}
			denied := resourceid.GroupKind{Group: "apps", Kind: "Deployment"}
			pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: rs.Name, UID: rs.UID, Controller: &yes}}
			if modern {
				denied = resourceid.GroupKind{Group: "core.strimzi.io", Kind: "StrimziPodSet"}
				pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: podset.GetAPIVersion(), Kind: podset.GetKind(), Name: podset.GetName(), UID: podset.GetUID(), Controller: &yes}}
			}
			if err := k8s.InitTestResourceCache(fake.NewClientset(pod, rs, deploy)); err != nil {
				t.Fatal(err)
			}
			resources := []k8s.APIResource{
				{Group: "", Version: "v1", Kind: "Pod", Name: "pods", Namespaced: true, Verbs: []string{"get", "list", "watch", "delete"}},
				{Group: "apps", Version: "v1", Kind: "ReplicaSet", Name: "replicasets", Namespaced: true, Verbs: []string{"get", "list", "watch", "delete"}},
				{Group: "apps", Version: "v1", Kind: "Deployment", Name: "deployments", Namespaced: true, Verbs: []string{"get", "list", "watch", "delete"}},
				{Group: strimziGroup, Version: "v1", Kind: "KafkaConnect", Name: "kafkaconnects", Namespaced: true, Verbs: []string{"get", "list", "watch", "delete"}},
				{Group: strimziGroup, Version: "v1", Kind: "KafkaConnector", Name: "kafkaconnectors", Namespaced: true, Verbs: []string{"get", "list", "watch", "delete"}},
				{Group: "core.strimzi.io", Version: "v1beta2", Kind: "StrimziPodSet", Name: "strimzipodsets", Namespaced: true, Verbs: []string{"get", "list", "watch", "delete"}},
			}
			listKinds := map[schema.GroupVersionResource]string{}
			for _, r := range resources[3:] {
				listKinds[schema.GroupVersionResource{Group: r.Group, Version: r.Version, Resource: r.Name}] = r.Kind + "List"
			}
			client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, connect, connector, podset)
			if err := k8s.InitTestDynamicResourceCache(client, resources); err != nil {
				t.Fatal(err)
			}
			cache := k8s.GetDynamicResourceCache()
			for gvr := range listKinds {
				if err := cache.EnsureWatching(gvr); err != nil {
					t.Fatal(err)
				}
				if !cache.WaitForSync(gvr, 5*time.Second) {
					t.Fatal("fixture cache not synchronized")
				}
			}
			access := func(*ownership.Catalog) ownership.Access { return ownership.Unrestricted() }
			if got := CachedStrimziEvidence(pod, nil, access); got == nil || len(got.Findings) != 1 || got.Source.Name != "connect" {
				t.Fatalf("cached owner chain lost: %+v", got)
			}
			restricted := func(*ownership.Catalog) ownership.Access {
				return strimziOwnerAccess{Access: ownership.Unrestricted(), denied: denied}
			}
			if got := CachedStrimziEvidence(pod, nil, restricted); got != nil {
				t.Fatalf("withheld owner exposed: %+v", got)
			}
			previous := connect.DeepCopy()
			previous.SetUID("another-cluster-connect")
			if got := CachedStrimziEvidence(previous, nil, access); got != nil {
				t.Fatal("attached current findings to a different subject incarnation")
			}
			if got := CachedStrimziEvidence(connector, nil, access); got != nil {
				t.Fatal("direct connector invented unavailable related findings")
			}
			switchCache := func(Ref) bool { k8s.ResetTestDynamicState(); return true }
			if got := CachedStrimziEvidence(connect, switchCache, access); got != nil {
				t.Fatal("context replacement during enrichment kept findings")
			}
		})
	}
}
