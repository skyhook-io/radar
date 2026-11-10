package search

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestSearchEventText(t *testing.T) {
	p := &fakeProvider{typed: map[string][]runtime.Object{"events": {&corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Name: "opaque-event", Namespace: "default"},
		Reason:     "CertificateExpirationWarning", Message: "restart rke2 to renew", Type: corev1.EventTypeWarning,
		InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: "control-plane-a"},
		Related:        &corev1.ObjectReference{Kind: "Node", Name: "related-node"},
	}}}}
	for query, path := range map[string]string{
		"CertificateExpirationWarning": "reason", "renew": "message",
		"control-plane-a": "involvedObject.name", "related-node": "related.name",
	} {
		result, err := Search(context.Background(), p, Parse("kind:Event "+query), Options{Include: IncludeNone})
		if err != nil {
			t.Fatal(err)
		}
		if result.Total != 1 || len(result.Hits[0].Snippets) == 0 || result.Hits[0].Snippets[0].Path != path {
			t.Fatalf("%q: want one Event matching %s, got %+v", query, path, result)
		}
		result, err = Search(context.Background(), p, Parse(query), Options{Include: IncludeNone})
		if err != nil || result.Total != 0 {
			t.Fatalf("%q: default search must still skip Events: result=%+v err=%v", query, result, err)
		}
	}
}

func TestSearchEventCRDKeepsOrdinaryContent(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "events"}
	p := &fakeProvider{
		dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{
			gvr: {{Object: map[string]any{
				"metadata": map[string]any{"name": "opaque-event", "namespace": "default"},
				"reason":   "CertificateExpirationWarning",
				"spec":     map[string]any{"message": "custom-content"},
			}}},
		},
		kinds:      map[schema.GroupVersionResource]string{gvr: "Event"},
		namespaced: map[schema.GroupVersionResource]bool{gvr: true},
	}
	result, err := Search(context.Background(), p, Parse("kind:Event CertificateExpirationWarning"), Options{Include: IncludeNone})
	if err != nil || result.Total != 0 {
		t.Fatalf("unrelated Event CRD acquired native fields: result=%+v err=%v", result, err)
	}
	result, err = Search(context.Background(), p, Parse("custom-content"), Options{Include: IncludeNone})
	if err != nil || result.Total != 1 {
		t.Fatalf("ordinary Event CRD content lost: result=%+v err=%v", result, err)
	}
}
