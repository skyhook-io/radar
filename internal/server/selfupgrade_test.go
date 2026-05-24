package server

import (
	"encoding/json"
	"testing"
)

// TestSelfUpgradePatchOptions is a tripwire: handleSelfUpgrade must use
// Server-Side Apply as Helm's field manager and force-conflicts to reclaim
// .image from old strategic-merge self-upgrades.
func TestSelfUpgradePatchOptions(t *testing.T) {
	opts := selfUpgradePatchOptions()
	if opts.FieldManager != "helm" {
		t.Errorf("FieldManager = %q, want %q", opts.FieldManager, "helm")
	}
	if opts.Force == nil || !*opts.Force {
		t.Errorf("Force = %v, want non-nil true", opts.Force)
	}
}

func TestIsValidRadarImageTag(t *testing.T) {
	tests := []struct {
		name string
		tag  string
		want bool
	}{
		{name: "semver", tag: "1.6.2", want: true},
		{name: "underscore and dash", tag: "v1_6-rc.1", want: true},
		{name: "empty", tag: "", want: false},
		{name: "too long", tag: "12345678901234567890123456789012345678901234567890123456789012345", want: false},
		{name: "starts with dot", tag: ".1.6.2", want: false},
		{name: "newline injection", tag: "1.6.2\nlevel=error", want: false},
		{name: "space injection", tag: "1.6.2 level=error", want: false},
		{name: "slash", tag: "release/1.6.2", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isValidRadarImageTag(tt.tag); got != tt.want {
				t.Fatalf("isValidRadarImageTag(%q) = %v, want %v", tt.tag, got, tt.want)
			}
		})
	}
}

func TestBuildSelfUpgradeApplyPatchUsesFullHelmDeploymentManifest(t *testing.T) {
	manifest := `---
# Source: radar/templates/service.yaml
apiVersion: v1
kind: Service
metadata:
  name: radar
---
# Source: radar/templates/deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: radar
  labels:
    app.kubernetes.io/name: radar
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: radar
      app.kubernetes.io/instance: radar
  template:
    metadata:
      labels:
        app.kubernetes.io/name: radar
        app.kubernetes.io/instance: radar
    spec:
      serviceAccountName: radar
      containers:
        - name: radar
          image: "ghcr.io/skyhook-io/radar:1.6.1"
          imagePullPolicy: IfNotPresent
        - name: sidecar
          image: "example.com/sidecar:v1"
`

	patch, err := buildSelfUpgradeApplyPatch(manifest, "radar", "radar", "radar", "ghcr.io/skyhook-io/radar:1.6.2")
	if err != nil {
		t.Fatalf("buildSelfUpgradeApplyPatch() error = %v", err)
	}

	var obj map[string]any
	if err := json.Unmarshal(patch, &obj); err != nil {
		t.Fatalf("patch is not valid JSON: %v", err)
	}

	if got := nestedString(t, obj, "metadata", "namespace"); got != "radar" {
		t.Errorf("metadata.namespace = %q, want radar", got)
	}
	if got := nestedString(t, obj, "metadata", "annotations", "meta.helm.sh/release-name"); got != "radar" {
		t.Errorf("release-name annotation = %q, want radar", got)
	}
	if got := nestedString(t, obj, "metadata", "annotations", "meta.helm.sh/release-namespace"); got != "radar" {
		t.Errorf("release-namespace annotation = %q, want radar", got)
	}
	if got := nestedFloat(t, obj, "spec", "replicas"); got != 1 {
		t.Errorf("spec.replicas = %v, want 1", got)
	}
	if got := nestedString(t, obj, "spec", "selector", "matchLabels", "app.kubernetes.io/name"); got != "radar" {
		t.Errorf("selector label = %q, want radar", got)
	}
	if got := nestedString(t, obj, "spec", "template", "spec", "serviceAccountName"); got != "radar" {
		t.Errorf("serviceAccountName = %q, want radar", got)
	}

	containers, ok, err := nestedSlice(obj, "spec", "template", "spec", "containers")
	if err != nil || !ok {
		t.Fatalf("containers lookup ok=%v err=%v", ok, err)
	}
	if got := containers[0].(map[string]any)["image"]; got != "ghcr.io/skyhook-io/radar:1.6.2" {
		t.Errorf("radar image = %q, want upgraded image", got)
	}
	if got := containers[0].(map[string]any)["imagePullPolicy"]; got != "IfNotPresent" {
		t.Errorf("radar imagePullPolicy = %q, want preserved", got)
	}
	if got := containers[1].(map[string]any)["image"]; got != "example.com/sidecar:v1" {
		t.Errorf("sidecar image = %q, want unchanged", got)
	}
}

func TestBuildSelfUpgradeApplyPatchRejectsMissingDeployment(t *testing.T) {
	_, err := buildSelfUpgradeApplyPatch("apiVersion: v1\nkind: Service\nmetadata:\n  name: radar\n", "radar", "radar", "radar", "ghcr.io/skyhook-io/radar:1.6.2")
	if err == nil {
		t.Fatal("buildSelfUpgradeApplyPatch() error = nil, want missing deployment error")
	}
}

func nestedString(t *testing.T, obj map[string]any, fields ...string) string {
	t.Helper()
	var current any = obj
	for _, field := range fields {
		currentMap, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("path %v: %q is not an object", fields, field)
		}
		current = currentMap[field]
	}
	value, ok := current.(string)
	if !ok {
		t.Fatalf("path %v: value is %T, want string", fields, current)
	}
	return value
}

func nestedFloat(t *testing.T, obj map[string]any, fields ...string) float64 {
	t.Helper()
	var current any = obj
	for _, field := range fields {
		currentMap, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("path %v: %q is not an object", fields, field)
		}
		current = currentMap[field]
	}
	value, ok := current.(float64)
	if !ok {
		t.Fatalf("path %v: value is %T, want number", fields, current)
	}
	return value
}
