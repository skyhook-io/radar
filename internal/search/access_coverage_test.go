package search

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/readscope"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

func TestTrivyAccessCoverageIndependentOfDeniedObjects(t *testing.T) {
	for _, deniedObjects := range []bool{false, true} {
		for _, query := range []string{"kind:VulnerabilityReport db", "db", "kind:VulnerabilityReport no-match"} {
			t.Run(fmt.Sprintf("objects=%v/%s", deniedObjects, query), func(t *testing.T) {
				reports := []*unstructured.Unstructured{vulnReport("read-a", "db-report", "php", "8", nil)}
				if deniedObjects {
					reports = append(reports, vulnReport("read-b", "db-hidden", "php", "8", nil))
				}
				p := trivyProvider(vulnGVR, "VulnerabilityReport", reports...)
				p.filterDynamicByNamespace = true
				access := &kindAccess{namespaces: []string{"read-a"}}
				res, err := Search(t.Context(), p, Parse(query), Options{Selection: readscope.Selection{Namespaces: []string{"read-a", "read-b"}}, DynamicKindAccess: access.options()})
				if err != nil {
					t.Fatal(err)
				}
				wantHits := []string{"read-a/db-report"}
				if query == "kind:VulnerabilityReport no-match" {
					wantHits = []string{}
				}
				if !slices.Equal(reportHitNames(res), wantHits) || !slices.Equal(p.dynamicListNamespaces, []string{"read-a"}) {
					t.Fatalf("hits=%v reads=%v", reportHitNames(res), p.dynamicListNamespaces)
				}
				if len(Parse(query).KindFilter) == 0 {
					if partial(res) || len(res.Coverage.Gaps) != 0 || !slices.Contains(res.Coverage.NarrowedBy, resourceapi.NarrowedByUserAccess) {
						t.Fatalf("broad coverage: %+v", res.Coverage)
					}
				} else {
					want := []resourceapi.Gap{{Code: readreason.UserForbidden, Kinds: resourceapi.Kinds([]resourceapi.KindRef{{Kind: "VulnerabilityReport", Group: trivyGroup}}, 1), Namespaces: resourceapi.UnknownNames()}}
					if !partial(res) || !reflect.DeepEqual(res.Coverage.Gaps, want) {
						t.Fatalf("coverage=%+v want=%+v", res.Coverage, want)
					}
				}
			})
		}
	}
}

func TestTrivyAccessPartitionsAndUnenumeratedScope(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		scope, allowed, unchecked []string
		reasons                   map[readreason.Code]*resourceapi.NameSet
		hits                      []string
	}{
		{"none", []string{"read-a", "read-b"}, nil, nil, map[readreason.Code]*resourceapi.NameSet{readreason.UserForbidden: resourceapi.AllNames()}, nil},
		{"all unchecked", []string{"read-a", "read-b"}, nil, []string{"read-a", "read-b"}, map[readreason.Code]*resourceapi.NameSet{readreason.UserPermissionCheckFailed: resourceapi.AllNames()}, nil},
		{"mixed", []string{"read-a", "read-b", "read-c"}, []string{"read-a"}, []string{"read-c"}, map[readreason.Code]*resourceapi.NameSet{readreason.UserForbidden: resourceapi.UnknownNames(), readreason.UserPermissionCheckFailed: resourceapi.UnknownNames()}, []string{"read-a/db-report"}},
		{"unenumerated", nil, []string{"read-a"}, nil, map[readreason.Code]*resourceapi.NameSet{readreason.UserForbidden: resourceapi.UnknownNames()}, []string{"read-a/db-report"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := trivyProvider(vulnGVR, "VulnerabilityReport", vulnReport("read-a", "db-report", "php", "8", nil))
			access := &kindAccess{namespaces: tc.allowed, unchecked: tc.unchecked}
			res, err := Search(t.Context(), p, Parse("kind:VulnerabilityReport"), Options{Selection: readscope.Selection{Namespaces: tc.scope}, DynamicKindAccess: access.options()})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(reportHitNames(res), tc.hits) || len(res.Coverage.Gaps) != len(tc.reasons) {
				t.Fatalf("%+v", res)
			}
			for _, gap := range res.Coverage.Gaps {
				if !reflect.DeepEqual(gap.Namespaces, tc.reasons[gap.Code]) {
					t.Fatalf("extent: %+v", gap)
				}
			}
		})
	}
}

