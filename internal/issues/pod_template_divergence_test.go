package issues

import (
	"fmt"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func templateTestPod() *corev1.Pod {
	return &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx", Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")}, Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")}}}}}, Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "app", LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled"}}, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}}}}}
}

func TestPodTemplateSymptomRelevance(t *testing.T) {
	cases := []struct {
		name, symptom string
		change        func(*corev1.Pod)
		want          string
	}{
		{"reduced memory", "oom", func(p *corev1.Pod) {
			p.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory] = resource.MustParse("64Mi")
		}, "limits.memory"},
		{"benign increased memory", "oom", func(p *corev1.Pod) {
			p.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory] = resource.MustParse("256Mi")
		}, ""},
		{"equivalent quantity", "oom", func(p *corev1.Pod) {
			p.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory] = resource.MustParse("134217728")
		}, ""},
		{"wrong container", "oom", func(p *corev1.Pod) {
			p.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory] = resource.MustParse("64Mi")
			p.Status.ContainerStatuses[0].Name = "sidecar"
		}, ""},
		{"injected container", "oom", func(p *corev1.Pod) { p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: "sidecar"}) }, ""},
		{"higher request", "scheduling", func(p *corev1.Pod) {
			p.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("1")
		}, "requests.cpu"},
		{"lower request", "scheduling", func(p *corev1.Pod) {
			p.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("50m")
		}, ""},
		{"limit copied to request", "scheduling", func(p *corev1.Pod) {
			p.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory] = resource.MustParse("128Mi")
		}, ""},
		{"priority default", "scheduling", func(p *corev1.Pod) { p.Spec.PriorityClassName = "batch" }, "priorityClassName"},
		{"empty selector", "scheduling", func(p *corev1.Pod) { p.Spec.NodeSelector = map[string]string{} }, ""},
		{"placement", "scheduling", func(p *corev1.Pod) { p.Spec.NodeSelector = map[string]string{"disk": "ssd"} }, "nodeSelector"},
		{"added toleration", "scheduling", func(p *corev1.Pod) {
			p.Spec.Tolerations = []corev1.Toleration{{Key: "node.kubernetes.io/not-ready", Operator: corev1.TolerationOpExists}}
		}, ""},
		{"preferred affinity", "scheduling", func(p *corev1.Pod) {
			p.Spec.Affinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{{Weight: 10}}}}
		}, ""},
		{"image shorthand", "image", func(p *corev1.Pod) { p.Spec.Containers[0].Image = "docker.io/library/nginx:latest" }, ""},
		{"changed image", "image", func(p *corev1.Pod) { p.Spec.Containers[0].Image = "nginx:missing" }, ".image"},
		{"healthy container image", "image", func(p *corev1.Pod) { p.Spec.Containers[0].Image = "nginx:missing"; p.Status.ContainerStatuses = nil }, ""},
		{"irrelevant sensitive fields", "oom", func(p *corev1.Pod) {
			p.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "PASSWORD", Value: "private"}}
			p.Spec.Containers[0].Command = []string{"private"}
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := templateTestPod()
			original := p.Spec.DeepCopy()
			tc.change(p)
			got := comparePodTemplate(p, original, tc.symptom)
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("unexpected differences: %+v", got)
				}
			} else if len(got) != 1 || !strings.Contains(got[0].path, tc.want) {
				t.Fatalf("differences=%+v want %s", got, tc.want)
			}
		})
	}
}

func TestPodTemplateSchedulingSetSemantics(t *testing.T) {
	p := templateTestPod()
	p.Spec.Affinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "zone", Operator: corev1.NodeSelectorOpIn, Values: []string{"a", "b"}}}}, {MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "disk", Operator: corev1.NodeSelectorOpExists}}}}}}}
	original := p.Spec.DeepCopy()
	terms := p.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	terms[0].MatchExpressions[0].Values = []string{"b", "a"}
	terms[0], terms[1] = terms[1], terms[0]
	if got := comparePodTemplate(p, original, "scheduling"); len(got) != 0 {
		t.Fatalf("order-only changes: %+v", got)
	}
	terms[1].MatchExpressions[0].Values = []string{"c"}
	if got := comparePodTemplate(p, original, "scheduling"); len(got) != 1 {
		t.Fatalf("required constraint change: %+v", got)
	}
	original = p.Spec.DeepCopy()
	p.Spec.Affinity = nil
	if got := comparePodTemplate(p, original, "scheduling"); len(got) != 1 {
		t.Fatalf("missing constraint: %+v", got)
	}
}

func TestPodTemplateTolerationSchedulingSemantics(t *testing.T) {
	p := templateTestPod()
	seconds := int64(100)
	p.Spec.Tolerations = []corev1.Toleration{{Key: "dedicated", Value: "batch", Effect: corev1.TaintEffectNoExecute, TolerationSeconds: &seconds}}
	original := p.Spec.DeepCopy()
	p.Spec.Tolerations[0].Operator = corev1.TolerationOpEqual
	p.Spec.Tolerations[0].TolerationSeconds = nil
	if got := comparePodTemplate(p, original, "scheduling"); len(got) != 0 {
		t.Fatalf("default operator or eviction seconds: %+v", got)
	}
	p.Spec.Tolerations = nil
	if got := comparePodTemplate(p, original, "scheduling"); len(got) != 1 {
		t.Fatalf("removed toleration: %+v", got)
	}
}

