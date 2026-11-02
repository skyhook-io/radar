package issues

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
)

// An admission rejection that names a webhook says which call failed but not
// why; one that only says the create timed out names nothing. This pass adds
// what the configuration and the backend's observed state show: failurePolicy,
// timeoutSeconds, the backend Service's ready pods and, for a call that timed
// out, NetworkPolicies that may keep the API server from reaching it.

const (
	factAdmissionWebhookCandidates = "admission_webhook_candidates"
	maxAdmissionWebhookCandidates  = 5
)

// admissionWebhookCallTimedOut matches a webhook call that waited out its
// deadline rather than failing at once (refused, no endpoints, x509): the
// shape a NetworkPolicy dropping the API server's packets produces.
var admissionWebhookCallTimedOut = regexp.MustCompile(`(?i)context deadline exceeded|i/o timeout|Client\.Timeout|TLS handshake timeout`)

// admissionPodCreators are the kinds whose FailedCreate is a Pod create.
// A CronJob row is a Job's failure folded into its CronJob, unless the
// CronJob controller failed creating the Job itself ("Error creating job:").
var admissionPodCreators = map[string]string{
	"ReplicaSet":  "apps",
	"Deployment":  "apps",
	"StatefulSet": "apps",
	"DaemonSet":   "apps",
	"Job":         "batch",
	"CronJob":     "batch",
}

func isPodCreateAdmissionFailure(i Issue) bool {
	if i.Category != issuesapi.CategoryAdmissionWebhookBlocking || (i.Reason != "WebhookUnavailable" && i.Reason != "CreateTimeout") ||
		i.Namespace == "" || admissionPodCreators[i.Kind] != i.Group {
		return false
	}
	if i.Kind == "CronJob" && strings.Contains(strings.ToLower(i.Message), "error creating job:") {
		return false
	}
	// RelatedIssues composes (and so enriches) before grouping, and a grouped
	// row can keep its representative's context.
	return i.DiagnosticContext == nil || !slices.ContainsFunc(i.DiagnosticContext.Facts, func(f issuesapi.DiagnosticFact) bool {
		return f.Type == factAdmissionWebhookCandidates
	})
}

type admissionWebhookCandidateProvider interface {
	admissionWebhookCandidatesFact(Issue, podTemplateAccess) *issuesapi.DiagnosticFact
}

func enrichAdmissionWebhookCandidates(shaped []Issue, p Provider, access podTemplateAccess) []Issue {
	provider, ok := p.(admissionWebhookCandidateProvider)
	if !ok {
		return shaped
	}
	var out []Issue
	for idx := range shaped {
		i := shaped[idx]
		if !isPodCreateAdmissionFailure(i) {
			continue
		}
		fact := provider.admissionWebhookCandidatesFact(i, access)
		if fact == nil {
			continue
		}
		if out == nil {
			out = append([]Issue(nil), shaped...)
		}
		ctx := issuesapi.DiagnosticContext{Role: issuesapi.DiagnosticRoleCandidate}
		if i.DiagnosticContext != nil {
			ctx = *i.DiagnosticContext
			ctx.Role = issuesapi.DiagnosticRoleCandidate
			ctx.Facts = slices.Clone(ctx.Facts)
		}
		ctx.Facts = append(ctx.Facts, *fact)
		out[idx].DiagnosticContext = &ctx
	}
	if out == nil {
		return shaped
	}
	return out
}

// admissionWebhook is one webhook's Pod CREATE scope and call settings.
type admissionWebhook struct {
	configuration   Ref
	name            string
	mutating        bool
	podCreate       bool
	namespace       labels.Selector
	objectSelector  bool
	matchConditions bool
	failurePolicy   string
	timeoutSeconds  int32
	service         *admissionv1.ServiceReference
}

type admissionWebhookSet struct {
	webhooks []admissionWebhook
	// unchecked names the configuration kinds that could not be inspected,
	// with the reason.
	unchecked []string
}

func (p *CacheProvider) admissionWebhooks(access podTemplateAccess) admissionWebhookSet {
	var set admissionWebhookSet
	for _, kind := range []string{"MutatingWebhookConfiguration", "ValidatingWebhookConfiguration"} {
		const group = "admissionregistration.k8s.io"
		if access.cluster != nil && !access.cluster(kind, group) {
			set.unchecked = append(set.unchecked, kind+"s were not checked: the caller cannot list them.")
			continue
		}
		if p.dynamic == nil || p.discovery == nil {
			set.unchecked = append(set.unchecked, kind+"s were not checked: Radar has not cached them.")
			continue
		}
		gvr, ok := p.discovery.GetGVRWithGroup(kind, group)
		if !ok || !p.dynamic.IsClusterWideSynced(gvr) {
			set.unchecked = append(set.unchecked, kind+"s were not checked: Radar has not cached them.")
			continue
		}
		items, err := p.dynamic.ListWatched(gvr)
		if err != nil {
			set.unchecked = append(set.unchecked, kind+"s were not checked: Radar has not cached them.")
			continue
		}
		unreadable := false
		for _, item := range items {
			ref := Ref{Group: group, Kind: kind, Name: item.GetName()}
			if !access.canRead(ref) {
				unreadable = true
				continue
			}
			set.webhooks = append(set.webhooks, compileAdmissionWebhooks(ref, item.Object)...)
		}
		if unreadable {
			set.unchecked = append(set.unchecked, "Some "+kind+"s are not readable by the caller and were not checked.")
		}
	}
	return set
}

