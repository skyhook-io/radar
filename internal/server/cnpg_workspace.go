package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"slices"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	bp "github.com/skyhook-io/radar/pkg/audit"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

const cnpgBarmanGroup = "barmancloud.cnpg.io"

const (
	cnpgCoverageFull         = "full"
	cnpgCoveragePartial      = "partial"
	cnpgCoverageDenied       = "denied"
	cnpgCoverageNotInstalled = "notInstalled"
	cnpgCoverageSyncing      = "syncing"
	cnpgCoverageError        = "error"
)

const (
	cnpgWorkspacePodsKey    = "pods"
	cnpgWorkspaceBackupsKey = "backups"
	cnpgWorkspaceSchedKey   = "scheduledBackups"
	cnpgWorkspaceClusterKey = "clusters"
)

// cnpgBackupWindow bounds how far back settled Backups are returned. The newest
// completed Backup per Cluster is kept regardless: it is the last-good-backup
// fact the workspace reports.
const cnpgBackupWindow = 7 * 24 * time.Hour

const cnpgNoDeclarativeBackupCheckID = "cnpgNoDeclarativeBackup"

type cnpgWorkspaceKind struct {
	key           string
	group         string
	kind          string
	resource      string
	clusterScoped bool
}

var cnpgWorkspaceKinds = []cnpgWorkspaceKind{
	{key: cnpgWorkspaceClusterKey, group: cnpgGroup, kind: "Cluster", resource: "clusters"},
	{key: cnpgWorkspaceBackupsKey, group: cnpgGroup, kind: "Backup", resource: "backups"},
	{key: cnpgWorkspaceSchedKey, group: cnpgGroup, kind: "ScheduledBackup", resource: "scheduledbackups"},
	{key: "poolers", group: cnpgGroup, kind: "Pooler", resource: "poolers"},
	{key: "databases", group: cnpgGroup, kind: "Database", resource: "databases"},
	{key: "publications", group: cnpgGroup, kind: "Publication", resource: "publications"},
	{key: "subscriptions", group: cnpgGroup, kind: "Subscription", resource: "subscriptions"},
	{key: "imageCatalogs", group: cnpgGroup, kind: "ImageCatalog", resource: "imagecatalogs"},
	{key: "clusterImageCatalogs", group: cnpgGroup, kind: "ClusterImageCatalog", resource: "clusterimagecatalogs", clusterScoped: true},
	{key: "objectStores", group: cnpgBarmanGroup, kind: "ObjectStore", resource: "objectstores"},
}

// CNPGWorkspaceCoverage states how much of one kind the caller could see.
// DeniedNamespaces lists only namespaces already in the caller's scope.
type CNPGWorkspaceCoverage struct {
	State            string   `json:"state"`
	DeniedNamespaces []string `json:"deniedNamespaces,omitempty"`
}

// CNPGWorkspaceIssue is the subset of issuesapi.Issue the workspace renders.
type CNPGWorkspaceIssue struct {
	ID        string             `json:"id"`
	Severity  issuesapi.Severity `json:"severity"`
	Category  issuesapi.Category `json:"category"`
	Kind      string             `json:"kind"`
	Group     string             `json:"group,omitempty"`
	Namespace string             `json:"namespace,omitempty"`
	Name      string             `json:"name"`
	Reason    string             `json:"reason"`
	Message   string             `json:"message,omitempty"`
	Cause     string             `json:"cause,omitempty"`
	Action    string             `json:"action,omitempty"`
	FirstSeen time.Time          `json:"first_seen,omitzero"`
}

