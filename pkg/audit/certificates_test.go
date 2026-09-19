package audit

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"
)

func expiryTestPEM(t *testing.T, expiry time.Time) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: expiry.Add(-90 * 24 * time.Hour), NotAfter: expiry}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
func TestTLSCertificateExpiry(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name              string
		remaining         time.Duration
		severity, message string
	}{
		{"healthy", 90 * 24 * time.Hour, "", ""},
		{"30 days", 30 * 24 * time.Hour, "", ""},
		{"under 30", 30*24*time.Hour - time.Second, SeverityWarning, "TLS certificate expires in 29d"},
		{"7 days", 7 * 24 * time.Hour, SeverityWarning, "TLS certificate expires in 7d"},
		{"under 7", 7*24*time.Hour - time.Second, SeverityDanger, "TLS certificate expires in 6d"},
		{"less than day", time.Hour, SeverityDanger, "TLS certificate expires in 1h"},
		{"expired hour", -time.Hour, SeverityDanger, "TLS certificate expired 1h ago"},
		{"expired 2 days", -48 * time.Hour, SeverityDanger, "TLS certificate expired 2d ago"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "tls", Namespace: "test"}, Type: corev1.SecretTypeTLS, Data: map[string][]byte{"tls.crt": expiryTestPEM(t, now.Add(tc.remaining)), "tls.key": []byte("must-not-appear")}}
			tr := newEvalTracker()
			fs := checkTLSCertificateExpiry(tr, []*corev1.Secret{sec}, now)
			r := buildResults(fs, tr, nil)
			failed := 0
			if tc.severity != "" {
				failed = 1
			}
			if len(fs) != failed {
				t.Fatalf("findings=%+v", fs)
			}
			if c := r.CheckCounts["tlsCertificateExpiry"]; c.Evaluated != 1 || c.Passed != 1-failed {
				t.Fatalf("counts=%+v", c)
			}
			if failed == 1 {
				f := fs[0]
				if f.Kind != "Secret" || f.Namespace != "test" || f.Name != "tls" || f.Severity != tc.severity || !strings.HasPrefix(f.Message, tc.message+" (") {
					t.Fatalf("finding=%+v", f)
				}
			}
		})
	}
}
func TestTLSCertificateExpiryEligibility(t *testing.T) {
	now := time.Now().UTC()
	valid := expiryTestPEM(t, now.Add(time.Hour))
	for _, tc := range []struct {
		name   string
		typ    corev1.SecretType
		data   []byte
		labels map[string]string
	}{
		{"opaque", corev1.SecretTypeOpaque, valid, nil},
		{"missing", corev1.SecretTypeTLS, nil, nil},
		{"malformed", corev1.SecretTypeTLS, []byte("not PEM"), nil},
		{"sealed", corev1.SecretTypeTLS, valid, map[string]string{"sealedsecrets.bitnami.com/sealed-secrets-key": "active"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: tc.name, Namespace: "test", Labels: tc.labels}, Type: tc.typ, Data: map[string][]byte{"tls.crt": tc.data}}
			r := RunChecks(&CheckInput{Secrets: []*corev1.Secret{sec}})
			if _, ok := r.CheckCounts["tlsCertificateExpiry"]; ok {
				t.Fatalf("not evaluable: %+v", r.CheckCounts)
			}
			for _, f := range r.Findings {
				if f.CheckID == "tlsCertificateExpiry" {
					t.Fatal(f)
				}
			}
		})
	}
	t.Run("chain uses leaf only", func(t *testing.T) {
		data := append(expiryTestPEM(t, now.Add(60*24*time.Hour)), valid...)
		fs := checkTLSCertificateExpiry(newEvalTracker(), []*corev1.Secret{{Type: corev1.SecretTypeTLS, Data: map[string][]byte{"tls.crt": data}}}, now)
		if len(fs) != 0 {
			t.Fatal(fs)
		}
	})
	t.Run("nil vs empty", func(t *testing.T) {
		for _, secrets := range [][]*corev1.Secret{nil, {}} {
			r := RunChecks(&CheckInput{Secrets: secrets})
			if slices.Contains(r.MissingInputs, "secrets") != (secrets == nil) {
				t.Fatalf("missing=%v", r.MissingInputs)
			}
			if _, ok := r.CheckCounts["tlsCertificateExpiry"]; ok {
				t.Fatal("empty counted as passing")
			}
		}
	})
}
