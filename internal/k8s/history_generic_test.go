package k8s

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/pkg/k8score"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestDiffGenericUnstructured_DesiredStateLeaves(t *testing.T) {
	old := collisionObject("pingcap.com/v1alpha1", "TidbCluster", map[string]any{
		"spec": map[string]any{"tidb": map[string]any{
			"statefulSetUpdateStrategy": "RollingUpdate", "replicas": int64(1),
			"password": "old-secret", "passwordType": "old-secret-type",
		}},
		"routing": map[string]any{"mode": "Internal"},
	})
	old.SetGeneration(1)
	new := old.DeepCopy()
	new.SetGeneration(2)
	new.Object["spec"] = map[string]any{"tidb": map[string]any{
		"statefulSetUpdateStrategy": "SomeStrategyForUpdate", "replicas": int64(2),
		"password": "new-secret", "passwordType": "new-secret-type",
	}}
	new.Object["routing"] = map[string]any{"mode": "External"}
	changes, summary := diffGenericUnstructured(old, new)
	want := []FieldChange{
		{Path: "routing.mode", OldValue: "Internal", NewValue: "External"},
		{Path: "spec.tidb.password", OldValue: hiddenValueMarker, NewValue: hiddenValueMarker},
		{Path: "spec.tidb.passwordType", OldValue: hiddenValueMarker, NewValue: hiddenValueMarker},
		{Path: "spec.tidb.replicas", OldValue: int64(1), NewValue: int64(2)},
		{Path: "spec.tidb.statefulSetUpdateStrategy", OldValue: "RollingUpdate", NewValue: "SomeStrategyForUpdate"},
		{Path: "metadata.generation", OldValue: int64(1), NewValue: int64(2)},
	}
	if !reflect.DeepEqual(changes, want) {
		t.Fatalf("changes = %+v, want %+v", changes, want)
	}
	if got := strings.Join(summary, ", "); got != "desired state changes: routing.mode, spec.tidb.password, spec.tidb.passwordType (+2 more)" {
		t.Fatalf("summary = %q", got)
	}
	if diff, record := timelineUpdateDiff("TidbCluster", old, new, nil, false); !record || diff == nil || !reflect.DeepEqual(diff.Fields, changes) {
		t.Fatalf("recording lost desired-state fields: record=%v diff=%+v", record, diff)
	}
}

func TestDiffGenericUnstructured_GenerationWithoutDesiredStateChange(t *testing.T) {
	for _, status := range []string{"none", "condition", "phase", "noise"} {
		t.Run(status, func(t *testing.T) {
			old := collisionObject("example.com/v1", "Widget", map[string]any{"spec": map[string]any{"replicas": int64(1)}})
			old.SetGeneration(1)
			new := old.DeepCopy()
			new.SetGeneration(2)
			switch status {
			case "condition":
				new.Object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "False"}}}
			case "phase":
				new.Object["status"] = map[string]any{"phase": "Running"}
			case "noise":
				new.SetResourceVersion("2")
				new.Object["status"] = map[string]any{"observedGeneration": int64(2)}
				old.Object["status"] = map[string]any{"observedGeneration": int64(1)}
			}
			for _, precomputed := range []bool{false, true} {
				diff, record := timelineUpdateDiff("Widget", old, new, ComputeDiff("Widget", old, new), precomputed)
				if status == "none" || status == "noise" {
					if record || diff != nil {
						t.Fatalf("generation/noise-only update recorded: %+v", diff)
					}
					continue
				}
				wantPath := "resource"
				if status == "condition" {
					wantPath = "status.conditions[Ready]"
				}
				if !record || diff == nil || len(diff.Fields) != 1 || diff.Fields[0].Path != wantPath || strings.Contains(diff.Summary, "spec") {
					t.Fatalf("status update = record %v diff %+v, want %s", record, diff, wantPath)
				}
			}
		})
	}
}