func TestTrivyAccessPrecedesCollectorStateAndWarming(t *testing.T) {
	for _, state := range []k8score.DynamicObservationState{k8score.DynamicObservationSyncing, k8score.DynamicObservationDenied, k8score.DynamicObservationUnwatched, k8score.DynamicObservationUnsupported} {
		for _, query := range []string{"kind:VulnerabilityReport", "db"} {
			for _, scope := range [][]string{nil, {"read-a"}} {
				p := trivyProvider(vulnGVR, "VulnerabilityReport")
				p.observations = map[schema.GroupVersionResource]k8score.DynamicResourceObservation{vulnGVR: {State: state, Scope: k8score.DynamicObservationScopeCluster}}
				access := &kindAccess{}
				res, err := Search(t.Context(), p, Parse(query), Options{Selection: readscope.Selection{Namespaces: scope}, DynamicKindAccess: access.options()})
				if err != nil {
					t.Fatal(err)
				}
				if len(p.warmed) > 0 || len(p.dynamicListNamespaces) > 0 {
					t.Fatalf("denied caller caused IO: %v %v", p.warmed, p.dynamicListNamespaces)
				}
				if state == k8score.DynamicObservationUnsupported {
					if len(access.asked) != 0 || (len(Parse(query).KindFilter) > 0 && !hasKindGap(res, readreason.KindNotWatchable, "VulnerabilityReport")) {
						t.Fatalf("unsupported: %+v %v", res, access.asked)
					}
					continue
				}
				if len(access.asked) == 0 {
					t.Fatal("collector verdict preceded access")
				}
				if len(Parse(query).KindFilter) > 0 {
					if len(res.Coverage.Gaps) != 1 || !hasKindGap(res, readreason.UserForbidden, "VulnerabilityReport") {
						t.Fatalf("state %s scope %v: %+v", state, scope, res.Coverage)
					}
				} else if partial(res) || !slices.Contains(res.Coverage.NarrowedBy, resourceapi.NarrowedByUserAccess) {
					t.Fatalf("broad state %s: %+v", state, res.Coverage)
				}
			}
		}
	}
}

