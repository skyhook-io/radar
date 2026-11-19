package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestVeleroContentsFormats(t *testing.T) {
	for _, restore := range []bool{false, true} {
		file := "backup-resource-list.json"
		if restore {
			file = "restore-resource-list.json"
		}
		body, err := os.ReadFile("testdata/velero/" + file)
		if err != nil {
			t.Fatal(err)
		}
		result, err := decodeVeleroContents(bytes.NewReader(body), restore, veleroContentsFilter{}, false)
		if err != nil {
			t.Fatal(err)
		}
		got := result.(veleroRunContents)
		if !reflect.DeepEqual(got.Namespaces, []string{"demo-app", "empty-app"}) {
			t.Fatalf("namespaces: %v", got.Namespaces)
		}
		if restore {
			if got.Total != 7 || got.Actions["created"] != 3 || got.Actions["failed"] != 2 || got.Actions["updated"] != 1 || got.Actions["skipped"] != 1 {
				t.Fatalf("restore: %+v", got)
			}
		} else if got.Total != 6 || len(got.Actions) != 0 {
			t.Fatalf("backup: %+v", got)
		}
		for _, group := range got.Groups {
			for _, item := range group.Items {
				if group.Kind == "v1/PersistentVolume" && (item.Namespace != "" || item.Name != "pvc-a12") {
					t.Fatalf("cluster item: %+v", item)
				}
			}
		}
	}
}

func TestVeleroContentsFiltersBeforeCappingAndCountsTheWholeFile(t *testing.T) {
	items := make([]string, maxVeleroArtifactRows+2)
	for i := range items {
		items[i] = fmt.Sprintf("app/item-%d(created)", i)
	}
	items[len(items)-1] = "app/late-failure(failed)"
	body, _ := json.Marshal(map[string][]string{"v1/ConfigMap": items, "v1/Namespace": {"empty(created)"}})
	for _, filter := range []veleroContentsFilter{{}, {action: "failed"}, {query: "late-failure"}, {namespace: "empty"}, {kind: "v1/Namespace"}} {
		result, err := decodeVeleroContents(bytes.NewReader(body), true, filter, false)
		if err != nil {
			t.Fatal(err)
		}
		got := result.(veleroRunContents)
		if got.Total != len(items)+1 || got.Actions["failed"] != 1 || !reflect.DeepEqual(got.Namespaces, []string{"app", "empty"}) {
			t.Fatalf("whole-file counts: %+v", got)
		}
		if filter == (veleroContentsFilter{}) {
			if !got.Truncated || got.Matched != got.Total {
				t.Fatalf("uncapped counts: %+v", got)
			}
		} else if got.Matched != 1 || got.Truncated {
			t.Fatalf("filtered: %+v", got)
		}
	}
}

