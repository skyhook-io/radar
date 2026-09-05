package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
)

func TestHandleDiagnoseReturnsSemanticCompleteness(t *testing.T) {
	setupFakeCacheForDiagnoseTests(t)
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	ctx := withClusterAdmin(t, "admin")
	perms := getPermCache().Get("admin", nil)
	perms.SetCanI("list", "apps", "deployments", "alpha", true)
	perms.SetCanI("list", "", "configmaps", "alpha", true)

	result, _, err := handleDiagnose(
		ctx,
		nil,
		testDiagnoseInput("deployment", "alpha", "cart"),
	)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal([]byte(extractText(t, result)), &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["logCoverage"]; !ok {
		t.Fatal("diagnose omitted semantic logCoverage")
	}
	if _, ok := got["recentChangesError"]; !ok {
		t.Fatal("diagnose omitted the recent-change collection error")
	}
}

func TestExpectedPreviousLogAbsenceUsesCapturedRestartState(t *testing.T) {
	terminated := corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
		Reason:   "Error",
		ExitCode: 1,
	}}
	tests := []struct {
		name      string
		pod       *corev1.Pod
		container string
		want      bool
	}{
		{
			name: "main container has never restarted",
			pod: &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
				Name: "api",
			}}}},
			container: "api",
			want:      true,
		},
		{
			name: "current terminated instance is not a previous instance",
			pod: &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
				Name:  "api",
				State: terminated,
			}}}},
			container: "api",
			want:      true,
		},
		{
			name: "restart count proves a prior instance",
			pod: &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
				Name:         "api",
				RestartCount: 1,
			}}}},
			container: "api",
		},
		{
			name: "last termination proves a prior instance",
			pod: &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
				Name:                 "api",
				LastTerminationState: terminated,
			}}}},
			container: "api",
		},
		{
			name: "init container has never restarted",
			pod: &corev1.Pod{Status: corev1.PodStatus{InitContainerStatuses: []corev1.ContainerStatus{{
				Name: "migrate",
			}}}},
			container: "migrate",
			want:      true,
		},
		{name: "missing status is unknown", pod: &corev1.Pod{}, container: "api"},
		{name: "nil pod is unknown", container: "api"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := expectedPreviousLogAbsence(test.pod, test.container); got != test.want {
				t.Fatalf("expectedPreviousLogAbsence() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestExpectedPreviousLogAbsencesForDiagnoseUsesStatusAndRetainedContent(t *testing.T) {
	entries := []podLogEntry{
		{
			Pod:                     "api-0",
			Container:               "api",
			Error:                   "arbitrary apiserver wording",
			expectedPreviousAbsence: true,
		},
		{
			Pod:                     "api-1",
			Container:               "api",
			Logs:                    aicontext.FilterLogs(""),
			expectedPreviousAbsence: true,
		},
		{
			Pod:       "api-2",
			Container: "api",
			Error:     `failed to get logs: previous terminated container "api" not found`,
		},
		{
			Pod:                     "api-3",
			Container:               "api",
			RawLines:                1,
			Logs:                    aicontext.FilterLogs("unexpected prior output"),
			expectedPreviousAbsence: true,
		},
	}

	want := []diagnosePodContainerRef{
		{Pod: "api-0", Container: "api"},
		{Pod: "api-1", Container: "api"},
	}
	if got := expectedPreviousLogAbsencesForDiagnose(entries); !reflect.DeepEqual(got, want) {
		t.Fatalf("expectedPreviousLogAbsencesForDiagnose() = %+v, want %+v", got, want)
	}
}

func TestHandleDiagnoseReportsRecentChangesLimitReached(t *testing.T) {
	for _, eventCount := range []int{4, 100} {
		t.Run(fmt.Sprintf("%d_events", eventCount), func(t *testing.T) {
			setupFakeCacheForDiagnoseTests(t)
			timelineStore := initCorrelationStore(t)
			ctx := withClusterAdmin(t, "admin")
			perms := getPermCache().Get("admin", nil)
			perms.SetCanI("list", "apps", "deployments", "alpha", true)
			perms.SetCanI("list", "", "configmaps", "alpha", true)

			for i := 0; i < eventCount; i++ {
				if err := timelineStore.Append(context.Background(), timeline.TimelineEvent{
					ID:             fmt.Sprintf("cart-spec-%03d", i),
					Timestamp:      time.Now().Add(-time.Duration(i+1) * time.Second),
					Source:         timeline.SourceInformer,
					ClusterContext: k8s.ActiveClusterContext(),
					APIVersion:     "apps/v1",
					Kind:           "Deployment",
					Namespace:      "alpha",
					Name:           "cart",
					EventType:      timeline.EventTypeUpdate,
					Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{
						Path:     "spec.template.spec.containers[cart].image",
						OldValue: fmt.Sprintf("cart:%d", i),
						NewValue: fmt.Sprintf("cart:%d", i+1),
					}}},
				}); err != nil {
					t.Fatalf("append timeline event %d: %v", i, err)
				}
			}

			result, _, err := handleDiagnose(ctx, nil, testDiagnoseInput("deployment", "alpha", "cart"))
			if err != nil {
				t.Fatalf("handleDiagnose: %v", err)
			}
			var response struct {
				RecentChanges          []json.RawMessage `json:"recentChanges"`
				RecentChangesSaturated bool              `json:"recentChangesSaturated"`
				RecentChangesError     string            `json:"recentChangesError"`
			}
			if err := json.Unmarshal([]byte(extractText(t, result)), &response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if len(response.RecentChanges) != 3 {
				t.Fatalf("recentChanges = %d, want capped 3", len(response.RecentChanges))
			}
			if !response.RecentChangesSaturated {
				t.Fatal("recentChangesSaturated = false, want diagnose to report a source or output limit")
			}
			if response.RecentChangesError != "" {
				t.Fatalf("recentChangesError = %q, want no collection error", response.RecentChangesError)
			}
		})
	}
}

func TestHandleGetResourceReportsRecentChangesCoverage(t *testing.T) {
	tests := []struct {
		name                string
		eventCount          int
		canList             bool
		wantChanges         int
		wantChangesPresent  bool
		wantSaturated       bool
		wantCoverageLimited bool
	}{
		{name: "complete", eventCount: 2, canList: true, wantChanges: 2, wantChangesPresent: true},
		{name: "output capped", eventCount: 4, canList: true, wantChanges: 3, wantChangesPresent: true, wantSaturated: true},
		{name: "fetch saturated", eventCount: 100, canList: true, wantChanges: 3, wantChangesPresent: true, wantSaturated: true},
		{name: "unreadable source without history", wantCoverageLimited: true},
		{name: "unreadable source with history", eventCount: 100, wantCoverageLimited: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setupFakeCacheForDiagnoseTests(t)
			timelineStore := initCorrelationStore(t)
			username := "get-resource-changes-" + strings.ReplaceAll(test.name, " ", "-")
			ctx := withClusterAdmin(t, username)
			getPermCache().Get(username, nil).SetCanI("list", "apps", "deployments", "alpha", test.canList)

			for i := 0; i < test.eventCount; i++ {
				if err := timelineStore.Append(context.Background(), timeline.TimelineEvent{
					ID:             fmt.Sprintf("cart-get-resource-%03d", i),
					Timestamp:      time.Now().Add(-time.Duration(i+1) * time.Second),
					Source:         timeline.SourceInformer,
					ClusterContext: k8s.ActiveClusterContext(),
					APIVersion:     "apps/v1",
					Kind:           "Deployment",
					Namespace:      "alpha",
					Name:           "cart",
					EventType:      timeline.EventTypeUpdate,
					Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{
						Path:     "spec.template.spec.containers[cart].image",
						OldValue: fmt.Sprintf("cart:%d", i),
						NewValue: fmt.Sprintf("cart:%d", i+1),
					}}},
				}); err != nil {
					t.Fatalf("append timeline event %d: %v", i, err)
				}
			}

			result, _, err := handleGetResource(ctx, nil, getResourceInput{
				Kind: "deployment", Namespace: "alpha", Name: "cart",
				Include: "changes", Context: "none",
			})
			if err != nil {
				t.Fatalf("handleGetResource: %v", err)
			}
			responseText := extractText(t, result)
			var response struct {
				RecentChanges                []json.RawMessage `json:"recentChanges"`
				RecentChangesSaturated       *bool             `json:"recentChangesSaturated"`
				RecentChangesCoverageLimited *bool             `json:"recentChangesCoverageLimited"`
			}
			if err := json.Unmarshal([]byte(responseText), &response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(responseText), &fields); err != nil {
				t.Fatalf("decode response fields: %v", err)
			}
			if response.RecentChangesSaturated == nil {
				t.Fatal("recentChangesSaturated is absent")
			}
			if *response.RecentChangesSaturated != test.wantSaturated {
				t.Fatalf("recentChangesSaturated = %v, want %v", *response.RecentChangesSaturated, test.wantSaturated)
			}
			if response.RecentChangesCoverageLimited == nil {
				t.Fatal("recentChangesCoverageLimited is absent")
			}
			if *response.RecentChangesCoverageLimited != test.wantCoverageLimited {
				t.Fatalf("recentChangesCoverageLimited = %v, want %v", *response.RecentChangesCoverageLimited, test.wantCoverageLimited)
			}
			if len(response.RecentChanges) != test.wantChanges {
				t.Fatalf("recentChanges = %d, want %d", len(response.RecentChanges), test.wantChanges)
			}
			_, changesPresent := fields["recentChanges"]
			if changesPresent != test.wantChangesPresent {
				t.Fatalf("recentChanges present = %v, want %v", changesPresent, test.wantChangesPresent)
			}
		})
	}
}

