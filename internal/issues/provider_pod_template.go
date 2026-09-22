package issues

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/skyhook-io/radar/pkg/issuesapi"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type podTemplateSources struct {
	webhooksOnce sync.Once
	webhooks     []templateWebhook
	priorityOnce sync.Once
	priorities   []schedulingv1.PriorityClass
	mu           sync.Mutex
	limitRanges  map[string][]*corev1.LimitRange
}

type templateWebhook struct {
	configuration Ref
	created       metav1.Time
	name          string
	create        bool
	update        bool
	namespace     labels.Selector
	object        labels.Selector
}

func (p *CacheProvider) podTemplateFact(issue Issue, access podTemplateAccess) *issuesapi.DiagnosticFact {
	if p == nil || p.cache == nil || p.cache.Pods() == nil || !access.canRead(Ref{Kind: "Pod", Namespace: issue.Namespace, Name: issue.Name}) {
		return nil
	}
	pod, err := p.cache.Pods().Pods(issue.Namespace).Get(issue.Name)
	if err != nil {
		return nil
	}
	owner := metav1.GetControllerOf(pod)
	if owner == nil || owner.UID == "" {
		return nil
	}
	gv, err := schema.ParseGroupVersion(owner.APIVersion)
	if err != nil {
		return nil
	}
	ref := Ref{Kind: owner.Kind, Group: gv.Group, Namespace: pod.Namespace, Name: owner.Name}
	if !access.canRead(ref) {
		return nil
	}
	var template *corev1.PodSpec
	switch {
	case ref.Group == "apps" && ref.Kind == "ReplicaSet" && p.cache.ReplicaSets() != nil:
		rs, err := p.cache.ReplicaSets().ReplicaSets(pod.Namespace).Get(ref.Name)
		if err == nil && rs.UID == owner.UID {
			template = &rs.Spec.Template.Spec
		}
	case ref.Group == "batch" && ref.Kind == "Job" && p.cache.Jobs() != nil:
		job, err := p.cache.Jobs().Jobs(pod.Namespace).Get(ref.Name)
		if err == nil && job.UID == owner.UID {
			template = &job.Spec.Template.Spec
		}
	}
	if template == nil {
		return nil
	}
	differences := comparePodTemplate(pod, template, templateSymptom(issue))
	if len(differences) == 0 {
		return nil
	}
	total := len(differences)
	if len(differences) > maxTemplateDifferences {
		differences = differences[:maxTemplateDifferences]
	}
	parts := make([]string, 0, len(differences))
	for _, difference := range differences {
		parts = append(parts, difference.message)
	}
	message := fmt.Sprintf("Live Pod %s/%s spec compared with owning %s %s's current template: %s.", pod.Namespace, pod.Name, ref.Kind, ref.Name, strings.Join(parts, "; "))
	if total > len(differences) {
		message += fmt.Sprintf(" Showing %d of %d relevant differences.", len(differences), total)
	}
	if templateSymptom(issue) == "oom" {
		message += " The template values do not describe the OOMing containers' current Pod memory limits. Investigate this discrepancy before choosing a memory-limit change. Pod resizing or later template edits can also explain the difference."
	}
	refs := []Ref{{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name}, ref}
	candidates := p.templateCandidates(pod, differences, access)
	candidateCount := len(candidates)
	if len(candidates) > 3 {
		candidates = candidates[:3]
	}
	for _, candidate := range candidates {
		message += " " + candidate.message + "."
		refs = append(refs, candidate.ref)
	}
	if slices.ContainsFunc(candidates, func(candidate templateCandidate) bool {
		return candidate.ref.Kind == "MutatingWebhookConfiguration"
	}) {
		message += " Inspect these webhooks' backing controller or policy configuration for mutations to the differing fields. Matching scope does not establish what they changed."
	}
	if candidateCount > len(candidates) {
		message += fmt.Sprintf(" %d further matching candidate objects not listed; display order does not indicate likelihood.", candidateCount-len(candidates))
	}
	message += " Writer and timing are unknown. Candidate inspection is incomplete: current readable configuration only; CEL conditions are not evaluated."
	return &issuesapi.DiagnosticFact{Type: factPodTemplateDivergence, Message: message, Refs: refs}
}

type templateCandidate struct {
	ref     Ref
	message string
}

