package traffic

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"
)

// countingSource counts its detections, so a test can tell a remembered
// answer from a fresh probe.
type countingSource struct {
	stubSource
	detections int
}

func (s *countingSource) Detect(ctx context.Context) (*DetectionResult, error) {
	s.detections++
	return s.stubSource.Detect(ctx)
}

func TestRecentSources_ReusesADetectionUntilItIsStale(t *testing.T) {
	source := &countingSource{stubSource: stubSource{name: "hubble", result: &DetectionResult{Available: true}}}
	m := &Manager{k8sClient: fake.NewSimpleClientset(), sources: map[string]TrafficSource{"hubble": source}}
	ctx := context.Background()

	first, err := m.RecentSources(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if source.detections != 1 || len(first.Detected) != 1 || first.Detected[0].Status != "available" {
		t.Fatalf("first call should detect: detections=%d detected=%v", source.detections, first.Detected)
	}
	if first.Active != "hubble" {
		t.Fatalf("active = %q, want hubble", first.Active)
	}

	// A copy, so a caller changing the answer can't change the remembered one.
	first.Detected[0].Status = "changed"
	second, _ := m.RecentSources(ctx, time.Minute)
	if source.detections != 1 {
		t.Fatalf("a recent detection should be reused, detections=%d", source.detections)
	}
	if second.Detected[0].Status != "available" {
		t.Fatalf("remembered answer was modified through a returned copy: %v", second.Detected)
	}

	// DetectSources always probes, and refreshes what is remembered.
	if _, err := m.DetectSources(ctx); err != nil {
		t.Fatal(err)
	}
	if source.detections != 2 {
		t.Fatalf("DetectSources should probe, detections=%d", source.detections)
	}

	// Too old: detect again.
	m.mu.Lock()
	m.lastDetectedAt = time.Now().Add(-2 * time.Minute)
	m.mu.Unlock()
	if _, err := m.RecentSources(ctx, time.Minute); err != nil {
		t.Fatal(err)
	}
	if source.detections != 3 {
		t.Fatalf("a stale detection should be replaced, detections=%d", source.detections)
	}

	// Metrics settings decide what Caretta and Beyla find, so changing them
	// makes the remembered answer stale however recent it is.
	SetBeylaJobSelector(`job=~".*beyla.*"`)
	t.Cleanup(func() { SetBeylaJobSelector("") })
	if _, err := m.RecentSources(ctx, time.Minute); err != nil {
		t.Fatal(err)
	}
	if source.detections != 4 {
		t.Fatalf("a metrics settings change should force detection, detections=%d", source.detections)
	}
}

// The response says "nothing detected" as an empty list; a remembered copy of
// it must not turn into null, which the Traffic view reads as a list.
func TestRecentSources_KeepsAnEmptyDetectionAList(t *testing.T) {
	m := &Manager{k8sClient: fake.NewSimpleClientset(), sources: map[string]TrafficSource{
		"hubble": &stubSource{name: "hubble", result: &DetectionResult{Available: false}},
	}}
	if _, err := m.DetectSources(context.Background()); err != nil {
		t.Fatal(err)
	}
	recent, err := m.RecentSources(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(recent)
	if !strings.Contains(string(body), `"detected":[]`) {
		t.Fatalf("detected should be an empty list, got %s", body)
	}
}
