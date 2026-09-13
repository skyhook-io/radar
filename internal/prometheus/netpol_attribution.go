package prometheus

import (
	"context"
	"fmt"
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/netpol"
	"github.com/skyhook-io/radar/pkg/prom"
)

// networkPolicyBlockedError names the NetworkPolicy that denies Radar's pod a
// candidate it could not reach. It unwraps to errPrometheusUnreachable so
// every errors.Is caller keeps its answer; only the message changes.
type networkPolicyBlockedError struct {
	msg string
}

func (e *networkPolicyBlockedError) Error() string { return e.msg }
func (e *networkPolicyBlockedError) Unwrap() error { return errPrometheusUnreachable }

// attributeNetworkPolicyBlock explains an in-cluster direct-probe failure by
// a NetworkPolicy when — and only when — the policies Radar can see deny the
// connection from its own pod to a candidate's ready backends. It returns nil
// whenever any input is missing or uncertain: Radar's pod identity, an
// unsynced or namespace-scoped cache that may hide an admitting policy, a
// backend it cannot resolve, or a probe that was refused rather than
// unreachable. A nil answer means "nothing to add", never "no policy blocks".
//
// The evaluation is of declared intent. A CNI that does not enforce
// NetworkPolicy would let the traffic through despite the denial; the message
// is worded as what the policy says.
func (c *Client) attributeNetworkPolicyBlock(ctx context.Context, cache *k8s.ResourceCache, candidates []prom.Candidate, reasons []prom.ProbeReason) error {
	selfName, selfNs := os.Getenv("MY_POD_NAME"), os.Getenv("MY_POD_NAMESPACE")
	if selfName == "" || selfNs == "" || c.k8sClient == nil {
		return nil
	}
	if cache == nil || !cacheAuthoritativeFor(cache, selfNs) {
		return nil
	}
	pods, netpols := cache.Pods(), cache.NetworkPolicies()
	if pods == nil || netpols == nil {
		return nil
	}
	selfPod, err := pods.Pods(selfNs).Get(selfName)
	if err != nil {
		return nil
	}
	src := netpol.Peer{Pod: selfPod, Namespace: namespaceObject(cache, selfNs)}
	selfPolicies, err := netpols.NetworkPolicies(selfNs).List(labels.Everything())
	if err != nil {
		return nil
	}

	for i, cand := range candidates {
		// Only a probe that got no HTTP answer at all can be a network-path
		// failure; an endpoint that replied 500 or refused credentials was
		// reached.
		if i >= len(reasons) || reasons[i] != prom.ProbeReasonTransportError {
			continue
		}
		if !cacheAuthoritativeFor(cache, cand.Namespace) {
			continue
		}
		backends, ok := c.candidateBackends(ctx, cache, cand)
		if !ok || len(backends) == 0 {
			continue
		}
		// Policies from every namespace involved: Radar's own, and each
		// backend's — a manually managed EndpointSlice can point outside the
		// Service's namespace, and the policy admitting the traffic lives
		// wherever the backend does.
		policies := append([]*networkingv1.NetworkPolicy{}, selfPolicies...)
		seen := map[string]bool{selfNs: true}
		complete := true
		for _, b := range backends {
			ns := b.Pod.Namespace
			if seen[ns] {
				continue
			}
			seen[ns] = true
			if !cacheAuthoritativeFor(cache, ns) {
				complete = false
				break
			}
			nsPolicies, err := netpols.NetworkPolicies(ns).List(labels.Everything())
			if err != nil {
				complete = false
				break
			}
			policies = append(policies, nsPolicies...)
		}
		if !complete {
			continue
		}
		verdict := netpol.Evaluate(src, backends, policies)
		if verdict.Kind != netpol.Denied {
			continue
		}
		return &networkPolicyBlockedError{msg: describeNetworkPolicyBlock(cand, selfPod, backends, verdict)}
	}
	return nil
}

