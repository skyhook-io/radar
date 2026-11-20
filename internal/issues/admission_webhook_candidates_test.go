package issues

import (
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func initAdmissionWebhooks(t *testing.T, configs ...runtime.Object) {
	t.Helper()
	mutating := schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "mutatingwebhookconfigurations"}
	validating := schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingwebhookconfigurations"}
	var objects []runtime.Object
	for _, config := range configs {
		switch c := config.(type) {
		case *admissionv1.MutatingWebhookConfiguration:
			c.TypeMeta = metav1.TypeMeta{Kind: "MutatingWebhookConfiguration", APIVersion: "admissionregistration.k8s.io/v1"}
		case *admissionv1.ValidatingWebhookConfiguration:
			c.TypeMeta = metav1.TypeMeta{Kind: "ValidatingWebhookConfiguration", APIVersion: "admissionregistration.k8s.io/v1"}
		}
		data, err := runtime.DefaultUnstructuredConverter.ToUnstructured(config)
		if err != nil {
			t.Fatal(err)
		}
		objects = append(objects, &unstructured.Unstructured{Object: data})
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		mutating: "MutatingWebhookConfigurationList", validating: "ValidatingWebhookConfigurationList",
	}, objects...)
	if err := k8s.InitTestDynamicResourceCache(client, []k8s.APIResource{
		{Group: mutating.Group, Version: "v1", Kind: "MutatingWebhookConfiguration", Name: mutating.Resource, Verbs: []string{"list", "watch"}},
		{Group: validating.Group, Version: "v1", Kind: "ValidatingWebhookConfiguration", Name: validating.Resource, Verbs: []string{"list", "watch"}},
	}); err != nil {
		t.Fatal(err)
	}
	cache := k8s.GetDynamicResourceCache()
	for _, gvr := range []schema.GroupVersionResource{mutating, validating} {
		if err := cache.EnsureWatching(gvr); err != nil || !cache.WaitForSync(gvr, 2*time.Second) {
			t.Fatalf("watch %s: %v", gvr.Resource, err)
		}
	}
}

func podCreateRules() []admissionv1.RuleWithOperations {
	return []admissionv1.RuleWithOperations{{Operations: []admissionv1.OperationType{admissionv1.Create}, Rule: admissionv1.Rule{APIGroups: []string{""}, APIVersions: []string{"v1"}, Resources: []string{"pods"}}}}
}

func mutatingPodWebhook(config, name, service string, timeout int32, policy admissionv1.FailurePolicyType) *admissionv1.MutatingWebhookConfiguration {
	return &admissionv1.MutatingWebhookConfiguration{ObjectMeta: metav1.ObjectMeta{Name: config}, Webhooks: []admissionv1.MutatingWebhook{{
		Name: name, Rules: podCreateRules(), TimeoutSeconds: &timeout, FailurePolicy: &policy,
		ClientConfig: admissionv1.WebhookClientConfig{Service: &admissionv1.ServiceReference{Namespace: "policy-system", Name: service}},
	}}}
}

func webhookBackend(name string, ready bool) (*corev1.Service, *corev1.Pod) {
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "policy-system"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": name}}}
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name + "-0", Namespace: "policy-system", Labels: map[string]string{"app": name}},
		Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: status}}}}
	return svc, pod
}

func timeoutIssue() Issue {
	return Issue{Kind: "ReplicaSet", Group: "apps", Namespace: "shop", Name: "web-abc",
		Category: issuesapi.CategoryAdmissionWebhookBlocking, Reason: "CreateTimeout",
		Message: "pod creation blocked: Error creating: Timeout: request did not complete within requested timeout - context deadline exceeded"}
}

