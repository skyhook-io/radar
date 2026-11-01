package k8s

import (
	"fmt"
	"testing"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/resourceid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func operatorChildrenWorld() (ownershiptest.World, ownership.Observation) {
	root := ownershiptest.Object{Ref: resourceid.Ref{Group: "opentelemetry.io", Kind: "OpenTelemetryCollector", Namespace: "observe", Name: "collector"}, APIVersion: "opentelemetry.io/v1beta1", UID: "root"}
	w := ownershiptest.World{Objects: []ownershiptest.Object{root}, Collection: map[resourceid.GroupKind]ownershiptest.Collection{}}
	for _, r := range []ownership.ServedResource{
		{Group: "opentelemetry.io", Version: "v1beta1", Resource: "opentelemetrycollectors", Kind: "OpenTelemetryCollector", Namespaced: true},
		{Group: "apps", Version: "v1", Resource: "deployments", Kind: "Deployment", Namespaced: true},
		{Group: "apps", Version: "v1", Resource: "statefulsets", Kind: "StatefulSet", Namespaced: true},
		{Group: "apps", Version: "v1", Resource: "daemonsets", Kind: "DaemonSet", Namespaced: true},
		{Version: "v1", Resource: "configmaps", Kind: "ConfigMap", Namespaced: true},
		{Group: "example.io", Version: "v1", Resource: "widgets", Kind: "Widget", Namespaced: true},
	} {
		r.Verbs = []string{"get", "list", "watch", "delete"}
		w.Resources = append(w.Resources, r)
	}
	w.Collection[resourceid.GroupKind{Group: "example.io", Kind: "Widget"}] = ownershiptest.Collection{State: ownershiptest.NotCollected}
	for n := range 600 {
		w.Objects = append(w.Objects, ownershiptest.Object{Ref: resourceid.Ref{Kind: "ConfigMap", Namespace: "observe", Name: fmt.Sprintf("config-%03d", n)}, APIVersion: "v1", UID: types.UID(fmt.Sprintf("config-%d", n)), OwnerReferences: []metav1.OwnerReference{root.OwnerRef(true)}})
	}
	for n := range 5 {
		w.Objects = append(w.Objects, ownershiptest.Object{Ref: resourceid.Ref{Group: "apps", Kind: "Deployment", Namespace: "observe", Name: fmt.Sprintf("runtime-%d", n)}, APIVersion: "apps/v1", UID: types.UID(fmt.Sprintf("runtime-%d", n)), OwnerReferences: []metav1.OwnerReference{root.OwnerRef(true)}})
	}
	return w, ownership.Observation{Ref: root.Ref, UID: root.UID}
}

func TestOperatorWorkloadsAreSelectedBeforeGenericPreviewLimits(t *testing.T) {
	w, root := operatorChildrenWorld()
	got := operatorChildrenFromSource(ownershiptest.Scan(w), root, func(*ownership.Catalog) ownership.Access { return ownership.Unrestricted() })
	if got.Unavailable != "" || got.Context == nil || len(got.Context.Items) != 25 || !got.Context.Truncated || got.Context.Complete {
		t.Fatalf("wrong general child coverage: %+v", got)
	}
	if len(got.Workloads) != 4 || got.Workloads[0].Name != "runtime-0" || got.Context.Items[0].Kind != "Deployment" {
		t.Fatalf("600 configs displaced runtime: %+v", got)
	}
	if !got.WorkloadsComplete {
		t.Fatal("uncollected unrelated Widget kind made runtime evidence unknown")
	}
	w.Collection[resourceid.GroupKind{Group: "apps", Kind: "DaemonSet"}] = ownershiptest.Collection{State: ownershiptest.Loading}
	got = operatorChildrenFromSource(ownershiptest.Scan(w), root, func(*ownership.Catalog) ownership.Access { return ownership.Unrestricted() })
	if got.WorkloadsComplete {
		t.Fatal("loading apps inventory treated as complete")
	}
}

func TestOperatorChildrenRefuseAReplacedRoot(t *testing.T) {
	w, root := operatorChildrenWorld()
	w.Objects[0].UID = "replacement"
	got := operatorChildrenFromSource(ownershiptest.Scan(w), root, func(*ownership.Catalog) ownership.Access { return ownership.Unrestricted() })
	if got.Context != nil || len(got.Workloads) > 0 || got.Unavailable == "" {
		t.Fatalf("old incarnation got current children: %+v", got)
	}
}

func TestOperatorNonControllerChildrenDoNotConsumeRuntimeSlots(t *testing.T) {
	w, root := operatorChildrenWorld()
	for i := range w.Objects {
		if w.Objects[i].Ref.Group == "apps" && w.Objects[i].Ref.Name != "runtime-4" {
			no := false
			w.Objects[i].OwnerReferences[0].Controller = &no
		}
	}
	got := operatorChildrenFromSource(ownershiptest.Scan(w), root, func(*ownership.Catalog) ownership.Access { return ownership.Unrestricted() })
	if len(got.Workloads) != 1 || got.Workloads[0].Name != "runtime-4" {
		t.Fatalf("co-owners consumed runtime slots: %+v", got.Workloads)
	}
	if len(got.Context.Items) != 25 {
		t.Fatal("general direct-child preview lost co-owned resources")
	}
}

func TestOperatorDeniedWorkloadInventoryIsNotAnEmptyRuntimeVerdict(t *testing.T) {
	w, root := operatorChildrenWorld()
	got := operatorChildrenFromSource(ownershiptest.Scan(w), root, func(*ownership.Catalog) ownership.Access {
		return liveManagerAccess{Access: ownership.Unrestricted(), denied: resourceid.GroupKind{Group: "apps", Kind: "Deployment"}}
	})
	if got.WorkloadsComplete || len(got.Workloads) != 0 {
		t.Fatalf("denied inventory looked complete: %+v", got)
	}
}
