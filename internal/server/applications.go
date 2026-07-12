package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/k8s"
	gitopsinsights "github.com/skyhook-io/radar/pkg/gitops/insights"
	"github.com/skyhook-io/radar/pkg/health"
	"github.com/skyhook-io/radar/pkg/packages"
	"github.com/skyhook-io/radar/pkg/subject"
	"github.com/skyhook-io/radar/pkg/topology"
)

// Applications is the workload-centric twin of /api/packages. Where packages
// answers "what software is installed" (chart/GitOps-declaration centric, the
// Add-ons surface), Applications answers "what deployable/owned software units
// run here, what runtime class they have, and what version they run" — the unit
// is a logical app/release grouping over concrete workloads.
//
// What defines an app boundary: the K8s STRUCTURAL relationship graph is the
// spine. A workload's app is its topmost EdgeManages ancestor — the root that
// collapses native owner chains (Pod→RS→Deployment), in-cluster GitOps managers
// (an ArgoCD Application / Flux Kustomization / Flux HelmRelease that manages a
// set of workloads), and generic-CRD owners. The pkg/subject Tier-2 label
// overlay (app.kubernetes.io/part-of, Argo/Flux/Helm signals) then CONSOLIDATES
// roots the graph can't connect — hub-spoke Argo (controller in another
// cluster), native-Helm release annotations — with a confidence score. Roots
// and overlay keys are unioned per workload; satellites (Services/Ingress/
// config/scalers/PDBs) are ATTACHED to an app via the same graph but never
// merge two apps that merely share one (the over-merge guardrail). Nothing is
// hidden: a singleton workload with no signal is its own raw row, and add-on
// machinery is classified with evidence rather than dropped.

// applicationsResponse is the GET /api/applications body.
type applicationsResponse struct {
	Applications []appRow    `json:"applications"`
	ArgoClaims   []argoClaim `json:"argoClaims,omitempty"`
}

// argoClaim propagates a declared Argo Application identity to the cluster its
// workloads actually run in. In hub-spoke Argo the Application CR lives in a
// control cluster while its workloads run in a member cluster, so this cluster's
// workload rows never see the Application — only the fleet hub, which knows every
// cluster, can stamp the identity onto the destination's rows. Emitted only for
// Applications with a DECLARED-portable identity (Argo source path / validated
// ApplicationSet fan-out); name/label apps are never propagated cross-cluster.
type argoClaim struct {
	Identity      *appIdentity  `json:"identity"`
	DestServer    string        `json:"destServer,omitempty"`
	DestName      string        `json:"destName,omitempty"`
	DestNamespace string        `json:"destNamespace,omitempty"`
	Workloads     []workloadRef `json:"workloads,omitempty"` // managed workloads (status.resources)
}

// workloadRef identifies one managed workload for the hub to match against a
// destination cluster's rows.
type workloadRef struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

const applicationsCacheTTL = 60 * time.Second

var applicationsCacheMaxEntries = 256

var (
	applicationsCacheMu sync.Mutex
	applicationsCache   = map[string]applicationsCacheEntry{}
)

type applicationsCacheEntry struct {
	at     time.Time
	rows   []appRow
	claims []argoClaim
}

// appRow is one logical app in this cluster.
type appRow struct {
	Key           string            `json:"key"`                      // overlay key, structural-root key, or "<ns>/<kind>/<name>" raw
	Name          string            `json:"name"`                     // display name
	Namespace     string            `json:"namespace,omitempty"`      // the single namespace the WORKLOADS run in (residence, not the GitOps manager's home); empty when they span several — see Namespaces
	Namespaces    []string          `json:"namespaces,omitempty"`     // all distinct workload namespaces, sorted; the unambiguous form of Namespace
	Tier          int               `json:"tier,omitempty"`           // pkg/subject overlay tier (0 = raw, no signal)
	Confidence    string            `json:"confidence,omitempty"`     // high | medium | low
	Category      string            `json:"category,omitempty"`       // app | addon | mixed; classification hint, never identity
	AddonReason   string            `json:"addonReason,omitempty"`    // add-on evidence when Category == addon/mixed
	WorkloadClass string            `json:"workload_class,omitempty"` // service | worker | job | mixed | unknown
	Health        string            `json:"health"`                   // worst-of across workloads
	Versions      []string          `json:"versions,omitempty"`       // distinct image tags (the running version)
	VersionSkew   bool              `json:"versionSkew,omitempty"`    // the SAME image runs different tags across workloads — real drift, unlike multi-image diversity
	AppVersion    string            `json:"appVersion,omitempty"`     // app.kubernetes.io/version when all workloads agree — the "main version" of a single-chart add-on; empty for multi-chart umbrellas
	Identity      *appIdentity      `json:"identity,omitempty"`       // app identity grouping evidence — see applications_identity.go
	MatchKeys     []string          `json:"matchKeys,omitempty"`      // exact grouping-signal evidence keys, namespace-scoped ("instance:ns:x","helm:ns:x",…) + informational "name-stem:x" (unscoped); the client joins timeline events to this app by these, matching on the event's namespace
	SourceRef     *appSourceRef     `json:"sourceRef,omitempty"`      // exact source system object when known (GitOps / native Helm)
	Workloads     []appWorkload     `json:"workloads"`
	Events        []appEvent        `json:"events,omitempty"`        // recent Warning events across the app's workloads/pods
	Relationships *appRelationships `json:"relationships,omitempty"` // structural satellites attached via topology

	sourceConflict bool
	sourceStrict   bool
}

// appSourceRef is the source-of-truth object when the grouping signal names one
// exactly enough to navigate. Label/name-stem apps intentionally have no source
// ref: they are apps, but not tied to a GitOps or Helm object Radar can open.
type appSourceRef struct {
	Type      string `json:"type"` // gitops | helm
	Tool      string `json:"tool,omitempty"`
	Group     string `json:"group,omitempty"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type appHistoryResponse struct {
	AppKey         string               `json:"appKey"`
	SourceRef      *appSourceRef        `json:"sourceRef,omitempty"`
	Summary        *appHistorySummary   `json:"summary,omitempty"`
	Anchors        []appHistoryAnchor   `json:"anchors,omitempty"`
	Incidents      []appHistoryIncident `json:"incidents,omitempty"`
	PartialSources []string             `json:"partialSources,omitempty"`
}

type appHistorySummary struct {
	State     string `json:"state"` // change | incident | none
	Title     string `json:"title"`
	Detail    string `json:"detail,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
}

type appHistoryAnchor struct {
	Type        string `json:"type"` // gitops | helm
	Title       string `json:"title"`
	Status      string `json:"status,omitempty"`
	Revision    string `json:"revision,omitempty"`
	Message     string `json:"message,omitempty"`
	Source      string `json:"source,omitempty"`
	InitiatedBy string `json:"initiatedBy,omitempty"`
	Timestamp   string `json:"timestamp,omitempty"`
}

type appHistoryIncident struct {
	Severity  string `json:"severity"`
	Title     string `json:"title"`
	Object    string `json:"object"`
	Message   string `json:"message,omitempty"`
	Count     int    `json:"count,omitempty"`
	FirstSeen string `json:"firstSeen,omitempty"`
	LastSeen  string `json:"lastSeen,omitempty"`
}

// appRelationships is the structural neighborhood of an app, derived from the
// topology graph: what fronts it (Services/Ingress/Routes) and what supports it
// (config, autoscalers, disruption budgets). Counts where names add no value.
type appRelationships struct {
	Services  []string `json:"services,omitempty"`
	Ingresses []string `json:"ingresses,omitempty"`
	// "Kind/name" (routes are polymorphic); Services/Ingresses carry bare names
	// since their kind is fixed.
	Routes  []string `json:"routes,omitempty"`
	Configs int      `json:"configs,omitempty"`
	Scalers int      `json:"scalers,omitempty"`
	Storage int      `json:"storage,omitempty"`
	PDBs    int      `json:"pdbs,omitempty"`

	configRefs  map[string]struct{}
	scalerRefs  map[string]struct{}
	storageRefs map[string]struct{}
	pdbRefs     map[string]struct{}
}

// appEvent is a recent k8s Warning event correlated to an app's workloads/pods
// (the "why is it broken" feed — BackOff, FailedScheduling, FailedMount, …).
type appEvent struct {
	Type      string `json:"type"`
	Reason    string `json:"reason"`
	Message   string `json:"message,omitempty"`
	Count     int    `json:"count"`
	Object    string `json:"object"` // "<Kind>/<name>"
	FirstSeen string `json:"firstSeen,omitempty"`
	LastSeen  string `json:"lastSeen,omitempty"`
}

// appWorkload is one concrete workload belonging to an app, with its primary
// container image as the version anchor when the workload has a pod template.
type appWorkload struct {
	Kind          string           `json:"kind"`
	Group         string           `json:"group,omitempty"`
	Namespace     string           `json:"namespace"`
	Name          string           `json:"name"`
	WorkloadClass string           `json:"workload_class,omitempty"` // service | worker | job | unknown
	Image         string           `json:"image,omitempty"`          // full primary-container image ref
	Version       string           `json:"version,omitempty"`        // image tag (digest-only → empty)
	AppVersion    string           `json:"appVersion,omitempty"`     // app.kubernetes.io/version label (upstream release, e.g. v2.49.1)
	Health        string           `json:"health"`
	Ready         int              `json:"ready"`            // ready/available replicas
	Desired       int              `json:"desired"`          // desired replicas
	Restarts      int              `json:"restarts"`         // total container restarts across the workload's pods
	Reason        string           `json:"reason,omitempty"` // last-terminated reason of the worst pod (CrashLoopBackOff/OOMKilled/…)
	Batch         *appBatchSummary `json:"batch,omitempty"`

	// envLabel is the explicit environment label, when the workload carries
	// one (see envLabelOf) — app-identity resolver input, not on the wire.
	envLabel string
	// nameLabel is app.kubernetes.io/name — the explicit, cluster-agnostic app
	// identity the chart/author declared. The strongest identity signal we have:
	// app-identity resolver input, not on the wire.
	nameLabel string
	// appAnnotation is app.skyhook.io/app — the user's explicit cross-cluster app
	// declaration (authoritative, portable). Resolver input, not on the wire.
	appAnnotation string
}

