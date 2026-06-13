package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
)

func TestTruncateLargeConfigMapData(t *testing.T) {
	small := map[string]any{"data": map[string]any{"k": "v"}}
	if _, note := truncateLargeConfigMapData(small); note != "" {
		t.Fatalf("small ConfigMap must pass untouched, got note %q", note)
	}

	big := strings.Repeat("x", 20*1024)
	payload := map[string]any{"data": map[string]any{"init.sql": big, "small": "v"}}
	out, note := truncateLargeConfigMapData(payload)
	if note == "" {
		t.Fatal("large ConfigMap must produce a truncation note")
	}
	data := out.(map[string]any)["data"].(map[string]any)
	v := data["init.sql"].(string)
	if len(v) >= len(big) {
		t.Fatalf("value not truncated: %d bytes", len(v))
	}
	if !strings.Contains(v, "[truncated by radar:") {
		t.Fatalf("truncated value lacks the explicit marker: %q", v[len(v)-100:])
	}
	if data["small"] != "v" {
		t.Fatalf("small value must be untouched, got %v", data["small"])
	}
	if !strings.Contains(note, "init.sql") {
		t.Fatalf("note must name truncated keys: %q", note)
	}

	// Non-map payloads pass through.
	if _, note := truncateLargeConfigMapData("raw"); note != "" {
		t.Fatal("non-map payload must pass through")
	}
}

// Many medium values can blow the total budget without any single value
// crossing the 8KB per-value cap — the guard must tighten the cap so the
// payload still shrinks toward the budget instead of passing through whole.
func TestTruncateLargeConfigMapData_ManyMediumValues(t *testing.T) {
	data := map[string]any{}
	for i := 0; i < 10; i++ {
		data[fmt.Sprintf("part-%d", i)] = strings.Repeat("x", 3*1024)
	}
	out, note := truncateLargeConfigMapData(map[string]any{"data": data})
	if note == "" {
		t.Fatal("30KB of medium values must trigger the guard")
	}
	got := out.(map[string]any)["data"].(map[string]any)
	totalAfter := 0
	for k, v := range got {
		s := v.(string)
		totalAfter += len(s)
		if !strings.Contains(s, "[truncated by radar:") {
			t.Fatalf("value %s not truncated: %d bytes", k, len(s))
		}
	}
	// 10 values capped near totalBudget/10 plus markers — far below the raw 30KB.
	if totalAfter > configMapGuardTotalBytes+10*200 {
		t.Fatalf("payload still %d bytes after truncation", totalAfter)
	}
}

// When every value sits under the truncation floor but the total is still
// over budget (many tiny keys), the guard can't shrink anything — it must
// still warn rather than stay silent about a large payload.
func TestTruncateLargeConfigMapData_ManyTinyValuesWarnOnly(t *testing.T) {
	data := map[string]any{}
	for i := 0; i < 40; i++ {
		data[fmt.Sprintf("tiny-%d", i)] = strings.Repeat("x", 500)
	}
	out, note := truncateLargeConfigMapData(map[string]any{"data": data})
	if note == "" {
		t.Fatal("20KB of tiny values must still produce a size warning")
	}
	if strings.Contains(note, "truncated to") {
		t.Fatalf("note claims truncation but nothing should shrink: %q", note)
	}
	for k, v := range out.(map[string]any)["data"].(map[string]any) {
		if len(v.(string)) != 500 {
			t.Fatalf("value %s modified: %d bytes", k, len(v.(string)))
		}
	}
}

// Values just above the truncation floor must never grow: keeping valueCap
// bytes plus the ~60B marker can exceed a 540B original. The guard must leave
// them intact and warn rather than enlarge the payload while claiming to cut.
func TestTruncateLargeConfigMapData_NeverEnlarges(t *testing.T) {
	data := map[string]any{}
	for i := 0; i < 40; i++ {
		data[fmt.Sprintf("near-floor-%d", i)] = strings.Repeat("x", 540) // > 512 cap, < cap+marker
	}
	in := map[string]any{"data": data}
	before := 0
	for _, v := range data {
		before += len(v.(string))
	}
	out, note := truncateLargeConfigMapData(in)
	if note == "" {
		t.Fatal("over-budget payload must still warn")
	}
	after := 0
	for k, v := range out.(map[string]any)["data"].(map[string]any) {
		s := v.(string)
		after += len(s)
		if len(s) != 540 {
			t.Fatalf("value %s changed to %d bytes — truncation enlarged or altered a near-floor value", k, len(s))
		}
	}
	if after > before {
		t.Fatalf("payload grew from %d to %d bytes", before, after)
	}
}

