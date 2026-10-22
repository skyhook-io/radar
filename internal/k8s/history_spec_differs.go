package k8s

import (
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/cronsched"
)

// diffCronJob covers the schedule controls and the job template. Routine
// status (lastScheduleTime, the active list) is left out: it moves on every
// run, and each run is recorded on its own Job. Only the schedule updates
// that change CronJob health are kept — the first run, and a run that ends a
// stale gap.
func diffCronJob(oldObj, newObj any) ([]FieldChange, []string) {
	oldCJ, ok1 := oldObj.(*batchv1.CronJob)
	newCJ, ok2 := newObj.(*batchv1.CronJob)
	if !ok1 || !ok2 {
		return nil, nil
	}
	var s specChangeSet
	oldSpec, newSpec := oldCJ.Spec, newCJ.Spec
	s.add("spec.schedule", "schedule", oldSpec.Schedule, newSpec.Schedule)
	s.add("spec.timeZone", "timeZone", stringPtrValue(oldSpec.TimeZone), stringPtrValue(newSpec.TimeZone))
	if oldSuspend, newSuspend := boolPtrTrue(oldSpec.Suspend), boolPtrTrue(newSpec.Suspend); oldSuspend != newSuspend {
		s.changes = append(s.changes, FieldChange{Path: "spec.suspend", OldValue: oldSuspend, NewValue: newSuspend})
		if newSuspend {
			s.summary = append(s.summary, "suspended")
		} else {
			s.summary = append(s.summary, "resumed")
		}
	}
	s.add("spec.concurrencyPolicy", "concurrencyPolicy", string(oldSpec.ConcurrencyPolicy), string(newSpec.ConcurrencyPolicy))
	s.add("spec.startingDeadlineSeconds", "startingDeadlineSeconds", int64PtrValue(oldSpec.StartingDeadlineSeconds), int64PtrValue(newSpec.StartingDeadlineSeconds))
	s.add("spec.successfulJobsHistoryLimit", "successfulJobsHistoryLimit", int32PtrValue(oldSpec.SuccessfulJobsHistoryLimit), int32PtrValue(newSpec.SuccessfulJobsHistoryLimit))
	s.add("spec.failedJobsHistoryLimit", "failedJobsHistoryLimit", int32PtrValue(oldSpec.FailedJobsHistoryLimit), int32PtrValue(newSpec.FailedJobsHistoryLimit))

	oldLast, newLast := oldCJ.Status.LastScheduleTime, newCJ.Status.LastScheduleTime
	if newLast != nil && (oldLast == nil || newLast.Sub(oldLast.Time) > cronsched.StaleThreshold(newSpec.Schedule)) {
		s.changes = append(s.changes, FieldChange{Path: "status.lastScheduleTime", OldValue: timePtrValue(oldLast), NewValue: newLast.UTC().Format(time.RFC3339)})
		if oldLast == nil {
			s.summary = append(s.summary, "first run scheduled")
		} else {
			s.summary = append(s.summary, fmt.Sprintf("scheduled after %s without a run", humanDuration(newLast.Sub(oldLast.Time))))
		}
	}

	// The pod-template differs speak in Deployment paths; rebase them onto
	// the job template so the rows name the CronJob's real fields.
	oldTemplate, newTemplate := oldSpec.JobTemplate.Spec.Template, newSpec.JobTemplate.Spec.Template
	for _, diff := range []func() ([]FieldChange, []string){
		func() ([]FieldChange, []string) { return diffContainerImages(oldTemplate.Spec, newTemplate.Spec) },
		func() ([]FieldChange, []string) { return diffPodTemplateConfig(oldTemplate.Spec, newTemplate.Spec) },
		func() ([]FieldChange, []string) {
			return diffPodTemplateMetadata(oldTemplate.ObjectMeta, newTemplate.ObjectMeta)
		},
	} {
		changes, summary := diff()
		for _, change := range changes {
			change.Path = "spec.jobTemplate.spec." + strings.TrimPrefix(change.Path, "spec.")
			s.changes = append(s.changes, change)
		}
		s.summary = append(s.summary, summary...)
	}
	return s.changes, s.summary
}

