package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/openobserve/oofake"
)

const ooSetupUID = "7b930c6c-dcbb-4d1f-9afd-67ea890873d7"

func setupOpenObserveTest(t *testing.T) *Server {
	t.Helper()
	s := setupLocalProfileTest(t)
	s.listenAddress = "127.0.0.1"
	client := fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: types.UID(ooSetupUID)}})
	previous := openObserveKubeClient
	openObserveKubeClient = func() kubernetes.Interface { return client }
	t.Cleanup(func() { openObserveKubeClient = previous })
	return s
}

func saveOOForSetup(t *testing.T, entry config.OpenObserveConfig) {
	t.Helper()
	if _, err := config.Update(func(c *config.Config) { c.OpenObserve = &entry }); err != nil {
		t.Fatal(err)
	}
}

func ooRequest(s *Server, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	switch {
	case method == http.MethodGet:
		s.handleOpenObserveSetupView(w, r)
	case method == http.MethodPost:
		s.handleOpenObserveSetup(w, r)
	case strings.HasSuffix(path, "/cluster"):
		s.handleOpenObserveRemoveCluster(w, r)
	default:
		s.handleOpenObserveRemoveAll(w, r)
	}
	return w
}

func decodeOOView(t *testing.T, w *httptest.ResponseRecorder) openObserveSetupView {
	t.Helper()
	var v openObserveSetupView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return v
}

func TestOpenObserveSetupRoutesNeedLocalConfigManagement(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*Server)
	}{
		{name: "authentication on", setup: func(s *Server) { s.authConfig = auth.Config{Mode: "proxy"} }},
		{name: "in cluster", setup: func(s *Server) {
			previous := k8s.ForceInCluster
			k8s.ForceInCluster = true
			t.Cleanup(func() { k8s.ForceInCluster = previous })
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := setupOpenObserveTest(t)
			tc.setup(s)
			for _, call := range [][2]string{
				{http.MethodGet, "/api/integrations/openobserve"},
				{http.MethodPost, "/api/integrations/openobserve/setup"},
				{http.MethodDelete, "/api/integrations/openobserve/cluster"},
				{http.MethodDelete, "/api/integrations/openobserve"},
			} {
				if w := ooRequest(s, call[0], call[1], `{}`); w.Code != http.StatusForbidden {
					t.Errorf("%s %s = %d %s, want 403", call[0], call[1], w.Code, w.Body.String())
				}
			}
		})
	}
}

// Shared listener without authentication: status is readable (it carries no
// token), but nothing can be saved or removed.
func TestOpenObserveSetupOnASharedListenerIsReadOnly(t *testing.T) {
	s := setupOpenObserveTest(t)
	s.listenAddress = "0.0.0.0"
	saveOOForSetup(t, config.OpenObserveConfig{BaseURL: "http://oo.example", Org: "default", User: "sa@example.com", Token: "oo-secret",
		Matches: map[string]config.OpenObserveMatch{ooSetupUID: {Label: "radar_cluster_uid", Value: ooSetupUID, By: "id", Context: "dev"}}})

	if w := ooRequest(s, http.MethodGet, "/api/integrations/openobserve", ""); w.Code != http.StatusOK || strings.Contains(w.Body.String(), "oo-secret") {
		t.Fatalf("GET = %d %s", w.Code, w.Body.String())
	}
	for _, call := range [][3]string{
		{http.MethodPost, "/api/integrations/openobserve/setup", `{"baseUrl":"http://attacker.example","user":"x","token":"y"}`},
		{http.MethodDelete, "/api/integrations/openobserve/cluster", ""},
		{http.MethodDelete, "/api/integrations/openobserve", ""},
	} {
		if w := ooRequest(s, call[0], call[1], call[2]); w.Code != http.StatusForbidden {
			t.Errorf("%s %s = %d %s, want 403", call[0], call[1], w.Code, w.Body.String())
		}
	}
	if got := config.Load().OpenObserve; got == nil || got.BaseURL != "http://oo.example" || len(got.Matches) != 1 {
		t.Fatalf("saved connection changed: %+v", got)
	}
}