func TestDiffGenericUnstructured_DesiredStateOrderOnly(t *testing.T) {
	old := collisionObject("networking.istio.io/v1", "VirtualService", map[string]any{
		"spec": map[string]any{"http": []any{
			map[string]any{"name": "specific", "match": "secret-specific-route"},
			map[string]any{"name": "fallback", "match": "secret-fallback-route"},
		}},
		"routing": []any{map[string]any{"name": "first"}, map[string]any{"name": "last"}},
	})
	old.SetGeneration(1)
	new := old.DeepCopy()
	new.SetGeneration(2)
	for _, items := range [][]any{new.Object["spec"].(map[string]any)["http"].([]any), new.Object["routing"].([]any)} {
		items[0], items[1] = items[1], items[0]
	}
	want := []FieldChange{
		{Path: "routing", OldValue: []string{"first", "last"}, NewValue: []string{"last", "first"}},
		{Path: "spec.http", OldValue: []string{"specific", "fallback"}, NewValue: []string{"fallback", "specific"}},
		{Path: "metadata.generation", OldValue: int64(1), NewValue: int64(2)},
	}
	for _, precomputed := range []bool{false, true} {
		diff, record := timelineUpdateDiff("VirtualService", old, new, ComputeDiff("VirtualService", old, new), precomputed)
		if !record || diff == nil || !reflect.DeepEqual(diff.Fields, want) {
			t.Fatalf("record=%v diff=%+v, want fields %+v", record, diff, want)
		}
		if diff.Summary != "desired state changes: routing order, spec.http order" {
			t.Fatalf("summary = %q", diff.Summary)
		}
	}
}

func TestDiffGenericUnstructured_DesiredStateNullRoot(t *testing.T) {
	old := collisionObject("example.com/v1", "Widget", map[string]any{})
	old.SetGeneration(1)
	new := old.DeepCopy()
	new.SetGeneration(2)
	new.Object["routing"] = nil
	diff := ComputeDiff("Widget", old, new)
	want := []FieldChange{
		{Path: "routing (order or structure changed)", OldValue: hiddenValueMarker, NewValue: hiddenValueMarker},
		{Path: "metadata.generation", OldValue: int64(1), NewValue: int64(2)},
	}
	if diff == nil || !reflect.DeepEqual(diff.Fields, want) {
		t.Fatalf("diff = %+v, want fields %+v", diff, want)
	}
}

func TestDiffGenericUnstructured_DesiredStateOrderBesideOtherRootChange(t *testing.T) {
	for _, reorderedRoot := range []string{"routing", "spec"} {
		t.Run(reorderedRoot, func(t *testing.T) {
			changedRoot := "routing"
			if reorderedRoot == changedRoot {
				changedRoot = "spec"
			}
			old := collisionObject("networking.istio.io/v1", "VirtualService", map[string]any{
				reorderedRoot: map[string]any{"http": []any{
					map[string]any{"name": "specific", "match": "secret-specific-route"},
					map[string]any{"name": "fallback", "match": "secret-fallback-route"},
				}},
				changedRoot: map[string]any{"replicas": int64(1)},
			})
			old.SetGeneration(1)
			new := old.DeepCopy()
			new.SetGeneration(2)
			items := new.Object[reorderedRoot].(map[string]any)["http"].([]any)
			items[0], items[1] = items[1], items[0]
			new.Object[changedRoot].(map[string]any)["replicas"] = int64(2)

			for _, precomputed := range []bool{false, true} {
				diff, record := timelineUpdateDiff("VirtualService", old, new, ComputeDiff("VirtualService", old, new), precomputed)
				if !record || diff == nil || len(diff.Fields) != 3 {
					t.Fatalf("record=%v diff=%+v, want both desired-state rows and generation", record, diff)
				}
				order := findChange(diff, reorderedRoot+".http")
				if order == nil || !reflect.DeepEqual(order.NewValue, []string{"fallback", "specific"}) {
					t.Fatalf("order row = %+v, want new route order", order)
				}
				leaf := findChange(diff, changedRoot+".replicas")
				if leaf == nil || leaf.OldValue != int64(1) || leaf.NewValue != int64(2) {
					t.Fatalf("leaf row = %+v, want replicas 1→2", leaf)
				}
			}
		})
	}
}