// diffPDB covers the disruption budget. Status is left out: disruptionsAllowed
// drops to 0 and back on every routine pod replacement, and a budget that
// keeps blocking evictions is reported by the PDB issue detector.
func diffPDB(oldObj, newObj any) ([]FieldChange, []string) {
	oldPDB, ok1 := oldObj.(*policyv1.PodDisruptionBudget)
	newPDB, ok2 := newObj.(*policyv1.PodDisruptionBudget)
	if !ok1 || !ok2 {
		return nil, nil
	}
	var s specChangeSet
	s.add("spec.minAvailable", "minAvailable", intOrStringValue(oldPDB.Spec.MinAvailable), intOrStringValue(newPDB.Spec.MinAvailable))
	s.add("spec.maxUnavailable", "maxUnavailable", intOrStringValue(oldPDB.Spec.MaxUnavailable), intOrStringValue(newPDB.Spec.MaxUnavailable))
	oldSelector, newSelector := labelSelectorStrings(oldPDB.Spec.Selector, newPDB.Spec.Selector)
	s.add("spec.selector", "selector", oldSelector, newSelector)
	s.add("spec.unhealthyPodEvictionPolicy", "unhealthyPodEvictionPolicy", stringPtrValue((*string)(oldPDB.Spec.UnhealthyPodEvictionPolicy)), stringPtrValue((*string)(newPDB.Spec.UnhealthyPodEvictionPolicy)))

	return s.changes, s.summary
}

func diffServicePolicy(oldObj, newObj any) ([]FieldChange, []string) {
	oldSvc, ok1 := oldObj.(*corev1.Service)
	newSvc, ok2 := newObj.(*corev1.Service)
	if !ok1 || !ok2 {
		return nil, nil
	}
	var s specChangeSet
	o, n := oldSvc.Spec, newSvc.Spec
	s.add("spec.externalTrafficPolicy", "externalTrafficPolicy", string(o.ExternalTrafficPolicy), string(n.ExternalTrafficPolicy))
	s.add("spec.internalTrafficPolicy", "internalTrafficPolicy", stringPtrValue((*string)(o.InternalTrafficPolicy)), stringPtrValue((*string)(n.InternalTrafficPolicy)))
	s.add("spec.sessionAffinity", "sessionAffinity", string(o.SessionAffinity), string(n.SessionAffinity))
	// The API server fills in the ClientIP timeout when affinity is turned on,
	// so the timeout only earns a summary entry when it changes on its own.
	s.addRow("spec.sessionAffinityConfig.clientIP.timeoutSeconds", "sessionAffinity timeout", clientIPTimeout(o.SessionAffinityConfig), clientIPTimeout(n.SessionAffinityConfig), o.SessionAffinity == n.SessionAffinity)
	s.add("spec.publishNotReadyAddresses", "publishNotReadyAddresses", o.PublishNotReadyAddresses, n.PublishNotReadyAddresses)
	s.add("spec.loadBalancerClass", "loadBalancerClass", stringPtrValue(o.LoadBalancerClass), stringPtrValue(n.LoadBalancerClass))
	if !equalStringSlices(o.LoadBalancerSourceRanges, n.LoadBalancerSourceRanges) {
		s.changes = append(s.changes, FieldChange{Path: "spec.loadBalancerSourceRanges", OldValue: o.LoadBalancerSourceRanges, NewValue: n.LoadBalancerSourceRanges})
		s.summary = append(s.summary, "loadBalancerSourceRanges changed")
	}
	return s.changes, s.summary
}

func diffIngressRouting(oldObj, newObj any) ([]FieldChange, []string) {
	oldIng, ok1 := oldObj.(*networkingv1.Ingress)
	newIng, ok2 := newObj.(*networkingv1.Ingress)
	if !ok1 || !ok2 {
		return nil, nil
	}
	var s specChangeSet
	s.add("spec.ingressClassName", "ingressClassName", stringPtrValue(oldIng.Spec.IngressClassName), stringPtrValue(newIng.Spec.IngressClassName))
	s.add("spec.defaultBackend", "defaultBackend", ingressBackendString(oldIng.Spec.DefaultBackend), ingressBackendString(newIng.Spec.DefaultBackend))
	return s.changes, s.summary
}

