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

// albAction holds the fields the controller validates. Keys decode
// case-insensitively, so both the PascalCase and camelCase spellings work.
type albAction struct {
	Type                string          `json:"type"`
	TargetGroupARN      *string         `json:"targetGroupARN"`
	TargetGroupName     *string         `json:"targetGroupName"`
	FixedResponseConfig *albStatusCode  `json:"fixedResponseConfig"`
	RedirectConfig      *albStatusCode  `json:"redirectConfig"`
	ForwardConfig       *albForwardConf `json:"forwardConfig"`
}

type albStatusCode struct {
	StatusCode string `json:"statusCode"`
}

type albForwardConf struct {
	TargetGroups []albTargetGroup `json:"targetGroups"`
}

type albTargetGroup struct {
	ServiceName     *string             `json:"serviceName"`
	ServicePort     *intstr.IntOrString `json:"servicePort"`
	TargetGroupARN  *string             `json:"targetGroupARN"`
	TargetGroupName *string             `json:"targetGroupName"`
	Weight          *int64              `json:"weight"`
}

// valid mirrors the controller's Action.validate. An empty forwardConfig is
// rejected as well, since it attaches nothing.
func (a albAction) valid() bool {
	switch a.Type {
	case "fixed-response":
		return a.FixedResponseConfig != nil && a.FixedResponseConfig.StatusCode != ""
	case "redirect":
		return a.RedirectConfig != nil && a.RedirectConfig.StatusCode != ""
	case "forward":
		external := a.TargetGroupARN != nil || a.TargetGroupName != nil
		if external == (a.ForwardConfig != nil) {
			return false
		}
		if a.ForwardConfig == nil {
			return true
		}
		groups := a.ForwardConfig.TargetGroups
		if len(groups) == 0 {
			return false
		}
		for _, tg := range groups {
			external := tg.TargetGroupARN != nil || tg.TargetGroupName != nil
			if external == (tg.ServiceName != nil) {
				return false
			}
			if tg.ServiceName != nil && !albPortSet(tg.ServicePort) {
				return false
			}
			if len(groups) > 1 && tg.Weight == nil {
				return false
			}
		}
		return true
	}
	return false
}

// albPortSet reports whether a servicePort names something a Service could
// expose. null, "" and 0 all decode without error but match no port.
func albPortSet(port *intstr.IntOrString) bool {
	if port == nil {
		return false
	}
	value := strings.TrimSpace(port.String())
	return value != "" && value != "0"
}

// parseALBAction reads the action annotation for a use-annotation backend.
//
// found is false when no annotation exists for the backend. malformed covers
// anything the controller would reject. targets list only the backends Radar
// can check.
// Redirect and fixed-response actions resolve nothing in-cluster, and neither
// do target groups given by ARN or name, so those yield no targets with found
// true.
func parseALBAction(annotations map[string]string, backendServiceName string) (targets []albActionTarget, found, malformed bool) {
	raw, ok := annotations[ALBActionAnnotation(backendServiceName)]
	if !ok {
		return nil, false, false
	}
	if strings.TrimSpace(raw) == "" {
		return nil, true, true
	}

	var action albAction
	if err := json.Unmarshal([]byte(raw), &action); err != nil || !action.valid() {
		return nil, true, true
	}
	if action.ForwardConfig == nil {
		return nil, true, false
	}

	for _, tg := range action.ForwardConfig.TargetGroups {
		if tg.ServiceName == nil {
			continue
		}
		targets = append(targets, albActionTarget{
			ServiceName: *tg.ServiceName,
			ServicePort: tg.ServicePort.String(),
		})
	}

	return targets, true, false
}
