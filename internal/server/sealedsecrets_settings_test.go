package server

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

// ctlDeploy is a ready sealed-secrets controller Deployment with the given
// container args and env, its pods labelled name=<name>.
func ctlDeploy(ns, name string, args []string, env []corev1.EnvVar) *appsv1.Deployment {
	one := int32(1)
	d := deploy(ns, name, map[string]string{"app.kubernetes.io/name": "sealed-secrets"}, &one, 1)
	d.Spec.Template.Labels = map[string]string{"name": name}
	d.Spec.Template.Spec.Containers = []corev1.Container{{
		Name: "sealed-secrets-controller", Image: "docker.io/bitnami/sealed-secrets-controller:0.41.2",
		Command: []string{"controller"}, Args: args, Env: env,
		Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}, {Name: "metrics", ContainerPort: 8081}},
	}}
	return d
}

func TestReadSealedSecretsSettings(t *testing.T) {
	env := func(name, value string) corev1.EnvVar { return corev1.EnvVar{Name: name, Value: value} }
	for _, tc := range []struct {
		name      string
		args      []string
		env       []corev1.EnvVar
		envFrom   bool
		order     string
		legacy    string
		allNs     controllerSetting
		additions controllerSetting
	}{
		{name: "defaults", order: keyOrderCertNotBefore, legacy: "sealed-secrets-key"},
		{name: "equals form", args: []string{"--key-order-priority=SecretCreationTimestamp", "--key-prefix=custom"}, order: keyOrderCreationTime, legacy: "custom"},
		// The Helm chart passes values as separate arguments.
		{name: "separate value", args: []string{"--update-status", "--key-prefix", "chart-key", "--additional-namespaces", "a,b"}, order: keyOrderCertNotBefore, legacy: "chart-key",
			additions: controllerSetting{Value: "a,b", Set: true}},
		{name: "bool flag takes no separate value", args: []string{"--all-namespaces", "--key-prefix", "k"}, order: keyOrderCertNotBefore, legacy: "k",
			allNs: controllerSetting{Value: "true", Set: true}},
		{name: "env", env: []corev1.EnvVar{env("SEALED_SECRETS_KEY_ORDER_PRIORITY", "SecretCreationTimestamp"), env("SEALED_SECRETS_ALL_NAMESPACES", "false")}, order: keyOrderCreationTime, legacy: "sealed-secrets-key",
			allNs: controllerSetting{Value: "false", Set: true}},
		{name: "argument wins over env", args: []string{"--key-order-priority=CertNotBefore"}, env: []corev1.EnvVar{env("SEALED_SECRETS_KEY_ORDER_PRIORITY", "SecretCreationTimestamp")}, order: keyOrderCertNotBefore, legacy: "sealed-secrets-key"},
		{name: "value from a reference is unknown", env: []corev1.EnvVar{{Name: "SEALED_SECRETS_KEY_ORDER_PRIORITY", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{Key: "k"}}}}, order: keyOrderUnknown, legacy: "sealed-secrets-key"},
		{name: "envFrom makes every unset flag unknown", envFrom: true, order: keyOrderUnknown, legacy: "",
			allNs: controllerSetting{Unknown: true}, additions: controllerSetting{Unknown: true}},
		{name: "envFrom doesn't override an argument", args: []string{"--key-prefix=x", "--key-order-priority=CertNotBefore"}, envFrom: true, order: keyOrderCertNotBefore, legacy: "x",
			allNs: controllerSetting{Unknown: true}, additions: controllerSetting{Unknown: true}},
		{name: "variable in an argument is unknown", args: []string{"--key-prefix=$(PREFIX)"}, order: keyOrderCertNotBefore, legacy: ""},
		{name: "invalid order falls back like the controller", args: []string{"--key-order-priority=Bogus"}, order: keyOrderCertNotBefore, legacy: "sealed-secrets-key"},
	} {
		d := ctlDeploy("kube-system", "sealed-secrets-controller", tc.args, tc.env)
		if tc.envFrom {
			d.Spec.Template.Spec.Containers[0].EnvFrom = []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{}}}
		}
		s := readSealedSecretsSettings(d)
		if got := s.keyOrder(); got != tc.order {
			t.Errorf("%s: key order %q, want %q", tc.name, got, tc.order)
		}
		if got := s.legacyKeyName(); got != tc.legacy {
			t.Errorf("%s: legacy %q, want %q", tc.name, got, tc.legacy)
		}
		if s.AllNamespaces != tc.allNs || s.AdditionalNamespaces != tc.additions {
			t.Errorf("%s: all %+v additional %+v, want %+v %+v", tc.name, s.AllNamespaces, s.AdditionalNamespaces, tc.allNs, tc.additions)
		}
	}
}

