// Package loki reads pod logs from the cluster's Loki: it finds Loki, detects
// how its streams are labelled and runs fixed queries built only from a
// namespace, pod, container, Job name and time window.
package loki

import (
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// Candidate is a Service that looks like Loki's read path.
type Candidate struct {
	Namespace, Name string
	Port            int
	Target          intstr.IntOrString // the port's targetPort, for a forward to the pod
	Selector        map[string]string
	Score           int
}

var nameScores = map[string]int{"loki-gateway": 40, "loki-query-frontend": 35, "loki-read": 30, "loki": 20}
var componentScores = map[string]int{"gateway": 30, "query-frontend": 25, "read": 20}

// Candidates ranks Services that look like a Loki read path, best first: a
// gateway or query frontend before a read path before a single binary. Gossip
// and canary Services never qualify; a headless one only when nothing else does.
func Candidates(services []corev1.Service) []Candidate {
	var found, headless []Candidate
	for _, s := range services {
		name := s.Name
		if strings.HasSuffix(name, "-memberlist") || strings.HasSuffix(name, "-canary") {
			continue
		}
		isHeadless := s.Spec.ClusterIP == corev1.ClusterIPNone || strings.HasSuffix(name, "-headless")
		score := nameScores[strings.TrimSuffix(name, "-headless")]
		if s.Labels["app.kubernetes.io/name"] == "loki" {
			score = max(score, 10+componentScores[s.Labels["app.kubernetes.io/component"]])
		}
		if score == 0 {
			continue
		}
		p := lokiPort(s, score >= 40)
		c := Candidate{Namespace: s.Namespace, Name: name, Port: int(p.Port), Target: p.TargetPort, Selector: s.Spec.Selector, Score: score}
		if isHeadless {
			headless = append(headless, c)
		} else {
			found = append(found, c)
		}
	}
	if len(found) == 0 {
		found = headless
	}
	sort.SliceStable(found, func(i, j int) bool {
		a, b := found[i], found[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	return found
}

// lokiPort: the port named http-metrics or http, a gateway's port 80, else
// Loki's default 3100.
func lokiPort(s corev1.Service, gateway bool) corev1.ServicePort {
	for _, want := range []string{"http-metrics", "http"} {
		for _, p := range s.Spec.Ports {
			if p.Name == want {
				return p
			}
		}
	}
	if gateway {
		for _, p := range s.Spec.Ports {
			if p.Port == 80 {
				return p
			}
		}
	}
	return corev1.ServicePort{Port: 3100}
}
