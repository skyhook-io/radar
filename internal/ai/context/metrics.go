package context

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// FormatPodMetrics renders current pod metrics with request/limit context as a string for LLM consumption.
// spec is optional — if nil, only current usage is shown (no request/limit comparison).
func FormatPodMetrics(input *PodMetricsInput, spec *corev1.PodSpec) string {
	if input == nil || len(input.Containers) == 0 {
		return "No metrics available."
	}

	// Build a lookup of resource requests/limits from the pod spec
	specLookup := make(map[string]corev1.ResourceRequirements)
	if spec != nil {
		for _, c := range spec.Containers {
			specLookup[c.Name] = c.Resources
		}
		for _, c := range spec.InitContainers {
			specLookup[c.Name] = c.Resources
		}
	}

	var b strings.Builder
	for _, cm := range input.Containers {
		b.WriteString(cm.Name)
		b.WriteString(": ")

		// CPU
		cpuStr := formatMetricLine("CPU", cm.CPU, specLookup[cm.Name])
		b.WriteString(cpuStr)

		// Memory
		memStr := formatMetricLine("Memory", cm.Memory, specLookup[cm.Name])
		b.WriteString(", ")
		b.WriteString(memStr)

		b.WriteByte('\n')
	}

	// Add trend info from history if available
	if input.History != nil {
		for _, ch := range input.History {
			if len(ch.DataPoints) < 3 {
				continue
			}
			trend := describeTrend(ch.DataPoints)
			if trend != "" {
				fmt.Fprintf(&b, "%s trend: %s\n", ch.Name, trend)
			}
		}
	}

	return b.String()
}

func formatMetricLine(metric string, currentValue string, reqs corev1.ResourceRequirements) string {
	var request, limit string

	if metric == "CPU" {
		if req, ok := reqs.Requests[corev1.ResourceCPU]; ok {
			request = req.String()
		}
		if lim, ok := reqs.Limits[corev1.ResourceCPU]; ok {
			limit = lim.String()
		}
	} else {
		if req, ok := reqs.Requests[corev1.ResourceMemory]; ok {
			request = req.String()
		}
		if lim, ok := reqs.Limits[corev1.ResourceMemory]; ok {
			limit = lim.String()
		}
	}

	if request == "" && limit == "" {
		return fmt.Sprintf("%s %s (no request/limit set)", metric, currentValue)
	}

	parts := fmt.Sprintf("%s %s", metric, currentValue)
	if request != "" {
		parts += fmt.Sprintf("/req:%s", request)
	}
	if limit != "" {
		parts += fmt.Sprintf("/lim:%s", limit)
	}

	return parts
}

func describeTrend(dataPoints []MetricsDataPoint) string {
	n := len(dataPoints)
	if n < 3 {
		return ""
	}

	// Compare average of last 3 vs first 3
	var recentCPU, recentMem, earlyMem, earlyCPU int64
	for i := 0; i < 3; i++ {
		earlyCPU += dataPoints[i].CPU
		earlyMem += dataPoints[i].Memory
		recentCPU += dataPoints[n-1-i].CPU
		recentMem += dataPoints[n-1-i].Memory
	}

	var trends []string
	if earlyCPU > 0 {
		cpuChange := float64(recentCPU-earlyCPU) / float64(earlyCPU) * 100
		if cpuChange > 50 {
			trends = append(trends, fmt.Sprintf("CPU rising (+%.0f%%)", cpuChange))
		} else if cpuChange < -50 {
			trends = append(trends, fmt.Sprintf("CPU falling (%.0f%%)", cpuChange))
		}
	}
	if earlyMem > 0 {
		memChange := float64(recentMem-earlyMem) / float64(earlyMem) * 100
		if memChange > 30 {
			trends = append(trends, fmt.Sprintf("Memory rising (+%.0f%%)", memChange))
		} else if memChange < -30 {
			trends = append(trends, fmt.Sprintf("Memory falling (%.0f%%)", memChange))
		}
	}

	if len(trends) == 0 {
		return ""
	}
	return strings.Join(trends, ", ")
}
