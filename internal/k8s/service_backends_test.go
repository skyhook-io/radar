package k8s

import (
	"reflect"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/pkg/k8score"
)

type backendPodOpt func(*corev1.Pod)

func backendPod(name, ownerKind, ownerName string, containers []corev1.Container, opts ...backendPodOpt) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", Labels: map[string]string{"tier": "web"}},
		Spec:       corev1.PodSpec{Containers: containers},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
	if ownerKind != "" {
		controller := true
		apiVersion := "apps/v1"
		if ownerKind == "Job" {
			apiVersion = "batch/v1"
		}
		pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: apiVersion, Kind: ownerKind, Name: ownerName, Controller: &controller}}
	}
	for _, opt := range opts {
		opt(pod)
	}
	return pod
}

func notReady(p *corev1.Pod) {
	p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}}
}

func container(name, image string, ports ...int32) corev1.Container {
	c := corev1.Container{Name: name, Image: image}
	for _, p := range ports {
		c.Ports = append(c.Ports, corev1.ContainerPort{ContainerPort: p})
	}
	return c
}

func backendService(targetPorts ...intstr.IntOrString) *corev1.Service {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "frontend", Namespace: "shop"},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"tier": "web"}, ClusterIP: "10.0.0.10"},
	}
	for i, tp := range targetPorts {
		svc.Spec.Ports = append(svc.Spec.Ports, corev1.ServicePort{Port: int32(80 + i), TargetPort: tp})
	}
	return svc
}

