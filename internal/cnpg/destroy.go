package cnpg

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"slices"
	"sort"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	auth "github.com/skyhook-io/radar/internal/auth"
	integration "github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/cnpg"
	"github.com/skyhook-io/radar/pkg/k8score"
)

const (
	cnpgPVCStatusAnnotation = "cnpg.io/pvcStatus"
	cnpgPVCStatusDetached   = "detached"
	cnpgDetachedClusterUID  = "radar.skyhook.io/detached-cluster-uid"
)

var (
	cnpgGrantDeletePVCs = auth.Grant{Verb: "delete", Resource: "persistentvolumeclaims"}
	cnpgGrantUpdatePVCs = auth.Grant{Verb: "update", Resource: "persistentvolumeclaims"}
	cnpgGrantListPVCs   = auth.Grant{Verb: "list", Resource: "persistentvolumeclaims"}
	grantGetPVCs        = auth.Grant{Verb: "get", Resource: "persistentvolumeclaims"}
	cnpgGrantListJobs   = auth.Grant{Verb: "list", Group: "batch", Resource: "jobs"}
	cnpgGrantDeleteJobs = auth.Grant{Verb: "delete", Group: "batch", Resource: "jobs"}
	cnpgGrantPatchPool  = auth.Grant{Verb: "patch", Group: Group, Resource: "poolers"}

	cnpgExtraActionGrants = map[string]auth.Grant{
		"cancelBackend":    grantCreateExec,
		"terminateBackend": grantCreateExec,
		"destroyInstance":  cnpgGrantDeletePods,
		"poolerPause":      cnpgGrantPatchPool,
		"poolerResume":     cnpgGrantPatchPool,
	}
)

// cnpgDestroyGrants: with keepPVC the PVCs are updated (detached), otherwise
// deleted; the fence on the destroyed name is lifted last (patch clusters).
func cnpgDestroyGrants(keepPVC bool) []auth.Grant {
	pvc := cnpgGrantDeletePVCs
	if keepPVC {
		pvc = cnpgGrantUpdatePVCs
	}
	return []auth.Grant{cnpgGrantDeletePods, cnpgGrantListPVCs, pvc, cnpgGrantListJobs, cnpgGrantDeleteJobs, GrantPatchClusters}
}

// cnpgDestroyPreflight asks the apiserver, as the caller, for every grant the
// sequence needs before its first write, so a missing one refuses the action
// instead of stopping it halfway.
func cnpgDestroyPreflight(ctx context.Context, x *cnpgClusterRun, keepPVC bool) error {
	namespace := x.cluster.GetNamespace()
	for _, g := range cnpgDestroyGrants(keepPVC) {
		resource := g.Resource
		if g.Subresource != "" {
			resource += "/" + g.Subresource
		}
		allowed, apiErr := k8score.CanI(ctx, x.c.Typed, namespace, g.Group, resource, g.Verb)
		switch {
		case apiErr:
			return integration.RefuseAction(http.StatusServiceUnavailable, "", "Could not confirm you may %s; nothing was changed", g.In(namespace).String())
		case !allowed:
			return integration.RefuseAction(http.StatusForbidden, "", "Destroying needs %s; nothing was changed", g.In(namespace).String())
		}
	}
	return nil
}

func cnpgDestroyInstanceBlocker(f CNPGClusterFacts, i CNPGInstanceFact) (string, string) {
	if r := cnpgGuardCommon(f); r != "" {
		return "state_blocked", r
	}
	switch {
	case f.Hibernated:
		return "hibernated", "The cluster is hibernated"
	case i.Pod == f.CurrentPrimary:
		return "primary", "It is the primary: destroying it forces an unplanned failover. Switch over to a standby first"
	case i.Pod == f.TargetPrimary:
		return "switchover_target", "It is the target of a switchover"
	case f.switchoverInFlight() != "", f.Phase == cnpgPhaseSwitchover, f.Phase == cnpgPhaseFailover:
		return "switchover_in_progress", "A switchover or failover is in progress"
	case !f.FencedInstances.Fences(i.Pod):
		return "fence_required", "Fence " + i.Pod + " first: a fenced instance cannot be promoted while it is destroyed"
	}
	return "", ""
}