func TestReadSealedSecretsSettingsPicksTheControllerContainer(t *testing.T) {
	d := ctlDeploy("kube-system", "sealed-secrets-controller", []string{"--key-prefix=mine"}, nil)
	sidecar := corev1.Container{Name: "proxy", Image: "envoy", EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{}}}}
	d.Spec.Template.Spec.Containers = append([]corev1.Container{sidecar}, d.Spec.Template.Spec.Containers...)
	if got := readSealedSecretsSettings(d).legacyKeyName(); got != "mine" {
		t.Fatalf("legacy = %q, want the controller container's prefix", got)
	}
}

func TestChooseControllerFor(t *testing.T) {
	global := ctlDeploy("kube-system", "sealed-secrets-controller", nil, nil)
	tenant := ctlDeploy("team-x", "sealed-secrets", []string{"--all-namespaces=false"}, nil)
	listing := ctlDeploy("ops", "sealed-secrets", []string{"--additional-namespaces", "team-z,team-w"}, nil)
	decoy := ctlDeploy("aaa", "sealed-secrets-controller", nil, nil)
	other := ctlDeploy("bbb", "sealed-secrets", nil, nil)
	unknown := ctlDeploy("ccc", "sealed-secrets", nil, nil)
	unknown.Spec.Template.Spec.Containers[0].EnvFrom = []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{}}}
	sharded := ctlDeploy("team-x", "sealed-secrets-shard", []string{"--all-namespaces=false", "--label-selector=shard=a"}, nil)

	for _, tc := range []struct {
		name       string
		in         []*appsv1.Deployment
		ns         string
		want       *appsv1.Deployment
		reason     string
		notWatched bool
	}{
		{name: "tenant's own namespace goes to its dedicated controller", in: []*appsv1.Deployment{global, tenant}, ns: "team-x", want: tenant},
		{name: "other namespaces go to the global controller", in: []*appsv1.Deployment{global, tenant}, ns: "team-y", want: global},
		{name: "a listed namespace goes to the listing controller", in: []*appsv1.Deployment{global, listing}, ns: "team-w", want: listing},
		{name: "a controller with additional namespaces doesn't watch everything", in: []*appsv1.Deployment{listing}, ns: "team-y", notWatched: true},
		{name: "its own namespace still counts", in: []*appsv1.Deployment{global, listing}, ns: "ops", want: listing},
		{name: "a look-alike watching everything doesn't beat kubeseal's default", in: []*appsv1.Deployment{decoy, global}, ns: "team-y", want: global},
		{name: "two controllers watching everything, neither the default", in: []*appsv1.Deployment{decoy, other}, ns: "team-y", reason: ambiguousOverlap},
		{name: "two dedicated controllers for one namespace", in: []*appsv1.Deployment{tenant, ctlDeploy("team-x", "second", []string{"--all-namespaces=false"}, nil)}, ns: "team-x", reason: ambiguousOverlap},
		{name: "an unreadable scope beside another controller", in: []*appsv1.Deployment{global, unknown}, ns: "team-y", reason: ambiguousUnknownScope},
		{name: "the only controller is chosen even when its scope is unreadable", in: []*appsv1.Deployment{unknown}, ns: "team-y", want: unknown},
		{name: "a label selector can't be checked here", in: []*appsv1.Deployment{global, sharded}, ns: "team-x", reason: ambiguousUnknownScope},
		{name: "a single controller that doesn't watch the namespace", in: []*appsv1.Deployment{tenant}, ns: "team-y", notWatched: true},
		{name: "none", in: nil, ns: "team-y", notWatched: true},
	} {
		got, _, reason, watched := chooseControllerFor(tc.in, tc.ns)
		if got != tc.want || reason != tc.reason || watched == tc.notWatched {
			name := func(d *appsv1.Deployment) string {
				if d == nil {
					return "<nil>"
				}
				return d.Namespace + "/" + d.Name
			}
			t.Errorf("%s: got %s reason %q watched %v, want %s reason %q watched %v", tc.name, name(got), reason, watched, name(tc.want), tc.reason, !tc.notWatched)
		}
	}
}

