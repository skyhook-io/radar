package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/k8s"
	bp "github.com/skyhook-io/radar/pkg/audit"
	"github.com/skyhook-io/radar/pkg/certs"
	"github.com/skyhook-io/radar/pkg/health"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func expiryPolicyPEM(t *testing.T, expiry time.Time) []byte {
	return expiryPolicyValidityPEM(t, expiry.Add(-time.Hour), expiry)
}

func expiryPolicyValidityPEM(t *testing.T, start, expiry time.Time) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: start, NotAfter: expiry, DNSNames: []string{"web.example.test"}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestCertificateAutomaticDefaultsAcrossHTTP(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	objects := []runtime.Object{&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}}}
	for _, sample := range []struct {
		name string
		left time.Duration
	}{{"fresh", 45 * time.Minute}, {"warning", 10 * time.Minute}, {"high", 2 * time.Minute}, {"expired", -time.Minute}} {
		expiry := now.Add(sample.left)
		objects = append(objects, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: sample.name, Namespace: "default"}, Type: corev1.SecretTypeTLS, Data: map[string][]byte{"tls.crt": expiryPolicyValidityPEM(t, expiry.Add(-time.Hour), expiry)}})
	}
	useTestResourceCache(t, fake.NewClientset(objects...))
	expiry := now.Add(45 * time.Minute)
	certificate := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "cert-manager.io/v1", "kind": "Certificate", "metadata": map[string]interface{}{"name": "fresh", "namespace": "default"}, "spec": map[string]interface{}{"secretName": "fresh"}, "status": map[string]interface{}{"notBefore": expiry.Add(-time.Hour).Format(time.RFC3339), "notAfter": expiry.Format(time.RFC3339)}}}
	if err := k8s.InitTestDynamicResourceCache(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), certificate), []k8s.APIResource{{Group: "cert-manager.io", Version: "v1", Name: "certificates", Kind: "Certificate", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	gvr := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
	dynamic := k8s.GetDynamicResourceCache()
	if err := dynamic.EnsureWatching(gvr); err != nil {
		t.Fatal(err)
	}
	if !dynamic.WaitForSync(gvr, 5*time.Second) {
		t.Fatal("certificate cache did not sync")
	}
	invalidateAuditCache()
	t.Cleanup(invalidateAuditCache)
	s := New(Config{})
	read := func(path string, out any) {
		t.Helper()
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, httptest.NewRequest("GET", path, nil))
		if rr.Code != 200 {
			t.Fatalf("%s: %d %s", path, rr.Code, rr.Body.String())
		}
		if err := json.Unmarshal(rr.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]certs.Health{"fresh": certs.HealthHealthy, "warning": certs.HealthDegraded, "high": certs.HealthUnhealthy, "expired": certs.HealthUnhealthy}
	var inventory []certs.Cert
	read("/api/certificates?namespaces=default", &inventory)
	if len(inventory) != 4 {
		t.Fatalf("dedup count=%d", len(inventory))
	}
	for _, cert := range inventory {
		if cert.Health != want[cert.Name] || cert.ExpiryPolicy.WarningSeconds != 1200 || cert.ExpiryPolicy.HighSeconds != 280 || cert.ExpiryPolicy.Automatic {
			t.Fatalf("automatic inventory=%+v", cert)
		}
	}
	var list map[string]CertExpiry
	read("/api/secrets/certificate-expiry?namespaces=default", &list)
	for name, level := range want {
		if list["default/"+name].Health != health.Level(level) {
			t.Fatalf("list disagrees for %s", name)
		}
	}
	var scan bp.ScanResults
	read("/api/audit?raw=true&namespaces=default", &scan)
	if counts := scan.CheckCounts["tlsCertificateExpiry"]; counts.Evaluated != 4 || counts.Passed != 1 {
		t.Fatalf("automatic audit counts=%+v", counts)
	}
	var secret struct{ CertificateInfo *SecretCertificateInfo }
	read("/api/resources/secrets/default/fresh", &secret)
	if secret.CertificateInfo == nil || secret.CertificateInfo.Certificates[0].ExpiryPolicy.WarningSeconds != 1200 {
		t.Fatal("Secret drawer missed automatic windows")
	}
	var detail struct {
		CertificateExpiryPolicy *health.CertificateExpiryWindows
	}
	read("/api/resources/certificates/default/fresh?group=cert-manager.io", &detail)
	if detail.CertificateExpiryPolicy == nil || !detail.CertificateExpiryPolicy.Automatic {
		t.Fatal("raw Certificate drawer missed automatic policy")
	}
}

func expiryPolicyFixture(t *testing.T) {
	t.Helper()
	now := time.Now().UTC()
	objects := []runtime.Object{&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}}}
	for _, sample := range []struct {
		name string
		left time.Duration
	}{
		{"fresh", 45 * time.Minute}, {"warning", 10 * time.Minute}, {"high", 2 * time.Minute}, {"expired", -time.Hour},
	} {
		objects = append(objects, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: sample.name, Namespace: "default"}, Type: corev1.SecretTypeTLS, Data: map[string][]byte{"tls.crt": expiryPolicyPEM(t, now.Add(sample.left))}})
	}
	objects = append(objects,
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "sealed-key", Namespace: "default", Labels: map[string]string{"sealedsecrets.bitnami.com/sealed-secrets-key": "active"}}, Type: corev1.SecretTypeTLS, Data: map[string][]byte{"tls.crt": expiryPolicyPEM(t, now.Add(time.Minute))}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "broken", Namespace: "default"}, Type: corev1.SecretTypeTLS, Data: map[string][]byte{"tls.crt": []byte("not PEM")}},
	)
	useTestResourceCache(t, fake.NewClientset(objects...))
	if err := k8s.InitTestDynamicResourceCache(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), []k8s.APIResource{{Version: "v1", Name: "pods", Kind: "Pod", Namespaced: true}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	invalidateAuditCache()
	t.Cleanup(invalidateAuditCache)
}

