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

	"github.com/skyhook-io/radar/internal/auth"
	integration "github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
	longhornsvc "github.com/skyhook-io/radar/internal/longhorn"
	longhornpkg "github.com/skyhook-io/radar/pkg/longhorn"
)

var longhornWorkspaceTestKinds = []k8s.APIResource{
	{Group: longhornpkg.Group, Version: "v1beta2", Kind: "Volume", Name: "volumes", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
	{Group: longhornpkg.Group, Version: "v1beta2", Kind: "Engine", Name: "engines", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
}

const lhDegradedAt = "2026-11-20T10:00:00Z"

func lhObj(kind, ns, name string, spec, status map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "longhorn.io/v1beta2", "kind": kind,
		"metadata": map[string]any{"name": name, "namespace": ns, "uid": ns + "-" + name, "creationTimestamp": "2026-11-01T00:00:00Z"},
		"spec":     spec, "status": status,
	}}
}

// Volumes in longhorn-system, one of each health, plus a healthy one in a
// second namespace for the partial read.
func longhornWorkspaceFixture() []runtime.Object {
	vol := func(ns, name, state, robustness string, extra map[string]any) *unstructured.Unstructured {
		status := map[string]any{"state": state, "robustness": robustness, "actualSize": int64(1024)}
		for k, v := range extra {
			status[k] = v
		}
		return lhObj("Volume", ns, name, map[string]any{"size": "1073741824", "numberOfReplicas": int64(3)}, status)
	}
	return []runtime.Object{
		vol("longhorn-system", "pvc-a", "attached", "healthy", nil),
		vol("longhorn-system", "pvc-b", "attached", "degraded", map[string]any{"lastDegradedAt": lhDegradedAt}),
		vol("longhorn-system", "pvc-c", "detached", "faulted", nil),
		vol("longhorn-system", "pvc-d", "detached", "unknown", nil),
		vol("other-lh", "pvc-x", "attached", "faulted", nil),
		lhObj("Engine", "longhorn-system", "pvc-a-e-0", map[string]any{"volumeName": "pvc-a"},
			map[string]any{"currentState": "running", "replicaModeMap": map[string]any{"r1": "RW", "r2": "RW", "r3": "WO"}}),
	}
}

