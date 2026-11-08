package server

import (
	"context"
	"reflect"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/internal/podlogs"
)

func TestStopDeselectedLogStreamsStopsOnlyRunningDroppedContainers(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "api-0", UID: "uid-1"},
		Status: corev1.PodStatus{
			InitContainerStatuses: []corev1.ContainerStatus{
				{Name: "istio-proxy", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
				{Name: "migrate", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{}}},
			},
			ContainerStatuses: []corev1.ContainerStatus{{Name: "app", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}},
		},
	}
	var active sync.Map
	contexts := map[string]context.Context{}
	add := func(key string, uid string) {
		ctx, cancel := context.WithCancel(context.Background())
		contexts[key] = ctx
		active.Store(key, &podLogStreamHandle{uid: k8stypes.UID(uid), cancel: cancel})
	}
	add("api-0/istio-proxy", "uid-1")
	add("api-0/migrate", "uid-1")
	add("api-0/app", "uid-1")
	add("api-1/istio-proxy", "uid-2")

	stopped := stopDeselectedLogStreams(&active, pod, []string{"app"})
	if !reflect.DeepEqual(stopped, []string{"api-0/istio-proxy"}) {
		t.Fatalf("stopped=%v", stopped)
	}
	if contexts["api-0/istio-proxy"].Err() == nil {
		t.Fatal("dropped running sidecar is still followed")
	}
	for _, key := range []string{"api-0/migrate", "api-0/app", "api-1/istio-proxy"} {
		if contexts[key].Err() != nil {
			t.Fatalf("%s was stopped", key)
		}
		if _, ok := active.Load(key); !ok {
			t.Fatalf("%s left the active set", key)
		}
	}
}

func TestUnavailableSourcesDropForgetsAWarning(t *testing.T) {
	sources := podlogs.UnavailableSources{}
	sources.Add("api-0/istio-proxy", 1, "container is waiting")
	if !sources.Drop("api-0/istio-proxy") || sources.Notice() != "" {
		t.Fatalf("drop left notice %q", sources.Notice())
	}
	if sources.Drop("api-0/istio-proxy") {
		t.Fatal("dropping an absent source reported a change")
	}
}
