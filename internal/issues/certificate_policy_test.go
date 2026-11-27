package issues

import (
	"github.com/skyhook-io/radar/pkg/health"
	"testing"
	"time"
)

func TestCNPGCertificatePolicyKeepsOperatorRenewalSeparate(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	u := cnpgCluster(map[string]any{"certificates": map[string]any{"serverTLSSecret": "user"}}, map[string]any{"certificates": map[string]any{"expirations": map[string]any{
		"user":     now.Add(time.Hour).Format(CNPGCertExpiryLayout),
		"operator": now.Add(2 * time.Hour).Format(CNPGCertExpiryLayout),
	}}})
	p := health.CertificateExpiryPolicy{WarningPercent: 20}
	got := detectCNPGCertificateIssues(cnpgClusterGVR, "Cluster", u, now, p)
	if len(got) != 2 {
		t.Fatalf("percentage override must retain unknown-lifetime fallback: %+v", got)
	}
	got = detectCNPGCertificateIssues(cnpgClusterGVR, "Cluster", u, now)
	if len(got) != 2 {
		t.Fatalf("default rows=%+v", got)
	}
}
