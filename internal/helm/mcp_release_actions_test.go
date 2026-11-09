package helm

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/release"
	helmtime "helm.sh/helm/v3/pkg/time"
)

func configMapDoc(name string, annotations map[string]string, data string) string {
	var meta strings.Builder
	fmt.Fprintf(&meta, "metadata:\n  name: %s\n", name)
	if len(annotations) > 0 {
		meta.WriteString("  annotations:\n")
		for k, v := range annotations {
			fmt.Fprintf(&meta, "    %s: %q\n", k, v)
		}
	}
	return "---\n# Source: fixture/templates/" + name + ".yaml\napiVersion: v1\nkind: ConfigMap\n" + meta.String() + "data:\n  v: " + data + "\n"
}

func seedManifestRevision(t *testing.T, cfg *action.Configuration, version int, status release.Status, manifest string, config map[string]any, hooks ...*release.Hook) {
	t.Helper()
	rel := &release.Release{Hooks: hooks,
		Name: "demo", Namespace: "default", Version: version, Manifest: manifest, Config: config,
		Chart: &chart.Chart{Metadata: &chart.Metadata{Name: "fixture", Version: fmt.Sprintf("1.%d.0", version), APIVersion: "v2"}},
		Info:  &release.Info{Status: status, FirstDeployed: helmtime.Now(), LastDeployed: helmtime.Now()},
	}
	if err := cfg.Releases.Create(rel); err != nil {
		t.Fatal(err)
	}
}

// rollbackFixture: revision 1 declares a, b; revision 2 (current) declares a
// (changed) and c. Rolling back to 1 re-creates b, changes a and removes c.
func rollbackFixture(t *testing.T) *action.Configuration {
	t.Helper()
	cfg := memoryActionConfig(t)
	seedManifestRevision(t, cfg, 1, release.StatusSuperseded, configMapDoc("a", nil, "one")+configMapDoc("b", nil, "one"), map[string]any{"replicaCount": 1},
		&release.Hook{Name: "migrate-down", Kind: "Job", Events: []release.HookEvent{release.HookPreRollback, release.HookPreUpgrade}},
		&release.Hook{Name: "smoke", Kind: "Pod", Events: []release.HookEvent{release.HookPostInstall}})
	seedManifestRevision(t, cfg, 2, release.StatusDeployed, configMapDoc("a", nil, "two")+configMapDoc("c", nil, "two"), map[string]any{"replicaCount": 2})
	return cfg
}

func refNames(refs []ResourceRef) string {
	names := make([]string, 0, len(refs))
	for _, r := range refs {
		names = append(names, r.Name)
	}
	return strings.Join(names, ",")
}

func TestMCPRollbackPreviewComparesStoredRevisionsAndReadsLiveState(t *testing.T) {
	cfg := rollbackFixture(t)
	ours := map[string]string{"meta.helm.sh/release-name": "demo", "meta.helm.sh/release-namespace": "default"}
	live := map[string]liveRead{
		"a": {Found: true, Annotations: ours},
		"b": {Found: true, Annotations: map[string]string{"meta.helm.sh/release-name": "other", "meta.helm.sh/release-namespace": "team"}},
		"c": {Found: true, Annotations: map[string]string{"helm.sh/resource-policy": "keep"}},
	}
	read := func(r renderedResource) liveRead { return live[r.Ref.Name] }
	got, err := newChartSourceTestClient(t).rollbackForMCPWith(cfg, MCPRollbackRequest{Name: "demo", Revision: 1, ExpectedRevision: 2, DryRun: true}, read)
	if err != nil {
		t.Fatal(err)
	}
	if got.TargetRevision != 1 || got.BaselineRevision != 2 || got.Revision != 3 || got.ChartVersion != "1.1.0" {
		t.Fatalf("revisions/chart: %+v", got)
	}
	if refNames(got.Added) != "b" || refNames(got.Changed) != "a" || refNames(got.Removed) != "c" {
		t.Fatalf("stored comparison: added=%v changed=%v removed=%v", got.Added, got.Changed, got.Removed)
	}
	if !strings.Contains(got.ValuesDiff, "replicaCount") {
		t.Fatalf("values diff should compare the two revisions' overrides: %q", got.ValuesDiff)
	}
	if len(got.Live.Takeovers) != 1 || got.Live.Takeovers[0].Name != "b" || got.Live.Takeovers[0].Owner != "team/other" {
		t.Fatalf("an object another release owns must be named as a takeover: %+v", got.Live.Takeovers)
	}
	if refNames(got.Live.Kept) != "c" || len(got.Live.Unknown) != 0 || got.Live.Checked != 3 {
		t.Fatalf("live keep on a removed object must be reported: %+v", got.Live)
	}
	if len(got.Hooks) != 1 || got.Hooks[0].Name != "migrate-down" || strings.Join(got.Hooks[0].Events, ",") != "pre-rollback" || strings.Join(got.Hooks[0].DeletePolicies, ",") != "before-hook-creation" {
		t.Fatalf("the target revision's rollback hooks must be listed, and only rollback events: %+v", got.Hooks)
	}
	if last, _ := cfg.Releases.Last("demo"); last.Version != 2 {
		t.Fatalf("preview wrote release history: latest is %d", last.Version)
	}
}

