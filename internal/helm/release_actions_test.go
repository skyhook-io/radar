package helm

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/cli"
	kubefake "helm.sh/helm/v3/pkg/kube/fake"
	"helm.sh/helm/v3/pkg/release"
	"k8s.io/client-go/rest"
)

func actionTestRelease(t *testing.T, cfg *action.Configuration, revision int, status release.Status) *release.Release {
	t.Helper()
	seedRelease(t, cfg, "demo", status, revision)
	rel, err := cfg.Releases.Get("demo", revision)
	if err != nil {
		t.Fatal(err)
	}
	rel.Manifest = `apiVersion: v1
kind: ConfigMap
metadata:
  name: config
---
apiVersion: v1
kind: Secret
metadata:
  name: retained
  annotations:
    helm.sh/resource-policy: keep
data:
  password: c2Vuc2l0aXZl
`
	rel.Config = map[string]any{"password": "hidden-values"}
	rel.Hooks = []*release.Hook{
		{Name: "cleanup", Kind: "Job", Events: []release.HookEvent{release.HookPreDelete}, Manifest: "apiVersion: batch/v1\nkind: Job\nmetadata:\n  name: cleanup\n"},
		{Name: "restore", Kind: "Job", Events: []release.HookEvent{release.HookPostRollback}},
		{Name: "install", Kind: "Job", Events: []release.HookEvent{release.HookPreInstall}},
	}
	if err := cfg.Releases.Update(rel); err != nil {
		t.Fatal(err)
	}
	cfg.KubeClient = &kubefake.PrintingKubeClient{Out: io.Discard}
	return rel
}

func TestReleaseActionPreviewUninstall(t *testing.T) {
	cfg := memoryActionConfig(t)
	actionTestRelease(t, cfg, 2, release.StatusUninstalling)
	for _, noHooks := range []bool{false, true} {
		preview, err := PreviewReleaseAction(cfg, "demo", ReleaseActionOptions{Action: "uninstall", NoHooks: noHooks, KeepHistory: true})
		if err != nil {
			t.Fatal(err)
		}
		if preview.Status != "uninstalling" || preview.Revision != 2 || len(preview.Resources) != 2 || len(preview.Hooks) != 1 {
			t.Fatalf("preview = %+v", preview)
		}
		if preview.Resources[0].Effect != "delete" || preview.Resources[1].Effect != "keep (helm.sh/resource-policy)" {
			t.Fatalf("effects = %+v", preview.Resources)
		}
		if preview.Hooks[0].Name != "cleanup" || (preview.Hooks[0].Effect == "run") == noHooks {
			t.Fatalf("wrong hooks: %+v", preview.Hooks)
		}
		raw, _ := json.Marshal(preview)
		for _, secret := range []string{"hidden-values", "c2Vuc2l0aXZl", preview.Fingerprint} {
			if strings.Contains(string(raw), secret) {
				t.Fatalf("preview leaked %q", secret)
			}
		}
		if !strings.Contains(strings.Join(preview.Warnings, " "), "retries the uninstalling") {
			t.Fatal("missing retry guidance")
		}
	}
	stored, _ := cfg.Releases.Last("demo")
	if stored.Info.Status != release.StatusUninstalling {
		t.Fatal("preview modified stored state")
	}
}

func TestReleaseActionRollbackPreviewAndBinding(t *testing.T) {
	cfg := memoryActionConfig(t)
	target := actionTestRelease(t, cfg, 1, release.StatusSuperseded)
	current := actionTestRelease(t, cfg, 2, release.StatusDeployed)
	options := ReleaseActionOptions{Action: "rollback", Revision: 1}
	preview, err := PreviewReleaseAction(cfg, "demo", options)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Resources) != 2 || len(preview.Hooks) != 1 || preview.Hooks[0].Name != "restore" || preview.Hooks[0].Effect != "run" {
		t.Fatalf("rollback preview = %+v", preview)
	}
	for _, mutate := range []func(){
		func() { options.NoHooks = true },
		func() { current.Info.Status = release.StatusFailed; cfg.Releases.Update(current) },
		func() { target.Hooks[1].Manifest = "changed"; cfg.Releases.Update(target) },
		func() { target.Config["password"] = "changed"; cfg.Releases.Update(target) },
	} {
		mutate()
		if err := RunReleaseAction(cfg, "demo", options, preview.Fingerprint); err == nil || !strings.Contains(err.Error(), "changed") {
			t.Fatalf("stale preview accepted: %v", err)
		}
		options.NoHooks = false
	}
	if _, err := cfg.Releases.Get("demo", 3); err == nil {
		t.Fatal("stale execution created rollback revision")
	}
	for _, revision := range []int{-1, 0, 2, 3} {
		if _, err := PreviewReleaseAction(cfg, "demo", ReleaseActionOptions{Action: "rollback", Revision: revision}); err == nil {
			t.Fatalf("revision %d accepted", revision)
		}
	}
}

func TestReleaseActionUninstallExecutionAndHistory(t *testing.T) {
	for _, keepHistory := range []bool{false, true} {
		t.Run(map[bool]string{true: "retain", false: "purge"}[keepHistory], func(t *testing.T) {
			cfg := memoryActionConfig(t)
			rel := actionTestRelease(t, cfg, 1, release.StatusUninstalling)
			rel.Manifest = ""
			cfg.Releases.Update(rel)
			options := ReleaseActionOptions{Action: "uninstall", NoHooks: true, KeepHistory: keepHistory}
			preview, err := PreviewReleaseAction(cfg, "demo", options)
			if err != nil {
				t.Fatal(err)
			}
			if err := RunReleaseAction(cfg, "demo", options, preview.Fingerprint); err != nil {
				t.Fatal(err)
			}
			stored, err := cfg.Releases.Last("demo")
			if keepHistory && (err != nil || stored.Info.Status != release.StatusUninstalled) {
				t.Fatalf("history not retained: %v %+v", err, stored)
			}
			if !keepHistory && err == nil {
				t.Fatal("history not purged")
			}
		})
	}
}

