package server

import (
	"encoding/json"
	"encoding/pem"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestSecretGetCertificateKeys(t *testing.T) {
	https := httptest.NewTLSServer(nil)
	t.Cleanup(https.Close)
	cert := https.Certificate()
	leaf := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	expired := sealingKeyPEM(t, time.Now().AddDate(-11, 0, 0))
	private := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("PRIVATE-SENTINEL")})
	broken := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("BROKEN-SENTINEL")})
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "bundle", Namespace: "default"},
		Type:       corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"trust.pem": append(append(append(private, leaf...), broken...), expired...),
			"ca.crt":    expired, "tls.crt": leaf, "tls.key": private, "bad.pem": broken,
			"password": []byte("PASSWORD-SENTINEL"),
		},
	}
	tls := secret.DeepCopy()
	tls.Name, tls.Type = "tls", corev1.SecretTypeTLS
	empty := secret.DeepCopy()
	empty.Name, empty.Data = "empty", map[string][]byte{"tls.key": private, "bad.pem": broken}
	badLeaf := tls.DeepCopy()
	badLeaf.Name = "bad-leaf"
	badLeaf.Data["tls.crt"] = append(broken, leaf...)
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}}
	useTestResourceCache(t, fake.NewClientset(ns, secret, tls, empty, badLeaf))

	for _, name := range []string{"bundle", "tls", "empty", "bad-leaf"} {
		t.Run(name, func(t *testing.T) {
			resp := get(t, "/api/resources/secrets/default/"+name)
			defer resp.Body.Close()
			var body struct{ CertificateInfo *SecretCertificateInfo }
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || resp.StatusCode != 200 {
				t.Fatalf("GET returned %d, decode error %v", resp.StatusCode, err)
			}
			info := body.CertificateInfo
			if name == "empty" {
				if info != nil {
					t.Fatal("displayed certificate metadata for an unreadable bundle")
				}
				return
			}
			if info == nil {
				t.Fatal("missing certificate metadata")
			}
			metadata, err := json.Marshal(info)
			if err != nil || strings.Contains(string(metadata), "SENTINEL") || strings.Contains(string(metadata), "-----BEGIN") {
				t.Fatal("unexpected raw data in certificate metadata")
			}
			chain := info.AdditionalKeys["trust.pem"]
			if len(chain) != 2 || !strings.EqualFold(chain[0].SerialNumber, cert.SerialNumber.Text(16)) || !chain[1].Expired {
				t.Fatalf("unexpected bundle order or expiry: %+v", chain)
			}
			if !slices.Equal(chain[0].Organizations, cert.Subject.Organization) || !slices.Contains(chain[0].SANs, cert.IPAddresses[0].String()) || !slices.Contains(chain[0].SANs, cert.DNSNames[0]) {
				t.Fatalf("incomplete certificate identity: %+v", chain[0])
			}
			if len(info.AdditionalKeys["ca.crt"]) != 1 {
				t.Fatal("expected the same certificate under its second key")
			}
			for _, key := range []string{"password", "tls.key", "bad.pem"} {
				if _, ok := info.AdditionalKeys[key]; ok {
					t.Errorf("unexpected metadata for %s", key)
				}
			}
			if name == "tls" {
				if len(info.Certificates) != 1 || info.AdditionalKeys["tls.crt"] != nil {
					t.Fatal("TLS serving chain must appear only in certificates")
				}
			} else if info.Certificates == nil || len(info.Certificates) != 0 || len(info.AdditionalKeys["tls.crt"]) != 1 {
				t.Fatal("expected the readable tls.crt bundle without a serving leaf")
			}
		})
	}
}
