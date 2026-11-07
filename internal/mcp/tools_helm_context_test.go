package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/skyhook-io/radar/internal/helm"
	"helm.sh/helm/v3/pkg/chart"
)

func TestHelmChartContextIsTargetedRedactedAndBounded(t *testing.T) {
	stored := &chart.Chart{Metadata: &chart.Metadata{Name: "fixture", Version: "1.0.0"},
		Values: map[string]any{"auth": map[string]any{"password": "plain-secret", "user": "app"}, "big": strings.Repeat("x", 20000), "a/b": true},
		Schema: []byte(`{"type":"object","properties":{"auth":{"type":"object","properties":{"password":{"default":"schema-secret"},"user":{"type":"string"}}}}}`)}
	root := helmChartContext(stored, "")
	if _, exists := root["defaultValue"]; exists {
		t.Fatal("root dumped all defaults")
	}
	encoded, _ := json.Marshal(root)
	if !strings.Contains(string(encoded), "/a~1b") || strings.Contains(string(encoded), "schema-secret") {
		t.Fatalf("bad outline/schema: %s", encoded)
	}
	selected := helmChartContext(stored, "/auth")
	encoded, _ = json.Marshal(selected)
	if strings.Contains(string(encoded), "plain-secret") || !strings.Contains(string(encoded), "[REDACTED]") || !strings.Contains(string(encoded), "/auth/user") {
		t.Fatalf("unsafe/incomplete selected context: %s", encoded)
	}
	if stored.Values["auth"].(map[string]any)["password"] != "plain-secret" {
		t.Fatal("read mutated stored defaults")
	}
	if got := helmChartContext(stored, "/a~1b")["defaultValue"]; got != true {
		t.Fatalf("escaped pointer: %v", got)
	}
	if got := helmChartContext(stored, "/big"); got["valueError"] == nil || got["defaultValue"] != nil {
		t.Fatal("oversized value not bounded")
	}
	if got := helmChartContext(stored, "/missing"); got["valueError"] == nil {
		t.Fatal("missing path treated as absent schema property")
	}
}

func TestHelmNotesRedactsPlainInterpolatedCredentials(t *testing.T) {
	values := map[string]any{"auth": map[string]any{"password": "short-sentinel"}, "secretName": "keep-ref"}
	got := redactHelmNotes("Connect with short-sentinel and Secret keep-ref", values)
	if strings.Contains(got, "short-sentinel") || !strings.Contains(got, "keep-ref") {
		t.Fatalf("notes redaction: %s", got)
	}
}

func TestHelmUpgradePreviewUsesExplicitStateAndBounds(t *testing.T) {
	preview := &helm.MCPUpgradeResult{Revision: 4, BaselineRevision: 2, ChartVersion: "1.0.0", ValuesDiff: strings.Repeat("x", 20000)}
	for i := 0; i < 120; i++ {
		preview.Removed = append(preview.Removed, helm.ResourceRef{Kind: "ConfigMap", Name: "fixture"})
	}
	result, _, err := helmUpgradePreviewResult(manageHelmInput{Namespace: "ns", Name: "app"}, preview)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &got); err != nil {
		t.Fatal(err)
	}
	if got["state"] != "preview" || got["currentRevision"] != 3.0 || got["baselineRevision"] != 2.0 || got["valuesDiffTruncated"] != true || got["removedTruncated"] != true || len(got["removed"].([]any)) != 100 {
		t.Fatalf("preview contract: %v", got)
	}
}

func TestHelmDryRunNeverFallsThroughToAnotherAction(t *testing.T) {
	for _, action := range []string{"install", "rollback", "uninstall"} {
		in := manageHelmInput{Action: action, Namespace: "ns", Name: "app", DryRun: true}
		if action == "install" {
			in.Chart = "app"
			in.Repository = "repo"
			in.Version = "1.0.0"
		}
		if action == "rollback" {
			in.Revision = 1
		}
		if err := in.validate(); err == nil {
			t.Fatalf("dry_run accepted for %s", action)
		}
	}
	if err := (manageHelmInput{Action: "upgrade", Namespace: "ns", Name: "app", DryRun: true}).validate(); err != nil {
		t.Fatal(err)
	}
}

func TestHelmChartContextSchemaOnlyAndPartial(t *testing.T) {
	c := &chart.Chart{Metadata: &chart.Metadata{Name: "fixture", Version: "1.0.0"}, Values: map[string]any{}, Schema: []byte(`{"type":"object","properties":{"optional":{"type":"string"}}}`)}
	got := helmChartContext(c, "/optional")
	if got["valueError"] == nil || got["schema"] != `{"type":"string"}` {
		t.Fatalf("schema-only field lost: %v", got)
	}
	c.Schema = []byte(`[]`)
	if got := helmChartContext(c, ""); got["schemaError"] == nil {
		t.Fatal("array schema accepted as object")
	}
	for i := 0; i < 150; i++ {
		c.Values[strings.Repeat("x", i+1)] = true
	}
	got = helmChartContext(c, "")
	if got["valuePathsTruncated"] != true || got["valuePathsCount"] != 150 {
		t.Fatalf("partial path outline not labeled: %v", got)
	}
}

func TestHelmNotesKeepsBooleanFlagsAndMasksNumericCredentials(t *testing.T) {
	got := redactHelmNotes("Run --set x=false; pass 123456789", map[string]any{"automountServiceAccountToken": false, "password": float64(123456789)})
	if !strings.Contains(got, "x=false") || strings.Contains(got, "123456789") {
		t.Fatalf("wrong scalar masking: %s", got)
	}
}
