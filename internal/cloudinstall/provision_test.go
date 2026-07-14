package cloudinstall

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestCloudInstallValues(t *testing.T) {
	v := cloudInstallValues("wss://api.radarhq.io/agent", "k3Fg-9pA", false)
	cloud, _ := v["cloud"].(map[string]any)
	if cloud["enabled"] != true {
		t.Error("cloud.enabled must be true")
	}
	if cloud["url"] != "wss://api.radarhq.io/agent" {
		t.Errorf("cloud.url = %v", cloud["url"])
	}
	if cloud["clusterName"] != "k3Fg-9pA" {
		t.Errorf("cloud.clusterName must carry the cluster id, got %v", cloud["clusterName"])
	}
	if cloud["existingSecret"] != CloudTokenSecretName {
		t.Errorf("cloud.existingSecret = %v", cloud["existingSecret"])
	}
	// Token must never appear in helm values (it lives in the Secret).
	if _, ok := cloud["token"]; ok {
		t.Error("cloud.token must NOT be set in values — token goes in the Secret")
	}
	if auth, _ := v["auth"].(map[string]any); auth["mode"] != "proxy" {
		t.Errorf("auth.mode must be proxy, got %v", v["auth"])
	}
	if rbac, _ := v["rbac"].(map[string]any); rbac["selfUpgrade"] != true {
		t.Errorf("rbac.selfUpgrade must default on, got %v", v["rbac"])
	}
}

func TestCloudInstallValuesCanDisableSelfUpgrade(t *testing.T) {
	v := cloudInstallValues("wss://api.radarhq.io/agent", "cluster", true)
	rbac, _ := v["rbac"].(map[string]any)
	if rbac["selfUpgrade"] != false {
		t.Fatalf("rbac.selfUpgrade = %v, want false", rbac["selfUpgrade"])
	}
}

func TestCloudAdoptionValuesPreserveFeatureRBACUnlessEnabled(t *testing.T) {
	preserved := cloudAdoptionValues("wss://api.radarhq.io/agent", "cluster", false, false)
	if _, ok := preserved["auth"]; ok {
		t.Fatal("adoption must preserve the existing auth value")
	}
	rbac, _ := preserved["rbac"].(map[string]any)
	if len(rbac) != 1 || rbac["selfUpgrade"] != true {
		t.Fatalf("default adoption RBAC = %#v, want only selfUpgrade=true", rbac)
	}

	enabled := cloudAdoptionValues("wss://api.radarhq.io/agent", "cluster", true, true)
	rbac, _ = enabled["rbac"].(map[string]any)
	for _, key := range []string{"helm", "secrets", "podExec", "portForward", "metrics"} {
		if rbac[key] != true {
			t.Errorf("rbac.%s = %v, want true", key, rbac[key])
		}
	}
	if rbac["selfUpgrade"] != false {
		t.Errorf("rbac.selfUpgrade = %v, want explicit opt-out", rbac["selfUpgrade"])
	}
}

func fakeWithSecretCreateMetadata() *fake.Clientset {
	kc := fake.NewSimpleClientset()
	kc.PrependReactor("create", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		create := action.(k8stesting.CreateAction)
		secret := create.GetObject().(*corev1.Secret).DeepCopy()
		secret.UID = types.UID("uid-created")
		secret.ResourceVersion = "1"
		gvr := schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
		if err := kc.Tracker().Create(gvr, secret, create.GetNamespace()); err != nil {
			return true, nil, err
		}
		return true, secret, nil
	})
	return kc
}

func TestTokenSecret_CreateOnlyRejectsExisting(t *testing.T) {
	kc := fakeWithSecretCreateMetadata()
	ctx := context.Background()

	created, err := createTokenSecret(ctx, kc, "radar", "rhc_first")
	if err != nil {
		t.Fatal(err)
	}
	if created.UID != "uid-created" || created.ResourceVersion != "1" {
		t.Fatalf("unexpected identity: %+v", created)
	}
	got, err := kc.CoreV1().Secrets("radar").Get(ctx, CloudTokenSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.StringData[cloudTokenSecretKey] != "rhc_first" {
		t.Errorf("first token = %q", got.StringData[cloudTokenSecretKey])
	}

	_, err = createTokenSecret(ctx, kc, "radar", "rhc_second")
	var exists *TokenSecretExistsError
	if !errors.As(err, &exists) {
		t.Fatalf("expected TokenSecretExistsError, got %T: %v", err, err)
	}
	got, _ = kc.CoreV1().Secrets("radar").Get(ctx, CloudTokenSecretName, metav1.GetOptions{})
	if got.StringData[cloudTokenSecretKey] != "rhc_first" {
		t.Errorf("existing token was overwritten, got %q", got.StringData[cloudTokenSecretKey])
	}
}

func TestCheckTokenSecretAvailable_TypedAndReadErrors(t *testing.T) {
	ctx := context.Background()
	if err := CheckTokenSecretAvailable(ctx, fake.NewSimpleClientset(), "radar"); err != nil {
		t.Fatalf("missing Secret should pass: %v", err)
	}
	kc := fake.NewSimpleClientset(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: CloudTokenSecretName, Namespace: "radar", UID: "existing", ResourceVersion: "9",
	}})
	err := CheckTokenSecretAvailable(ctx, kc, "radar")
	var exists *TokenSecretExistsError
	if !errors.As(err, &exists) || exists.UID != "existing" || exists.ResourceVersion != "9" {
		t.Fatalf("expected typed Secret metadata, got %T: %+v", err, exists)
	}

	want := errors.New("apiserver unavailable")
	broken := fake.NewSimpleClientset()
	broken.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, want
	})
	if err := CheckTokenSecretAvailable(ctx, broken, "radar"); !errors.Is(err, want) {
		t.Fatalf("expected read error to propagate, got %v", err)
	}
}

