package main

import (
	"bytes"
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/cloudinstall"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func enrollFlags() cloudStartupFlags {
	return cloudStartupFlags{
		HubURL:            "https://hub.example.com/",
		EnrollmentKeyFile: "/etc/radar/enrollment/key",
		TokenSecret:       "radar-cloud-token",
		PodNamespace:      "radar",
		HelmRelease:       "radar",
	}
}

func TestResolveCloudStartup(t *testing.T) {
	t.Run("nothing configured stays local", func(t *testing.T) {
		got, err := resolveCloudStartup(cloudStartupFlags{})
		if err != nil || got.mode != cloudOff {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
	t.Run("token path is unchanged", func(t *testing.T) {
		got, err := resolveCloudStartup(cloudStartupFlags{CloudURL: "wss://hub.example.com/agent", Token: "rhc_x", ClusterName: "prod"})
		if err != nil || got.mode != cloudTokenMode {
			t.Fatalf("got %+v, %v", got, err)
		}
		if _, err := resolveCloudStartup(cloudStartupFlags{CloudURL: "wss://hub.example.com/agent", Token: "rhc_x"}); err == nil || err.Error() != "--cloud-url requires --cloud-token and --cluster-name" {
			t.Fatalf("token without a cluster name: %v", err)
		}
		if _, err := resolveCloudStartup(cloudStartupFlags{CloudURL: "wss://hub.example.com/agent"}); err == nil {
			t.Fatal("cloud-url without a token was accepted")
		}
	})
	t.Run("a token wins over the key", func(t *testing.T) {
		f := enrollFlags()
		f.CloudURL, f.Token, f.ClusterName = "wss://hub.example.com/agent", "rhc_x", "prod"
		got, err := resolveCloudStartup(f)
		if err != nil || got.mode != cloudTokenMode || len(got.warnings) != 1 {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
	t.Run("enrollment", func(t *testing.T) {
		f := enrollFlags()
		f.NamePrefix = "nkp-"
		got, err := resolveCloudStartup(f)
		if err != nil || got.mode != cloudEnrollMode || got.hubOrigin != "https://hub.example.com" || got.names.Prefix != "nkp-" {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
	for _, tc := range []struct {
		name   string
		mutate func(*cloudStartupFlags)
		want   string
	}{
		{"no hub URL", func(f *cloudStartupFlags) { f.HubURL = "" }, "--cloud-hub-url"},
		{"plaintext remote hub", func(f *cloudStartupFlags) { f.HubURL = "http://hub.example.com" }, "https://"},
		{"insecure skip verify", func(f *cloudStartupFlags) { f.InsecureSkipVerify = true }, "--cloud-ca-file"},
		{"no token Secret", func(f *cloudStartupFlags) { f.TokenSecret = "" }, "--cloud-token-secret"},
		{"bad Secret name", func(f *cloudStartupFlags) { f.TokenSecret = "Bad_Name" }, "not a valid Secret name"},
		{"no release env", func(f *cloudStartupFlags) { f.HelmRelease = "" }, "RADAR_HELM_RELEASE"},
		{"name too long", func(f *cloudStartupFlags) { f.ClusterName = strings.Repeat("x", 81) }, "--cluster-name"},
		{"missing CA file", func(f *cloudStartupFlags) { f.CAFile = filepath.Join(t.TempDir(), "nope") }, "--cloud-ca-file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := enrollFlags()
			tc.mutate(&f)
			if _, err := resolveCloudStartup(f); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
	t.Run("loopback http hub is allowed", func(t *testing.T) {
		f := enrollFlags()
		f.HubURL = "http://127.0.0.1:8080"
		if got, err := resolveCloudStartup(f); err != nil || got.mode != cloudEnrollMode {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
}

func TestResetEnrollmentClearsEveryKey(t *testing.T) {
	kc := fake.NewClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "radar-cloud-token", Namespace: "radar", Annotations: map[string]string{"helm.sh/resource-policy": "keep"}},
		Data: map[string][]byte{
			cloud.SecretKeyToken: []byte("rhc_x"), cloud.SecretKeyHubOrigin: []byte("https://hub"), cloud.SecretKeyStatus: []byte("refused"),
			cloud.SecretKeyError: []byte("token rejected"), cloud.SecretKeyAt: []byte("2026-10-24T00:00:00Z"),
		},
	})
	var out, errOut bytes.Buffer
	if code := runResetEnrollment(context.Background(), kc, "radar", "radar-cloud-token", &out, &errOut); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut.String())
	}
	s, _ := kc.CoreV1().Secrets("radar").Get(context.Background(), "radar-cloud-token", metav1.GetOptions{})
	if len(s.Data) != 0 {
		t.Fatalf("data left after reset: %v", s.Data)
	}
	if s.Annotations["helm.sh/resource-policy"] != "keep" {
		t.Fatal("reset dropped the Secret's annotations")
	}
	if !strings.Contains(out.String(), "Cleared Secret radar/radar-cloud-token") {
		t.Fatalf("stdout = %q", out.String())
	}
	for _, a := range kc.Actions() {
		if a.GetVerb() == "delete" || a.GetVerb() == "create" {
			t.Fatalf("reset sent %s", a.GetVerb())
		}
	}

	out.Reset()
	if code := runResetEnrollment(context.Background(), kc, "radar", "radar-cloud-token", &out, &errOut); code != 0 || !strings.Contains(out.String(), "already empty") {
		t.Fatalf("second reset: exit %d, stdout %q", code, out.String())
	}
}

func TestResetEnrollmentRefusesOtherSecretsAndReportsMissing(t *testing.T) {
	kc := fake.NewClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "tls", Namespace: "radar"},
		Data:       map[string][]byte{"tls.crt": []byte("x"), "tls.key": []byte("y")},
	})
	var out, errOut bytes.Buffer
	if code := runResetEnrollment(context.Background(), kc, "radar", "tls", &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "not a Radar token Secret") {
		t.Fatalf("exit %d, stderr %q", code, errOut.String())
	}
	s, _ := kc.CoreV1().Secrets("radar").Get(context.Background(), "tls", metav1.GetOptions{})
	if len(s.Data) != 2 {
		t.Fatal("an unrelated Secret was cleared")
	}
	errOut.Reset()
	if code := runResetEnrollment(context.Background(), kc, "radar", "missing", &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "was not found") {
		t.Fatalf("exit %d, stderr %q", code, errOut.String())
	}
}

func TestCloudResetEnrollmentHelp(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := cloudResetEnrollment([]string{"--help"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "radar cloud reset-enrollment") {
		t.Fatalf("exit %d, stdout %q", code, out.String())
	}
}

func enrolledDeployment() *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "radar", Namespace: "radar"},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "radar",
			Args: []string{"--cloud-hub-url=https://hub.example.com", "--cloud-enrollment-key-file=/etc/radar/enrollment/key", "--cloud-token-secret=radar-cloud-token"},
			Env:  []corev1.EnvVar{{Name: "RADAR_CLOUD_MODE", Value: "true"}},
		}}}}},
	}
}

