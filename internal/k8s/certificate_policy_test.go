package k8s

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/health"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes/fake"
)

func TestLiveTLSAutomaticDefaultAndRenewal(t *testing.T) {
	defer ResetTestState()
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	secret := tlsSecret("shop", "web-tls", testCertPEM(t, now, now.Add(time.Hour), "web.example.test"))
	client := fake.NewClientset(secret,
		&networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: "nginx"}, Spec: networkingv1.IngressClassSpec{Controller: "k8s.io/ingress-nginx"}},
		readyPod("ingress-nginx", "controller-0", nginxPodLabels),
		tlsIngress("shop", "web", []string{"web-tls"}, withClass("nginx"), withAddress))
	if err := InitTestResourceCache(client); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		elapsed time.Duration
		reason  string
	}{{0, ""}, {40*time.Minute - time.Nanosecond, ""}, {40 * time.Minute, TLSCertificateExpiringReason}, {55 * time.Minute, TLSCertificateExpiringReason}, {time.Hour, TLSCertificateExpiredReason}, {-10 * time.Minute, TLSCertificateNotYetValidReason}} {
		got := detectLiveTLSCertificates(GetResourceCache(), "shop", NewDetectScope(), now.Add(tc.elapsed))
		if tc.reason == "" {
			if len(got) != 0 {
				t.Fatalf("fresh certificate flagged: %+v", got)
			}
		} else if len(got) != 1 || got[0].Reason != tc.reason {
			t.Fatalf("elapsed=%s detections=%+v want=%s", tc.elapsed, got, tc.reason)
		}
	}
	secret.Data["tls.crt"] = testCertPEM(t, now.Add(55*time.Minute), now.Add(115*time.Minute), "web.example.test")
	if _, err := client.CoreV1().Secrets("shop").Update(context.Background(), secret, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := wait.PollUntilContextTimeout(context.Background(), 10*time.Millisecond, 5*time.Second, true, func(context.Context) (bool, error) {
		cached, err := GetResourceCache().Secrets().Secrets("shop").Get("web-tls")
		return err == nil && bytes.Equal(cached.Data["tls.crt"], secret.Data["tls.crt"]), nil
	}); err != nil {
		t.Fatal("renewed certificate not observed", err)
	}
	if got := detectLiveTLSCertificates(GetResourceCache(), "shop", NewDetectScope(), now.Add(55*time.Minute)); len(got) != 0 {
		t.Fatalf("renewal did not clear warning: %+v", got)
	}
}

func TestLiveTLSCertificateIndependentWindowAndClock(t *testing.T) {
	defer ResetTestState()
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	secret := tlsSecret("shop", "web-tls", testCertPEM(t, now, now.Add(time.Hour), "web.example.test"))
	if err := InitTestResourceCache(fake.NewClientset(secret,
		&networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: "nginx"}, Spec: networkingv1.IngressClassSpec{Controller: "k8s.io/ingress-nginx"}},
		readyPod("ingress-nginx", "controller-0", nginxPodLabels),
		tlsIngress("shop", "web", []string{"web-tls"}, withClass("nginx"), withAddress),
	)); err != nil {
		t.Fatal(err)
	}
	p := health.CertificateExpiryPolicy{WarningPercent: 20}
	for _, tc := range []struct {
		elapsed time.Duration
		reason  string
	}{
		{0, ""}, {48*time.Minute - time.Nanosecond, ""}, {48 * time.Minute, TLSCertificateExpiringReason},
		{59 * time.Minute, TLSCertificateExpiringReason}, {time.Hour, TLSCertificateExpiredReason}, {time.Hour + time.Second, TLSCertificateExpiredReason},
		{-10 * time.Minute, TLSCertificateNotYetValidReason},
	} {
		t.Run(tc.elapsed.String(), func(t *testing.T) {
			got := detectLiveTLSCertificates(GetResourceCache(), "shop", NewDetectScope(p), now.Add(tc.elapsed))
			if tc.reason == "" {
				if len(got) != 0 {
					t.Fatalf("unexpected detection %+v", got)
				}
				return
			}
			if len(got) != 1 || got[0].Reason != tc.reason {
				t.Fatalf("detections=%+v, want %s", got, tc.reason)
			}
			if tc.reason == TLSCertificateExpiredReason && got[0].Severity != "critical" {
				t.Fatal("expired certificate severity was reduced")
			}
		})
	}
	if got := detectLiveTLSCertificates(GetResourceCache(), "elsewhere", NewDetectScope(p), now.Add(time.Hour)); len(got) != 0 {
		t.Fatal("namespace filter leaked certificate")
	}
}
