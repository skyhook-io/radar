package issues

import (
	"fmt"
	"net"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

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
	webhooksOnce     sync.Once
	webhooks         []templateWebhook
	webhooksLoaded   bool
	priorityOnce     sync.Once
	priorities       []schedulingv1.PriorityClass
	prioritiesLoaded bool
	mu               sync.Mutex
	limitRanges      map[string][]*corev1.LimitRange
}

type templateWebhook struct {
	configuration   Ref
	created         metav1.Time
	name            string
	create          bool
	update          bool
	namespace       labels.Selector
	object          labels.Selector
	loopback        bool
	matchConditions bool
}

func (p *CacheProvider) podTemplateFact(issue Issue, access podTemplateAccess) *issuesapi.DiagnosticFact {
	pod, template, ref := p.podTemplateBaseline(issue.Namespace, issue.Name, access)
	if pod == nil {
		return nil
	}
	differences := comparePodTemplate(pod, &template.Spec, templateSymptom(issue))
	if len(differences) == 0 {
		return nil
	}
	shown := differences
	if len(shown) > maxTemplateDifferences {
		shown = shown[:maxTemplateDifferences]
	}
	parts := make([]string, 0, len(shown))
	for _, difference := range shown {
		parts = append(parts, difference.message)
	}
	message := fmt.Sprintf("Live Pod %s/%s spec compared with owning %s %s's current template: %s.", pod.Namespace, pod.Name, ref.Kind, ref.Name, strings.Join(parts, "; "))
	if len(differences) > len(shown) {
		message += fmt.Sprintf(" Showing %d of %d relevant differences.", len(shown), len(differences))
	}
	if templateSymptom(issue) == "oom" {
		message += " The template values do not describe the OOMing containers' current Pod memory limits. Investigate this discrepancy before choosing a memory-limit change. Pod resizing or later template edits can also explain the difference."
	}
	sources := p.templateSources(pod, template, differences, access)
	for _, sentence := range sources.sentences {
		message += " " + sentence
	}
	refs := append([]Ref{{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name}, ref}, sources.refs...)
	return &issuesapi.DiagnosticFact{Type: factPodTemplateDivergence, Message: message, Refs: refs}
}

// podTemplateBaseline returns a readable Pod with the current template of its
// immediate ReplicaSet or Job controller, matched by group and UID.
func (p *CacheProvider) podTemplateBaseline(namespace, name string, access podTemplateAccess) (*corev1.Pod, *corev1.PodTemplateSpec, Ref) {
	if p == nil || p.cache == nil || p.cache.Pods() == nil || !access.canRead(Ref{Kind: "Pod", Namespace: namespace, Name: name}) {
		return nil, nil, Ref{}
	}
	pod, err := p.cache.Pods().Pods(namespace).Get(name)
	if err != nil {
		return nil, nil, Ref{}
	}
	template, ref := p.ownerTemplate(pod, access)
	if template == nil {
		return nil, nil, Ref{}
	}
	return pod, template, ref
}

func (p *CacheProvider) ownerTemplate(pod *corev1.Pod, access podTemplateAccess) (*corev1.PodTemplateSpec, Ref) {
	owner := metav1.GetControllerOf(pod)
	if owner == nil || owner.UID == "" {
		return nil, Ref{}
	}
	gv, err := schema.ParseGroupVersion(owner.APIVersion)
	if err != nil {
		return nil, Ref{}
	}
	ref := Ref{Kind: owner.Kind, Group: gv.Group, Namespace: pod.Namespace, Name: owner.Name}
	if !access.canRead(ref) {
		return nil, Ref{}
	}
	switch {
	case ref.Group == "apps" && ref.Kind == "ReplicaSet" && p.cache.ReplicaSets() != nil:
		rs, err := p.cache.ReplicaSets().ReplicaSets(pod.Namespace).Get(ref.Name)
		if err == nil && rs.UID == owner.UID {
			return &rs.Spec.Template, ref
		}
	case ref.Group == "batch" && ref.Kind == "Job" && p.cache.Jobs() != nil:
		job, err := p.cache.Jobs().Jobs(pod.Namespace).Get(ref.Name)
		if err == nil && job.UID == owner.UID {
			return &job.Spec.Template, ref
		}
	}
	return nil, Ref{}
}

const (
	maxTemplateCandidateLines = 5
	maxTemplateNamedSources   = 10
)

type templateCandidate struct {
	ref     Ref
	message string
	// loopback marks webhooks served from a loopback URL — on managed
	// Kubernetes, a control-plane-hosted endpoint. It orders the list only; it
	// says where admission calls go, not who installed the webhook or what it
	// can change.
	loopback bool
}

type templateSourceReport struct {
	sentences []string
	refs      []Ref
}

