package server

import (
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/netpol"
	pkgtraffic "github.com/skyhook-io/radar/pkg/traffic"
)

// PolicyEvaluation is the response for /api/network-policies/evaluate.
//
// Verdict is what the policies Radar can see say about the connection:
// "admitted" (a core NetworkPolicy admits it), "denied" (core policies isolate
// the pod and none admit it), "no-policy" (no core policy applies), or
// "undecidable" — Reason then says what could not be established. It is a
// reading of declared policy against the pod as it is now, not the network
// plugin's account of the observed flow.
type PolicyEvaluation struct {
	SelectingPolicies []PolicyMatch `json:"selectingPolicies"`
	Verdict           string        `json:"verdict"`
	Reason            string        `json:"reason,omitempty"`
	// Evaluated names what the verdict is about, so the caller can show it.
	Evaluated EvaluatedConnection `json:"evaluated"`
}

// EvaluatedConnection is the connection the handler actually evaluated after
// resolving the request against the cache.
type EvaluatedConnection struct {
	Direction string `json:"direction"`
	Pod       string `json:"pod,omitempty"` // namespace/name of the pod the policies select
	Peer      string `json:"peer,omitempty"`
	Port      int32  `json:"port,omitempty"`
	Protocol  string `json:"protocol,omitempty"`
}

// PolicyMatch describes one policy that applies to the evaluated pod.
// Effect is "admits", "does_not_admit" or "undecidable" — never "denies":
// NetworkPolicy is additive, and a policy that does not admit a connection
// blocks nothing by itself.
type PolicyMatch struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Kind      string `json:"kind"` // NetworkPolicy, CiliumNetworkPolicy, CiliumClusterwideNetworkPolicy
	Effect    string `json:"effect"`
	Reason    string `json:"reason"`
}

const (
	verdictAdmitted    = "admitted"
	verdictDenied      = "denied"
	verdictNoPolicy    = "no-policy"
	verdictUndecidable = "undecidable"
)

