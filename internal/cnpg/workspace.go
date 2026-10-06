package cnpg

import (
	"context"
	"log"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	listersbatchv1 "k8s.io/client-go/listers/batch/v1"

	integration "github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	bp "github.com/skyhook-io/radar/pkg/audit"
	"github.com/skyhook-io/radar/pkg/cnpg"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/topology"
)

const barmanGroup = "barmancloud.cnpg.io"

const (
	workspacePodsKey    = "pods"
	workspaceBackupsKey = "backups"
	workspaceSchedKey   = "scheduledBackups"
	workspaceClusterKey = "clusters"
)

// cnpgBackupWindow bounds how far back settled Backups are returned. The newest
// completed Backup per Cluster is kept regardless: it is the last-good-backup
// fact the workspace reports.
const backupWindow = 7 * 24 * time.Hour

const noDeclarativeBackupCheckID = "cnpgNoDeclarativeBackup"

// cnpgGroups are the API groups CloudNativePG objects are read from; a kind
// listed by name keeps only objects of these groups.
var groups = []string{Group, barmanGroup}

var workspaceKinds = []integration.WorkspaceKind{
	{Key: workspaceClusterKey, Group: Group, Kind: "Cluster", Resource: "clusters"},
	{Key: workspaceBackupsKey, Group: Group, Kind: "Backup", Resource: "backups"},
	{Key: workspaceSchedKey, Group: Group, Kind: "ScheduledBackup", Resource: "scheduledbackups"},
	{Key: "poolers", Group: Group, Kind: "Pooler", Resource: "poolers"},
	{Key: "databases", Group: Group, Kind: "Database", Resource: "databases"},
	{Key: "publications", Group: Group, Kind: "Publication", Resource: "publications"},
	{Key: "subscriptions", Group: Group, Kind: "Subscription", Resource: "subscriptions"},
	{Key: "databaseRoles", Group: Group, Kind: "DatabaseRole", Resource: "databaseroles"},
	{Key: "imageCatalogs", Group: Group, Kind: "ImageCatalog", Resource: "imagecatalogs"},
	{Key: "clusterImageCatalogs", Group: Group, Kind: "ClusterImageCatalog", Resource: "clusterimagecatalogs", ClusterScoped: true},
	{Key: "objectStores", Group: barmanGroup, Kind: "ObjectStore", Resource: "objectstores"},
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
	Installed      bool                                `json:"installed"`
	Context        string                              `json:"context"`
	Namespaces     []string                            `json:"namespaces"`
	Coverage       map[string]integration.KindCoverage `json:"coverage"`
	Objects        map[string][]any                    `json:"objects"`
	Issues         []CNPGWorkspaceIssue                `json:"issues"`
	Audit          []CNPGWorkspaceAuditFinding         `json:"audit"`
	BackupsOmitted int                                 `json:"backupsOmitted"`
	// ScheduleReadings words each readable ScheduledBackup's schedule as the
	// operator reads it, keyed "namespace/name"; a schedule the operator
	// cannot parse has no entry.
	ScheduleReadings map[string]string `json:"scheduleReadings,omitempty"`
	// ManagedBy is the GitOps or Helm manager each returned CNPG object's
	// labels and annotations name, keyed "Kind/namespace/name"; objects with
	// no such signal have no entry. Instance Pods are not included.
	ManagedBy map[string]topology.ResourceRef `json:"managedBy,omitempty"`
	// JobPods are the Pods of Jobs a returned Cluster controls (initdb, join,
	// restore…), kept apart from its instance Pods. JobCoverage says where the
	// caller's Jobs were read: a Job Pod is returned only there.
	JobPods     []any                     `json:"jobPods,omitempty"`
	JobCoverage *integration.KindCoverage `json:"jobCoverage,omitempty"`
}

func newCNPGWorkspaceResponse(namespaces []string, contextName string) CNPGWorkspaceResponse {
	resp := CNPGWorkspaceResponse{
		Context:    contextName,
		Namespaces: namespaces,
		Coverage:   map[string]integration.KindCoverage{},
		Objects:    map[string][]any{},
		Issues:     []CNPGWorkspaceIssue{},
		Audit:      []CNPGWorkspaceAuditFinding{},
	}
	for _, k := range workspaceKinds {
		resp.Coverage[k.Key] = integration.KindCoverage{State: integration.KindCoverageNotInstalled}
		resp.Objects[k.Key] = []any{}
	}
	resp.Coverage[workspacePodsKey] = integration.KindCoverage{State: integration.KindCoverageNotInstalled}
	resp.Objects[workspacePodsKey] = []any{}
	return resp
}

func (s *Reader) workspaceReadKind(ctx context.Context, cache *k8s.ResourceCache, k integration.WorkspaceKind, namespaces []string) (integration.KindAccess, []*unstructured.Unstructured) {
	return s.Observations.WorkspaceRead(ctx, cache, k, namespaces, groups)
}