// The SREGym shape: several fail-open mutating webhooks whose backends a
// NetworkPolicy isolates, so each call waits out its timeout and the create
// misses the API server's deadline. The error names none of them.
func TestCreateTimeoutListsMatchingWebhooks(t *testing.T) {
	defer k8s.ResetTestState()
	defer k8s.ResetTestDynamicState()
	isolatedSvc, isolatedPod := webhookBackend("isolated", true)
	openSvc, openPod := webhookBackend("open", true)
	downSvc, downPod := webhookBackend("down", false)
	selectorOnly := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "allow-observe", Namespace: "policy-system"}, Spec: networkingv1.NetworkPolicySpec{
		PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "isolated"}},
		Ingress:     []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"name": "observe"}}}}}},
	}}
	ipBlock := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "allow-cidr", Namespace: "policy-system"}, Spec: networkingv1.NetworkPolicySpec{
		PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "open"}},
		Ingress:     []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "10.0.0.0/8"}}}}},
	}}
	if err := k8s.InitTestResourceCache(fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop", Labels: map[string]string{"team": "shop"}}},
		isolatedSvc, isolatedPod, openSvc, openPod, downSvc, downPod, selectorOnly, ipBlock,
	)); err != nil {
		t.Fatal(err)
	}
	otherNamespace := mutatingPodWebhook("elsewhere", "elsewhere.example.com", "open", 30, admissionv1.Ignore)
	otherNamespace.Webhooks[0].NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"team": "billing"}}
	deploymentsOnly := mutatingPodWebhook("deployments", "deployments.example.com", "open", 30, admissionv1.Ignore)
	deploymentsOnly.Webhooks[0].Rules[0].Resources = []string{"deployments"}
	fail := admissionv1.Fail
	validating := &admissionv1.ValidatingWebhookConfiguration{ObjectMeta: metav1.ObjectMeta{Name: "policy"}, Webhooks: []admissionv1.ValidatingWebhook{{
		Name: "validate.example.com", Rules: podCreateRules(), FailurePolicy: &fail,
		NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "shop"}},
		ClientConfig:      admissionv1.WebhookClientConfig{Service: &admissionv1.ServiceReference{Namespace: "policy-system", Name: "down"}},
	}}}
	initAdmissionWebhooks(t,
		mutatingPodWebhook("open", "open.example.com", "open", 12, admissionv1.Ignore),
		mutatingPodWebhook("isolated", "isolated.example.com", "isolated", 8, admissionv1.Ignore),
		otherNamespace, deploymentsOnly, validating,
	)

	fact := NewCacheProvider().admissionWebhookCandidatesFact(timeoutIssue(), podTemplateAccess{})
	if fact == nil {
		t.Fatal("no fact")
	}
	msg := fact.Message
	for _, want := range []string{
		"the error does not say where the time went",
		"(1) mutating webhook isolated.example.com (MutatingWebhookConfiguration isolated): failurePolicy Ignore, timeout 8s; backend Service policy-system/isolated has 1 ready pod; NetworkPolicy allow-observe selects those pods and admits ingress only from pod or namespace selectors; if the cluster's network plugin enforces NetworkPolicies, it may block the API server",
		"(2) mutating webhook open.example.com (MutatingWebhookConfiguration open): failurePolicy Ignore, timeout 12s; backend Service policy-system/open has 1 ready pod.",
		"(3) validating webhook validate.example.com (ValidatingWebhookConfiguration policy): failurePolicy Fail, timeout 10s; backend Service policy-system/down has no ready pods.",
		"Mutating webhooks are called one after another, so several slow ones add up.",
		"If creates that none of these webhooks match also time out, suspect the API server or etcd instead. Find the slow or unreachable backend before removing or bypassing any webhook.",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message missing %q:\n%s", want, msg)
		}
	}
	for _, unwanted := range []string{"elsewhere", "deployments.example.com", "allow-cidr"} {
		if strings.Contains(msg, unwanted) {
			t.Fatalf("message names %q:\n%s", unwanted, msg)
		}
	}
	if fact.Type != factAdmissionWebhookCandidates || fact.Confidence != issuesapi.ConfidenceLow {
		t.Fatalf("fact=%+v", fact)
	}
	if len(fact.Refs) != 3 || fact.Refs[0].Name != "isolated" || fact.Refs[0].Kind != "MutatingWebhookConfiguration" {
		t.Fatalf("refs=%+v", fact.Refs)
	}

	// A caller who cannot read the backend namespace sees the webhook
	// settings but nothing about its Service or NetworkPolicies.
	fact = NewCacheProvider().admissionWebhookCandidatesFact(timeoutIssue(), podTemplateAccess{related: func(ref Ref) bool { return ref.Namespace != "policy-system" }})
	if strings.Contains(fact.Message, "policy-system") || strings.Contains(fact.Message, "NetworkPolicy") {
		t.Fatalf("backend state leaked:\n%s", fact.Message)
	}
	// Without access to one configuration kind, its webhooks are not listed
	// and the fact says that kind was not checked.
	fact = NewCacheProvider().admissionWebhookCandidatesFact(timeoutIssue(), podTemplateAccess{cluster: func(kind, _ string) bool { return kind != "ValidatingWebhookConfiguration" }})
	if strings.Contains(fact.Message, "validate.example.com") || !strings.Contains(fact.Message, "ValidatingWebhookConfigurations were not checked: the caller cannot list them.") {
		t.Fatalf("unreadable kind:\n%s", fact.Message)
	}
}