func TestDeleteTokenSecretIfUnchanged_GuardsIdentity(t *testing.T) {
	ctx := context.Background()
	kc := fakeWithSecretCreateMetadata()
	created, err := createTokenSecret(ctx, kc, "radar", "rhc_token")
	if err != nil {
		t.Fatal(err)
	}
	if err := deleteTokenSecretIfUnchanged(ctx, kc, *created); err != nil {
		t.Fatalf("delete unchanged Secret: %v", err)
	}
	if _, err := kc.CoreV1().Secrets("radar").Get(ctx, CloudTokenSecretName, metav1.GetOptions{}); err == nil {
		t.Fatal("unchanged Secret was not deleted")
	}

	kc = fakeWithSecretCreateMetadata()
	created, err = createTokenSecret(ctx, kc, "radar", "rhc_token")
	if err != nil {
		t.Fatal(err)
	}
	current, _ := kc.CoreV1().Secrets("radar").Get(ctx, CloudTokenSecretName, metav1.GetOptions{})
	current.ResourceVersion = "2"
	if _, err := kc.CoreV1().Secrets("radar").Update(ctx, current, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := deleteTokenSecretIfUnchanged(ctx, kc, *created); err == nil {
		t.Fatal("changed Secret must not be deleted")
	}
	if _, err := kc.CoreV1().Secrets("radar").Get(ctx, CloudTokenSecretName, metav1.GetOptions{}); err != nil {
		t.Fatalf("changed Secret should remain: %v", err)
	}
}

func TestEnsureNamespace(t *testing.T) {
	ctx := context.Background()
	// Missing → created.
	kc := fake.NewSimpleClientset()
	if err := ensureNamespace(ctx, kc, "radar"); err != nil {
		t.Fatal(err)
	}
	if _, err := kc.CoreV1().Namespaces().Get(ctx, "radar", metav1.GetOptions{}); err != nil {
		t.Fatalf("namespace not created: %v", err)
	}
	// Already exists → no error.
	kc2 := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "radar"}})
	if err := ensureNamespace(ctx, kc2, "radar"); err != nil {
		t.Fatalf("existing namespace must be a no-op: %v", err)
	}
}

func TestProvisionPrepared_RequiresPlanAndFields(t *testing.T) {
	kc := fake.NewSimpleClientset()
	if err := ProvisionPrepared(context.Background(), kc, nil, ProvisionConfig{}); err == nil {
		t.Error("expected error on nil plan")
	}
	if err := validateProvisionConfig(ProvisionConfig{Token: "t", CloudURL: "https://not-websocket.example", ClusterID: "c"}); err == nil {
		t.Error("expected non-WebSocket cloud URL to fail")
	}
}

func TestValidateProvisionConfigCloudURLTransportPolicy(t *testing.T) {
	for _, raw := range []string{"wss://api.radarhq.io/agent", "ws://localhost:9091/agent", "ws://127.0.0.1:9091/agent", "ws://[::1]:9091/agent"} {
		t.Run("valid_"+raw, func(t *testing.T) {
			if err := validateProvisionConfig(ProvisionConfig{Token: "t", CloudURL: raw, ClusterID: "c"}); err != nil {
				t.Fatalf("validateProvisionConfig(%q): %v", raw, err)
			}
		})
	}
	for _, raw := range []string{"ws://api.radarhq.io/agent", "ws://10.0.0.1/agent", "ws://localhost.example/agent"} {
		t.Run("invalid_"+raw, func(t *testing.T) {
			if err := validateProvisionConfig(ProvisionConfig{Token: "t", CloudURL: raw, ClusterID: "c"}); err == nil {
				t.Fatalf("validateProvisionConfig(%q) unexpectedly succeeded", raw)
			}
		})
	}
}
