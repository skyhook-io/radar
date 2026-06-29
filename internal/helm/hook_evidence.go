package helm

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
	"github.com/skyhook-io/radar/pkg/k8score"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

const (
	maxHookEvidencePods       = 2
	maxHookEvidenceEvents     = 6
	maxHookEvidenceContainers = 3
	maxHookEvidenceLogs       = 4
	hookLogTailLines          = 80
	hookLogReadLimitBytes     = 128 * 1024
	hookLogTimeout            = 4 * time.Second
)

type hookObjectRef struct {
	kind string
	name string
}

// EnrichHookDiagnosticsWithClusterEvidence attaches live Job/Pod/Event/log clues
// for failed or running Helm hooks. It uses the caller's Kubernetes client so
// auth-enabled deployments keep Kubernetes RBAC as the source of truth.
func EnrichHookDiagnosticsWithClusterEvidence(ctx context.Context, detail *HelmReleaseDetail, client kubernetes.Interface) {
	if detail == nil || len(detail.HookDiagnostics) == 0 {
		return
	}
	if client == nil {
		for i := range detail.HookDiagnostics {
			diag := &detail.HookDiagnostics[i]
			diag.EvidenceUnavailable = true
			diag.EvidenceUnavailableReason = "Radar could not read live hook evidence because no Kubernetes client was available for this request."
		}
		return
	}

	hooks := make(map[string]HelmHook, len(detail.Hooks)*2)
	for _, hook := range detail.Hooks {
		hooks[hookDiagnosticKey(hook.Namespace, hook.Kind, hook.Name)] = hook
		hooks[hookDiagnosticKey("", hook.Kind, hook.Name)] = hook
	}

	for i := range detail.HookDiagnostics {
		diag := &detail.HookDiagnostics[i]
		namespace := diag.Namespace
		if namespace == "" {
			namespace = detail.Namespace
		}
		hook, ok := hooks[hookDiagnosticKey(namespace, diag.Kind, diag.Name)]
		if !ok {
			hook = hooks[hookDiagnosticKey("", diag.Kind, diag.Name)]
		}
		if hook.Name == "" {
			hook = HelmHook{
				Name:      diag.Name,
				Namespace: namespace,
				Kind:      diag.Kind,
				Events:    diag.Events,
				Status:    diag.Phase,
			}
		}
		if hook.Namespace == "" {
			hook.Namespace = namespace
		}

		evidence := collectHookEvidence(ctx, client, hook)
		if evidence.hasData() {
			diag.Evidence = &evidence
		}
		if evidence.hasLiveEvidence() {
			diag.EvidenceUnavailable = false
			diag.EvidenceUnavailableReason = ""
			continue
		}
		if hasPrimaryHookEvidenceError(evidence.Errors) {
			diag.EvidenceUnavailable = true
			diag.EvidenceUnavailableReason = "Radar could not read live hook evidence with the current Kubernetes identity."
			continue
		}
		if diag.EvidenceUnavailable {
			continue
		}
		diag.EvidenceUnavailable = true
		diag.EvidenceUnavailableReason = "No live Job/Pod evidence found for this hook; it may have been deleted by a hook policy, TTL controller, or garbage collection."
	}
}

func hookDiagnosticKey(namespace, kind, name string) string {
	return namespace + "/" + strings.ToLower(kind) + "/" + name
}

func collectHookEvidence(ctx context.Context, client kubernetes.Interface, hook HelmHook) HookEvidence {
	namespace := hook.Namespace
	evidence := HookEvidence{}
	refs := []hookObjectRef{{kind: hook.Kind, name: hook.Name}}
	var pods []*corev1.Pod

	switch strings.ToLower(hook.Kind) {
	case "job":
		job, err := client.BatchV1().Jobs(namespace).Get(ctx, hook.Name, metav1.GetOptions{})
		if err == nil {
			evidence.Jobs = append(evidence.Jobs, hookJobEvidence(job))
			refs = append(refs, hookObjectRef{kind: "Job", name: job.Name})
		} else if !apierrors.IsNotFound(err) {
			evidence.Errors = append(evidence.Errors, "job: "+compactHookEvidenceError(err))
		}
		jobPods, errs := listHookPodsForJob(ctx, client, namespace, hook.Name)
		pods = append(pods, jobPods...)
		evidence.Errors = append(evidence.Errors, errs...)
	case "pod":
		pod, err := client.CoreV1().Pods(namespace).Get(ctx, hook.Name, metav1.GetOptions{})
		if err == nil {
			pods = append(pods, pod)
		} else if !apierrors.IsNotFound(err) {
			evidence.Errors = append(evidence.Errors, "pod: "+compactHookEvidenceError(err))
		}
	}

	pods = dedupeAndSortPods(pods)
	for _, pod := range pods {
		evidence.Pods = append(evidence.Pods, hookPodEvidence(pod))
		refs = append(refs, hookObjectRef{kind: "Pod", name: pod.Name})
	}

	events, eventErr := listHookEvents(ctx, client, namespace, refs)
	evidence.Events = events
	if eventErr != "" {
		evidence.Errors = append(evidence.Errors, eventErr)
	}

	logs := collectHookLogs(ctx, client, namespace, pods)
	evidence.Logs = append(evidence.Logs, logs...)
	evidence.Errors = compactHookEvidenceErrors(evidence.Errors)
	evidence.Summary = summarizeHookEvidence(evidence)
	return evidence
}