func TestCreateTimeoutWithoutMatchingWebhooks(t *testing.T) {
	defer k8s.ResetTestState()
	defer k8s.ResetTestDynamicState()
	if err := k8s.InitTestResourceCache(fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}})); err != nil {
		t.Fatal(err)
	}
	scoped := mutatingPodWebhook("scoped", "scoped.example.com", "open", 10, admissionv1.Fail)
	scoped.Webhooks[0].NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"team": "billing"}}
	initAdmissionWebhooks(t, scoped)

	fact := NewCacheProvider().admissionWebhookCandidatesFact(timeoutIssue(), podTemplateAccess{})
	if fact == nil || !strings.Contains(fact.Message, "No admission webhook matches Pod creation in namespace shop, so the API server or etcd is the more likely source of the delay.") {
		t.Fatalf("fact=%+v", fact)
	}
	// Unreadable namespace labels leave namespaceSelector webhooks unchecked,
	// so the "no webhook" conclusion is withheld.
	fact = NewCacheProvider().admissionWebhookCandidatesFact(timeoutIssue(), podTemplateAccess{related: func(ref Ref) bool { return ref.Kind != "Namespace" }})
	if strings.Contains(fact.Message, "No admission webhook") || !strings.Contains(fact.Message, "namespace shop's labels are not readable") {
		t.Fatalf("unknown labels:\n%s", fact.Message)
	}
}

func TestNamedWebhookFailureDescribesBackend(t *testing.T) {
	defer k8s.ResetTestState()
	defer k8s.ResetTestDynamicState()
	svc, pod := webhookBackend("isolated", true)
	denyAll := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "deny-all", Namespace: "policy-system"}, Spec: networkingv1.NetworkPolicySpec{}}
	if err := k8s.InitTestResourceCache(fake.NewClientset(svc, pod, denyAll)); err != nil {
		t.Fatal(err)
	}
	initAdmissionWebhooks(t, mutatingPodWebhook("injector", "inject.example.com", "isolated", 5, admissionv1.Fail))

	issue := timeoutIssue()
	issue.Reason = "WebhookUnavailable"
	issue.Message = `pod creation blocked: Error creating: Internal error occurred: failed calling webhook "inject.example.com": failed to call webhook: Post "https://isolated.policy-system.svc:443/inject?timeout=5s": context deadline exceeded`
	fact := NewCacheProvider().admissionWebhookCandidatesFact(issue, podTemplateAccess{})
	want := "Failing call: mutating webhook inject.example.com (MutatingWebhookConfiguration injector): failurePolicy Fail, timeout 5s; backend Service policy-system/isolated has 1 ready pod; NetworkPolicy deny-all selects those pods"
	if fact == nil || !strings.HasPrefix(fact.Message, want) || fact.Confidence != issuesapi.ConfidenceHigh {
		t.Fatalf("fact=%+v", fact)
	}
	// Pods the caller cannot read are not counted.
	fact = NewCacheProvider().admissionWebhookCandidatesFact(issue, podTemplateAccess{related: func(ref Ref) bool { return ref.Kind != "Pod" }})
	if fact == nil || strings.Contains(fact.Message, "ready pod") || !strings.Contains(fact.Message, "backend Service policy-system/isolated was not checked") {
		t.Fatalf("unreadable pods fact=%+v", fact)
	}
	// A TLS failure is not a reachability problem: no NetworkPolicy hedge.
	issue.Message = `pod creation blocked: Error creating: Internal error occurred: failed calling webhook "inject.example.com": failed to call webhook: Post "https://isolated.policy-system.svc:443/inject?timeout=5s": tls: failed to verify certificate: x509: certificate signed by unknown authority`
	fact = NewCacheProvider().admissionWebhookCandidatesFact(issue, podTemplateAccess{})
	if fact == nil || strings.Contains(fact.Message, "NetworkPolicy") {
		t.Fatalf("x509 fact=%+v", fact)
	}
	// A webhook Radar cannot see adds nothing.
	issue.Message = `pod creation blocked: Error creating: Internal error occurred: failed calling webhook "other.example.com": context deadline exceeded`
	if fact := NewCacheProvider().admissionWebhookCandidatesFact(issue, podTemplateAccess{}); fact != nil {
		t.Fatalf("unknown webhook fact=%+v", fact)
	}
}

