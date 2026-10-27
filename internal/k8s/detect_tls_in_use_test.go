package k8s

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/k8score"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func testCertPEM(t *testing.T, notBefore, notAfter time.Time) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "shop.example.test"},
		DNSNames:     []string{"shop.example.test"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func tlsSecret(ns, name string, cert []byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, CreationTimestamp: metav1.NewTime(time.Now().Add(-48 * time.Hour))},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{"tls.crt": cert, "tls.key": []byte("unused")},
	}
}

type tlsIngressOpt func(*networkingv1.Ingress)

func withAddress(ing *networkingv1.Ingress) {
	ing.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{{IP: "203.0.113.10"}}
}

func withAnnotation(key, value string) tlsIngressOpt {
	return func(ing *networkingv1.Ingress) {
		if ing.Annotations == nil {
			ing.Annotations = map[string]string{}
		}
		ing.Annotations[key] = value
	}
}

func withClass(class string) tlsIngressOpt {
	return func(ing *networkingv1.Ingress) { ing.Spec.IngressClassName = &class }
}

func tlsIngress(ns, name string, secrets []string, opts ...tlsIngressOpt) *networkingv1.Ingress {
	ing := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour))}}
	for _, s := range secrets {
		ing.Spec.TLS = append(ing.Spec.TLS, networkingv1.IngressTLS{Hosts: []string{"shop.example.test"}, SecretName: s})
	}
	for _, opt := range opts {
		opt(ing)
	}
	return ing
}

