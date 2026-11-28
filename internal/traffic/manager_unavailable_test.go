package traffic

import (
	"context"
	"strings"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/pkg/traffic"
)

// stubSource is a TrafficSource whose detection outcome the test dictates.
type stubSource struct {
	name   string
	result *DetectionResult
}

func (s *stubSource) Name() string { return s.name }

func (s *stubSource) Detect(context.Context) (*DetectionResult, error) { return s.result, nil }

func (s *stubSource) GetFlows(context.Context, FlowOptions) (*FlowsResponse, error) {
	return &FlowsResponse{Source: s.name, Flows: []Flow{}}, nil
}

func (s *stubSource) StreamFlows(context.Context, FlowOptions) (<-chan Flow, error) {
	ch := make(chan Flow)
	close(ch)
	return ch, nil
}

func (s *stubSource) Close() error { return nil }

// A source can be unavailable for a reason the user can act on: installed but
// with the wrong feature enabled, running but not scraped. Reporting only its
// name makes every such case indistinguishable from "not installed", so the
// explanation the source produced has to reach the response.
func TestDetectSources_CarriesWhyAnUnavailableSourceIsUnavailable(t *testing.T) {
	const reason = "Beyla is running but exposes no network flow metrics. " +
		`Add "network" to OTEL_EBPF_METRICS_FEATURES to enable them.`

	m := &Manager{k8sClient: fake.NewSimpleClientset(), sources: map[string]TrafficSource{
		"beyla": &stubSource{name: "beyla", result: &DetectionResult{
			Available: false,
			Present:   true,
			Version:   "v3.25.0",
			Message:   reason,
		}},
		"caretta": &stubSource{name: "caretta", result: &DetectionResult{
			Available: false,
			Message:   "Caretta not detected. Install Caretta for eBPF-based traffic visibility.",
		}},
	}}

	response, err := m.DetectSources(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// A source that is merely absent keeps its bare name, as before.
	if len(response.NotDetected) != 1 || response.NotDetected[0] != "caretta" {
		t.Fatalf("notDetected = %v, want just the absent source", response.NotDetected)
	}

	// The present-but-unusable one is a status with an explanation, reported the
	// same way a detection error is.
	var beyla *traffic.SourceStatus
	for i := range response.Detected {
		if response.Detected[i].Name == "beyla" {
			beyla = &response.Detected[i]
		}
	}
	if beyla == nil {
		t.Fatal("beyla's explanation was dropped; the user would see only 'not detected'")
	}
	if !strings.Contains(beyla.Message, "OTEL_EBPF_METRICS_FEATURES") {
		t.Errorf("message should say how to fix it, got: %q", beyla.Message)
	}
	assertEq(t, "status", beyla.Status, "not_found")
	assertEq(t, "version", beyla.Version, "v3.25.0")

	// Caretta is not installed at all. "Install Caretta" is what the
	// recommendation section is for; repeating it as a per-source problem would
	// bury the one source that does have a fixable problem.
	for _, s := range response.Detected {
		if s.Name == "caretta" {
			t.Errorf("a source that is merely absent must not be reported as unusable: %q", s.Message)
		}
	}

	// The point of putting it in Detected is that it must not read as usable. Both
	// halves are checked on their own: an AND of the two would pass whenever either
	// held, which is every run of this test.
	if response.Active != "" {
		t.Errorf("a not_found status must not make a source active, got %q", response.Active)
	}
	for _, s := range response.Detected {
		if s.Status == "available" {
			t.Errorf("%s is not available and must not be listed as such", s.Name)
		}
	}
	// generateRecommendation returns nil as soon as anything looks available, and
	// falls through to Caretta otherwise. Nothing here is available, so a nil
	// recommendation would mean not_found had been read as available.
	if response.Recommended == nil {
		t.Error("no source is usable, so a recommendation must still be offered")
	}
}

// A source that stops being available must stop being active. Detection only ever
// promoted, so a cluster whose source went away kept reporting that source as
// active while the flows endpoint answered 200 with an empty graph — the one
// outcome with no explanation attached to it.
func TestDetectSources_DropsAnActiveSourceThatStoppedBeingAvailable(t *testing.T) {
	beyla := &stubSource{name: "beyla", result: &DetectionResult{Available: true, Present: true}}
	m := &Manager{
		k8sClient: fake.NewSimpleClientset(),
		sources:   map[string]TrafficSource{"beyla": beyla},
	}

	if _, err := m.DetectSources(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.GetActiveSourceName() != "beyla" {
		t.Fatalf("an available source must become active, got %q", m.GetActiveSourceName())
	}

	// The network feature is turned off underneath a running Radar.
	beyla.result = &DetectionResult{
		Available: false,
		Present:   true,
		Message:   "Beyla is running, but Prometheus holds no network flow metrics for it.",
	}

	response, err := m.DetectSources(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if response.Active != "" {
		t.Errorf("response must not report an unavailable source as active, got %q", response.Active)
	}
	if name := m.GetActiveSourceName(); name != "" {
		t.Errorf("active source must be cleared, got %q", name)
	}
	// And the flows endpoint must say so rather than serving an unexplained blank.
	if _, err := m.GetFlows(context.Background(), FlowOptions{}); err == nil {
		t.Error("GetFlows must report that no source is available, not return an empty graph")
	}
}

// Losing a lower-priority source must not strand the user when a working one is
// still there.
func TestDetectSources_FailsOverToAnotherAvailableSource(t *testing.T) {
	hubble := &stubSource{name: "hubble", result: &DetectionResult{Available: true, Present: true}}
	beyla := &stubSource{name: "beyla", result: &DetectionResult{Available: true, Present: true}}
	m := &Manager{
		k8sClient: fake.NewSimpleClientset(),
		sources:   map[string]TrafficSource{"hubble": hubble, "beyla": beyla},
	}

	if _, err := m.DetectSources(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.GetActiveSourceName() != "hubble" {
		t.Fatalf("priority order must pick hubble, got %q", m.GetActiveSourceName())
	}

	hubble.result = &DetectionResult{Available: false, Present: true, Message: "gone"}
	if _, err := m.DetectSources(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name := m.GetActiveSourceName(); name != "beyla" {
		t.Errorf("must fall over to the remaining available source, got %q", name)
	}
}

func TestDetectSources_KeepsPreferenceWhenNothingIsAvailable(t *testing.T) {
	hubble := &stubSource{name: "hubble", result: &DetectionResult{}}
	beyla := &stubSource{name: "beyla", result: &DetectionResult{}}
	m := &Manager{
		k8sClient: fake.NewSimpleClientset(),
		sources:   map[string]TrafficSource{"hubble": hubble, "beyla": beyla},
	}
	m.PreferSource("beyla")

	if response, err := m.DetectSources(t.Context()); err != nil || response.Active != "" {
		t.Fatalf("empty detection = %+v, %v", response, err)
	}
	if got := m.SourcePreference(); got != "beyla" {
		t.Fatalf("preference after empty detection = %q, want beyla", got)
	}

	hubble.result = &DetectionResult{Available: true, Present: true}
	beyla.result = &DetectionResult{Available: true, Present: true}
	if response, err := m.DetectSources(t.Context()); err != nil || response.Active != "beyla" {
		t.Fatalf("detection after sources became available = %+v, %v, want beyla", response, err)
	}
}
