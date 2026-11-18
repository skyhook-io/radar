package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/internal/auth"
	integration "github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
	velerosvc "github.com/skyhook-io/radar/internal/velero"
	veleropkg "github.com/skyhook-io/radar/pkg/velero"
)

var veleroWorkspaceTestKinds = []k8s.APIResource{
	cnpgTestResource(veleroGroup, "Schedule", "schedules", true),
	cnpgTestResource(veleroGroup, "Backup", "backups", true),
	cnpgTestResource(veleroGroup, "BackupStorageLocation", "backupstoragelocations", true),
	cnpgTestResource(veleroGroup, "Restore", "restores", true),
	cnpgTestResource(veleroGroup, "BackupRepository", "backuprepositories", true),
	// Rancher's backup operator ships restores in another group.
	cnpgTestResource("resources.cattle.io", "Restore", "restores", true),
}

func veleroWorkspaceObj(kind, ns, name string, labels map[string]string, spec, status map[string]any, created time.Time) *unstructured.Unstructured {
	meta := map[string]any{"name": name, "namespace": ns, "creationTimestamp": created.UTC().Format(time.RFC3339)}
	if labels != nil {
		l := map[string]any{}
		for k, v := range labels {
			l[k] = v
		}
		meta["labels"] = l
	}
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "velero.io/v1", "kind": kind, "metadata": meta, "spec": spec, "status": status}}
}

// A schedule whose last submission was four hours ago on an hourly cron:
// late, unless a run of it is still in flight.
var lastHourlySubmission = time.Now().Add(-4 * time.Hour).UTC().Truncate(time.Second)

func lateHourlySchedule(ns string) *unstructured.Unstructured {
	return veleroWorkspaceObj("Schedule", ns, "hourly", nil,
		map[string]any{"schedule": "0 * * * *", "template": map[string]any{"storageLocation": "default"}},
		map[string]any{"phase": "Enabled", "lastBackup": lastHourlySubmission.Format(time.RFC3339)},
		time.Now().Add(-48*time.Hour))
}

// hourlyRunName is how Velero names the submission: in the server's local time,
// here UTC, which is what lets the zone be inferred without the Deployment.
var hourlyRunName = "hourly-" + lastHourlySubmission.Format("20060102150405")