// binaryData counts toward the size guard too — base64 blobs (cert bundles,
// jars) are routinely the largest ConfigMap payloads.
func TestTruncateLargeConfigMapData_BinaryData(t *testing.T) {
	blob := strings.Repeat("A", 20*1024)
	payload := map[string]any{
		"data":       map[string]any{"small": "v"},
		"binaryData": map[string]any{"bundle.jks": blob},
	}
	out, note := truncateLargeConfigMapData(payload)
	if note == "" {
		t.Fatal("large binaryData must produce a truncation note")
	}
	bin := out.(map[string]any)["binaryData"].(map[string]any)
	v := bin["bundle.jks"].(string)
	if len(v) >= len(blob) {
		t.Fatalf("binaryData value not truncated: %d bytes", len(v))
	}
	if !strings.Contains(v, "[truncated by radar:") {
		t.Fatal("truncated binaryData value lacks the explicit marker")
	}
	if !strings.Contains(note, "bundle.jks") {
		t.Fatalf("note must name the truncated key: %q", note)
	}
	if data := out.(map[string]any)["data"].(map[string]any); data["small"] != "v" {
		t.Fatalf("small data value must be untouched, got %v", data["small"])
	}
}

func TestKindMatchesProbe(t *testing.T) {
	cases := []struct {
		requested string
		probe     string
		want      bool
	}{
		{"deployment", "Deployment", true},
		{"deployments", "Deployment", true},
		{"Deployment", "Deployment", true},
		{"statefulset", "Deployment", false},
		{"services", "Service", true},
	}
	for _, tt := range cases {
		if got := kindMatchesProbe(tt.requested, tt.probe); got != tt.want {
			t.Errorf("kindMatchesProbe(%q,%q) = %v, want %v", tt.requested, tt.probe, got, tt.want)
		}
	}
}

// A wrong-kind guess must come back with the corrected retry call.
func TestNotFoundSuggestion_WrongKind(t *testing.T) {
	client := k8sfake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "postgresql", Namespace: "shop"},
	})
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestState)

	s := notFoundSuggestion(context.Background(), "statefulset", "shop", "postgresql")
	if !strings.Contains(s, "found Deployment shop/postgresql") || !strings.Contains(s, "kind=deployment") {
		t.Fatalf("suggestion = %q, want Deployment kind correction", s)
	}

	// Same kind, wrong namespace → namespace correction.
	s = notFoundSuggestion(context.Background(), "deployment", "prod", "postgresql")
	if !strings.Contains(s, "namespace=shop") {
		t.Fatalf("suggestion = %q, want namespace correction", s)
	}

	// Nothing similar → no suggestion.
	if s := notFoundSuggestion(context.Background(), "deployment", "shop", "nonexistent"); s != "" {
		t.Fatalf("expected empty suggestion, got %q", s)
	}
}

// Cross-namespace suggestions must never reveal a resource in a namespace the
// caller can't read — a not-found error would otherwise become an existence
// oracle across RBAC boundaries.
func TestNotFoundSuggestion_CrossNamespaceRBACGated(t *testing.T) {
	client := k8sfake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "postgresql", Namespace: "secret-ns"},
	})
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestState)

	restricted := withTestUserPerms(t, "intruder", nil, []string{"shop"})
	if s := notFoundSuggestion(restricted, "deployment", "shop", "postgresql"); s != "" {
		t.Fatalf("suggestion leaked a resource in an inaccessible namespace: %q", s)
	}

	// The same lookup by a user who CAN read secret-ns gets the correction.
	permitted := withTestUserPerms(t, "operator", nil, []string{"shop", "secret-ns"})
	s := notFoundSuggestion(permitted, "deployment", "shop", "postgresql")
	if !strings.Contains(s, "namespace=secret-ns") {
		t.Fatalf("suggestion = %q, want namespace correction for permitted user", s)
	}
}
