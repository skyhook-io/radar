package cachesource

import (
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// BenchmarkRecordingManagers is one search for the managers recording an object among 300
// Applications of 40 entries each, a few of them unreadable.
func BenchmarkRecordingManagers(b *testing.B) {
	w := ownershiptest.World{Resources: ownershiptest.Resources(), Collection: map[resourceid.GroupKind]ownershiptest.Collection{}}
	for _, k := range ownershiptest.Kinds {
		w.Collection[k.GroupKind] = ownershiptest.Collection{OnDemand: !k.Typed}
	}
	for i := range 300 {
		o := ownershiptest.Object{
			Ref:        resourceid.Ref{Group: ownership.ArgoApplication.Group, Kind: ownership.ArgoApplication.Kind, Namespace: "argocd", Name: fmt.Sprint("app-", i)},
			APIVersion: "argoproj.io/v1alpha1", UID: types.UID(fmt.Sprint("uid-app-", i)), ResourceVersion: "1",
			Membership: ownership.MembersRecorded, Destination: fmt.Sprint("team-", i%50),
		}
		if i%100 == 0 {
			o.Membership = ownership.MembersUnreadable
		}
		for j := range 40 {
			o.Members = append(o.Members, resourceid.Ref{Group: "apps", Kind: "Deployment", Namespace: o.Destination, Name: fmt.Sprintf("app-%d-service-%d", i, j)})
		}
		w.Objects = append(w.Objects, o)
	}
	s := begin(b, render(b, w).source())
	target := resourceid.Ref{Group: "apps", Kind: "Deployment", Namespace: "team-7", Name: "app-7-service-3"}
	if got := s.RecordingManagers(target); len(got.Managers) != 1 || len(got.Unreadable) != 3 || len(got.Gaps) != 0 {
		b.Fatalf("RecordingManagers = %+v", got)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		s.RecordingManagers(target)
	}
}