func TestAnalyzeServiceBackends(t *testing.T) {
	p5000 := intstr.FromInt32(5000)
	istio := container("istio-proxy", "docker.io/istio/proxyv2:1.22", 15090)
	tests := []struct {
		name          string
		svc           *corev1.Service
		pods          []*corev1.Pod
		wantWorkloads int
		wantDifferent bool
		wantApps      [][]string
		wantGapPorts  []int32
		wantProblem   string
	}{
		{
			name: "same image, second workload declares another port",
			svc:  backendService(p5000),
			pods: []*corev1.Pod{
				backendPod("frontend-1", "StatefulSet", "frontend", []corev1.Container{container("frontend", "hotel/app:v1", 5000)}),
				backendPod("search-1", "StatefulSet", "search", []corev1.Container{container("search", "hotel/app:v1", 8082)}),
			},
			wantWorkloads: 2,
			wantDifferent: true,
			wantGapPorts:  []int32{5000},
			wantProblem:   "Service frontend routes to 2 different applications (frontend, search); search's pods don't declare port 5000",
		},
		{
			name: "different repositories and a port gap",
			svc:  backendService(p5000),
			pods: []*corev1.Pod{
				backendPod("frontend-1", "StatefulSet", "frontend", []corev1.Container{container("frontend", "acme/frontend:1.0", 5000)}),
				backendPod("search-1", "StatefulSet", "search", []corev1.Container{container("search", "acme/search:2.0", 8082)}),
			},
			wantWorkloads: 2,
			wantDifferent: true,
			wantApps:      [][]string{{"frontend"}, {"search"}},
			wantGapPorts:  []int32{5000},
			wantProblem:   "Service frontend routes to 2 different applications (frontend, search); search's pods don't declare port 5000",
		},
		{
			name: "different repositories, ports not declared",
			svc:  backendService(p5000),
			pods: []*corev1.Pod{
				backendPod("frontend-1", "StatefulSet", "frontend", []corev1.Container{container("frontend", "acme/frontend:1.0")}),
				backendPod("search-1", "StatefulSet", "search", []corev1.Container{container("search", "acme/search:2.0")}),
			},
			wantWorkloads: 2,
			wantDifferent: true,
			wantApps:      [][]string{{"frontend"}, {"search"}},
			wantProblem:   "Service frontend routes to 2 different applications (frontend, search)",
		},
		{
			name: "canary: same repository, different tags",
			svc:  backendService(p5000),
			pods: []*corev1.Pod{
				backendPod("web-stable-1", "StatefulSet", "web-stable", []corev1.Container{container("web", "acme/web:1.0", 5000)}),
				backendPod("web-stable-2", "StatefulSet", "web-stable", []corev1.Container{container("web", "acme/web:1.0", 5000)}),
				backendPod("web-canary-1", "StatefulSet", "web-canary", []corev1.Container{container("web", "acme/web:1.1", 5000)}),
			},
			wantWorkloads: 2,
		},
		{
			name: "canary that adds a port still serves the target port",
			svc:  backendService(p5000),
			pods: []*corev1.Pod{
				backendPod("web-stable-1", "StatefulSet", "web-stable", []corev1.Container{container("web", "acme/web:1.0", 5000)}),
				backendPod("web-canary-1", "StatefulSet", "web-canary", []corev1.Container{container("web", "acme/web:1.1", 5000, 9090)}),
			},
			wantWorkloads: 2,
		},
		{
			name: "single workload",
			svc:  backendService(p5000),
			pods: []*corev1.Pod{
				backendPod("web-1", "StatefulSet", "web", []corev1.Container{container("web", "acme/web:1.0", 8080)}),
				backendPod("web-2", "StatefulSet", "web", []corev1.Container{container("web", "acme/web:1.0", 8080)}),
			},
			wantWorkloads: 1,
		},
		{
			name: "workload that declares no ports is not evidence of a gap",
			svc:  backendService(p5000),
			pods: []*corev1.Pod{
				backendPod("api-1", "StatefulSet", "api", []corev1.Container{container("api", "acme/app:1", 5000)}),
				backendPod("api-v2-1", "StatefulSet", "api-v2", []corev1.Container{container("api-v2", "acme/app:1")}),
			},
			wantWorkloads: 2,
		},
		{
			name: "namespace-wide mesh sidecar is not an application port",
			svc:  backendService(p5000),
			pods: []*corev1.Pod{
				backendPod("api-1", "StatefulSet", "api", []corev1.Container{container("api", "acme/app:1", 5000), istio}),
				backendPod("api-v2-1", "StatefulSet", "api-v2", []corev1.Container{container("api-v2", "acme/app:1"), istio}),
			},
			wantWorkloads: 2,
		},
		{
			name: "mesh sidecar injected into only one workload is not an application port",
			svc:  backendService(p5000),
			pods: []*corev1.Pod{
				backendPod("api-1", "StatefulSet", "api", []corev1.Container{container("api", "acme/app:1", 5000)}),
				backendPod("api-v2-1", "StatefulSet", "api-v2", []corev1.Container{container("api-v2", "acme/app:1"), istio}),
			},
			wantWorkloads: 2,
		},
		{
			name: "mesh sidecar image does not make workloads different applications",
			svc:  backendService(p5000),
			pods: []*corev1.Pod{
				backendPod("web-stable-1", "StatefulSet", "web-stable", []corev1.Container{container("web", "acme/web:1.0", 5000), istio}),
				backendPod("web-canary-1", "StatefulSet", "web-canary", []corev1.Container{container("web", "acme/web:1.1", 5000)}),
			},
			wantWorkloads: 2,
		},
		{
			name: "same image as web and worker, worker declares only a metrics port",
			svc:  backendService(intstr.FromInt32(8080)),
			pods: []*corev1.Pod{
				backendPod("web-1", "StatefulSet", "web", []corev1.Container{container("app", "acme/app:1", 8080)}),
				backendPod("worker-1", "StatefulSet", "worker", []corev1.Container{container("app", "acme/app:1", 9090)}),
			},
			wantWorkloads: 2,
			wantDifferent: true,
			wantGapPorts:  []int32{8080},
			wantProblem:   "Service frontend routes to 2 different applications (web, worker); worker's pods don't declare port 8080",
		},
		{
			name: "named targetPort is resolved by Kubernetes, not judged",
			svc:  backendService(intstr.FromString("http")),
			pods: []*corev1.Pod{
				backendPod("web-1", "StatefulSet", "web", []corev1.Container{{Name: "web", Image: "acme/web:1", Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}}}}),
				backendPod("worker-1", "StatefulSet", "worker", []corev1.Container{{Name: "worker", Image: "acme/web:1", Ports: []corev1.ContainerPort{{Name: "metrics", ContainerPort: 9090}}}}),
			},
			wantWorkloads: 2,
		},
		{
			name: "workload that resolves no named port receives no traffic",
			svc:  backendService(intstr.FromString("http")),
			pods: []*corev1.Pod{
				backendPod("web-1", "StatefulSet", "web", []corev1.Container{{Name: "web", Image: "acme/web:1", Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}}}}),
				backendPod("db-1", "StatefulSet", "db", []corev1.Container{container("db", "postgres:16", 5432)}),
			},
			wantWorkloads: 2,
		},
		{
			name: "unset targetPort defaults to the Service port, gap reported per port",
			svc: func() *corev1.Service {
				svc := backendService()
				svc.Spec.Ports = []corev1.ServicePort{{Port: 8080}, {Port: 9000, TargetPort: intstr.FromInt32(9000)}}
				return svc
			}(),
			pods: []*corev1.Pod{
				backendPod("web-1", "StatefulSet", "web", []corev1.Container{container("web", "acme/web:1", 8080, 9000)}),
				backendPod("admin-1", "StatefulSet", "admin", []corev1.Container{container("admin", "acme/web:1", 9000)}),
			},
			wantWorkloads: 2,
			wantDifferent: true,
			wantGapPorts:  []int32{8080},
			wantProblem:   "Service frontend routes to 2 different applications (web, admin); admin's pods don't declare port 8080",
		},
		{
			name: "port declared over another protocol does not count",
			svc: func() *corev1.Service {
				svc := backendService()
				svc.Spec.Ports = []corev1.ServicePort{{Port: 53, Protocol: corev1.ProtocolUDP}}
				return svc
			}(),
			pods: []*corev1.Pod{
				backendPod("dns-1", "StatefulSet", "dns", []corev1.Container{{Name: "dns", Image: "acme/dns:1", Ports: []corev1.ContainerPort{{ContainerPort: 53, Protocol: corev1.ProtocolUDP}}}}),
				backendPod("dns-tcp-1", "StatefulSet", "dns-tcp", []corev1.Container{container("dns-tcp", "acme/dns:1", 53)}),
			},
			wantWorkloads: 2,
			wantDifferent: true,
			wantGapPorts:  []int32{53},
			wantProblem:   "Service frontend routes to 2 different applications (dns, dns-tcp); dns-tcp's pods don't declare port 53/UDP",
		},
		{
			name: "headless Service is grouped but not judged",
			svc: func() *corev1.Service {
				svc := backendService(p5000)
				svc.Spec.ClusterIP = corev1.ClusterIPNone
				return svc
			}(),
			pods: []*corev1.Pod{
				backendPod("frontend-1", "StatefulSet", "frontend", []corev1.Container{container("frontend", "acme/frontend:1.0", 5000)}),
				backendPod("search-1", "StatefulSet", "search", []corev1.Container{container("search", "acme/search:2.0", 8082)}),
			},
			wantWorkloads: 2,
		},
		{
			name: "not-ready pods of the other application receive no traffic",
			svc:  backendService(p5000),
			pods: []*corev1.Pod{
				backendPod("frontend-1", "StatefulSet", "frontend", []corev1.Container{container("frontend", "acme/frontend:1.0", 5000)}),
				backendPod("search-1", "StatefulSet", "search", []corev1.Container{container("search", "acme/search:2.0", 8082)}, notReady),
			},
			wantWorkloads: 2,
		},
		{
			name: "publishNotReadyAddresses routes to not-ready pods",
			svc: func() *corev1.Service {
				svc := backendService(p5000)
				svc.Spec.PublishNotReadyAddresses = true
				return svc
			}(),
			pods: []*corev1.Pod{
				backendPod("frontend-1", "StatefulSet", "frontend", []corev1.Container{container("frontend", "acme/frontend:1.0", 5000)}),
				backendPod("search-1", "StatefulSet", "search", []corev1.Container{container("search", "acme/search:2.0", 8082)}, notReady),
			},
			wantWorkloads: 2,
			wantDifferent: true,
			wantApps:      [][]string{{"frontend"}, {"search"}},
			wantGapPorts:  []int32{5000},
		},
		{
			name: "terminating pods receive no traffic",
			svc:  backendService(p5000),
			pods: []*corev1.Pod{
				backendPod("frontend-1", "StatefulSet", "frontend", []corev1.Container{container("frontend", "acme/frontend:1.0", 5000)}),
				backendPod("search-1", "StatefulSet", "search", []corev1.Container{container("search", "acme/search:2.0", 8082)}, func(p *corev1.Pod) {
					now := metav1.Now()
					p.DeletionTimestamp = &now
				}),
			},
			wantWorkloads: 2,
		},
		{
			name: "finished pods are not backends",
			svc:  backendService(p5000),
			pods: []*corev1.Pod{
				backendPod("frontend-1", "StatefulSet", "frontend", []corev1.Container{container("frontend", "acme/frontend:1.0", 5000)}),
				backendPod("migrate-1", "Job", "migrate", []corev1.Container{container("migrate", "acme/migrate:1")}, func(p *corev1.Pod) { p.Status.Phase = corev1.PodFailed }),
			},
			wantWorkloads: 1,
		},
		{
			name: "bare pod with another image is its own workload",
			svc:  backendService(p5000),
			pods: []*corev1.Pod{
				backendPod("frontend-1", "StatefulSet", "frontend", []corev1.Container{container("frontend", "acme/frontend:1.0", 5000)}),
				backendPod("debug", "", "", []corev1.Container{container("shell", "busybox:1.36")}),
			},
			wantWorkloads: 2,
			wantDifferent: true,
			wantApps:      [][]string{{"frontend"}, {"debug"}},
		},
		{
			name: "Docker Hub spellings of one repository compare equal",
			svc:  backendService(intstr.FromInt32(80)),
			pods: []*corev1.Pod{
				backendPod("a-1", "StatefulSet", "a", []corev1.Container{container("nginx", "nginx:1.27", 80)}),
				backendPod("b-1", "StatefulSet", "b", []corev1.Container{container("web", "docker.io/library/nginx@sha256:abc", 80)}),
			},
			wantWorkloads: 2,
		},
		{
			name: "one application with different log-shipper sidecars",
			svc:  backendService(p5000),
			pods: []*corev1.Pod{
				backendPod("web-stable-1", "StatefulSet", "web-stable", []corev1.Container{container("web", "acme/web:1.0", 5000), container("logs", "fluent/fluent-bit:3.0", 2020)}),
				backendPod("web-canary-1", "StatefulSet", "web-canary", []corev1.Container{container("web", "acme/web:1.1", 5000), container("logs", "timberio/vector:0.40", 8686)}),
			},
			wantWorkloads: 2,
		},
		{
			name: "container order does not matter",
			svc:  backendService(p5000),
			pods: []*corev1.Pod{
				backendPod("web-stable-1", "StatefulSet", "web-stable", []corev1.Container{container("logs", "fluent/fluent-bit:3.0", 2020), container("web", "acme/web:1.0", 5000)}),
				backendPod("web-canary-1", "StatefulSet", "web-canary", []corev1.Container{container("web", "acme/web:1.1", 5000), container("logs", "timberio/vector:0.40", 8686)}),
			},
			wantWorkloads: 2,
		},
		{
			name: "a sidecar every workload runs hides the image signal but not the port signal",
			svc:  backendService(p5000),
			pods: []*corev1.Pod{
				backendPod("frontend-1", "StatefulSet", "frontend", []corev1.Container{container("frontend", "acme/frontend:1.0", 5000), container("vault-agent", "hashicorp/vault:1.17")}),
				backendPod("search-1", "StatefulSet", "search", []corev1.Container{container("search", "acme/search:2.0", 8082), container("vault-agent", "hashicorp/vault:1.17")}),
			},
			wantWorkloads: 2,
			wantDifferent: true,
			wantGapPorts:  []int32{5000},
		},
		{
			name: "named port declared over another protocol does not resolve",
			svc: func() *corev1.Service {
				svc := backendService(intstr.FromString("dns"))
				return svc
			}(),
			pods: []*corev1.Pod{
				backendPod("dns-1", "StatefulSet", "dns", []corev1.Container{{Name: "dns", Image: "acme/dns:1", Ports: []corev1.ContainerPort{{Name: "dns", ContainerPort: 53}}}}),
				backendPod("other-1", "StatefulSet", "other", []corev1.Container{{Name: "other", Image: "acme/other:1", Ports: []corev1.ContainerPort{{Name: "dns", ContainerPort: 53, Protocol: corev1.ProtocolUDP}}}}),
			},
			wantWorkloads: 2,
		},
		{
			name: "named port on a native sidecar resolves",
			svc:  backendService(intstr.FromString("http")),
			pods: []*corev1.Pod{
				backendPod("web-1", "StatefulSet", "web", []corev1.Container{{Name: "web", Image: "acme/web:1", Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}}}}),
				backendPod("proxy-1", "StatefulSet", "proxy", []corev1.Container{container("app", "acme/other:1")}, func(p *corev1.Pod) {
					always := corev1.ContainerRestartPolicyAlways
					p.Spec.InitContainers = []corev1.Container{{Name: "auth", Image: "acme/auth:1", RestartPolicy: &always, Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 4180}}}}
				}),
			},
			wantWorkloads: 2,
			wantDifferent: true,
			wantApps:      [][]string{{"proxy"}, {"web"}},
		},
		{
			name: "publishNotReadyAddresses publishes terminating pods",
			svc: func() *corev1.Service {
				svc := backendService(p5000)
				svc.Spec.PublishNotReadyAddresses = true
				return svc
			}(),
			pods: []*corev1.Pod{
				backendPod("frontend-1", "StatefulSet", "frontend", []corev1.Container{container("frontend", "acme/frontend:1.0", 5000)}),
				backendPod("search-1", "StatefulSet", "search", []corev1.Container{container("search", "acme/search:2.0")}, func(p *corev1.Pod) {
					now := metav1.Now()
					p.DeletionTimestamp = &now
				}),
			},
			wantWorkloads: 2,
			wantDifferent: true,
			wantApps:      [][]string{{"frontend"}, {"search"}},
		},
		{
			name: "stable and canary of one app plus an unrelated app",
			svc:  backendService(p5000),
			pods: []*corev1.Pod{
				backendPod("web-stable-1", "StatefulSet", "web-stable", []corev1.Container{container("web", "acme/web:1.0", 5000)}),
				backendPod("web-stable-2", "StatefulSet", "web-stable", []corev1.Container{container("web", "acme/web:1.0", 5000)}),
				backendPod("web-canary-1", "StatefulSet", "web-canary", []corev1.Container{container("web", "acme/web:1.1", 5000)}),
				backendPod("search-1", "StatefulSet", "search", []corev1.Container{container("search", "acme/search:2.0")}),
			},
			wantWorkloads: 3,
			wantDifferent: true,
			wantApps:      [][]string{{"web-stable", "web-canary"}, {"search"}},
			wantProblem:   "Service frontend routes to 2 different applications (web-stable + web-canary, search)",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AnalyzeServiceBackends(nil, tc.svc, tc.pods)
			if len(got.Workloads) != tc.wantWorkloads {
				t.Fatalf("workloads = %+v, want %d", got.Workloads, tc.wantWorkloads)
			}
			if got.DifferentApplications() != tc.wantDifferent {
				t.Fatalf("DifferentApplications = %v, want %v (apps %v, gaps %+v)", got.DifferentApplications(), tc.wantDifferent, got.Applications, got.PortGaps)
			}
			if tc.wantApps != nil && !reflect.DeepEqual(got.Applications, tc.wantApps) {
				t.Errorf("applications = %v, want %v", got.Applications, tc.wantApps)
			}
			var gapPorts []int32
			for _, g := range got.PortGaps {
				gapPorts = append(gapPorts, g.Port)
			}
			if !reflect.DeepEqual(gapPorts, tc.wantGapPorts) {
				t.Errorf("gap ports = %v, want %v", gapPorts, tc.wantGapPorts)
			}
			if tc.wantProblem != "" {
				if p := got.Problem(tc.svc.Name); p != tc.wantProblem {
					t.Errorf("problem = %q\n want %q", p, tc.wantProblem)
				}
			}
		})
	}
}

