package resourcecontext

import (
	"context"

	"github.com/skyhook-io/radar/pkg/configrefs"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// WebhookConsumerLookup is an optional indexed dynamic-cache capability.
// complete distinguishes synced absence from partial informer contents.
type WebhookConsumerLookup interface {
	AdmissionWebhookConsumers(schema.GroupVersionResource, string, string) ([]*unstructured.Unstructured, bool, error)
}

func addWebhookContext(ctx context.Context, obj runtime.Object, opts Options, rc *ResourceContext, omitted *omittedTracker) {
	if webhook, ok := obj.(*unstructured.Unstructured); ok {
		refs := configrefs.AdmissionWebhookServices(webhook)
		if len(refs) > 0 {
			dependencies := append([]ContextRef{}, rc.Dependencies...)
			for _, ref := range refs {
				dependencies = append(dependencies, ContextRef{Kind: "Service", Namespace: ref.Service.Namespace, Name: ref.Service.Name})
			}
			rc.Dependencies = filterBoundedRefs(ctx, opts.AccessChecker, dependencies, "dependencies", omitted)
		}
	}
	ident, ok := identityOf(obj)
	if !ok || ident.Kind != "Service" || ident.Group != "" || opts.DynamicProv == nil {
		return
	}
	lookup, supported := opts.DynamicProv.(WebhookConsumerLookup)
	if !supported {
		omitted.add("dependents", OmittedSourceUnavailable)
		return
	}
	dependents := append([]ContextRef{}, rc.Dependents...)
	for _, kind := range []string{"MutatingWebhookConfiguration", "ValidatingWebhookConfiguration"} {
		source := ContextRef{Group: "admissionregistration.k8s.io", Kind: kind}
		if !checkRef(ctx, opts.AccessChecker, &source) {
			omitted.add("dependents", OmittedRBACDenied)
			continue
		}
		gvr, ok := opts.DynamicProv.GetGVRWithGroup(kind, source.Group)
		if !ok {
			continue
		}
		objects, complete, err := lookup.AdmissionWebhookConsumers(gvr, ident.Namespace, ident.Name)
		if err != nil {
			omitted.add("dependents", OmittedCacheCold)
			continue
		}
		if !complete {
			omitted.add("dependents", OmittedCacheCold)
		}
		for _, webhook := range objects {
			for _, ref := range configrefs.AdmissionWebhookServices(webhook) {
				if ref.Service.Namespace == ident.Namespace && ref.Service.Name == ident.Name {
					dependents = append(dependents, ContextRef{Group: source.Group, Kind: kind, Name: webhook.GetName()})
					break
				}
			}
		}
	}
	rc.Dependents = filterBoundedRefs(ctx, opts.AccessChecker, dependents, "dependents", omitted)
}
