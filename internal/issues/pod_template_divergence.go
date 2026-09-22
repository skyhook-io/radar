package issues

import (
	"encoding/json"
	"fmt"
	"github.com/skyhook-io/radar/pkg/health"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	factPodTemplateDivergence = "pod_template_divergence"
	maxTemplateWitnesses      = 10
	maxTemplateDifferences    = 3
)

type podTemplateAccess struct {
	cluster func(kind, group string) bool
	related func(Ref) bool
}

func (a podTemplateAccess) canRead(ref Ref) bool {
	if ref.Kind == "Namespace" && ref.Group == "" && a.related != nil {
		return a.related(ref)
	}
	if ref.Namespace == "" && a.cluster != nil && !a.cluster(ref.Kind, ref.Group) {
		return false
	}
	return a.related == nil || a.related(ref)
}

type podTemplateProvider interface {
	podTemplateFact(Issue, podTemplateAccess) *issuesapi.DiagnosticFact
}

func templateSymptom(i Issue) string {
	switch i.Category {
	case issuesapi.CategoryOOMKilled:
		return "oom"
	case issuesapi.CategoryCrashLoop, issuesapi.CategoryHighRestart:
		if i.LastTerminatedReason == "OOMKilled" {
			return "oom"
		}
	case issuesapi.CategoryUnschedulable:
		return "scheduling"
	case issuesapi.CategoryImagePullFailed:
		return "image"
	}
	return ""
}

func enrichPodTemplateContext(shaped, flat []Issue, p Provider, access podTemplateAccess, grouped bool, subject Ref) []Issue {
	provider, ok := p.(podTemplateProvider)
	if !ok || len(shaped) == 0 {
		return shaped
	}
	byID := make(map[string][]Issue)
	for _, i := range flat {
		if i.Kind != "Pod" || i.Group != "" || templateSymptom(i) == "" {
			continue
		}
		if strings.EqualFold(subject.Kind, "Pod") && (subject.Group != "" || i.Namespace != subject.Namespace || i.Name != subject.Name) {
			continue
		}
		byID[i.ID] = append(byID[i.ID], i)
	}
	out := append([]Issue(nil), shaped...)
	for idx := range out {
		i := &out[idx]
		if i.DiagnosticContext != nil && len(i.DiagnosticContext.Facts) >= maxDiagnosticFacts {
			continue
		}
		members := byID[i.ID]
		if !grouped {
			members = []Issue{*i}
		}
		sort.SliceStable(members, func(a, b int) bool { return betterRepresentative(members[a], members[b]) })
		seen := make(map[string]bool)
		for _, member := range members {
			key := resourceKey(member.Group, member.Kind, member.Namespace, member.Name)
			if seen[key] {
				continue
			}
			if len(seen) == maxTemplateWitnesses {
				break
			}
			seen[key] = true
			if member.Kind != "Pod" || member.Group != "" || templateSymptom(member) == "" {
				continue
			}
			fact := provider.podTemplateFact(member, access)
			if fact == nil {
				continue
			}
			if grouped && !strings.EqualFold(subject.Kind, "Pod") && slices.ContainsFunc(members, func(other Issue) bool { return other.Namespace != member.Namespace || other.Name != member.Name }) {
				fact.Message += " This is one affected pod's example, not a comparison of every replica."
			}
			ctx := issuesapi.DiagnosticContext{Role: issuesapi.DiagnosticRoleContext}
			if i.DiagnosticContext != nil {
				ctx = *i.DiagnosticContext
				ctx.Facts = slices.Clone(ctx.Facts)
			}
			ctx.Facts = append(ctx.Facts, *fact)
			i.DiagnosticContext = &ctx
			break
		}
	}
	return out
}

type templateDifference struct {
	path            string
	message         string
	resource        corev1.ResourceName
	resourceKind    string
	value           resource.Quantity
	templateMissing bool
}