func cnpgGuardDestroyInstance(f CNPGClusterFacts, i CNPGInstanceFact) string {
	_, reason := cnpgDestroyInstanceBlocker(f, i)
	return reason
}

// cnpgPodLabelledPrimary reads the role the instance manager writes on its
// own Pod, which can lead status.currentPrimary during a promotion.
func cnpgPodLabelledPrimary(pod *corev1.Pod) bool { return cnpg.InstanceRole(pod) == "primary" }

type CNPGReviewedObject struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
}

// CNPGDestroyPVC is one volume the destroy acts on.
type CNPGDestroyPVC struct {
	Name         string `json:"name"`
	UID          string `json:"uid"`
	Role         string `json:"role,omitempty"`
	Tablespace   string `json:"tablespace,omitempty"`
	Owned        bool   `json:"owned"`
	Detached     bool   `json:"detached"`
	Capacity     string `json:"capacity,omitempty"`
	StorageClass string `json:"storageClass,omitempty"`
}

// CNPGDestroyPlan is GET /api/cnpg/clusters/{ns}/{name}/instances/{pod}/destroy-plan.
type CNPGDestroyPlan struct {
	UID          string               `json:"uid"`
	Context      string               `json:"context"`
	Facts        CNPGClusterFacts     `json:"facts"`
	Pod          string               `json:"pod"`
	PodUID       string               `json:"podUID"`
	Role         string               `json:"role"`
	PVCsReadable bool                 `json:"pvcsReadable"`
	PVCReason    string               `json:"pvcReason,omitempty"`
	PVCs         []CNPGDestroyPVC     `json:"pvcs"`
	JobsReadable bool                 `json:"jobsReadable"`
	Jobs         []CNPGReviewedObject `json:"jobs"`
	Actions      struct {
		Delete integration.ActionCapability `json:"delete"`
		Keep   integration.ActionCapability `json:"keep"`
	} `json:"actions"`
}

// cnpgOwnedByCluster matches upstream's IsOwnedByCluster, tightened to this
// Cluster's UID so a same-named predecessor's leftovers are not taken.
func cnpgOwnedByCluster(refs []metav1.OwnerReference, cluster string, uid types.UID) bool {
	for _, ref := range refs {
		if ref.Kind != "Cluster" || ref.Name != cluster || ref.UID != uid {
			continue
		}
		if gv, err := schema.ParseGroupVersion(ref.APIVersion); err == nil && gv.Group == Group {
			return true
		}
	}
	return false
}

// cnpgInstancePVCs lists the instance's PVCs as upstream's GetInstancePVCs
// does (instance label plus a PVC role) and keeps those upstream would act
// on: owned by the Cluster, or detached by an earlier keep-pvc destroy.
func cnpgInstancePVCs(ctx context.Context, typed kubernetes.Interface, namespace, cluster string, clusterUID types.UID, instance string) ([]corev1.PersistentVolumeClaim, error) {
	role, err := labels.NewRequirement(cnpgPVCRoleLabel, selection.In, []string{"PG_DATA", "PG_WAL", "PG_TABLESPACE"})
	if err != nil {
		return nil, err
	}
	inst, err := labels.NewRequirement(instanceNameLabel, selection.Equals, []string{instance})
	if err != nil {
		return nil, err
	}
	list, err := typed.CoreV1().PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{LabelSelector: labels.NewSelector().Add(*inst, *role).String()})
	if err != nil {
		return nil, err
	}
	out := []corev1.PersistentVolumeClaim{}
	for _, p := range list.Items {
		owned := cnpgOwnedByCluster(p.OwnerReferences, cluster, clusterUID)
		detached := p.Annotations[cnpgPVCStatusAnnotation] == cnpgPVCStatusDetached && p.Annotations[cnpgDetachedClusterUID] == string(clusterUID) && clusterUID != "" && p.Labels[instanceNameLabel] == instance
		if owned || detached {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func cnpgDestroyPVCOf(p corev1.PersistentVolumeClaim, cluster string, clusterUID types.UID) CNPGDestroyPVC {
	out := CNPGDestroyPVC{
		Name: p.Name, UID: string(p.UID), Role: p.Labels[cnpgPVCRoleLabel], Tablespace: p.Labels[cnpgTablespaceNameLabel],
		Owned:    cnpgOwnedByCluster(p.OwnerReferences, cluster, clusterUID),
		Detached: p.Annotations[cnpgPVCStatusAnnotation] == cnpgPVCStatusDetached,
	}
	if q, ok := p.Status.Capacity[corev1.ResourceStorage]; ok {
		out.Capacity = q.String()
	}
	if p.Spec.StorageClassName != nil {
		out.StorageClass = *p.Spec.StorageClassName
	}
	return out
}

func cnpgInstanceJobs(ctx context.Context, typed kubernetes.Interface, namespace, cluster string, clusterUID types.UID, instance string) ([]batchv1.Job, error) {
	list, err := typed.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{LabelSelector: labels.Set{instanceNameLabel: instance}.String()})
	if err != nil {
		return nil, err
	}
	jobs := make([]batchv1.Job, 0, len(list.Items))
	for _, j := range list.Items {
		if cnpgOwnedByCluster(j.OwnerReferences, cluster, clusterUID) {
			jobs = append(jobs, j)
		}
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].Name < jobs[j].Name })
	return jobs, nil
}

