package issues

import (
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/issuesapi"
)

func TestParseCNPGCertExpiry(t *testing.T) {
	want := time.Date(2026, 12, 28, 8, 28, 45, 0, time.UTC)
	for _, in := range []string{
		"2026-12-28 08:28:45 +0000 UTC",
		"2026-12-28 08:28:45.000000 +0000 UTC",
		"2026-12-28 08:28:45 +0000 UTC m=+7776000.000000001",
		"2026-12-28 10:28:45 +0200 EET",
	} {
		got, ok := ParseCNPGCertExpiry(in)
		if !ok || !got.Equal(want) {
			t.Errorf("ParseCNPGCertExpiry(%q) = %v, %v; want %v", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "soon", "2026-12-28T08:28:45Z"} {
		if _, ok := ParseCNPGCertExpiry(in); ok {
			t.Errorf("ParseCNPGCertExpiry(%q) parsed; want unknown", in)
		}
	}
}

func TestCNPGCertificateExpiryIssues(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) string { return now.Add(d).Format(CNPGCertExpiryLayout) }
	u := cnpgCluster(
		map[string]any{"certificates": map[string]any{"serverTLSSecret": "user-tls", "serverCASecret": "user-ca", "clientCASecret": "user-client-ca"}},
		map[string]any{"certificates": map[string]any{"expirations": map[string]any{
			"user-tls":            at(20 * 24 * time.Hour), // warning
			"user-ca":             at(3 * 24 * time.Hour),  // critical: owner must renew now
			"user-client-ca":      at(90 * 24 * time.Hour), // fine
			"pg-main-replication": at(5 * 24 * time.Hour),  // operator renews: routine
			"pg-main-server":      at(2 * time.Hour),       // operator renewal overdue
			"pg-main-ca":          at(-time.Hour),          // expired
			"garbage":             "not a time",            // unknown: no issue
		}}},
	)
	got := detectCNPGCertificateIssues(cnpgClusterGVR, "Cluster", u, now)
	bySecret := map[string]Issue{}
	for _, i := range got {
		bySecret[strings.TrimPrefix(i.Fingerprint, "CNPGCertificateExpiring/")] = i
	}
	want := map[string]issuesapi.Severity{
		"user-tls":       SeverityWarning,
		"user-ca":        SeverityCritical,
		"pg-main-server": SeverityCritical,
		"pg-main-ca":     SeverityCritical,
	}
	if len(bySecret) != len(want) {
		t.Fatalf("issues for %v; want %v", keysOf(bySecret), want)
	}
	for secret, sev := range want {
		i, ok := bySecret[secret]
		wantReason := ReasonCNPGCertificateExpiring
		if secret == "pg-main-ca" {
			wantReason = ReasonCNPGCertificateExpired
		}
		if !ok || i.Severity != sev || i.Reason != wantReason {
			t.Errorf("%s: %+v, want %s %s", secret, i, sev, wantReason)
		}
	}
	if bySecret["pg-main-ca"].Category != issuesapi.CategoryCertificateNotReady {
		t.Errorf("expired category = %q", bySecret["pg-main-ca"].Category)
	}
	if !strings.Contains(bySecret["user-tls"].Message, "its owner renews it") {
		t.Errorf("user-provided message = %q", bySecret["user-tls"].Message)
	}
	if bySecret["user-tls"].Category != issuesapi.CategoryCertificateNotReady {
		t.Errorf("category = %q", bySecret["user-tls"].Category)
	}
}

func TestCNPGDatabaseRoleNotAppliedIsReported(t *testing.T) {
	u := cnpgCluster(nil, map[string]any{"applied": false, "message": "database role is already managed by the CNPG cluster"})
	u.SetKind("DatabaseRole")
	got := detectCNPGIssues(cnpgClusterGVR, "DatabaseRole", u)
	if len(got) != 1 || got[0].Reason != "CNPGDeclarativeNotApplied" || !strings.Contains(got[0].Message, "already managed") {
		t.Fatalf("got %+v", got)
	}
}

func keysOf(m map[string]Issue) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