func TestVeleroContentsRejectsIncompleteFiles(t *testing.T) {
	for _, body := range []string{`null`, `[]`, `{"v1/Pod":null}`, `{"v1/Pod":["ns/p"]`, `{} {}`} {
		if _, err := decodeVeleroContents(strings.NewReader(body), false, veleroContentsFilter{}, false); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}

func TestVeleroLogFormatsAndFilters(t *testing.T) {
	for _, file := range []string{"run.log", "run.jsonl"} {
		body, err := os.ReadFile("testdata/velero/" + file)
		if err != nil {
			t.Fatal(err)
		}
		result, err := decodeVeleroLog(bytes.NewReader(body), map[string]bool{"error": true, "warning": true}, "", false)
		if err != nil {
			t.Fatal(err)
		}
		got := result.(veleroRunLog)
		if got.Counts["error"] != 1 || got.Counts["warning"] != 1 || got.Counts["debug"] != 1 || got.Counts["other"] != 2 || len(got.Lines) != 2 || got.Partial {
			t.Fatalf("%s: %+v", file, got)
		}
		filtered, err := decodeVeleroLog(bytes.NewReader(body), map[string]bool{"error": true}, "permission denied", false)
		if err != nil {
			t.Fatal(err)
		}
		want := filtered.(veleroRunLog)
		if len(want.Lines) != 1 || want.Counts["info"] != got.Counts["info"] {
			t.Fatalf("filtered: %+v", want)
		}
	}
}

func TestVeleroLogRowsDoNotCapCountsOrSearch(t *testing.T) {
	body := strings.Repeat("level=info msg=ok\n", maxVeleroArtifactRows+1) + "level=error msg=late-failure\n"
	result, err := decodeVeleroLog(strings.NewReader(body), map[string]bool{"info": true, "error": true}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	got := result.(veleroRunLog)
	if len(got.Lines) != maxVeleroArtifactRows || got.Counts["error"] != 1 || !got.Truncated || got.Partial {
		t.Fatalf("capped: %+v", got)
	}
	result, err = decodeVeleroLog(strings.NewReader(body), map[string]bool{"error": true}, "late-failure", false)
	if err != nil {
		t.Fatal(err)
	}
	got = result.(veleroRunLog)
	if len(got.Lines) != 1 || got.Truncated || got.Lines[0].Number != maxVeleroArtifactRows+2 {
		t.Fatalf("late error: %+v", got)
	}
}

// levels=auto answers in one download what the tab opens on: errors and
// warnings when the log has any, every line otherwise.
func TestVeleroLogAutoPicksProblemsElseEverything(t *testing.T) {
	for name, tc := range map[string]struct {
		body   string
		levels []string
		lines  int
	}{
		"with problems": {"level=info msg=a\nlevel=warning msg=b\nlevel=debug msg=c\nlevel=error msg=d\n", []string{"error", "warning"}, 2},
		"clean":         {"level=info msg=a\nlevel=debug msg=b\nplain line\n", veleroLogLevels, 3},
	} {
		t.Run(name, func(t *testing.T) {
			result, err := decodeVeleroLog(strings.NewReader(tc.body), nil, "", true)
			if err != nil {
				t.Fatal(err)
			}
			got := result.(veleroRunLog)
			if !reflect.DeepEqual(got.Levels, tc.levels) || len(got.Lines) != tc.lines || got.Matched != tc.lines {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func TestVeleroLogLineLimitIgnoresTheLineEnding(t *testing.T) {
	body := "level=info msg=" + strings.Repeat("x", maxVeleroLogLineBytes-len("level=info msg=")) + "\r\nlevel=error msg=after\r\n"
	result, err := decodeVeleroLog(strings.NewReader(body), map[string]bool{"error": true}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.(veleroRunLog); got.Partial || got.Counts["error"] != 1 {
		t.Fatalf("a 64 KiB CRLF line was read as over the limit: %+v", got)
	}
}

func TestVeleroLogResponseTextIsBounded(t *testing.T) {
	line := "level=error msg=" + strings.Repeat("x", 12<<10) + "\n"
	result, err := decodeVeleroLog(strings.NewReader(strings.Repeat(line, 1000)), map[string]bool{"error": true}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	got := result.(veleroRunLog)
	returned := 0
	for _, l := range got.Lines {
		returned += len(l.Text)
	}
	if returned > maxVeleroLogResponseBytes || len(got.Lines) == 0 || len(got.Lines) == 1000 || !got.Truncated || got.Matched != 1000 || got.Counts["error"] != 1000 {
		t.Fatalf("returned %d bytes in %d lines: matched %d, truncated %v", returned, len(got.Lines), got.Matched, got.Truncated)
	}
}

// Velero issues no download link for a run that is gone or not yet started, so
// the route answers from the run itself rather than waiting out the timeout.
func TestVeleroArtifactRoutesAnswerFromTheRunBeforeAskingVelero(t *testing.T) {
	for name, tc := range map[string]struct {
		phase string // "" with seed=false: no such run
		seed  bool
		want  string
	}{
		"deleted run":   {seed: false, want: "Velero Backup not found"},
		"not started":   {phase: "New", seed: true, want: "has not started this backup yet"},
		"phase not set": {phase: "", seed: true, want: "has not started this backup yet"},
	} {
		t.Run(name, func(t *testing.T) {
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{downloadRequestGVR: "DownloadRequestList", {Group: veleroGroup, Version: "v1", Resource: "backups"}: "BackupList"})
			if tc.seed {
				answerVeleroRun(dyn, tc.phase)
			}
			if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: veleroGroup, Version: "v1", Kind: "DownloadRequest", Name: "downloadrequests", Namespaced: true, IsCRD: true}}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(k8s.ResetTestDynamicState)
			t.Cleanup(k8s.SetTestLocalMode())
			srv := New(Config{DevMode: true, ListenAddress: "localhost"})
			t.Cleanup(srv.Stop)
			start := time.Now()
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "http://localhost/api/velero/backups/velero/nightly/log", nil))
			if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			if time.Since(start) > 2*time.Second {
				t.Errorf("took %s waiting for a link Velero never issues", time.Since(start))
			}
			for _, a := range dyn.Actions() {
				if a.GetVerb() == "create" {
					t.Errorf("created a %s for a run Velero cannot serve", a.GetResource().Resource)
				}
			}
		})
	}
}

func TestVeleroLogLongLineStopsWithPrefixCoverage(t *testing.T) {
	prefix := "level=info msg=ok\n"
	for _, length := range []int{maxVeleroLogLineBytes, maxVeleroLogLineBytes + 1} {
		body := prefix + "level=debug msg=" + strings.Repeat("x", length-len("level=debug msg=")) + "\nlevel=error msg=after\n"
		result, err := decodeVeleroLog(strings.NewReader(body), map[string]bool{"error": true}, "", false)
		if err != nil {
			t.Fatal(err)
		}
		got := result.(veleroRunLog)
		if length > maxVeleroLogLineBytes {
			if !got.Partial || got.DecodedBytes != int64(len(prefix)) || got.Counts["error"] != 0 || got.Limit != "64 KiB line limit" {
				t.Fatalf("prefix: %+v", got)
			}
		} else if got.Partial || got.Counts["error"] != 1 {
			t.Fatalf("exact line cap: %+v", got)
		}
	}
}

func TestVeleroArtifactGzipCaps(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		t.Run(fmt.Sprint("compressed=", compressed), func(t *testing.T) {
			var zipped bytes.Buffer
			compression, size := gzip.BestSpeed, maxVeleroDecodedBytes+65536
			if compressed {
				compression, size = gzip.NoCompression, maxVeleroCompressedBytes+65536
			}
			writer, err := gzip.NewWriterLevel(&zipped, compression)
			if err != nil {
				t.Fatal(err)
			}
			line := "level=info msg=" + strings.Repeat("x", 60000) + "\n"
			for size > 0 {
				n, err := io.WriteString(writer, line)
				if err != nil {
					t.Fatal(err)
				}
				size -= n
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			store := serveResults(t, zipped.Bytes())
			result, err := fetchVeleroArtifact(context.Background(), store.URL, localVeleroDownloadPolicy, maxVeleroCompressedBytes, maxVeleroDecodedBytes, func(r io.Reader) (any, error) { return decodeVeleroLog(r, map[string]bool{"error": true}, "", false) })
			if err != nil {
				t.Fatal(err)
			}
			got := result.(veleroRunLog)
			want := "64 MiB decoded limit"
			if compressed {
				want = "16 MiB compressed limit"
			}
			if !got.Partial || got.Limit != want || got.DecodedBytes == 0 || got.DecodedBytes > maxVeleroDecodedBytes {
				t.Fatalf("cap: %+v", got)
			}
		})
	}
}

func TestVeleroContentsSizeLimitNeverReturnsPartialCounts(t *testing.T) {
	body := `{"v1/ConfigMap":["app/` + strings.Repeat("x", 1024) + `"]}`
	store := serveResults(t, gzipped(t, body))
	for _, limits := range [][2]int64{{16, maxVeleroDecodedBytes}, {maxVeleroCompressedBytes, 64}} {
		result, err := fetchVeleroArtifact(context.Background(), store.URL, localVeleroDownloadPolicy, limits[0], limits[1], func(r io.Reader) (any, error) { return decodeVeleroContents(r, false, veleroContentsFilter{}, false) })
		if result != nil || !(errors.Is(err, errVeleroCompressedLimit) || errors.Is(err, errVeleroDecodedLimit)) {
			t.Fatalf("partial counts returned: %+v, %v", result, err)
		}
	}
}

func TestVeleroArtifactRoutesAndStorageFailures(t *testing.T) {
	for _, kind := range []string{"backups", "restores"} {
		for _, artifact := range []string{"contents", "log"} {
			for _, status := range []int{http.StatusOK, http.StatusNotFound, http.StatusForbidden, http.StatusTemporaryRedirect} {
				t.Run(fmt.Sprintf("%s/%s/%d", kind, artifact, status), func(t *testing.T) {
					store := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Location", "https://never-follow.invalid/?signature=secret")
						w.WriteHeader(status)
						body := `{"v1/Namespace":["empty"]}`
						if artifact == "log" {
							body = "level=warning msg=hello\n"
						}
						_, _ = w.Write(gzipped(t, body))
					}))
					t.Cleanup(store.Close)
					dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{downloadRequestGVR: "DownloadRequestList"})
					answerVeleroRun(dyn, "Completed")
					dyn.PrependReactor("get", "downloadrequests", func(k8stesting.Action) (bool, runtime.Object, error) {
						return true, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "velero.io/v1", "kind": "DownloadRequest", "status": map[string]any{"downloadURL": store.URL + "?signature=secret"}}}, nil
					})
					dyn.PrependReactor("create", "downloadrequests", func(a k8stesting.Action) (bool, runtime.Object, error) {
						obj := a.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
						target, _, _ := unstructured.NestedString(obj.Object, "spec", "target", "kind")
						suffix := "ResourceList"
						if artifact == "log" {
							suffix = "Log"
						}
						run := "Backup"
						if kind == "restores" {
							run = "Restore"
						}
						if target != run+suffix || obj.GetGenerateName() != "nightly-" {
							t.Errorf("download request: %+v", obj)
						}
						obj.SetName("nightly-generated")
						return false, nil, nil
					})
					if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: veleroGroup, Version: "v1", Kind: "DownloadRequest", Name: "downloadrequests", Namespaced: true, IsCRD: true}}); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(k8s.ResetTestDynamicState)
					t.Cleanup(k8s.SetTestLocalMode())
					srv := New(Config{DevMode: true, ListenAddress: "localhost"})
					t.Cleanup(srv.Stop)
					rec := httptest.NewRecorder()
					srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "http://localhost/api/velero/"+kind+"/velero/nightly/"+artifact, nil))
					expected := http.StatusServiceUnavailable
					if status == http.StatusOK || status == http.StatusNotFound {
						expected = status
					}
					if rec.Code != expected {
						t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
					}
					if status == http.StatusNotFound && !strings.Contains(rec.Body.String(), "Velero has no "+artifact+" for this run") {
						t.Fatalf("missing file: %s", rec.Body.String())
					}
					if strings.Contains(rec.Body.String(), "secret") {
						t.Fatalf("credential leak: %s", rec.Body.String())
					}
					deleted := false
					for _, a := range dyn.Actions() {
						if a.GetVerb() == "delete" {
							deleted = true
						}
					}
					if !deleted {
						t.Error("DownloadRequest was not cleaned up")
					}
				})
			}
		}
	}
}

