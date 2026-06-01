package resourcecontextrefs

import (
	"github.com/skyhook-io/radar/internal/k8s"
	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

func AppReferencesFromEnvServiceChecks(checks []k8s.EnvServiceRefCheck) *resourcecontext.AppReferences {
	if len(checks) == 0 {
		return nil
	}
	out := make([]resourcecontext.ServiceEnvReference, 0, len(checks))
	for _, check := range checks {
		out = append(out, resourcecontext.ServiceEnvReference{
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
	return &resourcecontext.AppReferences{ServiceEnv: out}
}
