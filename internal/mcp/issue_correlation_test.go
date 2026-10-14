package mcp

import (
	"context"
	"fmt"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/meaningfulchanges"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

func initCorrelationStore(t *testing.T) timeline.EventStore {
	t.Helper()
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 200}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	timeline.SetObservationStartForTest(time.Now().Add(-2 * time.Hour))
	return timeline.GetStore()
}

func correlationIssue(kind, name string) issuesapi.Issue {
	return issuesapi.Issue{
		Severity: issuesapi.SeverityCritical,
		Kind:     kind, Namespace: "shop", Name: name,
		Reason: "CrashLoopBackOff",
	}
}

// The shared correlation reports truncation; the MCP issues response must
// still carry it as correlation_truncated.
func TestAttachIssueChangeCorrelation_SetsResponseTruncation(t *testing.T) {
	initCorrelationStore(t)
	var list []issuesapi.Issue
	for i := 0; i < meaningfulchanges.CorrelationIssueCap+1; i++ {
		list = append(list, correlationIssue("Deployment", fmt.Sprintf("dep-%d", i)))
	}
	resp := issues.ListResponse{Issues: list}
	attachIssueChangeCorrelation(context.Background(), &resp)
	if !resp.CorrelationTruncated {
		t.Fatal("correlation_truncated must be set when issues exceed the cap")
	}
}

// A workload whose only relevant change is on a ConfigMap the caller can't
// read must stay unmarked — never an affirmative no_recent_changes claim —
// while the same history is reported in full when auth is off.
func TestAttachIssueChangeCorrelation_RBACHiddenIsNotNoChanges(t *testing.T) {
	client := k8sfake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Volumes: []corev1.Volume{{
					Name: "config",
					VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: "web-config"},
					}},
				}},
				Containers: []corev1.Container{{Name: "web", Image: "web:v1"}},
			}},
		},
	})
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestState)
	store := initCorrelationStore(t)
	if err := store.Append(context.Background(), timeline.TimelineEvent{
		ID: "cm-change", Timestamp: time.Now().Add(-5 * time.Minute),
		Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
		Kind: "ConfigMap", APIVersion: "v1", Namespace: "shop", Name: "web-config",
		EventType: timeline.EventTypeUpdate,
		Diff:      &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "data.mode", OldValue: "a", NewValue: "b"}}, Summary: "mode changed"},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	ctx := withClusterAdmin(t, "corr-scoped")
	getPermCache().Get("corr-scoped", nil).SetCanI("list", "apps", "deployments", "shop", true)
	scoped := issues.ListResponse{Issues: []issuesapi.Issue{correlationIssue("Deployment", "web")}}
	attachIssueChangeCorrelation(ctx, &scoped)
	if web := scoped.Issues[0]; web.NoRecentChanges != nil || len(web.CorrelatedChanges) != 0 {
		t.Fatalf("hidden ConfigMap change must leave the issue unmarked: %+v", web)
	}

	open := issues.ListResponse{Issues: []issuesapi.Issue{correlationIssue("Deployment", "web")}}
	attachIssueChangeCorrelation(context.Background(), &open)
	if web := open.Issues[0]; len(web.CorrelatedChanges) != 1 || web.CorrelatedChanges[0].Kind != "ConfigMap" {
		t.Fatalf("auth off: want the ConfigMap change, got %+v", web)
	}
}
