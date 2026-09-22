package issues

import (
	"fmt"
	"k8s.io/apimachinery/pkg/types"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func TestPodTemplateProviderOwnershipAndAuthorization(t *testing.T) {
	for _, kind := range []string{"ReplicaSet", "Job"} {
		t.Run(kind, func(t *testing.T) {
			defer k8s.ResetTestState()
			p := templateTestPod()
			p.Name = "worker"
			p.Namespace = "test"
			p.UID = "pod"
			original := p.Spec.DeepCopy()
			p.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory] = resource.MustParse("64Mi")
			group := "apps"
			if kind == "Job" {
				group = "batch"
			}
			controller := true
			p.OwnerReferences = []metav1.OwnerReference{{Kind: kind, APIVersion: group + "/v1", Name: "owner", UID: "owner", Controller: &controller}}
			metadata := metav1.ObjectMeta{Name: "owner", Namespace: "test", UID: "owner"}
			var owner runtime.Object = &appsv1.ReplicaSet{ObjectMeta: metadata, Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{Spec: *original}}}
			if kind == "Job" {
				owner = &batchv1.Job{ObjectMeta: metadata, Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: *original}}}
			}
			wrongUID := p.DeepCopy()
			wrongUID.Name = "recreated"
			wrongUID.OwnerReferences[0].UID = "old"
			wrongGroup := p.DeepCopy()
			wrongGroup.Name = "collision"
			wrongGroup.OwnerReferences[0].APIVersion = "example.test/v1"
			var unsupported []runtime.Object
			for _, kind := range []string{"StatefulSet", "DaemonSet", "Rollout"} {
				other := p.DeepCopy()
				other.Name = strings.ToLower(kind)
				other.OwnerReferences[0].Kind = kind
				if kind == "Rollout" {
					other.OwnerReferences[0].APIVersion = "argoproj.io/v1alpha1"
				}
				unsupported = append(unsupported, other)
			}
			orphan := p.DeepCopy()
			orphan.Name = "orphan"
			orphan.OwnerReferences = nil
			nonController := p.DeepCopy()
			nonController.Name = "reference"
			no := false
			nonController.OwnerReferences[0].Controller = &no
			if err := k8s.InitTestResourceCache(fake.NewClientset(append(unsupported, p, wrongUID, wrongGroup, orphan, nonController, owner)...)); err != nil {
				t.Fatal(err)
			}
			provider := NewCacheProvider()
			issue := Issue{Kind: "Pod", Namespace: "test", Name: p.Name, Category: issuesapi.CategoryOOMKilled}
			fact := provider.podTemplateFact(issue, podTemplateAccess{})
			if fact == nil || len(fact.Refs) != 2 || !strings.Contains(fact.Message, "64Mi") || !strings.Contains(fact.Message, "Writer and timing are unknown") {
				t.Fatalf("fact=%+v", fact)
			}
			for _, name := range []string{"recreated", "collision", "orphan", "reference", "statefulset", "daemonset", "rollout"} {
				issue.Name = name
				if got := provider.podTemplateFact(issue, podTemplateAccess{}); got != nil {
					t.Fatalf("%s comparison=%+v", name, got)
				}
			}
			issue.Name = p.Name
			for _, denied := range []string{"Pod", kind} {
				if got := provider.podTemplateFact(issue, podTemplateAccess{related: func(ref Ref) bool { return ref.Kind != denied }}); got != nil {
					t.Fatalf("denied %s leaked %+v", denied, got)
				}
			}
			if got := provider.podTemplateFact(issue, podTemplateAccess{cluster: func(string, string) bool { return false }}); got == nil {
				t.Fatal("unreadable candidates suppressed observed difference")
			}
		})
	}
}

func TestTemplateDefaultCandidates(t *testing.T) {
	lr := &corev1.LimitRange{Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{Type: corev1.LimitTypeContainer, Default: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")}, DefaultRequest: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")}}}}}
	for _, tc := range []struct {
		name, kind    string
		key           corev1.ResourceName
		value         string
		missing, want bool
	}{
		{"omitted limit", "limits", corev1.ResourceMemory, "64Mi", true, true},
		{"explicit limit", "limits", corev1.ResourceMemory, "64Mi", false, false},
		{"different default", "limits", corev1.ResourceMemory, "32Mi", true, false},
		{"request default", "requests", corev1.ResourceCPU, "100m", true, true},
		{"wrong resource key", "limits", corev1.ResourceCPU, "64Mi", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := matchingTemplateDefaults(lr, []templateDifference{{resource: tc.key, resourceKind: tc.kind, value: resource.MustParse(tc.value), templateMissing: tc.missing}})
			if got != tc.want {
				t.Fatalf("match=%v want %v", got, tc.want)
			}
		})
	}
}

