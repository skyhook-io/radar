package k8s

import (
	"encoding/json"
	"strings"

	"github.com/skyhook-io/radar/internal/ingressstatus"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// albActionSentinel is the port name that AWS Load Balancer Controller reserves to
// mean "this route's backends are described by an annotation, not by a Service port".
const albActionSentinel = "use-annotation"

const (
	albAnnotationPrefix       = "alb.ingress.kubernetes.io/"
	albActionAnnotationPrefix = albAnnotationPrefix + "actions."
	albIngressController      = "ingress.k8s.aws/alb"
	albLegacyIngressClass     = "alb"
)

// ingressServedByALB reports whether the AWS Load Balancer Controller serves
// the Ingress. The class comes from the named IngressClass, else the legacy
// class annotation, else the cluster default. A named class that doesn't
// resolve is not retried via the annotation, since controllers honour the
// field over it. With no class, any alb.* annotation counts as evidence.
// known is false when nothing identifies the controller.
func ingressServedByALB(cache *ResourceCache, ing *networkingv1.Ingress) (alb, known bool) {
	if name := ing.Spec.IngressClassName; name != nil && strings.TrimSpace(*name) != "" {
		if controller, ok := ingressClassController(cache, strings.TrimSpace(*name)); ok {
			return controller == albIngressController, true
		}
	} else if class := ingressstatus.LegacyClass(ing); class != "" {
		return class == albLegacyIngressClass, true
	} else if controller, ok := defaultIngressClassController(cache); ok {
		return controller == albIngressController, true
	}
	for key := range ing.Annotations {
		if strings.HasPrefix(key, albAnnotationPrefix) {
			return true, true
		}
	}
	return false, false
}

func ingressClassController(cache *ResourceCache, name string) (controller string, ok bool) {
	lister := cache.IngressClasses()
	if lister == nil || !cache.IsKindReady("ingressclasses") {
		return "", false
	}
	ic, err := lister.Get(name)
	if err != nil {
		return "", false
	}
	return ic.Spec.Controller, true
}

func defaultIngressClassController(cache *ResourceCache) (controller string, ok bool) {
	lister := cache.IngressClasses()
	if lister == nil || !cache.IsKindReady("ingressclasses") {
		return "", false
	}
	classes, err := lister.List(labels.Everything())
	if err != nil {
		return "", false
	}
	// More than one default is ambiguous. Lister order is not stable, so
	// picking one would flip the answer between scans.
	var defaults []string
	for _, ic := range classes {
		if ic.Annotations["ingressclass.kubernetes.io/is-default-class"] == "true" {
			defaults = append(defaults, ic.Spec.Controller)
		}
	}
	if len(defaults) != 1 {
		return "", false
	}
	return defaults[0], true
}

// ALBActionAnnotation returns the annotation key that AWS Load Balancer Controller
// reads a use-annotation backend's action from.
func ALBActionAnnotation(backendServiceName string) string {
	return albActionAnnotationPrefix + backendServiceName
}

// albActionTarget is one in-cluster backend of a forward action. Target groups
// addressed by ARN or name have no Service and produce no target.
type albActionTarget struct {
	ServiceName string
	ServicePort string
}

type albAction struct {
	ForwardConfig *struct {
		TargetGroups []struct {
			ServiceName *string             `json:"serviceName"`
			ServicePort *intstr.IntOrString `json:"servicePort"`
		} `json:"targetGroups"`
	} `json:"forwardConfig"`
}

// parseALBAction reads the action annotation for a use-annotation backend.
//
// found is false when no annotation exists for the backend. malformed covers
// an annotation the controller can't read either, including a blank one.
// targets list only the backends Radar can check. Redirect and fixed-response
// actions resolve nothing in-cluster, and neither do target groups given by
// ARN or name, so both yield no targets with found true.
func parseALBAction(annotations map[string]string, backendServiceName string) (targets []albActionTarget, found, malformed bool) {
	raw, ok := annotations[ALBActionAnnotation(backendServiceName)]
	if !ok {
		return nil, false, false
	}
	if strings.TrimSpace(raw) == "" {
		return nil, true, true
	}

	var action albAction
	if err := json.Unmarshal([]byte(raw), &action); err != nil {
		return nil, true, true
	}

	if action.ForwardConfig == nil {
		return nil, true, false
	}

	for _, tg := range action.ForwardConfig.TargetGroups {
		if tg.ServiceName == nil || *tg.ServiceName == "" {
			continue
		}

		port := ""
		if tg.ServicePort != nil {
			port = tg.ServicePort.String()
		}

		targets = append(targets, albActionTarget{
			ServiceName: *tg.ServiceName,
			ServicePort: port,
		})
	}

	return targets, true, false
}
