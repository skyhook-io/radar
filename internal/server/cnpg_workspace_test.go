package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
)

func cnpgTestResource(group, kind, resource string, namespaced bool) k8s.APIResource {
	return k8s.APIResource{Group: group, Version: "v1", Kind: kind, Name: resource, Namespaced: namespaced, IsCRD: true, Verbs: []string{"get", "list", "watch"}}
}

var cnpgWorkspaceTestKinds = func() []k8s.APIResource {
	var out []k8s.APIResource
	for _, k := range cnpgWorkspaceKinds {
		out = append(out, cnpgTestResource(k.group, k.kind, k.resource, !k.clusterScoped))
	}
	out = append(out,
		cnpgTestResource(veleroGroup, "Backup", "backups", true),
		k8s.APIResource{Group: "cluster.x-k8s.io", Version: "v1beta1", Kind: "Cluster", Name: "clusters", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
	)
	return out
}()

func seedCNPGWorkspace(t *testing.T, kinds []k8s.APIResource, objs ...runtime.Object) {
	t.Helper()
	listKinds := map[schema.GroupVersionResource]string{}
	for _, k := range kinds {
		listKinds[schema.GroupVersionResource{Group: k.Group, Version: k.Version, Resource: k.Name}] = k.Kind + "List"
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, objs...)
	if err := k8s.InitTestDynamicResourceCache(dyn, kinds); err != nil {
		t.Fatalf("seed cnpg: %v", err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
}

func cnpgObj(apiVersion, kind, ns, name string, spec, status map[string]any) *unstructured.Unstructured {
	meta := map[string]any{"name": name, "creationTimestamp": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}
	if ns != "" {
		meta["namespace"] = ns
	}
	obj := map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": meta}
	if spec != nil {
		obj["spec"] = spec
	}
	if status != nil {
		obj["status"] = status
	}
	return &unstructured.Unstructured{Object: obj}
}

func cnpgBackup(ns, name, cluster, phase string, stoppedAt time.Time) *unstructured.Unstructured {
	status := map[string]any{"phase": phase}
	if !stoppedAt.IsZero() {
		status["stoppedAt"] = stoppedAt.UTC().Format(time.RFC3339)
	}
	return cnpgObj("postgresql.cnpg.io/v1", "Backup", ns, name, map[string]any{"cluster": map[string]any{"name": cluster}}, status)
}

func decodeWorkspace(t *testing.T, resp *http.Response) CNPGWorkspaceResponse {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out CNPGWorkspaceResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func getWorkspaceNoAuth(t *testing.T, query string) CNPGWorkspaceResponse {
	t.Helper()
	resp, err := http.Get(testServer.URL + "/api/cnpg/workspace" + query)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	return decodeWorkspace(t, resp)
}

func objectNames(objs []any) []string {
	var out []string
	for _, o := range objs {
		m, _ := o.(map[string]any)
		meta, _ := m["metadata"].(map[string]any)
		name, _ := meta["name"].(string)
		out = append(out, name)
	}
	return out
}

func containsName(objs []any, name string) bool {
	for _, n := range objectNames(objs) {
		if n == name {
			return true
		}
	}
	return false
}

func assertEveryKey(t *testing.T, got CNPGWorkspaceResponse) {
	t.Helper()
	keys := []string{cnpgWorkspacePodsKey}
	for _, k := range cnpgWorkspaceKinds {
		keys = append(keys, k.key)
	}
	for _, k := range keys {
		if _, ok := got.Coverage[k]; !ok {
			t.Errorf("coverage missing key %q", k)
		}
		if objs, ok := got.Objects[k]; !ok || objs == nil {
			t.Errorf("objects missing key %q (or null)", k)
		}
	}
}

func TestCNPGWorkspace_NotInstalled(t *testing.T) {
	seedCNPGWorkspace(t, []k8s.APIResource{cnpgTestResource(veleroGroup, "Backup", "backups", true)})
	got := getWorkspaceNoAuth(t, "")
	if got.Installed {
		t.Error("installed = true on a cluster without CloudNativePG")
	}
	assertEveryKey(t, got)
	for k, c := range got.Coverage {
		if c.State != cnpgCoverageNotInstalled {
			t.Errorf("coverage[%s] = %q, want notInstalled", k, c.State)
		}
	}
}

func seedCNPGPods(t *testing.T, pods ...*corev1.Pod) {
	t.Helper()
	ctx := context.Background()
	for _, p := range pods {
		if _, err := testFakeClient.CoreV1().Pods(p.Namespace).Create(ctx, p, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create pod %s: %v", p.Name, err)
		}
		t.Cleanup(func() {
			_ = testFakeClient.CoreV1().Pods(p.Namespace).Delete(context.Background(), p.Name, metav1.DeleteOptions{})
		})
	}
	lister := k8s.GetResourceCache().Pods()
	deadline := time.Now().Add(5 * time.Second)
	for {
		seen := 0
		for _, p := range pods {
			if _, err := lister.Pods(p.Namespace).Get(p.Name); err == nil {
				seen++
			}
		}
		if seen == len(pods) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pods did not reach the cache")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func cnpgPod(ns, name, clusterLabel string, owners ...metav1.OwnerReference) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns,
			Labels:          map[string]string{"cnpg.io/cluster": clusterLabel, "cnpg.io/instanceRole": "primary"},
			OwnerReferences: owners,
		},
		Spec: corev1.PodSpec{NodeName: "node-1", Containers: []corev1.Container{{Name: "postgres", Image: "pg:17"}}},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			PodIP: "10.0.0.5",
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "postgres", Ready: true, RestartCount: 2, Image: "pg:17",
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			}},
		},
	}
}

