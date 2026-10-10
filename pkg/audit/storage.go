package audit

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/timeutil"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	pvcReviewAge        = 24 * time.Hour
	pvDeleteGrace       = time.Hour
	pvRetainGrace       = 24 * time.Hour
	pvDeleteEventWindow = 24 * time.Hour
)

func checkStorage(tr *evalTracker, input *CheckInput, now time.Time) []Finding {
	if input.PersistentVolumeClaims == nil {
		tr.missingInputs = append(tr.missingInputs, "persistentvolumeclaims")
	}
	consumerNamespaces := map[string]bool{}
	for _, ns := range input.PVCConsumerNamespaces {
		consumerNamespaces[ns] = true
	}
	pvs := map[string]*corev1.PersistentVolume{}
	for _, pv := range input.PersistentVolumes {
		pvs[pv.Name] = pv
	}
	classes := map[string]*storagev1.StorageClass{}
	for _, sc := range input.StorageClasses {
		classes[sc.Name] = sc
	}
	consumers := collectPVCConsumers(input)
	var findings []Finding
	for _, pvc := range input.PersistentVolumeClaims {
		if pvc.DeletionTimestamp != nil || hasControllerOwnerReference(pvc.OwnerReferences) || pvc.CreationTimestamp.IsZero() || now.Sub(pvc.CreationTimestamp.Time) <= pvcReviewAge {
			continue
		}
		if pvc.Status.Phase != corev1.ClaimBound && pvc.Status.Phase != corev1.ClaimPending {
			continue
		}
		consumed := consumers[pvc.Namespace+"/"+pvc.Name]
		complete := consumerNamespaces[pvc.Namespace] && input.Pods != nil && input.Deployments != nil && input.ReplicaSets != nil && input.StatefulSets != nil && input.DaemonSets != nil && input.Jobs != nil && input.CronJobs != nil
		if !consumed && !complete {
			tr.missingInputs = append(tr.missingInputs, "pvc-consumers")
			continue
		}
		checkID, message := "", ""
		switch pvc.Status.Phase {
		case corev1.ClaimBound:
			tr.record("pvcNoConsumer", pvc.Namespace)
			if consumed {
				continue
			}
			if input.PersistentVolumes == nil && pvc.Spec.VolumeName != "" {
				tr.missingInputs = append(tr.missingInputs, "persistentvolumes")
			}
			checkID = "pvcNoConsumer"
			message = "No consumer observed among Pods or built-in workload templates Radar can see. CRD consumers may still reference this claim."
		case corev1.ClaimPending:
			tr.record("pvcLongPending", pvc.Namespace)
			if consumed {
				continue
			}
			checkID = "pvcLongPending"
			message = "Pending since creation, " + storageAge(pvc.CreationTimestamp, now) + " ago. No consumer observed among Pods or built-in workload templates Radar can see; CRD consumers may still reference it."
			if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != "" {
				var sc *storagev1.StorageClass
				if pvc.Spec.StorageClassName != nil {
					sc = classes[*pvc.Spec.StorageClassName]
				}
				if sc == nil {
					if input.StorageClasses == nil {
						tr.missingInputs = append(tr.missingInputs, "storageclasses")
					}
					tr.missingInputs = append(tr.missingInputs, "pvc-binding-mode")
					message += " StorageClass binding mode not visible."
				} else if sc.VolumeBindingMode != nil && *sc.VolumeBindingMode == storagev1.VolumeBindingWaitForFirstConsumer {
					message += " StorageClass uses WaitForFirstConsumer."
				}
			}
		}
		findings = append(findings, Finding{Kind: "PersistentVolumeClaim", Namespace: pvc.Namespace, Name: pvc.Name, CheckID: checkID, Category: CategoryEfficiency, Severity: SeverityWarning, Message: message + " " + pvcStorageDetails(pvc, pvs[pvc.Spec.VolumeName], now)})
	}
	deleteFailures := latestPVDeleteFailures(input.PersistentVolumes, input.Events, now)
	for _, pv := range input.PersistentVolumes {
		tr.record("releasedPV", "")
		if pv.Status.Phase != corev1.VolumeReleased {
			continue
		}
		releasedAt := pv.CreationTimestamp
		if pv.Status.LastPhaseTransitionTime != nil && !pv.Status.LastPhaseTransitionTime.IsZero() {
			releasedAt = *pv.Status.LastPhaseTransitionTime
		}
		message := ""
		switch pv.Spec.PersistentVolumeReclaimPolicy {
		case corev1.PersistentVolumeReclaimRetain:
			if releasedAt.IsZero() || now.Sub(releasedAt.Time) <= pvRetainGrace {
				continue
			}
			message = "Released PV is kept after its claim was deleted (reclaim policy Retain)."
		case corev1.PersistentVolumeReclaimDelete:
			failure, hasFailure := deleteFailures[pv.UID]
			if hasFailure && now.Sub(failure.at) <= pvDeleteEventWindow {
				message = "Released PV deletion is failing: " + failure.event.Message
			} else {
				if releasedAt.IsZero() || now.Sub(releasedAt.Time) <= pvDeleteGrace {
					continue
				}
				message = "Released PV deletion has not completed (reclaim policy Delete)."
				if hasFailure {
					message += " Latest deletion warning (" + timeutil.FormatAgeShort(now.Sub(failure.at)) + " ago): " + failure.event.Message
				}
			}
			if input.Events == nil || !input.PVDeletionEventsComplete {
				tr.missingInputs = append(tr.missingInputs, "pv-deletion-events")
			}
		default:
			continue
		}
		age := "PV age " + storageAge(pv.CreationTimestamp, now)
		if pv.Status.LastPhaseTransitionTime != nil && !pv.Status.LastPhaseTransitionTime.IsZero() {
			age = "Released for " + storageAge(*pv.Status.LastPhaseTransitionTime, now)
		}
		formerClaim := "unknown"
		if pv.Spec.ClaimRef != nil {
			formerClaim = pv.Spec.ClaimRef.Namespace + "/" + pv.Spec.ClaimRef.Name
		}
		message += fmt.Sprintf(" Capacity %s; storage class %q; former claim %s; %s.", storageQuantity(pv.Spec.Capacity), pv.Spec.StorageClassName, formerClaim, age)
		findings = append(findings, Finding{Kind: "PersistentVolume", Name: pv.Name, CheckID: "releasedPV", Category: CategoryEfficiency, Severity: SeverityWarning, Message: message})
	}
	slices.Sort(tr.missingInputs)
	tr.missingInputs = slices.Compact(tr.missingInputs)
	return findings
}

