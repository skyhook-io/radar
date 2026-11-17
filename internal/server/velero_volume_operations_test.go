package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	velerosvc "github.com/skyhook-io/radar/internal/velero"
)

func TestVeleroVolumeOperationsRoute(t *testing.T) {
	for _, restore := range []bool{false, true} {
		t.Run(map[bool]string{false: "backup", true: "restore"}[restore], func(t *testing.T) {
			kind, plural, uidLabel := "Backup", "backups", "velero.io/backup-uid"
			if restore {
				kind, plural, uidLabel = "Restore", "restores", "velero.io/restore-uid"
			}
			run := veleroWorkspaceObj(kind, "velero", "nightly", nil, map[string]any{}, map[string]any{"phase": "InProgress"}, time.Now())
			run.SetUID(types.UID("new-run"))
			kinds := append([]k8s.APIResource{}, veleroWorkspaceTestKinds...)
			objects := []runtime.Object{run}
			for _, k := range velerosvc.VolumeOperationKinds(restore) {
				resource := cnpgTestResource(veleroGroup, k.Kind, k.Resource, true)
				if k.Kind == "DataUpload" || k.Kind == "DataDownload" {
					resource.Version = "v2alpha1"
				}
				kinds = append(kinds, resource)
				for _, uid := range []string{"new-run", "old-run", ""} {
					obj := veleroWorkspaceObj(k.Kind, "velero", "op-"+uid, map[string]string{uidLabel: uid}, map[string]any{}, map[string]any{"phase": "Failed", "message": "copy failed"}, time.Now())
					obj.SetAPIVersion(veleroGroup + "/" + resource.Version)
					objects = append(objects, obj)
				}
			}
			seedCNPGWorkspace(t, kinds, objects...)
			srv := New(Config{DevMode: true})
			t.Cleanup(srv.Stop)
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/velero/"+plural+"/velero/nightly/volume-operations", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			var got velerosvc.VolumeOperationsResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if len(got.Operations) != 2 {
				t.Fatalf("rows: %+v", got)
			}
			for _, row := range got.Operations {
				if row.Name != "op-new-run" || row.Message != "copy failed" {
					t.Fatal(row)
				}
			}
			for _, cov := range got.Coverage {
				if cov.State != "full" {
					t.Fatal(got.Coverage)
				}
			}
		})
	}
}

func TestVeleroVolumeOperationsCoverageAndCaller(t *testing.T) {
	kinds := append([]k8s.APIResource{}, veleroWorkspaceTestKinds...)
	kinds = append(kinds, cnpgTestResource(veleroGroup, "PodVolumeBackup", "podvolumebackups", true))
	op := veleroWorkspaceObj("PodVolumeBackup", "velero", "copy", map[string]string{"velero.io/backup-uid": "run"}, map[string]any{"node": "node-a"}, map[string]any{"phase": "InProgress", "progress": map[string]any{"totalBytes": int64(1024)}}, time.Now().Add(-time.Hour))
	seedCNPGWorkspace(t, kinds, op)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Impersonate-User") != "reader" {
			t.Error("run not read as caller")
		}
		if r.URL.Path != "/apis/velero.io/v1/namespaces/velero/backups/nightly" {
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"velero.io/v1","kind":"Backup","metadata":{"namespace":"velero","name":"nightly","uid":"run"},"status":{"phase":"Completed"}}`))
	}))
	t.Cleanup(api.Close)
	previous := k8s.SetTestConfig(&rest.Config{Host: api.URL})
	t.Cleanup(func() { k8s.SetTestConfig(previous) })
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{}}
	perms.SetCanI("get", veleroGroup, "backups", "velero", true)
	perms.SetCanI("list", veleroGroup, "podvolumebackups", "", false)
	perms.SetCanI("list", veleroGroup, "podvolumebackups", "velero", false)
	env.srv.permCache.Set("reader", nil, perms)
	resp := env.authGet(t, "/api/velero/backups/velero/nightly/volume-operations", "reader", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, readBody(t, resp))
	}
	var got velerosvc.VolumeOperationsResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Operations) != 0 || got.Coverage["podVolumeBackups"].State != "denied" || got.Coverage["dataUploads"].State != "notInstalled" {
		t.Fatalf("coverage: %+v", got)
	}
	controller := true
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "volume-agent", Namespace: "velero", Labels: map[string]string{"role": "node-agent"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "DaemonSet", Name: "node-agent", Controller: &controller}}}, Spec: corev1.PodSpec{NodeName: "node-a"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	if _, err := testFakeClient.CoreV1().Pods("velero").Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = testFakeClient.CoreV1().Pods("velero").Delete(context.Background(), pod.Name, metav1.DeleteOptions{})
	})
	waitFor(t, 5*time.Second, func() bool { _, err := k8s.GetResourceCache().Pods().Pods("velero").Get(pod.Name); return err == nil })
	perms.SetCanI("list", veleroGroup, "podvolumebackups", "velero", true)
	perms.SetCanI("list", "", "pods", "", false)
	for _, readPods := range []bool{false, true} {
		perms.SetCanI("list", "", "pods", "velero", readPods)
		response := env.authGet(t, "/api/velero/backups/velero/nightly/volume-operations", "reader", "")
		var annotated velerosvc.VolumeOperationsResponse
		if response.StatusCode != http.StatusOK {
			t.Fatalf("annotation status %d: %s", response.StatusCode, readBody(t, response))
		}
		err := json.NewDecoder(response.Body).Decode(&annotated)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if len(annotated.Operations) != 1 {
			t.Fatalf("annotation rows: %+v", annotated)
		}
		row := annotated.Operations[0]
		want := "unknown"
		if readPods {
			want = "running"
		}
		if !row.LongRunning || row.NodeAgent != want || row.BytesDone == nil || *row.BytesDone != 0 {
			t.Fatalf("pods readable %v: %+v", readPods, row)
		}
	}
	perms.SetCanI("get", veleroGroup, "backups", "velero", false)
	denied := env.authGet(t, "/api/velero/backups/velero/nightly/volume-operations", "reader", "")
	defer denied.Body.Close()
	if denied.StatusCode != http.StatusForbidden {
		t.Fatalf("run gate: %d", denied.StatusCode)
	}
}

func TestVeleroVolumeOperationsPinAndSharedListener(t *testing.T) {
	t.Run("pin first", func(t *testing.T) {
		pinNamespaceScope(t)
		env := newAuthTestServer(t)
		env.srv.permCache.Set("reader", nil, &auth.UserPermissions{AllowedNamespaces: []string{}})
		resp := env.authGet(t, "/api/velero/backups/velero/nightly/volume-operations", "reader", "")
		defer resp.Body.Close()
		assertOutsidePin(t, resp, readBody(t, resp))
	})
	srv := New(Config{DevMode: true, ListenAddress: "0.0.0.0"})
	t.Cleanup(srv.Stop)
	req := httptest.NewRequest(http.MethodGet, "/api/velero/backups/velero/nightly/volume-operations", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site read: %d", rec.Code)
	}
}