type appBatchSummary struct {
	Schedule         string `json:"schedule,omitempty"`
	Suspended        bool   `json:"suspended,omitempty"`
	ActiveRuns       int    `json:"activeRuns,omitempty"`
	RetainedRuns     int    `json:"retainedRuns,omitempty"`
	FailedRuns       int    `json:"failedRuns,omitempty"`
	SucceededRuns    int    `json:"succeededRuns,omitempty"`
	LatestRunName    string `json:"latestRunName,omitempty"`
	LatestRunPhase   string `json:"latestRunPhase,omitempty"`
	LatestStartedAt  string `json:"latestStartedAt,omitempty"`
	LatestFinishedAt string `json:"latestFinishedAt,omitempty"`
	LastScheduledAt  string `json:"lastScheduledAt,omitempty"`
	LastSuccessfulAt string `json:"lastSuccessfulAt,omitempty"`
	Message          string `json:"message,omitempty"`

	latestRunActive      bool
	latestRunScheduledAt string
}

// handleListApplications serves GET /api/applications.
//
//	?namespaces=a,b,c | ?namespace=a — limit to workloads in the namespace set.
func (s *Server) handleListApplications(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespaces := s.parseNamespacesForUser(r)
	resp, err := listApplicationsForRequest(r.Context(), namespaces, s.canRead(r, "argoproj.io", "clusterworkflowtemplates", "", "list"))
	if err != nil {
		if errors.Is(err, errResourceCacheUnavailable) {
			s.writeError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		log.Printf("[applications] ListApplications failed: %v", err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, resp)
}

// handleApplicationHistory serves a focused app-level change narrative.
//
//	?app=<appRow.key>&namespaces=a,b,c
//
// The namespace filter is the same one used by /api/applications, so a row found
// in the Overview is the exact subject used here. Slash-heavy app keys stay in a
// query param instead of a path segment.
func (s *Server) handleApplicationHistory(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	key := strings.TrimSpace(r.URL.Query().Get("app"))
	if key == "" {
		s.writeError(w, http.StatusBadRequest, "app query parameter is required")
		return
	}
	namespaces := s.parseNamespacesForUser(r)
	resp, err := listApplicationsForRequest(r.Context(), namespaces, s.canRead(r, "argoproj.io", "clusterworkflowtemplates", "", "list"))
	if err != nil {
		if errors.Is(err, errResourceCacheUnavailable) {
			s.writeError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		log.Printf("[applications] ApplicationHistory list failed: %v", err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var app *appRow
	for i := range resp.Applications {
		if resp.Applications[i].Key == key {
			app = &resp.Applications[i]
			break
		}
	}
	if app == nil {
		s.writeError(w, http.StatusNotFound, "application not found")
		return
	}

	history := appHistoryResponse{
		AppKey:    app.Key,
		SourceRef: app.SourceRef,
		Incidents: historyIncidents(app.Events),
	}
	if app.SourceRef != nil {
		anchors, partial := s.historyAnchorsForSource(r, app.SourceRef)
		history.Anchors = anchors
		history.PartialSources = append(history.PartialSources, partial...)
	}
	history.Summary = historySummary(history.Anchors, history.Incidents)
	s.writeJSON(w, history)
}

func (s *Server) historyAnchorsForSource(r *http.Request, source *appSourceRef) ([]appHistoryAnchor, []string) {
	if source == nil {
		return nil, nil
	}
	switch source.Type {
	case "gitops":
		return s.gitOpsHistoryAnchors(r, source)
	case "helm":
		return s.helmHistoryAnchors(r, source)
	default:
		return nil, nil
	}
}

func (s *Server) gitOpsHistoryAnchors(r *http.Request, source *appSourceRef) ([]appHistoryAnchor, []string) {
	if source.Namespace != "" {
		allowed := s.getUserNamespaces(r, []string{source.Namespace})
		if noNamespaceAccess(allowed) {
			return nil, []string{fmt.Sprintf("No access to source namespace %q.", source.Namespace)}
		}
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil, []string{"Resource cache is not available."}
	}
	req := &gitopsRequest{
		Kind:              gitOpsPluralKind(source.Kind),
		Namespace:         source.Namespace,
		Name:              source.Name,
		Group:             source.Group,
		Cache:             cache,
		AllowedNamespaces: s.getUserNamespaces(r, nil),
	}
	if req.Kind == "" {
		return nil, []string{"Unsupported GitOps source kind."}
	}
	if !req.HasNamespaceAccess() {
		return nil, []string{"No namespace access for GitOps history."}
	}
	tree, root, err := s.buildGitOpsTree(r.Context(), req)
	if err != nil {
		return nil, []string{fmt.Sprintf("GitOps history unavailable: %v", err)}
	}
	tree = s.filterGitOpsTreeForUser(r, req, tree)
	canAccess := func(group, kind, namespace, name string) bool {
		return s.canAccessGitOpsRef(r, req, group, kind, namespace, name, false)
	}
	resolver := newInsightsResolver(r.Context(), req.Cache, req.AllowedNamespaces, canAccess)
	insight := gitopsinsights.Build(root, tree, resolver)
	anchors := make([]appHistoryAnchor, 0, len(insight.History))
	for _, item := range insight.History {
		title := "GitOps reconcile"
		if source.Tool == "argocd" {
			title = "Argo CD sync"
		} else if source.Kind == "HelmRelease" {
			title = "Flux Helm reconcile"
		} else if source.Kind == "Kustomization" {
			title = "Flux Kustomization reconcile"
		}
		status := item.Phase
		if status == "" && item.Message != "" {
			status = "Recorded"
		}
		anchors = append(anchors, appHistoryAnchor{
			Type:        "gitops",
			Title:       title,
			Status:      status,
			Revision:    item.Revision,
			Message:     firstNonEmptyString(item.Message, item.RawMessage),
			Source:      item.Source,
			InitiatedBy: item.InitiatedBy,
			Timestamp:   item.DeployedAt,
		})
	}
	sort.SliceStable(anchors, func(i, j int) bool { return anchors[i].Timestamp > anchors[j].Timestamp })
	return anchors, nil
}

func (s *Server) helmHistoryAnchors(r *http.Request, source *appSourceRef) ([]appHistoryAnchor, []string) {
	client := helm.GetClient()
	if client == nil {
		return nil, []string{"Helm client is not initialized."}
	}
	username, groups := "", []string(nil)
	if user := auth.UserFromContext(r.Context()); user != nil {
		username, groups = user.Username, user.Groups
	}
	release, err := client.GetReleaseAsUser(source.Namespace, source.Name, username, groups)
	if err != nil {
		if helm.IsForbiddenError(err) {
			return nil, []string{"Insufficient permissions to read Helm release history."}
		}
		return nil, []string{fmt.Sprintf("Helm history unavailable: %v", err)}
	}
	anchors := make([]appHistoryAnchor, 0, len(release.History))
	for _, rev := range release.History {
		anchors = append(anchors, appHistoryAnchor{
			Type:      "helm",
			Title:     fmt.Sprintf("Helm revision %d", rev.Revision),
			Status:    rev.Status,
			Revision:  fmt.Sprintf("%d", rev.Revision),
			Message:   rev.Description,
			Source:    joinNonEmpty(rev.Chart, rev.AppVersion),
			Timestamp: rev.Updated.UTC().Format(time.RFC3339),
		})
	}
	sort.SliceStable(anchors, func(i, j int) bool { return anchors[i].Timestamp > anchors[j].Timestamp })
	return anchors, nil
}

func historyIncidents(events []appEvent) []appHistoryIncident {
	out := make([]appHistoryIncident, 0, len(events))
	for _, event := range events {
		title := event.Reason
		if event.Object != "" {
			title = event.Reason + " on " + event.Object
		}
		out = append(out, appHistoryIncident{
			Severity:  "warning",
			Title:     title,
			Object:    event.Object,
			Message:   event.Message,
			Count:     event.Count,
			FirstSeen: event.FirstSeen,
			LastSeen:  event.LastSeen,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastSeen > out[j].LastSeen })
	return out
}

func historySummary(anchors []appHistoryAnchor, incidents []appHistoryIncident) *appHistorySummary {
	if len(incidents) > 0 {
		incident := incidents[0]
		return &appHistorySummary{
			State:     "incident",
			Title:     "Current incident: " + incident.Title,
			Detail:    incident.Message,
			Timestamp: incident.LastSeen,
		}
	}
	if len(anchors) > 0 {
		anchor := anchors[0]
		detail := joinNonEmpty(anchor.Status, anchor.Revision, anchor.Message)
		return &appHistorySummary{
			State:     "change",
			Title:     anchor.Title,
			Detail:    detail,
			Timestamp: anchor.Timestamp,
		}
	}
	return &appHistorySummary{State: "none", Title: "No retained deployment history"}
}

func gitOpsPluralKind(kind string) string {
	switch kind {
	case "Application":
		return "applications"
	case "ApplicationSet":
		return "applicationsets"
	case "AppProject":
		return "appprojects"
	case "Kustomization":
		return "kustomizations"
	case "HelmRelease":
		return "helmreleases"
	default:
		return ""
	}
}

func joinNonEmpty(values ...string) string {
	parts := make([]string, 0, len(values))
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, " · ")
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// appGraph bundles the topology graph and the primitives derived from it that
// the collection pass needs. A nil graph (build failure / no cache) degrades
// cleanly: every workload becomes its own structural root and carries no
// satellites — identity then rests on the label overlay alone, raw-always.
type appGraph struct {
	topo     *topology.Topology
	idx      *topology.RelationshipsIndex
	provider topology.ResourceProvider
	dp       topology.DynamicProvider
	byID     map[string]topology.Node
	byKNN    map[string]string // lower(kind)|ns|name → node ID
}

// ListApplications builds the structural topology graph, resolves each app
// workload to its graph root + label overlay, and groups them into logical
// apps. Add-on machinery is classified (not dropped); nothing is hidden.
func ListApplications(ctx context.Context, namespaces []string) (*applicationsResponse, error) {
	return listApplicationsForRequest(ctx, namespaces, true)
}

func listApplicationsForRequest(ctx context.Context, namespaces []string, canListClusterWorkflowTemplates bool) (*applicationsResponse, error) {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil, errResourceCacheUnavailable
	}
	cacheKey := applicationsCacheKeyFor(namespaces, canListClusterWorkflowTemplates)
	applicationsCacheMu.Lock()
	entry, hit := applicationsCache[cacheKey]
	applicationsCacheMu.Unlock()
	if hit && time.Since(entry.at) < applicationsCacheTTL {
		return &applicationsResponse{Applications: entry.rows, ArgoClaims: entry.claims}, nil
	}

	g := buildAppGraph(cache, namespaces)
	wls := collectAppWorkloads(ctx, cache, namespaces, g, canListClusterWorkflowTemplates)
	rows := groupApplications(wls)
	sourcePaths, appSetChildren, argoItems := argoApplicationFacts(ctx, cache)
	appSetByKey := appSetFanouts(appSetChildren)
	enrichRowsWithManagedSourceRefs(ctx, cache, rows, argoItems)
	resolveAppIdentities(rows, sourcePaths, appSetByKey, namespaceEnvLabels(cache), fluxKustomizationFacts(ctx, cache))
	claims := collectArgoClaims(argoItems, sourcePaths, appSetByKey, namespaces)
	applicationsCacheMu.Lock()
	if len(applicationsCache) >= applicationsCacheMaxEntries {
		evictOldestApplicationsCacheEntry()
	}
	applicationsCache[cacheKey] = applicationsCacheEntry{at: time.Now(), rows: rows, claims: claims}
	applicationsCacheMu.Unlock()
	return &applicationsResponse{Applications: rows, ArgoClaims: claims}, nil
}

func evictOldestApplicationsCacheEntry() {
	var oldestKey string
	var oldestAt time.Time
	first := true
	for k, e := range applicationsCache {
		if first || e.at.Before(oldestAt) {
			oldestKey = k
			oldestAt = e.at
			first = false
		}
	}
	if !first {
		delete(applicationsCache, oldestKey)
	}
}

func clearApplicationsCache() {
	applicationsCacheMu.Lock()
	applicationsCache = map[string]applicationsCacheEntry{}
	applicationsCacheMu.Unlock()
}

func applicationsCacheKeyFor(namespaces []string, canListClusterWorkflowTemplates bool) string {
	permissionMode := "cwt-denied:"
	if canListClusterWorkflowTemplates {
		permissionMode = "cwt-visible:"
	}
	if namespaces == nil {
		return permissionMode + "*"
	}
	ns := append([]string(nil), namespaces...)
	sort.Strings(ns)
	return permissionMode + strings.Join(ns, ",")
}

// buildAppGraph constructs the same resources-view topology the /api/topology
// handler builds, then indexes it for root walks and satellite lookups.
func buildAppGraph(cache *k8s.ResourceCache, namespaces []string) *appGraph {
	g := &appGraph{byID: map[string]topology.Node{}, byKNN: map[string]string{}}
	provider := k8s.NewTopologyResourceProvider(cache)
	if provider == nil {
		return g
	}
	g.provider = provider
	g.dp = k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery())

	opts := topology.DefaultBuildOptions()
	opts.Namespaces = namespaces
	b := topology.NewBuilder(provider)
	if g.dp != nil {
		b = b.WithDynamic(g.dp)
	}
	topo, err := b.Build(opts)
	if err != nil || topo == nil {
		return g
	}
	g.topo = topo
	g.idx = topology.IndexByResource(topo)
	for _, n := range topo.Nodes {
		g.byID[n.ID] = n
		ns, _ := n.Data["namespace"].(string)
		g.byKNN[knnKey(string(n.Kind), ns, n.Name)] = n.ID
	}
	return g
}

func knnKey(kind, ns, name string) string {
	return strings.ToLower(kind) + "|" + ns + "|" + name
}

// isGitOpsManagerKind reports whether a node is an in-cluster GitOps manager —
// the boundary structuralRoot stops climbing AT. Above a manager lies either a
// source ref (GitRepository → Kustomization is an EdgeManages edge too) or a
// parent manager (app-of-apps); climbing THROUGH one would resolve every
// installation sharing that source/parent to the same structural root and
// union-find would merge them all into one app. ownerRef chains — including
// operator CRs (CNPG Cluster, Strimzi Kafka) — are not managers and keep
// climbing to the topmost owner.
func isGitOpsManagerKind(k topology.NodeKind) bool {
	switch k {
	case topology.KindApplication, topology.KindKustomization, topology.KindHelmRelease:
		return true
	default:
		return false
	}
}

// structuralRoot walks incoming EdgeManages edges from startID toward the
// app's structural root: the lowest in-cluster GitOps manager (ArgoCD
// Application, Flux Kustomization/HelmRelease) when one manages the workload,
// otherwise the workload's topmost ownerRef ancestor (incl. operator CRs). It
// stops AT the first manager — it does not climb through to the manager's
// source ref or parent manager.
func (g *appGraph) structuralRoot(startID string) (topology.Node, bool) {
	cur := startID
	top, ok := g.byID[cur]
	if g.idx == nil {
		return top, ok
	}
	visited := map[string]bool{cur: true}
	for {
		next := ""
		incoming, _ := g.idx.EdgesFor(cur)
		for _, e := range incoming {
			if e.Type == topology.EdgeManages {
				next = e.Source
				break
			}
		}
		if next == "" || visited[next] {
			break
		}
		visited[next] = true
		n, exists := g.byID[next]
		if exists {
			top = n
			ok = true
		}
		cur = next
		if exists && isGitOpsManagerKind(n.Kind) {
			break
		}
	}
	return top, ok
}

// rootOf returns the structural-root key ("<ns>/<Kind>/<name>") and root Kind
// for a workload, falling back to the workload itself when the graph is absent.
func (g *appGraph) rootOf(kind, ns, name string) (rootKey, rootKind string) {
	rootKey = ns + "/" + kind + "/" + name
	rootKind = kind
	if g.topo == nil {
		return
	}
	nodeID, found := g.byKNN[knnKey(kind, ns, name)]
	if !found {
		return
	}
	rn, ok := g.structuralRoot(nodeID)
	if !ok {
		return
	}
	rns, _ := rn.Data["namespace"].(string)
	return rns + "/" + string(rn.Kind) + "/" + rn.Name, string(rn.Kind)
}

// relationshipsFor pulls the workload's structural satellites from the graph.
func (g *appGraph) relationshipsFor(kind, ns, name string) *appRelationships {
	if g.topo == nil {
		return nil
	}
	rel := topology.GetRelationshipsWithIndex(kind, ns, name, g.topo, g.provider, g.dp, g.idx)
	if rel == nil {
		return nil
	}
	out := &appRelationships{Configs: len(rel.ConfigRefs), Scalers: len(rel.Scalers), Storage: len(rel.StorageRefs), PDBs: len(rel.PDBs)}
	out.configRefs = refsSet(rel.ConfigRefs)
	out.scalerRefs = refsSet(rel.Scalers)
	out.storageRefs = refsSet(rel.StorageRefs)
	out.pdbRefs = refsSet(rel.PDBs)
	for _, s := range rel.Services {
		out.Services = append(out.Services, s.Name)
	}
	for _, i := range rel.Ingresses {
		out.Ingresses = append(out.Ingresses, i.Name)
	}
	for _, r := range rel.Routes {
		// Routes are polymorphic (HTTPRoute/GRPCRoute/TCPRoute/TLSRoute), so ship
		// "Kind/name": the client keys its membership index on the concrete kind
		// (matching the lane ids), which a bare name can't reconstruct.
		out.Routes = append(out.Routes, r.Kind+"/"+r.Name)
	}
	if len(out.Services) == 0 && len(out.Ingresses) == 0 && len(out.Routes) == 0 &&
		out.Configs == 0 && out.Scalers == 0 && out.Storage == 0 && out.PDBs == 0 {
		return nil
	}
	return out
}

// appWorkloadInput is the pre-grouping shape: one workload plus the signals
// that decide which app it belongs to (structural root + label overlay) and how
// it is classified.
type appWorkloadInput struct {
	wl       appWorkload
	overlay  *subject.AppOverlay
	source   *appSourceRef
	events   []appEvent
	rels     *appRelationships
	rootKey  string
	rootKind string
	addon    bool
	addonWhy string
}

// collectAppWorkloads walks Deployments/StatefulSets/DaemonSets plus
// Jobs/CronJobs, captures the primary container image + runtime health, resolves
// each to its structural root and label overlay, and classifies add-on
// machinery. Pods and Warning events are indexed once per namespace and joined,
// not re-listed per workload.
func collectAppWorkloads(ctx context.Context, cache *k8s.ResourceCache, namespaces []string, g *appGraph, canListClusterWorkflowTemplates bool) []appWorkloadInput {
	var out []appWorkloadInput

	podsByNS := indexPodsByNamespace(cache, namespaces)
	eventsByObj := indexWarningEventsByObject(cache, namespaces)
	cronJobBatches := cronJobBatchSummaries(cache, namespaces)
	scaledJobBatches := scaledJobBatchSummaries(cache, namespaces)
	cronWorkflowBatches := cronWorkflowBatchSummaries(ctx, cache, namespaces)

	add := func(kind, ns, name string, lbls, anns map[string]string, image string, health packages.Health, ready, desired int, selector *metav1.LabelSelector, batch *appBatchSummary) {
		pods := podsForSelector(podsByNS[ns], selector)
		restarts, reason := podsRestarts(pods)
		meta := metav1.ObjectMeta{Namespace: ns, Name: name, Labels: lbls, Annotations: anns}
		overlay := subject.ResolveOverlay(&meta, false)
		rootKey, rootKind := g.rootOf(kind, ns, name)
		rels := g.relationshipsFor(kind, ns, name)
		addon, why := packages.ClassifyAddon(lbls["helm.sh/chart"], lbls["app.kubernetes.io/name"], lbls["app.kubernetes.io/part-of"], name, lbls["addonmanager.kubernetes.io/mode"], image)
		out = append(out, appWorkloadInput{
			wl: appWorkload{
				Kind:          kind,
				Group:         appWorkloadAPIGroup(kind),
				Namespace:     ns,
				Name:          name,
				WorkloadClass: classifyWorkload(kind, rels),
				Image:         image,
				Version:       imageTag(image),
				AppVersion:    lbls["app.kubernetes.io/version"],
				Health:        string(health),
				Ready:         ready,
				Desired:       desired,
				Restarts:      restarts,
				Reason:        reason,
				Batch:         batch,
				envLabel:      envLabelOf(lbls),
				nameLabel:     lbls["app.kubernetes.io/name"],
				appAnnotation: strings.TrimSpace(anns[appIdentityAnnotation]),
			},
			overlay:  overlay,
			source:   sourceRefForInput(overlay, rootKind, rootKey),
			events:   eventsForWorkload(eventsByObj[ns], kind, name, pods),
			rels:     rels,
			rootKey:  rootKey,
			rootKind: rootKind,
			addon:    addon,
			addonWhy: why,
		})
	}

	forEachNamespace := func(fn func(ns string)) {
		if namespaces == nil {
			fn("")
			return
		}
		for _, ns := range namespaces {
			fn(ns)
		}
	}

	if depLister := cache.Deployments(); depLister != nil {
		forEachNamespace(func(ns string) {
			var items []*appsv1.Deployment
			if ns == "" {
				items, _ = depLister.List(labels.Everything())
			} else {
				items, _ = depLister.Deployments(ns).List(labels.Everything())
			}
			for _, d := range items {
				add("Deployment", d.Namespace, d.Name, d.Labels, d.Annotations,
					primaryImage(d.Spec.Template.Spec.Containers),
					levelToPackagesHealth(health.Workload(d, time.Now()).Level),
					int(d.Status.AvailableReplicas), int(d.Status.Replicas), d.Spec.Selector, nil)
			}
		})
	}
	if dsLister := cache.DaemonSets(); dsLister != nil {
		forEachNamespace(func(ns string) {
			var items []*appsv1.DaemonSet
			if ns == "" {
				items, _ = dsLister.List(labels.Everything())
			} else {
				items, _ = dsLister.DaemonSets(ns).List(labels.Everything())
			}
			for _, d := range items {
				add("DaemonSet", d.Namespace, d.Name, d.Labels, d.Annotations,
					primaryImage(d.Spec.Template.Spec.Containers),
					levelToPackagesHealth(health.Workload(d, time.Now()).Level),
					int(d.Status.NumberReady), int(d.Status.DesiredNumberScheduled), d.Spec.Selector, nil)
			}
		})
	}
	if ssLister := cache.StatefulSets(); ssLister != nil {
		forEachNamespace(func(ns string) {
			var items []*appsv1.StatefulSet
			if ns == "" {
				items, _ = ssLister.List(labels.Everything())
			} else {
				items, _ = ssLister.StatefulSets(ns).List(labels.Everything())
			}
			for _, d := range items {
				add("StatefulSet", d.Namespace, d.Name, d.Labels, d.Annotations,
					primaryImage(d.Spec.Template.Spec.Containers),
					levelToPackagesHealth(health.Workload(d, time.Now()).Level),
					int(d.Status.ReadyReplicas), int(d.Status.Replicas), d.Spec.Selector, nil)
			}
		})
	}
	if jobLister := cache.Jobs(); jobLister != nil {
		forEachNamespace(func(ns string) {
			var items []*batchv1.Job
			if ns == "" {
				items, _ = jobLister.List(labels.Everything())
			} else {
				items, _ = jobLister.Jobs(ns).List(labels.Everything())
			}
			for _, j := range items {
				if controllerOwnerName(j.OwnerReferences, "CronJob") != "" {
					continue
				}
				if controllerOwnerName(j.OwnerReferences, "ScaledJob") != "" {
					continue
				}
				batch := jobBatchSummary(j)
				add("Job", j.Namespace, j.Name, j.Labels, j.Annotations,
					primaryImage(j.Spec.Template.Spec.Containers),
					batchHealth(batch, levelToPackagesHealth(health.Workload(j, time.Now()).Level)),
					0, 0, j.Spec.Selector, batch)
			}
		})
	}
	if cjLister := cache.CronJobs(); cjLister != nil {
		forEachNamespace(func(ns string) {
			var items []*batchv1.CronJob
			if ns == "" {
				items, _ = cjLister.List(labels.Everything())
			} else {
				items, _ = cjLister.CronJobs(ns).List(labels.Everything())
			}
			for _, cj := range items {
				batch := cronJobBatches[cj.Namespace+"/"+cj.Name]
				if batch == nil {
					batch = &appBatchSummary{}
				}
				batch.Schedule = cj.Spec.Schedule
				batch.Suspended = cj.Spec.Suspend != nil && *cj.Spec.Suspend
				setLatestBatchTime(&batch.LastScheduledAt, formatMetaTime(cj.Status.LastScheduleTime))
				setLatestBatchTime(&batch.LastSuccessfulAt, formatMetaTime(cj.Status.LastSuccessfulTime))
				add("CronJob", cj.Namespace, cj.Name, cj.Labels, cj.Annotations,
					primaryImage(cj.Spec.JobTemplate.Spec.Template.Spec.Containers),
					batchHealth(batch, levelToPackagesHealth(health.Workload(cj, time.Now()).Level)),
					0, 0, nil, batch)
			}
		})
	}
	addScaledJobWorkloads(ctx, cache, namespaces, add, scaledJobBatches)
	addArgoBatchWorkloads(ctx, cache, namespaces, add, cronWorkflowBatches, canListClusterWorkflowTemplates)
	return out
}

type addAppWorkloadFunc func(kind, ns, name string, lbls, anns map[string]string, image string, health packages.Health, ready, desired int, selector *metav1.LabelSelector, batch *appBatchSummary)

func jobBatchSummary(job *batchv1.Job) *appBatchSummary {
	b := &appBatchSummary{}
	applyRunToBatch(b, jobRunInfo(job))
	return b
}

func cronJobBatchSummaries(cache *k8s.ResourceCache, namespaces []string) map[string]*appBatchSummary {
	out := map[string]*appBatchSummary{}
	jobLister := cache.Jobs()
	if jobLister == nil {
		return out
	}
	forEachWorkloadNamespace(namespaces, func(ns string) {
		var jobs []*batchv1.Job
		if ns == "" {
			jobs, _ = jobLister.List(labels.Everything())
		} else {
			jobs, _ = jobLister.Jobs(ns).List(labels.Everything())
		}
		for _, job := range jobs {
			owner := controllerOwnerName(job.OwnerReferences, "CronJob")
			if owner == "" {
				continue
			}
			key := job.Namespace + "/" + owner
			if out[key] == nil {
				out[key] = &appBatchSummary{}
			}
			applyRunToBatch(out[key], jobRunInfo(job))
		}
	})
	return out
}

func scaledJobBatchSummaries(cache *k8s.ResourceCache, namespaces []string) map[string]*appBatchSummary {
	out := map[string]*appBatchSummary{}
	jobLister := cache.Jobs()
	if jobLister == nil {
		return out
	}
	forEachWorkloadNamespace(namespaces, func(ns string) {
		var jobs []*batchv1.Job
		if ns == "" {
			jobs, _ = jobLister.List(labels.Everything())
		} else {
			jobs, _ = jobLister.Jobs(ns).List(labels.Everything())
		}
		for _, job := range jobs {
			owner := controllerOwnerName(job.OwnerReferences, "ScaledJob")
			if owner == "" {
				continue
			}
			key := job.Namespace + "/" + owner
			if out[key] == nil {
				out[key] = &appBatchSummary{}
			}
			applyRunToBatch(out[key], jobRunInfo(job))
		}
	})
	return out
}

func cronWorkflowBatchSummaries(ctx context.Context, cache *k8s.ResourceCache, namespaces []string) map[string]*appBatchSummary {
	out := map[string]*appBatchSummary{}
	workflows := listArgoWorkflows(ctx, cache, namespaces)
	for _, wf := range workflows {
		owner := cronWorkflowOwnerName(wf)
		if owner == "" {
			continue
		}
		key := wf.GetNamespace() + "/" + owner
		if out[key] == nil {
			out[key] = &appBatchSummary{}
		}
		applyRunToBatch(out[key], workflowRunInfo(wf))
	}
	return out
}

func addScaledJobWorkloads(ctx context.Context, cache *k8s.ResourceCache, namespaces []string, add addAppWorkloadFunc, batches map[string]*appBatchSummary) {
	for _, sj := range listDynamicByNamespacesGroup(ctx, cache, namespaces, "ScaledJob", "keda.sh") {
		batch := batches[sj.GetNamespace()+"/"+sj.GetName()]
		if batch == nil {
			batch = &appBatchSummary{}
		}
		add("ScaledJob", sj.GetNamespace(), sj.GetName(), sj.GetLabels(), sj.GetAnnotations(),
			scaledJobPrimaryImage(sj), batchHealth(batch, scaledJobHealth(sj)), 0, 0, nil, batch)
	}
}

func addArgoBatchWorkloads(ctx context.Context, cache *k8s.ResourceCache, namespaces []string, add addAppWorkloadFunc, cronWorkflowBatches map[string]*appBatchSummary, canListClusterWorkflowTemplates bool) {
	workflows := listArgoWorkflows(ctx, cache, namespaces)
	cronWorkflows := listArgoCronWorkflows(ctx, cache, namespaces)
	cronWorkflowKeys := map[string]bool{}
	for _, cwf := range cronWorkflows {
		cronWorkflowKeys[cwf.GetNamespace()+"/"+cwf.GetName()] = true
	}
	templateInfos := argoWorkflowTemplateInfos(ctx, cache, namespaces, workflows, canListClusterWorkflowTemplates)
	templateBatches := workflowTemplateBatchSummaries(workflows, cronWorkflowKeys)

	for _, wf := range workflows {
		if owner := cronWorkflowOwnerName(wf); owner != "" && cronWorkflowKeys[wf.GetNamespace()+"/"+owner] {
			continue
		}
		if ref, ok := argoWorkflowTemplateRef(wf); ok {
			if _, exists := templateInfos[ref.key()]; exists {
				continue
			}
		}
		run := workflowRunInfo(wf)
		batch := &appBatchSummary{}
		applyRunToBatch(batch, run)
		add("Workflow", wf.GetNamespace(), wf.GetName(), wf.GetLabels(), wf.GetAnnotations(),
			workflowPrimaryImage(wf), workflowHealth(run.Phase), 0, 0, nil, batch)
	}

	templateKeys := make([]string, 0, len(templateInfos))
	for key := range templateInfos {
		templateKeys = append(templateKeys, key)
	}
	sort.Strings(templateKeys)
	for _, key := range templateKeys {
		info := templateInfos[key]
		batch := templateBatches[key]
		if batch == nil {
			batch = &appBatchSummary{}
		}
		add(info.kind, info.namespace, info.name, info.labels, info.annotations,
			templateImage(info.object, "spec", "templates"), batchHealth(batch, packages.HealthNeutral), 0, 0, nil, batch)
	}

	for _, cwf := range cronWorkflows {
		batch := cronWorkflowBatches[cwf.GetNamespace()+"/"+cwf.GetName()]
		if batch == nil {
			batch = &appBatchSummary{}
		}
		batch.Schedule = cronWorkflowSchedule(cwf)
		suspended, _, _ := unstructured.NestedBool(cwf.Object, "spec", "suspend")
		batch.Suspended = suspended
		lastScheduled, _, _ := unstructured.NestedString(cwf.Object, "status", "lastScheduledTime")
		setLatestBatchTime(&batch.LastScheduledAt, lastScheduled)
		add("CronWorkflow", cwf.GetNamespace(), cwf.GetName(), cwf.GetLabels(), cwf.GetAnnotations(),
			cronWorkflowPrimaryImage(cwf), batchHealth(batch, packages.HealthNeutral), 0, 0, nil, batch)
	}
}

func listArgoWorkflows(ctx context.Context, cache *k8s.ResourceCache, namespaces []string) []*unstructured.Unstructured {
	return listDynamicByNamespaces(ctx, cache, namespaces, "Workflow")
}

func listArgoCronWorkflows(ctx context.Context, cache *k8s.ResourceCache, namespaces []string) []*unstructured.Unstructured {
	return listDynamicByNamespaces(ctx, cache, namespaces, "CronWorkflow")
}

type argoTemplateRef struct {
	kind      string
	namespace string
	name      string
}

func (r argoTemplateRef) key() string {
	return r.kind + "/" + r.namespace + "/" + r.name
}

type argoTemplateInfo struct {
	argoTemplateRef
	labels      map[string]string
	annotations map[string]string
	object      map[string]any
}

func argoWorkflowTemplateInfos(ctx context.Context, cache *k8s.ResourceCache, namespaces []string, workflows []*unstructured.Unstructured, canListClusterWorkflowTemplates bool) map[string]argoTemplateInfo {
	out := map[string]argoTemplateInfo{}
	for _, wt := range listDynamicByNamespaces(ctx, cache, namespaces, "WorkflowTemplate") {
		ref := argoTemplateRef{kind: "WorkflowTemplate", namespace: wt.GetNamespace(), name: wt.GetName()}
		out[ref.key()] = argoTemplateInfo{
			argoTemplateRef: ref,
			labels:          wt.GetLabels(),
			annotations:     wt.GetAnnotations(),
			object:          wt.Object,
		}
	}
	if !canListClusterWorkflowTemplates {
		return out
	}
	for _, cwt := range listDynamicClusterScoped(ctx, cache, "ClusterWorkflowTemplate", "argoproj.io") {
		if !shouldIncludeClusterWorkflowTemplate(namespaces, workflows, cwt.GetName(), canListClusterWorkflowTemplates) {
			continue
		}
		ref := argoTemplateRef{kind: "ClusterWorkflowTemplate", name: cwt.GetName()}
		out[ref.key()] = argoTemplateInfo{
			argoTemplateRef: ref,
			labels:          cwt.GetLabels(),
			annotations:     cwt.GetAnnotations(),
			object:          cwt.Object,
		}
	}
	return out
}

func shouldIncludeClusterWorkflowTemplate(namespaces []string, workflows []*unstructured.Unstructured, name string, canList bool) bool {
	if !canList {
		return false
	}
	return namespaces == nil || clusterWorkflowTemplateReferenced(workflows, name)
}

func clusterWorkflowTemplateReferenced(workflows []*unstructured.Unstructured, name string) bool {
	for _, workflow := range workflows {
		ref, ok := argoWorkflowTemplateRef(workflow)
		if ok && ref.kind == "ClusterWorkflowTemplate" && ref.name == name {
			return true
		}
	}
	return false
}

func workflowTemplateBatchSummaries(workflows []*unstructured.Unstructured, cronWorkflowKeys map[string]bool) map[string]*appBatchSummary {
	out := map[string]*appBatchSummary{}
	for _, wf := range workflows {
		if owner := cronWorkflowOwnerName(wf); owner != "" && cronWorkflowKeys[wf.GetNamespace()+"/"+owner] {
			continue
		}
		ref, ok := argoWorkflowTemplateRef(wf)
		if !ok {
			continue
		}
		key := ref.key()
		if out[key] == nil {
			out[key] = &appBatchSummary{}
		}
		applyRunToBatch(out[key], workflowRunInfo(wf))
	}
	return out
}

func argoWorkflowTemplateRef(wf *unstructured.Unstructured) (argoTemplateRef, bool) {
	name, _, _ := unstructured.NestedString(wf.Object, "spec", "workflowTemplateRef", "name")
	clusterScope, _, _ := unstructured.NestedBool(wf.Object, "spec", "workflowTemplateRef", "clusterScope")
	if name == "" {
		name = wf.GetLabels()["workflows.argoproj.io/workflow-template"]
	}
	if name == "" {
		return argoTemplateRef{}, false
	}
	if clusterScope {
		return argoTemplateRef{kind: "ClusterWorkflowTemplate", name: name}, true
	}
	return argoTemplateRef{kind: "WorkflowTemplate", namespace: wf.GetNamespace(), name: name}, true
}

func listDynamicByNamespaces(ctx context.Context, cache *k8s.ResourceCache, namespaces []string, kind string) []*unstructured.Unstructured {
	return listDynamicByNamespacesGroup(ctx, cache, namespaces, kind, "argoproj.io")
}

func listDynamicByNamespacesGroup(ctx context.Context, cache *k8s.ResourceCache, namespaces []string, kind, group string) []*unstructured.Unstructured {
	var out []*unstructured.Unstructured
	forEachWorkloadNamespace(namespaces, func(ns string) {
		items, err := cache.ListDynamicWithGroup(ctx, kind, ns, group)
		if err != nil {
			return
		}
		out = append(out, items...)
	})
	return out
}

func listDynamicClusterScoped(ctx context.Context, cache *k8s.ResourceCache, kind, group string) []*unstructured.Unstructured {
	items, err := cache.ListDynamicWithGroup(ctx, kind, "", group)
	if err != nil {
		return nil
	}
	return items
}

func applyRunToBatch(b *appBatchSummary, run WorkloadRun) {
	b.RetainedRuns++
	if run.Active {
		b.ActiveRuns++
	}
	switch run.Phase {
	case "Succeeded":
		b.SucceededRuns++
		setLatestBatchTime(&b.LastSuccessfulAt, firstNonEmptyString(run.FinishedAt, run.StartedAt, run.ScheduledAt))
	case "Failed", "Error":
		b.FailedRuns++
	}
	setLatestBatchTime(&b.LastScheduledAt, run.ScheduledAt)
	if b.LatestRunName == "" || runIsNewer(run, b.latestRun()) {
		b.LatestRunName = run.Name
		b.LatestRunPhase = run.Phase
		b.LatestStartedAt = run.StartedAt
		b.LatestFinishedAt = run.FinishedAt
		b.latestRunActive = run.Active
		b.latestRunScheduledAt = run.ScheduledAt
		b.Message = run.Message
	}
}

func runIsNewer(a, b WorkloadRun) bool {
	aTime := runSortTime(a)
	bTime := runSortTime(b)
	if !aTime.Equal(bTime) {
		return aTime.After(bTime)
	}
	return runComesBefore(a, b)
}

func (b *appBatchSummary) latestRun() WorkloadRun {
	return WorkloadRun{
		Name:        b.LatestRunName,
		Phase:       b.LatestRunPhase,
		Active:      b.latestRunActive,
		StartedAt:   b.LatestStartedAt,
		FinishedAt:  b.LatestFinishedAt,
		ScheduledAt: b.latestRunScheduledAt,
	}
}

func setLatestBatchTime(target *string, value string) {
	if value == "" {
		return
	}
	if *target == "" || parseBatchTime(value).After(parseBatchTime(*target)) {
		*target = value
	}
}

func parseBatchTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t
	}
	return time.Time{}
}

func batchHealth(batch *appBatchSummary, fallback packages.Health) packages.Health {
	if batch == nil {
		return fallback
	}
	if fallback == packages.HealthUnhealthy {
		return fallback
	}
	if batch.ActiveRuns > 0 {
		if fallback == packages.HealthDegraded {
			return fallback
		}
		return packages.HealthNeutral
	}
	if batch.LatestRunPhase == "Failed" || batch.LatestRunPhase == "Error" {
		return packages.HealthUnhealthy
	}
	if fallback == packages.HealthDegraded {
		return fallback
	}
	if batch.Suspended {
		return packages.HealthNeutral
	}
	if batch.LatestRunPhase == "Succeeded" {
		return packages.HealthHealthy
	}
	return fallback
}

func workflowHealth(phase string) packages.Health {
	switch phase {
	case "Succeeded":
		return packages.HealthHealthy
	case "Running":
		return packages.HealthNeutral
	case "Failed", "Error":
		return packages.HealthUnhealthy
	case "Pending":
		return packages.HealthDegraded
	default:
		return packages.HealthUnknown
	}
}

func scaledJobPrimaryImage(sj *unstructured.Unstructured) string {
	containers, found, _ := unstructured.NestedSlice(sj.Object, "spec", "jobTargetRef", "template", "spec", "containers")
	if !found || len(containers) == 0 {
		return ""
	}
	first, ok := containers[0].(map[string]any)
	if !ok {
		return ""
	}
	image, _ := first["image"].(string)
	return image
}

func scaledJobHealth(sj *unstructured.Unstructured) packages.Health {
	conditions, _, _ := unstructured.NestedSlice(sj.Object, "status", "conditions")
	var activeCond, readyCond, pausedCond map[string]any
	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch condition["type"] {
		case "Active":
			activeCond = condition
		case "Paused":
			pausedCond = condition
		case "Ready":
			readyCond = condition
		}
	}
	if pausedCond != nil && pausedCond["status"] == "True" {
		return packages.HealthNeutral
	}
	if readyCond != nil {
		switch readyCond["status"] {
		case "False":
			return packages.HealthUnhealthy
		case "True":
			if activeCond == nil {
				return packages.HealthHealthy
			}
		}
	}
	if activeCond != nil {
		switch activeCond["status"] {
		case "True":
			return packages.HealthHealthy
		case "False":
			return packages.HealthNeutral
		}
	}
	active, _, _ := unstructured.NestedString(sj.Object, "status", "active")
	if active == "True" {
		return packages.HealthHealthy
	}
	return packages.HealthNeutral
}

func workflowPrimaryImage(wf *unstructured.Unstructured) string {
	if image := templateImage(wf.Object, "spec", "templates"); image != "" {
		return image
	}
	return templateImage(wf.Object, "status", "storedWorkflowTemplateSpec", "templates")
}

func cronWorkflowPrimaryImage(cwf *unstructured.Unstructured) string {
	if image := templateImage(cwf.Object, "spec", "workflowSpec", "templates"); image != "" {
		return image
	}
	return ""
}

func templateImage(obj map[string]any, path ...string) string {
	templates, found, _ := unstructured.NestedSlice(obj, path...)
	if !found {
		return ""
	}
	for _, raw := range templates {
		tpl, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		container, _, _ := unstructured.NestedMap(tpl, "container")
		if image, _ := container["image"].(string); image != "" {
			return image
		}
	}
	return ""
}

func cronWorkflowSchedule(cwf *unstructured.Unstructured) string {
	schedules, found, _ := unstructured.NestedStringSlice(cwf.Object, "spec", "schedules")
	if found && len(schedules) > 0 {
		return strings.Join(schedules, ", ")
	}
	schedule, _, _ := unstructured.NestedString(cwf.Object, "spec", "schedule")
	return schedule
}

func forEachWorkloadNamespace(namespaces []string, fn func(ns string)) {
	if namespaces == nil {
		fn("")
		return
	}
	for _, ns := range namespaces {
		fn(ns)
	}
}

// --- grouping ------------------------------------------------------------

// groupApplications partitions workloads into logical apps. Each workload
// contributes atoms — its structural-root key, its overlay key, and a canonical
// ArgoCD key (so tracking-id and instance label modes collapse) — that are
// union-found together. Workloads sharing any atom (transitively) are one app.
// Satellites are attached but never used to merge: two apps that share a Service
// stay two apps.
func groupApplications(inputs []appWorkloadInput) []appRow {
	d := newDSU()
	argoAppNamespaces := argoApplicationNamespaces(inputs)
	for _, in := range inputs {
		atoms := inputAtoms(in, argoAppNamespaces)
		for i := 1; i < len(atoms); i++ {
			d.union(atoms[0], atoms[i])
		}
	}

	rows := map[string]*appRow{}
	order := []string{}
	members := map[string][]appWorkloadInput{}
	for _, in := range inputs {
		comp := d.find("S:" + in.rootKey)
		if _, ok := members[comp]; !ok {
			order = append(order, comp)
		}
		members[comp] = append(members[comp], in)
	}

	for _, comp := range order {
		ins := members[comp]
		r := &appRow{}
		identifyApp(r, ins)
		servingHealth := packages.Health("")
		appVers := map[string]struct{}{}
		labeled := 0
		nss := map[string]struct{}{}
		tagsByRepo := map[string]map[string]struct{}{}
		for _, in := range ins {
			r.Workloads = append(r.Workloads, in.wl)
			r.Events = append(r.Events, in.events...)
			r.Health = string(packages.WorseHealth(packages.Health(r.Health), packages.Health(in.wl.Health)))
			if in.wl.WorkloadClass == "service" || in.wl.WorkloadClass == "worker" {
				servingHealth = packages.WorseHealth(servingHealth, packages.Health(in.wl.Health))
			}
			if v := in.wl.Version; v != "" && !slices.Contains(r.Versions, v) {
				r.Versions = append(r.Versions, v)
			}
			if av := in.wl.AppVersion; av != "" {
				appVers[av] = struct{}{}
				labeled++
			}
			if in.wl.Namespace != "" {
				nss[in.wl.Namespace] = struct{}{}
			}
			if repo, tag := imageRepo(in.wl.Image), in.wl.Version; repo != "" && tag != "" {
				if tagsByRepo[repo] == nil {
					tagsByRepo[repo] = map[string]struct{}{}
				}
				tagsByRepo[repo][tag] = struct{}{}
			}
			mergeRelationships(r, in.rels)
		}
		if r.WorkloadClass == "mixed" && servingHealth != "" {
			r.Health = string(servingHealth)
		}
		setStrictSourceRef(r, ins)
		// The app lives where its WORKLOADS run — a Flux HelmRelease in
		// flux-system deploying into demo is a demo app, not a flux-system one
		// (the manager's home is provenance, not residence; it also must not
		// trip the system-namespace filter). This deliberately overrides the
		// provenance-key namespace identifyApp set. Multiple namespaces →
		// Namespace empty, Namespaces carries the full list.
		if len(nss) > 0 {
			r.Namespaces = make([]string, 0, len(nss))
			for ns := range nss {
				r.Namespaces = append(r.Namespaces, ns)
			}
			sort.Strings(r.Namespaces)
			if len(r.Namespaces) == 1 {
				r.Namespace = r.Namespaces[0]
			} else {
				r.Namespace = ""
			}
		}
		// Version skew means the SAME image runs different tags across the
		// app's workloads — real drift. Different components shipping
		// different images at different versions is normal, not skew.
		for _, tags := range tagsByRepo {
			if len(tags) > 1 {
				r.VersionSkew = true
				break
			}
		}
		// A single upstream version is the app's "main version" only when EVERY
		// workload declares it and they agree (a single-chart add-on). One labeled
		// workload among unlabeled ones, or a multi-chart umbrella that disagrees,
		// leaves it empty — the UI falls back to per-workload image tags.
		if len(appVers) == 1 && labeled == len(ins) {
			for av := range appVers {
				r.AppVersion = av
			}
		}
		finalizeRelationships(r)
		sort.Strings(r.Versions)
		sort.SliceStable(r.Events, func(i, j int) bool { return r.Events[i].LastSeen > r.Events[j].LastSeen })
		if len(r.Events) > 12 {
			r.Events = r.Events[:12]
		}
		rows[comp] = r
	}

	out := make([]appRow, 0, len(order))
	for _, comp := range order {
		out = append(out, *rows[comp])
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// inputAtoms returns the union-find atoms for a workload. The structural-root
// atom is always present; overlay and canonical-Argo atoms consolidate roots
// the graph can't connect.
func inputAtoms(in appWorkloadInput, argoAppNamespaces map[string]map[string]bool) []string {
	atoms := []string{"S:" + in.rootKey}
	atoms = append(atoms, argoCanonicalAtoms(in.rootKind, in.rootKey, argoAppNamespaces)...)
	if in.overlay != nil {
		atoms = append(atoms, "O:"+in.overlay.Winner.Key)
		atoms = append(atoms, argoCanonicalAtoms("Application", in.overlay.Winner.Key, argoAppNamespaces)...)
	}
	return atoms
}

// identifyApp sets a row's identity (key/name/namespace/tier/confidence) and
// add-on classification from its member workloads. A label overlay wins (it
// carries an explicit tier + confidence); otherwise the structural root — and
// when that root is a GitOps manager, its kind synthesizes the tier so the
// surface still attributes provenance (Argo/Flux) for unlabeled in-cluster apps.
func identifyApp(r *appRow, ins []appWorkloadInput) {
	var best *subject.Signal
	for i := range ins {
		if ins[i].overlay == nil {
			continue
		}
		w := ins[i].overlay.Winner
		if best == nil || w.Tier < best.Tier || (w.Tier == best.Tier && w.Key < best.Key) {
			sig := w
			best = &sig
		}
	}
	if best != nil {
		r.Key = best.Key
		r.Name = appNameFromKey(best.Key)
		r.Namespace = namespaceFromKey(best.Key)
		r.Tier = int(best.Tier)
		r.Confidence = string(best.Confidence)
	} else {
		root := pickRoot(ins)
		r.Key = root.rootKey
		r.Name = appNameFromKey(root.rootKey)
		r.Namespace = namespaceFromKey(root.rootKey)
		if t, c, ok := managerTier(root.rootKind); ok {
			r.Tier = t
			r.Confidence = c
		}
	}

	r.Category, r.AddonReason = classifyAppCategory(ins)
	r.WorkloadClass = classifyAppWorkloads(ins)
	r.MatchKeys = collectExactMatchKeys(ins)
}

// signalMatchKind maps an overlay Signal (winner or retained runner-up) to its
// client-facing evidence kind + the EXACT grouping value the server keyed on —
// appNameFromKey(sig.Key), never a recomputed display name. Kinds are pinned to
// the pkg/subject tiers: note that "instance" is app.kubernetes.io/instance
// (TierInstance) while argocd.argoproj.io/instance (TierArgoInstance) maps to
// "argo". TierFluxKustomize has no event-matchable label kind, so it is
// skipped. The caller namespace-scopes the final key (see collectExactMatchKeys).
func signalMatchKind(sig subject.Signal) (kind, value string, ok bool) {
	value = appNameFromKey(sig.Key)
	if value == "" {
		return "", "", false
	}
	switch sig.Tier {
	// Native Helm (TierHelmRelease) is deliberately absent: its identity is
	// the meta.helm.sh/release-name ANNOTATION, and timeline events carry only
	// labels, so a key derived from it can never join. The chart's recommended
	// labels (instance/name/part-of) surface through their own tiers. Flux
	// HelmRelease identity is a label (helm.toolkit.fluxcd.io/name) — joinable.
	// TierArgoTrackingID stays even though the tracking id is an annotation:
	// its key equals the argocd.argoproj.io/instance label's, so it joins
	// whenever Argo's tracking mode also applies the label.
	case subject.TierFluxHelmRelease:
		return "helm", value, true
	case subject.TierArgoTrackingID, subject.TierArgoInstance:
		return "argo", value, true
	case subject.TierInstance:
		return "instance", value, true
	case subject.TierPartOf:
		return "part-of", value, true
	case subject.TierAppName:
		return "name", value, true
	case subject.TierBareApp:
		return "app", value, true
	}
	return "", "", false
}

// collectExactMatchKeys gathers the exact grouping-signal evidence keys for an
// app from EVERY member workload's overlay (winner + retained conflicts), deduped
// and sorted. These are the kinds a deleted member's timeline event can still
// carry as labels, so the client can re-join it to this app.
//
// Keys are namespace-scoped as "<kind>:<namespace>:<value>" using the workload's
// OWN namespace: the same label value (e.g. instance=redis) can appear in two
// namespaces belonging to different apps, and an unscoped "instance:redis" would
// let a historical lane join the wrong one. An app whose workloads span multiple
// namespaces therefore emits one scoped key per namespace it lives in. The
// client looks these up with the matching EVENT's namespace. The informational
// name-stem key is appended later (resolveAppIdentities), unscoped and excluded
// from event matching.
func collectExactMatchKeys(ins []appWorkloadInput) []string {
	seen := map[string]bool{}
	var out []string
	for _, in := range ins {
		if in.overlay == nil {
			continue
		}
		signals := append([]subject.Signal{in.overlay.Winner}, in.overlay.Conflicts...)
		for _, sig := range signals {
			kind, value, ok := signalMatchKind(sig)
			if !ok {
				continue
			}
			key := kind + ":" + in.wl.Namespace + ":" + value
			if !seen[key] {
				seen[key] = true
				out = append(out, key)
			}
		}
	}
	sort.Strings(out)
	return out
}

// pickRoot prefers a GitOps-manager root over a raw workload root for identity.
func pickRoot(ins []appWorkloadInput) appWorkloadInput {
	for _, in := range ins {
		if _, _, ok := managerTier(in.rootKind); ok {
			return in
		}
	}
	return ins[0]
}

// managerTier maps a structural manager-root kind to the overlay tier it stands
// in for, so an in-cluster GitOps-managed app without labels still attributes.
func managerTier(kind string) (tier int, confidence string, ok bool) {
	switch kind {
	case string(topology.KindHelmRelease):
		return int(subject.TierFluxHelmRelease), string(subject.ConfidenceHigh), true
	case string(topology.KindKustomization):
		return int(subject.TierFluxKustomize), string(subject.ConfidenceHigh), true
	case string(topology.KindApplication):
		return int(subject.TierArgoTrackingID), string(subject.ConfidenceHigh), true
	}
	return 0, "", false
}

func sourceRefForInput(overlay *subject.AppOverlay, rootKind, rootKey string) *appSourceRef {
	if overlay != nil {
		if ref := sourceRefFromSubject(overlay.Winner.Ref); ref != nil {
			return ref
		}
	}
	return sourceRefFromRoot(rootKind, rootKey)
}

func sourceRefFromSubject(ref subject.Ref) *appSourceRef {
	if ref.Name == "" || ref.Namespace == "" {
		return nil
	}
	switch {
	case ref.Kind == "HelmRelease" && ref.Group == "":
		return &appSourceRef{Type: "helm", Tool: "helm", Kind: ref.Kind, Namespace: ref.Namespace, Name: ref.Name}
	case ref.Kind == "Application" && ref.Group == "argoproj.io":
		return &appSourceRef{Type: "gitops", Tool: "argocd", Group: ref.Group, Kind: ref.Kind, Namespace: ref.Namespace, Name: ref.Name}
	case ref.Kind == "Kustomization" && ref.Group == "kustomize.toolkit.fluxcd.io":
		return &appSourceRef{Type: "gitops", Tool: "fluxcd", Group: ref.Group, Kind: ref.Kind, Namespace: ref.Namespace, Name: ref.Name}
	case ref.Kind == "HelmRelease" && ref.Group == "helm.toolkit.fluxcd.io":
		return &appSourceRef{Type: "gitops", Tool: "fluxcd", Group: ref.Group, Kind: ref.Kind, Namespace: ref.Namespace, Name: ref.Name}
	default:
		return nil
	}
}

func sourceRefFromRoot(rootKind, rootKey string) *appSourceRef {
	parts := strings.SplitN(rootKey, "/", 3)
	if len(parts) != 3 || parts[0] == "" || parts[2] == "" {
		return nil
	}
	ns, name := parts[0], parts[2]
	switch rootKind {
	case string(topology.KindApplication):
		return &appSourceRef{Type: "gitops", Tool: "argocd", Group: "argoproj.io", Kind: "Application", Namespace: ns, Name: name}
	case string(topology.KindKustomization):
		return &appSourceRef{Type: "gitops", Tool: "fluxcd", Group: "kustomize.toolkit.fluxcd.io", Kind: "Kustomization", Namespace: ns, Name: name}
	case string(topology.KindHelmRelease):
		return &appSourceRef{Type: "gitops", Tool: "fluxcd", Group: "helm.toolkit.fluxcd.io", Kind: "HelmRelease", Namespace: ns, Name: name}
	default:
		return nil
	}
}

func mergeSourceRef(r *appRow, ref *appSourceRef) {
	if ref == nil || r.sourceConflict {
		return
	}
	if r.SourceRef == nil {
		cp := *ref
		r.SourceRef = &cp
		return
	}
	if !sameSourceRef(r.SourceRef, ref) {
		if r.sourceStrict {
			return
		}
		r.SourceRef = nil
		r.sourceConflict = true
	}
}

func sameSourceRef(a, b *appSourceRef) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Type == b.Type &&
		a.Tool == b.Tool &&
		a.Group == b.Group &&
		a.Kind == b.Kind &&
		a.Namespace == b.Namespace &&
		a.Name == b.Name
}

func argoApplicationNamespaces(inputs []appWorkloadInput) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	add := func(kind, key string) {
		if kind != string(topology.KindApplication) {
			return
		}
		const marker = "/Application/"
		i := strings.Index(key, marker)
		if i < 0 {
			return
		}
		ns := key[:i]
		name := key[i+len(marker):]
		if ns == "" || name == "" || strings.Contains(name, "/") {
			return
		}
		if out[name] == nil {
			out[name] = map[string]bool{}
		}
		out[name][ns] = true
	}
	for _, in := range inputs {
		add(in.rootKind, in.rootKey)
		if in.overlay != nil {
			add("Application", in.overlay.Winner.Key)
		}
	}
	return out
}

// argoCanonicalAtoms extracts tracking-mode-independent atoms from an ArgoCD
// Application key. ResolveOverlay emits "<ns>/Application/<name>" for tracking-id
// (tier 3) but "/Application/<name>" (empty ns) for the instance label (tier 4);
// the in-cluster Application node's structural key is "<argo-ns>/Application/<name>".
// Namespace-qualified atoms keep distinct same-name Applications separate. The
// name-only bridge is emitted only when this result set has at most one concrete
// namespace for that Argo name, so tier-3 and tier-4 tracking modes can still
// collapse without mixing separate controller namespaces.
func argoCanonicalAtoms(kind, key string, argoAppNamespaces map[string]map[string]bool) []string {
	if kind != string(topology.KindApplication) {
		return nil
	}
	const marker = "/Application/"
	i := strings.Index(key, marker)
	if i < 0 {
		return nil
	}
	ns := key[:i]
	name := key[i+len(marker):]
	if name == "" || strings.Contains(name, "/") {
		return nil
	}
	namespaces := argoAppNamespaces[name]
	ambiguous := len(namespaces) > 1
	atoms := []string{}
	if ns != "" {
		atoms = append(atoms, "A:application:"+ns+"/"+name)
	}
	if !ambiguous {
		atoms = append(atoms, "A:application:"+name)
	}
	return atoms
}

func mergeRelationships(r *appRow, rel *appRelationships) {
	if rel == nil {
		return
	}
	if r.Relationships == nil {
		r.Relationships = &appRelationships{}
	}
	agg := r.Relationships
	agg.Services = append(agg.Services, rel.Services...)
	agg.Ingresses = append(agg.Ingresses, rel.Ingresses...)
	agg.Routes = append(agg.Routes, rel.Routes...)
	agg.configRefs = mergeRefSets(agg.configRefs, rel.configRefs)
	agg.scalerRefs = mergeRefSets(agg.scalerRefs, rel.scalerRefs)
	agg.storageRefs = mergeRefSets(agg.storageRefs, rel.storageRefs)
	agg.pdbRefs = mergeRefSets(agg.pdbRefs, rel.pdbRefs)
	if len(rel.configRefs) == 0 {
		agg.Configs += rel.Configs
	}
	if len(rel.scalerRefs) == 0 {
		agg.Scalers += rel.Scalers
	}
	if len(rel.storageRefs) == 0 {
		agg.Storage += rel.Storage
	}
	if len(rel.pdbRefs) == 0 {
		agg.PDBs += rel.PDBs
	}
}

func finalizeRelationships(r *appRow) {
	if r.Relationships == nil {
		return
	}
	r.Relationships.Services = dedupSorted(r.Relationships.Services, 20)
	r.Relationships.Ingresses = dedupSorted(r.Relationships.Ingresses, 20)
	r.Relationships.Routes = dedupSorted(r.Relationships.Routes, 20)
	if len(r.Relationships.configRefs) > 0 {
		r.Relationships.Configs = len(r.Relationships.configRefs)
	}
	if len(r.Relationships.scalerRefs) > 0 {
		r.Relationships.Scalers = len(r.Relationships.scalerRefs)
	}
	if len(r.Relationships.storageRefs) > 0 {
		r.Relationships.Storage = len(r.Relationships.storageRefs)
	}
	if len(r.Relationships.pdbRefs) > 0 {
		r.Relationships.PDBs = len(r.Relationships.pdbRefs)
	}
}

func refsSet(refs []topology.ResourceRef) map[string]struct{} {
	if len(refs) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(refs))
	for _, r := range refs {
		out[refKey(r)] = struct{}{}
	}
	return out
}

