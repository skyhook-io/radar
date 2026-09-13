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
	selfPod, err := cache.Pods().Pods(selfNs).Get(selfName)
	if err != nil {
		return nil
	}
	src := netpol.Peer{Pod: selfPod, Namespace: namespaceObject(cache, selfNs)}
	selfPolicies, err := cache.NetworkPolicies().NetworkPolicies(selfNs).List(labels.Everything())
	if err != nil {
		return nil
	}

	for i, cand := range candidates {
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
		policies := selfPolicies
		if cand.Namespace != selfNs {
			candPolicies, err := cache.NetworkPolicies().NetworkPolicies(cand.Namespace).List(labels.Everything())
			if err != nil {
				continue
			}
			policies = append(append([]*networkingv1.NetworkPolicy{}, selfPolicies...), candPolicies...)
		}
		verdict := netpol.Evaluate(src, backends, policies)
		if verdict.Kind != netpol.Denied {
			continue
		}
		return &networkPolicyBlockedError{msg: describeNetworkPolicyBlock(cand, selfPod, verdict)}
	}
	return nil
}

// cacheAuthoritativeFor reports whether the cache can answer completely for a
// namespace: every kind the evaluation reads is synced and covers it. A
// namespace-scoped or still-warming informer could be missing exactly the
// policy that admits the traffic, which would manufacture a denial.
func cacheAuthoritativeFor(cache *k8s.ResourceCache, ns string) bool {
	for _, kind := range []k8score.ResourceType{k8score.Pods, k8score.Services, k8score.NetworkPolicies} {
		synced, known := cache.InformerSynced(string(kind))
		if !known || !synced || !cache.KindCoversNamespace(string(kind), ns) {
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
	ns, err := cache.Namespaces().Get(name)
	if err != nil {
		return nil
	}
	return ns
}

// candidateBackends resolves the pods and pod ports that actually serve the
// candidate Service port, from its EndpointSlices. Endpoint membership, not
// the selector, is what routes traffic; the slice also carries the resolved
// port number, which a named targetPort can make differ per pod. ok is false
// when any published endpoint cannot be resolved to a cached Pod: a verdict
// over a partial backend set could deny a connection the missing pod admits.
func (c *Client) candidateBackends(ctx context.Context, cache *k8s.ResourceCache, cand prom.Candidate) ([]netpol.Backend, bool) {
	svc, err := cache.Services().Services(cand.Namespace).Get(cand.Name)
	if err != nil {
		return nil, false
	}
	portName, found := "", false
	for _, p := range svc.Spec.Ports {
		if int(p.Port) == cand.Port {
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
	candNs := namespaceObject(cache, cand.Namespace)
	var backends []netpol.Backend
	for i := range slices.Items {
		slice := &slices.Items[i]
		if slice.AddressType != discoveryv1.AddressTypeIPv4 && slice.AddressType != discoveryv1.AddressTypeIPv6 {
			continue
		}
		var port int32
		proto := corev1.ProtocolTCP
		havePort := false
		for _, p := range slice.Ports {
			if p.Port != nil && ((p.Name == nil && portName == "") || (p.Name != nil && *p.Name == portName)) {
				port, havePort = *p.Port, true
				if p.Protocol != nil {
					proto = *p.Protocol
				}
				break
			}
		}
		if !havePort {
			continue
		}
		for _, ep := range slice.Endpoints {
			// Ready unset means "unknown", which consumers are told to treat as
			// ready.
			if ep.Conditions.Ready != nil && !*ep.Conditions.Ready {
				continue
			}
			if ep.TargetRef == nil || ep.TargetRef.Kind != "Pod" {
				return nil, false
			}
			ns := ep.TargetRef.Namespace
			if ns == "" {
				ns = cand.Namespace
			}
			pod, err := cache.Pods().Pods(ns).Get(ep.TargetRef.Name)
			if err != nil {
				return nil, false
			}
			backends = append(backends, netpol.Backend{
				Peer:     netpol.Peer{Pod: pod, Namespace: candNs},
				Port:     port,
				Protocol: proto,
			})
		}
	}
	return backends, true
}

func describeNetworkPolicyBlock(cand prom.Candidate, self *corev1.Pod, v netpol.Verdict) string {
	target := fmt.Sprintf("%s/%s:%d", cand.Namespace, cand.Name, cand.Port)
	selfRef := self.Namespace + "/" + self.Name
	noun := "NetworkPolicy " + v.Policies[0]
	if len(v.Policies) > 1 {
		noun = "NetworkPolicies " + strings.Join(v.Policies, ", ")
	}
	switch v.Direction {
	case netpol.DirectionEgress:
		return fmt.Sprintf("Prometheus candidate %s was unreachable, and %s isolates egress from %s with no rule admitting it; add an egress rule to the %s namespace on port %d",
			target, noun, selfRef, cand.Namespace, cand.Port)
	default:
		return fmt.Sprintf("Prometheus candidate %s was unreachable, and %s isolates ingress to its pods with no rule admitting %s; add an ingress rule for Radar's namespace (%s)",
			target, noun, selfRef, self.Namespace)
	}
}
