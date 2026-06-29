package hpadiag

import (
	"fmt"
	"strings"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
)

type State string

const (
	StateOK                 State = "ok"
	StateScalingUp          State = "scaling_up"
	StateScalingDown        State = "scaling_down"
	StateLimitedMax         State = "limited_max"
	StateLimitedMin         State = "limited_min"
	StateMetricsUnavailable State = "metrics_unavailable"
	StateMetricsIncomplete  State = "metrics_incomplete"
	StateUnableToScale      State = "unable_to_scale"
	StateDisabled           State = "disabled"
	StatePinned             State = "pinned"
	StateStale              State = "stale"
	StateStabilized         State = "stabilized"
	StateUnknown            State = "unknown"
)

type ReasonID string

const (
	ReasonScalingUp            ReasonID = "scaling_up"
	ReasonScalingDown          ReasonID = "scaling_down"
	ReasonLimitedMax           ReasonID = "limited_max"
	ReasonLimitedMin           ReasonID = "limited_min"
	ReasonMetricsUnavailable   ReasonID = "metrics_unavailable"
	ReasonUnableToScale        ReasonID = "unable_to_scale"
	ReasonScalingDisabled      ReasonID = "scaling_disabled"
	ReasonPinned               ReasonID = "pinned"
	ReasonStaleStatus          ReasonID = "stale_status"
	ReasonScaleDownStabilized  ReasonID = "scale_down_stabilized"
	ReasonMissingCurrentMetric ReasonID = "missing_current_metric"
)

type Diagnosis struct {
	State   State           `json:"state"`
	Summary string          `json:"summary"`
	Target  TargetRef       `json:"target"`
	Bounds  ReplicaBounds   `json:"bounds"`
	Metrics []MetricSummary `json:"metrics,omitempty"`
	Reasons []Reason        `json:"reasons,omitempty"`
}

type TargetRef struct {
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Name       string `json:"name,omitempty"`
}

type ReplicaBounds struct {
	Min                int32 `json:"min"`
	Max                int32 `json:"max"`
	Current            int32 `json:"current"`
	Desired            int32 `json:"desired"`
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	Generation         int64 `json:"generation,omitempty"`
}

type Reason struct {
	ID              ReasonID `json:"id"`
	Message         string   `json:"message"`
	Detail          string   `json:"detail,omitempty"`
	ConditionType   string   `json:"conditionType,omitempty"`
	ConditionReason string   `json:"conditionReason,omitempty"`
}

type MetricSummary struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Current string `json:"current,omitempty"`
	Target  string `json:"target,omitempty"`
	Status  string `json:"status"`
}