func TestTemplateWebhookScopeAndDecoys(t *testing.T) {
	pod := templateTestPod()
	pod.Labels = map[string]string{"app": "checkout"}
	pod.CreationTimestamp = metav1.NewTime(time.Now())
	base := admissionv1.MutatingWebhookConfiguration{ObjectMeta: metav1.ObjectMeta{Name: "candidate", CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour))}, Webhooks: []admissionv1.MutatingWebhook{{Name: "mutate.example.test", Rules: []admissionv1.RuleWithOperations{{Operations: []admissionv1.OperationType{admissionv1.Create}, Rule: admissionv1.Rule{APIGroups: []string{""}, APIVersions: []string{"v1"}, Resources: []string{"pods"}}}}}}}
	cases := []struct {
		name   string
		change func(*admissionv1.MutatingWebhookConfiguration)
		ns     labels.Set
		want   string
	}{
		{"normal", func(*admissionv1.MutatingWebhookConfiguration) {}, nil, "CREATE"},
		{"deployment decoy", func(c *admissionv1.MutatingWebhookConfiguration) {
			c.Webhooks[0].Rules[0].Resources = []string{"deployments"}
		}, nil, ""},
		{"wrong api group", func(c *admissionv1.MutatingWebhookConfiguration) { c.Webhooks[0].Rules[0].APIGroups = []string{"apps"} }, nil, ""},
		{"subresource decoy", func(c *admissionv1.MutatingWebhookConfiguration) {
			c.Webhooks[0].Rules[0].Resources = []string{"pods/*"}
		}, nil, ""},
		{"wildcard resources", func(c *admissionv1.MutatingWebhookConfiguration) { c.Webhooks[0].Rules[0].Resources = []string{"*/*"} }, nil, "CREATE"},
		{"object decoy", func(c *admissionv1.MutatingWebhookConfiguration) {
			c.Webhooks[0].ObjectSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"app": "another"}}
		}, nil, ""},
		{"namespace selector", func(c *admissionv1.MutatingWebhookConfiguration) {
			c.Webhooks[0].NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"team": "store"}}
		}, labels.Set{"team": "store"}, "CREATE"},
		{"unknown namespace labels", func(c *admissionv1.MutatingWebhookConfiguration) {
			c.Webhooks[0].NamespaceSelector = &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "skip", Operator: metav1.LabelSelectorOpDoesNotExist}}}
		}, nil, ""},
		{"known empty namespace labels", func(c *admissionv1.MutatingWebhookConfiguration) {
			c.Webhooks[0].NamespaceSelector = &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "skip", Operator: metav1.LabelSelectorOpDoesNotExist}}}
		}, labels.Set{}, "CREATE"},
		{"cel unverified", func(c *admissionv1.MutatingWebhookConfiguration) {
			c.Webhooks[0].MatchConditions = []admissionv1.MatchCondition{{Name: "filter", Expression: "true"}}
		}, nil, ""},
		{"created after pod", func(c *admissionv1.MutatingWebhookConfiguration) {
			c.CreationTimestamp = metav1.NewTime(time.Now().Add(time.Hour))
		}, nil, ""},
		{"later update", func(c *admissionv1.MutatingWebhookConfiguration) {
			c.CreationTimestamp = metav1.NewTime(time.Now().Add(time.Hour))
			c.Webhooks[0].Rules[0].Operations = []admissionv1.OperationType{admissionv1.OperationAll}
		}, nil, "UPDATE"},
		{"delete only", func(c *admissionv1.MutatingWebhookConfiguration) {
			c.Webhooks[0].Rules[0].Operations = []admissionv1.OperationType{admissionv1.Delete}
		}, nil, ""},
		{"cluster scope", func(c *admissionv1.MutatingWebhookConfiguration) {
			scope := admissionv1.ClusterScope
			c.Webhooks[0].Rules[0].Scope = &scope
		}, nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config := base.DeepCopy()
			tc.change(config)
			var ops []string
			for _, w := range compileTemplateWebhooks(config) {
				ops = append(ops, w.matchingOperations(pod, tc.ns, true)...)
			}
			if got := strings.Join(ops, "/"); got != tc.want {
				t.Fatalf("operations=%s want %s", got, tc.want)
			}
		})
	}
}

