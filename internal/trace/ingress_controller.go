package trace

import (
	"fmt"
	"strings"

	"github.com/skyhook-io/radar/internal/ingressstatus"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
)

// An Ingress is only routing RULES; the traffic is actually served by an ingress
// CONTROLLER (e.g. ingress-nginx pods behind a LoadBalancer/NodePort Service).
// This file surfaces that tier on the Ingress hop - the entry address plus a
// plain-language "who serves this" finding - WITHOUT inventing a separate node.
//
// Honesty invariants (these are the whole point):
//   - REPORT THE TIER OF EVIDENCE, never intent as service. The tiers:
//     configured (a class/annotation exists) < controller observed (pods found
//     and ready) < programmed (status.loadBalancer address assigned). "Served
//     by" wording requires at least controller-observed or programmed evidence;
//     a class or annotation alone reads as "configured for", never "served by".
//   - FAIL TOWARD SILENCE on UNCERTAINTY. "couldn't read IngressClasses",
//     "couldn't see the controller pods", and "couldn't read its namespace"
//     must NEVER render as "no controller / broken". Most production is
//     cloud-LB or cross-namespace-RBAC.
//   - A genuinely-OBSERVED empty result is a real observation, not "couldn't
//     read": a synced-but-empty IngressClass list is authoritative and may
//     fire the no-controller warning; a cloud-class Ingress whose synced
//     status has no load balancer address fires the no-address warning.
//   - The only WARNING headlines are POSITIVELY gated: a true no-controller
//     (classes synced and readable, none resolves, no address, no cloud
//     annotations, no legacy class), controller pods that exist but are
//     0-ready, or a cloud-class Ingress with no assigned address.
//   - Caller scoping: controller pod detail (existence, ready counts) is only
//     disclosed when every matched pod's namespace is inside the caller's
//     allow-list; cluster-scoped IngressClass CONTENT - the spec.controller
//     string and the product identity derived from it (controller/product
//     names, glosses, and the pod label selectors in suggested commands) - is
//     omitted for namespace-scoped callers in EVERY branch, pills, tooltips,
//     and findings alike. They get the evidence tier plus any authorized pod
//     counts, identity-free.

// ingressEntryAddress returns the external entry address(es) a controller
// published for the Ingress (status.loadBalancer.ingress[]). Empty ≠ "no
// controller" - many valid in-cluster setups (bare NodePort, hostNetwork,
// on-prem) never populate it. For a CLOUD-class Ingress, though, the address
// IS the service evidence: the cloud LB only exists once one is assigned.
func ingressEntryAddress(ing *networkingv1.Ingress) []string {
	if ing == nil {
		return nil
	}
	var out []string
	for _, lb := range ing.Status.LoadBalancer.Ingress {
		switch {
		case lb.Hostname != "":
			out = append(out, lb.Hostname)
		case lb.IP != "":
			out = append(out, lb.IP)
		}
	}
	return out
}

type controllerInfo struct {
	name string // operator-facing name of what serves the traffic
	// controllerName is the in-cluster control-plane component when it differs
	// from name (the AWS Load Balancer Controller manages ALBs; the ALB itself
	// is the data plane). Empty = same as name.
	controllerName string
	labels         map[string]string // label set to find its pods (empty = nothing in-cluster to check)
	cloud          bool              // data plane is a cloud LB - service evidence is the assigned address
}

// podOwnerName names the in-cluster component whose pods we check - used in
// pod-health wording so a dead AWS Load Balancer Controller isn't reported as
// "the AWS Application Load Balancer has no ready pods" (the ALB is cloud
// infrastructure and has no pods).
func (i controllerInfo) podOwnerName() string {
	if i.controllerName != "" {
		return i.controllerName
	}
	return i.name
}

