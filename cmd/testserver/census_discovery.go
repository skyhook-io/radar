package main

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// installCensusDiscovery serves the built-in kinds through discovery, as a
// real API server does. A census fixture seeds typed objects only; without a
// discovery catalog, readers that resolve kinds through discovery have nothing
// to resolve against.
func installCensusDiscovery(client *fake.Clientset) error {
	lists := map[string]*metav1.APIResourceList{}
	var order []string
	for _, b := range resourceid.Builtins {
		if b.Version == "" {
			continue
		}
		gv := b.Version
		if b.Group != "" {
			gv = b.Group + "/" + b.Version
		}
		l, ok := lists[gv]
		if !ok {
			l = &metav1.APIResourceList{GroupVersion: gv}
			lists[gv] = l
			order = append(order, gv)
		}
		l.APIResources = append(l.APIResources, metav1.APIResource{
			Name: b.Resource, Kind: b.Kind, Group: b.Group, Version: b.Version, Namespaced: b.Namespaced,
			Verbs: metav1.Verbs{"create", "delete", "deletecollection", "get", "list", "patch", "update", "watch"},
		})
	}
	client.Resources = nil
	for _, gv := range order {
		client.Resources = append(client.Resources, lists[gv])
	}
	_, err := k8s.SetTestResourceDiscovery(client.Discovery())
	return err
}
