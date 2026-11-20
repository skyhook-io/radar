package images

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
)

func TestECRCredentialScopeAndTokenReuse(t *testing.T) {
	const allowed = "123456789012.dkr.ecr.us-east-1.amazonaws.com"
	var calls int
	kc := newECRKeychain(allowed+", registry.example.com", func(_ context.Context, registry, account, region string) (authn.Authenticator, time.Time, error) {
		calls++
		if registry != allowed || account != "123456789012" || region != "us-east-1" {
			t.Fatalf("unexpected token scope: %s %s %s", registry, account, region)
		}
		return &authn.Basic{Username: "AWS", Password: "test-token"}, time.Now().Add(time.Hour), nil
	})
	for _, host := range []string{allowed, allowed, allowed + ".example.com", "987654321012.dkr.ecr.us-east-1.amazonaws.com", "registry.example.com"} {
		repo, err := name.NewRepository(host + "/test")
		if err != nil {
			t.Fatal(err)
		}
		auth, err := kc.ResolveContext(context.Background(), repo)
		if err != nil || (auth != authn.Anonymous) != (host == allowed) {
			t.Fatalf("host %q authorized=%v err=%v", host, auth != authn.Anonymous, err)
		}
	}
	if calls != 1 {
		t.Fatalf("authorization calls=%d, want one", calls)
	}
}

func TestECRCredentialFailureStaysUnassessed(t *testing.T) {
	const allowed = "123456789012.dkr.ecr.us-east-1.amazonaws.com"
	var calls int
	kc := newECRKeychain(allowed, func(context.Context, string, string, string) (authn.Authenticator, time.Time, error) {
		calls++
		return nil, time.Time{}, errors.New("raw error from credential provider")
	})
	repo, err := name.NewRepository(allowed + "/test")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		_, err = kc.ResolveContext(context.Background(), repo)
		if err != errECRCredentials || classifyFetchError(err).status != StatusAuthRequired {
			t.Fatalf("unexpected credential error: %v", err)
		}
	}
	if calls != 1 {
		t.Fatalf("authorization calls=%d, want one", calls)
	}
	fallback := &ecrFallbackKeychain{ecr: kc, fallback: &pullSecretKeychain{auths: map[string]DockerConfigEntry{
		allowed: {Username: "AWS", Password: "fixture-fallback"},
	}}}
	auth, err := fallback.ResolveContext(context.Background(), repo)
	if err != nil || auth == authn.Anonymous {
		t.Fatalf("configured fallback was not used: %v", err)
	}
}

func TestECRCredentialUsesRegionalAWSAPI(t *testing.T) {
	const registry = "123456789012.dkr.ecr.us-east-1.amazonaws.com"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			RegistryIDs []string `json:"registryIds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.RegistryIDs) != 1 || input.RegistryIDs[0] != "123456789012" ||
			!strings.Contains(r.Header.Get("Authorization"), "/us-east-1/ecr/aws4_request") {
			t.Errorf("unexpected signed authorization request for registry IDs %v, err=%v", input.RegistryIDs, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		_ = json.NewEncoder(w).Encode(map[string]any{"authorizationData": []map[string]any{{
			"proxyEndpoint":      "https://" + registry,
			"authorizationToken": base64.StdEncoding.EncodeToString([]byte("AWS:fixture-token")),
			"expiresAt":          time.Now().Add(time.Hour).Unix(),
		}}})
	}))
	defer srv.Close()
	t.Setenv("AWS_ACCESS_KEY_ID", "fixture-access-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "fixture-secret-key")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_DEFAULT_PROFILE", "")
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "credentials"))
	t.Setenv("AWS_ENDPOINT_URL_ECR", srv.URL)
	t.Setenv("AWS_IGNORE_CONFIGURED_ENDPOINT_URLS", "false")

	auth, expires, err := requestECRToken(context.Background(), registry, "123456789012", "us-east-1")
	if err != nil || !time.Now().Before(expires) {
		t.Fatalf("token request failed: %v", err)
	}
	got, err := auth.Authorization()
	if err != nil || got.Username != "AWS" || got.Password != "fixture-token" {
		t.Fatalf("token decoding failed: %v", err)
	}
}

func TestECRCredentialRequiresAdvisoryAndRadarGrant(t *testing.T) {
	const registry = "123456789012.dkr.ecr.us-east-1.amazonaws.com"
	kc := newECRKeychain(registry, nil)
	previous := runtimeECRKeychain
	runtimeECRKeychain = func() *ecrKeychain { return kc }
	t.Cleanup(func() { runtimeECRKeychain = previous })
	ref, err := name.ParseReference(registry + "/app:fixture")
	if err != nil {
		t.Fatal(err)
	}
	inv := &RuntimeInventory{}
	w := &work{ref: ref, advisory: true}
	if options := inv.credentials(w, false); len(options) != 1 || options[0].radar {
		t.Fatalf("advisory without ambient grant: %+v", options)
	}
	options := inv.credentials(w, true)
	if len(options) != 1 || !options[0].radar || !options[0].advisory {
		t.Fatalf("authorized advisory should use the opted-in ECR identity: %+v", options)
	}
	if keychain, _, _ := clusterKeychain(options[0], ref); !isECRKeychain(keychain) {
		t.Fatal("authorized advisory did not select the ECR keychain")
	}
	if keychain, _, _ := clusterKeychain(credential{radar: true}, ref); isECRKeychain(keychain) {
		t.Fatal("regular inventory selected the advisory ECR keychain")
	}
}

func isECRKeychain(keychain authn.Keychain) bool {
	_, ok := keychain.(*ecrFallbackKeychain)
	return ok
}