func TestSealingKeysFollowTheControllerKeyOrder(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	// A key restored from a backup: an old certificate in a Secret created today.
	objs := []runtime.Object{
		sealingKeySecret("generated", "kube-system", "active", sealingKeyPEM(t, now.Add(-24*time.Hour)), now.Add(-24*time.Hour)),
		sealingKeySecret("restored", "kube-system", "active", sealingKeyPEM(t, now.Add(-300*24*time.Hour)), now),
	}
	active := func(keys []sealingKey) string {
		for _, k := range keys {
			if k.Active {
				return k.Name
			}
		}
		return ""
	}
	byCert := sealingKeysFromSecrets(objs, sealedSecretsSettings{})
	byCreation := sealingKeysFromSecrets(objs, sealedSecretsSettings{KeyOrder: controllerSetting{Value: "SecretCreationTimestamp", Set: true}})
	unknown := sealingKeysFromSecrets(objs, sealedSecretsSettings{KeyOrder: controllerSetting{Set: true, Unknown: true}})
	if got := active(byCert); got != "generated" {
		t.Errorf("certificate order: active %q, want generated", got)
	}
	if got := active(byCreation); got != "restored" || byCreation[0].Name != "restored" {
		t.Errorf("creation order: active %q first %q, want restored", got, byCreation[0].Name)
	}
	if got := active(unknown); got != "" {
		t.Errorf("unknown order: active %q, want none", got)
	}
	// "Valid from" stays the certificate's date whatever the order.
	for _, k := range byCreation {
		if k.Name == "restored" && !strings.HasPrefix(k.NotBefore, now.Add(-300*24*time.Hour).Format("2006-01-02")) {
			t.Errorf("restored notBefore = %q", k.NotBefore)
		}
	}

	// Equal ordering times: the controller keeps the earliest created.
	cert := sealingKeyPEM(t, now.Add(-time.Hour))
	tie := sealingKeysFromSecrets([]runtime.Object{
		sealingKeySecret("a-later", "kube-system", "active", cert, now),
		sealingKeySecret("z-earlier", "kube-system", "active", cert, now.Add(-time.Minute)),
	}, sealedSecretsSettings{})
	if got := active(tie); got != "z-earlier" {
		t.Errorf("tie: active %q, want z-earlier", got)
	}
}

