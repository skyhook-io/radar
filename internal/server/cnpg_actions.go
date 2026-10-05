package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/skyhook-io/radar/internal/auth"
)

// CloudNativePG write actions. Every write mirrors what `kubectl cnpg` does
// (promote, restart, reload, fence, hibernate, backup) so the operator sees
// exactly the request its own tooling would have made. A confirmation binds
// the facts the dialog showed, not just a resourceVersion: the server re-reads
// the Cluster, compares those facts and refuses with 409 when anything the
// user reviewed has moved. Disruptive writes are never retried automatically.

var (
	cnpgClusterGVR   = schema.GroupVersionResource{Group: cnpgGroup, Version: "v1", Resource: "clusters"}
	cnpgBackupGVR    = schema.GroupVersionResource{Group: cnpgGroup, Version: "v1", Resource: "backups"}
	cnpgScheduleGVR  = schema.GroupVersionResource{Group: cnpgGroup, Version: "v1", Resource: "scheduledbackups"}
	cnpgPoolerGVR    = schema.GroupVersionResource{Group: cnpgGroup, Version: "v1", Resource: "poolers"}
	cnpgDatabaseGVR  = schema.GroupVersionResource{Group: cnpgGroup, Version: "v1", Resource: "databases"}
	cnpgPublGVR      = schema.GroupVersionResource{Group: cnpgGroup, Version: "v1", Resource: "publications"}
	cnpgSubscrGVR    = schema.GroupVersionResource{Group: cnpgGroup, Version: "v1", Resource: "subscriptions"}
	cnpgScheduleRunR = regexp.MustCompile(`-\d{14}$`)
)

const (
	cnpgRestartAnnotation   = "kubectl.kubernetes.io/restartedAt"
	cnpgReloadAnnotation    = "cnpg.io/reloadedAt"
	cnpgHibernateAnnotation = "cnpg.io/hibernation"
	cnpgFencedAnnotation    = "cnpg.io/fencedInstances"
	cnpgRequestedFromAnno   = "radar.skyhook.io/requested-from"
	cnpgAllInstances        = "*"

	cnpgPhaseHealthy        = "Cluster in healthy state"
	cnpgPhaseSwitchover     = "Switchover in progress"
	cnpgPhaseFailover       = "Failing over"
	cnpgPhaseWaitingForUser = "Waiting for user action"
	cnpgPhaseInplaceRestart = "Primary instance is being restarted in-place"
	cnpgInplaceReason       = "Requested by the user"

	// The compact UTC stamp CloudNativePG itself uses for scheduled runs.
	cnpgCompactStamp = "20060102150405"

	// Refusal codes beyond the shared action contract's.
	cnpgCodeAllFenced       = "all_fenced"
	cnpgCodeWebhook         = "operator_webhook_unavailable"
	cnpgCodeAmbiguous       = "outcome_unknown"
	cnpgCodeInvalidSchedule = "invalid_schedule"
)

// CNPGFencedFacts is the parsed cnpg.io/fencedInstances annotation. Raw is the
// annotation exactly as stored ("" when unset) and is what a confirmation binds.
type CNPGFencedFacts struct {
	Raw       string   `json:"raw"`
	All       bool     `json:"all"`
	Instances []string `json:"instances"`
	Malformed bool     `json:"malformed,omitempty"`
}

// CNPGInstanceFact is one instance as the Cluster names it, joined to its Pod
// when the caller can read it. PodUID is empty when the Pod is missing or
// unreadable; PodReadable distinguishes the two.
type CNPGInstanceFact struct {
	Pod         string `json:"pod"`
	PodUID      string `json:"podUID"`
	Role        string `json:"role"`
	Ready       bool   `json:"ready"`
	Healthy     bool   `json:"healthy"`
	Fenced      bool   `json:"fenced"`
	PodReadable bool   `json:"podReadable"`
	PodExists   bool   `json:"podExists"`
}

// CNPGBackupMethodFact is one backup method the Cluster declares. Capability
// is "backup" when the method can take a backup, "unknown" for a plugin that
// has not reported its capabilities, "none" for a plugin that reports it
// cannot.
type CNPGBackupMethodFact struct {
	Method     string `json:"method"`
	PluginName string `json:"pluginName,omitempty"`
	Capability string `json:"capability"`
	Reason     string `json:"reason,omitempty"`
	Deprecated bool   `json:"deprecated,omitempty"`
}

// CNPGClusterFacts is the state the confirm dialog shows and the POST binds.
type CNPGClusterFacts struct {
	CurrentPrimary  string                 `json:"currentPrimary"`
	TargetPrimary   string                 `json:"targetPrimary"`
	Phase           string                 `json:"phase"`
	PhaseReason     string                 `json:"phaseReason,omitempty"`
	Hibernation     string                 `json:"hibernation"`
	Hibernated      bool                   `json:"hibernated"`
	FencedInstances CNPGFencedFacts        `json:"fencedInstances"`
	Instances       []CNPGInstanceFact     `json:"instances"`
	BackupMethods   []CNPGBackupMethodFact `json:"backupMethods"`
	BackupTarget    string                 `json:"backupTarget,omitempty"`
	// ArchivingFailing: the ContinuousArchiving condition is False. An
	// in-tree Barman backup then ends in phase walArchivingFailing.
	ArchivingFailing bool                 `json:"archivingFailing"`
	IsReplicaCluster bool                 `json:"isReplicaCluster"`
	Terminating      bool                 `json:"terminating"`
	Maintenance      CNPGMaintenanceFacts `json:"maintenance"`
	// ColdSnapshotBackup names a running offline volume-snapshot Backup of
	// the cluster: the operator fenced its instance and lifts the fence itself.
	ColdSnapshotBackup string `json:"coldSnapshotBackup,omitempty"`
}

type CNPGClusterActions struct {
	Backup          ActionCapability `json:"backup"`
	Switchover      ActionCapability `json:"switchover"`
	Restart         ActionCapability `json:"restart"`
	RestartInstance ActionCapability `json:"restartInstance"`
	Reload          ActionCapability `json:"reload"`
	Fence           ActionCapability `json:"fence"`
	Unfence         ActionCapability `json:"unfence"`
	Hibernate       ActionCapability `json:"hibernate"`
	Rehydrate       ActionCapability `json:"rehydrate"`
	// Psql is opening psql on the primary; DestroyInstance is the first
	// instance that may be destroyed (per-instance verdicts are authoritative).
	Psql            ActionCapability `json:"psql"`
	DestroyInstance ActionCapability `json:"destroyInstance"`
	// Restore is creating a new Cluster in this namespace that bootstraps
	// from this one's backups; the source is only read.
	Restore ActionCapability `json:"restore"`
	CNPGMaintenanceActions
}

// CNPGInstanceActions is the per-row verdict for actions that name one
// instance: a primary restarts in place (status write) where a standby's Pod
// is deleted, so the two carry different guards and grants.
type CNPGInstanceActions struct {
	Restart          ActionCapability `json:"restart"`
	SwitchoverTarget ActionCapability `json:"switchoverTarget"`
	Fence            ActionCapability `json:"fence"`
	Unfence          ActionCapability `json:"unfence"`
	Psql             ActionCapability `json:"psql"`
	Destroy          ActionCapability `json:"destroy"`
}

// CNPGRestartStep is one instance's fate in a cluster restart, in the order
// the operator works through them: standbys first, the primary last.
type CNPGRestartStep struct {
	Instance string `json:"instance"`
	Role     string `json:"role"`
	// Effect: recreate | skipped_fenced | switchover | restart | wait_for_user | restart_only_instance
	Effect string `json:"effect"`
}

type CNPGRestartPlan struct {
	PrimaryUpdateStrategy string            `json:"primaryUpdateStrategy"`
	PrimaryUpdateMethod   string            `json:"primaryUpdateMethod"`
	Steps                 []CNPGRestartStep `json:"steps"`
}

// CNPGEffectList names the objects a hibernation touches. Available is false
// when they could not be read; an unreadable list is not an empty one.
type CNPGEffectList struct {
	Available bool     `json:"available"`
	Reason    string   `json:"reason,omitempty"`
	Names     []string `json:"names"`
}

type CNPGVolumeFact struct {
	Name      string `json:"name"`
	Instance  string `json:"instance,omitempty"`
	Role      string `json:"role,omitempty"`
	Capacity  string `json:"capacity,omitempty"`
	Requested string `json:"requested,omitempty"`
}

type CNPGVolumeEffect struct {
	Available bool             `json:"available"`
	Reason    string           `json:"reason,omitempty"`
	Items     []CNPGVolumeFact `json:"items"`
}

type CNPGHibernateEffects struct {
	Poolers                     CNPGEffectList   `json:"poolers"`
	UnsuspendedScheduledBackups CNPGEffectList   `json:"unsuspendedScheduledBackups"`
	Databases                   CNPGEffectList   `json:"databases"`
	Publications                CNPGEffectList   `json:"publications"`
	Subscriptions               CNPGEffectList   `json:"subscriptions"`
	Volumes                     CNPGVolumeEffect `json:"volumes"`
}

// CNPGClusterCapabilitiesResponse is GET /api/cnpg/clusters/{ns}/{name}/capabilities.
type CNPGClusterCapabilitiesResponse struct {
	UID              string                         `json:"uid"`
	ResourceVersion  string                         `json:"resourceVersion"`
	Context          string                         `json:"context"`
	Facts            CNPGClusterFacts               `json:"facts"`
	Actions          CNPGClusterActions             `json:"actions"`
	InstanceActions  map[string]CNPGInstanceActions `json:"instanceActions"`
	RestartPlan      CNPGRestartPlan                `json:"restartPlan"`
	HibernateEffects CNPGHibernateEffects           `json:"hibernateEffects"`
	// Operator is whether the operator watching this namespace is acting on
	// it. Writes to the Cluster or new Backups are refused above when its
	// webhook rejects them; otherwise the dialogs warn.
	Operator CNPGOperatorVerdict `json:"operator"`
}

type CNPGScheduleFacts struct {
	// Generation binds "run" to the settings the user reviewed: every spec
	// change bumps it.
	Generation       int64  `json:"generation"`
	Cluster          string `json:"cluster"`
	Suspended        bool   `json:"suspended"`
	NextScheduleTime string `json:"nextScheduleTime,omitempty"`
	Method           string `json:"method,omitempty"`
	PluginName       string `json:"pluginName,omitempty"`
	Target           string `json:"target,omitempty"`
	// ClusterState: ok | missing | hibernated | unreadable
	ClusterState        string `json:"clusterState"`
	BackupBlockedReason string `json:"backupBlockedReason,omitempty"`
	Terminating         bool   `json:"terminating"`
	// CatchUp is set when resuming would create one backup right away: the
	// next run the operator computed is already in the past.
	CatchUp bool `json:"catchUp"`
	// Schedule is spec.schedule verbatim; setSchedule binds it.
	Schedule string `json:"schedule"`
	// Preview explains Schedule and when the operator runs it next.
	Preview CNPGSchedulePreview `json:"preview"`
}