func compileAdmissionWebhooks(ref Ref, obj map[string]any) []admissionWebhook {
	type webhookSpec struct {
		name              string
		rules             []admissionv1.RuleWithOperations
		namespaceSelector *metav1.LabelSelector
		objectSelector    *metav1.LabelSelector
		matchConditions   int
		failurePolicy     *admissionv1.FailurePolicyType
		timeoutSeconds    *int32
		service           *admissionv1.ServiceReference
	}
	var specs []webhookSpec
	if ref.Kind == "MutatingWebhookConfiguration" {
		var config admissionv1.MutatingWebhookConfiguration
		if runtime.DefaultUnstructuredConverter.FromUnstructured(obj, &config) != nil {
			return nil
		}
		for _, w := range config.Webhooks {
			specs = append(specs, webhookSpec{w.Name, w.Rules, w.NamespaceSelector, w.ObjectSelector, len(w.MatchConditions), w.FailurePolicy, w.TimeoutSeconds, w.ClientConfig.Service})
		}
	} else {
		var config admissionv1.ValidatingWebhookConfiguration
		if runtime.DefaultUnstructuredConverter.FromUnstructured(obj, &config) != nil {
			return nil
		}
		for _, w := range config.Webhooks {
			specs = append(specs, webhookSpec{w.Name, w.Rules, w.NamespaceSelector, w.ObjectSelector, len(w.MatchConditions), w.FailurePolicy, w.TimeoutSeconds, w.ClientConfig.Service})
		}
	}
	out := make([]admissionWebhook, 0, len(specs))
	for _, spec := range specs {
		ns := labels.Everything()
		if spec.namespaceSelector != nil {
			selector, err := metav1.LabelSelectorAsSelector(spec.namespaceSelector)
			if err != nil {
				continue
			}
			ns = selector
		}
		// The API server defaults: failurePolicy Fail, timeoutSeconds 10.
		failurePolicy := string(admissionv1.Fail)
		if spec.failurePolicy != nil {
			failurePolicy = string(*spec.failurePolicy)
		}
		timeout := int32(10)
		if spec.timeoutSeconds != nil {
			timeout = *spec.timeoutSeconds
		}
		create, _ := podRuleOperations(spec.rules)
		out = append(out, admissionWebhook{
			configuration: ref, name: spec.name, mutating: ref.Kind == "MutatingWebhookConfiguration",
			podCreate: create, namespace: ns,
			objectSelector:  spec.objectSelector != nil && (len(spec.objectSelector.MatchLabels) > 0 || len(spec.objectSelector.MatchExpressions) > 0),
			matchConditions: spec.matchConditions > 0,
			failurePolicy:   failurePolicy, timeoutSeconds: timeout, service: spec.service,
		})
	}
	return out
}

func (p *CacheProvider) admissionWebhookCandidatesFact(issue Issue, access podTemplateAccess) *issuesapi.DiagnosticFact {
	if p == nil || p.cache == nil {
		return nil
	}
	if issue.Reason == "WebhookUnavailable" {
		return p.namedWebhookFact(issue, access)
	}
	return p.createTimeoutFact(issue, access)
}

// namedWebhookFact describes the webhook a failed call names. The message
// proves the call was made, so its scope is not re-checked.
func (p *CacheProvider) namedWebhookFact(issue Issue, access podTemplateAccess) *issuesapi.DiagnosticFact {
	blocker, ok := k8s.ParseAdmissionBlocker(issue.Message)
	if !ok || blocker.WebhookName == "" {
		return nil
	}
	timedOut := admissionWebhookCallTimedOut.MatchString(issue.Message)
	var lines []string
	var refs []Ref
	for _, w := range p.admissionWebhooks(access).webhooks {
		if w.name != blocker.WebhookName {
			continue
		}
		line, lineRefs, _ := p.describeAdmissionWebhook(w, timedOut, access)
		lines = append(lines, line)
		refs = append(refs, lineRefs...)
	}
	if len(lines) == 0 {
		return nil
	}
	return &issuesapi.DiagnosticFact{
		Type:       factAdmissionWebhookCandidates,
		Message:    fmt.Sprintf("Failing call: %s.", strings.Join(lines, ". ")),
		Confidence: issuesapi.ConfidenceHigh,
		Refs:       limitRefs(dedupeRefs(refs), maxDiagnosticRefs),
	}
}