// filterCNPGGroup drops anything whose apiVersion is not a CNPG group, so a
// Velero Backup or a CAPI Cluster can never ride along on a kind-name match.
func filterCNPGGroup(items []*unstructured.Unstructured, err error) ([]*unstructured.Unstructured, error) {
	if err != nil {
		return nil, err
	}
	return integration.KeepGroups(items, groups), nil
}

func sortCNPGObjects(items []*unstructured.Unstructured) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].GetNamespace() != items[j].GetNamespace() {
			return items[i].GetNamespace() < items[j].GetNamespace()
		}
		return items[i].GetName() < items[j].GetName()
	})
}

func backupTime(u *unstructured.Unstructured) time.Time {
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
		if cur, ok := newestCompleted[key]; !ok || backupTime(u).After(backupTime(cur)) {
			newestCompleted[key] = u
		}
	}
	keepNewest := make(map[*unstructured.Unstructured]bool, len(newestCompleted))
	for _, u := range newestCompleted {
		keepNewest[u] = true
	}

	cutoff := now.Add(-backupWindow)
	kept := make([]*unstructured.Unstructured, 0, len(items))
	omitted := 0
	for _, u := range items {
		phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")
		settled := phase == "completed" || phase == "failed"
		if !settled || keepNewest[u] || !backupTime(u).Before(cutoff) {
			kept = append(kept, u)
			continue
		}
		omitted++
	}
	sort.SliceStable(kept, func(i, j int) bool {
		if kept[i].GetNamespace() != kept[j].GetNamespace() {
			return kept[i].GetNamespace() < kept[j].GetNamespace()
		}
		ti, tj := backupTime(kept[i]), backupTime(kept[j])
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return kept[i].GetName() < kept[j].GetName()
	})
	return kept, omitted
}

type WorkspacePodMeta struct {
	Name              string                  `json:"name"`
	Namespace         string                  `json:"namespace"`
	UID               types.UID               `json:"uid"`
	Labels            map[string]string       `json:"labels,omitempty"`
	OwnerReferences   []metav1.OwnerReference `json:"ownerReferences,omitempty"`
	CreationTimestamp metav1.Time             `json:"creationTimestamp"`
}

type WorkspaceContainerStatus struct {
	Name         string                `json:"name"`
	Ready        bool                  `json:"ready"`
	RestartCount int32                 `json:"restartCount"`
	State        corev1.ContainerState `json:"state"`
}

type WorkspacePod struct {
	APIVersion string           `json:"apiVersion"`
	Kind       string           `json:"kind"`
	Metadata   WorkspacePodMeta `json:"metadata"`
	Spec       struct {
		NodeName string `json:"nodeName,omitempty"`
	} `json:"spec"`
	Status struct {
		Phase             corev1.PodPhase            `json:"phase,omitempty"`
		PodIP             string                     `json:"podIP,omitempty"`
		StartTime         *metav1.Time               `json:"startTime,omitempty"`
		Conditions        []corev1.PodCondition      `json:"conditions,omitempty"`
		ContainerStatuses []WorkspaceContainerStatus `json:"containerStatuses,omitempty"`
	} `json:"status"`
}

// isCNPGInstancePod requires the controller ownerReference to name a visible
// Cluster by UID, not just by name: a label alone is something any workload
// can carry, and a Pod left behind by a deleted Cluster must not be attributed
// to a new one created under the same name. clusterUIDs is keyed ns/name.
func isCNPGInstancePod(p *corev1.Pod, clusterUIDs map[string]types.UID) bool {
	name := p.Labels["cnpg.io/cluster"]
	return cnpg.IsInstancePod(p, p.Namespace, name, clusterUIDs[p.Namespace+"/"+name])
}

func clusterUIDs(clusters []*unstructured.Unstructured) map[string]types.UID {
	out := make(map[string]types.UID, len(clusters))
	for _, c := range clusters {
		out[c.GetNamespace()+"/"+c.GetName()] = c.GetUID()
	}
	return out
}

func trimCNPGPod(p *corev1.Pod) WorkspacePod {
	out := WorkspacePod{APIVersion: "v1", Kind: "Pod"}
	out.Metadata = WorkspacePodMeta{
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
		out.Status.ContainerStatuses = append(out.Status.ContainerStatuses, WorkspaceContainerStatus{
			Name: cs.Name, Ready: cs.Ready, RestartCount: cs.RestartCount, State: cs.State,
		})
	}
	return out
}