func TestPodTemplateCurrentCandidatesAreAuthorizedAndBounded(t *testing.T) {
	defer k8s.ResetTestState()
	defer k8s.ResetTestDynamicState()
	pod := templateTestPod()
	pod.Name = "worker"
	pod.Namespace = "test"
	pod.Spec.PriorityClassName = "batch"
	lr := &corev1.LimitRange{ObjectMeta: metav1.ObjectMeta{Name: "defaults", Namespace: "test"}, Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{Type: corev1.LimitTypeContainer, DefaultRequest: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}}}}}
	if err := k8s.InitTestResourceCache(fake.NewClientset(lr, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test"}})); err != nil {
		t.Fatal(err)
	}
	pc := &schedulingv1.PriorityClass{TypeMeta: metav1.TypeMeta{Kind: "PriorityClass", APIVersion: "scheduling.k8s.io/v1"}, ObjectMeta: metav1.ObjectMeta{Name: "batch"}, GlobalDefault: true}
	wh := &admissionv1.MutatingWebhookConfiguration{TypeMeta: metav1.TypeMeta{Kind: "MutatingWebhookConfiguration", APIVersion: "admissionregistration.k8s.io/v1"}, ObjectMeta: metav1.ObjectMeta{Name: "mutator"}, Webhooks: []admissionv1.MutatingWebhook{{Name: "candidate.test", Rules: []admissionv1.RuleWithOperations{{Operations: []admissionv1.OperationType{admissionv1.Create}, Rule: admissionv1.Rule{APIGroups: []string{""}, APIVersions: []string{"v1"}, Resources: []string{"pods"}}}}}}}
	var objects []runtime.Object
	for _, obj := range []runtime.Object{pc, wh} {
		data, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
		if err != nil {
			t.Fatal(err)
		}
		objects = append(objects, &unstructured.Unstructured{Object: data})
	}
	pcGVR := schema.GroupVersionResource{Group: "scheduling.k8s.io", Version: "v1", Resource: "priorityclasses"}
	whGVR := schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "mutatingwebhookconfigurations"}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{pcGVR: "PriorityClassList", whGVR: "MutatingWebhookConfigurationList"}, objects...)
	if err := k8s.InitTestDynamicResourceCache(client, []k8s.APIResource{{Group: pcGVR.Group, Version: "v1", Kind: "PriorityClass", Name: pcGVR.Resource, Verbs: []string{"list", "watch"}}, {Group: whGVR.Group, Version: "v1", Kind: "MutatingWebhookConfiguration", Name: whGVR.Resource, Verbs: []string{"list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	for _, gvr := range []schema.GroupVersionResource{pcGVR, whGVR} {
		cache := k8s.GetDynamicResourceCache()
		if err := cache.EnsureWatching(gvr); err != nil || !cache.WaitForSync(gvr, 2*time.Second) {
			t.Fatalf("watch %s: %v", gvr, err)
		}
	}
	differences := []templateDifference{{path: "priorityClassName", templateMissing: true}, {path: "containers[app].resources.requests.cpu", resource: corev1.ResourceCPU, resourceKind: "requests", value: resource.MustParse("1"), templateMissing: true}}
	for _, denied := range []string{"", "PriorityClass", "MutatingWebhookConfiguration", "LimitRange"} {
		provider := NewCacheProvider()
		candidates := provider.templateCandidates(pod, differences, podTemplateAccess{related: func(ref Ref) bool { return ref.Kind != denied }})
		want := 3
		if denied != "" {
			want = 2
		}
		if len(candidates) != want {
			t.Fatalf("denied %s: candidates=%+v", denied, candidates)
		}
		for _, candidate := range candidates {
			if candidate.ref.Kind == denied {
				t.Fatalf("unreadable candidate: %+v", candidate)
			}
		}
	}
	provider := NewCacheProvider()
	candidates := provider.templateCandidates(pod, differences, podTemplateAccess{cluster: func(string, string) bool { return false }})
	if len(candidates) != 1 || candidates[0].ref.Kind != "LimitRange" {
		t.Fatalf("cluster scope denial: %+v", candidates)
	}
}

func TestPodTemplateGroupedCacheEvidenceAndCaps(t *testing.T) {
	defer k8s.ResetTestState()
	controller := true
	original := corev1.PodSpec{}
	for n := 0; n < 4; n++ {
		original.Containers = append(original.Containers, corev1.Container{Name: fmt.Sprintf("app-%d", n), Image: "nginx"})
	}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "owner", Namespace: "test", UID: "owner", OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", APIVersion: "apps/v1", Name: "app", UID: "deployment", Controller: &controller}}}, Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{Spec: original}}}
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "test", UID: "deployment"}}
	var objects []runtime.Object
	objects = append(objects, rs, dep)
	for n := 0; n < 2; n++ {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("worker-%d", n), Namespace: "test", UID: types.UID(fmt.Sprintf("pod-%d", n)), Annotations: map[string]string{"vpaUpdates": "Pod resources updated by a vertical autoscaler"}, OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", APIVersion: "apps/v1", Name: rs.Name, UID: rs.UID, Controller: &controller}}}, Spec: *original.DeepCopy(), Status: corev1.PodStatus{Phase: corev1.PodRunning}}
		for i := range pod.Spec.Containers {
			pod.Spec.Containers[i].Resources.Limits = corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("32Mi")}
			pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, corev1.ContainerStatus{Name: pod.Spec.Containers[i].Name, RestartCount: 5, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}, LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", FinishedAt: metav1.Now(), ExitCode: 137}}})
		}
		objects = append(objects, pod)
	}
	for n := 0; n < 4; n++ {
		objects = append(objects, &corev1.LimitRange{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("default-%d", n), Namespace: "test"}, Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{Type: corev1.LimitTypeContainer, Default: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("32Mi")}}}}})
	}
	if err := k8s.InitTestResourceCache(fake.NewClientset(objects...)); err != nil {
		t.Fatal(err)
	}
	rows, stats := ComposeWithStats(NewCacheProvider(), Filters{Grouped: true})
	found := false
	for _, row := range rows {
		if row.DiagnosticContext == nil {
			continue
		}
		for _, fact := range row.DiagnosticContext.Facts {
			if fact.Type != factPodTemplateDivergence {
				continue
			}
			found = true
			if row.Kind != "Deployment" || row.Name != "app" || row.Count != 2 {
				t.Fatalf("wrong subject: %+v", row)
			}
			if len(fact.Refs) != 5 || !strings.Contains(fact.Message, "Showing 3 of 4") || !strings.Contains(fact.Message, "1 further matching candidate") || !strings.Contains(fact.Message, "every replica") {
				t.Fatalf("unbounded/unclear fact: %+v", fact)
			}
			if strings.Contains(fact.Message, "default-3") || strings.Contains(fact.Message, "VPA") || strings.Contains(fact.Message, "vertical autoscaler") {
				t.Fatalf("omitted candidate or writer blamed: %+v", fact)
			}
		}
	}
	if !found {
		t.Fatalf("no grouped evidence: rows=%+v stats=%+v", rows, stats)
	}
}