func TestDiffGenericUnstructured_OrderBesideSiblingChange(t *testing.T) {
	old := collisionObject("networking.istio.io/v1", "VirtualService", map[string]any{"spec": map[string]any{
		"gateways": []any{"mesh"},
		"http":     []any{map[string]any{"name": "specific"}, map[string]any{"name": "fallback"}},
	}})
	old.SetGeneration(1)
	new := old.DeepCopy()
	new.SetGeneration(2)
	spec := new.Object["spec"].(map[string]any)
	spec["gateways"] = []any{"mesh", "edge"}
	spec["http"] = []any{map[string]any{"name": "fallback"}, map[string]any{"name": "specific"}}
	diff := ComputeDiff("VirtualService", old, new)
	if diff == nil || findChange(diff, "spec.http") == nil || findChange(diff, "spec.gateways[1]") == nil {
		t.Fatalf("diff = %+v, want both the route reorder and the gateway edit", diff)
	}
}

func TestDiffGenericUnstructured_UnkeyedInsertReportsOneItem(t *testing.T) {
	rule := func(n int) any {
		return map[string]any{"alert": fmt.Sprintf("Alert%d", n), "expr": fmt.Sprintf("up{job=%q} == 0", fmt.Sprint(n))}
	}
	var rules []any
	for i := 0; i < 10; i++ {
		rules = append(rules, rule(i))
	}
	old := collisionObject("monitoring.coreos.com/v1", "PrometheusRule", map[string]any{"spec": map[string]any{"rules": rules}})
	old.SetGeneration(1)
	for _, tc := range []struct {
		name  string
		rules []any
		want  FieldChange
	}{
		{"prepend", append([]any{rule(99)}, rules...), FieldChange{Path: "spec.rules[0]", OldValue: nil, NewValue: hiddenSetMarker}},
		{"remove middle", append(append([]any{}, rules[:4]...), rules[5:]...), FieldChange{Path: "spec.rules[4]", OldValue: hiddenSetMarker, NewValue: nil}},
		{"prepend and edit last", append(append([]any{rule(99)}, rules[:9]...), map[string]any{"alert": "Alert9", "expr": "edited"}), FieldChange{Path: "spec.rules[10].expr", OldValue: hiddenValueMarker, NewValue: hiddenValueMarker}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			new := old.DeepCopy()
			new.SetGeneration(2)
			new.Object["spec"] = map[string]any{"rules": tc.rules}
			diff := ComputeDiff("PrometheusRule", old, new)
			want := []FieldChange{tc.want, {Path: "metadata.generation", OldValue: int64(1), NewValue: int64(2)}}
			if tc.name == "prepend and edit last" {
				want = append([]FieldChange{{Path: "spec.rules[0]", OldValue: nil, NewValue: hiddenSetMarker}}, want...)
			}
			if diff == nil || !reflect.DeepEqual(diff.Fields, want) {
				t.Fatalf("diff = %+v, want %+v", diff, want)
			}
		})
	}
}

func TestTimelineUpdateDiff_CRDStrippedFieldEdit(t *testing.T) {
	crd := func(schemaType string) *unstructured.Unstructured {
		u := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition",
			"metadata": map[string]any{"name": "widgets.example.com", "generation": int64(1)},
			"spec": map[string]any{"group": "example.com", "versions": []any{map[string]any{
				"name": "v1", "served": true, "storage": true,
				"schema": map[string]any{"openAPIV3Schema": map[string]any{"type": schemaType}},
			}}},
		}}
		stripped, err := k8score.DropUnstructuredManagedFields(u)
		if err != nil {
			t.Fatal(err)
		}
		return stripped.(*unstructured.Unstructured)
	}
	old, new := crd("object"), crd("string")
	new.SetGeneration(2)
	for _, precomputed := range []bool{false, true} {
		diff, record := timelineUpdateDiff("CustomResourceDefinition", old, new, ComputeDiff("CustomResourceDefinition", old, new), precomputed)
		if !record || diff == nil || findChange(diff, "metadata.generation") == nil || !strings.Contains(diff.Summary, "schema or conversion changed") {
			t.Fatalf("CRD schema edit: record=%v diff=%+v", record, diff)
		}
	}
}

