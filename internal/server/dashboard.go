package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"slices"
	"sort"
	"sync"
	"time"

	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/internal/traffic"
	topology "github.com/skyhook-io/radar/pkg/topology"
)

// DashboardResponse is the aggregated response for the home dashboard
type DashboardResponse struct {
	Cluster                DashboardCluster                `json:"cluster"`
	Health                 DashboardHealth                 `json:"health"`
	Problems               []DashboardProblem              `json:"problems"`
	ResourceCounts         DashboardResourceCounts         `json:"resourceCounts"`
	RecentEvents           []DashboardEvent                `json:"recentEvents"`
	RecentChanges          []DashboardChange               `json:"recentChanges"`
	TopologySummary        DashboardTopologySummary        `json:"topologySummary"`
	TrafficSummary         *DashboardTrafficSummary        `json:"trafficSummary"`
	Metrics                *DashboardMetrics               `json:"metrics"`
	MetricsServerAvailable bool                            `json:"metricsServerAvailable"`
	CertificateHealth      *DashboardCertificateHealth     `json:"certificateHealth,omitempty"`
	NetworkPolicyCoverage  *DashboardNetworkPolicyCoverage `json:"networkPolicyCoverage,omitempty"`
	NodeVersionSkew        *k8s.VersionSkew                `json:"nodeVersionSkew,omitempty"`
	Audit                  *DashboardAudit                 `json:"audit,omitempty"`
	Visibility             *k8s.VisibilitySummary          `json:"visibility,omitempty"`
	// GitOpsControllers summarizes Argo CD / Flux controller pod health.
	// Omitted when no controllers are detected — the Home dashboard
	// hides the card entirely on non-GitOps clusters rather than
	// rendering an empty state.
	GitOpsControllers *DashboardGitOpsControllers `json:"gitopsControllers,omitempty"`
	DeferredLoading   bool                        `json:"deferredLoading,omitempty"`  // True while deferred informers (secrets, events, etc.) are still syncing
	PartialData       []string                    `json:"partialData,omitempty"`      // Resource kinds that timed out during critical sync (e.g. ["Pod", "Deployment"])
	AccessRestricted  bool                        `json:"accessRestricted,omitempty"` // True when user has no namespace access (RBAC)
}

// DashboardCRDsResponse is the response for CRD counts (loaded lazily)
type DashboardCRDsResponse struct {
	TopCRDs []DashboardCRDCount `json:"topCRDs"`
}

type DashboardCluster struct {
	Name      string `json:"name"`
	Platform  string `json:"platform"`
	Version   string `json:"version"`
	Connected bool   `json:"connected"`
}

type DashboardHealth struct {
	Healthy       int `json:"healthy"`
	Warning       int `json:"warning"`
	Error         int `json:"error"`
	WarningEvents int `json:"warningEvents"`
}

type DashboardProblem struct {
	Kind            string `json:"kind"`
	Namespace       string `json:"namespace"`
	Name            string `json:"name"`
	Group           string `json:"group,omitempty"` // API group for CRD disambiguation (e.g., "cluster.x-k8s.io")
	Severity        string `json:"severity"`        // "critical", "high", or "medium"
	Reason          string `json:"reason"`
	Message         string `json:"message"`
	Age             string `json:"age"`
	AgeSeconds      int64  `json:"ageSeconds"`         // For sorting: lower = more recent
	Duration        string `json:"duration"`           // How long the problem has persisted
	DurationSeconds int64  `json:"durationSeconds"`    // For sorting by problem age
	PodCount        int    `json:"podCount,omitempty"` // For workload rollups: number of affected pods
}

// mergeResourceCounts adds src's per-namespace counts into dst. Cluster-scoped
// fields (Nodes, Namespaces) are intentionally NOT merged here — those are
// populated separately at the call site after a SAR check. Merging per-
// namespace results would multi-count cluster-scoped values (each iteration
// returns the same cluster-wide number); getDashboardResourceCounts no longer
// populates them at all, so the caller is the only writer.
func mergeResourceCounts(dst *DashboardResourceCounts, src DashboardResourceCounts) {
	dst.Pods.Total += src.Pods.Total
	dst.Pods.Running += src.Pods.Running
	dst.Pods.Pending += src.Pods.Pending
	dst.Pods.Failed += src.Pods.Failed
	dst.Pods.Succeeded += src.Pods.Succeeded
	dst.Deployments.Total += src.Deployments.Total
	dst.Deployments.Available += src.Deployments.Available
	dst.Deployments.Unavailable += src.Deployments.Unavailable
	dst.StatefulSets.Total += src.StatefulSets.Total
	dst.StatefulSets.Ready += src.StatefulSets.Ready
	dst.StatefulSets.Unready += src.StatefulSets.Unready
	dst.DaemonSets.Total += src.DaemonSets.Total
	dst.DaemonSets.Ready += src.DaemonSets.Ready
	dst.DaemonSets.Unready += src.DaemonSets.Unready
	dst.Services += src.Services
	dst.Ingresses += src.Ingresses
	dst.Jobs.Total += src.Jobs.Total
	dst.Jobs.Active += src.Jobs.Active
	dst.Jobs.Succeeded += src.Jobs.Succeeded
	dst.Jobs.Failed += src.Jobs.Failed
	dst.CronJobs.Total += src.CronJobs.Total
	dst.CronJobs.Active += src.CronJobs.Active
	dst.CronJobs.Suspended += src.CronJobs.Suspended
	dst.ConfigMaps += src.ConfigMaps
	dst.Secrets += src.Secrets
	dst.PVCs.Total += src.PVCs.Total
	dst.PVCs.Bound += src.PVCs.Bound
	dst.PVCs.Pending += src.PVCs.Pending
	dst.PVCs.Unbound += src.PVCs.Unbound
	dst.Gateways += src.Gateways
	dst.Routes += src.Routes
	// Restricted: union (per-ns may report different missing kinds).
	for _, k := range src.Restricted {
		found := false
		for _, existing := range dst.Restricted {
			if existing == k {
				found = true
				break
			}
		}
		if !found {
			dst.Restricted = append(dst.Restricted, k)
		}
	}
}

type DashboardResourceCounts struct {
	Pods         ResourceCount `json:"pods"`
	Deployments  ResourceCount `json:"deployments"`
	StatefulSets WorkloadCount `json:"statefulSets"`
	DaemonSets   WorkloadCount `json:"daemonSets"`
	Services     int           `json:"services"`
	Ingresses    int           `json:"ingresses"`
	Nodes        NodeCount     `json:"nodes"`
	Namespaces   int           `json:"namespaces"`
	Jobs         JobCount      `json:"jobs"`
	CronJobs     CronJobCount  `json:"cronJobs"`
	ConfigMaps   int           `json:"configMaps"`
	Secrets      int           `json:"secrets"`
	PVCs         PVCCount      `json:"pvcs"`
	Gateways     int           `json:"gateways"`
	Routes       int           `json:"routes"`
	Restricted   []string      `json:"restricted,omitempty"` // Resource kinds the user cannot list
}

type WorkloadCount struct {
	Total   int `json:"total"`
	Ready   int `json:"ready"`
	Unready int `json:"unready"`
}

type DashboardMetrics struct {
	CPU    *MetricSummary `json:"cpu,omitempty"`
	Memory *MetricSummary `json:"memory,omitempty"`
	// UsageAvailable reports whether live usage (UsageMillis/UsagePercent) came
	// from metrics-server. When false, only RequestsMillis/CapacityMillis are
	// meaningful — usage fields are zero.
	UsageAvailable bool `json:"usageAvailable"`
}

type MetricSummary struct {
	UsageMillis    int64 `json:"usageMillis"`
	RequestsMillis int64 `json:"requestsMillis"`
	CapacityMillis int64 `json:"capacityMillis"`
	UsagePercent   int   `json:"usagePercent"`
	RequestPercent int   `json:"requestPercent"`
}

type ResourceCount struct {
	Total       int `json:"total"`
	Running     int `json:"running,omitempty"`
	Pending     int `json:"pending,omitempty"`
	Failed      int `json:"failed,omitempty"`
	Succeeded   int `json:"succeeded,omitempty"`
	Available   int `json:"available,omitempty"`
	Unavailable int `json:"unavailable,omitempty"`
}

type NodeCount struct {
	Total    int `json:"total"`
	Ready    int `json:"ready"`
	NotReady int `json:"notReady"`
	Cordoned int `json:"cordoned"`
}

