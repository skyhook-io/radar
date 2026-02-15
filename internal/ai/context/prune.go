package context

// Metadata keys to strip at all levels (beyond what cache.dropManagedFields already removes)
var stripMetadataKeys = map[string]bool{
	"resourceVersion":            true,
	"uid":                        true,
	"generation":                 true,
	"selfLink":                   true,
	"generateName":               true,
	"managedFields":              true,
	"deletionGracePeriodSeconds": true,
	"finalizers":                 true,
}

// Annotations to keep at Compact level (everything else is stripped).
// At Detail level, ALL annotations are kept.
var keepAnnotationPrefixes = []string{
	"kubernetes.io/ingress.class",
	"argo",
	"flux",
	"helm.sh",
	"app.kubernetes.io",
}

// Container spec fields to strip at all levels
var stripContainerFields = map[string]bool{
	"terminationMessagePath":   true,
	"terminationMessagePolicy": true,
}

// Pod spec fields to strip at all levels
var stripPodSpecFields = map[string]bool{
	"tolerations":      true,
	"dnsPolicy":        true,
	"schedulerName":    true,
	"priority":         true,
	"priorityClassName": true,
	"preemptionPolicy": true,
	"nodeName":         true,
	"enableServiceLinks": true,
}

// Additional container fields to strip at Compact level (aggressive spec pruning)
var stripContainerFieldsCompact = map[string]bool{
	"command":         true,
	"args":            true,
	"livenessProbe":   true,
	"readinessProbe":  true,
	"startupProbe":    true,
	"lifecycle":       true,
	"securityContext": true,
	"workingDir":      true,
	"stdin":           true,
	"stdinOnce":       true,
	"tty":             true,
}

// Additional pod spec fields to strip at Compact level
var stripPodSpecFieldsCompact = map[string]bool{
	"volumes":            true,
	"serviceAccountName": true,
	"serviceAccount":     true,
	"securityContext":    true,
	"hostNetwork":        true,
	"hostPID":            true,
	"hostIPC":            true,
	"affinity":           true,
	"topologySpreadConstraints": true,
}

// Pod status fields to strip at Detail and Compact levels
var stripPodStatusFields = map[string]bool{
	"hostIP":    true,
	"podIP":     true,
	"podIPs":    true,
	"qosClass":  true,
	"startTime": true,
}

// Workload status fields to strip at Detail and Compact levels (Deployment, StatefulSet, DaemonSet)
var stripWorkloadStatusFields = map[string]bool{
	"observedGeneration":       true,
	"collisionCount":           true,
	"updatedNumberScheduled":   true,
}

// pruneMetadataCommon strips metadata fields common to all levels.
func pruneMetadataCommon(meta map[string]any) {
	for key := range stripMetadataKeys {
		delete(meta, key)
	}
}

// pruneAnnotationsCompact filters annotations at Compact level: only keeps known prefixes.
func pruneAnnotationsCompact(meta map[string]any) {
	annotations, ok := meta["annotations"].(map[string]any)
	if !ok {
		return
	}
	filtered := make(map[string]any)
	for k, v := range annotations {
		if shouldKeepAnnotation(k) {
			filtered[k] = v
		}
	}
	if len(filtered) == 0 {
		delete(meta, "annotations")
	} else {
		meta["annotations"] = filtered
	}
}

func shouldKeepAnnotation(key string) bool {
	for _, prefix := range keepAnnotationPrefixes {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

// prunePodSpec strips noisy fields from a pod spec (direct or template.spec).
func prunePodSpec(spec map[string]any) {
	for key := range stripPodSpecFields {
		delete(spec, key)
	}
	pruneContainersInSpec(spec)
}

// prunePodSpecCompact additionally strips fields only removed at Compact level.
func prunePodSpecCompact(spec map[string]any) {
	for key := range stripPodSpecFields {
		delete(spec, key)
	}
	for key := range stripPodSpecFieldsCompact {
		delete(spec, key)
	}
	pruneContainersCompact(spec, "containers")
	pruneContainersCompact(spec, "initContainers")
}

func pruneContainersInSpec(spec map[string]any) {
	pruneContainerList(spec, "containers")
	pruneContainerList(spec, "initContainers")
}

// pruneContainerList strips noise from containers at Detail level.
// Keeps full spec (command, args, probes, etc.) — only strips terminationMessage* and simplifies imagePullPolicy.
func pruneContainerList(spec map[string]any, key string) {
	containers, ok := spec[key].([]any)
	if !ok {
		return
	}
	for _, c := range containers {
		container, ok := c.(map[string]any)
		if !ok {
			continue
		}
		for field := range stripContainerFields {
			delete(container, field)
		}
		// Strip imagePullPolicy unless it's "Never"
		if policy, ok := container["imagePullPolicy"].(string); ok && policy != "Never" {
			delete(container, "imagePullPolicy")
		}
		// Redact inline env values
		redactEnvValues(container)
	}
}

// pruneContainersCompact aggressively strips container fields at Compact level.
// Keeps: image, resources, env names (not values), ports.
func pruneContainersCompact(spec map[string]any, key string) {
	containers, ok := spec[key].([]any)
	if !ok {
		return
	}
	for _, c := range containers {
		container, ok := c.(map[string]any)
		if !ok {
			continue
		}
		for field := range stripContainerFields {
			delete(container, field)
		}
		for field := range stripContainerFieldsCompact {
			delete(container, field)
		}
		// Strip imagePullPolicy unless it's "Never"
		if policy, ok := container["imagePullPolicy"].(string); ok && policy != "Never" {
			delete(container, "imagePullPolicy")
		}
		// Simplify volumeMounts: keep names only
		simplifyVolumeMounts(container)
		// Simplify env: keep names only (strip values for token savings)
		simplifyEnvToNames(container)
	}
}

func simplifyVolumeMounts(container map[string]any) {
	mounts, ok := container["volumeMounts"].([]any)
	if !ok {
		return
	}
	names := make([]string, 0, len(mounts))
	for _, m := range mounts {
		if mount, ok := m.(map[string]any); ok {
			if name, ok := mount["name"].(string); ok {
				names = append(names, name)
			}
		}
	}
	container["volumeMounts"] = names
}

func simplifyEnvToNames(container map[string]any) {
	envList, ok := container["env"].([]any)
	if !ok {
		return
	}
	names := make([]string, 0, len(envList))
	for _, e := range envList {
		if env, ok := e.(map[string]any); ok {
			if name, ok := env["name"].(string); ok {
				names = append(names, name)
			}
		}
	}
	container["env"] = names
}

func redactEnvValues(container map[string]any) {
	envList, ok := container["env"].([]any)
	if !ok {
		return
	}
	for _, e := range envList {
		if env, ok := e.(map[string]any); ok {
			if val, ok := env["value"].(string); ok {
				env["value"] = RedactSecrets(val)
			}
		}
	}
}

// pruneStatusPod strips noisy fields from pod status.
func pruneStatusPod(status map[string]any) {
	for key := range stripPodStatusFields {
		delete(status, key)
	}
}

// pruneStatusWorkload strips noisy fields from workload status (Deployment, StatefulSet, DaemonSet).
func pruneStatusWorkload(status map[string]any) {
	for key := range stripWorkloadStatusFields {
		delete(status, key)
	}
}
