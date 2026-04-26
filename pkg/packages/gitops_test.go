package packages

import (
	"reflect"
	"testing"
)

func TestParseArgoApplication_SingleSourceHelm(t *testing.T) {
	obj := map[string]any{
		"metadata": map[string]any{
			"namespace": "argocd",
			"name":      "cert-manager",
		},
		"spec": map[string]any{
			"destination": map[string]any{
				"namespace": "cert-manager",
				"name":      "in-cluster",
			},
			"source": map[string]any{
				"chart":          "cert-manager",
				"targetRevision": "1.14.0",
			},
		},
		"status": map[string]any{
			"health": map[string]any{"status": "Healthy"},
		},
	}
	d, ok := ParseArgoApplication(obj)
	if !ok {
		t.Fatal("expected ok")
	}
	want := Declaration{
		Source:          "argocd",
		Namespace:       "argocd",
		Name:            "cert-manager",
		TargetNamespace: "cert-manager",
		TargetName:      "cert-manager",
		Chart:           "cert-manager",
		ChartVersion:    "1.14.0",
		Status:          "healthy",
	}
	if !reflect.DeepEqual(d, want) {
		t.Errorf("got %+v\nwant %+v", d, want)
	}
}

// First Helm source wins; we don't model multiple charts per app.
func TestParseArgoApplication_MultiSourceFirstHelmWins(t *testing.T) {
	obj := map[string]any{
		"metadata": map[string]any{"name": "stack", "namespace": "argocd"},
		"spec": map[string]any{
			"destination": map[string]any{"namespace": "monitoring"},
			"sources": []any{
				map[string]any{"repoURL": "git@example.com/values.git", "path": "."},
				map[string]any{"chart": "prometheus", "targetRevision": "25.0.0"},
				map[string]any{"chart": "grafana", "targetRevision": "7.0.0"},
			},
		},
	}
	d, ok := ParseArgoApplication(obj)
	if !ok {
		t.Fatal("expected ok")
	}
	if d.Chart != "prometheus" || d.ChartVersion != "25.0.0" {
		t.Errorf("first Helm source should win, got chart=%q version=%q", d.Chart, d.ChartVersion)
	}
}

func TestParseArgoApplication_MissingNameRejected(t *testing.T) {
	if _, ok := ParseArgoApplication(map[string]any{"metadata": map[string]any{}}); ok {
		t.Error("expected !ok for missing metadata.name")
	}
	if _, ok := ParseArgoApplication(map[string]any{}); ok {
		t.Error("expected !ok for missing metadata")
	}
}

func TestMapArgoHealth(t *testing.T) {
	cases := map[string]Health{
		"Healthy":     HealthHealthy,
		"healthy":     HealthHealthy,
		"Progressing": HealthDegraded,
		"Degraded":    HealthUnhealthy,
		"Suspended":   HealthDegraded,
		// Missing means "should exist but doesn't" — surface as a
		// real signal (degraded), not as the same bucket as Unknown.
		"Missing": HealthDegraded,
		"Unknown": HealthUnknown,
		"":       HealthUnknown,
		"Bogus":  HealthUnknown,
	}
	for in, want := range cases {
		if got := mapArgoHealth(in); got != want {
			t.Errorf("mapArgoHealth(%q) = %q, want %q", in, got, want)
		}
	}
}

// v2 layout: spec.chart.spec.{chart,version}.
func TestParseFluxHelmRelease_V2Layout(t *testing.T) {
	obj := map[string]any{
		"metadata": map[string]any{"name": "redis", "namespace": "data"},
		"spec": map[string]any{
			"chart": map[string]any{
				"spec": map[string]any{"chart": "redis", "version": "18.0.0"},
			},
			"targetNamespace": "redis",
			"releaseName":     "redis-prod",
		},
		"status": map[string]any{
			"conditions": []any{
				map[string]any{"type": "Ready", "status": "True"},
			},
		},
	}
	d, ok := ParseFluxHelmRelease(obj)
	if !ok {
		t.Fatal("expected ok")
	}
	if d.Chart != "redis" || d.ChartVersion != "18.0.0" {
		t.Errorf("v2 chart shape, got chart=%q version=%q", d.Chart, d.ChartVersion)
	}
	if d.TargetNamespace != "redis" || d.TargetName != "redis-prod" {
		t.Errorf("target wrong: ns=%q name=%q", d.TargetNamespace, d.TargetName)
	}
	if d.Status != "healthy" {
		t.Errorf("Ready=True should map healthy, got %q", d.Status)
	}
}

