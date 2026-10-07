package resourcecontext

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/skyhook-io/radar/pkg/topology"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"strings"
	"testing"
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
