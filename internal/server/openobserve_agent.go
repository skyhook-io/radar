package server

import (
	"net/http"
	"sort"

	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/auth"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
)

const maxMetricsPodSample = 100

type metricsPodSampleEntry struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
}

type metricsPodSample struct {
	Namespace string                  `json:"namespace"`
	Pods      []metricsPodSampleEntry `json:"pods"`
}

// handleMetricsPodSample lets Hub suggest which value of a metrics backend's
// cluster label is this cluster, by comparing Pod names and UIDs it finds there
// with Pods that run here. Like the other /api/agent reads it is tunnel-only
// and runs as the Hub caller: the sample comes from the one namespace with the
// most running Pods among those the caller may list Pods in, inside the
// namespace pin, so it discloses nothing the caller could not list.
func (s *Server) handleMetricsPodSample(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !cloud.IsAuthenticatedTunnelRequest(r.Context()) || auth.UserFromContext(r.Context()) == nil {
		http.NotFound(w, r)
		return
	}
	if !s.requireConnected(w) {
		return
	}
	cache := k8s.GetResourceCache()
	if cache == nil || cache.Pods() == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Pods are not available yet")
		return
	}
	generation := k8s.Current().ClientGeneration()
	pods, err := cache.Pods().List(labels.Everything())
	if err != nil {
		s.writeError(w, http.StatusServiceUnavailable, "Pods are not available yet")
		return
	}
	sample, ok := s.samplePods(r, pods)
	if k8s.Current().ClientGeneration() != generation {
		s.writeError(w, http.StatusConflict, "Kubernetes connection changed. Retry the request")
		return
	}
	if !ok {
		s.writeError(w, http.StatusForbidden, "Listing Pods is not permitted in any namespace with running Pods")
		return
	}
	s.writeJSON(w, sample)
}

func (s *Server) samplePods(r *http.Request, pods []*corev1.Pod) (metricsPodSample, bool) {
	if s.permCache == nil {
		return metricsPodSample{}, false
	}
	byNamespace := map[string][]*corev1.Pod{}
	for _, p := range pods {
		if p.Status.Phase != corev1.PodRunning || p.DeletionTimestamp != nil || outsideNamespacePin(p.Namespace) != nil {
			continue
		}
		byNamespace[p.Namespace] = append(byNamespace[p.Namespace], p)
	}
	namespaces := make([]string, 0, len(byNamespace))
	for ns := range byNamespace {
		namespaces = append(namespaces, ns)
	}
	sort.Slice(namespaces, func(i, j int) bool {
		a, b := len(byNamespace[namespaces[i]]), len(byNamespace[namespaces[j]])
		if a != b {
			return a > b
		}
		return namespaces[i] < namespaces[j]
	})
	for _, ns := range namespaces {
		if !s.prometheusAuthGate(r, "", "pods", ns, "list") {
			continue
		}
		members := byNamespace[ns]
		sort.Slice(members, func(i, j int) bool { return members[i].Name < members[j].Name })
		if len(members) > maxMetricsPodSample {
			members = members[:maxMetricsPodSample]
		}
		out := metricsPodSample{Namespace: ns, Pods: make([]metricsPodSampleEntry, len(members))}
		for i, p := range members {
			out.Pods[i] = metricsPodSampleEntry{Name: p.Name, UID: string(p.UID)}
		}
		return out, true
	}
	return metricsPodSample{}, false
}

// handleMetricsSourceRefresh is Hub's nudge after the metrics source for this
// cluster may have changed: Hub cannot push configuration, so the agent asks
// Hub for the source again now. Tunnel-only like the reads above; it carries
// no data and changes nothing Hub does not already decide.
func (s *Server) handleMetricsSourceRefresh(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !cloud.IsAuthenticatedTunnelRequest(r.Context()) || auth.UserFromContext(r.Context()) == nil {
		http.NotFound(w, r)
		return
	}
	cloud.RequestMetricsSourceRefresh()
	w.WriteHeader(http.StatusNoContent)
}

// openObserveCollectorSelectors are the Pod labels the OpenObserve collector
// chart sets. Any one matching marks a collector Pod; amend the list here as
// more installs are seen.
var openObserveCollectorSelectors = []labels.Set{
	{"app.kubernetes.io/name": "openobserve-collector"},
	{"app.kubernetes.io/part-of": "openobserve-collector"},
	{"app.kubernetes.io/instance": "openobserve-collector"},
}

func runningOpenObserveCollector(p *corev1.Pod) bool {
	if p.DeletionTimestamp != nil || p.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, set := range openObserveCollectorSelectors {
		if labels.SelectorFromSet(set).Matches(labels.Set(p.Labels)) {
			return true
		}
	}
	return false
}

// Presence is a setup hint. A negative result also covers collectors outside the caller's visibility.
func (s *Server) handleOpenObserveCollectorPresence(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !cloud.IsAuthenticatedTunnelRequest(r.Context()) || auth.UserFromContext(r.Context()) == nil {
		http.NotFound(w, r)
		return
	}
	detected := false
	generation := k8s.Current().ClientGeneration()
	cache := k8s.GetResourceCache()
	if s.permCache != nil && cache != nil && cache.Pods() != nil {
		if pods, err := cache.Pods().List(labels.Everything()); err == nil {
			for _, p := range pods {
				if runningOpenObserveCollector(p) && outsideNamespacePin(p.Namespace) == nil && s.prometheusAuthGate(r, "", "pods", p.Namespace, "list") {
					detected = true
					break
				}
			}
		}
	}
	if k8s.Current().ClientGeneration() != generation {
		detected = false
	}
	s.writeJSON(w, struct {
		Detected bool `json:"detected"`
	}{detected})
}
