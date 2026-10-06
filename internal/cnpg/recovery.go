package cnpg

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	auth "github.com/skyhook-io/radar/internal/auth"
	integration "github.com/skyhook-io/radar/internal/integration"
)

const (
	cnpgRestoreValidationAnno = "radar.skyhook.io/restore-validation"
	cnpgRestoreValidationMax  = 2000
	cnpgRecoveryEventLimit    = 40

	cnpgReadOK       = "ok"
	cnpgReadDenied   = "denied"
	cnpgReadNotFound = "notFound"
	cnpgReadError    = "error"
	cnpgReadSkipped  = "skipped"
)

var (
	GrantListPods       = auth.Grant{Verb: "list", Resource: "pods"}
	cnpgGrantListEvents = auth.Grant{Verb: "list", Resource: "events"}
	GrantGetCluster     = auth.Grant{Verb: "get", Group: Group, Resource: "clusters"}
)

// cnpgGatedRead runs read when the caller holds g in namespace. The SAR comes
// first so a denial names the grant; the read itself is made with the caller's
// client, so the apiserver has the final say either way.
func (s *Reader) gatedRead(ctx context.Context, g auth.Grant, namespace string, read func() error) integration.ReadSource {
	if s.Access.Permission(ctx, g.In(namespace)) == integration.PermissionDenied {
		return integration.ReadSource{State: cnpgReadDenied, Grant: g.In(namespace).Ref()}
	}
	return cnpgReadOutcome(read(), g, namespace)
}

func cnpgReadOutcome(err error, g auth.Grant, namespace string) integration.ReadSource {
	switch {
	case err == nil:
		return integration.ReadSource{State: cnpgReadOK}
	case apierrors.IsForbidden(err):
		return integration.ReadSource{State: cnpgReadDenied, Grant: g.In(namespace).Ref()}
	case apierrors.IsNotFound(err):
		return integration.ReadSource{State: cnpgReadNotFound, Reason: err.Error()}
	default:
		return integration.ReadSource{State: cnpgReadError, Reason: cnpgPlainReadError(err.Error())}
	}
}

// cnpgPlainReadError is the reader-facing text of a failed Kubernetes API
// read: a sentence for a failure in transit (the raw error is logged),
// otherwise the error itself.
func cnpgPlainReadError(raw string) string {
	if plain, ok := cnpgTransportSentence(errors.New(raw), 0, cnpgHAReadTimeout); ok {
		log.Printf("[cnpg] Read failed: %s", raw)
		return plain
	}
	return truncateCNPGRuntimeError(raw)
}

// CNPGRecoveryCluster is the restored Cluster's own progress. Counts are nil
// when the operator has not reported them yet, which is not zero.
type CNPGRecoveryCluster struct {
	UID            string                 `json:"uid"`
	Phase          string                 `json:"phase,omitempty"`
	PhaseReason    string                 `json:"phaseReason,omitempty"`
	Instances      *int64                 `json:"instances"`
	ReadyInstances *int64                 `json:"readyInstances"`
	CurrentPrimary string                 `json:"currentPrimary,omitempty"`
	CreatedAt      string                 `json:"createdAt,omitempty"`
	Ready          *CNPGRecoveryCondition `json:"ready,omitempty"`
}

