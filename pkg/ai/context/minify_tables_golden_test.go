package context

import (
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Table-exhaustive golden coverage of the minify drop tables. The expected
// key sets below are LITERAL copies of the tables — deliberately NOT built by
// iterating them — so that silently losing a table entry (or the code that
// applies a whole profile) fails a concrete assertion. The fixtures carry
// every table entry; TestGoldenListsMatchDropTables keeps the literals and
// the tables in exact sync in both directions.

var (
	goldenStripMetadataKeys = []string{
		"resourceVersion", "uid", "generation", "selfLink", "generateName",
		"managedFields", "deletionGracePeriodSeconds", "finalizers",
	}
	goldenStripPodSpecFields = []string{
		"tolerations", "dnsPolicy", "schedulerName", "priority",
		"priorityClassName", "preemptionPolicy", "nodeName", "enableServiceLinks",
	}
	goldenStripPodSpecFieldsCompact = []string{
		"volumes", "serviceAccountName", "serviceAccount", "securityContext",
		"hostNetwork", "hostPID", "hostIPC", "affinity", "topologySpreadConstraints",
	}
	goldenStripContainerFields = []string{
		"terminationMessagePath", "terminationMessagePolicy",
	}
	goldenStripContainerFieldsCompact = []string{
		"command", "args", "livenessProbe", "readinessProbe", "startupProbe",
		"lifecycle", "securityContext", "workingDir", "stdin", "stdinOnce", "tty",
	}
	goldenStripPodStatusFields = []string{
		"hostIP", "podIP", "podIPs", "qosClass", "startTime",
	}
	goldenStripWorkloadStatusFields = []string{
		"observedGeneration", "collisionCount", "updatedNumberScheduled",
	}
)

func assertSameKeySet(t *testing.T, name string, literal []string, table map[string]bool) {
	t.Helper()
	seen := map[string]bool{}
	for _, k := range literal {
		if !table[k] {
			t.Errorf("%s: literal key %q not in table — was a table entry dropped?", name, k)
		}
		seen[k] = true
	}
	for k := range table {
		if !seen[k] {
			t.Errorf("%s: table key %q missing from the golden literal list — add it AND fixture coverage", name, k)
		}
	}
}

func TestGoldenListsMatchDropTables(t *testing.T) {
	assertSameKeySet(t, "stripMetadataKeys", goldenStripMetadataKeys, stripMetadataKeys)
	assertSameKeySet(t, "stripPodSpecFields", goldenStripPodSpecFields, stripPodSpecFields)
	assertSameKeySet(t, "stripPodSpecFieldsCompact", goldenStripPodSpecFieldsCompact, stripPodSpecFieldsCompact)
	assertSameKeySet(t, "stripContainerFields", goldenStripContainerFields, stripContainerFields)
	assertSameKeySet(t, "stripContainerFieldsCompact", goldenStripContainerFieldsCompact, stripContainerFieldsCompact)
	assertSameKeySet(t, "stripPodStatusFields", goldenStripPodStatusFields, stripPodStatusFields)
	assertSameKeySet(t, "stripWorkloadStatusFields", goldenStripWorkloadStatusFields, stripWorkloadStatusFields)
}

// --- fixtures: every table entry present, plus keep fields ---

func goldenContainerFixture(name string) map[string]any {
	c := map[string]any{
		"name":            name,
		"image":           "nginx:1.25",
		"ports":           []any{map[string]any{"containerPort": int64(8080)}},
		"resources":       map[string]any{"requests": map[string]any{"cpu": "100m"}},
		"env":             []any{map[string]any{"name": "LOG_LEVEL", "value": "debug"}},
		"volumeMounts":    []any{map[string]any{"name": "data", "mountPath": "/data"}},
		"imagePullPolicy": "IfNotPresent",
	}
	for _, k := range goldenStripContainerFields {
		c[k] = "detail-stripped"
	}
	for _, k := range goldenStripContainerFieldsCompact {
		c[k] = "compact-stripped"
	}
	return c
}

func goldenPodSpecFixture() map[string]any {
	spec := map[string]any{
		"containers":     []any{goldenContainerFixture("app")},
		"initContainers": []any{goldenContainerFixture("init")},
		"restartPolicy":  "Always",
	}
	for _, k := range goldenStripPodSpecFields {
		spec[k] = "detail-stripped"
	}
	for _, k := range goldenStripPodSpecFieldsCompact {
		spec[k] = "compact-stripped"
	}
	return spec
}

func goldenMetadataFixture(name string) map[string]any {
	meta := map[string]any{
		"name":      name,
		"namespace": "default",
		"labels":    map[string]any{"app": name},
		"annotations": map[string]any{
			"app.kubernetes.io/name":  name,
			"custom.example.com/note": "kept-at-detail-only",
		},
	}
	for _, k := range goldenStripMetadataKeys {
		meta[k] = "stripped"
	}
	return meta
}

func goldenPodFixture() *unstructured.Unstructured {
	status := map[string]any{
		"phase":             "Running",
		"conditions":        []any{map[string]any{"type": "Ready", "status": "True"}},
		"containerStatuses": []any{map[string]any{"name": "app", "ready": true}},
	}
	for _, k := range goldenStripPodStatusFields {
		status[k] = "stripped"
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata":   goldenMetadataFixture("golden-pod"),
		"spec":       goldenPodSpecFixture(),
		"status":     status,
	}}
}

