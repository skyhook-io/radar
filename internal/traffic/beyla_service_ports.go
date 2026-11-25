package traffic

import (
	"log"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/k8s"
)

// servicePortLookup returns the distinct target ports of the Services that
// select a destination's pods, limited to one protocol when protocol is set.
// complete is false when some port could not be resolved (a named targetPort no
// container declares), so the ports returned are not known to be all of them.
type servicePortLookup func(namespace, workload, protocol string) (ports []int, complete bool)

// inferServicePorts gives port-0 edges a port when Beyla exports no dst_port.
// Without dst_port every edge reads as port 0; the destination's Services say
// which port it serves, but that is declared, not observed, so the edge is
// marked as taken from the Service. An HTTP server_port is observed and wins.
// Returns how many edges got a Service port, for the warning.
func inferServicePorts(flows []*Flow, l7Flows []Flow, byTransport bool, lookup servicePortLookup) int {
	l7Ports := make(map[dstKey]map[int]bool)
	for _, f := range l7Flows {
		if f.Port == 0 {
			continue
		}
		key := dstKey{f.Destination.Namespace, f.Destination.Name}
		if l7Ports[key] == nil {
			l7Ports[key] = make(map[int]bool)
		}
		l7Ports[key][f.Port] = true
	}

	inferred := 0
	for _, f := range flows {
		d := f.Destination
		// An unoriented conversation has no known server side, and a Node or
		// external end has no Service to read.
		if f.Port != 0 || f.DirectionUnknown || d.Namespace == "" ||
			(d.Kind != EndpointKindWorkload && d.Kind != EndpointKindPod) {
			continue
		}
		if ports := l7Ports[dstKey{d.Namespace, d.Name}]; len(ports) == 1 {
			for p := range ports {
				f.Port = p
			}
			continue
		}
		protocol := ""
		if byTransport {
			protocol = f.Protocol
		}
		ports, complete := lookup(d.Namespace, d.Name, protocol)
		switch {
		case len(ports) == 0:
			continue
		case len(ports) == 1 && complete:
			f.Port, f.PortSource = ports[0], PortSourceService
		default:
			f.PortSource = PortSourceServiceMultiple
		}
		inferred++
	}
	return inferred
}

// cachedServicePorts reads the destination's pod template and its namespace's
// Services from Radar's cache. Beyla names a pod by its top owner, so the name
// is looked up as a Deployment, StatefulSet, DaemonSet or bare Pod.
func cachedServicePorts(namespace, workload, protocol string) ([]int, bool) {
	cache := k8s.GetResourceCache()
	if cache == nil || cache.Services() == nil {
		return nil, false
	}
	var found []*corev1.PodTemplateSpec
	if l := cache.Deployments(); l != nil {
		if d, err := l.Deployments(namespace).Get(workload); err == nil {
			found = append(found, &d.Spec.Template)
		}
	}
	if l := cache.StatefulSets(); l != nil {
		if s, err := l.StatefulSets(namespace).Get(workload); err == nil {
			found = append(found, &s.Spec.Template)
		}
	}
	if l := cache.DaemonSets(); l != nil {
		if d, err := l.DaemonSets(namespace).Get(workload); err == nil {
			found = append(found, &d.Spec.Template)
		}
	}
	if l := cache.Pods(); l != nil {
		if p, err := l.Pods(namespace).Get(workload); err == nil {
			found = append(found, &corev1.PodTemplateSpec{ObjectMeta: p.ObjectMeta, Spec: p.Spec})
		}
	}
	tmpl, ok := onlyTemplate(found...)
	if !ok {
		return nil, false
	}
	svcs, err := cache.Services().Services(namespace).List(labels.Everything())
	if err != nil {
		log.Printf("[beyla] Failed to list Services in %s: %v", namespace, err)
		return nil, false
	}
	return serviceTargetPorts(svcs, tmpl, protocol)
}

// onlyTemplate returns the one template found. Two kinds sharing the name make
// it unknown which pods Beyla meant.
func onlyTemplate(templates ...*corev1.PodTemplateSpec) (*corev1.PodTemplateSpec, bool) {
	var only *corev1.PodTemplateSpec
	for _, t := range templates {
		if t == nil {
			continue
		}
		if only != nil {
			return nil, false
		}
		only = t
	}
	return only, only != nil
}

// serviceTargetPorts collects the target ports of the Services selecting the
// template's pods. The target port, not the Service port: Beyla sees the
// connection at the pod, after kube-proxy has rewritten the destination.
func serviceTargetPorts(svcs []*corev1.Service, tmpl *corev1.PodTemplateSpec, protocol string) ([]int, bool) {
	set := make(map[int]bool)
	complete := true
	for _, svc := range svcs {
		if len(svc.Spec.Selector) == 0 || !labels.SelectorFromSet(svc.Spec.Selector).Matches(labels.Set(tmpl.Labels)) {
			continue
		}
		for _, sp := range svc.Spec.Ports {
			proto := sp.Protocol
			if proto == "" {
				proto = corev1.ProtocolTCP
			}
			if protocol != "" && !strings.EqualFold(string(proto), protocol) {
				continue
			}
			if n := targetPortNumber(sp, tmpl.Spec.Containers); n > 0 {
				set[n] = true
			} else {
				complete = false
			}
		}
	}
	ports := make([]int, 0, len(set))
	for p := range set {
		ports = append(ports, p)
	}
	sort.Ints(ports)
	return ports, complete
}

func targetPortNumber(sp corev1.ServicePort, containers []corev1.Container) int {
	if sp.TargetPort.StrVal == "" {
		if sp.TargetPort.IntVal > 0 {
			return int(sp.TargetPort.IntVal)
		}
		return int(sp.Port)
	}
	for _, c := range containers {
		for _, cp := range c.Ports {
			if cp.Name == sp.TargetPort.StrVal {
				return int(cp.ContainerPort)
			}
		}
	}
	return 0
}
