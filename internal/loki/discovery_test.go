package loki

import (
	"reflect"
	"strings"
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
		{"a known Loki namespace outranks a better name elsewhere", []corev1.Service{
			svc("team-a", "loki-gateway", nil, port("http", 80)),
			svc("loki", "loki-read", nil, port("http-metrics", 3100)),
		}, []Candidate{{Namespace: "loki", Name: "loki-read", Port: 3100, Score: 30}, {Namespace: "team-a", Name: "loki-gateway", Port: 80, Score: 40}}},
		{"no named port falls back to 3100", []corev1.Service{svc("logging", "loki", nil, port("", 8080))}, []Candidate{{Namespace: "logging", Name: "loki", Port: 3100, Score: 20}}},
	} {
		if got := Candidates(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestChooseCandidates(t *testing.T) {
	c := func(ns, name string, score int) Candidate { return Candidate{Namespace: ns, Name: name, Score: score} }
	for _, tc := range []struct {
		name       string
		in         []Candidate
		restricted bool
		own        string
		want       []Candidate
		wantErr    string
	}{
		{"local keeps any namespace", []Candidate{c("team-a", "loki", 20)}, false, "", []Candidate{c("team-a", "loki", 20)}, ""},
		{"local refuses a tie across namespaces", []Candidate{c("team-a", "loki-gateway", 40), c("team-b", "loki-gateway", 40)}, false, "", nil, "team-a/loki-gateway and team-b/loki-gateway"},
		{"a tie in one namespace is fine", []Candidate{c("loki", "loki-gateway", 40), c("loki", "loki-gateway-2", 40)}, false, "", []Candidate{c("loki", "loki-gateway", 40), c("loki", "loki-gateway-2", 40)}, ""},
		{"a known namespace breaks a tie", []Candidate{c("loki", "loki", 20), c("team-a", "loki", 20)}, false, "", []Candidate{c("loki", "loki", 20), c("team-a", "loki", 20)}, ""},
		{"restricted drops other namespaces", []Candidate{c("loki", "loki-read", 30), c("team-a", "loki-gateway", 40), c("radar", "loki", 20)}, true, "radar", []Candidate{c("loki", "loki-read", 30), c("radar", "loki", 20)}, ""},
		{"restricted with nothing left", []Candidate{c("team-a", "loki-gateway", 40)}, true, "radar", nil, "--loki-url"},
	} {
		got, err := chooseCandidates(tc.in, tc.restricted, tc.own)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%s: error = %v, want %q", tc.name, err, tc.wantErr)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %+v, %v", tc.name, got, err)
		}
	}
}
