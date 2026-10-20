package issues

import (
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func TestPodTemplateContextComparisonStaysQuietOnAdmissionDefaults(t *testing.T) {
	scalar := func(s string) resource.Quantity { return resource.MustParse(s) }
	evictAfter := int64(300)
	template := corev1.PodSpec{
		Containers: []corev1.Container{{Name: "app", Image: "nginx:1.27", Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceMemory: scalar("128Mi")},
			Limits:   corev1.ResourceList{corev1.ResourceMemory: scalar("256Mi"), corev1.ResourceCPU: scalar("500m")},
		}}},
		Tolerations: []corev1.Toleration{{Key: "dedicated", Value: "web", Effect: corev1.TaintEffectNoExecute, TolerationSeconds: &evictAfter}},
	}
	runc := "runc"
	cases := []struct {
		name    string
		change  func(*corev1.Pod)
		resized bool
		want    []string
	}{
		{"identical", func(*corev1.Pod) {}, false, nil},
		{"istio injection", func(p *corev1.Pod) {
			p.Spec.InitContainers = append(p.Spec.InitContainers, corev1.Container{Name: "istio-init"}, corev1.Container{Name: "istio-proxy", Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: scalar("1Gi")}}})
			p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "istio-envoy"})
		}, false, nil},
		{"pod identity env and volumes", func(p *corev1.Pod) {
			p.Spec.Containers[0].Env = append(p.Spec.Containers[0].Env, corev1.EnvVar{Name: "AWS_ROLE_ARN", Value: "arn"})
			p.Spec.Containers[0].VolumeMounts = append(p.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: "aws-iam-token"})
		}, false, nil},
		{"extended resource added", func(p *corev1.Pod) {
			p.Spec.Containers[0].Resources.Limits["vpc.amazonaws.com/pod-eni"] = scalar("1")
			p.Spec.Containers[0].Resources.Requests["vpc.amazonaws.com/pod-eni"] = scalar("1")
		}, false, nil},
		{"limit copied to request", func(p *corev1.Pod) {
			p.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = scalar("500m")
		}, false, nil},
		{"toleration seconds changed", func(p *corev1.Pod) {
			zero := int64(0)
			p.Spec.Tolerations[0].TolerationSeconds = &zero
		}, false, []string{"a template toleration is missing or changed in the live pod spec"}},
		{"toleration seconds dropped", func(p *corev1.Pod) { p.Spec.Tolerations[0].TolerationSeconds = nil }, false, []string{"a template toleration is missing or changed in the live pod spec"}},
		{"added toleration with seconds stays quiet", func(p *corev1.Pod) {
			p.Spec.Tolerations = append(p.Spec.Tolerations, corev1.Toleration{Key: "node.kubernetes.io/unreachable", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: &evictAfter})
		}, false, nil},
		{"default tolerations added", func(p *corev1.Pod) {
			p.Spec.Tolerations = append(p.Spec.Tolerations, corev1.Toleration{Key: "node.kubernetes.io/not-ready", Operator: corev1.TolerationOpExists})
		}, false, nil},
		{"global default priority", func(p *corev1.Pod) { p.Spec.PriorityClassName = "default" }, false, nil},
		{"equivalent quantity and image spelling", func(p *corev1.Pod) {
			p.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory] = scalar("268435456")
			p.Spec.Containers[0].Image = "docker.io/library/nginx:1.27"
		}, false, nil},
		{"webhook rewrote memory", func(p *corev1.Pod) {
			p.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory] = scalar("16Mi")
			p.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory] = scalar("16Mi")
		}, false, []string{"containers[app].resources.limits.memory is 16Mi; template specifies 256Mi", "containers[app].resources.requests.memory is 16Mi; template specifies 128Mi"}},
		{"resized pod skips resources", func(p *corev1.Pod) {
			p.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory] = scalar("16Mi")
		}, true, nil},
		{"limitrange default for omitted resource", func(p *corev1.Pod) {
			p.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = scalar("100m")
		}, false, []string{"containers[app].resources.requests.cpu is 100m; the template leaves it unset"}},
		{"resource removed", func(p *corev1.Pod) {
			delete(p.Spec.Containers[0].Resources.Limits, corev1.ResourceCPU)
		}, false, []string{"containers[app].resources.limits.cpu is unset; template specifies 500m"}},
		{"digest pinned onto the same tag", func(p *corev1.Pod) { p.Spec.Containers[0].Image = "nginx:1.27@sha256:" + strings.Repeat("a", 64) }, false, nil},
		{"digest pinned onto another tag", func(p *corev1.Pod) { p.Spec.Containers[0].Image = "nginx:1.28@sha256:" + strings.Repeat("a", 64) }, false, []string{`containers[app].image is "nginx:1.28@sha256:` + strings.Repeat("a", 64) + `"; template specifies "nginx:1.27"`}},
		{"image replaced", func(p *corev1.Pod) { p.Spec.Containers[0].Image = "registry.example/nginx@sha256:abc" }, false, []string{`containers[app].image is "registry.example/nginx@sha256:abc"; template specifies "nginx:1.27"`}},
		{"placement and runtime", func(p *corev1.Pod) {
			p.Spec.NodeSelector = map[string]string{"pool": "spot"}
			p.Spec.RuntimeClassName = &runc
			p.Spec.Tolerations = nil
		}, false, []string{"nodeSelector differs from the template", `runtimeClassName is "runc"; template specifies ""`, "a template toleration is missing or changed in the live pod spec"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pod := &corev1.Pod{Spec: *template.DeepCopy()}
			tc.change(pod)
			var got []string
			for _, d := range comparePodTemplateContext(pod, &template, tc.resized) {
				got = append(got, d.message)
			}
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("differences=%q want %q", got, tc.want)
			}
		})
	}
}

