package main

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestReplayTypedAndDynamic(t *testing.T) {
	data, err := os.ReadFile("../../internal/clusterreplay/testdata/tiny.ndjson")
	if err != nil {
		t.Fatal(err)
	}
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
