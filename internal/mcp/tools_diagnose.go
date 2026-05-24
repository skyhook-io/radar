package mcp

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/k8s"
	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

// diagnoseInput is the one-shot debug bundle request. Kind is restricted
// to pod / deployment / statefulset / daemonset because diagnose resolves
// a pod set (workload→selector→pods) for log fan-out; CRDs have no
// comparable pod resolution.
type diagnoseInput struct {
	Kind      string `json:"kind" jsonschema:"workload kind: pod, deployment, statefulset, or daemonset"`
	Namespace string `json:"namespace" jsonschema:"workload namespace"`
	Name      string `json:"name" jsonschema:"resource name"`
	Container string `json:"container,omitempty" jsonschema:"specific container; defaults to all containers across the workload's pods"`
	TailLines int    `json:"tail_lines,omitempty" jsonschema:"lines per pod/container per stream (current AND previous), default 100"`
	Since     string `json:"since,omitempty" jsonschema:"only fetch logs newer than this duration (e.g. 30s, 10m, 1h); empty = full available history"`
}

// diagnoseResponse is the bundled output. logsCurrent + logsPrevious are
// fanned out across the resolved pod set; events is recent dedup'd Warning
// events filtered to either the workload controller OR any of its pods.
// LogsError + EventsError distinguish "no logs/events exist" from "couldn't
// fetch them" (nil kube client, lister error). Without these fields, an
// agent reading empty arrays as ground truth would misdiagnose.
// NarrowHint is set when the resolved pod set was capped for log fan-out
// — see capDiagnosePods.
type diagnoseResponse struct {
	Resource        any                              `json:"resource"`
	ResourceContext *resourcecontext.ResourceContext `json:"resourceContext,omitempty"`
	LogsCurrent     []podLogEntry                    `json:"logsCurrent,omitempty"`
	LogsPrevious    []podLogEntry                    `json:"logsPrevious,omitempty"`
	LogsError       string                           `json:"logsError,omitempty"`
	Events          []aicontext.DeduplicatedEvent    `json:"events,omitempty"`
	EventsError     string                           `json:"eventsError,omitempty"`
	Pods            int                              `json:"pods"`
	NarrowHint      string                           `json:"narrowHint,omitempty"`
}

// maxDiagnosePods caps the log fan-out so large DaemonSets / Deployments
// don't trigger N × M concurrent apiserver /pods/{name}/log calls and an
// unbounded response. Chosen to comfortably cover typical Deployment
// replica counts (3–5) and small DaemonSets (one-per-node on a 10-node
// cluster) while still bounding the worst case.
const maxDiagnosePods = 10

// capDiagnosePods returns the subset of pods to fetch logs from when the
// resolved set is larger than the cap. Pods are sorted by total container
// restart count descending so the most-likely-broken ones are sampled
// first. Returns the (possibly trimmed) slice and a truncated flag.
func capDiagnosePods(pods []*corev1.Pod, cap int) ([]*corev1.Pod, bool) {
	if len(pods) <= cap {
		return pods, false
	}
	sorted := make([]*corev1.Pod, len(pods))
	copy(sorted, pods)
	sort.SliceStable(sorted, func(i, j int) bool {
		return podTotalRestarts(sorted[i]) > podTotalRestarts(sorted[j])
	})
	return sorted[:cap], true
}

func podTotalRestarts(p *corev1.Pod) int32 {
	if p == nil {
		return 0
	}
	var total int32
	for _, cs := range p.Status.ContainerStatuses {
		total += cs.RestartCount
	}
	for _, cs := range p.Status.InitContainerStatuses {
		total += cs.RestartCount
	}
	return total
}

