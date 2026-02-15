package context

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestSummary_GenericCRD_ConditionPriority(t *testing.T) {
	tests := []struct {
		name       string
		conditions []map[string]any
		phase      string // fallback status.phase
		wantStatus string
	}{
		{
			name:       "Ready=True",
			conditions: []map[string]any{{"type": "Ready", "status": "True"}},
			wantStatus: "Ready",
		},
		{
			name:       "Ready=False with reason",
			conditions: []map[string]any{{"type": "Ready", "status": "False", "reason": "ConfigError"}},
			wantStatus: "ConfigError",
		},
		{
			name:       "Ready=False no reason",
			conditions: []map[string]any{{"type": "Ready", "status": "False"}},
			wantStatus: "NotReady",
		},
		{
			name: "Available over unknown condition",
			conditions: []map[string]any{
				{"type": "Healthy", "status": "True"},
				{"type": "Available", "status": "True"},
			},
			wantStatus: "Available",
		},
		{
			name: "Ready wins over Available",
			conditions: []map[string]any{
				{"type": "Available", "status": "True"},
				{"type": "Ready", "status": "True"},
			},
			wantStatus: "Ready",
		},
		{
			name: "Synced used when no Ready or Available",
			conditions: []map[string]any{
				{"type": "Synced", "status": "True"},
			},
			wantStatus: "Synced",
		},
		{
			name:       "no conditions, has phase",
			conditions: nil,
			phase:      "Active",
			wantStatus: "Active",
		},
		{
			name:       "empty — no conditions, no phase",
			conditions: nil,
			wantStatus: "",
		},
		{
			name: "falls back to first condition when none are priority",
			conditions: []map[string]any{
				{"type": "Initialized", "status": "True"},
				{"type": "Progressing", "status": "True"},
			},
			wantStatus: "Initialized",
		},
		{
			name: "first condition False",
			conditions: []map[string]any{
				{"type": "Initialized", "status": "False"},
			},
			wantStatus: "NotInitialized",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "example.io/v1",
					"kind":       "Widget",
					"metadata":   map[string]any{"name": "test", "namespace": "default"},
				},
			}

			// Build status
			status := map[string]any{}
			if len(tt.conditions) > 0 {
				conds := make([]any, len(tt.conditions))
				for i, c := range tt.conditions {
					conds[i] = c
				}
				status["conditions"] = conds
			}
			if tt.phase != "" {
				status["phase"] = tt.phase
			}
			if len(status) > 0 {
				obj.Object["status"] = status
			}

			raw := MinifyUnstructured(obj, LevelSummary)
			s, ok := raw.(*ResourceSummary)
			if !ok {
				t.Fatalf("Expected *ResourceSummary, got %T", raw)
			}
			if s.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", s.Status, tt.wantStatus)
			}
		})
	}
}

func TestSummary_ArgoApplication(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "Application",
			"metadata":   map[string]any{"name": "my-app", "namespace": "argocd"},
			"spec": map[string]any{
				"source": map[string]any{"repoURL": "https://github.com/org/repo"},
			},
			"status": map[string]any{
				"sync":   map[string]any{"status": "OutOfSync"},
				"health": map[string]any{"status": "Degraded"},
			},
		},
	}

	raw := MinifyUnstructured(obj, LevelSummary)
	s := raw.(*ResourceSummary)

	if s.Status != "OutOfSync" {
		t.Errorf("Status = %q, want OutOfSync", s.Status)
	}
	if s.Issue != "Degraded" {
		t.Errorf("Issue = %q, want Degraded", s.Issue)
	}
	if s.Image != "https://github.com/org/repo" {
		t.Errorf("Image (repo) = %q, want repo URL", s.Image)
	}

	// Healthy app should have no issue
	objHealthy := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "Application",
			"metadata":   map[string]any{"name": "healthy-app", "namespace": "argocd"},
			"status": map[string]any{
				"sync":   map[string]any{"status": "Synced"},
				"health": map[string]any{"status": "Healthy"},
			},
		},
	}
	rawH := MinifyUnstructured(objHealthy, LevelSummary)
	sH := rawH.(*ResourceSummary)
	if sH.Issue != "" {
		t.Errorf("Healthy app Issue = %q, want empty", sH.Issue)
	}
}

func TestSummary_FluxHelmRelease(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "helm.toolkit.fluxcd.io/v2beta1",
			"kind":       "HelmRelease",
			"metadata":   map[string]any{"name": "redis", "namespace": "flux-system"},
			"spec": map[string]any{
				"chart": map[string]any{
					"spec": map[string]any{"chart": "redis"},
				},
			},
			"status": map[string]any{
				"conditions": []any{
					map[string]any{"type": "Ready", "status": "True"},
				},
				"lastAppliedRevision": "16.8.5",
			},
		},
	}

	raw := MinifyUnstructured(obj, LevelSummary)
	s := raw.(*ResourceSummary)

	if s.Status != "Ready" {
		t.Errorf("Status = %q, want Ready", s.Status)
	}
	if s.Version != "16.8.5" {
		t.Errorf("Version = %q, want 16.8.5", s.Version)
	}
	if s.Image != "redis" {
		t.Errorf("Image (chart) = %q, want redis", s.Image)
	}
}
