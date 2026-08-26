package upgrade

import (
	"context"
	"slices"
	"sort"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/upgradereadiness"
)

type upgradeSourceResource struct {
	gvr        schema.GroupVersionResource
	namespaced bool
}

var upgradeSourceExcludedKinds = map[string]bool{
	"Event": true,
	"Lease": true,
}

func collectUpgradeSourceObjects(ctx context.Context, namespaces []string) ([]metav1.Object, []string) {
	client := k8s.DynamicClientFromContext(ctx)
	discovery := k8s.GetResourceDiscovery()
	if client == nil || discovery == nil {
		return nil, []string{"source-object discovery"}
	}
	apiResources, err := discovery.GetAPIResources()
	if err != nil {
		return nil, []string{"source-object discovery"}
	}
	resources := make([]upgradeSourceResource, 0, len(apiResources))
	for _, resource := range apiResources {
		if resource.IsCRD || upgradeSourceExcludedKinds[resource.Kind] || !slices.Contains(resource.Verbs, "list") || !upgradereadiness.IsUpgradeSourceObjectCandidate(resource.Kind, resource.Group) {
			continue
		}
		resources = append(resources, upgradeSourceResource{
			gvr:        schema.GroupVersionResource{Group: resource.Group, Version: resource.Version, Resource: resource.Name},
			namespaced: resource.Namespaced,
		})
	}
	if len(resources) == 0 {
		return nil, []string{"source-object discovery"}
	}
	objects, unavailable := collectUpgradeSourceObjectsWithClient(ctx, client, namespaces, resources)
	if discovery.HasPartialDiscovery() && !slices.Contains(unavailable, "source-object discovery") {
		unavailable = append(unavailable, "source-object discovery")
		sort.Strings(unavailable)
	}
	return objects, unavailable
}

func collectUpgradeSourceObjectsWithClient(ctx context.Context, client dynamic.Interface, namespaces []string, resources []upgradeSourceResource) ([]metav1.Object, []string) {
	objects := []metav1.Object{}
	unavailable := map[string]bool{}
	successfulLists := 0
	for _, resource := range resources {
		if namespaces == nil || !resource.namespaced {
			listed, err := listUpgradeSourceObjects(ctx, client.Resource(resource.gvr))
			objects = append(objects, listed...)
			if err != nil {
				unavailable[resource.gvr.Resource] = true
				continue
			}
			successfulLists++
			continue
		}
		for _, namespace := range namespaces {
			listed, err := listUpgradeSourceObjects(ctx, client.Resource(resource.gvr).Namespace(namespace))
			objects = append(objects, listed...)
			if err != nil {
				unavailable[resource.gvr.Resource] = true
				continue
			}
			successfulLists++
		}
	}
	if successfulLists == 0 && len(objects) == 0 {
		objects = nil
	}
	unavailableKinds := make([]string, 0, len(unavailable))
	for kind := range unavailable {
		unavailableKinds = append(unavailableKinds, kind)
	}
	sort.Strings(unavailableKinds)
	return objects, unavailableKinds
}

const upgradeSourceListPageSize int64 = 250

func listUpgradeSourceObjects(ctx context.Context, resource dynamic.ResourceInterface) ([]metav1.Object, error) {
	var objects []metav1.Object
	continueToken := ""
	for {
		list, err := resource.List(ctx, metav1.ListOptions{Limit: upgradeSourceListPageSize, Continue: continueToken})
		if err != nil {
			return objects, err
		}
		for i := range list.Items {
			if list.Items[i].GetAnnotations()["kubectl.kubernetes.io/last-applied-configuration"] != "" {
				objects = append(objects, list.Items[i].DeepCopy())
			}
		}
		continueToken = list.GetContinue()
		if continueToken == "" {
			return objects, nil
		}
	}
}

var (
	validatingWebhookGVR = schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingwebhookconfigurations"}
	mutatingWebhookGVR   = schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "mutatingwebhookconfigurations"}
	crdGVR               = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
	apiServiceGVR        = schema.GroupVersionResource{Group: "apiregistration.k8s.io", Version: "v1", Resource: "apiservices"}
	endpointSliceGVR     = schema.GroupVersionResource{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}
)

func collectUpgradeAPIServices(ctx context.Context, authz EvidenceAuthorizer) []*unstructured.Unstructured {
	client := k8s.DynamicClientFromContext(ctx)
	if client == nil || !authz.CanList(apiServiceGVR.Group, apiServiceGVR.Resource, "") {
		return nil
	}
	return collectUpgradeAPIServicesWithClient(ctx, client)
}

