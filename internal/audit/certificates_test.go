package audit

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"github.com/skyhook-io/radar/internal/k8s"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"math/big"
	"slices"
	"testing"
	"time"
)

func TestTLSExpiryHonorsSecretReadScope(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	data := map[string][]byte{"tls.crt": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
	a := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "tls", Namespace: "allowed"}, Type: corev1.SecretTypeTLS, Data: data}
	b := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "tls", Namespace: "denied"}, Type: corev1.SecretTypeTLS, Data: data}
	if err := k8s.InitTestResourceCache(fake.NewClientset(a, b)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	for _, tc := range []struct {
		name     string
		secretNS []string
		want     int
		missing  bool
	}{
		{"partial", []string{"allowed"}, 1, true},
		{"denied", []string{}, 0, true},
		{"allowed", []string{"allowed", "denied"}, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := RunFromCache(k8s.GetResourceCache(), []string{"allowed", "denied"}, &RunOptions{Scope: &ReadScope{Namespaces: []string{"allowed", "denied"}, SecretNamespaces: tc.secretNS}})
			count := 0
			for _, f := range r.Findings {
				if f.CheckID == "tlsCertificateExpiry" {
					count++
					if !slices.Contains(tc.secretNS, f.Namespace) {
						t.Fatalf("leaked %+v", f)
					}
				}
			}
			if count != tc.want || r.CheckCounts["tlsCertificateExpiry"].Evaluated != tc.want {
				t.Fatalf("findings=%d counts=%+v", count, r.CheckCounts)
			}
			if slices.Contains(r.MissingInputs, "secrets") != tc.missing {
				t.Fatalf("coverage=%v", r.MissingInputs)
			}
		})
	}
}
