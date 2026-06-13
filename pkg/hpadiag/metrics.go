package hpadiag

import (
	"fmt"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
)

func summarizeMetrics(hpa *autoscalingv2.HorizontalPodAutoscaler) []MetricSummary {
	if hpa == nil || (len(hpa.Spec.Metrics) == 0 && len(hpa.Status.CurrentMetrics) == 0) {
		return nil
	}

	currentByKey := make(map[string]autoscalingv2.MetricStatus, len(hpa.Status.CurrentMetrics))
	for _, current := range hpa.Status.CurrentMetrics {
		currentByKey[metricStatusKey(current)] = current
	}

	seen := make(map[string]struct{}, len(hpa.Spec.Metrics))
	out := make([]MetricSummary, 0, len(hpa.Spec.Metrics))
	for _, spec := range hpa.Spec.Metrics {
		key := metricSpecKey(spec)
		seen[key] = struct{}{}
		current, ok := currentByKey[key]
		status := "ok"
		currentValue := ""
		if ok {
			currentValue = formatMetricCurrent(current)
		}
		if !ok || currentValue == "" {
			status = "missing"
		}
		out = append(out, MetricSummary{
			Type:    string(spec.Type),
			Name:    metricSpecName(spec),
			Current: currentValue,
			Target:  formatMetricTarget(spec),
			Status:  status,
		})
	}

	for _, current := range hpa.Status.CurrentMetrics {
		key := metricStatusKey(current)
		if _, ok := seen[key]; ok {
			continue
		}
		name := metricStatusName(current)
		currentValue := formatMetricCurrent(current)
		if name == "unknown" && currentValue == "" {
			continue
		}
		out = append(out, MetricSummary{
			Type:    string(current.Type),
			Name:    name,
			Current: currentValue,
			Status:  "status_only",
		})
	}

	return out
}

func metricSpecKey(metric autoscalingv2.MetricSpec) string {
	return string(metric.Type) + "/" + metricSpecName(metric)
}

func metricStatusKey(metric autoscalingv2.MetricStatus) string {
	return string(metric.Type) + "/" + metricStatusName(metric)
}

func metricSpecName(metric autoscalingv2.MetricSpec) string {
	switch metric.Type {
	case autoscalingv2.ResourceMetricSourceType:
		if metric.Resource != nil {
			return metric.Resource.Name.String()
		}
	case autoscalingv2.ContainerResourceMetricSourceType:
		if metric.ContainerResource != nil {
			return fmt.Sprintf("%s/%s", metric.ContainerResource.Container, metric.ContainerResource.Name.String())
		}
	case autoscalingv2.PodsMetricSourceType:
		if metric.Pods != nil {
			return metric.Pods.Metric.Name
		}
	case autoscalingv2.ObjectMetricSourceType:
		if metric.Object != nil {
			return fmt.Sprintf("%s/%s/%s", metric.Object.DescribedObject.Kind, metric.Object.DescribedObject.Name, metric.Object.Metric.Name)
		}
	case autoscalingv2.ExternalMetricSourceType:
		if metric.External != nil {
			return metric.External.Metric.Name
		}
	}
	return "unknown"
}

func metricStatusName(metric autoscalingv2.MetricStatus) string {
	switch metric.Type {
	case autoscalingv2.ResourceMetricSourceType:
		if metric.Resource != nil {
			return metric.Resource.Name.String()
		}
	case autoscalingv2.ContainerResourceMetricSourceType:
		if metric.ContainerResource != nil {
			return fmt.Sprintf("%s/%s", metric.ContainerResource.Container, metric.ContainerResource.Name.String())
		}
	case autoscalingv2.PodsMetricSourceType:
		if metric.Pods != nil {
			return metric.Pods.Metric.Name
		}
	case autoscalingv2.ObjectMetricSourceType:
		if metric.Object != nil {
			return fmt.Sprintf("%s/%s/%s", metric.Object.DescribedObject.Kind, metric.Object.DescribedObject.Name, metric.Object.Metric.Name)
		}
	case autoscalingv2.ExternalMetricSourceType:
		if metric.External != nil {
			return metric.External.Metric.Name
		}
	}
	return "unknown"
}

func formatMetricTarget(metric autoscalingv2.MetricSpec) string {
	switch metric.Type {
	case autoscalingv2.ResourceMetricSourceType:
		if metric.Resource != nil {
			return formatTarget(metric.Resource.Target)
		}
	case autoscalingv2.ContainerResourceMetricSourceType:
		if metric.ContainerResource != nil {
			return formatTarget(metric.ContainerResource.Target)
		}
	case autoscalingv2.PodsMetricSourceType:
		if metric.Pods != nil {
			return formatTarget(metric.Pods.Target)
		}
	case autoscalingv2.ObjectMetricSourceType:
		if metric.Object != nil {
			return formatTarget(metric.Object.Target)
		}
	case autoscalingv2.ExternalMetricSourceType:
		if metric.External != nil {
			return formatTarget(metric.External.Target)
		}
	}
	return ""
}

func formatTarget(target autoscalingv2.MetricTarget) string {
	switch target.Type {
	case autoscalingv2.UtilizationMetricType:
		if target.AverageUtilization != nil {
			return fmt.Sprintf("%d%% utilization", *target.AverageUtilization)
		}
	case autoscalingv2.ValueMetricType:
		if target.Value != nil {
			return target.Value.String()
		}
	case autoscalingv2.AverageValueMetricType:
		if target.AverageValue != nil {
			return target.AverageValue.String() + " average"
		}
	}
	return ""
}

func formatMetricCurrent(metric autoscalingv2.MetricStatus) string {
	switch metric.Type {
	case autoscalingv2.ResourceMetricSourceType:
		if metric.Resource != nil {
			return formatCurrent(metric.Resource.Current)
		}
	case autoscalingv2.ContainerResourceMetricSourceType:
		if metric.ContainerResource != nil {
			return formatCurrent(metric.ContainerResource.Current)
		}
	case autoscalingv2.PodsMetricSourceType:
		if metric.Pods != nil {
			return formatCurrent(metric.Pods.Current)
		}
	case autoscalingv2.ObjectMetricSourceType:
		if metric.Object != nil {
			return formatCurrent(metric.Object.Current)
		}
	case autoscalingv2.ExternalMetricSourceType:
		if metric.External != nil {
			return formatCurrent(metric.External.Current)
		}
	}
	return ""
}

func formatCurrent(current autoscalingv2.MetricValueStatus) string {
	if current.AverageUtilization != nil {
		return fmt.Sprintf("%d%% utilization", *current.AverageUtilization)
	}
	if current.AverageValue != nil {
		return current.AverageValue.String() + " average"
	}
	if current.Value != nil {
		return current.Value.String()
	}
	return ""
}
