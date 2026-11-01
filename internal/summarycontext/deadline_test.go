package summarycontext

import (
	"context"
	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
	"slices"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/resourcecontext"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestExpiredBudgetPreservesHealthAndPreparedIssues(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	build := BuilderFromIndexes(ctx, func() IndexedTopology {
		t.Fatal("expired enrichment loaded the graph")
		return IndexedTopology{}
	}, IssueIndex{issueIndexKey("", "Pod", "shop", "web"): 7}, nil, nil, nil)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web"}, Status: corev1.PodStatus{Phase: corev1.PodFailed}}
	got := build(pod, nil, "", "Pod", "shop", "web")
	if got == nil || got.Health == "" || got.IssueCount != 7 || got.ManagedBy != nil {
		t.Fatalf("expired summary lost cheap evidence: %+v", got)
	}
	if len(got.Omitted) != 1 || got.Omitted[0] != (resourcecontext.OmittedField{Field: "managedBy", Reason: resourcecontext.OmittedBudgetExceeded}) {
		t.Fatalf("omissions %+v", got.Omitted)
	}
}

func TestUnbuiltSummaryKeepsHealthAndMarksUnknownCounts(t *testing.T) {
	for _, expired := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if expired {
			cancel()
		}
		build, stop := WithBudget(ctx, func(context.Context) Builder { return nil })
		pod := &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodFailed}}
		got := build(pod, nil, "", "Pod", "shop", "web")
		stop()
		cancel()
		if got == nil || got.Health == "" || len(got.Omitted) != 2 {
			t.Fatalf("expired=%v: %+v", expired, got)
		}
		want := resourcecontext.OmittedUnavailable
		if expired {
			want = resourcecontext.OmittedBudgetExceeded
		}
		for _, note := range got.Omitted {
			if note.Reason != want {
				t.Fatalf("expired=%v: %+v", expired, note)
			}
		}
	}
}

func TestBudgetStartsAtFirstRow(t *testing.T) {
	called := false
	var deadline time.Time
	build, stop := WithBudget(context.Background(), func(ctx context.Context) Builder {
		called = true
		deadline, _ = ctx.Deadline()
		return BuilderFromIndexes(ctx, nil, nil, nil, nil, nil)
	})
	defer stop()
	if called {
		t.Fatal("prepared context before the search produced a row")
	}
	time.Sleep(20 * time.Millisecond)
	started := time.Now()
	build(&corev1.Pod{}, nil, "", "Pod", "shop", "web")
	if !called || deadline.Before(started.Add(Budget-10*time.Millisecond)) {
		t.Fatal("search time consumed enrichment budget")
	}
}

func TestSlowPreparationDoesNotDisableOtherCallers(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	slow, stopSlow := WithBudget(ctx, func(context.Context) Builder { <-release; return nil })
	defer stopSlow()
	slow(&corev1.Pod{}, nil, "", "Pod", "shop", "web")
	fast, stopFast := WithBudget(context.Background(), func(ctx context.Context) Builder {
		return BuilderFromIndexes(ctx, nil, IssueIndex{issueIndexKey("", "Pod", "shop", "web"): 7}, nil, nil, nil)
	})
	defer stopFast()
	got := fast(&corev1.Pod{}, nil, "", "Pod", "shop", "web")
	if got == nil || got.IssueCount != 7 {
		t.Fatalf("another caller lost readable context: %+v", got)
	}
}

func TestTrivyPreparationFailureMarksScanned(t *testing.T) {
	for _, expired := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if expired {
			cancel()
		}
		build, stop := WithBudget(ctx, func(context.Context) Builder { return nil })
		report := &unstructured.Unstructured{}
		report.SetLabels(map[string]string{"trivy-operator.resource.kind": "ReplicaSet", "trivy-operator.resource.name": "web-old"})
		got := build(nil, report, aicontext.TrivyGroup, "VulnerabilityReport", "shop", "report")
		if got.Scanned == nil || got.Scanned.Name != "web-old" {
			t.Fatalf("lost label-derived scan target: %+v", got)
		}
		stop()
		cancel()
		want := resourcecontext.OmittedUnavailable
		if expired {
			want = resourcecontext.OmittedBudgetExceeded
		}
		if !slices.Contains(got.Omitted, resourcecontext.OmittedField{Field: "scanned", Reason: want}) {
			t.Fatalf("expired=%v: scanned omission missing: %+v", expired, got.Omitted)
		}
	}
}

func TestExpiredTrivyBudgetPreservesScanLabels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report := &unstructured.Unstructured{}
	report.SetLabels(map[string]string{"trivy-operator.resource.kind": "ReplicaSet", "trivy-operator.resource.name": "web-old"})
	build := BuilderFromIndexes(ctx, nil, nil, nil, nil, nil)
	got := build(nil, report, aicontext.TrivyGroup, "VulnerabilityReport", "shop", "report")
	if got.Scanned == nil || got.Scanned.Name != "web-old" || got.Scanned.Replicas != nil || got.Scanned.Missing {
		t.Fatalf("scan metadata lost or live state invented: %+v", got.Scanned)
	}
}