type templateCountingProvider struct {
	*fakeProvider
	calls   []string
	witness string
}

func (p *templateCountingProvider) podTemplateFact(i Issue, _ podTemplateAccess) *issuesapi.DiagnosticFact {
	p.calls = append(p.calls, i.Name)
	if i.Name != p.witness {
		return nil
	}
	return &issuesapi.DiagnosticFact{Type: factPodTemplateDivergence, Message: i.Name, Refs: []Ref{{Kind: "Pod", Namespace: i.Namespace, Name: i.Name}}}
}

func TestPodTemplateWitnessBoundsAndIsolation(t *testing.T) {
	var flat []Issue
	for n := 0; n < 12; n++ {
		flat = append(flat, Issue{ID: "group", Kind: "Pod", Namespace: "ns", Name: fmt.Sprintf("pod-%02d", n), Category: issuesapi.CategoryOOMKilled})
	}
	shaped := []Issue{flat[0]}
	p := &templateCountingProvider{fakeProvider: &fakeProvider{}, witness: "pod-11"}
	got := enrichPodTemplateContext(shaped, flat, p, podTemplateAccess{}, true, Ref{})
	if len(p.calls) != 10 || got[0].DiagnosticContext != nil {
		t.Fatalf("group cap calls=%v result=%+v", p.calls, got)
	}
	p.calls = nil
	got = enrichPodTemplateContext(shaped, flat, p, podTemplateAccess{}, true, Ref{Kind: "Pod", Namespace: "ns", Name: "pod-11"})
	if len(p.calls) != 1 || p.calls[0] != "pod-11" || got[0].DiagnosticContext == nil {
		t.Fatalf("specific pod beyond cap calls=%v result=%+v", p.calls, got)
	}
	p.calls = nil
	got = enrichPodTemplateContext(shaped, flat, p, podTemplateAccess{}, true, Ref{Kind: "Pod", Namespace: "ns", Name: "pod-00"})
	if len(p.calls) != 1 || got[0].DiagnosticContext != nil {
		t.Fatalf("sibling evidence leaked: calls=%v result=%+v", p.calls, got)
	}
	shaped[0].DiagnosticContext = &issuesapi.DiagnosticContext{Facts: make([]issuesapi.DiagnosticFact, maxDiagnosticFacts)}
	p.calls = nil
	enrichPodTemplateContext(shaped, flat, p, podTemplateAccess{}, true, Ref{})
	if len(p.calls) != 0 {
		t.Fatalf("stronger context must win: %v", p.calls)
	}
}

func TestPodTemplateComposeDoesNotCreateOrEscalateIssues(t *testing.T) {
	p := &templateCountingProvider{fakeProvider: &fakeProvider{}, witness: "worker"}
	if got := Compose(p, Filters{}); len(got) != 0 || len(p.calls) != 0 {
		t.Fatalf("healthy resources generated context: %+v calls=%v", got, p.calls)
	}
	p.problems = []k8s.Detection{{Kind: "Pod", Namespace: "test", Name: "worker", Severity: "critical", Reason: "OOMKilled", RestartCount: 3, LastTerminatedReason: "OOMKilled"}}
	with := Compose(p, Filters{Grouped: true})
	p.calls = nil
	without := Compose(p, Filters{Grouped: true, SkipPodTemplateContext: true})
	if len(with) != 1 || len(without) != 1 || len(p.calls) != 0 {
		t.Fatalf("count or skip changed: with=%+v without=%+v calls=%v", with, without, p.calls)
	}
	if with[0].Severity != without[0].Severity || with[0].ID != without[0].ID || with[0].Count != without[0].Count {
		t.Fatal("context changed issue identity/severity/count")
	}
	foundRestart, foundTemplate := false, false
	for _, fact := range with[0].DiagnosticContext.Facts {
		foundRestart = foundRestart || fact.Type == "restart_cause"
		foundTemplate = foundTemplate || fact.Type == factPodTemplateDivergence
	}
	if !foundRestart || !foundTemplate {
		t.Fatalf("context=%+v", with[0].DiagnosticContext)
	}
	p.calls = nil
	if got := RelatedIssues(p, RelatedIssueOptions{}, "", "Pod", "test", "unrelated"); len(got) != 0 || len(p.calls) != 0 {
		t.Fatalf("unmatched lookup performed comparisons: %+v calls=%v", got, p.calls)
	}
}

