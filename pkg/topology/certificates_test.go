package topology

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
)

// SKY-834 bug 54: the Home Cert Health card disagreed with the
// Secrets list page on how many TLS certs were expired. The two
// endpoints both flow through GetCertificateExpiry, so they SHOULD
// agree — but the dashboard wrapper used to take a single namespace
// argument and was only forwarded by the caller when the user had
// EXACTLY one namespace selected. With 2+ namespaces selected the
// dashboard collapsed to the empty-string ("all namespaces") path
// and counted certs from namespaces the Secrets page wasn't
// listing — leaving the user with an inconsistent picture.
//
// These tests pin the namespace-filter contract that both call paths
// now rely on:
//
//  1. nil/empty namespaces → all secrets visible to the provider
//     are scanned.
//  2. A specific list filters to only those namespaces and excludes
//     everything else.
//  3. Multi-namespace selection works (the original failure mode).

// genTLSSecret produces a TLS Secret with a valid PEM-encoded cert
// expiring in `daysFromNow` days. Negative values produce expired
// certs. Used to build a deterministic fixture for the table-test
// below without hitting the filesystem.
func genTLSSecret(t *testing.T, namespace, name string, daysFromNow int) *corev1.Secret {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Duration(daysFromNow) * 24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("createcert: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{"tls.crt": pemBytes},
	}
}

func TestGetCertificateExpiry_NamespaceFiltering(t *testing.T) {
	// Three TLS secrets across three namespaces, each with a
	// different expiry posture.
	prov := &mockProvider{
		secrets: []*corev1.Secret{
			genTLSSecret(t, "ns-a", "cert-a-expired", -7),  // expired 7d ago
			genTLSSecret(t, "ns-b", "cert-b-warning", 15),  // < 30d
			genTLSSecret(t, "ns-c", "cert-c-healthy", 200), // healthy
		},
	}

	cases := []struct {
		name       string
		namespaces []string
		want       []string // keys (ns/name) we expect to appear
	}{
		{
			// "all namespaces" path: dashboard.go signals this with
			// a nil slice (see MatchesNamespace contract).
			name:       "nil namespaces returns every TLS secret",
			namespaces: nil,
			want:       []string{"ns-a/cert-a-expired", "ns-b/cert-b-warning", "ns-c/cert-c-healthy"},
		},
		{
			// Important contract distinction: an empty (non-nil)
			// slice is a meaningful "no namespaces selected" state,
			// NOT a synonym for "all". Pin it so a future refactor
			// of MatchesNamespace doesn't silently flip the meaning
			// and reintroduce SKY-834-style mismatches.
			name:       "empty slice matches no namespaces",
			namespaces: []string{},
			want:       []string{},
		},
		{
			name:       "single namespace filters to that namespace only",
			namespaces: []string{"ns-a"},
			want:       []string{"ns-a/cert-a-expired"},
		},
		{
			// The exact failure mode of bug 54: 2-namespace
			// selection. Pre-fix, the dashboard wrapper would
			// collapse this to the all-namespaces path and the
			// Secrets list page would honour both → mismatched
			// counts in the UI.
			name:       "multi-namespace selection returns ONLY those namespaces",
			namespaces: []string{"ns-a", "ns-b"},
			want:       []string{"ns-a/cert-a-expired", "ns-b/cert-b-warning"},
		},
		{
			name:       "namespace with no TLS secrets returns empty",
			namespaces: []string{"ns-d"},
			want:       []string{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := GetCertificateExpiry(prov, tc.namespaces)
			if err != nil {
				t.Fatalf("GetCertificateExpiry: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d entries, want %d (%v vs %v)", len(got), len(tc.want), keys(got), tc.want)
			}
			for _, k := range tc.want {
				if _, ok := got[k]; !ok {
					t.Errorf("missing key %q in result %v", k, keys(got))
				}
			}
		})
	}
}

func TestGetCertificateExpiry_ExpiredFlag(t *testing.T) {
	// Defensive pin: the Expired flag on the entry should reflect
	// whether NotAfter is in the past. Both the dashboard and the
	// Secrets list page use this flag; a regression here would
	// reintroduce SKY-834 by a different route.
	prov := &mockProvider{
		secrets: []*corev1.Secret{
			genTLSSecret(t, "ns", "expired", -1),
			genTLSSecret(t, "ns", "valid", 10),
		},
	}
	got, err := GetCertificateExpiry(prov, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !got["ns/expired"].Expired {
		t.Errorf("expected ns/expired.Expired=true, got %+v", got["ns/expired"])
	}
	if got["ns/valid"].Expired {
		t.Errorf("expected ns/valid.Expired=false, got %+v", got["ns/valid"])
	}
}

func keys(m map[string]CertExpiry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
