package agentsandbox

import (
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/health"
	"github.com/skyhook-io/radar/pkg/readreason"
)

// Ref names one object.
type Ref struct {
	Kind      string `json:"kind"`
	Group     string `json:"group"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

func sandboxRef(u *unstructured.Unstructured) Ref {
	return Ref{Kind: "Sandbox", Group: Group, Namespace: u.GetNamespace(), Name: u.GetName()}
}

func claimRef(u *unstructured.Unstructured) Ref {
	return Ref{Kind: "SandboxClaim", Group: ExtensionsGroup, Namespace: u.GetNamespace(), Name: u.GetName()}
}

func poolRef(namespace, name string) Ref {
	return Ref{Kind: "SandboxWarmPool", Group: ExtensionsGroup, Namespace: namespace, Name: name}
}

func templateRef(namespace, name string) Ref {
	return Ref{Kind: "SandboxTemplate", Group: ExtensionsGroup, Namespace: namespace, Name: name}
}

func podRef(u *unstructured.Unstructured) Ref {
	return Ref{Kind: "Pod", Namespace: u.GetNamespace(), Name: health.AgentSandboxPodName(u)}
}

func key(u *unstructured.Unstructured) string { return u.GetNamespace() + "/" + u.GetName() }

type cond struct {
	status, reason, message string
	at                      time.Time
}

func condition(u *unstructured.Unstructured, condType string) (cond, bool) {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, raw := range conds {
		c, ok := raw.(map[string]any)
		if !ok || c["type"] != condType {
			continue
		}
		var out cond
		out.status, _ = c["status"].(string)
		out.reason, _ = c["reason"].(string)
		out.message, _ = c["message"].(string)
		out.message = strings.TrimSpace(out.message)
		if ts, _ := c["lastTransitionTime"].(string); ts != "" {
			out.at, _ = time.Parse(time.RFC3339, ts)
		}
		return out, true
	}
	return cond{}, false
}

func nestedString(u *unstructured.Unstructured, fields ...string) string {
	s, _, _ := unstructured.NestedString(u.Object, fields...)
	return strings.TrimSpace(s)
}

// nestedCount reads an integer field however the decoder typed it.
func nestedCount(obj map[string]any, fields ...string) (int64, bool) {
	v, found, _ := unstructured.NestedFieldNoCopy(obj, fields...)
	if !found {
		return 0, false
	}
	switch n := v.(type) {
	case int64:
		return n, true
	case int32:
		return int64(n), true
	case int:
		return int64(n), true
	case float64:
		return int64(n), true
	}
	return 0, false
}

// reportedSince is the best evidence of when a Sandbox's or claim's reported
// failure began: the latest transition among Ready and the conditions that
// move after it on the way there (Finished, PodScheduled, a resume).
// Kubernetes keeps Ready's transition time when only its reason changes, so
// Ready alone can predate the failure by days; the state began no earlier
// than this.
func reportedSince(u *unstructured.Unstructured) *time.Time {
	var latest time.Time
	consider := func(condType, status string) {
		if c, ok := condition(u, condType); ok && (status == "" || c.status == status) && c.at.After(latest) {
			latest = c.at
		}
	}
	consider("Ready", "")
	consider("Finished", "True")
	consider("PodScheduled", "True")
	consider("Suspended", "False")
	if latest.IsZero() {
		return nil
	}
	latest = latest.UTC()
	return &latest
}

// ClaimWarmPool is the pool a claim names in spec.warmPoolRef.
func ClaimWarmPool(claim *unstructured.Unstructured) string {
	return nestedString(claim, "spec", "warmPoolRef", "name")
}

// ClaimSandbox is the Sandbox a claim is bound to, from status.sandbox.name.
func ClaimSandbox(claim *unstructured.Unstructured) string {
	return nestedString(claim, "status", "sandbox", "name")
}

// WarmPoolTemplate is the SandboxTemplate a pool names.
func WarmPoolTemplate(pool *unstructured.Unstructured) string {
	return nestedString(pool, "spec", "sandboxTemplateRef", "name")
}

// ShutdownTime is a Sandbox's or claim's configured expiry, if any.
func ShutdownTime(u *unstructured.Unstructured) (time.Time, bool) {
	value := nestedString(u, "spec", "shutdownTime")
	if value == "" {
		value = nestedString(u, "spec", "lifecycle", "shutdownTime")
	}
	t, err := time.Parse(time.RFC3339, value)
	return t, err == nil
}

// sandboxOwner is the warm pool or claim that controls a Sandbox, by its
// controller owner reference.
func sandboxOwner(u *unstructured.Unstructured) (Ref, bool) {
	refs := u.GetOwnerReferences()
	if len(refs) == 0 {
		return Ref{}, false
	}
	owner := refs[0]
	for _, r := range refs {
		if r.Controller != nil && *r.Controller {
			owner = r
			break
		}
	}
	switch owner.Kind {
	case "SandboxWarmPool":
		return poolRef(u.GetNamespace(), owner.Name), true
	case "SandboxClaim":
		return Ref{Kind: "SandboxClaim", Group: ExtensionsGroup, Namespace: u.GetNamespace(), Name: owner.Name}, true
	}
	return Ref{}, false
}

// poolMembers are the Sandboxes a pool controls by owner reference, matched
// on UID when both sides record one. Name prefixes prove nothing.
func poolMembers(pool *unstructured.Unstructured, sandboxes []*unstructured.Unstructured) []*unstructured.Unstructured {
	var out []*unstructured.Unstructured
	for _, s := range sandboxes {
		if s.GetNamespace() != pool.GetNamespace() {
			continue
		}
		for _, r := range s.GetOwnerReferences() {
			if r.Kind == "SandboxWarmPool" && r.Name == pool.GetName() && (pool.GetUID() == "" || r.UID == "" || r.UID == pool.GetUID()) {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

// ReadWarmPool reads a SandboxWarmPool's ready reserve against its target.
// Mirrored in k8s-ui's getSandboxWarmPoolStatus.
func ReadWarmPool(u *unstructured.Unstructured) health.Verdict {
	if u.GetDeletionTimestamp() != nil {
		return health.Verdict{Level: health.LevelDegraded, Reason: "Terminating"}
	}
	status, ok := u.Object["status"].(map[string]any)
	if !ok {
		return health.Verdict{Level: health.LevelDegraded, Reason: "Pending"}
	}
	if observed, ok := nestedCount(status, "observedGeneration"); ok && observed < u.GetGeneration() {
		return health.Verdict{Level: health.LevelDegraded, Reason: "Progressing"}
	}
	desired, hasDesired := nestedCount(u.Object, "spec", "replicas")
	ready, _ := nestedCount(status, "readyReplicas")
	switch {
	case hasDesired && desired == 0:
		if replicas, _ := nestedCount(status, "replicas"); replicas > 0 {
			return health.Verdict{Level: health.LevelDegraded, Reason: "Scaling down"}
		}
		return health.Verdict{Level: health.LevelNeutral, Reason: "Scaled to zero"}
	case !hasDesired:
		if ready > 0 {
			return health.Verdict{Level: health.LevelHealthy, Reason: "Ready"}
		}
		return health.Verdict{Level: health.LevelDegraded, Reason: "Pending"}
	case ready >= desired:
		return health.Verdict{Level: health.LevelHealthy, Reason: "Ready"}
	}
	return health.Verdict{Level: health.LevelDegraded, Reason: "Warming"}
}

// ControllerState is how the controller Deployment reads.
type ControllerState string

const (
	ControllerRunning      ControllerState = "running"
	ControllerNotReady     ControllerState = "notReady"
	ControllerScaledToZero ControllerState = "scaledToZero"
	// ControllerNotFound: Radar read Deployments where the release manifest
	// puts the controller and it is not there. It may run under another name,
	// so this is not a fault.
	ControllerNotFound ControllerState = "notFound"
	ControllerUnread   ControllerState = "unread"
	// ControllerNotInstalled: no Agent Sandbox kind is served, so there is
	// nothing for a controller to do.
	ControllerNotInstalled ControllerState = "notInstalled"
)

// Controller is the controller Deployment's reading.
type Controller struct {
	State     ControllerState `json:"state"`
	Namespace string          `json:"namespace"`
	Name      string          `json:"name"`
	// Ready and Desired are set only when the Deployment was read.
	Ready   *int32 `json:"ready,omitempty"`
	Desired *int32 `json:"desired,omitempty"`
	// Code says why the Deployment was not read, when State is unread, in
	// Radar's read vocabulary (pkg/readreason): user_forbidden,
	// outside_namespace_pin, radar_namespace_not_collected,
	// radar_kind_not_collected, kind_sync_pending, kind_sync_timed_out or
	// read_error.
	Code readreason.Code `json:"code,omitempty"`
	// Since is when the Deployment's Available condition last turned False.
	Since *time.Time `json:"since,omitempty"`
}

// ReadController reads the Deployment found at the controller's place.
// Kubernetes leaves readyReplicas out at zero, so absent reads as none ready.
func ReadController(d *appsv1.Deployment) Controller {
	desired := int32(1)
	if d.Spec.Replicas != nil {
		desired = *d.Spec.Replicas
	}
	ready := d.Status.ReadyReplicas
	c := Controller{Namespace: ControllerNamespace, Name: ControllerName, Ready: &ready, Desired: &desired}
	for _, cnd := range d.Status.Conditions {
		if cnd.Type == appsv1.DeploymentAvailable && cnd.Status == "False" && !cnd.LastTransitionTime.IsZero() {
			t := cnd.LastTransitionTime.UTC()
			c.Since = &t
		}
	}
	switch {
	case desired == 0:
		c.State = ControllerScaledToZero
	case ready > 0:
		c.State = ControllerRunning
	default:
		c.State = ControllerNotReady
	}
	return c
}

// ControllerMissing is the reading when Deployments were read at the
// controller's place and it was not there.
func ControllerMissing() Controller {
	return Controller{State: ControllerNotFound, Namespace: ControllerNamespace, Name: ControllerName}
}

// ControllerNotRead is the reading when Radar could not read there.
func ControllerNotRead(code readreason.Code) Controller {
	return Controller{State: ControllerUnread, Namespace: ControllerNamespace, Name: ControllerName, Code: code}
}

func (c Controller) down() bool {
	return c.State == ControllerNotReady || c.State == ControllerScaledToZero
}
