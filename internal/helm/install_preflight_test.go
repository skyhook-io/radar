package helm

import (
	"errors"
	"fmt"
	"testing"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chartutil"
	kubefake "helm.sh/helm/v3/pkg/kube/fake"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage"
	"helm.sh/helm/v3/pkg/storage/driver"
	helmtime "helm.sh/helm/v3/pkg/time"
)

func memoryActionConfig(t *testing.T) *action.Configuration {
	t.Helper()
	mem := driver.NewMemory()
	mem.SetNamespace("default")
	return &action.Configuration{
		Releases:     storage.Init(mem),
		KubeClient:   &kubefake.PrintingKubeClient{},
		Capabilities: chartutil.DefaultCapabilities,
		Log:          func(string, ...interface{}) {},
	}
}

func seedRelease(t *testing.T, cfg *action.Configuration, name string, status release.Status, version int) {
	t.Helper()
	rel := &release.Release{
		Name:      name,
		Namespace: "default",
		Version:   version,
		Info: &release.Info{
			Status:        status,
			FirstDeployed: helmtime.Now(),
			LastDeployed:  helmtime.Now(),
		},
	}
	if err := cfg.Releases.Create(rel); err != nil {
		t.Fatalf("seed release: %v", err)
	}
}

func TestPreInstallCheck_NoPriorRelease(t *testing.T) {
	cfg := memoryActionConfig(t)
	fresh, err := preInstallCheck(cfg, "caretta", "default")
	if err != nil {
		t.Fatalf("preInstallCheck: %v", err)
	}
	if !fresh {
		t.Error("expected fresh=true when no record exists")
	}
}

func TestPreInstallCheck_PendingInstallSurfacesTypedError(t *testing.T) {
	cfg := memoryActionConfig(t)
	seedRelease(t, cfg, "caretta", release.StatusPendingInstall, 1)

	fresh, err := preInstallCheck(cfg, "caretta", "default")
	if fresh {
		t.Error("expected fresh=false")
	}
	var pending *ReleasePendingError
	if !errors.As(err, &pending) {
		t.Fatalf("expected *ReleasePendingError, got %T: %v", err, err)
	}
	if pending.Name != "caretta" || pending.Status != "pending-install" || pending.Revision != 1 {
		t.Errorf("unexpected detail: %+v", pending)
	}
}

func TestPreInstallCheck_DeployedSurfacesExistsError(t *testing.T) {
	cfg := memoryActionConfig(t)
	seedRelease(t, cfg, "caretta", release.StatusDeployed, 3)

	_, err := preInstallCheck(cfg, "caretta", "default")
	var exists *ReleaseExistsError
	if !errors.As(err, &exists) {
		t.Fatalf("expected *ReleaseExistsError, got %T: %v", err, err)
	}
	if exists.Revision != 3 {
		t.Errorf("revision = %d, want 3", exists.Revision)
	}
}

func TestPreInstallCheck_UninstallingSurfacesPendingError(t *testing.T) {
	cfg := memoryActionConfig(t)
	seedRelease(t, cfg, "caretta", release.StatusUninstalling, 2)

	_, err := preInstallCheck(cfg, "caretta", "default")
	var pending *ReleasePendingError
	if !errors.As(err, &pending) {
		t.Fatalf("expected *ReleasePendingError for an in-flight uninstall, got %T: %v", err, err)
	}
	if pending.Status != "uninstalling" {
		t.Errorf("status = %q, want uninstalling", pending.Status)
	}
}

func TestPreInstallCheck_FailedAllowsRecovery(t *testing.T) {
	cfg := memoryActionConfig(t)
	seedRelease(t, cfg, "caretta", release.StatusFailed, 1)

	fresh, err := preInstallCheck(cfg, "caretta", "default")
	if err != nil {
		t.Fatalf("preInstallCheck: %v", err)
	}
	if fresh {
		t.Error("expected fresh=false (an entry exists), so caller uses upgrade --install")
	}
}

func TestClassifyHelmRBACError(t *testing.T) {
	// Real Helm pre-flight error string from caretta install in cloud-mode.
	raw := errors.New(`install failed: Unable to continue with install: ` +
		`could not get information about the resource ClusterRole "caretta-grafana-clusterrole" in namespace "": ` +
		`clusterroles.rbac.authorization.k8s.io "caretta-grafana-clusterrole" is forbidden: ` +
		`User "user_01KPX4JSPW3G41BBD1NVM5BP2A" cannot get resource "clusterroles" in API group "rbac.authorization.k8s.io" at the cluster scope`)

	d, ok := classifyHelmRBACError(raw)
	if !ok {
		t.Fatal("expected classifyHelmRBACError to recognize the Helm pre-flight RBAC error")
	}
	if d.User != "user_01KPX4JSPW3G41BBD1NVM5BP2A" {
		t.Errorf("user = %q", d.User)
	}
	if d.Verb != "get" {
		t.Errorf("verb = %q, want get", d.Verb)
	}
	if d.Resource != "clusterroles" {
		t.Errorf("resource = %q, want clusterroles", d.Resource)
	}
	if d.Group != "rbac.authorization.k8s.io" {
		t.Errorf("group = %q, want rbac.authorization.k8s.io", d.Group)
	}
}

func TestClassifyHelmRBACError_NotMatching(t *testing.T) {
	cases := []error{
		nil,
		errors.New("install failed: timeout"),
		errors.New("cannot re-use a name that is still in use"),
		fmt.Errorf("some random %s error", "transient"),
	}
	for _, e := range cases {
		if _, ok := classifyHelmRBACError(e); ok {
			t.Errorf("classifyHelmRBACError(%v) should not match", e)
		}
	}
}

func TestClassifyInstallErrorCode(t *testing.T) {
	if got := classifyInstallErrorCode(&ReleasePendingError{Name: "x", Namespace: "y", Status: "pending-install"}); got != "release_pending" {
		t.Errorf("got %q, want release_pending", got)
	}
	if got := classifyInstallErrorCode(&ReleaseExistsError{Name: "x", Namespace: "y", Revision: 2}); got != "release_exists" {
		t.Errorf("got %q, want release_exists", got)
	}
	if got := classifyInstallErrorCode(errors.New("install failed: ... is forbidden: User \"u\" cannot get resource \"clusterroles\" in API group \"rbac.authorization.k8s.io\"")); got != "rbac_preflight" {
		t.Errorf("got %q, want rbac_preflight", got)
	}
	if got := classifyInstallErrorCode(errors.New("connection refused")); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
