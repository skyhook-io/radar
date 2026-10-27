package issues

import (
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// factTLSCertificateConsumers links an invalid-certificate Secret issue to
// the live Ingresses that use it, so the issue also shows up on them.
const factTLSCertificateConsumers = "tls_certificate_consumers"

const maxTLSCertificateConsumerRefs = 10

func tlsCertificateConsumersContext(p k8s.Detection) *issuesapi.DiagnosticContext {
	if (p.Reason != k8s.TLSCertificateExpiredReason && p.Reason != k8s.TLSCertificateNotYetValidReason) || len(p.AffectedRefs) == 0 {
		return nil
	}
	refs := limitRefs(p.AffectedRefs, maxTLSCertificateConsumerRefs)
	// No counts or names in the text: after per-caller redaction they would
	// describe Ingresses the caller can't read.
	message := "These live Ingresses use this Secret for TLS."
	if len(refs) < len(p.AffectedRefs) {
		message += " Only the first links are shown."
	}
	return &issuesapi.DiagnosticContext{
		Role: issuesapi.DiagnosticRoleCandidate,
		Facts: []issuesapi.DiagnosticFact{{
			Type:       factTLSCertificateConsumers,
			Message:    message,
			Confidence: issuesapi.ConfidenceHigh,
			Refs:       refs,
		}},
	}
}
