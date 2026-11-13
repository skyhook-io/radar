package loki

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func svc(ns, name string, labels map[string]string, ports ...corev1.ServicePort) corev1.Service {
	return corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels}, Spec: corev1.ServiceSpec{Ports: ports}}
}

func port(name string, n int32) corev1.ServicePort { return corev1.ServicePort{Name: name, Port: n} }

func TestCandidates(t *testing.T) {
	headless := svc("logging", "loki-headless", nil, port("http-metrics", 3100))
	headless.Spec.ClusterIP = "None"
	for _, tc := range []struct {
		name string
		in   []corev1.Service
		want []Candidate
	}{
		{"single binary skips headless and memberlist", []corev1.Service{
			svc("logging", "loki", nil, port("http-metrics", 3100), port("grpc", 9095)),
			headless,
			svc("logging", "loki-memberlist", nil, port("tcp", 7946)),
		}, []Candidate{{Namespace: "logging", Name: "loki", Port: 3100, Score: 20}}},
		{"gateway first, on port 80", []corev1.Service{
			svc("loki", "loki-read", nil, port("http-metrics", 3100)),
			svc("loki", "loki-gateway", nil, port("http", 80)),
		}, []Candidate{{Namespace: "loki", Name: "loki-gateway", Port: 80, Score: 40}, {Namespace: "loki", Name: "loki-read", Port: 3100, Score: 30}}},
		{"nothing like Loki", []corev1.Service{svc("monitoring", "prometheus", nil, port("http", 9090)), svc("monitoring", "grafana", nil, port("http", 80))}, nil},
		{"labelled query frontend", []corev1.Service{
			svc("obs", "logs", map[string]string{"app.kubernetes.io/name": "loki", "app.kubernetes.io/component": "query-frontend"}, port("http", 3100)),
		}, []Candidate{{Namespace: "obs", Name: "logs", Port: 3100, Score: 35}}},
		{"only a headless match is kept", []corev1.Service{headless}, []Candidate{{Namespace: "logging", Name: "loki-headless", Port: 3100, Score: 20}}},
		{"no named port falls back to 3100", []corev1.Service{svc("logging", "loki", nil, port("", 8080))}, []Candidate{{Namespace: "logging", Name: "loki", Port: 3100, Score: 20}}},
	} {
		if got := Candidates(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}