func TestVeleroArtifactRoutesRefuseLinkLocalDownloads(t *testing.T) {
	for _, artifact := range []string{"contents", "log"} {
		t.Run(artifact, func(t *testing.T) {
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{downloadRequestGVR: "DownloadRequestList"})
			answerVeleroRun(dyn, "Completed")
			dyn.PrependReactor("get", "downloadrequests", func(k8stesting.Action) (bool, runtime.Object, error) {
				return true, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "velero.io/v1", "kind": "DownloadRequest", "status": map[string]any{"downloadURL": "http://169.254.169.254/latest?signature=secret"}}}, nil
			})
			dyn.PrependReactor("create", "downloadrequests", func(a k8stesting.Action) (bool, runtime.Object, error) {
				a.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured).SetName("nightly-generated")
				return false, nil, nil
			})
			if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: veleroGroup, Version: "v1", Kind: "DownloadRequest", Name: "downloadrequests", Namespaced: true, IsCRD: true}}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(k8s.ResetTestDynamicState)
			// Local private listener: loopback would be allowed, link-local never is.
			t.Cleanup(k8s.SetTestLocalMode())
			srv := New(Config{DevMode: true, ListenAddress: "localhost"})
			t.Cleanup(srv.Stop)
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "http://localhost/api/velero/backups/velero/nightly/"+artifact, nil))
			body := rec.Body.String()
			if rec.Code != http.StatusServiceUnavailable || !strings.Contains(body, "download destination") || strings.Contains(body, "secret") || strings.Contains(body, "169.254") {
				t.Fatalf("status %d: %s", rec.Code, body)
			}
		})
	}
}

