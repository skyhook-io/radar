package helm

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"helm.sh/helm/v3/pkg/release"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/readscope"
	"github.com/skyhook-io/radar/internal/search"
	"github.com/skyhook-io/radar/pkg/readreason"
)

// A platform chart stored in team-a that runs its worker in the shared
// namespace: the app Pod is healthy, the worker can't pull its image.
const crossNamespaceManifest = `apiVersion: v1
kind: Pod
metadata:
  name: payments-app
  namespace: team-a
---
apiVersion: v1
kind: Pod
metadata:
  name: payments-worker
  namespace: shared
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: payments-settings
`

func useCrossNamespaceCache(t *testing.T) {
	t.Helper()
	running := corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{
		Name: "app", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	}}}
	pulling := corev1.PodStatus{Phase: corev1.PodPending, ContainerStatuses: []corev1.ContainerStatus{{
		Name: "worker", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}},
	}}}
	client := fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shared"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "payments-app"}, Status: running},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shared", Name: "payments-worker"}, Status: pulling},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "payments-settings"}},
	)
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestState)
}

func crossNamespaceRelease() *release.Release {
	rel := helmTestRelease("payments", "team-a", 1, release.StatusDeployed, "Install complete")
	rel.Manifest = crossNamespaceManifest
	return rel
}

// Two callers on one shared cache: dev-a sees team-a only, admin everything.
func teamACaller() *ObjectScope {
	return &ObjectScope{Access: search.ScanAccess{Selection: readscope.Selection{Namespaces: []string{"team-a"}}}}
}

func adminCaller() *ObjectScope { return &ObjectScope{} }

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestReleaseListHealthCoversOnlyWhatEachCallerCanRead(t *testing.T) {
	useCrossNamespaceCache(t)
	client := fake.NewSimpleClientset(helmReleaseSecret(t, "team-a", crossNamespaceRelease(), false))
	list := func(scope *ObjectScope) HelmRelease {
		t.Helper()
		rows, err := listReleasesAcrossNamespacesWithClient(context.Background(), client, "", []string{"team-a"}, true, scope)
		if err != nil || len(rows) != 1 {
			t.Fatalf("rows = %+v, %v", rows, err)
		}
		return rows[0]
	}

	admin := list(adminCaller())
	if admin.ResourceHealth != "unhealthy" || admin.HealthIssue != "ImagePullBackOff" || admin.StatusUnreadCount != 0 {
		t.Fatalf("admin row = %+v, want the worker's failure", admin)
	}
	devA := list(teamACaller())
	if devA.ResourceHealth != "unknown" || devA.HealthIssue != "" || devA.HealthSummary != "1/1 ready" || devA.StatusUnreadCount != 1 {
		t.Fatalf("dev-a row = %+v, want unknown health over the one Pod they can read", devA)
	}
	if strings.Contains(mustJSON(t, devA), "ImagePullBackOff") {
		t.Fatalf("dev-a row carries the shared worker's status: %s", mustJSON(t, devA))
	}
	if again := list(adminCaller()); again.ResourceHealth != "unhealthy" {
		t.Fatalf("admin after dev-a = %+v", again)
	}
}

func TestReleaseDetailWithholdsStatusOfObjectsTheCallerCantRead(t *testing.T) {
	useCrossNamespaceCache(t)
	cfg := memoryActionConfig(t)
	if err := cfg.Releases.Create(crossNamespaceRelease()); err != nil {
		t.Fatal(err)
	}
	detail := func(scope *ObjectScope) (*HelmReleaseDetail, map[string]OwnedResource) {
		t.Helper()
		d, err := getReleaseWith(cfg, "", "team-a", "payments", scope)
		if err != nil {
			t.Fatal(err)
		}
		byName := map[string]OwnedResource{}
		for _, r := range d.Resources {
			byName[r.Name] = r
		}
		return d, byName
	}

	devA, resources := detail(teamACaller())
	worker := resources["payments-worker"]
	if worker.Status != "" || worker.Ready != "" || worker.Issue != "" || worker.StatusUnread != readreason.UserNamespaceNotVisible {
		t.Fatalf("dev-a worker = %+v, want no status and a reason", worker)
	}
	if app := resources["payments-app"]; app.Status != "Running" || app.StatusUnread != "" {
		t.Fatalf("dev-a app = %+v, want its own status", app)
	}
	if devA.ResourceHealth != "unknown" || devA.StatusUnreadCount != 1 || strings.Contains(mustJSON(t, devA), "ImagePullBackOff") {
		t.Fatalf("dev-a detail = %s", mustJSON(t, devA))
	}

	admin, resources := detail(adminCaller())
	if resources["payments-worker"].Issue != "ImagePullBackOff" || admin.ResourceHealth != "unhealthy" || admin.StatusUnreadCount != 0 {
		t.Fatalf("admin detail = %s", mustJSON(t, admin))
	}
}