type CNPGRecoveryCondition struct {
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

// CNPGRecoverySpec is what the Cluster declares it recovers from.
type CNPGRecoverySpec struct {
	// SourceKind: objectStore (barman-cloud plugin), barmanObjectStore (in-tree),
	// backup (a Backup object), volumeSnapshots, or unknown.
	SourceKind  string         `json:"sourceKind"`
	Source      string         `json:"source,omitempty"`
	ObjectStore string         `json:"objectStore,omitempty"`
	ServerName  string         `json:"serverName,omitempty"`
	Backup      string         `json:"backup,omitempty"`
	Target      map[string]any `json:"target,omitempty"`
}

type CNPGContainerState struct {
	Name     string `json:"name"`
	State    string `json:"state"`
	Reason   string `json:"reason,omitempty"`
	Message  string `json:"message,omitempty"`
	ExitCode *int32 `json:"exitCode,omitempty"`
	Restarts int32  `json:"restarts"`
	Ready    bool   `json:"ready"`
}

// CNPGRecoveryPod is a Pod doing the recovery (kind "job": owned by one of
// the Cluster's Jobs, e.g. <cluster>-1-full-recovery) or an instance.
type CNPGRecoveryPod struct {
	Name           string               `json:"name"`
	UID            string               `json:"uid"`
	Kind           string               `json:"kind"`
	Job            string               `json:"job,omitempty"`
	OwnerVerified  bool                 `json:"ownerVerified"`
	Phase          string               `json:"phase"`
	Ready          bool                 `json:"ready"`
	StartedAt      string               `json:"startedAt,omitempty"`
	InitContainers []CNPGContainerState `json:"initContainers"`
	Containers     []CNPGContainerState `json:"containers"`
}

type CNPGRecoveryJob struct {
	Name        string `json:"name"`
	Active      int32  `json:"active"`
	Succeeded   int32  `json:"succeeded"`
	Failed      int32  `json:"failed"`
	Complete    bool   `json:"complete"`
	FailedWith  string `json:"failedWith,omitempty"`
	StartedAt   string `json:"startedAt,omitempty"`
	CompletedAt string `json:"completedAt,omitempty"`
}

type CNPGRecoveryEvent struct {
	Type     string `json:"type"`
	Reason   string `json:"reason"`
	Message  string `json:"message"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Count    int32  `json:"count"`
	LastSeen string `json:"lastSeen,omitempty"`
}

// CNPGRestoreValidation is the note a person recorded after checking a
// restored cluster. It is evidence that someone looked, never a verdict.
type CNPGRestoreValidation struct {
	Version    int                       `json:"version"`
	RecordedAt string                    `json:"recordedAt"`
	RecordedBy string                    `json:"recordedBy,omitempty"`
	Checked    string                    `json:"checked"`
	TargetTime string                    `json:"targetTime,omitempty"`
	Source     *CNPGRestoreValidationRef `json:"source,omitempty"`
	Target     CNPGRestoreValidationRef  `json:"target"`
}

// CNPGRestoreValidationRef names a Cluster; UID is empty when Radar could not
// read it as the recording user, and Verified says so.
type CNPGRestoreValidationRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid,omitempty"`
	Verified  bool   `json:"verified"`
}

// CNPGRecoveryResponse is GET /api/cnpg/clusters/{ns}/{name}/recovery.
type CNPGRecoveryResponse struct {
	Cluster         CNPGRecoveryCluster               `json:"cluster"`
	Recovery        *CNPGRecoverySpec                 `json:"recovery"`
	Pods            []CNPGRecoveryPod                 `json:"pods"`
	Jobs            []CNPGRecoveryJob                 `json:"jobs"`
	Events          []CNPGRecoveryEvent               `json:"events"`
	Coverage        map[string]integration.ReadSource `json:"coverage"`
	Validation      *CNPGRestoreValidation            `json:"validation,omitempty"`
	ValidationError string                            `json:"validationError,omitempty"`
	CapturedAt      string                            `json:"capturedAt"`
}

func (s *Reader) RecoverySnapshot(callerCtx context.Context, typed kubernetes.Interface, cluster *unstructured.Unstructured) CNPGRecoveryResponse {
	ctx := callerCtx
	namespace, name := cluster.GetNamespace(), cluster.GetName()
	resp := CNPGRecoveryResponse{
		Cluster:    cnpgRecoveryClusterOf(cluster),
		Recovery:   cnpgRecoverySpecOf(cluster),
		Pods:       []CNPGRecoveryPod{},
		Jobs:       []CNPGRecoveryJob{},
		Events:     []CNPGRecoveryEvent{},
		Coverage:   map[string]integration.ReadSource{},
		CapturedAt: time.Now().UTC().Format(time.RFC3339),
	}
	resp.Validation, resp.ValidationError = parseCNPGRestoreValidation(cluster.GetAnnotations()[cnpgRestoreValidationAnno])

	selector := labels.SelectorFromSet(labels.Set{clusterLabel: name}).String()
	var jobs []batchv1.Job
	resp.Coverage["jobs"] = s.gatedRead(callerCtx, cnpgGrantListJobs, namespace, func() error {
		list, err := typed.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err == nil {
			jobs = list.Items
		}
		return err
	})
	ownedJobs := map[string]bool{}
	for i := range jobs {
		j := &jobs[i]
		if !controlledBy(j.OwnerReferences, Group, "Cluster", name, cluster.GetUID()) {
			continue
		}
		ownedJobs[j.Name] = true
		resp.Jobs = append(resp.Jobs, cnpgRecoveryJobOf(j))
	}
	sort.Slice(resp.Jobs, func(i, j int) bool { return resp.Jobs[i].Name < resp.Jobs[j].Name })

	var pods []corev1.Pod
	resp.Coverage["pods"] = s.gatedRead(callerCtx, GrantListPods, namespace, func() error {
		list, err := typed.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err == nil {
			pods = list.Items
		}
		return err
	})
	jobsKnown := resp.Coverage["jobs"].State == cnpgReadOK
	for i := range pods {
		p := &pods[i]
		ref := controllerRef(p.OwnerReferences)
		if ref == nil {
			continue
		}
		switch {
		case ref.Kind == "Cluster" && ref.UID == cluster.GetUID():
			resp.Pods = append(resp.Pods, cnpgRecoveryPodOf(p, "instance", "", true))
		case ref.Kind == "Job" && ownedJobs[ref.Name]:
			resp.Pods = append(resp.Pods, cnpgRecoveryPodOf(p, "job", ref.Name, true))
		case ref.Kind == "Job" && !jobsKnown && strings.HasPrefix(ref.Name, name+"-"):
			resp.Pods = append(resp.Pods, cnpgRecoveryPodOf(p, "job", ref.Name, false))
		}
	}
	sort.Slice(resp.Pods, func(i, j int) bool {
		if resp.Pods[i].Kind != resp.Pods[j].Kind {
			return resp.Pods[i].Kind == "job"
		}
		return resp.Pods[i].Name < resp.Pods[j].Name
	})

	subjects := map[string]bool{"Cluster/" + name: true}
	for _, p := range resp.Pods {
		subjects["Pod/"+p.Name] = true
	}
	for _, j := range resp.Jobs {
		subjects["Job/"+j.Name] = true
	}
	var events []corev1.Event
	resp.Coverage["events"] = s.gatedRead(callerCtx, cnpgGrantListEvents, namespace, func() error {
		list, err := typed.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{})
		if err == nil {
			events = list.Items
		}
		return err
	})
	resp.Events = cnpgRecoveryEventsOf(events, subjects, cnpgRecoveryEventLimit)
	return resp
}

