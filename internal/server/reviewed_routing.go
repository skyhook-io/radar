package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/skyhook-io/radar/internal/k8s"
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
	Count   int      `json:"count"`
	Names   []string `json:"names"`
	members []string
	ready   map[string]string
}

// Only material Pod membership and readiness enter the token. Pod and
// EndpointSlice resourceVersions can advance without changing the reviewed impact.
func (i *serviceRoutingImpact) bindReview(token string) string {
	payload, _ := json.Marshal([][]string{{token}, i.CurrentReadyPods.members, i.ProposedReadyPods.members, i.PublishedReadyPods.members})
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

type serviceRoutingImpact struct {
	PublishedReadyPods            routingPodSample `json:"publishedReadyPods"`
	PublishedOtherEndpointEntries int              `json:"publishedOtherEndpointEntries"`
	CurrentReadyPods              routingPodSample `json:"currentReadyPods"`
	ProposedReadyPods             routingPodSample `json:"proposedReadyPods"`
	AddedReadyPods                routingPodSample `json:"addedReadyPods"`
	RemovedReadyPods              routingPodSample `json:"removedReadyPods"`
	VerifyEndpointSlices          string           `json:"verifyEndpointSlices"`
	PublishNotReadyAddresses      bool             `json:"publishNotReadyAddresses"`
	NamedTargetPorts              []string         `json:"namedTargetPorts"`
}

var errRoutingUnsupported = errors.New("this routing change cannot be reviewed")
var errRoutingIncomplete = errors.New("routing evidence is incomplete")
var errRoutingSelectorMismatch = errors.New("the proposed Pod template labels would no longer match the Deployment selector")

func validateDeploymentTemplateLabel(obj *unstructured.Unstructured, key, value string) error {
	paused, _, err := unstructured.NestedBool(obj.Object, "spec", "paused")
	if err != nil {
		return err
	}
	if paused {
		return fmt.Errorf("%w: resume the Deployment before changing its Pod template label", errRoutingUnsupported)
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
		return errRoutingSelectorMismatch
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
	const maxReviewedServicePorts = 64
	if len(svc.Spec.Ports) > maxReviewedServicePorts {
		return nil, fmt.Errorf("%w: this Service has more than %d ports", errRoutingIncomplete, maxReviewedServicePorts)
	}
	impact := &serviceRoutingImpact{PublishNotReadyAddresses: svc.Spec.PublishNotReadyAddresses, NamedTargetPorts: []string{}}
	for _, port := range svc.Spec.Ports {
		if port.TargetPort.StrVal != "" {
			impact.NamedTargetPorts = append(impact.NamedTargetPorts, port.TargetPort.StrVal)
		}
	}
	ns := svc.Namespace
	old, err := routingReadyPods(ctx, client, ns, svc.Spec.Selector, nil, svc.Spec.PublishNotReadyAddresses)
	if err != nil {
		return nil, err
	}
	impact.CurrentReadyPods = old
	svc.Spec.Selector[key] = value
	proposed, err := routingReadyPods(ctx, client, ns, svc.Spec.Selector, svc.Spec.Ports, svc.Spec.PublishNotReadyAddresses)
	if err != nil {
		return nil, err
	}
	impact.ProposedReadyPods = proposed
	impact.AddedReadyPods = changedReadyPods(proposed, old)
	impact.RemovedReadyPods = changedReadyPods(old, proposed)
	impact.VerifyEndpointSlices = fmt.Sprintf("/api/services/%s/%s/endpointslices", ns, svc.Name)
	list, err := listRoutingEvidence(ctx, client, discoveryv1.SchemeGroupVersion.WithResource("endpointslices"), ns, labels.Set{endpointSliceServiceLabel: svc.Name})
	if err != nil {
		return nil, err
	}
	ready := map[string]string{}
	var published []string
	for i := range list.Items {
		var es discoveryv1.EndpointSlice
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(list.Items[i].Object, &es); err != nil {
			return nil, err
		}
		for _, endpoint := range es.Endpoints {
			ref := endpoint.TargetRef
			isReady := (endpoint.Conditions.Ready == nil || *endpoint.Conditions.Ready) && (endpoint.Conditions.Terminating == nil || !*endpoint.Conditions.Terminating)
			target := ""
			if ref != nil {
				target = fmt.Sprintf("%s/%s/%s/%s", ref.Kind, ref.Namespace, ref.Name, ref.UID)
			}
			addresses := slices.Clone(endpoint.Addresses)
			sort.Strings(addresses)
			published = append(published, fmt.Sprintf("%s:%t:%s", target, isReady, strings.Join(addresses, ",")))
			if ref == nil || ref.Kind != "Pod" || (ref.Namespace != "" && ref.Namespace != ns) || ref.Name == "" || !isReady {
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
	impact.PublishedReadyPods = sampleRoutingPods(ready, published)
	return impact, nil
}

func routingReadyPods(ctx context.Context, client dynamic.Interface, namespace string, selector labels.Set, ports []corev1.ServicePort, publishNotReady bool) (routingPodSample, error) {
	list, err := listRoutingEvidence(ctx, client, corev1.SchemeGroupVersion.WithResource("pods"), namespace, selector)
	if err != nil {
		return routingPodSample{}, err
	}
	ready := map[string]string{}
	var members []string
	resolved := make([]bool, len(ports))
	for i := range list.Items {
		var pod corev1.Pod
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(list.Items[i].Object, &pod); err != nil {
			return routingPodSample{}, err
		}
		if pod.UID == "" {
			return routingPodSample{}, fmt.Errorf("%w: Pod %s has no UID", errRoutingIncomplete, pod.Name)
		}
		isReady := pod.DeletionTimestamp == nil && pod.Status.Phase == corev1.PodRunning && pod.Status.PodIP != "" && slices.ContainsFunc(pod.Status.Conditions, func(c corev1.PodCondition) bool { return c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue })
		if isReady || publishNotReady {
			members = append(members, fmt.Sprintf("%s/%s:%t:%s", pod.Name, pod.UID, isReady, pod.Status.PodIP))
		}
		if !isReady {
			continue
		}
		ready[string(pod.UID)] = pod.Name
		for j, port := range ports {
			if !resolved[j] && port.TargetPort.StrVal != "" && k8s.PodDeclaresNamedPort(&pod, port.TargetPort.StrVal, port.Protocol) {
				resolved[j] = true
			}
		}
	}
	if len(ready) > 0 {
		for j, port := range ports {
			if port.TargetPort.StrVal != "" && !resolved[j] {
				protocol := port.Protocol
				if protocol == "" {
					protocol = corev1.ProtocolTCP
				}
				return routingPodSample{}, fmt.Errorf("%w: no proposed Ready Pod declares named target port %q for protocol %s", errRoutingUnsupported, port.TargetPort.StrVal, protocol)
			}
		}
	}
	return sampleRoutingPods(ready, members), nil
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

func sampleRoutingPods(pods map[string]string, members []string) routingPodSample {
	names := make([]string, 0, len(pods))
	for _, name := range pods {
		names = append(names, name)
	}
	sort.Strings(names)
	sort.Strings(members)
	return routingPodSample{Count: len(names), Names: names[:min(len(names), 10)], members: members, ready: pods}
}

func changedReadyPods(from, without routingPodSample) routingPodSample {
	diff := map[string]string{}
	for uid, name := range from.ready {
		if _, ok := without.ready[uid]; !ok {
			diff[uid] = name
		}
	}
	return sampleRoutingPods(diff, nil)
}

type deploymentServiceImpact struct {
	NewServiceSelectors      []string `json:"newServiceSelectors"`
	ExistingServiceSelectors []string `json:"existingServiceSelectors"`
	token                    string
}

// Match both template label sets against live Service selectors. An approved
// label operation may add backend Pods, but cannot silently drain a Service.
func previewDeploymentServices(ctx context.Context, client dynamic.Interface, obj *unstructured.Unstructured, key, value, token string) (*deploymentServiceImpact, error) {
	if err := validateDeploymentTemplateLabel(obj, key, value); err != nil {
		return nil, err
	}
	before, _, err := unstructured.NestedStringMap(obj.Object, "spec", "template", "metadata", "labels")
	if err != nil {
		return nil, err
	}
	after := labels.Merge(labels.Set(before), labels.Set{key: value})
	services, err := listRoutingEvidence(ctx, client, corev1.SchemeGroupVersion.WithResource("services"), obj.GetNamespace(), nil)
	if err != nil {
		return nil, err
	}
	impact := &deploymentServiceImpact{NewServiceSelectors: []string{}, ExistingServiceSelectors: []string{}}
	var binding, removed []string
	for i := range services.Items {
		svc := &services.Items[i]
		sel, _, err := unstructured.NestedStringMap(svc.Object, "spec", "selector")
		if err != nil {
			return nil, err
		}
		serviceType, _, err := unstructured.NestedString(svc.Object, "spec", "type")
		if err != nil {
			return nil, err
		}
		if len(sel) == 0 || serviceType == string(corev1.ServiceTypeExternalName) {
			continue
		}
		selector := labels.SelectorFromSet(sel)
		was, is := selector.Matches(labels.Set(before)), selector.Matches(after)
		switch {
		case was && !is:
			removed = append(removed, svc.GetName())
		case !was && is:
			impact.NewServiceSelectors = append(impact.NewServiceSelectors, svc.GetName())
		case was && is:
			impact.ExistingServiceSelectors = append(impact.ExistingServiceSelectors, svc.GetName())
		default:
			continue
		}
		binding = append(binding, fmt.Sprintf("%s/%s:%s", svc.GetName(), svc.GetUID(), selector.String()))
	}
	if len(removed) > 0 {
		sort.Strings(removed)
		return nil, fmt.Errorf("%w: the Deployment rollout would remove its Pods from the following Service selectors: %s. Review those Services before changing this label", errRoutingUnsupported, strings.Join(removed[:min(len(removed), 10)], ", "))
	}
	sort.Strings(impact.NewServiceSelectors)
	sort.Strings(impact.ExistingServiceSelectors)
	sort.Strings(binding)
	payload, _ := json.Marshal([][]string{{token}, binding})
	sum := sha256.Sum256(payload)
	impact.token = hex.EncodeToString(sum[:])
	return impact, nil
}

func (s *Server) writeRoutingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errRoutingSelectorMismatch):
		s.writeErrorCode(w, http.StatusBadRequest, "routing_invalid_label", err.Error())
	case errors.Is(err, errRoutingUnsupported):
		s.writeErrorCode(w, http.StatusConflict, "routing_action_not_applicable", err.Error())
	case errors.Is(err, errRoutingIncomplete):
		s.writeErrorCode(w, http.StatusConflict, "routing_evidence_incomplete", err.Error())
	case apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err):
		s.writeErrorCode(w, http.StatusForbidden, "routing_evidence_denied", err.Error())
	default:
		s.writeErrorCode(w, http.StatusServiceUnavailable, "routing_evidence_unavailable", err.Error())
	}
}
