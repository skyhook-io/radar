package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/meaningfulchanges"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

func TestIssuesFilterSchemaDocumentsOnsetProvenanceBindings(t *testing.T) {
	var description string
	for _, field := range reflect.VisibleFields(reflect.TypeOf(issuesInput{})) {
		if field.Tag.Get("json") == "filter,omitempty" {
			description = field.Tag.Get("jsonschema")
		}
	}
	for _, text := range []string{"first_seen is the earliest evidence-backed active-time anchor", "may be Radar's first observation", "Every logical branch that compares first_seen as an age must guard known timing", "onset_coverage is emitted only when at least one contributing signal has unknown timing", "need not share first_seen's anchor", "resource_created_at is resource-age context", "never onset"} {
		if !strings.Contains(description, text) {
			t.Fatalf("issues filter schema must explain %q; got %q", text, description)
		}
	}
}

func TestGetChangesEmitsApplicationConfigurationClassificationWithoutIssueAwarePromotion(t *testing.T) {
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
	if !response.Changes[0].ApplicationConfigurationChange || response.Changes[0].NotLinkedToReturnedIssues {
		t.Fatalf("get_changes marker = %+v, want static classification only", response.Changes[0])
	}
}

func TestGetChangesDistinguishesOutputCappingFromFetchSaturation(t *testing.T) {
	appendChanges := func(t *testing.T, count int) {
		t.Helper()
		store := initCorrelationStore(t)
		for i := 0; i < count; i++ {
			if err := store.Append(context.Background(), timeline.TimelineEvent{
				ID: fmt.Sprintf("config-%03d", i), Timestamp: time.Now().Add(-time.Duration(i+1) * time.Second),
				Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
				Kind: "Deployment", Namespace: "shop", Name: fmt.Sprintf("frontend-%03d", i),
				EventType: timeline.EventTypeUpdate,
				Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{
					Path: "spec.template.spec.containers[frontend].env[CART_ADDR]",
				}}},
			}); err != nil {
				t.Fatalf("append change %d: %v", i, err)
			}
		}
	}
	getHint := func(t *testing.T) string {
		t.Helper()
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
		return response.NarrowHint
	}

	t.Run("output capped after complete fetch", func(t *testing.T) {
		appendChanges(t, 6)
		hint := getHint(t)
		if !strings.Contains(hint, "full requested window") ||
			!strings.Contains(hint, "lower-ranked changes were omitted") {
			t.Fatalf("output-cap hint = %q, want complete-window semantics", hint)
		}
		if strings.Contains(hint, "candidate cap") || strings.Contains(hint, "query never saw") {
			t.Fatalf("output-cap hint falsely claims fetch saturation: %q", hint)
		}
	})

	t.Run("candidate fetch saturated", func(t *testing.T) {
		appendChanges(t, 100)
		hint := getHint(t)
		if !strings.Contains(hint, meaningfulchanges.FetchSaturatedGuidance) {
			t.Fatalf("fetch-saturation hint = %q, want %q", hint, meaningfulchanges.FetchSaturatedGuidance)
		}
		if strings.Contains(hint, "full requested window") {
			t.Fatalf("fetch-saturation hint falsely claims complete observation: %q", hint)
		}
	})
}