// templateSources describes what could have produced the differences: the
// matching candidates (every one named, within a budget), mutator identities
// the Pod itself records, and what could not be inspected. Candidates are
// found from all differences, not only the displayed ones.
func (p *CacheProvider) templateSources(pod *corev1.Pod, template *corev1.PodTemplateSpec, differences []templateDifference, access podTemplateAccess) templateSourceReport {
	candidates, coverage := p.templateCandidates(pod, differences, access)
	var report templateSourceReport
	hasWebhook := false
	for idx, candidate := range candidates {
		if idx == maxTemplateNamedSources {
			break
		}
		report.refs = append(report.refs, candidate.ref)
		hasWebhook = hasWebhook || candidate.ref.Kind == "MutatingWebhookConfiguration"
		if idx < maxTemplateCandidateLines {
			report.sentences = append(report.sentences, candidate.message+".")
		}
	}
	if len(candidates) > maxTemplateCandidateLines {
		end := min(len(candidates), maxTemplateNamedSources)
		names := make([]string, 0, end-maxTemplateCandidateLines)
		for _, candidate := range candidates[maxTemplateCandidateLines:end] {
			names = append(names, candidate.ref.Kind+" "+candidate.ref.Name)
		}
		sentence := "Also matching: " + strings.Join(names, ", ") + "."
		if len(candidates) > end {
			sentence += fmt.Sprintf(" %d more matching candidate objects are not named.", len(candidates)-end)
		}
		report.sentences = append(report.sentences, sentence)
	}
	if hasWebhook {
		report.sentences = append(report.sentences, fmt.Sprintf("The Pod was created %s. Inspect these webhooks' backing controller or policy configuration for mutations to the differing fields. Matching scope does not establish what they changed; list order does not indicate likelihood, and webhooks served from a loopback endpoint are listed last.", pod.CreationTimestamp.UTC().Format(time.RFC3339)))
	}
	report.sentences = append(report.sentences, selfReportedMutations(pod, template, differences)...)
	report.sentences = append(report.sentences, coverage...)
	report.sentences = append(report.sentences, "Writer and timing are unknown. Candidate inspection is incomplete: current readable configuration only; CEL conditions are not evaluated.")
	return report
}

// selfReportedAnnotations are written by the mutator onto the Pod it changed.
// They name the mutator; they do not prove which field it set. Sidecar
// injector annotations are left out: they describe added containers, which
// the comparison ignores.
var selfReportedAnnotations = []struct {
	key       string
	resources bool
}{
	{key: "gatekeeper.sh/mutations"},
	{key: "vpaUpdates", resources: true},
}

func selfReportedMutations(pod *corev1.Pod, template *corev1.PodTemplateSpec, differences []templateDifference) []string {
	resourceDifference := slices.ContainsFunc(differences, func(d templateDifference) bool { return d.resource != "" })
	var out []string
	for _, annotation := range selfReportedAnnotations {
		value, ok := pod.Annotations[annotation.key]
		if !ok || value == "" || (annotation.resources && !resourceDifference) {
			continue
		}
		if _, copied := template.Annotations[annotation.key]; copied {
			continue
		}
		if len(value) > 200 {
			value = value[:200] + "…"
		}
		out = append(out, fmt.Sprintf("The Pod's %s annotation, absent from the template, reads %q.", annotation.key, value))
	}
	return out
}

