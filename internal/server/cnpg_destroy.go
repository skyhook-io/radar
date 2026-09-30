package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// Destroying an instance mirrors `kubectl cnpg destroy CLUSTER INSTANCE
// [--keep-pvc]`: the instance's PVCs are detached (keep) or deleted first, so
// the operator never sees a dangling PVC and recreates the Pod on it; then the
// Pod is deleted, then the instance's Jobs. The operator replaces the instance
// with a new one under a new serial. Unlike upstream, Radar refuses the
// primary (an unplanned failover, which a switchover does safely) and
// requires the instance to be fenced first: the operator never promotes a
// fenced instance, which is the only thing that keeps a failover from making
// it primary between the last check and a delete. The fence on the destroyed
// name is lifted last.

const (
	cnpgPVCStatusAnnotation = "cnpg.io/pvcStatus"
	cnpgPVCStatusDetached   = "detached"
)

var (
	cnpgGrantDeletePVCs = cnpgGrant{"delete", "", "persistentvolumeclaims", ""}
	cnpgGrantUpdatePVCs = cnpgGrant{"update", "", "persistentvolumeclaims", ""}
	cnpgGrantListPVCs   = cnpgGrant{"list", "", "persistentvolumeclaims", ""}
	cnpgGrantListJobs   = cnpgGrant{"list", "batch", "jobs", ""}
	cnpgGrantDeleteJobs = cnpgGrant{"delete", "batch", "jobs", ""}
	cnpgGrantPatchPool  = cnpgGrant{"patch", cnpgGroup, "poolers", ""}

	cnpgExtraActionGrants = map[string]cnpgGrant{
		"cancelBackend":    cnpgGrantCreateExec,
		"terminateBackend": cnpgGrantCreateExec,
		"destroyInstance":  cnpgGrantDeletePods,
		"poolerPause":      cnpgGrantPatchPool,
		"poolerResume":     cnpgGrantPatchPool,
	}
)

// cnpgDestroyGrants: with keepPVC the PVCs are updated (detached), otherwise
// deleted.
func cnpgDestroyGrants(keepPVC bool) []cnpgGrant {
	pvc := cnpgGrantDeletePVCs
	if keepPVC {
		pvc = cnpgGrantUpdatePVCs
	}
	return []cnpgGrant{cnpgGrantDeletePods, cnpgGrantListPVCs, pvc, cnpgGrantListJobs, cnpgGrantDeleteJobs}
}

func cnpgGuardDestroyInstance(f CNPGClusterFacts, i CNPGInstanceFact) string {
	if r := cnpgGuardCommon(f); r != "" {
		return r
	}
	switch {
	case f.Hibernated:
		return "The cluster is hibernated"
	case i.Pod == f.CurrentPrimary:
		return "It is the primary: destroying it forces an unplanned failover. Switch over to a standby first"
	case i.Pod == f.TargetPrimary:
		return "It is the target of a switchover"
	case f.switchoverInFlight() != "", f.Phase == cnpgPhaseSwitchover, f.Phase == cnpgPhaseFailover:
		return "A switchover or failover is in progress"
	case !f.FencedInstances.fences(i.Pod):
		return "Fence " + i.Pod + " first: a fenced instance cannot be promoted while it is destroyed"
	}
	return ""
}

// cnpgPodLabelledPrimary reads the role the instance manager writes on its
// own Pod, which can lead status.currentPrimary during a promotion.
func cnpgPodLabelledPrimary(pod *corev1.Pod) bool {
	role := pod.Labels["cnpg.io/instanceRole"]
	if role == "" {
		role = pod.Labels["role"]
	}
	return role == "primary"
}

type cnpgReviewedPVC struct {
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
	UID          string           `json:"uid"`
	Context      string           `json:"context"`
	Facts        CNPGClusterFacts `json:"facts"`
	Pod          string           `json:"pod"`
	PodUID       string           `json:"podUID"`
	Role         string           `json:"role"`
	PVCsReadable bool             `json:"pvcsReadable"`
	PVCReason    string           `json:"pvcReason,omitempty"`
	PVCs         []CNPGDestroyPVC `json:"pvcs"`
	JobsReadable bool             `json:"jobsReadable"`
	Jobs         []string         `json:"jobs"`
	Actions      struct {
		Delete CNPGActionCapability `json:"delete"`
		Keep   CNPGActionCapability `json:"keep"`
	} `json:"actions"`
}

