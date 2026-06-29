package prom

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
)

func TestScoreService_TableDriven(t *testing.T) {
	tests := []struct {
		name        string
		svc         corev1.Service
		wantMin     int
		wantMax     int
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
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			score, bp := ScoreService(tc.svc)
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
