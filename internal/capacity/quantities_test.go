package capacity

import (
	"math"
	"testing"

	"github.com/skyhook-io/radar/pkg/capacityapi"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestDifferenceCertainty(t *testing.T) {
	tests := []struct {
		name        string
		left, right capacityapi.Certainty
		want        capacityapi.Certainty
	}{
		{name: "exact minus exact", left: capacityapi.CertaintyExact, right: capacityapi.CertaintyExact, want: capacityapi.CertaintyExact},
		{name: "exact minus lower", left: capacityapi.CertaintyExact, right: capacityapi.CertaintyLowerBound, want: capacityapi.CertaintyUpperBound},
		{name: "lower minus exact", left: capacityapi.CertaintyLowerBound, right: capacityapi.CertaintyExact, want: capacityapi.CertaintyLowerBound},
		{name: "lower minus lower", left: capacityapi.CertaintyLowerBound, right: capacityapi.CertaintyLowerBound, want: capacityapi.CertaintyUnknown},
		{name: "upper minus exact", left: capacityapi.CertaintyUpperBound, right: capacityapi.CertaintyExact, want: capacityapi.CertaintyUpperBound},
		{name: "exact minus upper", left: capacityapi.CertaintyExact, right: capacityapi.CertaintyUpperBound, want: capacityapi.CertaintyLowerBound},
		{name: "upper minus lower", left: capacityapi.CertaintyUpperBound, right: capacityapi.CertaintyLowerBound, want: capacityapi.CertaintyUpperBound},
		{name: "lower minus upper", left: capacityapi.CertaintyLowerBound, right: capacityapi.CertaintyUpperBound, want: capacityapi.CertaintyLowerBound},
		{name: "upper minus upper", left: capacityapi.CertaintyUpperBound, right: capacityapi.CertaintyUpperBound, want: capacityapi.CertaintyUnknown},
		{name: "unknown left", left: capacityapi.CertaintyUnknown, right: capacityapi.CertaintyExact, want: capacityapi.CertaintyUnknown},
		{name: "unknown right", left: capacityapi.CertaintyExact, right: capacityapi.CertaintyUnknown, want: capacityapi.CertaintyUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := differenceCertainty(tt.left, tt.right); got != tt.want {
				t.Fatalf("differenceCertainty(%q, %q) = %q, want %q", tt.left, tt.right, got, tt.want)
			}
		})
	}
}

func TestResourceListDifferencesPreserveTheirDomains(t *testing.T) {
	configured := resourceList(map[corev1.ResourceName]string{
		corev1.ResourceCPU: "100", "nvidia.com/gpu": "8",
	})
	provisioned := resourceList(map[corev1.ResourceName]string{
		corev1.ResourceCPU: "60", corev1.ResourceMemory: "256Gi", corev1.ResourcePods: "100", "nvidia.com/gpu": "3",
	})

	headroom := subtractLeftResourceLists(configured, provisioned)
	if len(headroom) != 2 {
		t.Fatalf("configured headroom = %v, want only configured resource keys", headroom)
	}
	assertQuantity(t, headroom, corev1.ResourceCPU, "40")
	assertQuantity(t, headroom, "nvidia.com/gpu", "5")

	fullDifference := subtractResourceLists(configured, provisioned)
	assertQuantity(t, fullDifference, corev1.ResourceCPU, "40")
	assertQuantity(t, fullDifference, corev1.ResourceMemory, "-256Gi")
	assertQuantity(t, fullDifference, corev1.ResourcePods, "-100")
	assertQuantity(t, fullDifference, "nvidia.com/gpu", "5")
}

func TestQuantityObservationPreservesArbitraryResources(t *testing.T) {
	resources := resourceList(map[corev1.ResourceName]string{
		corev1.ResourceCPU:              "1250m",
		corev1.ResourceMemory:           "3Gi",
		corev1.ResourceEphemeralStorage: "100Gi",
		corev1.ResourcePods:             "17",
		"hugepages-2Mi":                 "64Mi",
		"nvidia.com/gpu":                "2",
		"example.com/fpga":              "3",
	})
	observation := QuantityObservation(resources, capacityapi.CertaintyExact, capacityapi.GranularityPerNode, capacityTestTime(), "fixture")
	if observation.Certainty != capacityapi.CertaintyExact || observation.Granularity != capacityapi.GranularityPerNode || len(observation.Resources) != len(resources) {
		t.Fatalf("QuantityObservation() = %+v", observation)
	}
	for name, want := range resources {
		text, ok := observation.Resources[string(name)]
		if !ok {
			t.Errorf("resource %q missing from vector", name)
			continue
		}
		got, err := resource.ParseQuantity(text)
		if err != nil || got.Cmp(want) != 0 {
			t.Errorf("resource %q = %q, want %q", name, text, want.String())
		}
	}
}

func TestLimitPressureUsesConfiguredResourcesOnly(t *testing.T) {
	limits := resourceList(map[corev1.ResourceName]string{
		corev1.ResourceCPU: "100", "nvidia.com/gpu": "8", "example.com/fpga": "0",
	})
	provisioned := resourceList(map[corev1.ResourceName]string{
		corev1.ResourceCPU: "80", corev1.ResourceMemory: "256Gi", "nvidia.com/gpu": "9", "example.com/fpga": "1",
	})
	pressure := limitPressure(provisioned, limits)
	if len(pressure) != 3 {
		t.Fatalf("limit pressure = %+v, want three configured resources", pressure)
	}
	if pressure[0].Resource != "cpu" || pressure[0].Percent == nil || math.Abs(*pressure[0].Percent-80) > 0.0001 || pressure[0].OverLimit {
		t.Errorf("CPU pressure = %+v", pressure[0])
	}
	if pressure[1].Resource != "example.com/fpga" || pressure[1].Percent != nil || !pressure[1].OverLimit {
		t.Errorf("zero-limit FPGA pressure = %+v", pressure[1])
	}
	if pressure[2].Resource != "nvidia.com/gpu" || pressure[2].Percent == nil || math.Abs(*pressure[2].Percent-112.5) > 0.0001 || !pressure[2].OverLimit {
		t.Errorf("GPU pressure = %+v", pressure[2])
	}
}

func TestMetricsResourceListUsesSchedulerQuantityUnits(t *testing.T) {
	resources := MetricsResourceList(750_000_000, 3*1024*1024*1024)
	assertQuantity(t, resources, corev1.ResourceCPU, "750m")
	assertQuantity(t, resources, corev1.ResourceMemory, "3Gi")
}