type JobCount struct {
	Total     int `json:"total"`
	Active    int `json:"active"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
}

type CronJobCount struct {
	Total     int `json:"total"`
	Active    int `json:"active"`
	Suspended int `json:"suspended"`
}

type PVCCount struct {
	Total   int `json:"total"`
	Bound   int `json:"bound"`
	Pending int `json:"pending"`
	Unbound int `json:"unbound"`
}

type DashboardCRDCount struct {
	Kind  string `json:"kind"`
	Name  string `json:"name"` // plural resource name (e.g. "rollouts")
	Group string `json:"group"`
	Count int    `json:"count"`
}

type DashboardEvent struct {
	Type           string `json:"type"`
	Reason         string `json:"reason"`
	Message        string `json:"message"`
	InvolvedObject string `json:"involvedObject"`
	Namespace      string `json:"namespace"`
	Timestamp      string `json:"timestamp"`
	Count          int32  `json:"count,omitempty"`
}

type DashboardChange struct {
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	ChangeType string `json:"changeType"`
	Summary    string `json:"summary"`
	Timestamp  string `json:"timestamp"`
}

type DashboardTopologySummary struct {
	NodeCount int `json:"nodeCount"`
	EdgeCount int `json:"edgeCount"`
}

type DashboardTrafficSummary struct {
	Source    string             `json:"source"`
	FlowCount int                `json:"flowCount"`
	TopFlows  []DashboardTopFlow `json:"topFlows"`
}

type DashboardTopFlow struct {
	Src            string  `json:"src"`
	Dst            string  `json:"dst"`
	RequestsPerSec float64 `json:"requestsPerSec,omitempty"`
	Connections    int64   `json:"connections"`
}

type DashboardHelmSummary struct {
	Total      int                    `json:"total"`
	Releases   []DashboardHelmRelease `json:"releases"`
	Restricted bool                   `json:"restricted,omitempty"` // True when user lacks permissions to list Helm releases (RBAC-denied)
	// Error + ErrorCode are populated when the Helm read failed for a
	// reason other than RBAC (Helm client not initialized, no resolved
	// rest.Config, network error). Lets the dashboard widget render
	// "Helm unavailable: not configured" instead of an empty list that
	// looks like "this cluster has zero releases." ErrorCode uses the
	// same vocabulary as packages.go ErrCode* (rbac_denied,
	// unreachable, timed_out, unconfigured, auth_required).
	Error     string `json:"error,omitempty"`
	ErrorCode string `json:"errorCode,omitempty"`
}

type DashboardHelmRelease struct {
	Name           string `json:"name"`
	Namespace      string `json:"namespace"`
	Chart          string `json:"chart"`
	ChartVersion   string `json:"chartVersion"`
	Status         string `json:"status"`
	ResourceHealth string `json:"resourceHealth,omitempty"`
}

// mergeHelmSummary appends src into dst. The first non-empty Error / ErrorCode
// wins so the UI surfaces a real failure rather than swallowing it under a
// later success. Restricted is OR-merged: restricted in any namespace ⇒ flag.
func mergeHelmSummary(dst *DashboardHelmSummary, src DashboardHelmSummary) {
	dst.Total += src.Total
	dst.Releases = append(dst.Releases, src.Releases...)
	if src.Restricted {
		dst.Restricted = true
	}
	if dst.Error == "" {
		dst.Error = src.Error
		dst.ErrorCode = src.ErrorCode
	}
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	dashStart := time.Now()
	namespaces := s.parseNamespacesForUser(r)
	if noNamespaceAccess(namespaces) {
		s.writeJSON(w, DashboardResponse{AccessRestricted: true})
		return
	}
	// Decide how to drive the per-namespace helpers:
	//   - namespaces == nil → cluster-admin / no-auth: single call with ""
	//     (cluster-wide informers + cluster-scoped sections).
	//   - non-nil → namespace-restricted user; iterate per ns and aggregate.
	//
	// Cluster-scoped fields (Nodes, total Namespaces, version skew) are gated
	// by SAR for namespace-restricted users — having list-pod cluster-wide is
	// not a license to read those.
	var iterateNamespaces []string
	if namespaces == nil {
		iterateNamespaces = []string{""}
	} else {
		iterateNamespaces = namespaces
	}

	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}

	resp := DashboardResponse{}
	if result := k8s.GetCachedPermissionResult(); result != nil {
		resp.Visibility = k8s.BuildVisibilitySummary(result, k8s.VisibilityNamespace(namespaces))
	}
	canReadNodes := s.canRead(r, "", "nodes", "", "list")
	canReadNamespaces := s.canRead(r, "", "namespaces", "", "list")

	// Signal to the frontend that some data (events, secrets, configmaps, etc.)
	// may be incomplete because deferred informers are still syncing.
	resp.DeferredLoading = !cache.IsDeferredSynced()

	// If critical informers were promoted at first paint, tell the
	// frontend which kinds are STILL loading (live-filtered, not the
	// snapshot from connect time) so the banner doesn't list kinds that
	// have since populated.
	if pending := cache.PendingPromotedKinds(); len(pending) > 0 {
		resp.PartialData = pending
	}

	// --- Slow network calls: run in parallel ---
	var wg sync.WaitGroup
	var cluster DashboardCluster
	var metrics *DashboardMetrics
	var trafficSummary *DashboardTrafficSummary

	ctx := r.Context()

	wg.Add(3)
	go func() {
		defer wg.Done()
		t := time.Now()
		cluster = s.getDashboardCluster(ctx)
		k8s.LogTiming("  [dashboard] cluster info: %v", time.Since(t))
	}()
	go func() {
		defer wg.Done()
		t := time.Now()
		// Pass allowed namespaces so pod-request totals only sum pods the
		// user has read access to. nil means "all namespaces" (cluster-
		// wide namespaced access / no-auth). Node capacity and usage are
		// cluster-scoped, so omit metrics unless the caller can list Nodes.
		if canReadNodes {
			metrics = s.getDashboardMetrics(ctx, namespaces)
		}
		k8s.LogTiming("  [dashboard] metrics: %v", time.Since(t))
	}()
	go func() {
		defer wg.Done()
		t := time.Now()
		trafficSummary = s.getDashboardTrafficSummary(ctx, namespaces)
		k8s.LogTiming("  [dashboard] traffic: %v", time.Since(t))
	}()

	// --- Fast cache-based calls: run while network calls are in flight ---
	// Iterate per allowed namespace and aggregate. For cluster-admin / no-auth
	// callers iterateNamespaces is [""], so this collapses to a single
	// cluster-wide call.
	t := time.Now()
	for _, ns := range iterateNamespaces {
		h, probs := s.getDashboardHealth(cache, ns)
		resp.Health.Healthy += h.Healthy
		resp.Health.Warning += h.Warning
		resp.Health.Error += h.Error
		for _, p := range probs {
			if p.Kind == "Node" && !canReadNodes {
				continue
			}
			resp.Problems = append(resp.Problems, p)
		}
	}
	k8s.LogTiming("  [dashboard] health: %v", time.Since(t))

	t = time.Now()
	for _, ns := range iterateNamespaces {
		mergeResourceCounts(&resp.ResourceCounts, s.getDashboardResourceCounts(cache, ns))
	}
	k8s.LogTiming("  [dashboard] resource counts: %v", time.Since(t))

	t = time.Now()
	for _, ns := range iterateNamespaces {
		resp.RecentEvents = append(resp.RecentEvents, s.getDashboardRecentEvents(cache, ns)...)
		resp.Health.WarningEvents += s.countWarningEvents(cache, ns)
	}
	k8s.LogTiming("  [dashboard] events: %v", time.Since(t))

	t = time.Now()
	resp.RecentChanges = s.getDashboardRecentChanges(ctx, namespaces)
	k8s.LogTiming("  [dashboard] changes: %v", time.Since(t))

	t = time.Now()
	resp.TopologySummary = s.getDashboardTopologySummary(namespaces)
	k8s.LogTiming("  [dashboard] topology: %v", time.Since(t))

	resp.CertificateHealth = s.getDashboardCertificateHealth(namespaces)
	resp.NetworkPolicyCoverage = s.getDashboardNetworkPolicyCoverage(cache, namespaces)
	resp.Audit = getDashboardAudit(cache, namespaces)
	resp.GitOpsControllers = s.getDashboardGitOpsControllers(cache, namespaces)

	if canReadNodes {
		if nodeLister := cache.Nodes(); nodeLister != nil {
			nodeList, _ := nodeLister.List(labels.Everything())
			resp.ResourceCounts.Nodes.Total = len(nodeList)
			for _, n := range nodeList {
				h := k8s.ClassifyNodeHealth(n)
				if h.Ready {
					if h.Unschedulable {
						resp.ResourceCounts.Nodes.Cordoned++
					} else {
						resp.ResourceCounts.Nodes.Ready++
					}
				} else {
					resp.ResourceCounts.Nodes.NotReady++
				}
			}
		}
	}
	if canReadNamespaces {
		if nsLister := cache.Namespaces(); nsLister != nil {
			nss, _ := nsLister.List(labels.Everything())
			resp.ResourceCounts.Namespaces = len(nss)
		}
	} else if len(namespaces) > 0 {
		// Restricted user — surface their accessible count instead of "0".
		resp.ResourceCounts.Namespaces = len(namespaces)
	}

	if canReadNodes && cache.Nodes() != nil {
		nodes, _ := cache.Nodes().List(labels.Everything())
		resp.NodeVersionSkew = k8s.DetectVersionSkew(nodes)
	}

	// --- Wait for network calls and assemble response ---
	wg.Wait()

	resp.Cluster = cluster
	resp.Metrics = metrics
	// "Available" means live usage is present — not merely that capacity/request
	// data exists. metrics is now non-nil whenever nodes are cached, so gate the
	// flag on actual usage so the frontend's "usage unavailable" hint still fires.
	resp.MetricsServerAvailable = metrics != nil && metrics.UsageAvailable
	resp.TrafficSummary = trafficSummary

	k8s.LogTiming(" [dashboard] total: %v", time.Since(dashStart))
	s.writeJSON(w, resp)
}

// handleDashboardHelm returns Helm release summary - loaded lazily to keep main dashboard fast
func (s *Server) handleDashboardHelm(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespaces := s.parseNamespacesForUser(r)
	if noNamespaceAccess(namespaces) {
		s.writeJSON(w, DashboardHelmSummary{})
		return
	}

	// Iterate per allowed namespace and aggregate. Cluster-admin / no-auth
	// (namespaces == nil) collapses to a single "" call (cluster-wide).
	var summary DashboardHelmSummary
	if namespaces == nil {
		summary = s.getDashboardHelmSummary(r, "")
	} else {
		for _, ns := range namespaces {
			mergeHelmSummary(&summary, s.getDashboardHelmSummary(r, ns))
		}
	}
	s.writeJSON(w, summary)
}

// handleDashboardCRDs returns CRD counts - loaded lazily to keep main dashboard fast.
//
// Cluster-scoped CRDs: each is gated per-kind via SubjectAccessReview, so a
// user with cluster-wide pod visibility (AllowedNamespaces==nil from the
// namespace-discovery probe) doesn't implicitly see cluster-scoped CRDs they
// can't list directly.
//
// Namespaced CRDs: scoped to the user's allowed namespaces. nil = all
// (auth disabled or user has cluster-wide namespaced access). Empty =
// no access.
func (s *Server) handleDashboardCRDs(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespaces := s.parseNamespacesForUser(r)

	clusterScoped := s.collectClusterScopedCRDCounts(r)
	nsScoped := s.collectNamespacedCRDCounts(r.Context(), namespaces)

	s.writeJSON(w, DashboardCRDsResponse{TopCRDs: mergeCRDCounts(clusterScoped, nsScoped)})
}

func (s *Server) getDashboardCluster(ctx context.Context) DashboardCluster {
	info, err := k8s.GetClusterInfo(ctx)
	if err != nil {
		return DashboardCluster{Connected: false}
	}
	return DashboardCluster{
		Name:      info.Cluster,
		Platform:  info.Platform,
		Version:   info.KubernetesVersion,
		Connected: true,
	}
}

func (s *Server) getDashboardHealth(cache *k8s.ResourceCache, namespace string) (DashboardHealth, []DashboardProblem) {
	health := DashboardHealth{}
	problems := make([]DashboardProblem, 0)

	now := time.Now()

	// Pod health
	var pods []*corev1.Pod
	var err error
	if podLister := cache.Pods(); podLister != nil {
		if namespace != "" {
			pods, err = podLister.Pods(namespace).List(labels.Everything())
		} else {
			pods, err = podLister.List(labels.Everything())
		}
	}
	// Pods the post-bind layer owns (stuck ContainerCreating on CNI/volume).
	// Computed up front so the warning rollup below can skip them the same way
	// it skips unschedulable pods — otherwise a long-Pending stuck pod gets
	// both a bare "Pending" rollup row and the richer post-bind row. Keyed
	// "namespace/name"; the slice is reused in the scheduling block below.
	postBind := k8s.DetectPostBindProblems(cache, namespace)
	postBindPods := make(map[string]bool, len(postBind))
	for _, p := range postBind {
		postBindPods[p.Namespace+"/"+p.Name] = true
	}

	// Group unhealthy pods by owner workload for rollup
	ownerGroups := make(map[ownerKey]*ownerGroup)
	var orphanProblems []DashboardProblem

	if err == nil {
		for _, pod := range pods {
			status := classifyPodHealth(pod, now)
			switch status {
			case "healthy":
				health.Healthy++
			case "warning":
				health.Warning++
				// Unschedulable pods (bind-time) and stuck-creating pods
				// (post-bind) are owned by the scheduling rows appended below,
				// which name the actual constraint; don't also roll them up
				// here as a bare "Pending".
				if !k8s.IsPodUnschedulable(pod) && !postBindPods[pod.Namespace+"/"+pod.Name] {
					collectPodForRollup(pod, "medium", now, ownerGroups, &orphanProblems)
				}
			case "error":
				health.Error++
				collectPodForRollup(pod, "critical", now, ownerGroups, &orphanProblems)
			}
		}
	}

	// Convert owner groups to rolled-up problems
	for key, g := range ownerGroups {
		// Build reason summary: "CrashLoopBackOff (3), Pending (1)"
		var reasonParts []string
		for reason, count := range g.reasons {
			if count > 1 {
				reasonParts = append(reasonParts, fmt.Sprintf("%s (%d)", reason, count))
			} else {
				reasonParts = append(reasonParts, reason)
			}
		}
		sort.Strings(reasonParts)

		problems = append(problems, DashboardProblem{
			Kind:      key.kind,
			Namespace: key.namespace,
			Name:      key.name,
			Severity:  g.severity,
			Reason: fmt.Sprintf("%d %s unhealthy", g.podCount, func() string {
				if g.podCount == 1 {
					return "pod"
				}
				return "pods"
			}()),
			Message:         k8s.Truncate(strings.Join(reasonParts, ", "), 200),
			Age:             k8s.FormatAge(g.newestAge),
			AgeSeconds:      int64(g.newestAge.Seconds()),
			Duration:        k8s.FormatAge(g.newestDur),
			DurationSeconds: int64(g.newestDur.Seconds()),
			PodCount:        g.podCount,
		})
	}
	// Add orphan pod problems (no owner workload)
	problems = append(problems, orphanProblems...)

	// Workload/HPA/CronJob/Node problems (excluding pods, handled above) +
	// direct dangling-ref errors (missing CM/Secret/PVC/SA refs, missing
	// HPA target, missing Ingress backend / TLS / port, missing roleRef,
	// missing StorageClass on a PVC, missing headless Service on a
	// StatefulSet) + webhook-config refs (missing Service on
	// Validating/MutatingWebhookConfiguration). Skip Pod-kind rows from
	// DetectProblems — REST's pod rollup + orphan handling above is the
	// canonical pod surface; including DetectProblems Pod rows would
	// duplicate them. (Missing-ref Pod rows are intentionally kept: those
	// catch pods stuck Pending on missing refs, which the pod-error loop
	// above doesn't surface.)
	detected := append(k8s.DetectProblems(cache, namespace), k8s.DetectMissingRefs(cache, namespace)...)
	detected = append(detected, k8s.DetectMissingWebhookRefs(cache, k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery(), namespace)...)
	// DetectProblems Pod rows duplicate REST's pod rollup above; skip them.
	// DetectMissingRefs Pod rows are kept (different failure category — won't-
	// schedule etc.). We can't tell the source from the Problem struct, so
	// distinguish by whether Reason starts with "Missing " (only emitted by
	// DetectMissingRefs at present). Dedupe keys by (ns, name, reason) so a
	// Pod with multiple distinct missing-ref reasons (e.g. PVC + ConfigMap)
	// keeps a row per blocker — agents triaging a Pending pod need ALL of
	// the missing refs, not just whichever fired first.
	seenPodReason := map[string]bool{}
	for _, p := range detected {
		if p.Kind == "Pod" {
			if !strings.HasPrefix(p.Reason, "Missing ") {
				continue
			}
			key := p.Namespace + "/" + p.Name + "/" + p.Reason
			if seenPodReason[key] {
				continue
			}
			seenPodReason[key] = true
		}
		problems = append(problems, DashboardProblem{
			Kind:            p.Kind,
			Namespace:       p.Namespace,
			Name:            p.Name,
			Severity:        p.Severity,
			Reason:          p.Reason,
			Message:         p.Message,
			Age:             p.Age,
			AgeSeconds:      p.AgeSeconds,
			Duration:        p.Duration,
			DurationSeconds: p.DurationSeconds,
		})
	}

	// Scheduling problems: unschedulable pods (with the offending node
	// constraint named), admission rejections (quota/PodSecurity/webhook — no
	// Pod exists, so the pod rollup above can't see them), and post-bind
	// CNI/volume stalls. Appended directly (not through the Missing-ref Pod
	// filter above) — an Unschedulable row IS the pod's scheduling reason; the
	// pod rollup above skips unschedulable + post-bind pods so they don't
	// double-surface. postBind was computed above for that skip; reuse it.
	sched := k8s.DetectSchedulingProblems(cache, namespace)
	sched = append(sched, k8s.DetectAdmissionProblems(cache, namespace)...)
	sched = append(sched, postBind...)
	for _, p := range sched {
		problems = append(problems, DashboardProblem{
			Kind:            p.Kind,
			Namespace:       p.Namespace,
			Name:            p.Name,
			Severity:        p.Severity,
			Reason:          p.Reason,
			Message:         p.Message,
			Age:             p.Age,
			AgeSeconds:      p.AgeSeconds,
			Duration:        p.Duration,
			DurationSeconds: p.DurationSeconds,
		})
	}

	// CAPI problems (Cluster API resources)
	for _, p := range k8s.DetectCAPIProblems(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery(), namespace) {
		problems = append(problems, DashboardProblem{
			Kind:            p.Kind,
			Namespace:       p.Namespace,
			Name:            p.Name,
			Group:           p.Group,
			Severity:        p.Severity,
			Reason:          p.Reason,
			Message:         p.Message,
			Age:             p.Age,
			AgeSeconds:      p.AgeSeconds,
			Duration:        p.Duration,
			DurationSeconds: p.DurationSeconds,
		})
	}

	// Sort: critical first, then high, then medium; within each group sort by age (most recent first)
	// "warning" is below medium — degraded states that aren't immediate
	// failures. Info is inert/posture and ranks last. Unknown severities also
	// rank last instead of falling through to Go's zero value beside critical.
	severityOrder := map[string]int{"critical": 0, "high": 1, "medium": 2, "warning": 3, "info": 4}
	sort.SliceStable(problems, func(i, j int) bool {
		si, ok := severityOrder[problems[i].Severity]
		if !ok {
			si = len(severityOrder)
		}
		sj, ok := severityOrder[problems[j].Severity]
		if !ok {
			sj = len(severityOrder)
		}
		if si != sj {
			return si < sj
		}
		return problems[i].AgeSeconds < problems[j].AgeSeconds
	})

	return health, problems
}

// classifyPodHealth delegates to the shared implementation in k8s.ClassifyPodHealth.
func classifyPodHealth(pod *corev1.Pod, now time.Time) string {
	return k8s.ClassifyPodHealth(pod, now)
}

func podToProblem(pod *corev1.Pod, severity string, now time.Time) DashboardProblem {
	reason := ""
	message := ""

	// Find the most relevant issue
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
			reason = cs.State.Waiting.Reason
			message = cs.State.Waiting.Message
			break
		}
		if cs.State.Terminated != nil && cs.State.Terminated.Reason != "" {
			reason = cs.State.Terminated.Reason
			message = cs.State.Terminated.Message
			break
		}
		if cs.RestartCount > 3 {
			reason = fmt.Sprintf("RestartCount: %d", cs.RestartCount)
			break
		}
	}

	if reason == "" && pod.Status.Phase == corev1.PodPending {
		reason = "Pending"
		for _, cond := range pod.Status.Conditions {
			if cond.Status == corev1.ConditionFalse && cond.Message != "" {
				message = cond.Message
				break
			}
		}
	}

	if reason == "" && pod.Status.Phase == corev1.PodFailed {
		reason = "Failed"
		if pod.Status.Message != "" {
			message = pod.Status.Message
		}
	}

	ageDur := now.Sub(pod.CreationTimestamp.Time)
	durDur := podProblemDuration(pod, now)

	return DashboardProblem{
		Kind:            "Pod",
		Namespace:       pod.Namespace,
		Name:            pod.Name,
		Severity:        severity,
		Reason:          reason,
		Message:         k8s.Truncate(message, 200),
		Age:             k8s.FormatAge(ageDur),
		AgeSeconds:      int64(ageDur.Seconds()),
		Duration:        k8s.FormatAge(durDur),
		DurationSeconds: int64(durDur.Seconds()),
	}
}

// podProblemDuration estimates how long a pod has been in a problematic state.
// Uses condition lastTransitionTime when available, falls back to creation time.
func podProblemDuration(pod *corev1.Pod, now time.Time) time.Duration {
	// For pending pods: use the PodScheduled or Ready condition transition
	// For running-but-unhealthy: use ContainersReady condition transition
	// For failed: use the Ready condition transition
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.ContainersReady && cond.Status == corev1.ConditionFalse {
			if !cond.LastTransitionTime.IsZero() {
				return now.Sub(cond.LastTransitionTime.Time)
			}
		}
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionFalse {
			if !cond.LastTransitionTime.IsZero() {
				return now.Sub(cond.LastTransitionTime.Time)
			}
		}
		if cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionFalse {
			if !cond.LastTransitionTime.IsZero() {
				return now.Sub(cond.LastTransitionTime.Time)
			}
		}
	}
	// Fallback: use pod creation time
	return now.Sub(pod.CreationTimestamp.Time)
}

type ownerKey struct{ kind, namespace, name string }
type ownerGroup struct {
	podCount  int
	severity  string
	reasons   map[string]int
	newestDur time.Duration
	newestAge time.Duration
}

// collectPodForRollup groups a problematic pod under its owner workload, or adds it as an orphan.
func collectPodForRollup(pod *corev1.Pod, severity string, now time.Time, groups map[ownerKey]*ownerGroup, orphans *[]DashboardProblem) {
	// Find the workload owner (Deployment owns ReplicaSet owns Pod, so look for grandparent)
	var ownerKind, ownerName string
	for _, ref := range pod.OwnerReferences {
		if ref.Kind == "ReplicaSet" || ref.Kind == "StatefulSet" || ref.Kind == "DaemonSet" || ref.Kind == "Job" {
			ownerKind = ref.Kind
			ownerName = ref.Name
			break
		}
	}

	// For ReplicaSets, strip the hash suffix to get the Deployment name
	if ownerKind == "ReplicaSet" {
		ownerKind = "Deployment"
		if idx := strings.LastIndex(ownerName, "-"); idx > 0 {
			ownerName = ownerName[:idx]
		}
	}

	// No workload owner — orphan pod
	if ownerKind == "" {
		*orphans = append(*orphans, podToProblem(pod, severity, now))
		return
	}

	key := ownerKey{ownerKind, pod.Namespace, ownerName}
	g, ok := groups[key]
	if !ok {
		g = &ownerGroup{
			reasons:   make(map[string]int),
			newestDur: 1<<62 - 1,
			newestAge: 1<<62 - 1,
		}
		groups[key] = g
	}

	g.podCount++
	// Keep worst severity: critical > high > medium
	order := map[string]int{"critical": 0, "high": 1, "medium": 2, "warning": 3}
	if g.severity == "" || order[severity] < order[g.severity] {
		g.severity = severity
	}

	reason := k8s.PodProblemReason(pod)
	if reason != "" {
		g.reasons[reason]++
	}

	ageDur := now.Sub(pod.CreationTimestamp.Time)
	durDur := podProblemDuration(pod, now)
	if durDur < g.newestDur {
		g.newestDur = durDur
	}
	if ageDur < g.newestAge {
		g.newestAge = ageDur
	}
}

func (s *Server) getDashboardResourceCounts(cache *k8s.ResourceCache, namespace string) DashboardResourceCounts {
	counts := DashboardResourceCounts{}
	var restricted []string

	// Pods
	var pods []*corev1.Pod
	if podLister := cache.Pods(); podLister != nil {
		if namespace != "" {
			pods, _ = podLister.Pods(namespace).List(labels.Everything())
		} else {
			pods, _ = podLister.List(labels.Everything())
		}
	} else {
		restricted = append(restricted, "pods")
	}
	counts.Pods.Total = len(pods)
	for _, pod := range pods {
		switch pod.Status.Phase {
		case corev1.PodRunning:
			counts.Pods.Running++
		case corev1.PodPending:
			counts.Pods.Pending++
		case corev1.PodFailed:
			counts.Pods.Failed++
		case corev1.PodSucceeded:
			counts.Pods.Succeeded++
		}
	}

	// Deployments
	if depLister := cache.Deployments(); depLister != nil {
		if namespace != "" {
			deps, _ := depLister.Deployments(namespace).List(labels.Everything())
			counts.Deployments.Total = len(deps)
			for _, d := range deps {
				if d.Status.AvailableReplicas == d.Status.Replicas && d.Status.Replicas > 0 {
					counts.Deployments.Available++
				} else if d.Status.Replicas > 0 {
					counts.Deployments.Unavailable++
				}
			}
		} else {
			deps, _ := depLister.List(labels.Everything())
			counts.Deployments.Total = len(deps)
			for _, d := range deps {
				if d.Status.AvailableReplicas == d.Status.Replicas && d.Status.Replicas > 0 {
					counts.Deployments.Available++
				} else if d.Status.Replicas > 0 {
					counts.Deployments.Unavailable++
				}
			}
		}
	} else {
		restricted = append(restricted, "deployments")
	}

	// StatefulSets (only count those with replicas > 0)
	if ssLister := cache.StatefulSets(); ssLister != nil {
		if namespace != "" {
			ssets, _ := ssLister.StatefulSets(namespace).List(labels.Everything())
			for _, ss := range ssets {
				if ss.Status.Replicas == 0 {
					continue
				}
				counts.StatefulSets.Total++
				if ss.Status.ReadyReplicas == ss.Status.Replicas {
					counts.StatefulSets.Ready++
				} else {
					counts.StatefulSets.Unready++
				}
			}
		} else {
			ssets, _ := ssLister.List(labels.Everything())
			for _, ss := range ssets {
				if ss.Status.Replicas == 0 {
					continue
				}
				counts.StatefulSets.Total++
				if ss.Status.ReadyReplicas == ss.Status.Replicas {
					counts.StatefulSets.Ready++
				} else {
					counts.StatefulSets.Unready++
				}
			}
		}
	} else {
		restricted = append(restricted, "statefulsets")
	}

	// DaemonSets (only count those with desired > 0)
	if dsLister := cache.DaemonSets(); dsLister != nil {
		if namespace != "" {
			dsets, _ := dsLister.DaemonSets(namespace).List(labels.Everything())
			for _, ds := range dsets {
				if ds.Status.DesiredNumberScheduled == 0 {
					continue
				}
				counts.DaemonSets.Total++
				if ds.Status.NumberUnavailable == 0 {
					counts.DaemonSets.Ready++
				} else {
					counts.DaemonSets.Unready++
				}
			}
		} else {
			dsets, _ := dsLister.List(labels.Everything())
			for _, ds := range dsets {
				if ds.Status.DesiredNumberScheduled == 0 {
					continue
				}
				counts.DaemonSets.Total++
				if ds.Status.NumberUnavailable == 0 {
					counts.DaemonSets.Ready++
				} else {
					counts.DaemonSets.Unready++
				}
			}
		}
	} else {
		restricted = append(restricted, "daemonsets")
	}

	// Services
	if svcLister := cache.Services(); svcLister != nil {
		if namespace != "" {
			svcs, _ := svcLister.Services(namespace).List(labels.Everything())
			counts.Services = len(svcs)
		} else {
			svcs, _ := svcLister.List(labels.Everything())
			counts.Services = len(svcs)
		}
	} else {
		restricted = append(restricted, "services")
	}

	// Ingresses
	if ingLister := cache.Ingresses(); ingLister != nil {
		if namespace != "" {
			ings, _ := ingLister.Ingresses(namespace).List(labels.Everything())
			counts.Ingresses = len(ings)
		} else {
			ings, _ := ingLister.List(labels.Everything())
			counts.Ingresses = len(ings)
		}
	} else {
		restricted = append(restricted, "ingresses")
	}

	// Gateways and routes (via dynamic cache)
	dynamicCache := k8s.GetDynamicResourceCache()
	resourceDiscovery := k8s.GetResourceDiscovery()
	if dynamicCache != nil && resourceDiscovery != nil {
		if gwGVR, ok := resourceDiscovery.GetGVR("Gateway"); ok {
			gateways, err := dynamicCache.List(gwGVR, namespace)
			if err != nil {
				log.Printf("WARNING [dashboard] Failed to count Gateways: %v", err)
			} else {
				counts.Gateways = len(gateways)
			}
		}
		for _, routeKind := range []string{"HTTPRoute", "GRPCRoute", "TCPRoute", "TLSRoute"} {
			if rGVR, ok := resourceDiscovery.GetGVR(routeKind); ok {
				routes, err := dynamicCache.List(rGVR, namespace)
				if err != nil {
					log.Printf("WARNING [dashboard] Failed to count %s: %v", routeKind, err)
				} else {
					counts.Routes += len(routes)
				}
			}
		}
	}

	// Jobs
	if jobLister := cache.Jobs(); jobLister != nil {
		if namespace != "" {
			jobList, _ := jobLister.Jobs(namespace).List(labels.Everything())
			counts.Jobs.Total = len(jobList)
			for _, j := range jobList {
				if j.Status.Active > 0 {
					counts.Jobs.Active++
				}
				counts.Jobs.Succeeded += int(j.Status.Succeeded)
				counts.Jobs.Failed += int(j.Status.Failed)
			}
		} else {
			jobList, _ := jobLister.List(labels.Everything())
			counts.Jobs.Total = len(jobList)
			for _, j := range jobList {
				if j.Status.Active > 0 {
					counts.Jobs.Active++
				}
				counts.Jobs.Succeeded += int(j.Status.Succeeded)
				counts.Jobs.Failed += int(j.Status.Failed)
			}
		}
	} else {
		restricted = append(restricted, "jobs")
	}

	// CronJobs
	if cjLister := cache.CronJobs(); cjLister != nil {
		if namespace != "" {
			cronJobs, _ := cjLister.CronJobs(namespace).List(labels.Everything())
			counts.CronJobs.Total = len(cronJobs)
			for _, cj := range cronJobs {
				if cj.Spec.Suspend != nil && *cj.Spec.Suspend {
					counts.CronJobs.Suspended++
				} else if len(cj.Status.Active) > 0 {
					counts.CronJobs.Active++
				}
			}
		} else {
			cronJobs, _ := cjLister.List(labels.Everything())
			counts.CronJobs.Total = len(cronJobs)
			for _, cj := range cronJobs {
				if cj.Spec.Suspend != nil && *cj.Spec.Suspend {
					counts.CronJobs.Suspended++
				} else if len(cj.Status.Active) > 0 {
					counts.CronJobs.Active++
				}
			}
		}
	} else {
		restricted = append(restricted, "cronjobs")
	}

	// ConfigMaps
	if cmLister := cache.ConfigMaps(); cmLister != nil {
		if namespace != "" {
			cms, _ := cmLister.ConfigMaps(namespace).List(labels.Everything())
			counts.ConfigMaps = len(cms)
		} else {
			cms, _ := cmLister.List(labels.Everything())
			counts.ConfigMaps = len(cms)
		}
	}

	// Secrets (may be nil if RBAC doesn't allow listing secrets)
	if secretsLister := cache.Secrets(); secretsLister != nil {
		if namespace != "" {
			secrets, _ := secretsLister.Secrets(namespace).List(labels.Everything())
			counts.Secrets = len(secrets)
		} else {
			secrets, _ := secretsLister.List(labels.Everything())
			counts.Secrets = len(secrets)
		}
	}

	// PVCs
	if pvcLister := cache.PersistentVolumeClaims(); pvcLister != nil {
		if namespace != "" {
			pvcs, _ := pvcLister.PersistentVolumeClaims(namespace).List(labels.Everything())
			counts.PVCs.Total = len(pvcs)
			for _, pvc := range pvcs {
				switch pvc.Status.Phase {
				case corev1.ClaimBound:
					counts.PVCs.Bound++
				case corev1.ClaimPending:
					counts.PVCs.Pending++
				default:
					counts.PVCs.Unbound++
				}
			}
		} else {
			pvcs, _ := pvcLister.List(labels.Everything())
			counts.PVCs.Total = len(pvcs)
			for _, pvc := range pvcs {
				switch pvc.Status.Phase {
				case corev1.ClaimBound:
					counts.PVCs.Bound++
				case corev1.ClaimPending:
					counts.PVCs.Pending++
				default:
					counts.PVCs.Unbound++
				}
			}
		}
	}

	counts.Restricted = restricted
	return counts
}

func (s *Server) getDashboardRecentEvents(cache *k8s.ResourceCache, namespace string) []DashboardEvent {
	eventLister := cache.Events()
	if eventLister == nil {
		return []DashboardEvent{}
	}
	var events []*corev1.Event
	var err error
	if namespace != "" {
		events, err = eventLister.Events(namespace).List(labels.Everything())
	} else {
		events, err = eventLister.List(labels.Everything())
	}
	if err != nil || len(events) == 0 {
		return []DashboardEvent{}
	}

	// Filter to Warning events only and sort by last timestamp desc
	var warnings []*corev1.Event
	for _, e := range events {
		if e.Type == "Warning" {
			warnings = append(warnings, e)
		}
	}

	sort.Slice(warnings, func(i, j int) bool {
		ci := max(warnings[i].Count, 1)
		cj := max(warnings[j].Count, 1)
		if ci != cj {
			return ci > cj
		}
		ti := warnings[i].LastTimestamp.Time
		tj := warnings[j].LastTimestamp.Time
		if ti.IsZero() {
			ti = warnings[i].CreationTimestamp.Time
		}
		if tj.IsZero() {
			tj = warnings[j].CreationTimestamp.Time
		}
		return ti.After(tj)
	})

	// Take top 5
	limit := min(len(warnings), 5)

	result := make([]DashboardEvent, 0, limit)
	for _, e := range warnings[:limit] {
		ts := e.LastTimestamp.Time
		if ts.IsZero() {
			ts = e.CreationTimestamp.Time
		}
		result = append(result, DashboardEvent{
			Type:           e.Type,
			Reason:         e.Reason,
			Message:        k8s.Truncate(e.Message, 200),
			InvolvedObject: fmt.Sprintf("%s/%s", e.InvolvedObject.Kind, e.InvolvedObject.Name),
			Namespace:      e.Namespace,
			Timestamp:      ts.Format(time.RFC3339),
			Count:          max(e.Count, 1),
		})
	}

	return result
}

func (s *Server) getDashboardRecentChanges(ctx context.Context, namespaces []string) []DashboardChange {
	store := timeline.GetStore()
	if store == nil {
		return []DashboardChange{}
	}

	opts := timeline.QueryOptions{
		Namespaces:     namespaces,
		Since:          time.Now().Add(-1 * time.Hour),
		Limit:          5,
		FilterPreset:   "workloads",
		ClusterContext: k8s.ActiveClusterContext(),
	}

	events, err := store.Query(ctx, opts)
	if err != nil || len(events) == 0 {
		return []DashboardChange{}
	}

	result := make([]DashboardChange, 0, len(events))
	for _, e := range events {
		summary := ""
		if e.Diff != nil && e.Diff.Summary != "" {
			summary = e.Diff.Summary
		} else if e.Message != "" {
			summary = k8s.Truncate(e.Message, 100)
		}

		result = append(result, DashboardChange{
			Kind:       e.Kind,
			Namespace:  e.Namespace,
			Name:       e.Name,
			ChangeType: string(e.EventType),
			Summary:    summary,
			Timestamp:  e.Timestamp.Format(time.RFC3339),
		})
	}

	return result
}

func (s *Server) getDashboardTopologySummary(namespaces []string) DashboardTopologySummary {
	// Use cached topology only when no namespace filter is active,
	// since the cached topology's namespace scope may not match the request.
	if namespaces == nil {
		if cachedTopo := s.broadcaster.GetCachedTopology(); cachedTopo != nil {
			return DashboardTopologySummary{
				NodeCount: len(cachedTopo.Nodes),
				EdgeCount: len(cachedTopo.Edges),
			}
		}
	}

	// Build topology with the requested namespace filter
	opts := topology.DefaultBuildOptions()
	opts.Namespaces = namespaces
	builder := topology.NewBuilder(k8s.NewTopologyResourceProvider(k8s.GetResourceCache())).WithDynamic(k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery()))
	topo, err := builder.Build(opts)
	if err != nil {
		log.Printf("[dashboard] Failed to build topology summary: %v", err)
		return DashboardTopologySummary{}
	}

	return DashboardTopologySummary{
		NodeCount: len(topo.Nodes),
		EdgeCount: len(topo.Edges),
	}
}

func (s *Server) getDashboardTrafficSummary(ctx context.Context, namespaces []string) *DashboardTrafficSummary {
	manager := traffic.GetManager()
	if manager == nil {
		return nil
	}

	sourceName := manager.GetActiveSourceName()
	if sourceName == "" {
		return nil
	}

	opts := traffic.DefaultFlowOptions()
	// Traffic only supports single namespace filter for now
	if len(namespaces) == 1 {
		opts.Namespace = namespaces[0]
	}

	response, err := manager.GetFlows(ctx, opts)
	if err != nil || len(response.Flows) == 0 {
		return &DashboardTrafficSummary{
			Source:    sourceName,
			FlowCount: 0,
			TopFlows:  []DashboardTopFlow{},
		}
	}

	// Aggregate flows
	aggregated := traffic.AggregateFlows(response.Flows)

	// Sort by connection count
	sort.Slice(aggregated, func(i, j int) bool {
		return aggregated[i].Connections > aggregated[j].Connections
	})

	topFlows := make([]DashboardTopFlow, 0, 3)
	limit := min(len(aggregated), 3)
	for _, f := range aggregated[:limit] {
		srcName := f.Source.Name
		if f.Source.Workload != "" {
			srcName = f.Source.Workload
		}
		dstName := f.Destination.Name
		if f.Destination.Workload != "" {
			dstName = f.Destination.Workload
		}
		topFlows = append(topFlows, DashboardTopFlow{
			Src:         srcName,
			Dst:         dstName,
			Connections: f.Connections,
		})
	}

	return &DashboardTrafficSummary{
		Source:    sourceName,
		FlowCount: len(aggregated),
		TopFlows:  topFlows,
	}
}

func (s *Server) getDashboardHelmSummary(r *http.Request, namespace string) DashboardHelmSummary {
	helmClient := helm.GetClient()
	if helmClient == nil {
		return DashboardHelmSummary{
			Releases:  []DashboardHelmRelease{},
			Error:     "Helm client not initialized",
			ErrorCode: ErrCodeUnconfigured,
		}
	}

	var username string
	var groups []string
	if user := auth.UserFromContext(r.Context()); user != nil {
		username = user.Username
		groups = user.Groups
	}
	releases, err := helmClient.ListReleasesAsUser(namespace, username, groups)
	if err != nil {
		if helm.IsForbiddenError(err) {
			return DashboardHelmSummary{Releases: []DashboardHelmRelease{}, Restricted: true}
		}
		log.Printf("[dashboard] Failed to list Helm releases: %v", err)
		return DashboardHelmSummary{
			Releases:  []DashboardHelmRelease{},
			Error:     err.Error(),
			ErrorCode: errorCodeForHelm(err.Error(), 0),
		}
	}

	result := DashboardHelmSummary{
		Total: len(releases),
	}

	// Sort: failed/unhealthy releases first to surface problems
	sort.SliceStable(releases, func(i, j int) bool {
		pi := helm.StatusPriority(releases[i].Status, releases[i].ResourceHealth)
		pj := helm.StatusPriority(releases[j].Status, releases[j].ResourceHealth)
		return pi < pj
	})

	// Take top 6 releases
	limit := min(len(releases), 6)

	result.Releases = make([]DashboardHelmRelease, 0, limit)
	for _, r := range releases[:limit] {
		result.Releases = append(result.Releases, DashboardHelmRelease{
			Name:           r.Name,
			Namespace:      r.Namespace,
			Chart:          r.Chart,
			ChartVersion:   r.ChartVersion,
			Status:         r.Status,
			ResourceHealth: r.ResourceHealth,
		})
	}

	return result
}

func (s *Server) countWarningEvents(cache *k8s.ResourceCache, namespace string) int {
	eventLister := cache.Events()
	if eventLister == nil {
		return 0
	}
	var events []*corev1.Event
	if namespace != "" {
		events, _ = eventLister.Events(namespace).List(labels.Everything())
	} else {
		events, _ = eventLister.List(labels.Everything())
	}
	count := 0
	for _, e := range events {
		if e.Type == "Warning" {
			count++
		}
	}
	return count
}

// metricsServerTimeout bounds the metrics-server query so a slow or unreachable
// metrics endpoint can't consume the dashboard's whole request budget (the
// handler runs this in a goroutine and blocks on it in wg.Wait()).
const metricsServerTimeout = 8 * time.Second

func (s *Server) getDashboardMetrics(ctx context.Context, allowedNamespaces []string) *DashboardMetrics {
	// Node capacity and pod requests come from the cache — they don't need
	// metrics-server. Live usage does. Compute the cached values first and
	// treat usage as best-effort so a missing/slow metrics-server still leaves
	// requests vs. capacity on the dashboard.
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil
	}
	nodeLister := cache.Nodes()
	if nodeLister == nil {
		return nil
	}
	nodes, _ := nodeLister.List(labels.Everything())
	if len(nodes) == 0 {
		return nil
	}

	// Sum capacity across all nodes (cached)
	var cpuCapacityMillis int64
	var memCapacityBytes int64
	for _, n := range nodes {
		cpuCapacityMillis += n.Status.Capacity.Cpu().MilliValue()
		memCapacityBytes += n.Status.Capacity.Memory().Value()
	}

	// Sum requests across pods the user can see (cached). For namespace-
	// restricted users this scopes to allowedNamespaces — without it, the
	// dashboard would expose aggregate pod-resource totals from namespaces they
	// have no read access to.
	var cpuRequestsMillis int64
	var memRequestsBytes int64
	var metricPods []*corev1.Pod
	if podLister := cache.Pods(); podLister != nil {
		if allowedNamespaces == nil {
			metricPods, _ = podLister.List(labels.Everything())
		} else {
			for _, ns := range allowedNamespaces {
				items, _ := podLister.Pods(ns).List(labels.Everything())
				metricPods = append(metricPods, items...)
			}
		}
	}
	for _, pod := range metricPods {
		// Skip completed/failed pods
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		for _, container := range pod.Spec.Containers {
			if container.Resources.Requests != nil {
				if cpu, ok := container.Resources.Requests[corev1.ResourceCPU]; ok {
					cpuRequestsMillis += cpu.MilliValue()
				}
				if mem, ok := container.Resources.Requests[corev1.ResourceMemory]; ok {
					memRequestsBytes += mem.Value()
				}
			}
		}
	}

	// Live usage from metrics-server — best-effort. Query via raw REST to avoid
	// adding k8s.io/metrics dependency; metrics-server forwards impersonation
	// headers, so a user without metrics.k8s.io/nodes access gets a 403. A
	// missing, forbidden, or slow metrics-server leaves usageAvailable false
	// rather than discarding the capacity/request data computed above.
	var cpuUsageMillis int64
	var memUsageBytes int64
	usageAvailable := false
	if client := k8s.ClientFromContext(ctx); client != nil {
		mctx, cancel := context.WithTimeout(ctx, metricsServerTimeout)
		defer cancel()
		data, err := client.CoreV1().RESTClient().Get().
			AbsPath("/apis/metrics.k8s.io/v1beta1/nodes").
			DoRaw(mctx)
		if err != nil {
			log.Printf("[dashboard] node metrics unavailable (showing requests/capacity only): %v", err)
		} else {
			var nodeMetricsList struct {
				Items []struct {
					Usage struct {
						CPU    string `json:"cpu"`
						Memory string `json:"memory"`
					} `json:"usage"`
				} `json:"items"`
			}
			if err := json.Unmarshal(data, &nodeMetricsList); err != nil {
				log.Printf("[dashboard] failed to parse node metrics: %v", err)
			} else if len(nodeMetricsList.Items) > 0 {
				for _, item := range nodeMetricsList.Items {
					cpuUsageMillis += parseCPUToMillis(item.Usage.CPU)
					memUsageBytes += parseMemoryToBytes(item.Usage.Memory)
				}
				usageAvailable = true
			}
		}
	}

	metrics := &DashboardMetrics{UsageAvailable: usageAvailable}
	if cpuCapacityMillis > 0 {
		metrics.CPU = &MetricSummary{
			UsageMillis:    cpuUsageMillis,
			RequestsMillis: cpuRequestsMillis,
			CapacityMillis: cpuCapacityMillis,
			UsagePercent:   int(cpuUsageMillis * 100 / cpuCapacityMillis),
			RequestPercent: int(cpuRequestsMillis * 100 / cpuCapacityMillis),
		}
	}
	if memCapacityBytes > 0 {
		// Convert bytes to MiB for the "millis" fields (repurposed as MiB)
		memUsageMiB := memUsageBytes / (1024 * 1024)
		memRequestsMiB := memRequestsBytes / (1024 * 1024)
		memCapacityMiB := memCapacityBytes / (1024 * 1024)
		metrics.Memory = &MetricSummary{
			UsageMillis:    memUsageMiB,
			RequestsMillis: memRequestsMiB,
			CapacityMillis: memCapacityMiB,
			UsagePercent:   int(memUsageMiB * 100 / memCapacityMiB),
			RequestPercent: int(memRequestsMiB * 100 / memCapacityMiB),
		}
	}

	return metrics
}

// parseCPUToMillis delegates to k8s.ParseCPUToMillis.
func parseCPUToMillis(s string) int64 { return k8s.ParseCPUToMillis(s) }

// parseMemoryToBytes delegates to k8s.ParseMemoryToBytes.
func parseMemoryToBytes(s string) int64 { return k8s.ParseMemoryToBytes(s) }

// Helper functions

// collectClusterScopedCRDCounts returns counts of cluster-scoped CRD instances
// the calling user is authorized to see. Each cluster-scoped CRD is gated by
// a per-kind SubjectAccessReview; CRDs the user can't list are omitted.
func (s *Server) collectClusterScopedCRDCounts(r *http.Request) []DashboardCRDCount {
	disc := k8s.GetResourceDiscovery()
	dynamicCache := k8s.GetDynamicResourceCache()
	if disc == nil || dynamicCache == nil {
		return nil
	}
	resources, err := disc.GetAPIResources()
	if err != nil {
		return nil
	}

	seen := make(map[string]bool)
	var counts []DashboardCRDCount
	for _, res := range resources {
		if !res.IsCRD || res.Namespaced {
			continue
		}
		key := res.Group + "/" + res.Kind
		if seen[key] {
			continue
		}
		seen[key] = true

		// Per-kind cluster-scoped SAR. canRead caches the result on
		// UserPermissions and short-circuits when auth is disabled.
		if !s.canRead(r, res.Group, res.Name, "", "list") {
			continue
		}
		gvr, ok := disc.GetGVRWithGroup(res.Kind, res.Group)
		if !ok {
			continue
		}
		if !dynamicCache.IsSynced(gvr) {
			continue
		}
		items, err := dynamicCache.List(gvr, "")
		if err != nil {
			log.Printf("WARNING [dashboard] Failed to count cluster-scoped CRD %s.%s: %v", res.Name, res.Group, err)
			continue
		}
		if len(items) > 0 {
			counts = append(counts, DashboardCRDCount{
				Kind:  res.Kind,
				Name:  res.Name,
				Group: res.Group,
				Count: len(items),
			})
		}
	}
	return counts
}

// collectNamespacedCRDCounts returns counts of namespaced CRD instances.
//
// allowed semantics (matches parseNamespacesForUser):
//   - nil:        cluster-wide listing (auth off or cluster-wide namespaced
//     access). One call to dynamicCache.List with namespace="".
//   - empty:      user has no namespace access; returns nil.
//   - non-empty:  iterate per allowed namespace and sum.
func (s *Server) collectNamespacedCRDCounts(_ context.Context, allowed []string) []DashboardCRDCount {
	if allowed != nil && len(allowed) == 0 {
		return nil
	}
	disc := k8s.GetResourceDiscovery()
	dynamicCache := k8s.GetDynamicResourceCache()
	if disc == nil || dynamicCache == nil {
		return nil
	}
	resources, err := disc.GetAPIResources()
	if err != nil {
		return nil
	}

	seen := make(map[string]bool)
	var counts []DashboardCRDCount
	for _, res := range resources {
		if !res.IsCRD || !res.Namespaced {
			continue
		}
		key := res.Group + "/" + res.Kind
		if seen[key] {
			continue
		}
		seen[key] = true

		gvr, ok := disc.GetGVRWithGroup(res.Kind, res.Group)
		if !ok {
			continue
		}
		if !dynamicCache.IsSynced(gvr) {
			continue
		}
		total := 0
		if allowed == nil {
			// Cluster-wide list across all namespaces.
			items, err := dynamicCache.List(gvr, "")
			if err != nil {
				log.Printf("WARNING [dashboard] Failed to count namespaced CRD %s.%s cluster-wide: %v", k8s.SanitizeForLog(res.Name), k8s.SanitizeForLog(res.Group), err)
				continue
			}
			total = len(items)
		} else {
			for _, ns := range allowed {
				items, err := dynamicCache.List(gvr, ns)
				if err != nil {
					log.Printf("WARNING [dashboard] Failed to count namespaced CRD %s.%s in ns=%s: %v", k8s.SanitizeForLog(res.Name), k8s.SanitizeForLog(res.Group), k8s.SanitizeForLog(ns), err)
					continue
				}
				total += len(items)
			}
		}
		if total > 0 {
			counts = append(counts, DashboardCRDCount{
				Kind:  res.Kind,
				Name:  res.Name,
				Group: res.Group,
				Count: total,
			})
		}
	}
	return counts
}

// mergeCRDCounts combines two CRD count lists, sorts by count descending,
// and trims to the top 8 (matching the cluster-wide getDashboardCRDCounts
// behavior).
func mergeCRDCounts(a, b []DashboardCRDCount) []DashboardCRDCount {
	merged := make([]DashboardCRDCount, 0, len(a)+len(b))
	merged = append(merged, a...)
	merged = append(merged, b...)
	sort.Slice(merged, func(i, j int) bool { return merged[i].Count > merged[j].Count })
	if len(merged) > 8 {
		merged = merged[:8]
	}
	return merged
}

// DashboardNetworkPolicyCoverage reports how many workloads are covered by at least one NetworkPolicy.
type DashboardNetworkPolicyCoverage struct {
	TotalPolicies    int `json:"totalPolicies"`
	CoveredWorkloads int `json:"coveredWorkloads"`
	TotalWorkloads   int `json:"totalWorkloads"`
}

type npSelector struct {
	namespace string
	selector  labels.Selector
}

func (s *Server) getDashboardNetworkPolicyCoverage(cache *k8s.ResourceCache, namespaces []string) *DashboardNetworkPolicyCoverage {
	npLister := cache.NetworkPolicies()
	if npLister == nil {
		return nil
	}

	var allNPs []npSelector
	if len(namespaces) == 0 {
		nps, err := npLister.List(labels.Everything())
		if err != nil {
			log.Printf("[dashboard] Failed to list NetworkPolicies: %v", err)
			return nil
		}
		for _, np := range nps {
			sel, err := metav1.LabelSelectorAsSelector(&np.Spec.PodSelector)
			if err != nil {
				continue
			}
			allNPs = append(allNPs, npSelector{np.Namespace, sel})
		}
	} else {
		for _, ns := range namespaces {
			nps, err := npLister.NetworkPolicies(ns).List(labels.Everything())
			if err != nil {
				log.Printf("[dashboard] Failed to list NetworkPolicies in namespace %s: %v", ns, err)
				continue
			}
			for _, np := range nps {
				sel, err := metav1.LabelSelectorAsSelector(&np.Spec.PodSelector)
				if err != nil {
					continue
				}
				allNPs = append(allNPs, npSelector{np.Namespace, sel})
			}
		}
	}

	if dynamicCache := k8s.GetDynamicResourceCache(); dynamicCache != nil {
		if discovery := k8s.GetResourceDiscovery(); discovery != nil {
			if cnpGVR, ok := discovery.GetGVR("CiliumNetworkPolicy"); ok {
				nsFilter := ""
				if len(namespaces) == 1 {
					nsFilter = namespaces[0]
				}
				cnps, err := dynamicCache.List(cnpGVR, nsFilter)
				if err == nil {
					for _, cnp := range cnps {
						ns := cnp.GetNamespace()
						if len(namespaces) > 1 && !slices.Contains(namespaces, ns) {
							continue
						}
						selectorMap, _, _ := unstructured.NestedMap(cnp.Object, "spec", "endpointSelector", "matchLabels")
						if len(selectorMap) == 0 {
							allNPs = append(allNPs, npSelector{ns, labels.Everything()})
						} else {
							selectorLabels := make(map[string]string)
							for k, v := range selectorMap {
								if sv, ok := v.(string); ok {
									selectorLabels[k] = sv
								}
							}
							if sel, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{MatchLabels: selectorLabels}); err == nil {
								allNPs = append(allNPs, npSelector{ns, sel})
							}
						}
					}
				}
			}
		}
	}

	covered := make(map[string]bool)
	totalWorkloads := 0

	checkCoverage := func(kind, ns, name string, templateLabels map[string]string) {
		key := kind + "/" + ns + "/" + name
		totalWorkloads++
		for _, np := range allNPs {
			if np.namespace != ns {
				continue
			}
			if np.selector.Matches(labels.Set(templateLabels)) {
				covered[key] = true
				break
			}
		}
	}

	if depLister := cache.Deployments(); depLister != nil {
		if len(namespaces) == 0 {
			deps, _ := depLister.List(labels.Everything())
			for _, d := range deps {
				checkCoverage("Deployment", d.Namespace, d.Name, d.Spec.Template.Labels)
			}
		} else {
			for _, ns := range namespaces {
				deps, _ := depLister.Deployments(ns).List(labels.Everything())
				for _, d := range deps {
					checkCoverage("Deployment", d.Namespace, d.Name, d.Spec.Template.Labels)
				}
			}
		}
	}

	if stsLister := cache.StatefulSets(); stsLister != nil {
		if len(namespaces) == 0 {
			stss, _ := stsLister.List(labels.Everything())
			for _, s := range stss {
				checkCoverage("StatefulSet", s.Namespace, s.Name, s.Spec.Template.Labels)
			}
		} else {
			for _, ns := range namespaces {
				stss, _ := stsLister.StatefulSets(ns).List(labels.Everything())
				for _, s := range stss {
					checkCoverage("StatefulSet", s.Namespace, s.Name, s.Spec.Template.Labels)
				}
			}
		}
	}

	if dsLister := cache.DaemonSets(); dsLister != nil {
		if len(namespaces) == 0 {
			dss, _ := dsLister.List(labels.Everything())
			for _, d := range dss {
				checkCoverage("DaemonSet", d.Namespace, d.Name, d.Spec.Template.Labels)
			}
		} else {
			for _, ns := range namespaces {
				dss, _ := dsLister.DaemonSets(ns).List(labels.Everything())
				for _, d := range dss {
					checkCoverage("DaemonSet", d.Namespace, d.Name, d.Spec.Template.Labels)
				}
			}
		}
	}

	return &DashboardNetworkPolicyCoverage{
		TotalPolicies:    len(allNPs),
		CoveredWorkloads: len(covered),
		TotalWorkloads:   totalWorkloads,
	}
}

// DashboardAudit is the audit summary in the dashboard response.
type DashboardAudit struct {
	Passing    int                                 `json:"passing"`
	Warning    int                                 `json:"warning"`
	Danger     int                                 `json:"danger"`
	Categories map[string]DashboardCategorySummary `json:"categories"`
}

// DashboardCategorySummary provides per-category counts for the dashboard.
type DashboardCategorySummary struct {
	Passing int `json:"passing"`
	Warning int `json:"warning"`
	Danger  int `json:"danger"`
}

func getDashboardAudit(cache *k8s.ResourceCache, namespaces []string) *DashboardAudit {
	results := applyAuditSettings(getCachedResults(cache, namespaces), getAuditConfig())
	if results == nil {
		return nil
	}
	cats := make(map[string]DashboardCategorySummary, len(results.Summary.Categories))
	for k, v := range results.Summary.Categories {
		cats[k] = DashboardCategorySummary{
			Passing: v.Passing,
			Warning: v.Warning,
			Danger:  v.Danger,
		}
	}
	return &DashboardAudit{
		Passing:    results.Summary.Passing,
		Warning:    results.Summary.Warning,
		Danger:     results.Summary.Danger,
		Categories: cats,
	}
}
