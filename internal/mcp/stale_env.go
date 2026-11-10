package mcp

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/internal/k8s"
)

const maxGetResourceStaleEnvItems = 5

// getResourceStaleEnv is get_resource's staleEnv field: env vars that running
// containers read from a Secret or ConfigMap key before that key's last data
// write. It is attached whenever non-empty, including with context=none,
// because an agent reading a rotated Secret otherwise can't tell its
// consumers still run the earlier value.
type getResourceStaleEnv struct {
	Total     int                       `json:"total"`
	Truncated bool                      `json:"truncated,omitempty"`
	Items     []getResourceStaleEnvItem `json:"items"`
}

type getResourceStaleEnvItem struct {
	Pod                string    `json:"pod"`
	AffectedPods       int       `json:"affectedPods,omitempty"`
	Container          string    `json:"container"`
	Env                string    `json:"env,omitempty"`
	Source             string    `json:"source"`
	Key                string    `json:"key,omitempty"`
	ReferenceKind      string    `json:"referenceKind"`
	EvidenceSource     string    `json:"evidenceSource"`
	ContainerStartedAt time.Time `json:"containerStartedAt"`
	ChangedAt          time.Time `json:"changedAt"`
	Message            string    `json:"message"`
}

// buildGetResourceStaleEnv returns the staleEnv field for a Secret or
// ConfigMap (the Pods whose env still holds an earlier value of it) or for a
// Pod or workload (which of its env vars do). Rows name Pods and sources, so
// the caller must be able to get Pods in the namespace and the source's kind;
// rows that fail either check are dropped without a count or marker.
func buildGetResourceStaleEnv(ctx context.Context, cache *k8s.ResourceCache, obj runtime.Object, namespace string) *getResourceStaleEnv {
	if cache == nil || obj == nil || namespace == "" {
		return nil
	}
	canRead := func(resource string) bool {
		return canReadInNamespace(ctx, "", resource, namespace, "get")
	}
	var facts []k8s.StaleEnvFact
	switch o := obj.(type) {
	case *corev1.Secret:
		if !canRead("pods") {
			return nil
		}
		facts = k8s.FindStaleEnvConsumersOf(ctx, cache, "Secret", namespace, o.Name)
	case *corev1.ConfigMap:
		if !canRead("pods") {
			return nil
		}
		facts = k8s.FindStaleEnvConsumersOf(ctx, cache, "ConfigMap", namespace, o.Name)
	case *corev1.Pod:
		facts = k8s.FindStaleEnvForPods(ctx, cache, []*corev1.Pod{o})
	default:
		kind := staleEnvWorkloadKind(obj)
		if kind == "" || !canRead("pods") {
			return nil
		}
		objectMeta, err := meta.Accessor(obj)
		if err != nil {
			return nil
		}
		pods, err := k8s.WorkloadPodsForUID(cache, kind, namespace, objectMeta.GetName(), obj.GetObjectKind().GroupVersionKind().Group, objectMeta.GetUID())
		if err != nil {
			return nil
		}
		facts = k8s.FindStaleEnvForPods(ctx, cache, pods)
	}

	readable := map[string]bool{}
	var items []getResourceStaleEnvItem
	for _, fact := range facts {
		resource := "secrets"
		if fact.SourceKind == "ConfigMap" {
			resource = "configmaps"
		}
		allowed, checked := readable[resource]
		if !checked {
			allowed = canRead(resource)
			readable[resource] = allowed
		}
		if !allowed {
			continue
		}
		affected := fact.AffectedPods
		if affected < 2 {
			affected = 0
		}
		items = append(items, getResourceStaleEnvItem{
			Pod:                fact.Pod,
			AffectedPods:       affected,
			Container:          fact.Container,
			Env:                fact.Env,
			Source:             fact.SourceKind + "/" + fact.SourceName,
			Key:                fact.Key,
			ReferenceKind:      fact.ReferenceKind,
			EvidenceSource:     fact.EvidenceSource,
			ContainerStartedAt: fact.ContainerStartedAt.UTC(),
			ChangedAt:          fact.ChangedAt.UTC(),
			Message:            fact.Message,
		})
	}
	if len(items) == 0 {
		return nil
	}
	out := &getResourceStaleEnv{Total: len(items), Items: items}
	if len(items) > maxGetResourceStaleEnvItems {
		out.Items = items[:maxGetResourceStaleEnvItems]
		out.Truncated = true
	}
	return out
}

// staleEnvWorkloadKind returns the workload kind whose Pods get_resource
// checks, or "" for anything else (including same-named kinds in other API
// groups).
func staleEnvWorkloadKind(obj runtime.Object) string {
	gvk := obj.GetObjectKind().GroupVersionKind()
	switch gvk.Kind {
	case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet":
		if gvk.Group == "apps" {
			return gvk.Kind
		}
	case "Job", "CronJob":
		if gvk.Group == "batch" {
			return gvk.Kind
		}
	case "Rollout":
		if gvk.Group == "argoproj.io" {
			return gvk.Kind
		}
	}
	return ""
}