func TestDetectLiveIngressTLSCertificates(t *testing.T) {
	defer ResetTestState()

	now := time.Now()
	expired := testCertPEM(t, now.Add(-90*24*time.Hour), now.Add(-24*time.Hour))
	future := testCertPEM(t, now.Add(48*time.Hour), now.Add(90*24*time.Hour))
	justIssued := testCertPEM(t, now.Add(time.Minute), now.Add(90*24*time.Hour))
	valid := testCertPEM(t, now.Add(-time.Hour), now.Add(10*24*time.Hour))
	nginx := withClass("nginx")
	objects := []runtime.Object{
		&networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: "nginx"}, Spec: networkingv1.IngressClassSpec{Controller: "k8s.io/ingress-nginx"}},
		&networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: "aws"}, Spec: networkingv1.IngressClassSpec{Controller: "ingress.k8s.aws/alb"}},
		tlsSecret("shop", "expired-tls", expired),
		tlsSecret("shop", "future-tls", future),
		tlsSecret("shop", "just-issued-tls", justIssued),
		tlsSecret("shop", "valid-tls", valid),
		tlsSecret("shop", "unused-expired-tls", expired),
		tlsSecret("shop", "alb-expired-tls", expired),
		tlsSecret("shop", "pending-expired-tls", expired),
		tlsSecret("shop", "unknown-class-expired-tls", expired),
		tlsSecret("shop", "passthrough-expired-tls", expired),
		tlsSecret("shop", "legacy-match-expired-tls", expired),
		tlsSecret("shop", "legacy-conflict-expired-tls", expired),
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "opaque", Namespace: "shop"}, Type: corev1.SecretTypeOpaque, Data: map[string][]byte{"tls.crt": expired}},
		// Two live Ingresses share one expired Secret: one row, both linked.
		tlsIngress("shop", "web", []string{"expired-tls", "expired-tls"}, nginx, withAddress),
		tlsIngress("shop", "api", []string{"expired-tls", "valid-tls"}, nginx, withAddress),
		tlsIngress("shop", "early", []string{"future-tls", "just-issued-tls"}, nginx, withAddress),
		tlsIngress("shop", "alb", []string{"alb-expired-tls"}, withClass("aws"), withAddress),
		// No address: no controller has picked it up, so it is not live.
		tlsIngress("shop", "pending", []string{"pending-expired-tls"}, nginx),
		// Class not installed: can't tell which controller serves it.
		tlsIngress("shop", "orphan", []string{"unknown-class-expired-tls"}, withClass("missing"), withAddress),
		tlsIngress("shop", "opaque", []string{"opaque", "missing-tls"}, nginx, withAddress),
		// TLS goes to the backend: the Ingress's Secret is not what clients see.
		tlsIngress("shop", "passthrough", []string{"passthrough-expired-tls"}, nginx, withAddress, withAnnotation("nginx.ingress.kubernetes.io/ssl-passthrough", "true")),
		// A legacy annotation naming the same class as the field is unambiguous.
		tlsIngress("shop", "legacy-match", []string{"legacy-match-expired-tls"}, nginx, withAddress, withAnnotation("kubernetes.io/ingress.class", "nginx")),
		// Controllers disagree on which of two different classes wins.
		tlsIngress("shop", "legacy-conflict", []string{"legacy-conflict-expired-tls"}, nginx, withAddress, withAnnotation("kubernetes.io/ingress.class", "other")),
	}
	if err := InitTestResourceCache(fake.NewClientset(objects...)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}

	got := detectLiveIngressTLSCertificates(GetResourceCache(), "", now)
	if len(got) != 3 || got[2].Name != "legacy-match-expired-tls" {
		t.Fatalf("detections = %+v, want expired-tls, future-tls and legacy-match-expired-tls only", got)
	}
	exp, early := got[0], got[1]
	if exp.Kind != "Secret" || exp.Namespace != "shop" || exp.Name != "expired-tls" || exp.Reason != TLSCertificateExpiredReason || exp.Severity != "critical" {
		t.Fatalf("expired detection = %+v", exp)
	}
	wantRefs := []issuesapi.Ref{
		{Group: "networking.k8s.io", Kind: "Ingress", Namespace: "shop", Name: "api"},
		{Group: "networking.k8s.io", Kind: "Ingress", Namespace: "shop", Name: "web"},
	}
	if len(exp.AffectedRefs) != 2 || exp.AffectedRefs[0] != wantRefs[0] || exp.AffectedRefs[1] != wantRefs[1] {
		t.Fatalf("affected refs = %+v, want %+v", exp.AffectedRefs, wantRefs)
	}
	if !exp.OnsetUnknown || !exp.OnsetAt.IsZero() || exp.Duration != "" {
		t.Fatalf("expiry time is not the failure onset: %+v", exp)
	}
	if len(exp.RequiredReads) != 0 {
		t.Fatalf("an addressed consumer needs no controller-pod evidence: %+v", exp.RequiredReads)
	}
	if !strings.Contains(exp.Message, "expired 1d ago") || strings.Contains(exp.Message, "web") || strings.Contains(exp.Message, "api") {
		t.Fatalf("message must give the expiry and name no Ingress: %q", exp.Message)
	}
	if early.Name != "future-tls" || early.Reason != TLSCertificateNotYetValidReason || !strings.Contains(early.Message, "not valid until") {
		t.Fatalf("not-yet-valid detection = %+v", early)
	}
	if early.Fingerprint == exp.Fingerprint {
		t.Fatalf("expired and not-yet-valid must be distinct causes: %q", early.Fingerprint)
	}
}

func TestDetectLiveIngressTLSCertificatesSkipsUncoveredSecretNamespace(t *testing.T) {
	defer ResetTestState()

	now := time.Now()
	expired := testCertPEM(t, now.Add(-90*24*time.Hour), now.Add(-time.Hour))
	objects := []runtime.Object{
		&networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: "nginx"}, Spec: networkingv1.IngressClassSpec{Controller: "k8s.io/ingress-nginx"}},
		tlsSecret("shop", "expired-tls", expired),
		tlsSecret("other", "expired-tls", expired),
		tlsIngress("shop", "web", []string{"expired-tls"}, withClass("nginx"), withAddress),
		tlsIngress("other", "web", []string{"expired-tls"}, withClass("nginx"), withAddress),
	}
	scopes := map[string]k8score.ResourceScope{
		"ingresses":      {Enabled: true},
		"ingressclasses": {Enabled: true},
		"secrets":        {Enabled: true, Namespace: "shop"},
	}
	if err := InitScopedTestResourceCache(fake.NewClientset(objects...), scopes); err != nil {
		t.Fatalf("InitScopedTestResourceCache: %v", err)
	}
	got := detectLiveIngressTLSCertificates(GetResourceCache(), "", now)
	if len(got) != 1 || got[0].Namespace != "shop" {
		t.Fatalf("detections = %+v, want only the namespace the Secret informer covers", got)
	}
}

