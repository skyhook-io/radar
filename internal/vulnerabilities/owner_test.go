package vulnerabilities

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

func ownerDeployment(ns, name string, labels, annotations map[string]string) *appsv1.Deployment {
	return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID("uid-" + name), Labels: labels, Annotations: annotations}}
}

func ownerObject(apiVersion, kind, ns, name string, spec, status map[string]any) *unstructured.Unstructured {
	obj := map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": map[string]any{"namespace": ns, "name": name, "uid": "uid-" + name}, "spec": spec}
	if status != nil {
		obj["status"] = status
	}
	return &unstructured.Unstructured{Object: obj}
}

// ownerCluster starts the caches with a Helm-installed, an Argo CD-managed,
// a Flux-managed and an unmanaged Deployment.
func ownerCluster(t *testing.T) *k8s.ResourceCache {
	t.Helper()
	client := fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop", Labels: map[string]string{"team": "storefront"}}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "other"}},
		ownerDeployment("shop", "web",
			map[string]string{"app.kubernetes.io/managed-by": "Helm", "helm.sh/chart": "web-1.2.0", "team": "payments"},
			map[string]string{"meta.helm.sh/release-name": "web", "meta.helm.sh/release-namespace": "shop"}),
		ownerDeployment("shop", "api", nil, map[string]string{"argocd.argoproj.io/tracking-id": "shop-api:apps/Deployment:shop/api"}),
		ownerDeployment("shop", "worker", map[string]string{"kustomize.toolkit.fluxcd.io/name": "infra", "kustomize.toolkit.fluxcd.io/namespace": "flux-system"}, nil),
		ownerDeployment("other", "plain", nil, nil),
		ownerDeployment("shop", "both", nil, map[string]string{"argocd.argoproj.io/tracking-id": "shop-api:apps/Deployment:shop/both"}),
	)
	k8s.ResetResourceCache()
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetResourceCache)

	app := ownerObject("argoproj.io/v1alpha1", "Application", "argocd", "shop-api",
		map[string]any{
			"destination": map[string]any{"server": "https://kubernetes.default.svc", "namespace": "shop"},
			"source":      map[string]any{"repoURL": "https://deploy:s3cret@github.com/acme/deploy.git", "path": "apps/api"},
		},
		map[string]any{"resources": []any{
			map[string]any{"group": "apps", "version": "v1", "kind": "Deployment", "namespace": "shop", "name": "api"},
			map[string]any{"group": "apps", "version": "v1", "kind": "Deployment", "namespace": "shop", "name": "both"},
		}})
	extra := ownerObject("argoproj.io/v1alpha1", "Application", "argocd", "shop-extra",
		map[string]any{
			"destination": map[string]any{"server": "https://kubernetes.default.svc", "namespace": "shop"},
			"source":      map[string]any{"repoURL": "https://github.com/acme/extra.git", "path": "apps/extra"},
		},
		map[string]any{"resources": []any{map[string]any{"group": "apps", "version": "v1", "kind": "Deployment", "namespace": "shop", "name": "both"}}})
	kustomization := ownerObject("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "flux-system", "infra",
		map[string]any{"path": "./clusters/prod", "sourceRef": map[string]any{"kind": "GitRepository", "name": "fleet"}},
		map[string]any{"inventory": map[string]any{"entries": []any{map[string]any{"id": "shop_worker_apps_Deployment", "v": "v1"}}}})
	repo := ownerObject("source.toolkit.fluxcd.io/v1", "GitRepository", "flux-system", "fleet", map[string]any{"url": "ssh://git@github.com/acme/fleet"}, nil)

	gvrs := map[schema.GroupVersionResource]string{
		{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}:             "ApplicationList",
		{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}: "KustomizationList",
		{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"}:   "GitRepositoryList",
		{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}:        "HelmReleaseList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), gvrs, app, extra, kustomization, repo)
	resources := []k8s.APIResource{
		{Group: "argoproj.io", Version: "v1alpha1", Name: "applications", Kind: "Application", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch", "delete"}},
		{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Name: "kustomizations", Kind: "Kustomization", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch", "delete"}},
		{Group: "source.toolkit.fluxcd.io", Version: "v1", Name: "gitrepositories", Kind: "GitRepository", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch", "delete"}},
		{Group: "helm.toolkit.fluxcd.io", Version: "v2", Name: "helmreleases", Kind: "HelmRelease", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch", "delete"}},
	}
	if err := k8s.InitTestDynamicResourceCache(dyn, resources); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	for _, kl := range k8score.AllKindListers() {
		if b, ok := resourceid.BuiltinForKind(kl.Kind()); ok {
			k8s.GetResourceDiscovery().AddAPIResource(k8s.APIResource{Group: b.Group, Version: b.Version, Kind: b.Kind, Name: b.Resource, Namespaced: b.Namespaced, Verbs: []string{"get", "list", "watch", "delete"}})
		}
	}
	dc := k8s.GetDynamicResourceCache()
	for gvr := range gvrs {
		if err := dc.EnsureWatching(gvr); err != nil || !dc.WaitForSync(gvr, 2*time.Second) {
			t.Fatalf("%s not ready: %v", gvr.Resource, err)
		}
	}
	return k8s.GetResourceCache()
}

var ownerRefs = []WorkloadRef{
	{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "web"},
	{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "api"},
	{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "worker"},
	{Kind: "Deployment", Group: "apps", Namespace: "other", Name: "plain"},
}

func ownersByName(owners []WorkloadOwner) map[string]WorkloadOwner {
	out := map[string]WorkloadOwner{}
	for _, o := range owners {
		out[o.Name] = o
	}
	return out
}

func TestResolveOwnersNamesManagersReposAndTeams(t *testing.T) {
	cache := ownerCluster(t)
	owners, unavailable, err := resolveOwners(context.Background(), cache, ownerRefs, nil, TeamLabel)
	if err != nil || unavailable != "" {
		t.Fatalf("resolveOwners: %v %q", err, unavailable)
	}
	got := ownersByName(owners)

	web := got["web"]
	if web.Manager == nil || web.Manager.Tool != "helm" || web.Manager.Name != "web" || web.Manager.Chart != "web-1.2.0" || len(web.Manager.Sources) != 0 {
		t.Errorf("web manager = %+v", web.Manager)
	}
	if web.Team != "payments" || web.TeamFrom != "workload" {
		t.Errorf("web team = %q from %q, want the workload's own label", web.Team, web.TeamFrom)
	}

	api := got["api"]
	if api.Manager == nil || api.Manager.Tool != "argocd" || api.Manager.Group != "argoproj.io" || api.Manager.Namespace != "argocd" || api.Manager.Name != "shop-api" {
		t.Fatalf("api manager = %+v", api.Manager)
	}
	if want := []OwnerSource{{Repo: "https://github.com/acme/deploy.git", Path: "apps/api"}}; !slices.Equal(api.Manager.Sources, want) {
		t.Errorf("api sources = %+v, want %+v (credentials dropped)", api.Manager.Sources, want)
	}
	if api.Team != "storefront" || api.TeamFrom != "namespace" {
		t.Errorf("api team = %q from %q, want the namespace's label", api.Team, api.TeamFrom)
	}

	worker := got["worker"]
	if worker.Manager == nil || worker.Manager.Tool != "fluxcd" || worker.Manager.Kind != "Kustomization" {
		t.Fatalf("worker manager = %+v", worker.Manager)
	}
	if want := []OwnerSource{{Repo: "ssh://git@github.com/acme/fleet", Path: "./clusters/prod"}}; !slices.Equal(worker.Manager.Sources, want) {
		t.Errorf("worker sources = %+v, want %+v", worker.Manager.Sources, want)
	}

	if _, listed := got["plain"]; listed {
		t.Errorf("an unmanaged workload with no team is listed: %+v", got["plain"])
	}
	if api.Manager.Shared {
		t.Errorf("api is listed by one Application only: %+v", api.Manager)
	}
}

// Two Applications list one workload: its tracking id names the lead, and
// the answer says it is shared rather than calling it the only deployer.
func TestResolveOwnersSaysAManagerIsShared(t *testing.T) {
	cache := ownerCluster(t)
	owners, _, err := resolveOwners(context.Background(), cache, []WorkloadRef{{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "both"}}, nil, TeamLabel)
	if err != nil || len(owners) != 1 {
		t.Fatalf("resolveOwners: %v %+v", err, owners)
	}
	if m := owners[0].Manager; m == nil || m.Name != "shop-api" || !m.Shared {
		t.Errorf("manager = %+v, want shop-api, shared", m)
	}
}

// denyKinds may read everything but the named kinds.
type denyKinds []resourceid.GroupKind

func (d denyKinds) CanRead(kind resourceid.GroupKind, _ string) ownership.Decision {
	if slices.Contains(d, kind) {
		return ownership.Denied
	}
	return ownership.Allowed
}
func (denyKinds) Namespaces() ([]string, bool) { return nil, true }

func TestResolveOwnersReadsOnlyWhatTheCallerMay(t *testing.T) {
	cache := ownerCluster(t)
	deny := denyKinds{{Group: "argoproj.io", Kind: "Application"}, {Group: "source.toolkit.fluxcd.io", Kind: "GitRepository"}, {Kind: "Namespace"}}
	owners, _, err := resolveOwners(context.Background(), cache, ownerRefs, func(*ownership.Catalog) ownership.Access { return deny }, TeamLabel)
	if err != nil {
		t.Fatal(err)
	}
	got := ownersByName(owners)
	// An Application the caller can't read is never named from its list,
	// and its repository never leaks.
	if api := got["api"]; api.Manager != nil || api.Team != "" {
		t.Errorf("api = %+v, want no manager and no namespace team", api)
	}
	// The Kustomization is readable, its GitRepository isn't: the manager
	// is named without a repository.
	if worker := got["worker"]; worker.Manager == nil || worker.Manager.Name != "infra" || len(worker.Manager.Sources) != 0 {
		t.Errorf("worker manager = %+v, want infra without its source", worker.Manager)
	}
}

func TestOwnerCells(t *testing.T) {
	join := func(v []string) string { return strings.Join(v, "; ") }
	if got := ownerCells(nil, join); !slices.Equal(got, make([]string, len(ownerColumns))) {
		t.Errorf("no owner = %q", got)
	}
	o := &WorkloadOwner{Manager: &OwnerManager{Tool: "argocd", Kind: "Application", Namespace: "argocd", Name: "shop", Sources: []OwnerSource{{Repo: "https://a/x", Path: "p"}, {Repo: "https://b/y"}}}, Team: "pay"}
	if got, want := ownerCells(o, join), []string{"argocd_application", "argocd/shop", "https://a/x; https://b/y", "p; ", "", "pay"}; !slices.Equal(got, want) {
		t.Errorf("cells = %q, want %q", got, want)
	}
	// A pathless Helm source before a Git one keeps the path in place.
	o.Manager.Sources = []OwnerSource{{Repo: "https://charts.example.com"}, {Repo: "https://a/x", Path: "apps"}}
	if got, want := ownerCells(o, join), []string{"argocd_application", "argocd/shop", "https://charts.example.com; https://a/x", "; apps", "", "pay"}; !slices.Equal(got, want) {
		t.Errorf("cells = %q, want %q", got, want)
	}
	if got := ownerCells(&WorkloadOwner{ManagerUnknown: true}, join); got[0] != "unknown" || got[1] != "" {
		t.Errorf("unknown = %q", got)
	}
	if len(ownerColumns) != 6 {
		t.Errorf("ownerColumns = %v", ownerColumns)
	}
}

func TestSafeRepo(t *testing.T) {
	for in, want := range map[string]string{
		"https://user:token@github.com/a/b.git?x=1": "https://github.com/a/b.git",
		"git@github.com:a/b.git":                    "git@github.com:a/b.git",
		"oci://ghcr.io/a/charts":                    "oci://ghcr.io/a/charts",
		"ssh://git:pw@github.com/a/b":               "ssh://git@github.com/a/b",
		"ssh://ghp_token@github.com/a/b":            "ssh://github.com/a/b",
		"token@github.com:a/b.git":                  "github.com:a/b.git",
		"git@github.com:a/b.git?private_token=x#y":  "",
		"minio.example:9000?token=left@right/b":     "",
		"user:pa/ss@host:repo":                      "host:repo",
		"key:secret@minio.local:9000/bucket":        "minio.local:9000/bucket",
		"minio.local:9000/bucket":                   "minio.local:9000/bucket",
		"us?er:pa#ss@github.com:a/b.git":            "",
	} {
		if got := safeRepo(in); got != want {
			t.Errorf("safeRepo(%q) = %q, want %q", in, got, want)
		}
	}
}

// The workload and findings exports carry each workload's owner; a
// workload Radar knows nothing about has the cells empty, never a guess.
func TestExportOwnerColumns(t *testing.T) {
	items, targets := exposureFixture()
	pending := WorkloadRef{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "pending"}
	targets = append(targets, liveTarget("shop", "ReplicaSet", "pending-1", "app", "acme/pending:1", "", pending))
	owners := []WorkloadOwner{
		{WorkloadRef: WorkloadRef{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "checkout"}, Manager: &OwnerManager{Tool: "helm", Kind: "HelmRelease", Namespace: "shop", Name: "checkout", Chart: "checkout-1.0.0"}, Team: "payments"},
		{WorkloadRef: pending, Manager: &OwnerManager{Tool: "fluxcd", Kind: "Kustomization", Group: "kustomize.toolkit.fluxcd.io", Namespace: "flux-system", Name: "apps", Sources: []OwnerSource{{Repo: "https://github.com/acme/fleet", Path: "./apps"}}}},
	}
	for _, view := range []View{ViewWorkload, ViewFindings} {
		resp := mustExport(t, items, targets, view, Filter{})
		resp.Owners = owners
		for _, r := range exportRows(t, resp, Filter{}) {
			got := []string{r["owner_kind"], r["owner"], r["source_repo"], r["source_path"], r["chart"], r["team"]}
			var want []string
			switch r["workload"] {
			case "checkout":
				want = []string{"helm_release", "shop/checkout", "", "", "checkout-1.0.0", "payments"}
			case "pending":
				want = []string{"flux_kustomization", "flux-system/apps", "https://github.com/acme/fleet", "./apps", "", ""}
			default:
				want = make([]string, 6)
			}
			if !slices.Equal(got, want) {
				t.Errorf("%s %s (%s): owner cells %q, want %q", view, r["workload"], r["status"], got, want)
			}
		}
	}
	for _, view := range []View{ViewImage, ViewFix, ViewCVE} {
		if slices.Contains(ExportColumns[view], "owner") {
			t.Errorf("%s rows list several workloads; owner columns belong on one-workload rows", view)
		}
	}
}

func TestPruneOwnersKeepsNamedWorkloads(t *testing.T) {
	kept := WorkloadRef{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "a"}
	resp := Response{
		Workloads: []Workload{{Kind: kept.Kind, Group: kept.Group, Namespace: kept.Namespace, Name: kept.Name}},
		Owners:    []WorkloadOwner{{WorkloadRef: kept, Team: "x"}, {WorkloadRef: WorkloadRef{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "b"}, Team: "y"}},
	}
	resp.PruneOwners()
	if len(resp.Owners) != 1 || resp.Owners[0].Name != "a" {
		t.Fatalf("owners = %+v", resp.Owners)
	}
}

// With both spec.source and spec.sources set, Argo CD deploys sources, so
// the owner names those, not the inactive source.
func TestArgoSourcesPrefersSources(t *testing.T) {
	app := ownerObject("argoproj.io/v1alpha1", "Application", "argocd", "shop", map[string]any{
		"source":  map[string]any{"repoURL": "https://github.com/acme/old.git", "path": "old"},
		"sources": []any{map[string]any{"repoURL": "https://charts.example.com", "chart": "shop"}, map[string]any{"repoURL": "https://github.com/acme/deploy.git", "path": "apps"}},
	}, nil)
	// Which of the two sources made a workload is unknown, so the chart
	// stays on its source.
	sources, chart := argoSources(app)
	if want := []OwnerSource{{Repo: "https://charts.example.com", Chart: "shop"}, {Repo: "https://github.com/acme/deploy.git", Path: "apps"}}; !slices.Equal(sources, want) || chart != "" {
		t.Errorf("argoSources = %+v, %q; want %+v, no chart", sources, chart, want)
	}
	single := ownerObject("argoproj.io/v1alpha1", "Application", "argocd", "one", map[string]any{
		"source": map[string]any{"repoURL": "https://charts.example.com", "chart": "shop"},
	}, nil)
	if sources, chart := argoSources(single); chart != "shop" || len(sources) != 1 || sources[0].Chart != "" {
		t.Errorf("single source = %+v, %q", sources, chart)
	}
}

// A workload whose store can't say whether it exists reads as unknown,
// never as "no owner found"; one the store has loaded without it is left
// out.
func TestResolveOwnersUnreadWorkloadIsUnknown(t *testing.T) {
	cache := ownerCluster(t)
	unread := WorkloadRef{Kind: "Rollout", Group: "argoproj.io", Namespace: "shop", Name: "canary"}
	gone := WorkloadRef{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "gone"}
	owners, _, err := resolveOwners(context.Background(), cache, []WorkloadRef{unread, gone}, nil, TeamLabel)
	if err != nil {
		t.Fatal(err)
	}
	if len(owners) != 1 || owners[0].WorkloadRef != unread || !owners[0].ManagerUnknown || owners[0].Manager != nil {
		t.Errorf("owners = %+v, want only the Rollout, unknown", owners)
	}
}