// handleEvaluateNetworkPolicies answers, for one observed connection, which
// policies apply to the pod at the enforcing end and whether any admits it.
//
// Query params:
//
//	direction        - "ingress" (policies on the destination) or "egress" (on the source); required
//	namespace, podName               - the destination pod
//	sourceNamespace, sourcePodName   - the source pod
//	sourceKind, sourceIP             - the source when it is not a pod (External | Host | Unknown)
//	destinationKind, destinationIP   - likewise for the destination
//	port, protocol                   - the destination port; protocol tcp|udp|sctp
//
// Every answer is positive-evidence-only: anything the cache cannot vouch for
// — a pod that no longer exists, a namespace its informers do not cover, a
// direction or protocol the flow record did not carry — is reported as
// undecidable with the reason, never guessed. Evidence the caller may not read
// (a peer pod in another namespace, Namespace labels) is left unresolved
// rather than read on their behalf.
func (s *Server) handleEvaluateNetworkPolicies(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "resource cache not ready")
		return
	}
	q := r.URL.Query()

	dir := netpol.Direction(q.Get("direction"))
	if dir != netpol.DirectionIngress && dir != netpol.DirectionEgress {
		s.writeJSON(w, PolicyEvaluation{
			Verdict: verdictUndecidable,
			Reason:  "the flow record does not say at which end the traffic was dropped, so it is not known which pod's policies to check",
		})
		return
	}

	// The enforcing end is the pod the policies select; the other end is the
	// peer they would have to admit.
	var selNs, selName string
	var peerEnd endpointParams
	if dir == netpol.DirectionIngress {
		selNs, selName = q.Get("namespace"), q.Get("podName")
		peerEnd = endpointParams{ns: q.Get("sourceNamespace"), name: q.Get("sourcePodName"), kind: q.Get("sourceKind"), ip: q.Get("sourceIP")}
	} else {
		selNs, selName = q.Get("sourceNamespace"), q.Get("sourcePodName")
		peerEnd = endpointParams{ns: q.Get("namespace"), name: q.Get("podName"), kind: q.Get("destinationKind"), ip: q.Get("destinationIP")}
	}
	if selNs == "" || selName == "" {
		s.writeError(w, http.StatusBadRequest, "the pod at the enforcing end must be named (namespace + podName for ingress, sourceNamespace + sourcePodName for egress)")
		return
	}

	var port int32
	if p := q.Get("port"); p != "" {
		n, err := strconv.ParseInt(p, 10, 32)
		if err != nil || n < 0 || n > 65535 {
			s.writeError(w, http.StatusBadRequest, "port must be a number between 0 and 65535")
			return
		}
		port = int32(n)
	}
	proto, protoKnown := connectionProtocol(q.Get("protocol"))
	evaluated := EvaluatedConnection{Direction: string(dir), Pod: selNs + "/" + selName, Port: port, Protocol: string(proto)}

	// Policy contents and the pod's own labels are what this endpoint reveals,
	// so the caller must be allowed to read both. Cilium policies are gated
	// separately below.
	if !s.canRead(r, "networking.k8s.io", "networkpolicies", selNs, "list") {
		s.writeError(w, http.StatusForbidden, fmt.Sprintf("listing NetworkPolicies in %s is not permitted for this user", selNs))
		return
	}
	if !s.canRead(r, "", "pods", selNs, "get") {
		s.writeError(w, http.StatusForbidden, fmt.Sprintf("reading pods in %s is not permitted for this user", selNs))
		return
	}

	// Completeness before any verdict: a namespace the informers do not cover
	// could hide the policy that admits the traffic.
	for _, kind := range []k8score.ResourceType{k8score.NetworkPolicies, k8score.Pods} {
		synced, known := cache.InformerSynced(string(kind))
		if !known || !synced || !cache.KindCoversNamespace(string(kind), selNs) {
			s.writeJSON(w, PolicyEvaluation{Verdict: verdictUndecidable, Evaluated: evaluated,
				Reason: fmt.Sprintf("Radar can't see %s in namespace %s (not synced yet, or outside the namespaces it watches)", kind, selNs)})
			return
		}
	}
	pods, netpols := cache.Pods(), cache.NetworkPolicies()
	if pods == nil || netpols == nil {
		s.writeJSON(w, PolicyEvaluation{Verdict: verdictUndecidable, Evaluated: evaluated, Reason: "Radar's policy and pod caches are still warming up"})
		return
	}

	selected, err := pods.Pods(selNs).Get(selName)
	if err != nil {
		s.writeJSON(w, PolicyEvaluation{Verdict: verdictUndecidable, Evaluated: evaluated,
			Reason: fmt.Sprintf("pod %s/%s no longer exists, and policies can only be checked against a pod that does", selNs, selName)})
		return
	}
	if !protoKnown {
		s.writeJSON(w, PolicyEvaluation{Verdict: verdictUndecidable, Evaluated: evaluated,
			Reason: "the flow record does not say whether this was TCP, UDP or SCTP, so port rules can't be checked"})
		return
	}
	peer, peerDesc := s.resolvePeer(r, cache, peerEnd)
	evaluated.Peer = peerDesc

	policies, err := netpols.NetworkPolicies(selNs).List(labels.Everything())
	if err != nil {
		log.Printf("[network-policy] Failed to list NetworkPolicies for %s/%s: %v", selNs, selName, err)
		s.writeError(w, http.StatusInternalServerError, "failed to list NetworkPolicies")
		return
	}
	sort.Slice(policies, func(i, j int) bool { return policies[i].Name < policies[j].Name })

	var matches []PolicyMatch
	overall := foldStart
	for _, np := range policies {
		e := netpol.Explain(np, dir, selected, peer, port, proto)
		if e.Effect == netpol.NotApplicable {
			continue
		}
		matches = append(matches, PolicyMatch{Name: np.Name, Namespace: np.Namespace, Kind: "NetworkPolicy", Effect: effectWord(e.Effect), Reason: e.Reason})
		overall = foldEffect(overall, e.Effect)
	}
	verdict, reason := overall.verdict()

	// Cilium unions its own allow rules with core ones and lets its deny rules
	// override both, so as soon as a Cilium policy governs this pod in this
	// direction — or Radar cannot tell whether one does — the core reading is
	// no longer a verdict about the connection, whichever way it went.
	cil := s.ciliumPolicies(r, cache, selected, s.namespaceObject(r, cache, selected.Namespace), dir)
	matches = append(matches, cil.rows...)
	if cil.state == ciliumApplies || cil.state == ciliumUnknown {
		verdict, reason = verdictUndecidable, cil.why+"; Kubernetes NetworkPolicies alone "+describeCore(overall)
	}

	s.writeJSON(w, PolicyEvaluation{SelectingPolicies: matches, Verdict: verdict, Reason: reason, Evaluated: evaluated})
}

