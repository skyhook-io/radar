package audit

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	utilversion "k8s.io/apimachinery/pkg/util/version"
	resourcehelper "k8s.io/component-helpers/resource"
)

type podResourceDefaulting struct {
	afterAdmission           bool
	hugepages                bool
	deriveRequestedHugepages bool
	repeatAfterAdmission     bool
}

func resourceDefaultingModes(version string) []podResourceDefaulting {
	v, err := utilversion.ParseGeneric(strings.TrimPrefix(strings.TrimSpace(version), "v"))
	var modes []podResourceDefaulting
	if err != nil || v.Major() != 1 || v.Minor() < 32 {
		modes = []podResourceDefaulting{{}, {hugepages: true, deriveRequestedHugepages: true}, {hugepages: true}, {afterAdmission: true, hugepages: true}}
	} else if v.Minor() < 37 {
		modes = []podResourceDefaulting{{hugepages: v.Minor() >= 33, deriveRequestedHugepages: v.Minor() == 33}}
	} else {
		// The default-on feature gate can still be disabled. Version alone cannot
		// establish which admission order this cluster uses.
		modes = []podResourceDefaulting{{hugepages: true}, {afterAdmission: true, hugepages: true}}
	}
	var candidates []podResourceDefaulting
	for _, mode := range modes {
		candidates = append(candidates, mode)
		if !mode.afterAdmission {
			// Decoding a webhook patch reruns API defaulting, even when the
			// patch only changes metadata. It can now see LimitRange defaults.
			mode.repeatAfterAdmission = true
			candidates = append(candidates, mode)
		}
	}
	return candidates
}

func podResourceCandidates(spec corev1.PodSpec, stored bool, version string, lrs []*corev1.LimitRange) []corev1.PodSpec {
	if stored {
		return []corev1.PodSpec{spec}
	}
	var candidates []corev1.PodSpec
	modes := []podResourceDefaulting{{}}
	if spec.Resources != nil {
		modes = resourceDefaultingModes(version)
	}
	for _, defaults := range resourceDefaultCandidates(lrs) {
		for _, mode := range modes {
			if mode.repeatAfterAdmission && len(lrs) == 0 {
				continue
			}
			pod := &corev1.Pod{Spec: *spec.DeepCopy()}
			forEachResourceContainer(&pod.Spec, defaultContainerRequests)
			if !mode.afterAdmission {
				defaultSharedResources(pod, mode)
			}
			forEachResourceContainer(&pod.Spec, func(c *corev1.Container) {
				fillResourceDefaults(&c.Resources.Requests, defaults.Requests)
				fillResourceDefaults(&c.Resources.Limits, defaults.Limits)
			})
			if mode.repeatAfterAdmission {
				forEachResourceContainer(&pod.Spec, defaultContainerRequests)
			}
			if mode.afterAdmission || mode.repeatAfterAdmission {
				defaultSharedResources(pod, mode)
			}
			candidates = append(candidates, pod.Spec)
		}
	}
	return candidates
}

func forEachResourceContainer(spec *corev1.PodSpec, visit func(*corev1.Container)) {
	for i := range spec.Containers {
		visit(&spec.Containers[i])
	}
	for i := range spec.InitContainers {
		visit(&spec.InitContainers[i])
	}
}

func defaultContainerRequests(c *corev1.Container) {
	fillResourceDefaults(&c.Resources.Requests, c.Resources.Limits)
}

func fillResourceDefaults(dst *corev1.ResourceList, defaults corev1.ResourceList) {
	for name, value := range defaults {
		if _, present := (*dst)[name]; present {
			continue
		}
		if *dst == nil {
			*dst = make(corev1.ResourceList)
		}
		(*dst)[name] = value.DeepCopy()
	}
}

