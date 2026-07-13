package prom

import (
	"context"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestScoreService_TableDriven(t *testing.T) {
	tests := []struct {
		name         string
		svc          corev1.Service
		wantMin      int
		wantMax      int
		wantBasePath string
	}{
		{
			name: "plain prometheus by app.kubernetes.io/name + port",
			svc: corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "prometheus-server",
					Namespace: "monitoring",
					Labels:    map[string]string{"app.kubernetes.io/name": "prometheus"},
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{{Port: 9090}},
				},
			},
			wantMin: 100 + 30 + 20 + 10, // name + port + name-contains + metrics ns
			wantMax: 500,
		},
		{
			name: "vmselect sets basePath",
			svc: corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "vmselect",
					Namespace: "monitoring",
					Labels:    map[string]string{"app.kubernetes.io/name": "vmselect"},
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{{Port: 8481}},
				},
			},
			wantMin:      90 + 25 + 20 + 10,
			wantMax:      200,
			wantBasePath: "/select/0/prometheus",
		},
		{
			name: "thanos-query scores lower than prometheus but non-zero",
			svc: corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "thanos-query",
					Namespace: "observability",
					Labels:    map[string]string{"app.kubernetes.io/name": "thanos-query"},
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{{Port: 9009}},
				},
			},
			wantMin: 80 + 25 + 15 + 10,
			wantMax: 200,
		},
		{
			name: "unrelated service scores zero",
			svc: corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "redis",
					Namespace: "default",
					Labels:    map[string]string{"app": "redis"},
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{{Port: 6379}},
				},
			},
			wantMax: 0,
		},
		{
			name: "ExternalName excluded",
			svc: corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "prometheus", Namespace: "monitoring"},
				Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeExternalName},
			},
			wantMax: 0,
		},
		{
			name: "skip-namespace excluded",
			svc: corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "prometheus",
					Namespace: "kube-public",
					Labels:    map[string]string{"app.kubernetes.io/name": "prometheus"},
				},
			},
			wantMax: 0,
		},
		// Non-query components must score zero even though their names contain
		// "prometheus" and they live in a metrics namespace — these are the
		// exact services that were polluting discovery and wasting port-forwards.
		{
			name: "node-exporter excluded despite prometheus in name",
			svc: corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kube-prometheus-stack-prometheus-node-exporter",
					Namespace: "monitoring",
					Labels:    map[string]string{"app.kubernetes.io/name": "prometheus-node-exporter"},
				},
				Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 9100}}},
			},
			wantMax: 0,
		},
		{
			name: "kube-state-metrics excluded",
			svc: corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kube-prometheus-stack-kube-state-metrics",
					Namespace: "monitoring",
					Labels:    map[string]string{"app.kubernetes.io/name": "kube-state-metrics"},
				},
				Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 8080}}},
			},
			wantMax: 0,
		},
		{
			name: "prometheus-operator excluded",
			svc: corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kube-prometheus-stack-operator",
					Namespace: "monitoring",
					Labels:    map[string]string{"app.kubernetes.io/component": "prometheus-operator"},
				},
				Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 443}}},
			},
			wantMax: 0,
		},
		{
			name: "alertmanager excluded",
			svc: corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kube-prometheus-stack-alertmanager",
					Namespace: "monitoring",
					Labels:    map[string]string{"app.kubernetes.io/name": "alertmanager"},
				},
				Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 9093}}},
			},
			wantMax: 0,
		},
		{
			name: "pushgateway excluded",
			svc: corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "prometheus-pushgateway",
					Namespace: "monitoring",
					Labels:    map[string]string{"app.kubernetes.io/name": "prometheus-pushgateway"},
				},
				Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 9091}}},
			},
			wantMax: 0,
		},
		{
			name: "definitive prometheus label overrides a deny-list substring in the name",
			svc: corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "grafana-prometheus-server", // "grafana" substring
					Namespace: "monitoring",
					Labels:    map[string]string{"app.kubernetes.io/name": "prometheus"},
				},
				Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 9090}}},
			},
			wantMin: 1, // must not be excluded by the "grafana" token
			wantMax: 500,
		},
		{
			name: "prometheus from an operator-named release is kept",
			svc: corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "prometheus-operator-prometheus", Namespace: "monitoring"},
				Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 9090}}},
			},
			wantMin: 1, // must not be excluded (name contains "operator" but isn't the operator)
			wantMax: 500,
		},
		{
			name: "kubelet scrape-target excluded despite prometheus in name",
			svc: corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kube-prometheus-stack-kubelet",
					Namespace: "kube-system",
				},
				Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 10250}}},
			},
			wantMax: 0,
		},
		{
			name: "thanos-store excluded (not the query API)",
			svc: corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "thanos-store",
					Namespace: "monitoring",
					Labels:    map[string]string{"app.kubernetes.io/name": "thanos-store"},
				},
				Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 10902}}},
			},
			wantMax: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			score, bp, _ := ScoreService(tc.svc)
			if score < tc.wantMin || (tc.wantMax > 0 && score > tc.wantMax) {
				t.Errorf("score=%d, want in [%d, %d]", score, tc.wantMin, tc.wantMax)
			}
			if tc.wantMax == 0 && score != 0 {
				t.Errorf("score=%d, want 0", score)
			}
			if tc.wantBasePath != "" && bp != tc.wantBasePath {
				t.Errorf("basePath=%q, want %q", bp, tc.wantBasePath)
			}
		})
	}
}