func TestMCPRollbackPreviewReportsUnrecordedOwnerAndUnreadableObjects(t *testing.T) {
	cfg := rollbackFixture(t)
	read := func(r renderedResource) liveRead {
		switch r.Ref.Name {
		case "a":
			return liveRead{Found: true} // exists but records no Helm owner
		case "b":
			return liveRead{Err: errors.New("forbidden")}
		}
		return liveRead{} // c absent: nothing to delete
	}
	got, err := newChartSourceTestClient(t).rollbackForMCPWith(cfg, MCPRollbackRequest{Name: "demo", Revision: 1, DryRun: true}, read)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Live.Takeovers) != 1 || got.Live.Takeovers[0].Name != "a" || got.Live.Takeovers[0].Owner != "" {
		t.Fatalf("missing ownership must be reported as unrecorded, not as ours: %+v", got.Live.Takeovers)
	}
	if refNames(got.Live.Unknown) != "b" || len(got.Live.Kept) != 0 || got.Live.Checked != 2 {
		t.Fatalf("a denied read must be unknown, not absent: %+v", got.Live)
	}
}

func TestMCPRollbackTakeoverComparesReleaseNamespaceToo(t *testing.T) {
	cfg := rollbackFixture(t)
	// Same release name, different release namespace: a different release.
	read := func(r renderedResource) liveRead {
		return liveRead{Found: true, Annotations: map[string]string{"meta.helm.sh/release-name": "demo", "meta.helm.sh/release-namespace": "team"}}
	}
	got, err := newChartSourceTestClient(t).rollbackForMCPWith(cfg, MCPRollbackRequest{Name: "demo", Revision: 1, DryRun: true}, read)
	if err != nil {
		t.Fatal(err)
	}
	owners := []string{}
	for _, tk := range got.Live.Takeovers {
		owners = append(owners, tk.Name+"="+tk.Owner)
	}
	if strings.Join(owners, ",") != "a=team/demo,b=team/demo" {
		t.Fatalf("a same-named release in another namespace owns these: %+v", got.Live.Takeovers)
	}
}

func TestMCPRollbackKeepAndOwnerMatchHelmExactly(t *testing.T) {
	cfg := rollbackFixture(t)
	for _, tc := range []struct {
		name     string
		removed  map[string]string // live annotations on c, which the rollback removes
		readdedB map[string]string // live annotations on b, which the rollback re-creates
		kept     bool
		owner    string
	}{
		{"exact keep", map[string]string{"helm.sh/resource-policy": "keep"}, nil, true, ""},
		// Helm's Update compares exactly; uninstall normalizes, rollback doesn't.
		{"padded or capitalized keep is deleted", map[string]string{"helm.sh/resource-policy": " Keep "}, nil, false, ""},
		{"name without namespace is unrecorded", nil, map[string]string{"meta.helm.sh/release-name": "demo"}, false, ""},
		{"namespace without name is unrecorded", nil, map[string]string{"meta.helm.sh/release-namespace": "team"}, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read := func(r renderedResource) liveRead {
				switch r.Ref.Name {
				case "c":
					return liveRead{Found: true, Annotations: tc.removed}
				case "b":
					if tc.readdedB != nil {
						return liveRead{Found: true, Annotations: tc.readdedB}
					}
				}
				return liveRead{}
			}
			got, err := newChartSourceTestClient(t).rollbackForMCPWith(cfg, MCPRollbackRequest{Name: "demo", Revision: 1, DryRun: true}, read)
			if err != nil {
				t.Fatal(err)
			}
			if (len(got.Live.Kept) == 1) != tc.kept {
				t.Fatalf("kept=%v, want %v", got.Live.Kept, tc.kept)
			}
			if tc.readdedB != nil && (len(got.Live.Takeovers) != 1 || got.Live.Takeovers[0].Owner != tc.owner) {
				t.Fatalf("partial ownership must read as unrecorded: %+v", got.Live.Takeovers)
			}
		})
	}
}

