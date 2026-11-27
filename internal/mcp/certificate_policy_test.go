package mcp

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http/httptest"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/server"
	"github.com/skyhook-io/radar/pkg/health"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCertificatePolicyReachesMCPHTTPAudit(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: now.Add(-30 * time.Minute), NotAfter: now.Add(30 * time.Minute), DNSNames: []string{"web.example.test"}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "short-tls", Namespace: "app"}, Type: corev1.SecretTypeTLS, Data: map[string][]byte{"tls.crt": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}}
	class := "nginx"
	if err := k8s.InitTestResourceCache(fake.NewClientset(secret,
		&networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: class}, Spec: networkingv1.IngressClassSpec{Controller: "k8s.io/ingress-nginx"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "controller", Namespace: "app", Labels: map[string]string{"app.kubernetes.io/name": "ingress-nginx", "app.kubernetes.io/component": "controller"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}},
		&networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "app"}, Spec: networkingv1.IngressSpec{IngressClassName: &class, TLS: []networkingv1.IngressTLS{{SecretName: "short-tls", Hosts: []string{"web.example.test"}}}, Rules: []networkingv1.IngressRule{{Host: "web.example.test"}}}, Status: networkingv1.IngressStatus{LoadBalancer: networkingv1.IngressLoadBalancerStatus{Ingress: []networkingv1.IngressLoadBalancerIngress{{IP: "203.0.113.10"}}}}},
	)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	for _, tc := range []struct {
		name        string
		policy      health.CertificateExpiryPolicy
		wantFinding bool
	}{
		{"automatic defaults", health.CertificateExpiryPolicy{}, false},
		{"explicit wide warning", health.CertificateExpiryPolicy{WarningPercent: 80}, true},
		{"configured", health.CertificateExpiryPolicy{WarningPercent: 20}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := server.New(server.Config{CertificateExpiryPolicy: tc.policy, MCPHandler: NewHandler()})
			ts := httptest.NewServer(srv.Handler())
			defer ts.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "expiry-test", Version: "test"}, nil)
			session, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{Endpoint: ts.URL + "/mcp"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "get_cluster_audit", Arguments: map[string]any{"namespace": "app"}})
			if err != nil || result.IsError {
				t.Fatalf("audit response=%+v error=%v", result, err)
			}
			var body auditToolResult
			if err := json.Unmarshal([]byte(extractText(t, result)), &body); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, f := range body.Findings {
				if f.Check == "tlsCertificateExpiry" {
					found = true
				}
			}
			if found != tc.wantFinding {
				t.Fatalf("TLS finding=%v want=%v; response=%+v", found, tc.wantFinding, body)
			}
			result, err = session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "get_dashboard", Arguments: map[string]any{"namespace": "app"}})
			if err != nil || result.IsError {
				t.Fatalf("dashboard response=%+v error=%v", result, err)
			}
			var dashboard mcpDashboard
			if err := json.Unmarshal([]byte(extractText(t, result)), &dashboard); err != nil {
				t.Fatal(err)
			}
			live := false
			for _, problem := range dashboard.Problems {
				if problem.Reason == k8s.TLSCertificateExpiringReason {
					live = true
				}
			}
			if live != tc.wantFinding {
				t.Fatalf("dashboard live TLS=%v want=%v; %+v", live, tc.wantFinding, dashboard.Problems)
			}
		})
	}
}
