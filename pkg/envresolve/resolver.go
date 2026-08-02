package envresolve

import (
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/kubectl/pkg/util/fieldpath"
	kubectlresource "k8s.io/kubectl/pkg/util/resource"
)

type binding struct {
	row       Row
	value     string
	available bool
}

func ResolvePod(pod *corev1.Pod, sources map[SourceID]SourceData, node NodeData) PodEnvironment {
	if pod == nil {
		return PodEnvironment{}
	}
	containers := make([]Container, 0, len(pod.Spec.InitContainers)+len(pod.Spec.Containers))
	for i := range pod.Spec.InitContainers {
		role := "init"
		if pod.Spec.InitContainers[i].RestartPolicy != nil && *pod.Spec.InitContainers[i].RestartPolicy == corev1.ContainerRestartPolicyAlways {
			role = "sidecar"
		}
		containers = append(containers, resolveContainer(pod, &pod.Spec.InitContainers[i], role, sources, node))
	}
	for i := range pod.Spec.Containers {
		containers = append(containers, resolveContainer(pod, &pod.Spec.Containers[i], "container", sources, node))
	}
	return PodEnvironment{Containers: containers}
}

func ResolveVariable(pod *corev1.Pod, containerName, variable string, sources map[SourceID]SourceData, node NodeData) (Row, string, bool, bool) {
	if pod == nil {
		return Row{}, "", false, false
	}
	for i := range pod.Spec.InitContainers {
		if pod.Spec.InitContainers[i].Name == containerName {
			return resolvedVariable(pod, &pod.Spec.InitContainers[i], variable, sources, node)
		}
	}
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == containerName {
			return resolvedVariable(pod, &pod.Spec.Containers[i], variable, sources, node)
		}
	}
	return Row{}, "", false, false
}

func resolvedVariable(pod *corev1.Pod, container *corev1.Container, variable string, sources map[SourceID]SourceData, node NodeData) (Row, string, bool, bool) {
	bindings, shadowed := resolveBindings(pod, container, sources, node)
	current, ok := bindings[variable]
	if !ok {
		return Row{}, "", false, false
	}
	current.row.ShadowedSources = compactRefs(shadowed[variable])
	return current.row, current.value, current.available, true
}