// createTimeoutFact lists the webhooks that match Pod creation in the
// issue's namespace. The timeout names no webhook, so they are candidates.
func (p *CacheProvider) createTimeoutFact(issue Issue, access podTemplateAccess) *issuesapi.DiagnosticFact {
	set := p.admissionWebhooks(access)
	nsLabels, nsKnown := p.namespaceLabels(issue.Namespace, access)
	type candidate struct {
		webhook admissionWebhook
		suspect bool
		line    string
		refs    []Ref
	}
	var candidates []candidate
	selectorsUnknown := false
	for _, w := range set.webhooks {
		if !w.podCreate {
			continue
		}
		if !w.namespace.Empty() {
			if !nsKnown {
				selectorsUnknown = true
				continue
			}
			if !w.namespace.Matches(nsLabels) {
				continue
			}
		}
		line, refs, policyBlocked := p.describeAdmissionWebhook(w, true, access)
		if w.objectSelector || w.matchConditions {
			line += " (its objectSelector or matchConditions may exclude these Pods)"
		}
		candidates = append(candidates, candidate{webhook: w, suspect: policyBlocked, line: line, refs: refs})
	}
	// Webhooks a NetworkPolicy may cut off first, then the longest waits.
	sort.SliceStable(candidates, func(a, b int) bool {
		if candidates[a].suspect != candidates[b].suspect {
			return candidates[a].suspect
		}
		return candidates[a].webhook.timeoutSeconds > candidates[b].webhook.timeoutSeconds
	})

	var msg strings.Builder
	msg.WriteString("The API server timed out creating Pods; the error does not say where the time went.")
	var refs []Ref
	if len(candidates) == 0 {
		if len(set.unchecked) == 0 && !selectorsUnknown {
			fmt.Fprintf(&msg, " No admission webhook matches Pod creation in namespace %s, so the API server or etcd is the more likely source of the delay.", issue.Namespace)
		}
	} else {
		fmt.Fprintf(&msg, " Admission webhooks that match Pod creation in namespace %s, any of which may have been slow or unreachable:", issue.Namespace)
		for idx, c := range candidates {
			if idx == maxAdmissionWebhookCandidates {
				fmt.Fprintf(&msg, " (and %d more)", len(candidates)-idx)
				break
			}
			fmt.Fprintf(&msg, " (%d) %s.", idx+1, c.line)
			refs = append(refs, c.refs[0])
		}
		var mutating int
		var total int32
		for _, c := range candidates {
			if c.webhook.mutating {
				mutating++
				total += c.webhook.timeoutSeconds
			}
		}
		if mutating > 1 {
			fmt.Fprintf(&msg, " Mutating webhooks are called one after another; if each of the %d matching ones waited its full timeout, the create would take %ds.", mutating, total)
		}
	}
	if selectorsUnknown {
		fmt.Fprintf(&msg, " Webhooks with a namespaceSelector were not checked: namespace %s's labels are not readable.", issue.Namespace)
	}
	for _, note := range set.unchecked {
		msg.WriteString(" " + note)
	}
	return &issuesapi.DiagnosticFact{
		Type:       factAdmissionWebhookCandidates,
		Message:    msg.String(),
		Confidence: issuesapi.ConfidenceLow,
		Refs:       limitRefs(dedupeRefs(refs), maxDiagnosticRefs),
	}
}

func (p *CacheProvider) namespaceLabels(namespace string, access podTemplateAccess) (labels.Set, bool) {
	if !access.canRead(Ref{Kind: "Namespace", Name: namespace}) || p.cache.Namespaces() == nil {
		return nil, false
	}
	ns, err := p.cache.Namespaces().Get(namespace)
	if err != nil {
		return nil, false
	}
	return labels.Set(ns.Labels), true
}

