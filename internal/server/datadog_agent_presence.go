package server

import (
	"net/http"
	"strings"

	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/auth"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
)

func runningDatadogAgent(p *corev1.Pod) bool {
	if p.DeletionTimestamp != nil || p.Status.Phase != corev1.PodRunning || p.Labels["agent.datadoghq.com/component"] != "agent" {
		return false
	}
	for _, c := range p.Status.ContainerStatuses {
		if c.Name != "agent" || c.State.Running == nil {
			continue
		}
		image, _, _ := strings.Cut(c.Image, "@")
		last := image[strings.LastIndex(image, "/")+1:]
		name, _, _ := strings.Cut(last, ":")
		if name == "agent" {
			return true
		}
	}
	return false
}

// Presence is a setup hint. A negative result also covers agents outside the caller's visibility.
func (s *Server) handleDatadogAgentPresence(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !cloud.IsAuthenticatedTunnelRequest(r.Context()) || auth.UserFromContext(r.Context()) == nil {
		http.NotFound(w, r)
		return
	}
	detected := false
	_, generation := k8s.GetClientWithGeneration()
	cache := k8s.GetResourceCache()
	if s.permCache != nil && cache != nil && cache.Pods() != nil {
		pods, err := cache.Pods().List(labels.SelectorFromSet(labels.Set{"agent.datadoghq.com/component": "agent"}))
		if err == nil {
			for _, p := range pods {
				if runningDatadogAgent(p) && outsideNamespacePin(p.Namespace) == nil && s.prometheusAuthGate(r, "", "pods", p.Namespace, "list") {
					detected = true
					break
				}
			}
		}
	}
	if _, current := k8s.GetClientWithGeneration(); current != generation {
		detected = false
	}
	s.writeJSON(w, struct {
		Detected bool `json:"detected"`
	}{detected})
}
