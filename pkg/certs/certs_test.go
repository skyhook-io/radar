package certs

import (
	"testing"
	"time"
)

func at(now time.Time, days int) *time.Time {
	t := now.Add(time.Duration(days) * 24 * time.Hour)
	return &t
}

func find(certs []Cert, ns, name string) *Cert {
	for i := range certs {
		if certs[i].Namespace == ns && certs[i].Name == name {
			return &certs[i]
		}
	}
	return nil
}

func TestAggregate_HealthThresholds(t *testing.T) {
	now := time.Now().UTC()
	got := Aggregate(Sources{
		Now: now,
		TLSSecrets: []Input{
			{Name: "healthy", Namespace: "a", NotAfter: at(now, 45), Source: SourceTLSSecret},
			{Name: "degraded", Namespace: "a", NotAfter: at(now, 10), Source: SourceTLSSecret},
			{Name: "soon", Namespace: "a", NotAfter: at(now, 3), Source: SourceTLSSecret},
			{Name: "expired", Namespace: "a", NotAfter: at(now, -2), Source: SourceTLSSecret},
		},
	})
	want := map[string]Health{
		"healthy":  HealthHealthy,
		"degraded": HealthDegraded,
		"soon":     HealthUnhealthy,
		"expired":  HealthUnhealthy,
	}
	for name, wantHealth := range want {
		c := find(got, "a", name)
		if c == nil {
			t.Fatalf("cert %q missing", name)
		}
		if c.Health != wantHealth {
			t.Errorf("cert %q health = %q, want %q", name, c.Health, wantHealth)
		}
	}
}

func TestAggregate_DedupsCertManagerOwnedSecret(t *testing.T) {
	now := time.Now().UTC()
	got := Aggregate(Sources{
		Now: now,
		CertManager: []Input{{
			Name: "api", Namespace: "prod", Issuer: "letsencrypt",
			Domains: []string{"api.example.com"}, NotAfter: at(now, 40),
			Source: SourceCertManager, SecretName: "api-tls",
		}},
		TLSSecrets: []Input{
			// Same physical cert as the Certificate above — must be dropped.
			{Name: "api-tls", Namespace: "prod", NotAfter: at(now, 40), Source: SourceTLSSecret},
			// An unrelated raw secret — must survive.
			{Name: "webhook", Namespace: "prod", NotAfter: at(now, 12), Source: SourceTLSSecret},
		},
	})
	if len(got) != 2 {
		t.Fatalf("got %d certs, want 2 (cert-manager api + raw webhook; api-tls deduped)", len(got))
	}
	if c := find(got, "prod", "api-tls"); c != nil {
		t.Error("api-tls TLS-secret row should have been deduped against the cert-manager Certificate")
	}
	api := find(got, "prod", "api")
	if api == nil || api.Source != SourceCertManager {
		t.Errorf("cert-manager row missing or wrong source: %+v", api)
	}
	if w := find(got, "prod", "webhook"); w == nil || w.Source != SourceTLSSecret {
		t.Errorf("unrelated raw secret row missing or wrong source: %+v", w)
	}
}

func TestAggregate_SortsMostUrgentFirstUnknownLast(t *testing.T) {
	now := time.Now().UTC()
	got := Aggregate(Sources{
		Now: now,
		CertManager: []Input{
			// No NotAfter → unknown, must sink to the bottom.
			{Name: "pending", Namespace: "a", Source: SourceCertManager},
		},
		TLSSecrets: []Input{
			{Name: "later", Namespace: "a", NotAfter: at(now, 30), Source: SourceTLSSecret},
			{Name: "sooner", Namespace: "a", NotAfter: at(now, 5), Source: SourceTLSSecret},
		},
	})
	if len(got) != 3 {
		t.Fatalf("got %d, want 3", len(got))
	}
	if got[0].Name != "sooner" || got[1].Name != "later" {
		t.Errorf("order = [%s,%s,...], want sooner,later first", got[0].Name, got[1].Name)
	}
	last := got[len(got)-1]
	if last.Name != "pending" || last.DaysLeft != nil || last.Health != HealthUnknown {
		t.Errorf("unknown-expiry cert should sort last as unknown: %+v", last)
	}
}

func TestAggregate_FormatsDomains(t *testing.T) {
	now := time.Now().UTC()
	got := Aggregate(Sources{
		Now: now,
		TLSSecrets: []Input{
			{Name: "multi", Namespace: "a", NotAfter: at(now, 40), Source: SourceTLSSecret,
				Domains: []string{"a.example", "b.example", "c.example", "d.example"}},
			{Name: "single", Namespace: "a", NotAfter: at(now, 41), Source: SourceTLSSecret,
				Domains: []string{"only.example"}},
		},
	})
	if c := find(got, "a", "multi"); c == nil || c.Domains != "a.example, b.example +2 more" {
		t.Errorf("multi-domain abbreviation = %q, want 'a.example, b.example +2 more'", c.Domains)
	}
	if c := find(got, "a", "single"); c == nil || c.Domains != "only.example" {
		t.Errorf("single domain = %q, want 'only.example'", c.Domains)
	}
}

