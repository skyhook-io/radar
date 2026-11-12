package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
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

var sealedSecretKinds = []k8s.APIResource{{Group: "bitnami.com", Version: "v1alpha1", Name: "sealedsecrets", Kind: "SealedSecret", Namespaced: true, Verbs: []string{"get", "list", "watch"}}}

func sealedSecret(ns, name, synced string) *unstructured.Unstructured {
	return cnpgObj("bitnami.com/v1alpha1", "SealedSecret", ns, name, map[string]any{}, map[string]any{
		"conditions": []any{map[string]any{"type": "Synced", "status": synced}},
	})
}

// seedSealedSecretsFixture: a ready controller in kube-system, two sealing
// keys, and SealedSecrets team/a (synced) and other/b (not synced).
func seedSealedSecretsFixture(t *testing.T, scopes map[string]k8score.ResourceScope) {
	t.Helper()
	restoreFixtureCache(t)
	k8s.ResetResourceCache()
	now := time.Now().UTC()
	one := int32(1)
	client := fake.NewClientset(
		deploy("kube-system", "sealed-secrets-controller", map[string]string{"app.kubernetes.io/name": "sealed-secrets"}, &one, 1),
		sealingKeySecret("sealed-secrets-keyold", "kube-system", "active", sealingKeyPEM(t, now.Add(-400*24*time.Hour)), now.Add(-400*24*time.Hour)),
		sealingKeySecret("sealed-secrets-keynew", "kube-system", "active", sealingKeyPEM(t, now.Add(-2*24*time.Hour)), now.Add(-2*24*time.Hour)),
	)
	var err error
	if scopes == nil {
		err = k8s.InitTestResourceCache(client)
	} else {
		err = k8s.InitScopedTestResourceCache(client, scopes)
	}
	if err != nil {
		t.Fatalf("init cache: %v", err)
	}
	seedCNPGWorkspace(t, sealedSecretKinds, sealedSecret("team", "a", "True"), sealedSecret("other", "b", "False"))
}

func getSealedSecretsController(t *testing.T, resp *http.Response) (sealedSecretsControllerResponse, string) {
	t.Helper()
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, raw)
	}
	var out sealedSecretsControllerResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, raw)
	}
	return out, string(raw)
}

func TestSealedSecretsControllerWithoutAuth(t *testing.T) {
	seedSealedSecretsFixture(t, nil)
	resp, err := http.Get(testServer.URL + "/api/sealedsecrets/controller")
	if err != nil {
		t.Fatal(err)
	}
	got, raw := getSealedSecretsController(t, resp)
	if !got.Installed || got.ControllerState != "ok" || got.Controller == nil ||
		got.Controller.Namespace != "kube-system" || got.Controller.Name != "sealed-secrets-controller" || !got.Controller.Ready {
		t.Fatalf("controller = %+v (%s)", got, raw)
	}
	if got.KeysState != "ok" || len(got.Keys) != 2 || !got.Keys[0].Active || got.Keys[0].Name != "sealed-secrets-keynew" || got.Keys[1].Active {
		t.Fatalf("keys = %+v (%s)", got.Keys, got.KeysState)
	}
	if got.Counts == nil || *got.Counts != (sealedSecretCounts{Total: 2, Synced: 1, NotSynced: 1}) {
		t.Fatalf("counts = %+v", got.Counts)
	}
	for _, bad := range []string{"SENTINEL", "BEGIN", "tls.key", "sealedsecrets.bitnami.com", "annotations", "labels"} {
		if strings.Contains(raw, bad) {
			t.Errorf("response leaks %q: %s", bad, raw)
		}
	}
}

func TestSealedSecretsControllerPerUser(t *testing.T) {
	seedSealedSecretsFixture(t, nil)
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"team"}}
	allow(perms, "bitnami.com", "sealedsecrets", "", false)
	allow(perms, "bitnami.com", "sealedsecrets", "team", true)
	allow(perms, "", "secrets", "kube-system", false)
	perms.SetCanI("get", "apps", "deployments", "kube-system", false)
	env.srv.permCache.Set("dana", nil, perms)

	got, raw := getSealedSecretsController(t, env.authGet(t, "/api/sealedsecrets/controller", "dana", ""))
	if got.ControllerState != "denied" || got.Controller != nil {
		t.Errorf("controller = %q %+v, want denied and omitted", got.ControllerState, got.Controller)
	}
	if got.KeysState != "denied" || len(got.Keys) != 0 || strings.Contains(raw, "sealed-secrets-key") {
		t.Errorf("keys = %q %+v, want denied and omitted", got.KeysState, got.Keys)
	}
	if got.Counts == nil || *got.Counts != (sealedSecretCounts{Total: 1, Synced: 1, Partial: true}) {
		t.Errorf("counts = %+v, want team only and partial", got.Counts)
	}
}

func TestSealedSecretsControllerKeysNotCollected(t *testing.T) {
	seedSealedSecretsFixture(t, map[string]k8score.ResourceScope{"deployments": {Enabled: true}, "secrets": {Enabled: false}})
	resp, err := http.Get(testServer.URL + "/api/sealedsecrets/controller")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := getSealedSecretsController(t, resp)
	if got.KeysState != "notCollected" || got.ControllerState != "ok" || len(got.Keys) != 0 {
		t.Fatalf("got keys %q controller %q, want notCollected / ok", got.KeysState, got.ControllerState)
	}
}

func TestSealedSecretsControllerNotInstalled(t *testing.T) {
	restoreFixtureCache(t)
	seedCNPGWorkspace(t, nil)
	resp, err := http.Get(testServer.URL + "/api/sealedsecrets/controller")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := getSealedSecretsController(t, resp)
	if got.Installed || got.KeysState != "unknown" || got.Counts != nil {
		t.Fatalf("got %+v, want not installed, keys unknown, no counts", got)
	}
}

func TestUnreadKeysState(t *testing.T) {
	for _, tc := range []struct {
		name string
		a    k8score.KindReadAssessment
		want string
	}{
		{"one informer still syncing", k8score.KindReadAssessment{Readiness: k8score.KindPending, AllNamespaces: true}, "loading"},
		{"namespace informer still syncing", k8score.KindReadAssessment{Readiness: k8score.KindPending, Read: []string{}, Loading: []string{"kube-system"}}, "loading"},
		{"not watched", k8score.KindReadAssessment{Readiness: k8score.KindUnavailable}, "notCollected"},
		{"namespace not covered", k8score.KindReadAssessment{Readiness: k8score.KindReady, Read: []string{}, Uncovered: []string{"kube-system"}}, "notCollected"},
		{"load failed", k8score.KindReadAssessment{Readiness: k8score.KindPending, Read: []string{}, Failed: []string{"kube-system"}}, "notCollected"},
	} {
		if got := unreadKeysState(tc.a); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}