func Analyze(hpa *autoscalingv2.HorizontalPodAutoscaler) *Diagnosis {
	if hpa == nil {
		return nil
	}

	min := int32(1)
	if hpa.Spec.MinReplicas != nil {
		min = *hpa.Spec.MinReplicas
	}
	observedGeneration := int64(0)
	if hpa.Status.ObservedGeneration != nil {
		observedGeneration = *hpa.Status.ObservedGeneration
	}

	d := &Diagnosis{
		State: StateOK,
		Target: TargetRef{
			APIVersion: hpa.Spec.ScaleTargetRef.APIVersion,
			Kind:       hpa.Spec.ScaleTargetRef.Kind,
			Name:       hpa.Spec.ScaleTargetRef.Name,
		},
		Bounds: ReplicaBounds{
			Min:                min,
			Max:                hpa.Spec.MaxReplicas,
			Current:            hpa.Status.CurrentReplicas,
			Desired:            hpa.Status.DesiredReplicas,
			ObservedGeneration: observedGeneration,
			Generation:         hpa.Generation,
		},
	}

	conditions := mapConditions(hpa.Status.Conditions)

	if cond, ok := conditions[autoscalingv2.AbleToScale]; ok && cond.Status == corev1.ConditionFalse {
		d.addConditionReason(ReasonUnableToScale, cond, "HPA controller cannot scale the target")
	}

	if cond, ok := conditions[autoscalingv2.ScalingActive]; ok && cond.Status == corev1.ConditionFalse {
		if isScalingDisabled(cond) {
			d.addConditionReason(ReasonScalingDisabled, cond, "HPA scaling is disabled because the target has zero replicas")
		} else {
			d.addConditionReason(ReasonMetricsUnavailable, cond, "HPA controller cannot read scaling metrics")
		}
	}

	if cond, ok := conditions[autoscalingv2.ScalingLimited]; ok && cond.Status == corev1.ConditionTrue {
		reason := strings.ToLower(cond.Reason)
		message := strings.ToLower(cond.Message)
		switch {
		case isPinned(min, hpa.Spec.MaxReplicas) && (strings.Contains(reason, "toomany") || strings.Contains(reason, "toofew") || strings.Contains(message, "maximum") || strings.Contains(message, "minimum")):
			d.addConditionReason(ReasonPinned, cond, fmt.Sprintf("HPA is pinned at %d replicas", hpa.Spec.MaxReplicas))
		case strings.Contains(reason, "toomany") || strings.Contains(message, "maximum"):
			d.addConditionReason(ReasonLimitedMax, cond, fmt.Sprintf("HPA is capped at maxReplicas=%d", hpa.Spec.MaxReplicas))
		case strings.Contains(reason, "toofew") || strings.Contains(message, "minimum"):
			d.addConditionReason(ReasonLimitedMin, cond, fmt.Sprintf("HPA is held at minReplicas=%d", min))
		case strings.Contains(reason, "stabiliz") || strings.Contains(message, "stabiliz"):
			d.addConditionReason(ReasonScaleDownStabilized, cond, "HPA is holding replicas because of scale-down stabilization")
		}
	}

	if hpa.Generation > 0 && observedGeneration > 0 && observedGeneration < hpa.Generation {
		d.Reasons = append(d.Reasons, Reason{
			ID:      ReasonStaleStatus,
			Message: "HPA status has not observed the latest spec generation yet",
			Detail:  fmt.Sprintf("observed generation %d, current generation %d", observedGeneration, hpa.Generation),
		})
	}

	switch {
	case hpa.Status.DesiredReplicas > hpa.Status.CurrentReplicas:
		d.Reasons = append(d.Reasons, Reason{
			ID:      ReasonScalingUp,
			Message: fmt.Sprintf("Scaling up from %d to %d replicas", hpa.Status.CurrentReplicas, hpa.Status.DesiredReplicas),
		})
	case hpa.Status.DesiredReplicas < hpa.Status.CurrentReplicas:
		d.Reasons = append(d.Reasons, Reason{
			ID:      ReasonScalingDown,
			Message: fmt.Sprintf("Scaling down from %d to %d replicas", hpa.Status.CurrentReplicas, hpa.Status.DesiredReplicas),
		})
	}

	d.Metrics = summarizeMetrics(hpa)
	if len(hpa.Status.CurrentMetrics) > 0 {
		if missing := missingMetricNames(d.Metrics); len(missing) > 0 && !d.hasReason(ReasonMetricsUnavailable) {
			d.Reasons = append(d.Reasons, Reason{
				ID:      ReasonMissingCurrentMetric,
				Message: "HPA status is missing current values for one or more configured metrics",
				Detail:  strings.Join(missing, ", "),
			})
		}
	}
	if isPinned(min, hpa.Spec.MaxReplicas) &&
		hpa.Status.CurrentReplicas == hpa.Status.DesiredReplicas &&
		hpa.Status.DesiredReplicas == hpa.Spec.MaxReplicas &&
		!d.hasReason(ReasonPinned) {
		d.Reasons = append(d.Reasons, Reason{
			ID:      ReasonPinned,
			Message: fmt.Sprintf("HPA is pinned at %d replicas", hpa.Spec.MaxReplicas),
		})
	}

	d.State = chooseState(d)
	d.Summary = summarizeState(d)
	return d
}

func mapConditions(conditions []autoscalingv2.HorizontalPodAutoscalerCondition) map[autoscalingv2.HorizontalPodAutoscalerConditionType]autoscalingv2.HorizontalPodAutoscalerCondition {
	out := make(map[autoscalingv2.HorizontalPodAutoscalerConditionType]autoscalingv2.HorizontalPodAutoscalerCondition, len(conditions))
	for _, cond := range conditions {
		out[cond.Type] = cond
	}
	return out
}

func isScalingDisabled(cond autoscalingv2.HorizontalPodAutoscalerCondition) bool {
	return strings.EqualFold(cond.Reason, "ScalingDisabled") || strings.Contains(strings.ToLower(cond.Message), "scaling is disabled")
}

func isPinned(min, max int32) bool {
	return max > 0 && min == max
}

func (d *Diagnosis) addConditionReason(id ReasonID, cond autoscalingv2.HorizontalPodAutoscalerCondition, fallback string) {
	message := cond.Message
	if message == "" {
		message = fallback
	}
	d.Reasons = append(d.Reasons, Reason{
		ID:              id,
		Message:         message,
		ConditionType:   string(cond.Type),
		ConditionReason: cond.Reason,
	})
}

func (d *Diagnosis) hasReason(id ReasonID) bool {
	for _, reason := range d.Reasons {
		if reason.ID == id {
			return true
		}
	}
	return false
}