func TestCertificatePolicyDashboardLiveWindow(t *testing.T) {
	class := "nginx"
	useTestResourceCache(t, fake.NewClientset(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "web-tls", Namespace: "default"}, Type: corev1.SecretTypeTLS, Data: map[string][]byte{"tls.crt": expiryPolicyPEM(t, time.Now().Add(10*time.Minute))}},
		&networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: class}, Spec: networkingv1.IngressClassSpec{Controller: "k8s.io/ingress-nginx"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "controller", Namespace: "default", Labels: map[string]string{"app.kubernetes.io/name": "ingress-nginx", "app.kubernetes.io/component": "controller"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}},
		&networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"}, Spec: networkingv1.IngressSpec{IngressClassName: &class, TLS: []networkingv1.IngressTLS{{SecretName: "web-tls", Hosts: []string{"web.example.test"}}}, Rules: []networkingv1.IngressRule{{Host: "web.example.test"}}}, Status: networkingv1.IngressStatus{LoadBalancer: networkingv1.IngressLoadBalancerStatus{Ingress: []networkingv1.IngressLoadBalancerIngress{{IP: "203.0.113.10"}}}}},
	))
	for _, tc := range []struct {
		name    string
		percent int
		want    bool
	}{{"default", 0, true}, {"configured", 10, false}} {
		t.Run(tc.name, func(t *testing.T) {
			s := New(Config{CertificateExpiryPolicy: health.CertificateExpiryPolicy{WarningPercent: tc.percent}})
			_, problems := s.getDashboardHealth(k8s.GetResourceCache(), "default")
			found := false
			for _, p := range problems {
				if p.Reason == k8s.TLSCertificateExpiringReason {
					found = true
				}
			}
			if found != tc.want {
				t.Fatalf("dashboard live expiry=%v want=%v; problems=%+v", found, tc.want, problems)
			}
		})
	}
}