// knownControllers maps an IngressClass spec.controller string to how to find /
// describe it. cloud=true means the DATA plane is a cloud load balancer, so
// the service evidence is the address in status.loadBalancer - but the CONTROL
// plane may still run in-cluster: the AWS Load Balancer Controller is a normal
// in-cluster Deployment (a dead/crashlooping LBC is the most common ALB
// failure), so it carries pod labels like any other controller. GCLB's
// controller runs inside the cloud provider's managed control plane - there is
// genuinely nothing in-cluster to check. Unknown controllers fall through to
// fail-soft handling (named, never condemned).
var knownControllers = map[string]controllerInfo{
	"k8s.io/ingress-nginx":           {name: "ingress-nginx", labels: map[string]string{"app.kubernetes.io/name": "ingress-nginx", "app.kubernetes.io/component": "controller"}},
	"nginx.org/ingress-controller":   {name: "NGINX Ingress (F5)", labels: map[string]string{"app.kubernetes.io/name": "nginx-ingress"}},
	"traefik.io/ingress-controller":  {name: "Traefik", labels: map[string]string{"app.kubernetes.io/name": "traefik"}},
	"projectcontour.io/contour":      {name: "Contour", labels: map[string]string{"app.kubernetes.io/name": "contour"}},
	"haproxy.org/ingress-controller": {name: "HAProxy Ingress", labels: map[string]string{"app.kubernetes.io/instance": "haproxy-ingress"}},
	"ingress.k8s.aws/alb":            {name: "AWS Application Load Balancer", controllerName: "aws-load-balancer-controller", labels: map[string]string{"app.kubernetes.io/name": "aws-load-balancer-controller"}, cloud: true},
	"k8s.io/ingress-gce":             {name: "Google Cloud load balancer", cloud: true},
}

// resolveIngressClass returns the name + controller string of the IngressClass
// governing the Ingress: the named class, else the cluster's default class.
// found=false when neither resolves - a POSITIVE signal that nothing is
// configured to serve it. The default-class path is the primary one in practice
// (Ingresses commonly omit ingressClassName).
//
// Reads from the DYNAMIC cache (IngressClasses isn't a typed informer in Radar)
// - already synced and RBAC-respecting, so a cluster where the caller can't
// watch ingressclasses simply yields "not found" → fail-soft, never "broken".
func resolveIngressClass(deps Deps, ing *networkingv1.Ingress) (name, controller string, found, couldRead bool) {
	// couldRead reports only the read-ERROR case (RBAC-denied / cold cache). An
	// UNWIRED dynamic/discovery client (nil at runtime - cold start / init
	// failure) is exactly that: we couldn't read the classes, so it must fall to
	// the soft "couldn't identify the controller" pill, never the no-controller
	// condemnation. couldRead=false keeps a possibly-healthy Ingress safe.
	if deps.Dynamic == nil || deps.Discovery == nil {
		return "", "", false, false
	}
	gvr, ok := deps.Discovery.GetGVRWithGroup("IngressClass", "networking.k8s.io")
	if !ok {
		// A discovery miss happens on a cold/unsynced cache too, not only when
		// the API genuinely isn't served. Fail toward silence (mirror the
		// nil-client branch above) so an unverifiable discovery state can never
		// trigger the no-controller condemnation.
		return "", "", false, false
	}
	classes, err := deps.Dynamic.ListWatched(gvr)
	// couldRead distinguishes "read the classes, none matched" (a positive
	// no-controller signal) from "couldn't read them at all" (RBAC-denied / cold
	// cache) - the latter must never condemn a possibly-healthy Ingress.
	couldRead = err == nil
	if err != nil || len(classes) == 0 {
		if c2, e2 := deps.Dynamic.List(gvr, ""); e2 == nil {
			classes = c2
			couldRead = true
		}
	}
	if len(classes) == 0 {
		// Empty WITHOUT error is ambiguous on its own: a cold/unsynced informer
		// and a genuinely class-less cluster both return []. The cache's sync
		// authority disambiguates: a synced CLUSTER-WIDE informer holding zero
		// IngressClasses is a real observation ("no ingress controller class
		// exists in this cluster") and keeps couldRead=true so the positive
		// no-controller warning can fire. Anything less (unsynced, unwatched,
		// namespace-scoped fallback) stays unverifiable - fail toward silence so
		// a just-created Ingress isn't false-condemned before the cache catches
		// up.
		couldRead = deps.Dynamic.IsClusterWideSynced(gvr)
	}
	want := ""
	if ing.Spec.IngressClassName != nil {
		want = *ing.Spec.IngressClassName
	}
	controllerOf := func(ic *unstructured.Unstructured) string {
		c, _, _ := unstructured.NestedString(ic.Object, "spec", "controller")
		return c
	}
	var def *unstructured.Unstructured
	for _, ic := range classes {
		if ic == nil {
			continue
		}
		if want != "" {
			if ic.GetName() == want {
				return ic.GetName(), controllerOf(ic), true, couldRead
			}
			continue
		}
		if ic.GetAnnotations()["ingressclass.kubernetes.io/is-default-class"] == "true" {
			def = ic
		}
	}
	if want == "" && def != nil {
		return def.GetName(), controllerOf(def), true, couldRead
	}
	return "", "", false, couldRead
}

