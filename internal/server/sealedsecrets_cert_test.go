package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	appsv1 "k8s.io/api/apps/v1"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

const (
	certProxyName = "http:sealed-secrets-controller:http"
	certProxyPath = "/api/v1/namespaces/kube-system/services/" + certProxyName + "/proxy/v1/cert.pem"
)

var sealingCertRSA = sync.OnceValues(func() ([]byte, []byte) {
	return sealingCertWithValidity(time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour))
})

func sealingCertWithValidity(notBefore, notAfter time.Time) ([]byte, []byte) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "sealed-secret"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), der
}

// fakeCertAPIServer stands in for the API server: it answers
// SubjectAccessReviews with a name-scoped proxy grant, then serves body,
// recording each request.
type fakeCertAPIServer struct {
	*httptest.Server
	grantProxyName string
	body           []byte
	status         int
	location       string

	mu       sync.Mutex
	reviews  []authv1.ResourceAttributes
	requests []*http.Request
}

func newFakeCertAPIServer(t *testing.T, body []byte) *fakeCertAPIServer {
	t.Helper()
	f := &fakeCertAPIServer{body: body, status: http.StatusOK}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/subjectaccessreviews") {
			var review authv1.SubjectAccessReview
			_ = json.NewDecoder(r.Body).Decode(&review)
			f.mu.Lock()
			if review.Spec.ResourceAttributes != nil {
				f.reviews = append(f.reviews, *review.Spec.ResourceAttributes)
			}
			f.mu.Unlock()
			review.APIVersion, review.Kind = "authorization.k8s.io/v1", "SubjectAccessReview"
			attrs := review.Spec.ResourceAttributes
			review.Status.Allowed = attrs != nil && f.grantProxyName != "" && attrs.Name == f.grantProxyName &&
				slices.Contains(review.Spec.Groups, "system:authenticated")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(review)
			return
		}
		f.mu.Lock()
		f.requests = append(f.requests, r.Clone(context.Background()))
		f.mu.Unlock()
		if r.Header.Get("Impersonate-User") != "" && r.URL.Path != "/api/v1/namespaces/kube-system/services/"+f.grantProxyName+"/proxy/v1/cert.pem" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if f.location != "" {
			w.Header().Set("Location", f.location)
		}
		w.WriteHeader(f.status)
		_, _ = w.Write(f.body)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeCertAPIServer) certRequests() []*http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*http.Request(nil), f.requests...)
}

// useFakeAPIServer points Radar's REST config and client at f, local mode.
func useFakeAPIServer(t *testing.T, f *fakeCertAPIServer) {
	t.Helper()
	t.Cleanup(k8s.SetTestLocalMode())
	cfg := &rest.Config{Host: f.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"}}
	prevCfg := k8s.SetTestConfig(cfg)
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	prevClient := k8s.SetTestClient(client)
	t.Cleanup(func() {
		k8s.SetTestConfig(prevCfg)
		k8s.SetTestClient(prevClient)
	})
}

func controllerService(ns, name string, ports ...corev1.ServicePort) *corev1.Service {
	return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}, Spec: corev1.ServiceSpec{Ports: ports}}
}

// seedSealedSecretsCertFixture: the controller at kubeseal's default location
// with its Service on port 8080 named http, plus extra objects.
func seedSealedSecretsCertFixture(t *testing.T, extra ...runtime.Object) {
	t.Helper()
	restoreFixtureCache(t)
	k8s.ResetResourceCache()
	one := int32(1)
	objs := append([]runtime.Object{
		deploy("kube-system", "sealed-secrets-controller", map[string]string{"app.kubernetes.io/name": "sealed-secrets"}, &one, 1),
		controllerService("kube-system", "sealed-secrets-controller", corev1.ServicePort{Name: "http", Port: 8080}),
	}, extra...)
	if err := k8s.InitTestResourceCache(fake.NewClientset(objs...)); err != nil {
		t.Fatalf("init cache: %v", err)
	}
	seedCNPGWorkspace(t, sealedSecretKinds)
}

func decodeCert(t *testing.T, resp *http.Response, wantStatus int) (sealedSecretsCertResponse, map[string]string) {
	t.Helper()
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		t.Fatalf("status = %d, want %d (%s)", resp.StatusCode, wantStatus, raw)
	}
	var ok sealedSecretsCertResponse
	var failure map[string]string
	if wantStatus == http.StatusOK {
		if err := json.Unmarshal(raw, &ok); err != nil {
			t.Fatalf("decode: %v (%s)", err, raw)
		}
	} else if err := json.Unmarshal(raw, &failure); err != nil {
		t.Fatalf("decode: %v (%s)", err, raw)
	}
	return ok, failure
}

