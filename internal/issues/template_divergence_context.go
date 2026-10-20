package issues

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

const (
	maxContextDifferences = 5
	maxWorkloadPods       = 200
)

// TemplateDivergence compares the live Pods behind obj with the current
// template of their immediate ReplicaSet or Job, without needing an issue.
// obj may be a Pod, ReplicaSet, Deployment or Job; StatefulSet and DaemonSet
// Pods have no single current baseline and are not compared. Returns nil when
// nothing differs, nothing readable could be compared, or obj is another kind.
// canI authorizes a verb on a resource in a namespace. A workload comparison
// needs list and get on the namespace's Pods (and, for a Deployment,
// ReplicaSets): a workload read implies neither. nil allows everything.
func (p *CacheProvider) TemplateDivergence(obj runtime.Object, canReadClusterScoped func(kind, group string) bool, canReadRelated func(Ref) bool, canI func(verb, group, resource, namespace string) bool) *resourcecontext.TemplateDivergence {
	if p == nil || p.cache == nil {
		return nil
	}
	canList := func(group, resource, namespace string) bool {
		return canI == nil || (canI("list", group, resource, namespace) && canI("get", group, resource, namespace))
	}
	access := podTemplateAccess{cluster: canReadClusterScoped, related: canReadRelated}
	switch o := obj.(type) {
	case *corev1.Pod:
		pod, template, ref := p.podTemplateBaseline(o.Namespace, o.Name, access)
		if pod == nil {
			return nil
		}
		return p.templateDivergenceFor([]podBaseline{{pod: pod, template: template, owner: ref}}, access, false, false)
	case *appsv1.ReplicaSet:
		if !canList("", "pods", o.Namespace) {
			return nil
		}
		return p.workloadTemplateDivergence(o.Namespace, map[types.UID]bool{o.UID: true}, access)
	case *batchv1.Job:
		if !canList("", "pods", o.Namespace) {
			return nil
		}
		return p.workloadTemplateDivergence(o.Namespace, map[types.UID]bool{o.UID: true}, access)
	case *appsv1.Deployment:
		if p.cache.ReplicaSets() == nil || !canList("apps", "replicasets", o.Namespace) || !canList("", "pods", o.Namespace) {
			return nil
		}
		replicaSets, err := p.cache.ReplicaSets().ReplicaSets(o.Namespace).List(labels.Everything())
		if err != nil {
			return nil
		}
		owners := make(map[types.UID]bool)
		for _, rs := range replicaSets {
			for _, ref := range rs.OwnerReferences {
				if ref.Controller != nil && *ref.Controller && ref.UID == o.UID {
					owners[rs.UID] = true
				}
			}
		}
		return p.workloadTemplateDivergence(o.Namespace, owners, access)
	}
	return nil
}

type podBaseline struct {
	pod      *corev1.Pod
	template *corev1.PodTemplateSpec
	owner    Ref
}

func (p *CacheProvider) workloadTemplateDivergence(namespace string, owners map[types.UID]bool, access podTemplateAccess) *resourcecontext.TemplateDivergence {
	if len(owners) == 0 || p.cache.Pods() == nil || !access.canRead(Ref{Kind: "Pod", Namespace: namespace}) {
		return nil
	}
	pods, err := p.cache.Pods().Pods(namespace).List(labels.Everything())
	if err != nil {
		return nil
	}
	var owned []*corev1.Pod
	for _, pod := range pods {
		for _, ref := range pod.OwnerReferences {
			if ref.Controller != nil && *ref.Controller && owners[ref.UID] {
				owned = append(owned, pod)
				break
			}
		}
	}
	sort.Slice(owned, func(a, b int) bool {
		if !owned[a].CreationTimestamp.Equal(&owned[b].CreationTimestamp) {
			return owned[b].CreationTimestamp.Before(&owned[a].CreationTimestamp)
		}
		return owned[a].Name < owned[b].Name
	})
	truncated := len(owned) > maxWorkloadPods
	if truncated {
		owned = owned[:maxWorkloadPods]
	}
	baselines := make([]podBaseline, 0, len(owned))
	withoutTemplate := 0
	for _, pod := range owned {
		// A Pod the caller cannot read is left out without trace, so not even
		// its existence shows in the counts.
		if !access.canRead(Ref{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name}) {
			continue
		}
		template, ref := p.ownerTemplate(pod, access)
		if template == nil {
			withoutTemplate++
			continue
		}
		baselines = append(baselines, podBaseline{pod: pod, template: template, owner: ref})
	}
	divergence := p.templateDivergenceFor(baselines, access, truncated, true)
	if divergence != nil {
		// Pods whose own template is unreadable or gone are left out of both
		// counts; say so rather than let the denominator read as every Pod.
		divergence.PodsNotCompared = withoutTemplate
		if divergence.PodsNotCompared > 0 {
			divergence.Summary += fmt.Sprintf(" %d further current Pods were not compared: their owner template is unreadable or no longer exists.", divergence.PodsNotCompared)
		}
	}
	return divergence
}