func chooseState(d *Diagnosis) State {
	switch {
	case d.hasReason(ReasonUnableToScale):
		return StateUnableToScale
	case d.hasReason(ReasonMetricsUnavailable):
		return StateMetricsUnavailable
	case d.hasReason(ReasonLimitedMax):
		return StateLimitedMax
	case d.hasReason(ReasonScalingDisabled):
		return StateDisabled
	case d.hasReason(ReasonPinned):
		return StatePinned
	case d.hasReason(ReasonScalingUp):
		return StateScalingUp
	case d.hasReason(ReasonScalingDown):
		return StateScalingDown
	case d.hasReason(ReasonScaleDownStabilized):
		return StateStabilized
	case d.hasReason(ReasonLimitedMin):
		return StateLimitedMin
	case d.hasReason(ReasonMissingCurrentMetric):
		return StateMetricsIncomplete
	case d.hasReason(ReasonStaleStatus):
		return StateStale
	default:
		return StateOK
	}
}

func summarizeState(d *Diagnosis) string {
	switch d.State {
	case StateUnableToScale:
		return "HPA cannot read or update the target scale"
	case StateMetricsUnavailable:
		if metric := missingRequestMetric(d); metric != "" {
			return fmt.Sprintf("Add %s requests to the target pods so HPA can compute replicas", metric)
		}
		return "HPA cannot compute replicas because required metrics are unavailable"
	case StateMetricsIncomplete:
		if detail := firstReasonDetail(d, ReasonMissingCurrentMetric); detail != "" {
			return fmt.Sprintf("HPA is missing current metric values for %s", detail)
		}
		return "HPA is missing current metric values"
	case StateLimitedMax:
		if d.Bounds.Max > 0 {
			if controllerReportedMaxLimit(d) {
				return fmt.Sprintf("HPA wants more replicas but is capped at maxReplicas=%d", d.Bounds.Max)
			}
			return fmt.Sprintf("HPA is at maxReplicas=%d", d.Bounds.Max)
		}
		return "HPA is capped at maxReplicas"
	case StateDisabled:
		return "HPA scaling is disabled because the target has zero replicas"
	case StatePinned:
		if d.Bounds.Max > 0 {
			return fmt.Sprintf("HPA is configured for a fixed replica count of %d", d.Bounds.Max)
		}
		return "HPA is configured for a fixed replica count"
	case StateLimitedMin:
		return fmt.Sprintf("HPA is holding at minReplicas=%d", d.Bounds.Min)
	case StateStale:
		return "HPA has not observed the latest spec generation yet"
	case StateScalingUp:
		return firstReasonMessage(d, ReasonScalingUp, "HPA is scaling up")
	case StateScalingDown:
		return firstReasonMessage(d, ReasonScalingDown, "HPA is scaling down")
	case StateStabilized:
		return "HPA is holding replicas during scale-down stabilization"
	case StateOK:
		return "HPA is within configured bounds"
	default:
		return "HPA status is unknown"
	}
}

func firstReasonMessage(d *Diagnosis, id ReasonID, fallback string) string {
	for _, reason := range d.Reasons {
		if reason.ID == id && reason.Message != "" {
			return reason.Message
		}
	}
	return fallback
}

func firstReasonDetail(d *Diagnosis, id ReasonID) string {
	for _, reason := range d.Reasons {
		if reason.ID == id && reason.Detail != "" {
			return reason.Detail
		}
	}
	return ""
}

func missingRequestMetric(d *Diagnosis) string {
	message := strings.ToLower(firstReasonMessage(d, ReasonMetricsUnavailable, ""))
	const marker = "missing request for "
	idx := strings.Index(message, marker)
	if idx < 0 {
		return ""
	}
	rest := strings.TrimSpace(message[idx+len(marker):])
	if rest == "" {
		return ""
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return ""
	}
	metric := strings.Trim(fields[0], `.,;:()[]{}"'`)
	return formatMetricName(metric)
}

func formatMetricName(name string) string {
	switch strings.ToLower(name) {
	case "cpu":
		return "CPU"
	case "memory":
		return "memory"
	default:
		return name
	}
}

func controllerReportedMaxLimit(d *Diagnosis) bool {
	for _, reason := range d.Reasons {
		if reason.ID != ReasonLimitedMax {
			continue
		}
		conditionReason := strings.ToLower(reason.ConditionReason)
		message := strings.ToLower(reason.Message)
		if strings.Contains(conditionReason, "toomany") || strings.Contains(message, "maximum") {
			return true
		}
	}
	return false
}

func missingMetricNames(metrics []MetricSummary) []string {
	var out []string
	for _, metric := range metrics {
		if metric.Status == "missing" {
			out = append(out, metric.Name)
		}
	}
	return out
}