func TestCNPGWorkspace_AuthDisabledReturnsEverythingAndOnlyOwnedInstancePods(t *testing.T) {
	seedCNPGWorkspace(t, cnpgWorkspaceTestKinds,
		cnpgObj("postgresql.cnpg.io/v1", "Cluster", "pgws", "pg-orders", map[string]any{"instances": int64(1)}, nil),
		cnpgObj("postgresql.cnpg.io/v1", "Pooler", "pgws", "pg-orders-rw", map[string]any{"cluster": map[string]any{"name": "pg-orders"}}, nil),
		cnpgObj("postgresql.cnpg.io/v1", "ClusterImageCatalog", "", "pg-fleet", nil, nil),
		cnpgObj("barmancloud.cnpg.io/v1", "ObjectStore", "pgws", "store", nil, nil),
	)
	owner := metav1.OwnerReference{APIVersion: "postgresql.cnpg.io/v1", Kind: "Cluster", Name: "pg-orders", UID: "c-uid", Controller: boolPtr(true)}
	seedCNPGPods(t,
		cnpgPod("pgws", "pg-orders-1", "pg-orders", owner),
		cnpgPod("pgws", "impostor-1", "pg-orders"),
		cnpgPod("pgws", "capi-owned-1", "pg-orders", metav1.OwnerReference{APIVersion: "cluster.x-k8s.io/v1beta1", Kind: "Cluster", Name: "pg-orders", UID: "x"}),
		cnpgPod("pgws", "other-owner-1", "pg-orders", metav1.OwnerReference{APIVersion: "postgresql.cnpg.io/v1", Kind: "Cluster", Name: "pg-billing", UID: "y"}),
	)

	got := getWorkspaceNoAuth(t, "")
	if !got.Installed {
		t.Fatal("installed = false with CNPG kinds discovered")
	}
	assertEveryKey(t, got)
	for k, c := range got.Coverage {
		if c.State != cnpgCoverageFull {
			t.Errorf("coverage[%s] = %+v, want full with auth disabled", k, c)
		}
	}
	if got.Namespaces != nil {
		t.Errorf("namespaces = %v, want null for an unfiltered view", got.Namespaces)
	}
	for key, name := range map[string]string{"clusters": "pg-orders", "poolers": "pg-orders-rw", "clusterImageCatalogs": "pg-fleet", "objectStores": "store"} {
		if !containsName(got.Objects[key], name) {
			t.Errorf("objects[%s] = %v, want %s", key, objectNames(got.Objects[key]), name)
		}
	}
	pods := objectNames(got.Objects["pods"])
	if len(pods) != 1 || pods[0] != "pg-orders-1" {
		t.Fatalf("pods = %v, want only the CNPG-owned instance pod", pods)
	}
	pod := got.Objects["pods"][0].(map[string]any)
	if _, ok := pod["spec"].(map[string]any)["containers"]; ok {
		t.Error("pod spec was not trimmed")
	}
	cs := pod["status"].(map[string]any)["containerStatuses"].([]any)[0].(map[string]any)
	if cs["restartCount"].(float64) != 2 || cs["ready"] != true {
		t.Errorf("container status = %v", cs)
	}
	if _, ok := cs["image"]; ok {
		t.Error("container status carried fields beyond the trimmed set")
	}
}