func seedNamespaces(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
		if _, err := testFakeClient.CoreV1().Namespaces().Create(context.Background(), ns, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create namespace %s: %v", name, err)
		}
		t.Cleanup(func() {
			_ = testFakeClient.CoreV1().Namespaces().Delete(context.Background(), name, metav1.DeleteOptions{})
		})
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		seen := 0
		for _, name := range names {
			if _, err := k8s.GetResourceCache().Namespaces().Get(name); err == nil {
				seen++
			}
		}
		if seen == len(names) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("namespaces never reached the cache")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func veleroWorkspaceFixture(ns string) []runtime.Object {
	return []runtime.Object{
		lateHourlySchedule(ns),
		veleroWorkspaceObj("BackupStorageLocation", ns, "default", nil,
			map[string]any{"provider": "aws", "default": true, "objectStorage": map[string]any{"bucket": "b"}},
			map[string]any{"phase": "Available", "lastValidationTime": time.Now().UTC().Format(time.RFC3339)}, time.Now().Add(-48*time.Hour)),
		veleroWorkspaceObj("Backup", ns, hourlyRunName, map[string]string{veleropkg.ScheduleNameLabel: "hourly"},
			map[string]any{"storageLocation": "default"},
			map[string]any{"phase": "Completed"}, lastHourlySubmission.Add(time.Second)),
		veleroWorkspaceObj("Backup", ns, "adhoc-broken", nil,
			map[string]any{"storageLocation": "default"},
			map[string]any{"phase": "Failed", "failureReason": "boom"}, time.Now().Add(-time.Hour)),
	}
}

func getVeleroWorkspace(t *testing.T, resp *http.Response) velerosvc.WorkspaceResponse {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out velerosvc.WorkspaceResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func warmVeleroBackups(t *testing.T) {
	t.Helper()
	if _, err := listDynamicSynced(context.Background(), k8s.GetResourceCache(), "Backup", veleroGroup, ""); err != nil {
		t.Fatalf("warm backups: %v", err)
	}
}

func scheduleNamed(got velerosvc.WorkspaceResponse, ns, name string) *veleropkg.ScheduleReading {
	for i := range got.Schedules {
		if got.Schedules[i].Namespace == ns && got.Schedules[i].Name == name {
			return &got.Schedules[i]
		}
	}
	return nil
}

func hasVeleroIssue(got velerosvc.WorkspaceResponse, kind, name string) bool {
	for _, iss := range got.Issues {
		if iss.Kind == kind && iss.Name == name {
			return true
		}
	}
	return false
}

func TestVeleroWorkspace_NotInstalled(t *testing.T) {
	seedCNPGWorkspace(t, cnpgWorkspaceTestKinds)
	resp, err := http.Get(testServer.URL + "/api/velero/workspace")
	if err != nil {
		t.Fatal(err)
	}
	got := getVeleroWorkspace(t, resp)
	if got.Installed {
		t.Fatal("installed = true on a cluster serving no Schedule kind")
	}
	if got.Coverage["schedules"].State != integration.KindCoverageNotInstalled || len(got.Schedules) != 0 {
		t.Fatalf("coverage %+v schedules %+v", got.Coverage, got.Schedules)
	}
}

func TestVeleroWorkspace_AuthDisabledReadsEverything(t *testing.T) {
	objs := append(veleroWorkspaceFixture("velero"),
		&unstructured.Unstructured{Object: map[string]any{"apiVersion": "resources.cattle.io/v1", "kind": "Restore", "metadata": map[string]any{"name": "rancher-restore", "namespace": "velero"}, "status": map[string]any{"phase": "Failed"}}},
	)
	seedCNPGWorkspace(t, veleroWorkspaceTestKinds, objs...)
	warmVeleroBackups(t)

	resp, err := http.Get(testServer.URL + "/api/velero/workspace")
	if err != nil {
		t.Fatal(err)
	}
	got := getVeleroWorkspace(t, resp)
	if !got.Installed || got.ObservedAt.IsZero() {
		t.Fatalf("installed=%v observedAt=%v", got.Installed, got.ObservedAt)
	}
	s := scheduleNamed(got, "velero", "hourly")
	if s == nil {
		t.Fatalf("schedule missing: %+v", got.Schedules)
	}
	if s.Runs.Read != veleropkg.RunsReadFull || s.Runs.NewestCompleted == nil || s.Runs.NewestCompleted.Name != hourlyRunName {
		t.Errorf("runs %+v", s.Runs)
	}
	// No Deployment is readable here, so the zone is inferred from the run's
	// name: probably late, never a definite late.
	if !s.Attention || s.State != veleropkg.StateProbablyLate || s.Zone.Source != veleropkg.ZoneInferred {
		t.Errorf("an hourly schedule four hours past its last submission is probably late, got %s zone=%+v attention=%v", s.State, s.Zone, s.Attention)
	}
	if len(got.Locations) != 1 || got.Locations[0].Backups == nil || got.Locations[0].Backups.Targeting != 2 {
		t.Errorf("locations %+v", got.Locations)
	}
	if len(got.Installations) != 1 || got.Installations[0].Namespace != "velero" || got.Installations[0].DefaultLocation != "default" {
		t.Errorf("installations %+v", got.Installations)
	}
	if !hasVeleroIssue(got, "Backup", "adhoc-broken") {
		t.Errorf("the failed ad-hoc backup raised no issue: %+v", got.Issues)
	}
	if hasVeleroIssue(got, "Restore", "rancher-restore") {
		t.Error("a Rancher Restore leaked into the Velero workspace")
	}
}

// Without the Backups read, runs are unknown and a missing submission cannot be
// called late: a hidden run could be holding it. The backup's own issue is
// withheld.
func TestVeleroWorkspace_DeniedBackupsLeaveRunsUnknownAndWithholdTheirIssues(t *testing.T) {
	seedCNPGWorkspace(t, veleroWorkspaceTestKinds, veleroWorkspaceFixture("velero")...)
	warmVeleroBackups(t)
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"velero"}}
	allow(perms, veleroGroup, "schedules", "", true)
	allow(perms, veleroGroup, "backupstoragelocations", "", true)
	allow(perms, veleroGroup, "backups", "", false)
	allow(perms, veleroGroup, "backups", "velero", false)
	env.srv.permCache.Set("no-backups", nil, perms)

	got := getVeleroWorkspace(t, env.authGet(t, "/api/velero/workspace", "no-backups", ""))
	if got.Coverage["backups"].State != integration.KindCoverageDenied {
		t.Errorf("backups coverage %+v, want denied", got.Coverage["backups"])
	}
	s := scheduleNamed(got, "velero", "hourly")
	if s == nil || s.Runs.Read != veleropkg.RunsReadUnknown || s.Runs.Newest != nil {
		t.Fatalf("runs must be unknown without the Backups read: %+v", s)
	}
	if s.State == veleropkg.StateLate {
		t.Errorf("state late without the Backups read; want lateOrHeld (or probablyLate)")
	}
	if hasVeleroIssue(got, "Backup", "adhoc-broken") {
		t.Error("an issue on a Backup the caller cannot list was returned")
	}
	if got.Locations[0].Backups != nil || got.Installations[0].UnassignedBackups != nil {
		t.Errorf("backup counts must stay unknown: %+v %+v", got.Locations[0].Backups, got.Installations[0].UnassignedBackups)
	}
}

