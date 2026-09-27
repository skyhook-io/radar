package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/connections"
	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/k8s"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/internal/settings"
)

func TestConfigurationOwnershipModes(t *testing.T) {
	t.Setenv("RADAR_CLOUD_MODE", "false")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	for _, tt := range []struct {
		name, authMode, pod, cloud, want, listen string
		file, tunnel                             bool
	}{
		{name: "local", want: "local"},
		{name: "proxy laptop", authMode: "proxy", want: "operator"},
		{name: "oidc laptop", authMode: "oidc", want: "operator"},
		{name: "pod with kubeconfig", pod: "10.0.0.1", want: "operator"},
		{name: "personal CLI in dev pod", pod: "10.0.0.1", listen: DefaultListenAddress, want: "local"},
		{name: "Helm with loopback listener", pod: "10.0.0.1", listen: DefaultListenAddress, file: true, want: "operator"},
		{name: "operator file", file: true, want: "operator"},
		{name: "cloud env", cloud: "true", authMode: "proxy", pod: "10.0.0.1", want: "cloud"},
		{name: "cloud tunnel", tunnel: true, authMode: "proxy", want: "cloud"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("RADAR_CLOUD_MODE", tt.cloud)
			t.Setenv("KUBERNETES_SERVICE_HOST", tt.pod)
			if tt.file {
				settings.SetOperatorConfig(&settings.OperatorConfig{Version: 1})
			}
			t.Cleanup(func() { settings.SetOperatorConfig(nil) })
			s := &Server{listenAddress: tt.listen, authConfig: auth.Config{Mode: tt.authMode}, cloudConnectCfg: CloudConnectConfig{CloudTunnelConfigured: tt.tunnel}}
			if got := s.configManagement(); got != tt.want {
				t.Fatalf("got %s, want %s", got, tt.want)
			}
		})
	}
}

func TestOperatorWritesDeniedBeforeSideEffects(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("RADAR_CLOUD_MODE", "false")
	s := &Server{authConfig: auth.Config{Mode: "proxy"}}
	original := config.Config{PrometheusURL: "http://original:9090", PrometheusHeaders: map[string]string{"Authorization": "secret"}}
	if err := config.Save(original); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	for name, handler := range map[string]http.HandlerFunc{
		"config":       s.handlePutConfig,
		"prometheus":   s.handleApplyPrometheusURL,
		"argocd":       s.handleApplyArgoCDConfig,
		"cost":         s.handleApplyCostSource,
		"audit":        s.handlePutAuditSettings,
		"preferences":  s.handlePutSettings,
		"context":      s.handleSwitchContext,
		"CAPI connect": s.handleCAPIClusterConnect,
	} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler(w, httptest.NewRequest(http.MethodPut, "/api/test", strings.NewReader("invalid json")))
			if w.Code != 403 || !strings.Contains(w.Body.String(), `"error_code":"operator_managed"`) {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
		})
	}
	after, err := os.ReadFile(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("rejected request changed config")
	}
	if _, err := os.Stat(settings.Path()); !os.IsNotExist(err) {
		t.Fatal("rejected request wrote settings")
	}
	old := k8s.ForceNamespaceScope
	k8s.ForceNamespaceScope = true
	t.Cleanup(func() { k8s.ForceNamespaceScope = old })
	w := httptest.NewRecorder()
	s.handleSetActiveNamespace(w, httptest.NewRequest(http.MethodPost, "/api/cluster/namespace", strings.NewReader(`{"namespaces":["other"]}`)))
	if w.Code != 403 {
		t.Fatalf("cache rescope status %d", w.Code)
	}
}

// seedLocalConfiguration writes both config.json and clusters.json and returns
// a reader of their combined bytes, so a test can prove a request changed neither.
func seedLocalConfiguration(t *testing.T, s *Server) func() string {
	t.Helper()
	if err := config.Save(config.Config{Port: 9280}); err != nil {
		t.Fatal(err)
	}
	target, err := k8s.CurrentProfileTarget()
	if err != nil {
		t.Fatal(err)
	}
	url := "http://saved.example"
	pending, err := s.localConnections.Prepare(target, connections.Update{Target: target, Revision: s.localConnections.Resolve(target, config.IntegrationMetrics, false).View.Revision, Kind: config.IntegrationMetrics, Action: "save", URL: &url})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.localConnections.Commit(t.Context(), pending); err != nil {
		t.Fatal(err)
	}
	return func() string {
		t.Helper()
		file, err := os.ReadFile(config.Path())
		if err != nil {
			t.Fatal(err)
		}
		profiles, err := os.ReadFile(s.localConnections.Store.Path)
		if err != nil {
			t.Fatal(err)
		}
		return string(file) + "\x00" + string(profiles)
	}
}