type CNPGScheduleActions struct {
	Suspend     ActionCapability `json:"suspend"`
	Resume      ActionCapability `json:"resume"`
	Run         ActionCapability `json:"run"`
	SetSchedule ActionCapability `json:"setSchedule"`
}

// CNPGScheduleCapabilitiesResponse is GET /api/cnpg/scheduledbackups/{ns}/{name}/capabilities.
type CNPGScheduleCapabilitiesResponse struct {
	UID             string              `json:"uid"`
	ResourceVersion string              `json:"resourceVersion"`
	Context         string              `json:"context"`
	Facts           CNPGScheduleFacts   `json:"facts"`
	Actions         CNPGScheduleActions `json:"actions"`
	Operator        CNPGOperatorVerdict `json:"operator"`
}

// cnpgReviewedFacts is the subset of facts a confirmation can bind. Decoded
// leniently: a client may echo the whole facts object it was served.
type cnpgReviewedFacts struct {
	CurrentPrimary  *string `json:"currentPrimary"`
	TargetPrimary   *string `json:"targetPrimary"`
	Hibernation     *string `json:"hibernation"`
	BackupTarget    *string `json:"backupTarget"`
	FencedInstances *struct {
		Raw *string `json:"raw"`
	} `json:"fencedInstances"`
	Suspended   *bool                 `json:"suspended"`
	Schedule    *string               `json:"schedule"`
	Generation  *int64                `json:"generation"`
	Maintenance *CNPGMaintenanceFacts `json:"maintenance"`
}

// CNPGActionResult is a successful action's answer. Requested, not completed:
// the operator acts on the write asynchronously, and the UI observes the
// outcome in status.
type CNPGActionResult struct {
	Action  string `json:"action"`
	Message string `json:"message"`
	// Backup is the name of the Backup created by backup / run.
	Backup string `json:"backup,omitempty"`
	// ResolvedAfterTimeout: the create timed out and the Backup was found by
	// re-reading the same name. Never a second create.
	ResolvedAfterTimeout bool `json:"resolvedAfterTimeout,omitempty"`
	CatchUp              bool `json:"catchUp,omitempty"`
	// Target identifies what the action acted on, so a caller can follow the
	// outcome (the backend signalled, the instance destroyed, the Pooler paused).
	Target *CNPGActionTarget `json:"target,omitempty"`
}

type cnpgActionClients struct {
	dyn   dynamic.Interface
	typed kubernetes.Interface
	now   func() time.Time
	// exec runs a command in a Pod as the caller; nil when unavailable.
	exec cnpgExecFunc
}

func (c cnpgActionClients) clock() time.Time {
	if c.now != nil {
		return c.now().UTC()
	}
	return time.Now().UTC()
}

// ---------- facts ----------

func parseCNPGFenced(raw string) CNPGFencedFacts {
	out := CNPGFencedFacts{Raw: raw, Instances: []string{}}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	var names []string
	// "null" decodes without error but is not a list the operator accepts.
	if err := json.Unmarshal([]byte(raw), &names); err != nil || names == nil {
		out.Malformed = true
		return out
	}
	seen := map[string]bool{}
	for _, n := range names {
		if n == cnpgAllInstances {
			out.All = true
		}
		if !seen[n] {
			seen[n] = true
			out.Instances = append(out.Instances, n)
		}
	}
	sort.Strings(out.Instances)
	return out
}

func (f CNPGFencedFacts) fences(instance string) bool {
	if f.Malformed {
		return false
	}
	if f.All {
		return true
	}
	for _, n := range f.Instances {
		if n == instance {
			return true
		}
	}
	return false
}

func cnpgIsReplicaCluster(cluster *unstructured.Unstructured) bool {
	replica, ok, _ := unstructured.NestedMap(cluster.Object, "spec", "replica")
	if !ok || replica == nil {
		return false
	}
	if enabled, ok := replica["enabled"].(bool); ok {
		return enabled
	}
	self, _ := replica["self"].(string)
	if strings.TrimSpace(self) == "" {
		self = cluster.GetName()
	}
	// The operator's Cluster.IsReplica(): without `enabled`, the cluster is a
	// replica unless it names itself as the primary (an unset primary included).
	primary, _ := replica["primary"].(string)
	return strings.TrimSpace(primary) != strings.TrimSpace(self)
}

func cnpgBackupMethods(cluster *unstructured.Unstructured) []CNPGBackupMethodFact {
	type pluginStatus struct {
		reported bool
		backup   bool
	}
	statuses := map[string]pluginStatus{}
	if list, ok, _ := unstructured.NestedSlice(cluster.Object, "status", "pluginStatus"); ok {
		for _, item := range list {
			m, _ := item.(map[string]any)
			name, _ := m["name"].(string)
			raw, present := m["backupCapabilities"]
			if !present {
				continue
			}
			caps, _ := raw.([]any)
			statuses[name] = pluginStatus{reported: true, backup: len(caps) > 0}
		}
	}
	type declared struct {
		name     string
		archiver bool
	}
	var plugins []declared
	if list, ok, _ := unstructured.NestedSlice(cluster.Object, "spec", "plugins"); ok {
		for _, item := range list {
			m, _ := item.(map[string]any)
			name, _ := m["name"].(string)
			if name == "" {
				continue
			}
			if enabled, ok := m["enabled"].(bool); ok && !enabled {
				continue
			}
			archiver, _ := m["isWALArchiver"].(bool)
			plugins = append(plugins, declared{name: name, archiver: archiver})
		}
	}
	sort.SliceStable(plugins, func(i, j int) bool { return plugins[i].archiver && !plugins[j].archiver })

	out := []CNPGBackupMethodFact{}
	for _, p := range plugins {
		fact := CNPGBackupMethodFact{Method: "plugin", PluginName: p.name}
		st := statuses[p.name]
		switch {
		case !st.reported:
			fact.Capability = "unknown"
			fact.Reason = "The plugin does not report whether it can take backups (older plugin versions omit this); the operator rejects the Backup if it cannot"
		case st.backup:
			fact.Capability = "backup"
		default:
			fact.Capability = "none"
			fact.Reason = "The plugin reports no backup capability"
		}
		out = append(out, fact)
	}
	if v, ok, _ := unstructured.NestedFieldNoCopy(cluster.Object, "spec", "backup", "volumeSnapshot"); ok && v != nil {
		out = append(out, CNPGBackupMethodFact{Method: "volumeSnapshot", Capability: "backup"})
	}
	if v, ok, _ := unstructured.NestedFieldNoCopy(cluster.Object, "spec", "backup", "barmanObjectStore"); ok && v != nil {
		out = append(out, CNPGBackupMethodFact{Method: "barmanObjectStore", Capability: "backup", Deprecated: true})
	}
	for i := range out {
		if reason := cnpgBackupDestinationGuard(cluster, out[i].Method, out[i].PluginName, nil); reason != "" {
			out[i].Capability, out[i].Reason = "none", reason
		}
	}
	return out
}

func cnpgBackupDestinationGuard(cluster *unstructured.Unstructured, method, pluginName string, parameters map[string]string) string {
	if method == "" {
		method = "barmanObjectStore"
	}
	missing := "Configure a backup destination on " + cluster.GetName() + " first"
	path, _, _ := unstructured.NestedString(cluster.Object, "spec", "backup", "barmanObjectStore", "destinationPath")
	snapshot, _, _ := unstructured.NestedFieldNoCopy(cluster.Object, "spec", "backup", "volumeSnapshot")
	plugins, _, _ := unstructured.NestedSlice(cluster.Object, "spec", "plugins")
	if path != "" || snapshot != nil {
		missing = "No " + method + " destination on " + cluster.GetName() + ". Configure this method or choose the cluster's configured backup method"
	}
	for _, raw := range plugins {
		p, _ := raw.(map[string]any)
		name, _ := p["name"].(string)
		cfg, _ := p["parameters"].(map[string]any)
		if p["enabled"] == false || name == "" || name == "barman-cloud.cloudnative-pg.io" && (cfg["barmanObjectName"] == nil || cfg["barmanObjectName"] == "") {
			continue
		}
		missing = "No " + method + " destination on " + cluster.GetName() + ". Use method plugin with pluginConfiguration.name " + name + ", or configure this method"
		break
	}
	switch method {
	case "barmanObjectStore":
		if path == "" {
			return missing
		}
	case "volumeSnapshot":
		if snapshot == nil {
			return missing
		}
	case "plugin":
		for _, raw := range plugins {
			p, _ := raw.(map[string]any)
			if p["name"] != pluginName || p["enabled"] == false {
				continue
			}
			if pluginName == "barman-cloud.cloudnative-pg.io" {
				cfg, _ := p["parameters"].(map[string]any)
				if cfg["barmanObjectName"] == "" || cfg["barmanObjectName"] == nil {
					if parameters["barmanObjectName"] == "" {
						return missing
					}
				}
			}
			return ""
		}
		return missing
	}
	return ""
}

func cnpgActionPodReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// cnpgClusterFactsOf builds the facts. typed may be nil, in which case the
// instances carry no Pod join (PodReadable false).
func cnpgClusterFactsOf(ctx context.Context, typed kubernetes.Interface, cluster *unstructured.Unstructured) (CNPGClusterFacts, map[string]*corev1.Pod) {
	str := func(fields ...string) string {
		v, _, _ := unstructured.NestedString(cluster.Object, fields...)
		return v
	}
	anno := cluster.GetAnnotations()
	facts := CNPGClusterFacts{
		CurrentPrimary:   str("status", "currentPrimary"),
		TargetPrimary:    str("status", "targetPrimary"),
		Phase:            str("status", "phase"),
		PhaseReason:      str("status", "phaseReason"),
		Hibernation:      anno[cnpgHibernateAnnotation],
		FencedInstances:  parseCNPGFenced(anno[cnpgFencedAnnotation]),
		BackupMethods:    cnpgBackupMethods(cluster),
		BackupTarget:     str("spec", "backup", "target"),
		IsReplicaCluster: cnpgIsReplicaCluster(cluster),
		Terminating:      !cluster.GetDeletionTimestamp().IsZero(),
		Instances:        []CNPGInstanceFact{},
		Maintenance:      cnpgMaintenanceFactsOf(cluster),
	}
	facts.Hibernated = facts.Hibernation == "on"
	conds, _, _ := unstructured.NestedSlice(cluster.Object, "status", "conditions")
	for _, raw := range conds {
		if c, ok := raw.(map[string]any); ok && c["type"] == "ContinuousArchiving" {
			facts.ArchivingFailing = c["status"] == "False"
		}
	}

	names, _, _ := unstructured.NestedStringSlice(cluster.Object, "status", "instanceNames")
	if len(names) == 0 && facts.CurrentPrimary != "" {
		names = []string{facts.CurrentPrimary}
	}
	healthy := map[string]bool{}
	if list, ok, _ := unstructured.NestedStringSlice(cluster.Object, "status", "instancesStatus", "healthy"); ok {
		for _, n := range list {
			healthy[n] = true
		}
	}
	uids := map[string]types.UID{cluster.GetNamespace() + "/" + cluster.GetName(): cluster.GetUID()}
	pods := map[string]*corev1.Pod{}
	for _, n := range names {
		inst := CNPGInstanceFact{Pod: n, Role: "standby", Healthy: healthy[n], Fenced: facts.FencedInstances.fences(n)}
		if n == facts.CurrentPrimary {
			inst.Role = "primary"
		}
		if typed != nil {
			pod, err := typed.CoreV1().Pods(cluster.GetNamespace()).Get(ctx, n, metav1.GetOptions{})
			switch {
			case err == nil:
				inst.PodReadable = true
				// A Pod of this name that the Cluster does not control is not
				// this instance; treat it like no Pod at all.
				if isCNPGInstancePod(pod, uids) {
					inst.PodExists = true
					inst.PodUID = string(pod.UID)
					inst.Ready = cnpgActionPodReady(pod)
					pods[n] = pod
				}
			case apierrors.IsNotFound(err):
				inst.PodReadable = true
			}
		}
		facts.Instances = append(facts.Instances, inst)
	}
	return facts, pods
}

func (f CNPGClusterFacts) instance(name string) (CNPGInstanceFact, bool) {
	for _, i := range f.Instances {
		if i.Pod == name {
			return i, true
		}
	}
	return CNPGInstanceFact{}, false
}

func (f CNPGClusterFacts) switchoverInFlight() string {
	if f.TargetPrimary != "" && f.CurrentPrimary != "" && f.TargetPrimary != f.CurrentPrimary {
		return f.TargetPrimary
	}
	return ""
}

// ---------- guards ----------
// Each returns "" when the action may proceed, or the reason it may not.
// Guards are per action on purpose: fencing, restarting a broken standby and
// rehydrating must stay available during an incident.

func cnpgGuardCommon(f CNPGClusterFacts) string {
	if f.Terminating {
		return "The cluster is being deleted"
	}
	return ""
}

func cnpgGuardBackup(f CNPGClusterFacts) string {
	if r := cnpgGuardCommon(f); r != "" {
		return r
	}
	if f.Hibernated {
		return "The cluster is hibernated: the operator fails a backup requested now"
	}
	for _, m := range f.BackupMethods {
		if m.Capability != "none" {
			return ""
		}
	}
	if len(f.BackupMethods) == 0 {
		return "Configure a backup destination first: the cluster declares no backup plugin or snapshot configuration"
	}
	for _, m := range f.BackupMethods {
		if m.Reason != "" {
			return m.Reason
		}
	}
	return "No declared backup method can take a backup"
}

func cnpgGuardSwitchover(f CNPGClusterFacts) string {
	if r := cnpgGuardCommon(f); r != "" {
		return r
	}
	if f.Hibernated {
		return "The cluster is hibernated: there is no primary to move"
	}
	if f.IsReplicaCluster {
		return "This cluster follows another one: a switchover here would only move the designated primary"
	}
	if t := f.switchoverInFlight(); t != "" {
		return "A switchover or failover is already in flight, to " + t
	}
	if f.Phase == cnpgPhaseSwitchover || f.Phase == cnpgPhaseFailover {
		return "A switchover or failover is already in flight"
	}
	if f.CurrentPrimary == "" {
		return "The cluster reports no primary yet"
	}
	for _, i := range f.Instances {
		if cnpgGuardSwitchoverTarget(f, i) == "" {
			return ""
		}
	}
	return "No standby can be promoted right now"
}

func cnpgGuardSwitchoverTarget(f CNPGClusterFacts, i CNPGInstanceFact) string {
	switch {
	case i.Pod == f.CurrentPrimary:
		return "It is the primary"
	case !i.PodReadable:
		return "Its Pod cannot be read"
	case !i.PodExists:
		return "Its Pod does not exist"
	case i.Fenced:
		return "It is fenced"
	case !i.Ready:
		return "Its Pod is not ready"
	}
	return ""
}

func cnpgGuardRestart(f CNPGClusterFacts) string {
	if r := cnpgGuardCommon(f); r != "" {
		return r
	}
	if f.Hibernated {
		return "The cluster is hibernated: there is no instance to restart"
	}
	if t := f.switchoverInFlight(); t != "" {
		return "A switchover or failover is in flight, to " + t
	}
	return ""
}

func cnpgGuardRestartInstance(f CNPGClusterFacts, i CNPGInstanceFact) string {
	if r := cnpgGuardCommon(f); r != "" {
		return r
	}
	if f.Hibernated {
		return "The cluster is hibernated: there is no instance to restart"
	}
	if i.Fenced {
		return "It is fenced: lift the fence first"
	}
	if i.Pod == f.CurrentPrimary {
		if t := f.switchoverInFlight(); t != "" {
			return "A switchover or failover is in flight, to " + t
		}
		if f.Phase != cnpgPhaseHealthy && f.Phase != cnpgPhaseWaitingForUser {
			return fmt.Sprintf("The primary restarts in place only on a healthy cluster or one waiting for user action; the phase is %q", f.Phase)
		}
		return ""
	}
	if i.PodReadable && !i.PodExists {
		return "Its Pod does not exist"
	}
	return ""
}

func cnpgGuardReload(f CNPGClusterFacts) string {
	if r := cnpgGuardCommon(f); r != "" {
		return r
	}
	if f.Hibernated {
		return "The cluster is hibernated: there is no instance to reload"
	}
	return ""
}

func cnpgGuardFence(f CNPGClusterFacts) string {
	if r := cnpgGuardCommon(f); r != "" {
		return r
	}
	if f.FencedInstances.Malformed {
		return "The cnpg.io/fencedInstances annotation does not parse as a JSON array of names; fix it before fencing"
	}
	if f.Hibernated {
		return "The cluster is hibernated: there is no PostgreSQL to stop"
	}
	if f.FencedInstances.All {
		return "Every instance is fenced already"
	}
	return ""
}

func cnpgGuardUnfence(f CNPGClusterFacts) string {
	if r := cnpgGuardCommon(f); r != "" {
		return r
	}
	if f.FencedInstances.Malformed {
		return "The cnpg.io/fencedInstances annotation does not parse as a JSON array of names; fix it before lifting a fence"
	}
	if len(f.FencedInstances.Instances) == 0 {
		return "No instance is fenced"
	}
	if f.ColdSnapshotBackup != "" {
		return fmt.Sprintf("Backup %s is a cold snapshot: the operator fenced the instance for it and lifts the fence itself once the snapshot is taken. Lifting it now would start PostgreSQL under the snapshot", f.ColdSnapshotBackup)
	}
	return ""
}

// cnpgRunningColdSnapshot names a running offline (cold) volume-snapshot
// Backup of the cluster, or "" when there is none. A failed list also gives
// "": lifting a fence must stay possible during an incident, and the operator
// still owns its own fence.
func cnpgRunningColdSnapshot(ctx context.Context, dyn dynamic.Interface, cluster *unstructured.Unstructured) string {
	if dyn == nil {
		return ""
	}
	list, err := dyn.Resource(cnpgBackupGVR).Namespace(cluster.GetNamespace()).List(ctx, metav1.ListOptions{})
	if err != nil {
		return ""
	}
	// An unset online follows the Cluster's default, which is online.
	clusterOnline, found, _ := unstructured.NestedBool(cluster.Object, "spec", "backup", "volumeSnapshot", "online")
	if !found {
		clusterOnline = true
	}
	for i := range list.Items {
		b := &list.Items[i]
		if name, _, _ := unstructured.NestedString(b.Object, "spec", "cluster", "name"); name != cluster.GetName() {
			continue
		}
		method, _, _ := unstructured.NestedString(b.Object, "status", "method")
		if method == "" {
			method, _, _ = unstructured.NestedString(b.Object, "spec", "method")
		}
		if method != "volumeSnapshot" {
			continue
		}
		online, found, _ := unstructured.NestedBool(b.Object, "status", "online")
		if !found {
			if online, found, _ = unstructured.NestedBool(b.Object, "spec", "online"); !found {
				online = clusterOnline
			}
		}
		if online {
			continue
		}
		switch phase, _, _ := unstructured.NestedString(b.Object, "status", "phase"); phase {
		case "", "completed", "failed", "walArchivingFailing":
			continue
		}
		return b.GetName()
	}
	return ""
}

func cnpgGuardHibernate(f CNPGClusterFacts) string {
	if r := cnpgGuardCommon(f); r != "" {
		return r
	}
	if f.Hibernated {
		return "The cluster is hibernated already"
	}
	return ""
}

func cnpgGuardRehydrate(f CNPGClusterFacts) string {
	if r := cnpgGuardCommon(f); r != "" {
		return r
	}
	if !f.Hibernated {
		return "The cluster is not hibernated"
	}
	return ""
}

// ---------- permissions ----------

