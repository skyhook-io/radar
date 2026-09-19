package certs

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"math/big"
	"testing"
	"time"
)

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

func TestFromTLSSecret_SkipsNonServingSecrets(t *testing.T) {
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
	cases[1].secret.Labels = map[string]string{"sealedsecrets.bitnami.com/sealed-secrets-key": "active"}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in, ok := FromTLSSecret(tc.secret)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if tc.wantOK {
				if in.Name != "web-tls" || in.Namespace != "prod" {
					t.Errorf("name/namespace = %q/%q, want web-tls/prod", in.Name, in.Namespace)
				}
				if in.Source != SourceTLSSecret {
					t.Errorf("source = %q, want tls-secret", in.Source)
				}
				if in.NotAfter == nil {
					t.Error("NotAfter = nil, want parsed expiry from the leaf cert")
				}
			}
		})
	}
}

func TestFromTLSSecretDoesNotPromoteIntermediate(t *testing.T) {
	chain := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("broken leaf")}), selfSignedPEM(t, "intermediate", nil, time.Now().Add(365*24*time.Hour))...)
	if _, ok := FromTLSSecret(&corev1.Secret{Type: corev1.SecretTypeTLS, Data: map[string][]byte{"tls.crt": chain}}); ok {
		t.Fatal("malformed leaf promoted intermediate")
	}
}

func TestFromTLSSecretDoesNotSkipMalformedPEM(t *testing.T) {
	data := append([]byte("-----BEGIN CERTIFICATE-----\n%%% invalid base64 %%%\n-----END CERTIFICATE-----\n"), selfSignedPEM(t, "intermediate", nil, time.Now().Add(365*24*time.Hour))...)
	if _, ok := FromTLSSecret(&corev1.Secret{Type: corev1.SecretTypeTLS, Data: map[string][]byte{"tls.crt": data}}); ok {
		t.Fatal("malformed PEM leaf promoted intermediate")
	}
}

func TestFromTLSSecretDoesNotSkipUnterminatedLeaf(t *testing.T) {
	data := append([]byte("-----BEGIN CERTIFICATE-----\nmissing end marker\n"), selfSignedPEM(t, "intermediate", nil, time.Now().Add(365*24*time.Hour))...)
	if _, ok := FromTLSSecret(&corev1.Secret{Type: corev1.SecretTypeTLS, Data: map[string][]byte{"tls.crt": data}}); ok {
		t.Fatal("unterminated leaf promoted intermediate")
	}
}
