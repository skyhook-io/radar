package context

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// FormatPodMetrics renders current pod metrics with request/limit context as a string for LLM consumption.
// spec is optional — if nil, only current usage is shown (no request/limit comparison).
// A pod-level budget (spec.resources) is rendered on its own line, and a
// container that sets no value of its own for a resource is described as
// drawing on that budget rather than as having no request or limit.
func FormatPodMetrics(input *PodMetricsInput, spec *corev1.PodSpec) string {
	if input == nil || len(input.Containers) == 0 {
		return "No metrics available."
	}

	// Build a lookup of resource requests/limits from the pod spec
	specLookup := make(map[string]corev1.ResourceRequirements)
	var pod corev1.ResourceRequirements
	if spec != nil {
		for _, c := range spec.Containers {
			specLookup[c.Name] = c.Resources
		}
		for _, c := range spec.InitContainers {
			specLookup[c.Name] = c.Resources
		}
		if spec.Resources != nil {
			pod = *spec.Resources
		}
	}

	var b strings.Builder
	if budget := formatPodBudget(pod); budget != "" {
		b.WriteString("pod-level budget (shared by all containers): ")
		b.WriteString(budget)
		b.WriteByte('\n')
	}
	for _, cm := range input.Containers {
		b.WriteString(cm.Name)
		b.WriteString(": ")
		b.WriteString(formatMetricLine("CPU", cm.CPU, specLookup[cm.Name], pod, corev1.ResourceCPU))
		b.WriteString(", ")
		b.WriteString(formatMetricLine("Memory", cm.Memory, specLookup[cm.Name], pod, corev1.ResourceMemory))
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

func formatPodBudget(pod corev1.ResourceRequirements) string {
	var parts []string
	for _, r := range []struct {
		label string
		name  corev1.ResourceName
	}{{"CPU", corev1.ResourceCPU}, {"Memory", corev1.ResourceMemory}} {
		request, limit := requestAndLimit(pod, r.name)
		if request == "" && limit == "" {
			continue
		}
		parts = append(parts, r.label+requestLimitSuffix(request, limit))
	}
	return strings.Join(parts, ", ")
}

func formatMetricLine(metric string, currentValue string, reqs, pod corev1.ResourceRequirements, name corev1.ResourceName) string {
	request, limit := requestAndLimit(reqs, name)
	if request == "" && limit == "" {
		if podRequest, podLimit := requestAndLimit(pod, name); podRequest != "" || podLimit != "" {
			return fmt.Sprintf("%s %s (no container request/limit; uses the pod-level budget)", metric, currentValue)
		}
		return fmt.Sprintf("%s %s (no request/limit set)", metric, currentValue)
	}
	return metric + " " + currentValue + requestLimitSuffix(request, limit)
}

func requestAndLimit(reqs corev1.ResourceRequirements, name corev1.ResourceName) (request, limit string) {
	if q, ok := reqs.Requests[name]; ok {
		request = q.String()
	}
	if q, ok := reqs.Limits[name]; ok {
		limit = q.String()
	}
	return request, limit
}

func requestLimitSuffix(request, limit string) string {
	var out string
	if request != "" {
		out += "/req:" + request
	}
	if limit != "" {
		out += "/lim:" + limit
	}
	return out
}

func describeTrend(dataPoints []MetricsDataPoint) string {
	n := len(dataPoints)
	if n < 3 {
		return ""
	}

	// Compare average of last 3 vs first 3
	var recentCPU, recentMem, earlyMem, earlyCPU int64
	for i := range 3 {
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
