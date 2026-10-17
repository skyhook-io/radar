package meaningfulchanges

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestWebhookChangeApplicability(t *testing.T) {
	shop := &metav1.LabelSelector{MatchLabels: map[string]string{"team": "shop"}}
	other := &metav1.LabelSelector{MatchLabels: map[string]string{"team": "other"}}
	known := NamespaceLabels{Labels: labels.Set{"team": "shop"}, Known: true}
	current := map[string][]webhookScope{
		"MutatingWebhookConfiguration/open":      {{name: "open.example", namespaced: true}},
		"MutatingWebhookConfiguration/selected":  {{name: "selected.example", namespace: shop, namespaced: true}},
		"MutatingWebhookConfiguration/elsewhere": {{name: "elsewhere.example", namespace: other, namespaced: true}},
		"MutatingWebhookConfiguration/objects":   {{name: "objects.example", namespace: shop, objectSelector: true, namespaced: true}},
		"MutatingWebhookConfiguration/cel":       {{name: "cel.example", matchConditions: true, namespaced: true}},
		"MutatingWebhookConfiguration/cluster":   {{name: "cluster.example"}},
		"MutatingWebhookConfiguration/two":       {{name: "a.example", namespace: other, namespaced: true}, {name: "b.example", namespaced: true}},
	}
	update := func(name string, fields ...issuesapi.ChangeField) issuesapi.RecentChange {
		return issuesapi.RecentChange{Kind: "MutatingWebhookConfiguration", Name: name, ChangeType: "update", Fields: fields}
	}
	cases := []struct {
		name       string
		change     issuesapi.RecentChange
		labels     NamespaceLabels
		loaded     bool
		want       issuesapi.ClusterScopedApplicability
		becauseHas string
	}{
		{"no namespace selector", issuesapi.RecentChange{Kind: "MutatingWebhookConfiguration", Name: "open", ChangeType: "add"}, known, true, issuesapi.ClusterScopedApplies, "webhook open.example has no namespaceSelector"},
		{"selector matches", update("selected", issuesapi.ChangeField{Path: "webhooks[selected.example]", OldValue: "failurePolicy=Ignore rules=x", NewValue: "failurePolicy=Fail rules=x"}), known, true, issuesapi.ClusterScopedApplies, "namespaceSelector matching namespace shop's labels"},
		{"selector excludes", update("elsewhere"), known, true, "", ""},
		{"labels unreadable", update("selected", issuesapi.ChangeField{Path: "webhooks[selected.example]", OldValue: "failurePolicy=Ignore rules=x", NewValue: "failurePolicy=Fail rules=x"}), NamespaceLabels{}, true, issuesapi.ClusterScopedMayApply, "labels are not readable"},
		{"object selector unevaluated", update("objects", issuesapi.ChangeField{Path: "webhooks[objects.example]", OldValue: "failurePolicy=Ignore rules=x", NewValue: "failurePolicy=Fail rules=x"}), known, true, issuesapi.ClusterScopedMayApply, "objectSelector is not evaluated"},
		{"match conditions unevaluated", update("cel", issuesapi.ChangeField{Path: "webhooks[cel.example]", OldValue: "failurePolicy=Ignore rules=x", NewValue: "failurePolicy=Fail rules=x"}), known, true, issuesapi.ClusterScopedMayApply, "matchConditions are not evaluated"},
		{"cluster-scoped rules only", update("cluster", issuesapi.ChangeField{Path: "webhooks[cluster.example]", OldValue: "failurePolicy=Ignore rules=x", NewValue: "failurePolicy=Fail rules=x"}), known, true, "", ""},
		{"deleted", issuesapi.RecentChange{Kind: "MutatingWebhookConfiguration", Name: "gone", ChangeType: "delete"}, known, true, issuesapi.ClusterScopedMayApply, "deleted"},
		{"no longer exists", update("gone"), known, true, issuesapi.ClusterScopedMayApply, "no longer exists"},
		{"cache not loaded", update("elsewhere"), known, false, issuesapi.ClusterScopedMayApply, "not cached"},
		{"re-scoped away", update("elsewhere", issuesapi.ChangeField{Path: "webhooks[elsewhere.example]", OldValue: "failurePolicy=Fail namespaceSelector={team=shop} rules=x", NewValue: "failurePolicy=Fail namespaceSelector={team=other} rules=x"}), known, true, issuesapi.ClusterScopedMayApply, "re-scoped webhook elsewhere.example"},
		{"webhook removed", update("elsewhere", issuesapi.ChangeField{Path: "webhooks[dropped.example]", OldValue: "rules=x"}), known, true, issuesapi.ClusterScopedMayApply, "removed or re-scoped webhook dropped.example"},
		{"match conditions edited", update("elsewhere", issuesapi.ChangeField{Path: "webhooks[elsewhere.example].matchConditions[c0].expression", OldValue: "true", NewValue: "false"}), known, true, issuesapi.ClusterScopedMayApply, "re-scoped webhook elsewhere.example"},
		{"other field edited elsewhere", update("elsewhere", issuesapi.ChangeField{Path: "webhooks[elsewhere.example].admissionReviewVersions", OldValue: "v1", NewValue: "v1beta1"}), known, true, "", ""},
		{"CA rotation elsewhere", update("elsewhere", issuesapi.ChangeField{Path: "webhooks[elsewhere.example]", OldValue: "caBundle=sha256:aaa namespaceSelector={team=other} rules=x", NewValue: "caBundle=sha256:bbb namespaceSelector={team=other} rules=x"}), known, true, "", ""},
		{"only the changed webhook is judged", update("two", issuesapi.ChangeField{Path: "webhooks[a.example]", OldValue: "failurePolicy=Ignore rules=x", NewValue: "failurePolicy=Fail rules=x"}), known, true, "", ""},
		{"unitemized update only may apply", update("open"), known, true, issuesapi.ClusterScopedMayApply, "does not itemize per webhook; currently webhook open.example has no namespaceSelector"},
		{"applies but another webhook was removed", update("two", issuesapi.ChangeField{Path: "webhooks[b.example]", OldValue: "failurePolicy=Ignore rules=x", NewValue: "failurePolicy=Fail rules=x"}, issuesapi.ChangeField{Path: "webhooks[gone.example]", OldValue: "rules=x"}), known, true, issuesapi.ClusterScopedApplies, "also removed or re-scoped webhook gone.example"},
		{"changed webhook applies", update("two", issuesapi.ChangeField{Path: "webhooks[b.example]", OldValue: "failurePolicy=Ignore rules=x", NewValue: "failurePolicy=Fail rules=x"}), known, true, issuesapi.ClusterScopedApplies, "b.example"},
	}
	cut := update("two")
	for n := 0; n < clusterScopedFieldLimit; n++ {
		cut.Fields = append(cut.Fields, issuesapi.ChangeField{Path: fmt.Sprintf("webhooks[a.example].rules[%d]", n), OldValue: "x", NewValue: "y"})
	}
	cut.Fields[0] = issuesapi.ChangeField{Path: "webhooks[a.example]", OldValue: "rules=x", NewValue: "rules=x"}
	cases = append(cases, struct {
		name       string
		change     issuesapi.RecentChange
		labels     NamespaceLabels
		loaded     bool
		want       issuesapi.ClusterScopedApplicability
		becauseHas string
	}{"cut diff never borrows an unchanged webhook", cut, known, true, issuesapi.ClusterScopedMayApply, "re-scoped webhook a.example; its earlier scope is not recorded; the timeline diff lists only part"})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, because := webhookChangeApplicability(tc.change, "shop", tc.labels, current, tc.loaded)
			if got != tc.want || !strings.Contains(because, tc.becauseHas) {
				t.Fatalf("applicability=%q because=%q want %q containing %q", got, because, tc.want, tc.becauseHas)
			}
		})
	}
}