func (p *CacheProvider) templateCandidates(pod *corev1.Pod, differences []templateDifference, access podTemplateAccess) ([]templateCandidate, []string) {
	var candidates []templateCandidate
	needsDefaults, needsPriority := false, false
	for _, d := range differences {
		needsDefaults = needsDefaults || (d.templateMissing && d.resource != "")
		needsPriority = needsPriority || (d.templateMissing && d.path == "priorityClassName")
	}
	var coverage []string
	if needsDefaults {
		if p.cache.LimitRanges() == nil {
			coverage = append(coverage, "LimitRange defaults were not inspected: Radar has not cached LimitRanges.")
		}
		unreadable := false
		for _, lr := range p.templateLimitRanges(pod.Namespace) {
			ref := Ref{Kind: "LimitRange", Namespace: pod.Namespace, Name: lr.Name}
			if !access.canRead(ref) {
				unreadable = true
				continue
			}
			if matchingTemplateDefaults(lr, differences) {
				candidates = append(candidates, templateCandidate{ref: ref, message: fmt.Sprintf("Candidate LimitRange %s has a default matching an omitted template resource", lr.Name)})
			}
		}
		if unreadable {
			coverage = append(coverage, "Some LimitRanges in the namespace are not readable by the caller and were not inspected.")
		}
	}
	priorityRef := Ref{Group: "scheduling.k8s.io", Kind: "PriorityClass", Name: pod.Spec.PriorityClassName}
	if needsPriority && !access.canRead(priorityRef) {
		coverage = append(coverage, "PriorityClasses were not inspected: the caller cannot read them.")
	} else if needsPriority {
		p.podTemplateSources.priorityOnce.Do(func() {
			p.loadTemplatePriorities()
		})
		if !p.podTemplateSources.prioritiesLoaded {
			coverage = append(coverage, "PriorityClasses were not inspected: Radar has not cached them.")
		}
		for _, pc := range p.podTemplateSources.priorities {
			if pc.Name == pod.Spec.PriorityClassName && pc.GlobalDefault {
				candidates = append(candidates, templateCandidate{ref: priorityRef, message: fmt.Sprintf("Candidate PriorityClass %s is currently the global default", pc.Name)})
			}
		}
	}
	webhookRef := Ref{Group: "admissionregistration.k8s.io", Kind: "MutatingWebhookConfiguration"}
	if access.cluster != nil && !access.cluster(webhookRef.Kind, webhookRef.Group) {
		return candidates, append(coverage, "Mutating webhook configurations were not inspected: the caller cannot list them.")
	}
	p.podTemplateSources.webhooksOnce.Do(func() { p.loadTemplateWebhooks() })
	if !p.podTemplateSources.webhooksLoaded {
		coverage = append(coverage, "Mutating webhook configurations were not inspected: Radar has not cached them.")
	}
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
	type configurationMatch struct {
		ref             Ref
		created         metav1.Time
		webhooks        []string
		loopbackHooks   []bool
		operations      []string
		loopback        bool
		matchConditions bool
	}
	var matches []*configurationMatch
	byName := make(map[string]*configurationMatch)
	unreadable, unknownNamespace := false, false
	for _, webhook := range p.podTemplateSources.webhooks {
		if !access.canRead(webhook.configuration) {
			unreadable = true
			continue
		}
		if !webhook.namespace.Empty() && namespaceLabels == nil {
			unknownNamespace = true
			continue
		}
		operations := webhook.matchingOperations(pod, namespaceLabels, allowUpdate)
		if len(operations) == 0 {
			continue
		}
		match := byName[webhook.configuration.Name]
		if match == nil {
			match = &configurationMatch{ref: webhook.configuration, created: webhook.created, loopback: true}
			byName[webhook.configuration.Name] = match
			matches = append(matches, match)
		}
		match.webhooks = append(match.webhooks, webhook.name)
		match.loopbackHooks = append(match.loopbackHooks, webhook.loopback)
		for _, operation := range operations {
			if !slices.Contains(match.operations, operation) {
				match.operations = append(match.operations, operation)
			}
		}
		// A configuration is listed last only when every matching webhook in
		// it is served from a loopback endpoint.
		match.loopback = match.loopback && webhook.loopback
		match.matchConditions = match.matchConditions || webhook.matchConditions
	}
	sort.SliceStable(matches, func(a, b int) bool {
		if matches[a].loopback != matches[b].loopback {
			return !matches[a].loopback
		}
		return matches[a].ref.Name < matches[b].ref.Name
	})
	for _, match := range matches {
		names := slices.Clone(match.webhooks)
		if !match.loopback {
			for idx, loopback := range match.loopbackHooks {
				if loopback {
					names[idx] += " (loopback endpoint)"
				}
			}
		}
		message := fmt.Sprintf("Mutating webhook %s / %s currently matches this Pod's %s rules and selectors", match.ref.Name, strings.Join(names, ", "), strings.Join(match.operations, "/"))
		if match.matchConditions {
			message += " (matchConditions present, not evaluated)"
		}
		if match.loopback {
			message += " (served from a control-plane loopback endpoint)"
		}
		if !match.created.IsZero() {
			message += "; configuration created " + match.created.UTC().Format(time.RFC3339)
		}
		candidates = append(candidates, templateCandidate{ref: match.ref, message: message, loopback: match.loopback})
	}
	if unreadable {
		coverage = append(coverage, "Some mutating webhook configurations are not readable by the caller and were not inspected.")
	}
	if unknownNamespace {
		coverage = append(coverage, "Webhooks with namespace selectors were not evaluated: the Pod's namespace labels are not readable.")
	}
	return candidates, coverage
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
	p.podTemplateSources.prioritiesLoaded = true
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
	p.podTemplateSources.webhooksLoaded = true
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
			created: config.CreationTimestamp, name: webhook.Name, namespace: ns, object: obj,
			loopback: loopbackWebhookURL(webhook.ClientConfig.URL), matchConditions: len(webhook.MatchConditions) > 0}
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

func loopbackWebhookURL(raw *string) bool {
	if raw == nil {
		return false
	}
	u, err := url.Parse(*raw)
	if err != nil {
		return false
	}
	if u.Hostname() == "localhost" {
		return true
	}
	ip := net.ParseIP(u.Hostname())
	return ip != nil && ip.IsLoopback()
}

func ruleIncludes(values []string, value string) bool {
	for _, v := range values {
		if v == value || v == "*" {
			return true
		}
	}
	return false
}
