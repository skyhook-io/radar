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

	"github.com/skyhook-io/radar/pkg/certs"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/k8score"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

// testCertPEM issues a certificate for names, by default *.example.test,
// which covers every host tlsIngress gives an Ingress.
func testCertPEM(t *testing.T, notBefore, notAfter time.Time, names ...string) []byte {
	t.Helper()
	if len(names) == 0 {
		names = []string{"*.example.test"}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: names[0]},
		DNSNames:     names,
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
	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour))},
		Spec:       networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{Host: name + ".example.test"}}},
	}
	for _, s := range secrets {
		ing.Spec.TLS = append(ing.Spec.TLS, networkingv1.IngressTLS{Hosts: []string{name + ".example.test"}, SecretName: s})
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
	// Valid certificates name hosts no Ingress here serves, so they don't
	// contest the expired ones.
	justIssued := testCertPEM(t, now.Add(time.Minute), now.Add(90*24*time.Hour), "just-issued.example.test")
	valid := testCertPEM(t, now.Add(-time.Hour), now.Add(10*24*time.Hour), "valid.example.test")
	nginx := withClass("nginx")
	objects := []runtime.Object{
		readyPod("ingress-nginx", "controller-0", nginxPodLabels),
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
		tlsSecret("shop", "legacy-only-expired-tls", expired),
		tlsSecret("shop", "empty-status-expired-tls", expired),
		tlsSecret("shop", "foreign-passthrough-expired-tls", expired),
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "opaque", Namespace: "shop"}, Type: corev1.SecretTypeOpaque, Data: map[string][]byte{"tls.crt": expired}},
		// Two live Ingresses share one expired Secret: one row, both linked.
		tlsIngress("shop", "web", []string{"expired-tls", "expired-tls"}, nginx, withAddress),
		tlsIngress("shop", "api", []string{"expired-tls", "valid-tls"}, nginx, withAddress),
		tlsIngress("shop", "early", []string{"future-tls", "just-issued-tls"}, nginx, withAddress),
		tlsIngress("shop", "alb", []string{"alb-expired-tls"}, withClass("aws"), withAddress),
		// No address, but nginx is running: a warning.
		tlsIngress("shop", "pending", []string{"pending-expired-tls"}, nginx),
		// Class not installed: can't tell which controller serves it.
		tlsIngress("shop", "orphan", []string{"unknown-class-expired-tls"}, withClass("missing"), withAddress),
		tlsIngress("shop", "opaque", []string{"opaque", "missing-tls"}, nginx, withAddress),
		// TLS goes to the backend: the Ingress's Secret is not what clients see.
		tlsIngress("shop", "passthrough", []string{"passthrough-expired-tls"}, nginx, withAddress, withAnnotation("nginx.ingress.kubernetes.io/ssl-passthrough", "true")),
		// A legacy annotation naming the same class as the field is unambiguous.
		tlsIngress("shop", "legacy-match", []string{"legacy-match-expired-tls"}, nginx, withAddress, withAnnotation("kubernetes.io/ingress.class", "nginx")),
		// A legacy annotation alone resolves through the IngressClass of that name.
		tlsIngress("shop", "legacy-only", []string{"legacy-only-expired-tls"}, withAddress, withAnnotation("kubernetes.io/ingress.class", "nginx")),
		// An empty status entry is no address: a warning, not critical.
		tlsIngress("shop", "empty-status", []string{"empty-status-expired-tls"}, nginx, func(ing *networkingv1.Ingress) {
			ing.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{{}}
		}),
		// Only the controllers' own passthrough annotations hand TLS to the backend.
		tlsIngress("shop", "foreign-passthrough", []string{"foreign-passthrough-expired-tls"}, nginx, withAddress, withAnnotation("example.com/ssl-passthrough", "true")),
		// Controllers disagree on which of two different classes wins.
		tlsIngress("shop", "legacy-conflict", []string{"legacy-conflict-expired-tls"}, nginx, withAddress, withAnnotation("kubernetes.io/ingress.class", "other")),
	}
	if err := InitTestResourceCache(fake.NewClientset(objects...)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}

	got := detectLiveIngressTLSCertificates(GetResourceCache(), "", nil, now)
	// A compose's scope, shared across namespaces, gives the same answer.
	scope := NewDetectScope()
	if scoped := detectLiveIngressTLSCertificates(GetResourceCache(), "shop", scope, now); len(scoped) != len(got) {
		t.Fatalf("scoped detections = %d, want %d", len(scoped), len(got))
	}
	if again := detectLiveIngressTLSCertificates(GetResourceCache(), "other", scope, now); len(again) != 0 {
		t.Fatalf("other namespace through the same scope = %+v", again)
	}
	var names []string
	for _, d := range got {
		names = append(names, d.Name)
	}
	if strings.Join(names, ",") != "empty-status-expired-tls,expired-tls,foreign-passthrough-expired-tls,future-tls,legacy-match-expired-tls,legacy-only-expired-tls,pending-expired-tls" {
		t.Fatalf("detections = %v", names)
	}
	emptyStatus, exp, early, pending := got[0], got[1], got[3], got[6]
	if emptyStatus.Severity != "warning" || pending.Severity != "warning" {
		t.Fatalf("Ingresses without an address are warnings: %+v, %+v", emptyStatus, pending)
	}
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