// connectionProtocol maps the flow's protocol onto the ones NetworkPolicy
// ports are expressed in. Anything else — ICMP, an L7 name, nothing at all —
// is not a protocol the port rules can be applied to, and defaulting it would
// invent a fact about the connection.
func connectionProtocol(raw string) (corev1.Protocol, bool) {
	switch strings.ToUpper(raw) {
	case "TCP":
		return corev1.ProtocolTCP, true
	case "UDP":
		return corev1.ProtocolUDP, true
	case "SCTP":
		return corev1.ProtocolSCTP, true
	}
	return "", false
}

type endpointParams struct{ ns, name, kind, ip string }

// resolvePeer builds the Peer for the non-enforcing end from what the flow
// record said it was. A pod is looked up by name, and only when the caller may
// read pods there; a pod that is gone or that the caller may not see is an
// unresolved Peer (nothing decidable), never a lookalike. Non-pod kinds map to
// the evaluator's own notions so selectors and ipBlocks are judged correctly.
func (s *Server) resolvePeer(r *http.Request, cache *k8s.ResourceCache, ep endpointParams) (netpol.Peer, string) {
	switch ep.kind {
	case pkgtraffic.EndpointKindExternal:
		return netpol.Peer{External: true, IP: ep.ip}, "external " + ep.ip
	case pkgtraffic.EndpointKindHost:
		return netpol.Peer{Host: true, IP: ep.ip}, "host-network " + ep.ip
	case pkgtraffic.EndpointKindUnknown:
		return netpol.Peer{IP: ep.ip}, "unidentified " + ep.ip
	}
	if ep.ns == "" || ep.name == "" {
		return netpol.Peer{IP: ep.ip}, "unresolved"
	}
	if !s.canRead(r, "", "pods", ep.ns, "get") {
		return netpol.Peer{IP: ep.ip}, ep.ns + "/" + ep.name + " (not visible to you)"
	}
	if pods := cache.Pods(); pods != nil && cache.KindCoversNamespace(string(k8score.Pods), ep.ns) {
		if pod, err := pods.Pods(ep.ns).Get(ep.name); err == nil {
			return netpol.Peer{Pod: pod, Namespace: s.namespaceObject(r, cache, ep.ns), IP: ep.ip}, ep.ns + "/" + ep.name
		}
	}
	return netpol.Peer{IP: ep.ip}, ep.ns + "/" + ep.name + " (no longer exists)"
}

// namespaceObject returns the Namespace when the caller may read namespaces
// and the cache has it; nil otherwise, which the evaluator treats as "labels
// unknown" rather than "no labels".
func (s *Server) namespaceObject(r *http.Request, cache *k8s.ResourceCache, name string) *corev1.Namespace {
	if !s.canRead(r, "", "namespaces", "", "get") {
		return nil
	}
	nss := cache.Namespaces()
	if nss == nil {
		return nil
	}
	ns, err := nss.Get(name)
	if err != nil {
		return nil
	}
	return ns
}

// effectFold accumulates per-policy effects into one answer under additive
// semantics: one admission is enough; a refusal is only a verdict when every
// applicable policy refuses; any open question keeps the whole thing open.
type effectFold struct{ applicable, admits, undecidable int }

var foldStart = effectFold{}

func foldEffect(f effectFold, e netpol.Effect) effectFold {
	switch e {
	case netpol.Admits:
		f.applicable++
		f.admits++
	case netpol.DoesNotAdmit:
		f.applicable++
	case netpol.Undecidable:
		f.applicable++
		f.undecidable++
	}
	return f
}

func (f effectFold) verdict() (string, string) {
	switch {
	case f.applicable == 0:
		return verdictNoPolicy, ""
	case f.admits > 0:
		return verdictAdmitted, ""
	case f.undecidable > 0:
		return verdictUndecidable, "no policy clearly allows this traffic, and at least one could not be checked"
	default:
		return verdictDenied, ""
	}
}

func describeCore(f effectFold) string {
	switch {
	case f.applicable == 0:
		return "do not apply to this pod"
	case f.admits > 0:
		return "would allow it"
	case f.undecidable > 0:
		return "could not be checked"
	default:
		return "would not allow it"
	}
}

func effectWord(e netpol.Effect) string {
	switch e {
	case netpol.Admits:
		return "admits"
	case netpol.DoesNotAdmit:
		return "does_not_admit"
	default:
		return "undecidable"
	}
}

type ciliumState int

const (
	ciliumAbsent  ciliumState = iota // Cilium's policy CRDs are not in the cluster
	ciliumNone                       // present, none governs the pod in this direction
	ciliumApplies                    // at least one does
	ciliumUnknown                    // present, but whether one applies could not be established
)

type ciliumFindings struct {
	state ciliumState
	why   string
	rows  []PolicyMatch
}

