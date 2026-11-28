package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/yaml"

	"github.com/skyhook-io/radar/internal/auth"
	integration "github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/kargo"
)

// The objects come from a live Kargo v1.12.7 project (pkg/kargo/testdata):
// test, uat and prod in project kargo-demo; prod holds nginx 1.31.9, test and
// uat hold 1.31.10.
func kargoFixture(t *testing.T, file string) []*unstructured.Unstructured {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "pkg", "kargo", "testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := yaml.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	out := make([]*unstructured.Unstructured, len(list.Items))
	for i, item := range list.Items {
		out[i] = &unstructured.Unstructured{Object: item}
	}
	return out
}

func kargoTestKinds() []k8s.APIResource {
	res := func(kind, plural string, namespaced bool) k8s.APIResource {
		return k8s.APIResource{Group: kargo.Group, Version: "v1alpha1", Kind: kind, Name: plural, Namespaced: namespaced, IsCRD: true, Verbs: []string{"get", "list", "watch"}}
	}
	return []k8s.APIResource{
		res(kargo.KindStage, "stages", true),
		res(kargo.KindFreight, "freights", true),
		res(kargo.KindWarehouse, "warehouses", true),
		res(kargo.KindProject, "projects", false),
	}
}

func seedKargo(t *testing.T) {
	t.Helper()
	var objs []runtime.Object
	for _, file := range []string{"stages.yaml", "freight.yaml", "warehouses.yaml"} {
		for _, u := range kargoFixture(t, file) {
			objs = append(objs, u)
		}
	}
	seedSyncedCNPGWorkspace(t, kargoTestKinds(), objs...)
}