func TestSavedConnectionRoutesRequireLocalInstallation(t *testing.T) {
	for _, tt := range []struct {
		name  string
		setup func(t *testing.T, s *Server)
	}{
		{"operator auth", func(t *testing.T, s *Server) { s.authConfig = auth.Config{Mode: "proxy"} }},
		{"in-cluster pod", func(t *testing.T, s *Server) { t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1") }},
		{"cloud", func(t *testing.T, s *Server) { t.Setenv("RADAR_CLOUD_MODE", "true") }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("RADAR_CLOUD_MODE", "false")
			t.Setenv("KUBERNETES_SERVICE_HOST", "")
			s := setupLocalProfileTest(t)
			files := seedLocalConfiguration(t, s)
			before := files()
			tt.setup(t, s)
			read := httptest.NewRecorder()
			s.handleLocalConnections(read, httptest.NewRequest(http.MethodGet, "/api/integrations/connections", nil))
			target, _ := k8s.CurrentProfileTarget()
			write := updateProfile(t, s, s.localConnections.Resolve(target, config.IntegrationMetrics, false).View, "apply", "http://replacement.example", nil)
			if read.Code != http.StatusForbidden || write.Code != http.StatusForbidden {
				t.Fatalf("read %d %s, write %d %s", read.Code, read.Body.String(), write.Code, write.Body.String())
			}
			if strings.Contains(read.Body.String(), "saved.example") {
				t.Fatalf("denied read exposed saved connections: %s", read.Body.String())
			}
			if files() != before {
				t.Fatal("denied request changed saved configuration")
			}
		})
	}
}

func TestLocalLegacyIntegrationWritesDeferToSavedConnections(t *testing.T) {
	t.Setenv("RADAR_CLOUD_MODE", "false")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	s := setupLocalProfileTest(t)
	files := seedLocalConfiguration(t, s)
	before := files()
	for name, tt := range map[string]struct {
		handler http.HandlerFunc
		body    string
	}{
		"prometheus": {s.handleApplyPrometheusURL, `{"prometheusUrl":"http://127.0.0.1:1"}`},
		"argocd":     {s.handleApplyArgoCDConfig, `{"argoCdUrl":"http://127.0.0.1:1","argoCdToken":"legacy-token"}`},
		"cost":       {s.handleApplyCostSource, `{"source":"kubecost","url":"http://127.0.0.1:1","apiKey":"legacy-key"}`},
	} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			tt.handler(w, httptest.NewRequest(http.MethodPut, "/api/integrations/"+name, strings.NewReader(tt.body)))
			if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "saved connections endpoint") {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if files() != before {
				t.Fatal("legacy route changed saved configuration")
			}
		})
	}
}

func TestOperatorHelmSourceRoutes(t *testing.T) {
	t.Setenv("RADAR_CLOUD_MODE", "false")
	s := &Server{authConfig: auth.Config{Mode: "proxy"}}
	h := helm.NewHandlers(nil)
	h.ConfigWriteAllowed = s.requireConfigEditable
	router := chi.NewRouter()
	h.RegisterRoutes(router)
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(method, "/helm/oci-sources", strings.NewReader(`{"source":"oci://example.com/charts"}`)))
		if w.Code != 403 || !strings.Contains(w.Body.String(), "operator_managed") {
			t.Fatalf("%s: %d %s", method, w.Code, w.Body.String())
		}
	}
}

func TestOperatorConfigReadPrivacy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("RADAR_CLOUD_MODE", "false")
	prometheuspkg.Initialize(nil, nil, "test")
	t.Cleanup(func() { prometheuspkg.Initialize(nil, nil, "") })
	prometheuspkg.Configure("https://private-machine-user:private-machine-password@metrics.example/prom?token=private-machine-token", nil)
	cfg := config.Config{Kubeconfig: "/private-machine-path", KubeconfigDirs: []string{"/private-machine-directory"}, TimelineDBPath: "/private-machine-db", AIHistoryDBPath: "/private-machine-ai", AIConsent: map[string]string{"private-machine-consent": "v1"}, PrometheusHeadersFromEnv: map[string]string{"Authorization": "PRIVATE_MACHINE_ENV"}, ArgoCDToken: "private-machine-token", KubecostAPIKey: "private-machine-key", Port: 9280}
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := settings.Save(settings.Settings{Theme: "dark", PinnedKinds: []settings.PinnedKind{{Name: "private-machine-pin"}}, ActiveNamespaces: map[string][]string{"private-machine-context": {"private-machine-ns"}}}); err != nil {
		t.Fatal(err)
	}
	s := &Server{authConfig: auth.Config{Mode: "proxy"}, effectiveConfig: &cfg}
	w := httptest.NewRecorder()
	s.handleGetConfig(w, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if strings.Contains(strings.ToLower(w.Body.String()), "private-machine") || strings.Contains(w.Body.String(), "PRIVATE_MACHINE_ENV") {
		t.Fatalf("private config leaked: %s", w.Body.String())
	}
	var response configResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Management != "operator" || response.Effective.Port != 9280 {
		t.Fatalf("missing effective public values: %+v", response)
	}
	if response.Effective.PrometheusURL != "https://metrics.example/prom" {
		t.Fatalf("unexpected display URL: %s", response.Effective.PrometheusURL)
	}
	w = httptest.NewRecorder()
	s.handleGetSettings(w, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	if strings.Contains(w.Body.String(), "private-machine") || strings.Contains(w.Body.String(), "dark") || !strings.Contains(w.Body.String(), `"preferenceStorage":"browser"`) {
		t.Fatalf("shared preferences: %s", w.Body.String())
	}
}
