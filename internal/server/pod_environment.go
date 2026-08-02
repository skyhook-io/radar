package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/envresolve"
)

const (
	maxPodEnvironmentSources = 64
	maxPodEnvironmentRows    = 1000
)

type podEnvironmentRevealRequest struct {
	Container string `json:"container"`
	Variable  string `json:"variable"`
}

type podEnvironmentRevealResponse struct {
	Value    string `json:"value"`
	Encoding string `json:"encoding"`
}

func (s *Server) handlePodEnvironment(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	if !s.requireConnected(w) {
		return
	}
	client := s.getClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available")
		return
	}
	pod, err := client.CoreV1().Pods(namespace).Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		s.writePodEnvironmentGetError(w, namespace, name, "Pod", err)
		return
	}
	sources, partial, truncated := loadPodEnvironmentSources(r, client, pod, false)
	node, nodePartial := loadPodEnvironmentNode(r, client, pod)
	response := envresolve.ResolvePod(pod, sources, node)
	partial = partial || nodePartial
	response.Partial = partial
	changes, coverage := k8s.FindPodEnvChanges(r.Context(), k8s.GetResourceCache(), pod, sources)
	response.Coverage = coverage
	attachPodEnvironmentEvidence(&response, changes, sources)
	rowsTruncated := truncatePodEnvironmentRows(&response, maxPodEnvironmentRows)
	response.Truncated = truncated || rowsTruncated
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, response)
}

func (s *Server) handleRevealPodEnvironment(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	auditDetails := auth.AuditActionDetails{
		Action: "reveal-pod-environment", Namespace: namespace, Name: name, Outcome: "invalid",
	}
	defer func() { auth.AuditLogAction(r, auditDetails) }()
	var request podEnvironmentRevealRequest
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Container == "" || request.Variable == "" || len(request.Container) > 253 || len(request.Variable) > 253 {
		s.writeError(w, http.StatusBadRequest, "container and variable are required")
		return
	}
	auditDetails.Container = request.Container
	auditDetails.Variable = request.Variable
	auditDetails.Outcome = "denied"
	if !s.requireConnected(w) {
		auditDetails.Outcome = "unavailable"
		return
	}
	client := s.getClientForRequest(r)
	if client == nil {
		auditDetails.Outcome = "unavailable"
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available")
		return
	}
	pod, err := client.CoreV1().Pods(namespace).Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		auditDetails.Outcome = podEnvironmentReadOutcome(err)
		s.writePodEnvironmentGetError(w, namespace, name, "Pod", err)
		return
	}
	sources, partial, truncated := loadPodEnvironmentSources(r, client, pod, true)
	node, _ := loadPodEnvironmentNode(r, client, pod)
	row, value, available, found := envresolve.ResolveVariable(pod, request.Container, request.Variable, sources, node)
	if !found {
		auditDetails.Outcome = "not-found"
		s.writeError(w, http.StatusNotFound, "environment variable not found")
		return
	}
	auditDetails.Source = secretAuditSource(row)
	if !row.Sensitive {
		auditDetails.Outcome = "invalid"
		s.writeError(w, http.StatusBadRequest, "environment variable is not Secret-backed")
		return
	}
	if !available {
		status, outcome, message := podEnvironmentRevealFailure(row)
		auditDetails.Outcome = outcome
		s.writeError(w, status, message)
		return
	}
	if partial || truncated {
		auditDetails.Outcome = "unavailable"
		s.writeError(w, http.StatusConflict, "environment sources could not be fully inspected; reveal cannot be resolved safely")
		return
	}
	encoding := "utf8"
	if !utf8.ValidString(value) {
		value = base64.StdEncoding.EncodeToString([]byte(value))
		encoding = "base64"
	}
	auditDetails.Outcome = "success"
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, podEnvironmentRevealResponse{Value: value, Encoding: encoding})
}

func loadPodEnvironmentNode(r *http.Request, client kubernetes.Interface, pod *corev1.Pod) (envresolve.NodeData, bool) {
	if !envresolve.PodEnvironmentNeedsNode(pod) {
		return envresolve.NodeData{}, false
	}
	if pod.Spec.NodeName == "" {
		return envresolve.NodeData{State: envresolve.SourceUnavailable}, true
	}
	node, err := client.CoreV1().Nodes().Get(r.Context(), pod.Spec.NodeName, metav1.GetOptions{})
	if err != nil {
		return envresolve.NodeData{State: sourceStateForError(err)}, true
	}
	return envresolve.NodeData{State: envresolve.SourceAvailable, Allocatable: node.Status.Allocatable}, false
}