func TestPodTemplateContextKeepsIstioAutoImageQuiet(t *testing.T) {
	template := corev1.PodSpec{Containers: []corev1.Container{{Name: "istio-proxy", Image: "auto"}}}
	pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "istio-proxy", Image: "docker.io/istio/proxyv2:1.24.0"}}}}
	if got := comparePodTemplateContext(pod, &template, false); len(got) != 0 {
		t.Fatalf("Istio's auto image placeholder reported: %+v", got)
	}
}

func TestPodTemplateContextReportsFiniteSecondsOnAForeverToleration(t *testing.T) {
	template := corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx"}}, Tolerations: []corev1.Toleration{{Key: "gpu", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute}}}
	pod := &corev1.Pod{Spec: *template.DeepCopy()}
	if got := comparePodTemplateContext(pod, &template, false); len(got) != 0 {
		t.Fatalf("identical tolerations reported: %+v", got)
	}
	seconds := int64(0)
	pod.Spec.Tolerations[0].TolerationSeconds = &seconds
	if got := comparePodTemplateContext(pod, &template, false); len(got) != 1 || got[0].path != "tolerations" {
		t.Fatalf("forever-to-0s toleration not reported: %+v", got)
	}
}

func TestPodTemplateCandidatesDiscloseUncachedLimitRanges(t *testing.T) {
	defer k8s.ResetTestState()
	if err := k8s.InitScopedTestResourceCache(fake.NewClientset(), map[string]k8score.ResourceScope{"pods": {Enabled: true}, "namespaces": {Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	omitted := []templateDifference{{path: "containers[app].resources.limits.memory", resource: corev1.ResourceMemory, resourceKind: "limits", templateMissing: true}}
	_, coverage := NewCacheProvider().templateCandidates(templateTestPod(), omitted, podTemplateAccess{cluster: func(string, string) bool { return false }})
	if !strings.Contains(strings.Join(coverage, " "), "LimitRange defaults were not inspected: Radar does not cache LimitRanges") {
		t.Fatalf("uncached LimitRanges not disclosed: %v", coverage)
	}
}

func initTemplateWebhooks(t *testing.T, configs ...*admissionv1.MutatingWebhookConfiguration) {
	t.Helper()
	gvr := schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "mutatingwebhookconfigurations"}
	var objects []runtime.Object
	for _, config := range configs {
		config.TypeMeta = metav1.TypeMeta{Kind: "MutatingWebhookConfiguration", APIVersion: "admissionregistration.k8s.io/v1"}
		data, err := runtime.DefaultUnstructuredConverter.ToUnstructured(config)
		if err != nil {
			t.Fatal(err)
		}
		objects = append(objects, &unstructured.Unstructured{Object: data})
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "MutatingWebhookConfigurationList"}, objects...)
	if err := k8s.InitTestDynamicResourceCache(client, []k8s.APIResource{{Group: gvr.Group, Version: "v1", Kind: "MutatingWebhookConfiguration", Name: gvr.Resource, Verbs: []string{"list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	cache := k8s.GetDynamicResourceCache()
	if err := cache.EnsureWatching(gvr); err != nil || !cache.WaitForSync(gvr, 2*time.Second) {
		t.Fatalf("watch webhooks: %v", err)
	}
}

func podWebhook(name, url string) admissionv1.MutatingWebhook {
	w := admissionv1.MutatingWebhook{Name: name, Rules: []admissionv1.RuleWithOperations{{Operations: []admissionv1.OperationType{admissionv1.Create}, Rule: admissionv1.Rule{APIGroups: []string{""}, APIVersions: []string{"v1"}, Resources: []string{"pods"}}}}}
	if url != "" {
		w.ClientConfig.URL = &url
	} else {
		w.ClientConfig.Service = &admissionv1.ServiceReference{Namespace: "ops", Name: name}
	}
	return w
}

// The cluster shapes measured on EKS: two control-plane-hosted webhooks match
// every Pod and add-ons take a third, so a culprit can sort after all of them.
// Every match is named, and loopback-served configurations come last.
func TestPodTemplateWebhookCandidatesNameEveryMatchAndListLoopbackLast(t *testing.T) {
	defer k8s.ResetTestState()
	defer k8s.ResetTestDynamicState()
	created := metav1.NewTime(time.Date(2026, 5, 10, 19, 27, 50, 0, time.UTC))
	cfg := func(name string, hooks ...admissionv1.MutatingWebhook) *admissionv1.MutatingWebhookConfiguration {
		return &admissionv1.MutatingWebhookConfiguration{ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: created}, Webhooks: hooks}
	}
	cel := podWebhook("cel.policy.example", "")
	cel.MatchConditions = []admissionv1.MatchCondition{{Name: "only-web", Expression: "object.metadata.name.startsWith('web')"}}
	initTemplateWebhooks(t,
		cfg("pod-identity-webhook", podWebhook("iam-for-pods.amazonaws.com", "https://127.0.0.1:23443/mutate")),
		cfg("vpc-resource-mutating-webhook", podWebhook("mpod.vpc.k8s.aws", "https://127.0.0.1:9443/mutate-v1-pod")),
		cfg("kueue-mutating-webhook-configuration", podWebhook("mpod.kb.io", "")),
		cfg("zz-platform-policy", podWebhook("mutation.policy.example", "")),
		cfg("mixed-endpoints", podWebhook("local.example", "https://localhost:8443/m"), podWebhook("remote.example", "")),
		cfg("cel-policy", cel),
	)
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}}
	if err := k8s.InitTestResourceCache(fake.NewClientset(ns)); err != nil {
		t.Fatal(err)
	}
	pod := templateTestPod()
	pod.Namespace = "shop"
	pod.CreationTimestamp = metav1.NewTime(created.Add(time.Hour))
	differences := []templateDifference{{path: "containers[app].resources.limits.memory", resource: corev1.ResourceMemory, resourceKind: "limits"}}
	candidates, coverage := NewCacheProvider().templateCandidates(pod, differences, podTemplateAccess{})
	var order []string
	for _, c := range candidates {
		order = append(order, c.ref.Name)
	}
	want := "cel-policy,kueue-mutating-webhook-configuration,mixed-endpoints,zz-platform-policy,pod-identity-webhook,vpc-resource-mutating-webhook"
	if strings.Join(order, ",") != want {
		t.Fatalf("order=%v want %s", order, want)
	}
	byName := map[string]string{}
	for _, c := range candidates {
		byName[c.ref.Name] = c.message
	}
	if !strings.Contains(byName["pod-identity-webhook"], "control-plane loopback endpoint") || strings.Contains(byName["mixed-endpoints"], "control-plane loopback endpoint") {
		t.Fatalf("loopback labelling: %v", byName)
	}
	if !strings.Contains(byName["mixed-endpoints"], "local.example (loopback endpoint), remote.example") {
		t.Fatalf("configuration webhooks not aggregated: %s", byName["mixed-endpoints"])
	}
	if !strings.Contains(byName["cel-policy"], "matchConditions present, not evaluated") || !strings.Contains(byName["zz-platform-policy"], "configuration created 2026-05-10T19:27:50Z") {
		t.Fatalf("facts missing: %v", byName)
	}
	if len(coverage) != 0 {
		t.Fatalf("unexpected coverage notes: %v", coverage)
	}

	report := NewCacheProvider().templateSources(pod, &corev1.PodTemplateSpec{}, differences, podTemplateAccess{})
	message := strings.Join(report.sentences, " ")
	if !strings.Contains(message, "Also matching: MutatingWebhookConfiguration vpc-resource-mutating-webhook.") || len(report.refs) != 6 {
		t.Fatalf("every match must be named: %s refs=%v", message, report.refs)
	}

	_, coverage = NewCacheProvider().templateCandidates(pod, differences, podTemplateAccess{related: func(ref Ref) bool { return ref.Kind != "Namespace" && ref.Name != "cel-policy" }})
	if !strings.Contains(strings.Join(coverage, " "), "not readable by the caller") {
		t.Fatalf("unreadable configuration not disclosed: %v", coverage)
	}
}

func TestPodTemplateWebhookCandidatesDiscloseUnreadableNamespaceLabels(t *testing.T) {
	defer k8s.ResetTestState()
	defer k8s.ResetTestDynamicState()
	selective := podWebhook("selective.example", "")
	selective.NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"team": "shop"}}
	initTemplateWebhooks(t, &admissionv1.MutatingWebhookConfiguration{ObjectMeta: metav1.ObjectMeta{Name: "selective"}, Webhooks: []admissionv1.MutatingWebhook{selective}})
	if err := k8s.InitTestResourceCache(fake.NewClientset()); err != nil {
		t.Fatal(err)
	}
	pod := templateTestPod()
	pod.Namespace = "shop"
	candidates, coverage := NewCacheProvider().templateCandidates(pod, nil, podTemplateAccess{related: func(ref Ref) bool { return ref.Kind != "Namespace" }})
	if len(candidates) != 0 || !strings.Contains(strings.Join(coverage, " "), "namespace labels are not readable") {
		t.Fatalf("candidates=%v coverage=%v", candidates, coverage)
	}
	_, coverage = NewCacheProvider().templateCandidates(pod, nil, podTemplateAccess{cluster: func(string, string) bool { return false }})
	if strings.Join(coverage, " ") != "Mutating webhook configurations were not inspected: the caller cannot list them." {
		t.Fatalf("coverage=%v", coverage)
	}
	omitted := []templateDifference{{path: "containers[app].resources.limits.memory", resource: corev1.ResourceMemory, resourceKind: "limits", templateMissing: true}, {path: "priorityClassName", templateMissing: true}}
	pod.Spec.PriorityClassName = "batch"
	_, coverage = NewCacheProvider().templateCandidates(pod, omitted, podTemplateAccess{related: func(ref Ref) bool { return ref.Kind != "LimitRange" && ref.Kind != "PriorityClass" }})
	if got := strings.Join(coverage, " "); !strings.Contains(got, "PriorityClasses were not inspected") {
		t.Fatalf("unreadable PriorityClass not disclosed: %v", coverage)
	}
	_, coverage = NewCacheProvider().templateCandidates(pod, omitted, podTemplateAccess{})
	if got := strings.Join(coverage, " "); !strings.Contains(got, "PriorityClasses were not inspected: Radar has not cached them.") {
		t.Fatalf("uncached PriorityClasses not disclosed: %v", coverage)
	}
}