func mergeRefSets(dst, src map[string]struct{}) map[string]struct{} {
	if len(src) == 0 {
		return dst
	}
	if dst == nil {
		dst = map[string]struct{}{}
	}
	for k := range src {
		dst[k] = struct{}{}
	}
	return dst
}

func refKey(r topology.ResourceRef) string {
	return r.Group + "/" + r.Kind + "/" + r.Namespace + "/" + r.Name
}

func classifyWorkload(kind string, rels *appRelationships) string {
	switch kind {
	case "Job", "CronJob", "Workflow", "CronWorkflow", "WorkflowTemplate", "ClusterWorkflowTemplate", "ScaledJob":
		return "job"
	case "Deployment", "StatefulSet", "DaemonSet":
		if rels != nil && (len(rels.Services) > 0 || len(rels.Ingresses) > 0 || len(rels.Routes) > 0) {
			return "service"
		}
		return "worker"
	default:
		return "unknown"
	}
}

func appWorkloadAPIGroup(kind string) string {
	switch kind {
	case "Workflow", "CronWorkflow", "WorkflowTemplate", "ClusterWorkflowTemplate":
		return "argoproj.io"
	case "ScaledJob":
		return "keda.sh"
	default:
		return ""
	}
}

func classifyAppWorkloads(ins []appWorkloadInput) string {
	classes := map[string]bool{}
	for _, in := range ins {
		switch in.wl.WorkloadClass {
		case "service", "worker", "job":
			classes[in.wl.WorkloadClass] = true
		}
		// Unclassifiable members (e.g. a bare Pod) don't poison a known class.
	}
	if len(classes) == 0 {
		return "unknown"
	}
	if classes["service"] && !classes["job"] {
		// A deployable unit with an API Deployment and a background worker is
		// still operated primarily as a service.
		return "service"
	}
	if len(classes) == 1 {
		for c := range classes {
			return c
		}
	}
	// A real composition (e.g. a service plus its scheduled jobs). The UI
	// derives the breakdown from the per-workload classes; "unknown" would
	// throw away what classifyWorkload confidently determined.
	return "mixed"
}