func TestDiscoveryAcceptsEnrollmentOnlyConfig(t *testing.T) {
	kc := fake.NewClientset(func() *appsv1.Deployment {
		d := enrolledDeployment()
		d.Labels = map[string]string{"helm.sh/chart": "radar-1.19.0", "app.kubernetes.io/instance": "radar"}
		return d
	}())
	result, err := cloudinstall.DiscoverRadarTargets(context.Background(), kc, nil, cloudinstall.DiscoveryOptions{Namespace: "radar", ReleaseName: "radar"})
	if err != nil || len(result.Selected) != 1 {
		t.Fatalf("discovery: %+v %v", result, err)
	}
	rt := result.Selected[0].Runtime
	if !rt.AlreadyCloud || !rt.UsesEnrollment() || rt.HubURL != "https://hub.example.com" || rt.EnrollmentTokenSecret != "radar-cloud-token" {
		t.Fatalf("runtime = %+v", rt)
	}
	if missing := missingCloudConfiguration(rt); len(missing) != 0 {
		t.Fatalf("enrollment-only config reported missing %v", missing)
	}
}

func TestEnrolledStatusShowsEnrollStatus(t *testing.T) {
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/agent/status":
			if r.Header.Get("Authorization") != "Bearer rhc_joined" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"cluster_id":"c1","status":"connected"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer hub.Close()
	wss := "ws" + strings.TrimPrefix(hub.URL, "http") + "/agent"

	target := cloudinstall.RadarTarget{Namespace: "radar", DeploymentName: "radar"}
	target.Runtime = cloudinstall.DeploymentRuntime{
		AlreadyCloud: true, EnrollmentConfigured: true, EnrollmentTokenSecret: "radar-cloud-token",
		CloudModeConfigured: true, CloudMode: true, HubURLConfigured: true, HubURL: hub.URL,
	}
	secret := func(data map[string]string) *fake.Clientset {
		d := map[string][]byte{}
		for k, v := range data {
			d[k] = []byte(v)
		}
		return fake.NewClientset(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "radar-cloud-token", Namespace: "radar"}, Data: d})
	}

	t.Run("joined and connected", func(t *testing.T) {
		kc := secret(map[string]string{cloud.SecretKeyToken: "rhc_joined", cloud.SecretKeyWSSURL: wss, cloud.SecretKeyStatus: "joined", cloud.SecretKeyAt: "2026-10-24T01:02:03Z"})
		res := checkHubTunnelStatus(context.Background(), kc, target, "")
		if res.verdict != hubTunnelHealthy || res.enrollStatus != "joined" {
			t.Fatalf("result = %+v", res)
		}
		var out bytes.Buffer
		printHubTunnelStatus(&out, target, res)
		if !strings.Contains(out.String(), "Enrollment: joined (since 2026-10-24T01:02:03Z)") {
			t.Fatalf("output:\n%s", out.String())
		}
	})
	t.Run("refused shows the error and the reset command", func(t *testing.T) {
		kc := secret(map[string]string{cloud.SecretKeyToken: "rhc_old", cloud.SecretKeyWSSURL: wss, cloud.SecretKeyStatus: "refused", cloud.SecretKeyError: "token rejected; run radar cloud reset-enrollment to join again"})
		res := checkHubTunnelStatus(context.Background(), kc, target, "")
		var out bytes.Buffer
		printHubTunnelStatus(&out, target, res)
		if res.verdict != hubTunnelUnhealthy || !strings.Contains(out.String(), "Enrollment error: token rejected") || !strings.Contains(out.String(), "radar cloud reset-enrollment --namespace radar --secret radar-cloud-token") {
			t.Fatalf("output:\n%s", out.String())
		}
		if got := cloudTargetStatusHeadline(target, res); !strings.Contains(got, "needs its enrollment reset") {
			t.Fatalf("headline = %q", got)
		}
	})
	t.Run("still joining", func(t *testing.T) {
		kc := secret(map[string]string{cloud.SecretKeyNonce: "n", cloud.SecretKeyStatus: "waiting", cloud.SecretKeyError: "the Hub is not ready to add clusters yet"})
		res := checkHubTunnelStatus(context.Background(), kc, target, "")
		if res.verdict != hubTunnelUnavailable || res.enrollStatus != "waiting" || res.enrollError == "" {
			t.Fatalf("result = %+v", res)
		}
	})
	t.Run("missing Secret", func(t *testing.T) {
		res := checkHubTunnelStatus(context.Background(), fake.NewClientset(), target, "")
		if res.verdict != hubTunnelUnhealthy || !strings.Contains(res.summary, "token Secret was not found") {
			t.Fatalf("result = %+v", res)
		}
	})
}