// Read in one installation but not another: the readable one keeps exact
// facts, and the denied namespace is never named, because the caller did not
// name it.
func TestVeleroWorkspace_PartialInstallationsDiscloseNoNames(t *testing.T) {
	objs := append(veleroWorkspaceFixture("velero"), veleroWorkspaceFixture("kommander")...)
	seedCNPGWorkspace(t, veleroWorkspaceTestKinds, objs...)
	seedNamespaces(t, "velero", "kommander")
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{}}
	for _, resource := range []string{"schedules", "backups", "backupstoragelocations", "restores", "backuprepositories"} {
		allow(perms, veleroGroup, resource, "", false)
		for _, ns := range allNamespaceNames() {
			allow(perms, veleroGroup, resource, ns, ns == "velero")
		}
		allow(perms, veleroGroup, resource, "velero", true)
		allow(perms, veleroGroup, resource, "kommander", false)
	}
	env.srv.permCache.Set("velero-only", nil, perms)

	got := getVeleroWorkspace(t, env.authGet(t, "/api/velero/workspace", "velero-only", ""))
	cov := got.Coverage["schedules"]
	if cov.State != integration.KindCoveragePartial || len(cov.DeniedNamespaces) != 0 {
		t.Errorf("schedules coverage %+v, want partial naming nothing denied", cov)
	}
	if scheduleNamed(got, "kommander", "hourly") != nil {
		t.Error("a schedule from an unreadable namespace was returned")
	}
	s := scheduleNamed(got, "velero", "hourly")
	if s == nil || s.Runs.Read != veleropkg.RunsReadFull {
		t.Fatalf("the readable installation lost its exact runs: %+v", s)
	}
	for _, inst := range got.Installations {
		if inst.Namespace == "kommander" {
			t.Error("an unreadable installation was reported")
		}
	}
	// No Pod or Deployment access anywhere: the failure on a Backup the caller
	// read is still raised, and the unreadable installation's is not.
	if !hasVeleroIssue(got, "Backup", "adhoc-broken") {
		t.Errorf("a Velero-only reader lost the failed backup's issue: %+v", got.Issues)
	}
	for _, iss := range got.Issues {
		if iss.Namespace == "kommander" {
			t.Errorf("an issue from an unreadable namespace was returned: %+v", iss)
		}
	}
	if got.IssueTotals["kommander"] != 0 {
		t.Errorf("totals %+v count an unreadable namespace", got.IssueTotals)
	}
}