// A cluster without cert-manager contributes zero CertManager inputs, so its
// TLS-secret certs must still come through on their own.
func TestAggregate_NoCertManagerStillReturnsSecretCerts(t *testing.T) {
	now := time.Now().UTC()
	got := Aggregate(Sources{
		Now:        now,
		TLSSecrets: []Input{{Name: "raw", Namespace: "a", NotAfter: at(now, 20), Source: SourceTLSSecret}},
	})
	if len(got) != 1 || got[0].Name != "raw" || got[0].Source != SourceTLSSecret {
		t.Fatalf("want the lone TLS-secret cert, got %+v", got)
	}
}

// A cert that expired an hour ago must floor to -1 day ("expired"), not
// truncate to 0 ("expires today"). Guards the math.Floor vs int() distinction.
func TestAggregate_JustExpiredFloorsToNegative(t *testing.T) {
	now := time.Now().UTC()
	oneHourAgo := now.Add(-time.Hour)
	got := Aggregate(Sources{
		Now:        now,
		TLSSecrets: []Input{{Name: "expired", Namespace: "a", NotAfter: &oneHourAgo, Source: SourceTLSSecret}},
	})
	if len(got) != 1 || got[0].DaysLeft == nil {
		t.Fatalf("want 1 cert with a daysLeft, got %+v", got)
	}
	if *got[0].DaysLeft != -1 {
		t.Errorf("daysLeft = %d, want -1 (floor — a just-expired cert is expired, not 'today')", *got[0].DaysLeft)
	}
	if got[0].Health != HealthUnhealthy {
		t.Errorf("health = %q, want unhealthy", got[0].Health)
	}
}

// Dedup keys on namespace+secretName, so the same secretName in two namespaces
// must NOT collapse — a prod/tls Certificate owning prod/tls must not suppress
// an unrelated staging/tls raw secret.
func TestAggregate_DedupIsNamespaceScoped(t *testing.T) {
	now := time.Now().UTC()
	got := Aggregate(Sources{
		Now: now,
		CertManager: []Input{{
			Name: "cert", Namespace: "prod", NotAfter: at(now, 40),
			Source: SourceCertManager, SecretName: "tls",
		}},
		TLSSecrets: []Input{
			{Name: "tls", Namespace: "prod", NotAfter: at(now, 40), Source: SourceTLSSecret},    // deduped (owned by prod cert)
			{Name: "tls", Namespace: "staging", NotAfter: at(now, 12), Source: SourceTLSSecret}, // survives (different ns)
		},
	})
	if find(got, "prod", "tls") != nil {
		t.Error("prod/tls secret should be deduped against the cert-manager Certificate")
	}
	if find(got, "staging", "tls") == nil {
		t.Error("staging/tls secret must survive — dedup must be namespace-scoped, not by bare secretName")
	}
}

// Health thresholds are `< 7` and `< 30`, and DaysLeft truncates Hours()/24.
// Pin the exact boundaries so a `<`→`<=` or truncation regression is caught.
func TestAggregate_HealthThresholdBoundaries(t *testing.T) {
	now := time.Now().UTC()
	got := Aggregate(Sources{
		Now: now,
		TLSSecrets: []Input{
			{Name: "d6", Namespace: "a", NotAfter: at(now, 6), Source: SourceTLSSecret},
			{Name: "d7", Namespace: "a", NotAfter: at(now, 7), Source: SourceTLSSecret},
			{Name: "d29", Namespace: "a", NotAfter: at(now, 29), Source: SourceTLSSecret},
			{Name: "d30", Namespace: "a", NotAfter: at(now, 30), Source: SourceTLSSecret},
		},
	})
	want := map[string]Health{
		"d6":  HealthUnhealthy, // 6 < 7
		"d7":  HealthDegraded,  // 7 is NOT < 7
		"d29": HealthDegraded,  // 29 < 30
		"d30": HealthHealthy,   // 30 is NOT < 30
	}
	for name, wantHealth := range want {
		c := find(got, "a", name)
		if c == nil {
			t.Fatalf("cert %q missing", name)
		}
		if c.Health != wantHealth {
			t.Errorf("cert %q health = %q, want %q", name, c.Health, wantHealth)
		}
	}
}