func TestSealingKeysLegacyKeyAsTheControllerLoadsIt(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	objs := []runtime.Object{
		sealingKeySecret("sealed-secrets-key", "kube-system", "compromised", sealingKeyPEM(t, now), now),
		sealingKeySecret("custom", "kube-system", "", sealingKeyPEM(t, now.Add(-time.Hour)), now.Add(-time.Hour)),
		sealingKeySecret("sealed-secrets-keyold", "kube-system", "compromised", sealingKeyPEM(t, now.Add(-2*time.Hour)), now.Add(-2*time.Hour)),
	}
	keys := sealingKeysFromSecrets(objs, sealedSecretsSettings{})
	byName := map[string]sealingKey{}
	for _, k := range keys {
		byName[k.Name] = k
	}
	if k := byName["sealed-secrets-key"]; k.NotUsed || !k.Active {
		t.Errorf("legacy key with a non-active label = %+v, want in use and sealing", k)
	}
	if _, listed := byName["custom"]; listed {
		t.Errorf("unlabelled Secret that isn't the legacy key was listed")
	}
	if k := byName["sealed-secrets-keyold"]; !k.NotUsed {
		t.Errorf("non-active labelled key = %+v, want not used", k)
	}

	withPrefix := sealingKeysFromSecrets(objs, sealedSecretsSettings{KeyPrefix: controllerSetting{Value: "custom", Set: true}})
	names := map[string]sealingKey{}
	for _, k := range withPrefix {
		names[k.Name] = k
	}
	if k, ok := names["custom"]; !ok || k.NotUsed {
		t.Errorf("--key-prefix=custom: legacy key = %+v (listed %v), want in use", k, ok)
	}
	if k := names["sealed-secrets-key"]; !k.NotUsed {
		t.Errorf("--key-prefix=custom: default-named Secret = %+v, want not used", k)
	}
}

// seedTwoControllers: the default controller in kube-system and a tenant
// controller in team-x serving only team-x, each with its own key.
func seedTwoControllers(t *testing.T) {
	t.Helper()
	restoreFixtureCache(t)
	k8s.ResetResourceCache()
	now := time.Now().UTC()
	client := fake.NewClientset(
		ctlDeploy("kube-system", "sealed-secrets-controller", nil, nil),
		ctlDeploy("team-x", "sealed-secrets", []string{"--all-namespaces=false"}, nil),
		sealingKeySecret("sealed-secrets-keyglobal", "kube-system", "active", sealingKeyPEM(t, now.Add(-time.Hour)), now.Add(-time.Hour)),
		sealingKeySecret("sealed-secrets-keytenant", "team-x", "active", sealingKeyPEM(t, now.Add(-2*time.Hour)), now.Add(-2*time.Hour)),
	)
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("init cache: %v", err)
	}
	seedCNPGWorkspace(t, sealedSecretKinds, sealedSecret("team-x", "a", "True"), sealedSecret("team-y", "b", "False"))
}

func TestSealedSecretsControllerPerNamespace(t *testing.T) {
	seedTwoControllers(t)
	for _, tc := range []struct {
		ns, wantNS, wantName, wantKey string
		atDefault                     bool
	}{
		{"team-x", "team-x", "sealed-secrets", "sealed-secrets-keytenant", false},
		{"team-y", "kube-system", "sealed-secrets-controller", "sealed-secrets-keyglobal", true},
	} {
		resp, err := http.Get(testServer.URL + "/api/sealedsecrets/controller?sealedSecretNamespace=" + tc.ns)
		if err != nil {
			t.Fatal(err)
		}
		got, raw := getSealedSecretsController(t, resp)
		if got.ControllerState != "ok" || got.Controller == nil || got.Controller.Namespace != tc.wantNS || got.Controller.Name != tc.wantName || got.AtKubesealDefault != tc.atDefault {
			t.Errorf("%s: controller %+v (%s)", tc.ns, got.Controller, raw)
		}
		if got.KeysState != "ok" || len(got.Keys) != 1 || got.Keys[0].Name != tc.wantKey || !got.Keys[0].Active || got.KeyOrder != keyOrderCertNotBefore {
			t.Errorf("%s: keys %+v order %q", tc.ns, got.Keys, got.KeyOrder)
		}
		// Choosing the controller must not narrow the totals to that namespace.
		if got.Counts == nil || *got.Counts != (sealedSecretCounts{Total: 2, Synced: 1, NotSynced: 1}) {
			t.Errorf("%s: counts %+v, want the whole cluster", tc.ns, got.Counts)
		}
	}

	resp, err := http.Get(testServer.URL + "/api/sealedsecrets/controller?sealedSecretNamespace=Not_A_Namespace")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid namespace: status %d, want 400", resp.StatusCode)
	}
}

