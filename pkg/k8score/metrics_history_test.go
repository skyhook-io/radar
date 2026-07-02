package k8score

import (
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestMetricsCollectionErrorLevel(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "api not found",
			err:  apierrors.NewNotFound(schema.GroupResource{Group: "metrics.k8s.io", Resource: "pods"}, "api"),
			want: "warning",
		},
		{
			name: "metrics api resource absent",
			err:  errors.New("the server could not find the requested resource (get pods.metrics.k8s.io)"),
			want: "warning",
		},
		{
			name: "metrics kind not matched",
			err:  errors.New("no matches for kind PodMetrics in version metrics.k8s.io/v1beta1"),
			want: "warning",
		},
		{
			name: "metrics not available",
			err:  errors.New("pods.metrics.k8s.io not available"),
			want: "warning",
		},
		{
			name: "no metrics known",
			err:  errors.New("no metrics known for pod api in pods.metrics.k8s.io"),
			want: "warning",
		},
		{
			name: "unable to fetch metrics",
			err:  errors.New("unable to fetch metrics from pods.metrics.k8s.io"),
			want: "warning",
		},
		{
			name: "metrics APIService unavailable",
			err:  errors.New("the server is currently unable to handle the request (get pods.metrics.k8s.io)"),
			want: "warning",
		},
		{
			name: "non metrics missing resource",
			err:  errors.New("the server could not find the requested resource"),
			want: "error",
		},
		{
			name: "forbidden metrics",
			err:  errors.New("pods.metrics.k8s.io is forbidden"),
			want: "error",
		},
		{
			name: "nil",
			err:  nil,
			want: "error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := metricsCollectionErrorLevel(tt.err); got != tt.want {
				t.Fatalf("metricsCollectionErrorLevel() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseCPU(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{"", 0},
		{"137492n", 137492},
		{"188u", 188000},
		{"250m", 250000000},
		{"1", 1000000000},
	}

	for _, tt := range tests {
		if got := parseCPU(tt.input); got != tt.want {
			t.Errorf("parseCPU(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}
