package readscope

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

func TestResolveNamespacesExplicitMatrix(t *testing.T) {
	in, out, empty := "a", "b", ""
	pins := []*string{nil, &in, &out, &empty}
	for _, tc := range []struct {
		name    string
		access  []string
		missing bool
		codes   [4]readreason.Code
	}{
		{"unrestricted existing", nil, false, [4]readreason.Code{"", "", readreason.OutsideNamespacePin, readreason.OutsideNamespacePin}},
		{"unrestricted missing", nil, true, [4]readreason.Code{readreason.NamespaceNotFound, readreason.NamespaceNotFound, readreason.NamespaceNotFound, readreason.NamespaceNotFound}},
		{"visible existing", []string{"a"}, false, [4]readreason.Code{"", "", readreason.OutsideNamespacePin, readreason.OutsideNamespacePin}},
		{"visible missing", []string{"a"}, true, [4]readreason.Code{"", "", readreason.OutsideNamespacePin, readreason.OutsideNamespacePin}},
		{"hidden existing", []string{"b"}, false, [4]readreason.Code{readreason.UserNamespaceNotVisible, readreason.UserNamespaceNotVisible, readreason.UserNamespaceNotVisible, readreason.UserNamespaceNotVisible}},
		{"hidden missing", []string{"b"}, true, [4]readreason.Code{readreason.UserNamespaceNotVisible, readreason.UserNamespaceNotVisible, readreason.UserNamespaceNotVisible, readreason.UserNamespaceNotVisible}},
		{"none existing", []string{}, false, [4]readreason.Code{readreason.UserNamespaceNotVisible, readreason.UserNamespaceNotVisible, readreason.UserNamespaceNotVisible, readreason.UserNamespaceNotVisible}},
		{"none missing", []string{}, true, [4]readreason.Code{readreason.UserNamespaceNotVisible, readreason.UserNamespaceNotVisible, readreason.UserNamespaceNotVisible, readreason.UserNamespaceNotVisible}},
	} {
		for i, pin := range pins {
			for _, checked := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/pin%d/checked%t", tc.name, i, checked), func(t *testing.T) {
					var missing []string
					if tc.missing {
						missing = []string{"a"}
					}
					got, failure := ResolveNamespaces([]string{"a"}, NamespaceAccess{tc.access, checked}, pin, missing)
					want := Selection{Named: []string{"a"}, Namespaces: []string{}}
					var wantFailure readreason.Code
					if !checked {
						wantFailure = readreason.UserPermissionCheckFailed
					} else if code := tc.codes[i]; code != "" {
						want.Gaps = []NamespaceGap{{code, []string{"a"}}}
					} else {
						want.Namespaces = []string{"a"}
					}
					if !reflect.DeepEqual(got, want) || failure != wantFailure {
						t.Fatalf("got %+v / %s, want %+v / %s", got, failure, want, wantFailure)
					}
				})
			}
		}
	}
}

