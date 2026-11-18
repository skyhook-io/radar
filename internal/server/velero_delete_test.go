package server

import (
	"context"
	"encoding/json"
	"errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
	veleropkg "github.com/skyhook-io/radar/pkg/velero"
)

func deletableBackup(name, uid, phase, location string) *unstructured.Unstructured {
	b := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "velero.io/v1",
		"kind":       "Backup",
		"metadata":   map[string]any{"name": name, "namespace": "velero"},
		"spec":       map[string]any{"storageLocation": location},
		"status":     map[string]any{"phase": phase},
	}}
	b.SetUID(types.UID(uid))
	return b
}

func storageLocation(name, mode, phase string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "velero.io/v1",
		"kind":       "BackupStorageLocation",
		"metadata":   map[string]any{"name": name, "namespace": "velero"},
		"spec":       map[string]any{"accessMode": mode},
		"status":     map[string]any{"phase": phase},
	}}
}

func deleteServer(t *testing.T, objs ...runtime.Object) (*Server, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		veleroBackupGVR:        "BackupList",
		veleroLocationGVR:      "BackupStorageLocationList",
		veleroDeleteRequestGVR: "DeleteBackupRequestList",
	}, objs...)
	dyn.PrependReactor("create", "deletebackuprequests", func(a clienttesting.Action) (bool, runtime.Object, error) {
		o := a.(clienttesting.CreateAction).GetObject().(*unstructured.Unstructured)
		if o.GetName() == "" {
			o.SetName(o.GetGenerateName() + "generated")
		}
		return false, nil, nil
	})
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{
		{Group: veleroGroup, Version: "v1", Kind: "Backup", Name: "backups", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list"}},
		{Group: veleroGroup, Version: "v1", Kind: "BackupStorageLocation", Name: "backupstoragelocations", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list"}},
		{Group: veleroGroup, Version: "v1", Kind: "DeleteBackupRequest", Name: "deletebackuprequests", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "create"}},
	}); err != nil {
		t.Fatalf("seed velero kinds: %v", err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	srv := New(Config{DevMode: true})
	t.Cleanup(srv.Stop)
	return srv, dyn
}

func serve(srv *Server, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func deleteRequests(t *testing.T, dyn *dynamicfake.FakeDynamicClient) []unstructured.Unstructured {
	t.Helper()
	list, err := dyn.Resource(veleroDeleteRequestGVR).Namespace("velero").List(t.Context(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return list.Items
}

func TestVeleroDeleteBackup(t *testing.T) {
	body := `{"uid":"uid-1"}`

	t.Run("asks Velero to delete the backup, as velero backup delete does", func(t *testing.T) {
		srv, dyn := deleteServer(t, deletableBackup("nightly-1", "uid-1", "Completed", "default"), storageLocation("default", "ReadWrite", "Available"))
		rec := serve(srv, http.MethodPost, "/api/velero/backups/velero/nightly-1/delete", body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		var got VeleroDeleteBackupResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		requests := deleteRequests(t, dyn)
		if len(requests) != 1 || requests[0].GetName() != got.Request {
			t.Fatalf("requests %d, response %+v", len(requests), got)
		}
		if spec, _, _ := unstructured.NestedString(requests[0].Object, "spec", "backupName"); spec != "nightly-1" {
			t.Errorf("spec.backupName = %q", spec)
		}
		if requests[0].GetLabels()[veleropkg.BackupUIDLabel] != "uid-1" {
			t.Errorf("labels %v", requests[0].GetLabels())
		}
		if _, err := dyn.Resource(veleroBackupGVR).Namespace("velero").Get(t.Context(), "nightly-1", metav1.GetOptions{}); err != nil {
			t.Error("the Backup object was deleted directly; only Velero may remove it")
		}
	})

	t.Run("the same confirmation sent again creates nothing new", func(t *testing.T) {
		srv, dyn := deleteServer(t, deletableBackup("nightly-1", "uid-1", "Completed", "default"), storageLocation("default", "ReadWrite", "Available"))
		first := serve(srv, http.MethodPost, "/api/velero/backups/velero/nightly-1/delete", body)
		second := serve(srv, http.MethodPost, "/api/velero/backups/velero/nightly-1/delete", body)
		if first.Code != http.StatusOK || second.Code != http.StatusOK || first.Body.String() != second.Body.String() {
			t.Fatalf("first %d %s, second %d %s", first.Code, first.Body, second.Code, second.Body)
		}
		if n := len(deleteRequests(t, dyn)); n != 1 {
			t.Errorf("%d requests, want 1", n)
		}
	})

	refusals := []struct {
		name     string
		backup   *unstructured.Unstructured
		location *unstructured.Unstructured
		body     string
		status   int
		says     string
	}{
		{"a backup recreated under the same name", deletableBackup("nightly-1", "uid-2", "Completed", "default"), storageLocation("default", "ReadWrite", "Available"), body, http.StatusConflict, "recreated"},
		{"a backup still running", deletableBackup("nightly-1", "uid-1", "InProgress", "default"), storageLocation("default", "ReadWrite", "Available"), body, http.StatusConflict, "still running"},
		{"no storage location", deletableBackup("nightly-1", "uid-1", "New", ""), storageLocation("default", "ReadWrite", "Available"), body, http.StatusConflict, "no storage location"},
		{"a read-only location", deletableBackup("nightly-1", "uid-1", "Completed", "default"), storageLocation("default", "ReadOnly", "Available"), body, http.StatusConflict, "read-only"},
		{"an unavailable location", deletableBackup("nightly-1", "uid-1", "Completed", "default"), storageLocation("default", "ReadWrite", "Unavailable"), body, http.StatusConflict, "not available"},
		{"no backup UID", deletableBackup("nightly-1", "uid-1", "Completed", "default"), storageLocation("default", "ReadWrite", "Available"), `{}`, http.StatusBadRequest, "uid"},
	}
	for _, c := range refusals {
		t.Run("refuses "+c.name, func(t *testing.T) {
			srv, dyn := deleteServer(t, c.backup, c.location)
			rec := serve(srv, http.MethodPost, "/api/velero/backups/velero/nightly-1/delete", c.body)
			if rec.Code != c.status || !strings.Contains(rec.Body.String(), c.says) {
				t.Fatalf("status = %d, body %s; want %d mentioning %q", rec.Code, rec.Body, c.status, c.says)
			}
			if n := len(deleteRequests(t, dyn)); n != 0 {
				t.Errorf("%d requests created", n)
			}
		})
	}

	t.Run("a location that can't be read leaves the refusal to Velero", func(t *testing.T) {
		srv, dyn := deleteServer(t, deletableBackup("nightly-1", "uid-1", "Completed", "elsewhere"))
		rec := serve(srv, http.MethodPost, "/api/velero/backups/velero/nightly-1/delete", body)
		if rec.Code != http.StatusOK || len(deleteRequests(t, dyn)) != 1 {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body)
		}
	})
}

func TestVeleroBackupDeletion(t *testing.T) {
	requestName := "nightly-1-generated"
	request := func(phase string, errs ...any) *unstructured.Unstructured {
		r := veleropkg.DeleteBackupRequestFor("velero", "nightly-1", "uid-1")
		r.SetName(requestName)
		status := map[string]any{"phase": phase}
		if len(errs) > 0 {
			status["errors"] = errs
		}
		r.Object["status"] = status
		return r
	}
	path := "/api/velero/backups/velero/nightly-1/deletion?uid=uid-1"
	cases := []struct {
		name string
		objs []runtime.Object
		want veleropkg.DeletionState
	}{
		{"waiting", []runtime.Object{request("New"), deletableBackup("nightly-1", "uid-1", "Completed", "default")}, veleropkg.DeletionWaiting},
		{"deleting", []runtime.Object{request("InProgress"), deletableBackup("nightly-1", "uid-1", "Deleting", "default")}, veleropkg.DeletionInProgress},
		{"refused by Velero", []runtime.Object{request("Processed", "backup storage location default not found"), deletableBackup("nightly-1", "uid-1", "Completed", "default")}, veleropkg.DeletionRefused},
		{"failed partway", []runtime.Object{request("Processed", "error deleting snapshot snap-1: throttled"), deletableBackup("nightly-1", "uid-1", "Deleting", "default")}, veleropkg.DeletionFailed},
		{"done: request and backup gone", nil, veleropkg.DeletionDone},
		{"done: a new backup took the name", []runtime.Object{deletableBackup("nightly-1", "uid-9", "Completed", "default")}, veleropkg.DeletionDone},
		{"unknown: request gone, backup remains", []runtime.Object{deletableBackup("nightly-1", "uid-1", "Completed", "default")}, veleropkg.DeletionUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := deleteServer(t, c.objs...)
			rec := serve(srv, http.MethodGet, path, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body)
			}
			var got VeleroDeletionResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.State != c.want {
				t.Errorf("state %q, want %q", got.State, c.want)
			}
		})
	}

	t.Run("says when the request was made", func(t *testing.T) {
		at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
		r := request("Processed", "backup storage location default not found")
		r.SetCreationTimestamp(metav1.NewTime(at))
		srv, _ := deleteServer(t, r, deletableBackup("nightly-1", "uid-1", "Completed", "default"))
		rec := serve(srv, http.MethodGet, path, "")
		var got VeleroDeletionResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Created == nil || !got.Created.Equal(at) {
			t.Fatalf("created = %v, want %v: %s", got.Created, at, rec.Body)
		}
	})

	t.Run("a request for another backup is excluded", func(t *testing.T) {
		other := veleropkg.DeleteBackupRequestFor("velero", "weekly-1", "uid-7")
		other.SetName(requestName)
		srv, _ := deleteServer(t, other)
		if rec := serve(srv, http.MethodGet, path, ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"state":"deleted"`) {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body)
		}
	})
}

func TestVeleroDeleteBackupRefusesStoppedServer(t *testing.T) {
	srv, dyn := deleteServer(t, deletableBackup("nightly-1", "uid-1", "Completed", "default"))
	seedVeleroActionServer(t, 0)
	rec := serve(srv, http.MethodPost, "/api/velero/backups/velero/nightly-1/delete", `{"uid":"uid-1"}`)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "stopped") || len(deleteRequests(t, dyn)) != 0 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestVeleroDeletionLookupNewestAndOpen(t *testing.T) {
	request := func(name, phase, uid string, ago time.Duration) *unstructured.Unstructured {
		r := veleropkg.DeleteBackupRequestFor("velero", "nightly-1", uid)
		r.SetName(name)
		r.SetCreationTimestamp(metav1.NewTime(time.Now().Add(-ago)))
		r.Object["status"] = map[string]any{"phase": phase}
		return r
	}
	srv, dyn := deleteServer(t, deletableBackup("nightly-1", "uid-1", "Completed", "default"),
		request("old-open", "InProgress", "uid-1", 3*time.Hour),
		request("new-processed", "Processed", "uid-1", time.Hour),
		request("other-uid", "New", "uid-2", 0))
	get := serve(srv, http.MethodGet, "/api/velero/backups/velero/nightly-1/deletion?uid=uid-1", "")
	if get.Code != 200 || !strings.Contains(get.Body.String(), `"request":"new-processed"`) {
		t.Fatalf("%d %s", get.Code, get.Body)
	}
	post := serve(srv, http.MethodPost, "/api/velero/backups/velero/nightly-1/delete", `{"uid":"uid-1"}`)
	if post.Code != 200 || !strings.Contains(post.Body.String(), "old-open") || len(deleteRequests(t, dyn)) != 3 {
		t.Fatalf("%d %s", post.Code, post.Body)
	}
	dyn.PrependReactor("list", "deletebackuprequests", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(veleroDeleteRequestGVR.GroupResource(), "", errors.New("denied"))
	})
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		path, body := "/api/velero/backups/velero/nightly-1/deletion?uid=uid-1", ""
		if method == http.MethodPost {
			path, body = "/api/velero/backups/velero/nightly-1/delete", `{"uid":"uid-1"}`
		}
		if rec := serve(srv, method, path, body); rec.Code != 403 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
	}
}

func seedVeleroActionServer(t *testing.T, replicas int32, flags ...string) {
	t.Helper()
	name := "velero-action-test"
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "velero"},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "velero", Command: []string{"/velero", "server"}, Args: flags}}}}},
		Status:     appsv1.DeploymentStatus{Replicas: replicas, UpdatedReplicas: replicas},
	}
	if _, err := testFakeClient.AppsV1().Deployments("velero").Create(t.Context(), dep, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	wait := func(exists bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			_, err := k8s.GetResourceCache().Deployments().Deployments("velero").Get(name)
			if (err == nil) == exists {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("deployment cache did not update")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	wait(true)
	t.Cleanup(func() {
		if err := testFakeClient.AppsV1().Deployments("velero").Delete(context.Background(), name, metav1.DeleteOptions{}); err != nil {
			t.Error(err)
		}
		wait(false)
	})
}
