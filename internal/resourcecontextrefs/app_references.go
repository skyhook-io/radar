package resourcecontextrefs

import (
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
	if len(staleChecks) > maxStaleSecretEnvContextReferences {
		staleChecks = staleChecks[:maxStaleSecretEnvContextReferences]
	}
	for _, check := range staleChecks {
		out.StaleSecretEnv = append(out.StaleSecretEnv, resourcecontext.StaleSecretEnvReference{
			Container:           check.Container,
			Env:                 check.EnvName,
			Source:              check.Source,
			Prefix:              check.Prefix,
			Secret:              resourcecontext.ContextRef{Kind: "Secret", Namespace: check.Namespace, Name: check.SecretName},
			Key:                 check.Key,
			ContainerStartedAt:  check.ContainerStartedAt,
			SecretDataChangedAt: check.SecretDataChangedAt,
			Message:             aicontext.RedactSecrets(check.Message),
		})
	}
	return out
}