func TestCloudCAFileFlagLoads(t *testing.T) {
	// LoadCAFile's PEM handling is covered in internal/cloud; this checks the
	// flag reaches it and an empty bundle fails startup.
	path := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(path, []byte("not pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := enrollFlags()
	f.CAFile = path
	if _, err := resolveCloudStartup(f); err == nil || !strings.Contains(err.Error(), "no PEM certificates") {
		t.Fatalf("err = %v", err)
	}
}

func TestStatusTrustsTheHubCAThatRadarMounts(t *testing.T) {
	// httptest's TLS certificate is self-signed, so only an explicit CA
	// bundle makes it trusted: the same position as a Hub with a private CA.
	hub := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent/status" || r.Header.Get("Authorization") != "Bearer rhc_joined" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"cluster_id":"c1","status":"connected"}`))
	}))
	defer hub.Close()
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: hub.Certificate().Raw}))
	wss := "wss" + strings.TrimPrefix(hub.URL, "https") + "/agent"

	// The Deployment as the chart renders it with cloud.caBundleConfigMap.
	deployment := enrolledDeployment()
	c := &deployment.Spec.Template.Spec.Containers[0]
	c.Args = append(c.Args, "--cloud-ca-file=/etc/radar/cloud-ca/ca.crt")
	c.VolumeMounts = []corev1.VolumeMount{{Name: "cloud-ca", MountPath: "/etc/radar/cloud-ca", ReadOnly: true}}
	deployment.Spec.Template.Spec.Volumes = []corev1.Volume{{Name: "cloud-ca", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
		LocalObjectReference: corev1.LocalObjectReference{Name: "corp-ca"},
		Items:                []corev1.KeyToPath{{Key: "bundle.pem", Path: "ca.crt"}},
	}}}}
	deployment.Labels = map[string]string{"helm.sh/chart": "radar-1.19.0", "app.kubernetes.io/instance": "radar"}

	tokenSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "radar-cloud-token", Namespace: "radar"}, Data: map[string][]byte{
		cloud.SecretKeyToken: []byte("rhc_joined"), cloud.SecretKeyWSSURL: []byte(wss), cloud.SecretKeyStatus: []byte("joined"),
	}}
	caConfigMap := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "corp-ca", Namespace: "radar"}, Data: map[string]string{"bundle.pem": caPEM}}

	discover := func(t *testing.T, objs ...runtimeObject) (cloudinstall.RadarTarget, *fake.Clientset) {
		t.Helper()
		kc := fake.NewClientset(objs...)
		result, err := cloudinstall.DiscoverRadarTargets(context.Background(), kc, nil, cloudinstall.DiscoveryOptions{Namespace: "radar", ReleaseName: "radar"})
		if err != nil || len(result.Selected) != 1 {
			t.Fatalf("discovery: %+v %v", result, err)
		}
		return result.Selected[0], kc
	}

	t.Run("the mounted ConfigMap is found and trusted", func(t *testing.T) {
		target, kc := discover(t, deployment.DeepCopy(), tokenSecret.DeepCopy(), caConfigMap.DeepCopy())
		if target.Runtime.CloudCAConfigMap != "corp-ca" || target.Runtime.CloudCAConfigMapKey != "bundle.pem" {
			t.Fatalf("CA source = %q/%q", target.Runtime.CloudCAConfigMap, target.Runtime.CloudCAConfigMapKey)
		}
		if res := checkHubTunnelStatus(context.Background(), kc, target, ""); res.verdict != hubTunnelHealthy {
			t.Fatalf("result = %+v", res)
		}
	})
	t.Run("without the CA the call fails", func(t *testing.T) {
		plain := enrolledDeployment()
		plain.Labels = deployment.Labels
		target, kc := discover(t, plain, tokenSecret.DeepCopy())
		if res := checkHubTunnelStatus(context.Background(), kc, target, ""); res.verdict == hubTunnelHealthy {
			t.Fatalf("trusted a private CA without a bundle: %+v", res)
		}
	})
	t.Run("--ca-file overrides", func(t *testing.T) {
		plain := enrolledDeployment()
		plain.Labels = deployment.Labels
		target, kc := discover(t, plain, tokenSecret.DeepCopy())
		path := filepath.Join(t.TempDir(), "ca.pem")
		if err := os.WriteFile(path, []byte(caPEM), 0o600); err != nil {
			t.Fatal(err)
		}
		if res := checkHubTunnelStatus(context.Background(), kc, target, path); res.verdict != hubTunnelHealthy {
			t.Fatalf("result = %+v", res)
		}
	})
	t.Run("an unreadable ConfigMap says what to do", func(t *testing.T) {
		target, kc := discover(t, deployment.DeepCopy(), tokenSecret.DeepCopy())
		res := checkHubTunnelStatus(context.Background(), kc, target, "")
		if !strings.Contains(res.summary, "CA bundle could not be read") || !strings.Contains(res.next, "--ca-file") {
			t.Fatalf("result = %+v", res)
		}
	})
	t.Run("token mode uses the same CA", func(t *testing.T) {
		tokenDeployment := deployment.DeepCopy()
		tc := &tokenDeployment.Spec.Template.Spec.Containers[0]
		tc.Args = []string{"--cloud-url=" + wss, "--cluster-name=c1", "--cloud-ca-file=/etc/radar/cloud-ca/ca.crt"}
		tc.Env = append(tc.Env, corev1.EnvVar{Name: "RADAR_CLOUD_TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "radar-cloud-config"}, Key: "token",
		}}})
		cfg := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "radar-cloud-config", Namespace: "radar"}, Data: map[string][]byte{"token": []byte("rhc_joined")}}
		target, kc := discover(t, tokenDeployment, cfg, caConfigMap.DeepCopy())
		if res := checkHubTunnelStatus(context.Background(), kc, target, ""); res.verdict != hubTunnelHealthy {
			t.Fatalf("result = %+v", res)
		}
	})
}