// cacheAuthoritativeFor reports whether the cache can answer completely for a
// namespace: every kind the evaluation reads is synced, ready to serve (a
// deferred kind's lister stays nil for a moment after its informer syncs),
// and covers the namespace. A namespace-scoped or still-warming informer
// could be missing exactly the policy that admits the traffic, which would
// manufacture a denial.
func cacheAuthoritativeFor(cache *k8s.ResourceCache, ns string) bool {
	for _, kind := range []k8score.ResourceType{k8score.Pods, k8score.Services, k8score.NetworkPolicies} {
		synced, known := cache.InformerSynced(string(kind))
		if !known || !synced || !cache.IsKindReady(string(kind)) || !cache.KindCoversNamespace(string(kind), ns) {
			return false
		}
	}
	return true
}

// namespaceObject returns the Namespace, or nil when it cannot be read — the
// evaluator then treats every namespaceSelector as undecidable rather than
// matching it against an empty label set.
func namespaceObject(cache *k8s.ResourceCache, name string) *corev1.Namespace {
	if synced, known := cache.InformerSynced(string(k8score.Namespaces)); !known || !synced {
		return nil
	}
	lister := cache.Namespaces()
	if lister == nil {
		return nil
	}
	ns, err := lister.Get(name)
	if err != nil {
		return nil
	}
	return ns
}

// candidateBackends resolves the pods and pod ports that actually serve the
// candidate Service port, from its EndpointSlices. Endpoint membership, not
// the selector, is what routes traffic; the slice also carries the resolved
// port number, which a named targetPort can make differ per pod. ok is false
// when any published endpoint cannot be tied to a cached Pod — by reference,
// UID and address — since a verdict over a partial or misattributed backend
// set could deny a connection the real backend admits.
func (c *Client) candidateBackends(ctx context.Context, cache *k8s.ResourceCache, cand prom.Candidate) ([]netpol.Backend, bool) {
	services, pods := cache.Services(), cache.Pods()
	if services == nil || pods == nil {
		return nil, false
	}
	svc, err := services.Services(cand.Namespace).Get(cand.Name)
	if err != nil {
		return nil, false
	}
	// The probe is HTTP over TCP; a UDP port sharing the number is not the
	// one it used.
	portName, found := "", false
	for _, p := range svc.Spec.Ports {
		if int(p.Port) == cand.Port && netpol.ProtocolOrTCP(string(p.Protocol)) == corev1.ProtocolTCP {
			portName, found = p.Name, true
			break
		}
	}
	if !found {
		return nil, false
	}
	slices, err := c.k8sClient.DiscoveryV1().EndpointSlices(cand.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: discoveryv1.LabelServiceName + "=" + cand.Name,
	})
	if err != nil {
		return nil, false
	}
	namespaces := map[string]*corev1.Namespace{}
	var backends []netpol.Backend
	for i := range slices.Items {
		slice := &slices.Items[i]
		if slice.AddressType != discoveryv1.AddressTypeIPv4 && slice.AddressType != discoveryv1.AddressTypeIPv6 {
			continue
		}
		port, havePort := slicePort(slice, portName)
		if !havePort {
			continue
		}
		for _, ep := range slice.Endpoints {
			// Traffic can still be routed to a terminating endpoint that is
			// serving (when no ready one is available), so it stays a backend;
			// anything more could only turn a denial into silence, which is the
			// safe direction. Ready unset means "unknown", to be treated as ready.
			ready := ep.Conditions.Ready == nil || *ep.Conditions.Ready
			serving := ep.Conditions.Serving != nil && *ep.Conditions.Serving
			if !ready && !serving {
				continue
			}
			if ep.TargetRef == nil || ep.TargetRef.Kind != "Pod" {
				return nil, false
			}
			ns := ep.TargetRef.Namespace
			if ns == "" {
				ns = cand.Namespace
			}
			pod, err := pods.Pods(ns).Get(ep.TargetRef.Name)
			if err != nil || !endpointMatchesPod(ep, pod) {
				return nil, false
			}
			nsObj, seen := namespaces[ns]
			if !seen {
				nsObj = namespaceObject(cache, ns)
				namespaces[ns] = nsObj
			}
			backends = append(backends, netpol.Backend{
				Peer:     netpol.Peer{Pod: pod, Namespace: nsObj},
				Port:     port,
				Protocol: corev1.ProtocolTCP,
			})
		}
	}
	return backends, true
}

