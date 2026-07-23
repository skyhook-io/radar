package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

func TestGetChangesEmitsTierAWithoutIssueAwarePromotion(t *testing.T) {
	store := initCorrelationStore(t)
	if err := store.Append(context.Background(), timeline.TimelineEvent{
		ID: "frontend-env", Timestamp: time.Now().Add(-time.Minute),
		Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
		Kind: "Deployment", Namespace: "shop", Name: "frontend",
		EventType: timeline.EventTypeUpdate,
		Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{
			Path: "spec.template.spec.containers[frontend].env[CART_ADDR]",
		}}},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	result, _, err := handleGetChanges(context.Background(), nil, getChangesInput{
		Namespace: "shop",
		Limit:     5,
	})
	if err != nil {
		t.Fatalf("handleGetChanges: %v", err)
	}
	var response getChangesResponseMCP
	if err := json.Unmarshal([]byte(extractText(t, result)), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Changes) != 1 {
		t.Fatalf("changes = %+v, want one entry", response.Changes)
	}
	if got := response.Changes[0].Salience; got != issuesapi.ChangeSalienceConfigEdit {
		t.Fatalf("get_changes salience = %q, want config_edit only", got)
	}
}

func TestIssuesResponsePrimeSuspectsNeverOverlapCorrelatedChanges(t *testing.T) {
	created := time.Now().Add(-10 * time.Minute)
	replicas := int32(1)
	client := fake.NewSimpleClientset(
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "flagd-config", Namespace: "shop"},
		},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "flagd",
				Namespace:         "shop",
				CreationTimestamp: metav1.NewTime(created),
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "flagd"}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "flagd"}},
					Spec: corev1.PodSpec{Containers: []corev1.Container{{
						Name:  "flagd",
						Image: "flagd:v1",
						EnvFrom: []corev1.EnvFromSource{{
							ConfigMapRef: &corev1.ConfigMapEnvSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: "flagd-config"},
							},
						}},
					}}},
				},
			},
			Status: appsv1.DeploymentStatus{
				UnavailableReplicas: 1,
				Conditions: []appsv1.DeploymentCondition{{
					Type:               appsv1.DeploymentAvailable,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: metav1.NewTime(created.Add(5 * time.Second)),
				}},
			},
		},
	)
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestState)

	store := initCorrelationStore(t)
	for _, event := range []timeline.TimelineEvent{
		{
			ID: "flagd-config", Timestamp: time.Now().Add(-12 * time.Minute),
			Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
			Kind: "ConfigMap", Namespace: "shop", Name: "flagd-config",
			EventType: timeline.EventTypeUpdate,
			Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{
				Path: "data.flags.json.flags.adFailure.defaultVariant", OldValue: "off", NewValue: "on",
			}}},
		},
		{
			ID: "frontend-env", Timestamp: time.Now().Add(-5 * time.Minute),
			Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
			Kind: "Deployment", Namespace: "shop", Name: "frontend",
			EventType: timeline.EventTypeUpdate,
			Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{
				Path: "spec.template.spec.containers[frontend].env[CART_ADDR]",
			}}},
		},
	} {
		if err := store.Append(context.Background(), event); err != nil {
			t.Fatalf("append %s: %v", event.ID, err)
		}
	}

	result, _, err := handleIssuesTool(context.Background(), nil, issuesInput{Namespace: "shop"})
	if err != nil {
		t.Fatalf("handleIssuesTool: %v", err)
	}
	var response issuesapi.Response
	if err := json.Unmarshal([]byte(extractText(t, result)), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.RecentChangesReason != "recent_changes_with_all_critical_issues_at_creation" {
		t.Fatalf("recent_changes_reason = %q, want creation-time critical branch", response.RecentChangesReason)
	}

	changeKey := func(change issuesapi.RecentChange) string {
		return fmt.Sprintf("%s/%s/%s@%s:%s", change.Kind, change.Namespace, change.Name, change.Timestamp, change.ChangeType)
	}
	correlated := map[string]bool{}
	correlatedFlagdConfig := false
	for _, issue := range response.Issues {
		for _, change := range issue.CorrelatedChanges {
			correlated[changeKey(change)] = true
			if change.Kind == "ConfigMap" && change.Namespace == "shop" && change.Name == "flagd-config" {
				correlatedFlagdConfig = true
			}
		}
	}
	if !correlatedFlagdConfig {
		t.Fatalf("final response did not correlate flagd-config to its failing consumer: %+v", response.Issues)
	}

	primeCount := 0
	explainedConfigFound := false
	for _, change := range response.RecentChanges {
		if change.Kind == "ConfigMap" && change.Namespace == "shop" && change.Name == "flagd-config" {
			explainedConfigFound = true
			if change.Salience != issuesapi.ChangeSalienceConfigEdit {
				t.Fatalf("correlated ConfigMap salience = %q, want config_edit", change.Salience)
			}
		}
		if change.Salience != issuesapi.ChangeSaliencePrimeSuspect {
			continue
		}
		primeCount++
		if correlated[changeKey(change)] {
			t.Fatalf("final MCP response marks correlated change as prime_suspect: %+v", change)
		}
	}
	if !explainedConfigFound {
		t.Fatalf("final response omitted explained ConfigMap from recent_changes: %+v", response.RecentChanges)
	}
	if primeCount == 0 {
		t.Fatalf("invariant test is vacuous: no prime_suspect in recent_changes: %+v", response.RecentChanges)
	}
}
