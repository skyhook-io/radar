package ownership_test

import (
	"slices"
	"testing"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"k8s.io/apimachinery/pkg/types"
)

type dependentGapsSession struct {
	ownership.ReadSession
	gaps []ownership.Gap
}

func (s dependentGapsSession) DependentCandidates(types.UID, ownership.SearchScope) ownership.DependentCandidatesRead {
	return ownership.DependentCandidatesRead{Gaps: s.gaps}
}

func TestChildrenPreservesSharedGapsWhileFilteringAndSorting(t *testing.T) {
	session, err := ownershiptest.Scan(ownershiptest.Fixture()).Begin()
	if err != nil {
		t.Fatal(err)
	}
	pod := ownership.Gap{Kind: pods, Namespace: "alpha", Reason: readreason.KindSyncPending}
	widget := ownership.Gap{Kind: resourceid.GroupKind{Group: "example.io", Kind: "Widget"}, Namespace: "alpha", Reason: readreason.KindSyncPending}
	for _, raw := range [][]ownership.Gap{{pod, widget}, {widget, pod, pod}} {
		original := slices.Clone(raw)
		caller := &access{}
		v := ownership.NewView(dependentGapsSession{session, raw}, caller)
		owner := at("apps", "ReplicaSet", "alpha", "web-1")
		first := v.Children(owner)
		if !slices.Equal(first.Gaps, []ownership.Gap{pod, widget}) {
			t.Fatalf("initial gaps: %v", first.Gaps)
		}
		caller.denied = []resourceid.GroupKind{pods}
		if got := v.Children(owner); !slices.Equal(got.Gaps, []ownership.Gap{widget}) {
			t.Fatalf("denied gaps: %v", got.Gaps)
		}
		caller.denied = nil
		caller.undecided = []resourceid.GroupKind{pods}
		if got := v.Children(owner); !slices.Equal(got.Gaps, []ownership.Gap{pod, widget}) {
			t.Fatalf("undecided gaps: %v", got.Gaps)
		}
		if !slices.Equal(raw, original) || !slices.Equal(first.Gaps, []ownership.Gap{pod, widget}) {
			t.Fatal("filtering changed source or earlier answer")
		}
	}
}