// templateDivergenceFor compares every baseline and describes the most recent
// differing Pod in full. For a workload the result names that example Pod.
func (p *CacheProvider) templateDivergenceFor(baselines []podBaseline, access podTemplateAccess, truncated, workload bool) *resourcecontext.TemplateDivergence {
	var example *podBaseline
	var exampleDifferences []templateDifference
	resizedExample := false
	differing := 0
	for idx := range baselines {
		b := &baselines[idx]
		resized := k8s.PodHasResizeSignal(b.pod)
		differences := comparePodTemplateContext(b.pod, &b.template.Spec, resized)
		if len(differences) == 0 {
			continue
		}
		differing++
		if example == nil {
			example, exampleDifferences, resizedExample = b, differences, resized
		}
	}
	if example == nil {
		return nil
	}
	messages := make([]string, 0, min(len(exampleDifferences), maxContextDifferences)+1)
	for idx, difference := range exampleDifferences {
		if idx == maxContextDifferences {
			messages = append(messages, fmt.Sprintf("%d more differences not listed", len(exampleDifferences)-idx))
			break
		}
		messages = append(messages, difference.message)
	}
	baseline := fmt.Sprintf("%s %s", example.owner.Kind, example.owner.Name)
	var summary, examplePod string
	if !workload {
		summary = fmt.Sprintf("Live Pod spec differs from owning %s's current template.", baseline)
	} else {
		examplePod = example.pod.Name
		scope := "current Pods"
		if truncated {
			scope = fmt.Sprintf("most recent Pods (of more than %d)", maxWorkloadPods)
		}
		summary = fmt.Sprintf("%d of %d %s differ from their owning ReplicaSet or Job template; the differences listed are Pod %s's against %s.", differing, len(baselines), scope, example.pod.Name, baseline)
	}
	summary += " This is context, not proof: admission (mutating webhooks, LimitRange defaults), in-place resize, or a direct edit can each produce it."
	if resizedExample {
		summary += " The example Pod reports an in-progress resize, so its resource values were not compared."
	}
	sources := p.templateSources(example.pod, example.template, exampleDifferences, access)
	return &resourcecontext.TemplateDivergence{
		Summary:         summary,
		PodsCompared:    len(baselines),
		PodsDiffering:   differing,
		ExamplePod:      examplePod,
		PodsCapped:      truncated,
		Baseline:        baseline,
		Differences:     messages,
		Sources:         sources.sentences,
		SourceRefs:      sourceRefs(sources),
		SourceGaps:      sources.coverage,
		SourcesNotNamed: sources.notNamed,
	}
}

// TemplateDivergenceNote is the one-line form for responses that carry no
// resource context, so a bare object read still says the live spec is not
// the template's.
func TemplateDivergenceNote(d *resourcecontext.TemplateDivergence) string {
	if d == nil || len(d.Differences) == 0 {
		return ""
	}
	first := d.Differences[0]
	if len(d.Differences) > 1 {
		first += " (and other differences)"
	}
	scope := fmt.Sprintf("Live Pod spec differs from %s's template", d.Baseline)
	if d.ExamplePod != "" {
		pods := "compared Pods"
		if d.PodsCapped {
			pods = "compared Pods (the most recent)"
		}
		scope = fmt.Sprintf("%d of %d %s differ from their owner template", d.PodsDiffering, d.PodsCompared, pods)
		if d.PodsNotCompared > 0 {
			scope += fmt.Sprintf(", %d more not compared", d.PodsNotCompared)
		}
		scope += fmt.Sprintf("; Pod %s vs %s", d.ExamplePod, d.Baseline)
	}
	return strings.TrimSpace(fmt.Sprintf("%s: %s. Read without context=none for the full comparison and the objects that could have changed it.", scope, first))
}

func sourceRefs(report templateSourceReport) []resourcecontext.TemplateSourceRef {
	if len(report.refs) == 0 {
		return nil
	}
	out := make([]resourcecontext.TemplateSourceRef, 0, len(report.refs))
	for idx, ref := range report.refs {
		source := resourcecontext.TemplateSourceRef{ContextRef: resourcecontext.ContextRef{Kind: ref.Kind, Group: ref.Group, Namespace: ref.Namespace, Name: ref.Name}}
		if created := report.created[idx]; !created.IsZero() {
			source.Created = created.UTC().Format(time.RFC3339)
		}
		out = append(out, source)
	}
	return out
}