func TestSealedSecretsCertWithoutAuth(t *testing.T) {
	seedSealedSecretsCertFixture(t)
	certPEM, der := sealingCertRSA()
	f := newFakeCertAPIServer(t, certPEM)
	useFakeAPIServer(t, f)

	// Nothing in the request can steer the upstream path, Service or port.
	resp, err := http.Get(testServer.URL + "/api/sealedsecrets/cert?sealedSecretNamespace=team&path=/v1/verify&service=evil&port=1")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := decodeCert(t, resp, http.StatusOK)
	sum := sha256.Sum256(der)
	if got.CertFingerprint != hex.EncodeToString(sum[:]) || got.CertPEM != string(certPEM) {
		t.Fatalf("cert = %+v, want fingerprint %x", got, sum)
	}
	if got.Controller != (sealedSecretsCertController{Namespace: "kube-system", Name: "sealed-secrets-controller"}) {
		t.Fatalf("controller = %+v", got.Controller)
	}
	reqs := f.certRequests()
	if len(reqs) != 1 || reqs[0].Method != http.MethodGet || reqs[0].URL.Path != certProxyPath || reqs[0].URL.RawQuery != "" {
		t.Fatalf("upstream requests = %v, want one GET %s", requestPaths(reqs), certProxyPath)
	}
	if reqs[0].Header.Get("Impersonate-User") != "" {
		t.Errorf("no-auth request impersonated %q", reqs[0].Header.Get("Impersonate-User"))
	}
}

func TestSealedSecretsCertInClusterDialsTheService(t *testing.T) {
	seedSealedSecretsCertFixture(t)
	certPEM, _ := sealingCertRSA()
	f := newFakeCertAPIServer(t, certPEM)
	prevForce := k8s.ForceInCluster
	k8s.ForceInCluster = true
	prevDial := sealedSecretsCertDialContext
	var dialed string
	sealedSecretsCertDialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialed = addr
		return (&net.Dialer{}).DialContext(ctx, network, f.Listener.Addr().String())
	}
	t.Cleanup(func() {
		k8s.ForceInCluster = prevForce
		sealedSecretsCertDialContext = prevDial
	})

	resp, err := http.Get(testServer.URL + "/api/sealedsecrets/cert")
	if err != nil {
		t.Fatal(err)
	}
	decodeCert(t, resp, http.StatusOK)
	reqs := f.certRequests()
	if dialed != "sealed-secrets-controller.kube-system.svc:8080" || len(reqs) != 1 || reqs[0].URL.Path != sealedSecretsCertPath {
		t.Fatalf("dialed %q, requests %v; want the Service's /v1/cert.pem", dialed, requestPaths(reqs))
	}
	if reqs[0].Header.Get("Authorization") != "" {
		t.Errorf("direct dial sent credentials")
	}
}

func TestSealedSecretsCertNeverGuessesTheController(t *testing.T) {
	label := map[string]string{"app.kubernetes.io/name": "sealed-secrets"}
	for _, tc := range []struct {
		name   string
		target string
		extra  []runtime.Object
		status int
		code   string
	}{
		{"several equal candidates", "", []runtime.Object{deploy("kube-system", "other-sealed-secrets", label, nil, 1)}, http.StatusConflict, "ambiguous"},
		{"a candidate whose scope can't be read", "team", []runtime.Object{labelSelectorController("team", "sealed-secrets")}, http.StatusConflict, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Without a kube-system default the two kube-system candidates tie.
			restoreFixtureCache(t)
			k8s.ResetResourceCache()
			objs := append([]runtime.Object{controllerService("kube-system", "sealed-secrets-controller", corev1.ServicePort{Name: "http", Port: 8080})}, tc.extra...)
			if tc.target == "" {
				objs = append(objs, deploy("kube-system", "sealed-secrets", label, nil, 1))
			} else {
				objs = append(objs, deploy("kube-system", "sealed-secrets-controller", label, nil, 1))
			}
			if err := k8s.InitTestResourceCache(fake.NewClientset(objs...)); err != nil {
				t.Fatalf("init cache: %v", err)
			}
			seedCNPGWorkspace(t, sealedSecretKinds)
			certPEM, _ := sealingCertRSA()
			f := newFakeCertAPIServer(t, certPEM)
			useFakeAPIServer(t, f)

			resp, err := http.Get(testServer.URL + "/api/sealedsecrets/cert?sealedSecretNamespace=" + tc.target)
			if err != nil {
				t.Fatal(err)
			}
			_, failure := decodeCert(t, resp, tc.status)
			if failure["error_code"] != tc.code || failure["error"] == "" {
				t.Fatalf("failure = %v, want error_code %q", failure, tc.code)
			}
			if reqs := f.certRequests(); len(reqs) != 0 {
				t.Fatalf("fetched %v for an unresolved controller", requestPaths(reqs))
			}
		})
	}
}

func labelSelectorController(ns, name string) *appsv1.Deployment {
	d := deploy(ns, name, map[string]string{"app.kubernetes.io/name": "sealed-secrets"}, nil, 1)
	d.Spec.Template.Spec.Containers = []corev1.Container{{Name: "controller", Image: "sealed-secrets-controller", Args: []string{"--label-selector=team=a"}}}
	return d
}

