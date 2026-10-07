package resourcecontext

import (
	"context"

	"github.com/skyhook-io/radar/pkg/configrefs"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

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
		objects, err := opts.DynamicProv.ListNamespaces(gvr, nil)
		if err != nil {
			omitted.add("dependents", OmittedCacheCold)
			continue
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