func (p *CacheProvider) templateCandidates(pod *corev1.Pod, differences []templateDifference, access podTemplateAccess) []templateCandidate {
	var candidates []templateCandidate
	needsDefaults, needsPriority := false, false
	for _, d := range differences {
		needsDefaults = needsDefaults || (d.templateMissing && d.resource != "")
		needsPriority = needsPriority || (d.templateMissing && d.path == "priorityClassName")
	}
	if needsDefaults {
		for _, lr := range p.templateLimitRanges(pod.Namespace) {
			ref := Ref{Kind: "LimitRange", Namespace: pod.Namespace, Name: lr.Name}
			if !access.canRead(ref) {
				continue
			}
			if matchingTemplateDefaults(lr, differences) {
				candidates = append(candidates, templateCandidate{ref, fmt.Sprintf("Candidate LimitRange %s has a default matching an omitted template resource", lr.Name)})
			}
		}
	}
	priorityRef := Ref{Group: "scheduling.k8s.io", Kind: "PriorityClass", Name: pod.Spec.PriorityClassName}
	if needsPriority && access.canRead(priorityRef) {
		p.podTemplateSources.priorityOnce.Do(func() {
			p.loadTemplatePriorities()
		})
		for _, pc := range p.podTemplateSources.priorities {
			if pc.Name == pod.Spec.PriorityClassName && pc.GlobalDefault {
				candidates = append(candidates, templateCandidate{priorityRef, fmt.Sprintf("Candidate PriorityClass %s is currently the global default", pc.Name)})
			}
		}
	}
	webhookRef := Ref{Group: "admissionregistration.k8s.io", Kind: "MutatingWebhookConfiguration"}
	if access.cluster != nil && !access.cluster(webhookRef.Kind, webhookRef.Group) {
		return candidates
	}
	p.podTemplateSources.webhooksOnce.Do(func() { p.loadTemplateWebhooks() })
	var namespaceLabels labels.Set
	if access.canRead(Ref{Kind: "Namespace", Name: pod.Namespace}) && p.cache.Namespaces() != nil {
		if ns, err := p.cache.Namespaces().Get(pod.Namespace); err == nil {
			namespaceLabels = labels.Set{}
			for key, value := range ns.Labels {
				namespaceLabels[key] = value
			}
		}
	}
	// Resource resizing uses pods/resize, which this parent-Pod matcher does not inspect.
	allowUpdate := slices.ContainsFunc(differences, func(d templateDifference) bool { return strings.HasSuffix(d.path, ".image") })
	seen := make(map[string]bool)
	for _, webhook := range p.podTemplateSources.webhooks {
		if seen[webhook.configuration.Name] || !access.canRead(webhook.configuration) {
			continue
		}
		operations := webhook.matchingOperations(pod, namespaceLabels, allowUpdate)
		if len(operations) == 0 {
			continue
		}
		seen[webhook.configuration.Name] = true
		candidates = append(candidates, templateCandidate{webhook.configuration,
			fmt.Sprintf("Mutating webhook %s / %s currently matches this Pod's %s rules and selectors", webhook.configuration.Name, webhook.name, strings.Join(operations, "/"))})
	}
	return candidates
}

func matchingTemplateDefaults(lr *corev1.LimitRange, differences []templateDifference) bool {
	for _, limit := range lr.Spec.Limits {
		if limit.Type != corev1.LimitTypeContainer {
			continue
		}
		for _, d := range differences {
			if !d.templateMissing || d.resource == "" {
				continue
			}
			defaults := limit.Default
			if d.resourceKind == "requests" {
				defaults = limit.DefaultRequest
			}
			if value, ok := defaults[d.resource]; ok && value.Cmp(d.value) == 0 {
				return true
			}
		}
	}
	return false
}

func (p *CacheProvider) templateLimitRanges(namespace string) []*corev1.LimitRange {
	p.podTemplateSources.mu.Lock()
	defer p.podTemplateSources.mu.Unlock()
	if p.podTemplateSources.limitRanges == nil {
		p.podTemplateSources.limitRanges = make(map[string][]*corev1.LimitRange)
	}
	if items, ok := p.podTemplateSources.limitRanges[namespace]; ok {
		return items
	}
	var items []*corev1.LimitRange
	if p.cache.LimitRanges() != nil {
		items, _ = p.cache.LimitRanges().LimitRanges(namespace).List(labels.Everything())
	}
	sort.Slice(items, func(a, b int) bool { return items[a].Name < items[b].Name })
	p.podTemplateSources.limitRanges[namespace] = items
	return items
}

