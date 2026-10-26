package context

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/skyhook-io/radar/pkg/resourcecontext"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestCrossplanePauseSummary(t *testing.T) {
	shapes := map[string]map[string]any{
		"top-level provider reference":  {"providerConfigRef": map[string]any{"name": "default"}},
		"nested provider reference":     {"crossplane": map[string]any{"providerConfigRef": map[string]any{"name": "default"}}},
		"top-level composed references": {"resourceRefs": []any{}},
		"nested composed references":    {"crossplane": map[string]any{"resourceRefs": []any{}}},
		"bound claim reference":         {"resourceRef": map[string]any{"name": "bound"}, "compositionRef": map[string]any{"name": "example"}},
		"unbound claim":                 {},
	}
	for name, spec := range shapes {
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				name, annotation, ready, synced, reason string
				want                                    resourcecontext.CrossplanePauseState
				issue                                   string
			}{
				{"ack ready", "true", "True", "False", "ReconcilePaused", resourcecontext.CrossplanePauseAcknowledged, ""},
				{"ack unavailable", "true", "False", "False", "ReconcilePaused", resourcecontext.CrossplanePauseAcknowledged, ""},
				{"independent sync error", "true", "True", "False", "ReconcileError", resourcecontext.CrossplanePauseRequested, "ReconcileError"},
				{"independent sync no reason", "true", "True", "False", "", resourcecontext.CrossplanePauseRequested, "NotSynced"},
				{"intent only", "true", "", "", "", resourcecontext.CrossplanePauseRequested, ""},
				{"not exact", "True", "True", "False", "ReconcilePaused", "", ""},
				{"no intent", "", "True", "False", "ReconcilePaused", "", ""},
			} {
				t.Run(tc.name, func(t *testing.T) {
					conds := []any{}
					if tc.ready != "" {
						conds = append(conds, map[string]any{"type": "Ready", "status": tc.ready, "reason": "AvailabilityReason"})
					}
					if tc.synced != "" {
						conds = append(conds, map[string]any{"type": "Synced", "status": tc.synced, "reason": tc.reason})
					}
					u := &unstructured.Unstructured{Object: map[string]any{
						"apiVersion": "example.org/v1", "kind": "Widget",
						"metadata": map[string]any{"name": "example", "annotations": map[string]any{"crossplane.io/paused": tc.annotation}},
						"spec":     spec, "status": map[string]any{"conditions": conds},
					}}
					before := u.DeepCopy()
					dynamic := MinifyUnstructured(u, LevelSummary).(*ResourceSummary)
					typed, err := Minify(u, LevelSummary)
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(dynamic, typed) {
						t.Fatalf("runtime wrapper differs: %+v vs %+v", dynamic, typed)
					}
					if dynamic.CrossplanePause != tc.want || dynamic.Issue != tc.issue {
						t.Fatalf("summary=%+v", dynamic)
					}
					if tc.want != "" && dynamic.Ready != tc.ready {
						t.Fatalf("recorded Ready=%q want %q", dynamic.Ready, tc.ready)
					}
					if tc.ready == "False" && dynamic.Status != "AvailabilityReason" {
						t.Fatalf("failure lost: %+v", dynamic)
					}
					if tc.ready == "True" && dynamic.Status != "Ready" {
						t.Fatalf("readiness lost: %+v", dynamic)
					}
					if dynamic.SummaryContext != nil || dynamic.Suspended != nil {
						t.Fatalf("bare summary invented context/suspended: %+v", dynamic)
					}
					if !reflect.DeepEqual(before, u) {
						t.Fatal("source mutated")
					}
				})
			}
		})
	}
}