func goldenWorkloadFixture(kind string) *unstructured.Unstructured {
	status := map[string]any{
		"replicas":      int64(3),
		"readyReplicas": int64(3),
	}
	for _, k := range goldenStripWorkloadStatusFields {
		status[k] = "stripped"
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       kind,
		"metadata":   goldenMetadataFixture("golden-workload"),
		"spec": map[string]any{
			"replicas": int64(3),
			"selector": map[string]any{"matchLabels": map[string]any{"app": "golden-workload"}},
			"template": map[string]any{
				"metadata": map[string]any{"labels": map[string]any{"app": "golden-workload"}},
				"spec":     goldenPodSpecFixture(),
			},
		},
		"status": status,
	}}
}

// --- assertion helpers ---

func nestedMap(t *testing.T, m map[string]any, path ...string) map[string]any {
	t.Helper()
	v, found, err := unstructured.NestedFieldNoCopy(m, path...)
	if err != nil || !found {
		t.Fatalf("path %v missing (found=%v err=%v)", path, found, err)
	}
	out, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("path %v is %T, want map", path, v)
	}
	return out
}

func nestedSliceOfMaps(t *testing.T, m map[string]any, path ...string) []map[string]any {
	t.Helper()
	v, found, err := unstructured.NestedFieldNoCopy(m, path...)
	if err != nil || !found {
		t.Fatalf("path %v missing (found=%v err=%v)", path, found, err)
	}
	items, ok := v.([]any)
	if !ok {
		t.Fatalf("path %v is %T, want slice", path, v)
	}
	out := make([]map[string]any, 0, len(items))
	for i, item := range items {
		elem, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("path %v element %d is %T, want map", path, i, item)
		}
		out = append(out, elem)
	}
	return out
}

func assertKeysAbsent(t *testing.T, where string, m map[string]any, keys []string) {
	t.Helper()
	for _, k := range keys {
		if _, ok := m[k]; ok {
			t.Errorf("%s: key %q survived, want stripped", where, k)
		}
	}
}

func assertKeysPresent(t *testing.T, where string, m map[string]any, keys []string) {
	t.Helper()
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			t.Errorf("%s: key %q missing, want kept", where, k)
		}
	}
}