// legacyIngressClass returns the value of the legacy
// kubernetes.io/ingress.class annotation, still honored by ingress-nginx and
// ubiquitous on older clusters. An Ingress declaring its class only via this
// annotation must not be condemned as "no controller".
func legacyIngressClass(ing *networkingv1.Ingress) string {
	return ingressstatus.LegacyClass(ing)
}

// hasCloudLBAnnotations reports cloud load-balancer ingress annotations. This
// is CONFIGURED-tier evidence at best, and only consulted when no IngressClass
// resolves: a resolved class is authoritative for who serves the Ingress, so
// stale alb.* annotations on an nginx-class Ingress must not skip the nginx
// pod-health check.
func hasCloudLBAnnotations(ing *networkingv1.Ingress) bool {
	return ingressstatus.HasCloudLoadBalancerAnnotations(ing)
}

// findControllerPods locates a controller's pods cluster-wide by its known label
// set. found=false means the labels matched nothing - which could be a different
// controller OR RBAC hiding the namespace, so the caller must treat it as
// "couldn't see", never "broken".
func findControllerPods(deps Deps, info controllerInfo) (pods []*corev1.Pod, found bool) {
	if deps.Cache == nil || deps.Cache.Pods() == nil || len(info.labels) == 0 {
		return nil, false
	}
	got, err := deps.Cache.Pods().Pods(metav1.NamespaceAll).List(labels.SelectorFromSet(labels.Set(info.labels)))
	if err != nil || len(got) == 0 {
		return nil, false
	}
	return got, true
}

// scopedCaller reports whether this request carries a namespace allow-list.
// The trace package treats a non-nil AllowedNamespaces as the caller's
// authorization boundary (mirroring the cross-namespace hop redaction in
// entries.go): cluster-scoped IngressClass CONTENT - the spec.controller
// string and the product identity derived from it - is not disclosed to
// scoped callers; they get the evidence tier without it.
func scopedCaller(deps Deps) bool { return deps.AllowedNamespaces != nil }

type controllerPodCheck int

const (
	podsNotFound controllerPodCheck = iota // labels matched nothing (or pod cache unavailable)
	podsRedacted                           // pods exist, but a namespace is outside the caller's scope
	podsChecked                            // pods found, every namespace within the caller's scope
)

// checkControllerPods locates the controller's pods and authorizes disclosure:
// pod-level detail (existence, ready counts, namespaces) is namespaced data,
// so it is only reported when EVERY matched pod's namespace is inside the
// caller's allow-list. A partial view could otherwise false-condemn the
// controller (counting only the visible subset as 0-ready) or leak replica
// counts the REST handler never authorized.
func checkControllerPods(deps Deps, info controllerInfo) (check controllerPodCheck, ready, total int) {
	pods, found := findControllerPods(deps, info)
	if !found {
		return podsNotFound, 0, 0
	}
	for _, p := range pods {
		if p != nil && !deps.NamespaceAllowed(p.Namespace) {
			return podsRedacted, 0, 0
		}
	}
	// Terminal (Succeeded/Failed) or being-deleted pods aren't serving -
	// exclude them so a healthy controller isn't reported as e.g. "1/3 ready"
	// because old/crashed pods linger under the same label (a rolling update
	// leaves a Terminating old pod; a crash can leave Failed pods).
	live := livePods(pods)
	return podsChecked, readyCount(live), len(live)
}