func TestPodTemplateNamespaceLabelsNeedGetNotList(t *testing.T) {
	access := podTemplateAccess{cluster: func(string, string) bool { return false }, related: func(ref Ref) bool { return ref.Kind == "Namespace" }}
	if !access.canRead(Ref{Kind: "Namespace", Name: "test"}) {
		t.Fatal("namespace get incorrectly requires cluster list")
	}
	if access.canRead(Ref{Kind: "PriorityClass", Group: "scheduling.k8s.io", Name: "batch"}) {
		t.Fatal("cluster candidate bypassed list gate")
	}
}

func TestPodTemplateUpdateCandidatesOnlyForImageChanges(t *testing.T) {
	defer k8s.ResetTestState()
	if err := k8s.InitTestResourceCache(fake.NewClientset()); err != nil {
		t.Fatal(err)
	}
	p := NewCacheProvider()
	p.podTemplateSources.webhooksOnce.Do(func() {
		p.podTemplateSources.webhooks = []templateWebhook{{configuration: Ref{Group: "admissionregistration.k8s.io", Kind: "MutatingWebhookConfiguration", Name: "update-only"}, name: "update.test", update: true, namespace: labels.Everything(), object: labels.Everything()}}
	})
	for _, path := range []string{"priorityClassName", "nodeSelector", "affinity.nodeAffinity.required", "tolerations", "containers[app].resources.requests.cpu", "containers[app].resources.limits.memory", "containers[app].image"} {
		got := p.templateCandidates(templateTestPod(), []templateDifference{{path: path}}, podTemplateAccess{})
		want := 0
		if strings.HasSuffix(path, ".image") {
			want = 1
		}
		if len(got) != want {
			t.Fatalf("%s candidates=%+v", path, got)
		}
	}
}