// CNPGWorkspaceAuditFinding is one audit finding on a visible CNPG object.
type CNPGWorkspaceAuditFinding struct {
	CheckID   string `json:"checkId"`
	Severity  string `json:"severity"`
	Kind      string `json:"kind"`
	Group     string `json:"group,omitempty"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Message   string `json:"message"`
}

// CNPGWorkspaceResponse is GET /api/cnpg/workspace.
type CNPGWorkspaceResponse struct {
	Installed      bool                             `json:"installed"`
	Context        string                           `json:"context"`
	Namespaces     []string                         `json:"namespaces"`
	Coverage       map[string]CNPGWorkspaceCoverage `json:"coverage"`
	Objects        map[string][]any                 `json:"objects"`
	Issues         []CNPGWorkspaceIssue             `json:"issues"`
	Audit          []CNPGWorkspaceAuditFinding      `json:"audit"`
	BackupsOmitted int                              `json:"backupsOmitted"`
}

// cnpgKindAccess is the resolved read scope for one kind. all means every
// namespace in the request's scope (or the cluster-scoped kind itself).
type cnpgKindAccess struct {
	state      string
	all        bool
	namespaces map[string]bool
}

func (a cnpgKindAccess) covers(namespace string) bool {
	if a.state != cnpgCoverageFull && a.state != cnpgCoveragePartial {
		return false
	}
	return a.all || a.namespaces[namespace]
}

func newCNPGWorkspaceResponse(namespaces []string) CNPGWorkspaceResponse {
	resp := CNPGWorkspaceResponse{
		Context:    k8s.ActiveClusterContext(),
		Namespaces: namespaces,
		Coverage:   map[string]CNPGWorkspaceCoverage{},
		Objects:    map[string][]any{},
		Issues:     []CNPGWorkspaceIssue{},
		Audit:      []CNPGWorkspaceAuditFinding{},
	}
	for _, k := range cnpgWorkspaceKinds {
		resp.Coverage[k.key] = CNPGWorkspaceCoverage{State: cnpgCoverageNotInstalled}
		resp.Objects[k.key] = []any{}
	}
	resp.Coverage[cnpgWorkspacePodsKey] = CNPGWorkspaceCoverage{State: cnpgCoverageNotInstalled}
	resp.Objects[cnpgWorkspacePodsKey] = []any{}
	return resp
}

// handleCNPGWorkspace serves GET /api/cnpg/workspace: every CloudNativePG kind
// plus instance Pods, each authorized on its own. The generic resource list
// does not gate namespaced CRDs per kind, so it cannot tell "no access" from
// "none"; this endpoint states which one it is for every kind.
func (s *Server) handleCNPGWorkspace(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}

	namespaces := s.parseNamespacesForUser(r)
	resp := newCNPGWorkspaceResponse(namespaces)

	disc := k8s.GetResourceDiscovery()
	if disc != nil {
		for _, k := range cnpgWorkspaceKinds {
			if _, ok := disc.GetGVRWithGroup(k.kind, k.group); ok {
				resp.Installed = true
				break
			}
		}
		if !resp.Installed {
			s.writeJSON(w, resp)
			return
		}
	}

	access := map[string]cnpgKindAccess{}
	items := map[string][]*unstructured.Unstructured{}
	for _, k := range cnpgWorkspaceKinds {
		if disc != nil {
			if _, ok := disc.GetGVRWithGroup(k.kind, k.group); !ok {
				access[k.key] = cnpgKindAccess{state: cnpgCoverageNotInstalled}
				continue
			}
		}
		acc, denied, list := s.cnpgWorkspaceReadKind(r, cache, k, namespaces)
		if acc.state != cnpgCoverageNotInstalled {
			resp.Installed = true
		}
		access[k.key] = acc
		items[k.key] = list
		resp.Coverage[k.key] = CNPGWorkspaceCoverage{State: acc.state, DeniedNamespaces: denied}
	}
	if !resp.Installed {
		s.writeJSON(w, resp)
		return
	}

	for _, k := range cnpgWorkspaceKinds {
		list := items[k.key]
		if k.key == cnpgWorkspaceBackupsKey {
			var omitted int
			list, omitted = windowCNPGBackups(list, time.Now())
			resp.BackupsOmitted = omitted
		} else {
			sortCNPGObjects(list)
		}
		out := make([]any, 0, len(list))
		for _, u := range list {
			out = append(out, u.Object)
		}
		resp.Objects[k.key] = out
	}

	podAccess, podDenied, pods := s.cnpgWorkspaceReadPods(r, cache, namespaces)
	access[cnpgWorkspacePodsKey] = podAccess
	resp.Coverage[cnpgWorkspacePodsKey] = CNPGWorkspaceCoverage{State: podAccess.state, DeniedNamespaces: podDenied}
	resp.Objects[cnpgWorkspacePodsKey] = pods

	resp.Issues = s.cnpgWorkspaceIssues(r, namespaces, access)
	resp.Audit = cnpgWorkspaceAudit(items[cnpgWorkspaceClusterKey], items[cnpgWorkspaceSchedKey], access[cnpgWorkspaceSchedKey])

	s.writeJSON(w, resp)
}

// cnpgWorkspaceScope resolves where the caller may list one namespaced
// resource: nil allowed means the whole request scope. denied only ever names
// namespaces drawn from the caller's own scope (their view filter, or all
// namespaces for a caller who may see every namespace).
func (s *Server) cnpgWorkspaceScope(r *http.Request, namespaces []string, group, resource string) (allowed, denied []string, any bool) {
	if noNamespaceAccess(namespaces) {
		return []string{}, nil, false
	}
	if s.canRead(r, group, resource, "", "list") {
		return namespaces, nil, true
	}
	candidates := namespaces
	if candidates == nil {
		candidates = allNamespaceNames()
	}
	if len(candidates) == 0 {
		return []string{}, nil, false
	}
	allowed = s.filterNamespacesByCanRead(r, group, resource, "list", candidates)
	for _, ns := range candidates {
		if !slices.Contains(allowed, ns) {
			denied = append(denied, ns)
		}
	}
	sort.Strings(denied)
	return allowed, denied, len(allowed) > 0
}

func accessFromScope(allowed, denied []string) cnpgKindAccess {
	acc := cnpgKindAccess{state: cnpgCoverageFull, all: allowed == nil}
	if len(denied) > 0 {
		acc.state = cnpgCoveragePartial
	}
	if allowed != nil {
		acc.namespaces = make(map[string]bool, len(allowed))
		for _, ns := range allowed {
			acc.namespaces[ns] = true
		}
	}
	return acc
}

func (s *Server) cnpgWorkspaceReadKind(r *http.Request, cache *k8s.ResourceCache, k cnpgWorkspaceKind, namespaces []string) (cnpgKindAccess, []string, []*unstructured.Unstructured) {
	var acc cnpgKindAccess
	var denied, readNamespaces []string
	if k.clusterScoped {
		if !s.canRead(r, k.group, k.resource, "", "list") {
			return cnpgKindAccess{state: cnpgCoverageDenied}, nil, nil
		}
		acc = cnpgKindAccess{state: cnpgCoverageFull, all: true}
	} else {
		allowed, d, ok := s.cnpgWorkspaceScope(r, namespaces, k.group, k.resource)
		if !ok {
			return cnpgKindAccess{state: cnpgCoverageDenied}, nil, nil
		}
		acc, denied, readNamespaces = accessFromScope(allowed, d), d, allowed
	}

	list, err := readCNPGKind(r.Context(), cache, k, readNamespaces)
	switch {
	case err == nil:
		return acc, denied, list
	case errors.Is(err, k8s.ErrUnknownDynamicKind):
		return cnpgKindAccess{state: cnpgCoverageNotInstalled}, nil, nil
	case errors.Is(err, errDynamicNotSynced):
		return cnpgKindAccess{state: cnpgCoverageSyncing}, nil, nil
	default:
		log.Printf("[cnpg] Failed to list %s.%s for workspace: %v", k.kind, k.group, err)
		return cnpgKindAccess{state: cnpgCoverageError}, nil, nil
	}
}

func readCNPGKind(ctx context.Context, cache *k8s.ResourceCache, k cnpgWorkspaceKind, namespaces []string) ([]*unstructured.Unstructured, error) {
	if namespaces == nil {
		return filterCNPGGroup(listDynamicSynced(ctx, cache, k.kind, k.group, ""))
	}
	var out []*unstructured.Unstructured
	for _, ns := range namespaces {
		list, err := filterCNPGGroup(listDynamicSynced(ctx, cache, k.kind, k.group, ns))
		if err != nil {
			return nil, err
		}
		out = append(out, list...)
	}
	return out, nil
}

// filterCNPGGroup drops anything whose apiVersion is not a CNPG group, so a
// Velero Backup or a CAPI Cluster can never ride along on a kind-name match.
func filterCNPGGroup(items []*unstructured.Unstructured, err error) ([]*unstructured.Unstructured, error) {
	if err != nil {
		return nil, err
	}
	out := items[:0:0]
	for _, u := range items {
		if u == nil {
			continue
		}
		if g := u.GroupVersionKind().Group; g != cnpgGroup && g != cnpgBarmanGroup {
			continue
		}
		out = append(out, u)
	}
	return out, nil
}

func sortCNPGObjects(items []*unstructured.Unstructured) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].GetNamespace() != items[j].GetNamespace() {
			return items[i].GetNamespace() < items[j].GetNamespace()
		}
		return items[i].GetName() < items[j].GetName()
	})
}

func cnpgBackupTime(u *unstructured.Unstructured) time.Time {
	for _, field := range []string{"stoppedAt", "startedAt"} {
		if v, _, _ := unstructured.NestedString(u.Object, "status", field); v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				return t
			}
		}
	}
	return u.GetCreationTimestamp().Time
}

// windowCNPGBackups keeps every in-flight Backup, settled ones from the last
// week, and each Cluster's newest completed Backup whatever its age. Sorted by
// namespace, newest first within it.
func windowCNPGBackups(items []*unstructured.Unstructured, now time.Time) ([]*unstructured.Unstructured, int) {
	newestCompleted := map[string]*unstructured.Unstructured{}
	for _, u := range items {
		if phase, _, _ := unstructured.NestedString(u.Object, "status", "phase"); phase != "completed" {
			continue
		}
		clusterName, _, _ := unstructured.NestedString(u.Object, "spec", "cluster", "name")
		key := u.GetNamespace() + "\x00" + clusterName
		if cur, ok := newestCompleted[key]; !ok || cnpgBackupTime(u).After(cnpgBackupTime(cur)) {
			newestCompleted[key] = u
		}
	}
	keepNewest := make(map[*unstructured.Unstructured]bool, len(newestCompleted))
	for _, u := range newestCompleted {
		keepNewest[u] = true
	}

	cutoff := now.Add(-cnpgBackupWindow)
	kept := make([]*unstructured.Unstructured, 0, len(items))
	omitted := 0
	for _, u := range items {
		phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")
		settled := phase == "completed" || phase == "failed"
		if !settled || keepNewest[u] || !cnpgBackupTime(u).Before(cutoff) {
			kept = append(kept, u)
			continue
		}
		omitted++
	}
	sort.SliceStable(kept, func(i, j int) bool {
		if kept[i].GetNamespace() != kept[j].GetNamespace() {
			return kept[i].GetNamespace() < kept[j].GetNamespace()
		}
		ti, tj := cnpgBackupTime(kept[i]), cnpgBackupTime(kept[j])
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return kept[i].GetName() < kept[j].GetName()
	})
	return kept, omitted
}

type cnpgWorkspacePodMeta struct {
	Name              string                  `json:"name"`
	Namespace         string                  `json:"namespace"`
	UID               types.UID               `json:"uid"`
	Labels            map[string]string       `json:"labels,omitempty"`
	OwnerReferences   []metav1.OwnerReference `json:"ownerReferences,omitempty"`
	CreationTimestamp metav1.Time             `json:"creationTimestamp"`
}

type cnpgWorkspaceContainerStatus struct {
	Name         string                `json:"name"`
	Ready        bool                  `json:"ready"`
	RestartCount int32                 `json:"restartCount"`
	State        corev1.ContainerState `json:"state"`
}

type cnpgWorkspacePod struct {
	APIVersion string               `json:"apiVersion"`
	Kind       string               `json:"kind"`
	Metadata   cnpgWorkspacePodMeta `json:"metadata"`
	Spec       struct {
		NodeName string `json:"nodeName,omitempty"`
	} `json:"spec"`
	Status struct {
		Phase             corev1.PodPhase                `json:"phase,omitempty"`
		PodIP             string                         `json:"podIP,omitempty"`
		StartTime         *metav1.Time                   `json:"startTime,omitempty"`
		Conditions        []corev1.PodCondition          `json:"conditions,omitempty"`
		ContainerStatuses []cnpgWorkspaceContainerStatus `json:"containerStatuses,omitempty"`
	} `json:"status"`
}

// isCNPGInstancePod requires the controller-set ownerReference as well as the
// label: a label alone is something any workload can carry.
func isCNPGInstancePod(p *corev1.Pod) bool {
	clusterName := p.Labels["cnpg.io/cluster"]
	if clusterName == "" {
		return false
	}
	for _, ref := range p.OwnerReferences {
		if ref.Kind != "Cluster" || ref.Name != clusterName {
			continue
		}
		if gv, err := schema.ParseGroupVersion(ref.APIVersion); err == nil && gv.Group == cnpgGroup {
			return true
		}
	}
	return false
}

func trimCNPGPod(p *corev1.Pod) cnpgWorkspacePod {
	out := cnpgWorkspacePod{APIVersion: "v1", Kind: "Pod"}
	out.Metadata = cnpgWorkspacePodMeta{
		Name:              p.Name,
		Namespace:         p.Namespace,
		UID:               p.UID,
		Labels:            p.Labels,
		OwnerReferences:   p.OwnerReferences,
		CreationTimestamp: p.CreationTimestamp,
	}
	out.Spec.NodeName = p.Spec.NodeName
	out.Status.Phase = p.Status.Phase
	out.Status.PodIP = p.Status.PodIP
	out.Status.StartTime = p.Status.StartTime
	out.Status.Conditions = p.Status.Conditions
	for _, cs := range p.Status.ContainerStatuses {
		out.Status.ContainerStatuses = append(out.Status.ContainerStatuses, cnpgWorkspaceContainerStatus{
			Name: cs.Name, Ready: cs.Ready, RestartCount: cs.RestartCount, State: cs.State,
		})
	}
	return out
}

func (s *Server) cnpgWorkspaceReadPods(r *http.Request, cache *k8s.ResourceCache, namespaces []string) (cnpgKindAccess, []string, []any) {
	out := []any{}
	allowed, denied, ok := s.cnpgWorkspaceScope(r, namespaces, "", "pods")
	if !ok {
		return cnpgKindAccess{state: cnpgCoverageDenied}, nil, out
	}
	if cache.Pods() == nil {
		log.Printf("[cnpg] Pod cache unavailable for workspace")
		return cnpgKindAccess{state: cnpgCoverageError}, nil, out
	}
	// The typed Pod informer may itself be namespace-scoped when Radar's own
	// identity cannot list Pods cluster-wide; what it does not hold is unread.
	within := capacityNamespacesWithinCache(cache, "pods", allowed)
	if within.unavailable {
		log.Printf("[cnpg] Pod cache does not cover the workspace scope")
		return cnpgKindAccess{state: cnpgCoverageError}, nil, out
	}
	if allowed != nil {
		for _, ns := range allowed {
			if !slices.Contains(within.namespaces, ns) {
				denied = append(denied, ns)
			}
		}
		sort.Strings(denied)
	}
	acc := accessFromScope(within.namespaces, denied)
	if within.partial {
		acc.state = cnpgCoveragePartial
	}

	pods := listPodsScoped(cache.Pods(), within.namespaces)
	sort.Slice(pods, func(i, j int) bool {
		if pods[i].Namespace != pods[j].Namespace {
			return pods[i].Namespace < pods[j].Namespace
		}
		return pods[i].Name < pods[j].Name
	})
	for _, p := range pods {
		if p != nil && isCNPGInstancePod(p) {
			out = append(out, trimCNPGPod(p))
		}
	}
	return acc, denied, out
}

var cnpgWorkspaceKeyByGroupKind = func() map[string]string {
	m := make(map[string]string, len(cnpgWorkspaceKinds))
	for _, k := range cnpgWorkspaceKinds {
		m[k.group+"/"+k.kind] = k.key
	}
	return m
}()

// cnpgWorkspaceIssues runs the same composition /api/issues serves, then keeps
// only CNPG subjects on a kind and namespace this response had coverage for —
// an issue on an object the caller could not list would disclose it.
func (s *Server) cnpgWorkspaceIssues(r *http.Request, namespaces []string, access map[string]cnpgKindAccess) []CNPGWorkspaceIssue {
	out := []CNPGWorkspaceIssue{}
	if noNamespaceAccess(namespaces) {
		return out
	}
	provider := issues.NewCacheProvider()
	if provider == nil {
		return out
	}
	composed, _ := issues.ComposeWithStats(provider, issues.Filters{
		Namespaces:           namespaces,
		Limit:                issues.NoLimit,
		Grouped:              true,
		CanReadClusterScoped: s.issueClusterScopedAccess(r),
		CanReadRelated:       s.issueRelatedResourceAccess(r),
	})
	for _, iss := range composed {
		if iss.Group != cnpgGroup && iss.Group != cnpgBarmanGroup {
			continue
		}
		key, ok := cnpgWorkspaceKeyByGroupKind[iss.Group+"/"+iss.Kind]
		if !ok || !access[key].covers(iss.Namespace) {
			continue
		}
		out = append(out, CNPGWorkspaceIssue{
			ID:        iss.ID,
			Severity:  iss.Severity,
			Category:  iss.Category,
			Kind:      iss.Kind,
			Group:     iss.Group,
			Namespace: iss.Namespace,
			Name:      iss.Name,
			Reason:    iss.Reason,
			Message:   iss.Message,
			Cause:     iss.Cause,
			Action:    iss.Action,
			FirstSeen: iss.FirstSeen,
		})
	}
	return out
}

// cnpgWorkspaceAudit reports the declarative-backup posture finding only for
// Clusters whose namespace had its ScheduledBackups read: without that list,
// "no schedule targets this cluster" is an absence nobody established.
func cnpgWorkspaceAudit(clusters, scheduled []*unstructured.Unstructured, schedAccess cnpgKindAccess) []CNPGWorkspaceAuditFinding {
	out := []CNPGWorkspaceAuditFinding{}
	var subjects []*unstructured.Unstructured
	for _, c := range clusters {
		if schedAccess.covers(c.GetNamespace()) {
			subjects = append(subjects, c)
		}
	}
	if len(subjects) == 0 {
		return out
	}
	results := bp.RunChecks(&bp.CheckInput{
		CNPGClusters:                      subjects,
		CNPGScheduledBackups:              scheduled,
		CNPGScheduledBackupsAuthoritative: true,
	})
	results = applyAuditSettings(results, getAuditConfig())
	if results == nil {
		return out
	}
	for _, f := range results.Findings {
		if f.CheckID != cnpgNoDeclarativeBackupCheckID {
			continue
		}
		out = append(out, CNPGWorkspaceAuditFinding{
			CheckID:   f.CheckID,
			Severity:  f.Severity,
			Kind:      f.Kind,
			Group:     f.Group,
			Namespace: f.Namespace,
			Name:      f.Name,
			Message:   f.Message,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out
}
