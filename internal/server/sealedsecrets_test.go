package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func sealingKeyPEM(t *testing.T, notBefore time.Time) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "sealed-secret"},
		NotBefore:    notBefore,
		NotAfter:     notBefore.Add(10 * 365 * 24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func sealingKeySecret(name, ns, labelValue string, crt []byte, created time.Time) *corev1.Secret {
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns,
			CreationTimestamp: metav1.NewTime(created),
			Annotations:       map[string]string{"note": "ANNOTATION-SENTINEL"},
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{"tls.crt": crt, "tls.key": []byte("PRIVATE-KEY-SENTINEL")},
	}
	if labelValue != "" {
		s.Labels = map[string]string{sealingKeyLabel: labelValue}
	}
	return s
}

func TestSealingKeysFromSecrets(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	day := 24 * time.Hour
	objs := []runtime.Object{
		sealingKeySecret("old", "kube-system", "active", sealingKeyPEM(t, now.Add(-400*day)), now.Add(-400*day)),
		sealingKeySecret("new", "kube-system", "active", sealingKeyPEM(t, now.Add(-2*day)), now.Add(-2*day)),
		sealingKeySecret("bad", "kube-system", "compromised", sealingKeyPEM(t, now.Add(-1*day)), now.Add(-1*day)),
		sealingKeySecret("broken", "kube-system", "active", []byte("garbage"), now.Add(-10*day)),
		sealingKeySecret("plain", "kube-system", "", sealingKeyPEM(t, now), now),
	}
	got := sealingKeysFromSecrets(objs)
	var names []string
	for _, k := range got {
		names = append(names, k.Name)
	}
	if strings.Join(names, ",") != "bad,new,broken,old" {
		t.Fatalf("order = %v, want bad,new,broken,old", names)
	}
	for _, k := range got {
		if k.Active != (k.Name == "new") {
			t.Errorf("%s active = %v", k.Name, k.Active)
		}
		if k.NotUsed != (k.Name == "bad") {
			t.Errorf("%s notUsed = %v", k.Name, k.NotUsed)
		}
		if k.Name == "broken" && k.NotBefore != "" {
			t.Errorf("broken notBefore = %q, want empty", k.NotBefore)
		}
		if k.Name == "new" && (k.NotBefore == "" || k.NotAfter == "") {
			t.Errorf("new key dates missing: %+v", k)
		}
	}
	raw, _ := json.Marshal(got)
	for _, bad := range []string{"SENTINEL", "BEGIN", "tls.key", "note"} {
		if strings.Contains(string(raw), bad) {
			t.Errorf("response leaks %q: %s", bad, raw)
		}
	}
}

func deploy(ns, name string, labels map[string]string, replicas *int32, ready int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels},
		Spec:       appsv1.DeploymentSpec{Replicas: replicas},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: ready},
	}
}

func TestFindSealedSecretsController(t *testing.T) {
	label := map[string]string{"app.kubernetes.io/name": "sealed-secrets"}
	byName := deploy("a", "sealed-secrets-controller", nil, nil, 1)
	byLabel := deploy("b", "controller", label, nil, 1)
	if got := findSealedSecretsController([]*appsv1.Deployment{byName, byLabel}); got != byLabel {
		t.Errorf("label match should win over a name match, got %v", got)
	}
	if got := findSealedSecretsController([]*appsv1.Deployment{byName, deploy("c", "other", nil, nil, 1)}); got != byName {
		t.Errorf("name match expected, got %v", got)
	}
	ax, bx := deploy("a", "x", label, nil, 1), deploy("b", "x", label, nil, 1)
	if got := findSealedSecretsController([]*appsv1.Deployment{bx, ax}); got != ax {
		t.Errorf("first by namespace/name expected a/x, got %v", got)
	}
	if got := findSealedSecretsController([]*appsv1.Deployment{deploy("c", "other", nil, nil, 1), nil}); got != nil {
		t.Errorf("no match expected, got %v", got)
	}

	one, zero := int32(1), int32(0)
	for _, tc := range []struct {
		d    *appsv1.Deployment
		want bool
	}{
		{deploy("a", "x", nil, &one, 1), true},
		{deploy("a", "x", nil, nil, 1), true},
		{deploy("a", "x", nil, &one, 0), false},
		{deploy("a", "x", nil, &zero, 0), false},
	} {
		ref := sealedSecretsControllerRefFor(tc.d)
		if ref.Ready != tc.want || ref.Kind != "Deployment" || ref.Namespace != "a" || ref.Name != "x" {
			t.Errorf("ref = %+v, want ready %v", ref, tc.want)
		}
	}
}