func hookJobEvidence(job *batchv1.Job) HookJobEvidence {
	out := HookJobEvidence{
		Name:      job.Name,
		Namespace: job.Namespace,
		Active:    job.Status.Active,
		Succeeded: job.Status.Succeeded,
		Failed:    job.Status.Failed,
		Status:    "unknown",
	}
	if job.Status.Active > 0 {
		out.Status = "active"
	}
	if job.Status.Succeeded > 0 {
		out.Status = "succeeded"
	}
	if job.Status.Failed > 0 {
		out.Status = "failed"
	}
	for _, cond := range job.Status.Conditions {
		if cond.Type == batchv1.JobComplete && cond.Status == corev1.ConditionTrue {
			out.Status = "succeeded"
		}
		if cond.Type == batchv1.JobFailed && cond.Status == corev1.ConditionTrue {
			out.Status = "failed"
		}
		parts := []string{string(cond.Type) + "=" + string(cond.Status)}
		if cond.Reason != "" {
			parts = append(parts, cond.Reason)
		}
		if cond.Message != "" {
			parts = append(parts, truncateHookText(aicontext.RedactSecrets(cond.Message), 220))
		}
		out.Conditions = append(out.Conditions, strings.Join(parts, ": "))
	}
	return out
}

func hookPodEvidence(pod *corev1.Pod) HookPodEvidence {
	out := HookPodEvidence{
		Name:      pod.Name,
		Namespace: pod.Namespace,
		Phase:     string(pod.Status.Phase),
		Reason:    pod.Status.Reason,
		Message:   truncateHookText(aicontext.RedactSecrets(pod.Status.Message), 220),
	}
	var ready, total int
	consider := func(status corev1.ContainerStatus) {
		total++
		if status.Ready {
			ready++
		}
		out.RestartCount += status.RestartCount
		if out.Reason == "" {
			if status.State.Waiting != nil {
				out.Reason = status.State.Waiting.Reason
				out.Message = truncateHookText(aicontext.RedactSecrets(status.State.Waiting.Message), 220)
			} else if status.State.Terminated != nil && status.State.Terminated.ExitCode != 0 {
				out.Reason = status.State.Terminated.Reason
				out.Message = truncateHookText(aicontext.RedactSecrets(status.State.Terminated.Message), 220)
			}
		}
	}
	for _, status := range pod.Status.InitContainerStatuses {
		consider(status)
	}
	for _, status := range pod.Status.ContainerStatuses {
		consider(status)
	}
	if total > 0 {
		out.Ready = fmt.Sprintf("%d/%d", ready, total)
	}
	return out
}

func listHookPodsForJob(ctx context.Context, client kubernetes.Interface, namespace, jobName string) ([]*corev1.Pod, []string) {
	var pods []*corev1.Pod
	var errs []string
	selectors := []string{
		labels.Set{"job-name": jobName}.String(),
		labels.Set{"batch.kubernetes.io/job-name": jobName}.String(),
	}
	for _, selector := range selectors {
		list, err := client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			if !apierrors.IsNotFound(err) {
				errs = append(errs, "pods: "+compactHookEvidenceError(err))
			}
			continue
		}
		for i := range list.Items {
			pod := list.Items[i]
			pods = append(pods, &pod)
		}
	}
	return dedupeAndSortPods(pods), compactHookEvidenceErrors(errs)
}

