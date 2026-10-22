package k8s

import (
	"github.com/skyhook-io/radar/pkg/k8score"
	"slices"
	"testing"
)

func TestAssessKindReadMissingWrapper(t *testing.T) {
	for _, cache := range []*ResourceCache{nil, {}} {
		var source interface {
			AssessKindRead(string, []string) k8score.KindReadAssessment
		} = cache
		requested := []string{"alpha"}
		got := source.AssessKindRead("pods", requested)
		if got.Readiness != k8score.KindUnavailable || got.Complete || got.AllNamespaces || got.Read != nil || !slices.Equal(got.Requested, requested) {
			t.Fatalf("missing wrapper = %+v", got)
		}
	}
}
