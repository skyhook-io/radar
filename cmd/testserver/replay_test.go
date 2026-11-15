package main

import (
	"compress/gzip"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestReplayTypedAndDynamic(t *testing.T) {
	data, err := os.ReadFile("../../internal/clusterreplay/testdata/tiny.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), `"context_hash":`, `"failed_groups":["broken.io"],"skipped":[{"resource":"secrets","error":"forbidden"}],"context_hash":`, 1))
	path := filepath.Join(t.TempDir(), "capture.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	z := gzip.NewWriter(f)
	if _, err = z.Write(data); err != nil {
		t.Fatal(err)
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	s, err := loadReplay(path)
	if err != nil {
		t.Fatal(err)
	}
	admin := startReplayAdmin(0, s)
	defer admin.Close()
	status := httptest.NewRecorder()
	admin.Handler.ServeHTTP(status, httptest.NewRequest("GET", "/loadtest/status", nil))
	var body struct {
		FailedGroups []string `json:"failed_groups"`
		Skipped      []struct {
			Resource string
			Error    string
		} `json:"skipped"`
	}
	if err := json.Unmarshal(status.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.FailedGroups) != 1 || body.FailedGroups[0] != "broken.io" || len(body.Skipped) != 1 || body.Skipped[0].Resource != "secrets" || body.Skipped[0].Error != "forbidden" {
		t.Fatalf("capture gaps absent from status: %s", status.Body.String())
	}
	defer k8s.ResetTestState()
	defer k8s.ResetTestDynamicState()
	if err = k8s.InitTestResourceCache(s.client); err != nil {
		t.Fatal(err)
	}
	if err = s.start(); err != nil {
		t.Fatal(err)
	}
	if len(s.unsupported) != 0 {
		t.Fatal(s.unsupported)
	}
	if got := k8s.GetResourceCache().GetKindObjectCounts()["Pod"]; got != 1 {
		t.Fatalf("pods=%d", got)
	}
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	items, err := k8s.GetDynamicResourceCache().List(gvr, "default")
	if err != nil || len(items) != 2 {
		t.Fatalf("widgets=%d err=%v", len(items), err)
	}
	discovery := k8s.GetResourceDiscovery()
	if _, ok := discovery.GetGVRWithGroup("Widget", "example.io"); !ok {
		t.Fatal("missing Widget discovery")
	}
	if err = discovery.Refresh(); err != nil {
		t.Fatal(err)
	}
	if _, ok := discovery.GetGVRWithGroup("Widget", "example.io"); !ok {
		t.Fatal("catalog lost on refresh")
	}
}