var (
	cnpgGrantCreateBackups    = Grant{Verb: "create", Group: cnpgGroup, Resource: "backups"}
	cnpgGrantCreateClusters   = Grant{Verb: "create", Group: cnpgGroup, Resource: "clusters"}
	cnpgGrantPatchStatus      = Grant{Verb: "patch", Group: cnpgGroup, Resource: "clusters", Subresource: "status"}
	cnpgGrantPatchClusters    = Grant{Verb: "patch", Group: cnpgGroup, Resource: "clusters"}
	cnpgGrantDeletePods       = Grant{Verb: "delete", Resource: "pods"}
	cnpgGrantGetPods          = Grant{Verb: "get", Resource: "pods"}
	cnpgGrantPatchSchedules   = Grant{Verb: "patch", Group: cnpgGroup, Resource: "scheduledbackups"}
	cnpgClusterActionGrants   = map[string]Grant{"backup": cnpgGrantCreateBackups, "switchover": cnpgGrantPatchStatus, "restart": cnpgGrantPatchClusters, "reload": cnpgGrantPatchClusters, "fence": cnpgGrantPatchClusters, "unfence": cnpgGrantPatchClusters, "hibernate": cnpgGrantPatchClusters, "rehydrate": cnpgGrantPatchClusters}
	cnpgScheduleActionGrants  = map[string]Grant{"suspend": cnpgGrantPatchSchedules, "resume": cnpgGrantPatchSchedules, "run": cnpgGrantCreateBackups, "setSchedule": cnpgGrantPatchSchedules}
	cnpgClusterActionsOrdered = []string{"backup", "switchover", "restart", "restartInstance", "reload", "fence", "unfence", "hibernate", "rehydrate", "cancelBackend", "terminateBackend", "destroyInstance"}
)

// ---------- capabilities ----------

func (s *Server) handleCNPGClusterCapabilities(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	dyn, contextName := s.getDynamicClientSnapshotForRequest(r)
	typed := s.getClientForRequest(r)
	if dyn == nil || typed == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	resp, err := s.cnpgClusterCapabilities(r, cnpgActionClients{dyn: dyn, typed: typed}, contextName, namespace, name)
	if err != nil {
		s.writeCNPGActionError(w, err, "capabilities", namespace, name)
		return
	}
	s.writeJSON(w, resp)
}

func (s *Server) cnpgClusterCapabilities(r *http.Request, c cnpgActionClients, contextName, namespace, name string) (*CNPGClusterCapabilitiesResponse, error) {
	ctx := r.Context()
	cluster, err := c.dyn.Resource(cnpgClusterGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	facts, _ := cnpgClusterFactsOf(ctx, c.typed, cluster)
	facts.ColdSnapshotBackup = cnpgRunningColdSnapshot(ctx, c.dyn, cluster)

	perm := map[Grant]string{}
	permOf := func(g Grant) string {
		if v, ok := perm[g]; ok {
			return v
		}
		v := s.grantPermission(r, g.In(namespace))
		perm[g] = v
		return v
	}
	one := func(guard string, gs ...Grant) ActionCapability {
		ps := make([]string, len(gs))
		bound := make([]Grant, len(gs))
		for i, g := range gs {
			ps[i] = permOf(g)
			bound[i] = g.In(namespace)
		}
		return capabilityVerdict(guard, ps, bound)
	}

	instanceActions := map[string]CNPGInstanceActions{}
	// The cluster-level restartInstance verdict is the first allowed row, or
	// the first row's refusal when none is.
	restartInstance := one(cnpgIfNoGuard(cnpgGuardCommon(facts), "The cluster has no instance"), cnpgGrantDeletePods)
	restartAdopted := false
	destroyInstance := one(cnpgIfNoGuard(cnpgGuardCommon(facts), "The cluster has no standby"), cnpgDestroyGrants(false)...)
	destroyAdopted := false
	for _, inst := range facts.Instances {
		grant := cnpgGrantDeletePods
		if inst.Pod == facts.CurrentPrimary {
			grant = cnpgGrantPatchStatus
		}
		restart := one(cnpgGuardRestartInstance(facts, inst), grant)
		if !restartInstance.Allowed && (restart.Allowed || !restartAdopted) {
			restartInstance = restart
			restartAdopted = true
		}
		switchGuard := cnpgGuardSwitchover(facts)
		if tg := cnpgGuardSwitchoverTarget(facts, inst); tg != "" {
			switchGuard = tg
		}
		fenceGuard := cnpgGuardFence(facts)
		if fenceGuard == "" && inst.Fenced {
			fenceGuard = "It is fenced already"
		}
		unfenceGuard := cnpgGuardUnfence(facts)
		if unfenceGuard == "" {
			switch {
			case facts.FencedInstances.All:
				unfenceGuard = `The whole cluster is fenced with ["*"]: lift every fence, or convert it to an explicit list first`
			case !inst.Fenced:
				unfenceGuard = "It is not fenced"
			}
		}
		destroy := one(cnpgGuardDestroyInstance(facts, inst), cnpgDestroyGrants(false)...)
		if !destroyInstance.Allowed && (destroy.Allowed || !destroyAdopted) {
			destroyInstance = destroy
			destroyAdopted = true
		}
		instanceActions[inst.Pod] = CNPGInstanceActions{
			Restart:          restart,
			SwitchoverTarget: one(switchGuard, cnpgGrantPatchStatus, cnpgGrantGetPods),
			Fence:            one(fenceGuard, cnpgGrantPatchClusters),
			Unfence:          one(unfenceGuard, cnpgGrantPatchClusters),
			Psql:             one(cnpgGuardPsql(facts, inst), cnpgGrantCreateExec),
			Destroy:          destroy,
		}
	}
	psql := one(cnpgIfNoGuard(cnpgGuardCommon(facts), "No primary is reported"), cnpgGrantCreateExec)
	if primary, ok := facts.instance(facts.CurrentPrimary); ok {
		psql = instanceActions[primary.Pod].Psql
	}

	resp := &CNPGClusterCapabilitiesResponse{
		UID:             string(cluster.GetUID()),
		ResourceVersion: cluster.GetResourceVersion(),
		Context:         contextName,
		Facts:           facts,
		Actions: CNPGClusterActions{
			Backup:          one(cnpgGuardBackup(facts), cnpgGrantCreateBackups),
			Switchover:      one(cnpgGuardSwitchover(facts), cnpgGrantPatchStatus, cnpgGrantGetPods),
			Restart:         one(cnpgGuardRestart(facts), cnpgGrantPatchClusters),
			RestartInstance: restartInstance,
			Reload:          one(cnpgGuardReload(facts), cnpgGrantPatchClusters),
			Fence:           one(cnpgGuardFence(facts), cnpgGrantPatchClusters),
			Unfence:         one(cnpgGuardUnfence(facts), cnpgGrantPatchClusters),
			Hibernate:       one(cnpgGuardHibernate(facts), cnpgGrantPatchClusters),
			Rehydrate:       one(cnpgGuardRehydrate(facts), cnpgGrantPatchClusters),
			Psql:            psql,
			DestroyInstance: destroyInstance,
			Restore:         s.cnpgRestoreCapability(r, namespace),
			CNPGMaintenanceActions: CNPGMaintenanceActions{
				SetMaintenance:   one(cnpgGuardSetMaintenance(facts), cnpgGrantPatchClusters),
				UnsetMaintenance: one(cnpgGuardUnsetMaintenance(facts), cnpgGrantPatchClusters),
			},
		},
		InstanceActions:  instanceActions,
		RestartPlan:      cnpgRestartPlanOf(cluster, facts),
		HibernateEffects: cnpgHibernateEffectsOf(ctx, c, cluster),
		Operator:         s.cnpgOperatorVerdictFor(r, namespace),
	}
	cnpgApplyOperatorGuard(resp)
	return resp, nil
}

// cnpgApplyOperatorGuard refuses the actions whose write the CNPG admission
// webhook sees — a new Backup, or a patch of the Cluster itself — while that
// webhook rejects writes. Status patches and Pod deletes bypass it.
func cnpgApplyOperatorGuard(resp *CNPGClusterCapabilitiesResponse) {
	v, a := resp.Operator, &resp.Actions
	for _, c := range []*ActionCapability{&a.Backup, &a.Restart, &a.Reload, &a.Fence, &a.Unfence, &a.Hibernate, &a.Rehydrate, &a.SetMaintenance, &a.UnsetMaintenance} {
		*c = cnpgOperatorWebhookGuard(v, *c)
	}
	for pod, ia := range resp.InstanceActions {
		ia.Fence = cnpgOperatorWebhookGuard(v, ia.Fence)
		ia.Unfence = cnpgOperatorWebhookGuard(v, ia.Unfence)
		resp.InstanceActions[pod] = ia
	}
}

// ifNoGuard returns guard when set, fallback otherwise.
func cnpgIfNoGuard(guard, fallback string) string {
	if guard != "" {
		return guard
	}
	return fallback
}

func cnpgRestartPlanOf(cluster *unstructured.Unstructured, f CNPGClusterFacts) CNPGRestartPlan {
	strategy, _, _ := unstructured.NestedString(cluster.Object, "spec", "primaryUpdateStrategy")
	method, _, _ := unstructured.NestedString(cluster.Object, "spec", "primaryUpdateMethod")
	if strategy == "" {
		strategy = "unsupervised"
	}
	if method == "" {
		method = "restart"
	}
	plan := CNPGRestartPlan{PrimaryUpdateStrategy: strategy, PrimaryUpdateMethod: method, Steps: []CNPGRestartStep{}}
	standbys := 0
	for _, i := range f.Instances {
		if i.Pod == f.CurrentPrimary {
			continue
		}
		standbys++
		effect := "recreate"
		if i.Fenced {
			effect = "skipped_fenced"
		}
		plan.Steps = append(plan.Steps, CNPGRestartStep{Instance: i.Pod, Role: "standby", Effect: effect})
	}
	if f.CurrentPrimary == "" {
		return plan
	}
	step := CNPGRestartStep{Instance: f.CurrentPrimary, Role: "primary"}
	switch {
	case f.FencedInstances.fences(f.CurrentPrimary):
		step.Effect = "skipped_fenced"
	case standbys == 0:
		step.Effect = "restart_only_instance"
	case strategy == "supervised":
		step.Effect = "wait_for_user"
	case method == "switchover":
		step.Effect = "switchover"
	default:
		step.Effect = "restart"
	}
	plan.Steps = append(plan.Steps, step)
	return plan
}

func cnpgEffectListOf(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, namespace, cluster string, keep func(*unstructured.Unstructured) bool) CNPGEffectList {
	out := CNPGEffectList{Names: []string{}}
	list, err := dyn.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
	switch {
	case apierrors.IsNotFound(err):
		// The kind is not served (an older operator): none can exist.
		out.Available = true
		return out
	case apierrors.IsForbidden(err):
		out.Reason = "You are not allowed to list " + gvr.Resource
		return out
	case err != nil:
		out.Reason = "Could not list " + gvr.Resource + ": " + err.Error()
		return out
	}
	out.Available = true
	for i := range list.Items {
		item := &list.Items[i]
		ref, _, _ := unstructured.NestedString(item.Object, "spec", "cluster", "name")
		if ref != cluster || (keep != nil && !keep(item)) {
			continue
		}
		out.Names = append(out.Names, item.GetName())
	}
	sort.Strings(out.Names)
	return out
}

func cnpgHibernateEffectsOf(ctx context.Context, c cnpgActionClients, cluster *unstructured.Unstructured) CNPGHibernateEffects {
	ns, name := cluster.GetNamespace(), cluster.GetName()
	eff := CNPGHibernateEffects{
		Poolers: cnpgEffectListOf(ctx, c.dyn, cnpgPoolerGVR, ns, name, nil),
		UnsuspendedScheduledBackups: cnpgEffectListOf(ctx, c.dyn, cnpgScheduleGVR, ns, name, func(u *unstructured.Unstructured) bool {
			suspended, _, _ := unstructured.NestedBool(u.Object, "spec", "suspend")
			return !suspended
		}),
		Databases:     cnpgEffectListOf(ctx, c.dyn, cnpgDatabaseGVR, ns, name, nil),
		Publications:  cnpgEffectListOf(ctx, c.dyn, cnpgPublGVR, ns, name, nil),
		Subscriptions: cnpgEffectListOf(ctx, c.dyn, cnpgSubscrGVR, ns, name, nil),
		Volumes:       CNPGVolumeEffect{Items: []CNPGVolumeFact{}},
	}
	if c.typed == nil {
		eff.Volumes.Reason = "Volumes could not be read"
		return eff
	}
	pvcs, err := c.typed.CoreV1().PersistentVolumeClaims(ns).List(ctx, metav1.ListOptions{LabelSelector: cnpgClusterLabel + "=" + name})
	switch {
	case apierrors.IsForbidden(err):
		eff.Volumes.Reason = "You are not allowed to list persistentvolumeclaims"
		return eff
	case err != nil:
		eff.Volumes.Reason = "Could not list persistentvolumeclaims: " + err.Error()
		return eff
	}
	eff.Volumes.Available = true
	for _, pvc := range pvcs.Items {
		// A label is something any object can carry; an owner naming another
		// Cluster UID disqualifies the claim.
		foreign := false
		for _, ref := range pvc.OwnerReferences {
			if ref.Kind == "Cluster" && ref.UID != cluster.GetUID() {
				foreign = true
			}
		}
		if foreign {
			continue
		}
		v := CNPGVolumeFact{
			Name:     pvc.Name,
			Instance: pvc.Labels[cnpgInstanceNameLabel],
			Role:     pvc.Labels[cnpgPVCRoleLabel],
		}
		if q, ok := pvc.Status.Capacity[corev1.ResourceStorage]; ok {
			v.Capacity = q.String()
		}
		if q, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
			v.Requested = q.String()
		}
		eff.Volumes.Items = append(eff.Volumes.Items, v)
	}
	sort.Slice(eff.Volumes.Items, func(i, j int) bool { return eff.Volumes.Items[i].Name < eff.Volumes.Items[j].Name })
	return eff
}

