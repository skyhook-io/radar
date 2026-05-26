package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/skyhook-io/radar/pkg/certs"
)

// selfSignedPEM returns a PEM-encoded self-signed cert for use as a secret's
// tls.crt, so secretToCertInput's parse path has a real leaf to read.
func selfSignedPEM(t *testing.T, cn string, dns []string, notAfter time.Time) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
		DNSNames:     dns,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

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

func TestSecretToCertInput_SkipsNonServingSecrets(t *testing.T) {
	valid := selfSignedPEM(t, "svc.example", []string{"svc.example"}, time.Now().Add(40*24*time.Hour))
	cases := []struct {
		name   string
		secret *corev1.Secret
		wantOK bool
	}{
		{
			name:   "non-TLS type skipped",
			secret: &corev1.Secret{Type: corev1.SecretTypeOpaque, Data: map[string][]byte{"tls.crt": valid}},
			wantOK: false,
		},
		{
			name: "sealed-secrets keypair skipped",
			secret: &corev1.Secret{
				Type: corev1.SecretTypeTLS,
				Data: map[string][]byte{"tls.crt": valid},
			},
			wantOK: false, // label set below
		},
		{
			name:   "missing tls.crt skipped",
			secret: &corev1.Secret{Type: corev1.SecretTypeTLS, Data: map[string][]byte{}},
			wantOK: false,
		},
		{
			name:   "unparseable tls.crt skipped",
			secret: &corev1.Secret{Type: corev1.SecretTypeTLS, Data: map[string][]byte{"tls.crt": []byte("not a pem")}},
			wantOK: false,
		},
		{
			name: "valid TLS serving cert accepted",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "web-tls", Namespace: "prod"},
				Type:       corev1.SecretTypeTLS,
				Data:       map[string][]byte{"tls.crt": valid},
			},
			wantOK: true,
		},
	}
	// Attach the sealed-secrets label to the dedicated case.
	cases[1].secret.Labels = map[string]string{sealedSecretsKeyLabel: "active"}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in, ok := secretToCertInput(tc.secret)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if tc.wantOK {
				if in.Name != "web-tls" || in.Namespace != "prod" {
					t.Errorf("name/namespace = %q/%q, want web-tls/prod", in.Name, in.Namespace)
				}
				if in.Source != certs.SourceTLSSecret {
					t.Errorf("source = %q, want tls-secret", in.Source)
				}
				if in.NotAfter == nil {
					t.Error("NotAfter = nil, want parsed expiry from the leaf cert")
				}
			}
		})
	}
}
