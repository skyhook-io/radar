package images

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/google"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
	corev1 "k8s.io/api/core/v1"
)

type imageRegistryTransport struct {
	manifest []byte
	wantAuth string
	auths    []string
}

func (f *imageRegistryTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.auths = append(f.auths, r.Header.Get("Authorization"))
	status, body := http.StatusUnauthorized, `{"errors":[{"code":"UNAUTHORIZED","message":"registry-private-body"}]}`
	header := http.Header{"Www-Authenticate": {`Basic realm="registry"`}}
	if r.URL.Path != "/v2/" && r.Header.Get("Authorization") == f.wantAuth && f.wantAuth != "" {
		status, body = http.StatusOK, string(f.manifest)
		header.Set("Content-Type", string(types.OCIManifestSchema1))
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

func TestImageInspectorCredentialIsolation(t *testing.T) {
	imageTestCache(t, imageTestSecret("team", "pod-secret", "gcr.io", "pod-user"))
	dockerDir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", dockerDir)
	if err := os.WriteFile(filepath.Join(dockerDir, "config.json"), []byte(`{"auths":{"gcr.io":{"auth":"YW1iaWVudDpzZWNyZXQ="}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	manifest, err := empty.Image.RawManifest()
	if err != nil {
		t.Fatal(err)
	}
	previousTransport, previousGoogle := remote.DefaultTransport, google.Keychain
	t.Cleanup(func() { remote.DefaultTransport, google.Keychain = previousTransport, previousGoogle })

	for _, tc := range []struct {
		name               string
		keychain           authn.Keychain
		wantUser, password string
		succeeds           bool
	}{
		{"Pod secrets only", getPodKeychain("team", imageTestPod(), false, func(string) bool { return true }), "pod-user", "test-password", true},
		{"no Pod secrets", getPodKeychain("team", &corev1.Pod{}, false, func(string) bool { return true }), "", "", false},
		{"missing Pod secrets", getPodKeychain("team", &corev1.Pod{Spec: corev1.PodSpec{ImagePullSecrets: []corev1.LocalObjectReference{{Name: "missing"}}}}, false, func(string) bool { return true }), "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			google.Keychain = rejectingImageKeychain{t}
			expectedAuth := ""
			if tc.wantUser != "" {
				expectedAuth = "Basic " + base64.StdEncoding.EncodeToString([]byte(tc.wantUser+":"+tc.password))
			}
			transport := &imageRegistryTransport{manifest: manifest, wantAuth: expectedAuth}
			remote.DefaultTransport = transport
			_, _, _, err := (&Inspector{}).fetchImageBruteForce(context.Background(), InspectRequest{Image: "gcr.io/project/app:v1", Namespace: "team", PullSecretNames: []string{"other-secret"}, keychain: tc.keychain})
			if (err == nil) != tc.succeeds {
				t.Fatalf("fetch success=%v, want %v: %v", err == nil, tc.succeeds, err)
			}
			if len(transport.auths) == 0 {
				t.Fatal("fake registry not called")
			}
			found := false
			for _, authorization := range transport.auths {
				if authorization != "" && authorization != expectedAuth {
					t.Fatal("unexpected registry credentials")
				}
				found = found || authorization == expectedAuth
			}
			if !found {
				t.Fatal("expected credentials not sent")
			}
		})
	}

	t.Run("local retains ambient Docker credentials", func(t *testing.T) {
		googleCalls := 0
		google.Keychain = imageAnonymousKeychain{resolve: func() { googleCalls++ }}
		transport := &imageRegistryTransport{manifest: manifest, wantAuth: "Basic " + base64.StdEncoding.EncodeToString([]byte("ambient:secret"))}
		remote.DefaultTransport = transport
		_, _, _, err := (&Inspector{}).fetchImageBruteForce(context.Background(), InspectRequest{Image: "gcr.io/project/app:v1"})
		if err != nil || googleCalls != 1 {
			t.Fatalf("local fetch err=%v, Google keychain calls=%d", err, googleCalls)
		}
	})
}

type imageAnonymousKeychain struct{ resolve func() }

func (k imageAnonymousKeychain) Resolve(authn.Resource) (authn.Authenticator, error) {
	k.resolve()
	return authn.Anonymous, nil
}

func TestImagePodKeychainResolvesOnce(t *testing.T) {
	imageTestCache(t, imageTestSecret("team", "pod-secret", "gcr.io", "pod-user"))
	var checks atomic.Int32
	keychain := getPodKeychain("team", imageTestPod(), false, func(secret string) bool {
		if secret != "pod-secret" {
			t.Errorf("unexpected Secret %s", secret)
		}
		checks.Add(1)
		return true
	})
	if checks.Load() != 0 {
		t.Fatal("Secret checked before keychain resolution")
	}
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() { assertImageCredentials(t, keychain, "gcr.io/project/app:v1", "pod-user") })
	}
	wg.Wait()
	if got := checks.Load(); got != 1 {
		t.Fatalf("Secret checked %d times, want 1", got)
	}
}
