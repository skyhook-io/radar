package substrate

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"

	authv1 "k8s.io/api/authentication/v1"
	certsv1 "k8s.io/api/certificates/v1"
	certsv1beta1 "k8s.io/api/certificates/v1beta1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func testCertPEM(t *testing.T, cn string) (string, *x509.Certificate) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), cert
}

// serving makes the fake discovery serve clustertrustbundles in the given
// certificates.k8s.io versions.
func serving(c *fake.Clientset, versions ...string) {
	for _, v := range versions {
		c.Resources = append(c.Resources, &metav1.APIResourceList{GroupVersion: "certificates.k8s.io/" + v, APIResources: []metav1.APIResource{{Name: "clustertrustbundles"}}})
	}
}

func TestClusterTrustPoolSelectsLiveServiceDNSBundles(t *testing.T) {
	live, liveCert := testCertPEM(t, "live substrate CA")
	otherSigner, _ := testCertPEM(t, "another signer")
	notLive, _ := testCertPEM(t, "canary")
	want := x509.NewCertPool()
	want.AddCert(liveCert)

	t.Run("v1", func(t *testing.T) {
		c := fake.NewClientset(
			&certsv1.ClusterTrustBundle{ObjectMeta: metav1.ObjectMeta{Name: "live", Labels: map[string]string{"podcert.ate.dev/canarying": "live"}},
				Spec: certsv1.ClusterTrustBundleSpec{SignerName: serviceDNSSignerName, TrustBundle: live}},
			&certsv1.ClusterTrustBundle{ObjectMeta: metav1.ObjectMeta{Name: "other-signer", Labels: map[string]string{"podcert.ate.dev/canarying": "live"}},
				Spec: certsv1.ClusterTrustBundleSpec{SignerName: "example.com/other", TrustBundle: otherSigner}},
			&certsv1.ClusterTrustBundle{ObjectMeta: metav1.ObjectMeta{Name: "canary", Labels: map[string]string{"podcert.ate.dev/canarying": "next"}},
				Spec: certsv1.ClusterTrustBundleSpec{SignerName: serviceDNSSignerName, TrustBundle: notLive}},
		)
		serving(c, "v1", "v1beta1")
		pool, err := clusterTrustPool(context.Background(), c)
		if err != nil {
			t.Fatal(err)
		}
		if !pool.Equal(want) {
			t.Error("the pool must hold only the live bundle of Substrate's service-DNS signer")
		}
	})

	t.Run("v1beta1 when v1 is not served", func(t *testing.T) {
		c := fake.NewClientset(&certsv1beta1.ClusterTrustBundle{ObjectMeta: metav1.ObjectMeta{Name: "live", Labels: map[string]string{"podcert.ate.dev/canarying": "live"}},
			Spec: certsv1beta1.ClusterTrustBundleSpec{SignerName: serviceDNSSignerName, TrustBundle: live}})
		serving(c, "v1beta1")
		pool, err := clusterTrustPool(context.Background(), c)
		if err != nil || !pool.Equal(want) {
			t.Fatalf("v1beta1 fallback: %v", err)
		}
	})

	for name, c := range map[string]*fake.Clientset{
		"no API served": fake.NewClientset(),
		"no live bundle for the signer": func() *fake.Clientset {
			c := fake.NewClientset(&certsv1.ClusterTrustBundle{ObjectMeta: metav1.ObjectMeta{Name: "canary", Labels: map[string]string{"podcert.ate.dev/canarying": "next"}},
				Spec: certsv1.ClusterTrustBundleSpec{SignerName: serviceDNSSignerName, TrustBundle: notLive}})
			serving(c, "v1")
			return c
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := clusterTrustPool(context.Background(), c)
			var tier *TierError
			if !errors.As(err, &tier) || tier.Code != CodeTLS {
				t.Errorf("err = %v, want a tls tier error", err)
			}
		})
	}
}