// v2beta2 layout: spec.chart.{chart,version}.
func TestParseFluxHelmRelease_V2Beta2Layout(t *testing.T) {
	obj := map[string]any{
		"metadata": map[string]any{"name": "nginx", "namespace": "kube-system"},
		"spec": map[string]any{
			"chart": map[string]any{"chart": "nginx", "version": "1.5.0"},
		},
	}
	d, ok := ParseFluxHelmRelease(obj)
	if !ok {
		t.Fatal("expected ok")
	}
	if d.Chart != "nginx" || d.ChartVersion != "1.5.0" {
		t.Errorf("v2beta2 chart shape, got chart=%q version=%q", d.Chart, d.ChartVersion)
	}
	// targetNamespace falls back to the HR's own namespace when not set.
	if d.TargetNamespace != "kube-system" {
		t.Errorf("targetNamespace fallback, got %q", d.TargetNamespace)
	}
	// releaseName falls back to the HR's name.
	if d.TargetName != "nginx" {
		t.Errorf("releaseName fallback, got %q", d.TargetName)
	}
}

func TestParseFluxKustomization(t *testing.T) {
	obj := map[string]any{
		"metadata": map[string]any{"name": "platform", "namespace": "flux-system"},
		"spec": map[string]any{
			"targetNamespace": "platform",
		},
		"status": map[string]any{
			"conditions": []any{
				map[string]any{"type": "Ready", "status": "False", "reason": "ReconciliationFailed"},
			},
		},
	}
	d, ok := ParseFluxKustomization(obj)
	if !ok {
		t.Fatal("expected ok")
	}
	if d.Chart != "" {
		t.Errorf("Kustomization should leave Chart empty, got %q", d.Chart)
	}
	if d.TargetNamespace != "platform" || d.TargetName != "platform" {
		t.Errorf("target wrong: ns=%q name=%q", d.TargetNamespace, d.TargetName)
	}
	if d.Status != "unhealthy" {
		t.Errorf("Ready=False with hard reason should map unhealthy, got %q", d.Status)
	}
}

// Stalled=True overrides Ready=True — Flux uses Stalled to signal a
// terminal failure that needs operator intervention even if the previous
// reconciliation succeeded.
func TestFluxConditionStatus_StalledOverridesReady(t *testing.T) {
	obj := map[string]any{
		"status": map[string]any{
			"conditions": []any{
				map[string]any{"type": "Ready", "status": "True"},
				map[string]any{"type": "Stalled", "status": "True", "reason": "InstallFailed"},
			},
		},
	}
	if got := fluxConditionStatus(obj); got != "unhealthy" {
		t.Errorf("Stalled=True should override Ready=True, got %q", got)
	}
}

func TestFluxConditionStatus_TransientReasonDegraded(t *testing.T) {
	obj := map[string]any{
		"status": map[string]any{
			"conditions": []any{
				map[string]any{"type": "Ready", "status": "False", "reason": "Progressing"},
			},
		},
	}
	if got := fluxConditionStatus(obj); got != "degraded" {
		t.Errorf("Ready=False with transient reason should map degraded, got %q", got)
	}
}

func TestFluxConditionStatus_NoConditions(t *testing.T) {
	if got := fluxConditionStatus(map[string]any{}); got != "unknown" {
		t.Errorf("missing status → unknown, got %q", got)
	}
	if got := fluxConditionStatus(map[string]any{"status": map[string]any{}}); got != "unknown" {
		t.Errorf("empty status → unknown, got %q", got)
	}
}