func TestDiscover_WellKnownFirst(t *testing.T) {
	// Install a standard prometheus-server at a well-known location
	// plus an unrelated redis service.
	wellKnown := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "prometheus-server", Namespace: "monitoring"},
		Spec: corev1.ServiceSpec{
			ClusterIP: "10.0.0.1",
			Ports:     []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromInt(9090)}},
		},
	}
	redis := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "redis", Namespace: "default"},
		Spec: corev1.ServiceSpec{
			ClusterIP: "10.0.0.2",
			Ports:     []corev1.ServicePort{{Port: 6379}},
		},
	}
	// Install an additional unknown-but-scoring dynamic candidate.
	thanos := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "thanos-query",
			Namespace: "observability",
			Labels:    map[string]string{"app.kubernetes.io/name": "thanos-query"},
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "10.0.0.3",
			Ports:     []corev1.ServicePort{{Port: 9009}},
		},
	}

	k8s := fake.NewSimpleClientset(wellKnown, redis, thanos)
	cands, err := Discover(context.Background(), k8s, DiscoverOptions{IncludeDynamic: true, MaxDynamic: 3})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(cands) < 2 {
		t.Fatalf("want at least 2 candidates, got %d", len(cands))
	}

	// First must be the well-known match.
	if cands[0].Source != CandidateSourceWellKnown {
		t.Errorf("cands[0].Source = %q, want well_known", cands[0].Source)
	}
	if cands[0].Namespace != "monitoring" || cands[0].Name != "prometheus-server" {
		t.Errorf("cands[0] = %s/%s, want monitoring/prometheus-server", cands[0].Namespace, cands[0].Name)
	}
	if cands[0].ClusterAddr != "http://prometheus-server.monitoring.svc.cluster.local:80" {
		t.Errorf("cluster addr = %q", cands[0].ClusterAddr)
	}
	if cands[0].TargetPort != 9090 {
		t.Errorf("TargetPort = %d, want 9090", cands[0].TargetPort)
	}

	// Dynamic thanos match should be present.
	var sawDynamicThanos bool
	for _, c := range cands {
		if c.Source == CandidateSourceDynamic && c.Name == "thanos-query" {
			sawDynamicThanos = true
			break
		}
	}
	if !sawDynamicThanos {
		t.Errorf("expected dynamic thanos candidate; got %+v", cands)
	}

	// Redis must not appear in any form.
	for _, c := range cands {
		if c.Name == "redis" {
			t.Errorf("redis should not be a candidate: %+v", c)
		}
	}
}