func TestClusterScopedForNamespaceBudgetsAndAuthorizesBeforeCapping(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	defer k8s.ResetTestDynamicState()
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 100}); err != nil {
		t.Fatal(err)
	}
	mwcGVR := schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "mutatingwebhookconfigurations"}
	vwcGVR := schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingwebhookconfigurations"}
	podRule := []admissionv1.RuleWithOperations{{Operations: []admissionv1.OperationType{admissionv1.Create}, Rule: admissionv1.Rule{APIGroups: []string{""}, APIVersions: []string{"v1"}, Resources: []string{"pods"}}}}
	var objects []runtime.Object
	add := func(obj runtime.Object) {
		data, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
		if err != nil {
			t.Fatal(err)
		}
		objects = append(objects, &unstructured.Unstructured{Object: data})
	}
	store := timeline.GetStore()
	appendEvent := func(kind, name string, at time.Time) {
		if err := store.Append(context.Background(), timeline.TimelineEvent{ID: kind + name, Timestamp: at, Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
			Kind: kind, APIVersion: "admissionregistration.k8s.io/v1", Name: name, EventType: timeline.EventTypeAdd}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	for n := 0; n < 12; n++ {
		name := fmt.Sprintf("policy-%02d", n)
		add(&admissionv1.MutatingWebhookConfiguration{TypeMeta: metav1.TypeMeta{Kind: "MutatingWebhookConfiguration", APIVersion: "admissionregistration.k8s.io/v1"}, ObjectMeta: metav1.ObjectMeta{Name: name}, Webhooks: []admissionv1.MutatingWebhook{{Name: name + ".example", Rules: podRule}}})
		appendEvent("MutatingWebhookConfiguration", name, now.Add(-time.Duration(n)*time.Minute))
	}
	add(&admissionv1.ValidatingWebhookConfiguration{TypeMeta: metav1.TypeMeta{Kind: "ValidatingWebhookConfiguration", APIVersion: "admissionregistration.k8s.io/v1"}, ObjectMeta: metav1.ObjectMeta{Name: "elsewhere"},
		Webhooks: []admissionv1.ValidatingWebhook{{Name: "elsewhere.example", Rules: podRule, NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "other"}}}}})
	appendEvent("ValidatingWebhookConfiguration", "elsewhere", now)
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{mwcGVR: "MutatingWebhookConfigurationList", vwcGVR: "ValidatingWebhookConfigurationList"}, objects...)
	if err := k8s.InitTestDynamicResourceCache(client, []k8s.APIResource{
		{Group: mwcGVR.Group, Version: "v1", Kind: "MutatingWebhookConfiguration", Name: mwcGVR.Resource, Verbs: []string{"list", "watch"}},
		{Group: vwcGVR.Group, Version: "v1", Kind: "ValidatingWebhookConfiguration", Name: vwcGVR.Resource, Verbs: []string{"list", "watch"}},
	}); err != nil {
		t.Fatal(err)
	}
	cache := k8s.GetDynamicResourceCache()
	for _, gvr := range []schema.GroupVersionResource{mwcGVR, vwcGVR} {
		if err := cache.EnsureWatching(gvr); err != nil || !cache.WaitForSync(gvr, 2*time.Second) {
			t.Fatalf("watch %s: %v", gvr, err)
		}
	}
	timeline.SetObservationStartForTest(now.Add(-2 * time.Hour))
	shop := NamespaceLabels{Labels: labels.Set{"team": "shop"}, Known: true}
	got := ClusterScopedForNamespace(context.Background(), "shop", shop, time.Hour, nil)
	if got.Error != "" || got.Incomplete != "" || len(got.Changes) != 5 || len(got.NotListed) != 5 || !got.Truncated {
		t.Fatalf("section=%+v", got)
	}
	if got.Changes[0].Name != "policy-00" || got.Changes[0].Applicability != issuesapi.ClusterScopedApplies || !strings.Contains(got.NotListed[4], "MutatingWebhookConfiguration policy-09") {
		t.Fatalf("rows=%+v notListed=%v", got.Changes, got.NotListed)
	}
	for _, c := range got.Changes {
		if c.Name == "elsewhere" {
			t.Fatalf("listed a webhook scoped to another namespace: %+v", c)
		}
	}
	if !got.Informative() {
		t.Fatal("rows must make the section informative")
	}

	// A kind the caller cannot read is said to be unread, never queried.
	denied := ClusterScopedForNamespace(context.Background(), "shop", shop, time.Hour, func(kind string) bool { return kind != "MutatingWebhookConfiguration" })
	if len(denied.Changes) != 0 || denied.Incomplete != "MutatingWebhookConfiguration changes are not readable by the caller." || !denied.Informative() {
		t.Fatalf("denied section=%+v", denied)
	}

	// A window longer than Radar has been watching is not a complete negative.
	timeline.SetObservationStartForTest(now.Add(-10 * time.Minute))
	fresh := ClusterScopedForNamespace(context.Background(), "shop", shop, time.Hour, nil)
	if !strings.Contains(fresh.Incomplete, "Radar has recorded changes only since") {
		t.Fatalf("observation gap not disclosed: %+v", fresh)
	}
}