func assertGoldenPodSpec(t *testing.T, spec map[string]any, level VerbosityLevel, where string) {
	t.Helper()
	assertKeysAbsent(t, where, spec, goldenStripPodSpecFields)
	if level == LevelCompact {
		assertKeysAbsent(t, where, spec, goldenStripPodSpecFieldsCompact)
	} else {
		assertKeysPresent(t, where, spec, goldenStripPodSpecFieldsCompact)
	}
	assertKeysPresent(t, where, spec, []string{"restartPolicy"})
	for _, listKey := range []string{"containers", "initContainers"} {
		containers := nestedSliceOfMaps(t, spec, listKey)
		if len(containers) != 1 {
			t.Fatalf("%s.%s: got %d containers, want 1", where, listKey, len(containers))
		}
		c := containers[0]
		cWhere := where + "." + listKey + "[0]"
		assertKeysAbsent(t, cWhere, c, goldenStripContainerFields)
		if level == LevelCompact {
			assertKeysAbsent(t, cWhere, c, goldenStripContainerFieldsCompact)
			// Conditional simplification still runs at Compact
			// (simplifyEnvToNames/simplifyVolumeMounts emit []string).
			env, ok := c["env"].([]string)
			if !ok || len(env) != 1 || env[0] != "LOG_LEVEL" {
				t.Errorf("%s: env not simplified to names: %#v", cWhere, c["env"])
			}
			mounts, ok := c["volumeMounts"].([]string)
			if !ok || len(mounts) != 1 || mounts[0] != "data" {
				t.Errorf("%s: volumeMounts not simplified to names: %#v", cWhere, c["volumeMounts"])
			}
		} else {
			assertKeysPresent(t, cWhere, c, goldenStripContainerFieldsCompact)
		}
		assertKeysPresent(t, cWhere, c, []string{"name", "image", "ports", "resources"})
		if _, ok := c["imagePullPolicy"]; ok {
			t.Errorf("%s: imagePullPolicy %q survived, want conditionally stripped", cWhere, c["imagePullPolicy"])
		}
	}
}

func assertGoldenMetadata(t *testing.T, meta map[string]any, level VerbosityLevel, where string) {
	t.Helper()
	assertKeysAbsent(t, where, meta, goldenStripMetadataKeys)
	assertKeysPresent(t, where, meta, []string{"name", "namespace", "labels"})
	annotations := nestedMap(t, meta, "annotations")
	assertKeysPresent(t, where+".annotations", annotations, []string{"app.kubernetes.io/name"})
	if level == LevelCompact {
		assertKeysAbsent(t, where+".annotations", annotations, []string{"custom.example.com/note"})
	} else {
		assertKeysPresent(t, where+".annotations", annotations, []string{"custom.example.com/note"})
	}
}

// --- the golden tests ---

func TestMinifyUnstructured_TableExhaustive_Pod(t *testing.T) {
	for _, level := range []VerbosityLevel{LevelDetail, LevelCompact} {
		levelName := map[VerbosityLevel]string{LevelDetail: "Detail", LevelCompact: "Compact"}[level]
		t.Run(levelName, func(t *testing.T) {
			out, ok := MinifyUnstructured(goldenPodFixture(), level).(map[string]any)
			if !ok {
				t.Fatalf("MinifyUnstructured returned %T, want map", out)
			}
			assertGoldenMetadata(t, nestedMap(t, out, "metadata"), level, "metadata")
			assertGoldenPodSpec(t, nestedMap(t, out, "spec"), level, "spec")
			status := nestedMap(t, out, "status")
			assertKeysAbsent(t, "status", status, goldenStripPodStatusFields)
			assertKeysPresent(t, "status", status, []string{"phase", "conditions", "containerStatuses"})
		})
	}
}

func TestMinifyUnstructured_TableExhaustive_Workloads(t *testing.T) {
	for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet", "ReplicaSet"} {
		for _, level := range []VerbosityLevel{LevelDetail, LevelCompact} {
			levelName := map[VerbosityLevel]string{LevelDetail: "Detail", LevelCompact: "Compact"}[level]
			t.Run(fmt.Sprintf("%s/%s", kind, levelName), func(t *testing.T) {
				out, ok := MinifyUnstructured(goldenWorkloadFixture(kind), level).(map[string]any)
				if !ok {
					t.Fatalf("MinifyUnstructured returned %T, want map", out)
				}
				assertGoldenMetadata(t, nestedMap(t, out, "metadata"), level, "metadata")
				spec := nestedMap(t, out, "spec")
				assertKeysPresent(t, "spec", spec, []string{"replicas", "selector"})
				assertGoldenPodSpec(t, nestedMap(t, spec, "template", "spec"), level, "spec.template.spec")
				status := nestedMap(t, out, "status")
				assertKeysAbsent(t, "status", status, goldenStripWorkloadStatusFields)
				assertKeysPresent(t, "status", status, []string{"replicas", "readyReplicas"})
			})
		}
	}
}
