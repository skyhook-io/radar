// cmd/testserver is a standalone binary that starts a Radar server backed by
// a fake Kubernetes client with seed data.  It serves the embedded frontend
// and listens on a configurable port (default 9281).
//
// Usage:
//
//	go run ./cmd/testserver            # port 9281
//	go run ./cmd/testserver -port 9999
//
// Intended for Playwright / e2e tests — no real cluster required.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/server"
	"github.com/skyhook-io/radar/internal/static"
	"github.com/skyhook-io/radar/internal/timeline"
)

func main() {
	port := flag.Int("port", 9281, "port to listen on")
	flag.Parse()

	// Isolate config/settings writes to a temp directory so e2e tests
	// don't touch the developer's real ~/.radar/config.json.
	tmpHome, err := os.MkdirTemp("", "radar-testserver-*")
	if err != nil {
		log.Fatalf("Failed to create temp HOME: %v", err)
	}
	os.Setenv("HOME", tmpHome)
	defer os.RemoveAll(tmpHome)

	// --- Fake K8s client with seed data ---

	replicas := int32(2)
	deployUID := "deploy-uid-1234"
	rsUID := "rs-uid-5678"

	fakeClient := fake.NewClientset(
		// Namespaces
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "default"},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		},
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "kube-system"},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		},

		// Deployment
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "nginx",
				Namespace: "default",
				UID:       "deploy-uid-1234",
				Labels:    map[string]string{"app": "nginx"},
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"app": "nginx"},
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "nginx"}},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "nginx", Image: "nginx:1.25"}}},
				},
			},
			Status: appsv1.DeploymentStatus{Replicas: 2, ReadyReplicas: 2},
		},

		// ReplicaSet
		&appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "nginx-abc",
				Namespace: "default",
				UID:       "rs-uid-5678",
				Labels:    map[string]string{"app": "nginx"},
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Name:       "nginx",
					UID:        types.UID(deployUID),
					Controller: boolPtr(true),
				}},
			},
			Spec: appsv1.ReplicaSetSpec{
				Replicas: &replicas,
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"app": "nginx"},
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "nginx"}},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "nginx", Image: "nginx:1.25"}}},
				},
			},
			Status: appsv1.ReplicaSetStatus{Replicas: 2, ReadyReplicas: 2},
		},

		// Pods
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "nginx-abc-111",
				Namespace: "default",
				Labels:    map[string]string{"app": "nginx"},
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "apps/v1",
					Kind:       "ReplicaSet",
					Name:       "nginx-abc",
					UID:        types.UID(rsUID),
					Controller: boolPtr(true),
				}},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "nginx", Image: "nginx:1.25"}},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{
					Name: "nginx", Ready: true,
					State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
				}},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "nginx-abc-222",
				Namespace: "default",
				Labels:    map[string]string{"app": "nginx"},
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "apps/v1",
					Kind:       "ReplicaSet",
					Name:       "nginx-abc",
					UID:        types.UID(rsUID),
					Controller: boolPtr(true),
				}},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "nginx", Image: "nginx:1.25"}},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{
					Name: "nginx", Ready: true,
					State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
				}},
			},
		},

		// Service
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "nginx",
				Namespace: "default",
				Labels:    map[string]string{"app": "nginx"},
			},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{"app": "nginx"},
				Ports:    []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromInt(80)}},
			},
		},

		// Node
		&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "test-node-1"},
			Status: corev1.NodeStatus{
				Conditions: []corev1.NodeCondition{
					{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
				},
			},
		},
	)

	// --- Initialize subsystems ---

	if err := k8s.InitTestResourceCache(fakeClient); err != nil {
		log.Fatalf("InitTestResourceCache: %v", err)
	}

	k8s.SetConnectionStatus(k8s.ConnectionStatus{
		State:   k8s.StateConnected,
		Context: "fake-test",
	})

	if err := timeline.InitStore(timeline.DefaultStoreConfig()); err != nil {
		log.Fatalf("InitStore: %v", err)
	}

	// --- Start server ---

	effectiveCfg := &config.Config{Port: *port}

	srv := server.New(server.Config{
		Port:            *port,
		StaticFS:        static.FS,
		StaticRoot:      "dist",
		EffectiveConfig: effectiveCfg,
	})

	ready := make(chan struct{})
	go func() {
		if err := srv.StartWithReady(ready); err != nil {
			log.Fatalf("Server error: %v", err)
		}
	}()
	<-ready

	fmt.Fprintf(os.Stderr, "Test server ready on http://localhost:%d (fake k8s)\n", *port)

	// Wait for interrupt
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	srv.Stop()
	timeline.ResetStore()
	k8s.ResetTestState()
}

func boolPtr(b bool) *bool { return &b }