func TestDiffGenericUnstructured_PathCapAcrossRoots(t *testing.T) {
	old := collisionObject("example.com/v1", "Widget", map[string]any{"spec": map[string]any{}, "settings": map[string]any{}})
	old.SetGeneration(1)
	new := old.DeepCopy()
	new.SetGeneration(2)
	for i := 0; i < maxUntrackedSpecRows+5; i++ {
		root := "spec"
		if i < 10 {
			root = "settings"
		}
		new.Object[root].(map[string]any)[fmt.Sprintf("field%02d", i)] = int64(i)
	}
	changes, summary := diffGenericUnstructured(old, new)
	if len(changes) != maxUntrackedSpecRows+2 {
		t.Fatalf("got %d rows, want 25 values, overflow and generation", len(changes))
	}
	overflow := changes[maxUntrackedSpecRows]
	want := []string{"spec.field25", "spec.field26", "spec.field27", "spec.field28", "spec.field29"}
	if overflow.Path != "desired state (more fields)" || !reflect.DeepEqual(overflow.NewValue, want) {
		t.Fatalf("overflow = %+v, want paths %v", overflow, want)
	}
	if got := strings.Join(summary, ", "); got != "desired state changes: settings.field00, settings.field01, settings.field02 (+27 more)" {
		t.Fatalf("summary = %q", got)
	}
}

func TestDiffGenericUnstructured_SampleTidbEdit(t *testing.T) {
	old := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "pingcap.com/v1alpha1", "kind": "TidbCluster",
		"spec": map[string]any{"tidb": map[string]any{"statefulSetUpdateStrategy": "RollingUpdate"}},
	}}
	old.SetGeneration(1)
	new := old.DeepCopy()
	new.SetGeneration(2)
	new.Object["spec"].(map[string]any)["tidb"].(map[string]any)["statefulSetUpdateStrategy"] = "SomeStrategyForUpdate"
	diff := ComputeDiff("TidbCluster", old, new)
	if diff == nil || diff.Summary != "desired state changes: spec.tidb.statefulSetUpdateStrategy" || len(diff.Fields) != 2 || diff.Fields[0].Path != "spec.tidb.statefulSetUpdateStrategy" || diff.Fields[0].OldValue != "RollingUpdate" || diff.Fields[0].NewValue != "SomeStrategyForUpdate" {
		t.Fatalf("sample diff = %+v", diff)
	}
}

func TestDiffGenericUnstructured_AddedBlockCollapsed(t *testing.T) {
	old := collisionObject("example.com/v1", "Widget", map[string]any{"spec": map[string]any{}})
	old.SetGeneration(1)
	new := old.DeepCopy()
	new.SetGeneration(2)
	block := map[string]any{}
	for i := 0; i < 1000; i++ {
		block[fmt.Sprintf("field%d", i)] = int64(i)
	}
	new.Object["spec"].(map[string]any)["settings"] = block
	diff := ComputeDiff("Widget", old, new)
	want := []FieldChange{
		{Path: "spec.settings", OldValue: nil, NewValue: hiddenSetMarker},
		{Path: "metadata.generation", OldValue: int64(1), NewValue: int64(2)},
	}
	if diff == nil || !reflect.DeepEqual(diff.Fields, want) || diff.Summary != "desired state changes: spec.settings" {
		t.Fatalf("added block = %+v, want %+v", diff, want)
	}
}

