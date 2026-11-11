package context

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func certificateSigningRequest(apiVersion string, spec, status map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion,
		"kind":       "CertificateSigningRequest",
		"metadata":   map[string]any{"name": "node-csr-worker-1"},
		"spec":       spec,
		"status":     status,
	}}
}

func TestSummary_CertificateSigningRequest(t *testing.T) {
	approved := map[string]any{"type": "Approved", "status": "True", "reason": "KubectlApprove"}
	spec := map[string]any{"signerName": "kubernetes.io/kubelet-serving"}
	tests := []struct {
		name       string
		apiVersion string
		status     map[string]any
		wantStatus string
		wantIssue  string
	}{
		{name: "pending", status: map[string]any{}, wantStatus: "Pending approval"},
		{name: "approved, not issued", status: map[string]any{"conditions": []any{approved}}, wantStatus: "Approved, not issued"},
		{name: "issued", status: map[string]any{"certificate": "Y2VydA==", "conditions": []any{approved}}, wantStatus: "Issued"},
		{
			name: "approved, then the signer failed",
			status: map[string]any{"conditions": []any{approved, map[string]any{
				"type": "Failed", "status": "True", "reason": "SignerValidationFailure", "message": "invalid usage: server auth",
			}}},
			wantStatus: "Failed",
			wantIssue:  "SignerValidationFailure: invalid usage: server auth",
		},
		{
			name:       "denied",
			status:     map[string]any{"conditions": []any{map[string]any{"type": "Denied", "status": "True", "reason": "KubectlDeny"}}},
			wantStatus: "Denied",
			wantIssue:  "KubectlDeny",
		},
		{
			name:       "certificate without approval",
			status:     map[string]any{"certificate": "Y2VydA=="},
			wantStatus: "Pending approval",
			wantIssue:  "certificate returned without an Approved condition",
		},
		{
			// A CRD that reuses the kind keeps the generic reading.
			name:       "foreign group",
			apiVersion: "example.com/v1",
			status:     map[string]any{"conditions": []any{approved}},
			wantStatus: "Approved",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			apiVersion := tt.apiVersion
			if apiVersion == "" {
				apiVersion = "certificates.k8s.io/v1"
			}
			got := summarizeUnstructured(certificateSigningRequest(apiVersion, spec, tt.status))
			if got.Status != tt.wantStatus || got.Issue != tt.wantIssue {
				t.Fatalf("status=%q issue=%q; want status=%q issue=%q", got.Status, got.Issue, tt.wantStatus, tt.wantIssue)
			}
			if tt.apiVersion == "" && got.Type != "kubernetes.io/kubelet-serving" {
				t.Errorf("type = %q, want the signer name", got.Type)
			}
		})
	}
}

func TestMinify_CertificateSigningRequestDecodesPayloads(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: "system:node:worker-1", Organization: []string{"system:nodes"}},
		DNSNames: []string{"worker-1.example.internal"},
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	request := base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
	unreadable := base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not der")}))
	obj := certificateSigningRequest("certificates.k8s.io/v1",
		map[string]any{"request": request, "signerName": "kubernetes.io/kubelet-serving", "username": "system:node:worker-1"},
		map[string]any{"certificate": unreadable, "conditions": []any{map[string]any{"type": "Approved", "status": "True"}}},
	)

	for _, level := range []VerbosityLevel{LevelDetail, LevelCompact} {
		got, ok := MinifyUnstructured(obj, level).(map[string]any)
		if !ok {
			t.Fatalf("level %d: not a map", level)
		}
		raw, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), request) || strings.Contains(string(raw), unreadable) {
			t.Fatalf("level %d still carries base64 payloads: %s", level, raw)
		}
		decoded, _, _ := unstructured.NestedMap(got, "spec", "request")
		if decoded["status"] != "decoded" || decoded["commonName"] != "system:node:worker-1" {
			t.Errorf("level %d request = %v", level, decoded)
		}
		certificate, _, _ := unstructured.NestedMap(got, "status", "certificate")
		if certificate["status"] != "invalid" {
			t.Errorf("level %d certificate = %v, want invalid rather than absent", level, certificate)
		}
		if signer, _, _ := unstructured.NestedString(got, "spec", "signerName"); signer != "kubernetes.io/kubelet-serving" {
			t.Errorf("level %d dropped the signer: %q", level, signer)
		}
	}
	if original, _, _ := unstructured.NestedString(obj.Object, "spec", "request"); original != request {
		t.Fatal("minifying mutated the source object")
	}
}