type runtimeObject = k8sruntime.Object

// enrolledInstall is a Radar Deployment and its token Secret as the chart
// renders them for one release.
func enrolledInstall(release, tokenSecret string) []runtimeObject {
	d := enrolledDeployment()
	d.Name = release
	if release != "radar" {
		d.Name = release + "-radar"
	}
	d.Labels = map[string]string{"helm.sh/chart": "radar-1.19.0", "app.kubernetes.io/instance": release}
	d.Spec.Template.Spec.Containers[0].Args = []string{
		"--cloud-hub-url=https://hub.example.com",
		"--cloud-enrollment-key-file=/etc/radar/cloud-enrollment/key",
		"--cloud-token-secret=" + tokenSecret,
	}
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: tokenSecret, Namespace: "radar"}, Data: map[string][]byte{
		cloud.SecretKeyToken: []byte("rhc_" + release), cloud.SecretKeyHubOrigin: []byte("https://hub.example.com"),
	}}
	return []runtimeObject{d, s}
}

func TestResetEnrollmentFindsTheTokenSecret(t *testing.T) {
	for _, tc := range []struct {
		name, release, secret, flagRelease string
	}{
		{name: "default release", release: "radar", secret: "radar-cloud-token"},
		{name: "custom release name", release: "edge", secret: "edge-radar-cloud-token"},
		{name: "custom tokenSecretName", release: "radar", secret: "fleet-token"},
		{name: "explicit --release", release: "edge", secret: "edge-radar-cloud-token", flagRelease: "edge"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kc := fake.NewClientset(enrolledInstall(tc.release, tc.secret)...)
			got, err := findEnrollmentTokenSecret(context.Background(), kc, "radar", tc.flagRelease)
			if err != nil || got != tc.secret {
				t.Fatalf("got %q, %v; want %q", got, err, tc.secret)
			}
			var out, errOut bytes.Buffer
			if code := runResetEnrollment(context.Background(), kc, "radar", got, &out, &errOut); code != 0 {
				t.Fatalf("reset exit %d: %s", code, errOut.String())
			}
			s, _ := kc.CoreV1().Secrets("radar").Get(context.Background(), tc.secret, metav1.GetOptions{})
			if len(s.Data) != 0 {
				t.Fatalf("Secret %s not cleared", tc.secret)
			}
		})
	}
}