// A Secret outside the Secret informer is never reported, and since it could
// hold a valid certificate for any host, it contests every Secret the same
// controller serves; another controller's rows are unaffected.
func TestDetectLiveIngressTLSCertificatesUncoveredSecretNamespace(t *testing.T) {
	defer ResetTestState()

	now := time.Now()
	expired := testCertPEM(t, now.Add(-90*24*time.Hour), now.Add(-time.Hour))
	objects := []runtime.Object{
		&networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: "nginx"}, Spec: networkingv1.IngressClassSpec{Controller: "k8s.io/ingress-nginx"}},
		&networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: "custom"}, Spec: networkingv1.IngressClassSpec{Controller: "example.com/custom"}},
		readyPod("ingress-nginx", "controller-0", nginxPodLabels),
		tlsSecret("shop", "custom-tls", expired),
		tlsIngress("shop", "custom", []string{"custom-tls"}, withClass("custom"), withAddress),
		tlsSecret("shop", "expired-tls", expired),
		tlsSecret("other", "expired-tls", expired),
		tlsIngress("shop", "web", []string{"expired-tls"}, withClass("nginx"), withAddress),
		tlsIngress("other", "admin", []string{"expired-tls"}, withClass("nginx"), withAddress),
	}
	scopes := map[string]k8score.ResourceScope{
		"ingresses":      {Enabled: true},
		"ingressclasses": {Enabled: true},
		"pods":           {Enabled: true},
		"secrets":        {Enabled: true, Namespace: "shop"},
	}
	if err := InitScopedTestResourceCache(fake.NewClientset(objects...), scopes); err != nil {
		t.Fatalf("InitScopedTestResourceCache: %v", err)
	}
	got := detectLiveIngressTLSCertificates(GetResourceCache(), "", nil, now)
	if len(got) != 1 || got[0].Namespace != "shop" || got[0].Name != "custom-tls" {
		t.Fatalf("detections = %+v, want only the other controller's custom-tls", got)
	}
}

var nginxPodLabels = map[string]string{"app.kubernetes.io/name": "ingress-nginx", "app.kubernetes.io/component": "controller"}