func dedupeAndSortPods(pods []*corev1.Pod) []*corev1.Pod {
	seen := map[string]bool{}
	out := make([]*corev1.Pod, 0, len(pods))
	for _, pod := range pods {
		if pod == nil {
			continue
		}
		key := pod.Namespace + "/" + pod.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, pod)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Namespace+"/"+out[i].Name < out[j].Namespace+"/"+out[j].Name
	})
	if len(out) > maxHookEvidencePods {
		out = out[:maxHookEvidencePods]
	}
	return out
}

func listHookEvents(ctx context.Context, client kubernetes.Interface, namespace string, refs []hookObjectRef) ([]HookEventEvidence, string) {
	if len(refs) == 0 {
		return nil, ""
	}
	want := map[string]bool{}
	for _, ref := range refs {
		if ref.kind != "" && ref.name != "" {
			want[strings.ToLower(ref.kind)+"/"+ref.name] = true
		}
	}
	list, err := client.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, ""
		}
		return nil, "events: " + compactHookEvidenceError(err)
	}
	events := make([]corev1.Event, 0)
	for _, event := range list.Items {
		key := strings.ToLower(event.InvolvedObject.Kind) + "/" + event.InvolvedObject.Name
		if want[key] {
			events = append(events, event)
		}
	}
	sort.SliceStable(events, func(i, j int) bool {
		leftWarning := events[i].Type == corev1.EventTypeWarning
		rightWarning := events[j].Type == corev1.EventTypeWarning
		if leftWarning != rightWarning {
			return leftWarning
		}
		return hookEventTime(events[i]).After(hookEventTime(events[j]))
	})
	if len(events) > maxHookEvidenceEvents {
		events = events[:maxHookEvidenceEvents]
	}
	out := make([]HookEventEvidence, 0, len(events))
	for _, event := range events {
		out = append(out, HookEventEvidence{
			InvolvedKind: event.InvolvedObject.Kind,
			InvolvedName: event.InvolvedObject.Name,
			Type:         event.Type,
			Reason:       event.Reason,
			Message:      truncateHookText(aicontext.RedactSecrets(event.Message), 260),
			Count:        event.Count,
			LastSeen:     formatHookTime(hookEventTime(event)),
		})
	}
	return out, ""
}

func hookEventTime(event corev1.Event) time.Time {
	if !event.EventTime.Time.IsZero() {
		return event.EventTime.Time
	}
	if !event.LastTimestamp.Time.IsZero() {
		return event.LastTimestamp.Time
	}
	return event.FirstTimestamp.Time
}

func collectHookLogs(ctx context.Context, client kubernetes.Interface, namespace string, pods []*corev1.Pod) []HookLogEvidence {
	logs := []HookLogEvidence{}
	for _, pod := range pods {
		if len(logs) >= maxHookEvidenceLogs {
			break
		}
		for _, container := range hookLogContainerNames(pod) {
			if len(logs) >= maxHookEvidenceLogs {
				break
			}
			if log, ok := fetchHookLog(ctx, client, namespace, pod.Name, container, false); ok {
				logs = append(logs, log)
			}
			if len(logs) >= maxHookEvidenceLogs {
				break
			}
			if hookContainerRestartCount(pod, container) > 0 {
				if log, ok := fetchHookLog(ctx, client, namespace, pod.Name, container, true); ok {
					logs = append(logs, log)
				}
			}
		}
	}
	return logs
}

func hookLogContainerNames(pod *corev1.Pod) []string {
	if pod == nil {
		return nil
	}
	priority := map[string]int{}
	addPriority := func(status corev1.ContainerStatus) {
		if status.State.Waiting != nil || status.State.Terminated != nil || status.RestartCount > 0 || !status.Ready {
			priority[status.Name] = 0
		}
	}
	for _, status := range pod.Status.InitContainerStatuses {
		addPriority(status)
	}
	for _, status := range pod.Status.ContainerStatuses {
		addPriority(status)
	}
	for _, c := range pod.Spec.InitContainers {
		if _, ok := priority[c.Name]; !ok {
			priority[c.Name] = 1
		}
	}
	for _, c := range pod.Spec.Containers {
		if _, ok := priority[c.Name]; !ok {
			priority[c.Name] = 1
		}
	}
	names := make([]string, 0, len(priority))
	for name := range priority {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if priority[names[i]] != priority[names[j]] {
			return priority[names[i]] < priority[names[j]]
		}
		return names[i] < names[j]
	})
	if len(names) > maxHookEvidenceContainers {
		names = names[:maxHookEvidenceContainers]
	}
	return names
}

