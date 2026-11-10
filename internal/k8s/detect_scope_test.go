package k8s

import "testing"

// Loaders build on other lookups through the same scope, so a nested get must
// not wait on the outer one.
func TestDetectScopeNestedGet(t *testing.T) {
	scope := NewDetectScope()
	loads := 0
	got := scope.get("outer", func() any {
		return scope.get("inner", func() any { loads++; return 1 }).(int) + 1
	})
	if got != 2 || scope.get("inner", func() any { loads++; return 0 }) != 1 || loads != 1 {
		t.Fatalf("outer = %v, loads = %d", got, loads)
	}
}
