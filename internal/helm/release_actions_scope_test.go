package helm

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/release"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
)

func TestReleaseActionIncompleteManifestStatuses(t *testing.T) {
	for _, tc := range []struct {
		action          string
		invalidRevision int
	}{{"uninstall", 2}, {"rollback", 1}, {"rollback", 2}} {
		action := tc.action
		for _, manifest := range []string{
			"not: [valid yaml",
			"apiVersion: v1\nkind: List\nitems: []",
			"apiVersion: v1\nkind: ConfigMap\nmetadata: {}",
			"kind: ConfigMap\nmetadata:\n  name: demo",
		} {
			t.Run(fmt.Sprintf("%s/revision%d/%s", action, tc.invalidRevision, manifest), func(t *testing.T) {
				cfg := memoryActionConfig(t)
				options := ReleaseActionOptions{Action: action}
				var target *release.Release
				if action == "rollback" {
					target = actionTestRelease(t, cfg, 1, release.StatusSuperseded)
					options.Revision = 1
				}
				rel := actionTestRelease(t, cfg, 2, release.StatusDeployed)
				if tc.invalidRevision == 1 {
					rel = target
				}
				rel.Manifest = manifest
				if err := cfg.Releases.Update(rel); err != nil {
					t.Fatal(err)
				}
				preview, err := PreviewReleaseAction(cfg, "demo", options)
				if err == nil || preview != nil {
					t.Fatalf("incomplete preview accepted: %+v %v", preview, err)
				}
				rec := httptest.NewRecorder()
				writeReleaseActionError(rec, err)
				if rec.Code != 422 {
					t.Fatalf("preview refusal: %d %s", rec.Code, rec.Body)
				}
				err = RunReleaseAction(cfg, "demo", options, "old-confirmation")
				if err == nil {
					t.Fatal("incomplete execution accepted")
				}
				rec = httptest.NewRecorder()
				writeReleaseExecutionError(rec, action, "default", "demo", err)
				if rec.Code != 422 {
					t.Fatalf("execution refusal: %d %s", rec.Code, rec.Body)
				}
				stored, err := cfg.Releases.Last("demo")
				if err != nil || stored.Version != 2 || stored.Info.Status != release.StatusDeployed {
					t.Fatalf("refusal mutated storage: %+v %v", stored, err)
				}
			})
		}
	}
}

func TestReleaseActionScopeUsesHelmMapper(t *testing.T) {
	cfg := memoryActionConfig(t)
	target := actionTestRelease(t, cfg, 1, release.StatusSuperseded)
	current := actionTestRelease(t, cfg, 2, release.StatusDeployed)
	manifest := func(group, kind, name, namespace string) string {
		return fmt.Sprintf("apiVersion: %s/v1\nkind: %s\nmetadata:\n  name: %s\n  namespace: %q\n", group, kind, name, namespace)
	}
	target.Manifest = manifest("custom.example", "ClusterWidget", "shared", "") + "---\n" + manifest("custom.example", "ClusterWidget", "target", "") + "---\n" + manifest("custom.example", "Node", "defaulted", "") + "---\n" + manifest("custom.example", "Node", "explicit", "other")
	current.Manifest = manifest("custom.example", "ClusterWidget", "current", "incorrect") + "---\n" + manifest("custom.example", "ClusterWidget", "shared", "incorrect")
	for _, rel := range []*release.Release{target, current} {
		rel.Namespace = "app"
		if err := cfg.Releases.Create(rel); err != nil {
			t.Fatal(err)
		}
	}
	getter := newRESTConfigGetter(&rest.Config{Host: "http://unused.invalid"}, "storage", "", nil)
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{{Group: "custom.example", Version: "v1"}})
	mapper.Add(schema.GroupVersionKind{Group: "custom.example", Version: "v1", Kind: "ClusterWidget"}, meta.RESTScopeRoot)
	mapper.Add(schema.GroupVersionKind{Group: "custom.example", Version: "v1", Kind: "Node"}, meta.RESTScopeNamespace)
	getter.mapper = mapper
	cfg.RESTClientGetter = getter
	for _, options := range []ReleaseActionOptions{{Action: "uninstall"}, {Action: "rollback", Revision: 1}} {
		preview, err := PreviewReleaseAction(cfg, "demo", options)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range preview.Resources {
			want := ""
			if r.Name == "defaulted" {
				want = "app"
			} else if r.Name == "explicit" {
				want = "other"
			}
			if r.Namespace != want {
				t.Fatalf("wrong mapped scope: %+v, want namespace %q", r, want)
			}
			if options.Action == "rollback" && r.Name == "shared" && r.Effect != "update" {
				t.Fatalf("cluster scope changed identity: %+v", r)
			}
		}
		wantCount := 2
		if options.Action == "rollback" {
			wantCount = 5
		}
		if len(preview.Resources) != wantCount {
			t.Fatalf("resources: %+v", preview.Resources)
		}
	}
}

func TestReleaseActionUnresolvedScopeRefusesCurrentAndTarget(t *testing.T) {
	for _, revision := range []int{1, 2} {
		t.Run(fmt.Sprint(revision), func(t *testing.T) {
			cfg := memoryActionConfig(t)
			target := actionTestRelease(t, cfg, 1, release.StatusSuperseded)
			current := actionTestRelease(t, cfg, 2, release.StatusDeployed)
			rel := target
			if revision == 2 {
				rel = current
			}
			for _, apiVersion := range []string{"unknown.example/v1", "v2"} {
				rel.Manifest = fmt.Sprintf("apiVersion: %s\nkind: ConfigMap\nmetadata:\n  name: unresolved\n  namespace: explicit\n", apiVersion)
				if err := cfg.Releases.Update(rel); err != nil {
					t.Fatal(err)
				}
				options := ReleaseActionOptions{Action: "rollback", Revision: 1}
				preview, err := PreviewReleaseAction(cfg, "demo", options)
				if preview != nil || err == nil || !strings.Contains(err.Error(), "scope") || !strings.Contains(err.Error(), "unresolved") || !strings.Contains(err.Error(), "no confirmation issued") {
					t.Fatalf("unknown scope accepted or unclear: %+v %v", preview, err)
				}
				if err := RunReleaseAction(cfg, "demo", options, "old-confirmation"); err == nil {
					t.Fatal("execution accepted unresolved scope")
				}
			}
		})
	}
}

func TestReleaseActionMapperUnavailableRefusesConfirmation(t *testing.T) {
	cfg := memoryActionConfig(t)
	actionTestRelease(t, cfg, 1, release.StatusDeployed)
	cfg.RESTClientGetter = newRESTConfigGetter(nil, "default", "", nil)
	preview, err := PreviewReleaseAction(cfg, "demo", ReleaseActionOptions{Action: "uninstall"})
	if preview != nil || err == nil || !strings.Contains(err.Error(), "cannot resolve resource scopes") || !strings.Contains(err.Error(), "no confirmation issued") {
		t.Fatalf("unavailable mapper accepted or unclear: %+v %v", preview, err)
	}
	rec := httptest.NewRecorder()
	writeReleaseActionError(rec, err)
	if rec.Code != 422 {
		t.Fatalf("mapper refusal: %d %s", rec.Code, rec.Body)
	}
}