func classifyAppCategory(ins []appWorkloadInput) (category, reason string) {
	addonCount := 0
	reasons := []string{}
	for _, in := range ins {
		if !in.addon {
			continue
		}
		addonCount++
		if in.addonWhy != "" && !slices.Contains(reasons, in.addonWhy) {
			reasons = append(reasons, in.addonWhy)
		}
	}
	if addonCount == 0 {
		return "app", ""
	}
	reason = strings.Join(reasons, "; ")
	if addonCount == len(ins) {
		return "addon", reason
	}
	if reason != "" {
		reason = "mixed add-on evidence: " + reason
	}
	return "mixed", reason
}

// --- small helpers --------------------------------------------------------

// dsu is a string union-find for partitioning workloads by shared atoms.
type dsu struct{ parent map[string]string }

func newDSU() *dsu { return &dsu{parent: map[string]string{}} }

func (d *dsu) find(x string) string {
	p, ok := d.parent[x]
	if !ok {
		d.parent[x] = x
		return x
	}
	if p != x {
		d.parent[x] = d.find(p)
	}
	return d.parent[x]
}

func (d *dsu) union(a, b string) {
	ra, rb := d.find(a), d.find(b)
	if ra != rb {
		d.parent[ra] = rb
	}
}

func dedupSorted(in []string, cap int) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	if len(out) > cap {
		out = out[:cap]
	}
	return out
}