func cnpgRecoveryClusterOf(cluster *unstructured.Unstructured) CNPGRecoveryCluster {
	out := CNPGRecoveryCluster{UID: string(cluster.GetUID())}
	if ts := cluster.GetCreationTimestamp(); !ts.IsZero() {
		out.CreatedAt = ts.UTC().Format(time.RFC3339)
	}
	out.Phase, _, _ = unstructured.NestedString(cluster.Object, "status", "phase")
	out.PhaseReason, _, _ = unstructured.NestedString(cluster.Object, "status", "phaseReason")
	out.CurrentPrimary, _, _ = unstructured.NestedString(cluster.Object, "status", "currentPrimary")
	if v, ok, _ := unstructured.NestedInt64(cluster.Object, "status", "instances"); ok {
		out.Instances = &v
	}
	if v, ok, _ := unstructured.NestedInt64(cluster.Object, "status", "readyInstances"); ok {
		out.ReadyInstances = &v
	}
	conds, _, _ := unstructured.NestedSlice(cluster.Object, "status", "conditions")
	for _, c := range conds {
		m, _ := c.(map[string]any)
		if m == nil || m["type"] != "Ready" {
			continue
		}
		status, _ := m["status"].(string)
		reason, _ := m["reason"].(string)
		message, _ := m["message"].(string)
		out.Ready = &CNPGRecoveryCondition{Status: status, Reason: reason, Message: message}
	}
	return out
}

func cnpgRecoverySpecOf(cluster *unstructured.Unstructured) *CNPGRecoverySpec {
	rec, ok, _ := unstructured.NestedMap(cluster.Object, "spec", "bootstrap", "recovery")
	if !ok {
		return nil
	}
	out := &CNPGRecoverySpec{SourceKind: "unknown"}
	if t, ok := rec["recoveryTarget"].(map[string]any); ok && len(t) > 0 {
		out.Target = t
	}
	if b, ok := rec["backup"].(map[string]any); ok {
		out.SourceKind = "backup"
		out.Backup, _ = b["name"].(string)
		return out
	}
	if _, ok := rec["volumeSnapshots"]; ok {
		out.SourceKind = "volumeSnapshots"
		return out
	}
	out.Source, _ = rec["source"].(string)
	if out.Source == "" {
		return out
	}
	ext, _, _ := unstructured.NestedSlice(cluster.Object, "spec", "externalClusters")
	for _, e := range ext {
		m, _ := e.(map[string]any)
		if m == nil || m["name"] != out.Source {
			continue
		}
		if p, ok := m["plugin"].(map[string]any); ok {
			params, _ := p["parameters"].(map[string]any)
			out.SourceKind = "objectStore"
			out.ObjectStore, _ = params["barmanObjectName"].(string)
			out.ServerName, _ = params["serverName"].(string)
			if out.ServerName == "" {
				out.ServerName = out.Source
			}
		} else if b, ok := m["barmanObjectStore"].(map[string]any); ok {
			out.SourceKind = "barmanObjectStore"
			out.ServerName, _ = b["serverName"].(string)
			if out.ServerName == "" {
				out.ServerName = out.Source
			}
		}
	}
	return out
}