func resolveContainer(pod *corev1.Pod, container *corev1.Container, role string, sources map[SourceID]SourceData, node NodeData) Container {
	bindings, shadowed := resolveBindings(pod, container, sources, node)
	rows := make([]Row, 0, len(bindings))
	for name, current := range bindings {
		current.row.ShadowedSources = compactRefs(shadowed[name])
		if current.row.State == ValueMissing && !current.row.Optional {
			current.row.MissingImpact = RequiredMissingImpact(pod, container.Name)
			current.row.Message = missingImpactMessage(current.row.Message, current.row.MissingImpact)
		}
		if !current.row.Sensitive {
			current.row.Value = current.value
		}
		rows = append(rows, current.row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return Container{Name: container.Name, Role: role, Rows: rows}
}

func RequiredMissingImpact(pod *corev1.Pod, containerName string) MissingImpact {
	if pod == nil {
		return MissingImpactStartupBlocked
	}
	statuses := append([]corev1.ContainerStatus(nil), pod.Status.InitContainerStatuses...)
	statuses = append(statuses, pod.Status.ContainerStatuses...)
	for _, status := range statuses {
		if status.Name != containerName {
			continue
		}
		if status.State.Waiting != nil && status.State.Waiting.Reason == "CreateContainerConfigError" {
			return MissingImpactStartupBlocked
		}
		if status.State.Waiting != nil {
			return MissingImpactStartupBlocked
		}
		if status.State.Running != nil || status.State.Terminated != nil || status.LastTerminationState.Terminated != nil || status.ContainerID != "" {
			return MissingImpactRestartBlocked
		}
		break
	}
	return MissingImpactStartupBlocked
}

func missingImpactMessage(message string, impact MissingImpact) string {
	suffix := "This prevents the container from starting."
	if impact == MissingImpactRestartBlocked {
		suffix = "The Pod has already started, but this container cannot start again while this is missing."
	}
	if message == "" {
		return suffix
	}
	return message + " " + suffix
}

func resolveBindings(pod *corev1.Pod, container *corev1.Container, sources map[SourceID]SourceData, node NodeData) (map[string]binding, map[string][]SourceRef) {
	bindings := make(map[string]binding)
	shadowed := make(map[string][]SourceRef)
	for _, from := range container.EnvFrom {
		kind, name, optional := envFromIdentity(from)
		if name == "" {
			continue
		}
		source := sources[SourceID{Kind: kind, Name: name}]
		if source.Kind == "" {
			source = SourceData{Kind: kind, Name: name, State: SourceMissing, Optional: optional}
		}
		keys := append([]string(nil), source.Keys...)
		if len(keys) == 0 {
			for key := range source.Values {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		if source.State != SourceAvailable {
			name := from.Prefix + "* (" + kind + "/" + source.Name + ")"
			state, message := stateForSource(source.State, optional, kind, source.Name)
			if source.Message != "" {
				message = source.Message
			}
			bindings[name] = binding{row: Row{Name: name, State: state, Sensitive: kind == "Secret", Source: SourceRef{Kind: kind, Name: source.Name}, Message: message, Optional: optional, Placeholder: true}}
			continue
		}
		for _, key := range keys {
			name := from.Prefix + key
			if len(validation.IsEnvVarName(name)) != 0 {
				continue
			}
			ref := SourceRef{Kind: kind, Name: source.Name, Key: key}
			if previous, ok := bindings[name]; ok {
				shadowed[name] = append(shadowed[name], previous.row.Source)
			}
			value, available := source.Values[key]
			state := ValueResolved
			if source.Sensitive {
				state = ValueMasked
			}
			bindings[name] = binding{row: Row{Name: name, State: state, Sensitive: source.Sensitive, Source: ref, Optional: optional}, value: value, available: available}
		}
	}

	for _, env := range container.Env {
		if previous, ok := bindings[env.Name]; ok {
			shadowed[env.Name] = append(shadowed[env.Name], previous.row.Source)
		}
		bindings[env.Name] = resolveExplicit(pod, container, env, bindings, sources, node)
	}

	return bindings, shadowed
}

func resolveExplicit(pod *corev1.Pod, container *corev1.Container, env corev1.EnvVar, bindings map[string]binding, sources map[SourceID]SourceData, node NodeData) binding {
	if env.ValueFrom == nil {
		value, deps, sensitive, available, runtimeDependent, dependencyState, dependencyMessage := expand(env.Value, bindings)
		state := ValueResolved
		if dependencyState != ValueResolved {
			state = dependencyState
		} else if sensitive {
			state = ValueMasked
		} else if !available || runtimeDependent {
			state = ValueUnavailable
		}
		message := dependencyMessage
		if runtimeDependent && message == "" {
			message = "This value is completed when the container starts because it uses another variable that is not declared on the Pod."
		}
		return binding{row: Row{Name: env.Name, State: state, Sensitive: sensitive, Source: SourceRef{Kind: "Direct"}, Dependencies: deps, Message: message, RuntimeDependent: runtimeDependent}, value: value, available: available && !runtimeDependent}
	}
	if ref := env.ValueFrom.ConfigMapKeyRef; ref != nil {
		return resolveKeyRef(env.Name, "ConfigMap", ref.Name, ref.Key, ref.Optional != nil && *ref.Optional, sources)
	}
	if ref := env.ValueFrom.SecretKeyRef; ref != nil {
		return resolveKeyRef(env.Name, "Secret", ref.Name, ref.Key, ref.Optional != nil && *ref.Optional, sources)
	}
	if ref := env.ValueFrom.FieldRef; ref != nil {
		value, err := podFieldValue(pod, ref.FieldPath)
		if err != nil {
			return unavailable(env.Name, "Pod", err.Error())
		}
		return binding{row: Row{Name: env.Name, State: ValueResolved, Source: SourceRef{Kind: "Pod", Key: ref.FieldPath}, CurrentPodValue: true, Message: "This is the current Pod value; the container evaluated it when it started."}, value: value, available: true}
	}
	if ref := env.ValueFrom.ResourceFieldRef; ref != nil {
		resourceContainer, err := resourceFieldContainer(pod, container, ref.ContainerName)
		if err != nil {
			return unavailable(env.Name, "Container resources", err.Error())
		}
		if resourceName, ok := missingLimitResource(resourceContainer, ref.Resource); ok {
			if node.State != SourceAvailable {
				state, message := stateForSource(node.State, false, "Node", pod.Spec.NodeName)
				return binding{row: Row{Name: env.Name, State: state, Source: SourceRef{Kind: "Container resources", Key: ref.Resource}, Message: message}}
			}
			quantity, ok := node.Allocatable[resourceName]
			if !ok {
				return unavailable(env.Name, "Container resources", "The assigned Node does not report this resource.")
			}
			copy := resourceContainer.DeepCopy()
			if copy.Resources.Limits == nil {
				copy.Resources.Limits = corev1.ResourceList{}
			}
			copy.Resources.Limits[resourceName] = quantity
			resourceContainer = copy
		}
		value, err := kubectlresource.ExtractContainerResourceValue(ref, resourceContainer)
		if err != nil {
			return unavailable(env.Name, "Container resources", err.Error())
		}
		return binding{row: Row{Name: env.Name, State: ValueResolved, Source: SourceRef{Kind: "Container resources", Key: ref.Resource}, CurrentPodValue: true, Message: "This is the current resource value; the container evaluated it when it started."}, value: value, available: true}
	}
	return unavailable(env.Name, "Unknown", "Unsupported value source")
}

func podFieldValue(pod *corev1.Pod, path string) (string, error) {
	switch path {
	case "spec.nodeName":
		return pod.Spec.NodeName, nil
	case "spec.serviceAccountName":
		return pod.Spec.ServiceAccountName, nil
	case "status.hostIP":
		return pod.Status.HostIP, nil
	case "status.hostIPs":
		values := make([]string, 0, len(pod.Status.HostIPs))
		for _, ip := range pod.Status.HostIPs {
			values = append(values, ip.IP)
		}
		return strings.Join(values, ","), nil
	case "status.podIP":
		return pod.Status.PodIP, nil
	case "status.podIPs":
		values := make([]string, 0, len(pod.Status.PodIPs))
		for _, ip := range pod.Status.PodIPs {
			values = append(values, ip.IP)
		}
		return strings.Join(values, ","), nil
	default:
		return fieldpath.ExtractFieldPathAsString(pod, path)
	}
}

func missingLimitResource(container *corev1.Container, selector string) (corev1.ResourceName, bool) {
	if !strings.HasPrefix(selector, "limits.") {
		return "", false
	}
	name := corev1.ResourceName(strings.TrimPrefix(selector, "limits."))
	_, exists := container.Resources.Limits[name]
	return name, !exists
}

func PodEnvironmentNeedsNode(pod *corev1.Pod) bool {
	if pod == nil {
		return false
	}
	containers := append([]corev1.Container(nil), pod.Spec.InitContainers...)
	containers = append(containers, pod.Spec.Containers...)
	for i := range containers {
		for _, env := range containers[i].Env {
			if env.ValueFrom == nil || env.ValueFrom.ResourceFieldRef == nil {
				continue
			}
			ref := env.ValueFrom.ResourceFieldRef
			resourceContainer, err := resourceFieldContainer(pod, &containers[i], ref.ContainerName)
			if err != nil {
				continue
			}
			if _, missing := missingLimitResource(resourceContainer, ref.Resource); missing {
				return true
			}
		}
	}
	return false
}

func resourceFieldContainer(pod *corev1.Pod, current *corev1.Container, name string) (*corev1.Container, error) {
	if name == "" || name == current.Name {
		return current, nil
	}
	for i := range pod.Spec.InitContainers {
		if pod.Spec.InitContainers[i].Name == name {
			return &pod.Spec.InitContainers[i], nil
		}
	}
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == name {
			return &pod.Spec.Containers[i], nil
		}
	}
	return nil, fmt.Errorf("container %q was not found", name)
}

func resolveKeyRef(envName, kind, name, key string, optional bool, sources map[SourceID]SourceData) binding {
	source := sources[SourceID{Kind: kind, Name: name}]
	if source.Kind == "" {
		source = SourceData{Kind: kind, Name: name, State: SourceMissing}
	}
	ref := SourceRef{Kind: kind, Name: name, Key: key}
	if source.State != SourceAvailable {
		state, message := stateForSource(source.State, optional, kind, name)
		if source.Message != "" {
			message = source.Message
		}
		return binding{row: Row{Name: envName, State: state, Sensitive: kind == "Secret", Source: ref, Message: message, Optional: optional}}
	}
	value, ok := source.Values[key]
	if !ok && source.Sensitive {
		for _, candidate := range source.Keys {
			if candidate == key {
				return binding{row: Row{Name: envName, State: ValueMasked, Sensitive: true, Source: ref, Optional: optional}}
			}
		}
	}
	if !ok {
		message := fmt.Sprintf("Key %s is missing from %s/%s.", key, kind, name)
		if optional {
			message = fmt.Sprintf("Optional key %s is absent from %s/%s.", key, kind, name)
		}
		return binding{row: Row{Name: envName, State: ValueMissing, Sensitive: source.Sensitive, Source: ref, Message: message, Optional: optional}}
	}
	state := ValueResolved
	if source.Sensitive {
		state = ValueMasked
	}
	return binding{row: Row{Name: envName, State: state, Sensitive: source.Sensitive, Source: ref, Optional: optional}, value: value, available: true}
}

func expand(input string, bindings map[string]binding) (string, []SourceRef, bool, bool, bool, ValueState, string) {
	var out strings.Builder
	var deps []SourceRef
	sensitive, available, runtimeDependent := false, true, false
	dependencyState, dependencyMessage := ValueResolved, ""
	for i := 0; i < len(input); {
		if input[i] != '$' || i+1 >= len(input) {
			out.WriteByte(input[i])
			i++
			continue
		}
		if input[i+1] == '$' {
			out.WriteByte('$')
			i += 2
			continue
		}
		if input[i+1] != '(' {
			out.WriteByte(input[i])
			i++
			continue
		}
		end := strings.IndexByte(input[i+2:], ')')
		if end < 0 {
			out.WriteString(input[i:])
			break
		}
		end += i + 2
		name := input[i+2 : end]
		dep, ok := bindings[name]
		if !ok {
			out.WriteString(input[i : end+1])
			runtimeDependent = true
		} else {
			if dep.row.State == ValueMissing && dep.row.Optional {
				out.WriteString(input[i : end+1])
				i = end + 1
				continue
			}
			deps = append(deps, SourceRef{Kind: "Variable", Variable: name})
			if dep.row.Source.Kind != "Direct" {
				deps = append(deps, dep.row.Source)
			}
			deps = append(deps, dep.row.Dependencies...)
			sensitive = sensitive || dep.row.Sensitive
			available = available && dep.available
			runtimeDependent = runtimeDependent || dep.row.RuntimeDependent
			if rankValueState(dep.row.State) > rankValueState(dependencyState) {
				dependencyState = dep.row.State
				dependencyMessage = dep.row.Message
			}
			out.WriteString(dep.value)
		}
		i = end + 1
	}
	return out.String(), compactRefs(deps), sensitive, available, runtimeDependent, dependencyState, dependencyMessage
}

func rankValueState(state ValueState) int {
	switch state {
	case ValueDenied:
		return 3
	case ValueMissing:
		return 2
	case ValueUnavailable:
		return 1
	default:
		return 0
	}
}

func envFromIdentity(from corev1.EnvFromSource) (string, string, bool) {
	if from.ConfigMapRef != nil {
		return "ConfigMap", from.ConfigMapRef.Name, from.ConfigMapRef.Optional != nil && *from.ConfigMapRef.Optional
	}
	if from.SecretRef != nil {
		return "Secret", from.SecretRef.Name, from.SecretRef.Optional != nil && *from.SecretRef.Optional
	}
	return "", "", false
}

func stateForSource(state SourceState, optional bool, kind, name string) (ValueState, string) {
	if state == SourceDenied {
		return ValueDenied, fmt.Sprintf("Access to %s/%s is required to resolve these variables.", kind, name)
	}
	if state == SourceUnavailable {
		return ValueUnavailable, fmt.Sprintf("%s/%s could not be read.", kind, name)
	}
	if optional {
		return ValueMissing, fmt.Sprintf("Optional %s/%s is absent.", kind, name)
	}
	return ValueMissing, fmt.Sprintf("%s/%s is missing.", kind, name)
}

func unavailable(name, kind, message string) binding {
	return binding{row: Row{Name: name, State: ValueUnavailable, Source: SourceRef{Kind: kind}, Message: message}}
}

func compactRefs(refs []SourceRef) []SourceRef {
	seen := make(map[string]bool)
	out := make([]SourceRef, 0, len(refs))
	for _, ref := range refs {
		key := ref.Kind + "\x00" + ref.Name + "\x00" + ref.Key + "\x00" + ref.Variable
		if ref.Kind == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, ref)
	}
	return out
}
