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
	TLSCertificateExpiringReason    = "TLSCertificateExpiring"
)

// A served certificate this close to expiry is reported before it fails;
// further out it stays an audit finding.
const tlsExpiringWindow = 24 * time.Hour

// maxTLSMessageHosts bounds the certificate hostnames a message lists.
const maxTLSMessageHosts = 3

// A certificate issued moments ago can carry a NotBefore slightly ahead of
// this host's clock; only a start time clearly in the future is a failure.
const tlsNotYetValidTolerance = 5 * time.Minute

// tlsConsumers is the live uses of one TLS Secret: those with a load-balancer
// address, and those that count only because their controller's pods are
// running, whose namespaces are evidence a caller must be able to read.
type tlsConsumers struct {
	addressedRefs []issuesapi.Ref
	runningRefs   []issuesapi.Ref
	podNamespaces map[string]bool
}

// tlsUse is one live resource using a TLS Secret for a host. An empty host
// stands for every name the certificate covers.
type tlsUse struct {
	secretNamespace, secretName string
	host                        string
	controller                  string
	ref                         issuesapi.Ref
	addressed                   bool
	podNamespaces               []string
}

// tlsServingIndex is every live TLS use in the cluster, and for each
// controller every Secret any of its resources references, live or not, with
// the hosts it is referenced for ("" for any).
type tlsServingIndex struct {
	uses       []tlsUse
	referenced map[string]map[string]map[string]bool // controller -> namespace/secret -> hosts
}

func (idx *tlsServingIndex) reference(controller, secretNamespace, secretName, host string) {
	if idx.referenced[controller] == nil {
		idx.referenced[controller] = map[string]map[string]bool{}
	}
	key := secretNamespace + "/" + secretName
	if idx.referenced[controller][key] == nil {
		idx.referenced[controller][key] = map[string]bool{}
	}
	idx.referenced[controller][key][host] = true
}

