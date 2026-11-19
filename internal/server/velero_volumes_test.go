package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestVeleroVolumeRecords(t *testing.T) {
	for _, restore := range []bool{false, true} {
		file := "backup-volume-infos.json"
		if restore {
			file = "restore-volume-info.json"
		}
		body, err := os.ReadFile("testdata/velero/" + file)
		if err != nil {
			t.Fatal(err)
		}
		result, err := decodeVeleroVolumes(bytes.NewReader(body), restore)
		if err != nil {
			t.Fatal(err)
		}
		got := result.(veleroRunVolumes)
		if restore {
			if got.Total != 4 || got.Volumes[0].Method != "CSISnapshot" || got.Volumes[1].PodVolumeInfo.PodName != "app" || got.Volumes[2].NativeSnapshotInfo.SnapshotHandle != "snap-123" {
				t.Fatalf("restore projection: %+v", got)
			}
			encoded, _ := json.Marshal(got)
			for _, field := range []string{`"result"`, `"skipped"`, `"preserveLocalSnapshot"`, `"started"`, `"completed"`} {
				if bytes.Contains(encoded, []byte(field)) {
					t.Errorf("restore claimed %s: %s", field, encoded)
				}
			}
			if got.Volumes[0].SnapshotDataMovementInfo.Size != nil || got.Volumes[1].PodVolumeInfo.Phase != "" {
				t.Fatal("absent values became known")
			}
		} else {
			if got.Total != 5 || got.Truncated {
				t.Fatalf("backup counts: %+v", got)
			}
			native, csi, mover, fs, skipped := got.Volumes[0], got.Volumes[1], got.Volumes[2], got.Volumes[3], got.Volumes[4]
			if native.Method != "NativeSnapshot" || native.Result != "succeeded" || native.NativeSnapshotInfo.Phase != "Completed" || native.PVName != "pv-native" {
				t.Fatalf("native: %+v", native)
			}
			if csi.Method != "CSISnapshot" || csi.Result != "" || *csi.CSISnapshotInfo.Size != 1<<30 || !*csi.CSISnapshotInfo.ReadyToUse {
				t.Fatalf("csi: %+v", csi)
			}
			if !mover.SnapshotDataMoved || mover.SnapshotDataMovementInfo.DataMover != "velero" || *mover.SnapshotDataMovementInfo.IncrementalSize != 1024 {
				t.Fatalf("mover: %+v", mover)
			}
			if fs.Method != "PodVolumeBackup" || fs.Result != "failed" || fs.PodVolumeInfo.NodeName != "node-a" || fs.Started == "" || fs.Completed == "" {
				t.Fatalf("fs: %+v", fs)
			}
			if skipped.Skipped == nil || !*skipped.Skipped || skipped.SkippedReason != "volume excluded by policy" || skipped.Result != "" {
				t.Fatalf("skipped: %+v", skipped)
			}
		}
	}
}