// controllerStatus is the controller-tier readout for an Ingress hop. The quiet
// cases (a controller IS serving it) become a config PILL - servedBy + its
// tooltip - so a healthy Ingress doesn't light up as a finding. Only a real
// PROBLEM (no controller / pods unready / cloud class without an address) is a
// Finding. This keeps the common healthy path silent and reserves findings for
// things to act on.
type controllerStatus struct {
	servedBy      string // short pill label, "" = no pill
	servedByTitle string // pill tooltip (the gloss + shared-infra + health detail)
	finding       Finding
	hasFinding    bool
}

// "ingress controller" gloss + shared-infra cue, woven into the tooltips/cause so
// a non-k8s operator learns the term and never reads shared infra as this
// Ingress's own pods.
const sharedControllerNote = "It's shared cluster infrastructure that serves this and other Ingresses."

const scopedRedactionNote = "controller status not checked (outside your namespace access)"

func ingressControllerStatus(deps Deps, ing *networkingv1.Ingress) controllerStatus {
	var st controllerStatus
	if ing == nil {
		return st
	}
	addr := ingressEntryAddress(ing)
	_, ctrlStr, classFound, classReadable := resolveIngressClass(deps, ing)

	// A resolved class is authoritative for who serves this Ingress - the
	// annotation fallbacks below only apply when NO class resolves.
	if classFound {
		info, known := knownControllers[ctrlStr]
		switch {
		case known && info.cloud:
			return cloudClassStatus(deps, ing, info, addr)
		case known:
			return inClusterClassStatus(deps, info, addr)
		default:
			return unknownControllerStatus(deps, ctrlStr, addr)
		}
	}

	// No IngressClass resolved - weaker signals, weaker claims.
	if hasCloudLBAnnotations(ing) {
		if len(addr) > 0 {
			st.servedBy = "via a cloud LB"
			st.servedByTitle = "Served by a cloud load balancer - a load balancer address is assigned."
			return st
		}
		// Annotations are intent, not service: without a resolvable class or an
		// assigned address, "Served by" would overstate the evidence. The
		// annotations may also be stale, so this stays a soft configured-tier
		// pill, never a finding.
		st.servedBy = "cloud LB (no address)"
		st.servedByTitle = "This Ingress has cloud load balancer annotations, but no load balancer address has been assigned - Radar couldn't confirm anything is serving it yet."
		return st
	}
	if len(addr) > 0 {
		// An address was published, so a controller IS serving it - just couldn't
		// identify which. Reachable, not broken. The @addr pill already shows it,
		// so no extra servedBy pill.
		st.servedBy = "via a controller"
		st.servedByTitle = fmt.Sprintf("Reachable at %s - an ingress controller serves this, but Radar couldn’t identify which one.", strings.Join(addr, ", "))
		return st
	}
	if legacy := legacyIngressClass(ing); legacy != "" {
		// Class declared only via the legacy kubernetes.io/ingress.class annotation
		// (still honored by ingress-nginx). A class IS specified - name it, don't
		// condemn it as "no controller".
		st.servedBy = "via a controller"
		st.servedByTitle = fmt.Sprintf("Class %q is set via the legacy kubernetes.io/ingress.class annotation. %s", legacy, sharedControllerNote)
		return st
	}
	if !classReadable {
		// Couldn't read IngressClasses at all (RBAC-denied / cold cache) - that is
		// not the same as "none configured". Fail toward silence: a soft pill, never
		// a no-controller condemnation of a possibly-healthy Ingress. (A SYNCED
		// empty class list does not land here - resolveIngressClass reports it as
		// readable, because it is a real observation.)
		st.servedBy = "via a controller"
		st.servedByTitle = "Radar couldn’t identify the ingress controller (it couldn’t read IngressClasses). This isn’t a sign that nothing is serving the Ingress."
		return st
	}
	// POSITIVE no-controller signal: classes are readable and none resolves, no
	// address, no cloud annotations, no legacy class. Only here do we say nothing
	// serves it.
	cause := "An ingress controller is the component that actually serves Ingress traffic. None is configured here: no IngressClass resolves (none set, no default installed) and no controller has assigned it an address."
	if ingressstatus.ClassifyUnresolvedClass(ing) == ingressstatus.NamedClassMissing {
		// A class IS named, but no IngressClass object by that name resolved -
		// "none set" would be factually wrong and mildly condemn a configured class.
		cause = fmt.Sprintf("An ingress controller is the component that actually serves Ingress traffic. This Ingress names class %q, but no IngressClass by that name was found (it may be misspelled, not installed, or not yet synced) and no controller has assigned it an address.", *ing.Spec.IngressClassName)
	}
	st.finding = Finding{
		Code:     "ingress:no-controller",
		Severity: SeverityWarning,
		Message:  "No ingress controller is handling this - the routing rules exist but nothing is serving them.",
		Cause:    cause,
		Action:   "Install an ingress controller, or set the Ingress’s class to one that’s installed. See what’s available:",
		Command:  "kubectl get ingressclass",
	}
	st.hasFinding = true
	return st
}