func clientIPTimeout(c *corev1.SessionAffinityConfig) any {
	if c == nil || c.ClientIP == nil {
		return nil
	}
	return int32PtrValue(c.ClientIP.TimeoutSeconds)
}

// keyedListChanges reports added and removed items of a list whose items are
// all identified on both sides: by name (Gateway listeners, and route rules,
// where the name is optional), or by host for Ingress rules, which have no
// name. Edits inside a kept item are left to the backstop, which keys the
// list the same way. ok is false when the items can only be compared by
// position.
func keyedListChanges(path, noun string, oldItems, newItems []any, describe func(map[string]any) any) (changes []FieldChange, summary []string, ok bool) {
	field := listKeyField(oldItems, newItems)
	if field == "" {
		return nil, nil, false
	}
	oldByKey, _ := listByKey(oldItems, field)
	newByKey, _ := listByKey(newItems, field)
	for _, key := range unionKeys(oldByKey, newByKey) {
		oldItem, oldOK := oldByKey[key].(map[string]any)
		newItem, newOK := newByKey[key].(map[string]any)
		switch {
		case !oldOK:
			changes = append(changes, FieldChange{Path: path + "[" + key + "]", OldValue: nil, NewValue: describe(newItem)})
			summary = append(summary, fmt.Sprintf("%s %s added", noun, key))
		case !newOK:
			changes = append(changes, FieldChange{Path: path + "[" + key + "]", OldValue: describe(oldItem), NewValue: nil})
			summary = append(summary, fmt.Sprintf("%s %s removed", noun, key))
		}
	}
	return changes, summary, true
}

// gatewayListenerString names a listener by what it accepts.
func gatewayListenerString(listener map[string]any) any {
	protocol, _ := listener["protocol"].(string)
	out := fmt.Sprintf("%s:%v", protocol, listener["port"])
	if host, _ := listener["hostname"].(string); host != "" {
		out += " " + host
	}
	return out
}

// hpaBehaviorCovered are the scaling-rule fields diffHPAScalingConfig compares
// against the API defaults, which the API server writes into a direction left
// unset; the backstop would report those defaults as an edit. Any other
// behavior field, including ones newer than the compiled types, stays visible.
var hpaBehaviorCovered = func() []string {
	var out []string
	for _, dir := range []string{"scaleUp", "scaleDown"} {
		for _, field := range []string{"stabilizationWindowSeconds", "selectPolicy", "policies", "tolerance"} {
			out = append(out, "spec.behavior."+dir+"."+field)
		}
	}
	return out
}()

// webhookSummaryFields are the webhook fields webhookSummary compares; the
// backstop reports changes to any other webhook field.
var webhookSummaryFields = []string{
	"failurePolicy", "clientConfig", "sideEffects", "matchPolicy", "timeoutSeconds",
	"reinvocationPolicy", "namespaceSelector", "objectSelector", "rules",
}

// webhookControllerWrittenPaths are rewritten by CA injectors (cert-manager's
// cainjector, Istio, Knative and most operators' cert controllers) on every
// certificate renewal. The webhook row already reports a rotation, by digest
// only, as "caBundle rotated"; the backstop never reports it again.
var webhookControllerWrittenPaths = []string{"webhooks[*].clientConfig.caBundle"}

// webhookSummaryWithoutCA drops the CA digest from a webhookSummary so a
// rotation can be told apart from a change to what the webhook does.
func webhookSummaryWithoutCA(summary string) string {
	parts := strings.Fields(summary)
	out := parts[:0]
	for _, part := range parts {
		if !strings.HasPrefix(part, "caBundle=") {
			out = append(out, part)
		}
	}
	return strings.Join(out, " ")
}