func collectPVCConsumers(input *CheckInput) map[string]bool {
	refs := map[string]bool{}
	add := func(ns string, spec corev1.PodSpec) {
		for _, volume := range spec.Volumes {
			if volume.PersistentVolumeClaim != nil {
				refs[ns+"/"+volume.PersistentVolumeClaim.ClaimName] = true
			}
		}
	}
	for _, p := range input.Pods {
		add(p.Namespace, p.Spec)
		for _, v := range p.Spec.Volumes {
			if v.Ephemeral != nil {
				refs[p.Namespace+"/"+p.Name+"-"+v.Name] = true
			}
		}
	}
	for _, d := range input.Deployments {
		add(d.Namespace, d.Spec.Template.Spec)
	}
	for _, rs := range input.ReplicaSets {
		add(rs.Namespace, rs.Spec.Template.Spec)
	}
	for _, ds := range input.DaemonSets {
		add(ds.Namespace, ds.Spec.Template.Spec)
	}
	for _, ss := range input.StatefulSets {
		add(ss.Namespace, ss.Spec.Template.Spec)
	}
	for _, j := range input.Jobs {
		add(j.Namespace, j.Spec.Template.Spec)
	}
	for _, cj := range input.CronJobs {
		add(cj.Namespace, cj.Spec.JobTemplate.Spec.Template.Spec)
	}
	// Match retained ordinals too: scaling to zero does not relinquish data.
	generated := map[string]bool{}
	for _, ss := range input.StatefulSets {
		for _, template := range ss.Spec.VolumeClaimTemplates {
			generated[ss.Namespace+"/"+template.Name+"-"+ss.Name] = true
		}
	}
	for _, pvc := range input.PersistentVolumeClaims {
		cut := strings.LastIndexByte(pvc.Name, '-')
		if cut < 0 || !generated[pvc.Namespace+"/"+pvc.Name[:cut]] {
			continue
		}
		ordinal := pvc.Name[cut+1:]
		if n, err := strconv.ParseUint(ordinal, 10, 32); err == nil && strconv.FormatUint(n, 10) == ordinal {
			refs[pvc.Namespace+"/"+pvc.Name] = true
		}
	}
	return refs
}