func (s *Server) handleCNPGScheduleCapabilities(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	dyn, contextName := s.getDynamicClientSnapshotForRequest(r)
	if dyn == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	resp, err := s.cnpgScheduleCapabilities(r, cnpgActionClients{dyn: dyn}, contextName, namespace, name)
	if err != nil {
		s.writeCNPGActionError(w, err, "capabilities", namespace, name)
		return
	}
	s.writeJSON(w, resp)
}

func cnpgScheduleFactsOf(ctx context.Context, c cnpgActionClients, sched *unstructured.Unstructured) CNPGScheduleFacts {
	str := func(fields ...string) string {
		v, _, _ := unstructured.NestedString(sched.Object, fields...)
		return v
	}
	suspended, _, _ := unstructured.NestedBool(sched.Object, "spec", "suspend")
	f := CNPGScheduleFacts{
		Generation:       sched.GetGeneration(),
		Cluster:          str("spec", "cluster", "name"),
		Suspended:        suspended,
		NextScheduleTime: str("status", "nextScheduleTime"),
		Method:           str("spec", "method"),
		PluginName:       str("spec", "pluginConfiguration", "name"),
		Target:           str("spec", "target"),
		Terminating:      !sched.GetDeletionTimestamp().IsZero(),
		Schedule:         str("spec", "schedule"),
	}
	if t, err := time.Parse(time.RFC3339, f.NextScheduleTime); err == nil && !t.After(c.clock()) {
		f.CatchUp = true
	}
	f.Preview = cnpgSchedulePreview(f.Schedule, cnpgScheduleLastCheck(sched), suspended, c.clock())
	switch cluster, err := c.dyn.Resource(cnpgClusterGVR).Namespace(sched.GetNamespace()).Get(ctx, f.Cluster, metav1.GetOptions{}); {
	case f.Cluster == "":
		f.ClusterState = "missing"
	case apierrors.IsNotFound(err):
		f.ClusterState = "missing"
	case err != nil:
		f.ClusterState = "unreadable"
	case cluster.GetAnnotations()[cnpgHibernateAnnotation] == "on":
		f.ClusterState = "hibernated"
	default:
		f.ClusterState = "ok"
		parameters, _, _ := unstructured.NestedStringMap(sched.Object, "spec", "pluginConfiguration", "parameters")
		f.BackupBlockedReason = cnpgBackupDestinationGuard(cluster, f.Method, f.PluginName, parameters)
	}
	return f
}

func cnpgGuardScheduleRun(f CNPGScheduleFacts) string {
	switch {
	case f.Terminating:
		return "The schedule is being deleted"
	case f.Cluster == "":
		return "The schedule names no cluster"
	case f.ClusterState == "missing":
		return "The cluster of the schedule does not exist in this namespace"
	case f.ClusterState == "hibernated":
		return "The cluster is hibernated: the operator fails a backup requested now"
	case f.ClusterState == "unreadable":
		return "The cluster's backup destination could not be read"
	}
	return f.BackupBlockedReason
}

func (s *Server) cnpgScheduleCapabilities(r *http.Request, c cnpgActionClients, contextName, namespace, name string) (*CNPGScheduleCapabilitiesResponse, error) {
	sched, err := c.dyn.Resource(cnpgScheduleGVR).Namespace(namespace).Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	f := cnpgScheduleFactsOf(r.Context(), c, sched)
	one := func(guard string, g Grant) ActionCapability {
		g = g.In(namespace)
		return capabilityVerdict(guard, []string{s.grantPermission(r, g)}, []Grant{g})
	}
	suspendGuard, resumeGuard := "", ""
	if f.Terminating {
		suspendGuard, resumeGuard = "The schedule is being deleted", "The schedule is being deleted"
	} else if f.Suspended {
		suspendGuard = "The schedule is suspended already"
	} else {
		resumeGuard = "The schedule is not suspended"
	}
	operator := s.cnpgOperatorVerdictFor(r, namespace)
	return &CNPGScheduleCapabilitiesResponse{
		UID:             string(sched.GetUID()),
		ResourceVersion: sched.GetResourceVersion(),
		Context:         contextName,
		Facts:           f,
		Actions: CNPGScheduleActions{
			Suspend:     cnpgOperatorWebhookGuard(operator, one(suspendGuard, cnpgGrantPatchSchedules)),
			Resume:      cnpgOperatorWebhookGuard(operator, one(resumeGuard, cnpgGrantPatchSchedules)),
			Run:         cnpgOperatorWebhookGuard(operator, one(cnpgGuardScheduleRun(f), cnpgGrantCreateBackups)),
			SetSchedule: cnpgOperatorWebhookGuard(operator, one(map[bool]string{true: "The schedule is being deleted"}[f.Terminating], cnpgGrantPatchSchedules)),
		},
		Operator: operator,
	}, nil
}

// ---------- POST ----------

func (s *Server) handleCNPGClusterAction(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	action := chi.URLParam(r, "action")
	if _, ok := cnpgClusterActionRunners[action]; !ok {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown CloudNativePG cluster action %q: must be one of %s", action, strings.Join(cnpgClusterActionsOrdered, ", ")))
		return
	}
	req, dyn, ok := s.decodeActionRequest(w, r)
	if !ok {
		return
	}
	auth.AuditLog(r, namespace, name)
	typed := s.getClientForRequest(r)
	if typed == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	res, err := runCNPGClusterAction(r.Context(), cnpgActionClients{dyn: dyn, typed: typed, exec: s.cnpgExecFor(r)}, namespace, name, action, req)
	if err != nil {
		s.writeCNPGActionError(w, err, action, namespace, name)
		return
	}
	log.Printf("[cnpg] %s on Cluster %s/%s requested", action, sanitizeForLog(namespace), sanitizeForLog(name))
	s.writeJSON(w, res)
}

func (s *Server) handleCNPGScheduleAction(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	action := chi.URLParam(r, "action")
	if _, ok := cnpgScheduleActionGrants[action]; !ok {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown ScheduledBackup action %q: must be suspend, resume, run or setSchedule", action))
		return
	}
	req, dyn, ok := s.decodeActionRequest(w, r)
	if !ok {
		return
	}
	auth.AuditLog(r, namespace, name)
	res, err := runCNPGScheduleAction(r.Context(), cnpgActionClients{dyn: dyn}, namespace, name, action, req)
	if err != nil {
		s.writeCNPGActionError(w, err, action, namespace, name)
		return
	}
	log.Printf("[cnpg] %s on ScheduledBackup %s/%s requested", action, sanitizeForLog(namespace), sanitizeForLog(name))
	s.writeJSON(w, res)
}

type cnpgClusterRun struct {
	c        cnpgActionClients
	cluster  *unstructured.Unstructured
	facts    CNPGClusterFacts
	reviewed cnpgReviewedFacts
	pods     map[string]*corev1.Pod
	params   json.RawMessage
}

type cnpgClusterRunner struct {
	// binds lists the reviewed facts this action requires and compares.
	binds []string
	// needsPods: the action reads instance Pods to validate its target.
	needsPods bool
	run       func(ctx context.Context, x *cnpgClusterRun) (*CNPGActionResult, error)
}