// WorkspaceReadPods returns the instance Pods of visible Clusters and,
// where the caller may also list Jobs, the Pods of the Jobs those Clusters
// control, plus the namespace/name set of what it returned — the only Pods
// whose issues the response may carry.
func (s *Reader) workspaceReadPods(ctx context.Context, cache *k8s.ResourceCache, namespaces []string, clusterUIDs map[string]types.UID) (podAcc integration.KindAccess, out, jobOut []any, jobAcc integration.KindAccess, returned map[string]bool) {
	out, jobOut, returned = []any{}, []any{}, map[string]bool{}
	podAcc, read := s.Observations.TypedScope(ctx, cache, namespaces, "", "pods")
	jobAcc, _ = s.Observations.TypedScope(ctx, cache, namespaces, "batch", "jobs")
	if (jobAcc.State == integration.KindCoverageFull || jobAcc.State == integration.KindCoveragePartial) && (cache.Jobs() == nil || !cache.IsKindReady("jobs")) {
		jobAcc = integration.KindAccess{State: integration.KindCoverageSyncing}
	}
	if podAcc.State == integration.KindCoverageDenied || podAcc.State == integration.KindCoverageUncached {
		return podAcc, out, jobOut, jobAcc, returned
	}
	if cache.Pods() == nil {
		log.Printf("[cnpg] Pod cache unavailable for workspace")
		return integration.KindAccess{State: integration.KindCoverageError}, out, jobOut, jobAcc, returned
	}

	pods := integration.ListPodsScoped(cache.Pods(), read)
	sort.Slice(pods, func(i, j int) bool {
		if pods[i].Namespace != pods[j].Namespace {
			return pods[i].Namespace < pods[j].Namespace
		}
		return pods[i].Name < pods[j].Name
	})
	for _, p := range pods {
		switch {
		case p == nil:
		case isCNPGInstancePod(p, clusterUIDs):
			out = append(out, trimCNPGPod(p))
			returned[p.Namespace+"/"+p.Name] = true
		case jobAcc.Covers(p.Namespace) && isCNPGClusterJobPod(p, clusterUIDs, cache.Jobs()):
			jobOut = append(jobOut, trimCNPGPod(p))
			returned[p.Namespace+"/"+p.Name] = true
		}
	}
	return podAcc, out, jobOut, jobAcc, returned
}

// isCNPGClusterJobPod reports a Pod of a Job its Cluster controls: the Pod's
// controller is that exact Job (name and UID), and the Job's controller is the
// Cluster (name and UID). Labels alone never adopt a Pod, and a Job recreated
// under the same name does not adopt the previous Job's Pods.
func isCNPGClusterJobPod(p *corev1.Pod, clusterUIDs map[string]types.UID, jobs listersbatchv1.JobLister) bool {
	clusterName := p.Labels[clusterLabel]
	if clusterName == "" || p.Labels[jobRoleLabel] == "" || jobs == nil {
		return false
	}
	uid, ok := clusterUIDs[p.Namespace+"/"+clusterName]
	if !ok || uid == "" {
		return false
	}
	ref := controllerRef(p.OwnerReferences)
	if ref == nil || ref.Kind != "Job" {
		return false
	}
	job, err := jobs.Jobs(p.Namespace).Get(ref.Name)
	if err != nil || job == nil {
		return false
	}
	return controlledBy(p.OwnerReferences, "batch", "Job", job.Name, job.UID) &&
		controlledBy(job.OwnerReferences, Group, "Cluster", clusterName, uid)
}

var cnpgWorkspaceKeyByGroupKind = func() map[string]string {
	m := make(map[string]string, len(workspaceKinds))
	for _, k := range workspaceKinds {
		m[k.Group+"/"+k.Kind] = k.Key
	}
	return m
}()