// --namespace-scope bounds the request even though the picker does not.
func TestVeleroWorkspace_ForcedNamespaceScopeClamps(t *testing.T) {
	objs := append(veleroWorkspaceFixture("velero"), veleroWorkspaceFixture("kommander")...)
	seedCNPGWorkspace(t, veleroWorkspaceTestKinds, objs...)
	k8s.ForceNamespaceScope = true
	k8s.SetFallbackNamespace("kommander")
	t.Cleanup(func() {
		k8s.ForceNamespaceScope = false
		k8s.SetFallbackNamespace("")
	})

	resp, err := http.Get(testServer.URL + "/api/velero/workspace")
	if err != nil {
		t.Fatal(err)
	}
	got := getVeleroWorkspace(t, resp)
	if got.ForcedNamespace != "kommander" {
		t.Errorf("forcedNamespace = %q", got.ForcedNamespace)
	}
	if scheduleNamed(got, "velero", "hourly") != nil || scheduleNamed(got, "kommander", "hourly") == nil {
		t.Errorf("schedules %+v, want only kommander's", got.Schedules)
	}

	resp, err = http.Get(testServer.URL + "/api/velero/schedules/velero/hourly/activity")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("activity outside the forced scope: status %d, want 403", resp.StatusCode)
	}
}