// indexPodsByNamespace lists pods once per namespace and buckets them. Each
// workload still scans its namespace bucket by selector; the important bit is
// avoiding repeated lister/cache reads.
func indexPodsByNamespace(cache *k8s.ResourceCache, namespaces []string) map[string][]*corev1.Pod {
	out := map[string][]*corev1.Pod{}
	lister := cache.Pods()
	if lister == nil {
		return out
	}
	add := func(ns string) {
		var pods []*corev1.Pod
		if ns == "" {
			pods, _ = lister.List(labels.Everything())
		} else {
			pods, _ = lister.Pods(ns).List(labels.Everything())
		}
		for _, p := range pods {
			out[p.Namespace] = append(out[p.Namespace], p)
		}
	}
	if namespaces == nil {
		add("")
	} else {
		for _, ns := range namespaces {
			add(ns)
		}
	}
	return out
}

// indexWarningEventsByObject lists events once per namespace and indexes the
// Warnings by involvedObject name, so each workload joins its events in O(1)
// instead of re-scanning the whole namespace event stream.
func indexWarningEventsByObject(cache *k8s.ResourceCache, namespaces []string) map[string]map[string][]*corev1.Event {
	out := map[string]map[string][]*corev1.Event{}
	lister := cache.Events()
	if lister == nil {
		return out
	}
	add := func(ns string) {
		var evs []*corev1.Event
		if ns == "" {
			evs, _ = lister.List(labels.Everything())
		} else {
			evs, _ = lister.Events(ns).List(labels.Everything())
		}
		for _, e := range evs {
			if e.Type != "Warning" {
				continue
			}
			m := out[e.Namespace]
			if m == nil {
				m = map[string][]*corev1.Event{}
				out[e.Namespace] = m
			}
			key := e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name
			m[key] = append(m[key], e)
		}
	}
	if namespaces == nil {
		add("")
	} else {
		for _, ns := range namespaces {
			add(ns)
		}
	}
	return out
}