func TestServiceBackendsSummary(t *testing.T) {
	b := ServiceBackends{Workloads: []ServiceBackendWorkload{{Kind: "Deployment", Name: "frontend", Pods: 2}, {Kind: "Deployment", Name: "search", Pods: 1}}}
	if got, want := b.Summary(), "3 pods from 2 workloads: frontend (2), search (1)"; got != want {
		t.Fatalf("Summary = %q, want %q", got, want)
	}
	for i := 0; i < 5; i++ {
		b.Workloads = append(b.Workloads, ServiceBackendWorkload{Kind: "Pod", Name: "p" + string(rune('a'+i)), Pods: 1})
	}
	if got := b.Summary(); !strings.HasSuffix(got, "pc (1), 2 more") {
		t.Fatalf("Summary should cap the list: %q", got)
	}
}

// A ReplicaSet resolves to the Deployment that owns it, so two revisions of one
// Deployment mid-rollout are one workload; a ReplicaSet that isn't cached stays
// itself rather than guessing a Deployment from its name.
func TestPodWorkloadResolvesReplicaSetOwner(t *testing.T) {
	controller := true
	rs := func(name, deployment string) *appsv1.ReplicaSet {
		return &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "shop",
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: deployment, Controller: &controller}},
		}}
	}
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:        fake.NewClientset(rs("web-6d4cf56db6", "web"), rs("web-7f9b8c5d44", "web")),
		ResourceTypes: map[string]bool{k8score.ReplicaSets: true},
		DeferredTypes: map[string]bool{},
	})
	if err != nil {
		t.Fatalf("NewResourceCache: %v", err)
	}
	t.Cleanup(core.Stop)
	cache := &ResourceCache{ResourceCache: core}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if l := cache.ReplicaSets(); l != nil {
			if got, _ := l.List(labels.Everything()); len(got) == 2 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("replicasets never synced")
		}
		time.Sleep(10 * time.Millisecond)
	}

	web := []corev1.Container{container("web", "acme/web:1", 8080)}
	got := AnalyzeServiceBackends(cache, backendService(intstr.FromInt32(8080)), []*corev1.Pod{
		backendPod("web-6d4cf56db6-a", "ReplicaSet", "web-6d4cf56db6", web),
		backendPod("web-7f9b8c5d44-b", "ReplicaSet", "web-7f9b8c5d44", web),
	})
	if len(got.Workloads) != 1 || got.Workloads[0].Kind != "Deployment" || got.Workloads[0].Name != "web" || got.Workloads[0].Group != "apps" || got.Workloads[0].Pods != 2 {
		t.Fatalf("workloads = %+v, want one Deployment web with 2 pods", got.Workloads)
	}

	owner, ok := PodWorkload(cache, backendPod("api-5c6b7d8e9f-c", "ReplicaSet", "api-5c6b7d8e9f", web))
	if !ok || owner.Kind != "ReplicaSet" || owner.Name != "api-5c6b7d8e9f" {
		t.Fatalf("uncached ReplicaSet = %+v, want the ReplicaSet itself", owner)
	}
	if _, ok := PodWorkload(cache, backendPod("debug", "", "", web)); ok {
		t.Fatal("a Pod with no controller has no workload")
	}
}