func TestMCPRollbackLiveChecksAreBounded(t *testing.T) {
	var current, target strings.Builder
	for i := 0; i < maxRollbackLiveChecks+5; i++ {
		target.WriteString(configMapDoc(fmt.Sprintf("t%03d", i), nil, "x"))
	}
	current.WriteString(configMapDoc("gone", nil, "x"))
	calls := 0
	read := func(renderedResource) liveRead { calls++; return liveRead{} }
	got := checkRollbackLive(&release.Release{Name: "demo", Namespace: "default", Manifest: current.String()}, &release.Release{Name: "demo", Namespace: "default", Manifest: target.String()}, read)
	if calls != maxRollbackLiveChecks || got.Checked != maxRollbackLiveChecks || len(got.Unknown) != 6 {
		t.Fatalf("want %d reads and the rest unknown: calls=%d checked=%d unknown=%d", maxRollbackLiveChecks, calls, got.Checked, len(got.Unknown))
	}
}

func TestMCPRollbackRefusalsAndExecution(t *testing.T) {
	cfg := rollbackFixture(t)
	c := newChartSourceTestClient(t)
	none := func(renderedResource) liveRead { return liveRead{} }
	var preflight *OperationPreflightError
	for name, req := range map[string]MCPRollbackRequest{
		"stale":   {Name: "demo", Revision: 1, ExpectedRevision: 1, DryRun: true},
		"future":  {Name: "demo", Revision: 3, DryRun: true},
		"missing": {Name: "demo", Revision: 0, DryRun: true},
	} {
		if _, err := c.rollbackForMCPWith(cfg, req, none); !errors.As(err, &preflight) {
			t.Fatalf("%s: want a refusal before Helm runs, got %v", name, err)
		}
	}
	got, err := c.rollbackForMCPWith(cfg, MCPRollbackRequest{Name: "demo", Revision: 1, ExpectedRevision: 2}, none)
	if err != nil {
		t.Fatal(err)
	}
	last, _ := cfg.Releases.Last("demo")
	if got.Revision != 3 || got.TargetRevision != 1 || got.Live != nil || last.Version != 3 || last.Manifest != configMapDoc("a", nil, "one")+configMapDoc("b", nil, "one") {
		t.Fatalf("execution must record revision 3 from revision 1: %+v latest=%d", got, last.Version)
	}
}