func pvcStorageDetails(pvc *corev1.PersistentVolumeClaim, pv *corev1.PersistentVolume, now time.Time) string {
	class, volume, reclaim := "not assigned", "not bound", "not visible"
	if pvc.Spec.StorageClassName != nil {
		class = fmt.Sprintf("%q", *pvc.Spec.StorageClassName)
	}
	if pvc.Spec.VolumeName != "" {
		volume = pvc.Spec.VolumeName
	}
	if pv != nil {
		reclaim = string(pv.Spec.PersistentVolumeReclaimPolicy)
	}
	details := fmt.Sprintf("Requested %s; storage class %s; PVC age %s.", storageQuantity(pvc.Spec.Resources.Requests), class, storageAge(pvc.CreationTimestamp, now))
	if pvc.Status.Phase == corev1.ClaimBound {
		details += fmt.Sprintf(" Bound PV %s; reclaim policy %s.", volume, reclaim)
	}
	return details
}

func storageQuantity(resources corev1.ResourceList) string {
	if size, ok := resources[corev1.ResourceStorage]; ok {
		return size.String()
	}
	return "unknown"
}

func storageAge(created metav1.Time, now time.Time) string {
	if created.IsZero() {
		return "unknown"
	}
	return timeutil.FormatAgeShort(now.Sub(created.Time))
}

type pvDeleteFailure struct {
	event *corev1.Event
	at    time.Time
}

func latestPVDeleteFailures(pvs []*corev1.PersistentVolume, events []*corev1.Event, now time.Time) map[types.UID]pvDeleteFailure {
	volumes := map[types.UID]*corev1.PersistentVolume{}
	for _, pv := range pvs {
		if pv.UID != "" && pv.Status.Phase == corev1.VolumeReleased && pv.Spec.PersistentVolumeReclaimPolicy == corev1.PersistentVolumeReclaimDelete {
			volumes[pv.UID] = pv
		}
	}
	latest := map[types.UID]pvDeleteFailure{}
	if len(volumes) == 0 {
		return latest
	}
	for _, e := range events {
		ref := e.InvolvedObject
		pv := volumes[ref.UID]
		if pv == nil || e.Type != corev1.EventTypeWarning || e.Reason != "VolumeFailedDelete" || ref.Kind != "PersistentVolume" || ref.Name != pv.Name || ref.Namespace != "" || resourceid.GroupFromAPIVersion(ref.APIVersion) != "" {
			continue
		}
		at := e.LastTimestamp.Time
		if e.Series != nil && e.Series.LastObservedTime.Time.After(at) {
			at = e.Series.LastObservedTime.Time
		}
		if at.IsZero() {
			at = e.EventTime.Time
		}
		if at.IsZero() {
			at = e.CreationTimestamp.Time
		}
		if at.IsZero() || at.After(now) || !at.After(latest[ref.UID].at) {
			continue
		}
		latest[ref.UID] = pvDeleteFailure{event: e, at: at}
	}
	return latest
}