func TestCertificatePolicyHTTPAcrossSurfacesAndCache(t *testing.T) {
	expiryPolicyFixture(t)
	policy := health.CertificateExpiryPolicy{WarningPercent: 20}
	s := New(Config{CertificateExpiryPolicy: policy})
	ts := httptest.NewServer(s.router)
	defer ts.Close()
	read := func(path string, out any) {
		t.Helper()
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status=%d", path, resp.StatusCode)
		}
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	var inventory []certs.Cert
	read("/api/certificates?namespaces=default", &inventory)
	if len(inventory) != 4 {
		t.Fatalf("inventory count=%d, want four serving certificates", len(inventory))
	}
	want := map[string]certs.Health{"fresh": certs.HealthHealthy, "warning": certs.HealthDegraded, "high": certs.HealthUnhealthy, "expired": certs.HealthUnhealthy}
	for _, row := range inventory {
		if row.Health != want[row.Name] || row.ExpiryPolicy.WarningSeconds != 720 {
			t.Fatalf("inventory=%+v", row)
		}
	}
	var list map[string]CertExpiry
	read("/api/secrets/certificate-expiry?namespaces=default", &list)
	if len(list) != 5 {
		t.Fatalf("expiry count=%d", len(list))
	}
	if list["default/sealed-key"].Expiry == "" {
		t.Fatal("Secret list lost sealed-secrets key expiry")
	}
	for _, row := range inventory {
		if string(list["default/"+row.Name].Health) != string(row.Health) {
			t.Fatalf("list/inventory disagree: %s", row.Name)
		}
	}
	var detail struct{ CertificateInfo *SecretCertificateInfo }
	read("/api/resources/secrets/default/fresh", &detail)
	if detail.CertificateInfo == nil || len(detail.CertificateInfo.Certificates) != 1 || detail.CertificateInfo.Certificates[0].ExpiryPolicy == nil || detail.CertificateInfo.Certificates[0].ExpiryPolicy.HighSeconds != 168 {
		t.Fatalf("detail policy=%+v", detail.CertificateInfo)
	}
	var scan bp.ScanResults
	read("/api/audit?raw=true&namespaces=default", &scan)
	if got := scan.CheckCounts["tlsCertificateExpiry"]; got.Evaluated != 4 || got.Passed != 1 {
		t.Fatalf("audit counts=%+v", got)
	}
	for _, f := range scan.Findings {
		if f.CheckID == "tlsCertificateExpiry" && f.Name == "fresh" {
			t.Fatal("raw Audit ignored configured window")
		}
	}
	dashboard := s.getDashboardCertificateHealth([]string{"default"})
	if dashboard == nil || dashboard.Total != 5 || dashboard.Healthy != 1 || dashboard.Warning != 1 || dashboard.Critical != 2 || dashboard.Expired != 1 {
		t.Fatalf("dashboard=%+v", dashboard)
	}
	var empty []certs.Cert
	read("/api/certificates?namespaces=elsewhere", &empty)
	if len(empty) != 0 {
		t.Fatal("namespace filter exposed another namespace")
	}
	defaultServer := New(Config{CertificateExpiryPolicy: health.CertificateExpiryPolicy{WarningPercent: 80}})
	request := httptest.NewRequest(http.MethodGet, "/api/audit?raw=true&namespaces=default", nil)
	rr := httptest.NewRecorder()
	defaultServer.handleAudit(rr, request)
	var defaultScan bp.ScanResults
	if err := json.Unmarshal(rr.Body.Bytes(), &defaultScan); err != nil {
		t.Fatal(err)
	}
	if defaultScan.CheckCounts["tlsCertificateExpiry"].Passed != 0 {
		t.Fatal("custom policy result leaked through cache into default server")
	}
	read("/api/audit?raw=true&namespaces=default", &scan)
	if scan.CheckCounts["tlsCertificateExpiry"].Passed != 1 {
		t.Fatal("default result leaked through cache into custom server")
	}
}

