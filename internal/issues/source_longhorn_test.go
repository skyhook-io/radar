package issues

import (
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/issuesapi"
)

var longhornVolumesGVR = schema.GroupVersionResource{Group: "longhorn.io", Version: "v1beta2", Resource: "volumes"}

func longhornVolume(name string, status map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "longhorn.io/v1beta2", "kind": "Volume",
		"metadata": map[string]any{"name": name, "namespace": "longhorn-system", "creationTimestamp": "2026-11-01T00:00:00Z"},
		"status":   status,
	}}
	return u
}

func TestLonghornVolumeIssues(t *testing.T) {
	degradedAt := time.Now().Add(-90 * time.Minute).UTC().Format(time.RFC3339)
	got := detectLonghornVolumeIssues(longhornVolumesGVR, "Volume", []*unstructured.Unstructured{
		longhornVolume("v-faulted", map[string]any{"state": "detached", "robustness": "faulted"}),
		longhornVolume("v-degraded", map[string]any{"state": "attached", "robustness": "degraded", "lastDegradedAt": degradedAt}),
		longhornVolume("v-detached", map[string]any{"state": "detached", "robustness": "unknown"}),
		longhornVolume("v-healthy", map[string]any{"state": "attached", "robustness": "healthy"}),
	})
	byName := map[string]Issue{}
	for _, iss := range got {
		byName[iss.Name] = iss
	}
	if len(got) != 2 {
		t.Fatalf("got %d issues %v, want faulted and degraded only", len(got), byName)
	}
	f := byName["v-faulted"]
	if f.Severity != SeverityCritical || f.Reason != ReasonLonghornVolumeFaulted {
		t.Errorf("faulted = %s/%s, want critical/%s", f.Severity, f.Reason, ReasonLonghornVolumeFaulted)
	}
	d := byName["v-degraded"]
	if d.Severity != SeverityWarning || d.Reason != ReasonLonghornVolumeDegraded {
		t.Errorf("degraded = %s/%s, want warning/%s", d.Severity, d.Reason, ReasonLonghornVolumeDegraded)
	}
	// Longhorn reports degraded whenever replicas are short, including when it
	// can't place a new one, so the Issue mustn't claim a rebuild is running.
	if strings.Contains(d.Message, "is rebuilding") {
		t.Errorf("degraded message %q claims a rebuild in progress", d.Message)
	}
	for _, g := range issuesapi.Catalog() {
		for _, c := range g.Categories {
			if c.Category == issuesapi.CategoryVolumeDegraded && strings.Contains(c.Description, "is rebuilding") {
				t.Errorf("catalog text %q claims a rebuild in progress", c.Description)
			}
		}
	}
	if d.OnsetUnknown || time.Since(d.FirstSeen) < 80*time.Minute {
		t.Errorf("degraded onset = %v (unknown=%v), want lastDegradedAt", d.FirstSeen, d.OnsetUnknown)
	}
	if got := Classify(classifyInput{Source: SourceCondition, Reason: f.Reason, Kind: "Volume", APIGroup: "longhorn.io"}); got != issuesapi.CategoryVolumeFaulted {
		t.Errorf("faulted category = %s, want %s", got, issuesapi.CategoryVolumeFaulted)
	}
	if got := Classify(classifyInput{Source: SourceCondition, Reason: d.Reason, Kind: "Volume", APIGroup: "longhorn.io"}); got != issuesapi.CategoryVolumeDegraded {
		t.Errorf("degraded category = %s, want %s", got, issuesapi.CategoryVolumeDegraded)
	}
}
