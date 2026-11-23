package helm

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

// The permission preflight uses the serving cluster's capabilities.
func TestStillAdmittedRefusesAWriteAfterTheConnectionChanged(t *testing.T) {
	t.Cleanup(k8s.SnapshotTestConnection())
	k8s.RetireServingConnection()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected, Context: "a"})
	admitted, err := k8s.CaptureServingConnection()
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(admitted.Bind(t.Context()))

	rec := httptest.NewRecorder()
	if !stillAdmitted(rec, r) {
		t.Fatalf("refused on the admitted connection: %d %s", rec.Code, rec.Body)
	}
	k8s.RetireServingConnection()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected, Context: "b"})
	rec = httptest.NewRecorder()
	if stillAdmitted(rec, r) || rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "context_changed") {
		t.Fatalf("after a switch: %d %s", rec.Code, rec.Body)
	}
	// Unbound callers (MCP, API) are not affected.
	rec = httptest.NewRecorder()
	if !stillAdmitted(rec, httptest.NewRequest(http.MethodPost, "/", nil)) {
		t.Fatalf("an unbound request was refused: %d", rec.Code)
	}
}

func TestBoundHelmActionConfigStaysOnAdmittedConnectionAcrossSwitch(t *testing.T) {
	t.Cleanup(k8s.SnapshotTestConnection())
	discovery := fake.NewSimpleClientset().Discovery()
	if _, err := k8s.InstallTestServingConnection("a", &rest.Config{Host: "https://a"}, nil, discovery); err != nil {
		t.Fatal(err)
	}
	admitted, err := k8s.CaptureServingConnection()
	if err != nil {
		t.Fatal(err)
	}
	ctx := admitted.Bind(t.Context())
	if _, err := k8s.InstallTestServingConnection("b", &rest.Config{Host: "https://b"}, nil, discovery); err != nil {
		t.Fatal(err)
	}

	client := &Client{}
	for _, username := range []string{"", "alice"} {
		requestCtx := ctx
		var groups []string
		if username != "" {
			groups = []string{"team-a"}
			requestCtx = pkgauth.ContextWithUser(ctx, &pkgauth.User{Username: username, Groups: groups})
		}
		action, err := client.actionConfigForWrite(requestCtx, "default")
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := action.RESTClientGetter.ToRESTConfig()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Host != "https://a" || cfg.Impersonate.UserName != username || !slices.Equal(cfg.Impersonate.Groups, groups) {
			t.Errorf("host %q, impersonated user %q, groups %v; want https://a, %q, %v", cfg.Host, cfg.Impersonate.UserName, cfg.Impersonate.Groups, username, groups)
		}
	}
}

// With a single kubeconfig file, unbound Helm reads the file on every action,
// so an edit that points the context at another server retargets it. A bound
// write keeps the config Radar admitted.
func TestBoundHelmActionConfigIgnoresAnEditedKubeconfig(t *testing.T) {
	t.Cleanup(k8s.SnapshotTestConnection())
	path := filepath.Join(t.TempDir(), "config")
	edited := "apiVersion: v1\nkind: Config\ncurrent-context: a\n" +
		"clusters: [{name: a, cluster: {server: \"https://b\"}}]\n" +
		"users: [{name: a, user: {token: t}}]\n" +
		"contexts: [{name: a, context: {cluster: a, user: a}}]\n"
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := k8s.InstallTestServingConnection("a", &rest.Config{Host: "https://a"}, nil, fake.NewSimpleClientset().Discovery()); err != nil {
		t.Fatal(err)
	}
	admitted, err := k8s.CaptureServingConnection()
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{kubeconfig: path}

	unbound, err := client.actionConfigForWrite(t.Context(), "default")
	if err != nil {
		t.Fatal(err)
	}
	if cfg, err := unbound.RESTClientGetter.ToRESTConfig(); err != nil || cfg.Host != "https://b" {
		t.Fatalf("unbound Helm should follow the file (the case this guards): host %v, err %v", cfg, err)
	}

	bound, err := client.actionConfigForWrite(admitted.Bind(t.Context()), "default")
	if err != nil {
		t.Fatal(err)
	}
	if cfg, err := bound.RESTClientGetter.ToRESTConfig(); err != nil || cfg.Host != "https://a" {
		t.Fatalf("bound write: host %v, err %v; want https://a", cfg, err)
	}
}