func stageView(t *testing.T, resp KargoProjectResponse, name string) KargoStageView {
	t.Helper()
	for _, s := range resp.Stages {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("stage %s not in %+v", name, resp.Stages)
	return KargoStageView{}
}

func TestBuildKargoProject_PipelineVersionsAndAliases(t *testing.T) {
	stages, freight, warehouses := kargoFixture(t, "stages.yaml"), kargoFixture(t, "freight.yaml"), kargoFixture(t, "warehouses.yaml")
	got := buildKargoProject("kargo-demo", kargoProjectInputs{stagesRead: true, stages: stages, freightRead: true, freight: freight, warehousesRead: true, warehouses: warehouses}, time.Now())

	var order []string
	for _, s := range got.Stages {
		order = append(order, s.Name)
	}
	if len(order) != 3 || order[0] != "test" || order[1] != "uat" || order[2] != "prod" {
		t.Fatalf("pipeline order = %v, want [test uat prod]", order)
	}
	prod := stageView(t, got, "prod")
	if len(prod.Current) != 1 || prod.Current[0].Versions[0] != "nginx:1.31.9" || prod.Current[0].Alias == "" {
		t.Fatalf("prod current = %+v, want nginx:1.31.9 with an alias", prod.Current)
	}
	if len(prod.WhyNotHere) != 1 || prod.WhyNotHere[0].Reason != string(kargo.ReasonVerificationFailed) {
		t.Fatalf("prod whyNotHere = %+v, want the failed verification in uat", prod.WhyNotHere)
	}
	if runs := prod.WhyNotHere[0].AnalysisRuns; len(runs) != 1 || runs[0].Namespace != "kargo-demo" || !strings.HasPrefix(runs[0].Name, "uat.") {
		t.Fatalf("prod whyNotHere analysis runs = %+v, want uat's AnalysisRun", runs)
	}
	uat := stageView(t, got, "uat")
	if uat.Verification == nil || uat.Verification.Phase != "Failed" || uat.Verification.AnalysisRun == nil {
		t.Fatalf("uat verification = %+v, want Failed with its AnalysisRun", uat.Verification)
	}
	if test := stageView(t, got, "test"); test.LastPromotion == nil || test.LastPromotion.Phase != "Errored" || !strings.HasPrefix(test.LastPromotion.Failure, `step "promo-process::step-6" met error threshold`) ||
		!strings.HasPrefix(test.LastPromotion.FailureCause, "error pushing commits to remote: ") {
		t.Fatalf("test last promotion = %+v, want Errored with its full failure and its cause", test.LastPromotion)
	}
	if len(got.Freight) != 2 || got.FreightTotal != 2 {
		t.Fatalf("freight = %d (total %d), want 2", len(got.Freight), got.FreightTotal)
	}
	if _, ok := got.Freight[0]["metadata"].(map[string]any)["annotations"]; ok {
		t.Error("freight objects must not carry annotations")
	}
	if len(got.Warehouses) != 1 || got.Warehouses[0].Latest == nil || got.Warehouses[0].FreightCount != 2 {
		t.Fatalf("warehouses = %+v, want kargo-demo with its latest of 2 Freight", got.Warehouses)
	}
	if runs := got.VerificationRuns[uat.Current[0].Name]; len(runs) == 0 {
		t.Errorf("no verification runs for uat's Freight: %+v", got.VerificationRuns)
	}
}

// Stages readable and Freight denied: versions from the Stages, no aliases,
// no Freight-derived answers, and warehouses without Freight.
func TestBuildKargoProject_FreightDeniedKeepsVersions(t *testing.T) {
	stages, warehouses := kargoFixture(t, "stages.yaml"), kargoFixture(t, "warehouses.yaml")
	got := buildKargoProject("kargo-demo", kargoProjectInputs{stagesRead: true, stages: stages, warehousesRead: true, warehouses: warehouses}, time.Now())
	if got.FreightRead || len(got.Freight) != 0 {
		t.Fatalf("freightRead=%v freight=%d, want unread", got.FreightRead, len(got.Freight))
	}
	for _, s := range got.Stages {
		for _, c := range s.Current {
			if c.Alias != "" || c.Since != "" || len(c.Versions) == 0 {
				t.Errorf("%s current = %+v, want versions without alias or since", s.Name, c)
			}
		}
	}
	prod := stageView(t, got, "prod")
	if lag := prod.WhyNotHere[0]; lag.State == string(kargo.LagBehind) && lag.Reason != string(kargo.ReasonPromotionFailed) {
		t.Errorf("prod lag = %+v: a Freight-dependent reason was asserted without Freight", lag)
	}
	if wh := got.Warehouses[0]; wh.Latest != nil || wh.FreightCount != 0 {
		t.Errorf("warehouse = %+v, want no Freight facts", wh)
	}
}

func getKargoJSON[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out T
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestKargoProject_RestrictedReader(t *testing.T) {
	seedKargo(t)
	env := newAuthTestServer(t)

	full := &auth.UserPermissions{AllowedNamespaces: []string{"kargo-demo"}}
	for _, res := range []string{"stages", "freights", "warehouses"} {
		allow(full, kargo.Group, res, "", false)
		allow(full, kargo.Group, res, "kargo-demo", true)
	}
	env.srv.permCache.Set("reader", nil, full)

	stagesOnly := &auth.UserPermissions{AllowedNamespaces: []string{"kargo-demo"}}
	allow(stagesOnly, kargo.Group, "stages", "", false)
	allow(stagesOnly, kargo.Group, "stages", "kargo-demo", true)
	for _, res := range []string{"freights", "warehouses"} {
		allow(stagesOnly, kargo.Group, res, "", false)
		allow(stagesOnly, kargo.Group, res, "kargo-demo", false)
	}
	env.srv.permCache.Set("stages-only", nil, stagesOnly)

	control := getKargoJSON[KargoProjectResponse](t, env.authGet(t, "/api/kargo/projects/kargo-demo", "reader", ""))
	if !control.FreightRead || stageView(t, control, "prod").Current[0].Alias == "" || len(control.Warehouses) != 1 {
		t.Fatalf("control: freightRead=%v warehouses=%d prod=%+v", control.FreightRead, len(control.Warehouses), stageView(t, control, "prod").Current)
	}

	got := getKargoJSON[KargoProjectResponse](t, env.authGet(t, "/api/kargo/projects/kargo-demo", "stages-only", ""))
	if got.Coverage["freight"].State != integration.KindCoverageDenied || got.Coverage["warehouses"].State != integration.KindCoverageDenied {
		t.Fatalf("coverage = %+v, want freight and warehouses denied", got.Coverage)
	}
	if got.Coverage["stages"].State != integration.KindCoverageFull || len(got.Stages) != 3 {
		t.Fatalf("stages coverage=%+v count=%d, want full and 3", got.Coverage["stages"], len(got.Stages))
	}
	prod := stageView(t, got, "prod")
	if prod.Current[0].Alias != "" || prod.Current[0].Versions[0] != "nginx:1.31.9" {
		t.Errorf("prod current = %+v, want versions without alias", prod.Current)
	}
	if got.FreightRead || len(got.Freight) != 0 || len(got.Warehouses) != 0 {
		t.Errorf("freightRead=%v freight=%d warehouses=%d, want nothing Freight or Warehouse", got.FreightRead, len(got.Freight), len(got.Warehouses))
	}

	aliases := getKargoJSON[KargoFreightAliasesResponse](t, env.authGet(t, "/api/kargo/freight-aliases?namespaces=kargo-demo", "stages-only", ""))
	if len(aliases.Aliases) != 0 || aliases.Coverage.State != integration.KindCoverageDenied {
		t.Errorf("aliases = %+v, want none and denied", aliases)
	}
	readerAliases := getKargoJSON[KargoFreightAliasesResponse](t, env.authGet(t, "/api/kargo/freight-aliases?namespaces=kargo-demo", "reader", ""))
	if len(readerAliases.Aliases) != 2 {
		t.Errorf("reader aliases = %+v, want 2", readerAliases.Aliases)
	}
}

func TestKargoWorkspace_ProjectsFollowStageAccess(t *testing.T) {
	seedKargo(t)
	env := newAuthTestServer(t)

	reader := &auth.UserPermissions{AllowedNamespaces: []string{"kargo-demo"}}
	allow(reader, kargo.Group, "stages", "", false)
	allow(reader, kargo.Group, "stages", "kargo-demo", true)
	allow(reader, kargo.Group, "projects", "", false)
	env.srv.permCache.Set("reader", nil, reader)

	none := &auth.UserPermissions{AllowedNamespaces: []string{"kargo-demo"}}
	allow(none, kargo.Group, "stages", "", false)
	allow(none, kargo.Group, "stages", "kargo-demo", false)
	allow(none, kargo.Group, "projects", "", false)
	env.srv.permCache.Set("none", nil, none)

	got := getKargoJSON[KargoWorkspaceResponse](t, env.authGet(t, "/api/kargo/workspace", "reader", ""))
	if !got.Installed || len(got.Projects) != 1 || got.Projects[0].Name != "kargo-demo" || got.Projects[0].Stages != 3 {
		t.Fatalf("workspace = %+v, want kargo-demo with 3 stages", got)
	}

	denied := getKargoJSON[KargoWorkspaceResponse](t, env.authGet(t, "/api/kargo/workspace", "none", ""))
	if len(denied.Projects) != 0 || denied.Coverage["stages"].State != integration.KindCoverageDenied {
		t.Errorf("no-access workspace = %+v, want no projects and stages denied", denied)
	}
}

func TestKargoProject_RejectsInvalidName(t *testing.T) {
	seedKargo(t)
	resp, err := http.Get(testServer.URL + "/api/kargo/projects/Not_A_Namespace")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// A Project without Stages is listed for a caller who can list Stages in its
// namespace but not cluster-wide.
func TestKargoWorkspace_EmptyProjectForNamespaceScopedReader(t *testing.T) {
	var objs []runtime.Object
	for _, file := range []string{"stages.yaml", "freight.yaml", "warehouses.yaml"} {
		for _, u := range kargoFixture(t, file) {
			objs = append(objs, u)
		}
	}
	objs = append(objs, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": kargo.Group + "/v1alpha1", "kind": kargo.KindProject,
		"metadata": map[string]any{"name": "payments"},
	}})
	seedSyncedCNPGWorkspace(t, kargoTestKinds(), objs...)
	env := newAuthTestServer(t)

	perms := &auth.UserPermissions{AllowedNamespaces: []string{"kargo-demo", "payments"}}
	allow(perms, kargo.Group, "stages", "", false)
	allow(perms, kargo.Group, "stages", "kargo-demo", true)
	allow(perms, kargo.Group, "stages", "payments", true)
	allow(perms, kargo.Group, "projects", "", true)
	env.srv.permCache.Set("scoped", nil, perms)

	other := &auth.UserPermissions{AllowedNamespaces: []string{"kargo-demo", "payments"}}
	allow(other, kargo.Group, "stages", "", false)
	allow(other, kargo.Group, "stages", "kargo-demo", true)
	allow(other, kargo.Group, "stages", "payments", false)
	allow(other, kargo.Group, "projects", "", true)
	env.srv.permCache.Set("other", nil, other)

	names := func(resp KargoWorkspaceResponse) []string {
		var out []string
		for _, p := range resp.Projects {
			out = append(out, p.Name)
		}
		return out
	}
	got := getKargoJSON[KargoWorkspaceResponse](t, env.authGet(t, "/api/kargo/workspace", "scoped", ""))
	if n := names(got); len(n) != 2 || n[0] != "kargo-demo" || n[1] != "payments" {
		t.Errorf("projects = %v, want [kargo-demo payments]", n)
	}
	// Without Stage access in payments, the Project isn't listed.
	denied := getKargoJSON[KargoWorkspaceResponse](t, env.authGet(t, "/api/kargo/workspace", "other", ""))
	if n := names(denied); len(n) != 1 || n[0] != "kargo-demo" {
		t.Errorf("projects = %v, want [kargo-demo]", n)
	}
}

// Every Warehouse keeps its own newest Freight in the answer, however much
// newer Freight other Warehouses produced.
func TestBuildKargoProject_EachWarehouseKeepsItsNewestFreight(t *testing.T) {
	base := time.Now().Add(-48 * time.Hour)
	freight := func(warehouse string, i int, at time.Time) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": kargo.Group + "/v1alpha1", "kind": kargo.KindFreight,
			"metadata":     map[string]any{"name": warehouse + "-" + strconv.Itoa(i), "namespace": "p"},
			"origin":       map[string]any{"kind": kargo.KindWarehouse, "name": warehouse},
			"discoveredAt": at.UTC().Format(time.RFC3339),
		}}
	}
	var items []*unstructured.Unstructured
	for i := range 3 {
		items = append(items, freight("old", i, base.Add(time.Duration(i)*time.Minute)))
	}
	for i := range kargoFreightPerWarehouse + 150 {
		items = append(items, freight("busy", i, base.Add(time.Hour+time.Duration(i)*time.Minute)))
	}
	got := buildKargoProject("p", kargoProjectInputs{freightRead: true, freight: items}, time.Now())
	count := map[string]int{}
	for _, f := range got.Freight {
		count[f["origin"].(map[string]any)["name"].(string)]++
	}
	if count["old"] != 3 || count["busy"] != kargoFreightPerWarehouse {
		t.Errorf("freight per warehouse = %v, want old 3 and busy %d", count, kargoFreightPerWarehouse)
	}
	if got.FreightByWarehouse["old"] != 3 || got.FreightByWarehouse["busy"] != kargoFreightPerWarehouse+150 {
		t.Errorf("freightByWarehouse = %v, want old 3 and busy %d", got.FreightByWarehouse, kargoFreightPerWarehouse+150)
	}
	if got.FreightTotal != len(items) {
		t.Errorf("freightTotal = %d, want %d", got.FreightTotal, len(items))
	}
}

