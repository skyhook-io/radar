package mcp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/meaningfulchanges"
	"github.com/skyhook-io/radar/internal/reachability"
	"github.com/skyhook-io/radar/internal/trace"
	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/probe"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

// diagnoseCommonInput is the non-mutating diagnose contract shared by both MCP
// endpoints. Workloads resolve to a pod set for log fan-out; GitOps reconcilers
// take a no-pods status path.
type diagnoseCommonInput struct {
	Kind      string `json:"kind" jsonschema:"kind to diagnose: a workload (pod, deployment, statefulset, daemonset) for logs+events+startup blockers, a GitOps reconciler (application, kustomization, Flux HelmRelease) for sync/health summary + parsed failure cause, or a network entry kind (service, ingress, httproute, grpcroute, gateway) for a path-shaped trace of which hop drops traffic"`
	Probe     bool   `json:"probe,omitempty" jsonschema:"active reachability test for network entry kinds: when true, augment the static trace with DNS/TCP/TLS/HTTP probes as applicable. Explicitly non-HTTP Service ports stop at TCP; Radar does not send them an unrelated HTTP request. Uses direct TCP when radar is in-cluster, K8s API server proxy from a laptop - the same call works either way. Probes can escalate the static verdict when failures are unanimous on a hop, but never soften broken or unknown. Probe failures attributable to the vantage (e.g. NetworkPolicy blocking radar's path) can produce false-positive escalations; the per-hop chip carries the granular signal. Costs 0-3s wall time. No effect for non-network kinds."`
	Namespace string `json:"namespace" jsonschema:"resource namespace"`
	Name      string `json:"name" jsonschema:"resource name"`
	Container string `json:"container,omitempty" jsonschema:"specific container; defaults to all containers across the workload's pods"`
	TailLines int    `json:"tail_lines,omitempty" jsonschema:"lines per pod/container per stream (current AND previous), default 100"`
	Since     string `json:"since,omitempty" jsonschema:"only fetch logs newer than this duration (e.g. 30s, 10m, 1h); empty = full available history"`
}

// diagnoseInput is the full /mcp contract. InCluster is the only mutating
// option and therefore does not exist in diagnoseReadOnlyInput.
type diagnoseInput struct {
	diagnoseCommonInput
	InCluster bool `json:"in_cluster,omitempty" jsonschema:"run the reachability probe from INSIDE the cluster (real dataplane), not from where radar runs. HTTP(S) routes test their declared request; explicitly non-HTTP Service ports use TCP only. Radar creates UP TO 5 short-lived, self-destructing probe pods (one per intended route, run sequentially) under YOUR RBAC (restricted, non-root, no service-account token); each runs the probe and is deleted within ~60s. USE THIS when a prior diagnose (with probe=true) returned a route with confidence:indirect or verdict:unknown - i.e. 'reached via API server, real-traffic path NOT confirmed' - and you need to confirm the live path; or to test from where NetworkPolicy / service-mesh mTLS actually applies, which the apiserver-proxy vantage cannot. EFFECT: this CREATES pods (the only mutating diagnose option); it needs create-jobs + list-pods + get-pods/log RBAC in the namespace. On success the tested route's confidence becomes 'real' and the verdict/headline reflect the live result. For a front-doored route the in-cluster dial bypasses the entry: rows carry segment:'backend' and the headline notes the entry path was not exercised - not proof of the whole route; if a probe pod can't start (unschedulable, image pull, an admission webhook injecting init containers) you get a plain-English reason; if you can't create pods you get a copyable kubectl command to run by hand. Runtime scales with the number of routes (each probe pod can take a few seconds, up to ~5x). probe=true is forced on when this is set; only meaningful for network entry kinds."`
}

// diagnoseReadOnlyInput is the strict /mcp-readonly contract. Schema validation
// rejects in_cluster because the field is absent.
type diagnoseReadOnlyInput struct {
	diagnoseCommonInput
}