// routeRuleBackends names a Gateway route rule by where it sends traffic.
func routeRuleBackends(rule map[string]any) any {
	refs, _, _ := unstructured.NestedSlice(rule, "backendRefs")
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		m, _ := ref.(map[string]any)
		name, _ := m["name"].(string)
		if port, ok := m["port"]; ok {
			name += fmt.Sprintf(":%v", port)
		}
		out = append(out, name)
	}
	return out
}

// ingressRuleBackends names an Ingress rule by its paths and services.
func ingressRuleBackends(rule map[string]any) any {
	paths, _, _ := unstructured.NestedSlice(rule, "http", "paths")
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		m, _ := p.(map[string]any)
		path, _ := m["path"].(string)
		svc, _, _ := unstructured.NestedString(m, "backend", "service", "name")
		out = append(out, path+"→"+svc)
	}
	return out
}

func ingressRulesList(ing *networkingv1.Ingress) []any {
	spec, ok := specMapOf(ing)
	if !ok {
		return nil
	}
	rules, _ := spec["rules"].([]any)
	return rules
}

func ingressBackendString(b *networkingv1.IngressBackend) any {
	switch {
	case b == nil:
		return nil
	case b.Service != nil:
		port := b.Service.Port.Name
		if port == "" {
			port = fmt.Sprint(b.Service.Port.Number)
		}
		return b.Service.Name + ":" + port
	case b.Resource != nil:
		return b.Resource.Kind + "/" + b.Resource.Name
	default:
		return "set"
	}
}

// diffHPAScalingConfig covers what the HPA scales and on which signals. Min
// and max replicas are in diffHPA.
func diffHPAScalingConfig(oldObj, newObj any) ([]FieldChange, []string) {
	oldHPA, ok1 := oldObj.(*autoscalingv2.HorizontalPodAutoscaler)
	newHPA, ok2 := newObj.(*autoscalingv2.HorizontalPodAutoscaler)
	if !ok1 || !ok2 {
		return nil, nil
	}
	var s specChangeSet
	o, n := oldHPA.Spec, newHPA.Spec
	s.add("spec.scaleTargetRef", "scaleTargetRef", scaleTargetString(o.ScaleTargetRef), scaleTargetString(n.ScaleTargetRef))

	oldMetrics, newMetrics := hpaMetricTargets(o.Metrics), hpaMetricTargets(n.Metrics)
	if !reflect.DeepEqual(oldMetrics, newMetrics) {
		s.changes = append(s.changes, FieldChange{Path: "spec.metrics", OldValue: hpaMetricList(oldMetrics), NewValue: hpaMetricList(newMetrics)})
		keys := make(map[string]bool, len(oldMetrics)+len(newMetrics))
		for key := range oldMetrics {
			keys[key] = true
		}
		for key := range newMetrics {
			keys[key] = true
		}
		sorted := make([]string, 0, len(keys))
		for key := range keys {
			sorted = append(sorted, key)
		}
		sort.Strings(sorted)
		for _, key := range sorted {
			oldMetric, oldOK := oldMetrics[key]
			newMetric, newOK := newMetrics[key]
			switch {
			case !oldOK:
				s.summary = append(s.summary, fmt.Sprintf("metric %s added (target %s)", newMetric.display, newMetric.target))
			case !newOK:
				s.summary = append(s.summary, fmt.Sprintf("metric %s removed", oldMetric.display))
			case oldMetric.target != newMetric.target:
				s.summary = append(s.summary, fmt.Sprintf("%s target: %s→%s", newMetric.display, oldMetric.target, newMetric.target))
			}
		}
	}

	var oldBehavior, newBehavior autoscalingv2.HorizontalPodAutoscalerBehavior
	if o.Behavior != nil {
		oldBehavior = *o.Behavior
	}
	if n.Behavior != nil {
		newBehavior = *n.Behavior
	}
	for _, dir := range []struct {
		name     string
		old, new *autoscalingv2.HPAScalingRules
		defaults string
	}{
		{"scaleUp", oldBehavior.ScaleUp, newBehavior.ScaleUp, hpaDefaultScaleUp},
		{"scaleDown", oldBehavior.ScaleDown, newBehavior.ScaleDown, hpaDefaultScaleDown},
	} {
		oldRules, newRules := hpaScalingRulesString(dir.old, dir.defaults), hpaScalingRulesString(dir.new, dir.defaults)
		if oldRules != newRules {
			s.changes = append(s.changes, FieldChange{Path: "spec.behavior." + dir.name, OldValue: oldRules, NewValue: newRules})
			s.summary = append(s.summary, fmt.Sprintf("%s behavior changed", dir.name))
		}
	}
	return s.changes, s.summary
}