func TestIssuesResponseUnlinkedChangesNeverOverlapCorrelatedChanges(t *testing.T) {
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
	for i := 0; i < 4; i++ {
		if err := store.Append(context.Background(), timeline.TimelineEvent{
			ID: fmt.Sprintf("generic-spec-%d", i), Timestamp: time.Now().Add(-time.Duration(6+i) * time.Minute),
			Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
			Kind: "Deployment", Namespace: "shop", Name: fmt.Sprintf("generic-%d", i),
			EventType: timeline.EventTypeUpdate,
			Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{
				Path: "spec.replicas", OldValue: int32(1), NewValue: int32(2),
			}}},
		}); err != nil {
			t.Fatalf("append generic spec change %d: %v", i, err)
		}
	}

	result, _, err := handleIssuesTool(context.Background(), nil, issuesInput{Namespace: "shop"})
	if err != nil {
		t.Fatalf("handleIssuesTool: %v", err)
	}
	raw := []byte(extractText(t, result))
	var response issuesapi.Response
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("decode raw response: %v", err)
	}
	if response.RecentChangesReason != "recent_changes_with_all_critical_issues_at_creation" {
		t.Fatalf("recent_changes_reason = %q, want creation-time critical branch", response.RecentChangesReason)
	}
	if !response.RecentChangesTruncated {
		t.Fatal("six observed changes recapped to five must set recent_changes_truncated")
	}
	if strings.Contains(response.RecentChangesGuidance, "candidate cap") {
		t.Fatalf("recap-only response must not claim the candidate query saturated: %q", response.RecentChangesGuidance)
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

	unlinkedCount := 0
	explainedConfigFound := false
	for _, change := range response.RecentChanges {
		if change.Kind == "ConfigMap" && change.Namespace == "shop" && change.Name == "flagd-config" {
			explainedConfigFound = true
			if !change.ApplicationConfigurationChange || change.NotLinkedToReturnedIssues {
				t.Fatalf("correlated ConfigMap marker = %+v, want static classification only", change)
			}
		}
		if !change.NotLinkedToReturnedIssues {
			continue
		}
		unlinkedCount++
		if correlated[changeKey(change)] {
			t.Fatalf("final MCP response marks correlated change as not linked: %+v", change)
		}
	}
	if !explainedConfigFound {
		t.Fatalf("final response omitted explained ConfigMap from recent_changes: %+v", response.RecentChanges)
	}
	if unlinkedCount == 0 {
		t.Fatalf("invariant test is vacuous: no not-linked change in recent_changes: %+v", response.RecentChanges)
	}

	recentChanges, ok := wire["recent_changes"].([]any)
	if !ok {
		t.Fatalf("raw recent_changes = %#v, want array", wire["recent_changes"])
	}
	rawUnlinked := 0
	rawLinkedConfig := false
	for _, entry := range recentChanges {
		change, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("raw recent change = %#v, want object", entry)
		}
		if change["not_linked_to_returned_issues"] == true {
			rawUnlinked++
			if change["application_configuration_change"] != true {
				t.Fatalf("unlinked wire entry lacks static classification: %#v", change)
			}
		}
		if change["kind"] == "ConfigMap" && change["name"] == "flagd-config" {
			rawLinkedConfig = true
			if change["application_configuration_change"] != true {
				t.Fatalf("linked ConfigMap wire entry lacks static classification: %#v", change)
			}
			if _, exists := change["not_linked_to_returned_issues"]; exists {
				t.Fatalf("linked ConfigMap wire entry carries relational marker: %#v", change)
			}
		}
		if _, exists := change["salience"]; exists {
			t.Fatalf("obsolete salience key remains on wire: %#v", change)
		}
	}
	if rawUnlinked == 0 || !rawLinkedConfig {
		t.Fatalf("raw wire assertions are vacuous: unlinked=%d linkedConfig=%v", rawUnlinked, rawLinkedConfig)
	}
	if strings.Contains(string(raw), "prime_suspect") || strings.Contains(string(raw), "config_edit") {
		t.Fatalf("obsolete marker value remains on wire: %s", raw)
	}

	rawIssues, ok := wire["issues"].([]any)
	if !ok {
		t.Fatalf("raw issues = %#v, want array", wire["issues"])
	}
	rawCorrelatedConfig := false
	for _, entry := range rawIssues {
		issue, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("raw issue = %#v, want object", entry)
		}
		correlatedChanges, _ := issue["correlated_changes"].([]any)
		for _, correlatedEntry := range correlatedChanges {
			change, ok := correlatedEntry.(map[string]any)
			if !ok {
				t.Fatalf("raw correlated change = %#v, want object", correlatedEntry)
			}
			if change["kind"] == "ConfigMap" && change["name"] == "flagd-config" {
				rawCorrelatedConfig = true
				if change["application_configuration_change"] != true {
					t.Fatalf("correlated ConfigMap lacks static classification: %#v", change)
				}
				if _, exists := change["not_linked_to_returned_issues"]; exists {
					t.Fatalf("correlated ConfigMap carries top-level relational marker: %#v", change)
				}
			}
		}
	}
	if !rawCorrelatedConfig {
		t.Fatal("raw response did not contain the expected correlated ConfigMap")
	}

	filteredResult, _, err := handleIssuesTool(context.Background(), nil, issuesInput{
		Namespace: "shop",
		Severity:  "critical",
	})
	if err != nil {
		t.Fatalf("handleIssuesTool severity=critical: %v", err)
	}
	filteredRaw := []byte(extractText(t, filteredResult))
	var filteredResponse issuesapi.Response
	if err := json.Unmarshal(filteredRaw, &filteredResponse); err != nil {
		t.Fatalf("decode filtered response: %v", err)
	}
	for _, change := range filteredResponse.RecentChanges {
		if change.NotLinkedToReturnedIssues {
			t.Fatalf("severity-filtered response promoted a change: %+v", change)
		}
	}
	if !strings.Contains(filteredResponse.RecentChangesGuidance, "severity-filtered") ||
		!strings.Contains(filteredResponse.RecentChangesGuidance, "omitted issue rows may explain them") {
		t.Fatalf("filtered guidance does not disclose fail-closed behavior: %q", filteredResponse.RecentChangesGuidance)
	}
	if strings.Contains(string(filteredRaw), "not_linked_to_returned_issues") {
		t.Fatalf("severity-filtered raw response contains relational marker: %s", filteredRaw)
	}

	completeResult, _, err := handleIssuesTool(context.Background(), nil, issuesInput{
		Namespace: "shop",
		Severity:  "critical,warning",
	})
	if err != nil {
		t.Fatalf("handleIssuesTool severity=critical,warning: %v", err)
	}
	completeRaw := []byte(extractText(t, completeResult))
	var completeResponse issuesapi.Response
	if err := json.Unmarshal(completeRaw, &completeResponse); err != nil {
		t.Fatalf("decode complete-severity response: %v", err)
	}
	completeUnlinked := false
	for _, change := range completeResponse.RecentChanges {
		if change.NotLinkedToReturnedIssues {
			completeUnlinked = true
			break
		}
	}
	if !completeUnlinked {
		t.Fatalf("complete explicit severity set failed to evaluate linkage: %+v", completeResponse.RecentChanges)
	}
	if strings.Contains(completeResponse.RecentChangesGuidance, "severity-filtered") {
		t.Fatalf("complete explicit severity set got filtered guidance: %q", completeResponse.RecentChangesGuidance)
	}
}
