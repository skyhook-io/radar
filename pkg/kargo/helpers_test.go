package kargo

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

func parse(t *testing.T, doc string) *unstructured.Unstructured {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal([]byte(doc), &m); err != nil {
		t.Fatal(err)
	}
	return &unstructured.Unstructured{Object: m}
}

func set(t *testing.T, u *unstructured.Unstructured, value any, fields ...string) {
	t.Helper()
	if err := unstructured.SetNestedField(u.Object, value, fields...); err != nil {
		t.Fatal(err)
	}
}