func readyPod(ns, name string, podLabels map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: podLabels},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
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
	notReady := readyPod("edge", "nginx-0", nginxPodLabels)
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
		tlsSecret("shop", "stale-tls", expired),
		// An address can outlive the controller that published it: with no
		// ready nginx pod, this addressed Ingress is not live.
		tlsIngress("shop", "stale", []string{"stale-tls"}, withClass("nginx"), withAddress),
		tlsIngress("shop", "via-traefik", []string{"traefik-tls", "mixed-tls"}, withClass("traefik")),
		tlsIngress("shop", "addressed", []string{"mixed-tls"}, withClass("custom"), withAddress),
		tlsIngress("shop", "via-nginx", []string{"nginx-tls"}, withClass("nginx")),
		tlsIngress("shop", "via-custom", []string{"custom-tls"}, withClass("custom")),
	}
	if err := InitTestResourceCache(fake.NewClientset(objects...)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	got := detectLiveIngressTLSCertificates(GetResourceCache(), "", nil, now)
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

// An addressed Ingress is live even when its class resolves to no
// IngressClass object, unless ALB could be serving it or its address may be
// left over from a removed controller. A Secret counts for a host only when
// its certificate covers the host and no other certificate the controller
// could serve for it does.
func TestDetectLiveIngressTLSCertificatesSelection(t *testing.T) {
	now := time.Now()
	expired := testCertPEM(t, now.Add(-90*24*time.Hour), now.Add(-time.Hour))
	certPEMs := map[string][]byte{}
	for _, s := range []string{"gce-tls", "classless-tls", "missing-class-tls", "alb-legacy-tls", "canary-tls", "delegated-tls", "shadowed-tls", "gke-managed-tls", "wildcard-tls", "exact-tls", "store-a-tls"} {
		certPEMs[s] = expired
	}
	// A certificate that doesn't cover the rule's host is not presented for it.
	certPEMs["orphan-tls"] = testCertPEM(t, now.Add(-90*24*time.Hour), now.Add(-time.Hour), "old.example.test")
	// A valid certificate for the host, in the same Ingress or another one the
	// same controller serves, may be the one presented instead.
	certPEMs["valid-first-tls"] = testCertPEM(t, now.Add(-time.Hour), now.Add(90*24*time.Hour), "shadowed.example.test")
	certPEMs["store-b-tls"] = testCertPEM(t, now.Add(-time.Hour), now.Add(90*24*time.Hour), "store-a.example.test", "store-b.example.test")

	orphan := tlsIngress("shop", "orphan", []string{"orphan-tls"}, withClass("nginx"), withAddress)
	shadowed := tlsIngress("shop", "shadowed", []string{"valid-first-tls", "shadowed-tls"}, withClass("nginx"), withAddress)
	// A valid wildcard doesn't contest a certificate naming the host
	// exactly (store-based controllers prefer the exact name), unless it is
	// declared for that host too: ingress-nginx serves the first entry
	// declared for the host.
	certPEMs["named-tls"] = testCertPEM(t, now.Add(-90*24*time.Hour), now.Add(-time.Hour), "named.other.test")
	certPEMs["declared-tls"] = testCertPEM(t, now.Add(-90*24*time.Hour), now.Add(-time.Hour), "declared.other.test")
	certPEMs["valid-wild-tls"] = testCertPEM(t, now.Add(-time.Hour), now.Add(90*24*time.Hour), "*.other.test")
	named := tlsIngress("shop", "named", []string{"named-tls"}, withClass("nginx"), withAddress)
	named.Spec.Rules[0].Host = "named.other.test"
	wildElsewhere := tlsIngress("shop", "wild-elsewhere", []string{"valid-wild-tls"}, withClass("nginx"), withAddress)
	wildElsewhere.Spec.Rules[0].Host = "elsewhere.other.test"
	declared := tlsIngress("shop", "declared", []string{"valid-wild-tls", "declared-tls"}, withClass("nginx"), withAddress)
	declared.Spec.Rules[0].Host = "declared.other.test"
	// A Secret declared for the same host that doesn't exist may send the
	// controller to its default certificate.
	exact := tlsIngress("shop", "exact", []string{"missing-tls", "exact-tls"}, withClass("nginx"), withAddress)
	wildcard := tlsIngress("shop", "wildcard", []string{"wildcard-tls"}, withClass("nginx"), withAddress)
	storeA := tlsIngress("shop", "store-a", []string{"store-a-tls"}, withClass("nginx"), withAddress)
	storeB := tlsIngress("shop", "store-b", []string{"store-b-tls"}, withClass("nginx"), withAddress)
	base := func() []runtime.Object {
		objects := []runtime.Object{
			&networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: "nginx"}, Spec: networkingv1.IngressClassSpec{Controller: "k8s.io/ingress-nginx"}},
			readyPod("ingress-nginx", "controller-0", nginxPodLabels),
			tlsIngress("shop", "gce", []string{"gce-tls"}, withAddress, withAnnotation("kubernetes.io/ingress.class", "gce")),
			tlsIngress("shop", "classless", []string{"classless-tls"}, withAddress),
			tlsIngress("shop", "missing-class", []string{"missing-class-tls"}, withClass("removed"), withAddress),
			tlsIngress("shop", "alb-legacy", []string{"alb-legacy-tls"}, withAddress, withAnnotation("kubernetes.io/ingress.class", "alb")),
			tlsIngress("shop", "canary", []string{"canary-tls"}, withClass("nginx"), withAddress, withAnnotation("nginx.ingress.kubernetes.io/canary", "true")),
			tlsIngress("shop", "delegated", []string{"delegated-tls"}, withClass("nginx"), withAddress, withAnnotation("projectcontour.io/tls-cert-namespace", "certs")),
			orphan.DeepCopy(), shadowed.DeepCopy(), exact.DeepCopy(), wildcard.DeepCopy(), storeA.DeepCopy(), storeB.DeepCopy(), named.DeepCopy(), wildElsewhere.DeepCopy(), declared.DeepCopy(),
			tlsIngress("shop", "gke-managed", []string{"gke-managed-tls"}, withAddress, withAnnotation("kubernetes.io/ingress.class", "gce"), withAnnotation("networking.gke.io/managed-certificates", "shop-cert")),
		}
		for name, cert := range certPEMs {
			objects = append(objects, tlsSecret("shop", name, cert))
		}
		return objects
	}
	detect := func(t *testing.T, objects []runtime.Object) string {
		t.Helper()
		defer ResetTestState()
		if err := InitTestResourceCache(fake.NewClientset(objects...)); err != nil {
			t.Fatalf("InitTestResourceCache: %v", err)
		}
		var names []string
		for _, d := range detectLiveIngressTLSCertificates(GetResourceCache(), "", nil, now) {
			if d.Severity != "critical" {
				t.Errorf("%s severity = %s, want critical", d.Name, d.Severity)
			}
			names = append(names, d.Name)
		}
		return strings.Join(names, ",")
	}
	if got := detect(t, base()); got != "classless-tls,gce-tls,named-tls,wildcard-tls" {
		t.Fatalf("detections = %s, want the legacy-gce, class-less, exactly named and wildcard Ingresses only", got)
	}
	withALBClass := append(base(), &networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: "aws"}, Spec: networkingv1.IngressClassSpec{Controller: "ingress.k8s.aws/alb"}})
	// ALB can claim a legacy class of any name and class-less Ingresses.
	if got := detect(t, withALBClass); got != "named-tls,wildcard-tls" {
		t.Fatalf("with an ALB class installed, detections = %s, want only the nginx-class named-tls and wildcard-tls", got)
	}
}