func TestDiffGenericUnstructured_OverflowPathsBounded(t *testing.T) {
	oldSpec, newSpec := map[string]any{}, map[string]any{}
	for i := 0; i < 2000; i++ {
		key := fmt.Sprintf("field%04d", i)
		oldSpec[key], newSpec[key] = int64(0), int64(1)
	}
	old := collisionObject("example.com/v1", "Widget", map[string]any{"spec": oldSpec})
	old.SetGeneration(1)
	new := old.DeepCopy()
	new.SetGeneration(2)
	new.Object["spec"] = newSpec
	diff := ComputeDiff("Widget", old, new)
	if diff == nil || len(diff.Fields) != maxUntrackedSpecRows+2 {
		t.Fatalf("diff = %+v, want capped value rows, overflow and generation", diff)
	}
	overflow := diff.Fields[maxUntrackedSpecRows]
	paths, ok := overflow.NewValue.([]string)
	if !ok || len(paths) != maxUntrackedSpecRows || paths[0] != "spec.field0025" || paths[len(paths)-1] != "spec.field0049" || overflow.Path != "desired state (more fields; 1950 paths omitted)" {
		t.Fatalf("overflow = %+v, want 25 paths and 1950 omitted", overflow)
	}
	if diff.Summary != "desired state changes: spec.field0000, spec.field0001, spec.field0002 (+1997 more)" {
		t.Fatalf("summary = %q", diff.Summary)
	}
}

func TestDiffGenericUnstructured_QualifiedKeysHidden(t *testing.T) {
	old := collisionObject("example.com/v1", "Widget", map[string]any{"spec": map[string]any{
		"encryption": map[string]any{"key": "old material"},
		"tls":        map[string]any{"key": "old material"},
		"foo":        map[string]any{"key": "old material"},
	}})
	old.SetGeneration(1)
	new := old.DeepCopy()
	new.SetGeneration(2)
	new.Object["spec"] = map[string]any{
		"encryption": map[string]any{"key": "new material"},
		"tls":        map[string]any{"key": int64(123456)},
		"foo":        map[string]any{"key": "new material"},
	}
	diff := ComputeDiff("Widget", old, new)
	for _, path := range []string{"spec.encryption.key", "spec.tls.key", "spec.foo.key"} {
		change := findChange(diff, path)
		if change == nil || change.OldValue != hiddenValueMarker || change.NewValue != hiddenValueMarker {
			t.Fatalf("%s = %+v, want hidden values", path, change)
		}
	}
}

