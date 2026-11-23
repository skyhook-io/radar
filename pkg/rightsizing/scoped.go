package rightsizing

import (
	"iter"
	"math"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// Scope binds live configuration and authorized Pod membership to one Kubernetes incarnation.
// PodSpec contains only names, resource requests/limits and native sidecar restart policies.
type Scope struct {
	ClusterUID           string            `json:"clusterUID"`
	WorkloadUID          string            `json:"workloadUID"`
	TemplateHash         string            `json:"templateHash"`
	CreatedAt            time.Time         `json:"createdAt"`
	Kind                 string            `json:"kind"`
	Namespace            string            `json:"namespace"`
	Name                 string            `json:"name"`
	Labels               map[string]string `json:"labels,omitempty"`
	Replicas             int               `json:"replicas"`
	PodSpec              corev1.PodSpec    `json:"podSpec"`
	Pods                 []Pod             `json:"pods"`
	HPAManaged           HPATargets        `json:"hpaManaged,omitempty"`
	HPAEvidenceAvailable bool              `json:"hpaEvidenceAvailable"`
	CurrentOOM           map[string]bool   `json:"currentOOM,omitempty"`
	ManagedBy            *WorkloadManager  `json:"managedBy,omitempty"`
}

// HPATargets records resource utilization targets. A bare resource applies to
// all containers; a container/resource key applies only to that container.
type HPATargets map[string]bool

func (targets HPATargets) Add(container, resource string) {
	if container == "" {
		targets[resource] = true
		return
	}
	targets[container+"/"+resource] = true
}

func (targets HPATargets) Controls(container, resource string) bool {
	return targets[resource] || targets[container+"/"+resource]
}

type Pod struct {
	QOSClass      corev1.PodQOSClass `json:"qosClass"`
	Name          string             `json:"name"`
	UID           string             `json:"uid"`
	CreatedAt     time.Time          `json:"createdAt"`
	RestartCounts map[string]int32   `json:"restartCounts"`
}

// RuntimeContainers yields the app containers and restartable init containers
// whose long-running resource use can be sized.
func RuntimeContainers(spec *corev1.PodSpec) iter.Seq[*corev1.Container] {
	return func(yield func(*corev1.Container) bool) {
		for i := range spec.Containers {
			if !yield(&spec.Containers[i]) {
				return
			}
		}
		for i := range spec.InitContainers {
			c := &spec.InitContainers[i]
			if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways && !yield(c) {
				return
			}
		}
	}
}

// ContainerEvidence records CPU in cores, memory in bytes and throttled periods as a ratio from 0 to 1.
// A nil observation means unknown; a non-nil zero is a measured value.
type ContainerEvidence struct {
	CPU95, CPU99, MemoryMax, ThrottleRatio *float64
	CPUSamples, MemorySamples              int
	WindowOOM, OOMEvidenceAvailable        bool
}

// Evaluate applies Radar's request policy to 5-minute observations for the named current Pods.
// CPU statistics and maximum memory are over the hottest replica in each complete time bucket.
func Evaluate(scope Scope, evidence map[string]ContainerEvidence) []Row {
	rows := make([]Row, 0, len(scope.PodSpec.Containers)*2)
	appendContainer := func(c corev1.Container) {
		e := evidence[c.Name]
		requests := EffectiveContainerRequests(c.Resources)
		for _, key := range []string{"cpu", "memory"} {
			row := Row{Container: c.Name, Resource: key, Fit: FitInsufficientHistory, Confidence: ConfidenceLow, ExpectedSamples: 7 * 24 * 12, HPAManaged: scope.HPAManaged.Controls(c.Name, key), HPAEvidenceAvailable: scope.HPAEvidenceAvailable}
			res := corev1.ResourceName(key)
			req, hasReq := requests[res]
			lim, hasLimit := c.Resources.Limits[res]
			var request, limit = &req, &lim
			if !hasReq {
				request = nil
			}
			if !hasLimit {
				limit = nil
			}
			SetCurrentQuantities(&row, request, limit, key)
			if q := FixedPodLevelRequest(&scope.PodSpec, res); q != nil {
				value, formatted := QuantityToFloat(*q, key), q.String()
				row.PodLevelRequestValue, row.PodLevelRequest = &value, &formatted
			}
			value, samples, stat := e.CPU95, e.CPUSamples, "P95"
			if key == "memory" {
				value, samples, stat = e.MemoryMax, e.MemorySamples, "Max"
				row.CurrentPodOOM, row.WindowOOMEvidence, row.OOMEvidenceAvailable = scope.CurrentOOM[c.Name], e.WindowOOM, e.OOMEvidenceAvailable
			} else {
				if finiteValue(e.CPU99) && finiteValue(value) {
					row.Peak = &ObservedStatistic{Name: "P99", Value: *e.CPU99, Formatted: FormatObservedValue(*e.CPU99, key)}
					row.Bursty = IsBurstyCPU(*value, *e.CPU99)
				}
				if finiteValue(e.ThrottleRatio) && *e.ThrottleRatio <= 1 {
					row.ThrottleAvailable, row.ThrottleRatio = true, e.ThrottleRatio
				}
			}
			if finiteValue(value) && samples > 0 && samples <= row.ExpectedSamples {
				row.Observed = &ObservedStatistic{Name: stat, Value: *value, Formatted: FormatObservedValue(*value, key)}
				row.SampleCount = samples
				row.Coverage = float64(samples) / float64(row.ExpectedSamples)
				row.Confidence = ConfidenceFor(samples, row.Coverage, OwnerCoverageCurrentPods)
				if samples >= RightsizingMinSamples {
					ClassifyRightsizingFit(&row, *value, request, limit, key)
				} else {
					row.RecommendationReason = "insufficient_history"
				}
				MarkPodLevelRequest(&row)
			}
			rows = append(rows, row)
		}
	}
	for c := range RuntimeContainers(&scope.PodSpec) {
		appendContainer(*c)
	}
	var qos PodQoS
	for _, pod := range scope.Pods {
		qos.Observe(pod.QOSClass)
	}
	Finalize(rows, &scope.PodSpec, qos, scope.Replicas == 0)
	return rows
}

func finiteValue(value *float64) bool {
	return value != nil && *value >= 0 && !math.IsNaN(*value) && !math.IsInf(*value, 0)
}

// WorkloadManager names what owns a workload's spec, so a request change is
// made at that source rather than patched live where it would be overwritten.
// Signal says which metadata produced the answer: labels get copied between
// templates, and a reader weighing a surprising owner needs to know why.
type WorkloadManager struct {
	Tool      string `json:"tool"`
	Kind      string `json:"kind,omitempty"`
	Group     string `json:"group,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
	Signal    string `json:"signal"`
}
