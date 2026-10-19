package resourcecontext

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/pkg/topology"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

type nodePlacementProvider struct {
	mockResourceProvider
	calls *int
	err   error
}

func (p nodePlacementProvider) Pods() ([]*corev1.Pod, error) {
	*p.calls++
	return p.pods, p.err
}

func TestBuild_NodePlacementReusesLookupAndMarksUnavailable(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker"}}
	for _, withTopology := range []bool{false, true} {
		for _, failed := range []bool{false, true} {
			calls := 0
			provider := nodePlacementProvider{calls: &calls, mockResourceProvider: mockResourceProvider{pods: []*corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "prod"}, Spec: corev1.PodSpec{NodeName: "worker"}}}}}
			if failed {
				provider.err = errors.New("pod source unavailable")
			}
			opts := Options{Tier: TierBasic, Provider: provider}
			if withTopology {
				opts.Topology = &topology.Topology{}
			}
			rc := Build(context.Background(), node, opts)
			if calls != 1 {
				t.Fatalf("topology=%v failed=%v: %d Pod scans", withTopology, failed, calls)
			}
			if failed {
				if rc.ReferencedBy != nil {
					t.Fatalf("failed source emitted refs: %+v", rc.ReferencedBy)
				}
				found := false
				for _, field := range rc.Omitted {
					if field.Field == "referencedBy" && field.Reason == OmittedUnavailable {
						found = true
					}
				}
				if !found {
					t.Fatalf("unavailability hidden: %+v", rc.Omitted)
				}
			} else if rc.ReferencedBy == nil || rc.ReferencedBy.Total != 1 {
				t.Fatalf("missing placement: %+v", rc)
			}
		}
	}

	calls := 0
	provider := nodePlacementProvider{calls: &calls}
	rc := Build(context.Background(), node, Options{Tier: TierBasic, Provider: provider, Topology: &topology.Topology{}})
	if calls != 1 || rc.ReferencedBy != nil || len(rc.Omitted) != 0 {
		t.Fatalf("empty observation: %d %+v", calls, rc)
	}

	rel := &topology.Relationships{PodPlacementObserved: true, Pods: []topology.ResourceRef{{Kind: "Pod", Namespace: "prod", Name: "app"}}}
	rc = Build(context.Background(), node, Options{Tier: TierBasic, Relationships: rel})
	if rc.ReferencedBy == nil || rc.ReferencedBy.Total != 1 {
		t.Fatalf("precomputed placement needs another provider: %+v", rc)
	}
	raw, err := json.Marshal(rel)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "Placement") || strings.Contains(string(raw), "placement") {
		t.Fatalf("internal lookup state exposed: %s", raw)
	}
}

func TestBuild_NodePlacementWithoutProviderIsUnavailable(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker"}}
	rc := Build(context.Background(), node, Options{Tier: TierBasic})
	if rc.ReferencedBy != nil || len(rc.Omitted) != 1 || rc.Omitted[0] != (OmittedField{Field: "referencedBy", Reason: OmittedUnavailable}) {
		t.Fatalf("missing provider context: %+v", rc)
	}
	rc = Build(context.Background(), node, Options{Tier: TierBasic, Relationships: &topology.Relationships{PodPlacementObserved: true}})
	if rc.ReferencedBy != nil || len(rc.Omitted) != 0 {
		t.Fatalf("observed empty placement without provider: %+v", rc)
	}
}

func TestBuild_NodePlacementTopologyGuardAndExclusions(t *testing.T) {
	for _, custom := range []bool{false, true} {
		calls := 0
		provider := nodePlacementProvider{calls: &calls, mockResourceProvider: mockResourceProvider{pods: []*corev1.Pod{
			nil,
			{ObjectMeta: metav1.ObjectMeta{Name: "assigned", Namespace: "prod"}, Spec: corev1.PodSpec{NodeName: "worker"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "finished", Namespace: "prod"}, Spec: corev1.PodSpec{NodeName: "worker"}, Status: corev1.PodStatus{Phase: corev1.PodSucceeded}},
			{ObjectMeta: metav1.ObjectMeta{Name: "failed", Namespace: "prod"}, Spec: corev1.PodSpec{NodeName: "worker"}, Status: corev1.PodStatus{Phase: corev1.PodFailed}},
			{ObjectMeta: metav1.ObjectMeta{Name: "unassigned", Namespace: "prod"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "prod"}, Spec: corev1.PodSpec{NodeName: "other"}},
		}}}
		var obj runtime.Object = &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker"}}
		if custom {
			obj = &unstructured.Unstructured{Object: map[string]any{"apiVersion": "custom.example/v1", "kind": "Node", "metadata": map[string]any{"name": "worker"}}}
		}
		rc := Build(context.Background(), obj, Options{Tier: TierBasic, Topology: &topology.Topology{}, Provider: provider})
		if custom {
			if calls != 0 || rc.ReferencedBy != nil {
				t.Fatalf("custom Node borrowed core placement: calls=%d context=%+v", calls, rc)
			}
		} else if calls != 1 || rc.ReferencedBy == nil || rc.ReferencedBy.Total != 1 || rc.ReferencedBy.Items[0].Name != "assigned" {
			t.Fatalf("core placement exclusions: calls=%d context=%+v", calls, rc)
		}
	}
}

func TestBuild_ReverseReferenceSourceFailureIsExplicit(t *testing.T) {
	for _, obj := range []runtime.Object{&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "settings", Namespace: "prod"}}, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "settings", Namespace: "prod"}}} {
		calls := 0
		provider := nodePlacementProvider{calls: &calls, err: errors.New("Pod source unavailable")}
		rc := Build(context.Background(), obj, Options{Tier: TierBasic, Provider: provider})
		if calls != 1 || rc.ReferencedBy != nil {
			t.Fatalf("failed reverse source: %d %+v", calls, rc)
		}
		found := false
		for _, field := range rc.Omitted {
			if field.Field == "referencedBy" && field.Reason == OmittedUnavailable {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing reverse availability: %+v", rc.Omitted)
		}
	}
}

type pendingPodInventoryProvider struct{ mockResourceProvider }

func (p pendingPodInventoryProvider) Pods() ([]*corev1.Pod, error) {
	return p.pods, p.InventoryError("pods", "")
}

func (p pendingPodInventoryProvider) InventoryError(resource, _ string) error {
	if resource == "pods" {
		return &topology.InventoryError{Resource: "pods", State: topology.InventoryPending}
	}
	return nil
}

func TestBuild_NodePlacementRetainsVisiblePodsWhileInventoryLoads(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker"}}
	provider := pendingPodInventoryProvider{mockResourceProvider{pods: []*corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "prod"}, Spec: corev1.PodSpec{NodeName: "worker"}}}}}
	for _, withTopology := range []bool{false, true} {
		opts := Options{Tier: TierBasic, Provider: provider}
		if withTopology {
			opts.Topology = &topology.Topology{}
		}
		rc := Build(context.Background(), node, opts)
		if rc.ReferencedBy == nil || rc.ReferencedBy.Total != 1 {
			t.Fatalf("topology=%v: visible Pod dropped while loading: %+v", withTopology, rc.ReferencedBy)
		}
		found := false
		for _, field := range rc.Omitted {
			if field.Field == "referencedBy" && field.Reason == OmittedCacheCold {
				found = true
			}
		}
		if !found {
			t.Fatalf("topology=%v: loading inventory not reported: %+v", withTopology, rc.Omitted)
		}
	}
}