// workspaceIssues runs the same composition /api/issues serves, but reads
// the flat evidence rows: the grouped view folds instance-Pod evidence into
// the owning Cluster's row, which would hand Pod failure detail to a caller
// who may list Clusters but not Pods. A row is kept only when its own subject
// is visible here — a CNPG kind covered in its namespace, or an instance or
// Cluster Job Pod this response returned. IDs are the subject-derived IDs /api/issues uses.
func (s *Reader) workspaceIssues(ctx context.Context, namespaces []string, access map[string]integration.KindAccess, returnedPods map[string]bool) []CNPGWorkspaceIssue {
	out := []CNPGWorkspaceIssue{}
	if integration.NoNamespaceAccess(namespaces) {
		return out
	}
	composed := s.Observations.Issues(ctx, namespaces)
	for _, iss := range composed {
		if !workspaceIssueVisible(iss, access, returnedPods) {
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

func workspaceIssueVisible(iss issues.Issue, access map[string]integration.KindAccess, returnedPods map[string]bool) bool {
	if iss.Group == "" && iss.Kind == "Pod" {
		return access[workspacePodsKey].Covers(iss.Namespace) && returnedPods[iss.Namespace+"/"+iss.Name]
	}
	if iss.Group != Group && iss.Group != barmanGroup {
		return false
	}
	for _, read := range iss.RequiredReads {
		key := ""
		if read.Group == "" && read.Resource == "pods" {
			key = workspacePodsKey
		}
		for _, kind := range workspaceKinds {
			if kind.Group == read.Group && kind.Resource == read.Resource {
				key = kind.Key
				break
			}
		}
		if key == "" || !access[key].Covers(read.Namespace) {
			return false
		}
	}
	key, ok := cnpgWorkspaceKeyByGroupKind[iss.Group+"/"+iss.Kind]

	return ok && access[key].Covers(iss.Namespace)
}

// cnpgWorkspaceAudit reports the declarative-backup posture finding only for
// Clusters whose namespace had its ScheduledBackups read: without that list,
// "no schedule targets this cluster" is an absence nobody established.
func (s *Reader) workspaceAudit(clusters, scheduled []*unstructured.Unstructured, schedAccess integration.KindAccess) []CNPGWorkspaceAuditFinding {
	out := []CNPGWorkspaceAuditFinding{}
	var subjects []*unstructured.Unstructured
	for _, c := range clusters {
		if schedAccess.Covers(c.GetNamespace()) {
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
	results = s.Observations.FilterAudit(results)
	if results == nil {
		return out
	}
	for _, f := range results.Findings {
		if f.CheckID != noDeclarativeBackupCheckID {
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

func scheduleReadings(scheduled []*unstructured.Unstructured) map[string]string {
	out := map[string]string{}
	for _, sb := range scheduled {
		spec, _, _ := unstructured.NestedString(sb.Object, "spec", "schedule")
		if strings.TrimSpace(spec) == "" || len(spec) > scheduleMaxLen {
			continue
		}
		if _, err := cnpg.ParseSchedule(spec); err != nil {
			continue
		}
		if reading := describeCNPGSchedule(spec); reading != "" {
			out[sb.GetNamespace()+"/"+sb.GetName()] = reading
		}
	}
	return out
}

func (s *Reader) Workspace(ctx context.Context, cache *k8s.ResourceCache, namespaces []string, contextName string) CNPGWorkspaceResponse {
	resp := newCNPGWorkspaceResponse(namespaces, contextName)

	disc := s.Observations.Discovery
	if disc != nil {
		for _, k := range workspaceKinds {
			if _, ok := disc.GetGVRWithGroup(k.Kind, k.Group); ok {
				resp.Installed = true
				break
			}
		}
		if !resp.Installed {
			return resp
		}
	}

	access := map[string]integration.KindAccess{}
	items := map[string][]*unstructured.Unstructured{}
	for _, k := range workspaceKinds {
		if disc != nil {
			if _, ok := disc.GetGVRWithGroup(k.Kind, k.Group); !ok {
				access[k.Key] = integration.KindAccess{State: integration.KindCoverageNotInstalled}
				continue
			}
		}
		acc, list := s.workspaceReadKind(ctx, cache, k, namespaces)
		if acc.State != integration.KindCoverageNotInstalled {
			resp.Installed = true
		}
		access[k.Key] = acc
		items[k.Key] = list
		resp.Coverage[k.Key] = acc.Coverage()
	}
	if !resp.Installed {
		return resp
	}

	for _, k := range workspaceKinds {
		list := items[k.Key]
		if k.Key == workspaceBackupsKey {
			var omitted int
			list, omitted = windowCNPGBackups(list, time.Now())
			resp.BackupsOmitted = omitted
		} else {
			sortCNPGObjects(list)
		}
		out := make([]any, 0, len(list))
		for _, u := range list {
			out = append(out, u.Object)
			if ref := topology.ManagedByFromMeta(u); ref != nil {
				if resp.ManagedBy == nil {
					resp.ManagedBy = map[string]topology.ResourceRef{}
				}
				resp.ManagedBy[k.Kind+"/"+u.GetNamespace()+"/"+u.GetName()] = *ref
			}
		}
		resp.Objects[k.Key] = out
	}

	podAccess, pods, jobPods, jobAccess, returnedPods := s.workspaceReadPods(ctx, cache, namespaces, clusterUIDs(items[workspaceClusterKey]))
	jobCov := jobAccess.Coverage()
	resp.JobPods, resp.JobCoverage = jobPods, &jobCov
	access[workspacePodsKey] = podAccess
	resp.Coverage[workspacePodsKey] = podAccess.Coverage()
	resp.Objects[workspacePodsKey] = pods

	resp.Issues = s.workspaceIssues(ctx, namespaces, access, returnedPods)
	resp.Audit = s.workspaceAudit(items[workspaceClusterKey], items[workspaceSchedKey], access[workspaceSchedKey])
	resp.ScheduleReadings = scheduleReadings(items[workspaceSchedKey])

	return resp
}