func BenchmarkPodTemplateGrouped500Pods(b *testing.B) {
	p := &templateCountingProvider{fakeProvider: &fakeProvider{}, witness: "none"}
	var flat []Issue
	for n := 0; n < 500; n++ {
		flat = append(flat, Issue{ID: fmt.Sprintf("group-%d", n/50), Kind: "Pod", Namespace: "ns", Name: fmt.Sprintf("pod-%03d", n), Category: issuesapi.CategoryOOMKilled})
	}
	shaped := GroupIssues(flat)
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		p.calls = nil
		enrichPodTemplateContext(shaped, flat, p, podTemplateAccess{}, true, Ref{})
		if len(p.calls) != 100 {
			b.Fatalf("calls=%d want 10 per group", len(p.calls))
		}
	}
}

func TestPodTemplateInitAndRecoveredContainers(t *testing.T) {
	for _, symptom := range []string{"oom", "image"} {
		t.Run(symptom, func(t *testing.T) {
			p := templateTestPod()
			p.Spec.InitContainers = p.Spec.Containers
			p.Spec.Containers = nil
			p.Status.InitContainerStatuses = p.Status.ContainerStatuses
			p.Status.ContainerStatuses = nil
			original := p.Spec.DeepCopy()
			p.Spec.InitContainers[0].Image = "nginx:missing"
			p.Spec.InitContainers[0].Resources.Limits[corev1.ResourceMemory] = resource.MustParse("64Mi")
			got := comparePodTemplate(p, original, symptom)
			if len(got) != 1 || !strings.HasPrefix(got[0].path, "initContainers[") {
				t.Fatalf("init comparison: %+v", got)
			}
		})
	}
	p := templateTestPod()
	original := p.Spec.DeepCopy()
	p.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory] = resource.MustParse("64Mi")
	p.Status.ContainerStatuses[0].State = corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(time.Now().Add(-time.Hour))}}
	if got := comparePodTemplate(p, original, "oom"); len(got) != 0 {
		t.Fatalf("stale OOM history: %+v", got)
	}
}

func TestPodTemplateSamplingCountsDistinctPods(t *testing.T) {
	i := Issue{ID: "group", Kind: "Pod", Namespace: "ns", Name: "worker", Category: issuesapi.CategoryOOMKilled}
	p := &templateCountingProvider{fakeProvider: &fakeProvider{}, witness: i.Name}
	got := enrichPodTemplateContext([]Issue{i}, []Issue{i, i}, p, podTemplateAccess{}, true, Ref{})
	if strings.Contains(got[0].DiagnosticContext.Facts[0].Message, "every replica") {
		t.Fatal("duplicate row treated as another replica")
	}
}

func BenchmarkPodTemplateFlat500Pods(b *testing.B) {
	p := &templateCountingProvider{fakeProvider: &fakeProvider{}, witness: "none"}
	var flat []Issue
	for n := 0; n < 500; n++ {
		flat = append(flat, Issue{ID: fmt.Sprintf("group-%d", n/50), Kind: "Pod", Namespace: "ns", Name: fmt.Sprintf("pod-%03d", n), Category: issuesapi.CategoryOOMKilled})
	}
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		p.calls = nil
		enrichPodTemplateContext(flat, flat, p, podTemplateAccess{}, false, Ref{})
		if len(p.calls) != 500 {
			b.Fatalf("calls=%d", len(p.calls))
		}
	}
}

func TestPodTemplateExistingOOMDiagnosisRequiresSourceRead(t *testing.T) {
	source := &corev1.ObjectReference{Kind: "ReplicaSet", APIVersion: "apps/v1", Namespace: "test", Name: "owner"}
	p := &fakeProvider{problems: []k8s.Detection{{Kind: "Pod", Namespace: "test", Name: "worker", Severity: "critical", Reason: "OOMKilled", Message: "Container OOMKilled", Cause: "Private owner limit", Action: "Private owner advice", DiagnosisSource: source}}}
	for _, readable := range []bool{true, false} {
		rows := Compose(p, Filters{CanReadRelated: func(ref Ref) bool { return ref.Kind != "ReplicaSet" || readable }})
		if len(rows) != 1 {
			t.Fatalf("original failure lost: %+v", rows)
		}
		if got := strings.Contains(rows[0].Cause, "Private"); got != readable {
			t.Fatalf("readable=%v cause=%q", readable, rows[0].Cause)
		}
		if !readable && strings.Contains(rows[0].Action, "Private") {
			t.Fatalf("advice leaked: %+v", rows[0])
		}
	}
}

func TestPodTemplateCompletedInitDoesNotSupplyOOMContext(t *testing.T) {
	p := templateTestPod()
	p.Spec.InitContainers = []corev1.Container{*p.Spec.Containers[0].DeepCopy()}
	p.Spec.InitContainers[0].Name = "setup"
	p.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "setup", RestartCount: 1, LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", FinishedAt: metav1.Now()}}, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Completed", ExitCode: 0, FinishedAt: metav1.Now()}}}}
	original := p.Spec.DeepCopy()
	p.Spec.InitContainers[0].Resources.Limits[corev1.ResourceMemory] = resource.MustParse("64Mi")
	if got := comparePodTemplate(p, original, "oom"); len(got) != 0 {
		t.Fatalf("completed init supplied unrelated OOM context: %+v", got)
	}
}
