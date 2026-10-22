package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

const helmSecretTestName = "sh.helm.release.v1.web.v3"

func helmReleaseSecretFixture(payload string) *corev1.Secret {
	return &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      helmSecretTestName,
			Namespace: "default",
			UID:       "uid-web-v3",
			Labels:    map[string]string{"owner": "helm", "name": "web", "status": "deployed", "version": "3"},
		},
		Type: k8score.HelmReleaseSecretType,
		Data: map[string][]byte{"release": []byte(payload)},
	}
}

type fakeSecretAPI struct {
	url         string
	gets        int
	impersonate []string // Impersonate-User of each GET
	serveUID    string   // when set, the live Secret has this UID instead
}

// useHelmReleaseSecretCluster caches one Helm release Secret and points the
// live client at a fake API server that answers GETs for it with payload.
func useHelmReleaseSecretCluster(t *testing.T, payload string) *fakeSecretAPI {
	t.Helper()
	useTestResourceCache(t, fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive}},
		helmReleaseSecretFixture(payload),
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "app-config", Namespace: "default"},
			Type:       corev1.SecretTypeOpaque,
			Data:       map[string][]byte{"password": []byte("hunter2")},
		},
	))

	fakeAPI := &fakeSecretAPI{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/namespaces/default/secrets/"+helmSecretTestName {
			http.NotFound(w, r)
			return
		}
		fakeAPI.gets++
		fakeAPI.impersonate = append(fakeAPI.impersonate, r.Header.Get("Impersonate-User"))
		w.Header().Set("Content-Type", "application/json")
		live := helmReleaseSecretFixture(payload)
		if fakeAPI.serveUID != "" {
			live.UID = types.UID(fakeAPI.serveUID)
		}
		_ = json.NewEncoder(w).Encode(live)
	}))
	t.Cleanup(api.Close)
	fakeAPI.url = api.URL
	prev := k8s.SetTestClient(kubernetes.NewForConfigOrDie(&rest.Config{Host: api.URL}))
	t.Cleanup(func() { k8s.SetTestClient(prev) })
	return fakeAPI
}

type secretRow struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Data map[string]*string `json:"data"`
}

// The cache holds a digest in place of each Helm release value; the list
// must serve the key with no value, for one namespace and for all of them
// (the default view).
func TestSecretsList_HelmReleaseServedWithoutValues(t *testing.T) {
	useHelmReleaseSecretCluster(t, "H4sIpayload")

	for _, path := range []string{"/api/resources/secrets?namespaces=default", "/api/resources/secrets"} {
		resp := get(t, path)
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			t.Fatalf("%s: expected 200, got %d", path, resp.StatusCode)
		}
		var rows []secretRow
		err := json.NewDecoder(resp.Body).Decode(&rows)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("%s: decode: %v", path, err)
		}
		seen := map[string]secretRow{}
		for _, row := range rows {
			seen[row.Metadata.Name] = row
		}

		helm, ok := seen[helmSecretTestName]
		if !ok {
			t.Fatalf("%s: Helm release Secret missing from the list: %v", path, rows)
		}
		value, hasKey := helm.Data["release"]
		if !hasKey {
			t.Fatalf("%s: release key dropped; the Keys column would read 0", path)
		}
		if value != nil {
			t.Fatalf("%s: the list served a value for a Helm release Secret: %q", path, *value)
		}
		if v := seen["app-config"].Data["password"]; v == nil || *v == "" {
			t.Fatalf("%s: an ordinary Secret lost its value: %v", path, seen["app-config"].Data)
		}
	}
}

func TestSecretGet_HelmReleaseServesLivePayload(t *testing.T) {
	fakeAPI := useHelmReleaseSecretCluster(t, "H4sIpayload")

	resp := get(t, "/api/resources/secrets/default/"+helmSecretTestName)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Resource secretRow `json:"resource"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// JSON carries []byte as base64.
	if v := body.Resource.Data["release"]; v == nil || *v != "SDRzSXBheWxvYWQ=" {
		t.Fatalf("release value = %v, want the live payload", v)
	}
	if fakeAPI.gets != 1 {
		t.Fatalf("live GETs = %d, want 1", fakeAPI.gets)
	}

	// Ordinary Secrets are still answered from the cache.
	resp2 := get(t, "/api/resources/secrets/default/app-config")
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp2.StatusCode)
	}
	if fakeAPI.gets != 1 {
		t.Fatalf("an ordinary Secret was read live (GETs = %d)", fakeAPI.gets)
	}
}

// The live read can fail where the cached read would not have: the Secret
// was deleted since it was cached, or the API server refuses the caller.
func TestSecretGet_HelmReleaseLiveReadFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason metav1.StatusReason
		code   int
	}{
		{"deleted since cached", metav1.StatusReasonNotFound, http.StatusNotFound},
		{"forbidden by the API server", metav1.StatusReasonForbidden, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useHelmReleaseSecretCluster(t, "H4sIpayload")
			failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.code)
				_ = json.NewEncoder(w).Encode(metav1.Status{
					TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
					Status:   metav1.StatusFailure, Reason: tc.reason, Code: int32(tc.code),
				})
			}))
			t.Cleanup(failing.Close)
			prev := k8s.SetTestClient(kubernetes.NewForConfigOrDie(&rest.Config{Host: failing.URL}))
			t.Cleanup(func() { k8s.SetTestClient(prev) })

			resp := get(t, "/api/resources/secrets/default/"+helmSecretTestName)
			defer resp.Body.Close()
			if resp.StatusCode != tc.code {
				t.Fatalf("expected %d, got %d", tc.code, resp.StatusCode)
			}
		})
	}
}

// With auth on, the live read runs as the caller, never as Radar's own
// identity, so the API server applies the caller's RBAC.
func TestSecretGet_HelmReleaseLiveReadImpersonatesCaller(t *testing.T) {
	fakeAPI := useHelmReleaseSecretCluster(t, "H4sIpayload")
	prevConfig := k8s.SetTestConfig(&rest.Config{Host: fakeAPI.url})
	t.Cleanup(func() { k8s.SetTestConfig(prevConfig) })

	env := newAuthTestServer(t)
	env.srv.permCache.Set("bob", nil, &auth.UserPermissions{AllowedNamespaces: []string{"default"}})
	seedServerSecretGetCanI(t, env, "bob", []string{"default"}, nil)

	resp := env.authGet(t, "/api/resources/secrets/default/"+helmSecretTestName, "bob", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if len(fakeAPI.impersonate) != 1 || fakeAPI.impersonate[0] != "bob" {
		t.Fatalf("live read impersonated %q, want bob", fakeAPI.impersonate)
	}
}

// The live read must return the object the cache answered for. Another UID
// under the same name means the Secret was recreated, or the cluster context
// switched between the cache lookup and the read; the caller retries.
func TestSecretGet_HelmReleaseLiveReadOfAnotherObjectIsRetryable(t *testing.T) {
	fakeAPI := useHelmReleaseSecretCluster(t, "H4sIpayload")
	fakeAPI.serveUID = "uid-from-another-cluster"

	resp := get(t, "/api/resources/secrets/default/"+helmSecretTestName)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
	var body struct {
		Code string `json:"error_code"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Code != "cluster_connecting" {
		t.Fatalf("code = %q, want cluster_connecting so the drawer retries", body.Code)
	}
}