func handleDiagnoseReadOnly(ctx context.Context, req *mcp.CallToolRequest, input diagnoseReadOnlyInput) (*mcp.CallToolResult, any, error) {
	return handleDiagnose(ctx, req, diagnoseInput{diagnoseCommonInput: input.diagnoseCommonInput})
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
	Resource            any                              `json:"resource"`
	ResourceContext     *resourcecontext.ResourceContext `json:"resourceContext,omitempty"`
	LogsCurrent         []podLogEntry                    `json:"logsCurrent,omitempty"`
	LogsPrevious        []podLogEntry                    `json:"logsPrevious,omitempty"`
	CrashCause          []diagnoseCrashCause             `json:"crashCause,omitempty"`
	CrashCauseTruncated bool                             `json:"crashCauseTruncated,omitempty"`
	LogsError           string                           `json:"logsError,omitempty"`
	Events              []aicontext.DeduplicatedEvent    `json:"events,omitempty"`
	EventsError         string                           `json:"eventsError,omitempty"`
	// StartupBlockers carries why the workload can't reach Running when that's
	// the failure mode, spanning the whole pre-Running path: unschedulable pods
	// (offending node constraint named), admission rejections (quota/
	// PodSecurity/webhook — where no Pod is created), or post-bind CNI/volume
	// stalls. Empty when the workload starts fine. Named for the symptom
	// ("can't start"), not the subsystem — "scheduling" alone would mislead,
	// since it also covers admission and post-bind.
	StartupBlockers []startupBlocker `json:"startupBlockers,omitempty"`
	// RelatedIssues is what Radar's issues engine already classified for this
	// object: the grouped issues whose subject OR an affected member is the
	// diagnosed resource (crashloop, missing refs, HPA can't-scale, GitOps
	// failure, …). Saves the agent re-deriving from raw logs/events what the
	// issue engine knows. Empty when nothing is wrong.
	RelatedIssues []issues.Issue           `json:"relatedIssues,omitempty"`
	ChangeContext *issuesapi.ChangeContext `json:"changeContext,omitempty"`
	RecentChanges []issuesapi.RecentChange `json:"recentChanges,omitempty"`
	// DNSContext is attached only when this diagnosed resource shows DNS
	// symptoms or has non-default DNS settings. It includes cluster DNS facts
	// without adding one kube-system issue to every namespaced issue list.
	DNSContext *diagnoseDNSContext `json:"dnsContext,omitempty"`
	Pods       int                 `json:"pods"`
	NarrowHint string              `json:"narrowHint,omitempty"`
	// Warnings are state-derived advisories on the diagnosed object — e.g.,
	// "resource is being deleted", "managed by Helm, edits may revert",
	// "condition has been False since creation". Empty when nothing notable.
	Warnings []string `json:"warnings,omitempty"`
	// GitOpsDiagnosis is set only for GitOps reconcilers (Argo Application /
	// Flux Kustomization / HelmRelease), which have no pods — see gitopsDiagnosis.
	GitOpsDiagnosis *gitopsDiagnosis `json:"gitopsDiagnosis,omitempty"`
}

// gitopsDiagnosis is the status summary for a GitOps reconciler. The actionable
// cause/remediation is NOT duplicated here — it flows via diagnoseResponse.
// RelatedIssues, which carries the parsed gitops_* issue for the same object.
type gitopsDiagnosis struct {
	Tool           string `json:"tool"`                     // argocd | flux
	Sync           string `json:"sync,omitempty"`           // Argo status.sync.status
	Health         string `json:"health,omitempty"`         // Argo status.health.status
	OperationPhase string `json:"operationPhase,omitempty"` // Argo status.operationState.phase
	// Flux equivalents — a Kustomization/HelmRelease has no Argo sync/health
	// rollup, so summarize from the Ready condition, suspend flag, and the last
	// successfully applied revision instead.
	Suspended bool   `json:"suspended,omitempty"`       // Flux spec.suspend
	Ready     string `json:"ready,omitempty"`           // Flux Ready condition: "<status>: <reason>"
	Revision  string `json:"appliedRevision,omitempty"` // Flux status.lastAppliedRevision
}