func TestAdmissionWebhookCandidatesOnlyOnPodCreateFailures(t *testing.T) {
	defer k8s.ResetTestState()
	defer k8s.ResetTestDynamicState()
	if err := k8s.InitTestResourceCache(fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}})); err != nil {
		t.Fatal(err)
	}
	initAdmissionWebhooks(t, mutatingPodWebhook("open", "open.example.com", "open", 10, admissionv1.Ignore))
	denied := timeoutIssue()
	denied.Reason = "WebhookDenied"
	// A Job's pod-create failure folded into its CronJob keeps the fact; the
	// CronJob controller failing to create the Job does not.
	cronJobPods := timeoutIssue()
	cronJobPods.Kind, cronJobPods.Group = "CronJob", "batch"
	cronJobJobs := cronJobPods
	cronJobJobs.Message = "pod creation blocked: Error creating job: Timeout: request did not complete within requested timeout - context deadline exceeded"
	out := enrichAdmissionWebhookCandidates([]Issue{timeoutIssue(), denied, cronJobPods, cronJobJobs}, NewCacheProvider(), podTemplateAccess{})
	for _, idx := range []int{0, 2} {
		if out[idx].DiagnosticContext == nil || out[idx].DiagnosticContext.Facts[0].Type != factAdmissionWebhookCandidates || out[idx].DiagnosticContext.Role != issuesapi.DiagnosticRoleCandidate {
			t.Fatalf("issue %d context=%+v", idx, out[idx].DiagnosticContext)
		}
	}
	if out[1].DiagnosticContext != nil || out[3].DiagnosticContext != nil {
		t.Fatalf("unexpected context: %+v / %+v", out[1].DiagnosticContext, out[3].DiagnosticContext)
	}
	// A second pass (RelatedIssues enriches flat rows, then the grouped
	// match) does not repeat the fact.
	again := enrichAdmissionWebhookCandidates(out, NewCacheProvider(), podTemplateAccess{})
	if len(again[0].DiagnosticContext.Facts) != 1 {
		t.Fatalf("facts=%+v", again[0].DiagnosticContext.Facts)
	}
}

// On a real cluster a dozen long-standing webhooks match every Pod; one
// created minutes ago with a certain scope is listed ahead of them.
func TestCreateTimeoutRanksRecentConfigurationsFirst(t *testing.T) {
	defer k8s.ResetTestState()
	defer k8s.ResetTestDynamicState()
	if err := k8s.InitTestResourceCache(fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}})); err != nil {
		t.Fatal(err)
	}
	old := mutatingPodWebhook("addon", "addon.example.com", "addon", 30, admissionv1.Ignore)
	old.CreationTimestamp = metav1.NewTime(time.Now().Add(-90 * 24 * time.Hour))
	scoped := mutatingPodWebhook("scoped-new", "scoped.example.com", "scoped", 10, admissionv1.Ignore)
	scoped.CreationTimestamp = metav1.NewTime(time.Now().Add(-10 * time.Minute))
	scoped.Webhooks[0].ObjectSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"inject": "true"}}
	fresh := mutatingPodWebhook("fresh", "fresh.example.com", "fresh", 5, admissionv1.Ignore)
	fresh.CreationTimestamp = metav1.NewTime(time.Now().Add(-5 * time.Minute))
	initAdmissionWebhooks(t, old, scoped, fresh)

	msg := NewCacheProvider().admissionWebhookCandidatesFact(timeoutIssue(), podTemplateAccess{}).Message
	first, second, third := strings.Index(msg, "(1) mutating webhook fresh.example.com (MutatingWebhookConfiguration fresh, created 5m ago)"),
		strings.Index(msg, "(2) mutating webhook scoped.example.com (MutatingWebhookConfiguration scoped-new, created 10m ago; its objectSelector or matchConditions may exclude these Pods)"),
		strings.Index(msg, "(3) mutating webhook addon.example.com (MutatingWebhookConfiguration addon): failurePolicy Ignore, timeout 30s")
	if first < 0 || second < first || third < second {
		t.Fatalf("order:\n%s", msg)
	}
}

func TestNamedWebhookDeclaredTwice(t *testing.T) {
	if got := namedWebhookMessage("hook.example.com", []string{"a", "b"}); !strings.HasPrefix(got, "The error names webhook hook.example.com, which 2 configurations declare; it does not say which one was called: a. b.") {
		t.Fatal(got)
	}
}