func TestDetectServiceBackendProblemsMixedBackends(t *testing.T) {
	now := time.Date(2026, time.October, 17, 12, 0, 0, 0, time.UTC)
	svc := backendService(intstr.FromInt32(5000))
	svc.CreationTimestamp = metav1.NewTime(now.Add(-time.Hour))
	frontend := backendPod("frontend-1", "StatefulSet", "frontend", []corev1.Container{container("frontend", "hotel/app:v1", 5000)})
	search := backendPod("search-1", "StatefulSet", "search", []corev1.Container{container("search", "hotel/app:v1", 8082)})

	got := detectServiceBackendProblems(nil, svc, []*corev1.Pod{frontend, search}, now)
	if len(got) != 1 {
		t.Fatalf("detections = %+v, want one", got)
	}
	d := got[0]
	if d.Severity != "warning" || d.Reason != ServiceMixedBackendsReason || d.Fingerprint != ServiceMixedBackendsFingerprint {
		t.Fatalf("detection = %+v", d)
	}
	if want := "Service frontend routes to 2 different applications (frontend, search); search's pods don't declare port 5000"; d.Message != want {
		t.Fatalf("message = %q, want %q", d.Message, want)
	}
	if want := "The selector matches pods of frontend and search, so requests are spread across all of them; search's pods don't declare port 5000, so requests that reach them may fail."; d.Cause != want {
		t.Fatalf("cause = %q, want %q", d.Cause, want)
	}
	if d.Degraded == nil || strings.Contains(d.Degraded.Message, "search") || strings.Contains(d.Degraded.Cause, "search") {
		t.Fatalf("degraded finding must not name the workloads: %+v", d.Degraded)
	}
	if len(d.RequiredReads) != 1 || d.RequiredReads[0].Resource != "pods" || d.RequiredReads[0].Namespace != "shop" {
		t.Fatalf("required reads = %+v", d.RequiredReads)
	}

	canary := backendPod("frontend-canary-1", "StatefulSet", "frontend-canary", []corev1.Container{container("frontend", "hotel/app:v2", 5000)})
	if got := detectServiceBackendProblems(nil, svc, []*corev1.Pod{frontend, canary}, now); len(got) != 0 {
		t.Fatalf("same-image canary must not raise an issue: %+v", got)
	}
}

func TestServiceBackendsCauseWithoutPortGap(t *testing.T) {
	b := ServiceBackends{
		Workloads:    []ServiceBackendWorkload{{Name: "web-stable"}, {Name: "web-canary"}, {Name: "search"}},
		Applications: [][]string{{"web-stable", "web-canary"}, {"search"}},
	}
	if got, want := b.Cause(), "The selector matches pods of web-stable + web-canary and search, so requests are spread across all of them."; got != want {
		t.Fatalf("Cause = %q, want %q", got, want)
	}
}
