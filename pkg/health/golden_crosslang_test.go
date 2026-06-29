package health

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
)

// goldenVector is one (object, expected-level) pair in the shared cross-language
// contract. The same testdata/golden_vectors.json is loaded by the frontend's
// vitest (packages/k8s-ui/src/__tests__/health-golden.test.ts) so the TS table
// classifiers can't drift from pkg/health, which is the source of truth.
type goldenVector struct {
	Name   string          `json:"name"`
	Kind   string          `json:"kind"`
	Level  string          `json:"level"`
	Object json.RawMessage `json:"object"`
}

// TestGoldenVectorsCrossLang pins that pkg/health produces the level recorded in
// the shared fixture for each object. The display/wire level is what the frontend
// renders, so pods go through PodDisplayLevel (folds unschedulable/terminating)
// and workloads through Workload. Keep this in lockstep with the vitest mirror.
func TestGoldenVectorsCrossLang(t *testing.T) {
	raw, err := os.ReadFile("testdata/golden_vectors.json")
	if err != nil {
		t.Fatalf("read golden vectors: %v", err)
	}
	var file struct {
		Vectors []goldenVector `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parse golden vectors: %v", err)
	}
	if len(file.Vectors) == 0 {
		t.Fatal("no golden vectors loaded")
	}

	// Fixed clock — every vector is time-independent (see the fixture comment), so
	// the exact value is irrelevant, but pinning it keeps the run reproducible.
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)

	for _, v := range file.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			got := classifyGolden(t, v.Kind, v.Object, now)
			if string(got) != v.Level {
				t.Errorf("%s [%s]: got level %q, want %q", v.Name, v.Kind, got, v.Level)
			}
		})
	}
}

func classifyGolden(t *testing.T, kind string, obj json.RawMessage, now time.Time) Level {
	t.Helper()
	switch kind {
	case "Pod":
		var pod corev1.Pod
		mustUnmarshal(t, obj, &pod)
		return PodDisplayLevel(&pod, now)
	case "Deployment":
		var d appsv1.Deployment
		mustUnmarshal(t, obj, &d)
		return Workload(&d, now).Level
	case "StatefulSet":
		var s appsv1.StatefulSet
		mustUnmarshal(t, obj, &s)
		return Workload(&s, now).Level
	case "DaemonSet":
		var ds appsv1.DaemonSet
		mustUnmarshal(t, obj, &ds)
		return Workload(&ds, now).Level
	case "Job":
		var j batchv1.Job
		mustUnmarshal(t, obj, &j)
		return Workload(&j, now).Level
	case "CronJob":
		var cj batchv1.CronJob
		mustUnmarshal(t, obj, &cj)
		return Workload(&cj, now).Level
	case "PersistentVolumeClaim":
		var pvc corev1.PersistentVolumeClaim
		mustUnmarshal(t, obj, &pvc)
		return Workload(&pvc, now).Level
	default:
		t.Fatalf("golden vector kind %q has no Go classifier mapping", kind)
		return LevelUnknown
	}
}

func mustUnmarshal(t *testing.T, raw json.RawMessage, into any) {
	t.Helper()
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("unmarshal object: %v", err)
	}
}
