// Package loki reads pod logs from the cluster's Loki: it finds Loki, detects
// how its streams are labelled and runs fixed queries built only from a
// namespace, pod, container, Job name and time window.
package loki

import (
	"fmt"
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

// lokiNamespaces are where Loki is commonly installed. A Service there
// outranks any Service elsewhere, whatever its name: anyone who can create a
// Service in their own namespace could otherwise plant a "loki-gateway" and
// receive every History query.
var lokiNamespaces = map[string]bool{"loki": true, "logging": true, "monitoring": true, "observability": true}

// Candidates ranks Services that look like a Loki read path, best first: one
// in a known Loki namespace before any other, then a gateway or query
// frontend before a read path before a single binary. Gossip
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
		if ka, kb := lokiNamespaces[a.Namespace], lokiNamespaces[b.Namespace]; ka != kb {
			return ka
		}
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

// chooseCandidates narrows ranked candidates to the ones Radar may connect to.
// Restricted (in-cluster, or with authentication) keeps only the known Loki
// namespaces and Radar's own: there, other users can create Services. Either
// way two equally good candidates in different namespaces are refused rather
// than guessed between.
func chooseCandidates(found []Candidate, restricted bool, own string) ([]Candidate, error) {
	if restricted {
		kept := found[:0:0]
		for _, c := range found {
			if lokiNamespaces[c.Namespace] || (own != "" && c.Namespace == own) {
				kept = append(kept, c)
			}
		}
		if len(kept) == 0 {
			return nil, fmt.Errorf("no Loki Service in the loki, logging, monitoring or observability namespace or Radar's own; Radar doesn't look elsewhere when it is shared, so set the Loki URL (--loki-url)")
		}
		found = kept
	}
	if len(found) > 1 {
		a, b := found[0], found[1]
		if a.Namespace != b.Namespace && a.Score == b.Score && lokiNamespaces[a.Namespace] == lokiNamespaces[b.Namespace] {
			return nil, fmt.Errorf("Services %s/%s and %s/%s look equally like Loki; set the Loki URL", a.Namespace, a.Name, b.Namespace, b.Name)
		}
	}
	return found, nil
}
