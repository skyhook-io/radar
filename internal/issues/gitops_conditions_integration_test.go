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
	for _, grouped := range []bool{false, true} {
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
			if row.Group != gvr.Group || row.Kind != "Application" || row.Name != app.GetName() || row.Namespace != app.GetNamespace() {
				t.Fatalf("lost Application identity: %+v", row)
			}
			switch row.Reason {
			case "AutoSyncBlockedEmpty":
				if row.Category != issuesapi.CategoryGitOpsOperationFailed || row.Severity != SeverityCritical || !strings.Contains(row.Action, "confirm") || row.RemediationKind != "" {
					t.Fatalf("lost safe blocked-auto-sync diagnosis: %+v", row)
				}
			case "SharedResourceWarning", "RepeatedResourceWarning", "OrphanedResourceWarning":
				if row.Severity != SeverityWarning || row.Action == "" {
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
}