func TestVeleroScheduleActivity(t *testing.T) {
	seedCNPGWorkspace(t, veleroWorkspaceTestKinds, veleroWorkspaceFixture("velero")...)
	env := newAuthTestServer(t)
	reader := &auth.UserPermissions{AllowedNamespaces: []string{"velero"}}
	for _, resource := range []string{"schedules", "backups", "backupstoragelocations", "restores", "backuprepositories"} {
		allow(reader, veleroGroup, resource, "velero", true)
	}
	env.srv.permCache.Set("reader", nil, reader)
	denied := &auth.UserPermissions{AllowedNamespaces: []string{"velero"}}
	allow(denied, veleroGroup, "schedules", "velero", false)
	env.srv.permCache.Set("denied", nil, denied)

	resp := env.authGet(t, "/api/velero/schedules/velero/hourly/activity", "reader", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var got velerosvc.ScheduleActivityResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Reading.Name != "hourly" || len(got.Reading.Runs.Recent) != 1 || got.Reading.Runs.Recent[0].Name != hourlyRunName {
		t.Errorf("reading %+v", got.Reading)
	}

	for _, c := range []struct {
		user, path string
		want       int
	}{
		{"denied", "/api/velero/schedules/velero/hourly/activity", http.StatusForbidden},
		{"reader", "/api/velero/schedules/velero/missing/activity", http.StatusNotFound},
	} {
		r := env.authGet(t, c.path, c.user, "")
		r.Body.Close()
		if r.StatusCode != c.want {
			t.Errorf("%s %s: status %d, want %d", c.user, c.path, r.StatusCode, c.want)
		}
	}
}

// The server's zone and flags come from its Deployment only for a caller who
// may read Deployments there.
func TestVeleroWorkspace_ServerConfigFollowsDeploymentAccess(t *testing.T) {
	seedCNPGWorkspace(t, veleroWorkspaceTestKinds, veleroWorkspaceFixture("velero")...)
	seedNamespaces(t, "velero")
	one := int32(1)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "velero", Namespace: "velero"},
		Spec: appsv1.DeploymentSpec{Replicas: &one, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "velero", Image: "velero/velero:v1.18.1", Command: []string{"/velero"}, Args: []string{"server"},
			Env: []corev1.EnvVar{{Name: "TZ", Value: "Europe/Berlin"}},
		}}}}},
		Status: appsv1.DeploymentStatus{Replicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1},
	}
	if _, err := testFakeClient.AppsV1().Deployments("velero").Create(context.Background(), dep, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	t.Cleanup(func() {
		_ = testFakeClient.AppsV1().Deployments("velero").Delete(context.Background(), "velero", metav1.DeleteOptions{})
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := k8s.GetResourceCache().Deployments().Deployments("velero").Get("velero"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("deployment never reached the cache")
		}
		time.Sleep(20 * time.Millisecond)
	}

	env := newAuthTestServer(t)
	for _, u := range []struct {
		name        string
		deployments bool
	}{{"with-deployments", true}, {"velero-only", false}} {
		perms := &auth.UserPermissions{AllowedNamespaces: []string{"velero"}}
		for _, resource := range []string{"schedules", "backups", "backupstoragelocations", "restores", "backuprepositories"} {
			allow(perms, veleroGroup, resource, "velero", true)
		}
		allow(perms, "apps", "deployments", "velero", u.deployments)
		env.srv.permCache.Set(u.name, nil, perms)
	}

	with := getVeleroWorkspace(t, env.authGet(t, "/api/velero/workspace", "with-deployments", ""))
	if with.Installations[0].Server.Source != veleropkg.ServerFromDeployment || with.Installations[0].Server.TimeZone != "Europe/Berlin" {
		t.Errorf("server %+v", with.Installations[0].Server)
	}
	if s := scheduleNamed(with, "velero", "hourly"); s == nil || s.Zone.Source != veleropkg.ZoneFromServer {
		t.Errorf("schedule zone %+v", s)
	}
	without := getVeleroWorkspace(t, env.authGet(t, "/api/velero/workspace", "velero-only", ""))
	if without.Installations[0].Server.Source != veleropkg.ServerUnread || without.Installations[0].Server.TimeZone != "" {
		t.Errorf("server %+v, want unread", without.Installations[0].Server)
	}
}

// A caller without cluster-wide list access is checked only where Velero
// objects are, so the cost does not grow with the cluster's namespaces, and
// the views work when Radar cannot list Namespaces at all.
func TestVeleroWorkspace_RestrictedCallerCheckedOnlyWhereVeleroIs(t *testing.T) {
	seedCNPGWorkspace(t, veleroWorkspaceTestKinds, veleroWorkspaceFixture("velero")...)
	others := make([]string, 30)
	for i := range others {
		others[i] = fmt.Sprintf("app-%02d", i)
	}
	seedNamespaces(t, append([]string{"velero"}, others...)...)
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{}}
	for _, resource := range []string{"schedules", "backups", "backupstoragelocations", "restores", "backuprepositories"} {
		allow(perms, veleroGroup, resource, "", false)
		allow(perms, veleroGroup, resource, "velero", true)
		allow(perms, veleroGroup, resource, "app-00", true)
	}
	env.srv.permCache.Set("velero-reader", nil, perms)

	got := getVeleroWorkspace(t, env.authGet(t, "/api/velero/workspace", "velero-reader", ""))
	if scheduleNamed(got, "velero", "hourly") == nil {
		t.Fatalf("the readable installation is missing: %+v", got.Schedules)
	}
	// The caller may list app-00 too, but it holds no Velero objects, so it is
	// never checked or read: checking every namespace would cost one access
	// review per namespace per kind.
	for _, ns := range got.Coverage["schedules"].AllowedNamespaces {
		if ns != "velero" {
			t.Fatalf("read schedules in %s, which holds no Velero objects: %+v", ns, got.Coverage["schedules"])
		}
	}
}