// cnpgOwnedByCluster matches upstream's IsOwnedByCluster, tightened to this
// Cluster's UID so a same-named predecessor's leftovers are not taken.
func cnpgOwnedByCluster(refs []metav1.OwnerReference, cluster string, uid types.UID) bool {
	for _, ref := range refs {
		if ref.Kind != "Cluster" || ref.Name != cluster || ref.UID != uid {
			continue
		}
		if gv, err := schema.ParseGroupVersion(ref.APIVersion); err == nil && gv.Group == cnpgGroup {
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
	inst, err := labels.NewRequirement(cnpgInstanceNameLabel, selection.Equals, []string{instance})
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
		detached := p.Annotations[cnpgPVCStatusAnnotation] == cnpgPVCStatusDetached && p.Labels[cnpgInstanceNameLabel] == instance
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

func cnpgInstanceJobs(ctx context.Context, typed kubernetes.Interface, namespace, instance string) ([]string, error) {
	list, err := typed.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{LabelSelector: labels.Set{cnpgInstanceNameLabel: instance}.String()})
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(list.Items))
	for _, j := range list.Items {
		names = append(names, j.Name)
	}
	sort.Strings(names)
	return names, nil
}

func (s *Server) handleCNPGDestroyPlan(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespace, name, pod := chi.URLParam(r, "namespace"), chi.URLParam(r, "name"), chi.URLParam(r, "pod")
	dyn, contextName := s.getDynamicClientSnapshotForRequest(r)
	typed := s.getClientForRequest(r)
	if dyn == nil || typed == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	plan, err := s.cnpgDestroyPlan(r, cnpgActionClients{dyn: dyn, typed: typed}, contextName, namespace, name, pod)
	if err != nil {
		s.writeCNPGActionError(w, err, "destroyInstance", namespace, name)
		return
	}
	s.writeJSON(w, plan)
}

func (s *Server) cnpgDestroyPlan(r *http.Request, c cnpgActionClients, contextName, namespace, name, pod string) (*CNPGDestroyPlan, error) {
	ctx := r.Context()
	cluster, err := c.dyn.Resource(cnpgClusterGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	facts, _ := cnpgClusterFactsOf(ctx, c.typed, cluster)
	inst, ok := facts.instance(pod)
	if !ok {
		return nil, cnpgRefuse(http.StatusNotFound, "", "%s is not an instance of Cluster %s/%s", pod, namespace, name)
	}
	plan := &CNPGDestroyPlan{
		UID: string(cluster.GetUID()), Context: contextName, Facts: facts,
		Pod: pod, PodUID: inst.PodUID, Role: inst.Role, PVCs: []CNPGDestroyPVC{}, Jobs: []string{},
	}
	if pvcs, err := cnpgInstancePVCs(ctx, c.typed, namespace, name, cluster.GetUID(), pod); err == nil {
		plan.PVCsReadable = true
		for _, p := range pvcs {
			plan.PVCs = append(plan.PVCs, cnpgDestroyPVCOf(p, name, cluster.GetUID()))
		}
	} else {
		plan.PVCReason = cnpgReadReason(err)
	}
	if jobs, err := cnpgInstanceJobs(ctx, c.typed, namespace, pod); err == nil {
		plan.JobsReadable = true
		plan.Jobs = jobs
	}
	guard := cnpgGuardDestroyInstance(facts, inst)
	if guard == "" && !plan.PVCsReadable {
		guard = "The instance's PVCs cannot be read (" + plan.PVCReason + "), so they cannot be reviewed"
	}
	cap := func(keep bool) CNPGActionCapability {
		gs := cnpgDestroyGrants(keep)
		ps := make([]string, len(gs))
		for i, g := range gs {
			ps[i] = s.cnpgPermission(r, g, namespace)
		}
		return cnpgCapability(guard, namespace, ps, gs)
	}
	plan.Actions.Delete = cap(false)
	plan.Actions.Keep = cap(true)
	return plan, nil
}

func cnpgReadReason(err error) string {
	if apierrors.IsForbidden(err) {
		return "no access"
	}
	return cnpgPlainReadError(err.Error())
}

type cnpgDestroyParams struct {
	Pod     string             `json:"pod"`
	PodUID  string             `json:"podUID"`
	KeepPVC bool               `json:"keepPVC"`
	PVCs    *[]cnpgReviewedPVC `json:"pvcs"`
}

func cnpgRunDestroyInstance(ctx context.Context, x *cnpgClusterRun) (*CNPGActionResult, error) {
	var p cnpgDestroyParams
	if err := decodeCNPGParams(x.params, &p); err != nil {
		return nil, err
	}
	if p.Pod == "" || p.PVCs == nil {
		return nil, cnpgRefuse(http.StatusBadRequest, "", "params.pod, params.podUID and params.pvcs are required: the confirmation must bind the Pod and volumes reviewed")
	}
	inst, ok := x.facts.instance(p.Pod)
	if !ok {
		return nil, cnpgChanged(x.facts, "%s is not an instance of this cluster", p.Pod)
	}
	if r := cnpgGuardDestroyInstance(x.facts, inst); r != "" {
		return nil, cnpgBlocked(r)
	}
	if !inst.PodReadable {
		return nil, cnpgRefuse(http.StatusForbidden, "", "Pod %s cannot be read, so it cannot be verified as this cluster's instance", p.Pod)
	}
	if inst.PodUID != p.PodUID {
		return nil, cnpgChanged(x.facts, "Pod %s changed since you reviewed it", p.Pod)
	}
	namespace, cluster, clusterUID := x.cluster.GetNamespace(), x.cluster.GetName(), x.cluster.GetUID()
	pvcs, err := cnpgInstancePVCs(ctx, x.c.typed, namespace, cluster, clusterUID, p.Pod)
	if err != nil {
		return nil, err
	}
	now := make([]cnpgReviewedPVC, 0, len(pvcs))
	for _, v := range pvcs {
		now = append(now, cnpgReviewedPVC{Name: v.Name, UID: string(v.UID)})
	}
	if !cnpgSamePVCs(now, *p.PVCs) {
		return nil, cnpgChanged(x.facts, "The volumes of %s changed since you reviewed them (now: %s); review the action again", p.Pod, cnpgPVCList(now))
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
		return cnpgPartial(completed, err)
	}
	recheck := func() error {
		fresh, err := x.c.dyn.Resource(cnpgClusterGVR).Namespace(namespace).Get(ctx, cluster, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("re-reading Cluster %s/%s: %w", namespace, cluster, err)
		}
		facts, _ := cnpgClusterFactsOf(ctx, nil, fresh)
		if fresh.GetUID() != clusterUID {
			return cnpgChanged(facts, "Cluster %s/%s was deleted and recreated; nothing further was done", namespace, cluster)
		}
		if r := cnpgGuardDestroyInstance(facts, CNPGInstanceFact{Pod: p.Pod}); r != "" {
			return cnpgChanged(facts, "%s can no longer be destroyed: %s", p.Pod, r)
		}
		pod, err := x.c.typed.CoreV1().Pods(namespace).Get(ctx, p.Pod, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			return nil
		case err != nil:
			return fmt.Errorf("re-reading Pod %s: %w", p.Pod, err)
		case podDeleted:
			return nil
		case string(pod.UID) != p.PodUID:
			return cnpgChanged(facts, "Pod %s was replaced since you reviewed it", p.Pod)
		case cnpgPodLabelledPrimary(pod):
			return cnpgChanged(facts, "%s is now labelled primary: it can no longer be destroyed", p.Pod)
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
				if !(ref.Kind == "Cluster" && ref.Name == cluster) {
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
			pvc.Labels[cnpgInstanceNameLabel] = p.Pod
			if _, err := x.c.typed.CoreV1().PersistentVolumeClaims(namespace).Update(ctx, pvc, metav1.UpdateOptions{}); err != nil {
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
			err := x.c.typed.CoreV1().PersistentVolumeClaims(namespace).Delete(ctx, pvc.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
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
		err := x.c.typed.CoreV1().Pods(namespace).Delete(ctx, p.Pod, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
		if err != nil && !apierrors.IsNotFound(err) {
			return nil, stop(fmt.Errorf("deleting Pod %s: %w", p.Pod, err))
		}
		completed = append(completed, "deleted Pod "+p.Pod)
		podDeleted = true
	}

	jobs, err := cnpgInstanceJobs(ctx, x.c.typed, namespace, p.Pod)
	if err != nil {
		return nil, stop(fmt.Errorf("listing the instance's Jobs: %w", err))
	}
	if len(jobs) > 0 {
		if err := recheck(); err != nil {
			return nil, stop(err)
		}
	}
	background := metav1.DeletePropagationBackground
	for _, j := range jobs {
		if err := x.c.typed.BatchV1().Jobs(namespace).Delete(ctx, j, metav1.DeleteOptions{PropagationPolicy: &background}); err != nil && !apierrors.IsNotFound(err) {
			return nil, stop(fmt.Errorf("deleting Job %s: %w", j, err))
		}
		completed = append(completed, "deleted Job "+j)
	}

	lifted, err := cnpgLiftDestroyedFence(ctx, x, p.Pod)
	if err != nil {
		return nil, stop(fmt.Errorf("lifting the fence on %s (lift it with Unfence): %w", p.Pod, err))
	}
	if lifted {
		completed = append(completed, "lifted the fence on "+p.Pod)
	}
	log.Printf("[cnpg] destroyed instance %s of %s/%s (keepPVC=%v, pvcs=%d, jobs=%d)", sanitizeForLog(p.Pod), sanitizeForLog(namespace), sanitizeForLog(cluster), p.KeepPVC, len(pvcs), len(jobs))

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
		Target:  &CNPGActionTarget{Pod: p.Pod, PodUID: p.PodUID, KeepPVC: &keep, PVCs: now, Jobs: jobs},
	}, nil
}

func cnpgSamePVCs(a, b []cnpgReviewedPVC) bool {
	key := func(l []cnpgReviewedPVC) []string {
		out := make([]string, len(l))
		for i, v := range l {
			out[i] = v.Name + "\x00" + v.UID
		}
		return out
	}
	return cnpgSameStringSet(key(a), key(b))
}

func cnpgPVCList(l []cnpgReviewedPVC) string {
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
	fresh, err := x.c.dyn.Resource(cnpgClusterGVR).Namespace(x.cluster.GetNamespace()).Get(ctx, x.cluster.GetName(), metav1.GetOptions{})
	if err != nil {
		return false, err
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
	err = cnpgMergePatch(ctx, x.c.dyn, cnpgClusterGVR, fresh, map[string]any{
		"metadata": map[string]any{"annotations": map[string]any{cnpgFencedAnnotation: value}},
	})
	return err == nil, err
}