// hpaMetricTargets keys each metric by what it measures, so a change of
// target reads as one edit rather than a remove plus an add.
type hpaMetric struct {
	display, target string
}

// hpaMetricTargets maps each metric's identity to its display name and
// target. The identity keeps selector values verbatim so two metrics that
// differ only in a redacted value stay distinct; only display is redacted.
func hpaMetricTargets(metrics []autoscalingv2.MetricSpec) map[string]hpaMetric {
	out := make(map[string]hpaMetric, len(metrics))
	for _, m := range metrics {
		var key, display string
		var target autoscalingv2.MetricTarget
		switch {
		case m.Resource != nil:
			display, target = string(m.Resource.Name), m.Resource.Target
			key = "resource/" + display
		case m.ContainerResource != nil:
			display, target = m.ContainerResource.Container+"/"+string(m.ContainerResource.Name), m.ContainerResource.Target
			key = "containerResource/" + display
		case m.Pods != nil:
			key, display, target = "pods/"+metricIdentifierString(m.Pods.Metric, false), "pods/"+metricIdentifierString(m.Pods.Metric, true), m.Pods.Target
		case m.Object != nil:
			ref := m.Object.DescribedObject
			prefix := "object/" + ref.APIVersion + "/" + ref.Kind + "/" + ref.Name + "/"
			key, display, target = prefix+metricIdentifierString(m.Object.Metric, false), prefix+metricIdentifierString(m.Object.Metric, true), m.Object.Target
		case m.External != nil:
			key, display, target = "external/"+metricIdentifierString(m.External.Metric, false), "external/"+metricIdentifierString(m.External.Metric, true), m.External.Target
		default:
			key = "type/" + string(m.Type)
			display = string(m.Type)
		}
		// Duplicate identities stay distinct so no metric is overwritten.
		base := key
		for n := 2; ; n++ {
			if _, taken := out[key]; !taken {
				break
			}
			key = fmt.Sprintf("%s#%d", base, n)
		}
		out[key] = hpaMetric{display: display, target: metricTargetString(target)}
	}
	return out
}

func metricIdentifierString(id autoscalingv2.MetricIdentifier, redact bool) string {
	if id.Selector == nil {
		return id.Name
	}
	sel := id.Selector
	if redact {
		sel = redactedSelector(sel)
	}
	return id.Name + "{" + metav1.FormatLabelSelector(sel) + "}"
}

// metricTargetString renders every target field, so the metrics row covers
// the whole metric.
func metricTargetString(t autoscalingv2.MetricTarget) string {
	var parts []string
	if t.AverageUtilization != nil {
		parts = append(parts, fmt.Sprintf("%d%%", *t.AverageUtilization))
	}
	if t.AverageValue != nil {
		parts = append(parts, "avg "+t.AverageValue.String())
	}
	if t.Value != nil {
		parts = append(parts, t.Value.String())
	}
	if len(parts) == 0 || (t.Type != autoscalingv2.UtilizationMetricType && t.Type != autoscalingv2.AverageValueMetricType && t.Type != autoscalingv2.ValueMetricType) {
		parts = append(parts, "type="+string(t.Type))
	}
	return strings.Join(parts, " ")
}

func scaleTargetString(ref autoscalingv2.CrossVersionObjectReference) string {
	out := ref.Kind + "/" + ref.Name
	if ref.APIVersion != "" {
		out += " (" + ref.APIVersion + ")"
	}
	return out
}

func hpaMetricList(metrics map[string]hpaMetric) []string {
	out := make([]string, 0, len(metrics))
	for _, metric := range metrics {
		out = append(out, metric.display+"="+metric.target)
	}
	sort.Strings(out)
	return out
}

