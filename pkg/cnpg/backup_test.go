package cnpg

import (
	"encoding/json"
	"os"
	"testing"

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
