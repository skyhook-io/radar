package insights

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// liveWith builds an Unstructured with the given last-applied-configuration
// (raw JSON string) and live spec map. Lets each test case express drift
// inputs without boilerplate.
func liveWith(lastApplied string, liveSpec map[string]any) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Resource",
		"metadata": map[string]any{
			"name":      "x",
			"namespace": "y",
			"annotations": map[string]any{
				kubectlLastAppliedAnnotation: lastApplied,
			},
		},
	}}
	if liveSpec != nil {
		obj.Object["spec"] = liveSpec
	}
	return obj
}

func TestComputeDrift_NoAnnotation_ReturnsNil(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"a": 1}}}
	if got := computeDriftFromLastApplied(obj, nil); got != nil {
		t.Fatalf("expected nil when annotation missing, got %#v", got)
	}
}
func TestComputeDrift_InvalidJSON_ReturnsNil(t *testing.T) {
	if got := computeDriftFromLastApplied(liveWith("not-json", map[string]any{"a": 1}), nil); got != nil {
		t.Fatalf("expected nil on parse failure, got %#v", got)
	}
}

func TestComputeDrift_NoDrift_ReturnsNil(t *testing.T) {
	desired := `{"spec":{"a":1,"b":"two"}}`
	got := computeDriftFromLastApplied(liveWith(desired, map[string]any{"a": int64(1), "b": "two"}), nil)
	if got != nil {
		t.Fatalf("expected nil when desired and live match, got %d entries", len(got.Entries))
	}
}

func TestComputeDrift_KarpenterStyleSchemaMigration(t *testing.T) {
	// This is the actual case the user hit in the cluster: NodePool's
	// expireAfter moved from spec.disruption to spec.template.spec, and
	// budgets got defaulted in. Verify all three drift entries surface.
	desired := `{"spec":{"disruption":{"consolidateAfter":"30s","consolidationPolicy":"WhenEmptyOrUnderutilized","expireAfter":"720h"},"template":{"spec":{"requirements":[]}}}}`
	live := map[string]any{
		"disruption": map[string]any{
			"budgets":             []any{map[string]any{"nodes": "10%"}},
			"consolidateAfter":    "30s",
			"consolidationPolicy": "WhenEmptyOrUnderutilized",
		},
		"template": map[string]any{
			"spec": map[string]any{
				"expireAfter":  "720h",
				"requirements": []any{},
			},
		},
	}
	got := computeDriftFromLastApplied(liveWith(desired, live), nil)
	if got == nil {
		t.Fatal("expected drift, got nil")
	}
	wantPaths := map[string]DriftOp{
		"spec.disruption.expireAfter":    DriftOpRemoved,
		"spec.disruption.budgets":        DriftOpAdded,
		"spec.template.spec.expireAfter": DriftOpAdded,
	}
	for _, e := range got.Entries {
		want, ok := wantPaths[e.Path]
		if !ok {
			continue
		}
		if e.Op != want {
			t.Errorf("path %s: op = %q, want %q", e.Path, e.Op, want)
		}
		delete(wantPaths, e.Path)
	}
	for path, op := range wantPaths {
		t.Errorf("missing expected entry: %s (op=%s); entries=%v", path, op, got.Entries)
	}
}

func TestComputeDrift_ScalarChange(t *testing.T) {
	desired := `{"spec":{"replicas":3}}`
	got := computeDriftFromLastApplied(liveWith(desired, map[string]any{"replicas": int64(5)}), nil)
	if got == nil || len(got.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %v", got)
	}
	e := got.Entries[0]
	if e.Path != "spec.replicas" || e.Op != DriftOpChanged {
		t.Errorf("entry = %+v, want path=spec.replicas op=changed", e)
	}
	if !strings.Contains(e.Desired, "3") || !strings.Contains(e.Live, "5") {
		t.Errorf("expected desired to contain 3 and live to contain 5, got desired=%q live=%q", e.Desired, e.Live)
	}
}

