package configrefs

import (
	"reflect"
	"slices"
	"testing"
)

func TestReflectionIdentityAndPropagation(t *testing.T) {
	source := Ref{"Secret", "source", "tls"}
	mirror := Ref{"Secret", "target", "different-name"}
	leaf := Ref{"Secret", "leaf", "tls"}
	objects := []Object{{Ref: source, Annotations: map[string]string{reflectorPrefix + "reflection-allowed": "true"}}, {Ref: mirror, Annotations: map[string]string{reflectorPrefix + "reflects": "source/tls", reflectorPrefix + "auto-reflects": "True"}}, {Ref: leaf, Annotations: map[string]string{reflectorPrefix + "reflects": "target/different-name"}}, {Ref: Ref{"Secret", "unrelated", "tls"}}, {Ref: Ref{"ConfigMap", "target", "different-name"}, Annotations: map[string]string{reflectorPrefix + "reflects": "source/tls"}}}
	first := BuildReflections(objects)
	if len(first.Links) != 2 || !first.Automatic[mirror] {
		t.Fatalf("relationships: %+v", first)
	}
	used := map[Ref]bool{leaf: true}
	first.PropagateUse(used)
	if len(used) != 3 || !used[source] || !used[mirror] {
		t.Fatalf("propagation: %v", used)
	}
	slices.Reverse(objects)
	if second := BuildReflections(objects); !reflect.DeepEqual(first, second) {
		t.Fatal("order dependent")
	}
	unused := map[Ref]bool{}
	first.PropagateUse(unused)
	if len(unused) != 0 {
		t.Fatal("auto mirror must not seed application use")
	}
}
func TestReflectionMalformedAndCycles(t *testing.T) {
	a := Ref{"ConfigMap", "ns", "a"}
	b := Ref{"ConfigMap", "ns", "b"}
	for _, value := range []string{"", "ns", "ns/", "/a", "ns/a/extra", "ns/a", "other/missing"} {
		if len(BuildReflections([]Object{{Ref: a, Annotations: map[string]string{reflectorPrefix + "reflects": value}}}).Links) != 0 {
			t.Errorf("resolved %q", value)
		}
	}
	index := BuildReflections([]Object{{Ref: a, Annotations: map[string]string{reflectorPrefix + "reflects": "ns/b"}}, {Ref: b, Annotations: map[string]string{reflectorPrefix + "reflects": "ns/a"}}})
	used := map[Ref]bool{a: true}
	index.PropagateUse(used)
	if len(used) != 2 {
		t.Fatal("cycle traversal")
	}
}
