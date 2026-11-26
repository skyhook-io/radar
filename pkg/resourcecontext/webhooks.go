package resourcecontext

import (
	"context"
	"slices"

	"github.com/skyhook-io/radar/pkg/configrefs"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/ownership"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type cachedDynamicReader interface {
	ListWatchedReadOnly(schema.GroupVersionResource) ([]*unstructured.Unstructured, error)
	Observation(schema.GroupVersionResource) k8score.DynamicResourceObservation
}

func appendWebhookConsumers(ctx context.Context, dependents []ContextRef, ident resourceIdentity, opts Options, omitted *omittedTracker) []ContextRef {
	if opts.DynamicProv == nil {
		omitted.add("dependents", OmittedCacheCold)
		return dependents
	}
	lookup, supported := opts.DynamicProv.(cachedDynamicReader)
	if !supported {
		omitted.add("dependents", OmittedUnavailable)
		return dependents
	}
	for _, kind := range []string{"MutatingWebhookConfiguration", "ValidatingWebhookConfiguration"} {
		source := ContextRef{Group: "admissionregistration.k8s.io", Kind: kind}
		switch decideRef(ctx, opts.AccessChecker, &source) {
		case ownership.Allowed:
		case ownership.Denied:
			omitted.add("dependents", OmittedRBACDenied)
			continue
		default:
			omitted.add("dependents", OmittedUnavailable)
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
			if apierrors.IsForbidden(err) {
				omitted.add("dependents", OmittedRBACDenied)
			} else {
				omitted.add("dependents", OmittedUnavailable)
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
	slices.SortFunc(dependents, compareRefIdentity)
	return dependents
}
