package insights

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestMatchesFinalizerController(t *testing.T) {
	root := &unstructured.Unstructured{}
	for _, tt := range []struct {
		name, group, finalizer, workload string
		labels                           map[string]string
		want                             bool
	}{
		{name: "catalog label", group: "operator.victoriametrics.com", finalizer: "apps.victoriametrics.com/finalizer", workload: "custom", labels: map[string]string{"app.kubernetes.io/name": "victoria-metrics-operator"}, want: true},
		{name: "catalog wrong group", group: "unrelated.test", finalizer: "apps.victoriametrics.com/finalizer", workload: "victoria-metrics-operator"},
		{name: "name", group: "widgets.example.com", finalizer: "widgets.example.com/cleanup", workload: "release-widgets-operator", want: true},
		{name: "part-of", group: "widgets.example.com", finalizer: "widgets.example.com/cleanup", workload: "custom", labels: map[string]string{"app.kubernetes.io/part-of": "widgets-operator"}, want: true},
		{name: "unrelated domain", group: "other.example.com", finalizer: "widgets.example.com/cleanup", workload: "widgets-operator"},
		{name: "domain only", group: "widgets.example.com", finalizer: "widgets.example.com/cleanup", workload: "widgets-server"},
		{name: "substring", group: "widgets.example.com", finalizer: "widgets.example.com/cleanup", workload: "notwidgets-operator"},
		{name: "reserved domain", group: "k8s.io", finalizer: "k8s.io/cleanup", workload: "k8s-operator"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root.SetAPIVersion(tt.group + "/v1")
			workload := &metav1.ObjectMeta{Name: tt.workload, Labels: tt.labels}
			if got := MatchesFinalizerController(tt.finalizer, root, workload); got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}