func (p *CacheProvider) loadTemplatePriorities() {
	if p.dynamic == nil || p.discovery == nil {
		return
	}
	gvr, ok := p.discovery.GetGVRWithGroup("PriorityClass", "scheduling.k8s.io")
	if !ok || !p.dynamic.IsClusterWideSynced(gvr) {
		return
	}
	items, err := p.dynamic.ListWatched(gvr)
	if err != nil {
		return
	}
	for _, item := range items {
		var pc schedulingv1.PriorityClass
		if runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, &pc) == nil {
			p.podTemplateSources.priorities = append(p.podTemplateSources.priorities, pc)
		}
	}
}

func (p *CacheProvider) loadTemplateWebhooks() {
	if p.dynamic == nil || p.discovery == nil {
		return
	}
	gvr, ok := p.discovery.GetGVRWithGroup("MutatingWebhookConfiguration", "admissionregistration.k8s.io")
	if !ok || !p.dynamic.IsClusterWideSynced(gvr) {
		return
	}
	items, err := p.dynamic.ListWatched(gvr)
	if err != nil {
		return
	}
	for _, item := range items {
		var config admissionv1.MutatingWebhookConfiguration
		if runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, &config) != nil {
			continue
		}
		p.podTemplateSources.webhooks = append(p.podTemplateSources.webhooks, compileTemplateWebhooks(&config)...)
	}
	sort.Slice(p.podTemplateSources.webhooks, func(a, b int) bool {
		left, right := p.podTemplateSources.webhooks[a], p.podTemplateSources.webhooks[b]
		if left.configuration.Name != right.configuration.Name {
			return left.configuration.Name < right.configuration.Name
		}
		return left.name < right.name
	})
}

func compileTemplateWebhooks(config *admissionv1.MutatingWebhookConfiguration) []templateWebhook {
	var out []templateWebhook
	for _, webhook := range config.Webhooks {
		if len(webhook.MatchConditions) > 0 {
			continue
		}
		ns, err := metav1.LabelSelectorAsSelector(webhook.NamespaceSelector)
		if webhook.NamespaceSelector == nil {
			ns = labels.Everything()
		}
		if err != nil {
			continue
		}
		obj, err := metav1.LabelSelectorAsSelector(webhook.ObjectSelector)
		if webhook.ObjectSelector == nil {
			obj = labels.Everything()
		}
		if err != nil {
			continue
		}
		candidate := templateWebhook{configuration: Ref{Kind: "MutatingWebhookConfiguration", Group: "admissionregistration.k8s.io", Name: config.Name},
			created: config.CreationTimestamp, name: webhook.Name, namespace: ns, object: obj}
		for _, rule := range webhook.Rules {
			if rule.Scope != nil && *rule.Scope == admissionv1.ClusterScope {
				continue
			}
			if !ruleIncludes(rule.APIGroups, "") || !ruleIncludes(rule.APIVersions, "v1") {
				continue
			}
			if !ruleIncludes(rule.Resources, "pods") && !slices.Contains(rule.Resources, "*/*") {
				continue
			}
			for _, operation := range rule.Operations {
				candidate.create = candidate.create || operation == admissionv1.Create || operation == admissionv1.OperationAll
				candidate.update = candidate.update || operation == admissionv1.Update || operation == admissionv1.OperationAll
			}
		}
		if candidate.create || candidate.update {
			out = append(out, candidate)
		}
	}
	return out
}

func (w templateWebhook) matchingOperations(pod *corev1.Pod, namespaceLabels labels.Set, allowUpdate bool) []string {
	if !w.namespace.Empty() && namespaceLabels == nil {
		return nil
	}
	if !w.namespace.Matches(namespaceLabels) || !w.object.Matches(labels.Set(pod.Labels)) {
		return nil
	}
	var operations []string
	if w.create && (w.created.IsZero() || pod.CreationTimestamp.IsZero() || !w.created.After(pod.CreationTimestamp.Time)) {
		operations = append(operations, "CREATE")
	}
	if w.update && allowUpdate {
		operations = append(operations, "UPDATE")
	}
	return operations
}

func ruleIncludes(values []string, value string) bool {
	for _, v := range values {
		if v == value || v == "*" {
			return true
		}
	}
	return false
}