func TestHealthDoesNotClaimHealthyOverWithheldWorkloads(t *testing.T) {
	running := OwnedResource{Kind: "Pod", APIVersion: "v1", Name: "app", Status: "Running", Ready: "1/1"}
	for _, tc := range []struct {
		name        string
		resources   []OwnedResource
		wantHealth  string
		wantSummary string
	}{
		{"withheld Pod", []OwnedResource{running, {Kind: "Pod", APIVersion: "v1", Name: "w", StatusUnread: readreason.UserForbidden}}, "unknown", "1/1 ready"},
		{"withheld Deployment", []OwnedResource{running, {Kind: "Deployment", APIVersion: "apps/v1", Name: "w", StatusUnread: readreason.UserPermissionCheckFailed}}, "unknown", "1/1 ready"},
		{"withheld ConfigMap", []OwnedResource{running, {Kind: "ConfigMap", APIVersion: "v1", Name: "c", StatusUnread: readreason.UserForbidden}}, "healthy", "1/1 ready"},
		{"readable failure", []OwnedResource{{Kind: "Pod", APIVersion: "v1", Name: "app", Status: "ImagePullBackOff", Issue: "ImagePullBackOff"}, {Kind: "Pod", APIVersion: "v1", Name: "w", StatusUnread: readreason.UserForbidden}}, "unhealthy", "0/1 ImagePullBackOff"},
		{"custom kind named Pod", []OwnedResource{running, {Kind: "Pod", APIVersion: "example.com/v1", Name: "lookalike"}}, "healthy", "1/1 ready"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			health, _, summary := computeResourceHealth(tc.resources)
			if health != tc.wantHealth || summary != tc.wantSummary {
				t.Fatalf("health %q summary %q, want %q %q", health, summary, tc.wantHealth, tc.wantSummary)
			}
		})
	}
}

func TestNoStatusAccessReadsNoStatus(t *testing.T) {
	useCrossNamespaceCache(t)
	for name, access := range map[string]StatusAccess{"nil": nil, "nil scope": (*ObjectScope)(nil)} {
		t.Run(name, func(t *testing.T) {
			resources := parseManifestResources(crossNamespaceManifest, "team-a")
			health, issue, summary := readReleaseStatus(resources, access)
			if health != "unknown" || issue != "" || summary != "" {
				t.Fatalf("health %q issue %q summary %q", health, issue, summary)
			}
			for _, r := range resources {
				if r.Status != "" {
					t.Fatalf("%s read status %q", r.Name, r.Status)
				}
			}
		})
	}
}

func TestStatusUnreadCountCoversOnlyHealthObjects(t *testing.T) {
	resources := []OwnedResource{
		{Kind: "Pod", APIVersion: "v1", StatusUnread: readreason.UserForbidden},
		{Kind: "Deployment", APIVersion: "apps/v1", StatusUnread: readreason.UserPermissionCheckFailed},
		{Kind: "Role", APIVersion: "rbac.authorization.k8s.io/v1", StatusUnread: readreason.UserForbidden},
		{Kind: "Secret", APIVersion: "v1", StatusUnread: readreason.UserForbidden},
		{Kind: "CustomResourceDefinition", APIVersion: "apiextensions.k8s.io/v1", StatusUnread: readreason.UserForbidden},
		{Kind: "Pod", APIVersion: "example.com/v1", StatusUnread: readreason.UserForbidden},
	}
	if got := statusUnreadCount(resources); got != 2 {
		t.Fatalf("health coverage count=%d, want 2; full unread markers must stay on each resource", got)
	}
	if resources[2].StatusUnread == "" || resources[3].StatusUnread == "" {
		t.Fatal("non-health read markers were removed")
	}
}

