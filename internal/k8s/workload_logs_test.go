package k8s

import "testing"

func TestWorkflowArchiveLogsConfigured(t *testing.T) {
	tests := []struct {
		name     string
		workflow map[string]any
		want     bool
	}{
		{
			name:     "workflow spec",
			workflow: map[string]any{"spec": map[string]any{"archiveLogs": true}},
			want:     true,
		},
		{
			name: "template archive location",
			workflow: map[string]any{"spec": map[string]any{"templates": []any{
				map[string]any{"name": "main"},
				map[string]any{"archiveLocation": map[string]any{"archiveLogs": true}},
			}}},
			want: true,
		},
		{
			name:     "off",
			workflow: map[string]any{"spec": map[string]any{"archiveLogs": false}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := WorkflowArchiveLogsConfigured(tt.workflow); got != tt.want {
				t.Fatalf("WorkflowArchiveLogsConfigured() = %v, want %v", got, tt.want)
			}
		})
	}
}