func TestTemplateDivergenceForPodsAndWorkloads(t *testing.T) {
	defer k8s.ResetTestState()
	controller := true
	template := corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx", Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")}}}}}}
	oldTemplate := *template.DeepCopy()
	oldTemplate.Spec.Containers[0].Image = "nginx:old"
	dep := &appsv1.Deployment{TypeMeta: metav1.TypeMeta{Kind: "Deployment", APIVersion: "apps/v1"}, ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", UID: "dep"}}
	rs := func(name string, uid types.UID, tmpl corev1.PodTemplateSpec) *appsv1.ReplicaSet {
		return &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", UID: uid, OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", APIVersion: "apps/v1", Name: "web", UID: "dep", Controller: &controller}}}, Spec: appsv1.ReplicaSetSpec{Template: tmpl}}
	}
	current, previous := rs("web-new", "rs-new", template), rs("web-old", "rs-old", oldTemplate)
	pod := func(name string, owner *appsv1.ReplicaSet, age time.Duration, memory string) *corev1.Pod {
		spec := owner.Spec.Template.Spec.DeepCopy()
		spec.Containers[0].Resources.Limits[corev1.ResourceMemory] = resource.MustParse(memory)
		return &corev1.Pod{TypeMeta: metav1.TypeMeta{Kind: "Pod", APIVersion: "v1"}, ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", UID: types.UID(name), CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", APIVersion: "apps/v1", Name: owner.Name, UID: owner.UID, Controller: &controller}}}, Spec: *spec}
	}
	// The old ReplicaSet's Pod matches its own template although its image
	// differs from the new one: a rolling update is not divergence.
	oldPod := pod("web-old-1", previous, 3*time.Hour, "256Mi")
	healthy := pod("web-new-1", current, 2*time.Hour, "256Mi")
	mutated := pod("web-new-2", current, time.Hour, "16Mi")
	job := &batchv1.Job{TypeMeta: metav1.TypeMeta{Kind: "Job", APIVersion: "batch/v1"}, ObjectMeta: metav1.ObjectMeta{Name: "migrate", Namespace: "shop", UID: "job"}, Spec: batchv1.JobSpec{Template: template}}
	jobPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "migrate-1", Namespace: "shop", OwnerReferences: []metav1.OwnerReference{{Kind: "Job", APIVersion: "batch/v1", Name: "migrate", UID: "job", Controller: &controller}}}, Spec: *template.Spec.DeepCopy()}
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "shop", UID: "sts"}}
	if err := k8s.InitTestResourceCache(fake.NewClientset(dep, current, previous, oldPod, healthy, mutated, job, jobPod, sts)); err != nil {
		t.Fatal(err)
	}
	p := NewCacheProvider()

	got := p.TemplateDivergence(dep, nil, nil, nil)
	if got == nil || got.PodsCompared != 3 || got.PodsDiffering != 1 || got.ExamplePod != "web-new-2" || got.Baseline != "ReplicaSet web-new" {
		t.Fatalf("deployment divergence=%+v", got)
	}
	if len(got.Differences) != 1 || got.Differences[0] != "containers[app].resources.limits.memory is 16Mi; template specifies 256Mi" || !strings.Contains(got.Summary, "1 of 3 current Pods") || !strings.Contains(got.Summary, "context, not proof") {
		t.Fatalf("deployment divergence=%+v", got)
	}
	if note := TemplateDivergenceNote(got); note != "1 of 3 compared Pods differ from their owner template; Pod web-new-2 vs ReplicaSet web-new: containers[app].resources.limits.memory is 16Mi; template specifies 256Mi. Read without context=none for the full comparison and the objects that could have changed it." {
		t.Fatalf("note=%q", note)
	}
	if got := p.TemplateDivergence(mutated, nil, nil, nil); got == nil || got.PodsCompared != 1 || got.ExamplePod != "" || !strings.HasPrefix(got.Summary, "Live Pod spec differs from owning ReplicaSet web-new's current template.") {
		t.Fatalf("pod divergence=%+v", got)
	}
	if got := p.TemplateDivergence(current, nil, nil, nil); got == nil || got.PodsCompared != 2 || got.ExamplePod != "web-new-2" {
		t.Fatalf("replicaset divergence=%+v", got)
	}
	for name, obj := range map[string]runtime.Object{"healthy pod": healthy, "old pod": oldPod, "job": job, "statefulset": sts} {
		if got := p.TemplateDivergence(obj, nil, nil, nil); got != nil {
			t.Fatalf("%s: unexpected divergence %+v", name, got)
		}
	}
	// One unreadable Pod, or one unreadable template, leaves only that Pod
	// out — and the result says how many were left out.
	if got := p.TemplateDivergence(dep, nil, func(ref Ref) bool { return ref.Name != "web-new-2" }, nil); got != nil {
		t.Fatalf("the only differing Pod is unreadable, yet: %+v", got)
	}
	if got := p.TemplateDivergence(dep, nil, func(ref Ref) bool { return ref.Name != "web-old" }, nil); got == nil || got.PodsCompared != 2 || got.PodsNotCompared != 1 || !strings.Contains(got.Summary, "1 further current Pods were not compared") || !strings.HasPrefix(TemplateDivergenceNote(got), "1 of 2 compared Pods differ from their owner template, 1 more not compared;") {
		t.Fatalf("partial workload=%+v note=%q", got, TemplateDivergenceNote(got))
	}
	if got := p.TemplateDivergence(dep, nil, func(ref Ref) bool { return ref.Name != "web-old-1" }, nil); got == nil || got.PodsCompared != 2 || got.PodsNotCompared != 0 {
		t.Fatalf("an unreadable Pod must leave no trace in the counts: %+v", got)
	}
	for _, denied := range []string{"list pods", "get pods", "list replicasets"} {
		if got := p.TemplateDivergence(dep, nil, nil, func(verb, _, r, _ string) bool { return verb+" "+r != denied }); got != nil {
			t.Fatalf("compared without %s permission: %+v", denied, got)
		}
	}
	if got := p.TemplateDivergence(job, nil, nil, func(_, _, r, _ string) bool { return r != "pods" }); got != nil {
		t.Fatalf("listed Job Pods without list permission: %+v", got)
	}
	if note := TemplateDivergenceNote(&resourcecontext.TemplateDivergence{PodsCompared: 200, PodsDiffering: 3, PodsCapped: true, ExamplePod: "a", Baseline: "ReplicaSet r", Differences: []string{"x"}}); !strings.HasPrefix(note, "3 of 200 compared Pods (the most recent) differ") {
		t.Fatalf("capped note=%q", note)
	}
	for _, denied := range []string{"Pod", "ReplicaSet"} {
		if got := p.TemplateDivergence(dep, nil, func(ref Ref) bool { return ref.Kind != denied }, nil); got != nil {
			t.Fatalf("denied %s leaked %+v", denied, got)
		}
		if got := p.TemplateDivergence(mutated, nil, func(ref Ref) bool { return ref.Kind != denied }, nil); got != nil {
			t.Fatalf("denied %s leaked pod comparison %+v", denied, got)
		}
	}
}