// cloudClassStatus reports an Ingress whose class resolves to a cloud load
// balancer (ALB/GCLB). The cloud LB is the DATA plane, so the service evidence
// is the address in status.loadBalancer - a cloud class with no address is
// configured, not served. The CONTROL plane may still be an ordinary in-cluster
// controller (the AWS Load Balancer Controller), but its pods are checked ONLY
// on the no-address path, where a dead controller is the likely culprit and
// the check changes the diagnosis. With an address assigned - the steady-state
// hot path, rebuilt on every trace - the pod scan (an unindexed cluster-wide
// pod list) is skipped: the provisioned LB keeps serving its last-applied
// rules regardless of controller health, so the readout is the programmed
// tier and claims no pod facts.
func cloudClassStatus(deps Deps, ing *networkingv1.Ingress, info controllerInfo, addr []string) controllerStatus {
	var st controllerStatus
	scoped := scopedCaller(deps)
	gloss := fmt.Sprintf("%s (a cloud load balancer)", info.name)
	pill := "via " + info.name
	if scoped {
		gloss = "a cloud load balancer"
		pill = "via a cloud LB"
	}

	if len(addr) > 0 {
		// Programmed tier: the controller provisioned the LB and published its
		// address - the strongest passive evidence of service.
		st.servedBy = pill
		st.servedByTitle = fmt.Sprintf("Served by %s - a load balancer address is assigned.", gloss)
		return st
	}

	check, ready, total := checkControllerPods(deps, info)

	if check == podsChecked && ready == 0 {
		// The controller exists in-cluster but has no ready pods, and no
		// address was ever published - nothing serves the routing rules. The
		// pod facts (counts) are authorized by podsChecked; the controller
		// NAME and its label selector are class-derived identity, withheld
		// from scoped callers.
		st.finding = Finding{
			Code:     "ingress:controller-unready",
			Severity: SeverityWarning,
			Message:  fmt.Sprintf("The %s has no ready pods and no load balancer address is assigned - this Ingress isn’t being served.", info.podOwnerName()),
			Cause:    fmt.Sprintf("This controller provisions the cloud load balancer for the Ingress; 0 of %d of its pods are Ready and no address has been published in the Ingress status, so nothing is serving the routing rules.", total),
			Action:   "Check the controller’s pods:",
			Command:  fmt.Sprintf("kubectl get pods -A -l %s", labels.SelectorFromSet(labels.Set(info.labels)).String()),
		}
		if scoped {
			st.finding.Message = "This Ingress’s controller has no ready pods and no load balancer address is assigned - this Ingress isn’t being served."
			st.finding.Action = "Check the controller’s pods."
			st.finding.Command = ""
		}
		st.hasFinding = true
		return st
	}

	// Configured tier only: the class is cloud but no address exists. That is
	// an OBSERVED, actionable state - an address-less cloud Ingress is almost
	// certainly not being served - not an uncertainty to stay silent about.
	cause := fmt.Sprintf("This Ingress’s class is served by %s: the controller must provision the load balancer and publish its address in the Ingress status before traffic can arrive, and none is published. A just-created Ingress can take a few minutes to provision; a persistently empty address usually means provisioning failed or the controller isn’t running.", gloss)
	if check == podsChecked {
		if scoped {
			cause += fmt.Sprintf(" The controller is running (%d/%d pods ready), so check the Ingress events for provisioning errors.", ready, total)
		} else {
			cause += fmt.Sprintf(" The %s is running (%d/%d pods ready), so check its logs and the Ingress events for provisioning errors.", info.podOwnerName(), ready, total)
		}
	}
	st.finding = Finding{
		Code:     "ingress:no-address",
		Severity: SeverityWarning,
		Message:  fmt.Sprintf("Configured for %s - no load balancer address has been assigned yet, so there is likely nothing serving this Ingress.", gloss),
		Cause:    cause,
		Action:   "Check the Ingress events for provisioning errors:",
		Command:  fmt.Sprintf("kubectl describe ingress %s -n %s", ing.Name, ing.Namespace),
	}
	st.hasFinding = true
	return st
}