func TestDiscover_DeDupesWellKnownAndDynamic(t *testing.T) {
	// A service that matches both a well-known location (monitoring/prometheus-server)
	// and scores in the dynamic scan (app.kubernetes.io/name=prometheus) must
	// appear exactly once, sourced from the well-known layer.
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "prometheus-server",
			Namespace: "monitoring",
			Labels:    map[string]string{"app.kubernetes.io/name": "prometheus"},
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "10.0.0.1",
			Ports:     []corev1.ServicePort{{Port: 9090}},
		},
	}

	k8s := fake.NewSimpleClientset(svc)
	cands, err := Discover(context.Background(), k8s, DiscoverOptions{IncludeDynamic: true, MaxDynamic: 5})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	var count int
	for _, c := range cands {
		if c.Namespace == "monitoring" && c.Name == "prometheus-server" {
			count++
			if c.Source != CandidateSourceWellKnown {
				t.Errorf("de-duped candidate should retain well_known source, got %q", c.Source)
			}
		}
	}
	if count != 1 {
		t.Fatalf("want monitoring/prometheus-server exactly once, got %d (all: %+v)", count, cands)
	}
}

func TestDiscover_ExcludesControlPlaneScrapeTargets(t *testing.T) {
	// A kube-prometheus-stack control-plane scrape-target Service carries
	// "prometheus" in its name but only proxies a component's /metrics; it must
	// not become a candidate — it can't answer PromQL and would waste a
	// port-forward. Excluded by name (a component token), not by score.
	noise := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kube-prometheus-stack-kube-etcd",
			Namespace: "kube-system",
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "None",
			Ports:     []corev1.ServicePort{{Port: 2381}},
		},
	}

	k8s := fake.NewSimpleClientset(noise)
	cands, err := Discover(context.Background(), k8s, DiscoverOptions{IncludeDynamic: true, MaxDynamic: 5})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(cands) != 0 {
		t.Fatalf("control-plane scrape-target service became a candidate: %+v", cands)
	}
}

func TestDiscover_ExcludesNamespaceOnlyNeighbor(t *testing.T) {
	// An unrelated Service that only matches by sharing a metrics namespace (no
	// name/label/port Prometheus signal) must NOT become a candidate — discovery
	// probes candidates with the operator's configured Prometheus headers, so a
	// namespace neighbor would receive credentials it shouldn't.
	neighbor := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "billing-api",
			Namespace: "monitoring",
			Labels:    map[string]string{"app": "billing"},
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "10.0.0.42",
			Ports:     []corev1.ServicePort{{Port: 8080, Name: "http"}},
		},
	}

	if _, _, identity := ScoreService(*neighbor); identity {
		t.Fatal("namespace-only neighbor reported a Prometheus identity signal")
	}

	k8s := fake.NewSimpleClientset(neighbor)
	cands, err := Discover(context.Background(), k8s, DiscoverOptions{IncludeDynamic: true, MaxDynamic: 5})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(cands) != 0 {
		t.Fatalf("namespace-only neighbor became a candidate (would be probed with creds): %+v", cands)
	}
}

func TestDiscover_KeepsPrometheusWithNamedTargetPort(t *testing.T) {
	// An unlabeled Prometheus behind service port 80 with a *named* targetPort
	// scores only on its name (a named targetPort has no numeric value to
	// credit). Score ranks, it does not gate — so this real endpoint must still
	// be discovered and left for the probe to validate, not silently dropped.
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "prometheus-server", Namespace: "apps"},
		Spec: corev1.ServiceSpec{
			ClusterIP: "10.0.0.6",
			Ports:     []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromString("web")}},
		},
	}

	k8s := fake.NewSimpleClientset(svc)
	cands, err := Discover(context.Background(), k8s, DiscoverOptions{IncludeDynamic: true, MaxDynamic: 5})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	var found bool
	for _, c := range cands {
		if c.Namespace == "apps" && c.Name == "prometheus-server" {
			found = true
		}
	}
	if !found {
		t.Fatalf("prometheus with a named targetPort was pruned: %+v", cands)
	}
}

