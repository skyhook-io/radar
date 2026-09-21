package k8s

import (
	"encoding/json"
	"strings"

	"k8s.io/apimachinery/pkg/util/intstr"
)

// albActionSentinel is the port name that AWS Load Balancer Controller reserves to
// mean "this route's backends are described by an annotation, not by a Service port".
const albActionSentinel = "use-annotation"

const albActionAnnotationPrefix = "alb.ingress.kubernetes.io/actions."

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
			ServiceName    *string             `json:"serviceName"`
			ServicePort    *intstr.IntOrString `json:"servicePort"`
			TargetGroupARN *string             `json:"targetGroupARN"`
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