func TestCrossplanePauseCompactAndLifecycle(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.org/v1", "kind": "Widget",
		"metadata": map[string]any{"name": "example", "annotations": map[string]any{"crossplane.io/paused": "true", "crossplane.io/external-name": "private-id"}, "deletionTimestamp": "2026-10-26T09:00:00Z", "finalizers": []any{"example.org/cleanup"}},
		"status":   map[string]any{"conditions": []any{map[string]any{"type": "Synced", "status": "False", "reason": "ReconcilePaused"}}},
	}}
	s := MinifyUnstructured(u, LevelSummary).(*ResourceSummary)
	if !s.Terminating || len(s.Finalizers) != 1 || s.CrossplanePause != resourcecontext.CrossplanePauseAcknowledged {
		t.Fatalf("deletion context=%+v", s)
	}
	if s.Ready != "" || s.Issue != "" || s.Status != "ReconcilePaused" {
		t.Fatalf("unreported readiness invented a failure or readiness value: %+v", s)
	}
	for _, level := range []VerbosityLevel{LevelCompact, LevelDetail} {
		result := MinifyUnstructured(u, level).(map[string]any)
		annotations, _, _ := unstructured.NestedStringMap(result, "metadata", "annotations")
		if annotations["crossplane.io/paused"] != "true" {
			t.Fatalf("level %v dropped intent", level)
		}
		if level == LevelCompact && annotations["crossplane.io/external-name"] != "" {
			t.Fatal("broad Crossplane prefix retained")
		}
		conditions, _, _ := unstructured.NestedSlice(result, "status", "conditions")
		if !reflect.DeepEqual(conditions, u.Object["status"].(map[string]any)["conditions"]) {
			t.Fatal("raw condition rewritten")
		}
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{"crossplane.io/paused": "true"}}, Data: map[string][]byte{"token": []byte("must-not-leak")}, StringData: map[string]string{"password": "must-not-leak"}}
	result, err := Minify(secret, LevelCompact)
	if err != nil {
		t.Fatal(err)
	}
	wire, _ := json.Marshal(result)
	var fields map[string]any
	_ = json.Unmarshal(wire, &fields)
	if fields["data"] != nil || fields["stringData"] != nil {
		t.Fatalf("secret leaked: %s", wire)
	}
}

func TestSummaryRuntimeUnstructuredCuratedAndNil(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "keda.sh/v1alpha1", "kind": "ScaledObject", "metadata": map[string]any{"name": "worker"}, "spec": map[string]any{"scaleTargetRef": map[string]any{"name": "queue-worker"}}}}
	got, err := Minify(u, LevelSummary)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, MinifyUnstructured(u, LevelSummary)) {
		t.Fatal("curated KEDA extractor parity lost")
	}
	if got.(*ResourceSummary).Target != "Deployment (apps)/queue-worker" {
		t.Fatalf("curated extraction missing: %+v", got)
	}
	var nilResource *unstructured.Unstructured
	got, err = Minify(nilResource, LevelSummary)
	if err != nil || got.(*ResourceSummary).Kind != "Unknown" {
		t.Fatalf("nil=%+v,%v", got, err)
	}
	if MinifyUnstructured(nilResource, LevelSummary).(*ResourceSummary).Kind != "Unknown" {
		t.Fatal("unstructured nil parity")
	}
}

func TestCrossplanePauseDoesNotRewriteCuratedSummary(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Rollout",
		"metadata": map[string]any{"name": "canary"},
		"spec":     map[string]any{"replicas": int64(3)},
		"status": map[string]any{"phase": "Paused", "readyReplicas": int64(2), "abort": true, "conditions": []any{
			map[string]any{"type": "Ready", "status": "True"},
			map[string]any{"type": "Synced", "status": "False", "reason": "ReconcileError"},
		}},
	}}
	baseline := MinifyUnstructured(u, LevelSummary).(*ResourceSummary)
	if baseline.Ready != "2/3" || baseline.Issue == "" {
		t.Fatalf("curated fixture missing readiness/error: %+v", baseline)
	}
	u.SetAnnotations(map[string]string{"crossplane.io/paused": "true"})
	got := MinifyUnstructured(u, LevelSummary).(*ResourceSummary)
	if got.Ready != baseline.Ready || got.Issue != baseline.Issue || got.Status != baseline.Status || got.CrossplanePause != resourcecontext.CrossplanePauseRequested {
		t.Fatalf("curated facts rewritten: before=%+v after=%+v", baseline, got)
	}
}

func TestCrossplanePausePreservesTopLevelFactsWithAncestorStatus(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.org/v1", "kind": "Policy",
		"metadata": map[string]any{"name": "mixed-status"},
		"status": map[string]any{
			"conditions": []any{
				map[string]any{"type": "Ready", "status": "True"},
				map[string]any{"type": "Synced", "status": "False", "reason": "ReconcileError"},
			},
			"ancestors": []any{map[string]any{
				"ancestorRef": map[string]any{"group": "gateway.networking.k8s.io", "kind": "Gateway", "name": "edge"},
				"conditions":  []any{map[string]any{"type": "Accepted", "status": "True"}},
			}},
		},
	}}
	baseline := MinifyUnstructured(u, LevelSummary).(*ResourceSummary)
	if baseline.Status != "Accepted" {
		t.Fatalf("ancestor fixture missing status: %+v", baseline)
	}
	u.SetAnnotations(map[string]string{"crossplane.io/paused": "true"})
	got := MinifyUnstructured(u, LevelSummary).(*ResourceSummary)
	if got.Status != baseline.Status || got.Ready != "True" || got.Issue != "ReconcileError" || got.CrossplanePause != resourcecontext.CrossplanePauseRequested {
		t.Fatalf("top-level facts lost beside ancestor status: %+v", got)
	}
}