func TestRecentChangesSourceTrackedRequiresExactFeedIdentity(t *testing.T) {
	tests := []struct {
		name string
		gvk  schema.GroupVersionKind
		want bool
	}{
		{name: "tracked deployment", gvk: schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, want: true},
		{name: "tracked core service", gvk: schema.GroupVersionKind{Version: "v1", Kind: "Service"}, want: true},
		{name: "same kind in another group", gvk: schema.GroupVersionKind{Group: "serving.knative.dev", Version: "v1", Kind: "Service"}},
		{name: "untracked kind", gvk: schema.GroupVersionKind{Version: "v1", Kind: "Pod"}},
		{name: "missing type metadata", gvk: schema.GroupVersionKind{Kind: "Deployment"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := recentChangesSourceTracked(test.gvk); got != test.want {
				t.Fatalf("recentChangesSourceTracked(%s) = %v, want %v", test.gvk, got, test.want)
			}
		})
	}
}

func TestHandleGetResourceDoesNotCallUntrackedChangeFeedComplete(t *testing.T) {
	setupFakeCacheForDiagnoseTests(t)
	timelineStore := initCorrelationStore(t)
	ctx := withClusterAdmin(t, "get-resource-untracked-changes")

	if err := timelineStore.Append(context.Background(), timeline.TimelineEvent{
		ID:             "untracked-pod-update",
		Timestamp:      time.Now().Add(-time.Minute),
		Source:         timeline.SourceInformer,
		ClusterContext: k8s.ActiveClusterContext(),
		APIVersion:     "v1",
		Kind:           "Pod",
		Namespace:      "alpha",
		Name:           "cart-abc123",
		EventType:      timeline.EventTypeUpdate,
		Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{
			Path: "status.phase", OldValue: "Pending", NewValue: "Running",
		}}},
	}); err != nil {
		t.Fatalf("append pod history: %v", err)
	}

	result, _, err := handleGetResource(ctx, nil, getResourceInput{
		Kind: "pod", Namespace: "alpha", Name: "cart-abc123",
		Include: "changes", Context: "none",
	})
	if err != nil {
		t.Fatalf("handleGetResource: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(extractText(t, result)), &fields); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, present := fields["recentChanges"]; present {
		t.Fatal("untracked source must not expose rows or an authoritative empty array")
	}
	if got := string(fields["recentChangesSaturated"]); got != "false" {
		t.Fatalf("recentChangesSaturated = %s, want false", got)
	}
	if got := string(fields["recentChangesCoverageLimited"]); got != "true" {
		t.Fatalf("recentChangesCoverageLimited = %s, want true", got)
	}
}

