package k8s

import (
	"reflect"
	"testing"
)

func TestSplitYAMLDocuments(t *testing.T) {
	content := `---
apiVersion: v1
kind: ConfigMap
data:
  script: |
    echo before
    ---
    echo after
--- # second resource
apiVersion: v1
kind: Service
metadata:
  name: demo
---not-a-separator: true
`

	want := []string{
		"apiVersion: v1\nkind: ConfigMap\ndata:\n  script: |\n    echo before\n    ---\n    echo after",
		"apiVersion: v1\nkind: Service\nmetadata:\n  name: demo\n---not-a-separator: true",
	}
	if got := SplitYAMLDocuments(content); !reflect.DeepEqual(got, want) {
		t.Fatalf("SplitYAMLDocuments() = %#v, want %#v", got, want)
	}
}

func TestSplitYAMLDocumentsCRLF(t *testing.T) {
	got := SplitYAMLDocuments("kind: ConfigMap\r\n---\r\nkind: Service\r\n")
	want := []string{"kind: ConfigMap", "kind: Service"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SplitYAMLDocuments() = %#v, want %#v", got, want)
	}
}