// TestComputeDrift_ArrayElementFieldChange pins that a single field changing
// inside one list element reads as that field's path — not a wall of
// serialized JSON for the whole array. This is the container-resources case:
// only spec.containers[0].resources.limits.memory changed.
func TestComputeDrift_ArrayElementFieldChange(t *testing.T) {
	desired := `{"spec":{"containers":[{"name":"app","image":"app:v1","resources":{"limits":{"memory":"128Mi"}}}]}}`
	live := map[string]any{
		"containers": []any{
			map[string]any{
				"name":      "app",
				"image":     "app:v1",
				"resources": map[string]any{"limits": map[string]any{"memory": "256Mi"}},
			},
		},
	}
	got := computeDriftFromLastApplied(liveWith(desired, live), nil)
	if got == nil || len(got.Entries) != 1 {
		t.Fatalf("expected exactly 1 entry, got %v", got)
	}
	e := got.Entries[0]
	if e.Path != "spec.containers.[0].resources.limits.memory" || e.Op != DriftOpChanged {
		t.Errorf("entry = %+v, want the specific nested array path", e)
	}
	if !strings.Contains(e.Desired, "128Mi") || !strings.Contains(e.Live, "256Mi") {
		t.Errorf("desired/live should hold just the changed scalar, got desired=%q live=%q", e.Desired, e.Live)
	}
}

// TestComputeDrift_ArrayLengthChange pins that adding a list element surfaces
// just the new element at its index, not a whole-array rewrite.
func TestComputeDrift_ArrayLengthChange(t *testing.T) {
	desired := `{"spec":{"ports":[{"port":80}]}}`
	live := map[string]any{
		"ports": []any{
			map[string]any{"port": int64(80)},
			map[string]any{"port": int64(443)},
		},
	}
	got := computeDriftFromLastApplied(liveWith(desired, live), nil)
	if got == nil || len(got.Entries) != 1 {
		t.Fatalf("expected 1 entry for the added element, got %v", got)
	}
	if e := got.Entries[0]; e.Path != "spec.ports.[1]" || e.Op != DriftOpAdded {
		t.Errorf("entry = %+v, want spec.ports.[1] added", e)
	}
}

func TestComputeDrift_TreatsEmptyAsNil(t *testing.T) {
	// Defaulted-in empty maps and arrays shouldn't show as drift.
	desired := `{"spec":{"a":{}}}`
	got := computeDriftFromLastApplied(liveWith(desired, map[string]any{"a": map[string]any{}}), nil)
	if got != nil {
		t.Errorf("empty map vs empty map should not produce drift, got %v", got.Entries)
	}
}

