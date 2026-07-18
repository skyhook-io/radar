package server

import (
	"context"
	"net/http"
	"sort"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/skyhook-io/radar/internal/k8s"
)

var upgradeSourceGVRs = []schema.GroupVersionResource{
	{Group: "", Version: "v1", Resource: "pods"},
	{Group: "", Version: "v1", Resource: "services"},
	{Group: "", Version: "v1", Resource: "endpoints"},
	{Group: "apps", Version: "v1", Resource: "deployments"},
	{Group: "apps", Version: "v1", Resource: "replicasets"},
	{Group: "apps", Version: "v1", Resource: "statefulsets"},
	{Group: "apps", Version: "v1", Resource: "daemonsets"},
	{Group: "batch", Version: "v1", Resource: "jobs"},
	{Group: "batch", Version: "v1", Resource: "cronjobs"},
	{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"},
	{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"},
}

func collectUpgradeSourceObjects(r *http.Request, namespaces []string) ([]metav1.Object, []string) {
	client := k8s.DynamicClientFromContext(r.Context())
	if client == nil {
		return nil, nil
	}
	return collectUpgradeSourceObjectsWithClient(r.Context(), client, namespaces)
}

func collectUpgradeSourceObjectsWithClient(ctx context.Context, client dynamic.Interface, namespaces []string) ([]metav1.Object, []string) {
	objects := []metav1.Object{}
	unavailable := map[string]bool{}
	successfulLists := 0
	for _, gvr := range upgradeSourceGVRs {
		if namespaces == nil {
			list, err := client.Resource(gvr).List(ctx, metav1.ListOptions{})
			if err != nil {
				unavailable[gvr.Resource] = true
				continue
			}
			successfulLists++
			for i := range list.Items {
				objects = append(objects, list.Items[i].DeepCopy())
			}
			continue
		}
		for _, namespace := range namespaces {
			list, err := client.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
			if err != nil {
				unavailable[gvr.Resource] = true
				continue
			}
			successfulLists++
			for i := range list.Items {
				objects = append(objects, list.Items[i].DeepCopy())
			}
		}
	}
	if successfulLists == 0 {
		objects = nil
	}
	unavailableKinds := make([]string, 0, len(unavailable))
	for kind := range unavailable {
		unavailableKinds = append(unavailableKinds, kind)
	}
	sort.Strings(unavailableKinds)
	return objects, unavailableKinds
}

var (
	validatingWebhookGVR = schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingwebhookconfigurations"}
	mutatingWebhookGVR   = schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "mutatingwebhookconfigurations"}
	crdGVR               = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
	endpointSliceGVR     = schema.GroupVersionResource{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}
)

func (s *Server) collectUpgradeWebhookEvidence(r *http.Request) (configs, crds []*unstructured.Unstructured, endpointSlices []*discoveryv1.EndpointSlice, services []*corev1.Service) {
	dynamicClient := k8s.DynamicClientFromContext(r.Context())
	typedClient := k8s.ClientFromContext(r.Context())
	if dynamicClient == nil || typedClient == nil {
		return nil, nil, nil, nil
	}
	configs = []*unstructured.Unstructured{}
	endpointSlices = []*discoveryv1.EndpointSlice{}
	services = []*corev1.Service{}
	for _, gvr := range []schema.GroupVersionResource{validatingWebhookGVR, mutatingWebhookGVR} {
		if !s.canRead(r, gvr.Group, gvr.Resource, "", "list") {
			return nil, nil, nil, nil
		}
		list, err := dynamicClient.Resource(gvr).List(r.Context(), metav1.ListOptions{})
		if err != nil {
			return nil, nil, nil, nil
		}
		for i := range list.Items {
			configs = append(configs, list.Items[i].DeepCopy())
		}
	}
	if s.canRead(r, crdGVR.Group, crdGVR.Resource, "", "list") {
		crds = collectUpgradeCRDs(r.Context(), dynamicClient)
	}
	referenced := webhookServiceNamespaces(configs, crds)
	for _, namespace := range referenced {
		if !s.canRead(r, "", "services", namespace, "list") || !s.canRead(r, endpointSliceGVR.Group, endpointSliceGVR.Resource, namespace, "list") {
			return nil, crds, nil, nil
		}
		svcList, err := typedClient.CoreV1().Services(namespace).List(r.Context(), metav1.ListOptions{})
		if err != nil {
			return nil, crds, nil, nil
		}
		for i := range svcList.Items {
			services = append(services, svcList.Items[i].DeepCopy())
		}
		sliceList, err := dynamicClient.Resource(endpointSliceGVR).Namespace(namespace).List(r.Context(), metav1.ListOptions{})
		if err != nil {
			return nil, crds, nil, nil
		}
		for i := range sliceList.Items {
			var slice discoveryv1.EndpointSlice
			if runtime.DefaultUnstructuredConverter.FromUnstructured(sliceList.Items[i].Object, &slice) != nil {
				return nil, crds, nil, nil
			}
			endpointSlices = append(endpointSlices, &slice)
		}
	}
	return configs, crds, endpointSlices, services
}

func collectUpgradeCRDs(ctx context.Context, client dynamic.Interface) []*unstructured.Unstructured {
	list, err := client.Resource(crdGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil
	}
	crds := make([]*unstructured.Unstructured, 0, len(list.Items))
	for i := range list.Items {
		crds = append(crds, compactUpgradeCRD(&list.Items[i]))
	}
	return crds
}

func webhookServiceNamespaces(configs, crds []*unstructured.Unstructured) []string {
	set := map[string]bool{}
	for _, config := range configs {
		webhooks, _, _ := unstructured.NestedSlice(config.Object, "webhooks")
		for _, raw := range webhooks {
			webhook, _ := raw.(map[string]any)
			ns, _, _ := unstructured.NestedString(webhook, "clientConfig", "service", "namespace")
			if ns != "" {
				set[ns] = true
			}
		}
	}
	for _, crd := range crds {
		ns, _, _ := unstructured.NestedString(crd.Object, "spec", "conversion", "webhook", "clientConfig", "service", "namespace")
		if ns != "" {
			set[ns] = true
		}
	}
	out := make([]string, 0, len(set))
	for ns := range set {
		out = append(out, ns)
	}
	sort.Strings(out)
	return out
}

func compactUpgradeCRD(crd *unstructured.Unstructured) *unstructured.Unstructured {
	compact := &unstructured.Unstructured{Object: map[string]any{"apiVersion": crd.GetAPIVersion(), "kind": crd.GetKind(), "metadata": map[string]any{"name": crd.GetName()}}}
	for _, path := range [][]string{{"spec", "versions"}, {"spec", "conversion"}, {"status", "storedVersions"}} {
		value, found, _ := unstructured.NestedFieldCopy(crd.Object, path...)
		if found {
			_ = unstructured.SetNestedField(compact.Object, value, path...)
		}
	}
	return compact
}
