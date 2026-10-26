package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/cloudinstall"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func enrollFlags() cloudStartupFlags {
	return cloudStartupFlags{
		HubURL:            "https://hub.example.com/",
		EnrollmentKeyFile: "/etc/radar/enrollment/key",
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

func enrolledDeployment() *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "radar", Namespace: "radar"},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "radar",
			Args: []string{"--cloud-hub-url=https://hub.example.com", "--cloud-enrollment-key-file=/etc/radar/enrollment/key"},
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
	if !rt.AlreadyCloud || !rt.UsesEnrollment() || rt.HubURL != "https://hub.example.com" {
		t.Fatalf("runtime = %+v", rt)
	}
	if missing := missingCloudConfiguration(rt); len(missing) != 0 {
		t.Fatalf("enrollment-only config reported missing %v", missing)
	}
}

func TestEnrolledStatusPointsAtTheLog(t *testing.T) {
	target := cloudinstall.RadarTarget{Namespace: "edge-ns", DeploymentName: "edge-radar"}
	target.Runtime = cloudinstall.DeploymentRuntime{
		AlreadyCloud: true, EnrollmentConfigured: true,
		CloudModeConfigured: true, CloudMode: true, HubURLConfigured: true, HubURL: "https://hub.example.com",
		StatusObserved: true, DesiredReplicas: 1, TotalReplicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1,
	}
	kc := fake.NewClientset()
	res := checkHubTunnelStatus(context.Background(), kc, target)
	if res.verdict != hubTunnelUnavailable || !strings.Contains(res.summary, "joins the Hub with an enrollment key") || !strings.HasSuffix(res.next, "kubectl -n edge-ns logs deploy/edge-radar") {
		t.Fatalf("result = %+v", res)
	}
	if len(kc.Actions()) != 0 {
		t.Fatalf("status made Kubernetes calls: %v", kc.Actions())
	}
	var out bytes.Buffer
	printCloudStatus(&out, target)
	if !strings.Contains(out.String(), "Hub: https://hub.example.com (joins with an enrollment key)") {
		t.Fatalf("output:\n%s", out.String())
	}
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