// inClusterClassStatus reports an Ingress whose class resolves to a known
// in-cluster controller (ingress-nginx, Traefik, ...). Pod readiness is the
// primary evidence; the LB address adds the programmed tier when present but
// its absence is normal (NodePort/hostNetwork setups never publish one).
func inClusterClassStatus(deps Deps, info controllerInfo, addr []string) controllerStatus {
	var st controllerStatus
	scoped := scopedCaller(deps)
	check, ready, total := checkControllerPods(deps, info)

	switch check {
	case podsChecked:
		if ready == 0 {
			// The one hard in-cluster PROBLEM: pods exist but none Ready. The
			// pod facts (counts) are authorized by podsChecked; the controller
			// NAME and its label selector are class-derived identity, withheld
			// from scoped callers.
			st.finding = Finding{
				Code:     "ingress:controller-unready",
				Severity: SeverityWarning,
				Message:  fmt.Sprintf("The ingress controller (%s) has no ready pods - traffic to this Ingress can’t be served right now.", info.name),
				Cause:    fmt.Sprintf("An ingress controller is the component that actually serves Ingress traffic; %d of %d %s pods are Ready.", 0, total, info.name),
				Action:   "Check the ingress controller’s pods:",
				Command:  fmt.Sprintf("kubectl get pods -A -l %s", labels.SelectorFromSet(labels.Set(info.labels)).String()),
			}
			if scoped {
				st.finding.Message = "This Ingress’s ingress controller has no ready pods - traffic to this Ingress can’t be served right now."
				st.finding.Cause = fmt.Sprintf("An ingress controller is the component that actually serves Ingress traffic; %d of %d of its pods are Ready.", 0, total)
				st.finding.Action = "Check the ingress controller’s pods."
				st.finding.Command = ""
			}
			st.hasFinding = true
			return st
		}
		if scoped {
			st.servedBy = "via a controller"
			if len(addr) > 0 {
				st.servedByTitle = fmt.Sprintf("Handled by the cluster’s ingress controller (%d/%d pods ready) - a load balancer address is assigned. %s", ready, total, sharedControllerNote)
			} else {
				st.servedByTitle = fmt.Sprintf("Handled by the cluster’s ingress controller (%d/%d pods ready). %s", ready, total, sharedControllerNote)
			}
			return st
		}
		st.servedBy = info.name
		if len(addr) > 0 {
			st.servedByTitle = fmt.Sprintf("Handled by the cluster’s %s ingress controller (%d/%d pods ready) - a load balancer address is assigned. %s", info.name, ready, total, sharedControllerNote)
		} else {
			st.servedByTitle = fmt.Sprintf("Handled by the cluster’s %s ingress controller (%d/%d pods ready). %s", info.name, ready, total, sharedControllerNote)
		}
		return st
	case podsRedacted:
		// The controller's pods exist but live (at least partly) outside the
		// caller's namespace allow-list - disclose neither counts nor
		// namespaces, and drop to the configured/programmed tier.
		st.servedBy = "via a controller"
		if len(addr) > 0 {
			st.servedByTitle = fmt.Sprintf("Served by this class’s ingress controller - a load balancer address is assigned; %s. %s", scopedRedactionNote, sharedControllerNote)
		} else {
			st.servedByTitle = fmt.Sprintf("This Ingress’s class is configured for an ingress controller - %s. %s", scopedRedactionNote, sharedControllerNote)
		}
		return st
	}

	// Pods not found - almost always a namespace Radar's own service account
	// can't read. Never condemn; and without observed pods, an address is the
	// only thing that upgrades "configured" to "served".
	if scoped {
		st.servedBy = "via a controller"
		if len(addr) > 0 {
			st.servedByTitle = fmt.Sprintf("Served by this class’s ingress controller - a load balancer address is assigned. Radar couldn’t see its pods, so controller health wasn’t checked. %s", sharedControllerNote)
		} else {
			st.servedByTitle = fmt.Sprintf("This Ingress’s class is configured for an ingress controller - Radar couldn’t see its pods, so it couldn’t confirm the controller is running. %s", sharedControllerNote)
		}
		return st
	}
	st.servedBy = info.name
	if len(addr) > 0 {
		st.servedByTitle = fmt.Sprintf("Served by the %s ingress controller - a load balancer address is assigned. Radar couldn’t see its pods (they may be in a namespace it can’t access), so controller health wasn’t checked. %s", info.name, sharedControllerNote)
	} else {
		st.servedByTitle = fmt.Sprintf("This Ingress’s class is configured for the %s ingress controller - Radar couldn’t see its pods (they may be in a namespace it can’t access), so it couldn’t confirm it’s running. %s", info.name, sharedControllerNote)
	}
	return st
}

