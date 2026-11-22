package helm

import (
	"net/http"
	"net/http/httptest"
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
	admitted, err := k8s.CaptureBoundConnection()
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(k8s.WithBoundConnection(t.Context(), admitted))

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
	admitted, err := k8s.CaptureBoundConnection()
	if err != nil {
		t.Fatal(err)
	}
	ctx := k8s.WithBoundConnection(t.Context(), admitted)
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
