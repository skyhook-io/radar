package helm

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/release"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestReleaseActionPreviewShape(t *testing.T) {
	owner := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"namespace": "flux-system", "name": "app"},
		"spec":     map[string]any{"releaseName": "demo", "storageNamespace": "store"},
	}}
	fluxWarning := "Flux HelmRelease flux-system/app owns this release and may reconcile it back. Change its source of truth before managing it directly."
	purgeWarning := "Uninstall purges Helm release history by default, including earlier revisions."
	hookWarning := "Hook default/cleanup has no before-hook-creation delete policy. A surviving Job or Pod can make execution fail with AlreadyExists. Inspect get_helm_release hook diagnostics; explicitly approve deleting that hook object after inspecting it, or use no_hooks after reviewing skipped cleanup."
	for _, tc := range []struct {
		name        string
		keepHistory bool
		fluxOwned   bool
		hooks       bool
		noHooks     bool
		summary     string
		warnings    []string
	}{
		{"plain retained history", true, false, false, false, "uninstall deletes 1 resource, keeps 1 resource, retains history", []string{}},
		{"plain purge", false, false, false, false, "uninstall deletes 1 resource, keeps 1 resource, purges history", []string{purgeWarning}},
		{"Flux owned", true, true, false, false, "uninstall deletes 1 resource, keeps 1 resource, retains history; owned by Flux HelmRelease flux-system/app", []string{fluxWarning}},
		{"hooks run", true, false, true, false, "uninstall deletes 1 resource, keeps 1 resource, runs 1 pre-delete hook, runs 1 post-delete hook, retains history", []string{hookWarning}},
		{"hooks skipped", true, false, true, true, "uninstall deletes 1 resource, keeps 1 resource, skips 1 pre-delete hook, skips 1 post-delete hook, retains history", []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := memoryActionConfig(t)
			rel := actionTestRelease(t, cfg, 1, release.StatusDeployed)
			if tc.hooks {
				rel.Hooks[0].DeletePolicies = []release.HookDeletePolicy{release.HookSucceeded}
				rel.Hooks[0].LastRun.Phase = release.HookPhaseRunning
				rel.Hooks = append(rel.Hooks, &release.Hook{Name: "after", Kind: "Job", Events: []release.HookEvent{release.HookPostDelete}})
			} else {
				rel.Hooks = nil
			}
			if err := cfg.Releases.Update(rel); err != nil {
				t.Fatal(err)
			}
			preview, err := PreviewReleaseAction(cfg, "demo", ReleaseActionOptions{Action: "uninstall", KeepHistory: tc.keepHistory, NoHooks: tc.noHooks})
			if err != nil {
				t.Fatal(err)
			}
			fluxMap := map[string]string{}
			if tc.fluxOwned {
				fluxMap = fluxHelmReleaseKeys([]*unstructured.Unstructured{owner})
			}
			if err := enrichReleaseActionPreview(context.Background(), cfg, "store", preview, fluxMap); err != nil {
				t.Fatal(err)
			}
			if preview.Summary != tc.summary || !reflect.DeepEqual(preview.Warnings, tc.warnings) {
				t.Fatalf("summary/warnings: %q, %v; want %q, %v", preview.Summary, preview.Warnings, tc.summary, tc.warnings)
			}
			if len(preview.Limits) != 2 || !strings.Contains(preview.Limits[0], "not a Kubernetes server-side dry run") || !strings.Contains(preview.Limits[1], "atomic release preconditions") {
				t.Fatalf("standing caveats are not in limits: %+v", preview)
			}
			raw, err := json.Marshal(preview)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(string(raw), `{"summary":`) {
				t.Fatalf("preview does not lead with summary: %s", raw)
			}
			var shape map[string]json.RawMessage
			if err := json.Unmarshal(raw, &shape); err != nil {
				t.Fatal(err)
			}
			if _, exists := shape["statusMeaning"]; exists != tc.hooks {
				t.Fatalf("statusMeaning should appear only with hooks: %s", raw)
			}
			if tc.hooks && (strings.Count(string(raw), `"statusMeaning"`) != 1 || !strings.Contains(preview.StatusMeaning, "last recorded phase")) {
				t.Fatalf("hook status explanation should appear once at preview level: %s", raw)
			}
			if tc.fluxOwned && preview.ManagedByFluxHelmRelease != "flux-system/app" {
				t.Fatalf("missing owner: %s", raw)
			}
		})
	}
}