// unknownControllerStatus reports a class that resolves to a controller string
// Radar has no pod-lookup knowledge for. Never condemned: many controllers
// never publish an address and we can't check their pods. The raw
// spec.controller string is cluster-scoped IngressClass content, so scoped
// callers get the tier without it.
func unknownControllerStatus(deps Deps, ctrlStr string, addr []string) controllerStatus {
	var st controllerStatus
	st.servedBy = "via a controller"
	if scopedCaller(deps) {
		if len(addr) > 0 {
			st.servedByTitle = fmt.Sprintf("Served by this class’s ingress controller - a load balancer address is assigned. Controller details are cluster-scoped and not shown for namespace-scoped access. %s", sharedControllerNote)
		} else {
			st.servedByTitle = fmt.Sprintf("This Ingress’s class is configured for an ingress controller Radar doesn’t know how to check. Controller details are cluster-scoped and not shown for namespace-scoped access. %s", sharedControllerNote)
		}
		return st
	}
	if len(addr) > 0 {
		st.servedByTitle = fmt.Sprintf("Served by ingress controller %q - a load balancer address is assigned. %s", ctrlStr, sharedControllerNote)
	} else {
		st.servedByTitle = fmt.Sprintf("This Ingress’s class is handled by controller %q - Radar doesn’t know how to check its pods, and no load balancer address is published (many controllers never publish one). %s", ctrlStr, sharedControllerNote)
	}
	return st
}
