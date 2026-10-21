package ownership_test

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/skyhook-io/radar/pkg/ownership"
)

func TestAnyNamespace(t *testing.T) {
	namespaces := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}
	decide := func(byNamespace map[string]ownership.Decision) func(string) ownership.Decision {
		return func(ns string) ownership.Decision { return byNamespace[ns] }
	}
	for _, tc := range []struct {
		name       string
		namespaces []string
		decisions  map[string]ownership.Decision
		want       ownership.Decision
	}{
		{"none", nil, nil, ownership.Denied},
		{"all denied", namespaces, nil, ownership.Denied},
		{"one allowed, past the first batch", namespaces, map[string]ownership.Decision{"j": ownership.Allowed}, ownership.Allowed},
		{"one undecided", namespaces, map[string]ownership.Decision{"c": ownership.Undecided}, ownership.Undecided},
		{"allowed beats undecided", namespaces, map[string]ownership.Decision{"c": ownership.Undecided, "h": ownership.Allowed}, ownership.Allowed},
	} {
		if got := ownership.AnyNamespace(tc.namespaces, decide(tc.decisions)); got != tc.want {
			t.Errorf("%s: AnyNamespace = %v, want %v", tc.name, got, tc.want)
		}
	}
	var checked atomic.Int32
	ownership.AnyNamespace(namespaces, func(ns string) ownership.Decision {
		checked.Add(1)
		if ns == "a" {
			return ownership.Allowed
		}
		return ownership.Denied
	})
	if n := checked.Load(); n == int32(len(namespaces)) {
		t.Errorf("checked all %d namespaces after the first batch allowed", n)
	}
}

// A caller who may read every namespace of a large cluster costs a bounded
// number of checks: past them, a kind no checked namespace allows is
// undecided rather than denied.
func TestAnyNamespaceChecksABoundedNumber(t *testing.T) {
	var namespaces []string
	for i := range 1000 {
		namespaces = append(namespaces, fmt.Sprint("ns-", i))
	}
	for _, tc := range []struct {
		name    string
		allowed string
		want    ownership.Decision
	}{
		{"allowed among the first", "ns-3", ownership.Allowed},
		{"allowed only far down the list", "ns-900", ownership.Undecided},
		{"allowed nowhere", "", ownership.Undecided},
	} {
		var checked atomic.Int32
		got := ownership.AnyNamespace(namespaces, func(ns string) ownership.Decision {
			checked.Add(1)
			if ns == tc.allowed {
				return ownership.Allowed
			}
			return ownership.Denied
		})
		if got != tc.want {
			t.Errorf("%s: AnyNamespace = %v, want %v", tc.name, got, tc.want)
		}
		if n := checked.Load(); n > 16 {
			t.Errorf("%s: %d checks for one decision", tc.name, n)
		}
	}
}

func TestAnyNamespaceTreatsAPanickingCheckAsUndecided(t *testing.T) {
	got := ownership.AnyNamespace([]string{"alpha", "beta"}, func(namespace string) ownership.Decision {
		if namespace == "alpha" {
			panic("permission check failed")
		}
		return ownership.Denied
	})
	if got != ownership.Undecided {
		t.Fatalf("AnyNamespace with a panicking check = %v, want undecided", got)
	}
}