func TestOpenObserveSetupViewHidesTheToken(t *testing.T) {
	s := setupOpenObserveTest(t)
	v := decodeOOView(t, ooRequest(s, http.MethodGet, "/api/integrations/openobserve", ""))
	if v.Connection != nil || v.Cluster.UID != ooSetupUID || v.Cluster.Match != nil || v.Cluster.Error != "" {
		t.Fatalf("view without a connection = %+v", v)
	}

	saveOOForSetup(t, config.OpenObserveConfig{BaseURL: "http://oo.example", Org: "default", User: "sa@example.com", Token: "oo-secret", CACertFile: "/tmp/ca.pem",
		Matches: map[string]config.OpenObserveMatch{
			ooSetupUID: {Label: "cluster", Value: "dev", By: "name", Context: "dev"},
			"other":    {Label: "radar_cluster_uid", Value: "other", By: "id", Context: "prod"},
		}})
	w := ooRequest(s, http.MethodGet, "/api/integrations/openobserve", "")
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "oo-secret") || strings.Contains(w.Body.String(), `"token"`) {
		t.Fatalf("GET = %d %s", w.Code, w.Body.String())
	}
	v = decodeOOView(t, w)
	c := v.Connection
	if c == nil || c.BaseURL != "http://oo.example" || c.TokenSource != "saved" || c.TokenEnv != "" || c.CACertFile != "/tmp/ca.pem" ||
		len(c.Clusters) != 2 || c.Clusters[0] != (openObserveMatchedTarget{Context: "dev", By: "name"}) || c.Clusters[1] != (openObserveMatchedTarget{Context: "prod", By: "id"}) {
		t.Fatalf("connection = %+v", c)
	}
	if m := v.Cluster.Match; m == nil || m.Label != "cluster" || m.Value != "dev" || m.By != "name" {
		t.Fatalf("cluster = %+v", v.Cluster)
	}

	saveOOForSetup(t, config.OpenObserveConfig{BaseURL: "http://oo.example", Org: "default", User: "sa@example.com", Token: "oo-secret", TokenEnv: "OO_TOKEN"})
	if v := decodeOOView(t, ooRequest(s, http.MethodGet, "/api/integrations/openobserve", "")); v.Connection.TokenSource != "env" || v.Connection.TokenEnv != "OO_TOKEN" || v.Connection.Clusters == nil {
		t.Fatalf("env token view = %+v", v.Connection)
	}
}

func TestOpenObserveRemoveClusterKeepsTheOthers(t *testing.T) {
	s := setupOpenObserveTest(t)
	entry := config.OpenObserveConfig{BaseURL: "http://oo.example", Org: "default", User: "sa@example.com", Token: "oo-secret",
		Matches: map[string]config.OpenObserveMatch{
			ooSetupUID: {Label: "radar_cluster_uid", Value: ooSetupUID, By: "id", Context: "dev"},
			"other":    {Label: "radar_cluster_uid", Value: "other", By: "id", Context: "prod"},
		}}
	saveOOForSetup(t, entry)

	w := ooRequest(s, http.MethodDelete, "/api/integrations/openobserve/cluster", "")
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "oo-secret") {
		t.Fatalf("DELETE cluster = %d %s", w.Code, w.Body.String())
	}
	v := decodeOOView(t, w)
	if v.Cluster.Match != nil || v.Connection == nil || len(v.Connection.Clusters) != 1 || v.Connection.Clusters[0].Context != "prod" {
		t.Fatalf("after DELETE cluster = %+v", v)
	}
	if got := config.Load().OpenObserve; got == nil || got.Token != "oo-secret" || len(got.Matches) != 1 {
		t.Fatalf("stored after DELETE cluster = %+v", got)
	}

	w = ooRequest(s, http.MethodDelete, "/api/integrations/openobserve", "")
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE all = %d %s", w.Code, w.Body.String())
	}
	if v := decodeOOView(t, w); v.Connection != nil || v.Cluster.UID != ooSetupUID {
		t.Fatalf("after DELETE all = %+v", v)
	}
	if !strings.Contains(w.Body.String(), `"connection":null`) {
		t.Fatalf("connection must be null: %s", w.Body.String())
	}
	if config.Load().OpenObserve != nil {
		t.Fatal("DELETE all left the connection saved")
	}
}

