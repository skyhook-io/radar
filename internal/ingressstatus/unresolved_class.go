package ingressstatus

import (
	"strings"

	networkingv1 "k8s.io/api/networking/v1"
)

type UnresolvedClass int

const (
	ClassResolutionNotActionable UnresolvedClass = iota
	NamedClassMissing
	NoClassOrDefault
)

func ClassifyUnresolvedClass(ingress *networkingv1.Ingress) UnresolvedClass {
	if ingress == nil || HasAssignedAddress(ingress) || HasCloudLoadBalancerAnnotations(ingress) || LegacyClass(ingress) != "" {
		return ClassResolutionNotActionable
	}
	if ingress.Spec.IngressClassName != nil && strings.TrimSpace(*ingress.Spec.IngressClassName) != "" {
		return NamedClassMissing
	}
	return NoClassOrDefault
}

func LegacyClass(ingress *networkingv1.Ingress) string {
	if ingress == nil {
		return ""
	}
	return strings.TrimSpace(ingress.Annotations["kubernetes.io/ingress.class"])
}

func HasCloudLoadBalancerAnnotations(ingress *networkingv1.Ingress) bool {
	if ingress == nil {
		return false
	}
	for key, value := range ingress.Annotations {
		if strings.HasPrefix(key, "alb.ingress.kubernetes.io/") ||
			strings.HasPrefix(key, "ingress.gcp.kubernetes.io/") ||
			strings.HasPrefix(key, "networking.gke.io/") {
			return true
		}
		if key == "kubernetes.io/ingress.class" && (strings.Contains(value, "alb") || strings.Contains(value, "gce")) {
			return true
		}
	}
	return false
}

// HasAssignedAddress reports a load-balancer status entry with an IP or hostname.
func HasAssignedAddress(ingress *networkingv1.Ingress) bool {
	for _, address := range ingress.Status.LoadBalancer.Ingress {
		if address.IP != "" || address.Hostname != "" {
			return true
		}
	}
	return false
}

// ControllerPodLabels maps an IngressClass spec.controller to the labels that
// find its pods, for the in-cluster controllers that serve traffic themselves
// and take certificates from the Ingress's TLS Secrets.
var ControllerPodLabels = map[string]map[string]string{
	"k8s.io/ingress-nginx":           {"app.kubernetes.io/name": "ingress-nginx", "app.kubernetes.io/component": "controller"},
	"nginx.org/ingress-controller":   {"app.kubernetes.io/name": "nginx-ingress"},
	"traefik.io/ingress-controller":  {"app.kubernetes.io/name": "traefik"},
	"projectcontour.io/contour":      {"app.kubernetes.io/name": "contour"},
	"haproxy.org/ingress-controller": {"app.kubernetes.io/instance": "haproxy-ingress"},
}