func TestResolveTargetPort(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api-0", Namespace: SystemNamespace, Labels: map[string]string{"app": "ateapi"}},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "ateapi", Ports: []corev1.ContainerPort{{Name: "grpc", ContainerPort: 8443}}}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	svc := func(p corev1.ServicePort) *corev1.Service {
		return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: APIService, Namespace: SystemNamespace},
			Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "ateapi"}, Ports: []corev1.ServicePort{p}}}
	}
	tests := []struct {
		name    string
		port    corev1.ServicePort
		want    int
		wantErr bool
	}{
		{"numeric target port", corev1.ServicePort{Port: 443, TargetPort: intstr.FromInt(9443)}, 9443, false},
		{"named target port resolved on the pod", corev1.ServicePort{Port: 443, TargetPort: intstr.FromString("grpc")}, 8443, false},
		{"unset target port is the service port", corev1.ServicePort{Port: 443}, 443, false},
		{"named port the pod lacks", corev1.ServicePort{Port: 443, TargetPort: intstr.FromString("nope")}, 0, true},
		{"no port 443", corev1.ServicePort{Port: 80, TargetPort: intstr.FromInt(8080)}, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := svc(tc.port)
			got, err := resolveTargetPort(context.Background(), fake.NewClientset(pod, s), s)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Errorf("port %d err %v, want %d (error %v)", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

// tokenReactor answers TokenRequests for ate-system/ate-client, recording
// each request.
func tokenReactor(c *fake.Clientset, now func() time.Time, requests *[]*authv1.TokenRequest, fail error) {
	c.PrependReactor("create", "serviceaccounts", func(action ktesting.Action) (bool, runtime.Object, error) {
		create, ok := action.(ktesting.CreateAction)
		if !ok || action.GetSubresource() != "token" {
			return false, nil, nil
		}
		if fail != nil {
			return true, nil, fail
		}
		req := create.GetObject().(*authv1.TokenRequest)
		if impl, ok := action.(ktesting.CreateActionImpl); !ok || action.GetNamespace() != SystemNamespace || impl.Name != ClientServiceAccount {
			return true, nil, apierrors.NewNotFound(schema.GroupResource{Resource: "serviceaccounts"}, "unexpected")
		}
		*requests = append(*requests, req.DeepCopy())
		out := req.DeepCopy()
		out.Status.Token = "token-" + string(rune('0'+len(*requests)))
		out.Status.ExpirationTimestamp = metav1.NewTime(now().Add(time.Duration(*req.Spec.ExpirationSeconds) * time.Second))
		return true, out, nil
	})
}

func TestSharedTokenAudienceAndRemint(t *testing.T) {
	clock := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	c := fake.NewClientset()
	var requests []*authv1.TokenRequest
	tokenReactor(c, now, &requests, nil)
	src := &sharedToken{client: c, now: now}

	first, err := src.token(context.Background())
	if err != nil || first != "token-1" {
		t.Fatalf("first token = %q, %v", first, err)
	}
	req := requests[0]
	if !slices.Equal(req.Spec.Audiences, []string{APIServerName}) || *req.Spec.ExpirationSeconds != int64(tokenLifetime/time.Second) {
		t.Errorf("TokenRequest = audiences %v expiration %d, want [%s] %d", req.Spec.Audiences, *req.Spec.ExpirationSeconds, APIServerName, int64(tokenLifetime/time.Second))
	}

	clock = clock.Add(45 * time.Minute)
	if again, _ := src.token(context.Background()); again != "token-1" || len(requests) != 1 {
		t.Errorf("a token with 15m left is reused: %q after %d requests", again, len(requests))
	}
	clock = clock.Add(6 * time.Minute)
	if next, _ := src.token(context.Background()); next != "token-2" || len(requests) != 2 {
		t.Errorf("a token within 10m of expiry is re-minted: %q after %d requests", next, len(requests))
	}
}

func TestSharedTokenDeniedIsReadable(t *testing.T) {
	c := fake.NewClientset()
	var requests []*authv1.TokenRequest
	tokenReactor(c, time.Now, &requests, apierrors.NewForbidden(schema.GroupResource{Resource: "serviceaccounts/token"}, ClientServiceAccount, errors.New("cannot create")))
	_, err := (&sharedToken{client: c}).token(context.Background())
	code, msg := classify(err, nil)
	if code != CodeUnauthenticated || !strings.Contains(msg, "Kubernetes denied create serviceaccounts/token in "+SystemNamespace) {
		t.Errorf("denied TokenRequest = %s %q", code, msg)
	}
}