func certsInput(names ...string) certs.Input {
	return certs.Input{Domains: names}
}

func TestTLSCertificateCovers(t *testing.T) {
	cert := certsInput("*.example.test", "SHOP.other.test")
	for _, tc := range []struct {
		host string
		want bool
	}{
		{"api.example.test", true},
		{"shop.other.test", true},
		{"a.b.example.test", false},
		{"example.test", false},
		{"other.test", false},
	} {
		if got := tlsCertificateCovers(cert, tc.host); got != tc.want {
			t.Errorf("tlsCertificateCovers(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

// Several Ingresses defining TLS for one host leave the choice to the
// controller (ingress-nginx takes the oldest Ingress's, Traefik the first
// certificate in its store), so an expired Secret there counts only when no
// valid certificate for the host competes, whichever Ingress is older and
// whatever namespace it lives in. Ingresses presenting one Secret for one
// host all use it.
func TestDetectLiveIngressTLSCertificatesSharedHost(t *testing.T) {
	defer ResetTestState()

	now := time.Now()
	expired := testCertPEM(t, now.Add(-90*24*time.Hour), now.Add(-time.Hour))
	created := func(age time.Duration) tlsIngressOpt {
		return func(ing *networkingv1.Ingress) { ing.CreationTimestamp = metav1.NewTime(now.Add(-age)) }
	}
	sameHost := func(host string) tlsIngressOpt {
		return func(ing *networkingv1.Ingress) {
			ing.Spec.Rules[0].Host = host
			ing.Spec.TLS[0].Hosts = []string{host}
		}
	}
	nginx := withClass("nginx")
	objects := []runtime.Object{
		&networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: "nginx"}, Spec: networkingv1.IngressClassSpec{Controller: "k8s.io/ingress-nginx"}},
		readyPod("ingress-nginx", "controller-0", nginxPodLabels),
		tlsSecret("shop", "old-valid-tls", testCertPEM(t, now.Add(-time.Hour), now.Add(10*24*time.Hour), "shop.example.test")),
		tlsSecret("shop", "new-expired-tls", expired),
		tlsSecret("other", "old-expired-tls", expired),
		tlsSecret("shop", "new-valid-tls", testCertPEM(t, now.Add(-time.Hour), now.Add(10*24*time.Hour), "api.example.test")),
		tlsSecret("shop", "twin-expired-tls", expired),
		tlsIngress("shop", "old", []string{"old-valid-tls"}, nginx, withAddress, created(48*time.Hour), sameHost("shop.example.test")),
		tlsIngress("shop", "new", []string{"new-expired-tls"}, nginx, withAddress, created(time.Hour), sameHost("shop.example.test")),
		tlsIngress("other", "older", []string{"old-expired-tls"}, nginx, withAddress, created(72*time.Hour), sameHost("api.example.test")),
		tlsIngress("shop", "newer", []string{"new-valid-tls"}, nginx, withAddress, created(2*time.Hour), sameHost("api.example.test")),
		tlsIngress("shop", "twin-a", []string{"twin-expired-tls"}, nginx, withAddress, created(5*time.Hour), sameHost("twin.example.test")),
		tlsIngress("shop", "twin-b", []string{"twin-expired-tls"}, nginx, withAddress, created(4*time.Hour), sameHost("twin.example.test")),
	}
	if err := InitTestResourceCache(fake.NewClientset(objects...)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	got := detectLiveIngressTLSCertificates(GetResourceCache(), "", nil, now)
	if len(got) != 1 || got[0].Name != "twin-expired-tls" || len(got[0].AffectedRefs) != 2 {
		t.Fatalf("detections = %+v, want only the twin Secret, linked twice", got)
	}
	// A namespace-filtered scan still weighs the competitor in another namespace.
	if got := detectLiveIngressTLSCertificates(GetResourceCache(), "other", nil, now); len(got) != 0 {
		t.Fatalf("other-scoped detections = %+v, want none", got)
	}
}

// A served certificate within a day of expiry is a warning, however certain
// the serving is; further out it is left to audit. Messages name the
// certificate's hosts, and the action names the cert-manager Certificate.
func TestDetectLiveIngressTLSCertificatesExpiringSoon(t *testing.T) {
	defer ResetTestState()

	now := time.Now()
	soon := tlsSecret("shop", "soon-tls", testCertPEM(t, now.Add(-60*24*time.Hour), now.Add(3*time.Hour), "soon.example.test"))
	soon.Annotations = map[string]string{"cert-manager.io/certificate-name": "soon-cert"}
	objects := []runtime.Object{
		&networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: "nginx"}, Spec: networkingv1.IngressClassSpec{Controller: "k8s.io/ingress-nginx"}},
		readyPod("ingress-nginx", "controller-0", nginxPodLabels),
		soon,
		tlsSecret("shop", "later-tls", testCertPEM(t, now.Add(-60*24*time.Hour), now.Add(48*time.Hour), "later.example.test")),
		tlsIngress("shop", "soon", []string{"soon-tls"}, withClass("nginx"), withAddress),
		tlsIngress("shop", "later", []string{"later-tls"}, withClass("nginx"), withAddress),
	}
	if err := InitTestResourceCache(fake.NewClientset(objects...)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	got := detectLiveIngressTLSCertificates(GetResourceCache(), "", nil, now)
	if len(got) != 1 {
		t.Fatalf("detections = %+v, want only soon-tls", got)
	}
	d := got[0]
	if d.Name != "soon-tls" || d.Reason != TLSCertificateExpiringReason || d.Severity != "warning" {
		t.Fatalf("expiring detection = %+v", d)
	}
	if !strings.HasPrefix(d.Message, "The certificate for soon.example.test in this Secret expires in ") {
		t.Fatalf("message = %q, want the certificate's host and the time left", d.Message)
	}
	if !strings.Contains(d.Action, "Certificate shop/soon-cert") {
		t.Fatalf("action = %q, want the cert-manager Certificate named", d.Action)
	}
}
