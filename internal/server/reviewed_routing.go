package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// Keep the published EndpointSlice observations distinct from a projection of
// which Ready Pods match the old and new selectors. Endpoints can lag behind
// Pod readiness, and numeric equality does not mean every port has a backend.
type routingPodSample struct {
	Count int      `json:"count"`
	Names []string `json:"names"`
}
type serviceRoutingImpact struct {
	PublishedReadyPods            routingPodSample `json:"publishedReadyPods"`
	PublishedOtherEndpointEntries int              `json:"publishedOtherEndpointEntries"`
	CurrentReadyPods              routingPodSample `json:"currentReadyPods"`
	ProposedReadyPods             routingPodSample `json:"proposedReadyPods"`
	PublishNotReadyAddresses      bool             `json:"publishNotReadyAddresses"`
	NamedTargetPorts              []string         `json:"namedTargetPorts"`
}

var errRoutingUnsupported = errors.New("this routing change cannot be reviewed")
var errRoutingIncomplete = errors.New("routing evidence is incomplete")

func validateDeploymentTemplateLabel(obj *unstructured.Unstructured, key, value string) error {
	paused, _, err := unstructured.NestedBool(obj.Object, "spec", "paused")
	if err != nil {
		return err
	}
	if paused {
		return errors.New("resume the Deployment before changing its Pod template label")
	}
	raw, ok, err := unstructured.NestedMap(obj.Object, "spec", "selector")
	if err != nil || !ok {
		return errors.New("the Deployment selector could not be read")
	}
	var spec metav1.LabelSelector
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &spec); err != nil {
		return err
	}
	selector, err := metav1.LabelSelectorAsSelector(&spec)
	if err != nil {
		return err
	}
	podLabels, _, err := unstructured.NestedStringMap(obj.Object, "spec", "template", "metadata", "labels")
	if err != nil {
		return err
	}
	if podLabels == nil {
		podLabels = map[string]string{}
	}
	podLabels[key] = value
	if !selector.Matches(labels.Set(podLabels)) {
		return errors.New("the proposed Pod template labels would no longer match the Deployment selector")
	}
	return nil
}

func previewServiceRouting(ctx context.Context, client dynamic.Interface, obj *unstructured.Unstructured, key, value string) (*serviceRoutingImpact, error) {
	var svc corev1.Service
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &svc); err != nil {
		return nil, err
	}
	if svc.Spec.Type == corev1.ServiceTypeExternalName || len(svc.Spec.Selector) == 0 {
		return nil, fmt.Errorf("%w: only Services with a nonempty Pod selector can be changed", errRoutingUnsupported)
	}
	impact := &serviceRoutingImpact{PublishNotReadyAddresses: svc.Spec.PublishNotReadyAddresses, NamedTargetPorts: []string{}}
	for _, port := range svc.Spec.Ports {
		if port.TargetPort.StrVal != "" {
			impact.NamedTargetPorts = append(impact.NamedTargetPorts, port.TargetPort.StrVal)
		}
	}
	ns := svc.Namespace
	old, err := routingReadyPods(ctx, client, ns, svc.Spec.Selector)
	if err != nil {
		return nil, err
	}
	impact.CurrentReadyPods = old
	svc.Spec.Selector[key] = value
	proposed, err := routingReadyPods(ctx, client, ns, svc.Spec.Selector)
	if err != nil {
		return nil, err
	}
	impact.ProposedReadyPods = proposed
	list, err := listRoutingEvidence(ctx, client, discoveryv1.SchemeGroupVersion.WithResource("endpointslices"), ns, labels.Set{endpointSliceServiceLabel: svc.Name})
	if err != nil {
		return nil, err
	}
	ready := map[string]string{}
	for i := range list.Items {
		var es discoveryv1.EndpointSlice
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(list.Items[i].Object, &es); err != nil {
			return nil, err
		}
		for _, endpoint := range es.Endpoints {
			ref := endpoint.TargetRef
			if ref == nil || ref.Kind != "Pod" || (ref.Namespace != "" && ref.Namespace != ns) || ref.Name == "" || (endpoint.Conditions.Ready != nil && !*endpoint.Conditions.Ready) || (endpoint.Conditions.Terminating != nil && *endpoint.Conditions.Terminating) {
				impact.PublishedOtherEndpointEntries++
				continue
			}
			identity := string(ref.UID)
			if identity == "" {
				identity = ref.Name
			}
			ready[identity] = ref.Name
		}
	}
	impact.PublishedReadyPods = sampleRoutingPods(ready)
	return impact, nil
}

func routingReadyPods(ctx context.Context, client dynamic.Interface, namespace string, selector labels.Set) (routingPodSample, error) {
	list, err := listRoutingEvidence(ctx, client, corev1.SchemeGroupVersion.WithResource("pods"), namespace, selector)
	if err != nil {
		return routingPodSample{}, err
	}
	ready := map[string]string{}
	for i := range list.Items {
		var pod corev1.Pod
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(list.Items[i].Object, &pod); err != nil {
			return routingPodSample{}, err
		}
		if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning || pod.Status.PodIP == "" {
			continue
		}
		if slices.ContainsFunc(pod.Status.Conditions, func(c corev1.PodCondition) bool { return c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue }) {
			ready[pod.Name] = pod.Name
		}
	}
	return sampleRoutingPods(ready), nil
}

func listRoutingEvidence(ctx context.Context, client dynamic.Interface, gvr schema.GroupVersionResource, ns string, selector labels.Set) (*unstructured.UnstructuredList, error) {
	sel, err := labels.ValidatedSelectorFromSet(selector)
	if err != nil {
		return nil, err
	}
	list, err := client.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{LabelSelector: sel.String(), Limit: serviceEndpointSliceLimit})
	if err != nil {
		return nil, fmt.Errorf("could not list %s for the routing preview: %w", gvr.Resource, err)
	}
	if list.GetContinue() != "" || len(list.Items) > serviceEndpointSliceLimit {
		return nil, fmt.Errorf("%w: more than %d matching %s", errRoutingIncomplete, serviceEndpointSliceLimit, gvr.Resource)
	}
	return list, nil
}

func sampleRoutingPods(pods map[string]string) routingPodSample {
	names := make([]string, 0, len(pods))
	for _, name := range pods {
		names = append(names, name)
	}
	sort.Strings(names)
	return routingPodSample{Count: len(names), Names: names[:min(len(names), 10)]}
}

func (s *Server) writeRoutingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errRoutingUnsupported):
		s.writeErrorCode(w, http.StatusConflict, "routing_action_not_applicable", err.Error())
	case errors.Is(err, errRoutingIncomplete):
		s.writeErrorCode(w, http.StatusConflict, "routing_evidence_incomplete", err.Error())
	case apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err):
		s.writeErrorCode(w, http.StatusForbidden, "routing_evidence_denied", err.Error())
	default:
		s.writeErrorCode(w, http.StatusBadGateway, "routing_evidence_unavailable", err.Error())
	}
}