// The API server writes these defaults into any direction left unset once
// spec.behavior exists, so an unset direction compares equal to them.
const (
	hpaDefaultScaleUp   = "stabilization=0s, select=Max, Pods 4/15s, Percent 100/15s"
	hpaDefaultScaleDown = "stabilization=300s, select=Max, Percent 100/15s"
)

func hpaScalingRulesString(r *autoscalingv2.HPAScalingRules, defaults string) string {
	if r == nil {
		return defaults
	}
	var parts []string
	if r.StabilizationWindowSeconds != nil {
		parts = append(parts, fmt.Sprintf("stabilization=%ds", *r.StabilizationWindowSeconds))
	}
	if r.SelectPolicy != nil {
		parts = append(parts, "select="+string(*r.SelectPolicy))
	}
	for _, p := range r.Policies {
		parts = append(parts, fmt.Sprintf("%s %d/%ds", p.Type, p.Value, p.PeriodSeconds))
	}
	if r.Tolerance != nil {
		parts = append(parts, "tolerance="+r.Tolerance.String())
	}
	if len(parts) == 0 {
		return defaults
	}
	return strings.Join(parts, ", ")
}

// diffRolloutSpec adds the Argo Rollout strategy and pod-template config to
// diffRollout, which covers images, replicas and rollout progress.
func diffRolloutSpec(oldObj, newObj any) ([]FieldChange, []string) {
	oldRO, ok1 := oldObj.(*unstructured.Unstructured)
	newRO, ok2 := newObj.(*unstructured.Unstructured)
	if !ok1 || !ok2 {
		return nil, nil
	}
	var s specChangeSet
	s.add("spec.strategy", "strategy", rolloutStrategyKind(oldRO), rolloutStrategyKind(newRO))
	for _, field := range []struct{ path, label string }{
		{"canary.maxSurge", "maxSurge"},
		{"canary.maxUnavailable", "maxUnavailable"},
		{"blueGreen.autoPromotionEnabled", "autoPromotionEnabled"},
		{"blueGreen.activeService", "activeService"},
		{"blueGreen.previewService", "previewService"},
	} {
		fields := append([]string{"spec", "strategy"}, strings.Split(field.path, ".")...)
		oldVal, _, _ := unstructured.NestedFieldNoCopy(oldRO.Object, fields...)
		newVal, _, _ := unstructured.NestedFieldNoCopy(newRO.Object, fields...)
		s.add("spec.strategy."+field.path, field.label, scalarOrNil(oldVal), scalarOrNil(newVal))
	}
	oldSteps, _, _ := unstructured.NestedSlice(oldRO.Object, "spec", "strategy", "canary", "steps")
	newSteps, _, _ := unstructured.NestedSlice(newRO.Object, "spec", "strategy", "canary", "steps")
	if !reflect.DeepEqual(oldSteps, newSteps) {
		s.changes = append(s.changes, FieldChange{Path: "spec.strategy.canary.steps", OldValue: len(oldSteps), NewValue: len(newSteps)})
		s.summary = append(s.summary, fmt.Sprintf("canary steps changed (%d→%d steps)", len(oldSteps), len(newSteps)))
	}

	oldTemplate, oldOK := rolloutPodTemplate(oldRO)
	newTemplate, newOK := rolloutPodTemplate(newRO)
	if oldOK && newOK {
		changes, summary := diffPodTemplateConfig(oldTemplate.Spec, newTemplate.Spec)
		s.changes = append(s.changes, changes...)
		s.summary = append(s.summary, summary...)
		changes, summary = diffPodTemplateMetadata(oldTemplate.ObjectMeta, newTemplate.ObjectMeta)
		s.changes = append(s.changes, changes...)
		s.summary = append(s.summary, summary...)
	}
	return s.changes, s.summary
}

func rolloutStrategyKind(u *unstructured.Unstructured) string {
	strategy, _, _ := unstructured.NestedMap(u.Object, "spec", "strategy")
	for _, kind := range []string{"canary", "blueGreen"} {
		if _, ok := strategy[kind]; ok {
			return kind
		}
	}
	return ""
}