func TestCNPGWorkspace_CollidingKindsNeverLeak(t *testing.T) {
	seedCNPGWorkspace(t, cnpgWorkspaceTestKinds,
		cnpgObj("postgresql.cnpg.io/v1", "Cluster", "pgws", "pg-orders", nil, nil),
		cnpgBackup("pgws", "pg-orders-b1", "pg-orders", "running", time.Time{}),
		cnpgObj("velero.io/v1", "Backup", "pgws", "velero-nightly", nil, map[string]any{"phase": "Completed"}),
		cnpgObj("cluster.x-k8s.io/v1beta1", "Cluster", "pgws", "capi-workload", nil, nil),
	)
	got := getWorkspaceNoAuth(t, "")
	if containsName(got.Objects["backups"], "velero-nightly") {
		t.Error("a Velero Backup was returned as a CNPG Backup")
	}
	if containsName(got.Objects["clusters"], "capi-workload") {
		t.Error("a CAPI Cluster was returned as a CNPG Cluster")
	}
	if !containsName(got.Objects["backups"], "pg-orders-b1") || !containsName(got.Objects["clusters"], "pg-orders") {
		t.Errorf("CNPG objects missing: backups=%v clusters=%v", objectNames(got.Objects["backups"]), objectNames(got.Objects["clusters"]))
	}
}

func TestCNPGWorkspace_DeniedKindAndItsIssuesAreWithheld(t *testing.T) {
	seedCNPGWorkspace(t, cnpgWorkspaceTestKinds,
		cnpgObj("postgresql.cnpg.io/v1", "Cluster", "pg", "pg-orders", nil, nil),
		cnpgBackup("pg", "pg-orders-broken", "pg-orders", "failed", time.Now().Add(-time.Hour)),
	)
	// Warm the Backup informer so the issues engine can see the failed Backup
	// whichever caller asks; otherwise a denied answer would pass vacuously.
	if _, err := listDynamicSynced(context.Background(), k8s.GetResourceCache(), "Backup", cnpgGroup, ""); err != nil {
		t.Fatalf("warm backups: %v", err)
	}

	env := newAuthTestServer(t)
	for _, u := range []struct {
		name          string
		backupsListed bool
	}{{"reader", true}, {"no-backups", false}} {
		perms := &auth.UserPermissions{AllowedNamespaces: []string{"pg"}}
		allow(perms, cnpgGroup, "clusters", "", true)
		allow(perms, cnpgGroup, "backups", "", u.backupsListed)
		allow(perms, cnpgGroup, "backups", "pg", u.backupsListed)
		env.srv.permCache.Set(u.name, nil, perms)
	}

	hasBackupIssue := func(got CNPGWorkspaceResponse) bool {
		for _, iss := range got.Issues {
			if iss.Kind == "Backup" && iss.Name == "pg-orders-broken" {
				return true
			}
		}
		return false
	}

	control := decodeWorkspace(t, env.authGet(t, "/api/cnpg/workspace", "reader", ""))
	if control.Coverage["backups"].State != cnpgCoverageFull || !containsName(control.Objects["backups"], "pg-orders-broken") {
		t.Fatalf("control: backups coverage=%+v objects=%v", control.Coverage["backups"], objectNames(control.Objects["backups"]))
	}
	if !hasBackupIssue(control) {
		t.Fatalf("control: the failed Backup raised no issue, so the denied case would prove nothing: %+v", control.Issues)
	}

	got := decodeWorkspace(t, env.authGet(t, "/api/cnpg/workspace", "no-backups", ""))
	if got.Coverage["backups"].State != cnpgCoverageDenied {
		t.Errorf("backups coverage = %+v, want denied", got.Coverage["backups"])
	}
	if len(got.Objects["backups"]) != 0 {
		t.Errorf("backups = %v, want [] when denied", objectNames(got.Objects["backups"]))
	}
	if hasBackupIssue(got) {
		t.Error("an issue on a Backup the caller cannot list was returned")
	}
	if got.Coverage["clusters"].State != cnpgCoverageFull || !containsName(got.Objects["clusters"], "pg-orders") {
		t.Errorf("clusters coverage=%+v objects=%v, want full", got.Coverage["clusters"], objectNames(got.Objects["clusters"]))
	}
}