func TestVeleroArtifactsGates(t *testing.T) {
	for _, artifact := range []string{"contents", "log"} {
		env := newAuthTestServer(t)
		perms := &auth.UserPermissions{AllowedNamespaces: []string{"velero"}}
		perms.SetCanI("get", veleroGroup, "backups", "velero", true)
		perms.SetCanI("get", veleroGroup, "restores", "velero", false)
		env.srv.permCache.Set("reader", nil, perms)
		for kind, want := range map[string]int{"restores": 403, "schedules": 400} {
			resp := env.authPost(t, "/api/velero/"+kind+"/velero/nightly/"+artifact, "reader", "", "")
			resp.Body.Close()
			if resp.StatusCode != want {
				t.Fatalf("%s/%s: %d", kind, artifact, resp.StatusCode)
			}
		}
		t.Run(artifact+" pin", func(t *testing.T) {
			pinNamespaceScope(t)
			resp := env.authPost(t, "/api/velero/backups/velero/nightly/"+artifact, "reader", "", "")
			defer resp.Body.Close()
			assertOutsidePin(t, resp, readBody(t, resp))
		})
	}
}

func TestVeleroArtifactsSharedListenerRejectsCrossSite(t *testing.T) {
	srv := New(Config{DevMode: true, ListenAddress: "0.0.0.0"})
	t.Cleanup(srv.Stop)
	for _, artifact := range []string{"contents", "log"} {
		req := httptest.NewRequest(http.MethodPost, "http://localhost/api/velero/backups/velero/nightly/"+artifact, nil)
		req.Header.Set("Origin", "https://untrusted.invalid")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s: %d %s", artifact, rec.Code, rec.Body.String())
		}
	}
}

