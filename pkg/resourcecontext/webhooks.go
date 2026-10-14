package resourcecontext

import (
	"context"

	"github.com/skyhook-io/radar/pkg/configrefs"
	"github.com/skyhook-io/radar/pkg/k8score"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type cachedDynamicReader interface {
	ListWatchedReadOnly(schema.GroupVersionResource) ([]*unstructured.Unstructured, error)
	Observation(schema.GroupVersionResource) k8score.DynamicResourceObservation
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
	if !ok || ident.Kind != "Service" || ident.Group != "" {
		return
	}
	if opts.DynamicProv == nil {
		omitted.add("dependents", OmittedCacheCold)
		return
	}
	lookup, supported := opts.DynamicProv.(cachedDynamicReader)
	if !supported {
		omitted.add("dependents", OmittedUnavailable)
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
			omitted.add("dependents", OmittedCacheCold)
			continue
		}
		observation := lookup.Observation(gvr)
		switch observation.State {
		case k8score.DynamicObservationDenied:
			omitted.add("dependents", OmittedRBACDenied)
			continue
		case k8score.DynamicObservationUnsupported:
			omitted.add("dependents", OmittedUnavailable)
			continue
		}
		objects, err := lookup.ListWatchedReadOnly(gvr)
		if err != nil {
			switch {
			case apierrors.IsForbidden(err):
				omitted.add("dependents", OmittedRBACDenied)
			default:
				omitted.add("dependents", inventoryOmissionReason(err))
			}
			continue
		}
		if observation.State != k8score.DynamicObservationSynced {
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
