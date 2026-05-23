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
