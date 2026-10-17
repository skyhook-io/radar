package issues

import (
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// absenceCheckedResources are the kinds whose absence an issue detector can
// conclude: reference targets, the Pods a Service selects, and the
// NetworkPolicies that could allow traffic.
var absenceCheckedResources = []string{
	k8score.Pods,
	k8score.NetworkPolicies,
	k8score.Secrets,
	k8score.ConfigMaps,
	k8score.PersistentVolumeClaims,
	k8score.ServiceAccounts,
	k8score.Services,
	k8score.Deployments,
	k8score.StatefulSets,
	k8score.DaemonSets,
	k8score.IngressClasses,
	k8score.StorageClasses,
	k8score.Roles,
	k8score.ClusterRoles,
}

// AbsenceUncheckedSources lists the kinds whose cache cannot confirm absence
// in some namespace because Radar may list but not watch them. Findings that
// conclude such an object is missing are not reported, so an answer without
// them must not read as an all-clear.
//
// The check spans every namespace, not only the requested ones: a subject in
// one namespace can reference a target in another (Gateway routes,
// webhooks). An empty non-nil namespaces (no access) yields nothing.
func AbsenceUncheckedSources(cache *k8s.ResourceCache, namespaces []string) []issuesapi.UncheckedSource {
	if cache == nil || (namespaces != nil && len(namespaces) == 0) {
		return nil
	}
	var out []issuesapi.UncheckedSource
	for _, resource := range absenceCheckedResources {
		listOnly := cache.KindListOnly(resource, "")
		stale := cache.KindStale(resource, "")
		if !listOnly {
			continue
		}
		reason := issuesapi.UncheckedReasonListOnly
		if stale {
			reason = issuesapi.UncheckedReasonStale
		}
		out = append(out, issuesapi.UncheckedSource{Source: issuesapi.UncheckedSourceAbsence, Reason: reason, Resource: resource})
	}
	return out
}