// startupBlocker is the compact row diagnose embeds for one reason a workload
// can't reach Running — the same signal the issues tool emits, scoped here to
// this workload (bind-time, admission, or post-bind).
type startupBlocker struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Reason   string `json:"reason"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

type diagnoseDNSContext struct {
	Signals         []string             `json:"signals,omitempty"`
	CoreDNSFindings []diagnoseDNSFinding `json:"coreDNSFindings,omitempty"`
}

type diagnoseDNSFinding struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Severity  string `json:"severity"`
	Reason    string `json:"reason"`
	Message   string `json:"message,omitempty"`
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
	if input.Namespace == "" {
		return nil, nil, fmt.Errorf("namespace is required")
	}
	if input.Name == "" {
		return nil, nil, fmt.Errorf("name is required")
	}
	// GitOps reconcilers (Argo Application / Flux Kustomization / HelmRelease)
	// have no pods, so they take a dedicated path: reconciler status summary +
	// the parsed failure issue (via RelatedIssues), no log/pod fan-out.
	if gk, group, resource, tool, ok := gitopsDiagnoseTarget(input.Kind); ok {
		return handleGitOpsDiagnose(ctx, input, gk, group, resource, tool)
	}
	// Network entry kinds get a path-shaped trace instead of pod-log fan-out
	// - "this Service is broken because the upstream Route's parent Gateway
	// is not Accepted" is a different shape of answer than logs+events. See
	// internal/trace.
	if traceKind, ok := networkTraceKind(input.Kind); ok {
		return handleNetworkTraceDiagnose(ctx, input, traceKind)
	}
	kindNorm := normalizeDiagnoseKind(input.Kind)
	if kindNorm == "" {
		return nil, nil, fmt.Errorf("invalid kind %q: must be pod, deployment, statefulset, daemonset, application, kustomization, Flux HelmRelease, or a network entry kind (service, ingress, httproute, grpcroute, gateway)", input.Kind)
	}

	if !checkNamespaceAccess(ctx, input.Namespace) {
		return nil, nil, fmt.Errorf("forbidden: no access to namespace %q", input.Namespace)
	}

	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil, nil, errNotConnected()
	}

	obj, err := k8s.FetchResource(cache, kindNorm, input.Namespace, input.Name)
	if err != nil {
		return nil, nil, notFoundError(ctx, err, kindNorm, input.Namespace, input.Name)
	}
	k8s.SetTypeMeta(obj)
	gvk := obj.GetObjectKind().GroupVersionKind()
	canonicalGroup := gvk.Group
	canonicalKind := gvk.Kind
	if canonicalKind == "" {
		canonicalKind = kindNorm
	}
	minified, err := aicontext.Minify(obj, aicontext.LevelDetail)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to minify: %w", err)
	}

	pods, err := resolveDiagnosePods(cache, kindNorm, input.Namespace, input.Name, obj)
	if err != nil {
		return nil, nil, err
	}
	resCtx := buildMCPResourceContextWithStaleChecks(
		ctx,
		obj,
		kindNorm,
		input.Namespace,
		input.Name,
		resourcecontext.TierDiagnostic,
		k8s.FindStaleSecretEnvChecksForPods(ctx, cache, pods),
	)

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
		// Surface the issues Radar already classified for this object (subject
		// or affected member), scoped to its namespace — so the agent sees
		// "crashloop + missing ConfigMap" up front, not just raw logs.
		RelatedIssues: issues.RelatedIssues(issues.NewCacheProvider(), issues.RelatedIssueOptions{
			Namespaces:           issueNamespacesForResource(input.Namespace),
			CanReadClusterScoped: issueClusterScopedAccess(ctx),
			CanReadRelated:       issueRelatedResourceAccess(ctx),
		}, canonicalGroup, canonicalKind, input.Namespace, input.Name),
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
			resp.CrashCause, resp.CrashCauseTruncated = crashCauseForDiagnose(logPods, current, previous, time.Now())
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

	resp.StartupBlockers = startupBlockersForWorkload(cache, kindNorm, input.Namespace, input.Name, pods)
	if len(resp.RelatedIssues) > 0 || len(resp.StartupBlockers) > 0 {
		if p := issues.NewCacheProvider(); p != nil {
			resp.ChangeContext = p.ChangeContextForIssue(issues.Issue{
				Group:     canonicalGroup,
				Kind:      canonicalKind,
				Namespace: input.Namespace,
				Name:      input.Name,
			})
		}
	}
	if changes, _, err := meaningfulchanges.RecentForWorkloadAndConfigMaps(ctx, obj, kindNorm, input.Namespace, input.Name, meaningfulchanges.DefaultSince, meaningfulchanges.ResourceLimit, meaningfulchanges.DefaultFieldLimit); err == nil && len(changes) > 0 {
		// Per-kind RBAC: the result includes consumed ConfigMaps the caller may
		// not be able to read even when authorized on the workload subject.
		resp.RecentChanges = filterRecentChangesRBAC(ctx, changes)
	}
	resp.DNSContext = dnsContextForDiagnose(ctx, cache, obj, pods, resp.LogsCurrent, resp.LogsPrevious, resp.Events)
	resp.Warnings = k8score.EnrichRuntimeObjectWarnings(obj)
	capped, capStats := capMultiPodLogBundles(resp.LogsCurrent, resp.LogsPrevious)
	resp.LogsCurrent = capped[0]
	resp.LogsPrevious = capped[1]
	if capStats.Truncated {
		capHint := multiPodLogBundleNarrowHint(input.Namespace, capStats, capStats.FirstOmittedBundle == 1)
		if logsTruncated {
			resp.NarrowHint = fmt.Sprintf(
				"workload has %d pods; sampled top %d by restart count for logs — for a pod outside the sample, call diagnose with kind=pod and its name; %s",
				len(pods), len(logPods), capHint,
			)
		} else {
			resp.NarrowHint = capHint
		}
	}
	return toJSONResult(resp)
}

// gitopsDiagnoseTarget recognizes the GitOps reconciler kinds diagnose handles
// without a pod set, returning the canonical Kind, API group, resource plural,
// and tool label. The plural feeds the per-kind SAR gate before the cached read.
func gitopsDiagnoseTarget(kind string) (canonicalKind, group, resource, tool string, ok bool) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "application", "applications", "app":
		return "Application", "argoproj.io", "applications", "argocd", true
	case "kustomization", "kustomizations":
		return "Kustomization", "kustomize.toolkit.fluxcd.io", "kustomizations", "flux", true
	case "helmrelease", "helmreleases", "hr":
		return "HelmRelease", "helm.toolkit.fluxcd.io", "helmreleases", "flux", true
	}
	return "", "", "", "", false
}

// fluxReadySummary renders a Flux object's Ready condition as "<status>: <reason>"
// (e.g. "False: ReconciliationFailed"), or just "<status>" when no reason is set.
// Empty when there's no Ready condition.
func fluxReadySummary(u *unstructured.Unstructured) string {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := cm["type"].(string); t != "Ready" {
			continue
		}
		status, _ := cm["status"].(string)
		if reason, _ := cm["reason"].(string); reason != "" {
			return status + ": " + reason
		}
		return status
	}
	return ""
}

// handleGitOpsDiagnose is the no-pods diagnose path for GitOps reconcilers:
// reconciler status summary + the parsed failure issue (RelatedIssues). It does
// NOT reimplement the insights builder — the issues engine already classifies
// and parses the reconciler's failure (cause/action/remediation), so this stays
// a thin status read.
func handleGitOpsDiagnose(ctx context.Context, input diagnoseInput, canonicalKind, group, resource, tool string) (*mcp.CallToolResult, any, error) {
	// Two distinct gates, both required — same as the workload path above.
	// (1) Radar's namespace allow-list: the user only sees their scoped
	// namespaces, even when cluster RBAC would permit a read outside them.
	if !checkNamespaceAccess(ctx, input.Namespace) {
		return nil, nil, fmt.Errorf("forbidden: no access to namespace %q", input.Namespace)
	}
	// (2) Per-kind K8s RBAC: the object is read from the shared cache (connector
	// identity), so namespace access alone is not enough — a user who can read
	// ordinary resources in the namespace but not the GitOps CR must not receive
	// it. Gate on the exact (group, resource, get) the read performs.
	if !canReadInNamespace(ctx, group, resource, input.Namespace, "get") {
		return nil, nil, fmt.Errorf("forbidden: cannot get %s.%s in namespace %q", resource, group, input.Namespace)
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil, nil, errNotConnected()
	}
	u, err := cache.GetDynamicWithGroup(ctx, canonicalKind, input.Namespace, input.Name, group)
	if err != nil {
		// Distinguish "the CRD isn't installed / not yet discovered" from a
		// genuinely absent object — telling an agent "resource not found" when
		// the GitOps controller isn't even installed sends it debugging the
		// wrong thing.
		if errors.Is(err, k8s.ErrUnknownDynamicKind) {
			return nil, nil, fmt.Errorf("%s (%s) is not installed or not yet discovered in this cluster — is %s running?", canonicalKind, group, tool)
		}
		return nil, nil, notFoundError(ctx, err, canonicalKind, input.Namespace, input.Name)
	}
	minified, err := aicontext.Minify(u, aicontext.LevelDetail)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to minify: %w", err)
	}
	gd := &gitopsDiagnosis{Tool: tool}
	if tool == "flux" {
		gd.Suspended, _, _ = unstructured.NestedBool(u.Object, "spec", "suspend")
		gd.Revision, _, _ = unstructured.NestedString(u.Object, "status", "lastAppliedRevision")
		gd.Ready = fluxReadySummary(u)
	} else {
		gd.Sync, _, _ = unstructured.NestedString(u.Object, "status", "sync", "status")
		gd.Health, _, _ = unstructured.NestedString(u.Object, "status", "health", "status")
		gd.OperationPhase, _, _ = unstructured.NestedString(u.Object, "status", "operationState", "phase")
	}
	resp := diagnoseResponse{
		Resource:        minified,
		GitOpsDiagnosis: gd,
		RelatedIssues: issues.RelatedIssues(issues.NewCacheProvider(), issues.RelatedIssueOptions{
			Namespaces:           issueNamespacesForResource(input.Namespace),
			CanReadClusterScoped: issueClusterScopedAccess(ctx),
			CanReadRelated:       issueRelatedResourceAccess(ctx),
		}, group, canonicalKind, input.Namespace, input.Name),
		Warnings: k8score.EnrichRuntimeObjectWarnings(u),
	}
	return toJSONResult(resp)
}

func dnsContextForDiagnose(ctx context.Context, cache *k8s.ResourceCache, obj any, pods []*corev1.Pod, current, previous []podLogEntry, events []aicontext.DeduplicatedEvent) *diagnoseDNSContext {
	signals := diagnoseDNSSignals(obj, pods, current, previous, events)
	if len(signals) == 0 {
		return nil
	}
	out := &diagnoseDNSContext{Signals: signals}
	if canReadInNamespace(ctx, "", "configmaps", "kube-system", "list") {
		findings := k8s.DetectSuspiciousCoreDNS(cache, time.Now())
		for _, f := range findings {
			out.CoreDNSFindings = append(out.CoreDNSFindings, diagnoseDNSFinding{
				Kind:      f.Kind,
				Namespace: f.Namespace,
				Name:      f.Name,
				Severity:  f.Severity,
				Reason:    f.Reason,
				Message:   f.Message,
			})
			if len(out.CoreDNSFindings) >= 5 {
				break
			}
		}
	}
	return out
}

func diagnoseDNSSignals(obj any, pods []*corev1.Pod, current, previous []podLogEntry, events []aicontext.DeduplicatedEvent) []string {
	seen := map[string]bool{}
	var signals []string
	add := func(s string) {
		if s == "" || seen[s] || len(signals) >= 8 {
			return
		}
		seen[s] = true
		signals = append(signals, s)
	}
	for _, s := range dnsConfigSignalsForObject(obj) {
		add(s)
	}
	for _, p := range pods {
		for _, s := range dnsConfigSignalsForPodSpec(p.Name, p.Spec) {
			add(s)
		}
	}
	if logEntriesContainDNSSymptoms(current) || logEntriesContainDNSSymptoms(previous) {
		add("pod logs contain DNS resolution errors")
	}
	for _, e := range events {
		if textContainsDNSSymptom(e.Reason + " " + e.Message) {
			add("warning events contain DNS resolution errors")
			break
		}
	}
	return signals
}

func dnsConfigSignalsForObject(obj any) []string {
	switch o := obj.(type) {
	case *corev1.Pod:
		return dnsConfigSignalsForPodSpec(o.Name, o.Spec)
	case *appsv1.Deployment:
		return dnsConfigSignalsForPodSpec(o.Name, o.Spec.Template.Spec)
	case *appsv1.StatefulSet:
		return dnsConfigSignalsForPodSpec(o.Name, o.Spec.Template.Spec)
	case *appsv1.DaemonSet:
		return dnsConfigSignalsForPodSpec(o.Name, o.Spec.Template.Spec)
	default:
		return nil
	}
}

func dnsConfigSignalsForPodSpec(name string, spec corev1.PodSpec) []string {
	var out []string
	policy := spec.DNSPolicy
	if policy != "" && policy != corev1.DNSClusterFirst {
		if !(spec.HostNetwork && policy == corev1.DNSClusterFirstWithHostNet) {
			out = append(out, fmt.Sprintf("%s uses dnsPolicy=%s", name, policy))
		}
	}
	if spec.DNSConfig != nil {
		if len(spec.DNSConfig.Nameservers) > 0 {
			out = append(out, fmt.Sprintf("%s sets dnsConfig.nameservers", name))
		}
		if len(spec.DNSConfig.Searches) > 0 {
			out = append(out, fmt.Sprintf("%s sets dnsConfig.searches", name))
		}
		if len(spec.DNSConfig.Options) > 0 {
			out = append(out, fmt.Sprintf("%s sets dnsConfig.options", name))
		}
	}
	return out
}

func logEntriesContainDNSSymptoms(entries []podLogEntry) bool {
	for _, e := range entries {
		for _, line := range e.Logs.Lines {
			if textContainsDNSSymptom(line) {
				return true
			}
		}
		if textContainsDNSSymptom(e.Error) {
			return true
		}
	}
	return false
}

func textContainsDNSSymptom(text string) bool {
	low := strings.ToLower(text)
	return strings.Contains(low, "no such host") ||
		strings.Contains(low, "nxdomain") ||
		strings.Contains(low, "temporary failure in name resolution") ||
		strings.Contains(low, "name or service not known") ||
		strings.Contains(low, "server misbehaving") ||
		strings.Contains(low, "lookup ") && strings.Contains(low, ".svc")
}

// startupBlockersForWorkload runs the pre-Running detectors over the namespace
// and keeps the rows relevant to THIS workload: its own pods (bind-time /
// post-bind) and admission FailedCreate on the workload or its ReplicaSet.
// Namespace-scoped findings that aren't tied to this workload (the prior
// blanket "any ResourceQuota" case) are deliberately excluded — attaching a
// namespace's quota state to an unrelated workload over-attributes failures.
func startupBlockersForWorkload(cache *k8s.ResourceCache, kind, namespace, name string, pods []*corev1.Pod) []startupBlocker {
	all := k8s.DetectSchedulingProblems(cache, namespace)
	all = append(all, k8s.DetectAdmissionProblems(cache, namespace)...)
	all = append(all, k8s.DetectPostBindProblems(cache, namespace)...)
	if len(all) == 0 {
		return nil
	}

	podNames := make(map[string]bool, len(pods))
	for _, p := range pods {
		podNames[p.Name] = true
	}
	dispKind := normalizeDisplayKind(kind)

	var out []startupBlocker
	for _, p := range all {
		relevant := false
		switch {
		case p.Kind == "Pod" && podNames[p.Name]:
			relevant = true
		case p.Kind == dispKind && p.Name == name:
			relevant = true // FailedCreate on the workload itself (StatefulSet/DaemonSet)
		case dispKind == "Deployment" && p.Kind == "ReplicaSet" && isReplicaSetOf(p.Name, name):
			relevant = true // FailedCreate on the Deployment's ReplicaSet
		}
		if !relevant {
			continue
		}
		out = append(out, startupBlocker{
			Kind:     p.Kind,
			Name:     p.Name,
			Reason:   p.Reason,
			Severity: p.Severity,
			Message:  p.Message,
		})
	}
	return out
}

// isReplicaSetOf reports whether rsName belongs to the given Deployment.
// Deployment ReplicaSets are named "<deployment>-<podTemplateHash>" with a
// single hyphen-free hash segment, so we require exactly one trailing segment
// after "<deployment>-". This avoids a prefix false-match against a sibling
// Deployment that merely shares the prefix (diagnosing "api" must not claim
// "api-gateway-<hash>", which belongs to Deployment "api-gateway").
func isReplicaSetOf(rsName, deployName string) bool {
	suffix, ok := strings.CutPrefix(rsName, deployName+"-")
	return ok && suffix != "" && !strings.Contains(suffix, "-")
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
	// Deliberately a fixed-size evidence sample with silent truncation: this
	// feeds one section of a composite diagnosis, where a hint would be
	// noise. get_events is the exhaustive, truncation-signaled path.
	dedup, _ := aicontext.DeduplicateEventsN(matched, limit)
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
// networkTraceKind returns the canonical entry-kind name for trace
// diagnostics, accepting plural and lowercase forms the way agents tend to
// write them. Empty string + false means "not a network entry kind" - the
// caller falls through to the workload/pod branch.
func networkTraceKind(kind string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "service", "services":
		return "Service", true
	case "ingress", "ingresses":
		return "Ingress", true
	case "httproute", "httproutes":
		return "HTTPRoute", true
	case "grpcroute", "grpcroutes":
		return "GRPCRoute", true
	case "gateway", "gateways":
		return "Gateway", true
	}
	return "", false
}

// networkDiagnoseResponse is the coverage-honest, agent-shaped output for a
// network entry kind. The summary headline + counts are the primary read;
// routes/notTested carry the per-intended-route truth; brokenRoute NAMES the
// culprit (no raw array index). path keeps each hop's static findings (so the
// targetPort/missing-ref/scale-0 diagnosis isn't lost) without the verbose
// per-probe dump - that story is already projected into routes + localization.
// No flat RelatedIssues: the per-hop Findings are the non-duplicated home for it.
type networkDiagnoseResponse struct {
	Subject trace.ResourceRef `json:"subject"`
	// Verdict is a coarse rollup enum (healthy | degraded | broken | unknown) that
	// agents may key follow-up actions on. It is intentionally lossy: `healthy`
	// means "no failing route was found" - it can include routes that were only
	// REACHED (a 3xx/4xx response, or a transport-only TCP/TLS connection) rather
	// than verified with a real 2xx, and can reflect a static-config-only
	// assessment (probe=false) or partial coverage. Consumers that need certainty
	// must read Routes[].Outcome (verified vs reached vs not-tested), each route's
	// Confidence (real vs indirect), and the Summary.Headline / Diagnosis text
	// rather than keying solely on Verdict. (Wire vocabulary reshape is deferred to
	// a later wave; this field's meaning is unchanged here.)
	Verdict string `json:"verdict"`
	Reason  string `json:"reason,omitempty"`
	// Diagnosis is the lead: the one finding that matters - cause, the named
	// culprit, the next action - hoisted from path[] so the agent reads the
	// "why + what next" without walking the hop chain. nil when there is
	// nothing to diagnose (every route verified over real traffic).
	Diagnosis   *trace.Diagnosis    `json:"diagnosis,omitempty"`
	Summary     coverageSummary     `json:"summary"`
	Routes      []trace.RouteResult `json:"routes,omitempty"`
	NotTested   []trace.RouteSkip   `json:"notTested,omitempty"`
	BrokenRoute *trace.ResourceRef  `json:"brokenRoute,omitempty"`
	Path        []networkHop        `json:"path,omitempty"`
	Upstreams   []networkHop        `json:"upstreams,omitempty"`
	// InClusterTests is present only when in_cluster=true: the raw result of each
	// route's live in-cluster probe (vantage in-cluster). On success the matching
	// routes[] entry is already upgraded to confidence:real; this section carries
	// the per-probe detail and, when the probe couldn't run, the honest status +
	// a copyable kubectl command to run by hand.
	InClusterTests []reachability.InClusterTestResult `json:"inClusterTests,omitempty"`
}

type coverageSummary struct {
	Tested int `json:"tested"`
	Passed int `json:"passed"`
	Failed int `json:"failed"`
	// Derived counts routes known broken WITHOUT being dialled - a backendRef
	// naming nothing, or a backend with no ready endpoints. They are real breaks
	// but not test results, so they are reported apart from passed/failed. An
	// agent that reads only passed/failed would otherwise see a trace with a
	// missing backend as entirely clean.
	Derived  int    `json:"derived,omitempty"`
	Skipped  int    `json:"skipped"`
	Headline string `json:"headline"`
}

// networkHop is a slimmed hop for the agent: identity, edge, static findings,
// and meta - no probes/config dump (the probe story lives in routes).
type networkHop struct {
	Resource trace.ResourceRef `json:"resource"`
	Edge     string            `json:"edge,omitempty"`
	Findings []trace.Finding   `json:"findings,omitempty"`
	Meta     map[string]any    `json:"meta,omitempty"`
}

// buildNetworkDiagnoseResponse projects a finalized Trace into the coverage-
// honest agent shape. Pure (no cluster I/O) so it is unit-testable.
func buildNetworkDiagnoseResponse(tr *trace.Trace) networkDiagnoseResponse {
	resp := networkDiagnoseResponse{
		Subject:     tr.Subject,
		Verdict:     trace.CoverageVerdict(tr),
		Diagnosis:   tr.Diagnosis,
		Routes:      tr.Routes,
		NotTested:   tr.NotTested,
		BrokenRoute: tr.BrokenRoute,
		Summary:     coverageSummary{Headline: tr.Headline},
		Path:        slimNetworkHops(tr.Downstream, false, tr.Subject),
		// Upstreams are OTHER entries that share this subject as a backend; their
		// missing-ref findings are usually about THEIR sibling routes, not the
		// subject's path - scope those out so they don't leak into this diagnosis
		// (B1). But a missing-ref that NAMES the subject (e.g. an upstream Ingress
		// referencing the subject Service on a port it doesn't expose) is a real
		// subject break represented only on the upstream hop - keep it.
		Upstreams: slimNetworkHops(tr.Upstreams, true, tr.Subject),
	}
	// Keep the legacy reason only for a special-shape unknown (selectorless, RBAC
	// denied, lookup failure, timed out), where it carries the honest reason the
	// coverage counts cannot. Those set UnknownClass (classifyUnknown always assigns
	// one). A verdict collapsed to unknown from a healthy static path, reached only
	// via the apiserver proxy or nothing tested from here, leaves UnknownClass empty;
	// its headline and routes already explain, and surfacing tr.Reason there would
	// risk the B3 contradiction (a stale "verified via API server" beside a "couldn't
	// test any route" headline).
	if tr.Verdict == trace.VerdictUnknown && tr.UnknownClass != "" {
		resp.Reason = tr.Reason
	} else if (tr.Verdict == trace.VerdictBroken || tr.Verdict == trace.VerdictDegraded) && tr.Reason != "" {
		// A broken/degraded verdict can come from probe evidence with no static
		// finding - e.g. all entry paths unreachable while the subject itself is
		// healthy. Without the reason an agent sees verdict=broken beside a
		// "Reachable" headline with no cause; surface it.
		resp.Reason = tr.Reason
	}
	if tr.Coverage != nil {
		resp.Summary.Tested = tr.Coverage.Tested
		resp.Summary.Passed = tr.Coverage.Passed
		resp.Summary.Failed = tr.Coverage.Failed
		resp.Summary.Derived = tr.Coverage.Derived
		resp.Summary.Skipped = tr.Coverage.Skipped
	}
	return resp
}

func slimNetworkHops(hops []trace.Hop, scopeUpstream bool, subject trace.ResourceRef) []networkHop {
	if len(hops) == 0 {
		return nil
	}
	out := make([]networkHop, 0, len(hops))
	for _, h := range hops {
		findings := h.Findings
		if scopeUpstream {
			findings = withoutMissingRef(findings, subject)
		}
		out = append(out, networkHop{Resource: h.Resource, Edge: h.Edge, Findings: findings, Meta: h.Meta})
	}
	return out
}

// withoutMissingRef drops missing-backend-ref findings that belong to an
// upstream's OWN sibling routes (not about the subject this response is about).
// A missing-ref that NAMES the subject is a genuine subject break represented
// only on the upstream hop (e.g. an Ingress pointing at the subject Service on a
// port it doesn't expose), so it is kept. Reuses the trace predicates so the rule
// has one definition.
func withoutMissingRef(fs []trace.Finding, subject trace.ResourceRef) []trace.Finding {
	var out []trace.Finding
	for _, f := range fs {
		if trace.IsMissingRefCode(f.Code) && !trace.MissingRefNamesSubject(f.Message, subject.Name, subject.Namespace) {
			continue
		}
		out = append(out, f)
	}
	return out
}

// handleNetworkTraceDiagnose is the diagnose tool's response for a network
// entry kind. It deliberately skips the pod-log fan-out path - the trace IS
// the diagnosis, projected into a coverage-honest shape (see networkDiagnoseResponse).
func handleNetworkTraceDiagnose(ctx context.Context, input diagnoseInput, kind string) (*mcp.CallToolResult, any, error) {
	if !checkNamespaceAccess(ctx, input.Namespace) {
		return nil, nil, fmt.Errorf("forbidden: no access to namespace %q", input.Namespace)
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil, nil, fmt.Errorf("not connected to cluster")
	}
	deps := trace.Deps{
		Cache:     cache,
		Dynamic:   k8s.GetDynamicResourceCache(),
		Discovery: k8s.GetResourceDiscovery(),
		Issues:    issues.NewCacheProvider(),
		// Probes call services/proxy + pods/proxy on this client. Use the
		// per-request impersonated identity (or the SA when auth is disabled)
		// so the apiserver enforces the caller's RBAC on the proxy verbs.
		// ClientFromContext returns nil on impersonation failure; the probe
		// layer treats nil as "skip the apiserver path."
		Client: k8s.ClientFromContext(ctx),
		// Carry the caller's namespace scope into the trace walk so
		// cross-namespace fan-out (Route backendRefs, parent Gateways,
		// upstream Routes) is redacted when the dependent is outside
		// scope. filterNamespacesForUser returns the caller's allowed
		// set; passing only the subject namespace would be too narrow
		// (every cross-namespace dependency would redact even ones the
		// caller can read), so pass the full allowed set.
		AllowedNamespaces: filterNamespacesForUser(ctx, nil),
	}
	// in_cluster only has routes to test when probing ran (static known-break routes
	// carry no InClusterRequest). Force the probe path when the caller asked to
	// confirm the live path, so in_cluster:true without probe:true isn't a silent no-op.
	tr, err := trace.BuildTraceWithOptions(ctx, deps, kind, input.Namespace, input.Name, trace.Options{Probe: input.Probe || input.InCluster})
	if err != nil {
		return nil, nil, fmt.Errorf("trace failed: %w", err)
	}
	var inClusterTests []reachability.InClusterTestResult
	if input.InCluster {
		// Creating transient probe pods is a mutating, product-gated action. Mirror
		// the REST handler (reachability_run.go requireCloudRole) and the helm
		// precedent (tools_helm.go) so a sub-Member Cloud Viewer can't bypass the
		// RoleMember gate via MCP. probe=true / static paths stay viewer-accessible.
		if role := pkgauth.CloudRoleFromContext(ctx); !role.AtLeast(pkgauth.RoleMember) {
			return nil, nil, fmt.Errorf("Radar Cloud role %q cannot run an in-cluster reachability test (requires member or higher)", role.String())
		}
		var byTarget map[string][]probe.Result
		inClusterTests, byTarget = reachability.RunInClusterTests(ctx, tr, input.Namespace)
		// Fold the live results back in: an in-cluster pass upgrades the route to
		// confidence:real and re-derives counts/headline/diagnosis/verdict.
		trace.ApplyInClusterResults(tr, byTarget)
	}
	resp := buildNetworkDiagnoseResponse(tr)
	resp.InClusterTests = inClusterTests
	return toJSONResult(resp)
}

// Shared between diagnose (passes resolved pod names for full workload
// coverage) and attachResourceExtras / get_resource include=events
// (passes nil — supplemental fetch; callers wanting pod-level events should
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