func defaultSharedResources(pod *corev1.Pod, mode podResourceDefaulting) {
	shared := pod.Spec.Resources
	if shared == nil || len(shared.Requests)+len(shared.Limits) == 0 {
		return
	}
	if mode.hugepages {
		for name, value := range resourcehelper.AggregateContainerLimits(pod, resourcehelper.PodResourcesOptions{}) {
			if !strings.HasPrefix(string(name), corev1.ResourceHugePagesPrefix) {
				continue
			}
			if _, present := shared.Requests[name]; !present || mode.deriveRequestedHugepages {
				fillResourceDefaults(&shared.Limits, corev1.ResourceList{name: value})
			}
		}
	}
	if !mode.afterAdmission && len(shared.Limits) == 0 {
		return
	}
	requests := resourcehelper.AggregateContainerRequests(pod, resourcehelper.PodResourcesOptions{})
	for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
		if value, present := requests[name]; present {
			fillResourceDefaults(&shared.Requests, corev1.ResourceList{name: value})
		}
	}
	fillResourceDefaults(&shared.Requests, shared.Limits)
	if !mode.afterAdmission {
		return
	}
	limits := resourcehelper.AggregateContainerLimits(pod, resourcehelper.PodResourcesOptions{})
	for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
		request, requested := shared.Requests[name]
		if _, present := shared.Limits[name]; present || !requested {
			continue
		}
		allLimited := true
		forEachResourceContainer(&pod.Spec, func(c *corev1.Container) {
			if _, present := c.Resources.Limits[name]; !present {
				allLimited = false
			}
		})
		if allLimited {
			limit := limits[name]
			if request.Cmp(limit) > 0 {
				limit = request
			}
			fillResourceDefaults(&shared.Limits, corev1.ResourceList{name: limit})
		}
	}
}

// LimitRanges have no defined ordering. Per-key winners form a conservative
// superset of possible orders. Zero/positive classes suffice for missing-value
// checks; these candidates must not be used to validate resource quantities.
func resourceDefaultCandidates(lrs []*corev1.LimitRange) []corev1.ResourceRequirements {
	ranges := make([]corev1.ResourceRequirements, 0, len(lrs))
	hugepages := corev1.ResourceRequirements{}
	for _, lr := range lrs {
		d := corev1.ResourceRequirements{Requests: corev1.ResourceList{}, Limits: corev1.ResourceList{}}
		for _, item := range lr.Spec.Limits {
			if item.Type != corev1.LimitTypeContainer {
				continue
			}
			for name, value := range item.DefaultRequest {
				d.Requests[name] = value
			}
			for name, value := range item.Default {
				d.Limits[name] = value
			}
		}
		ranges = append(ranges, d)
		// CPU/memory coverage depends on hugepage key presence, not its
		// quantity: a derived hugepage limit activates legacy pod requests.
		for name, value := range d.Limits {
			if strings.HasPrefix(string(name), corev1.ResourceHugePagesPrefix) {
				fillResourceDefaults(&hugepages.Limits, corev1.ResourceList{name: value})
			}
		}
		for name, value := range d.Requests {
			if strings.HasPrefix(string(name), corev1.ResourceHugePagesPrefix) {
				fillResourceDefaults(&hugepages.Requests, corev1.ResourceList{name: value})
			}
		}
	}
	candidates := []corev1.ResourceRequirements{hugepages}
	for _, requests := range []bool{true, false} {
		for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
			var choices []resource.Quantity
			seen := map[bool]bool{}
			for _, d := range ranges {
				values := d.Limits
				if requests {
					values = d.Requests
				}
				if value, present := values[name]; present && !seen[value.IsZero()] {
					seen[value.IsZero()] = true
					choices = append(choices, value)
				}
			}
			if len(choices) == 0 {
				continue
			}
			var next []corev1.ResourceRequirements
			for _, candidate := range candidates {
				for _, value := range choices {
					d := candidate.DeepCopy()
					values := &d.Limits
					if requests {
						values = &d.Requests
					}
					fillResourceDefaults(values, corev1.ResourceList{name: value})
					next = append(next, *d)
				}
			}
			candidates = next
		}
	}
	return candidates
}

func resourceConfigured(spec corev1.PodSpec, c corev1.Container, name corev1.ResourceName, request bool) bool {
	values := c.Resources.Limits
	if request {
		values = c.Resources.Requests
	}
	if value := values[name]; !value.IsZero() {
		return true
	}
	if spec.Resources != nil {
		values = spec.Resources.Limits
		if request {
			values = spec.Resources.Requests
		}
		value := values[name]
		return !value.IsZero()
	}
	return false
}

func efficiencyContainers(spec corev1.PodSpec) []corev1.Container {
	containers := append([]corev1.Container(nil), spec.Containers...)
	for _, c := range spec.InitContainers {
		if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			containers = append(containers, c)
		}
	}
	return containers
}
