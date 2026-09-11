package k8score

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestPublicMetricsEntryPointsUseV1Beta1(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	dyn.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetResource() != PodMetricsGVR {
			t.Fatalf("pod metrics GVR = %v, want %v", action.GetResource(), PodMetricsGVR)
		}
		return true, &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "api", "namespace": "default"}}}, nil
	})
	dyn.PrependReactor("get", "nodes", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetResource() != NodeMetricsGVR {
			t.Fatalf("node metrics GVR = %v, want %v", action.GetResource(), NodeMetricsGVR)
		}
		return true, &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "worker"}}}, nil
	})

	if _, err := GetPodMetrics(context.Background(), dyn, "default", "api"); err != nil {
		t.Fatalf("GetPodMetrics: %v", err)
	}
	if _, err := GetNodeMetrics(context.Background(), dyn, "worker"); err != nil {
		t.Fatalf("GetNodeMetrics: %v", err)
	}

	store := NewMetricsHistoryStore(dyn)
	if got, ok := store.metricsGVR("pods"); !ok || got != PodMetricsGVR {
		t.Fatalf("default pod metrics GVR = %v, ok=%v", got, ok)
	}
	if got, ok := store.metricsGVR("nodes"); !ok || got != NodeMetricsGVR {
		t.Fatalf("default node metrics GVR = %v, ok=%v", got, ok)
	}
}

func TestMetricsEntryPointsAcceptDiscoveredGVR(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: MetricsAPIGroup, Version: "v1", Resource: "pods"}
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	dyn.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetResource() != gvr {
			t.Fatalf("pod metrics GVR = %v, want %v", action.GetResource(), gvr)
		}
		return true, &unstructured.Unstructured{}, nil
	})

	if _, err := GetPodMetricsWithGVR(context.Background(), dyn, gvr, "default", "api"); err != nil {
		t.Fatalf("GetPodMetricsWithGVR: %v", err)
	}
}