func TestTypedGapsPreserveSearchedExtent(t *testing.T) {
	for _, code := range []readreason.Code{readreason.KindSyncPending, readreason.KindSyncTimedOut, readreason.RadarCredentialsRejected, readreason.ReadError} {
		for _, whole := range []bool{false, true} {
			p := &fakeProvider{typedNamespaceReasons: map[string]map[string]string{"Pod": {"read-b": string(code)}}}
			if whole {
				p.typedNamespaceReasons["Pod"]["read-a"] = string(code)
			}
			res, err := Search(t.Context(), p, Parse("kind:Pod ns:read-a ns:read-b"), Options{Selection: readscope.Selection{Namespaces: []string{"read-a", "read-b"}}})
			if err != nil {
				t.Fatal(err)
			}
			want := resourceapi.UnknownNames()
			if whole {
				want = resourceapi.AllNames()
			}
			if len(res.Coverage.Gaps) != 1 || res.Coverage.Gaps[0].Code != code || !reflect.DeepEqual(res.Coverage.Gaps[0].Namespaces, want) {
				t.Fatalf("%s whole=%v: %+v", code, whole, res.Coverage)
			}
		}
	}
	{
		p := &fakeProvider{typed: map[string][]runtime.Object{"secrets": {&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "read-a", Name: "db-creds"}}, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "read-b", Name: "db-hidden"}}}}}
		for _, query := range []string{"kind:Secret db", "db"} {
			opts := Options{Selection: readscope.Selection{Namespaces: []string{"read-a", "read-b"}}}
			opts.TypedKindAccess = func([]string, string, string) ([]string, []string, []string) {
				return []string{"read-a"}, []string{"read-b"}, nil
			}

			res, err := Search(t.Context(), p, Parse(query), opts)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(hitNames(res), []string{"Secret/read-a/db-creds"}) {
				t.Fatalf("hits: %+v", res)
			}
			if query == "db" {
				if partial(res) || !slices.Contains(res.Coverage.NarrowedBy, resourceapi.NarrowedByUserAccess) {
					t.Fatalf("broad: %+v", res.Coverage)
				}
			} else if len(res.Coverage.Gaps) != 1 || res.Coverage.Gaps[0].Namespaces.Extent != resourceapi.ExtentUnknown {
				t.Fatalf("named: %+v", res.Coverage)
			}
		}
	}
	for _, narrowed := range []bool{false, true} {
		p := &fakeProvider{listErrors: map[string]error{"secrets": fmt.Errorf("list failed")}}
		scope := []string{"read-a", "read-b"}
		allowed := scope
		if narrowed {
			allowed = scope[:1]
		}
		res, err := Search(t.Context(), p, Parse("kind:Secret"), Options{Selection: readscope.Selection{Namespaces: scope}, TypedKindAccess: func([]string, string, string) ([]string, []string, []string) {
			return allowed, scope[len(allowed):], nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		want := resourceapi.AllNames()
		if narrowed {
			want = resourceapi.UnknownNames()
		}
		found := false
		for _, gap := range res.Coverage.Gaps {
			if gap.Code == readreason.ReadError {
				found = true
				if !reflect.DeepEqual(gap.Namespaces, want) {
					t.Fatalf("narrowed=%v: %+v", narrowed, gap)
				}
			}
		}
		if !found {
			t.Fatalf("missing list error: %+v", res.Coverage)
		}
	}
}

func TestCheckNamespacesStopsSchedulingOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	var checks atomic.Int32
	budget := new(atomic.Int32)
	budget.Store(readscope.MaxAccessChecks)
	namespaces := make([]string, 200)
	for i := range namespaces {
		namespaces[i] = fmt.Sprint(i)
	}
	allowed, unchecked := readscope.BudgetedNamespaceAccess(ctx, namespaces, budget, func(string) (bool, bool) { return false, false }, func(string) (bool, bool) { checks.Add(1); cancel(); return true, true })
	if checks.Load() > 16 || len(allowed)+len(unchecked) != len(namespaces) || len(unchecked) == 0 {
		t.Fatalf("checks=%d allowed=%d unchecked=%d", checks.Load(), len(allowed), len(unchecked))
	}
	before := checks.Load()
	_, unchecked = readscope.BudgetedNamespaceAccess(ctx, namespaces, budget, func(string) (bool, bool) { return false, false }, func(string) (bool, bool) { checks.Add(1); return true, true })
	if checks.Load() != before || len(unchecked) != len(namespaces) {
		t.Fatalf("cancelled scheduling: checks=%d unchecked=%d", checks.Load(), len(unchecked))
	}
}

func TestSearchSensitiveKindsLiveCoverageContract(t *testing.T) {
	p := trivyProvider(vulnGVR, "VulnerabilityReport", vulnReport("read-a", "db-report", "php", "8", nil), vulnReport("read-b", "db-hidden", "php", "8", nil))
	p.filterDynamicByNamespace = true
	p.typed = map[string][]runtime.Object{"secrets": {&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "read-a", Name: "db-creds"}}, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "read-b", Name: "db-hidden"}}}}
	for _, query := range []string{"kind:Secret db", "kind:VulnerabilityReport", "db"} {
		access := &kindAccess{namespaces: []string{"read-a"}}
		opts := Options{Selection: readscope.Selection{Namespaces: []string{"read-a", "read-b"}}, DynamicKindAccess: access.options(), TypedKindAccess: func([]string, string, string) ([]string, []string, []string) {
			return []string{"read-a"}, []string{"read-b"}, nil
		}}
		res, err := Search(t.Context(), p, Parse(query), opts)
		if err != nil {
			t.Fatal(err)
		}
		kind, group, want := "Secret", "", []string{"Secret/read-a/db-creds"}
		if query == "kind:VulnerabilityReport" {
			kind, group, want = "VulnerabilityReport", trivyGroup, []string{"VulnerabilityReport/read-a/db-report"}
		}
		if query == "db" {
			want = append(want, "VulnerabilityReport/read-a/db-report")
			if partial(res) || len(res.Coverage.Gaps) != 0 || !slices.Contains(res.Coverage.NarrowedBy, resourceapi.NarrowedByUserAccess) {
				t.Fatalf("broad: %+v", res.Coverage)
			}
		} else {
			gaps := []resourceapi.Gap{{Code: readreason.UserForbidden, Kinds: resourceapi.Kinds([]resourceapi.KindRef{{Kind: kind, Group: group}}, 1), Namespaces: resourceapi.UnknownNames()}}
			if !partial(res) || !reflect.DeepEqual(res.Coverage.Gaps, gaps) {
				t.Fatalf("%s: %+v", query, res.Coverage)
			}
		}
		got := hitNames(res)
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Fatalf("%s: hits=%v want=%v", query, got, want)
		}
	}
}

