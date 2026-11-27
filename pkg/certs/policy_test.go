package certs

import (
	"github.com/skyhook-io/radar/pkg/health"
	"testing"
	"time"
)

func TestAggregateMissingStartUsesOnlyMatchingAvailableSecret(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	start, expiry := now.Add(-15*time.Minute), now.Add(45*time.Minute)
	input := Input{Name: "web", Namespace: "app", SecretName: "web-tls", NotAfter: &expiry, Source: SourceCertManager}
	secret := Input{Name: "web-tls", Namespace: "app", NotBefore: &start, NotAfter: &expiry, Source: SourceTLSSecret}
	sources := Sources{Now: now, ExpiryPolicy: health.CertificateExpiryPolicy{WarningPercent: 20}, CertManager: []Input{input}, TLSSecrets: []Input{secret}}
	got := Aggregate(sources)
	if len(got) != 1 || got[0].Health != HealthHealthy || got[0].ExpiryPolicy.WarningSeconds != 720 {
		t.Fatalf("matching Secret validity not used: %+v", got)
	}
	otherExpiry := expiry.Add(time.Hour)
	sources.TLSSecrets[0].NotAfter = &otherExpiry
	if got = Aggregate(sources); got[0].ExpiryPolicy.WarningSeconds != 2592000 {
		t.Fatal("different certificate's start was used")
	}
	sources.TLSSecrets = nil
	if got = Aggregate(sources); got[0].ExpiryPolicy.WarningSeconds != 2592000 {
		t.Fatal("missing or unreadable Secret weakened fallback")
	}
}

func TestAggregateCustomPolicyAndAuthoritativeCertificate(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	p := health.CertificateExpiryPolicy{WarningPercent: 20}
	fresh, expired := now.Add(45*time.Minute), now.Add(-time.Hour)
	start := now.Add(-15 * time.Minute)
	sources := Sources{Now: now, ExpiryPolicy: p,
		CertManager: []Input{{Name: "managed", Namespace: "app", SecretName: "managed-tls", NotAfter: &expired, Source: SourceCertManager}, {Name: "pending", Namespace: "app", SecretName: "pending-tls", Source: SourceCertManager}},
		TLSSecrets:  []Input{{Name: "managed-tls", Namespace: "app", NotAfter: &fresh, NotBefore: &start, Source: SourceTLSSecret}, {Name: "plain", Namespace: "app", NotAfter: &fresh, NotBefore: &start, Source: SourceTLSSecret}},
	}
	got := Aggregate(sources)
	if len(got) != 3 || find(got, "app", "managed-tls") != nil {
		t.Fatalf("dedup=%+v", got)
	}
	if find(got, "app", "managed").Health != HealthUnhealthy {
		t.Fatal("stale CR expiry lost its existing authority")
	}
	if find(got, "app", "pending").Health != HealthUnknown {
		t.Fatal("unknown expiry claimed healthy")
	}
	if find(got, "app", "plain").Health != HealthHealthy {
		t.Fatal("raw short-lived certificate ignored custom window")
	}
	sources.ExpiryPolicy = health.CertificateExpiryPolicy{WarningPercent: 100}
	if find(Aggregate(sources), "app", "plain").Health != HealthDegraded {
		t.Fatal("per-call policy leaked into default behavior")
	}
}
