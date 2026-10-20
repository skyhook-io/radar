package readscope

import (
	"reflect"
	"testing"
)

func TestSelectNamespacesSelectionAndAuthorization(t *testing.T) {
	for _, tt := range []struct {
		name                     string
		allowed, requested, want []string
	}{
		{"unrestricted all", nil, nil, nil},
		{"unrestricted empty preserved", nil, []string{}, []string{}},
		{"unrestricted selection", nil, []string{"b"}, []string{"b"}},
		{"omitted selection", []string{"a", "b"}, nil, []string{"a", "b"}},
		{"empty selection", []string{"a", "b"}, []string{}, []string{"a", "b"}},
		{"no authorization", []string{}, nil, []string{}},
		{"no overlap", []string{"a"}, []string{"b"}, []string{}},
		{"selection order", []string{"a", "b"}, []string{"b", "c", "a"}, []string{"b", "a"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := SelectNamespaces(tt.allowed, tt.requested); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}