func collectUpgradeAPIServicesWithClient(ctx context.Context, client dynamic.Interface) []*unstructured.Unstructured {
	list, err := client.Resource(apiServiceGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil
	}
	apiServices := make([]*unstructured.Unstructured, 0, len(list.Items))
	for i := range list.Items {
		apiServices = append(apiServices, list.Items[i].DeepCopy())
	}
	return apiServices
}

func collectUpgradeWebhookEvidence(ctx context.Context, authz EvidenceAuthorizer) (configs []*unstructured.Unstructured, unavailableConfigKinds []string, crds []*unstructured.Unstructured, endpointSlices []*discoveryv1.EndpointSlice, services []*corev1.Service) {
	defer func() { sort.Strings(unavailableConfigKinds) }()
	dynamicClient := k8s.DynamicClientFromContext(ctx)
	typedClient := k8s.ClientFromContext(ctx)
	if dynamicClient == nil || typedClient == nil {
		return nil, nil, nil, nil, nil
	}
	configs = []*unstructured.Unstructured{}
	endpointSlices = []*discoveryv1.EndpointSlice{}
	services = []*corev1.Service{}
	for _, gvr := range []schema.GroupVersionResource{validatingWebhookGVR, mutatingWebhookGVR} {
		if !authz.CanList(gvr.Group, gvr.Resource, "") {
			unavailableConfigKinds = append(unavailableConfigKinds, gvr.Resource)
			continue
		}
		list, err := dynamicClient.Resource(gvr).List(ctx, metav1.ListOptions{})
		if err != nil {
			unavailableConfigKinds = append(unavailableConfigKinds, gvr.Resource)
			continue
		}
		for i := range list.Items {
			configs = append(configs, list.Items[i].DeepCopy())
		}
	}
	if authz.CanList(crdGVR.Group, crdGVR.Resource, "") {
		crds = collectUpgradeCRDs(ctx, dynamicClient)
	}
	referenced := webhookServiceNamespaces(configs, crds)
	for _, namespace := range referenced {
		if !authz.CanList("", "services", namespace) || !authz.CanList(endpointSliceGVR.Group, endpointSliceGVR.Resource, namespace) {
			return configs, unavailableConfigKinds, crds, nil, nil
		}
		svcList, err := typedClient.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return configs, unavailableConfigKinds, crds, nil, nil
		}
		for i := range svcList.Items {
			services = append(services, svcList.Items[i].DeepCopy())
		}
		sliceList, err := dynamicClient.Resource(endpointSliceGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return configs, unavailableConfigKinds, crds, nil, nil
		}
		for i := range sliceList.Items {
			var slice discoveryv1.EndpointSlice
			if runtime.DefaultUnstructuredConverter.FromUnstructured(sliceList.Items[i].Object, &slice) != nil {
				return configs, unavailableConfigKinds, crds, nil, nil
			}
			endpointSlices = append(endpointSlices, &slice)
		}
	}
	return configs, unavailableConfigKinds, crds, endpointSlices, services
}

func collectUpgradeCRDs(ctx context.Context, client dynamic.Interface) []*unstructured.Unstructured {
	crds := []*unstructured.Unstructured{}
	continueToken := ""
	for {
		list, err := client.Resource(crdGVR).List(ctx, metav1.ListOptions{Limit: upgradeSourceListPageSize, Continue: continueToken})
		if err != nil {
			return nil
		}
		for i := range list.Items {
			crds = append(crds, compactUpgradeCRD(&list.Items[i]))
		}
		continueToken = list.GetContinue()
		if continueToken == "" {
			return crds
		}
	}
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
	versions, _, _ := unstructured.NestedSlice(crd.Object, "spec", "versions")
	if versions != nil {
		compactVersions := make([]any, 0, len(versions))
		for _, raw := range versions {
			version, _ := raw.(map[string]any)
			compactVersion := map[string]any{}
			for _, field := range []string{"name", "served", "storage"} {
				if value, ok := version[field]; ok {
					compactVersion[field] = value
				}
			}
			compactVersions = append(compactVersions, compactVersion)
		}
		_ = unstructured.SetNestedSlice(compact.Object, compactVersions, "spec", "versions")
	}
	for _, path := range [][]string{{"spec", "conversion"}, {"status", "storedVersions"}} {
		value, found, _ := unstructured.NestedFieldCopy(crd.Object, path...)
		if found {
			_ = unstructured.SetNestedField(compact.Object, value, path...)
		}
	}
	return compact
}