// The row budget applies after applicability: many newer changes that do not
// concern the namespace must not push an applicable one out.
func TestClusterScopedForNamespaceJudgesEveryCandidateBeforeTheBudget(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	defer k8s.ResetTestDynamicState()
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 500}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	timeline.SetObservationStartForTest(now.Add(-2 * time.Hour))
	store := timeline.GetStore()
	podRule := []admissionv1.RuleWithOperations{{Operations: []admissionv1.OperationType{admissionv1.Create}, Rule: admissionv1.Rule{APIGroups: []string{""}, APIVersions: []string{"v1"}, Resources: []string{"pods"}}}}
	var objects []runtime.Object
	addConfig := func(name string, selector *metav1.LabelSelector) {
		data, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&admissionv1.MutatingWebhookConfiguration{TypeMeta: metav1.TypeMeta{Kind: "MutatingWebhookConfiguration", APIVersion: "admissionregistration.k8s.io/v1"}, ObjectMeta: metav1.ObjectMeta{Name: name}, Webhooks: []admissionv1.MutatingWebhook{{Name: name + ".example", Rules: podRule, NamespaceSelector: selector}}})
		if err != nil {
			t.Fatal(err)
		}
		objects = append(objects, &unstructured.Unstructured{Object: data})
	}
	addConfig("elsewhere", &metav1.LabelSelector{MatchLabels: map[string]string{"team": "other"}})
	addConfig("target", nil)
	for n := 0; n < 60; n++ {
		if err := store.Append(context.Background(), timeline.TimelineEvent{ID: fmt.Sprintf("noise-%d", n), Timestamp: now.Add(-time.Duration(n) * time.Second), Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
			Kind: "MutatingWebhookConfiguration", APIVersion: "admissionregistration.k8s.io/v1", Name: "elsewhere", EventType: timeline.EventTypeUpdate,
			Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "webhooks[elsewhere.example]", OldValue: fmt.Sprintf("caBundle=sha256:%03d rules=x", n), NewValue: fmt.Sprintf("caBundle=sha256:%03d rules=x", n+1)}}}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Append(context.Background(), timeline.TimelineEvent{ID: "target", Timestamp: now.Add(-30 * time.Minute), Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
		Kind: "MutatingWebhookConfiguration", APIVersion: "admissionregistration.k8s.io/v1", Name: "target", EventType: timeline.EventTypeAdd}); err != nil {
		t.Fatal(err)
	}
	gvr := schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "mutatingwebhookconfigurations"}
	vgvr := schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingwebhookconfigurations"}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "MutatingWebhookConfigurationList", vgvr: "ValidatingWebhookConfigurationList"}, objects...)
	if err := k8s.InitTestDynamicResourceCache(client, []k8s.APIResource{
		{Group: gvr.Group, Version: "v1", Kind: "MutatingWebhookConfiguration", Name: gvr.Resource, Verbs: []string{"list", "watch"}},
		{Group: vgvr.Group, Version: "v1", Kind: "ValidatingWebhookConfiguration", Name: vgvr.Resource, Verbs: []string{"list", "watch"}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, r := range []schema.GroupVersionResource{gvr, vgvr} {
		if err := k8s.GetDynamicResourceCache().EnsureWatching(r); err != nil || !k8s.GetDynamicResourceCache().WaitForSync(r, 2*time.Second) {
			t.Fatalf("watch %s: %v", r, err)
		}
	}
	got := ClusterScopedForNamespace(context.Background(), "shop", NamespaceLabels{Labels: labels.Set{"team": "shop"}, Known: true}, time.Hour, nil)
	if len(got.Changes) != 1 || got.Changes[0].Name != "target" || got.Changes[0].Applicability != issuesapi.ClusterScopedApplies {
		t.Fatalf("applicable change pushed out by irrelevant ones: %+v", got)
	}
}

func TestRuleCoversNamespaced(t *testing.T) {
	defer k8s.ResetTestDynamicState()
	if err := k8s.InitTestDynamicResourceCache(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), []k8s.APIResource{
		{Group: "", Version: "v1", Kind: "Node", Name: "nodes", Namespaced: false, Verbs: []string{"list"}},
		{Group: "", Version: "v1", Kind: "Pod", Name: "pods", Namespaced: true, Verbs: []string{"list"}},
	}); err != nil {
		t.Fatal(err)
	}
	scope := func(s admissionv1.ScopeType) *admissionv1.ScopeType { return &s }
	rule := func(s *admissionv1.ScopeType, groups []string, resources ...string) admissionv1.RuleWithOperations {
		return admissionv1.RuleWithOperations{Rule: admissionv1.Rule{APIGroups: groups, Resources: resources, Scope: s}}
	}
	for _, tc := range []struct {
		name string
		rule admissionv1.RuleWithOperations
		want bool
	}{
		{"nodes with omitted scope", rule(nil, []string{""}, "nodes"), false},
		{"node status subresource", rule(nil, []string{""}, "nodes/status"), false},
		{"pods with omitted scope", rule(nil, []string{""}, "pods"), true},
		{"explicit namespaced", rule(scope(admissionv1.NamespacedScope), []string{""}, "nodes"), true},
		{"explicit cluster", rule(scope(admissionv1.ClusterScope), []string{""}, "pods"), false},
		{"wildcard resources", rule(scope(admissionv1.AllScopes), []string{""}, "*"), true},
		{"unknown resource", rule(nil, []string{"example.test"}, "widgets"), true},
	} {
		if got := ruleCoversNamespaced(tc.rule); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestClusterScopedForNamespaceDisclosesEvictedHistoryAndUncachedKinds(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	defer k8s.ResetTestDynamicState()
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 5}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	timeline.SetObservationStartForTest(now.Add(-2 * time.Hour))
	for n := 0; n < 10; n++ {
		if err := timeline.GetStore().Append(context.Background(), timeline.TimelineEvent{ID: fmt.Sprintf("e%d", n), Timestamp: now.Add(-time.Duration(10-n) * time.Second), Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
			Kind: "ConfigMap", Namespace: "other", Name: fmt.Sprintf("cm-%d", n), EventType: timeline.EventTypeAdd}); err != nil {
			t.Fatal(err)
		}
	}
	// Only MutatingWebhookConfigurations are cached.
	gvr := schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "mutatingwebhookconfigurations"}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "MutatingWebhookConfigurationList"})
	if err := k8s.InitTestDynamicResourceCache(client, []k8s.APIResource{{Group: gvr.Group, Version: "v1", Kind: "MutatingWebhookConfiguration", Name: gvr.Resource, Verbs: []string{"list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	if err := k8s.GetDynamicResourceCache().EnsureWatching(gvr); err != nil || !k8s.GetDynamicResourceCache().WaitForSync(gvr, 2*time.Second) {
		t.Fatalf("watch: %v", err)
	}
	got := ClusterScopedForNamespace(context.Background(), "shop", NamespaceLabels{Known: true}, time.Hour, nil)
	if !strings.Contains(got.Incomplete, "older events were evicted") {
		t.Fatalf("evicted history not disclosed: %+v", got)
	}
	if !strings.Contains(got.Incomplete, "Current ValidatingWebhookConfigurations are not cached") || strings.Contains(got.Incomplete, "Current MutatingWebhookConfigurations") {
		t.Fatalf("per-kind cache coverage: %+v", got)
	}
}