func handleDiagnose(ctx context.Context, _ *mcp.CallToolRequest, input diagnoseInput) (*mcp.CallToolResult, any, error) {
	kindNorm := normalizeDiagnoseKind(input.Kind)
	if kindNorm == "" {
		return nil, nil, fmt.Errorf("invalid kind %q: must be pod, deployment, statefulset, or daemonset", input.Kind)
	}
	if input.Namespace == "" {
		return nil, nil, fmt.Errorf("namespace is required")
	}
	if input.Name == "" {
		return nil, nil, fmt.Errorf("name is required")
	}

	if !checkNamespaceAccess(ctx, input.Namespace) {
		return nil, nil, fmt.Errorf("forbidden: no access to namespace %q", input.Namespace)
	}

	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil, nil, fmt.Errorf("not connected to cluster")
	}

	obj, err := k8s.FetchResource(cache, kindNorm, input.Namespace, input.Name)
	if err != nil {
		return nil, nil, fmt.Errorf("resource not found: %w", err)
	}
	k8s.SetTypeMeta(obj)
	minified, err := aicontext.Minify(obj, aicontext.LevelDetail)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to minify: %w", err)
	}

	resCtx := buildMCPResourceContext(ctx, obj, kindNorm, input.Namespace, input.Name, resourcecontext.TierDiagnostic)

	pods, err := resolveDiagnosePods(cache, kindNorm, input.Namespace, input.Name, obj)
	if err != nil {
		return nil, nil, err
	}

	tailLines := int64(100)
	if input.TailLines > 0 {
		tailLines = int64(input.TailLines)
	}
	if tailLines > 1000 {
		tailLines = 1000
	}

	sinceSeconds, err := parseLogsSince(input.Since)
	if err != nil {
		return nil, nil, err
	}

	resp := diagnoseResponse{
		Resource:        minified,
		ResourceContext: resCtx,
		Pods:            len(pods),
	}

	// Cap the log fan-out so a DaemonSet with 50 nodes doesn't trigger
	// 50 × N containers × 2 (current + previous) concurrent apiserver
	// /pods/{name}/log requests and a multi-MB response. Sample the
	// "most likely broken" pods first by total restart count — the
	// failing pods are usually the ones a debugger wants logs from
	// anyway. Emit a narrowHint so the caller knows to drill down via
	// kind=pod + specific pod name when they want full coverage.
	logPods, logsTruncated := capDiagnosePods(pods, maxDiagnosePods)

	// Fan out current + previous in parallel — previous is expected to error
	// for healthy pods (no previous container instance); fetchPodLogs records
	// per-entry Error so the caller can see which streams failed without
	// blocking the whole diagnose call. When the kube client is unavailable
	// (auth drop, expired token, missing rest.Config), we surface that as
	// LogsError instead of silently returning empty arrays — without it the
	// agent can't distinguish "no logs" from "couldn't fetch logs."
	if len(logPods) > 0 {
		if k8s.ClientFromContext(ctx) == nil {
			resp.LogsError = "no kube client on context — logs unavailable for this request"
		} else {
			var (
				current, previous []podLogEntry
				wg                sync.WaitGroup
			)
			wg.Add(2)
			go func() {
				defer wg.Done()
				current = fetchPodLogs(ctx, logPods, input.Namespace, input.Container, "", tailLines, sinceSeconds, false)
			}()
			go func() {
				defer wg.Done()
				previous = fetchPodLogs(ctx, logPods, input.Namespace, input.Container, "", tailLines, sinceSeconds, true)
			}()
			wg.Wait()
			resp.LogsCurrent = current
			resp.LogsPrevious = previous
		}
	}
	if logsTruncated {
		resp.NarrowHint = fmt.Sprintf(
			"workload has %d pods; sampled top %d by restart count for logs — for full coverage, call diagnose with kind=pod and a specific pod name, or fall back to get_workload_logs which fans out across all pods",
			len(pods), len(logPods),
		)
	}

	events, eventsErr := fetchEventsForResource(cache, kindNorm, input.Namespace, input.Name, pods, 10)
	resp.Events = events
	if eventsErr != nil {
		resp.EventsError = eventsErr.Error()
	}
	return toJSONResult(resp)
}

// normalizeDiagnoseKind accepts pod/deployment/statefulset/daemonset in any
// singular/plural form and returns the plural cache form. Empty return means
// unsupported. Delegates to normalizeWorkloadKind for the workload kinds so
// the canonical mapping lives in one place.
func normalizeDiagnoseKind(kind string) string {
	if s := strings.ToLower(strings.TrimSpace(kind)); s == "pod" || s == "pods" {
		return "pods"
	}
	return normalizeWorkloadKind(kind)
}

