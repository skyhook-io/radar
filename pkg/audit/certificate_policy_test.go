package audit

import (
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/health"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestTLSCertificateCustomPolicyAndClock(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	p := health.CertificateExpiryPolicy{WarningPercent: 20}
	for _, tc := range []struct {
		left     time.Duration
		severity string
	}{
		{time.Hour, ""}, {12 * time.Minute, ""}, {12*time.Minute - time.Second, SeverityWarning}, {168 * time.Second, SeverityWarning}, {168*time.Second - time.Second, SeverityDanger}, {0, SeverityDanger}, {-time.Hour, SeverityDanger},
	} {
		t.Run(tc.left.String(), func(t *testing.T) {
			secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "tls", Namespace: "app"}, Type: corev1.SecretTypeTLS, Data: map[string][]byte{"tls.crt": expiryTestValidityPEM(t, now.Add(tc.left-time.Hour), now.Add(tc.left))}}
			result := RunChecks(&CheckInput{Secrets: []*corev1.Secret{secret}, Now: now, CertificateExpiryPolicy: p})
			got := ""
			for _, f := range result.Findings {
				if f.CheckID == "tlsCertificateExpiry" {
					got = f.Severity
				}
			}
			if got != tc.severity {
				t.Fatalf("severity=%s, want %s", got, tc.severity)
			}
			count := result.CheckCounts["tlsCertificateExpiry"]
			if count.Evaluated != 1 || (count.Passed == 1) != (tc.severity == "") {
				t.Fatalf("counts=%+v", count)
			}
		})
	}
}