func TestMCPUninstallPreviewMatchesHelmResourcePolicy(t *testing.T) {
	list := "---\n# Source: fixture/templates/list.yaml\napiVersion: v1\nkind: List\nmetadata:\n  name: kept-list\n  annotations:\n    helm.sh/resource-policy: keep\nitems:\n- apiVersion: v1\n  kind: ConfigMap\n  metadata: {name: list-child-1}\n- apiVersion: v1\n  kind: ConfigMap\n  metadata: {name: list-child-2}\n"
	childOnly := "---\n# Source: fixture/templates/childonly.yaml\napiVersion: v1\nkind: List\nitems:\n- apiVersion: v1\n  kind: ConfigMap\n  metadata:\n    name: child-annotated\n    annotations: {helm.sh/resource-policy: keep}\n"
	manifest := configMapDoc("plain", nil, "x") + configMapDoc("kept", map[string]string{"helm.sh/resource-policy": "keep"}, "x") +
		configMapDoc("other-policy", map[string]string{"helm.sh/resource-policy": "delete"}, "x") + configMapDoc("annotated", map[string]string{"team": "a"}, "x") + list + childOnly
	rel := &release.Release{Name: "demo", Namespace: "default", Version: 4, Manifest: manifest, Info: &release.Info{Status: release.StatusDeployed},
		Hooks: []*release.Hook{
			{Name: "drop-db", Kind: "Job", Events: []release.HookEvent{release.HookPreDelete}, LastRun: release.HookExecution{Phase: release.HookPhaseSucceeded}},
			{Name: "notify", Kind: "Job", Events: []release.HookEvent{release.HookPostInstall, release.HookPostDelete}, DeletePolicies: []release.HookDeletePolicy{release.HookSucceeded}},
			{Name: "migrate", Kind: "Job", Events: []release.HookEvent{release.HookPreUpgrade}},
		}}
	got, err := uninstallPreviewFor(rel)
	if err != nil {
		t.Fatal(err)
	}
	deleted, kept := refNames(got.Delete), refNames(got.Kept)
	for _, want := range []string{"plain", "annotated", "child-annotated"} {
		if !strings.Contains(","+deleted+",", ","+want+",") {
			t.Fatalf("%s must be deleted (a List child's own policy doesn't count): delete=%s", want, deleted)
		}
	}
	for _, want := range []string{"kept", "other-policy", "list-child-1", "list-child-2"} {
		if !strings.Contains(","+kept+",", ","+want+",") || strings.Contains(","+deleted+",", ","+want+",") {
			t.Fatalf("%s must be left by Helm, not deleted: delete=%s kept=%s", want, deleted, kept)
		}
	}
	if len(got.Hooks) != 2 || got.Hooks[0].Name != "drop-db" || strings.Join(got.Hooks[0].DeletePolicies, ",") != "before-hook-creation" || got.Hooks[0].LastPhase != "Succeeded" ||
		got.Hooks[1].Name != "notify" || strings.Join(got.Hooks[1].Events, ",") != "post-delete" || strings.Join(got.Hooks[1].DeletePolicies, ",") != "hook-succeeded" {
		t.Fatalf("delete hooks with effective policies: %+v", got.Hooks)
	}
	if got.HistoryOnly || got.Revision != 4 || got.ParseErrors != 0 {
		t.Fatalf("preview header: %+v", got)
	}

	// Helm's own uninstall reports what it kept; ours must agree on keep.
	cfg := memoryActionConfig(t)
	rel.Info.FirstDeployed, rel.Info.LastDeployed = helmtime.Now(), helmtime.Now()
	rel.Chart = &chart.Chart{Metadata: &chart.Metadata{Name: "fixture", Version: "1.0.0", APIVersion: "v2"}}
	rel.Hooks = nil
	if err := cfg.Releases.Create(rel); err != nil {
		t.Fatal(err)
	}
	res, err := action.NewUninstall(cfg).Run("demo")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"kept", "kept-list"} {
		if !strings.Contains(res.Info, name) {
			t.Fatalf("fixture drifted from Helm: Helm kept %q", res.Info)
		}
	}
	// A policy other than keep lands in neither of Helm's sets: not reported as
	// kept by Helm, yet not deleted. Our kept list means "left in place".
	if strings.Contains(res.Info, "other-policy") {
		t.Fatalf("Helm now reports non-keep policies as kept; revisit the preview: %q", res.Info)
	}
}

func TestMCPUninstallPreviewOfUninstalledReleaseIsHistoryOnly(t *testing.T) {
	rel := &release.Release{Name: "demo", Namespace: "default", Version: 2, Manifest: configMapDoc("left", nil, "x"), Info: &release.Info{Status: release.StatusUninstalled},
		Hooks: []*release.Hook{{Name: "drop-db", Events: []release.HookEvent{release.HookPreDelete}}}}
	got, err := uninstallPreviewFor(rel)
	if err != nil {
		t.Fatal(err)
	}
	if !got.HistoryOnly || len(got.Delete) != 0 || len(got.Kept) != 0 || len(got.Hooks) != 0 {
		t.Fatalf("Helm only purges history here; nothing may be listed for deletion or as hooks: %+v", got)
	}
}
