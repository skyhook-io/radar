package issues

import (
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// factTLSCertificateConsumers links an invalid-certificate Secret issue to
// the live Ingresses and Gateways that use it, so the issue also shows up on
// them.
const factTLSCertificateConsumers = "tls_certificate_consumers"

func tlsCertificateConsumersContext(p k8s.Detection) *issuesapi.DiagnosticContext {
	if (p.Reason != k8s.TLSCertificateExpiredReason && p.Reason != k8s.TLSCertificateNotYetValidReason && p.Reason != k8s.TLSCertificateExpiringReason) || len(p.AffectedRefs) == 0 {
		return nil
	}
	// No counts or names in the text: after per-caller redaction they would
	// describe resources the caller can't read.
	return &issuesapi.DiagnosticContext{
		Role:  issuesapi.DiagnosticRoleCandidate,
		Facts: []issuesapi.DiagnosticFact{affectedRefsFact(factTLSCertificateConsumers, "These live Ingresses and Gateways use this Secret for TLS.", p.AffectedRefs)},
	}
}