// detectLiveIngressTLSCertificates reports a kubernetes.io/tls Secret whose
// leaf certificate is expired, not yet valid, or within a day of expiry
// while a live Ingress uses it for TLS. Audit lists every expiring
// certificate; this reports only the ones something serves.
//
// The Ingress must resolve to a controller that takes certificates from its
// Secret (not the AWS Load Balancer Controller, which uses ACM), and must
// look live:
//   - it has a load-balancer address, and its controller, when Radar knows
//     how to find its pods, has a ready one (an address can outlive the
//     controller that published it): critical.
//   - it has none, but its controller has a ready pod: warning. Some
//     controllers never publish an address (Traefik behind a ClusterIP
//     Service), but a running controller doesn't prove it picked up this
//     Ingress.
//
// Controllers pick a certificate per host differently (ingress-nginx by
// Ingress age and TLS entry, Traefik from one store keyed by certificate
// names), so the Secret counts for a host only when its certificate covers
// the host and no other currently valid certificate any resource of the same
// controller references does. Anything else may never be served, so it is
// left to audit.
func detectLiveIngressTLSCertificates(cache *ResourceCache, namespace string, scope *DetectScope, now time.Time) []Detection {
	secLister := cache.Secrets()
	if secLister == nil {
		return nil
	}
	idx := tlsServingIndexFor(cache, scope)
	certFor := func(ns, name string) (certs.Input, bool, bool) {
		return tlsSecretCertificate(cache, scope, ns, name)
	}

	consumers := map[string]*tlsConsumers{} // namespace/secret
	for _, use := range idx.uses {
		if namespace != "" && use.secretNamespace != namespace {
			continue
		}
		cert, ok, _ := certFor(use.secretNamespace, use.secretName)
		// Only a certificate with a problem is checked against the others.
		if !ok || (tlsCertificateValid(cert, now) && cert.NotAfter.Sub(now) > tlsExpiringWindow) || !tlsUseSelectsCertificate(idx, use, cert, now, certFor) {
			continue
		}
		key := use.secretNamespace + "/" + use.secretName
		c := consumers[key]
		if c == nil {
			c = &tlsConsumers{podNamespaces: map[string]bool{}}
			consumers[key] = c
		}
		if use.addressed {
			c.addressedRefs = appendUniqueRef(c.addressedRefs, use.ref)
			continue
		}
		c.runningRefs = appendUniqueRef(c.runningRefs, use.ref)
		for _, ns := range use.podNamespaces {
			c.podNamespaces[ns] = true
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

// tlsServingIndexFor builds the index once per scope. Ingress uses need a
// cluster-wide Ingress inventory: a Secret is reported only when no other
// certificate could be served for its host, which an inventory missing some
// namespaces can't establish.
func tlsServingIndexFor(cache *ResourceCache, scope *DetectScope) *tlsServingIndex {
	return scope.get("tlsServingIndex", func() any {
		idx := &tlsServingIndex{referenced: map[string]map[string]map[string]bool{}}
		ingLister := cache.Ingresses()
		if ingLister == nil || !cache.IsKindClusterWide("ingresses") {
			return idx
		}
		ings, _ := ingLister.List(labels.Everything())
		for _, ing := range ings {
			controller, addressed, ok := tlsIngressController(cache, ing)
			if !ok {
				continue
			}
			for _, tls := range ing.Spec.TLS {
				if tls.SecretName == "" {
					continue
				}
				for _, rule := range ing.Spec.Rules {
					idx.reference(controller, ing.Namespace, tls.SecretName, normalizeTLSHost(rule.Host))
				}
			}
			var podNamespaces []string
			if _, known := ingressstatus.ControllerPodLabels[controller]; known {
				podNamespaces = readyControllerPodNamespaces(cache, scope, controller)
				if len(podNamespaces) == 0 {
					continue
				}
			} else if !addressed {
				continue
			}
			if addressed {
				podNamespaces = nil
			}
			ref := issuesapi.Ref{Group: "networking.k8s.io", Kind: "Ingress", Namespace: ing.Namespace, Name: ing.Name}
			for _, tls := range ing.Spec.TLS {
				if tls.SecretName == "" {
					continue
				}
				for _, rule := range ing.Spec.Rules {
					if host := normalizeTLSHost(rule.Host); host != "" {
						idx.uses = append(idx.uses, tlsUse{
							secretNamespace: ing.Namespace, secretName: tls.SecretName, host: host,
							controller: controller, ref: ref, addressed: addressed, podNamespaces: podNamespaces,
						})
					}
				}
			}
		}
		return idx
	}).(*tlsServingIndex)
}

// tlsUseSelectsCertificate reports whether the use's controller presents
// this certificate for some host of the use: the certificate covers the host
// and no other currently valid certificate the controller references does.
// A store-based controller (Traefik) picks a certificate naming the host
// exactly over a wildcard, so a valid wildcard doesn't contest one that names
// the host, unless it is declared for that host too: ingress-nginx serves the
// first TLS entry declared for a host whatever its names.
// A referenced Secret Radar can't read contests every host, as it may hold
// such a certificate; one referenced for the same host that is missing or
// holds no certificate contests it too, as the controller may fall back to a
// default certificate.
func tlsUseSelectsCertificate(idx *tlsServingIndex, use tlsUse, cert certs.Input, now time.Time, certFor func(ns, name string) (certs.Input, bool, bool)) bool {
	hosts := []string{use.host}
	if use.host == "" {
		hosts = cert.Domains
	}
	self := use.secretNamespace + "/" + use.secretName
	for _, host := range hosts {
		host = normalizeTLSHost(host)
		if host == "" || !tlsCertificateCovers(cert, host) {
			continue
		}
		exact := tlsCertificateNames(cert, host)
		contested := false
		for other, otherHosts := range idx.referenced[use.controller] {
			if other == self {
				continue
			}
			ns, name, _ := strings.Cut(other, "/")
			alt, ok, readable := certFor(ns, name)
			sameHost := otherHosts[host] || otherHosts[""]
			if !readable || (sameHost && !ok) || (ok && tlsCertificateValid(alt, now) && tlsCertificateCovers(alt, host) && (!exact || sameHost || tlsCertificateNames(alt, host))) {
				contested = true
				break
			}
		}
		if !contested {
			return true
		}
	}
	return false
}

// tlsSecretCertificate parses a TLS Secret's leaf once per scope. readable is
// false when Radar can't tell what the Secret holds: its namespace is outside
// the Secret informer. A Secret that doesn't exist is readable and holds no
// certificate.
func tlsSecretCertificate(cache *ResourceCache, scope *DetectScope, ns, name string) (cert certs.Input, ok, readable bool) {
	type parsed struct {
		cert         certs.Input
		ok, readable bool
	}
	p := scope.get("tlsCert\x00"+ns+"/"+name, func() any {
		secLister := cache.Secrets()
		if secLister == nil || !cache.KindCoversNamespace("secrets", ns) {
			return parsed{}
		}
		secret, err := secLister.Secrets(ns).Get(name)
		if err != nil || secret.DeletionTimestamp != nil {
			return parsed{readable: true}
		}
		c, ok := certs.FromTLSSecret(secret)
		return parsed{cert: c, ok: ok, readable: true}
	}).(parsed)
	return p.cert, p.ok, p.readable
}

func tlsCertificateValid(cert certs.Input, now time.Time) bool {
	if cert.NotAfter == nil || !now.Before(*cert.NotAfter) {
		return false
	}
	return cert.NotBefore == nil || !cert.NotBefore.After(now.Add(tlsNotYetValidTolerance))
}

// tlsCertificateCovers matches a host against the certificate's names, a
// wildcard name covering exactly one leading label.
func tlsCertificateCovers(cert certs.Input, host string) bool {
	for _, name := range cert.Domains {
		name = normalizeTLSHost(name)
		if name == host || tlsWildcardCovers(name, host) {
			return true
		}
	}
	return false
}

// tlsCertificateNames reports whether the certificate names host exactly,
// not through a wildcard.
func tlsCertificateNames(cert certs.Input, host string) bool {
	for _, name := range cert.Domains {
		if normalizeTLSHost(name) == host {
			return true
		}
	}
	return false
}

func appendUniqueRef(refs []issuesapi.Ref, ref issuesapi.Ref) []issuesapi.Ref {
	for _, r := range refs {
		if r == ref {
			return refs
		}
	}
	return append(refs, ref)
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

// tlsIngressController returns the controller of a live TLS Ingress whose
// own Secrets are what clients see, and whether it has an address. An
// unresolved class gets a stand-in key from its legacy class name.
func tlsIngressController(cache *ResourceCache, ing *networkingv1.Ingress) (controller string, addressed, ok bool) {
	if ing.DeletionTimestamp != nil || len(ing.Spec.TLS) == 0 || ingressTLSServedElsewhere(ing) {
		return "", false, false
	}
	addressed = ingressstatus.HasAssignedAddress(ing)
	controller, ok = ingressController(cache, ing)
	if !ok && addressed && unresolvedClassOutsideALB(cache, ing) {
		// Ingresses sharing a legacy class name share a controller.
		return "legacy-class:" + ingressstatus.LegacyClass(ing), true, true
	}
	return controller, addressed, ok && controller != albIngressController
}

func tlsCertificateAction(secret *corev1.Secret) string {
	renew := "if cert-manager issues it, check that Certificate's status and events"
	if name := strings.TrimSpace(secret.Annotations["cert-manager.io/certificate-name"]); name != "" {
		renew = fmt.Sprintf("cert-manager issues it from Certificate %s/%s, so check that Certificate's status and events", secret.Namespace, name)
	}
	return fmt.Sprintf("Replace the certificate in Secret %s/%s with a valid one; %s. To see what clients receive: openssl s_client -connect <ingress address>:443 -servername <host> </dev/null | openssl x509 -noout -dates", secret.Namespace, secret.Name, renew)
}

func normalizeTLSHost(host string) string {
	return strings.ToLower(strings.TrimSpace(host))
}

// tlsWildcardCovers matches a wildcard name against a host, covering exactly
// one leading label.
func tlsWildcardCovers(name, host string) bool {
	suffix, ok := strings.CutPrefix(name, "*.")
	if !ok {
		return false
	}
	label, rest, found := strings.Cut(host, ".")
	return found && label != "" && rest == suffix
}

// ingressTLSServedElsewhere reports an Ingress whose own TLS Secret is not the
// certificate clients see: SSL passthrough hands TLS to the backend, an
// ingress-nginx canary takes its TLS from the primary Ingress, Contour's
// tls-cert-namespace reads the Secret from another namespace, and GKE prefers
// pre-shared and Google-managed certificates over the Secret.
func ingressTLSServedElsewhere(ing *networkingv1.Ingress) bool {
	if ingressSSLPassthrough(ing) || strings.EqualFold(strings.TrimSpace(ing.Annotations["nginx.ingress.kubernetes.io/canary"]), "true") {
		return true
	}
	for _, key := range tlsElsewhereAnnotations {
		if strings.TrimSpace(ing.Annotations[key]) != "" {
			return true
		}
	}
	return false
}

var tlsElsewhereAnnotations = []string{
	"projectcontour.io/tls-cert-namespace",
	"ingress.gcp.kubernetes.io/pre-shared-cert",
	"networking.gke.io/managed-certificates",
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

// readyControllerPodNamespaces returns the namespaces of the controller's
// ready pods, or nil when none can be seen.
func readyControllerPodNamespaces(cache *ResourceCache, scope *DetectScope, controller string) []string {
	return scope.get("controllerPods\x00"+controller, func() any {
		return scanReadyControllerPodNamespaces(cache, controller)
	}).([]string)
}

func scanReadyControllerPodNamespaces(cache *ResourceCache, controller string) []string {
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
	user := "an Ingress references it for TLS and its ingress controller is running, so it is likely, not confirmed, served"
	severity := "warning"
	if addressed {
		user = "an Ingress with a load-balancer address references it for TLS"
		severity = "critical"
	}
	// The hostnames come from the certificate, which the caller reads with the
	// Secret, so they name nothing the row's Secret access doesn't already show.
	subject := "The certificate in this Secret"
	if hosts := cert.Domains; len(hosts) > 0 {
		listed := strings.Join(hosts[:min(len(hosts), maxTLSMessageHosts)], ", ")
		if extra := len(hosts) - maxTLSMessageHosts; extra > 0 {
			listed += fmt.Sprintf(" (+%d more)", extra)
		}
		subject = "The certificate for " + listed + " in this Secret"
	}
	var reason, fingerprint, message, cause string
	switch {
	case cert.NotAfter != nil && now.After(*cert.NotAfter):
		reason, fingerprint = TLSCertificateExpiredReason, "tls-certificate-expired"
		message = fmt.Sprintf("%s expired %s ago (%s), and %s.",
			subject, timeutil.FormatAgeShort(now.Sub(*cert.NotAfter)), cert.NotAfter.UTC().Format(time.RFC3339), user)
		cause = "The certificate's validity period has ended. The ingress controller either serves it, and clients that verify certificates reject the connection, or falls back to its default certificate, which those clients also reject unless it happens to cover this host."
	case cert.NotBefore != nil && cert.NotBefore.After(now.Add(tlsNotYetValidTolerance)):
		reason, fingerprint = TLSCertificateNotYetValidReason, "tls-certificate-not-yet-valid"
		message = fmt.Sprintf("%s is not valid until %s (in %s), and %s.",
			subject, cert.NotBefore.UTC().Format(time.RFC3339), timeutil.FormatAgeShort(cert.NotBefore.Sub(now)), user)
		cause = "The certificate's validity period has not started, often because it was issued with a wrong clock or start date. Clients that verify certificates reject it until then."
	case cert.NotAfter != nil && cert.NotAfter.Sub(now) <= tlsExpiringWindow:
		// Nothing fails yet, so this is a warning however certain the serving is.
		reason, fingerprint, severity = TLSCertificateExpiringReason, "tls-certificate-expiring", "warning"
		message = fmt.Sprintf("%s expires in %s (%s), and %s.",
			subject, timeutil.FormatAgeShort(cert.NotAfter.Sub(now)), cert.NotAfter.UTC().Format(time.RFC3339), user)
		cause = "The certificate is still valid but expires within a day. Unless it is renewed first, clients that verify certificates start rejecting the connection at that time."
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
		Action:            tlsCertificateAction(secret),
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
