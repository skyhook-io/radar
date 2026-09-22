package server

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
)

func (s *Server) handleJobSetLogs(w http.ResponseWriter, r *http.Request) {
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if !s.authorizeJobSetEvidence(w, r, namespace) || !s.authorizePodLogRead(w, r, namespace) {
		return
	}
	root, _, owned, err := loadJobSetPods(r.Context(), namespace, name)
	if err != nil {
		s.writeWorkloadError(w, err)
		return
	}
	pods := []*corev1.Pod{}
	labels := map[string]string{}
	role := r.URL.Query().Get("role")
	for _, source := range owned {
		if role != "" && source.Job.Labels[replicatedJobNameLabel] != role {
			continue
		}
		pods = append(pods, source.Pod)
		labels[source.Pod.Name] = jobSetSourceLabel(source)
	}
	client := s.getClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client unavailable")
		return
	}
	snapshot := collectLogsFromPods(r.Context(), client, namespace, pods, r.URL.Query().Get("container"), parseTailLines(r.URL.Query().Get("tailLines"), 100), parseSinceSeconds(r.URL.Query().Get("sinceSeconds")), true)
	shownPods := []*corev1.Pod{}
	shownLabels := map[string]string{}
	for _, pod := range pods {
		if snapshot.SourcePods[pod.Name] {
			shownPods = append(shownPods, pod)
			shownLabels[pod.Name] = labels[pod.Name]
		}
	}
	for i := range snapshot.Logs {
		snapshot.Logs[i].SourceLabel = labels[snapshot.Logs[i].Pod]
	}
	sortLogsByTimestamp(snapshot.Logs)
	s.writeJSON(w, map[string]any{
		"uid": root.GetUID(), "pods": buildPodInfos(shownPods), "logs": snapshot.Logs,
		"notice": snapshot.Notice, "sourceLabels": shownLabels, "capturedAt": time.Now().UTC().Format(time.RFC3339),
		"emptyMessage": "No readable logs in this snapshot. Select a member Job to investigate its Pods or refresh after they start.",
	})
}
