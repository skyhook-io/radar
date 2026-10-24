package context

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestSummary_TrivyVulnerabilityReport(t *testing.T) {
	scanned := time.Now().Add(-3 * time.Hour).UTC().Format(time.RFC3339)
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "aquasecurity.github.io/v1alpha1",
		"kind":       "VulnerabilityReport",
		"metadata": map[string]any{
			"name":      "replicaset-web-5d8f9-php",
			"namespace": "shop",
			"labels": map[string]any{
				"trivy-operator.resource.kind":  "ReplicaSet",
				"trivy-operator.resource.name":  "web-5d8f9",
				"trivy-operator.container.name": "php",
			},
		},
		"report": map[string]any{
			"updateTimestamp": scanned,
			"registry":        map[string]any{"server": "index.docker.io"},
			"artifact":        map[string]any{"repository": "library/php", "tag": "8.3.10-apache", "digest": "sha256:abc"},
			"summary":         map[string]any{"criticalCount": int64(2), "highCount": int64(7), "mediumCount": int64(0), "lowCount": int64(1)},
			"vulnerabilities": []any{map[string]any{"vulnerabilityID": "CVE-2026-109618"}},
		},
	}}
	s := summarizeUnstructured(obj)
	if s.Image != "index.docker.io/library/php:8.3.10-apache@sha256:abc" {
		t.Errorf("Image = %q", s.Image)
	}
	if s.Owner != "ReplicaSet/web-5d8f9" || s.Target != "container php" {
		t.Errorf("Owner = %q, Target = %q", s.Owner, s.Target)
	}
	if !strings.HasPrefix(s.Status, "scanned 3h ago: ") || !strings.HasSuffix(s.Status, "2 critical, 7 high, 1 low") {
		t.Errorf("Status = %q", s.Status)
	}
}

func TestSummary_TrivyReportWithoutFindings(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "aquasecurity.github.io/v1alpha1",
		"kind":       "ExposedSecretReport",
		"metadata":   map[string]any{"name": "r", "namespace": "shop"},
		"report":     map[string]any{"summary": map[string]any{"criticalCount": int64(0)}},
	}}
	if s := summarizeUnstructured(obj); s.Status != "no findings" {
		t.Errorf("Status = %q", s.Status)
	}
}

func TestSummary_TrivyComplianceReportUsesGenericPath(t *testing.T) {
	// ClusterComplianceReport keeps spec/status and has no report field.
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "aquasecurity.github.io/v1alpha1",
		"kind":       "ClusterComplianceReport",
		"metadata":   map[string]any{"name": "nsa"},
		"status":     map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}},
	}}
	if s := summarizeUnstructured(obj); s.Status != "Ready" {
		t.Errorf("Status = %q", s.Status)
	}
}

func TestMinify_ExposedSecretReportRedactsMatches(t *testing.T) {
	newReport := func() *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "aquasecurity.github.io/v1alpha1",
			"kind":       "ExposedSecretReport",
			"metadata":   map[string]any{"name": "r", "namespace": "shop"},
			"report": map[string]any{"secrets": []any{map[string]any{
				"ruleID": "aws-access-key-id", "target": "/app/.env", "severity": "CRITICAL",
				"match": "AWS_ACCESS_KEY_ID=AKIAREALSECRETVALUE",
				"code":  map[string]any{"lines": []any{map[string]any{"content": "AWS_ACCESS_KEY_ID=AKIAREALSECRETVALUE"}}},
			}}},
		}}
	}
	for _, level := range []VerbosityLevel{LevelDetail, LevelCompact} {
		out, err := json.Marshal(MinifyUnstructured(newReport(), level))
		if err != nil {
			t.Fatal(err)
		}
		body := string(out)
		if strings.Contains(body, "AKIAREALSECRETVALUE") {
			t.Errorf("level %v leaks the matched secret: %s", level, body)
		}
		if !strings.Contains(body, "aws-access-key-id") || !strings.Contains(body, "/app/.env") {
			t.Errorf("level %v dropped what was found where: %s", level, body)
		}
	}
}