func TestVeleroRestoreContentsReadsFailedEntriesAsWarnedOnASettledRestoreWithoutErrors(t *testing.T) {
	body := `{"v1/ConfigMap":["demo-copy/settings(failed)","demo-copy/fresh(created)"],"apps/v1/Deployment":["demo-copy/web(failed)"]}`
	for warnedOnly, want := range map[bool]map[string]int{
		false: {"failed": 2, "created": 1},
		true:  {"warned": 2, "created": 1},
	} {
		result, err := decodeVeleroContents(strings.NewReader(body), true, veleroContentsFilter{}, warnedOnly)
		if err != nil {
			t.Fatal(err)
		}
		if got := result.(veleroRunContents).Actions; !reflect.DeepEqual(got, want) {
			t.Fatalf("warnedOnly=%v: actions %v, want %v", warnedOnly, got, want)
		}
		filtered, err := decodeVeleroContents(strings.NewReader(body), true, veleroContentsFilter{action: "warned"}, warnedOnly)
		if err != nil {
			t.Fatal(err)
		}
		if got := filtered.(veleroRunContents).Matched; got != want["warned"] {
			t.Fatalf("warnedOnly=%v: warned filter matched %d, want %d", warnedOnly, got, want["warned"])
		}
	}
}

// Only a restore that has settled with no errors reads its failed entries as
// warned; the route asks Velero for the resource list alone.
func TestVeleroRestoreContentsRouteReadsTheRestoresOutcome(t *testing.T) {
	list := `{"v1/ConfigMap":["demo-copy/settings(failed)","demo-copy/fresh(created)"],"v1/Secret":["demo-copy/token(failed)"]}`
	store := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(gzipped(t, list))
	}))
	t.Cleanup(store.Close)
	for name, tc := range map[string]struct {
		status map[string]any
		want   map[string]int
	}{
		"in progress":         {map[string]any{"phase": "InProgress", "warnings": int64(2)}, map[string]int{"failed": 2, "created": 1}},
		"completed, errors":   {map[string]any{"phase": "PartiallyFailed", "warnings": int64(2), "errors": int64(1)}, map[string]int{"failed": 2, "created": 1}},
		"completed, no error": {map[string]any{"phase": "Completed", "warnings": int64(2)}, map[string]int{"warned": 2, "created": 1}},
	} {
		t.Run(name, func(t *testing.T) {
			restore := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "velero.io/v1", "kind": "Restore",
				"metadata": map[string]any{"name": "demo-copy", "namespace": "velero"},
				"status":   tc.status,
			}}
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{downloadRequestGVR: "DownloadRequestList", veleroRestoreGVR: "RestoreList"}, restore)
			var targets []string
			dyn.PrependReactor("create", "downloadrequests", func(a k8stesting.Action) (bool, runtime.Object, error) {
				obj := a.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
				target, _, _ := unstructured.NestedString(obj.Object, "spec", "target", "kind")
				targets = append(targets, target)
				obj.SetName("dr")
				return false, nil, nil
			})
			dyn.PrependReactor("get", "downloadrequests", func(a k8stesting.Action) (bool, runtime.Object, error) {
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
			srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "http://localhost/api/velero/restores/velero/demo-copy/contents", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			var got veleroRunContents
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Actions, tc.want) {
				t.Fatalf("actions %v, want %v", got.Actions, tc.want)
			}
			if !reflect.DeepEqual(targets, []string{"RestoreResourceList"}) {
				t.Fatalf("download requests %v, want the resource list alone", targets)
			}
		})
	}
}

var veleroRestoreGVR = schema.GroupVersionResource{Group: veleroGroup, Version: "v1", Resource: "restores"}

// answerVeleroRun makes the fake answer a get of any Backup or Restore with a
// run of that name in phase, as the artifact routes read the run first.
func answerVeleroRun(dyn *dynamicfake.FakeDynamicClient, phase string) {
	for resource, kind := range veleroRunKinds {
		dyn.PrependReactor("get", resource, func(a k8stesting.Action) (bool, runtime.Object, error) {
			return true, &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "velero.io/v1", "kind": kind,
				"metadata": map[string]any{"name": a.(k8stesting.GetAction).GetName(), "namespace": a.GetNamespace()},
				"status":   map[string]any{"phase": phase},
			}}, nil
		})
	}
}
