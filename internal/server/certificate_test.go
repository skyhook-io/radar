package server

import (
	"github.com/skyhook-io/radar/pkg/certs"
	"testing"
	"time"
)

func TestProjectCertManagerCert_Issued(t *testing.T) {
	notAfter := time.Now().Add(45 * 24 * time.Hour).UTC().Format(time.RFC3339)
	obj := map[string]any{
		"metadata": map[string]any{"name": "api-prod", "namespace": "default"},
		"spec": map[string]any{
			"secretName": "api-prod-tls",
			"issuerRef":  map[string]any{"name": "letsencrypt"},
			"dnsNames":   []any{"api.example.com", "www.example.com"},
		},
		"status": map[string]any{"notAfter": notAfter},
	}
	in := projectCertManagerCert(obj)
	if in.Name != "api-prod" || in.Namespace != "default" {
		t.Errorf("name/namespace = %q/%q, want api-prod/default", in.Name, in.Namespace)
	}
	if in.SecretName != "api-prod-tls" {
		t.Errorf("secretName = %q, want api-prod-tls", in.SecretName)
	}
	if in.Issuer != "letsencrypt" {
		t.Errorf("issuer = %q, want letsencrypt", in.Issuer)
	}
	if len(in.Domains) != 2 || in.Domains[0] != "api.example.com" {
		t.Errorf("domains = %v, want [api.example.com www.example.com]", in.Domains)
	}
	if in.Source != certs.SourceCertManager {
		t.Errorf("source = %q, want cert-manager", in.Source)
	}
	if in.NotAfter == nil {
		t.Error("NotAfter = nil, want parsed time")
	}
}

func TestProjectCertManagerCert_NotYetIssued(t *testing.T) {
	// No status block at all — a Certificate that hasn't been issued.
	obj := map[string]any{
		"metadata": map[string]any{"name": "pending", "namespace": "default"},
		"spec":     map[string]any{"secretName": "pending-tls", "issuerRef": map[string]any{"name": "letsencrypt"}},
	}
	in := projectCertManagerCert(obj)
	if in.Name != "pending" {
		t.Fatalf("name = %q, want pending", in.Name)
	}
	if in.NotAfter != nil {
		t.Errorf("NotAfter = %v, want nil (no status.notAfter)", in.NotAfter)
	}
}