func TestReleaseListKeepsHealthAlignedAcrossNamespaces(t *testing.T) {
	useCrossNamespaceCache(t)
	healthy := helmTestRelease("healthy", "team-a", 1, release.StatusDeployed, "Install complete")
	healthy.Manifest = "apiVersion: v1\nkind: Pod\nmetadata:\n  name: payments-app\n  namespace: team-a\n"
	unhealthy := helmTestRelease("unhealthy", "shared", 1, release.StatusDeployed, "Install complete")
	unhealthy.Manifest = "apiVersion: v1\nkind: Pod\nmetadata:\n  name: payments-worker\n  namespace: shared\n"
	client := fake.NewSimpleClientset(helmReleaseSecret(t, "team-a", healthy, false), helmReleaseSecret(t, "shared", unhealthy, false))
	for _, namespaces := range [][]string{{"team-a", "shared"}, {"shared", "team-a"}} {
		rows, err := listReleasesAcrossNamespacesWithClient(context.Background(), client, "", namespaces, true, adminCaller())
		if err != nil || len(rows) != 2 {
			t.Fatalf("rows=%d err=%v", len(rows), err)
		}
		for _, row := range rows {
			if row.Name != row.ResourceHealth {
				t.Fatalf("release %s received another namespace's health: %s", row.Name, row.ResourceHealth)
			}
		}
	}
}

// A release installed when Deployments were served under extensions keeps that
// group in its stored manifest; its live Deployment is still read and counted.
func TestLegacyBuiltInGroupsStillReadAndCount(t *testing.T) {
	useCrossNamespaceCache(t)
	resources := []OwnedResource{{Kind: "Pod", APIVersion: "v1", Name: "payments-app", Namespace: "team-a"}, {Kind: "Pod", APIVersion: "v1", Name: "payments-worker", Namespace: "shared"}}
	enrichResourcesWithStatus(resources, adminCaller())
	legacy := OwnedResource{Kind: "Deployment", APIVersion: "extensions/v1beta1", Name: "old", Status: "Progressing", Ready: "0/1"}
	if _, ok := builtInGroup(legacy); !ok {
		t.Fatal("extensions/v1beta1 Deployment isn't treated as a Deployment")
	}
	if health, _, _ := computeResourceHealth([]OwnedResource{resources[0], legacy}); health != "degraded" {
		t.Fatalf("health = %q, want the legacy Deployment counted", health)
	}
	if _, ok := builtInGroup(OwnedResource{Kind: "Pod", APIVersion: "extensions/v1beta1"}); ok {
		t.Fatal("Pods were never served under extensions")
	}
}

func TestStatusLookupSkipsCustomKindsNamedLikeBuiltIns(t *testing.T) {
	useCrossNamespaceCache(t)
	resources := []OwnedResource{{Kind: "Pod", APIVersion: "example.com/v1", Name: "payments-app", Namespace: "team-a"}}
	enrichResourcesWithStatus(resources, adminCaller())
	if resources[0].Status != "" {
		t.Fatalf("custom kind read the core Pod's status %q", resources[0].Status)
	}
}

func TestNamespaceStatusScope(t *testing.T) {
	useCrossNamespaceCache(t)
	read := func(scope NamespaceStatusScope) map[string]OwnedResource {
		resources := parseManifestResources(crossNamespaceManifest+`---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: payments-reader
`, "team-a")
		enrichResourcesWithStatus(resources, scope)
		byName := map[string]OwnedResource{}
		for _, r := range resources {
			byName[r.Name] = r
		}
		return byName
	}
	devA := read(NamespaceStatusScope{Namespaces: []string{"team-a"}, Checked: true})
	if devA["payments-worker"].Status != "" || devA["payments-worker"].StatusUnread != readreason.UserNamespaceNotVisible || devA["payments-app"].Status != "Running" {
		t.Fatalf("team-a scope = %+v", devA)
	}
	if cr := devA["payments-reader"]; cr.Status != "" || cr.StatusUnread != "" {
		t.Fatalf("cluster-scoped object = %+v, want no lookup and no marker", cr)
	}
	if all := read(NamespaceStatusScope{Checked: true}); all["payments-worker"].Issue != "ImagePullBackOff" {
		t.Fatalf("full scope worker = %+v", all["payments-worker"])
	}
	if zero := read(NamespaceStatusScope{}); zero["payments-app"].Status != "" || zero["payments-app"].StatusUnread != readreason.UserPermissionCheckFailed {
		t.Fatalf("zero scope app = %+v, want nothing read", zero["payments-app"])
	}
	keys := map[string]bool{}
	scopes := []NamespaceStatusScope{{}, {Checked: true}, {Checked: true, Namespaces: []string{}}, {Checked: true, Namespaces: []string{"team-a"}},
		{Checked: true, Namespaces: []string{"unchecked"}}, {Checked: true, Namespaces: []string{"all"}}}
	for _, s := range scopes {
		keys[s.CacheKey()] = true
	}
	if len(keys) != len(scopes) {
		t.Fatalf("cache keys collide: %v", keys)
	}
}

