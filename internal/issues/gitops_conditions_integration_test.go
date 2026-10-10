package issues

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/gitops/insights"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestGitOpsConditionsFixtureThroughIssues(t *testing.T) {
	defer k8s.ResetTestDynamicState()
	data, err := os.ReadFile("testdata/argo-blocked-autosync.json")
	if err != nil {
		t.Fatal(err)
	}
	app := &unstructured.Unstructured{}
	if err := json.Unmarshal(data, &app.Object); err != nil {
		t.Fatal(err)
	}
	original := app.DeepCopy()
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "ApplicationList"}, app)
	if err := k8s.InitTestDynamicResourceCache(client, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: "Application", Namespaced: true, Verbs: []string{"list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	cache := k8s.GetDynamicResourceCache()
	if err := cache.EnsureWatching(gvr); err != nil {
		t.Fatal(err)
	}
	if !cache.WaitForSync(gvr, 5*time.Second) {
		t.Fatal("Application informer did not sync")
	}
	provider := &CacheProvider{dynamic: cache, discovery: k8s.GetResourceDiscovery()}
	problems := provider.DetectGitOpsProblems([]string{app.GetNamespace()})
	p := &fakeProvider{
		gitopsProblems: problems,
		dynamic:        map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {app}},
		kinds:          map[schema.GroupVersionResource]string{gvr: "Application"},
		namespaced:     map[schema.GroupVersionResource]bool{gvr: true},
	}
	initialIDs := map[bool]map[string]string{}
	for _, grouped := range []bool{false, true} {
		initialIDs[grouped] = map[string]string{}
		rows := Compose(p, Filters{Limit: NoLimit, Grouped: grouped})
		if len(rows) != 4 {
			t.Fatalf("grouped=%v: want 4 independent issues, got %+v", grouped, rows)
		}
		seen := map[string]bool{}
		for _, row := range rows {
			if seen[row.Reason] {
				t.Fatalf("duplicate condition row: %+v", row)
			}
			seen[row.Reason] = true
			initialIDs[grouped][row.Reason] = row.ID
			if row.Group != gvr.Group || row.Kind != "Application" || row.Name != app.GetName() || row.Namespace != app.GetNamespace() {
				t.Fatalf("lost Application identity: %+v", row)
			}
			switch row.Reason {
			case "AutoSyncBlockedEmpty":
				if row.Category != issuesapi.CategoryGitOpsSyncFailed || row.Severity != SeverityCritical || !strings.Contains(row.Action, "confirm") || row.RemediationKind != "" {
					t.Fatalf("lost safe blocked-auto-sync diagnosis: %+v", row)
				}
			case "SharedResourceWarning", "RepeatedResourceWarning", "OrphanedResourceWarning":
				if row.Category != issuesapi.CategoryGitOpsResourceWarning || row.Severity != SeverityWarning || row.Action == "" {
					t.Fatalf("lost warning: %+v", row)
				}
			default:
				t.Fatalf("unexpected issue: %+v", row)
			}
			t.Logf("grouped=%v: %s %s: %s | %s", grouped, row.Severity, row.Reason, row.Message, row.Action)
		}
	}
	detail := insights.Build(app, nil, nil)
	if len(detail.Issues) != 4 {
		t.Fatalf("detail must retain one condition row each, got %+v", detail.Issues)
	}
	current, err := client.Resource(gvr).Namespace(app.GetNamespace()).Get(t.Context(), app.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(current.Object, original.Object) {
		t.Fatal("diagnosis must not mutate the Application")
	}
	updated := app.DeepCopy()
	conditions, _, _ := unstructured.NestedSlice(updated.Object, "status", "conditions")
	conditions[3].(map[string]any)["message"] = "Application has 42 orphaned resources"
	conditions = append(conditions, map[string]any{"type": "SharedResourceWarning", "message": "Service/api is part of applications empty-addons and another-owner", "lastTransitionTime": "2026-10-10T00:01:00Z"})
	_ = unstructured.SetNestedSlice(updated.Object, conditions, "status", "conditions")
	if _, err := client.Resource(gvr).Namespace(app.GetNamespace()).Update(t.Context(), updated, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		p.gitopsProblems = provider.DetectGitOpsProblems([]string{app.GetNamespace()})
		var observed bool
		for _, d := range p.gitopsProblems {
			if d.Reason == "OrphanedResourceWarning" && strings.Contains(d.Message, "42 orphaned") {
				observed = true
			}
		}
		if observed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("informer did not observe changed warning count")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, grouped := range []bool{false, true} {
		rows := Compose(p, Filters{Limit: NoLimit, Grouped: grouped})
		if len(rows) != 4 {
			t.Fatalf("grouped=%v: warning explosion: %+v", grouped, rows)
		}
		for _, row := range rows {
			if initialIDs[grouped][row.Reason] != row.ID {
				t.Fatalf("grouped=%v: episode ID changed for %s: %s -> %s", grouped, row.Reason, initialIDs[grouped][row.Reason], row.ID)
			}
			if row.Reason == "SharedResourceWarning" && (!strings.Contains(row.Message, "2 resources") || !strings.Contains(row.Message, "another-owner") || row.FirstSeen.Format(time.RFC3339) != "2026-10-09T23:58:00Z") {
				t.Fatalf("aggregate lost evidence: %+v", row)
			}
		}
	}
}