func TestOpenObserveSetupSavesAndNeverReturnsTheToken(t *testing.T) {
	s := setupOpenObserveTest(t)
	srv := oofake.Start(t, &oofake.Server{IDs: map[string]string{ooSetupUID: "dev"}})

	w := ooRequest(s, http.MethodPost, "/api/integrations/openobserve/setup", `{"baseUrl":"`+srv.URL+`","org":"default","user":"sa@example.com","token":"oo-secret"}`)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "oo-secret") {
		t.Fatalf("POST = %d %s", w.Code, w.Body.String())
	}
	var res map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	notes, _ := res["notes"].(map[string]any)
	if res["outcome"] != "saved" || res["clusterUid"] != ooSetupUID || notes == nil || notes["missing"] == nil {
		t.Fatalf("result = %s", w.Body.String())
	}
	match, _ := res["match"].(map[string]any)
	if match["by"] != "id" || match["label"] != "radar_cluster_uid" || match["value"] != ooSetupUID {
		t.Fatalf("match = %v", match)
	}
	if got := config.Load().OpenObserve; got == nil || got.Token != "oo-secret" || got.Matches[ooSetupUID].Context != "dev" {
		t.Fatalf("saved = %+v", got)
	}

	failing := oofake.Start(t, &oofake.Server{FailStatus: http.StatusUnauthorized, FailBody: "no such user oo-secret-2"})
	w = ooRequest(s, http.MethodPost, "/api/integrations/openobserve/setup", `{"baseUrl":"`+failing.URL+`","user":"sa@example.com","token":"oo-secret-2"}`)
	if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), "oo-secret-2") || !strings.Contains(w.Body.String(), "OpenObserve rejected the login") {
		t.Fatalf("failed login = %d %s", w.Code, w.Body.String())
	}
	if got := config.Load().OpenObserve; got == nil || got.BaseURL != srv.URL {
		t.Fatalf("a failed login changed the saved connection: %+v", got)
	}

	big := `{"token":"` + strings.Repeat("x", openObserveSetupBodyLimit) + `"}`
	if w := ooRequest(s, http.MethodPost, "/api/integrations/openobserve/setup", big); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body = %d %s", w.Code, w.Body.String())
	}
	if w := ooRequest(s, http.MethodPost, "/api/integrations/openobserve/setup", `{"unknown":1}`); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown field = %d %s", w.Code, w.Body.String())
	}
}

func TestOpenObserveWritesRefuseAClusterThePageDidNotShow(t *testing.T) {
	s := setupOpenObserveTest(t)
	entry := config.OpenObserveConfig{BaseURL: "http://oo.example", Org: "default", User: "sa@example.com", Token: "oo-secret",
		Matches: map[string]config.OpenObserveMatch{ooSetupUID: {Label: "radar_cluster_uid", Value: ooSetupUID, By: "id", Context: "dev"}}}
	saveOOForSetup(t, entry)

	w := httptest.NewRecorder()
	s.handleOpenObserveRemoveCluster(w, httptest.NewRequest(http.MethodDelete, "/api/integrations/openobserve/cluster?clusterUid=another-cluster", nil))
	if w.Code != http.StatusConflict {
		t.Fatalf("DELETE cluster for another cluster = %d %s", w.Code, w.Body.String())
	}
	if got := config.Load().OpenObserve; got == nil || len(got.Matches) != 1 {
		t.Fatalf("a refused DELETE changed the saved matches: %+v", got)
	}

	w = ooRequest(s, http.MethodPost, "/api/integrations/openobserve/setup", `{"useSaved":true,"clusterUid":"another-cluster"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("setup for another cluster = %d %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	s.handleOpenObserveRemoveCluster(w, httptest.NewRequest(http.MethodDelete, "/api/integrations/openobserve/cluster?clusterUid="+ooSetupUID, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE cluster for the shown cluster = %d %s", w.Code, w.Body.String())
	}
	if got := config.Load().OpenObserve; got == nil || len(got.Matches) != 0 {
		t.Fatalf("DELETE cluster for the shown cluster kept the match: %+v", got)
	}
}
