package audit

import (
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/upgradereadiness"
)

func RunUpgradeReadinessFromCache(cache *k8s.ResourceCache, namespaces []string, currentVersion, targetVersion string) (*upgradereadiness.ScanResults, error) {
	if cache == nil {
		return upgradereadiness.Scan(nil, currentVersion, targetVersion)
	}

	input := collectWorkloadInput(cache, namespaces)
	return upgradereadiness.Scan(&upgradereadiness.Input{
		Pods:         input.Pods,
		Deployments:  input.Deployments,
		ReplicaSets:  listNamespaced(cache.ReplicaSets(), namespaces),
		StatefulSets: input.StatefulSets,
		DaemonSets:   input.DaemonSets,
		Jobs:         input.Jobs,
		CronJobs:     input.CronJobs,
	}, currentVersion, targetVersion)
}
