package summarycontext

import (
	"context"
	"testing"

	"github.com/skyhook-io/radar/pkg/resourcecontext"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestUntilDeadlineStopsEnrichmentBetweenRows(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	build := UntilDeadline(ctx, func(runtime.Object, *unstructured.Unstructured, string, string, string, string) *resourcecontext.ResourceSummaryContext {
		calls++
		return &resourcecontext.ResourceSummaryContext{Health: "healthy"}
	})
	if got := build(nil, nil, "", "Pod", "shop", "first"); got == nil {
		t.Fatal("active enrichment skipped")
	}
	cancel()
	if got := build(nil, nil, "", "Pod", "shop", "second"); got != nil {
		t.Fatalf("expired enrichment %+v", got)
	}
	if calls != 1 {
		t.Fatalf("enrichment ran %d times", calls)
	}
}