func TestObjectScopeLeavesObjectsOutsideThePinAlone(t *testing.T) {
	useCrossNamespaceCache(t)
	previous := k8s.ForceNamespaceScope
	k8s.ForceNamespaceScope = true
	k8s.SetNamespaceScopeOverride("team-a")
	t.Cleanup(func() {
		k8s.ForceNamespaceScope = previous
		k8s.SetNamespaceScopeOverride("")
	})
	resources := parseManifestResources(crossNamespaceManifest, "team-a")
	enrichResourcesWithStatus(resources, adminCaller())
	for _, r := range resources {
		if r.Name == "payments-worker" && (r.Status != "" || r.StatusUnread != "") {
			t.Fatalf("outside the pin = %+v, want no lookup and no marker", r)
		}
	}
}

// A list checks the caller's access once per kind across all of its releases,
// not once per object: dev-a's exact-kind reviews are one cluster-wide and one
// per visible namespace for each kind, however many releases there are. Every
// row reads as it does when each object is checked on its own.
func TestReleaseListChecksAccessPerKindNotPerObject(t *testing.T) {
	useCrossNamespaceCache(t)
	const manifest = crossNamespaceManifest + `---
apiVersion: v1
kind: Secret
metadata:
  name: payments-key
  namespace: team-a
---
apiVersion: v1
kind: Secret
metadata:
  name: payments-key
  namespace: shared
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: payments-reader
  namespace: team-a
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: payments-reader
`
	var secrets []runtime.Object
	for i := range 12 {
		rel := helmTestRelease(fmt.Sprintf("payments-%d", i), "team-a", 1, release.StatusDeployed, "Install complete")
		rel.Manifest = manifest
		secrets = append(secrets, helmReleaseSecret(t, "team-a", rel, false))
	}
	client := fake.NewSimpleClientset(secrets...)

	var mu sync.Mutex
	reviews, gateCalls := map[string]int{}, 0
	devA := func() *ObjectScope {
		budget := new(atomic.Int32)
		budget.Store(readscope.MaxAccessChecks)
		return &ObjectScope{Access: search.ScanAccess{
			Selection: readscope.Selection{Namespaces: []string{"team-a"}},
			TypedKindAccess: func(namespaces []string, _, resource string) ([]string, []string, []string) {
				gateCalls++
				return readscope.ClusterFirstNamespaceAccess(context.Background(), namespaces, budget,
					func(string) (bool, bool) { return false, false },
					func(ns string) (bool, bool) {
						mu.Lock()
						defer mu.Unlock()
						reviews[resource+"/"+ns]++
						return ns == "team-a", true
					})
			},
			CanReadClusterScoped: func(_, _, resource string) (bool, bool) {
				gateCalls++
				reviews[resource]++
				return false, true
			},
		}}
	}
	rows, err := listReleasesAcrossNamespacesWithClient(context.Background(), client, "", []string{"team-a"}, true, devA())
	if err != nil || len(rows) != 12 {
		t.Fatalf("rows = %d, %v", len(rows), err)
	}
	want := map[string]int{"secrets/": 1, "secrets/team-a": 1, "roles/": 1, "roles/team-a": 1, "clusterroles": 1}
	if !maps.Equal(reviews, want) || gateCalls != 3 {
		t.Fatalf("reviews = %v over %d gate calls, want %v over 3", reviews, gateCalls, want)
	}

	for name, scope := range map[string]func() *ObjectScope{"dev-a": devA, "admin": adminCaller} {
		rows, err := listReleasesAcrossNamespacesWithClient(context.Background(), client, "", []string{"team-a"}, true, scope())
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			perObject := parseManifestResources(manifest, "team-a")
			enrichResourcesWithStatus(perObject, scope())
			health, issue, summary := computeResourceHealth(perObject)
			if row.ResourceHealth != health || row.HealthIssue != issue || row.HealthSummary != summary || row.StatusUnreadCount != statusUnreadCount(perObject) {
				t.Fatalf("%s row %s = %q %q %q %d, per object %q %q %q %d", name, row.Name,
					row.ResourceHealth, row.HealthIssue, row.HealthSummary, row.StatusUnreadCount, health, issue, summary, statusUnreadCount(perObject))
			}
		}
	}
}
