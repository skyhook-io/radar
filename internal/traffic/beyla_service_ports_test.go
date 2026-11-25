package traffic

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func portFlow(dstNs, dst, kind, proto string) *Flow {
	return &Flow{
		Source:      Endpoint{Name: "api", Namespace: "shop", Kind: "Workload"},
		Destination: Endpoint{Name: dst, Namespace: dstNs, Kind: kind},
		Protocol:    proto,
	}
}

func lookupFrom(m map[string][]int) servicePortLookup {
	return func(ns, name, _, proto string) ([]int, bool) {
		ports, ok := m[ns+"/"+name+"|"+proto]
		return ports, ok
	}
}

func TestInferServicePorts(t *testing.T) {
	lookup := lookupFrom(map[string][]int{
		"shop/postgres|":   {5432},
		"shop/web|":        {80, 8443},
		"shop/dns|udp":     {53},
		"shop/partial|":    {9000},
		"shop/no-service|": nil,
		"shop/redis|":      {6379},
	})
	single := portFlow("shop", "postgres", "Workload", "tcp")
	multi := portFlow("shop", "web", "Workload", "tcp")
	none := portFlow("shop", "no-service", "Workload", "tcp")
	node := portFlow("", "node-a", "Pod", "tcp")
	unoriented := portFlow("shop", "postgres", "Workload", "tcp")
	unoriented.DirectionUnknown = true
	svc := portFlow("shop", "redis", "Service", "tcp")
	l7 := portFlow("shop", "web", "Workload", "tcp")
	l7.Destination.Name = "checkout"

	flows := []*Flow{single, multi, none, node, unoriented, l7, svc}
	l7Flows := []Flow{{Destination: Endpoint{Name: "checkout", Namespace: "shop"}, Port: 8080}}
	if n := inferServicePorts(flows, l7Flows, false, lookup); n != 3 {
		t.Errorf("inferred = %d, want 3 (postgres, web and the redis Service)", n)
	}
	if svc.Port != 6379 || svc.PortSource != PortSourceService {
		t.Errorf("a Service destination takes its own port: got %d/%q", svc.Port, svc.PortSource)
	}

	if single.Port != 5432 || single.PortSource != PortSourceService {
		t.Errorf("one Service port: got %d/%q, want 5432 from Service", single.Port, single.PortSource)
	}
	if multi.Port != 0 || multi.PortSource != PortSourceServiceMultiple {
		t.Errorf("several Service ports: got %d/%q, want 0 marked multiple", multi.Port, multi.PortSource)
	}
	if none.Port != 0 || none.PortSource != "" {
		t.Errorf("no Service: got %d/%q, want port 0 unmarked", none.Port, none.PortSource)
	}
	if node.Port != 0 || node.PortSource != "" {
		t.Errorf("node destination: got %d/%q, want untouched", node.Port, node.PortSource)
	}
	if unoriented.Port != 0 || unoriented.PortSource != "" {
		t.Errorf("unoriented flow has no known destination: got %d/%q", unoriented.Port, unoriented.PortSource)
	}
	if l7.Port != 8080 || l7.PortSource != "" {
		t.Errorf("an observed HTTP server_port wins and reads as observed: got %d/%q", l7.Port, l7.PortSource)
	}
}

func TestInferServicePortsFiltersByTransportWhenExported(t *testing.T) {
	dns := portFlow("shop", "dns", "Workload", "udp")
	inferServicePorts([]*Flow{dns}, nil, true, lookupFrom(map[string][]int{"shop/dns|udp": {53}}))
	if dns.Port != 53 || dns.PortSource != PortSourceService {
		t.Errorf("got %d/%q, want 53 from Service", dns.Port, dns.PortSource)
	}
}

func TestInferServicePortsIncompleteReadsMultiple(t *testing.T) {
	// One resolved port plus one named targetPort that matches no container port:
	// the resolved one is not known to be the only one.
	f := portFlow("shop", "partial", "Workload", "tcp")
	inferServicePorts([]*Flow{f}, nil, false, func(string, string, string, string) ([]int, bool) { return []int{9000}, false })
	if f.Port != 0 || f.PortSource != PortSourceServiceMultiple {
		t.Errorf("got %d/%q, want multiple", f.Port, f.PortSource)
	}
}

func TestServiceTargetPorts(t *testing.T) {
	tmpl := corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Ports: []corev1.ContainerPort{
			{Name: "http", ContainerPort: 8080, Protocol: corev1.ProtocolTCP},
		}}}},
	}
	svc := func(name string, sel map[string]string, ports ...corev1.ServicePort) *corev1.Service {
		return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop"},
			Spec: corev1.ServiceSpec{Selector: sel, Ports: ports}}
	}
	svcs := []*corev1.Service{
		svc("web", map[string]string{"app": "web"},
			corev1.ServicePort{Port: 80, TargetPort: intstr.FromString("http")},
			corev1.ServicePort{Port: 9090, Protocol: corev1.ProtocolUDP}),
		svc("other", map[string]string{"app": "other"}, corev1.ServicePort{Port: 1}),
		svc("headless-no-selector", nil, corev1.ServicePort{Port: 2}),
	}

	ports, complete := serviceTargetPorts(svcs, &tmpl, "tcp")
	if !complete || len(ports) != 1 || ports[0] != 8080 {
		t.Errorf("tcp: got %v complete=%t, want [8080] (named targetPort resolved, UDP port filtered)", ports, complete)
	}
	ports, _ = serviceTargetPorts(svcs, &tmpl, "")
	if len(ports) != 2 {
		t.Errorf("any transport: got %v, want 8080 and 9090 (unset targetPort is the port)", ports)
	}

	tmpl.Spec.Containers[0].Ports = nil
	if _, complete := serviceTargetPorts(svcs, &tmpl, "tcp"); complete {
		t.Error("a named targetPort no container declares must make the answer incomplete")
	}
}

func TestServiceOwnPorts(t *testing.T) {
	svc := &corev1.Service{Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{
		{Port: 53, Protocol: corev1.ProtocolUDP}, {Port: 53}, {Port: 9153, TargetPort: intstr.FromInt(9999)},
	}}}
	if got := servicePorts(svc, "tcp"); len(got) != 2 || got[0] != 53 || got[1] != 9153 {
		t.Errorf("got %v, want [53 9153]: the Service's own ports, not its targets", got)
	}
}

func TestPodTemplateFromCacheRefusesAmbiguousNames(t *testing.T) {
	d := &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{}}}
	s := &appsv1.StatefulSet{Spec: appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{}}}
	if _, ok := onlyTemplate(&d.Spec.Template, &s.Spec.Template); ok {
		t.Error("a Deployment and a StatefulSet with one name: which pods Beyla meant is unknown")
	}
	if tmpl, ok := onlyTemplate(nil, &s.Spec.Template, nil); !ok || tmpl != &s.Spec.Template {
		t.Error("one match must be returned")
	}
}

func TestWarningSaysPortsComeFromServices(t *testing.T) {
	p := l4LabelPresence{metric: "beyla_network_flow_bytes_total", transport: true, servicePorts: 3}
	w := p.warning(5)
	if !strings.Contains(w, "taken from Service specs, not observed") {
		t.Errorf("warning must say the ports are inferred: %s", w)
	}
	if strings.Contains(w, "every edge here appears as port 0") {
		t.Errorf("no longer every edge: %s", w)
	}
	if !strings.Contains(w, "Adding dst.port") {
		t.Errorf("the dst.port advice stays for edges still at port 0: %s", w)
	}
}