func seedLonghornPVs(t *testing.T) {
	t.Helper()
	pv := func(name, handle, claimNS, claim string) *corev1.PersistentVolume {
		return &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: corev1.PersistentVolumeSpec{
				PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: longhornpkg.CSIDriver, VolumeHandle: handle}},
				ClaimRef:               &corev1.ObjectReference{Namespace: claimNS, Name: claim},
			},
			Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound},
		}
	}
	for _, p := range []*corev1.PersistentVolume{pv("pv-a", "pvc-a", "app", "data-a"), pv("pv-b", "pvc-b", "tenant", "data-b")} {
		if _, err := testFakeClient.CoreV1().PersistentVolumes().Create(context.Background(), p, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create pv: %v", err)
		}
		t.Cleanup(func() {
			_ = testFakeClient.CoreV1().PersistentVolumes().Delete(context.Background(), p.Name, metav1.DeleteOptions{})
		})
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, a, _ := k8s.GetResourceCache().ReadableObject("persistentvolumes", "", "pv-a")
		_, b, _ := k8s.GetResourceCache().ReadableObject("persistentvolumes", "", "pv-b")
		if a && b {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("PVs never reached the cache")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func seedLonghornWorkspace(t *testing.T) {
	t.Helper()
	seedSyncedCNPGWorkspace(t, longhornWorkspaceTestKinds, longhornWorkspaceFixture()...)
	seedLonghornPVs(t)
}

func decodeLonghornWorkspace(t *testing.T, resp *http.Response) longhornsvc.WorkspaceResponse {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out longhornsvc.WorkspaceResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func lhVolume(got longhornsvc.WorkspaceResponse, ns, name string) *longhornpkg.VolumeReading {
	for i := range got.Volumes {
		if got.Volumes[i].Namespace == ns && got.Volumes[i].Name == name {
			return &got.Volumes[i]
		}
	}
	return nil
}

// lhUser may list Volumes and Engines cluster-wide unless told otherwise.
func lhUser(env *authTestEnv, name string, volumes, engines, pvs bool, claimNamespaces ...string) {
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"longhorn-system"}}
	allow(perms, longhornpkg.Group, "volumes", "", volumes)
	allow(perms, longhornpkg.Group, "engines", "", engines)
	for _, ns := range []string{"longhorn-system", "other-lh"} {
		allow(perms, longhornpkg.Group, "volumes", ns, volumes)
		allow(perms, longhornpkg.Group, "engines", ns, engines)
	}
	allow(perms, "", "persistentvolumes", "", pvs)
	for _, ns := range []string{"app", "tenant"} {
		allow(perms, "", "persistentvolumeclaims", ns, false)
	}
	for _, ns := range claimNamespaces {
		allow(perms, "", "persistentvolumeclaims", ns, true)
	}
	env.srv.permCache.Set(name, nil, perms)
}

func TestLonghornWorkspace_NotInstalled(t *testing.T) {
	seedCNPGWorkspace(t, cnpgWorkspaceTestKinds)
	resp, err := http.Get(testServer.URL + "/api/longhorn/workspace")
	if err != nil {
		t.Fatal(err)
	}
	got := decodeLonghornWorkspace(t, resp)
	if got.Installed || got.Counts != nil || len(got.Volumes) != 0 {
		t.Fatalf("installed=%v counts=%+v volumes=%+v", got.Installed, got.Counts, got.Volumes)
	}
	for _, key := range []string{"volumes", "engines", longhornsvc.PersistentVolumesKey} {
		if got.Coverage[key].State != integration.KindCoverageNotInstalled {
			t.Errorf("%s coverage %+v, want notInstalled", key, got.Coverage[key])
		}
	}
}

// Degraded and faulted are counted and raised apart: degraded is a warning
// with its onset, faulted is critical.
func TestLonghornWorkspace_AuthDisabledReadsEverything(t *testing.T) {
	seedLonghornWorkspace(t)
	resp, err := http.Get(testServer.URL + "/api/longhorn/workspace")
	if err != nil {
		t.Fatal(err)
	}
	got := decodeLonghornWorkspace(t, resp)
	want := longhornpkg.HealthCounts{Healthy: 1, Degraded: 1, Faulted: 2, Detached: 1, Complete: true}
	if !got.Installed || got.Counts == nil || *got.Counts != want {
		t.Fatalf("counts %+v, want %+v", got.Counts, want)
	}
	a := lhVolume(got, "longhorn-system", "pvc-a")
	if a == nil || a.HealthyReplicas == nil || *a.HealthyReplicas != 2 || *a.NumberOfReplicas != 3 || *a.Size != 1073741824 {
		t.Fatalf("pvc-a %+v", a)
	}
	if a.Use.State != longhornpkg.UseBound || a.Use.PersistentVolume != "pv-a" || a.Use.Claim == nil || *a.Use.Claim != (longhornpkg.ClaimRef{Namespace: "app", Name: "data-a"}) {
		t.Errorf("pvc-a use %+v", a.Use)
	}
	if c := lhVolume(got, "longhorn-system", "pvc-c"); c == nil || c.Use.State != longhornpkg.UseNone || c.HealthyReplicas != nil || c.HealthyReplicasUnknown != longhornpkg.ReplicasNoEngine {
		t.Errorf("pvc-c %+v", c)
	}
	severity := map[string]string{}
	for _, iss := range got.Issues {
		severity[iss.Name] = string(iss.Severity)
		if iss.Name == "pvc-b" && iss.FirstSeen.UTC().Format(time.RFC3339) != lhDegradedAt {
			t.Errorf("degraded onset %v, want %s", iss.FirstSeen, lhDegradedAt)
		}
	}
	if severity["pvc-b"] != "warning" || severity["pvc-c"] != "critical" || severity["pvc-x"] != "critical" || len(got.Issues) != 3 || got.IssueTotal != 3 {
		t.Errorf("issues %+v", got.Issues)
	}
}

func TestLonghornWorkspace_DeniedVolumesReadNothing(t *testing.T) {
	seedLonghornWorkspace(t)
	env := newAuthTestServer(t)
	lhUser(env, "no-volumes", false, true, true, "app", "tenant")
	got := decodeLonghornWorkspace(t, env.authGet(t, "/api/longhorn/workspace", "no-volumes", ""))
	if got.Coverage["volumes"].State != integration.KindCoverageDenied || got.Counts != nil || len(got.Volumes) != 0 || len(got.Issues) != 0 || got.IssueTotal != 0 {
		t.Fatalf("coverage %+v counts %+v volumes %d issues %d", got.Coverage, got.Counts, len(got.Volumes), got.IssueTotal)
	}
	if got.Coverage[longhornsvc.PersistentVolumesKey].State != integration.KindCoverageDenied {
		t.Errorf("PVs coverage %+v: nothing was read for them", got.Coverage[longhornsvc.PersistentVolumesKey])
	}
}

// A caller who may list Volumes in one namespace only: the counts say they
// are partial, and nothing from the other namespace is returned or counted.
func TestLonghornWorkspace_PartialNamespaces(t *testing.T) {
	seedLonghornWorkspace(t)
	env := newAuthTestServer(t)
	lhUser(env, "one-ns", false, false, true)
	perms := env.srv.permCache.Get("one-ns", nil)
	allow(perms, longhornpkg.Group, "volumes", "longhorn-system", true)
	allow(perms, longhornpkg.Group, "engines", "longhorn-system", true)
	got := decodeLonghornWorkspace(t, env.authGet(t, "/api/longhorn/workspace", "one-ns", ""))
	if cov := got.Coverage["volumes"]; cov.State != integration.KindCoveragePartial || len(cov.DeniedNamespaces) != 0 {
		t.Errorf("volumes coverage %+v, want partial naming nothing denied", cov)
	}
	want := longhornpkg.HealthCounts{Healthy: 1, Degraded: 1, Faulted: 1, Detached: 1}
	if got.Counts == nil || *got.Counts != want {
		t.Errorf("counts %+v, want %+v (incomplete)", got.Counts, want)
	}
	if lhVolume(got, "other-lh", "pvc-x") != nil || got.IssueTotal != 2 {
		t.Errorf("an unreadable namespace leaked: volumes %+v issueTotal %d", got.Volumes, got.IssueTotal)
	}
}

func TestLonghornWorkspace_UnreadEnginesLeaveReplicasUnknown(t *testing.T) {
	seedLonghornWorkspace(t)
	env := newAuthTestServer(t)
	lhUser(env, "no-engines", true, false, true, "app", "tenant")
	got := decodeLonghornWorkspace(t, env.authGet(t, "/api/longhorn/workspace", "no-engines", ""))
	a := lhVolume(got, "longhorn-system", "pvc-a")
	if a == nil || a.HealthyReplicas != nil || a.HealthyReplicasUnknown != longhornpkg.ReplicasEngineUnread {
		t.Fatalf("pvc-a %+v: healthy replicas must be unknown, never the requested count", a)
	}
}

func TestLonghornWorkspace_UnreadPVsNameNoUse(t *testing.T) {
	seedLonghornWorkspace(t)
	env := newAuthTestServer(t)
	lhUser(env, "no-pvs", true, true, false, "app", "tenant")
	got := decodeLonghornWorkspace(t, env.authGet(t, "/api/longhorn/workspace", "no-pvs", ""))
	if got.Coverage[longhornsvc.PersistentVolumesKey].State != integration.KindCoverageDenied {
		t.Errorf("PVs coverage %+v", got.Coverage[longhornsvc.PersistentVolumesKey])
	}
	for _, v := range got.Volumes {
		if v.Use.State != longhornpkg.UseUnknown || v.Use.PersistentVolume != "" || v.Use.Claim != nil {
			t.Errorf("%s use %+v without the PV read", v.Name, v.Use)
		}
	}
}

func TestLonghornWorkspace_ClaimWithheldOutsideCallerNamespaces(t *testing.T) {
	seedLonghornWorkspace(t)
	env := newAuthTestServer(t)
	lhUser(env, "app-only", true, true, true, "app")
	got := decodeLonghornWorkspace(t, env.authGet(t, "/api/longhorn/workspace", "app-only", ""))
	if a := lhVolume(got, "longhorn-system", "pvc-a"); a == nil || a.Use.Claim == nil || a.Use.Claim.Name != "data-a" {
		t.Errorf("pvc-a use %+v, want its claim named", a)
	}
	b := lhVolume(got, "longhorn-system", "pvc-b")
	if b == nil || b.Use.PersistentVolume != "pv-b" || b.Use.Claim != nil || !b.Use.ClaimWithheld {
		t.Errorf("pvc-b use %+v, want the PV named and its claim withheld", b)
	}
}
