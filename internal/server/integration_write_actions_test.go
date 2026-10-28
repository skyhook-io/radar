package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"sigs.k8s.io/yaml"

	cnpgsvc "github.com/skyhook-io/radar/internal/cnpg"
)

// Every /api route family that serves a write method is classified here, so a
// new family can't skip the table. Integration families act on integration
// custom resources; workload routes also accept Argo Rollouts.
var (
	integrationRouteFamilies = []string{"argo", "flux", "rollouts", "velero", "gitops", "cnpg", "policy"}
	// Built-in kinds (cronjobs, nodes, pods), generic edits of any object as
	// the caller (resources), Helm, Radar's own settings, sessions and
	// connections, and read-only queries sent as POST.
	// capi's connect reads a kubeconfig Secret and switches Radar's context; it
	// writes no Cluster API object.
	nonIntegrationRouteFamilies = []string{
		"agent", "capi", "cloud", "cluster", "config", "connection", "contexts", "cronjobs", "curl",
		"desktop", "diagnose", "github", "helm", "integrations", "nodes", "opencost", "pods",
		"portforwards", "prometheus", "resources", "settings", "trace", "traffic", "usage-data",
		"version-check", "whats-new",
	}
	// Read-only queries sent as POST inside an integration family.
	readOnlyIntegrationRoutes = []string{
		"POST /api/gitops/write-evidence",
		"POST /api/cnpg/clusters/{namespace}/{name}/protection/preview",
	}
)

// integrationWriteActions returns the table actions a write route needs.
func integrationWriteActions(method, route string) ([]string, error) {
	if !strings.HasPrefix(route, "/api/") {
		// The SPA, static assets and MCP mounts; MCP tools have their own test.
		return nil, nil
	}
	family, _, _ := strings.Cut(strings.TrimPrefix(route, "/api/"), "/")
	action := method + " " + route
	switch {
	case family == "workloads":
		return []string{action + " kind=rollouts"}, nil
	case slices.Contains(nonIntegrationRouteFamilies, family):
		return nil, nil
	case !slices.Contains(integrationRouteFamilies, family):
		return nil, fmt.Errorf("%s: classify /api/%s as an integration or non-integration route family", action, family)
	case slices.Contains(readOnlyIntegrationRoutes, action):
		return nil, nil
	case strings.Contains(route, "{action}"):
		var names []string
		switch {
		case family == "rollouts":
			for name := range rolloutOperations {
				names = append(names, name)
			}
		case route == "/api/cnpg/clusters/{namespace}/{name}/actions/{action}":
			names = cnpgsvc.ClusterActions()
		case route == "/api/cnpg/scheduledbackups/{namespace}/{name}/actions/{action}":
			names = []string{"suspend", "resume", "run", "setSchedule", "repairMethod"}
		case route == "/api/cnpg/poolers/{namespace}/{name}/actions/{action}":
			names = []string{"pause", "resume"}
		default:
			return nil, fmt.Errorf("%s dispatches on {action}; enumerate its actions here", action)
		}
		var actions []string
		for _, name := range names {
			actions = append(actions, action+" action="+name)
		}
		return actions, nil
	default:
		return []string{action}, nil
	}
}

func TestIntegrationWriteActionClassification(t *testing.T) {
	for route, want := range map[string]bool{
		"/api/crossplane/managedresources/{namespace}/{name}/pause": false,
		"/api/argo/applications/{namespace}/{name}/sync":            true,
		"/api/workloads/{kind}/{namespace}/{name}/restart":          true,
		"/api/helm/releases/{namespace}/{name}/rollback":            true,
		"/assets/*": true,
	} {
		if _, err := integrationWriteActions(http.MethodPost, route); (err == nil) != want {
			t.Errorf("%s: classified = %v, want %v (err %v)", route, err == nil, want, err)
		}
	}
}

// TestIntegrationWriteActionsHaveReviewedRows makes a new integration action
// route fail until files/integration-write-baseline.yaml lists it in some
// row's actions. It checks the action inventory only: whether a row names
// every resource and verb the action needs stays a review and live-test job.
func TestIntegrationWriteActionsHaveReviewedRows(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "helm", "radar", "files", "integration-write-baseline.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var policy struct {
		Entries []struct {
			Actions []string `json:"actions"`
		} `json:"entries"`
	}
	if err := yaml.Unmarshal(raw, &policy); err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, entry := range policy.Entries {
		for _, action := range entry.Actions {
			if !strings.HasPrefix(action, "MCP ") {
				listed[action] = true
			}
		}
	}

	var required []string
	seenReadOnly := map[string]bool{}
	srv := New(Config{DevMode: true})
	err = chi.Walk(srv.router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if slices.Contains([]string{http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace, http.MethodConnect}, method) {
			return nil
		}
		if action := method + " " + route; slices.Contains(readOnlyIntegrationRoutes, action) {
			seenReadOnly[action] = true
		}
		actions, err := integrationWriteActions(method, route)
		if err != nil {
			t.Error(err)
		}
		required = append(required, actions...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(required) < 20 {
		t.Fatalf("found only %d integration action routes; the router walk is not seeing the API", len(required))
	}
	for _, action := range required {
		if !listed[action] {
			t.Errorf("integration action %q has no row in files/integration-write-baseline.yaml", action)
		}
		delete(listed, action)
	}
	for action := range listed {
		t.Errorf("files/integration-write-baseline.yaml lists %q, which is not a registered integration action route", action)
	}
	for _, action := range readOnlyIntegrationRoutes {
		if !seenReadOnly[action] {
			t.Errorf("readOnlyIntegrationRoutes lists %q, which is not a registered write-method route", action)
		}
	}
}
