package trace

import "testing"

// The requests a test would send come from the specs alone: a Service
// subject's ports, or each Ingress rule's host and path to the Service port
// it names. A Gateway's attached route and a drained backend send none.
func TestPlannedInClusterRoutes(t *testing.T) {
	svc := &Trace{
		Subject: ResourceRef{Kind: "Service", Namespace: "prio", Name: "lb-app"},
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Service", Namespace: "prio", Name: "lb-app"}, Config: &HopConfig{Ports: []PortMap{{Name: "http", Port: 80}, {Name: "redis", Port: 6379}}}},
			{Resource: ResourceRef{Kind: "Pods", Namespace: "prio"}, Edge: "Service->Pods"},
		},
	}
	got := PlannedInClusterRoutes(svc)
	if len(got) != 2 || got[0].Target != "lb-app:80" || got[1].Target != "lb-app:6379" {
		t.Fatalf("service routes: %+v", got)
	}
	if r := got[0].InClusterRequest; r == nil || r.Protocol != "http" || r.Path != "/" {
		t.Fatalf("port 80 request: %+v", r)
	}
	if r := got[1].InClusterRequest; r == nil || r.Protocol != "tcp" {
		t.Fatalf("port 6379 request: %+v", r)
	}

	backend := &HopConfig{Ports: []PortMap{{Name: "http", Port: 8080}}}
	ing := &Trace{
		Subject: ResourceRef{Kind: "Ingress", Namespace: "prio", Name: "ing-app"},
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Ingress", Namespace: "prio", Name: "ing-app"}, Config: &HopConfig{
				Hostnames: []string{"shop.example.com"},
				Rules: []RouteRule{
					{Hosts: []string{"shop.example.com"}, Paths: []string{"/api"}, Backends: []BackendRef{{Kind: "Service", Name: "web", Port: "8080"}}},
					{Hosts: []string{"shop.example.com"}, Paths: []string{"/old"}, Backends: []BackendRef{{Kind: "Service", Name: "drained", Port: "80"}}},
				},
			}},
			{Resource: ResourceRef{Kind: "Service", Namespace: "prio", Name: "web"}, Edge: "Ingress->Service", Config: backend},
			{Resource: ResourceRef{Kind: "Pods", Namespace: "prio"}, Edge: "Service->Pods"},
		},
	}
	got = PlannedInClusterRoutes(ing)
	if len(got) != 1 || got[0].Target != "web:8080" || got[0].TargetNamespace != "prio" {
		t.Fatalf("ingress routes: %+v", got)
	}
	if r := got[0].InClusterRequest; r == nil || r.Host != "shop.example.com" || r.Path != "/api" {
		t.Fatalf("ingress request: %+v", r)
	}
	if PlannedInClusterRoutes(nil) != nil || PlannedInClusterRoutes(&Trace{}) != nil {
		t.Fatal("an empty trace plans nothing")
	}
}