func comparePodTemplate(pod *corev1.Pod, template *corev1.PodSpec, symptom string) []templateDifference {
	var differences []templateDifference
	if symptom == "scheduling" && pod.Spec.PriorityClassName != template.PriorityClassName {
		message := fmt.Sprintf("priorityClassName is %q; template specifies %q", pod.Spec.PriorityClassName, template.PriorityClassName)
		if template.PriorityClassName == "" {
			message = fmt.Sprintf("priorityClassName is %q; the template leaves it unset (defaulting can supply it)", pod.Spec.PriorityClassName)
		}
		differences = append(differences, templateDifference{
			path: "priorityClassName", templateMissing: template.PriorityClassName == "",
			message: message,
		})
	}
	for _, set := range []struct {
		path     string
		live     []corev1.Container
		template []corev1.Container
		statuses []corev1.ContainerStatus
	}{
		{"containers", pod.Spec.Containers, template.Containers, pod.Status.ContainerStatuses},
		{"initContainers", pod.Spec.InitContainers, template.InitContainers, pod.Status.InitContainerStatuses},
	} {
		original := make(map[string]corev1.Container, len(set.template))
		for _, c := range set.template {
			original[c.Name] = c
		}
		activeOOM := make(map[string]bool)
		if symptom == "oom" {
			for _, status := range health.ActiveOOMKilledContainers(&corev1.Pod{Spec: corev1.PodSpec{Containers: set.live}, Status: corev1.PodStatus{ContainerStatuses: set.statuses}}, time.Now()) {
				if set.path == "initContainers" && status.State.Terminated != nil && status.State.Terminated.ExitCode == 0 {
					continue
				}
				activeOOM[status.Name] = true
			}
		}
		statuses := make(map[string]corev1.ContainerStatus, len(set.statuses))
		for _, s := range set.statuses {
			statuses[s.Name] = s
		}
		containers := slices.Clone(set.live)
		sort.Slice(containers, func(a, b int) bool { return containers[a].Name < containers[b].Name })
		for _, live := range containers {
			before, shared := original[live.Name]
			if !shared {
				continue
			}
			path := fmt.Sprintf("%s[%s]", set.path, live.Name)
			status := statuses[live.Name]
			switch symptom {
			case "oom":
				if !activeOOM[live.Name] {
					continue
				}
				value := live.Resources.Limits[corev1.ResourceMemory]
				old, present := before.Resources.Limits[corev1.ResourceMemory]
				if value.Sign() > 0 && (!present || old.IsZero() || value.Cmp(old) < 0) {
					differences = append(differences, resourceDifference(path, "limits", corev1.ResourceMemory, old, value, present))
				}
			case "scheduling":
				keys := make([]string, 0, len(live.Resources.Requests))
				for key := range live.Resources.Requests {
					keys = append(keys, string(key))
				}
				sort.Strings(keys)
				for _, key := range keys {
					resourceName := corev1.ResourceName(key)
					value := live.Resources.Requests[resourceName]
					old, present := before.Resources.Requests[resourceName]
					if value.Sign() <= 0 || value.Cmp(old) <= 0 {
						continue
					}
					if !present {
						limit, hasLimit := before.Resources.Limits[resourceName]
						liveLimit, hasLiveLimit := live.Resources.Limits[resourceName]
						if hasLimit && hasLiveLimit && limit.Cmp(value) == 0 && liveLimit.Cmp(limit) == 0 {
							continue
						}
					}
					differences = append(differences, resourceDifference(path, "requests", resourceName, old, value, present))
				}
			case "image":
				if status.State.Waiting == nil || (status.State.Waiting.Reason != "ImagePullBackOff" && status.State.Waiting.Reason != "ErrImagePull") {
					continue
				}
				if before.Image != "" && !sameTemplateImage(before.Image, live.Image) {
					differences = append(differences, templateDifference{path: path + ".image", message: path + ".image differs from the template; inspect the two image references"})
				}
			}
		}
	}
	if symptom == "scheduling" {
		if !maps.Equal(pod.Spec.NodeSelector, template.NodeSelector) {
			differences = append(differences, templateDifference{path: "nodeSelector", message: "nodeSelector differs from the template"})
		}
		for path, original := range requiredAffinities(template.Affinity) {
			if canonicalSchedulingValue(original) != canonicalSchedulingValue(requiredAffinities(pod.Spec.Affinity)[path]) {
				differences = append(differences, templateDifference{path: path, message: path + " differs from the template"})
			}
		}
		for _, original := range template.Tolerations {
			if !slices.ContainsFunc(pod.Spec.Tolerations, func(live corev1.Toleration) bool {
				if original.Operator == "" {
					original.Operator = corev1.TolerationOpEqual
				}
				if live.Operator == "" {
					live.Operator = corev1.TolerationOpEqual
				}
				return original.Key == live.Key && original.Operator == live.Operator && original.Value == live.Value && original.Effect == live.Effect
			}) {
				differences = append(differences, templateDifference{path: "tolerations", message: "a template toleration is missing or changed in the live pod spec"})
				break
			}
		}
	}
	sort.SliceStable(differences, func(a, b int) bool {
		if (differences[a].path == "priorityClassName") != (differences[b].path == "priorityClassName") {
			return differences[a].path == "priorityClassName"
		}
		return differences[a].path < differences[b].path
	})
	return differences
}

