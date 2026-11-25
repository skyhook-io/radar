package prometheus

import (
	"encoding/json"
	"math"
	"os"
	"slices"
	"testing"

	"github.com/skyhook-io/radar/pkg/rightsizing"
	corev1 "k8s.io/api/core/v1"
)

func TestRightsizingDecisionParity(t *testing.T) {
	raw, err := os.ReadFile("../../pkg/rightsizing/testdata/decisions.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, Reason, WantTarget string
		QoS                      corev1.PodQOSClass
		WantStatus               rightsizing.DecisionStatus
		Demand, WantChange       float64
		HPAKnown, Peak, Idle     bool
		Samples                  int
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			samples := tc.Samples
			if samples == 0 {
				samples = 2016
			}
			replicas := 2
			if tc.Idle {
				replicas = 0
			}
			request := mustQuantity(t, "100m")
			scope := rightsizing.Scope{Replicas: replicas, HPAEvidenceAvailable: tc.HPAKnown, Pods: []rightsizing.Pod{{QOSClass: tc.QoS}},
				PodSpec: corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: *request},
				}}}}}
			e := rightsizing.ContainerEvidence{CPU95: &tc.Demand, CPUSamples: samples}
			results := map[string]queryOutcome{"cpu_stat": {values: map[string]float64{"api": tc.Demand}}, "cpu_coverage": {values: map[string]float64{"api": float64(samples)}}}
			if tc.Peak {
				e.CPU99 = &tc.Demand
				results["cpu_peak"] = queryOutcome{values: map[string]float64{"api": tc.Demand}}
			}
			workload := rightsizingWorkload{hpaAvailable: tc.HPAKnown, scaledToZero: tc.Idle, replicas: replicas}
			workload.qos.Observe(tc.QoS)
			promRows := []RightsizingRow{buildRightsizingRow(containerSpec{name: "api", cpuReq: request}, "cpu", 2016, OwnerCoverageCurrentPods, workload, results)}
			rightsizing.Finalize(promRows, &scope.PodSpec, workload.qos, workload.scaledToZero)
			ddRows := rightsizing.Evaluate(scope, map[string]rightsizing.ContainerEvidence{"api": e})
			rows := []RightsizingRow{promRows[0], ddRows[0]}
			// The scan derives both CPU percentiles from the same bucket series.
			if tc.Peak {
				workload.containers = []containerSpec{{name: "api", cpuReq: request}}
				buckets := make([]float64, samples)
				for i := range buckets {
					buckets[i] = tc.Demand
				}
				key := scanKey{container: "api"}
				scan := buildScanWorkload(scanWorkload{workload: workload, replicas: replicas}, scanBatchEvidence{cpu: map[scanKey][]float64{key: buckets}})
				rows = append(rows, scan.Rows[0])
			}
			for _, row := range rows {
				target := ""
				if row.RecommendedReq != nil {
					target = *row.RecommendedReq
				}
				if row.Decision.Status != tc.WantStatus || target != tc.WantTarget || (tc.Reason != "" && !slices.Contains(row.Decision.Reasons, tc.Reason)) ||
					math.Abs(row.RequestChange(replicas)-tc.WantChange) > 1e-9 {
					t.Errorf("decision=%+v target=%s change=%g", row.Decision, target, row.RequestChange(replicas))
				}
			}
		})
	}
}

func TestLimitOnlyContainerUsesEffectiveReservation(t *testing.T) {
	limit := mustQuantity(t, "1")
	container := corev1.Container{Name: "api", Resources: corev1.ResourceRequirements{
		Limits: corev1.ResourceList{corev1.ResourceCPU: *limit},
	}}
	extracted := extractContainerSpec(container)
	if extracted.cpuReq == nil || extracted.cpuReq.Cmp(*limit) != 0 {
		t.Fatal("Prometheus lost the effective CPU request")
	}
	demand := .02
	scope := rightsizing.Scope{Replicas: 2, HPAEvidenceAvailable: true, Pods: []rightsizing.Pod{{QOSClass: corev1.PodQOSBurstable}},
		PodSpec: corev1.PodSpec{Containers: []corev1.Container{container}}}
	row := rightsizing.Evaluate(scope, map[string]rightsizing.ContainerEvidence{"api": {CPU95: &demand, CPU99: &demand, CPUSamples: 2016}})[0]
	if row.Fit != FitOversized || row.Decision.Status != rightsizing.DecisionActionable || row.RequestChange(2) >= 0 {
		t.Fatalf("unexpected fit, decision and impact: %v %+v %v", row.Fit, row.Decision, row.RequestChange(2))
	}
	if container.Resources.Requests != nil {
		t.Fatal("evaluating requests mutated the cached template")
	}
}
