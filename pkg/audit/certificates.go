package audit

import (
	"fmt"
	"time"

	"github.com/skyhook-io/radar/pkg/certs"
	"github.com/skyhook-io/radar/pkg/timeutil"
	corev1 "k8s.io/api/core/v1"
)

func checkTLSCertificateExpiry(tr *evalTracker, secrets []*corev1.Secret, now time.Time) []Finding {
	sources := certs.Sources{Now: now}
	for _, secret := range secrets {
		if input, ok := certs.FromTLSSecret(secret); ok && input.NotAfter != nil {
			sources.TLSSecrets = append(sources.TLSSecrets, input)
		}
	}
	var findings []Finding
	for _, cert := range certs.Aggregate(sources) {
		tr.record("tlsCertificateExpiry", cert.Namespace)
		if cert.Health == certs.HealthHealthy {
			continue
		}
		severity := SeverityWarning
		if cert.Health == certs.HealthUnhealthy {
			severity = SeverityDanger
		}
		expiry, _ := time.Parse(time.RFC3339, cert.Expiry)
		remaining := expiry.Sub(now)
		message := fmt.Sprintf("TLS certificate expires in %s (%s)", timeutil.FormatAgeShort(remaining), cert.Expiry)
		if remaining <= 0 {
			message = fmt.Sprintf("TLS certificate expired %s ago (%s)", timeutil.FormatAgeShort(-remaining), cert.Expiry)
		}
		findings = append(findings, Finding{Kind: "Secret", Namespace: cert.Namespace, Name: cert.Name, CheckID: "tlsCertificateExpiry", Category: CategoryReliability, Severity: severity, Message: message})
	}
	return findings
}
