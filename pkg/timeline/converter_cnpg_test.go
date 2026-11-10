package timeline

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func cnpgTestObject(apiVersion, kind string, labels map[string]string, spec map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": map[string]any{"name": "x", "namespace": "db"}}}
	if spec != nil {
		u.Object["spec"] = spec
	}
	if labels != nil {
		u.SetLabels(labels)
	}
	return u
}

func TestExtractLabelsRetainsTheOwningCNPGCluster(t *testing.T) {
	clusterRef := map[string]any{"cluster": map[string]any{"name": "pg-orders"}}
	cases := []struct {
		name string
		obj  any
		want string
	}{
		{"unlabelled Backup records spec.cluster.name", cnpgTestObject("postgresql.cnpg.io/v1", "Backup", nil, clusterRef), "pg-orders"},
		{"every spec.cluster kind", cnpgTestObject("postgresql.cnpg.io/v1", "Database", map[string]string{"team": "a"}, clusterRef), "pg-orders"},
		{"the label wins over spec", cnpgTestObject("postgresql.cnpg.io/v1", "Pooler", map[string]string{"cnpg.io/cluster": "pg-labelled"}, clusterRef), "pg-labelled"},
		{"barman ObjectStore label", cnpgTestObject("barmancloud.cnpg.io/v1", "ObjectStore", map[string]string{"cnpg.io/cluster": "pg-orders"}, nil), "pg-orders"},
		{"a Velero Backup is not attributed", cnpgTestObject("velero.io/v1", "Backup", map[string]string{"cnpg.io/cluster": "pg-orders"}, clusterRef), ""},
		{"a Deployment keeps no cnpg label", cnpgTestObject("apps/v1", "Deployment", map[string]string{"cnpg.io/cluster": "pg-orders"}, nil), ""},
		{"CNPG object without a cluster", cnpgTestObject("postgresql.cnpg.io/v1", "ImageCatalog", nil, nil), ""},
		{"typed Pod", &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pg-orders-1", Labels: map[string]string{"cnpg.io/cluster": "pg-orders"}}}, "pg-orders"},
		{"unstructured Pod", cnpgTestObject("v1", "Pod", map[string]string{"cnpg.io/cluster": "pg-orders"}, nil), "pg-orders"},
		{"a Pod's spec is never read", cnpgTestObject("v1", "Pod", nil, clusterRef), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractLabels(tc.obj)[CNPGClusterLabel]
			if got != tc.want {
				t.Fatalf("cnpg.io/cluster = %q, want %q (labels %v)", got, tc.want, ExtractLabels(tc.obj))
			}
		})
	}
}

func TestExtractLabelsStaysNilWithoutAnythingToRetain(t *testing.T) {
	if got := ExtractLabels(cnpgTestObject("apps/v1", "Deployment", nil, nil)); got != nil {
		t.Fatalf("labels = %v, want nil", got)
	}
	if got := ExtractLabels(cnpgTestObject("apps/v1", "Deployment", map[string]string{"unrelated": "x"}, nil)); got != nil {
		t.Fatalf("labels = %v, want nil", got)
	}
}

// A deleted child's tombstone keeps the attribution, so K8s Events that arrive
// after the delete still name the Cluster.
func TestTombstoneEntryCarriesCNPGAttribution(t *testing.T) {
	entry, ok := ExtractTombstoneEntry(cnpgTestObject("postgresql.cnpg.io/v1", "Backup", nil, map[string]any{"cluster": map[string]any{"name": "pg-orders"}}))
	if !ok || entry.Labels[CNPGClusterLabel] != "pg-orders" {
		t.Fatalf("tombstone labels = %v ok=%v", entry.Labels, ok)
	}
}