func readyPod(ns, name string, podLabels map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: podLabels},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}
}

// A controller that publishes no address (Traefik behind a ClusterIP Service)
// still counts when its pods are running, as a warning whose evidence includes
// those pods. A known controller with no ready pod, or one Radar doesn't know
// how to find, does not.
func TestDetectLiveIngressTLSCertificatesRunningControllerWithoutAddress(t *testing.T) {
	defer ResetTestState()

	now := time.Now()
	expired := testCertPEM(t, now.Add(-90*24*time.Hour), now.Add(-time.Hour))
	traefikLabels := map[string]string{"app.kubernetes.io/name": "traefik"}
	notReady := readyPod("edge", "nginx-0", map[string]string{"app.kubernetes.io/name": "ingress-nginx", "app.kubernetes.io/component": "controller"})
	notReady.Status.Conditions[0].Status = corev1.ConditionFalse
	objects := []runtime.Object{
		&networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: "traefik"}, Spec: networkingv1.IngressClassSpec{Controller: "traefik.io/ingress-controller"}},
		&networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: "nginx"}, Spec: networkingv1.IngressClassSpec{Controller: "k8s.io/ingress-nginx"}},
		&networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: "custom"}, Spec: networkingv1.IngressClassSpec{Controller: "example.com/custom"}},
		readyPod("traefik", "traefik-0", traefikLabels),
		notReady,
		tlsSecret("shop", "traefik-tls", expired),
		tlsSecret("shop", "mixed-tls", expired),
		tlsSecret("shop", "nginx-tls", expired),
		tlsSecret("shop", "custom-tls", expired),
		tlsIngress("shop", "via-traefik", []string{"traefik-tls", "mixed-tls"}, withClass("traefik")),
		tlsIngress("shop", "addressed", []string{"mixed-tls"}, withClass("custom"), withAddress),
		tlsIngress("shop", "via-nginx", []string{"nginx-tls"}, withClass("nginx")),
		tlsIngress("shop", "via-custom", []string{"custom-tls"}, withClass("custom")),
	}
	if err := InitTestResourceCache(fake.NewClientset(objects...)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	got := detectLiveIngressTLSCertificates(GetResourceCache(), "", now)
	if len(got) != 2 {
		t.Fatalf("detections = %+v, want mixed-tls and traefik-tls", got)
	}
	mixed, traefik := got[0], got[1]
	// The address-less consumer stays unlinked: linking it would rest on
	// controller pods a caller of the critical row may not be allowed to see.
	if mixed.Name != "mixed-tls" || mixed.Severity != "critical" || len(mixed.RequiredReads) != 0 || len(mixed.AffectedRefs) != 1 || mixed.AffectedRefs[0].Name != "addressed" {
		t.Fatalf("a Secret any addressed Ingress uses is critical and links only addressed Ingresses: %+v", mixed)
	}
	if traefik.Name != "traefik-tls" || traefik.Severity != "warning" || !strings.Contains(traefik.Message, "controller is running") || !strings.Contains(traefik.Cause, "not on the controller having picked it up") {
		t.Fatalf("running-controller detection = %+v", traefik)
	}
	if len(traefik.RequiredReads) != 1 || traefik.RequiredReads[0] != (issuesapi.EvidenceRead{Resource: "pods", Namespace: "traefik", Verb: "list"}) {
		t.Fatalf("controller pods must be required evidence: %+v", traefik.RequiredReads)
	}
}
