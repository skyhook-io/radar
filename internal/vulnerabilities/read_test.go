package vulnerabilities

import (
	"slices"
	"testing"

	"github.com/skyhook-io/radar/internal/readplan"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
)

// Where Radar collects a join input only in some namespaces, the answer is
// narrowed to those and the rest are named gaps, never read as absent.
func TestNarrowToTyped(t *testing.T) {
	read, gaps, ok := narrowToTyped(k8score.KindReadAssessment{Readiness: k8score.KindReady, AllNamespaces: true}, nil, nil)
	if !ok || read != nil || len(gaps) != 0 {
		t.Fatalf("all namespaces ready: %v %v %v", read, gaps, ok)
	}
	if _, _, ok := narrowToTyped(k8score.KindReadAssessment{Readiness: k8score.KindPending, AllNamespaces: true}, nil, nil); ok {
		t.Fatal("a kind still loading everywhere can't answer")
	}
	if _, _, ok := narrowToTyped(k8score.KindReadAssessment{Readiness: k8score.KindUnavailable}, nil, nil); ok {
		t.Fatal("an uncollected kind can't answer")
	}
	read, gaps, ok = narrowToTyped(k8score.KindReadAssessment{
		Readiness: k8score.KindPending, Read: []string{"a"}, Loading: []string{"b"}, Uncovered: []string{"c"},
	}, []string{"a", "b", "c"}, nil)
	if !ok || !slices.Equal(read, []string{"a"}) || len(gaps) != 2 {
		t.Fatalf("partial: %v %+v", read, gaps)
	}
	codes := []readreason.Code{gaps[0].Code, gaps[1].Code}
	if !slices.Contains(codes, readreason.RadarNamespaceNotCollected) || !slices.Contains(codes, readreason.KindSyncPending) {
		t.Fatalf("codes: %v", codes)
	}
	// Asked for every namespace from a namespace-scoped collector: the
	// namespaces it doesn't collect are unknown, and say so.
	_, gaps, _ = narrowToTyped(k8score.KindReadAssessment{Readiness: k8score.KindReady, Read: []string{"a"}}, nil, nil)
	if len(gaps) != 1 || gaps[0].Code != readreason.RadarNamespaceNotCollected {
		t.Fatalf("collection-limited: %+v", gaps)
	}
}

func TestNarrowToDynamic(t *testing.T) {
	plan := readplan.Plan{Partitions: []readplan.Partition{
		{Extent: readplan.NamespaceSet, Namespaces: []string{"a"}, Verdict: readplan.Verdict{State: readplan.Readable}},
		{Extent: readplan.NamespaceSet, Namespaces: []string{"b"}, Verdict: readplan.Verdict{State: readplan.OutsideCollection, Code: readreason.RadarNamespaceNotCollected}},
	}}
	read, gaps := narrowToDynamic(plan, []string{"a", "b"}, nil)
	if !slices.Equal(read, []string{"a"}) || len(gaps) != 1 || gaps[0].Namespaces.Names[0] != "b" {
		t.Fatalf("%v %+v", read, gaps)
	}
	limited := readplan.Plan{CollectionLimited: true, Partitions: []readplan.Partition{
		{Extent: readplan.NamespaceSet, Namespaces: []string{"a"}, Verdict: readplan.Verdict{State: readplan.Readable}},
	}}
	if read, gaps := narrowToDynamic(limited, nil, nil); !slices.Equal(read, []string{"a"}) || len(gaps) != 1 || gaps[0].Code != readreason.RadarNamespaceNotCollected {
		t.Fatalf("collection-limited: %v %+v", read, gaps)
	}
	whole := readplan.Plan{Partitions: []readplan.Partition{{Extent: readplan.AllNamespaces, Verdict: readplan.Verdict{State: readplan.Readable}}}}
	if read, gaps := narrowToDynamic(whole, nil, nil); read != nil || len(gaps) != 0 {
		t.Fatalf("whole: %v %+v", read, gaps)
	}
}