func (s *Reader) DestroyPlan(callerCtx context.Context, c ActionClients, contextName, namespace, name, pod string) (*CNPGDestroyPlan, error) {
	ctx := callerCtx
	cluster, err := c.Dynamic.Resource(ClusterGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	facts, _ := cnpgClusterFactsOf(ctx, c.Typed, cluster)
	inst, ok := facts.instance(pod)
	if !ok {
		return nil, integration.RefuseAction(http.StatusNotFound, "", "%s is not an instance of Cluster %s/%s", pod, namespace, name)
	}
	plan := &CNPGDestroyPlan{
		UID: string(cluster.GetUID()), Context: contextName, Facts: facts,
		Pod: pod, PodUID: inst.PodUID, Role: inst.Role, PVCs: []CNPGDestroyPVC{}, Jobs: []CNPGReviewedObject{},
	}
	pvcs, err := cnpgInstancePVCs(ctx, c.Typed, namespace, name, cluster.GetUID(), pod)
	if err == nil {
		plan.PVCsReadable = true
		for _, p := range pvcs {
			plan.PVCs = append(plan.PVCs, cnpgDestroyPVCOf(p, name, cluster.GetUID()))
		}
	} else {
		plan.PVCReason = cnpgReadReason(err)
	}
	if jobs, err := cnpgInstanceJobs(ctx, c.Typed, namespace, name, cluster.GetUID(), pod); err == nil {
		plan.JobsReadable = true
		for _, j := range jobs {
			plan.Jobs = append(plan.Jobs, CNPGReviewedObject{Name: j.Name, UID: string(j.UID)})
		}
	}
	reasonCode, guard := cnpgDestroyInstanceBlocker(facts, inst)
	if guard == "" && !plan.JobsReadable {
		guard = "The instance's Jobs cannot be read, so they cannot be reviewed"
		reasonCode = "jobs_unreadable"
	}
	if guard == "" && !plan.PVCsReadable {
		guard = "The instance's PVCs cannot be read (" + plan.PVCReason + "), so they cannot be reviewed"
		reasonCode = "pvcs_unreadable"
	}
	cap := func(keep bool) integration.ActionCapability {
		gs := cnpgDestroyGrants(keep)
		ps := make([]string, len(gs))
		for i, g := range gs {
			gs[i] = g.In(namespace)
			ps[i] = s.Access.Permission(callerCtx, gs[i])
		}
		actionGuard, actionCode := guard, reasonCode
		if keep && actionGuard == "" {
			actionGuard = cnpgKeepPVCBlocker(pvcs, name, cluster.GetUID())
			if actionGuard != "" {
				actionCode = "owners_remain"
			}
		}
		capability := integration.CapabilityVerdict(actionGuard, ps, gs)
		if capability.Permission != integration.PermissionDenied {
			capability.ReasonCode = actionCode
		}
		return capability
	}
	plan.Actions.Delete = cap(false)
	plan.Actions.Keep = cap(true)
	return plan, nil
}

func cnpgKeepPVCBlocker(pvcs []corev1.PersistentVolumeClaim, cluster string, uid types.UID) string {
	for _, pvc := range pvcs {
		var owners []string
		for _, ref := range pvc.OwnerReferences {
			if !cnpgOwnedByCluster([]metav1.OwnerReference{ref}, cluster, uid) {
				owners = append(owners, ref.Kind+" "+ref.Name+" ("+ref.APIVersion+", UID "+string(ref.UID)+")")
			}
		}
		if len(owners) > 0 {
			return "Keeping PVC " + pvc.Name + " would leave other owners able to garbage-collect it: " + strings.Join(owners, ", ") + ". Review its owner references first"
		}
	}
	return ""
}

func cnpgReadReason(err error) string {
	if apierrors.IsForbidden(err) {
		return "no access"
	}
	return cnpgPlainReadError(err.Error())
}

type cnpgDestroyParams struct {
	Pod     string                `json:"pod"`
	PodUID  string                `json:"podUID"`
	KeepPVC bool                  `json:"keepPVC"`
	PVCs    *[]CNPGReviewedObject `json:"pvcs"`
	Jobs    *[]CNPGReviewedObject `json:"jobs"`
}

func cnpgRunDestroyInstance(ctx context.Context, x *cnpgClusterRun) (*CNPGActionResult, error) {
	var p cnpgDestroyParams
	if err := integration.DecodeActionParams(x.params, &p); err != nil {
		return nil, err
	}
	if p.Pod == "" || p.PVCs == nil || p.Jobs == nil {
		return nil, integration.RefuseAction(http.StatusBadRequest, "", "params.pod, params.podUID, params.pvcs and params.jobs are required: the confirmation must bind the Pod, volumes and Jobs reviewed")
	}
	inst, ok := x.facts.instance(p.Pod)
	if !ok {
		return nil, integration.ChangedAction(x.facts, "%s is not an instance of this cluster", p.Pod)
	}
	if r := cnpgGuardDestroyInstance(x.facts, inst); r != "" {
		return nil, integration.BlockedAction(r)
	}
	if !inst.PodReadable {
		return nil, integration.RefuseAction(http.StatusForbidden, "", "Pod %s cannot be read, so it cannot be verified as this cluster's instance", p.Pod)
	}
	if inst.PodUID != p.PodUID {
		return nil, integration.ChangedAction(x.facts, "Pod %s changed since you reviewed it", p.Pod)
	}
	if err := cnpgDestroyPreflight(ctx, x, p.KeepPVC); err != nil {
		return nil, err
	}
	namespace, cluster, clusterUID := x.cluster.GetNamespace(), x.cluster.GetName(), x.cluster.GetUID()
	pvcs, err := cnpgInstancePVCs(ctx, x.c.Typed, namespace, cluster, clusterUID, p.Pod)
	if err != nil {
		return nil, err
	}
	now := make([]CNPGReviewedObject, 0, len(pvcs))
	for _, v := range pvcs {
		now = append(now, CNPGReviewedObject{Name: v.Name, UID: string(v.UID)})
	}
	if !cnpgSameReviewedObjects(now, *p.PVCs) {
		return nil, integration.ChangedAction(x.facts, "The volumes of %s changed since you reviewed them (now: %s); review the action again", p.Pod, cnpgPVCList(now))
	}

	if p.KeepPVC {
		if reason := cnpgKeepPVCBlocker(pvcs, cluster, clusterUID); reason != "" {
			return nil, integration.BlockedAction(reason)
		}
	}
	jobs, err := cnpgInstanceJobs(ctx, x.c.Typed, namespace, cluster, clusterUID, p.Pod)
	if err != nil {
		return nil, err
	}
	reviewedJobs := make([]CNPGReviewedObject, 0, len(jobs))
	jobNames := make([]string, 0, len(jobs))
	for _, j := range jobs {
		reviewedJobs = append(reviewedJobs, CNPGReviewedObject{Name: j.Name, UID: string(j.UID)})
		jobNames = append(jobNames, j.Name)
	}
	if !cnpgSameReviewedObjects(reviewedJobs, *p.Jobs) {
		return nil, integration.ChangedAction(x.facts, "The Jobs of %s changed since you reviewed them; review the action again", p.Pod)
	}

	// CloudNativePG offers no lock against a failover, so the instance is
	// re-verified as a standby before every destructive step: the window in
	// which a promotion can land is one request, not the whole sequence.
	var completed []string
	podDeleted := false
	stop := func(err error) error {
		if len(completed) == 0 {
			return err
		}
		return integration.PartialAction(completed, err)
	}
	recheck := func() error {
		fresh, err := x.c.Dynamic.Resource(ClusterGVR).Namespace(namespace).Get(ctx, cluster, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("re-reading Cluster %s/%s: %w", namespace, cluster, err)
		}
		facts, _ := cnpgClusterFactsOf(ctx, nil, fresh)
		if fresh.GetUID() != clusterUID {
			return integration.ChangedAction(facts, "Cluster %s/%s was deleted and recreated; nothing further was done", namespace, cluster)
		}
		if r := cnpgGuardDestroyInstance(facts, CNPGInstanceFact{Pod: p.Pod}); r != "" {
			return integration.ChangedAction(facts, "%s can no longer be destroyed: %s", p.Pod, r)
		}
		pod, err := x.c.Typed.CoreV1().Pods(namespace).Get(ctx, p.Pod, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			return nil
		case err != nil:
			return fmt.Errorf("re-reading Pod %s: %w", p.Pod, err)
		case podDeleted:
			return nil
		case string(pod.UID) != p.PodUID:
			return integration.ChangedAction(facts, "Pod %s was replaced since you reviewed it", p.Pod)
		case cnpgPodLabelledPrimary(pod):
			return integration.ChangedAction(facts, "%s is now labelled primary: it can no longer be destroyed", p.Pod)
		}
		return nil
	}

	if p.KeepPVC {
		for i := range pvcs {
			pvc := &pvcs[i]
			if !cnpgOwnedByCluster(pvc.OwnerReferences, cluster, clusterUID) {
				continue
			}
			if err := recheck(); err != nil {
				return nil, stop(err)
			}
			refs := pvc.OwnerReferences[:0:0]
			for _, ref := range pvc.OwnerReferences {
				if !cnpgOwnedByCluster([]metav1.OwnerReference{ref}, cluster, clusterUID) {
					refs = append(refs, ref)
				}
			}
			pvc.OwnerReferences = refs
			if pvc.Annotations == nil {
				pvc.Annotations = map[string]string{}
			}
			if pvc.Labels == nil {
				pvc.Labels = map[string]string{}
			}
			pvc.Annotations[cnpgPVCStatusAnnotation] = cnpgPVCStatusDetached
			pvc.Annotations[cnpgDetachedClusterUID] = string(clusterUID)
			pvc.Labels[instanceNameLabel] = p.Pod
			if _, err := x.c.Typed.CoreV1().PersistentVolumeClaims(namespace).Update(ctx, pvc, metav1.UpdateOptions{}); err != nil {
				return nil, stop(fmt.Errorf("detaching PVC %s: %w", pvc.Name, err))
			}
			completed = append(completed, "detached PVC "+pvc.Name)
		}
	} else {
		for _, pvc := range pvcs {
			if err := recheck(); err != nil {
				return nil, stop(err)
			}
			uid := pvc.UID
			err := x.c.Typed.CoreV1().PersistentVolumeClaims(namespace).Delete(ctx, pvc.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
			if err != nil && !apierrors.IsNotFound(err) {
				return nil, stop(fmt.Errorf("deleting PVC %s: %w", pvc.Name, err))
			}
			completed = append(completed, "deleted PVC "+pvc.Name)
		}
	}

	if inst.PodExists {
		if err := recheck(); err != nil {
			return nil, stop(err)
		}
		uid := types.UID(p.PodUID)
		err := x.c.Typed.CoreV1().Pods(namespace).Delete(ctx, p.Pod, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
		if err != nil && !apierrors.IsNotFound(err) {
			return nil, stop(fmt.Errorf("deleting Pod %s: %w", p.Pod, err))
		}
		completed = append(completed, "deleted Pod "+p.Pod)
		podDeleted = true
	}

	background := metav1.DeletePropagationBackground
	for _, reviewed := range jobs {
		job := reviewed
		for attempt := 0; attempt < 2; attempt++ {
			if err := recheck(); err != nil {
				return nil, stop(err)
			}
			uid, rv := job.UID, job.ResourceVersion
			err := x.c.Typed.BatchV1().Jobs(namespace).Delete(ctx, job.Name, metav1.DeleteOptions{PropagationPolicy: &background, Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv}})
			if err == nil || apierrors.IsNotFound(err) {
				break
			}
			if attempt == 0 && apierrors.IsConflict(err) {
				// Status updates change the version without changing the reviewed
				// identity. Re-list with the existing grant before one bounded retry.
				fresh, readErr := cnpgInstanceJobs(ctx, x.c.Typed, namespace, cluster, clusterUID, p.Pod)
				if readErr != nil {
					return nil, stop(fmt.Errorf("re-reading Jobs: %w", readErr))
				}
				index := slices.IndexFunc(fresh, func(j batchv1.Job) bool { return j.Name == reviewed.Name && j.UID == reviewed.UID })
				if index < 0 {
					return nil, stop(integration.ChangedAction(x.facts, "Job %s was replaced or is no longer owned by this instance", reviewed.Name))
				}
				job = fresh[index]
				continue
			}
			return nil, stop(fmt.Errorf("deleting Job %s: %w", job.Name, err))
		}
		completed = append(completed, "deleted Job "+reviewed.Name)
	}

	lifted, err := cnpgLiftDestroyedFence(ctx, x, p.Pod)
	if err != nil {
		return nil, stop(fmt.Errorf("lifting the fence on %s (lift it with Unfence): %w", p.Pod, err))
	}
	if lifted {
		completed = append(completed, "lifted the fence on "+p.Pod)
	}
	log.Printf("[cnpg] destroyed instance %s of %s/%s (keepPVC=%v, pvcs=%d, jobs=%d)", k8s.SanitizeForLog(p.Pod), k8s.SanitizeForLog(namespace), k8s.SanitizeForLog(cluster), p.KeepPVC, len(pvcs), len(jobs))

	keep := p.KeepPVC
	msg := fmt.Sprintf("Instance %s destroyed; the operator creates a replacement instance", p.Pod)
	if keep {
		msg = fmt.Sprintf("Instance %s destroyed and its volumes kept, detached; the operator creates a replacement instance", p.Pod)
	}
	if !lifted {
		msg += `. The cluster-wide fence ["*"] stays in place`
	}
	return &CNPGActionResult{
		Message: msg,
		Target:  &CNPGActionTarget{Pod: p.Pod, PodUID: p.PodUID, KeepPVC: &keep, PVCs: now, Jobs: jobNames},
	}, nil
}

func cnpgSameReviewedObjects(a, b []CNPGReviewedObject) bool {
	key := func(l []CNPGReviewedObject) []string {
		out := make([]string, len(l))
		for i, v := range l {
			out[i] = v.Name + "\x00" + v.UID
		}
		return out
	}
	return cnpgSameStringSet(key(a), key(b))
}

func cnpgPVCList(l []CNPGReviewedObject) string {
	if len(l) == 0 {
		return "none"
	}
	names := make([]string, len(l))
	for i, v := range l {
		names[i] = v.Name
	}
	return strings.Join(names, ", ")
}

// cnpgLiftDestroyedFence removes the destroyed name from a list-form fence,
// against the Cluster as it is now. A ["*"] fence is the user's, not this
// action's, and stays.
func cnpgLiftDestroyedFence(ctx context.Context, x *cnpgClusterRun, pod string) (bool, error) {
	fresh, err := x.c.Dynamic.Resource(ClusterGVR).Namespace(x.cluster.GetNamespace()).Get(ctx, x.cluster.GetName(), metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	if fresh.GetUID() != x.cluster.GetUID() {
		return false, fmt.Errorf("Cluster %s/%s was deleted and recreated; its fences were left alone", x.cluster.GetNamespace(), x.cluster.GetName())
	}
	f := parseCNPGFenced(fresh.GetAnnotations()[cnpgFencedAnnotation])
	if f.All || f.Malformed || !slices.Contains(f.Instances, pod) {
		return false, nil
	}
	rest := make([]string, 0, len(f.Instances))
	for _, n := range f.Instances {
		if n != pod {
			rest = append(rest, n)
		}
	}
	value, err := cnpgFencedValue(false, rest)
	if err != nil {
		return false, err
	}
	err = integration.MergePatchAtVersion(ctx, x.c.Dynamic, ClusterGVR, fresh, map[string]any{
		"metadata": map[string]any{"annotations": map[string]any{cnpgFencedAnnotation: value}},
	})
	return err == nil, err
}