func loadPodEnvironmentSources(r *http.Request, client kubernetes.Interface, pod *corev1.Pod, includeSecretValues bool) (map[envresolve.SourceID]envresolve.SourceData, bool, bool) {
	ids := podEnvironmentSourceIDs(pod)
	truncated := len(ids) > maxPodEnvironmentSources
	allIDs := ids
	if truncated {
		ids = ids[:maxPodEnvironmentSources]
	}
	sources := make(map[envresolve.SourceID]envresolve.SourceData, len(allIDs))
	for _, id := range allIDs[len(ids):] {
		sources[id] = envresolve.SourceData{
			Kind: id.Kind, Name: id.Name, State: envresolve.SourceUnavailable,
			Sensitive: id.Kind == "Secret", Message: fmt.Sprintf("%s/%s was not inspected because this Pod references too many sources.", id.Kind, id.Name),
		}
	}
	partial := false
	for _, id := range ids {
		source := envresolve.SourceData{Kind: id.Kind, Name: id.Name, State: envresolve.SourceAvailable, Sensitive: id.Kind == "Secret"}
		switch id.Kind {
		case "ConfigMap":
			configMap, err := client.CoreV1().ConfigMaps(pod.Namespace).Get(r.Context(), id.Name, metav1.GetOptions{})
			if err != nil {
				source.State = sourceStateForError(err)
				partial = partial || source.State != envresolve.SourceMissing
			} else {
				source.Values = make(map[string]string, len(configMap.Data))
				for key, value := range configMap.Data {
					source.Keys = append(source.Keys, key)
					source.Values[key] = value
				}
			}
		case "Secret":
			secret, err := client.CoreV1().Secrets(pod.Namespace).Get(r.Context(), id.Name, metav1.GetOptions{})
			if err != nil {
				source.State = sourceStateForError(err)
				partial = partial || source.State != envresolve.SourceMissing
			} else {
				source.Values = make(map[string]string)
				for key, value := range secret.Data {
					source.Keys = append(source.Keys, key)
					if includeSecretValues {
						source.Values[key] = string(value)
					}
				}
			}
		}
		sort.Strings(source.Keys)
		sources[id] = source
	}
	return sources, partial, truncated
}

