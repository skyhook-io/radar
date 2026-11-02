package configrefs

import (
	"strings"

	"github.com/skyhook-io/radar/pkg/resourceid"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// WebhookServiceReference is a declared in-cluster admission backend. URL
// targets and CRD conversion webhooks are separate contracts.
type WebhookServiceReference struct {
	Service       Ref
	WebhookName   string
	FailurePolicy string
}

func AdmissionWebhookServices(obj *unstructured.Unstructured) []WebhookServiceReference {
	if obj == nil || resourceid.GroupFromAPIVersion(obj.GetAPIVersion()) != "admissionregistration.k8s.io" || obj.GetNamespace() != "" {
		return nil
	}
	switch obj.GetKind() {
	case "MutatingWebhookConfiguration", "ValidatingWebhookConfiguration":
	default:
		return nil
	}
	rawWebhooks, _, _ := unstructured.NestedFieldNoCopy(obj.Object, "webhooks")
	webhooks, _ := rawWebhooks.([]any)
	var refs []WebhookServiceReference
	for _, raw := range webhooks {
		webhook, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		namespace, _, _ := unstructured.NestedString(webhook, "clientConfig", "service", "namespace")
		name, _, _ := unstructured.NestedString(webhook, "clientConfig", "service", "name")
		if namespace == "" || name == "" {
			continue
		}
		webhookName, _, _ := unstructured.NestedString(webhook, "name")
		policy, _, _ := unstructured.NestedString(webhook, "failurePolicy")
		if strings.EqualFold(policy, "Ignore") {
			policy = "Ignore"
		} else {
			policy = "Fail"
		}
		refs = append(refs, WebhookServiceReference{Service: Ref{Kind: "Service", Namespace: namespace, Name: name}, WebhookName: webhookName, FailurePolicy: policy})
	}
	return refs
}