func TestSealedSecretsCertPerUser(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(map[bool]string{false: "denied", true: "allowed"}[allowed], func(t *testing.T) {
			seedSealedSecretsCertFixture(t)
			certPEM, _ := sealingCertRSA()
			f := newFakeCertAPIServer(t, certPEM)
			if allowed {
				f.grantProxyName = certProxyName
			}
			useFakeAPIServer(t, f)
			env := newAuthTestServer(t)

			resp := env.authGet(t, "/api/sealedsecrets/cert", "dana", "team-a")
			want := http.StatusForbidden
			if allowed {
				want = http.StatusOK
			}
			_, failure := decodeCert(t, resp, want)

			wantReview := authv1.ResourceAttributes{Namespace: "kube-system", Verb: "get", Resource: "services", Subresource: "proxy", Name: certProxyName}
			f.mu.Lock()
			reviews := append([]authv1.ResourceAttributes(nil), f.reviews...)
			f.mu.Unlock()
			found := false
			for _, r := range reviews {
				found = found || r == wantReview
			}
			if !found {
				t.Fatalf("reviews = %+v, want one for %+v", reviews, wantReview)
			}
			reqs := f.certRequests()
			if !allowed {
				if failure["error_code"] != "denied" || len(reqs) != 0 || strings.Contains(failure["error"], "sealed-secrets-controller") {
					t.Fatalf("failure %v, requests %v; want denied without fetching or naming the Service", failure, requestPaths(reqs))
				}
				return
			}
			if len(reqs) != 1 || reqs[0].URL.Path != certProxyPath || reqs[0].Header.Get("Impersonate-User") != "dana" {
				t.Fatalf("requests %v; want one proxy GET as dana", requestPaths(reqs))
			}
		})
	}
}

func TestSealedSecretsCertRejectsUnusableUpstream(t *testing.T) {
	ecdsaPEM := sealingKeyPEM(t, time.Now().Add(-time.Hour))
	certPEM, _ := sealingCertRSA()
	expiredPEM, _ := sealingCertWithValidity(time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour))
	for _, tc := range []struct {
		name     string
		body     []byte
		status   int
		location string
	}{
		{"not PEM", []byte("hello"), http.StatusOK, ""},
		{"PEM but not a certificate", pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte("x")}), http.StatusOK, ""},
		{"certificate that does not parse", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("x")}), http.StatusOK, ""},
		{"not an RSA key", ecdsaPEM, http.StatusOK, ""},
		{"expired", expiredPEM, http.StatusOK, ""},
		{"over 64 KiB", append(append([]byte{}, certPEM...), make([]byte, sealedSecretsCertMaxBytes)...), http.StatusOK, ""},
		{"error status", certPEM, http.StatusServiceUnavailable, ""},
		{"redirect is not followed", nil, http.StatusFound, "/elsewhere"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seedSealedSecretsCertFixture(t)
			f := newFakeCertAPIServer(t, tc.body)
			f.status, f.location = tc.status, tc.location
			useFakeAPIServer(t, f)

			resp, err := http.Get(testServer.URL + "/api/sealedsecrets/cert")
			if err != nil {
				t.Fatal(err)
			}
			_, failure := decodeCert(t, resp, http.StatusBadGateway)
			if failure["error_code"] != "upstream" {
				t.Fatalf("failure = %v, want upstream", failure)
			}
			if reqs := f.certRequests(); len(reqs) != 1 || reqs[0].URL.Path != certProxyPath {
				t.Fatalf("requests %v, want only %s", requestPaths(reqs), certProxyPath)
			}
		})
	}
}

func TestSealedSecretsHTTPPort(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ports []corev1.ServicePort
		want  int32
	}{
		{"named http", []corev1.ServicePort{{Name: "metrics", Port: 8081}, {Name: "http", Port: 8080}}, 8080},
		{"the only TCP port", []corev1.ServicePort{{Port: 8080}, {Port: 53, Protocol: corev1.ProtocolUDP}}, 8080},
		{"several unnamed", []corev1.ServicePort{{Name: "a", Port: 8080}, {Name: "b", Port: 9090}}, 0},
		{"http over UDP is not it", []corev1.ServicePort{{Name: "http", Port: 8080, Protocol: corev1.ProtocolUDP}}, 0},
	} {
		port := sealedSecretsHTTPPort(controllerService("ns", "svc", tc.ports...))
		var got int32
		if port != nil {
			got = port.Port
		}
		if got != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, got, tc.want)
		}
	}
}

func requestPaths(reqs []*http.Request) []string {
	out := make([]string, len(reqs))
	for i, r := range reqs {
		out[i] = r.Method + " " + r.URL.RequestURI()
	}
	return out
}
