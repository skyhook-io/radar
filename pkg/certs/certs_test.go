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

// A cluster without cert-manager contributes zero CertManager inputs; the TLS
// secrets must still come through (this is the "certs not showing" regression).
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
