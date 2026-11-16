package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	veleropkg "github.com/skyhook-io/radar/pkg/velero"
)

func backupNowSchedule(name, resourceVersion string) *unstructured.Unstructured {
	s := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "velero.io/v1",
		"kind":       "Schedule",
		"metadata":   map[string]any{"name": name, "namespace": "velero", "resourceVersion": resourceVersion},
		"spec": map[string]any{
			"schedule": "0 1 * * *",
			"template": map[string]any{"includedNamespaces": []any{"payments"}, "storageLocation": "default", "ttl": "720h0m0s"},
		},
	}}
	return s
}

func backupNowServer(t *testing.T, objs ...runtime.Object) (*Server, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		veleroScheduleGVR: "ScheduleList",
		veleroBackupGVR:   "BackupList",
	}, objs...)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{
		{Group: veleroGroup, Version: "v1", Kind: "Schedule", Name: "schedules", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list"}},
		{Group: veleroGroup, Version: "v1", Kind: "Backup", Name: "backups", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "create"}},
	}); err != nil {
		t.Fatalf("seed velero kinds: %v", err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	srv := New(Config{DevMode: true})
	t.Cleanup(srv.Stop)
	return srv, dyn
}

func postBackupNow(srv *Server, path, body string, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func createdBackups(dyn *dynamicfake.FakeDynamicClient) int {
	n := 0
	for _, a := range dyn.Actions() {
		if a.GetVerb() == "create" && a.GetResource().Resource == "backups" {
			n++
		}
	}
	return n
}

func TestVeleroBackupNow(t *testing.T) {
	t.Run("creates the backup Velero's CLI would, from the reviewed schedule", func(t *testing.T) {
		srv, dyn := backupNowServer(t, backupNowSchedule("nightly", "7"))
		rec := postBackupNow(srv, "/api/velero/schedules/velero/nightly/backup", `{"submission":"confirmation-0001","resourceVersion":"7"}`, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		var got VeleroBackupNowResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Namespace != "velero" || !strings.HasPrefix(got.Name, "nightly-") {
			t.Fatalf("response %+v", got)
		}
		b, err := dyn.Resource(veleroBackupGVR).Namespace("velero").Get(t.Context(), got.Name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("the backup was not created: %v", err)
		}
		if b.GetAnnotations()[veleroSubmissionAnnotation] != "confirmation-0001" {
			t.Fatalf("missing submission: %v", b.GetAnnotations())
		}
		if b.GetLabels()[veleropkg.ScheduleNameLabel] != "nightly" {
			t.Errorf("labels %v: without the schedule-name label it is not one of the schedule's runs", b.GetLabels())
		}
		if loc, _, _ := unstructured.NestedString(b.Object, "spec", "storageLocation"); loc != "default" {
			t.Errorf("spec %v, want the schedule's template", b.Object["spec"])
		}
	})

	// The answer to the first request was lost and the same confirmation is
	// sent again: it gets the backup it already made, and no second one.
	t.Run("returns the backup a repeated confirmation already created", func(t *testing.T) {
		srv, dyn := backupNowServer(t, backupNowSchedule("nightly", "7"))
		body := `{"submission":"confirmation-0001","resourceVersion":"7","name":"nightly-20261115210509"}`
		first := postBackupNow(srv, "/api/velero/schedules/velero/nightly/backup", body, nil)
		again := postBackupNow(srv, "/api/velero/schedules/velero/nightly/backup", body, nil)
		if first.Code != http.StatusOK || again.Code != http.StatusOK || !strings.Contains(again.Body.String(), "nightly-20261115210509") {
			t.Fatalf("first %d %s, again %d %s", first.Code, first.Body.String(), again.Code, again.Body.String())
		}
		list, err := dyn.Resource(veleroBackupGVR).Namespace("velero").List(t.Context(), metav1.ListOptions{})
		if err != nil || len(list.Items) != 1 {
			t.Fatalf("backups = %d (%v), want exactly one", len(list.Items), err)
		}
		// The schedule was edited after the backup was made: the replay still
		// gets that backup rather than a refusal.
		edited := backupNowSchedule("nightly", "9")
		if _, err := dyn.Resource(veleroScheduleGVR).Namespace("velero").Update(t.Context(), edited, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		if after := postBackupNow(srv, "/api/velero/schedules/velero/nightly/backup", body, nil); after.Code != http.StatusOK {
			t.Fatalf("replay after an edit: %d %s", after.Code, after.Body.String())
		}
	})

	t.Run("refuses a name that is not this schedule's", func(t *testing.T) {
		srv, dyn := backupNowServer(t, backupNowSchedule("nightly", "7"))
		rec := postBackupNow(srv, "/api/velero/schedules/velero/nightly/backup", `{"submission":"confirmation-0001","resourceVersion":"7","name":"other-20261115210509"}`, nil)
		if rec.Code != http.StatusBadRequest || createdBackups(dyn) != 0 {
			t.Fatalf("status = %d creates = %d, want 400 and nothing created", rec.Code, createdBackups(dyn))
		}
	})

	t.Run("refuses a schedule edited since it was reviewed", func(t *testing.T) {
		srv, dyn := backupNowServer(t, backupNowSchedule("nightly", "8"))
		rec := postBackupNow(srv, "/api/velero/schedules/velero/nightly/backup", `{"submission":"confirmation-0001","resourceVersion":"7"}`, nil)
		if rec.Code != http.StatusConflict || createdBackups(dyn) != 0 {
			t.Fatalf("status = %d, creates = %d; want 409 and nothing created", rec.Code, createdBackups(dyn))
		}
	})

	t.Run("refuses a schedule whose backups would fail", func(t *testing.T) {
		long := strings.Repeat("x", 64)
		srv, dyn := backupNowServer(t, backupNowSchedule(long, "7"))
		rec := postBackupNow(srv, "/api/velero/schedules/velero/"+long+"/backup", `{"submission":"confirmation-0001","resourceVersion":"7"}`, nil)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "63 characters") || createdBackups(dyn) != 0 {
			t.Fatalf("status = %d body = %s creates = %d", rec.Code, rec.Body.String(), createdBackups(dyn))
		}
	})

	t.Run("needs the reviewed version", func(t *testing.T) {
		srv, _ := backupNowServer(t, backupNowSchedule("nightly", "7"))
		if rec := postBackupNow(srv, "/api/velero/schedules/velero/nightly/backup", `{}`, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("says when the schedule is gone", func(t *testing.T) {
		srv, _ := backupNowServer(t)
		if rec := postBackupNow(srv, "/api/velero/schedules/velero/nightly/backup", `{"submission":"confirmation-0001","resourceVersion":"7"}`, nil); rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})

	// Shared without authentication, anyone reaching Radar may back up, as with
	// every other write; a page in their browser on another site may not.
	t.Run("refuses a request another site's page started", func(t *testing.T) {
		srv, dyn := backupNowServer(t, backupNowSchedule("nightly", "7"))
		rec := postBackupNow(srv, "/api/velero/schedules/velero/nightly/backup", `{"submission":"confirmation-0001","resourceVersion":"7"}`, map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"})
		if rec.Code != http.StatusForbidden || createdBackups(dyn) != 0 {
			t.Fatalf("status = %d creates = %d, want 403 and nothing created", rec.Code, createdBackups(dyn))
		}
	})
}

func TestVeleroBackupNowSubmissionIdentity(t *testing.T) {
	path := "/api/velero/schedules/velero/nightly/backup"
	body := `{"resourceVersion":"7","name":"nightly-20261115210509","submission":"confirmation-first"}`
	t.Run("two confirmations in the same second", func(t *testing.T) {
		srv, _ := backupNowServer(t, backupNowSchedule("nightly", "7"))
		if first := postBackupNow(srv, path, body, nil); first.Code != 200 {
			t.Fatal(first.Body)
		}
		other := postBackupNow(srv, path, strings.ReplaceAll(body, "confirmation-first", "confirmation-second"), nil)
		if other.Code != 409 || !strings.Contains(other.Body.String(), "another backup was created") {
			t.Fatalf("%d %s", other.Code, other.Body)
		}
	})
	t.Run("a controller backup is not a replay", func(t *testing.T) {
		backup, _ := veleropkg.BackupFromSchedule(backupNowSchedule("nightly", "7"), time.Now())
		backup.SetName("nightly-20261115210509")
		srv, _ := backupNowServer(t, backupNowSchedule("nightly", "7"), backup)
		rec := postBackupNow(srv, path, body, nil)
		if rec.Code != 409 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
	})
	t.Run("own replay survives a schedule edit", func(t *testing.T) {
		srv, dyn := backupNowServer(t, backupNowSchedule("nightly", "7"))
		first := postBackupNow(srv, path, body, nil)
		if _, err := dyn.Resource(veleroScheduleGVR).Namespace("velero").Update(t.Context(), backupNowSchedule("nightly", "8"), metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		again := postBackupNow(srv, path, body, nil)
		if first.Code != 200 || again.Code != 200 || first.Body.String() != again.Body.String() || createdBackups(dyn) != 1 {
			t.Fatalf("first %d %s, again %d %s", first.Code, first.Body, again.Code, again.Body)
		}
	})
	for _, id := range []string{"", "short", "bad/slash-identity", strings.Repeat("a", 65)} {
		t.Run("invalid ID "+id, func(t *testing.T) {
			srv, dyn := backupNowServer(t, backupNowSchedule("nightly", "7"))
			rec := postBackupNow(srv, path, strings.ReplaceAll(body, "confirmation-first", id), nil)
			if rec.Code != 400 || createdBackups(dyn) != 0 {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestVeleroBackupNowRefusesDisabledController(t *testing.T) {
	for _, flags := range [][]string{{"--restore-only"}, {"--disable-controllers=backup"}} {
		t.Run(strings.Join(flags, " "), func(t *testing.T) {
			srv, dyn := backupNowServer(t, backupNowSchedule("nightly", "7"))
			seedVeleroActionServer(t, 1, flags...)
			rec := postBackupNow(srv, "/api/velero/schedules/velero/nightly/backup", `{"resourceVersion":"7","submission":"confirmation-one"}`, nil)
			if rec.Code != 409 || !strings.Contains(rec.Body.String(), "backup controller is disabled") || createdBackups(dyn) != 0 {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
		})
	}
}