func TestVeleroWorkspace_RestrictedCallerWithoutNamespaceListing(t *testing.T) {
	seedCNPGWorkspace(t, veleroWorkspaceTestKinds, veleroWorkspaceFixture("velero")...)
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{}}
	for _, resource := range []string{"schedules", "backups", "backupstoragelocations", "restores", "backuprepositories"} {
		allow(perms, veleroGroup, resource, "", false)
		allow(perms, veleroGroup, resource, "velero", true)
	}
	env.srv.permCache.Set("velero-reader", nil, perms)

	got := getVeleroWorkspace(t, env.authGet(t, "/api/velero/workspace", "velero-reader", ""))
	if scheduleNamed(got, "velero", "hourly") == nil {
		t.Fatalf("no namespaces in Radar's cache emptied the views: %+v", got.Coverage)
	}
	// Every namespace holding Velero was readable, and the answer still does
	// not say so: "full" here would tell the caller there is nothing else.
	if cov := got.Coverage["schedules"]; cov.State != integration.KindCoveragePartial || len(cov.DeniedNamespaces) != 0 {
		t.Errorf("schedules coverage %+v, want partial naming nothing", cov)
	}
}

// An installation before its first backup (or after every backup expired)
// holds no Backups, and a restricted reader's Backups read there is still
// made: no runs reads "none kept", not "unknown".
func TestVeleroWorkspace_RestrictedCallerBeforeTheFirstBackup(t *testing.T) {
	seedCNPGWorkspace(t, veleroWorkspaceTestKinds, lateHourlySchedule("velero"))
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{}}
	for _, resource := range []string{"schedules", "backups", "backupstoragelocations", "restores", "backuprepositories"} {
		allow(perms, veleroGroup, resource, "", false)
		allow(perms, veleroGroup, resource, "velero", true)
	}
	env.srv.permCache.Set("velero-reader", nil, perms)

	got := getVeleroWorkspace(t, env.authGet(t, "/api/velero/workspace", "velero-reader", ""))
	if cov := got.Coverage["backups"]; cov.State == integration.KindCoverageDenied {
		t.Fatalf("backups coverage %+v: an empty installation read as denied", cov)
	}
	s := scheduleNamed(got, "velero", "hourly")
	if s == nil || s.Runs.Read != veleropkg.RunsReadFull || s.Runs.Kept != 0 {
		t.Fatalf("runs %+v, want read and none kept", s)
	}
}

// A reader of Velero's kinds alone, with no Pod or Deployment access, gets the
// runs through the same per-kind access as the views; what they cannot read is
// reported as partial coverage, never as an exact list.
func TestVeleroRuns_VeleroOnlyReader(t *testing.T) {
	objs := append(veleroWorkspaceFixture("velero"), veleroWorkspaceFixture("kommander")...)
	seedCNPGWorkspace(t, veleroWorkspaceTestKinds, objs...)
	seedNamespaces(t, "velero", "kommander")
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{}}
	for _, resource := range []string{"schedules", "backups", "backupstoragelocations", "restores", "backuprepositories"} {
		allow(perms, veleroGroup, resource, "", false)
		for _, ns := range allNamespaceNames() {
			allow(perms, veleroGroup, resource, ns, ns == "velero")
		}
	}
	env.srv.permCache.Set("velero-only", nil, perms)

	resp := env.authGet(t, "/api/velero/runs/backups", "velero-only", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var got velerosvc.RunsResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Coverage.State != integration.KindCoveragePartial {
		t.Errorf("coverage %+v, want partial", got.Coverage)
	}
	if len(got.Runs) != 2 {
		t.Fatalf("runs %+v, want the two in velero", got.Runs)
	}
	for _, r := range got.Runs {
		if r.Namespace != "velero" {
			t.Errorf("a run from an unreadable namespace: %+v", r)
		}
	}

	for _, path := range []string{"/api/velero/runs/backups?schedule=absent", "/api/velero/runs/restores?backup=absent"} {
		resp := env.authGet(t, path, "velero-only", "")
		var filtered velerosvc.RunsResponse
		err := json.NewDecoder(resp.Body).Decode(&filtered)
		resp.Body.Close()
		if err != nil || resp.StatusCode != 200 || filtered.Total != 0 || len(filtered.Runs) != 0 {
			t.Fatalf("filter %s: %+v %v", path, filtered, err)
		}
	}

	bad := env.authGet(t, "/api/velero/runs/schedules", "velero-only", "")
	defer bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Errorf("schedules: status %d, want 400", bad.StatusCode)
	}
}