// Argo CD's Application CRD has no status subresource, so every controller
// status write (reconciledAt on each refresh, a sync request) bumps generation.
func TestTimelineUpdateDiff_ArgoApplicationGenerationBumps(t *testing.T) {
	app := func(apiVersion string) *unstructured.Unstructured {
		u := collisionObject(apiVersion, "Application", map[string]any{
			"spec": map[string]any{
				"project": "default",
				"source": map[string]any{
					"repoURL": "https://example.com/repo.git", "targetRevision": "main", "path": "app",
					"helm": map[string]any{"valuesObject": map[string]any{"replicas": int64(1), "db": map[string]any{"key": "old", "pin": int64(1111)}}},
					"kustomize": map[string]any{
						"commonAnnotations": map[string]any{"path": "old-private"},
						"commonLabels":      map[string]any{"targetRevision": "old-private"},
					},
				},
				"sources": []any{
					map[string]any{"name": "base", "repoURL": "https://example.com/base.git"},
					map[string]any{"name": "overlay", "repoURL": "https://example.com/overlay.git"},
				},
				"syncPolicy": map[string]any{"automated": map[string]any{"enabled": true, "prune": true}},
			},
			"status": map[string]any{
				"reconciledAt":   "2026-11-14T10:00:00Z",
				"sync":           map[string]any{"status": "Synced", "revision": "abc"},
				"health":         map[string]any{"status": "Healthy"},
				"operationState": map[string]any{"phase": "Succeeded"},
			},
		})
		u.SetGeneration(10)
		return u
	}
	set := func(u *unstructured.Unstructured, value any, path ...string) {
		if err := unstructured.SetNestedField(u.Object, value, path...); err != nil {
			t.Fatal(err)
		}
	}
	requestSync := func(u *unstructured.Unstructured) {
		u.Object["operation"] = map[string]any{"sync": map[string]any{"revision": "abc"}, "initiatedBy": map[string]any{"automated": true}}
		unstructured.RemoveNestedField(u.Object, "status", "operationState")
	}
	sources := func(names ...string) []any {
		out := make([]any, len(names))
		for i, name := range names {
			out[i] = map[string]any{"name": name, "repoURL": "https://example.com/" + name + ".git"}
		}
		return out
	}

	tests := []struct {
		name       string
		apiVersion string
		genDelta   int64
		mutate     func(*unstructured.Unstructured)
		wantPaths  []string
		wantHidden []string
		wantValues map[string][2]any
		// unnamedSources starts from two sources without a name field.
		unnamedSources bool
	}{
		{name: "refresh writes reconciledAt", genDelta: 1, mutate: func(u *unstructured.Unstructured) {
			set(u, "2026-11-14T10:03:00Z", "status", "reconciledAt")
		}},
		{name: "bare generation bump", genDelta: 1, mutate: func(*unstructured.Unstructured) {}},
		{name: "folded status writes", genDelta: 5, mutate: func(*unstructured.Unstructured) {}},
		{name: "sync request", genDelta: 1, mutate: requestSync},
		{name: "sync request beside a spec edit", genDelta: 1, mutate: func(u *unstructured.Unstructured) {
			requestSync(u)
			set(u, "other", "spec", "source", "path")
		}, wantPaths: []string{"spec.source.path"}},
		{name: "sync status beside a spec edit", genDelta: 1, mutate: func(u *unstructured.Unstructured) {
			set(u, "OutOfSync", "status", "sync", "status")
			set(u, "other", "spec", "source", "path")
		}, wantPaths: []string{"status.sync.status", "spec.source.path"}},
		{name: "tracked targetRevision edit", genDelta: 1, mutate: func(u *unstructured.Unstructured) {
			set(u, "v2", "spec", "source", "targetRevision")
		}, wantPaths: []string{"spec.source.targetRevision"}},
		{name: "auto-sync toggle is one row", genDelta: 1, mutate: func(u *unstructured.Unstructured) {
			set(u, false, "spec", "syncPolicy", "automated", "enabled")
		}, wantPaths: []string{"spec.syncPolicy.automated"}},
		{name: "helm valuesObject values are hidden", genDelta: 1, mutate: func(u *unstructured.Unstructured) {
			set(u, int64(3), "spec", "source", "helm", "valuesObject", "replicas")
			set(u, "new", "spec", "source", "helm", "valuesObject", "db", "key")
			set(u, int64(2222), "spec", "source", "helm", "valuesObject", "db", "pin")
		}, wantPaths: []string{
			"spec.source.helm.valuesObject.db.key", "spec.source.helm.valuesObject.db.pin", "spec.source.helm.valuesObject.replicas",
		}, wantHidden: []string{
			"spec.source.helm.valuesObject.replicas", "spec.source.helm.valuesObject.db.key", "spec.source.helm.valuesObject.db.pin",
		}},
		{name: "kustomize common label and annotation values are hidden", genDelta: 1, mutate: func(u *unstructured.Unstructured) {
			set(u, "new-private", "spec", "source", "kustomize", "commonAnnotations", "path")
			set(u, "new-private", "spec", "source", "kustomize", "commonLabels", "targetRevision")
		}, wantPaths: []string{
			"spec.source.kustomize.commonAnnotations.path", "spec.source.kustomize.commonLabels.targetRevision",
		}, wantHidden: []string{
			"spec.source.kustomize.commonAnnotations.path", "spec.source.kustomize.commonLabels.targetRevision",
		}},
		{name: "sources reorder beside a sync status change", genDelta: 1, mutate: func(u *unstructured.Unstructured) {
			set(u, "OutOfSync", "status", "sync", "status")
			u.Object["spec"].(map[string]any)["sources"] = sources("overlay", "base")
		}, wantPaths: []string{"status.sync.status", "spec.sources"}},
		{name: "unnamed sources reorder reads as an order change", genDelta: 1, mutate: func(u *unstructured.Unstructured) {
			u.Object["spec"].(map[string]any)["sources"] = []any{
				map[string]any{"repoURL": "https://example.com/values.git", "ref": "values"},
				map[string]any{"repoURL": "https://example.com/chart.git", "chart": "app"},
			}
		}, unnamedSources: true, wantPaths: []string{"spec.sources"}, wantValues: map[string][2]any{"spec.sources": {[]string{"0", "1"}, []string{"1", "0"}}}},
		{name: "multi-source revision change shows the revision", genDelta: 1, mutate: func(u *unstructured.Unstructured) {
			u.Object["spec"].(map[string]any)["sources"] = []any{
				map[string]any{"name": "base", "repoURL": "https://example.com/base.git"},
				map[string]any{"name": "overlay", "repoURL": "https://example.com/overlay.git", "targetRevision": "v2"},
			}
		}, wantPaths: []string{"spec.sources[overlay].targetRevision"}, wantValues: map[string][2]any{"spec.sources[overlay].targetRevision": {nil, "v2"}}},
		{name: "foreign-group Application keeps operation as desired state", apiVersion: "example.com/v1", genDelta: 1, mutate: func(u *unstructured.Unstructured) {
			u.Object["operation"] = map[string]any{"sync": map[string]any{}}
		}, wantPaths: []string{"operation", "metadata.generation"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			apiVersion := tt.apiVersion
			if apiVersion == "" {
				apiVersion = "argoproj.io/v1alpha1"
			}
			old := app(apiVersion)
			if tt.unnamedSources {
				old.Object["spec"].(map[string]any)["sources"] = []any{
					map[string]any{"repoURL": "https://example.com/chart.git", "chart": "app"},
					map[string]any{"repoURL": "https://example.com/values.git", "ref": "values"},
				}
			}
			new := old.DeepCopy()
			new.SetGeneration(old.GetGeneration() + tt.genDelta)
			tt.mutate(new)

			recomputed, recordRecomputed := timelineUpdateDiff("Application", old, new, nil, false)
			precomputed, recordPrecomputed := timelineUpdateDiff("Application", old, new, ComputeDiff("Application", old, new), true)
			if recordRecomputed != recordPrecomputed || !reflect.DeepEqual(recomputed, precomputed) {
				t.Fatalf("precomputed and recomputed paths disagree: %+v / %+v", recomputed, precomputed)
			}
			if len(tt.wantPaths) == 0 {
				if recordRecomputed || recomputed != nil {
					t.Fatalf("recorded %+v, want no event", recomputed)
				}
				return
			}
			if !recordRecomputed || recomputed == nil {
				t.Fatalf("not recorded, want %v", tt.wantPaths)
			}
			var got []string
			for _, field := range recomputed.Fields {
				got = append(got, field.Path)
			}
			if !reflect.DeepEqual(got, tt.wantPaths) {
				t.Fatalf("paths = %v, want %v (summary %q)", got, tt.wantPaths, recomputed.Summary)
			}
			for path, want := range tt.wantValues {
				for _, field := range recomputed.Fields {
					if field.Path == path && (!reflect.DeepEqual(field.OldValue, want[0]) || !reflect.DeepEqual(field.NewValue, want[1])) {
						t.Fatalf("%s = %v -> %v, want %v -> %v", path, field.OldValue, field.NewValue, want[0], want[1])
					}
				}
			}
			for _, path := range tt.wantHidden {
				for _, field := range recomputed.Fields {
					if field.Path == path && (field.OldValue != hiddenValueMarker || field.NewValue != hiddenValueMarker) {
						t.Fatalf("%s = %v -> %v, want hidden values", path, field.OldValue, field.NewValue)
					}
				}
			}
		})
	}
}

func TestTimelineUpdateDiff_AuditedCRDSpecEditStillRecorded(t *testing.T) {
	old := collisionObject("kustomize.toolkit.fluxcd.io/v1", "Kustomization", map[string]any{"spec": map[string]any{"path": "./app", "interval": "5m"}})
	old.SetGeneration(1)
	new := old.DeepCopy()
	new.SetGeneration(2)
	new.Object["spec"].(map[string]any)["interval"] = "10m"
	diff, record := timelineUpdateDiff("Kustomization", old, new, nil, false)
	if !record || diff == nil || len(diff.Fields) != 1 || diff.Fields[0].Path != "metadata.generation" {
		t.Fatalf("audited spec edit fallback changed: record=%v diff=%+v", record, diff)
	}
}