var cnpgClusterActionRunners = map[string]cnpgClusterRunner{
	"backup":           {binds: []string{"hibernation"}, run: cnpgRunBackup},
	"switchover":       {binds: []string{"currentPrimary", "targetPrimary", "fencedInstances"}, needsPods: true, run: cnpgRunSwitchover},
	"restart":          {binds: []string{"currentPrimary", "targetPrimary", "hibernation", "fencedInstances"}, run: cnpgRunRestart},
	"restartInstance":  {binds: []string{"currentPrimary", "targetPrimary", "fencedInstances"}, needsPods: true, run: cnpgRunRestartInstance},
	"reload":           {binds: []string{"hibernation"}, run: cnpgRunReload},
	"fence":            {binds: []string{"currentPrimary", "hibernation", "fencedInstances"}, run: cnpgRunFence},
	"unfence":          {binds: []string{"currentPrimary", "fencedInstances"}, run: cnpgRunUnfence},
	"hibernate":        {binds: []string{"hibernation"}, run: cnpgRunHibernation("on")},
	"rehydrate":        {binds: []string{"hibernation"}, run: cnpgRunHibernation("off")},
	"cancelBackend":    {needsPods: true, run: cnpgRunSignalBackend(cnpgSignalCancel)},
	"terminateBackend": {needsPods: true, run: cnpgRunSignalBackend(cnpgSignalTerminate)},
	"destroyInstance":  {binds: []string{"currentPrimary", "targetPrimary", "fencedInstances"}, needsPods: true, run: cnpgRunDestroyInstance},
}

func decodeCNPGReviewedFacts(raw json.RawMessage) (cnpgReviewedFacts, error) {
	var f cnpgReviewedFacts
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return f, nil
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return f, fmt.Errorf("facts: %w", err)
	}
	return f, nil
}

// cnpgFactsDiffer compares the reviewed facts an action binds with the facts
// now. A bound fact missing from the request is a malformed request.
func cnpgFactsDiffer(binds []string, reviewed cnpgReviewedFacts, now CNPGClusterFacts) (changed []string, missing string) {
	for _, b := range binds {
		var got *string
		var want string
		switch b {
		case "currentPrimary":
			got, want = reviewed.CurrentPrimary, now.CurrentPrimary
		case "targetPrimary":
			got, want = reviewed.TargetPrimary, now.TargetPrimary
		case "hibernation":
			got, want = reviewed.Hibernation, now.Hibernation
		case "maintenance":
			if reviewed.Maintenance == nil {
				return nil, b
			}
			if *reviewed.Maintenance != now.Maintenance {
				changed = append(changed, b)
			}
			continue
		case "fencedInstances":
			b = "fencedInstances.raw"
			if reviewed.FencedInstances != nil {
				got = reviewed.FencedInstances.Raw
			}
			want = now.FencedInstances.Raw
		}
		if got == nil {
			return nil, b
		}
		if *got != want {
			changed = append(changed, b)
		}
	}
	return changed, ""
}

func runCNPGClusterAction(ctx context.Context, c cnpgActionClients, namespace, name, action string, req ActionRequest) (*CNPGActionResult, error) {
	runner, ok := cnpgClusterActionRunners[action]
	if !ok {
		return nil, refuseAction(http.StatusBadRequest, "", "unknown action %q", action)
	}
	reviewed, err := decodeCNPGReviewedFacts(req.Facts)
	if err != nil {
		return nil, refuseAction(http.StatusBadRequest, "", "%v", err)
	}
	cluster, err := c.dyn.Resource(cnpgClusterGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	typed := c.typed
	if !runner.needsPods {
		typed = nil
	}
	facts, pods := cnpgClusterFactsOf(ctx, typed, cluster)
	facts.ColdSnapshotBackup = cnpgRunningColdSnapshot(ctx, c.dyn, cluster)
	if string(cluster.GetUID()) != req.UID {
		return nil, changedAction(facts, "Cluster %s/%s was deleted and recreated since you reviewed it", namespace, name)
	}
	changed, missing := cnpgFactsDiffer(runner.binds, reviewed, facts)
	if missing != "" {
		return nil, refuseAction(http.StatusBadRequest, "", "facts.%s is required for %s: the confirmation must bind what the dialog showed", missing, action)
	}
	if len(changed) > 0 {
		return nil, changedAction(facts, "Cluster %s/%s changed since you confirmed (%s); review the action again", namespace, name, strings.Join(changed, ", "))
	}
	x := &cnpgClusterRun{c: c, cluster: cluster, facts: facts, reviewed: reviewed, pods: pods, params: req.Params}
	res, err := runner.run(ctx, x)
	if err != nil {
		if apierrors.IsConflict(err) {
			// The object moved between our read and the write. Never retried:
			// report what it looks like now so the user can review again.
			current := facts
			if fresh, gerr := c.dyn.Resource(cnpgClusterGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{}); gerr == nil {
				current, _ = cnpgClusterFactsOf(ctx, typed, fresh)
			}
			return nil, changedAction(current, "Cluster %s/%s changed while the request was being sent; review the action again", namespace, name)
		}
		return nil, err
	}
	res.Action = action
	return res, nil
}

// cnpgConditionsWithReady returns status.conditions with the Ready condition
// set from phase, exactly as the operator's SetClusterReadyCondition does. The
// merge patch replaces the whole list, as upstream's MergeFrom does.
func cnpgConditionsWithReady(cluster *unstructured.Unstructured, phase string, now time.Time) ([]any, error) {
	var conds []metav1.Condition
	if raw, ok, _ := unstructured.NestedSlice(cluster.Object, "status", "conditions"); ok {
		data, err := json.Marshal(raw)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &conds); err != nil {
			return nil, fmt.Errorf("status.conditions: %w", err)
		}
	}
	ready := metav1.Condition{Type: "Ready", Status: metav1.ConditionFalse, Reason: "ClusterIsNotReady", Message: "Cluster Is Not Ready", LastTransitionTime: metav1.NewTime(now)}
	if phase == cnpgPhaseHealthy {
		ready = metav1.Condition{Type: "Ready", Status: metav1.ConditionTrue, Reason: "ClusterIsReady", Message: "Cluster is Ready", LastTransitionTime: metav1.NewTime(now)}
	}
	if conds == nil {
		conds = []metav1.Condition{}
	}
	meta.SetStatusCondition(&conds, ready)
	data, err := json.Marshal(conds)
	if err != nil {
		return nil, err
	}
	var out []any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func cnpgStatusPatch(ctx context.Context, x *cnpgClusterRun, status map[string]any) error {
	phase, _ := status["phase"].(string)
	conds, err := cnpgConditionsWithReady(x.cluster, phase, x.c.clock())
	if err != nil {
		return err
	}
	status["conditions"] = conds
	return mergePatchAtVersion(ctx, x.c.dyn, cnpgClusterGVR, x.cluster, map[string]any{"status": status}, "status")
}

func cnpgAnnotationPatch(ctx context.Context, x *cnpgClusterRun, key string, value any) error {
	return mergePatchAtVersion(ctx, x.c.dyn, cnpgClusterGVR, x.cluster, map[string]any{
		"metadata": map[string]any{"annotations": map[string]any{key: value}},
	})
}

// --- backup ---

type cnpgBackupParams struct {
	Method           string            `json:"method"`
	PluginName       string            `json:"pluginName,omitempty"`
	PluginParameters map[string]string `json:"pluginParameters,omitempty"`
	Target           string            `json:"target,omitempty"`
	Online           *bool             `json:"online,omitempty"`
	Name             string            `json:"name,omitempty"`
}

func cnpgRunBackup(ctx context.Context, x *cnpgClusterRun) (*CNPGActionResult, error) {
	var p cnpgBackupParams
	if err := decodeActionParams(x.params, &p); err != nil {
		return nil, err
	}
	if r := cnpgGuardBackup(x.facts); r != "" {
		return nil, blockedAction(r)
	}
	var chosen *CNPGBackupMethodFact
	for i, m := range x.facts.BackupMethods {
		if m.Method == p.Method && (m.Method != "plugin" || m.PluginName == p.PluginName) {
			chosen = &x.facts.BackupMethods[i]
			break
		}
	}
	if chosen == nil {
		return nil, refuseAction(http.StatusBadRequest, "", "method %q%s is not a backup method this cluster declares", p.Method, cnpgPluginSuffix(p.PluginName))
	}
	if r := cnpgBackupDestinationGuard(x.cluster, p.Method, p.PluginName, p.PluginParameters); r != "" {
		return nil, blockedAction(r)
	}
	if chosen.Capability == "none" {
		return nil, refuseAction(http.StatusBadRequest, "", "plugin %s reports no backup capability", chosen.PluginName)
	}
	if len(p.PluginParameters) > 0 && chosen.Method != "plugin" {
		return nil, refuseAction(http.StatusBadRequest, "", "pluginParameters apply only to the plugin method")
	}
	if p.Target != "" && p.Target != "primary" && p.Target != "prefer-standby" {
		return nil, refuseAction(http.StatusBadRequest, "", "target must be primary or prefer-standby (or omitted to inherit the cluster's)")
	}
	// A Backup without its own target inherits the cluster's, so the one the
	// confirmation showed is bound. The facts omit it when unset.
	if p.Target == "" {
		reviewedTarget := ""
		if x.reviewed.BackupTarget != nil {
			reviewedTarget = *x.reviewed.BackupTarget
		}
		if reviewedTarget != x.facts.BackupTarget {
			return nil, changedAction(x.facts, "Cluster %s/%s's backup target changed since you confirmed (backupTarget); review the action again", x.cluster.GetNamespace(), x.cluster.GetName())
		}
	}
	clusterName := x.cluster.GetName()
	name := p.Name
	if name == "" {
		name = clusterName + "-" + x.c.clock().Format(cnpgCompactStamp)
	}
	if err := cnpgValidateBackupName(ctx, x.c.dyn, x.cluster.GetNamespace(), name); err != nil {
		return nil, err
	}
	spec := map[string]any{
		"cluster": map[string]any{"name": clusterName},
		"method":  chosen.Method,
	}
	if chosen.Method == "plugin" {
		pc := map[string]any{"name": chosen.PluginName}
		if len(p.PluginParameters) > 0 {
			params := map[string]any{}
			for k, v := range p.PluginParameters {
				params[k] = v
			}
			pc["parameters"] = params
		}
		spec["pluginConfiguration"] = pc
	}
	if p.Target != "" {
		spec["target"] = p.Target
	}
	if p.Online != nil {
		spec["online"] = *p.Online
	}
	backup := cnpgBackupObject(x.cluster.GetNamespace(), name, clusterName, nil, spec)
	return cnpgCreateBackup(ctx, x.c.dyn, backup, clusterName)
}

func cnpgPluginSuffix(plugin string) string {
	if plugin == "" {
		return ""
	}
	return " (" + plugin + ")"
}

func cnpgBackupObject(namespace, name, cluster string, annotations map[string]any, spec map[string]any) *unstructured.Unstructured {
	md := map[string]any{
		"name":      name,
		"namespace": namespace,
		"labels":    map[string]any{cnpgClusterLabel: cluster},
	}
	if len(annotations) > 0 {
		md["annotations"] = annotations
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": cnpgGroup + "/v1",
		"kind":       "Backup",
		"metadata":   md,
		"spec":       spec,
	}}
}