func TestResetEnrollmentNeedsReleaseWithTwoInstalls(t *testing.T) {
	objs := append(enrolledInstall("radar", "radar-cloud-token"), enrolledInstall("edge", "edge-radar-cloud-token")...)
	kc := fake.NewClientset(objs...)
	_, err := findEnrollmentTokenSecret(context.Background(), kc, "radar", "")
	if err == nil || !strings.Contains(err.Error(), "choose one with --release") || !strings.Contains(err.Error(), `release "edge"`) || !strings.Contains(err.Error(), `release "radar"`) {
		t.Fatalf("err = %v", err)
	}
	for _, a := range kc.Actions() {
		if a.GetVerb() == "update" {
			t.Fatal("a Secret was changed while the target was ambiguous")
		}
	}
	got, err := findEnrollmentTokenSecret(context.Background(), kc, "radar", "edge")
	if err != nil || got != "edge-radar-cloud-token" {
		t.Fatalf("with --release: %q, %v", got, err)
	}
}

func TestResetEnrollmentWithoutAnEnrolledRadar(t *testing.T) {
	_, err := findEnrollmentTokenSecret(context.Background(), fake.NewClientset(), "radar", "")
	if err == nil || !strings.Contains(err.Error(), "no Radar in namespace") || !strings.Contains(err.Error(), "--secret") {
		t.Fatalf("err = %v", err)
	}
	_, err = findEnrollmentTokenSecret(context.Background(), fake.NewClientset(enrolledInstall("radar", "radar-cloud-token")...), "radar", "other")
	if err == nil || !strings.Contains(err.Error(), `release "other"`) {
		t.Fatalf("err = %v", err)
	}
}