func TestHandleDiagnoseReportsRBACLimitedRecentChangesWithoutRowCounts(t *testing.T) {
	setupFakeCacheForDiagnoseTests(t)
	timelineStore := initCorrelationStore(t)
	ctx := withClusterAdmin(t, "diagnose-changes-scoped")
	perms := getPermCache().Get("diagnose-changes-scoped", nil)
	perms.SetCanI("list", "apps", "deployments", "alpha", true)
	perms.SetCanI("list", "", "configmaps", "alpha", false)

	events := []timeline.TimelineEvent{
		{
			ID:             "cart-image-change",
			Timestamp:      time.Now().Add(-2 * time.Minute),
			Source:         timeline.SourceInformer,
			ClusterContext: k8s.ActiveClusterContext(),
			APIVersion:     "apps/v1",
			Kind:           "Deployment",
			Namespace:      "alpha",
			Name:           "cart",
			EventType:      timeline.EventTypeUpdate,
			Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{
				Path: "spec.template.spec.containers[cart].image", OldValue: "cart:v1", NewValue: "cart:v2",
			}}},
		},
		{
			ID:             "cart-config-change",
			Timestamp:      time.Now().Add(-time.Minute),
			Source:         timeline.SourceInformer,
			ClusterContext: k8s.ActiveClusterContext(),
			APIVersion:     "v1",
			Kind:           "ConfigMap",
			Namespace:      "alpha",
			Name:           "cart-config",
			EventType:      timeline.EventTypeUpdate,
			Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{
				Path: "data.mode", OldValue: "staging", NewValue: "production",
			}}},
		},
	}
	for _, event := range events {
		if err := timelineStore.Append(context.Background(), event); err != nil {
			t.Fatalf("append %s: %v", event.ID, err)
		}
	}

	result, _, err := handleDiagnose(ctx, nil, testDiagnoseInput("deployment", "alpha", "cart"))
	if err != nil {
		t.Fatalf("handleDiagnose: %v", err)
	}
	var response struct {
		RecentChanges []struct {
			Kind string `json:"kind"`
		} `json:"recentChanges"`
		RecentChangesCoverageLimited bool `json:"recentChangesCoverageLimited"`
	}
	if err := json.Unmarshal([]byte(extractText(t, result)), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.RecentChangesCoverageLimited {
		t.Fatal("recentChangesCoverageLimited = false, want true")
	}
	if len(response.RecentChanges) != 1 || response.RecentChanges[0].Kind != "Deployment" {
		t.Fatalf("visible recentChanges = %+v, want only the readable Deployment", response.RecentChanges)
	}
	if bytes.Contains([]byte(extractText(t, result)), []byte("recentChangesWithheld")) {
		t.Fatal("diagnose exposed an RBAC-hidden row count")
	}
}

