package issuecorrelation

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/meaningfulchanges"
	"github.com/skyhook-io/radar/pkg/configrefs"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// historyCoversObjects reports whether Radar holds the whole window's
// history for the correlated objects: either the store covers the window, or
// every object was created after the point where its history becomes
// complete, so nothing about it can have been evicted. Only a cached workload
// is dated, from its own creation time (a grouped issue's
// resource_created_at can be a newer member's); its referenced ConfigMaps and
// Secrets must be new too, and one missing from the cache is not known to be
// new.
func historyCoversObjects(obj any, refs []configrefs.Ref, namespace string, coverage issuesapi.HistoryCoverage) bool {
	if !coverage.Incomplete {
		return true
	}
	workload, ok := obj.(metav1.Object)
	if !ok || !workload.GetCreationTimestamp().After(coverage.CompleteSince) {
		return false
	}
	// A Rollout that takes its template from another workload changes with
	// it, and that workload's age isn't checked here.
	if _, _, separate := meaningfulchanges.RolloutTemplateSource(obj); separate {
		return false
	}
	cache := k8s.GetResourceCache()
	for _, ref := range refs {
		if cache == nil {
			return false
		}
		var created metav1.Time
		switch ref.Kind {
		case "ConfigMap":
			if cache.ConfigMaps() == nil {
				return false
			}
			cm, err := cache.ConfigMaps().ConfigMaps(namespace).Get(ref.Name)
			if err != nil {
				return false
			}
			created = cm.CreationTimestamp
		case "Secret":
			if cache.Secrets() == nil {
				return false
			}
			secret, err := cache.Secrets().Secrets(namespace).Get(ref.Name)
			if err != nil {
				return false
			}
			created = secret.CreationTimestamp
		}
		if !created.After(coverage.CompleteSince) {
			return false
		}
	}
	return true
}
