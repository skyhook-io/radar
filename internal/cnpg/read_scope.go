package cnpg

import (
	"slices"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

type KindReadAssessment struct {
	Readiness k8score.KindReadiness
	Read      []string
	Uncovered []string
	Complete  bool
}

// AssessKindRead keeps unread cache entries from becoming absence claims.
// A nil target means all namespaces; an empty target means none. The host supplies
// an authorized target and must restrict returned names to caller-visible namespaces.
func AssessKindRead(cache *k8s.ResourceCache, resource string, requested []string) KindReadAssessment {
	assessment := KindReadAssessment{}
	if cache == nil || cache.ResourceCache == nil {
		return assessment
	}
	assessment.Readiness = cache.KindReadinessFor(resource)
	if assessment.Readiness == k8score.KindUnavailable {
		return assessment
	}
	covered := cache.KindNamespaces(resource)
	if covered == nil {
		if assessment.Readiness == k8score.KindReady {
			assessment.Read = slices.Clone(requested)
			assessment.Complete = true
		}
		return assessment
	}
	if requested == nil {
		if assessment.Readiness == k8score.KindReady {
			assessment.Read = covered
		}
		return assessment
	}
	if assessment.Readiness == k8score.KindReady {
		assessment.Read = make([]string, 0, len(requested))
	}
	watched := make(map[string]struct{}, len(covered))
	for _, namespace := range covered {
		watched[namespace] = struct{}{}
	}
	for _, namespace := range requested {
		if _, ok := watched[namespace]; ok {
			if assessment.Readiness == k8score.KindReady {
				assessment.Read = append(assessment.Read, namespace)
			}
		} else {
			assessment.Uncovered = append(assessment.Uncovered, namespace)
		}
	}
	assessment.Complete = assessment.Readiness == k8score.KindReady && len(assessment.Uncovered) == 0
	return assessment
}
