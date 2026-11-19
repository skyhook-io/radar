package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
)

func schedulePatches(dyn *dynamicfake.FakeDynamicClient) []k8stesting.PatchAction {
	var out []k8stesting.PatchAction
	for _, a := range dyn.Actions() {
		if p, ok := a.(k8stesting.PatchAction); ok && a.GetResource().Resource == "schedules" {
			out = append(out, p)
		}
	}
	return out
}

func patchBody(t *testing.T, p k8stesting.PatchAction) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(p.GetPatch(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestVeleroSchedulePause(t *testing.T) {
	// The patch is what `velero schedule pause|unpause` writes: spec.paused,
	// and spec.skipImmediately reset to unset unless the user chose, bound to
	// the reviewed version.
	cases := []struct {
		name   string
		path   string
		body   string
		paused bool
		skip   any
	}{
		{name: "pause leaves the skip to the server", path: "pause", body: `{"resourceVersion":"7"}`, paused: true, skip: nil},
		{name: "resume skipping the due run", path: "resume", body: `{"resourceVersion":"7","skipImmediately":true}`, skip: true},
		{name: "resume running the due run", path: "resume", body: `{"resourceVersion":"7","skipImmediately":false}`, skip: false},
		{name: "resume as the CLI does by default", path: "resume", body: `{"resourceVersion":"7","skipImmediately":null}`, skip: nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := backupNowSchedule("nightly", "7")
			s.Object["spec"].(map[string]any)["paused"] = !c.paused
			s.Object["spec"].(map[string]any)["skipImmediately"] = true
			srv, dyn := backupNowServer(t, s)
			rec := postBackupNow(srv, "/api/velero/schedules/velero/nightly/"+c.path, c.body, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
			}
			var resp VeleroSchedulePauseResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.Paused != c.paused || !resp.Changed {
				t.Fatalf("response %+v (%v)", resp, err)
			}
			patches := schedulePatches(dyn)
			if len(patches) != 1 {
				t.Fatalf("%d patches, want 1", len(patches))
			}
			body := patchBody(t, patches[0])
			spec := body["spec"].(map[string]any)
			skip, present := spec["skipImmediately"]
			if spec["paused"] != c.paused || !present || skip != c.skip {
				t.Errorf("spec patch %v, want paused=%v skipImmediately=%v (present, so a stored value is replaced)", spec, c.paused, c.skip)
			}
			if rv := body["metadata"].(map[string]any)["resourceVersion"]; rv != "7" {
				t.Errorf("patch resourceVersion %v, want the reviewed 7", rv)
			}
		})
	}

	t.Run("refuses a schedule edited since it was reviewed", func(t *testing.T) {
		srv, dyn := backupNowServer(t, backupNowSchedule("nightly", "8"))
		rec := postBackupNow(srv, "/api/velero/schedules/velero/nightly/pause", `{"resourceVersion":"7"}`, nil)
		if rec.Code != http.StatusConflict || len(schedulePatches(dyn)) != 0 {
			t.Fatalf("status = %d patches = %d; want 409 and nothing written", rec.Code, len(schedulePatches(dyn)))
		}
	})

	// The version check at the apiserver: an edit between the read and the patch.
	t.Run("an edit racing the patch is changed, not overwritten", func(t *testing.T) {
		srv, dyn := backupNowServer(t, backupNowSchedule("nightly", "7"))
		dyn.PrependReactor("patch", "schedules", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewConflict(veleroScheduleGVR.GroupResource(), "nightly", nil)
		})
		if rec := postBackupNow(srv, "/api/velero/schedules/velero/nightly/pause", `{"resourceVersion":"7"}`, nil); rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409", rec.Code)
		}
	})

	// A confirmation sent again after its answer was lost, or another user got
	// there first: nothing to do, as the CLI says "already paused".
	t.Run("already in that state writes nothing", func(t *testing.T) {
		s := backupNowSchedule("nightly", "9")
		s.Object["spec"].(map[string]any)["paused"] = true
		srv, dyn := backupNowServer(t, s)
		rec := postBackupNow(srv, "/api/velero/schedules/velero/nightly/pause", `{"resourceVersion":"7"}`, nil)
		var resp VeleroSchedulePauseResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		if rec.Code != http.StatusOK || !resp.Paused || resp.Changed || len(schedulePatches(dyn)) != 0 {
			t.Fatalf("status = %d resp = %+v patches = %d", rec.Code, resp, len(schedulePatches(dyn)))
		}
	})

	t.Run("forbidden to read", func(t *testing.T) {
		srv, dyn := backupNowServer(t, backupNowSchedule("nightly", "7"))
		dyn.PrependReactor("get", "schedules", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(veleroScheduleGVR.GroupResource(), "nightly", nil)
		})
		if rec := postBackupNow(srv, "/api/velero/schedules/velero/nightly/pause", `{"resourceVersion":"7"}`, nil); rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})

	t.Run("forbidden to patch", func(t *testing.T) {
		srv, dyn := backupNowServer(t, backupNowSchedule("nightly", "7"))
		dyn.PrependReactor("patch", "schedules", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(veleroScheduleGVR.GroupResource(), "nightly", nil)
		})
		if rec := postBackupNow(srv, "/api/velero/schedules/velero/nightly/pause", `{"resourceVersion":"7"}`, nil); rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})

	t.Run("says when the schedule is gone", func(t *testing.T) {
		srv, _ := backupNowServer(t)
		if rec := postBackupNow(srv, "/api/velero/schedules/velero/nightly/resume", `{"resourceVersion":"7"}`, nil); rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("an unexpected failure to read is unavailable", func(t *testing.T) {
		srv, dyn := backupNowServer(t, backupNowSchedule("nightly", "7"))
		dyn.PrependReactor("get", "schedules", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewServiceUnavailable("apiserver restarting")
		})
		if rec := postBackupNow(srv, "/api/velero/schedules/velero/nightly/pause", `{"resourceVersion":"7"}`, nil); rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
	})

	t.Run("an unexpected failure to patch is an internal error", func(t *testing.T) {
		srv, dyn := backupNowServer(t, backupNowSchedule("nightly", "7"))
		dyn.PrependReactor("patch", "schedules", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewInternalError(errors.New("etcd unavailable"))
		})
		if rec := postBackupNow(srv, "/api/velero/schedules/velero/nightly/pause", `{"resourceVersion":"7"}`, nil); rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
	})

	t.Run("needs the reviewed version", func(t *testing.T) {
		srv, _ := backupNowServer(t, backupNowSchedule("nightly", "7"))
		if rec := postBackupNow(srv, "/api/velero/schedules/velero/nightly/pause", `{}`, nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("refuses a request another site's page started", func(t *testing.T) {
		srv, dyn := backupNowServer(t, backupNowSchedule("nightly", "7"))
		rec := postBackupNow(srv, "/api/velero/schedules/velero/nightly/pause", `{"resourceVersion":"7"}`, map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"})
		if rec.Code != http.StatusForbidden || len(schedulePatches(dyn)) != 0 {
			t.Fatalf("status = %d patches = %d", rec.Code, len(schedulePatches(dyn)))
		}
	})
}

// With auth on, both the read and the patch run as the caller, never as
// Radar's own client, so the apiserver decides what the user may change.
func TestVeleroSchedulePauseReadsAndPatchesAsTheCaller(t *testing.T) {
	srv, dyn := backupNowServer(t, backupNowSchedule("nightly", "7"))
	var mu sync.Mutex
	var requests []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" as "+r.Header.Get("Impersonate-User"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"velero.io/v1","kind":"Schedule","metadata":{"name":"nightly","namespace":"velero","resourceVersion":"7"},"spec":{"schedule":"0 1 * * *"}}`))
	}))
	t.Cleanup(api.Close)
	previous := k8s.SetTestConfig(&rest.Config{Host: api.URL})
	t.Cleanup(func() { k8s.SetTestConfig(previous) })

	router := chi.NewRouter()
	router.Post("/api/velero/schedules/{namespace}/{name}/pause", srv.handleVeleroSchedulePause(true))
	req := httptest.NewRequest(http.MethodPost, "/api/velero/schedules/velero/nightly/pause", strings.NewReader(`{"resourceVersion":"7"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.ContextWithUser(req.Context(), &auth.User{Username: "alice"}))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 || requests[0] != "GET as alice" || requests[1] != "PATCH as alice" {
		t.Fatalf("API requests = %v, want a GET and a PATCH as alice", requests)
	}
	if n := len(schedulePatches(dyn)); n != 0 {
		t.Fatalf("issued %d patches through Radar's own client", n)
	}
}