// ciliumPolicies finds the CiliumNetworkPolicies and
// CiliumClusterwideNetworkPolicies whose endpointSelector selects the pod and
// that carry rules for dir. Radar does not evaluate their rules; they are
// listed so the reader knows they are in play.
func (s *Server) ciliumPolicies(r *http.Request, cache *k8s.ResourceCache, pod *corev1.Pod, ns *corev1.Namespace, dir netpol.Direction) ciliumFindings {
	discovery := k8s.GetResourceDiscovery()
	if discovery == nil {
		return ciliumFindings{state: ciliumUnknown, why: "Radar has not finished discovering this cluster's APIs, so it is not known whether Cilium policies are in play"}
	}
	cnpGVR, hasCNP := discovery.GetGVRWithGroup("CiliumNetworkPolicy", "cilium.io")
	ccnpGVR, hasCCNP := discovery.GetGVRWithGroup("CiliumClusterwideNetworkPolicy", "cilium.io")
	if !hasCNP && !hasCCNP {
		return ciliumFindings{state: ciliumAbsent}
	}
	identity := ciliumIdentityFor(pod, ns)
	out := ciliumFindings{state: ciliumNone}
	consider := func(kind, namespace string, items []*unstructured.Unstructured) {
		for _, obj := range items {
			selects, decided, why := ciliumGoverns(obj, identity, dir)
			switch {
			case !decided:
				if out.state != ciliumApplies {
					out.state, out.why = ciliumUnknown, kind+" "+obj.GetName()+" "+why
				}
			case selects:
				out.state, out.why = ciliumApplies, "a "+kind+" also applies to this pod, and Radar reads only Kubernetes NetworkPolicies"
				out.rows = append(out.rows, PolicyMatch{
					Name: obj.GetName(), Namespace: namespace, Kind: kind, Effect: "undecidable",
					Reason: "Cilium policy; Radar reads only Kubernetes NetworkPolicies, so what this one did to the flow is known only from the plugin's own report",
				})
			}
		}
	}
	list := func(kind, namespace string, gvr schema.GroupVersionResource) bool {
		if !s.canRead(r, gvr.Group, gvr.Resource, namespace, "list") {
			out.state, out.why = ciliumUnknown, "this cluster has "+kind+" objects and listing them is not permitted for this user"
			return false
		}
		items, err := listDynamicSynced(r.Context(), cache, kind, gvr.Group, namespace)
		if err != nil {
			log.Printf("[network-policy] Failed to list %s: %v", kind, err)
			out.state, out.why = ciliumUnknown, "this cluster has "+kind+" objects and Radar could not read them"
			return false
		}
		consider(kind, namespace, items)
		return true
	}
	if hasCNP && !list("CiliumNetworkPolicy", pod.Namespace, cnpGVR) {
		return out
	}
	if hasCCNP {
		list("CiliumClusterwideNetworkPolicy", "", ccnpGVR)
	}
	return out
}

// ciliumIdentity is the label set Cilium matches an endpointSelector against:
// the pod's labels plus the identity labels Cilium derives for it. Namespace
// labels are included only when the Namespace was readable; a selector on
// them is otherwise undecidable, which known records.
type ciliumIdentity struct {
	set     labels.Set
	nsKnown bool
}

func ciliumIdentityFor(pod *corev1.Pod, ns *corev1.Namespace) ciliumIdentity {
	set := labels.Set{}
	for k, v := range pod.Labels {
		set[k] = v
	}
	set["io.kubernetes.pod.namespace"] = pod.Namespace
	sa := pod.Spec.ServiceAccountName
	if sa == "" {
		sa = "default"
	}
	set["io.cilium.k8s.policy.serviceaccount"] = sa
	id := ciliumIdentity{set: set, nsKnown: ns != nil}
	if ns != nil {
		for k, v := range ns.Labels {
			set["io.cilium.k8s.namespace.labels."+k] = v
		}
	}
	return id
}

