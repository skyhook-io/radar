package resourcecontextrefs

import (
	"sort"

	"github.com/skyhook-io/radar/internal/k8s"
	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

const (
	maxDuplicateEnvVarContextOccurrences = 5
	maxStaleSecretEnvContextReferences   = 5
)

func AppReferencesFromEnvChecks(serviceChecks []k8s.EnvServiceRefCheck, duplicateChecks []k8s.DuplicateEnvVarCheck, removedChecks []k8s.RemovedServiceEnvCheck, staleChecks []k8s.StaleSecretEnvCheck) *resourcecontext.AppReferences {
	if len(serviceChecks) == 0 && len(duplicateChecks) == 0 && len(removedChecks) == 0 && len(staleChecks) == 0 {
		return nil
	}
	out := &resourcecontext.AppReferences{
		ServiceEnv:        make([]resourcecontext.ServiceEnvReference, 0, len(serviceChecks)),
		DuplicateEnv:      make([]resourcecontext.DuplicateEnvVarReference, 0, len(duplicateChecks)),
		RemovedServiceEnv: make([]resourcecontext.RemovedServiceEnvReference, 0, len(removedChecks)),
	}
	for _, check := range serviceChecks {
		out.ServiceEnv = append(out.ServiceEnv, resourcecontext.ServiceEnvReference{
			Status:         check.Status,
			Container:      check.Container,
			Env:            check.EnvName,
			Value:          aicontext.RedactSecrets(check.Value),
			Service:        resourcecontext.ContextRef{Kind: "Service", Namespace: check.ServiceNamespace, Name: check.ServiceName},
			ReferencedPort: check.ReferencedPort,
			ServicePorts:   check.ServicePorts,
			Message:        aicontext.RedactSecrets(check.Message),
		})
	}
	for _, check := range duplicateChecks {
		displayed := check.Occurrences
		if len(displayed) > maxDuplicateEnvVarContextOccurrences {
			displayed = displayed[:maxDuplicateEnvVarContextOccurrences]
		}
		occurrences := make([]resourcecontext.DuplicateEnvVarOccurrence, 0, len(displayed))
		for _, occurrence := range displayed {
			occurrences = append(occurrences, resourcecontext.DuplicateEnvVarOccurrence{
				Position: occurrence.Position,
				Value:    aicontext.RedactSecrets(occurrence.Value),
			})
		}
		out.DuplicateEnv = append(out.DuplicateEnv, resourcecontext.DuplicateEnvVarReference{
			Container:         check.Container,
			Env:               check.EnvName,
			Count:             len(check.Occurrences),
			Occurrences:       occurrences,
			LastDeclaredValue: aicontext.RedactSecrets(check.LastDeclaredValue),
			Message:           aicontext.RedactSecrets(check.Message),
		})
	}
	for _, check := range removedChecks {
		out.RemovedServiceEnv = append(out.RemovedServiceEnv, resourcecontext.RemovedServiceEnvReference{
			Container:      check.Container,
			Env:            check.EnvName,
			OldValue:       aicontext.RedactSecrets(check.OldValue),
			Service:        resourcecontext.ContextRef{Kind: "Service", Namespace: check.ServiceNamespace, Name: check.ServiceName},
			ReferencedPort: check.ReferencedPort,
			RemovedAt:      check.RemovedAt,
			Message:        aicontext.RedactSecrets(check.Message),
		})
	}
	type staleGroup struct {
		check        k8s.StaleSecretEnvCheck
		pods         map[string]bool
		affectedPods int
	}
	groupsByKey := make(map[string]*staleGroup)
	for _, check := range staleChecks {
		key := check.Container + "\x00" + check.EnvName + "\x00" + check.ReferenceKind + "\x00" +
			check.EvidenceSource + "\x00" + check.Prefix + "\x00" + check.SecretName + "\x00" + check.Key
		group := groupsByKey[key]
		if group == nil {
			group = &staleGroup{check: check, pods: make(map[string]bool)}
			groupsByKey[key] = group
		}
		group.pods[check.PodName] = true
		if check.AffectedPods > group.affectedPods {
			group.affectedPods = check.AffectedPods
		}
		if check.PodName < group.check.PodName {
			group.check = check
		}
	}
	groups := make([]*staleGroup, 0, len(groupsByKey))
	for _, group := range groupsByKey {
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool {
		a, b := groups[i].check, groups[j].check
		if a.EvidenceSource != b.EvidenceSource {
			return a.EvidenceSource == "observed_change"
		}
		if a.SecretName != b.SecretName {
			return a.SecretName < b.SecretName
		}
		if a.Container != b.Container {
			return a.Container < b.Container
		}
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		if a.ReferenceKind != b.ReferenceKind {
			return a.ReferenceKind < b.ReferenceKind
		}
		if a.Prefix != b.Prefix {
			return a.Prefix < b.Prefix
		}
		return a.EnvName < b.EnvName
	})
	if len(groups) > maxStaleSecretEnvContextReferences {
		out.StaleSecretEnvTruncated = true
		groups = groups[:maxStaleSecretEnvContextReferences]
	}
	for _, group := range groups {
		check := group.check
		affectedPods := len(group.pods)
		if group.affectedPods > affectedPods {
			affectedPods = group.affectedPods
		}
		out.StaleSecretEnv = append(out.StaleSecretEnv, resourcecontext.StaleSecretEnvReference{
			Pod:                check.PodName,
			AffectedPods:       affectedPods,
			Container:          check.Container,
			Env:                check.EnvName,
			ReferenceKind:      check.ReferenceKind,
			EvidenceSource:     check.EvidenceSource,
			Prefix:             check.Prefix,
			Secret:             resourcecontext.ContextRef{Kind: "Secret", Namespace: check.Namespace, Name: check.SecretName},
			Key:                check.Key,
			ContainerStartedAt: check.ContainerStartedAt,
			SecretChangedAt:    check.SecretChangedAt,
			Message:            aicontext.RedactSecrets(check.Message),
		})
	}
	return out
}
