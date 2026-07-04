package context

import (
	"sort"

	"github.com/skyhook-io/radar/pkg/prune"
)

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
	"tolerations":        true,
	"dnsPolicy":          true,
	"schedulerName":      true,
	"priority":           true,
	"priorityClassName":  true,
	"preemptionPolicy":   true,
	"nodeName":           true,
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
	"volumes":                   true,
	"serviceAccountName":        true,
	"serviceAccount":            true,
	"securityContext":           true,
	"hostNetwork":               true,
	"hostPID":                   true,
	"hostIPC":                   true,
	"affinity":                  true,
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
	"observedGeneration":     true,
	"collisionCount":         true,
	"updatedNumberScheduled": true,
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

// pruneContainerList applies the CONDITIONAL per-container pruning at Detail
// level; unconditional key drops (stripContainerFields) ride the shared
// profiles' ElementDrops.
func pruneContainerConditional(container map[string]any) {
	// Strip imagePullPolicy unless it's "Never"
	if policy, ok := container["imagePullPolicy"].(string); ok && policy != "Never" {
		delete(container, "imagePullPolicy")
	}
	// Redact inline env values
	redactEnvValues(container)
}

// pruneContainersCompact applies the CONDITIONAL per-container pruning at
// Compact level; unconditional key drops (stripContainerFields +
// stripContainerFieldsCompact) ride the shared profiles' ElementDrops.
// Keeps: image, resources, env names (not values), ports.
func pruneContainerConditionalCompact(container map[string]any) {
	// Strip imagePullPolicy unless it's "Never"
	if policy, ok := container["imagePullPolicy"].(string); ok && policy != "Never" {
		delete(container, "imagePullPolicy")
	}
	// Simplify volumeMounts + env: keep names only (strip values for tokens)
	simplifyVolumeMounts(container)
	simplifyEnvToNames(container)
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

// The flat drop tables above are POLICY; the tree surgery executing them is
// pkg/prune (shared with the resources API's include=summary profiles —
// internal/server/resource_summary.go). Conditional logic (annotation
// filtering, imagePullPolicy, env simplification/redaction) stays in this
// package; unconditional key deletion — including per-container-element
// drops — routes through the shared mechanism.

// containerElementPaths covers both direct pod specs (Pod) and workload
// template specs (Deployment/StatefulSet/DaemonSet/ReplicaSet/Job).
var containerElementPaths = [][]string{
	{"spec", "containers"},
	{"spec", "initContainers"},
	{"spec", "template", "spec", "containers"},
	{"spec", "template", "spec", "initContainers"},
}

// forEachContainer runs fn over every container map at each known container
// location (containerElementPaths — the SAME source the declarative
// ElementDrops derive from, so the conditional walk can't drift from the
// key-drop paths). Missing/non-slice locations are skipped.
func forEachContainer(m map[string]any, fn func(container map[string]any)) {
	for _, path := range containerElementPaths {
		cur := m
		ok := true
		for _, seg := range path[:len(path)-1] {
			cur, ok = cur[seg].(map[string]any)
			if !ok {
				break
			}
		}
		if !ok {
			continue
		}
		list, ok := cur[path[len(path)-1]].([]any)
		if !ok {
			continue
		}
		for _, c := range list {
			if container, ok := c.(map[string]any); ok {
				fn(container)
			}
		}
	}
}

func containerElementDrops(table map[string]bool) []prune.ElementDrop {
	keys := make([]string, 0, len(table))
	for key := range table {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	drops := make([]prune.ElementDrop, 0, len(containerElementPaths))
	for _, path := range containerElementPaths {
		drops = append(drops, prune.ElementDrop{Path: path, Keys: keys})
	}
	return drops
}

var (
	detailBaseProfile = func() prune.Profile {
		p := prune.FromDropTables(map[string]map[string]bool{
			"metadata":           stripMetadataKeys,
			"spec":               stripPodSpecFields,
			"spec.template.spec": stripPodSpecFields,
		})
		p.ElementDrops = containerElementDrops(stripContainerFields)
		return p
	}()
	compactExtraProfile = func() prune.Profile {
		p := prune.FromDropTables(map[string]map[string]bool{
			"spec":               stripPodSpecFieldsCompact,
			"spec.template.spec": stripPodSpecFieldsCompact,
		})
		p.ElementDrops = containerElementDrops(stripContainerFieldsCompact)
		return p
	}()
	statusProfileByKind = map[string]prune.Profile{
		"pod":         prune.FromDropTables(map[string]map[string]bool{"status": stripPodStatusFields}),
		"deployment":  prune.FromDropTables(map[string]map[string]bool{"status": stripWorkloadStatusFields}),
		"statefulset": prune.FromDropTables(map[string]map[string]bool{"status": stripWorkloadStatusFields}),
		"daemonset":   prune.FromDropTables(map[string]map[string]bool{"status": stripWorkloadStatusFields}),
		"replicaset":  prune.FromDropTables(map[string]map[string]bool{"status": stripWorkloadStatusFields}),
	}
)
