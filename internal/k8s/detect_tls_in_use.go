package k8s

import (
	"fmt"
	"sort"
	"strings"
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

// tlsConsumers is the live Ingresses using one TLS Secret. addressed is set
// when at least one has a load-balancer address; otherwise every consumer
// counts only because its controller's pods are running, and those pods'
// namespaces are evidence a caller must be able to read.
type tlsConsumers struct {
	refs          []issuesapi.Ref
	addressed     bool
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
		if ing.DeletionTimestamp != nil || len(ing.Spec.TLS) == 0 {
			continue
		}
		controller, ok := ingressController(cache, ing)
		if !ok || controller == albIngressController {
			continue
		}
		addressed := len(ing.Status.LoadBalancer.Ingress) > 0
		var podNamespaces []string
		if !addressed {
			if _, seen := controllerPods[controller]; !seen {
				controllerPods[controller] = readyControllerPodNamespaces(cache, controller)
			}
			podNamespaces = controllerPods[controller]
			if len(podNamespaces) == 0 {
				continue
			}
		}
		seen := map[string]bool{}
		for _, tls := range ing.Spec.TLS {
			if tls.SecretName == "" || seen[tls.SecretName] {
				continue
			}
			seen[tls.SecretName] = true
			key := ing.Namespace + "/" + tls.SecretName
			c := consumers[key]
			if c == nil {
				c = &tlsConsumers{podNamespaces: map[string]bool{}}
				consumers[key] = c
			}
			c.refs = append(c.refs, issuesapi.Ref{Group: "networking.k8s.io", Kind: "Ingress", Namespace: ing.Namespace, Name: ing.Name})
			c.addressed = c.addressed || addressed
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
// The legacy class annotation names no controller, so it resolves only when
// it marks the Ingress for the AWS Load Balancer Controller.
func ingressController(cache *ResourceCache, ing *networkingv1.Ingress) (string, bool) {
	alb, known := ingressServedByALB(cache, ing)
	if alb {
		return albIngressController, true
	}
	if !known || ingressstatus.LegacyClass(ing) != "" {
		return "", false
	}
	if name := ing.Spec.IngressClassName; name != nil && strings.TrimSpace(*name) != "" {
		return ingressClassController(cache, strings.TrimSpace(*name))
	}
	return defaultIngressClassController(cache)
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
	user := "an Ingress whose ingress controller is running references it for TLS"
	severity := "warning"
	if consumers.addressed {
		user = "an Ingress with a load-balancer address references it for TLS"
		severity = "critical"
	}
	var reason, fingerprint, message, cause string
	switch {
	case cert.NotAfter != nil && now.After(*cert.NotAfter):
		reason, fingerprint = TLSCertificateExpiredReason, "tls-certificate-expired"
		message = fmt.Sprintf("The certificate in this Secret expired %s ago (%s), and %s.",
			timeutil.FormatAgeShort(now.Sub(*cert.NotAfter)), cert.NotAfter.UTC().Format(time.RFC3339), user)
		cause = "The certificate's validity period has ended. The ingress controller either serves it, and clients that verify certificates reject the connection, or falls back to its default certificate, which those clients also reject for this host."
	case cert.NotBefore != nil && cert.NotBefore.After(now.Add(tlsNotYetValidTolerance)):
		reason, fingerprint = TLSCertificateNotYetValidReason, "tls-certificate-not-yet-valid"
		message = fmt.Sprintf("The certificate in this Secret is not valid until %s (in %s), and %s.",
			cert.NotBefore.UTC().Format(time.RFC3339), timeutil.FormatAgeShort(cert.NotBefore.Sub(now)), user)
		cause = "The certificate's validity period has not started, often because it was issued with a wrong clock or start date. Clients that verify certificates reject it until then."
	default:
		return Detection{}, false
	}
	var reads []issuesapi.EvidenceRead
	if !consumers.addressed {
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

	refs := append([]issuesapi.Ref(nil), consumers.refs...)
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