func TestDiscover_KeepsUnlabeledThanosQuery(t *testing.T) {
	// An unlabeled thanos-query on its common HTTP port is a real query API and
	// must survive the dynamic score floor (name match + recognized port).
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "thanos-query", Namespace: "obs"},
		Spec: corev1.ServiceSpec{
			ClusterIP: "10.0.0.8",
			Ports:     []corev1.ServicePort{{Port: 10902}},
		},
	}

	k8s := fake.NewSimpleClientset(svc)
	cands, err := Discover(context.Background(), k8s, DiscoverOptions{IncludeDynamic: true, MaxDynamic: 5})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	var found bool
	for _, c := range cands {
		if c.Name == "thanos-query" {
			found = true
		}
	}
	if !found {
		t.Fatalf("unlabeled thanos-query was pruned: %+v", cands)
	}
}

func TestDiscover_KeepsUnlabeledCustomPrometheus(t *testing.T) {
	// A real Prometheus in a non-standard namespace with no conventional labels
	// and a service port of 80 scores only on the name match, but it is a
	// genuine query endpoint and must still be discovered — pruning must not be
	// so aggressive that it drops it.
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "prometheus-server", Namespace: "platform"},
		Spec: corev1.ServiceSpec{
			ClusterIP: "10.0.0.9",
			Ports:     []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromInt(9090)}},
		},
	}

	k8s := fake.NewSimpleClientset(svc)
	cands, err := Discover(context.Background(), k8s, DiscoverOptions{IncludeDynamic: true, MaxDynamic: 5})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	var found bool
	for _, c := range cands {
		if c.Namespace == "platform" && c.Name == "prometheus-server" {
			found = true
		}
	}
	if !found {
		t.Fatalf("unlabeled custom prometheus was pruned: %+v", cands)
	}
}

func TestDiscover_SkipsDynamicWhenDisabled(t *testing.T) {
	// Only a dynamic-scoring service is present (no well-known match).
	prom := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-prometheus",
			Namespace: "observability",
			Labels:    map[string]string{"app.kubernetes.io/name": "prometheus"},
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "10.0.0.5",
			Ports:     []corev1.ServicePort{{Port: 9090}},
		},
	}

	k8s := fake.NewSimpleClientset(prom)
	cands, err := Discover(context.Background(), k8s, DiscoverOptions{IncludeDynamic: false})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 0 {
		t.Errorf("expected no candidates when dynamic is disabled and no well-known match; got %d", len(cands))
	}
}

func TestDiscover_HeadlessServiceProducesPod0Addr(t *testing.T) {
	headless := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "prometheus-server", Namespace: "monitoring"},
		Spec: corev1.ServiceSpec{
			ClusterIP: "None",
			Ports:     []corev1.ServicePort{{Port: 9090}},
		},
	}
	k8s := fake.NewSimpleClientset(headless)
	cands, err := Discover(context.Background(), k8s, DiscoverOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 {
		t.Fatalf("want 1 candidate, got %d", len(cands))
	}
	want := "http://prometheus-server-0.prometheus-server.monitoring.svc.cluster.local:9090"
	if cands[0].ClusterAddr != want {
		t.Errorf("cluster addr = %q, want %q", cands[0].ClusterAddr, want)
	}
}

func TestDiscover_NilClient(t *testing.T) {
	_, err := Discover(context.Background(), nil, DiscoverOptions{})
	if err == nil {
		t.Error("expected error for nil client")
	}
}

// TestDiscover_LoggerCalledSerially guards the DiscoverOptions.Logger contract:
// well-known lookups run concurrently, but the Logger must not be — a caller can
// pass an unsynchronized callback. The reactor makes every well-known Get fail
// (non-NotFound) so each would log; an unsynchronized slice append from parallel
// workers would trip the race detector.
func TestDiscover_LoggerCalledSerially(t *testing.T) {
	k8s := fake.NewSimpleClientset()
	k8s.PrependReactor("get", "services", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("simulated API error")
	})

	var logs []string
	_, err := Discover(context.Background(), k8s, DiscoverOptions{
		Logger: func(format string, args ...interface{}) {
			logs = append(logs, fmt.Sprintf(format, args...)) // deliberately unsynchronized
		},
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(logs) == 0 {
		t.Fatal("expected error logs from failed well-known lookups")
	}
}