func TestVeleroVolumesRejectMalformedAndIncompleteRecords(t *testing.T) {
	// [{}] names no volume, so it is not a record Velero wrote.
	for _, body := range []string{`null`, `{}`, `[`, `[null]`, `[{}] {}`, `[{}]`, `[{"backupMethod":"CSISnapshot"}]`} {
		if _, err := decodeVeleroVolumes(strings.NewReader(body), false); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
}

func TestVeleroVolumesCountBeyondRowLimit(t *testing.T) {
	body := "[" + strings.Repeat(`{"pvcName":"data","backupMethod":"NativeSnapshot"},`, maxVeleroArtifactRows) + `{"pvName":"late","skipped":true,"skippedReason":"late"}]`
	result, err := decodeVeleroVolumes(strings.NewReader(body), false)
	if err != nil {
		t.Fatal(err)
	}
	got := result.(veleroRunVolumes)
	if !got.Truncated || got.Total != maxVeleroArtifactRows+1 || len(got.Volumes) != maxVeleroArtifactRows {
		t.Fatalf("counts: %+v", got)
	}
}

func TestVeleroVolumesUnsupportedDownloadTarget(t *testing.T) {
	// Another invalid request, such as a policy refusing its metadata, is not
	// a CRD that predates the target.
	unrelated := apierrors.NewInvalid(schema.GroupKind{Group: veleroGroup, Kind: "DownloadRequest"}, "request", field.ErrorList{field.Required(field.NewPath("metadata", "labels"), "a team label is required")})
	for _, kind := range []string{"backups", "restores"} {
		dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
		answerVeleroRun(dyn, "Completed")
		dyn.PrependReactor("create", "downloadrequests", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, unrelated
		})
		if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: veleroGroup, Version: "v1", Kind: "DownloadRequest", Name: "downloadrequests", Namespaced: true, IsCRD: true}}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(k8s.ResetTestDynamicState)
		other := New(Config{DevMode: true})
		t.Cleanup(other.Stop)
		rec := httptest.NewRecorder()
		other.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/velero/"+kind+"/velero/nightly/volumes", nil))
		created := false
		for _, a := range dyn.Actions() {
			created = created || a.GetVerb() == "create"
		}
		if !created || rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "CRD must support") {
			t.Fatalf("an unrelated rejection read as an old CRD (create attempted %v): %d %s", created, rec.Code, rec.Body.String())
		}

		dyn = dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
		answerVeleroRun(dyn, "Completed")
		dyn.PrependReactor("create", "downloadrequests", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewInvalid(schema.GroupKind{Group: veleroGroup, Kind: "DownloadRequest"}, "request", field.ErrorList{field.NotSupported(field.NewPath("spec", "target", "kind"), "VolumeInfo", []string{"BackupLog", "RestoreLog"})})
		})
		if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: veleroGroup, Version: "v1", Kind: "DownloadRequest", Name: "downloadrequests", Namespaced: true, IsCRD: true}}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(k8s.ResetTestDynamicState)
		srv := New(Config{DevMode: true})
		t.Cleanup(srv.Stop)
		rec = httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/velero/"+kind+"/velero/nightly/volumes", nil))
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "CRD must support") || !strings.Contains(rec.Body.String(), "Velero 1.13+") {
			t.Fatalf("unsupported target: %d %s", rec.Code, rec.Body.String())
		}
	}
}

// The record is provisional unless the run had finished when Radar read it,
// whatever its phase is by the time the browser asks about operations.
func TestVeleroVolumeRecordSaysWhetherTheRunHadFinished(t *testing.T) {
	store := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(gzipped(t, `[{"pvcName":"data","backupMethod":"CSISnapshot"}]`))
	}))
	t.Cleanup(store.Close)
	for phase, want := range map[string]bool{"Finalizing": false, "WaitingForPluginOperations": false, "InProgress": false, "Completed": true, "PartiallyFailed": true} {
		t.Run(phase, func(t *testing.T) {
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{downloadRequestGVR: "DownloadRequestList"})
			// The run completes while the file is fetched: what counts is the
			// phase read before asking for it.
			answerVeleroRun(dyn, phase)
			first := true
			dyn.PrependReactor("get", "backups", func(k8stesting.Action) (bool, runtime.Object, error) {
				if first {
					first = false
					return false, nil, nil
				}
				return true, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "velero.io/v1", "kind": "Backup", "metadata": map[string]any{"name": "nightly", "namespace": "velero"}, "status": map[string]any{"phase": "Completed"}}}, nil
			})
			dyn.PrependReactor("create", "downloadrequests", func(a k8stesting.Action) (bool, runtime.Object, error) {
				a.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured).SetName("dr")
				return false, nil, nil
			})
			dyn.PrependReactor("get", "downloadrequests", func(k8stesting.Action) (bool, runtime.Object, error) {
				return true, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "velero.io/v1", "kind": "DownloadRequest", "status": map[string]any{"downloadURL": store.URL}}}, nil
			})
			if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: veleroGroup, Version: "v1", Kind: "DownloadRequest", Name: "downloadrequests", Namespaced: true, IsCRD: true}}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(k8s.ResetTestDynamicState)
			t.Cleanup(k8s.SetTestLocalMode())
			srv := New(Config{DevMode: true, ListenAddress: "localhost"})
			t.Cleanup(srv.Stop)
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "http://localhost/api/velero/backups/velero/nightly/volumes", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			var got veleroRunVolumes
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(rec.Body.String(), fmt.Sprintf(`"finishedAtRead":%v`, want)) || got.FinishedAtRead != want || got.Total != 1 {
				t.Fatalf("finishedAtRead = %v, want %v: %+v", got.FinishedAtRead, want, got)
			}
		})
	}
}
