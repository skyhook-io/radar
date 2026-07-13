package k8s

import (
	"testing"

	"github.com/skyhook-io/radar/pkg/k8score"
)

func TestBuildVisibilitySummaryOK(t *testing.T) {
	s := BuildVisibilitySummary(&PermissionCheckResult{Scopes: map[string]k8score.ResourceScope{
		k8score.Pods:        {Enabled: true},
		k8score.Deployments: {Enabled: true},
		k8score.Services:    {Enabled: true},
		k8score.Events:      {Enabled: true},
		k8score.ConfigMaps:  {Enabled: true},
		k8score.Secrets:     {Enabled: true},
		k8score.Nodes:       {Enabled: true},
	}}, "")
	if s != nil {
		t.Fatalf("expected nil summary for full visibility, got %+v", s)
	}
}

func TestBuildVisibilitySummaryDegraded(t *testing.T) {
	s := BuildVisibilitySummary(&PermissionCheckResult{Scopes: map[string]k8score.ResourceScope{
		k8score.Services: {Enabled: true},
	}}, "prod")
	if s == nil {
		t.Fatal("expected visibility summary")
	}
	if s.State != "degraded" {
		t.Fatalf("state = %q, want degraded", s.State)
	}
	if s.Scope.Namespace != "prod" {
		t.Fatalf("namespace = %q, want prod", s.Scope.Namespace)
	}
	if s.Core["pods"] != "unavailable" || s.Core["deployments"] != "unavailable" || s.Core["services"] != "allowed" {
		t.Fatalf("unexpected core map: %+v", s.Core)
	}
}

func TestBuildVisibilitySummaryLimited(t *testing.T) {
	s := BuildVisibilitySummary(&PermissionCheckResult{Scopes: map[string]k8score.ResourceScope{
		k8score.Pods:        {Enabled: true},
		k8score.Deployments: {Enabled: true},
		k8score.Services:    {Enabled: true},
	}}, "prod")
	if s == nil {
		t.Fatal("expected visibility summary")
	}
	if s.State != "limited" {
		t.Fatalf("state = %q, want limited", s.State)
	}
	if len(s.MissingOptionalKinds) == 0 {
		t.Fatal("expected missing optional kinds")
	}
}

func TestBuildVisibilitySummaryNamespaceLimited(t *testing.T) {
	s := BuildVisibilitySummary(&PermissionCheckResult{Scopes: map[string]k8score.ResourceScope{
		k8score.Pods:        {Enabled: true, Namespace: "prod"},
		k8score.Deployments: {Enabled: true, Namespace: "prod"},
		k8score.Services:    {Enabled: true, Namespace: "prod"},
	}}, "")
	if s == nil {
		t.Fatal("expected visibility summary for cluster-wide request backed by namespace-scoped cache")
	}
	if s.State != "limited" {
		t.Fatalf("state = %q, want limited", s.State)
	}
	if s.Core["pods"] != "namespace_limited" {
		t.Fatalf("pods status = %q, want namespace_limited", s.Core["pods"])
	}

	s = BuildVisibilitySummary(&PermissionCheckResult{Scopes: map[string]k8score.ResourceScope{
		k8score.Pods:        {Enabled: true, Namespace: "prod"},
		k8score.Deployments: {Enabled: true, Namespace: "prod"},
		k8score.Services:    {Enabled: true, Namespace: "prod"},
	}}, "staging")
	if s == nil || s.State != "degraded" {
		t.Fatalf("expected degraded for different namespace, got %+v", s)
	}
}

// Multi-namespace kinds: ResourceScope.Namespace is only the first grant.
// A namespace present in ScopeNamespaces is served by the union lister and
// must report allowed, not unavailable.
func TestBuildVisibilitySummaryMultiNamespace(t *testing.T) {
	result := &PermissionCheckResult{
		Scopes: map[string]k8score.ResourceScope{
			k8score.Pods:        {Enabled: true, Namespace: "team-a"},
			k8score.Deployments: {Enabled: true, Namespace: "team-a"},
			k8score.Services:    {Enabled: true, Namespace: "team-a"},
			k8score.Events:      {Enabled: true, Namespace: "team-a"},
			k8score.ConfigMaps:  {Enabled: true, Namespace: "team-a"},
		},
		ScopeNamespaces: map[string][]string{
			k8score.Pods:        {"team-a", "team-b"},
			k8score.Deployments: {"team-a", "team-b"},
			k8score.Services:    {"team-a", "team-b"},
			k8score.Events:      {"team-a", "team-b"},
			k8score.ConfigMaps:  {"team-a", "team-b"},
		},
	}

	if s := BuildVisibilitySummary(result, "team-b"); s != nil {
		t.Fatalf("secondary granted namespace must be fully visible, got %+v", s)
	}
	if s := BuildVisibilitySummary(result, "team-a"); s != nil {
		t.Fatalf("primary namespace must be fully visible, got %+v", s)
	}
	s := BuildVisibilitySummary(result, "team-c")
	if s == nil || s.State != "degraded" {
		t.Fatalf("ungranted namespace must stay degraded, got %+v", s)
	}
	if s.Core["pods"] != "unavailable" {
		t.Fatalf("pods in ungranted namespace = %q, want unavailable", s.Core["pods"])
	}
}
