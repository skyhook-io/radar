package rightsizing

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestEvaluateContainerEvidence(t *testing.T) {
	cpu, memory := 0.02, float64(80<<20)
	scope := Scope{HPAEvidenceAvailable: true, PodSpec: corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("512Mi")}}}}}}
	e := ContainerEvidence{CPU95: &cpu, CPU99: &cpu, MemoryMax: &memory, CPUSamples: 2016, MemorySamples: 2016}
	for _, tc := range []struct {
		name     string
		change   func(*Scope, *ContainerEvidence)
		cpu, mem string
	}{
		{"missing restart evidence", func(*Scope, *ContainerEvidence) {}, "150m", ""},
		{"verified restarts", func(_ *Scope, e *ContainerEvidence) { e.OOMEvidenceAvailable = true }, "150m", "256Mi"},
		{"observed OOM", func(_ *Scope, e *ContainerEvidence) { e.OOMEvidenceAvailable = true; e.WindowOOM = true }, "150m", ""},
		{"autoscaled CPU", func(s *Scope, _ *ContainerEvidence) { s.HPAManaged = map[string]bool{"cpu": true} }, "", ""},
		{"short series", func(_ *Scope, e *ContainerEvidence) { e.CPUSamples = 71 }, "", ""},
		{"missing peak", func(_ *Scope, e *ContainerEvidence) { e.CPU99 = nil }, "150m", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, e := scope, e
			tc.change(&s, &e)
			rows := Evaluate(s, map[string]ContainerEvidence{"api": e})
			if len(rows) != 2 {
				t.Fatal(rows)
			}
			for i, want := range []string{tc.cpu, tc.mem} {
				got := ""
				if rows[i].RecommendedReq != nil {
					got = *rows[i].RecommendedReq
				}
				if want != got {
					t.Errorf("%s: %q want %q (%#v)", rows[i].Resource, got, want, rows[i])
				}
			}
		})
	}
}

func TestMemoryMinimumBasis(t *testing.T) {
	request := resource.MustParse("24Mi")
	for _, tc := range []struct {
		observedMi float64
		minimum    bool
	}{{27, true}, {56, false}, {80, false}} {
		row := Row{Resource: "memory"}
		ClassifyRightsizingFit(&row, tc.observedMi*(1<<20), &request, nil, "memory")
		if row.RecommendedReq == nil || row.CalculatedAtMinimum != tc.minimum {
			t.Errorf("observed %vMi: target %v, calculatedAtMinimum %v", tc.observedMi, row.RecommendedReq, row.CalculatedAtMinimum)
		}
	}
}