// slicePort finds the TCP port a slice publishes under the Service port's
// name (both empty for a single unnamed port).
func slicePort(slice *discoveryv1.EndpointSlice, portName string) (int32, bool) {
	for _, p := range slice.Ports {
		if p.Port == nil {
			continue
		}
		name := ""
		if p.Name != nil {
			name = *p.Name
		}
		if name != portName {
			continue
		}
		proto := corev1.ProtocolTCP
		if p.Protocol != nil {
			proto = *p.Protocol
		}
		if proto != corev1.ProtocolTCP {
			continue
		}
		return *p.Port, true
	}
	return 0, false
}

// endpointMatchesPod guards against a slice that still names a pod which has
// since been replaced, or a manually managed slice whose address is not the
// referenced pod's: the UID must agree when the reference carries one, and
// the address traffic is routed to — the first one; consumers are told to
// use only that — must be one of the pod's.
func endpointMatchesPod(ep discoveryv1.Endpoint, pod *corev1.Pod) bool {
	if ep.TargetRef.UID != "" && ep.TargetRef.UID != pod.UID {
		return false
	}
	if len(ep.Addresses) == 0 {
		return false
	}
	routed := ep.Addresses[0]
	if pod.Status.PodIP == routed {
		return true
	}
	for _, ip := range pod.Status.PodIPs {
		if ip.IP == routed {
			return true
		}
	}
	return false
}

func describeNetworkPolicyBlock(cand prom.Candidate, self *corev1.Pod, backends []netpol.Backend, v netpol.Verdict) string {
	target := fmt.Sprintf("%s/%s:%d", cand.Namespace, cand.Name, cand.Port)
	selfRef := self.Namespace + "/" + self.Name
	subject := "NetworkPolicy " + v.Policies[0] + " isolates"
	if len(v.Policies) > 1 {
		subject = "NetworkPolicies " + strings.Join(v.Policies, ", ") + " isolate"
	}
	switch v.Direction {
	case netpol.DirectionEgress:
		// The rule has to admit what was evaluated — the backends' own
		// namespaces and pod ports — not the Service's, which a targetPort or a
		// manually managed EndpointSlice can make different.
		return fmt.Sprintf("Prometheus candidate %s was unreachable, and %s egress from %s with no rule admitting it; add an egress rule to %s",
			target, subject, selfRef, backendDestinations(backends))
	case netpol.DirectionBoth:
		return fmt.Sprintf("Prometheus candidate %s was unreachable, and %s the path from %s: no backend is admitted by both Radar's egress rules and its own ingress rules; allow egress to %s and ingress from Radar's namespace (%s)",
			target, subject, selfRef, backendDestinations(backends), self.Namespace)
	default:
		return fmt.Sprintf("Prometheus candidate %s was unreachable, and %s ingress to its pods with no rule admitting %s; add an ingress rule for Radar's namespace (%s)",
			target, subject, selfRef, self.Namespace)
	}
}

// backendDestinations renders the distinct namespace:port pairs an egress rule
// must cover, in first-seen order.
func backendDestinations(backends []netpol.Backend) string {
	seen := map[string]bool{}
	var out []string
	for _, b := range backends {
		dest := fmt.Sprintf("namespace %s port %d", b.Pod.Namespace, b.Port)
		if seen[dest] {
			continue
		}
		seen[dest] = true
		out = append(out, dest)
	}
	return strings.Join(out, ", ")
}
