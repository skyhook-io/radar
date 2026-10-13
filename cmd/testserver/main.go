// cmd/testserver is a standalone binary that starts a Radar server backed by
// a fake Kubernetes client with seed data.  It serves the embedded frontend
// and listens on a configurable port (default 9281).
//
// Usage:
//
//	go run -tags withoutebpf ./cmd/testserver                 # port 9281, small nginx fixture
//	go run -tags withoutebpf ./cmd/testserver -port 9999
//	go run -tags withoutebpf ./cmd/testserver -pods 50000     # load-test population (see below)
//
// Load-test mode (-pods > 0): instead of the small nginx fixture, the server
// seeds a topology-realistic synthetic population (Deployments -> ReplicaSets
// -> Pods with matching Services, spread across nodes and namespaces) so
// Radar's UI can be driven at high object counts with no real cluster. The
// count is live-controllable via a small admin listener (default port+1):
//
//	curl -XPOST localhost:9282/loadtest/scale -d '{"pods":10000}'
//	curl localhost:9282/loadtest/status
//
// Intended for Playwright / e2e tests and UI load testing — no real cluster
// required.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/loadtest"
	"github.com/skyhook-io/radar/internal/server"
	"github.com/skyhook-io/radar/internal/static"
	"github.com/skyhook-io/radar/internal/timeline"
)

func main() {
	port := flag.Int("port", 9281, "port to listen on")
	pods := flag.Int("pods", 0, "load-test: number of synthetic pods to seed (0 = small nginx fixture)")
	nodes := flag.Int("nodes", 0, "load-test: number of fake nodes (0 = default)")
	namespaces := flag.Int("namespaces", 0, "load-test: number of namespaces to spread apps across (0 = default)")
	podsPerApp := flag.Int("pods-per-app", 0, "load-test: pods per Deployment/ReplicaSet (0 = default)")
	adminPort := flag.Int("admin-port", 0, "load-test: admin listener port for live scaling (0 = <port>+1)")
	censusProfile := flag.String("census", "", "load-test: seed a named census profile (e.g. large-eks) instead of -pods")
	censusScale := flag.Float64("census-scale", 1.0, "load-test: scale factor applied to -census counts")
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

	var (
		fakeClient *fake.Clientset
		gen        *loadtest.Generator
	)
	if *censusProfile != "" {
		c, ok := loadtest.Profiles[*censusProfile]
		if !ok {
			log.Fatalf("unknown -census profile %q", *censusProfile)
		}
		c = c.Scale(*censusScale)
		start := time.Now()
		objs := loadtest.CensusObjects(c, "registry.example/app:v1")
		fakeClient = fake.NewClientset(objs...)
		fmt.Fprintf(os.Stderr, "Seeding census %q scale %.3f: %d objects (%s)\n",
			*censusProfile, *censusScale, len(objs), time.Since(start).Round(time.Millisecond))
	} else if *pods > 0 {
		gen = loadtest.New(loadtest.Config{
			Pods: *pods, Nodes: *nodes, Namespaces: *namespaces, PodsPerApp: *podsPerApp,
		})
		start := time.Now()
		objs := gen.SeedObjects()
		fakeClient = fake.NewClientset(objs...)
		cfg := gen.Config()
		fmt.Fprintf(os.Stderr, "Seeding %d pods across %d apps / %d namespaces / %d nodes (%d objects, %s)\n",
			cfg.Pods, gen.AppCount(), cfg.Namespaces, cfg.Nodes, len(objs), time.Since(start).Round(time.Millisecond))
	} else {
		fakeClient = fake.NewClientset(seedObjects()...)
	}

	// --- Initialize subsystems ---

	// The census path measures startup, which only exists when the cache has the
	// live deferred/critical split; InitTestResourceCache syncs everything at once.
	if *censusProfile != "" {
		if err := k8s.InitLoadTestResourceCache(fakeClient); err != nil {
			log.Fatalf("InitLoadTestResourceCache: %v", err)
		}
	} else if err := k8s.InitTestResourceCache(fakeClient); err != nil {
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
		ListenAddress:   server.DefaultListenAddress,
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

	var adminSrv *http.Server
	if gen != nil {
		ap := *adminPort
		if ap == 0 {
			ap = *port + 1
		}
		adminSrv = startAdminServer(ap, gen, fakeClient)
	}

	// Wait for interrupt
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	if adminSrv != nil {
		_ = adminSrv.Close()
	}
	srv.Stop()
	timeline.ResetStore()
	k8s.ResetTestState()
}

// informerCount reports the object count Radar's informer cache currently holds
// for a Kind ("Pod", "Deployment", …) — the count the UI sees. It is the drain
// signal the scaler paces every watched kind against, and the source of truth
// for the post-scale convergence check.
func informerCount(kind string) int {
	rc := k8s.GetResourceCache()
	if rc == nil {
		return 0
	}
	return rc.GetKindObjectCounts()[kind]
}

func startAdminServer(port int, gen *loadtest.Generator, client kubernetes.Interface) *http.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("/loadtest/status", func(w http.ResponseWriter, r *http.Request) {
		cfg := gen.Config()
		writeJSON(w, http.StatusOK, map[string]any{
			"pods":         informerCount("Pod"),
			"target":       gen.Current(),
			"nodes":        cfg.Nodes,
			"namespaces":   cfg.Namespaces,
			"pods_per_app": cfg.PodsPerApp,
		})
	})

	mux.HandleFunc("/loadtest/scale", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
			return
		}
		var body struct {
			Pods *int `json:"pods"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Pods == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must be {\"pods\": N}"})
			return
		}
		res, err := gen.ScaleTo(r.Context(), client, *body.Pods, informerCount)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, res)
	})

	// Bind synchronously so a failed bind (e.g. port already in use) is surfaced
	// here rather than swallowed in a goroutine after we've claimed the admin
	// endpoint is ready.
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("[loadtest] admin listener unavailable on %s: %v (live scaling disabled)", addr, err)
		return nil
	}
	adminSrv := &http.Server{Handler: mux}
	go func() {
		if err := adminSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("[loadtest] admin server error: %v", err)
		}
	}()
	fmt.Fprintf(os.Stderr, "Load-test admin on http://localhost:%d  (POST /loadtest/scale {\"pods\":N}, GET /loadtest/status)\n", port)
	return adminSrv
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// seedObjects returns the default small nginx fixture used when -pods is unset,
// preserving the fixture Playwright and manual smoke runs expect.
func seedObjects() []runtime.Object {
	replicas := int32(2)
	deployUID := "deploy-uid-1234"
	rsUID := "rs-uid-5678"

	return []runtime.Object{
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "default"},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		},
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "kube-system"},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		},

		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "nginx",
				Namespace: "default",
				UID:       "deploy-uid-1234",
				Labels:    map[string]string{"app": "nginx"},
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "nginx"}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "nginx"}},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "nginx", Image: "nginx:1.25"}}},
				},
			},
			Status: appsv1.DeploymentStatus{Replicas: 2, ReadyReplicas: 2},
		},

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
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "nginx"}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "nginx"}},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "nginx", Image: "nginx:1.25"}}},
				},
			},
			Status: appsv1.ReplicaSetStatus{Replicas: 2, ReadyReplicas: 2},
		},

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

		&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "test-node-1"},
			Status: corev1.NodeStatus{
				Conditions: []corev1.NodeCondition{
					{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
				},
			},
		},
	}
}

func boolPtr(b bool) *bool { return &b }
