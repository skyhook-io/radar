package topology

import (
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestTrafficInternetReachesOnlyPublicKnativeServices(t *testing.T) {
	ksvcGVR := schema.GroupVersionResource{Group: "serving.knative.dev", Version: "v1", Resource: "services"}
	dmGVR := schema.GroupVersionResource{Group: "serving.knative.dev", Version: "v1beta1", Resource: "domainmappings"}
	ksvc := func(name, statusURL, addressURL string, labels map[string]string) *unstructured.Unstructured {
		obj := genericIdentityObject(ksvcGVR, "Service", "team", name)
		obj.SetLabels(labels)
		if statusURL != "" {
			obj.Object["status"] = map[string]any{"url": statusURL, "address": map[string]any{"url": addressURL}}
		}
		return obj
	}
	domainMapping := func(host, apiVersion, kind, name string) *unstructured.Unstructured {
		obj := genericIdentityObject(dmGVR, "DomainMapping", "team", host)
		obj.Object["spec"] = map[string]any{"ref": map[string]any{"apiVersion": apiVersion, "kind": kind, "name": name}}
		return obj
	}
	private := map[string]string{"networking.knative.dev/visibility": "cluster-local"}
	dynamic := &genericIdentityDynamic{
		watched: []schema.GroupVersionResource{ksvcGVR, dmGVR},
		kinds:   map[schema.GroupVersionResource]string{ksvcGVR: "Service", dmGVR: "DomainMapping"},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{
			ksvcGVR: {
				ksvc("web", "https://web.team.example.com", "http://web.team.svc.cluster.local", nil),
				// The label alone decides, even before the Route publishes a private URL.
				ksvc("billing", "https://billing.team.example.com", "http://billing.team.svc.cluster.local", private),
				// Made private through its Route: its URL is its cluster-local address.
				ksvc("jobs", "http://jobs.team.svc.cluster.local", "http://jobs.team.svc.cluster.local", nil),
				// The same on a cluster with a custom cluster domain.
				ksvc("reports", "http://reports.team.svc.corp.internal", "http://reports.team.svc.corp.internal", nil),
				// A public domain that happens to start with "svc." stays public.
				ksvc("shop", "https://shop.team.svc.example.com", "http://shop.team.svc.cluster.local", nil),
				// Nothing published yet: not known to be private.
				ksvc("pending", "", "", nil),
				// Private, but published on a custom domain by a DomainMapping.
				ksvc("api", "http://api.team.svc.cluster.local", "http://api.team.svc.cluster.local", private),
				// Private, but published through the Route it generates.
				ksvc("search", "http://search.team.svc.cluster.local", "http://search.team.svc.cluster.local", private),
				// A DomainMapping to a core Service of the same name does not publish it.
				ksvc("admin", "http://admin.team.svc.cluster.local", "http://admin.team.svc.cluster.local", private),
			},
			dmGVR: {
				domainMapping("api.example.com", "serving.knative.dev/v1", "Service", "api"),
				domainMapping("search.example.com", "serving.knative.dev/v1", "Route", "search"),
				domainMapping("admin.example.com", "v1", "Service", "admin"),
			},
		},
		listCalls: map[schema.GroupVersionResource]int{},
	}
	opts := DefaultBuildOptions()
	opts.ViewMode = ViewModeTraffic
	topo, err := NewBuilder(&mockProvider{}).WithDynamic(dynamic).Build(opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	internet := map[string]bool{}
	for _, edge := range topo.Edges {
		if edge.Source == "internet" {
			internet[edge.Target] = true
		}
	}
	for _, public := range []string{"web", "shop", "pending", "api", "search"} {
		if !internet["knativeservice/team/"+public] {
			t.Errorf("public %s lost its Internet edge: %v", public, internet)
		}
	}
	for _, private := range []string{"billing", "jobs", "reports", "admin"} {
		if internet["knativeservice/team/"+private] {
			t.Errorf("cluster-local %s drawn as reachable from the Internet", private)
		}
	}

	// The node says why it has, or lacks, an Internet edge.
	for name, want := range map[string]string{"billing": "cluster-local", "jobs": "cluster-local", "reports": "cluster-local", "admin": "cluster-local", "web": "", "shop": "", "pending": "", "api": "", "search": ""} {
		if got, _ := nodeByID(topo.Nodes, "knativeservice/team/"+name).Data["visibility"].(string); got != want {
			t.Errorf("%s visibility = %q, want %q", name, got, want)
		}
	}
	for name, want := range map[string]string{"api": "[api.example.com]", "search": "[search.example.com]", "billing": "[]"} {
		got, _ := nodeByID(topo.Nodes, "knativeservice/team/"+name).Data["mappedDomains"].([]string)
		if fmt.Sprint(got) != want {
			t.Errorf("%s mappedDomains = %v, want %s", name, got, want)
		}
	}

	if got := nodeByID(topo.Nodes, "knativeservice/team/web").Data["internalUrl"]; got != "http://web.team.svc.cluster.local" {
		t.Errorf("web internalUrl = %v, want its status.address.url", got)
	}

	// When DomainMappings can't be listed, a private-looking Service may still
	// be mapped, so it isn't labelled cluster-local.
	failing := &failingDomainMappings{genericIdentityDynamic: dynamic, gvr: dmGVR}
	topo, err = NewBuilder(&mockProvider{}).WithDynamic(failing).Build(opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := nodeByID(topo.Nodes, "knativeservice/team/billing").Data["visibility"]; got != nil {
		t.Errorf("visibility with unreadable DomainMappings = %v, want unset", got)
	}
}

type failingDomainMappings struct {
	*genericIdentityDynamic
	gvr schema.GroupVersionResource
}

func (d *failingDomainMappings) ListNamespaces(gvr schema.GroupVersionResource, namespaces []string) ([]*unstructured.Unstructured, error) {
	if gvr == d.gvr {
		return nil, fmt.Errorf("forbidden")
	}
	return d.genericIdentityDynamic.ListNamespaces(gvr, namespaces)
}

func TestKnativeFrontDoorServiceExposesTheKnativeService(t *testing.T) {
	ksvcGVR := schema.GroupVersionResource{Group: "serving.knative.dev", Version: "v1", Resource: "services"}
	dynamic := &genericIdentityDynamic{
		watched: []schema.GroupVersionResource{ksvcGVR},
		kinds:   map[schema.GroupVersionResource]string{ksvcGVR: "Service"},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{ksvcGVR: {
			genericIdentityObject(ksvcGVR, "Service", "team", "web"),
			genericIdentityObject(ksvcGVR, "Service", "team", "api"),
		}},
		listCalls: map[schema.GroupVersionResource]int{},
	}
	frontDoor := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "team", OwnerReferences: []metav1.OwnerReference{
		{APIVersion: "serving.knative.dev/v1", Kind: "Route", Name: "web", UID: "route-uid"},
	}}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeExternalName, ExternalName: "kourier-internal.kourier-system.svc.cluster.local"}}
	// Shares a Knative Service's name but isn't Knative's: no relationship.
	unrelated := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "team"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "api"}}}
	topo, err := NewBuilder(&mockProvider{services: []*corev1.Service{frontDoor, unrelated}}).WithDynamic(dynamic).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	edges := map[string]EdgeType{}
	for _, e := range topo.Edges {
		edges[e.Source+" -> "+e.Target] = e.Type
	}
	if edges["service/team/web -> knativeservice/team/web"] != EdgeExposes {
		t.Errorf("front-door Service should expose the Knative Service; edges: %v", edges)
	}
	for _, wrong := range []string{"knativeservice/team/web -> service/team/web", "knativeservice/team/api -> service/team/api", "service/team/api -> knativeservice/team/api"} {
		if _, ok := edges[wrong]; ok {
			t.Errorf("unexpected edge %s", wrong)
		}
	}

	idx := IndexByResource(topo)
	ksvcRel := GetRelationshipsWithObject("services", "team", "web", genericIdentityObject(ksvcGVR, "Service", "team", "web"), topo, nil, nil, idx)
	if ksvcRel == nil || len(ksvcRel.Services) != 1 || ksvcRel.Services[0].Group != "" || len(ksvcRel.Backends) != 0 {
		t.Errorf("Knative Service should list its front door under Services, not Routes To: %+v", ksvcRel)
	}
	svcRel := GetRelationshipsWithIndex("services", "team", "web", topo, nil, nil, idx)
	if svcRel == nil || len(svcRel.Backends) != 1 || svcRel.Backends[0].Group != "serving.knative.dev" {
		t.Errorf("front-door Service should list the Knative Service under Routes To: %+v", svcRel)
	}
}
