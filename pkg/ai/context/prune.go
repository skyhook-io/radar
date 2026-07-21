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
	// Simplify volumeMounts; env lists are handled recursively under spec.
	simplifyVolumeMounts(container)
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

func sanitizeSpecEnvLists(m map[string]any, compact bool) {
	spec, ok := m["spec"]
	if !ok {
		return
	}
	sanitizeEnvLists(spec, compact)
}

func sanitizeEnvLists(node any, compact bool) {
	switch value := node.(type) {
	case map[string]any:
		for key, child := range value {
			if key == "env" {
				if envList, ok := child.([]any); ok && containsEnvEntry(envList) {
					value[key] = sanitizeEnvEntries(envList, compact)
					continue
				}
			}
			sanitizeEnvLists(child, compact)
		}
	case []any:
		for _, child := range value {
			sanitizeEnvLists(child, compact)
		}
	}
}

func containsEnvEntry(items []any) bool {
	for _, item := range items {
		env, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if _, ok := env["name"].(string); !ok {
			continue
		}
		_, hasValue := env["value"]
		_, hasValueFrom := env["valueFrom"]
		if hasValue || hasValueFrom || len(env) == 1 {
			return true
		}
	}
	return false
}

func sanitizeEnvEntries(envList []any, compact bool) any {
	out := make([]any, 0, len(envList))
	names := make([]string, 0, len(envList))
	allRecognized := true
	for _, item := range envList {
		env, ok := item.(map[string]any)
		if !ok {
			allRecognized = false
			if compact {
				continue
			}
			if literal, ok := item.(string); ok {
				out = append(out, RedactSecrets(literal))
			} else {
				out = append(out, item)
			}
			continue
		}
		name, hasName := env["name"].(string)
		_, hasValue := env["value"]
		_, hasValueFrom := env["valueFrom"]
		emptyLiteral := hasName && len(env) == 1
		if !hasName || (!hasValue && !hasValueFrom && !emptyLiteral) {
			allRecognized = false
			if literal, ok := env["value"].(string); ok {
				if compact {
					delete(env, "value")
				} else {
					env["value"] = RedactSecrets(literal)
				}
			}
			if compact {
				delete(env, "valueFrom")
			}
			sanitizeEnvLists(env, compact)
			if len(env) > 0 {
				out = append(out, env)
			}
			continue
		}
		if compact {
			names = append(names, name)
			out = append(out, name)
			continue
		}
		if literal, ok := env["value"]; ok {
			if IsSensitiveEnvName(name) {
				env["value"] = "[REDACTED]"
			} else if literal, ok := literal.(string); ok {
				env["value"] = RedactSecrets(literal)
			}
		}
		out = append(out, env)
	}
	if compact && allRecognized {
		return names
	}
	return out
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
