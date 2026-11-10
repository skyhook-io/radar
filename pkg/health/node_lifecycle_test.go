package health

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
)

func TestNodeLifecycleCrossLanguage(t *testing.T) {
	raw, err := os.ReadFile("testdata/node_lifecycle.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Now     time.Time `json:"now"`
		Vectors []struct {
			Name string      `json:"name"`
			Node corev1.Node `json:"node"`
			NodeLifecycleState
		} `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, v := range fixture.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			got := NodeLifecycle(&v.Node, fixture.Now)
			got.StartedAt = got.StartedAt.UTC()
			v.StartedAt = v.StartedAt.UTC()
			gotJSON, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			wantJSON, err := json.Marshal(v.NodeLifecycleState)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(gotJSON, wantJSON) {
				t.Fatalf("got %s; want %s", gotJSON, wantJSON)
			}
		})
	}
}

func TestCountNodeFleetExclusive(t *testing.T) {
	raw, err := os.ReadFile("testdata/node_lifecycle.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Now     time.Time `json:"now"`
		Vectors []struct {
			Name string      `json:"name"`
			Node corev1.Node `json:"node"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	selected := map[string]bool{"failure-True": true, "cordoned": true, "autoscaler-Unknown": true, "preexisting-failure": true, "pressure-DiskPressure": true, "removal-age-30": true, "candidate-True": true}
	nodes := []*corev1.Node{}
	for _, vector := range fixture.Vectors {
		if selected[vector.Name] {
			nodes = append(nodes, &vector.Node)
		}
	}
	got := CountNodeFleet(nodes, fixture.Now)
	want := NodeFleetCounts{Total: 7, Ready: 2, NotReady: 1, Cordoned: 1, Removing: 3, RemovingUnhealthy: 2}
	if got != want {
		t.Fatalf("got %+v; want %+v", got, want)
	}
}