// The OOM issue fact and the context comparison must not disagree on the
// motivating shape: a webhook rewriting a template-set memory limit.
func TestPodTemplateIssueFactNamesWebhookAfterAddOns(t *testing.T) {
	defer k8s.ResetTestState()
	defer k8s.ResetTestDynamicState()
	initTemplateWebhooks(t,
		&admissionv1.MutatingWebhookConfiguration{ObjectMeta: metav1.ObjectMeta{Name: "pod-identity-webhook"}, Webhooks: []admissionv1.MutatingWebhook{podWebhook("iam-for-pods.amazonaws.com", "https://127.0.0.1:23443/mutate")}},
		&admissionv1.MutatingWebhookConfiguration{ObjectMeta: metav1.ObjectMeta{Name: "vpc-resource-mutating-webhook"}, Webhooks: []admissionv1.MutatingWebhook{podWebhook("mpod.vpc.k8s.aws", "https://127.0.0.1:9443/mutate-v1-pod")}},
		&admissionv1.MutatingWebhookConfiguration{ObjectMeta: metav1.ObjectMeta{Name: "kueue-mutating-webhook-configuration"}, Webhooks: []admissionv1.MutatingWebhook{podWebhook("mpod.kb.io", "")}},
		&admissionv1.MutatingWebhookConfiguration{ObjectMeta: metav1.ObjectMeta{Name: "zz-memory-policy"}, Webhooks: []admissionv1.MutatingWebhook{podWebhook("mutation.policy.example", "")}},
	)
	controller := true
	original := templateTestPod().Spec
	p := templateTestPod()
	p.Name, p.Namespace = "web-1", "shop"
	p.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory] = resource.MustParse("16Mi")
	p.OwnerReferences = []metav1.OwnerReference{{Kind: "ReplicaSet", APIVersion: "apps/v1", Name: "web", UID: "rs", Controller: &controller}}
	owner := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", UID: "rs"}, Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{Spec: original}}}
	if err := k8s.InitTestResourceCache(fake.NewClientset(p, owner, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}})); err != nil {
		t.Fatal(err)
	}
	fact := NewCacheProvider().podTemplateFact(Issue{Kind: "Pod", Namespace: "shop", Name: "web-1", Category: issuesapi.CategoryOOMKilled}, podTemplateAccess{})
	if fact == nil {
		t.Fatal("no fact")
	}
	rogue := strings.Index(fact.Message, "Mutating webhook zz-memory-policy")
	identity := strings.Index(fact.Message, "Mutating webhook pod-identity-webhook")
	if rogue < 0 || identity < 0 || rogue > identity || !strings.Contains(fact.Message, "Mutating webhook kueue-mutating-webhook-configuration") {
		t.Fatalf("culprit not listed before loopback webhooks: %s", fact.Message)
	}
}
