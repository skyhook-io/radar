package configrefs

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"reflect"
	"testing"
)

func TestAdmissionWebhookServices(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "admissionregistration.k8s.io/v1", "kind": "ValidatingWebhookConfiguration", "metadata": map[string]any{"name": "validation"}, "webhooks": []any{
		map[string]any{"name": "one.example.com", "clientConfig": map[string]any{"service": map[string]any{"namespace": "backend", "name": "admission"}, "caBundle": "not-copied"}},
		map[string]any{"name": "two.example.com", "failurePolicy": "Ignore", "clientConfig": map[string]any{"service": map[string]any{"namespace": "backend", "name": "admission"}}},
		map[string]any{"name": "url.example.com", "clientConfig": map[string]any{"url": "https://external.example.com"}},
		map[string]any{"clientConfig": map[string]any{"service": map[string]any{"name": "no-namespace"}}}, "invalid",
	}}}
	before := obj.DeepCopy()
	refs := AdmissionWebhookServices(obj)
	if len(refs) != 2 || refs[0].Service != (Ref{Kind: "Service", Namespace: "backend", Name: "admission"}) || refs[0].FailurePolicy != "Fail" || refs[1].FailurePolicy != "Ignore" || refs[0].WebhookName != "one.example.com" || refs[1].Path != "webhooks[1].clientConfig.service.name" {
		t.Fatalf("declarations: %+v", refs)
	}
	if !reflect.DeepEqual(obj, before) {
		t.Fatal("mutated input")
	}
	obj.SetKind("MutatingWebhookConfiguration")
	if len(AdmissionWebhookServices(obj)) != 2 {
		t.Fatal("mutating configuration omitted")
	}
	for _, mutate := range []func(*unstructured.Unstructured){func(o *unstructured.Unstructured) { o.SetAPIVersion("custom.example/v1") }, func(o *unstructured.Unstructured) { o.SetKind("CustomResourceDefinition") }, func(o *unstructured.Unstructured) { o.SetNamespace("not-cluster-scoped") }} {
		copy := before.DeepCopy()
		mutate(copy)
		if len(AdmissionWebhookServices(copy)) != 0 {
			t.Fatal("unrelated object produced admission refs")
		}
	}
	if len(AdmissionWebhookServices(nil)) != 0 {
		t.Fatal("nil produced refs")
	}
}
