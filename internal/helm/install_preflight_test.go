package helm

import (
	"errors"
	"fmt"
	"strings"
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
	mode, err := preInstallCheck(cfg, "caretta", "default")
	if err != nil {
		t.Fatalf("preInstallCheck: %v", err)
	}
	if mode != installFresh {
		t.Errorf("mode = %v, want installFresh", mode)
	}
}

func TestPreInstallCheck_PendingInstallSurfacesTypedError(t *testing.T) {
	cfg := memoryActionConfig(t)
	seedRelease(t, cfg, "caretta", release.StatusPendingInstall, 1)

	_, err := preInstallCheck(cfg, "caretta", "default")
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

func TestPreInstallCheck_FailedRoutesToUpgrade(t *testing.T) {
	cfg := memoryActionConfig(t)
	seedRelease(t, cfg, "caretta", release.StatusFailed, 1)

	mode, err := preInstallCheck(cfg, "caretta", "default")
	if err != nil {
		t.Fatalf("preInstallCheck: %v", err)
	}
	if mode != installUpgrade {
		t.Errorf("mode = %v, want installUpgrade (action.Upgrade fallback handles Failed)", mode)
	}
}

// TestPreInstallCheck_UninstalledRoutesToReplace pins routing for the
// helm-uninstall-with-keep-history scenario. action.Upgrade rejects
// Uninstalled with ErrNoDeployedReleases (upgrade.go:231-233 only falls back
// for Failed/Superseded), so the recovery must use action.Install with
// Replace=true (install.go:549 accepts Uninstalled+Failed for Replace).
func TestPreInstallCheck_UninstalledRoutesToReplace(t *testing.T) {
	cfg := memoryActionConfig(t)
	seedRelease(t, cfg, "caretta", release.StatusUninstalled, 1)

	mode, err := preInstallCheck(cfg, "caretta", "default")
	if err != nil {
		t.Fatalf("preInstallCheck: %v", err)
	}
	if mode != installReplace {
		t.Errorf("mode = %v, want installReplace — Upgrade rejects Uninstalled, only Install.Replace handles it", mode)
	}
}

// TestPreInstallCheck_MultiRevisionUsesLatest pins that classification reads
// the latest revision, not an arbitrary one. Multi-revision history (e.g. v1
// failed, v2 retry) must route off the most recent state — reading an older
// revision misroutes the install (recoverable vs in-flight vs deployed).
func TestPreInstallCheck_MultiRevisionUsesLatest(t *testing.T) {
	cfg := memoryActionConfig(t)
	seedRelease(t, cfg, "caretta", release.StatusDeployed, 1)
	seedRelease(t, cfg, "caretta", release.StatusFailed, 2)

	mode, err := preInstallCheck(cfg, "caretta", "default")
	if err != nil {
		t.Fatalf("preInstallCheck: %v", err)
	}
	if mode != installUpgrade {
		t.Errorf("mode = %v — latest revision is v2/failed, expected installUpgrade", mode)
	}
	var exists *ReleaseExistsError
	if errors.As(err, &exists) {
		t.Errorf("got ReleaseExistsError — classifier read v1/deployed instead of v2/failed: %+v", exists)
	}
}

// TestPreInstallCheck_UnknownStatusFailsClosed pins fail-closed behavior: a
// status not enumerated in the switch (e.g. a future helm version adds a new
// in-flight tier) must surface as ReleasePendingError, never silently flow
// into the recoverable upgrade --install branch.
func TestPreInstallCheck_UnknownStatusFailsClosed(t *testing.T) {
	cfg := memoryActionConfig(t)
	seedRelease(t, cfg, "caretta", release.Status("pending-test"), 1)

	_, err := preInstallCheck(cfg, "caretta", "default")
	var pending *ReleasePendingError
	if !errors.As(err, &pending) {
		t.Fatalf("expected *ReleasePendingError for unknown status, got %T: %v", err, err)
	}
	if pending.Status != "pending-test" {
		t.Errorf("status = %q, want pending-test", pending.Status)
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

// TestClassifyInstallError pins the (status, code, message) mapping for every
// branch the frontend depends on. Streaming and non-streaming endpoints both go
// through this classifier, so a regression here breaks both UIs at once.
func TestClassifyInstallError(t *testing.T) {
	rbacErr := errors.New(`Unable to continue: clusterroles.rbac.authorization.k8s.io "x" is forbidden: ` +
		`User "u" cannot get resource "clusterroles" in API group "rbac.authorization.k8s.io" at the cluster scope`)

	cases := []struct {
		name        string
		err         error
		wantStatus  int
		wantCode    string
		msgContains string
	}{
		{"pending", &ReleasePendingError{Name: "x", Namespace: "y", Status: "pending-install", Revision: 1}, 409, "release_pending", "uninstall and retry"},
		{"exists", &ReleaseExistsError{Name: "x", Namespace: "y", Revision: 2}, 409, "release_exists", "use upgrade"},
		{"rbac_preflight", rbacErr, 403, "rbac_preflight", "missing get on clusterroles.rbac.authorization.k8s.io"},
		{"forbidden_generic", errors.New("user is forbidden"), 403, "", "insufficient permissions"},
		{"unclassified", errors.New("connection refused"), 500, "", "connection refused"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cls := classifyInstallError(tc.err)
			if cls.Status != tc.wantStatus {
				t.Errorf("status = %d, want %d", cls.Status, tc.wantStatus)
			}
			if cls.Code != tc.wantCode {
				t.Errorf("code = %q, want %q", cls.Code, tc.wantCode)
			}
			if !strings.Contains(cls.Message, tc.msgContains) {
				t.Errorf("message %q missing %q", cls.Message, tc.msgContains)
			}
		})
	}
}

// TestInstallStreamErrorEvent ensures the SSE envelope carries the same
// friendly message and error_code as the JSON HTTP path. A typo in the
// field name ("errorCode" vs "error_code") would silently break the frontend's
// install-stream branching; this pins the wire format.
func TestInstallStreamErrorEvent(t *testing.T) {
	event := installStreamErrorEvent(&ReleasePendingError{Name: "x", Namespace: "y", Status: "pending-install", Revision: 1})
	if event["type"] != "error" {
		t.Errorf(`type = %v, want "error"`, event["type"])
	}
	if event["error_code"] != "release_pending" {
		t.Errorf("error_code = %v, want release_pending", event["error_code"])
	}
	msg, _ := event["message"].(string)
	if !strings.Contains(msg, "uninstall and retry") {
		t.Errorf("message = %q, want friendly text including %q", msg, "uninstall and retry")
	}

	noCode := installStreamErrorEvent(errors.New("connection refused"))
	if _, present := noCode["error_code"]; present {
		t.Errorf("unclassified error should omit error_code, got %v", noCode["error_code"])
	}
}