func hookContainerRestartCount(pod *corev1.Pod, name string) int32 {
	if pod == nil {
		return 0
	}
	for _, status := range pod.Status.InitContainerStatuses {
		if status.Name == name {
			return status.RestartCount
		}
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == name {
			return status.RestartCount
		}
	}
	return 0
}

func fetchHookLog(ctx context.Context, client kubernetes.Interface, namespace, podName, container string, previous bool) (HookLogEvidence, bool) {
	log := HookLogEvidence{Pod: podName, Container: container, Previous: previous}
	tailLines := int64(hookLogTailLines)
	logCtx, cancel := context.WithTimeout(ctx, hookLogTimeout)
	defer cancel()

	stream, err := k8score.GetContainerLogs(logCtx, client, namespace, podName, container, k8score.LogOptions{
		TailLines: &tailLines,
		Previous:  previous,
	})
	if err != nil {
		log.Error = compactHookEvidenceError(err)
		return log, true
	}
	defer stream.Close()

	data, err := io.ReadAll(io.LimitReader(stream, hookLogReadLimitBytes))
	if err != nil {
		log.Error = compactHookEvidenceError(err)
		return log, true
	}
	filtered := aicontext.FilterLogs(string(data))
	if len(filtered.Lines) == 0 {
		return log, false
	}
	log.Lines = filtered.Lines
	log.TotalLines = filtered.TotalLines
	log.MatchedLines = filtered.MatchedLines
	log.Fallback = filtered.Fallback
	return log, true
}

func (e HookEvidence) hasData() bool {
	return e.Summary != "" || len(e.Jobs) > 0 || len(e.Pods) > 0 || len(e.Events) > 0 || len(e.Logs) > 0 || len(e.Errors) > 0
}

func (e HookEvidence) hasLiveEvidence() bool {
	if len(e.Jobs) > 0 || len(e.Pods) > 0 || len(e.Events) > 0 {
		return true
	}
	for _, log := range e.Logs {
		if len(log.Lines) > 0 {
			return true
		}
	}
	return false
}

func hasPrimaryHookEvidenceError(errors []string) bool {
	for _, err := range errors {
		if strings.HasPrefix(err, "job:") || strings.HasPrefix(err, "pod:") || strings.HasPrefix(err, "pods:") {
			return true
		}
	}
	return false
}

func summarizeHookEvidence(e HookEvidence) string {
	parts := []string{}
	if len(e.Jobs) > 0 {
		parts = append(parts, fmt.Sprintf("%d job%s", len(e.Jobs), pluralSuffix(len(e.Jobs))))
	}
	if len(e.Pods) > 0 {
		parts = append(parts, fmt.Sprintf("%d pod%s", len(e.Pods), pluralSuffix(len(e.Pods))))
	}
	if len(e.Events) > 0 {
		parts = append(parts, fmt.Sprintf("%d event%s", len(e.Events), pluralSuffix(len(e.Events))))
	}
	logsWithLines := 0
	for _, log := range e.Logs {
		if len(log.Lines) > 0 {
			logsWithLines++
		}
	}
	if logsWithLines > 0 {
		parts = append(parts, fmt.Sprintf("%d log snippet%s", logsWithLines, pluralSuffix(logsWithLines)))
	}
	if len(parts) == 0 {
		return ""
	}
	return "Found " + strings.Join(parts, ", ") + "."
}

func pluralSuffix(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

func compactHookEvidenceError(err error) string {
	if err == nil {
		return ""
	}
	if apierrors.IsForbidden(err) {
		return "forbidden: current identity cannot read this hook evidence"
	}
	if apierrors.IsNotFound(err) {
		return "not found"
	}
	if apierrors.IsUnauthorized(err) {
		return "unauthorized: current identity cannot read this hook evidence"
	}
	return truncateHookText(err.Error(), 240)
}

func compactHookEvidenceErrors(errors []string) []string {
	if len(errors) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(errors))
	for _, err := range errors {
		err = strings.TrimSpace(err)
		if err == "" || seen[err] {
			continue
		}
		seen[err] = true
		out = append(out, err)
	}
	if len(out) > 4 {
		out = out[:4]
	}
	return out
}

func truncateHookText(value string, maxLen int) string {
	value = strings.TrimSpace(value)
	if maxLen <= 0 || len(value) <= maxLen {
		return value
	}
	if maxLen <= 3 {
		return value[:maxLen]
	}
	return value[:maxLen-3] + "..."
}

func formatHookTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}