func TestUninstallOptionsDryRunDoesNotMutate(t *testing.T) {
	cfg := memoryActionConfig(t)
	actionTestRelease(t, cfg, 1, release.StatusDeployed)
	if err := uninstallWithOptions(cfg, "demo", UninstallOptions{DryRun: true}); err != nil {
		t.Fatal(err)
	}
	stored, _ := cfg.Releases.Last("demo")
	if stored.Info.Status != release.StatusDeployed {
		t.Fatal("dry run mutated release")
	}
}

func TestReleaseActionAlreadyUninstalledAndMalformed(t *testing.T) {
	cfg := memoryActionConfig(t)
	rel := actionTestRelease(t, cfg, 1, release.StatusUninstalled)
	if _, err := PreviewReleaseAction(cfg, "demo", ReleaseActionOptions{Action: "uninstall", KeepHistory: true}); err == nil {
		t.Fatal("already uninstalled keep history accepted")
	}
	preview, err := PreviewReleaseAction(cfg, "demo", ReleaseActionOptions{Action: "uninstall"})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Hooks[0].Effect != "skip (already uninstalled)" || !strings.Contains(preview.Resources[0].Effect, "history purge") {
		t.Fatalf("purge claims resource deletion: %+v", preview)
	}
	for _, manifest := range []string{"not: [valid yaml", "apiVersion: v1\nkind: List\nitems: []", "kind: ConfigMap\nmetadata: {}"} {
		rel.Manifest = manifest
		cfg.Releases.Update(rel)
		if _, err := PreviewReleaseAction(cfg, "demo", ReleaseActionOptions{Action: "uninstall"}); err == nil {
			t.Fatal("unenumerable manifest accepted")
		}
	}
}

func TestUninstallOptionsQuery(t *testing.T) {
	for _, query := range []string{"no_hooks=1", "keep_history=", "dry_run=yes", "no_hooks=true&no_hooks=false"} {
		if _, err := uninstallOptionsFromQuery(httptest.NewRequest("DELETE", "/?"+query, nil)); err == nil {
			t.Fatalf("query %q accepted", query)
		}
	}
	options, err := uninstallOptionsFromQuery(httptest.NewRequest("DELETE", "/?no_hooks=true&keep_history=true&dry_run=true", nil))
	if err != nil || !options.NoHooks || !options.KeepHistory || !options.DryRun {
		t.Fatalf("options = %+v, %v", options, err)
	}
	options, err = uninstallOptionsFromQuery(httptest.NewRequest("DELETE", "/", nil))
	if err != nil || options != (UninstallOptions{}) {
		t.Fatalf("default changed: %+v %v", options, err)
	}
}

func TestReleaseActionsImpersonateStorageReadsAndUninstall(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Impersonate-User") != "alice" || !slices.Contains(r.Header.Values("Impersonate-Group"), "team") {
			t.Errorf("request %s lost identity: %v", r.URL.Path, r.Header)
		}
		http.Error(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403}`, http.StatusForbidden)
	}))
	defer srv.Close()
	client := &Client{settings: cli.New(), restConfig: &rest.Config{Host: srv.URL}}
	cfg, err := client.GetActionConfigForUser("default", "alice", []string{"team"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PreviewReleaseAction(cfg, "demo", ReleaseActionOptions{Action: "uninstall"}); err == nil {
		t.Fatal("preview ignored forbidden read")
	}
	if err := uninstallWithOptions(cfg, "demo", UninstallOptions{NoHooks: true}); err == nil {
		t.Fatal("uninstall ignored forbidden server")
	}
	if calls < 2 {
		t.Fatalf("only %d requests", calls)
	}
}

func TestReleaseActionRollbackExecution(t *testing.T) {
	cfg := memoryActionConfig(t)
	target := actionTestRelease(t, cfg, 1, release.StatusSuperseded)
	target.Chart = &chart.Chart{Metadata: &chart.Metadata{Name: "demo", Version: "1.0.0"}}
	target.Manifest = ""
	if err := cfg.Releases.Update(target); err != nil {
		t.Fatal(err)
	}
	current := actionTestRelease(t, cfg, 3, release.StatusDeployed)
	current.Manifest = ""
	if err := cfg.Releases.Update(current); err != nil {
		t.Fatal(err)
	}
	if _, err := PreviewReleaseAction(cfg, "demo", ReleaseActionOptions{Action: "rollback", Revision: 2}); err == nil {
		t.Fatal("missing revision accepted")
	}
	options := ReleaseActionOptions{Action: "rollback", Revision: 1, NoHooks: true}
	preview, err := PreviewReleaseAction(cfg, "demo", options)
	if err != nil {
		t.Fatal(err)
	}
	if err := RunReleaseAction(cfg, "demo", options, preview.Fingerprint); err != nil {
		t.Fatal(err)
	}
	stored, err := cfg.Releases.Last("demo")
	if err != nil || stored.Version != 4 || stored.Info.Status != release.StatusDeployed || stored.Config["password"] != "hidden-values" {
		t.Fatalf("rollback result = %+v %v", stored, err)
	}
}