func TestResolveNamespacesImplicitAndEmpty(t *testing.T) {
	pin, emptyPin := "a", ""
	for _, tc := range []struct {
		name              string
		requested, access []string
		pin               *string
		namespaces        []string
		narrowed          []resourceapi.Narrowing
		code              readreason.Code
	}{
		{"implicit unrestricted", nil, nil, nil, nil, nil, ""},
		{"implicit restricted", nil, []string{"b", "a"}, nil, []string{"b", "a"}, []resourceapi.Narrowing{resourceapi.NarrowedByUserAccess}, ""},
		{"implicit no access", nil, []string{}, nil, []string{}, []resourceapi.Narrowing{resourceapi.NarrowedByUserAccess}, readreason.UserNoNamespaceAccess},
		{"implicit unrestricted pin", nil, nil, &pin, []string{"a"}, []resourceapi.Narrowing{resourceapi.NarrowedByNamespacePin}, ""},
		{"implicit visible pin", nil, []string{"b", "a"}, &pin, []string{"a"}, []resourceapi.Narrowing{resourceapi.NarrowedByUserAccess, resourceapi.NarrowedByNamespacePin}, ""},
		{"implicit hidden pin", nil, []string{"b"}, &pin, []string{}, []resourceapi.Narrowing{resourceapi.NarrowedByUserAccess, resourceapi.NarrowedByNamespacePin}, readreason.UserNoNamespaceAccess},
		{"implicit no access pin", nil, []string{}, &pin, []string{}, []resourceapi.Narrowing{resourceapi.NarrowedByUserAccess, resourceapi.NarrowedByNamespacePin}, readreason.UserNoNamespaceAccess},
		{"implicit empty pin", nil, nil, &emptyPin, []string{}, []resourceapi.Narrowing{resourceapi.NarrowedByNamespacePin}, readreason.UserNoNamespaceAccess},
		{"empty unrestricted", []string{}, nil, nil, []string{}, nil, ""},
		{"empty restricted defaults to ceiling", []string{}, []string{"b", "a"}, nil, []string{"b", "a"}, nil, ""},
		{"empty no access", []string{}, []string{}, nil, []string{}, nil, ""},
		{"empty unrestricted pin", []string{}, nil, &pin, []string{}, nil, ""},
		{"empty restricted pin", []string{}, []string{"a"}, &pin, []string{}, nil, ""},
		{"empty no access pin", []string{}, []string{}, &pin, []string{}, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, code := ResolveNamespaces(tc.requested, NamespaceAccess{tc.access, true}, tc.pin, []string{"a", "b"})
			want := Selection{Named: tc.requested, Namespaces: tc.namespaces, NarrowedBy: tc.narrowed}
			if !reflect.DeepEqual(got, want) || code != tc.code {
				t.Fatalf("got %#v / %s, want %#v / %s", got, code, want, tc.code)
			}
			unchecked, code := ResolveNamespaces(tc.requested, NamespaceAccess{tc.access, false}, tc.pin, nil)
			if code != readreason.UserPermissionCheckFailed || unchecked.Namespaces == nil || len(unchecked.Namespaces) != 0 || len(unchecked.Gaps) != 0 || len(unchecked.NarrowedBy) != 0 {
				t.Fatalf("unchecked: %+v / %s", unchecked, code)
			}
		})
	}
}

func TestResolveNamespacesPartitionsAndOwnsResults(t *testing.T) {
	requested := []string{"hidden", "pinned", "missing", "read", "read"}
	access := []string{"read", "pinned"}
	pin := "read"
	got, code := ResolveNamespaces(requested, NamespaceAccess{access, true}, &pin, []string{"missing"})
	want := Selection{Named: []string{"hidden", "missing", "pinned", "read"}, Namespaces: []string{"read"}, Gaps: []NamespaceGap{{readreason.OutsideNamespacePin, []string{"pinned"}}, {readreason.UserNamespaceNotVisible, []string{"hidden", "missing"}}}}
	if !reflect.DeepEqual(got, want) || code != "" {
		t.Fatalf("got %+v / %s, want %+v", got, code, want)
	}
	if !reflect.DeepEqual(requested, []string{"hidden", "pinned", "missing", "read", "read"}) {
		t.Fatal("mutated request")
	}
	got, code = ResolveNamespaces(requested, NamespaceAccess{Checked: true}, &pin, []string{"missing"})
	want.Gaps = []NamespaceGap{{readreason.NamespaceNotFound, []string{"missing"}}, {readreason.OutsideNamespacePin, []string{"hidden", "pinned"}}}
	if !reflect.DeepEqual(got, want) || code != "" {
		t.Fatalf("unrestricted: got %+v / %s, want %+v", got, code, want)
	}
	implicit, _ := ResolveNamespaces(nil, NamespaceAccess{access, true}, nil, nil)
	implicit.Namespaces[0] = "changed"
	if access[0] != "read" {
		t.Fatal("result aliases access ceiling")
	}
}
