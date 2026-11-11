package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
)

const csrPath = "/api/resources/certificatesigningrequests/_/node-csr-worker-1?group=certificates.k8s.io"

// seedCertificateSigningRequest serves one approved node CSR, whose returned
// certificate is unreadable, from the dynamic cache.
func seedCertificateSigningRequest(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "system:node:worker-1", Organization: []string{"system:nodes"}},
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	request := base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
	unreadable := base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not der")}))

	gvr := schema.GroupVersionResource{Group: "certificates.k8s.io", Version: "v1", Resource: "certificatesigningrequests"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "CertificateSigningRequestList"},
		&unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "certificates.k8s.io/v1",
			"kind":       "CertificateSigningRequest",
			"metadata":   map[string]any{"name": "node-csr-worker-1"},
			"spec": map[string]any{
				"request":    request,
				"signerName": "kubernetes.io/kube-apiserver-client-kubelet",
				"usages":     []any{"digital signature", "client auth"},
				"username":   "system:bootstrap:abcdef",
			},
			"status": map[string]any{
				"certificate": unreadable,
				"conditions":  []any{map[string]any{"type": "Approved", "status": "True", "reason": "AutoApproved"}},
			},
		}},
	)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{
		Group: "certificates.k8s.io", Version: "v1", Kind: "CertificateSigningRequest", Name: "certificatesigningrequests",
		Namespaced: false, Verbs: []string{"get", "list", "watch"},
	}}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := k8s.GetResourceCache().GetDynamicWithGroup(context.Background(), "certificatesigningrequests", "", "node-csr-worker-1", "certificates.k8s.io"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("dynamic informer never synced the CertificateSigningRequest")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return request
}

func TestGetCertificateSigningRequestDecodesItsPayloads(t *testing.T) {
	request := seedCertificateSigningRequest(t)

	var body map[string]any
	assertOK(t, get(t, csrPath), &body)
	if got := mustNested(t, body, "resource", "spec", "request"); got != request {
		t.Errorf("the raw request must still be served, got %v", got)
	}
	if got := mustNested(t, body, "certificateSigningRequest", "request", "status"); got != "decoded" {
		t.Errorf("request status = %v, want decoded", got)
	}
	if got := mustNested(t, body, "certificateSigningRequest", "request", "commonName"); got != "system:node:worker-1" {
		t.Errorf("request commonName = %v", got)
	}
	// Present but unreadable is reported as invalid, never as absent.
	if got := mustNested(t, body, "certificateSigningRequest", "certificate", "status"); got != "invalid" {
		t.Errorf("certificate status = %v, want invalid", got)
	}
	if problems, _ := mustNested(t, body, "certificateSigningRequest", "certificate", "problems").([]any); len(problems) != 1 {
		t.Errorf("certificate problems = %v, want one", problems)
	}

	var deployment map[string]any
	assertOK(t, get(t, "/api/resources/deployments/default/nginx"), &deployment)
	if _, ok := deployment["certificateSigningRequest"]; ok {
		t.Error("only CertificateSigningRequests carry decoded request details")
	}

	var resources []map[string]any
	assertOK(t, get(t, "/api/api-resources"), &resources)
	featured := false
	for _, resource := range resources {
		if resource["group"] == "certificates.k8s.io" && resource["kind"] == "CertificateSigningRequest" {
			featured = resource["featured"] == true
		}
	}
	if !featured {
		t.Error("CertificateSigningRequests should be listed under Other Kubernetes APIs")
	}
}

// Requests record their requester's identity, so reading one takes a grant on
// certificatesigningrequests itself; cluster-wide pod visibility is not enough.
func TestCertificateSigningRequestReadsRequireTheirOwnRBAC(t *testing.T) {
	seedCertificateSigningRequest(t)
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: nil}
	for _, verb := range []string{"get", "list"} {
		perms.SetCanI(verb, "certificates.k8s.io", "certificatesigningrequests", "", false)
	}
	env.srv.permCache.Set("broad-reader", nil, perms)

	resp := env.authGet(t, csrPath, "broad-reader", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("get without a grant: status %d, want 403", resp.StatusCode)
	}
	resp = env.authGet(t, "/api/resources/certificatesigningrequests?group=certificates.k8s.io", "broad-reader", "")
	var items []any
	if resp.StatusCode == http.StatusOK {
		json.NewDecoder(resp.Body).Decode(&items)
	}
	resp.Body.Close()
	if len(items) != 0 {
		t.Fatalf("list without a grant leaked %d requests", len(items))
	}

	for _, verb := range []string{"get", "list"} {
		perms.SetCanI(verb, "certificates.k8s.io", "certificatesigningrequests", "", true)
	}
	resp = env.authGet(t, csrPath, "broad-reader", "")
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get with a grant: status %d, want 200", resp.StatusCode)
	}
	if got := mustNested(t, body, "certificateSigningRequest", "request", "status"); got != "decoded" {
		t.Errorf("request status = %v, want decoded", got)
	}
}