func TestCNPGWorkspace_PartialNamespaceCoverage(t *testing.T) {
	seedCNPGWorkspace(t, cnpgWorkspaceTestKinds,
		cnpgObj("postgresql.cnpg.io/v1", "Cluster", "a", "pg-a", nil, nil),
		cnpgObj("postgresql.cnpg.io/v1", "Cluster", "b", "pg-b", nil, nil),
		cnpgObj("postgresql.cnpg.io/v1", "Cluster", "c", "pg-c", nil, nil),
	)
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"a", "b"}}
	allow(perms, cnpgGroup, "clusters", "", false)
	allow(perms, cnpgGroup, "clusters", "a", true)
	allow(perms, cnpgGroup, "clusters", "b", false)
	env.srv.permCache.Set("scoped", nil, perms)

	got := decodeWorkspace(t, env.authGet(t, "/api/cnpg/workspace", "scoped", ""))
	cov := got.Coverage["clusters"]
	if cov.State != cnpgCoveragePartial || len(cov.DeniedNamespaces) != 1 || cov.DeniedNamespaces[0] != "b" {
		t.Errorf("clusters coverage = %+v, want partial denied [b]", cov)
	}
	names := objectNames(got.Objects["clusters"])
	if len(names) != 1 || names[0] != "pg-a" {
		t.Errorf("clusters = %v, want only pg-a", names)
	}

	// A view filter narrows the scope; the denied list never grows past it.
	filtered := decodeWorkspace(t, env.authGet(t, "/api/cnpg/workspace?namespaces=a", "scoped", ""))
	if filtered.Coverage["clusters"].State != cnpgCoverageFull {
		t.Errorf("filtered to a: coverage = %+v, want full", filtered.Coverage["clusters"])
	}
	if len(filtered.Namespaces) != 1 || filtered.Namespaces[0] != "a" {
		t.Errorf("namespaces = %v, want [a]", filtered.Namespaces)
	}
}

func TestCNPGWorkspace_ClusterImageCatalogNeedsClusterScopeGrant(t *testing.T) {
	seedCNPGWorkspace(t, cnpgWorkspaceTestKinds,
		cnpgObj("postgresql.cnpg.io/v1", "ClusterImageCatalog", "", "pg-fleet", nil, nil),
	)
	env := newAuthTestServer(t)
	nsOnly := &auth.UserPermissions{AllowedNamespaces: []string{"pg"}}
	nsOnly.SetCanI("list", cnpgGroup, "clusterimagecatalogs", "pg", true)
	allow(nsOnly, cnpgGroup, "clusterimagecatalogs", "", false)
	env.srv.permCache.Set("ns-only", nil, nsOnly)

	clusterWide := &auth.UserPermissions{AllowedNamespaces: []string{"pg"}}
	allow(clusterWide, cnpgGroup, "clusterimagecatalogs", "", true)
	env.srv.permCache.Set("cluster-wide", nil, clusterWide)

	got := decodeWorkspace(t, env.authGet(t, "/api/cnpg/workspace", "ns-only", ""))
	if got.Coverage["clusterImageCatalogs"].State != cnpgCoverageDenied || len(got.Objects["clusterImageCatalogs"]) != 0 {
		t.Errorf("namespace-level grant exposed ClusterImageCatalogs: %+v %v", got.Coverage["clusterImageCatalogs"], objectNames(got.Objects["clusterImageCatalogs"]))
	}

	got = decodeWorkspace(t, env.authGet(t, "/api/cnpg/workspace?namespaces=pg", "cluster-wide", ""))
	if got.Coverage["clusterImageCatalogs"].State != cnpgCoverageFull || !containsName(got.Objects["clusterImageCatalogs"], "pg-fleet") {
		t.Errorf("cluster-scope grant: %+v %v, want full with pg-fleet regardless of the view filter", got.Coverage["clusterImageCatalogs"], objectNames(got.Objects["clusterImageCatalogs"]))
	}
}