// TestComputeDrift_NilishCrossSide pins the cross-side behavior: "nil/empty
// on one side, real value on the other" emits a single entry at the parent
// path rather than recursing into the other side. This keeps the diff
// readable when an entire subtree was added or removed.
func TestComputeDrift_NilishCrossSide(t *testing.T) {
	cases := []struct {
		name        string
		desired     string
		live        map[string]any
		wantOp      DriftOp
		wantPath    string
		wantInValue string // substring expected in the populated side's JSON
	}{
		{
			name:        "empty desired map vs non-empty live map → added at parent path",
			desired:     `{"spec":{"a":{}}}`,
			live:        map[string]any{"a": map[string]any{"x": "1"}},
			wantOp:      DriftOpAdded,
			wantPath:    "spec.a",
			wantInValue: `"x"`,
		},
		{
			name:        "non-empty desired map vs empty live map → removed at parent path",
			desired:     `{"spec":{"a":{"x":"1"}}}`,
			live:        map[string]any{"a": map[string]any{}},
			wantOp:      DriftOpRemoved,
			wantPath:    "spec.a",
			wantInValue: `"x"`,
		},
		{
			name:        "missing-on-desired vs present-on-live → added at the closest containing path",
			desired:     `{"spec":{}}`,
			live:        map[string]any{"replicas": int64(3)},
			wantOp:      DriftOpAdded,
			wantPath:    "spec",
			wantInValue: `"replicas"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := computeDriftFromLastApplied(liveWith(tc.desired, tc.live), nil)
			if got == nil {
				t.Fatalf("expected drift, got nil")
			}
			var entry *DriftEntry
			for i := range got.Entries {
				if got.Entries[i].Path == tc.wantPath {
					entry = &got.Entries[i]
					break
				}
			}
			if entry == nil {
				t.Fatalf("expected entry at %s; got entries=%+v", tc.wantPath, got.Entries)
			}
			if entry.Op != tc.wantOp {
				t.Errorf("path %s: op = %q, want %q", tc.wantPath, entry.Op, tc.wantOp)
			}
			payload := entry.Live + entry.Desired
			if !strings.Contains(payload, tc.wantInValue) {
				t.Errorf("path %s: payload should contain %q; live=%q desired=%q", tc.wantPath, tc.wantInValue, entry.Live, entry.Desired)
			}
		})
	}
}

// TestComputeDrift_DeploymentDefaultsAreNotDrift pins the headline fix:
// when last-applied is a partial Deployment manifest, the K8s API server
// fills in defaults like progressDeadlineSeconds:600, strategy.RollingUpdate,
// container imagePullPolicy:IfNotPresent, dnsPolicy:ClusterFirst,
// restartPolicy:Always, schedulerName, terminationGracePeriodSeconds, etc.
// Before this fix, every one of those defaults surfaced as drift. With the
// scheme-defaulting pass applied to the desired side first, they cancel out.
func TestComputeDrift_DeploymentDefaultsAreNotDrift(t *testing.T) {
	// Minimal user-applied Deployment — exactly what kubectl/Argo would
	// write to last-applied-configuration from a partial manifest.
	desired := `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"guestbook-ui","namespace":"demo"},"spec":{"replicas":1,"selector":{"matchLabels":{"app":"guestbook-ui"}},"template":{"metadata":{"labels":{"app":"guestbook-ui"}},"spec":{"containers":[{"name":"guestbook-ui","image":"gcr.io/google-samples/gb-frontend:v5","ports":[{"containerPort":80}]}]}}}}`

	// Live Deployment as the API server stores it — same user spec plus
	// all the defaulted fields the user never set.
	live := map[string]any{
		"replicas":                int64(1),
		"progressDeadlineSeconds": int64(600),
		"revisionHistoryLimit":    int64(10),
		"strategy": map[string]any{
			"type": "RollingUpdate",
			"rollingUpdate": map[string]any{
				"maxSurge":       "25%",
				"maxUnavailable": "25%",
			},
		},
		"selector": map[string]any{
			"matchLabels": map[string]any{"app": "guestbook-ui"},
		},
		"template": map[string]any{
			"metadata": map[string]any{"labels": map[string]any{"app": "guestbook-ui"}},
			"spec": map[string]any{
				"containers": []any{
					map[string]any{
						"name":                     "guestbook-ui",
						"image":                    "gcr.io/google-samples/gb-frontend:v5",
						"imagePullPolicy":          "IfNotPresent",
						"terminationMessagePath":   "/dev/termination-log",
						"terminationMessagePolicy": "File",
						"resources":                map[string]any{},
						"ports": []any{
							map[string]any{
								"containerPort": int64(80),
								"protocol":      "TCP",
							},
						},
					},
				},
				"dnsPolicy":                     "ClusterFirst",
				"restartPolicy":                 "Always",
				"schedulerName":                 "default-scheduler",
				"securityContext":               map[string]any{},
				"terminationGracePeriodSeconds": int64(30),
			},
		},
	}

	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]any{
			"name":      "guestbook-ui",
			"namespace": "demo",
			"annotations": map[string]any{
				kubectlLastAppliedAnnotation: desired,
			},
		},
		"spec": live,
	}}

	got := computeDriftFromLastApplied(obj, nil)
	if got != nil && len(got.Entries) > 0 {
		// We expect no drift: every "live extra" field is an API server default
		// and should cancel out via applySchemeDefaults.
		t.Errorf("expected no drift on a partial Deployment manifest with only API server defaults filled in; got %d entries:\n%v", len(got.Entries), got.Entries)
	}
}

// TestFilterIgnoredPaths_PrefixMatch pins the ignoreDifferences filter
// semantics: an ignore on "/spec/replicas" strips exactly the matching
// entry; an ignore on a parent path strips everything underneath.
func TestFilterIgnoredPaths_PrefixMatch(t *testing.T) {
	entries := []DriftEntry{
		{Path: "spec.replicas", Op: DriftOpChanged},
		{Path: "spec.template.spec.containers", Op: DriftOpChanged},
		{Path: "spec.template.spec.dnsPolicy", Op: DriftOpAdded},
		{Path: "spec.strategy.type", Op: DriftOpChanged},
	}
	out := filterIgnoredPaths(entries, []string{
		"/spec/replicas",      // exact path
		"/spec/template/spec", // parent prefix
	})
	if len(out) != 1 {
		t.Fatalf("expected 1 entry after filter, got %d (%v)", len(out), out)
	}
	if out[0].Path != "spec.strategy.type" {
		t.Errorf("kept entry = %q, want spec.strategy.type", out[0].Path)
	}
}

// TestFilterIgnoredPaths_ArrayIndexPointer pins that an ignoreDifferences JSON
// pointer into a specific array index matches the bracketed drift path that
// array descent now emits — a rule on /spec/containers/0/image must strip
// spec.containers.[0].image.
func TestFilterIgnoredPaths_ArrayIndexPointer(t *testing.T) {
	entries := []DriftEntry{
		{Path: "spec.containers.[0].image", Op: DriftOpChanged},
		{Path: "spec.containers.[0].resources.limits.memory", Op: DriftOpChanged},
		{Path: "spec.replicas", Op: DriftOpChanged},
	}
	out := filterIgnoredPaths(entries, []string{"/spec/containers/0/image"})
	if len(out) != 2 {
		t.Fatalf("expected the indexed image entry stripped, got %d (%v)", len(out), out)
	}
	for _, e := range out {
		if e.Path == "spec.containers.[0].image" {
			t.Errorf("array-index ignore pointer failed to match bracketed drift path")
		}
	}
}

// TestFilterIgnoredPaths_PrefixDoesNotMatchPartialSegment pins that
// "/spec/replic" doesn't accidentally match "spec.replicas". Prefix
// matching must respect path segment boundaries.
func TestFilterIgnoredPaths_PrefixDoesNotMatchPartialSegment(t *testing.T) {
	entries := []DriftEntry{
		{Path: "spec.replicas", Op: DriftOpChanged},
	}
	out := filterIgnoredPaths(entries, []string{"/spec/replic"})
	if len(out) != 1 {
		t.Errorf("expected entry preserved (partial-segment ignore must not match); got %d (%v)", len(out), out)
	}
}

// TestParseArgoIgnoreDifferences_MatchesByGroupKindAndOptionalNameNs pins
// the scoping semantics: a rule with empty name/namespace matches every
// resource of the given group/kind; named/namespaced rules narrow.
func TestParseArgoIgnoreDifferences_MatchesByGroupKindAndOptionalNameNs(t *testing.T) {
	app := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"ignoreDifferences": []any{
				map[string]any{
					"group":        "apps",
					"kind":         "Deployment",
					"jsonPointers": []any{"/spec/replicas"},
				},
				map[string]any{
					"group":        "apps",
					"kind":         "Deployment",
					"name":         "specific-deploy",
					"namespace":    "ns",
					"jsonPointers": []any{"/spec/template/spec/dnsPolicy"},
				},
			},
		},
	}}
	ig := parseArgoIgnoreDifferences(app)
	// Broad rule matches any Deployment
	if got := ig.pointersFor(Ref{Group: "apps", Kind: "Deployment", Namespace: "other", Name: "anything"}); len(got) != 1 || got[0] != "/spec/replicas" {
		t.Errorf("broad rule should match any Deployment; got %v", got)
	}
	// Narrow rule applies only to the named resource — broad still applies too
	got := ig.pointersFor(Ref{Group: "apps", Kind: "Deployment", Namespace: "ns", Name: "specific-deploy"})
	if len(got) != 2 {
		t.Errorf("named match should pick up both rules; got %v", got)
	}
	// Different kind = no matches
	if got := ig.pointersFor(Ref{Group: "", Kind: "Service", Name: "x"}); got != nil {
		t.Errorf("Service shouldn't match Deployment rules; got %v", got)
	}
}

// TestComputeDrift_ServerAssignedFieldsNotDrift pins the kindServerAssignedPaths
// filter: Service.clusterIP/clusterIPs/ipFamilies/internalTrafficPolicy,
// Pod.nodeName, PVC.volumeName are all *runtime*-assigned and shouldn't
// surface as drift even though they're added on live. A regression to the
// filter (deleting a kind entry, or detaching it from the diff pipeline)
// would silently re-introduce these as "added" entries.
func TestComputeDrift_ServerAssignedFieldsNotDrift(t *testing.T) {
	cases := []struct {
		name     string
		kind     string
		desired  string
		liveSpec map[string]any
	}{
		{
			name:    "Service clusterIP, clusterIPs, ipFamilies, internalTrafficPolicy",
			kind:    "Service",
			desired: `{"apiVersion":"v1","kind":"Service","spec":{"type":"ClusterIP","ports":[{"port":80,"protocol":"TCP"}],"selector":{"app":"x"}}}`,
			liveSpec: map[string]any{
				"type":                  "ClusterIP",
				"sessionAffinity":       "None",
				"ipFamilyPolicy":        "SingleStack",
				"clusterIP":             "10.0.0.5",
				"clusterIPs":            []any{"10.0.0.5"},
				"ipFamilies":            []any{"IPv4"},
				"internalTrafficPolicy": "Cluster",
				"ports":                 []any{map[string]any{"port": int64(80), "protocol": "TCP"}},
				"selector":              map[string]any{"app": "x"},
			},
		},
		{
			name:    "Pod nodeName",
			kind:    "Pod",
			desired: `{"apiVersion":"v1","kind":"Pod","spec":{"containers":[{"name":"c","image":"nginx"}]}}`,
			liveSpec: map[string]any{
				"containers": []any{map[string]any{
					"name":                     "c",
					"image":                    "nginx",
					"imagePullPolicy":          "Always",
					"terminationMessagePath":   "/dev/termination-log",
					"terminationMessagePolicy": "File",
					"resources":                map[string]any{},
				}},
				"dnsPolicy":                     "ClusterFirst",
				"restartPolicy":                 "Always",
				"schedulerName":                 "default-scheduler",
				"securityContext":               map[string]any{},
				"terminationGracePeriodSeconds": int64(30),
				"nodeName":                      "worker-3",
			},
		},
		{
			name:    "PersistentVolumeClaim volumeName",
			kind:    "PersistentVolumeClaim",
			desired: `{"apiVersion":"v1","kind":"PersistentVolumeClaim","spec":{"accessModes":["ReadWriteOnce"],"resources":{"requests":{"storage":"1Gi"}}}}`,
			liveSpec: map[string]any{
				"accessModes": []any{"ReadWriteOnce"},
				"resources":   map[string]any{"requests": map[string]any{"storage": "1Gi"}},
				"volumeName":  "pvc-7a8b9c0d-1234",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obj := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "v1",
				"kind":       tc.kind,
				"metadata": map[string]any{
					"name":      "x",
					"namespace": "ns",
					"annotations": map[string]any{
						kubectlLastAppliedAnnotation: tc.desired,
					},
				},
				"spec": tc.liveSpec,
			}}
			got := computeDriftFromLastApplied(obj, nil)
			if got != nil && len(got.Entries) > 0 {
				t.Errorf("expected no drift on %s with only server-assigned fields differing; got %d entries:\n%v", tc.kind, len(got.Entries), got.Entries)
			}
		})
	}
}

// TestComputeDrift_AllKindDefaults_NoNoise covers each registered
// kindDefaulters entry. One happy-path test per kind, asserting "manifest
// with the user-required fields only, live with all the API server's
// defaults filled in" → no drift. A typo in any defaulter (e.g.,
// "OrderedReady " with a trailing space) fails the matching kind's case.
func TestComputeDrift_AllKindDefaults_NoNoise(t *testing.T) {
	containerLive := func(name string) map[string]any {
		return map[string]any{
			"name":                     name,
			"image":                    "nginx",
			"imagePullPolicy":          "Always",
			"terminationMessagePath":   "/dev/termination-log",
			"terminationMessagePolicy": "File",
			"resources":                map[string]any{},
		}
	}
	podSpecLive := func(restartPolicy string) map[string]any {
		return map[string]any{
			"containers":                    []any{containerLive("c")},
			"dnsPolicy":                     "ClusterFirst",
			"restartPolicy":                 restartPolicy,
			"schedulerName":                 "default-scheduler",
			"securityContext":               map[string]any{},
			"terminationGracePeriodSeconds": int64(30),
		}
	}
	// Pod template "metadata" gets dropped from comparison via isNilish
	// when both sides have only an empty labels map. Set labels on both
	// sides consistently for the workload kinds that ship them.
	templateLiveBare := func(restartPolicy string) map[string]any {
		return map[string]any{
			"metadata": map[string]any{},
			"spec":     podSpecLive(restartPolicy),
		}
	}
	templateLiveLabeled := func(restartPolicy string) map[string]any {
		return map[string]any{
			"metadata": map[string]any{"labels": map[string]any{"app": "x"}},
			"spec":     podSpecLive(restartPolicy),
		}
	}

	cases := []struct {
		kind     string
		desired  string
		liveSpec map[string]any
	}{
		{
			kind:    "StatefulSet",
			desired: `{"apiVersion":"apps/v1","kind":"StatefulSet","spec":{"selector":{"matchLabels":{"app":"x"}},"serviceName":"x","template":{"metadata":{"labels":{"app":"x"}},"spec":{"containers":[{"name":"c","image":"nginx"}]}}}}`,
			liveSpec: map[string]any{
				"replicas":             int64(1),
				"revisionHistoryLimit": int64(10),
				"podManagementPolicy":  "OrderedReady",
				"updateStrategy":       map[string]any{"type": "RollingUpdate"},
				"selector":             map[string]any{"matchLabels": map[string]any{"app": "x"}},
				"serviceName":          "x",
				"template":             templateLiveLabeled("Always"),
			},
		},
		{
			kind:    "DaemonSet",
			desired: `{"apiVersion":"apps/v1","kind":"DaemonSet","spec":{"selector":{"matchLabels":{"app":"x"}},"template":{"metadata":{"labels":{"app":"x"}},"spec":{"containers":[{"name":"c","image":"nginx"}]}}}}`,
			liveSpec: map[string]any{
				"revisionHistoryLimit": int64(10),
				"updateStrategy": map[string]any{
					"type":          "RollingUpdate",
					"rollingUpdate": map[string]any{"maxUnavailable": int64(1)},
				},
				"selector": map[string]any{"matchLabels": map[string]any{"app": "x"}},
				"template": templateLiveLabeled("Always"),
			},
		},
		{
			kind:    "ReplicaSet",
			desired: `{"apiVersion":"apps/v1","kind":"ReplicaSet","spec":{"selector":{"matchLabels":{"app":"x"}},"template":{"metadata":{"labels":{"app":"x"}},"spec":{"containers":[{"name":"c","image":"nginx"}]}}}}`,
			liveSpec: map[string]any{
				"replicas": int64(1),
				"selector": map[string]any{"matchLabels": map[string]any{"app": "x"}},
				"template": templateLiveLabeled("Always"),
			},
		},
		{
			kind:    "Job",
			desired: `{"apiVersion":"batch/v1","kind":"Job","spec":{"template":{"metadata":{},"spec":{"containers":[{"name":"c","image":"nginx"}],"restartPolicy":"Never"}}}}`,
			liveSpec: map[string]any{
				"backoffLimit":   int64(6),
				"completionMode": "NonIndexed",
				"template":       templateLiveBare("Never"),
			},
		},
		{
			kind:    "CronJob",
			desired: `{"apiVersion":"batch/v1","kind":"CronJob","spec":{"schedule":"* * * * *","jobTemplate":{"spec":{"template":{"metadata":{},"spec":{"containers":[{"name":"c","image":"nginx"}],"restartPolicy":"Never"}}}}}}`,
			liveSpec: map[string]any{
				"schedule":                   "* * * * *",
				"concurrencyPolicy":          "Allow",
				"failedJobsHistoryLimit":     int64(1),
				"successfulJobsHistoryLimit": int64(3),
				"suspend":                    false,
				"jobTemplate": map[string]any{
					"spec": map[string]any{
						"backoffLimit":   int64(6),
						"completionMode": "NonIndexed",
						"template":       templateLiveBare("Never"),
					},
				},
			},
		},
		{
			kind:     "Pod",
			desired:  `{"apiVersion":"v1","kind":"Pod","spec":{"containers":[{"name":"c","image":"nginx"}]}}`,
			liveSpec: podSpecLive("Always"),
		},
		{
			kind:    "Service",
			desired: `{"apiVersion":"v1","kind":"Service","spec":{"ports":[{"port":80}],"selector":{"app":"x"}}}`,
			liveSpec: map[string]any{
				"type":            "ClusterIP",
				"sessionAffinity": "None",
				"ipFamilyPolicy":  "SingleStack",
				"ports":           []any{map[string]any{"port": int64(80), "protocol": "TCP"}},
				"selector":        map[string]any{"app": "x"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			obj := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "test/v1",
				"kind":       tc.kind,
				"metadata": map[string]any{
					"name":      "x",
					"namespace": "ns",
					"annotations": map[string]any{
						kubectlLastAppliedAnnotation: tc.desired,
					},
				},
				"spec": tc.liveSpec,
			}}
			got := computeDriftFromLastApplied(obj, nil)
			if got != nil && len(got.Entries) > 0 {
				t.Errorf("expected no drift on %s with only API server defaults filled in; got %d entries:\n%v", tc.kind, len(got.Entries), got.Entries)
			}
		})
	}
}

func TestDefaultImagePullPolicyMatchesKubernetesTagRules(t *testing.T) {
	cases := []struct {
		image string
		want  string
	}{
		{image: "nginx", want: "Always"},
		{image: "nginx:latest", want: "Always"},
		{image: "nginx:1.27", want: "IfNotPresent"},
		{image: "registry.example.com:5000/ns/app:1.0", want: "IfNotPresent"},
		{image: "registry.example.com:5000/ns/app", want: "Always"},
		{image: "nginx@sha256:abcdef", want: "Always"},
	}
	for _, tc := range cases {
		t.Run(tc.image, func(t *testing.T) {
			if got := defaultImagePullPolicy(tc.image); got != tc.want {
				t.Fatalf("defaultImagePullPolicy(%q) = %q, want %q", tc.image, got, tc.want)
			}
		})
	}
}

// TestArgoIgnoreRule_MatchesWildcards pins the empty-string-as-wildcard
// semantics on each scope field. The previous implementation required
// non-empty group/kind and would skip every rule that omitted them,
// breaking the Argo-compatible "ignore field X across all kinds" pattern.
func TestArgoIgnoreRule_MatchesWildcards(t *testing.T) {
	cases := []struct {
		name string
		rule argoIgnoreRule
		ref  Ref
		want bool
	}{
		{
			name: "empty group wildcards group",
			rule: argoIgnoreRule{kind: "Deployment"},
			ref:  Ref{Group: "apps", Kind: "Deployment", Namespace: "x", Name: "y"},
			want: true,
		},
		{
			name: "empty kind wildcards kind",
			rule: argoIgnoreRule{group: "apps"},
			ref:  Ref{Group: "apps", Kind: "StatefulSet", Namespace: "x", Name: "y"},
			want: true,
		},
		{
			name: "all empty matches everything",
			rule: argoIgnoreRule{},
			ref:  Ref{Group: "", Kind: "Pod", Namespace: "x", Name: "y"},
			want: true,
		},
		{
			name: "non-matching kind is rejected",
			rule: argoIgnoreRule{group: "apps", kind: "Deployment"},
			ref:  Ref{Group: "apps", Kind: "StatefulSet"},
			want: false,
		},
		{
			name: "named rule narrows to one resource",
			rule: argoIgnoreRule{kind: "Deployment", name: "specific"},
			ref:  Ref{Kind: "Deployment", Name: "other"},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.rule.matches(tc.ref); got != tc.want {
				t.Errorf("matches(%+v) = %v, want %v", tc.ref, got, tc.want)
			}
		})
	}
}