func resourceDifference(container, kind string, key corev1.ResourceName, before, live resource.Quantity, present bool) templateDifference {
	path := fmt.Sprintf("%s.resources.%s.%s", container, kind, key)
	original := "omitted"
	if present {
		original = before.String()
	}
	return templateDifference{path: path, resource: key, resourceKind: kind, value: live, templateMissing: !present,
		message: fmt.Sprintf("%s is %s; template specifies %s", path, live.String(), original)}
}

func sameTemplateImage(a, b string) bool {
	if a == b {
		return true
	}
	left, errA := name.ParseReference(a)
	right, errB := name.ParseReference(b)
	return errA == nil && errB == nil && left.Name() == right.Name()
}

func requiredAffinities(a *corev1.Affinity) map[string]any {
	var node *corev1.NodeSelector
	var pod, anti []corev1.PodAffinityTerm
	if a != nil {
		if a.NodeAffinity != nil {
			node = a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
		}
		if a.PodAffinity != nil {
			pod = a.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution
		}
		if a.PodAntiAffinity != nil {
			anti = a.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution
		}
	}
	return map[string]any{"affinity.nodeAffinity.required": node, "affinity.podAffinity.required": pod, "affinity.podAntiAffinity.required": anti}
}

// Scheduling selectors contain sets nested under AND/OR structures. Sorting each
// set preserves that structure while avoiding differences caused only by order.
func canonicalSchedulingValue(value any) string {
	data, _ := json.Marshal(value)
	var decoded any
	_ = json.Unmarshal(data, &decoded)
	var canonical func(any) any
	canonical = func(v any) any {
		switch v := v.(type) {
		case []any:
			for i := range v {
				v[i] = canonical(v[i])
			}
			sort.Slice(v, func(a, b int) bool {
				left, _ := json.Marshal(v[a])
				right, _ := json.Marshal(v[b])
				return string(left) < string(right)
			})
			return v
		case map[string]any:
			for k, item := range v {
				if item == nil {
					delete(v, k)
					continue
				}
				if list, ok := item.([]any); ok && len(list) == 0 {
					delete(v, k)
					continue
				}
				v[k] = canonical(item)
			}
			return v
		default:
			return v
		}
	}
	if decoded == nil {
		return "null"
	}
	if list, ok := decoded.([]any); ok && len(list) == 0 {
		return "null"
	}
	if m, ok := decoded.(map[string]any); ok && len(m) == 0 {
		return "{}"
	}
	data, _ = json.Marshal(canonical(decoded))
	return string(data)
}