func rolloutPodTemplate(u *unstructured.Unstructured) (corev1.PodTemplateSpec, bool) {
	var out corev1.PodTemplateSpec
	template, found, _ := unstructured.NestedMap(u.Object, "spec", "template")
	if !found {
		return out, false
	}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(template, &out); err != nil {
		return out, false
	}
	return out, true
}

func scalarOrNil(v any) any {
	switch typed := v.(type) {
	case nil:
		return nil
	case string, bool, int64, float64:
		return typed
	default:
		return fmt.Sprint(typed)
	}
}

func timePtrValue(t *metav1.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

func stringPtrValue(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func int64PtrValue(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func boolPtrTrue(p *bool) bool {
	return p != nil && *p
}

// labelSelectorStrings renders a pair of selectors with values under
// secret-looking keys masked. A masked value that differs between the two
// reads "REDACTED-changed" on the new side, so the change stays visible.
func labelSelectorStrings(oldSel, newSel *metav1.LabelSelector) (any, any) {
	render := func(sel, other *metav1.LabelSelector) any {
		if sel == nil {
			return nil
		}
		return metav1.FormatLabelSelector(redactedSelectorAgainst(sel, other))
	}
	return render(oldSel, nil), render(newSel, oldSel)
}

// redactedSelector masks label values under secret-looking keys, the policy
// pod-template label rows use, before a selector is rendered into a row.
func redactedSelector(sel *metav1.LabelSelector) *metav1.LabelSelector {
	return redactedSelectorAgainst(sel, nil)
}

// redactedSelectorAgainst masks like redactedSelector, marking a masked value
// that differs from the one in previous.
func redactedSelectorAgainst(sel, previous *metav1.LabelSelector) *metav1.LabelSelector {
	out := sel.DeepCopy()
	mask := func(changed bool) string {
		if changed {
			return "REDACTED-changed"
		}
		return "REDACTED"
	}
	for key, value := range out.MatchLabels {
		if envNameLooksSecret(key) {
			changed := previous != nil && previous.MatchLabels[key] != value
			out.MatchLabels[key] = mask(changed)
		}
	}
	for i := range out.MatchExpressions {
		expr := &out.MatchExpressions[i]
		if !envNameLooksSecret(expr.Key) || len(expr.Values) == 0 {
			continue
		}
		changed := previous != nil
		if previous != nil {
			for _, prev := range previous.MatchExpressions {
				if prev.Key == expr.Key && prev.Operator == expr.Operator && slices.Equal(prev.Values, expr.Values) {
					changed = false
				}
			}
		}
		expr.Values = []string{mask(changed)}
	}
	return out
}

// redactedLabelMaps masks values under secret-looking keys in a pair of
// label maps (nodeSelector), marking a masked value that changed.
func redactedLabelMaps(oldMap, newMap map[string]string) (any, any) {
	render := func(m, previous map[string]string, comparing bool) any {
		if m == nil {
			return nil
		}
		out := make(map[string]string, len(m))
		for key, value := range m {
			switch {
			case !envNameLooksSecret(key):
				out[key] = value
			case comparing && previous[key] != value:
				out[key] = "[REDACTED-changed]"
			default:
				out[key] = "[REDACTED]"
			}
		}
		return out
	}
	return render(oldMap, nil, false), render(newMap, oldMap, true)
}

// humanDuration renders a gap the way a person says it: "30h", "3d4h", "45m".
func humanDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	days := int(d / (24 * time.Hour))
	hours := int(d % (24 * time.Hour) / time.Hour)
	minutes := int(d % time.Hour / time.Minute)
	switch {
	case days >= 3:
		if hours == 0 {
			return fmt.Sprintf("%dd", days)
		}
		return fmt.Sprintf("%dd%dh", days, hours)
	case d >= time.Hour:
		total := int(d / time.Hour)
		if minutes == 0 {
			return fmt.Sprintf("%dh", total)
		}
		return fmt.Sprintf("%dh%dm", total, minutes)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}