func TestWindowCNPGBackups(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	items := []*unstructured.Unstructured{
		cnpgBackup("pg", "running-old", "orders", "running", time.Time{}),
		cnpgBackup("pg", "orders-recent", "orders", "completed", now.Add(-2*day)),
		cnpgBackup("pg", "orders-old", "orders", "completed", now.Add(-20*day)),
		cnpgBackup("pg", "orders-failed-old", "orders", "failed", now.Add(-30*day)),
		cnpgBackup("pg", "orders-failed-new", "orders", "failed", now.Add(-1*day)),
		cnpgBackup("pg", "billing-only-old", "billing", "completed", now.Add(-40*day)),
		cnpgBackup("pg", "billing-older", "billing", "completed", now.Add(-50*day)),
		cnpgBackup("aa", "other-ns", "orders", "completed", now.Add(-60*day)),
	}
	// A running Backup older than the window stays: in flight is never settled.
	items[0].Object["metadata"].(map[string]any)["creationTimestamp"] = now.Add(-90 * day).Format(time.RFC3339)

	kept, omitted := windowCNPGBackups(items, now)
	var names []string
	for _, u := range kept {
		names = append(names, u.GetName())
	}
	want := []string{"other-ns", "orders-failed-new", "orders-recent", "billing-only-old", "running-old"}
	if len(names) != len(want) {
		t.Fatalf("kept = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("kept = %v, want %v (namespace, then newest first)", names, want)
		}
	}
	if omitted != 3 {
		t.Errorf("omitted = %d, want 3 (orders-old, orders-failed-old, billing-older)", omitted)
	}
}

func TestCNPGWorkspace_BackupsOmittedIsReported(t *testing.T) {
	seedCNPGWorkspace(t, cnpgWorkspaceTestKinds,
		cnpgBackup("pgws", "new", "orders", "completed", time.Now().Add(-time.Hour)),
		cnpgBackup("pgws", "ancient", "orders", "completed", time.Now().Add(-30*24*time.Hour)),
	)
	got := getWorkspaceNoAuth(t, "")
	if got.BackupsOmitted != 1 || containsName(got.Objects["backups"], "ancient") {
		t.Errorf("backupsOmitted=%d backups=%v, want 1 and ancient omitted", got.BackupsOmitted, objectNames(got.Objects["backups"]))
	}
}

func TestCNPGWorkspace_AuditNeedsScheduledBackupEvidence(t *testing.T) {
	seedCNPGWorkspace(t, cnpgWorkspaceTestKinds,
		cnpgObj("postgresql.cnpg.io/v1", "Cluster", "pg", "unscheduled", nil, nil),
		cnpgObj("postgresql.cnpg.io/v1", "Cluster", "pg", "scheduled", nil, nil),
		cnpgObj("postgresql.cnpg.io/v1", "ScheduledBackup", "pg", "nightly", map[string]any{"cluster": map[string]any{"name": "scheduled"}}, nil),
	)
	env := newAuthTestServer(t)
	for _, u := range []struct {
		name  string
		sched bool
	}{{"sees-schedules", true}, {"no-schedules", false}} {
		perms := &auth.UserPermissions{AllowedNamespaces: []string{"pg"}}
		allow(perms, cnpgGroup, "clusters", "", true)
		allow(perms, cnpgGroup, "scheduledbackups", "", u.sched)
		allow(perms, cnpgGroup, "scheduledbackups", "pg", u.sched)
		env.srv.permCache.Set(u.name, nil, perms)
	}

	got := decodeWorkspace(t, env.authGet(t, "/api/cnpg/workspace", "sees-schedules", ""))
	if len(got.Audit) != 1 || got.Audit[0].Name != "unscheduled" || got.Audit[0].CheckID != cnpgNoDeclarativeBackupCheckID {
		t.Errorf("audit = %+v, want one %s finding on unscheduled", got.Audit, cnpgNoDeclarativeBackupCheckID)
	}

	got = decodeWorkspace(t, env.authGet(t, "/api/cnpg/workspace", "no-schedules", ""))
	if got.Coverage["scheduledBackups"].State != cnpgCoverageDenied {
		t.Errorf("scheduledBackups coverage = %+v, want denied", got.Coverage["scheduledBackups"])
	}
	if len(got.Audit) != 0 {
		t.Errorf("audit = %+v, want none — without the ScheduledBackup list the absence is unestablished", got.Audit)
	}
}

func TestIsCNPGInstancePod(t *testing.T) {
	owned := cnpgPod("pg", "x-1", "x", metav1.OwnerReference{APIVersion: "postgresql.cnpg.io/v1", Kind: "Cluster", Name: "x"})
	if !isCNPGInstancePod(owned) {
		t.Error("owned instance pod rejected")
	}
	unlabeled := owned.DeepCopy()
	unlabeled.Labels = nil
	if isCNPGInstancePod(unlabeled) {
		t.Error("pod without the cluster label accepted")
	}
}