func TestCertificatePolicyDynamicResourceDetail(t *testing.T) {
	expiryPolicyFixture(t)
	certificate := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "cert-manager.io/v1", "kind": "Certificate", "metadata": map[string]interface{}{"name": "web", "namespace": "default"}}}
	if err := k8s.InitTestDynamicResourceCache(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), certificate), []k8s.APIResource{{Group: "cert-manager.io", Version: "v1", Name: "certificates", Kind: "Certificate", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	gvr := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
	dynamic := k8s.GetDynamicResourceCache()
	if err := dynamic.EnsureWatching(gvr); err != nil {
		t.Fatal(err)
	}
	if !dynamic.WaitForSync(gvr, 5*time.Second) {
		t.Fatal("certificate informer did not sync")
	}
	s := New(Config{CertificateExpiryPolicy: health.CertificateExpiryPolicy{WarningPercent: 20}})
	// The group-qualified CRD path returns separately from typed Secrets.
	for _, suffix := range []string{"", "&objectOnly=true"} {
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/resources/certificates/default/web?group=cert-manager.io"+suffix, nil))
		var result struct {
			CertificateExpiryPolicy *health.CertificateExpiryWindows `json:"certificateExpiryPolicy"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if rr.Code != 200 || result.CertificateExpiryPolicy == nil || result.CertificateExpiryPolicy.WarningPercent != 20 || !result.CertificateExpiryPolicy.Automatic {
			t.Fatalf("dynamic policy missing: status=%d body=%s", rr.Code, rr.Body.String())
		}
	}
}

func TestCertificatePolicyPreservesSecretAuthorization(t *testing.T) {
	expiryPolicyFixture(t)
	env := newAuthTestServer(t)
	env.srv.certificateExpiryPolicy = health.CertificateExpiryPolicy{WarningPercent: 20}
	user := "certificate-no-secrets"
	permissions := &auth.UserPermissions{AllowedNamespaces: []string{"default"}}
	permissions.SetCanI("list", "", "secrets", "default", false)
	permissions.SetCanI("get", "", "secrets", "default", false)
	env.srv.permCache.Set(user, nil, permissions)
	for _, path := range []string{"/api/certificates?namespaces=default", "/api/secrets/certificate-expiry?namespaces=default", "/api/audit?raw=true&namespaces=default"} {
		resp := env.authGet(t, path, user, "")
		var body json.RawMessage
		err := json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status=%d error=%v", path, resp.StatusCode, err)
		}
		var decoded any
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"fresh", "warning", "high", "expired"} {
			if containsJSONName(decoded, name) {
				t.Fatalf("GET %s exposed %s", path, name)
			}
		}
	}
}

func containsJSONName(value any, name string) bool {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			if key == "name" && item == name {
				return true
			}
			if key == "default/"+name {
				return true
			}
			if containsJSONName(item, name) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if containsJSONName(item, name) {
				return true
			}
		}
	}
	return false
}

func TestCertificatePolicyConfigUpdateValidatesBeforePersisting(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("RADAR_CLOUD_MODE", "false")
	s := &Server{}
	original := config.Config{CertificateWarningPercent: "20"}
	if err := config.Save(original); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"certificateWarningPercent":"invalid"}`,
		`{"certificateWarningPercent":"0"}`,
		`{"certificateWarningPercent":"-1"}`,
	} {
		rr := httptest.NewRecorder()
		s.handlePutConfig(rr, httptest.NewRequest("PUT", "/api/config", strings.NewReader(body)))
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
		if got := config.Load(); got.CertificateWarningPercent != original.CertificateWarningPercent {
			t.Fatal("invalid config replaced stored policy")
		}
	}
	rr := httptest.NewRecorder()
	s.handlePutConfig(rr, httptest.NewRequest("PUT", "/api/config", strings.NewReader(`{"certificateWarningPercent":"20"}`)))
	if rr.Code != http.StatusOK {
		t.Fatal(rr.Code, rr.Body.String())
	}
	p, err := config.Load().CertificateExpiryPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if p.WarningPercent != 20 {
		t.Fatalf("persisted policy=%+v", p)
	}
}