func cnpgRecoveryJobOf(j *batchv1.Job) CNPGRecoveryJob {
	out := CNPGRecoveryJob{Name: j.Name, Active: j.Status.Active, Succeeded: j.Status.Succeeded, Failed: j.Status.Failed}
	if j.Status.StartTime != nil {
		out.StartedAt = j.Status.StartTime.UTC().Format(time.RFC3339)
	}
	if j.Status.CompletionTime != nil {
		out.CompletedAt = j.Status.CompletionTime.UTC().Format(time.RFC3339)
	}
	for _, c := range j.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			out.Complete = true
		case batchv1.JobFailed:
			out.FailedWith = strings.TrimSpace(c.Reason + ": " + c.Message)
		}
	}
	return out
}

func cnpgContainerStatesOf(specs []corev1.Container, statuses []corev1.ContainerStatus) []CNPGContainerState {
	byName := map[string]corev1.ContainerStatus{}
	for _, st := range statuses {
		byName[st.Name] = st
	}
	out := make([]CNPGContainerState, 0, len(specs))
	for _, c := range specs {
		cs := CNPGContainerState{Name: c.Name, State: "unknown"}
		if st, ok := byName[c.Name]; ok {
			cs.Restarts = st.RestartCount
			cs.Ready = st.Ready
			switch {
			case st.State.Running != nil:
				cs.State = "running"
			case st.State.Terminated != nil:
				t := st.State.Terminated
				cs.State, cs.Reason, cs.Message = "terminated", t.Reason, truncateCNPGRuntimeError(t.Message)
				code := t.ExitCode
				cs.ExitCode = &code
			case st.State.Waiting != nil:
				cs.State, cs.Reason, cs.Message = "waiting", st.State.Waiting.Reason, truncateCNPGRuntimeError(st.State.Waiting.Message)
			}
		}
		out = append(out, cs)
	}
	return out
}

func cnpgRecoveryPodOf(p *corev1.Pod, kind, job string, verified bool) CNPGRecoveryPod {
	out := CNPGRecoveryPod{
		Name:           p.Name,
		UID:            string(p.UID),
		Kind:           kind,
		Job:            job,
		OwnerVerified:  verified,
		Phase:          string(p.Status.Phase),
		Ready:          cnpgActionPodReady(p),
		InitContainers: cnpgContainerStatesOf(p.Spec.InitContainers, p.Status.InitContainerStatuses),
		Containers:     cnpgContainerStatesOf(p.Spec.Containers, p.Status.ContainerStatuses),
	}
	if p.Status.StartTime != nil {
		out.StartedAt = p.Status.StartTime.UTC().Format(time.RFC3339)
	}
	return out
}

func cnpgEventTime(e *corev1.Event) time.Time {
	switch {
	case !e.LastTimestamp.IsZero():
		return e.LastTimestamp.Time
	case !e.EventTime.IsZero():
		return e.EventTime.Time
	default:
		return e.CreationTimestamp.Time
	}
}

// cnpgRecoveryEventsOf keeps events about the given Kind/name subjects,
// newest first.
func cnpgRecoveryEventsOf(events []corev1.Event, subjects map[string]bool, limit int) []CNPGRecoveryEvent {
	kept := make([]*corev1.Event, 0)
	for i := range events {
		e := &events[i]
		if subjects[e.InvolvedObject.Kind+"/"+e.InvolvedObject.Name] {
			kept = append(kept, e)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return cnpgEventTime(kept[i]).After(cnpgEventTime(kept[j])) })
	if len(kept) > limit {
		kept = kept[:limit]
	}
	out := make([]CNPGRecoveryEvent, 0, len(kept))
	for _, e := range kept {
		ev := CNPGRecoveryEvent{Type: e.Type, Reason: e.Reason, Message: e.Message, Kind: e.InvolvedObject.Kind, Name: e.InvolvedObject.Name, Count: e.Count}
		if t := cnpgEventTime(e); !t.IsZero() {
			ev.LastSeen = t.UTC().Format(time.RFC3339)
		}
		out = append(out, ev)
	}
	return out
}

