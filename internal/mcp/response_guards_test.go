package mcp

import (
	"context"
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
