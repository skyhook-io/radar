package cnpg

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestBackupDeclarations(t *testing.T) {
	data, err := os.ReadFile("testdata/backup-declarations.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name                                           string
		Cluster                                        map[string]any
		Method, Plugin, BlockerCode, DestinationPlugin string
		HasDestination                                 bool
		Barman                                         *struct{ ObjectStore, ServerName string }
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			declaration := ParseBackupDeclaration(&unstructured.Unstructured{Object: tc.Cluster})
			if declaration.HasDestination() != tc.HasDestination || declaration.DestinationPlugin() != tc.DestinationPlugin {
				t.Fatalf("destination = %v, plugin = %q", declaration.HasDestination(), declaration.DestinationPlugin())
			}
			blocker := declaration.DestinationBlocker(tc.Method, tc.Plugin)
			code := ""
			if blocker != nil {
				code = blocker.Code
			}
			if code != tc.BlockerCode {
				t.Fatalf("blocker = %q, want %q", code, tc.BlockerCode)
			}
			plugin, ok := declaration.BarmanPlugin()
			if ok != (tc.Barman != nil) {
				t.Fatalf("Barman enabled = %v", ok)
			}
			if tc.Barman != nil && (plugin.ObjectStore != tc.Barman.ObjectStore || plugin.ServerName != tc.Barman.ServerName) {
				t.Fatalf("Barman = %+v, want %+v", plugin, tc.Barman)
			}
		})
	}
}

func TestBackupMatchesClusterIncarnation(t *testing.T) {
	cluster := &unstructured.Unstructured{Object: map[string]any{"apiVersion": Group + "/v1", "kind": "Cluster", "metadata": map[string]any{"namespace": "pg", "name": "main", "uid": "current", "creationTimestamp": "2026-10-01T00:00:00Z"}}}
	backup := &unstructured.Unstructured{Object: map[string]any{"apiVersion": Group + "/v1", "kind": "Backup", "metadata": map[string]any{"namespace": "pg", "name": "backup", "creationTimestamp": "2026-10-01T01:00:00Z"}, "spec": map[string]any{"cluster": map[string]any{"name": "main"}}}}
	if !BackupMatchesCluster(backup, cluster) {
		t.Fatal("current unowned backup excluded")
	}
	for _, uid := range []string{"previous", "current"} {
		b := backup.DeepCopy()
		_ = unstructured.SetNestedField(b.Object, uid, "status", "pluginMetadata", "clusterUID")
		if BackupMatchesCluster(b, cluster) != (uid == "current") {
			t.Fatalf("pluginMetadata UID %s not respected", uid)
		}
		b = backup.DeepCopy()
		b.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: Group + "/v1", Kind: "Cluster", Name: "main", UID: types.UID(uid)}})
		if BackupMatchesCluster(b, cluster) != (uid == "current") {
			t.Fatalf("owner UID %s not respected", uid)
		}
	}
	old := backup.DeepCopy()
	old.SetCreationTimestamp(metav1.NewTime(time.Date(2026, 9, 30, 23, 0, 0, 0, time.UTC)))
	if BackupMatchesCluster(old, cluster) {
		t.Fatal("backup from before creation included")
	}
	old = backup.DeepCopy()
	_ = unstructured.SetNestedField(old.Object, "2026-09-30T23:00:00Z", "status", "startedAt")
	if BackupMatchesCluster(old, cluster) {
		t.Fatal("run from before creation included")
	}
	wrong := backup.DeepCopy()
	wrong.SetNamespace("elsewhere")
	if BackupMatchesCluster(wrong, cluster) {
		t.Fatal("cross-namespace backup included")
	}
	wrong.SetNamespace("pg")
	wrong.SetAPIVersion("velero.io/v1")
	if BackupMatchesCluster(wrong, cluster) {
		t.Fatal("other API group's Backup included")
	}
}
