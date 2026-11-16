package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/cloudinstall"
	"k8s.io/client-go/kubernetes/fake"
)

func TestEnrollmentKeygenCreatesMatchingKeysWithoutOverwriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "enrollment.pem")
	var out, errOut bytes.Buffer
	if code := cloudEnrollmentKeygen([]string{"--out", path}, &out, &errOut); code != 0 {
		t.Fatalf("exit=%d output=%s", code, &errOut)
	}
	privateKey, err := cloud.ReadEnrollmentPrivateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	publicPEM, err := os.ReadFile(path + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	block, rest := pem.Decode(publicPEM)
	if block == nil || block.Type != "PUBLIC KEY" || len(rest) != 0 {
		t.Fatal("public file must be SPKI PUBLIC KEY PEM")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, ok := parsed.(ed25519.PublicKey)
	if !ok || !bytes.Equal(publicKey, privateKey[32:]) {
		t.Fatal("private/public files do not match")
	}
	if strings.Contains(out.String()+errOut.String(), "BEGIN PRIVATE KEY") {
		t.Fatal("private material printed")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("private permissions should be0600: %v %v", info, err)
		}
	}
	if code := cloudEnrollmentKeygen([]string{"--out", path}, &out, &errOut); code != 1 {
		t.Fatalf("existing private file overwritten, exit=%d", code)
	}
	again, err := cloud.ReadEnrollmentPrivateKey(path)
	if err != nil || !bytes.Equal(again, privateKey) {
		t.Fatal("existing private key changed")
	}
}

func TestEnrollmentKeygenPublicFileConflictLeavesNoNewPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "enrollment.pem")
	if err := os.WriteFile(path+".pub", []byte("existing public file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeEnrollmentKeyPair(path); err == nil {
		t.Fatal("public file conflict accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("failed command left a private key: %v", err)
	}
	public, err := os.ReadFile(path + ".pub")
	if err != nil || string(public) != "existing public file" {
		t.Fatal("existing public file changed")
	}
}

func TestResolveSignedEnrollmentStartup(t *testing.T) {
	flags := enrollFlags()
	flags.EnrollmentKeyFile = ""
	flags.EnrollmentPrivateKeyFile = "/etc/radar/enrollment/private-key"
	flags.EnrollmentOrganization = "org_customer"
	if result, err := resolveCloudStartup(flags); err != nil || result.mode != cloudEnrollMode {
		t.Fatalf("valid signing setup rejected: %+v %v", result, err)
	}
	for _, mutate := range []func(*cloudStartupFlags){
		func(f *cloudStartupFlags) { f.Token = "rhc_legacy" },
		func(f *cloudStartupFlags) { f.CloudURL = "wss://hub.example.com/agent" },
		func(f *cloudStartupFlags) { f.EnrollmentKeyFile = "/etc/radar/key" },
		func(f *cloudStartupFlags) { f.EnrollmentOrganization = "" },
		func(f *cloudStartupFlags) { f.EnrollmentOrganization = "org,another" },
		func(f *cloudStartupFlags) { f.InsecureSkipVerify = true },
	} {
		candidate := flags
		mutate(&candidate)
		if _, err := resolveCloudStartup(candidate); err == nil {
			t.Fatalf("unsafe signing startup accepted: %+v", candidate)
		}
	}
}

func TestPublicKeyEnrollmentDiscoveryAndStatus(t *testing.T) {
	deployment := enrolledDeployment()
	deployment.Labels = map[string]string{"helm.sh/chart": "radar-1.20.3", "app.kubernetes.io/instance": "radar"}
	deployment.Spec.Template.Spec.Containers[0].Args = []string{
		"--cloud-hub-url=https://hub.example.com",
		"--cloud-enrollment-private-key-file=/etc/radar/cloud-enrollment/private-key",
		"--cloud-enrollment-organization=org_customer",
	}
	client := fake.NewClientset(deployment)
	discovery, err := cloudinstall.DiscoverRadarTargets(context.Background(), client, nil, cloudinstall.DiscoveryOptions{Namespace: "radar", ReleaseName: "radar"})
	if err != nil || len(discovery.Selected) != 1 {
		t.Fatalf("discovery: %+v %v", discovery, err)
	}
	target := discovery.Selected[0]
	if !target.Runtime.UsesEnrollment() || !target.Runtime.EnrollmentPrivateKeyConfigured || target.Runtime.EnrollmentOrganization != "org_customer" {
		t.Fatalf("public-key runtime not discovered: %+v", target.Runtime)
	}
	if missing := missingCloudConfiguration(target.Runtime); len(missing) != 0 {
		t.Fatalf("signed setup incorrectly requires a bearer token: %v", missing)
	}
	status := checkHubTunnelStatus(context.Background(), client, target)
	if !strings.Contains(status.summary, "public-key enrollment") {
		t.Fatalf("wrong status explanation: %+v", status)
	}
	target.Runtime.EnrollmentOrganization = ""
	target.Runtime.EnrollmentOrganizationConfigured = false
	if missing := missingCloudConfiguration(target.Runtime); len(missing) != 1 || missing[0] != "enrollment organization" {
		t.Fatalf("missing signed org not reported: %v", missing)
	}
}