func parseCNPGRestoreValidation(raw string) (*CNPGRestoreValidation, string) {
	if strings.TrimSpace(raw) == "" {
		return nil, ""
	}
	var v CNPGRestoreValidation
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, "the " + cnpgRestoreValidationAnno + " annotation is not valid JSON"
	}
	if v.RecordedAt == "" || v.Checked == "" {
		return nil, "the " + cnpgRestoreValidationAnno + " annotation is missing recordedAt or checked"
	}
	return &v, ""
}

// cnpgRestoreValidationParams is the POST body's params. Identity and UIDs
// are never taken from the client: the server records who asked and the UIDs
// it read.
type cnpgRestoreValidationParams struct {
	Checked    string `json:"checked"`
	TargetTime string `json:"targetTime,omitempty"`
	Source     *struct {
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
	} `json:"source,omitempty"`
}

func RecordCNPGRestoreValidation(ctx context.Context, dyn dynamic.Interface, namespace, name string, req integration.ActionRequest, recordedBy string, now time.Time) (*CNPGRestoreValidation, error) {
	var params cnpgRestoreValidationParams
	if err := integration.DecodeActionParams(req.Params, &params); err != nil {
		return nil, err
	}
	checked := strings.TrimSpace(params.Checked)
	if checked == "" {
		return nil, integration.RefuseAction(http.StatusBadRequest, "", "params.checked is required: say what you checked")
	}
	if utf8.RuneCountInString(checked) > cnpgRestoreValidationMax {
		return nil, integration.RefuseAction(http.StatusBadRequest, "", "params.checked is longer than %d characters", cnpgRestoreValidationMax)
	}
	if params.TargetTime != "" {
		if _, err := time.Parse(time.RFC3339, params.TargetTime); err != nil {
			return nil, integration.RefuseAction(http.StatusBadRequest, "", "params.targetTime must be RFC 3339")
		}
	}
	cluster, err := dyn.Resource(ClusterGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if string(cluster.GetUID()) != req.UID {
		return nil, integration.ChangedAction(nil, "Cluster %s/%s was deleted and recreated since you reviewed it", namespace, name)
	}
	if cnpgRecoverySpecOf(cluster) == nil {
		return nil, integration.BlockedAction("This Cluster was not bootstrapped from a backup (spec.bootstrap.recovery is not set)")
	}
	note := &CNPGRestoreValidation{
		Version:    1,
		RecordedAt: now.UTC().Format(time.RFC3339),
		RecordedBy: recordedBy,
		Checked:    checked,
		TargetTime: params.TargetTime,
		Target:     CNPGRestoreValidationRef{Namespace: namespace, Name: name, UID: string(cluster.GetUID()), Verified: true},
	}
	if params.Source != nil && params.Source.Name != "" {
		srcNS := params.Source.Namespace
		if srcNS == "" {
			srcNS = namespace
		}
		ref := CNPGRestoreValidationRef{Namespace: srcNS, Name: params.Source.Name}
		if src, err := dyn.Resource(ClusterGVR).Namespace(srcNS).Get(ctx, params.Source.Name, metav1.GetOptions{}); err == nil {
			ref.UID, ref.Verified = string(src.GetUID()), true
		}
		note.Source = &ref
	}
	data, err := json.Marshal(note)
	if err != nil {
		return nil, err
	}
	if err := integration.MergePatchAtVersion(ctx, dyn, ClusterGVR, cluster, map[string]any{
		"metadata": map[string]any{"annotations": map[string]any{cnpgRestoreValidationAnno: string(data)}},
	}); err != nil {
		if apierrors.IsConflict(err) {
			return nil, integration.ChangedAction(nil, "Cluster %s/%s changed while the note was being recorded; try again", namespace, name)
		}
		return nil, err
	}
	return note, nil
}

func (s *Reader) RestoreCapability(ctx context.Context, namespace string) integration.ActionCapability {
	g := cnpgGrantCreateClusters.In(namespace)
	c := integration.CapabilityVerdict("", []string{s.Access.Permission(ctx, g)}, []auth.Grant{g})
	return cnpgOperatorWebhookGuard(s.operatorVerdictFor(ctx, namespace), c)
}