func podEnvironmentSourceIDs(pod *corev1.Pod) []envresolve.SourceID {
	seen := make(map[envresolve.SourceID]bool)
	containers := append([]corev1.Container(nil), pod.Spec.InitContainers...)
	containers = append(containers, pod.Spec.Containers...)
	for _, container := range containers {
		for _, from := range container.EnvFrom {
			if from.ConfigMapRef != nil {
				seen[envresolve.SourceID{Kind: "ConfigMap", Name: from.ConfigMapRef.Name}] = true
			}
			if from.SecretRef != nil {
				seen[envresolve.SourceID{Kind: "Secret", Name: from.SecretRef.Name}] = true
			}
		}
		for _, env := range container.Env {
			if env.ValueFrom == nil {
				continue
			}
			if env.ValueFrom.ConfigMapKeyRef != nil {
				seen[envresolve.SourceID{Kind: "ConfigMap", Name: env.ValueFrom.ConfigMapKeyRef.Name}] = true
			}
			if env.ValueFrom.SecretKeyRef != nil {
				seen[envresolve.SourceID{Kind: "Secret", Name: env.ValueFrom.SecretKeyRef.Name}] = true
			}
		}
	}
	out := make([]envresolve.SourceID, 0, len(seen))
	for id := range seen {
		if id.Name != "" {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func sourceStateForError(err error) envresolve.SourceState {
	switch {
	case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
		return envresolve.SourceDenied
	case apierrors.IsNotFound(err):
		return envresolve.SourceMissing
	default:
		return envresolve.SourceUnavailable
	}
}

func truncatePodEnvironmentRows(response *envresolve.PodEnvironment, limit int) bool {
	if limit < 0 {
		limit = 0
	}
	original := make([][]envresolve.Row, len(response.Containers))
	total := 0
	for i := range response.Containers {
		original[i] = response.Containers[i].Rows
		total += len(original[i])
		response.Containers[i].Rows = nil
	}
	if total <= limit {
		for i := range response.Containers {
			response.Containers[i].Rows = original[i]
		}
		return false
	}
	for offset, added := 0, 0; added < limit; offset++ {
		progress := false
		for i := range response.Containers {
			if offset >= len(original[i]) || added >= limit {
				continue
			}
			response.Containers[i].Rows = append(response.Containers[i].Rows, original[i][offset])
			added++
			progress = true
		}
		if !progress {
			break
		}
	}
	for i := range response.Containers {
		response.Containers[i].Truncated = len(response.Containers[i].Rows) < len(original[i])
	}
	return true
}

func attachPodEnvironmentEvidence(response *envresolve.PodEnvironment, changes []k8s.PodEnvChange, sources map[envresolve.SourceID]envresolve.SourceData) {
	byVariable := make(map[string][]k8s.PodEnvChange)
	byContainer := make(map[string][]k8s.PodEnvChange)
	for _, change := range changes {
		source := sources[envresolve.SourceID{Kind: change.Source.Kind, Name: change.Source.Name}]
		if source.State != envresolve.SourceAvailable {
			continue
		}
		key := change.Container + "\x00" + change.Variable
		byVariable[key] = append(byVariable[key], change)
		byContainer[change.Container] = append(byContainer[change.Container], change)
	}
	for i := range response.Containers {
		container := &response.Containers[i]
		seen := make(map[string]bool, len(container.Rows))
		for j := range response.Containers[i].Rows {
			row := &container.Rows[j]
			seen[row.Name] = true
			for _, change := range byContainer[container.Name] {
				if !rowUsesSource(*row, change.Source) || row.Evidence != nil && !change.Evidence.ChangedAt.After(row.Evidence.ChangedAt) {
					continue
				}
				evidence := change.Evidence
				row.Evidence = &evidence
			}
		}
		for key, variableChanges := range byVariable {
			changeContainer, variable, ok := strings.Cut(key, "\x00")
			if !ok || changeContainer != container.Name || seen[variable] {
				continue
			}
			var change *k8s.PodEnvChange
			for j := range variableChanges {
				candidate := &variableChanges[j]
				if candidate.Evidence.Kind == "removed" && (change == nil || candidate.Evidence.ChangedAt.After(change.Evidence.ChangedAt)) {
					change = candidate
				}
			}
			if change == nil {
				continue
			}
			evidence := change.Evidence
			container.Rows = append(container.Rows, envresolve.Row{
				Name: variable, State: envresolve.ValueUnavailable, Sensitive: change.Source.Kind == "Secret",
				Source: change.Source, Message: "This source key was removed after the container started.", Evidence: &evidence,
			})
		}
		sort.Slice(container.Rows, func(a, b int) bool { return container.Rows[a].Name < container.Rows[b].Name })
	}
}

func rowUsesSource(row envresolve.Row, source envresolve.SourceRef) bool {
	refs := append([]envresolve.SourceRef{row.Source}, row.Dependencies...)
	for _, ref := range refs {
		if ref.Kind == source.Kind && ref.Name == source.Name && ref.Key == source.Key {
			return true
		}
	}
	return false
}

func secretAuditSource(row envresolve.Row) string {
	var refs []envresolve.SourceRef
	refs = append(refs, row.Source)
	refs = append(refs, row.Dependencies...)
	var sources []string
	for _, ref := range refs {
		if ref.Kind == "Secret" {
			sources = append(sources, fmt.Sprintf("Secret/%s[%s]", ref.Name, ref.Key))
		}
	}
	return strings.Join(sources, ",")
}

func (s *Server) writePodEnvironmentGetError(w http.ResponseWriter, namespace, name, kind string, err error) {
	switch {
	case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
		s.writeError(w, http.StatusForbidden, fmt.Sprintf("access denied reading %s/%s", kind, name))
	case apierrors.IsNotFound(err):
		s.writeError(w, http.StatusNotFound, fmt.Sprintf("%s/%s not found", kind, name))
	default:
		log.Printf("[environment] Failed to read %s %s/%s: %s", sanitizeForLog(kind), sanitizeForLog(namespace), sanitizeForLog(name), sanitizeForLog(err.Error()))
		s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to read %s", kind))
	}
}

func podEnvironmentReadOutcome(err error) string {
	switch {
	case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
		return "denied"
	case apierrors.IsNotFound(err):
		return "not-found"
	default:
		return "unavailable"
	}
}

func podEnvironmentRevealFailure(row envresolve.Row) (int, string, string) {
	message := row.Message
	switch row.State {
	case envresolve.ValueDenied:
		if message == "" {
			message = "access to the Secret-backed value is required"
		}
		return http.StatusForbidden, "denied", message
	case envresolve.ValueMissing:
		if message == "" {
			message = "Secret-backed value is missing"
		}
		return http.StatusNotFound, "not-found", message
	default:
		if message == "" {
			message = "Secret-backed value is unavailable"
		}
		return http.StatusServiceUnavailable, "unavailable", message
	}
}