func TestHandleDiagnoseMarksUnreadableEmptyChangeSourceAsLimited(t *testing.T) {
	setupFakeCacheForDiagnoseTests(t)
	initCorrelationStore(t)
	ctx := withClusterAdmin(t, "diagnose-empty-changes-scoped")
	perms := getPermCache().Get("diagnose-empty-changes-scoped", nil)
	perms.SetCanI("list", "apps", "deployments", "alpha", true)
	perms.SetCanI("list", "", "configmaps", "alpha", false)

	result, _, err := handleDiagnose(ctx, nil, testDiagnoseInput("deployment", "alpha", "cart"))
	if err != nil {
		t.Fatalf("handleDiagnose: %v", err)
	}
	var response struct {
		RecentChanges                []json.RawMessage `json:"recentChanges"`
		RecentChangesCoverageLimited bool              `json:"recentChangesCoverageLimited"`
	}
	if err := json.Unmarshal([]byte(extractText(t, result)), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.RecentChangesCoverageLimited {
		t.Fatal("an unreadable referenced ConfigMap must make recent-change coverage limited even with no rows")
	}
	if len(response.RecentChanges) != 0 {
		t.Fatalf("recentChanges = %+v, want no visible rows", response.RecentChanges)
	}
}

func TestDiagnoseResponseMarshalsCompletenessMetadata(t *testing.T) {
	response := diagnoseResponse{
		Resource:                     map[string]any{"kind": "Deployment"},
		Pods:                         1,
		EventsTotalGroups:            12,
		LogCoverage:                  &diagnoseLogCoverage{ResolvedPods: 1, SelectedPods: 1},
		ExpectedPreviousLogAbsences:  []diagnosePodContainerRef{{Pod: "api-0", Container: "api"}},
		RecentChangesCoverageLimited: true,
		RecentChangesSaturated:       true,
		RecentChangesError:           "change source unavailable",
		LogsPrevious: []podLogEntry{{
			Pod:                     "api-0",
			Container:               "api",
			expectedPreviousAbsence: true,
		}},
	}
	payload, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte("expectedPreviousAbsence")) {
		t.Fatalf("diagnose response leaked private absence state: %s", payload)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"eventsTotalGroups",
		"recentChangesCoverageLimited",
		"recentChangesSaturated",
		"recentChangesError",
		"logCoverage",
		"expectedPreviousLogAbsences",
	} {
		if _, ok := decoded[field]; !ok {
			t.Errorf("diagnose response omitted semantic completeness field %q", field)
		}
	}
}