func TestSealedSecretsControllerNotWatched(t *testing.T) {
	restoreFixtureCache(t)
	k8s.ResetResourceCache()
	client := fake.NewClientset(ctlDeploy("team-x", "sealed-secrets", []string{"--all-namespaces=false"}, nil))
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("init cache: %v", err)
	}
	seedCNPGWorkspace(t, sealedSecretKinds, sealedSecret("team-y", "b", "False"))
	resp, err := http.Get(testServer.URL + "/api/sealedsecrets/controller?sealedSecretNamespace=team-y")
	if err != nil {
		t.Fatal(err)
	}
	got, raw := getSealedSecretsController(t, resp)
	if got.ControllerState != "notWatched" || got.Controller != nil || got.KeysState != "unknown" || strings.Contains(raw, "team-x") {
		t.Fatalf("got %+v (%s), want notWatched with nothing named", got, raw)
	}
}

// Every caller gets the same "ambiguous" answer, with its reason and nothing
// that names the candidates.
func TestSealedSecretsControllerAmbiguousIsTheSameForEveryCaller(t *testing.T) {
	restoreFixtureCache(t)
	k8s.ResetResourceCache()
	client := fake.NewClientset(
		ctlDeploy("team-x", "sealed-secrets", []string{"--all-namespaces=false"}, nil),
		ctlDeploy("team-x", "second", []string{"--all-namespaces=false"}, nil),
	)
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("init cache: %v", err)
	}
	seedCNPGWorkspace(t, sealedSecretKinds)

	resp, err := http.Get(testServer.URL + "/api/sealedsecrets/controller?sealedSecretNamespace=team-x")
	if err != nil {
		t.Fatal(err)
	}
	anon, anonRaw := getSealedSecretsController(t, resp)

	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"team-x"}}
	perms.SetCanI("get", "apps", "deployments", "team-x", false)
	env.srv.permCache.Set("vic", nil, perms)
	user, userRaw := getSealedSecretsController(t, env.authGet(t, "/api/sealedsecrets/controller?sealedSecretNamespace=team-x", "vic", ""))

	for who, got := range map[string]sealedSecretsControllerResponse{"anonymous": anon, "limited user": user} {
		if got.ControllerState != "ambiguous" || got.AmbiguousReason != ambiguousOverlap || got.Controller != nil || got.KeysState != "unknown" {
			t.Errorf("%s: %+v, want ambiguous/overlap with no controller or keys", who, got)
		}
	}
	for _, raw := range []string{anonRaw, userRaw} {
		if strings.Contains(raw, "second") || strings.Contains(raw, `"name"`) {
			t.Errorf("ambiguous answer names a candidate: %s", raw)
		}
	}
}

// A caller denied the controller still learns whether it sits at kubeseal's
// default location (what a bare `kubeseal --fetch-cert` reaches), never its
// namespace or name otherwise.
func TestSealedSecretsControllerDeniedAtKubesealDefault(t *testing.T) {
	seedTwoControllers(t)
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"team-x", "team-y"}}
	perms.SetCanI("get", "apps", "deployments", "kube-system", false)
	perms.SetCanI("get", "apps", "deployments", "team-x", false)
	perms.SetCanI("list", "", "secrets", "kube-system", false)
	perms.SetCanI("list", "", "secrets", "team-x", false)
	env.srv.permCache.Set("dana", nil, perms)
	for _, tc := range []struct {
		ns        string
		atDefault bool
	}{{"team-y", true}, {"team-x", false}} {
		got, raw := getSealedSecretsController(t, env.authGet(t, "/api/sealedsecrets/controller?sealedSecretNamespace="+tc.ns, "dana", ""))
		if got.ControllerState != "denied" || got.Controller != nil || got.AtKubesealDefault != tc.atDefault {
			t.Errorf("%s: %+v, want denied with atKubesealDefault %v", tc.ns, got, tc.atDefault)
		}
		if strings.Contains(raw, `"namespace"`) || strings.Contains(raw, `"name"`) {
			t.Errorf("%s: denied answer names the controller: %s", tc.ns, raw)
		}
	}
}