// cnpgValidateBackupName refuses a name that is not a DNS subdomain, that has
// the form of a scheduled run (the operator would then skip that run), or that
// already exists.
func cnpgValidateBackupName(ctx context.Context, dyn dynamic.Interface, namespace, name string) error {
	if errs := validation.IsDNS1123Subdomain(name); len(errs) > 0 {
		return refuseAction(http.StatusBadRequest, "", "backup name %q is invalid: %s", name, strings.Join(errs, "; "))
	}
	if cnpgScheduleRunR.MatchString(name) {
		schedule := name[:len(name)-15]
		_, err := dyn.Resource(cnpgScheduleGVR).Namespace(namespace).Get(ctx, schedule, metav1.GetOptions{})
		switch {
		case err == nil:
			return refuseAction(http.StatusBadRequest, "", "backup name %q has the form of a run of ScheduledBackup %s: the operator would skip that run", name, schedule)
		case apierrors.IsNotFound(err):
		default:
			return fmt.Errorf("cannot check the name against ScheduledBackup %s: %w", schedule, err)
		}
	}
	_, err := dyn.Resource(cnpgBackupGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	switch {
	case err == nil:
		return refuseAction(http.StatusConflict, "", "a Backup named %s already exists", name)
	case apierrors.IsNotFound(err):
		return nil
	default:
		return err
	}
}

func cnpgAmbiguousOutcome(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || apierrors.IsTimeout(err) || apierrors.IsServerTimeout(err) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// cnpgCreateBackup creates the Backup once. A create whose answer was lost is
// resolved by reading the same name back, never by creating another.
func cnpgCreateBackup(ctx context.Context, dyn dynamic.Interface, backup *unstructured.Unstructured, cluster string) (*CNPGActionResult, error) {
	ns, name := backup.GetNamespace(), backup.GetName()
	_, err := dyn.Resource(cnpgBackupGVR).Namespace(ns).Create(ctx, backup, metav1.CreateOptions{})
	if err == nil {
		return &CNPGActionResult{Backup: name, Message: "Backup " + name + " created"}, nil
	}
	if !cnpgAmbiguousOutcome(err) {
		return nil, err
	}
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	got, gerr := dyn.Resource(cnpgBackupGVR).Namespace(ns).Get(readCtx, name, metav1.GetOptions{})
	if gerr == nil {
		ref, _, _ := unstructured.NestedString(got.Object, "spec", "cluster", "name")
		if ref == cluster {
			return &CNPGActionResult{Backup: name, ResolvedAfterTimeout: true, Message: "Backup " + name + " created (confirmed by reading it back after the request timed out)"}, nil
		}
	}
	return nil, refuseAction(http.StatusGatewayTimeout, cnpgCodeAmbiguous,
		"The create of Backup %s timed out and reading it back did not find it; it may still appear. Check the cluster's backups before trying again: %v", name, err)
}

// --- switchover ---

type cnpgSwitchoverParams struct {
	Target       string `json:"target"`
	TargetPodUID string `json:"targetPodUID"`
}

func cnpgRunSwitchover(ctx context.Context, x *cnpgClusterRun) (*CNPGActionResult, error) {
	var p cnpgSwitchoverParams
	if err := decodeActionParams(x.params, &p); err != nil {
		return nil, err
	}
	if p.Target == "" || p.TargetPodUID == "" {
		return nil, refuseAction(http.StatusBadRequest, "", "params.target and params.targetPodUID are required")
	}
	if r := cnpgGuardSwitchover(x.facts); r != "" {
		return nil, blockedAction(r)
	}
	inst, ok := x.facts.instance(p.Target)
	if !ok {
		return nil, changedAction(x.facts, "%s is not an instance of this cluster", p.Target)
	}
	if inst.PodExists && inst.PodUID != p.TargetPodUID {
		return nil, changedAction(x.facts, "Pod %s was recreated since you reviewed it", p.Target)
	}
	if r := cnpgGuardSwitchoverTarget(x.facts, inst); r != "" {
		return nil, blockedAction(p.Target + " cannot be promoted: " + r)
	}
	err := cnpgStatusPatch(ctx, x, map[string]any{
		"targetPrimary": p.Target,
		// RFC3339 with microseconds, as the operator's pgTime.GetCurrentTimestamp.
		"targetPrimaryTimestamp": x.c.clock().Format(metav1.RFC3339Micro),
		"phase":                  cnpgPhaseSwitchover,
		"phaseReason":            "Switching over to " + p.Target,
	})
	if err != nil {
		return nil, err
	}
	return &CNPGActionResult{Message: "Switchover to " + p.Target + " requested"}, nil
}

// --- restart / reload ---

func cnpgRunRestart(ctx context.Context, x *cnpgClusterRun) (*CNPGActionResult, error) {
	if err := decodeActionParams(x.params, &struct{}{}); err != nil {
		return nil, err
	}
	if r := cnpgGuardRestart(x.facts); r != "" {
		return nil, blockedAction(r)
	}
	if err := cnpgAnnotationPatch(ctx, x, cnpgRestartAnnotation, x.c.clock().Format(time.RFC3339)); err != nil {
		return nil, err
	}
	return &CNPGActionResult{Message: "Rolling restart requested"}, nil
}

func cnpgRunReload(ctx context.Context, x *cnpgClusterRun) (*CNPGActionResult, error) {
	if err := decodeActionParams(x.params, &struct{}{}); err != nil {
		return nil, err
	}
	if r := cnpgGuardReload(x.facts); r != "" {
		return nil, blockedAction(r)
	}
	if err := cnpgAnnotationPatch(ctx, x, cnpgReloadAnnotation, x.c.clock().Format(metav1.RFC3339Micro)); err != nil {
		return nil, err
	}
	return &CNPGActionResult{Message: "Configuration reload requested"}, nil
}

type cnpgRestartInstanceParams struct {
	Pod    string `json:"pod"`
	PodUID string `json:"podUID"`
}

func cnpgRunRestartInstance(ctx context.Context, x *cnpgClusterRun) (*CNPGActionResult, error) {
	var p cnpgRestartInstanceParams
	if err := decodeActionParams(x.params, &p); err != nil {
		return nil, err
	}
	if p.Pod == "" || p.PodUID == "" {
		return nil, refuseAction(http.StatusBadRequest, "", "params.pod and params.podUID are required")
	}
	inst, ok := x.facts.instance(p.Pod)
	if !ok {
		return nil, changedAction(x.facts, "%s is not an instance of this cluster", p.Pod)
	}
	if r := cnpgGuardRestartInstance(x.facts, inst); r != "" {
		return nil, blockedAction(r)
	}
	if !inst.PodReadable {
		return nil, refuseAction(http.StatusForbidden, "", "Pod %s cannot be read, so it cannot be verified as this cluster's instance", p.Pod)
	}
	if !inst.PodExists || inst.PodUID != p.PodUID {
		return nil, changedAction(x.facts, "Pod %s was recreated since you reviewed it", p.Pod)
	}
	if inst.Pod == x.facts.CurrentPrimary {
		err := cnpgStatusPatch(ctx, x, map[string]any{
			"phase":       cnpgPhaseInplaceRestart,
			"phaseReason": cnpgInplaceReason,
		})
		if err != nil {
			return nil, err
		}
		return &CNPGActionResult{Message: "In-place restart of primary " + p.Pod + " requested"}, nil
	}
	uid := types.UID(p.PodUID)
	err := x.c.typed.CoreV1().Pods(x.cluster.GetNamespace()).Delete(ctx, p.Pod, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &uid},
	})
	if err != nil {
		return nil, err
	}
	return &CNPGActionResult{Message: "Pod " + p.Pod + " deleted; the operator recreates it on its volumes"}, nil
}

// --- fencing ---

type cnpgFenceParams struct {
	Instances      json.RawMessage `json:"instances"`
	ConvertFromAll bool            `json:"convertFromAll,omitempty"`
	Remaining      []string        `json:"remaining,omitempty"`
}

// cnpgFenceTargets reads params.instances: "*" or a list of names.
func cnpgFenceTargets(raw json.RawMessage) (all bool, names []string, err error) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s != cnpgAllInstances {
			return false, nil, refuseAction(http.StatusBadRequest, "", `params.instances must be "*" or a list of instance names`)
		}
		return true, nil, nil
	}
	if err := json.Unmarshal(raw, &names); err != nil || len(names) == 0 {
		return false, nil, refuseAction(http.StatusBadRequest, "", `params.instances must be "*" or a non-empty list of instance names`)
	}
	for _, n := range names {
		if n == cnpgAllInstances {
			return true, nil, nil
		}
	}
	return false, names, nil
}

// cnpgFencedValue serializes a fenced set; nil removes the annotation.
func cnpgFencedValue(all bool, names []string) (any, error) {
	if all {
		return `["*"]`, nil
	}
	if len(names) == 0 {
		return nil, nil
	}
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	b, err := json.Marshal(sorted)
	return string(b), err
}

func cnpgRunFence(ctx context.Context, x *cnpgClusterRun) (*CNPGActionResult, error) {
	var p cnpgFenceParams
	if err := decodeActionParams(x.params, &p); err != nil {
		return nil, err
	}
	if p.ConvertFromAll || len(p.Remaining) > 0 {
		return nil, refuseAction(http.StatusBadRequest, "", "convertFromAll and remaining apply only to unfence")
	}
	all, names, err := cnpgFenceTargets(p.Instances)
	if err != nil {
		return nil, err
	}
	if r := cnpgGuardFence(x.facts); r != "" {
		return nil, blockedAction(r)
	}
	next := map[string]bool{}
	for _, n := range x.facts.FencedInstances.Instances {
		next[n] = true
	}
	for _, n := range names {
		if _, ok := x.facts.instance(n); !ok {
			return nil, refuseAction(http.StatusBadRequest, "", "%s is not an instance of this cluster", n)
		}
		if next[n] {
			return nil, blockedAction(n + " is fenced already")
		}
		next[n] = true
	}
	value, err := cnpgFencedValue(all, cnpgSortedKeys(next))
	if err != nil {
		return nil, err
	}
	if err := cnpgAnnotationPatch(ctx, x, cnpgFencedAnnotation, value); err != nil {
		return nil, err
	}
	return &CNPGActionResult{Message: "Fencing requested"}, nil
}