func TestTrivyAccessBoundsFailureAndWarmScope(t *testing.T) {
	for _, scope := range [][]string{nil, {"read-a", "read-b"}} {
		p := trivyProvider(vulnGVR, "VulnerabilityReport")
		p.dynamicErrors = map[string]error{"": fmt.Errorf("collector failed"), "read-a": fmt.Errorf("collector failed")}
		access := &kindAccess{namespaces: []string{"read-a"}}
		res, err := Search(t.Context(), p, Parse("kind:VulnerabilityReport"), Options{Selection: readscope.Selection{Namespaces: scope}, DynamicKindAccess: access.options()})
		if err != nil {
			t.Fatal(err)
		}
		want := map[readreason.Code]bool{readreason.UserForbidden: true}
		if scope != nil {
			want[readreason.ReadError] = true
		}
		if len(res.Coverage.Gaps) != len(want) {
			t.Fatalf("scope %v: %+v", scope, res.Coverage)
		}
		for _, gap := range res.Coverage.Gaps {
			if !want[gap.Code] || !reflect.DeepEqual(gap.Namespaces, resourceapi.UnknownNames()) {
				t.Fatalf("scope %v: %+v", scope, gap)
			}
		}
	}
	p := trivyProvider(vulnGVR, "VulnerabilityReport", vulnReport("read-a", "db-report", "php", "8", nil))
	p.observations = map[schema.GroupVersionResource]k8score.DynamicResourceObservation{vulnGVR: {State: k8score.DynamicObservationUnwatched}}
	p.filterDynamicByNamespace = true
	access := &kindAccess{namespaces: []string{"read-a"}}
	res, err := Search(t.Context(), p, Parse("kind:VulnerabilityReport"), Options{Selection: readscope.Selection{Namespaces: []string{"read-a", "read-b"}}, DynamicKindAccess: access.options()})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.warmNamespaces, []string{"read-a"}) || !slices.Equal(reportHitNames(res), []string{"read-a/db-report"}) || !hasKindGap(res, readreason.UserForbidden, "VulnerabilityReport") {
		t.Fatalf("warming=%v result=%+v", p.warmNamespaces, res)
	}
}

func TestIncompleteDiscoveryKeepsKnownResults(t *testing.T) {
	p := &fakeProvider{catalogErr: errIncompleteDiscovery, typed: map[string][]runtime.Object{"pods": {newPod("read-a", "db", "image", nil)}}}
	for _, query := range []string{"db", "kind:Pod db"} {
		res, err := Search(t.Context(), p, Parse(query), Options{})
		if err != nil {
			t.Fatal(err)
		}
		if partial(res) || len(res.Coverage.Gaps) != 0 || !slices.Equal(hitNames(res), []string{"Pod/read-a/db"}) {
			t.Fatalf("%s: %+v", query, res)
		}
		narrowed := slices.Contains(res.Coverage.NarrowedBy, resourceapi.NarrowedByKindCollection)
		if narrowed != (query == "db") {
			t.Fatalf("%s: %+v", query, res.Coverage)
		}
	}
}
