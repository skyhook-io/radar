package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	veleropkg "github.com/skyhook-io/radar/pkg/velero"
)

const testSubmission = "confirmation-0001"

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
	body := `{"uid":"uid-1","submission":"` + testSubmission + `"}`

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
		{"a read-only location", deletableBackup("nightly-1", "uid-1", "Completed", "default"), storageLocation("default", "ReadOnly", "Available"), body, http.StatusConflict, "read-only"},
		{"an unavailable location", deletableBackup("nightly-1", "uid-1", "Completed", "default"), storageLocation("default", "ReadWrite", "Unavailable"), body, http.StatusConflict, "not available"},
		{"no submission ID", deletableBackup("nightly-1", "uid-1", "Completed", "default"), storageLocation("default", "ReadWrite", "Available"), `{"uid":"uid-1"}`, http.StatusBadRequest, "submission"},
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
	requestName := veleropkg.DeleteBackupRequestName("nightly-1", testSubmission)
	request := func(phase string, errs ...any) *unstructured.Unstructured {
		r := veleropkg.DeleteBackupRequestFor("velero", "nightly-1", "uid-1", testSubmission)
		status := map[string]any{"phase": phase}
		if len(errs) > 0 {
			status["errors"] = errs
		}
		r.Object["status"] = status
		return r
	}
	path := "/api/velero/backups/velero/nightly-1/deletion?request=" + requestName + "&uid=uid-1"
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

	t.Run("a request for another backup is refused", func(t *testing.T) {
		other := veleropkg.DeleteBackupRequestFor("velero", "weekly-1", "uid-7", "x")
		other.SetName(requestName)
		srv, _ := deleteServer(t, other)
		if rec := serve(srv, http.MethodGet, path, ""); rec.Code != http.StatusConflict {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body)
		}
	})
}