// resolveDiagnosePods returns the set of pods to fetch logs from. For
// kind=pods that's just the requested pod; for workload kinds it resolves
// via the workload's pod selector and the cache's pod-by-workload index.
func resolveDiagnosePods(cache *k8s.ResourceCache, kindNorm, namespace, name string, obj any) ([]*corev1.Pod, error) {
	if kindNorm == "pods" {
		pod, ok := obj.(*corev1.Pod)
		if !ok || pod == nil {
			return nil, fmt.Errorf("resolved object is not a Pod")
		}
		return []*corev1.Pod{pod}, nil
	}
	selector, err := k8s.GetWorkloadSelector(cache, kindNorm, namespace, name)
	if err != nil {
		return nil, err
	}
	return cache.GetPodsForWorkload(namespace, selector), nil
}

// fetchEventsForResource returns up to `limit` recent dedup'd events
// involving this resource. When pods is non-empty, also matches pod-level
// events on any of those pods — the operator-relevant events
// (CrashLoopBackOff, ImagePullBackOff, FailedScheduling) fire on the Pods,
// not the controller, so a workload-rooted diagnose without pod-level
// events would miss its headline cases. The error return distinguishes
// "no warnings exist" from "apiserver list failed and we couldn't tell"
// — diagnose surfaces it as EventsError so the agent doesn't read empty
// events as ground truth.
func fetchEventsForResource(cache *k8s.ResourceCache, kind, namespace, name string, pods []*corev1.Pod, limit int) ([]aicontext.DeduplicatedEvent, error) {
	eventLister := cache.Events()
	if eventLister == nil {
		// Mirror attachResourceExtras / get_resource(include=events): surface
		// "couldn't load" rather than returning empty, so handleDiagnose sets
		// EventsError and agents don't read silence as "no warnings."
		return nil, fmt.Errorf("events lister unavailable (insufficient permissions or cache cold)")
	}
	events, err := eventLister.Events(namespace).List(labels.Everything())
	if err != nil {
		log.Printf("[mcp] diagnose: failed to list events for %s/%s/%s: %v", kind, namespace, name, err)
		return nil, err
	}
	podNames := make(map[string]bool, len(pods))
	for _, p := range pods {
		if p != nil {
			podNames[p.Name] = true
		}
	}
	matched := filterEventsByInvolvedObject(events, normalizeDisplayKind(kind), name, podNames)
	if len(matched) == 0 {
		return nil, nil
	}
	dedup := aicontext.DeduplicateEvents(matched)
	if limit > 0 && len(dedup) > limit {
		dedup = dedup[:limit]
	}
	return dedup, nil
}

// filterEventsByInvolvedObject keeps Warning events whose InvolvedObject
// matches either the controller (displayKind+name) OR any of the pods in
// podNames (skipped when displayKind is "Pod" — the controller branch
// above already covers single-pod and otherwise this branch would
// double-count).
//
// Filters to Type==Warning intentionally — the diagnose tool description
// + get_resource(include=events) both promise warning events only.
// Normal events (Pulled / Created / Scheduled) would pollute triage by
// reading as "things worth diagnosing" when they're just lifecycle
// breadcrumbs.
//
// Shared between diagnose (passes resolved pod names for full workload
// coverage) and attachResourceExtras / get_resource include=events
// (passes nil — sidecar fetch; callers wanting pod-level events should
// use the diagnose tool which does the workload→pods resolution).
func filterEventsByInvolvedObject(events []*corev1.Event, displayKind, name string, podNames map[string]bool) []corev1.Event {
	var matched []corev1.Event
	for _, e := range events {
		if e.Type != corev1.EventTypeWarning {
			continue
		}
		if strings.EqualFold(e.InvolvedObject.Kind, displayKind) && e.InvolvedObject.Name == name {
			matched = append(matched, *e)
			continue
		}
		if displayKind != "Pod" && strings.EqualFold(e.InvolvedObject.Kind, "Pod") && podNames[e.InvolvedObject.Name] {
			matched = append(matched, *e)
		}
	}
	return matched
}
