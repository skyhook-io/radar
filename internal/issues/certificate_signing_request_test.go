package issues

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Waiting for an approver, or for a signer after approval, is the ordinary
// course of a CertificateSigningRequest and must not raise a cluster Issue.
func TestWaitingCertificateSigningRequestsAreNotIssues(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "certificates.k8s.io", Version: "v1", Resource: "certificatesigningrequests"}
	request := func(name string, conditions ...any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "certificates.k8s.io/v1",
			"kind":       "CertificateSigningRequest",
			"metadata":   map[string]any{"name": name},
			"spec":       map[string]any{"signerName": "kubernetes.io/kubelet-serving"},
			"status":     map[string]any{"conditions": conditions},
		}}
	}
	p := &fakeProvider{
		dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {
			request("pending"),
			request("approved", map[string]any{"type": "Approved", "status": "True", "reason": "KubectlApprove"}),
		}},
		kinds:      map[schema.GroupVersionResource]string{gvr: "CertificateSigningRequest"},
		namespaced: map[schema.GroupVersionResource]bool{gvr: false},
	}
	if got := detectGenericCRDIssues(p, Filters{}, nil, nil); len(got) != 0 {
		t.Fatalf("got %d issues, want none: %+v", len(got), got)
	}
}
