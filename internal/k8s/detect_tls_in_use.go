package k8s

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/skyhook-io/radar/internal/ingressstatus"
	"github.com/skyhook-io/radar/pkg/certs"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/timeutil"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/labels"
)

const (
	TLSCertificateExpiredReason     = "TLSCertificateExpired"
	TLSCertificateNotYetValidReason = "TLSCertificateNotYetValid"
)

// A certificate issued moments ago can carry a NotBefore slightly ahead of
// this host's clock; only a start time clearly in the future is a failure.
const tlsNotYetValidTolerance = 5 * time.Minute

// tlsConsumers is the live Ingresses using one TLS Secret: those with a
// load-balancer address, and those that count only because their
// controller's pods are running, whose namespaces are evidence a caller must
// be able to read.
type tlsConsumers struct {
	addressedRefs []issuesapi.Ref
	runningRefs   []issuesapi.Ref
	podNamespaces map[string]bool
}

// detectLiveIngressTLSCertificates reports a kubernetes.io/tls Secret whose
// leaf certificate is expired or not yet valid while a live Ingress uses it
// for TLS. Audit lists every expiring certificate; this reports only the ones
// something serves, so clients verifying the certificate fail right now.
//
// The Ingress must resolve to an installed IngressClass whose controller is
// not the AWS Load Balancer Controller (which takes certificates from ACM,
// not from the Secret), and must look live:
//   - it has a load-balancer address: critical.
//   - it has none, but its class names a known in-cluster controller with a
//     ready pod: warning. Some controllers never publish an address (Traefik
//     behind a ClusterIP Service), but a running controller doesn't prove it
//     picked up this Ingress.
//
// Anything else may never have been served, so it is left to audit.
func detectLiveIngressTLSCertificates(cache *ResourceCache, namespace string, now time.Time) []Detection {
	ingLister := cache.Ingresses()
	secLister := cache.Secrets()
	if ingLister == nil || secLister == nil {
		return nil
	}
	var ings []*networkingv1.Ingress
	if namespace != "" {
		ings, _ = ingLister.Ingresses(namespace).List(labels.Everything())
	} else {
		ings, _ = ingLister.List(labels.Everything())
	}

	controllerPods := map[string][]string{} // controller -> namespaces of its ready pods
	consumers := map[string]*tlsConsumers{} // namespace/secret
	for _, ing := range ings {
		if ing.DeletionTimestamp != nil || len(ing.Spec.TLS) == 0 || ingressTLSServedElsewhere(ing) {
			continue
		}
		addressed := ingressstatus.HasAssignedAddress(ing)
		controller, ok := ingressController(cache, ing)
		if !ok && addressed {
			ok = unresolvedClassOutsideALB(cache, ing)
		}
		if !ok || controller == albIngressController {
			continue
		}
		var podNamespaces []string
		if !addressed {
			if _, seen := controllerPods[controller]; !seen {
				controllerPods[controller] = readyControllerPodNamespacesMemo(cache, controller)
			}
			podNamespaces = controllerPods[controller]
			if len(podNamespaces) == 0 {
				continue
			}
		}
		seen := map[string]bool{}
		for _, tls := range ing.Spec.TLS {
			if tls.SecretName == "" || seen[tls.SecretName] || !tlsEntryServed(ing, tls) {
				continue
			}
			seen[tls.SecretName] = true
			key := ing.Namespace + "/" + tls.SecretName
			c := consumers[key]
			if c == nil {
				c = &tlsConsumers{podNamespaces: map[string]bool{}}
				consumers[key] = c
			}
			ref := issuesapi.Ref{Group: "networking.k8s.io", Kind: "Ingress", Namespace: ing.Namespace, Name: ing.Name}
			if addressed {
				c.addressedRefs = append(c.addressedRefs, ref)
				continue
			}
			c.runningRefs = append(c.runningRefs, ref)
			for _, ns := range podNamespaces {
				c.podNamespaces[ns] = true
			}
		}
	}

	keys := make([]string, 0, len(consumers))
	for key := range consumers {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var out []Detection
	for _, key := range keys {
		ns, secretName, _ := strings.Cut(key, "/")
		if !cache.KindCoversNamespace("secrets", ns) {
			continue
		}
		secret, err := secLister.Secrets(ns).Get(secretName)
		if err != nil {
			continue
		}
		if det, ok := liveTLSCertificateProblem(secret, consumers[key], now); ok {
			out = append(out, det)
		}
	}
	return out
}

// ingressController resolves the controller of the IngressClass serving ing.
// The legacy class annotation resolves through the IngressClass of the same
// name. Controllers differ on whether it or spec.ingressClassName wins, so
// when both are set and differ, the Ingress is not resolved.
func ingressController(cache *ResourceCache, ing *networkingv1.Ingress) (string, bool) {
	alb, known := ingressServedByALB(cache, ing)
	if alb {
		return albIngressController, true
	}
	name := ""
	if ing.Spec.IngressClassName != nil {
		name = strings.TrimSpace(*ing.Spec.IngressClassName)
	}
	if legacy := ingressstatus.LegacyClass(ing); legacy != "" {
		// Controllers match the annotation against their own class name, which
		// is conventionally the name of their IngressClass.
		if name != "" && legacy != name {
			return "", false
		}
		return ingressClassController(cache, legacy)
	}
	if !known {
		return "", false
	}
	if name != "" {
		return ingressClassController(cache, name)
	}
	return defaultIngressClassController(cache)
}

// unresolvedClassOutsideALB reports whether an addressed Ingress whose class
// resolves to no IngressClass is still served by something other than the
// AWS Load Balancer Controller: a legacy class annotation with no IngressClass
// object (GKE's gce, older ingress-nginx installs), or no class at all. Both
// are refused while any installed class is ALB's: ALB can claim a legacy
// class of any name, and class-less Ingresses. A class field naming an
// uninstalled class is not resolved: its address may be left over from a
// removed controller.
func unresolvedClassOutsideALB(cache *ResourceCache, ing *networkingv1.Ingress) bool {
	lister := cache.IngressClasses()
	if lister == nil || !cache.IsKindReady("ingressclasses") || hasALBAnnotation(ing) {
		return false
	}
	name := ""
	if ing.Spec.IngressClassName != nil {
		name = strings.TrimSpace(*ing.Spec.IngressClassName)
	}
	legacy := ingressstatus.LegacyClass(ing)
	if legacy == albLegacyIngressClass || (legacy == "" && name != "") || (legacy != "" && name != "" && name != legacy) {
		return false
	}
	classes, err := lister.List(labels.Everything())
	if err != nil {
		return false
	}
	for _, ic := range classes {
		if ic.Spec.Controller == albIngressController {
			return false
		}
	}
	return true
}

// tlsEntryServed reports whether a rule serves one of the TLS entry's hosts.
// Controllers pick a certificate by the rule's host, so an entry naming only
// hosts no rule serves is never presented. An entry with no hosts is kept.
func tlsEntryServed(ing *networkingv1.Ingress, tls networkingv1.IngressTLS) bool {
	if len(tls.Hosts) == 0 {
		return true
	}
	for _, host := range tls.Hosts {
		for _, rule := range ing.Spec.Rules {
			if strings.EqualFold(strings.TrimSpace(host), strings.TrimSpace(rule.Host)) {
				return true
			}
		}
	}
	return false
}

// ingressTLSServedElsewhere reports an Ingress whose own TLS Secret is not the
// certificate clients see: SSL passthrough hands TLS to the backend, an
// ingress-nginx canary takes its TLS from the primary Ingress, and Contour's
// tls-cert-namespace reads the Secret from another namespace.
func ingressTLSServedElsewhere(ing *networkingv1.Ingress) bool {
	if ingressSSLPassthrough(ing) || strings.EqualFold(strings.TrimSpace(ing.Annotations["nginx.ingress.kubernetes.io/canary"]), "true") {
		return true
	}
	_, delegated := ing.Annotations["projectcontour.io/tls-cert-namespace"]
	return delegated
}

// sslPassthroughAnnotations hand TLS to the backend (ingress-nginx, HAProxy
// Ingress and HAProxy Kubernetes Ingress), so the Ingress's own TLS Secret is
// not what clients see.
var sslPassthroughAnnotations = []string{
	"nginx.ingress.kubernetes.io/ssl-passthrough",
	"haproxy-ingress.github.io/ssl-passthrough",
	"ingress.kubernetes.io/ssl-passthrough",
	"haproxy.org/ssl-passthrough",
}

func ingressSSLPassthrough(ing *networkingv1.Ingress) bool {
	for _, key := range sslPassthroughAnnotations {
		if strings.EqualFold(strings.TrimSpace(ing.Annotations[key]), "true") {
			return true
		}
	}
	return false
}

// A namespace-filtered compose runs the detector once per namespace, and each
// run would otherwise scan every pod for the controller's labels.
const controllerPodMemoTTL = 5 * time.Second

type controllerPodMemo struct {
	mu    sync.Mutex
	at    time.Time
	byKey map[string][]string
}

func readyControllerPodNamespacesMemo(cache *ResourceCache, controller string) []string {
	m := &cache.controllerPods
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.byKey == nil || time.Since(m.at) > controllerPodMemoTTL {
		m.at, m.byKey = time.Now(), map[string][]string{}
	}
	namespaces, ok := m.byKey[controller]
	if !ok {
		namespaces = readyControllerPodNamespaces(cache, controller)
		m.byKey[controller] = namespaces
	}
	return namespaces
}

// readyControllerPodNamespaces returns the namespaces of the controller's
// ready pods, or nil when none can be seen.
func readyControllerPodNamespaces(cache *ResourceCache, controller string) []string {
	podLabels, ok := ingressstatus.ControllerPodLabels[controller]
	podLister := cache.Pods()
	if !ok || podLister == nil {
		return nil
	}
	pods, err := podLister.List(labels.SelectorFromSet(podLabels))
	if err != nil {
		return nil
	}
	namespaces := map[string]bool{}
	for _, pod := range pods {
		if pod.DeletionTimestamp == nil && pod.Status.Phase == corev1.PodRunning && isPodReadyForProblem(pod) {
			namespaces[pod.Namespace] = true
		}
	}
	out := make([]string, 0, len(namespaces))
	for ns := range namespaces {
		out = append(out, ns)
	}
	sort.Strings(out)
	return out
}

func liveTLSCertificateProblem(secret *corev1.Secret, consumers *tlsConsumers, now time.Time) (Detection, bool) {
	if secret.DeletionTimestamp != nil {
		return Detection{}, false
	}
	cert, ok := certs.FromTLSSecret(secret)
	if !ok {
		return Detection{}, false
	}
	addressed := len(consumers.addressedRefs) > 0
	user := "an Ingress whose ingress controller is running references it for TLS"
	severity := "warning"
	if addressed {
		user = "an Ingress with a load-balancer address references it for TLS"
		severity = "critical"
	}
	var reason, fingerprint, message, cause string
	switch {
	case cert.NotAfter != nil && now.After(*cert.NotAfter):
		reason, fingerprint = TLSCertificateExpiredReason, "tls-certificate-expired"
		message = fmt.Sprintf("The certificate in this Secret expired %s ago (%s), and %s.",
			timeutil.FormatAgeShort(now.Sub(*cert.NotAfter)), cert.NotAfter.UTC().Format(time.RFC3339), user)
		cause = "The certificate's validity period has ended. The ingress controller either serves it, and clients that verify certificates reject the connection, or falls back to its default certificate, which those clients also reject unless it happens to cover this host."
	case cert.NotBefore != nil && cert.NotBefore.After(now.Add(tlsNotYetValidTolerance)):
		reason, fingerprint = TLSCertificateNotYetValidReason, "tls-certificate-not-yet-valid"
		message = fmt.Sprintf("The certificate in this Secret is not valid until %s (in %s), and %s.",
			cert.NotBefore.UTC().Format(time.RFC3339), timeutil.FormatAgeShort(cert.NotBefore.Sub(now)), user)
		cause = "The certificate's validity period has not started, often because it was issued with a wrong clock or start date. Clients that verify certificates reject it until then."
	default:
		return Detection{}, false
	}
	// An addressed consumer stands on its own; links to address-less ones would
	// rest on controller pods the caller may not be allowed to see.
	refs := append([]issuesapi.Ref(nil), consumers.addressedRefs...)
	var reads []issuesapi.EvidenceRead
	if !addressed {
		refs = append(refs, consumers.runningRefs...)
		cause += " No Ingress using it has a load-balancer address, so this rests on its controller running, not on the controller having picked it up."
		podNamespaces := make([]string, 0, len(consumers.podNamespaces))
		for ns := range consumers.podNamespaces {
			podNamespaces = append(podNamespaces, ns)
		}
		sort.Strings(podNamespaces)
		for _, ns := range podNamespaces {
			reads = append(reads, issuesapi.EvidenceRead{Resource: "pods", Namespace: ns, Verb: "list"})
		}
	}

	sort.Slice(refs, func(i, j int) bool { return refs[i].Name < refs[j].Name })
	age := now.Sub(secret.CreationTimestamp.Time)
	if secret.CreationTimestamp.IsZero() || age < 0 {
		age = 0
	}
	return Detection{
		Kind:              "Secret",
		Namespace:         secret.Namespace,
		Name:              secret.Name,
		Severity:          severity,
		Reason:            reason,
		Message:           message,
		MessageSource:     issuesapi.MessageSourceRadar,
		Cause:             cause,
		Action:            fmt.Sprintf("Replace the certificate in Secret %s/%s with a valid one; if cert-manager issues it, check that Certificate's status and events. To see what clients receive: openssl s_client -connect <ingress address>:443 -servername <host> </dev/null | openssl x509 -noout -dates", secret.Namespace, secret.Name),
		Fingerprint:       fingerprint,
		Age:               timeutil.FormatAgeShort(age),
		AgeSeconds:        int64(age.Seconds()),
		ResourceCreatedAt: secret.CreationTimestamp.Time,
		// The expiry time is not when the failure began: the Ingress may have
		// started using an already-expired certificate at any point since.
		OnsetUnknown:  true,
		AffectedRefs:  refs,
		RequiredReads: reads,
	}, true
}