func cnpgRunUnfence(ctx context.Context, x *cnpgClusterRun) (*CNPGActionResult, error) {
	var p cnpgFenceParams
	if err := decodeActionParams(x.params, &p); err != nil {
		return nil, err
	}
	all, names, err := cnpgFenceTargets(p.Instances)
	if err != nil {
		return nil, err
	}
	if r := cnpgGuardUnfence(x.facts); r != "" {
		return nil, blockedAction(r)
	}
	var value any
	switch {
	case all:
		value = nil
	case x.facts.FencedInstances.All:
		if !p.ConvertFromAll {
			return nil, refuseAction(http.StatusConflict, cnpgCodeAllFenced,
				`Every instance is fenced with ["*"]: lifting one instance means rewriting the fence as the explicit list of the others. Lift every fence, or confirm the conversion with convertFromAll and the remaining list`)
		}
		// The reviewer must have seen the full explicit set that stays fenced.
		want := map[string]bool{}
		for _, i := range x.facts.Instances {
			want[i.Pod] = true
		}
		for _, n := range names {
			if _, ok := x.facts.instance(n); !ok {
				return nil, refuseAction(http.StatusBadRequest, "", "%s is not an instance of this cluster", n)
			}
			delete(want, n)
		}
		if !cnpgSameStringSet(p.Remaining, cnpgSortedKeys(want)) {
			return nil, changedAction(x.facts, "The instances that stay fenced are %s, not %s; review the conversion again",
				strings.Join(cnpgSortedKeys(want), ", "), strings.Join(p.Remaining, ", "))
		}
		if value, err = cnpgFencedValue(false, cnpgSortedKeys(want)); err != nil {
			return nil, err
		}
	default:
		if p.ConvertFromAll {
			return nil, refuseAction(http.StatusBadRequest, "", `convertFromAll applies only while ["*"] fences every instance`)
		}
		next := map[string]bool{}
		for _, n := range x.facts.FencedInstances.Instances {
			next[n] = true
		}
		for _, n := range names {
			if !next[n] {
				return nil, blockedAction(n + " is not fenced")
			}
			delete(next, n)
		}
		if value, err = cnpgFencedValue(false, cnpgSortedKeys(next)); err != nil {
			return nil, err
		}
	}
	if err := cnpgAnnotationPatch(ctx, x, cnpgFencedAnnotation, value); err != nil {
		return nil, err
	}
	return &CNPGActionResult{Message: "Lifting the fence requested"}, nil
}

func cnpgSortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		if v {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func cnpgSameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, v := range a {
		seen[v]++
	}
	for _, v := range b {
		seen[v]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

// --- hibernation ---

func cnpgRunHibernation(value string) func(context.Context, *cnpgClusterRun) (*CNPGActionResult, error) {
	return func(ctx context.Context, x *cnpgClusterRun) (*CNPGActionResult, error) {
		if err := decodeActionParams(x.params, &struct{}{}); err != nil {
			return nil, err
		}
		guard, msg := cnpgGuardHibernate, "Hibernation requested"
		if value == "off" {
			guard, msg = cnpgGuardRehydrate, "Rehydration requested"
		}
		if r := guard(x.facts); r != "" {
			return nil, blockedAction(r)
		}
		if err := cnpgAnnotationPatch(ctx, x, cnpgHibernateAnnotation, value); err != nil {
			return nil, err
		}
		return &CNPGActionResult{Message: msg}, nil
	}
}

// --- scheduled backups ---

func runCNPGScheduleAction(ctx context.Context, c cnpgActionClients, namespace, name, action string, req ActionRequest) (*CNPGActionResult, error) {
	reviewed, err := decodeCNPGReviewedFacts(req.Facts)
	if err != nil {
		return nil, refuseAction(http.StatusBadRequest, "", "%v", err)
	}
	var params struct {
		Schedule *string `json:"schedule"`
	}
	if action == "setSchedule" {
		if err := decodeActionParams(req.Params, &params); err != nil {
			return nil, err
		}
		if params.Schedule == nil {
			return nil, refuseAction(http.StatusBadRequest, "", "params.schedule is required for setSchedule")
		}
		if p := cnpgSchedulePreview(*params.Schedule, nil, false, c.clock()); !p.Valid {
			return nil, refuseAction(http.StatusBadRequest, cnpgCodeInvalidSchedule, "invalid schedule %q: %s", *params.Schedule, p.Error)
		}
	} else if err := decodeActionParams(req.Params, &struct{}{}); err != nil {
		return nil, err
	}
	sched, err := c.dyn.Resource(cnpgScheduleGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	facts := cnpgScheduleFactsOf(ctx, c, sched)
	if string(sched.GetUID()) != req.UID {
		return nil, changedAction(facts, "ScheduledBackup %s/%s was deleted and recreated since you reviewed it", namespace, name)
	}
	switch action {
	case "setSchedule":
		if reviewed.Schedule == nil {
			return nil, refuseAction(http.StatusBadRequest, "", "facts.schedule is required for setSchedule: the confirmation must bind what the dialog showed")
		}
		if *reviewed.Schedule != facts.Schedule {
			return nil, changedAction(facts, "ScheduledBackup %s/%s schedule changed since you reviewed it; review the action again", namespace, name)
		}
	case "run":
		if reviewed.Generation == nil {
			return nil, refuseAction(http.StatusBadRequest, "", "facts.generation is required for run")
		}
		if *reviewed.Generation != facts.Generation {
			return nil, changedAction(facts, "ScheduledBackup %s/%s settings changed since you confirmed; review the action again", namespace, name)
		}
	default:
		if reviewed.Suspended == nil {
			return nil, refuseAction(http.StatusBadRequest, "", "facts.suspended is required for %s", action)
		}
		if *reviewed.Suspended != facts.Suspended {
			return nil, changedAction(facts, "ScheduledBackup %s/%s changed since you confirmed (suspended); review the action again", namespace, name)
		}
	}
	if facts.Terminating {
		return nil, blockedAction("The schedule is being deleted")
	}

	switch action {
	case "setSchedule":
		if *params.Schedule == facts.Schedule {
			return nil, blockedAction("The schedule is already " + facts.Schedule)
		}
		err := mergePatchAtVersion(ctx, c.dyn, cnpgScheduleGVR, sched, map[string]any{"spec": map[string]any{"schedule": *params.Schedule}})
		if apierrors.IsConflict(err) {
			if fresh, gerr := c.dyn.Resource(cnpgScheduleGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{}); gerr == nil {
				facts = cnpgScheduleFactsOf(ctx, c, fresh)
			}
			return nil, changedAction(facts, "ScheduledBackup %s/%s changed while the request was being sent; review the action again", namespace, name)
		}
		if err != nil {
			return nil, err
		}
		preview := cnpgSchedulePreview(*params.Schedule, cnpgScheduleLastCheck(sched), facts.Suspended, c.clock())
		return &CNPGActionResult{Action: action, Message: "Schedule set to " + *params.Schedule, CatchUp: preview.RunsImmediately}, nil
	case "suspend", "resume":
		want := action == "suspend"
		if facts.Suspended == want {
			return nil, blockedAction(map[bool]string{true: "The schedule is suspended already", false: "The schedule is not suspended"}[want])
		}
		err := mergePatchAtVersion(ctx, c.dyn, cnpgScheduleGVR, sched, map[string]any{"spec": map[string]any{"suspend": want}})
		if apierrors.IsConflict(err) {
			if fresh, gerr := c.dyn.Resource(cnpgScheduleGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{}); gerr == nil {
				facts = cnpgScheduleFactsOf(ctx, c, fresh)
			}
			return nil, changedAction(facts, "ScheduledBackup %s/%s changed while the request was being sent; review the action again", namespace, name)
		}
		if err != nil {
			return nil, err
		}
		res := &CNPGActionResult{Action: action, Message: "Schedule suspended"}
		if !want {
			res.Message = "Schedule resumed"
			res.CatchUp = facts.CatchUp
		}
		return res, nil
	}

	if r := cnpgGuardScheduleRun(facts); r != "" {
		return nil, blockedAction(r)
	}
	backupName := name + "-manual-" + c.clock().Format(cnpgCompactStamp)
	if errs := validation.IsDNS1123Subdomain(backupName); len(errs) > 0 {
		return nil, refuseAction(http.StatusBadRequest, "", "backup name %q is invalid: %s", backupName, strings.Join(errs, "; "))
	}
	backup := cnpgBackupObject(namespace, backupName, facts.Cluster, map[string]any{cnpgRequestedFromAnno: name}, cnpgBackupSpecFromSchedule(sched))
	res, err := cnpgCreateBackup(ctx, c.dyn, backup, facts.Cluster)
	if err != nil {
		return nil, err
	}
	res.Action = action
	return res, nil
}

// cnpgBackupSpecFromSchedule copies the complete backup settings of the
// schedule. Fields the schedule leaves unset stay unset, so the Backup
// inherits the cluster's defaults exactly as a scheduled run would.
func cnpgBackupSpecFromSchedule(sched *unstructured.Unstructured) map[string]any {
	spec := map[string]any{}
	src, _, _ := unstructured.NestedMap(sched.Object, "spec")
	for _, key := range []string{"cluster", "method", "pluginConfiguration", "online", "onlineConfiguration", "target"} {
		if v, ok := src[key]; ok && v != nil {
			spec[key] = v
		}
	}
	return spec
}

// ---------- errors ----------

// writeCNPGActionError adds the CloudNativePG reading of an admission-webhook
// failure to the shared action error answer.
func (s *Server) writeCNPGActionError(w http.ResponseWriter, err error, action, namespace, name string) {
	var ae *actionError
	if !errors.As(err, &ae) && strings.Contains(err.Error(), "failed calling webhook") {
		err = refuseAction(http.StatusServiceUnavailable, cnpgCodeWebhook, "%s", "The CloudNativePG operator's admission webhook did not answer — the operator may be down: "+err.Error())
	}
	s.writeActionError(w, "cnpg", err, action, namespace, name, cnpgGrantFor)
}

func cnpgGrantFor(action string) (Grant, bool) {
	if g, ok := cnpgClusterActionGrants[action]; ok {
		return g, true
	}
	if g, ok := cnpgScheduleActionGrants[action]; ok {
		return g, true
	}
	if g, ok := cnpgExtraActionGrants[action]; ok {
		return g, true
	}
	return Grant{}, false
}