// podsForSelector filters an already-listed namespace pod set by a workload's
// selector — no extra API/cache calls.
func podsForSelector(pods []*corev1.Pod, selector *metav1.LabelSelector) []*corev1.Pod {
	if selector == nil || len(pods) == 0 {
		return nil
	}
	sel, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return nil
	}
	var out []*corev1.Pod
	for _, p := range pods {
		if sel.Matches(labels.Set(p.Labels)) {
			out = append(out, p)
		}
	}
	return out
}

// primaryImage returns the first container's image (the conventional "the app"
// container — mirrors pkg/ai/context/summary.go's first-container choice).
func primaryImage(containers []corev1.Container) string {
	if len(containers) > 0 {
		return containers[0].Image
	}
	return ""
}

// podsRestarts sums container restarts across a workload's pods and returns the
// last-terminated reason of the worst (most-restarting) pod — the crash signal
// (CrashLoopBackOff / OOMKilled / Error).
func podsRestarts(pods []*corev1.Pod) (int, string) {
	total := 0
	var worst int32 = -1
	reason := ""
	for _, p := range pods {
		rc, r := health.PodRestartContext(p)
		total += int(rc)
		if rc > worst {
			worst = rc
			reason = r
		}
	}
	return total, reason
}

// eventsForWorkload joins a workload's Warning events from the per-namespace
// index (the workload object + its pods), deduped by (object, reason) with
// summed counts — the "why is it broken" feed (FailedScheduling, ImagePullBackOff,
// FailedMount, …) that restarts alone miss.
func eventsForWorkload(byObject map[string][]*corev1.Event, workloadKind, workloadName string, pods []*corev1.Pod) []appEvent {
	if byObject == nil {
		return nil
	}
	names := make([]string, 0, len(pods)+1)
	names = append(names, workloadKind+"/"+workloadName)
	for _, p := range pods {
		names = append(names, "Pod/"+p.Name)
	}
	byKey := map[string]*appEvent{}
	order := []string{}
	for _, n := range names {
		for _, e := range byObject[n] {
			key := e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name + "/" + e.Reason
			c := int(e.Count)
			if c < 1 {
				c = 1
			}
			if a, ok := byKey[key]; ok {
				a.Count += c
				if ts := appEventLastSeen(e); ts > a.LastSeen {
					a.LastSeen = ts
					a.Message = e.Message
				}
				if ts := appEventFirstSeen(e); ts != "" && (a.FirstSeen == "" || ts < a.FirstSeen) {
					a.FirstSeen = ts
				}
				continue
			}
			byKey[key] = &appEvent{
				Type: e.Type, Reason: e.Reason, Message: e.Message, Count: c,
				Object:    e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name,
				FirstSeen: appEventFirstSeen(e),
				LastSeen:  appEventLastSeen(e),
			}
			order = append(order, key)
		}
	}
	out := make([]appEvent, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	return out
}

func appEventFirstSeen(e *corev1.Event) string {
	if e == nil {
		return ""
	}
	if !e.FirstTimestamp.IsZero() {
		return e.FirstTimestamp.Time.UTC().Format(time.RFC3339)
	}
	if !e.EventTime.IsZero() {
		return e.EventTime.Time.UTC().Format(time.RFC3339)
	}
	if !e.CreationTimestamp.IsZero() {
		return e.CreationTimestamp.Time.UTC().Format(time.RFC3339)
	}
	return ""
}

func appEventLastSeen(e *corev1.Event) string {
	if e == nil {
		return ""
	}
	if !e.LastTimestamp.IsZero() {
		return e.LastTimestamp.Time.UTC().Format(time.RFC3339)
	}
	if !e.EventTime.IsZero() {
		return e.EventTime.Time.UTC().Format(time.RFC3339)
	}
	if !e.CreationTimestamp.IsZero() {
		return e.CreationTimestamp.Time.UTC().Format(time.RFC3339)
	}
	return ""
}

func controllerOwnerName(refs []metav1.OwnerReference, kind string) string {
	for _, owner := range refs {
		if owner.Kind == kind && owner.Name != "" && owner.Controller != nil && *owner.Controller {
			return owner.Name
		}
	}
	return ""
}

func cronWorkflowOwnerName(wf *unstructured.Unstructured) string {
	if owner := controllerOwnerName(wf.GetOwnerReferences(), "CronWorkflow"); owner != "" {
		return owner
	}
	return wf.GetLabels()["workflows.argoproj.io/cron-workflow"]
}

// imageTag extracts the tag from an image ref. Digest-pinned refs (@sha256:…)
// and untagged refs (implicit :latest) return "" — no false version.
func imageTag(image string) string {
	if image == "" {
		return ""
	}
	if at := strings.Index(image, "@"); at >= 0 {
		image = image[:at]
	}
	slash := strings.LastIndex(image, "/")
	colon := strings.LastIndex(image, ":")
	if colon > slash {
		return image[colon+1:]
	}
	return ""
}

// imageRepo is the image ref without its tag/digest — the unit version skew is
// measured across: two workloads running the same repo at different tags.
func imageRepo(image string) string {
	if image == "" {
		return ""
	}
	if at := strings.Index(image, "@"); at >= 0 {
		image = image[:at]
	}
	slash := strings.LastIndex(image, "/")
	colon := strings.LastIndex(image, ":")
	if colon > slash {
		return image[:colon]
	}
	return image
}

// levelToPackagesHealth projects a canonical health.Level onto the package wire
// vocabulary. neutral (intentional/idle — suspended, scaled-to-zero) maps to the
// dedicated HealthNeutral; it aggregates as most-benign, so an all-idle app rolls
// up to "Idle" in the Applications UI while a mixed healthy+idle app still reads
// Healthy (WorseHealth prefers healthy on the tie).
func levelToPackagesHealth(l health.Level) packages.Health {
	return packages.Health(l)
}

func appNameFromKey(key string) string {
	if i := strings.LastIndex(key, "/"); i >= 0 && i < len(key)-1 {
		return key[i+1:]
	}
	return key
}

func namespaceFromKey(key string) string {
	if i := strings.Index(key, "/"); i > 0 {
		return key[:i]
	}
	return ""
}

// (worstAppHealth / appHealthRank removed — the app rollup now uses
// packages.WorseHealth, the single rollup ordering.)