// ciliumGoverns reports whether a Cilium policy selects the pod with a rule
// for dir. A policy may carry one spec or a list of specs; any one of them
// that selects the pod and has a section for the direction (allow or deny)
// means the policy governs it. A spec without any rule sections is read
// conservatively as governing both directions. decided is false when the
// selector cannot be judged from what Radar knows.
func ciliumGoverns(obj *unstructured.Unstructured, id ciliumIdentity, dir netpol.Direction) (selects, decided bool, why string) {
	var specs []map[string]any
	if spec, ok, _ := unstructured.NestedMap(obj.Object, "spec"); ok {
		specs = append(specs, spec)
	}
	if list, ok, _ := unstructured.NestedSlice(obj.Object, "specs"); ok {
		for _, item := range list {
			if m, ok := item.(map[string]any); ok {
				specs = append(specs, m)
			}
		}
	}
	openWhy := ""
	for _, spec := range specs {
		selMap, ok, _ := unstructured.NestedMap(spec, "endpointSelector")
		if !ok {
			// A spec without an endpointSelector selects nodes, not pods.
			continue
		}
		if !ciliumSpecCovers(spec, dir) {
			continue
		}
		matches, decided, why := ciliumSelectorMatches(selMap, id)
		switch {
		case decided && matches:
			// One selecting spec is enough, whatever the others left open.
			return true, true, ""
		case !decided && openWhy == "":
			openWhy = why
		}
	}
	if openWhy != "" {
		return false, false, openWhy
	}
	return false, true, ""
}

func ciliumSpecCovers(spec map[string]any, dir netpol.Direction) bool {
	has := func(key string) bool { _, ok := spec[key]; return ok }
	if !has("ingress") && !has("ingressDeny") && !has("egress") && !has("egressDeny") {
		return true
	}
	if dir == netpol.DirectionIngress {
		return has("ingress") || has("ingressDeny")
	}
	return has("egress") || has("egressDeny")
}

// ciliumSelectorMatches evaluates an endpointSelector against the identity.
// Cilium label keys carry a source prefix ("k8s:", "any:"). Requirements are
// ANDed, so the ones Radar can answer are checked first: a definite miss on
// any of them means the policy does not select the pod, whatever the rest
// say. Only when every answerable requirement matches does a key Radar
// cannot reproduce — the cluster name, namespace labels when the Namespace
// was not readable — make the selector undecidable.
func ciliumSelectorMatches(selMap map[string]any, id ciliumIdentity) (matches, decided bool, why string) {
	var sel metav1.LabelSelector
	openWhy := ""
	if ml, ok, _ := unstructured.NestedMap(selMap, "matchLabels"); ok {
		sel.MatchLabels = map[string]string{}
		for k, v := range ml {
			s, ok := v.(string)
			if !ok {
				return false, false, "has a matchLabels value Radar could not read"
			}
			key, ok, why := ciliumSelectorKey(k, id)
			if !ok {
				if openWhy == "" {
					openWhy = why
				}
				continue
			}
			sel.MatchLabels[key] = s
		}
	}
	if exprs, ok, _ := unstructured.NestedSlice(selMap, "matchExpressions"); ok {
		for _, raw := range exprs {
			e, ok := raw.(map[string]any)
			if !ok {
				return false, false, "has a matchExpressions entry Radar could not read"
			}
			key, ok, why := ciliumSelectorKey(fmt.Sprint(e["key"]), id)
			if !ok {
				if openWhy == "" {
					openWhy = why
				}
				continue
			}
			req := metav1.LabelSelectorRequirement{Key: key, Operator: metav1.LabelSelectorOperator(fmt.Sprint(e["operator"]))}
			if vals, ok := e["values"].([]any); ok {
				for _, v := range vals {
					req.Values = append(req.Values, fmt.Sprint(v))
				}
			}
			sel.MatchExpressions = append(sel.MatchExpressions, req)
		}
	}
	parsed, err := metav1.LabelSelectorAsSelector(&sel)
	if err != nil {
		return false, false, "has a selector Radar could not parse"
	}
	if !parsed.Matches(id.set) {
		return false, true, ""
	}
	if openWhy != "" {
		return false, false, openWhy
	}
	return true, true, ""
}

// ciliumSelectorKey strips the label source from a selector key and says
// whether the identity can answer for it.
func ciliumSelectorKey(raw string, id ciliumIdentity) (key string, ok bool, why string) {
	source, key, hasSource := strings.Cut(raw, ":")
	if !hasSource {
		key = raw
	} else if source != "k8s" && source != "any" {
		// reserved:, cidr:, container: and the like describe endpoints that
		// are not pods, so a selector on them cannot select this pod — but
		// whether Cilium attaches such a label to a pod is not Radar's to say.
		return "", false, "selects on the " + source + " label source, which Radar does not model"
	}
	switch {
	case key == "io.cilium.k8s.policy.cluster":
		return "", false, "selects on the cluster name, which Radar does not know"
	case strings.HasPrefix(key, "io.cilium.k8s.namespace.labels.") && !id.nsKnown:
		return "", false, "selects on namespace labels Radar could not read here"
	}
	return key, true, ""
}