// describeAdmissionWebhook returns one webhook's settings and backend state,
// the refs it rests on (the configuration first), and whether it names
// NetworkPolicies that may block the API server.
func (p *CacheProvider) describeAdmissionWebhook(w admissionWebhook, timedOut bool, access podTemplateAccess) (string, []Ref, bool) {
	kind := "validating"
	if w.mutating {
		kind = "mutating"
	}
	line := fmt.Sprintf("%s webhook %s (%s %s): failurePolicy %s, timeout %ds", kind, w.name, w.configuration.Kind, w.configuration.Name, w.failurePolicy, w.timeoutSeconds)
	refs := []Ref{w.configuration}
	if w.service == nil {
		return line + "; it calls a URL, which Radar does not check", refs, false
	}
	svcRef := Ref{Kind: "Service", Namespace: w.service.Namespace, Name: w.service.Name}
	if !access.canRead(svcRef) {
		return line, refs, false
	}
	backend, pods, known := p.webhookBackendState(w.service)
	if !known {
		return line + fmt.Sprintf("; backend Service %s/%s was not checked", w.service.Namespace, w.service.Name), refs, false
	}
	line += "; " + backend
	refs = append(refs, svcRef)
	if !timedOut || len(pods) == 0 {
		return line, refs, false
	}
	policies := p.selectorOnlyIngressPolicies(w.service.Namespace, pods, access)
	if len(policies) == 0 {
		return line, refs, false
	}
	names := make([]string, 0, len(policies))
	for _, np := range policies {
		names = append(names, np.Name)
		refs = append(refs, Ref{Group: "networking.k8s.io", Kind: "NetworkPolicy", Namespace: np.Namespace, Name: np.Name})
	}
	line += fmt.Sprintf("; NetworkPolicy %s selects those pods and admits ingress only from pod or namespace selectors, so it may block the API server, which usually calls webhooks from outside the pod network", strings.Join(names, ", "))
	return line, refs, true
}

// webhookBackendState describes a webhook's backend Service from the cache.
// known=false when Services or Pods are not fully collected for its namespace,
// so an empty answer would prove nothing.
func (p *CacheProvider) webhookBackendState(svc *admissionv1.ServiceReference) (string, []*corev1.Pod, bool) {
	name := svc.Namespace + "/" + svc.Name
	if p.cache.Services() == nil || !p.cache.AssessKindRead("services", []string{svc.Namespace}).Complete {
		return "", nil, false
	}
	service, err := p.cache.Services().Services(svc.Namespace).Get(svc.Name)
	if err != nil {
		return fmt.Sprintf("backend Service %s does not exist", name), nil, true
	}
	if len(service.Spec.Selector) == 0 || p.cache.Pods() == nil || !p.cache.AssessKindRead("pods", []string{svc.Namespace}).Complete {
		return "", nil, false
	}
	pods, err := p.cache.Pods().Pods(svc.Namespace).List(labels.SelectorFromSet(service.Spec.Selector))
	if err != nil {
		return "", nil, false
	}
	var ready []*corev1.Pod
	for _, pod := range pods {
		for _, c := range pod.Status.Conditions {
			if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue && pod.DeletionTimestamp == nil {
				ready = append(ready, pod)
				break
			}
		}
	}
	switch len(ready) {
	case 0:
		return fmt.Sprintf("backend Service %s has no ready pods", name), nil, true
	case 1:
		return fmt.Sprintf("backend Service %s has 1 ready pod", name), ready, true
	default:
		return fmt.Sprintf("backend Service %s has %d ready pods", name, len(ready)), ready, true
	}
}

// selectorOnlyIngressPolicies returns the NetworkPolicies that isolate the
// backend pods for ingress when none of them has a rule that could admit the
// API server: a rule with no `from` (any source) or an ipBlock peer. Port
// lists are ignored, so a rule that admits any source on another port still
// counts as admitting. nil when any policy admits, or when policies are not
// fully collected or readable.
func (p *CacheProvider) selectorOnlyIngressPolicies(namespace string, pods []*corev1.Pod, access podTemplateAccess) []*networkingv1.NetworkPolicy {
	lister := p.cache.NetworkPolicies()
	if lister == nil || !p.cache.AssessKindRead("networkpolicies", []string{namespace}).Complete {
		return nil
	}
	policies, err := lister.NetworkPolicies(namespace).List(labels.Everything())
	if err != nil {
		return nil
	}
	var selecting []*networkingv1.NetworkPolicy
	for _, np := range policies {
		if !access.canRead(Ref{Group: "networking.k8s.io", Kind: "NetworkPolicy", Namespace: np.Namespace, Name: np.Name}) {
			return nil
		}
		if !policyIsolatesIngress(np) {
			continue
		}
		selector, err := metav1.LabelSelectorAsSelector(&np.Spec.PodSelector)
		if err != nil {
			return nil
		}
		if !slices.ContainsFunc(pods, func(pod *corev1.Pod) bool { return selector.Matches(labels.Set(pod.Labels)) }) {
			continue
		}
		for _, rule := range np.Spec.Ingress {
			if len(rule.From) == 0 || slices.ContainsFunc(rule.From, func(peer networkingv1.NetworkPolicyPeer) bool { return peer.IPBlock != nil }) {
				return nil
			}
		}
		selecting = append(selecting, np)
	}
	sort.Slice(selecting, func(a, b int) bool { return selecting[a].Name < selecting[b].Name })
	return selecting
}

func policyIsolatesIngress(np *networkingv1.NetworkPolicy) bool {
	if len(np.Spec.PolicyTypes) == 0 {
		return true
	}
	return slices.Contains(np.Spec.PolicyTypes, networkingv1.PolicyTypeIngress)
}