// A project lists the namespaces its Stages' Argo CD Applications deploy to
// in this cluster, so the page can open the project behind an app namespace.
func TestKargoWorkspace_DeployNamespaces(t *testing.T) {
	var objs []runtime.Object
	for _, file := range []string{"stages.yaml", "freight.yaml", "warehouses.yaml"} {
		for _, u := range kargoFixture(t, file) {
			objs = append(objs, u)
		}
	}
	app := func(name, server, namespace string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
			"metadata": map[string]any{"name": name, "namespace": "argocd"},
			"spec":     map[string]any{"destination": map[string]any{"server": server, "namespace": namespace}},
		}}
	}
	objs = append(objs,
		app("kargo-demo-prod", "https://kubernetes.default.svc", "kargo-demo-prod"),
		app("kargo-demo-test", "https://remote.example.com", "kargo-demo-test"),
	)
	kinds := append(kargoTestKinds(), k8s.APIResource{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application", Name: "applications", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}})
	seedSyncedCNPGWorkspace(t, kinds, objs...)

	resp, err := http.Get(testServer.URL + "/api/kargo/workspace")
	if err != nil {
		t.Fatal(err)
	}
	got := getKargoJSON[KargoWorkspaceResponse](t, resp)
	if len(got.Projects) != 1 {
		t.Fatalf("projects = %+v", got.Projects)
	}
	// test's app deploys to another cluster, so its namespace isn't this one's.
	if ns := got.Projects[0].DeployNamespaces; len(ns) != 1 || ns[0] != "kargo-demo-prod" {
		t.Errorf("deploy namespaces = %v, want [kargo-demo-prod]", ns)
	}
}

// Radar's own identity can't list Kargo: the answer says Radar's access is
// missing (uncached), for a caller with cluster-wide access and for one
// checked project by project, never that the caller is denied.
func TestKargoWorkspace_RadarIdentityDenied(t *testing.T) {
	listKinds := map[schema.GroupVersionResource]string{}
	for _, k := range kargoTestKinds() {
		listKinds[schema.GroupVersionResource{Group: k.Group, Version: k.Version, Resource: k.Name}] = k.Kind + "List"
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
	dyn.PrependReactor("*", "*", func(a k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(a.GetResource().GroupResource(), "", errors.New("radar may not list"))
	})
	if err := k8s.InitTestDynamicResourceCacheWithFallbacks(dyn, kargoTestKinds(), nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)

	resp, err := http.Get(testServer.URL + "/api/kargo/workspace")
	if err != nil {
		t.Fatal(err)
	}
	got := getKargoJSON[KargoWorkspaceResponse](t, resp)
	if !got.Installed || len(got.Projects) != 0 || got.Coverage["stages"].State != integration.KindCoverageUncached {
		t.Fatalf("workspace = %+v, want installed, no projects, stages uncached", got)
	}

	env := newAuthTestServer(t)
	scoped := &auth.UserPermissions{AllowedNamespaces: []string{"kargo-demo"}}
	allow(scoped, kargo.Group, "stages", "", false)
	allow(scoped, kargo.Group, "stages", "kargo-demo", true)
	allow(scoped, kargo.Group, "projects", "", false)
	env.srv.permCache.Set("scoped", nil, scoped)
	if got := getKargoJSON[KargoWorkspaceResponse](t, env.authGet(t, "/api/kargo/workspace", "scoped", "")); got.Coverage["stages"].State != integration.KindCoverageUncached {
		t.Errorf("namespace-scoped caller: stages coverage = %+v, want uncached", got.Coverage["stages"])
	}
}
