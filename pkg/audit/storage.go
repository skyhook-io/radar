package audit

import (
	"fmt"
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
		tr.skip("persistentvolumeclaims")
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
	var defaultClass *storagev1.StorageClass
	for _, sc := range input.StorageClasses {
		classes[sc.Name] = sc
		if sc.Annotations["storageclass.kubernetes.io/is-default-class"] == "true" && (defaultClass == nil || sc.CreationTimestamp.After(defaultClass.CreationTimestamp.Time) || sc.CreationTimestamp.Equal(&defaultClass.CreationTimestamp) && sc.Name < defaultClass.Name) {
			defaultClass = sc
		}
	}
	consumers := collectPVCConsumers(input)
	var findings []Finding
	for _, pvc := range input.PersistentVolumeClaims {
		if pvc.DeletionTimestamp != nil || hasControllerOwnerReference(pvc.OwnerReferences) {
			continue
		}
		if pvc.Status.Phase != corev1.ClaimBound && pvc.Status.Phase != corev1.ClaimPending {
			continue
		}
		consumed := consumers[pvc.Namespace+"/"+pvc.Name]
		reviewSince := pvc.CreationTimestamp
		var unusedSince metav1.Time
		if pvc.Status.Phase == corev1.ClaimBound {
			for _, condition := range pvc.Status.Conditions {
				if condition.Type != corev1.PersistentVolumeClaimUnused {
					continue
				}
				switch condition.Status {
				case corev1.ConditionFalse:
					consumed = true
				case corev1.ConditionTrue:
					unusedSince = condition.LastTransitionTime
					if !unusedSince.IsZero() {
						reviewSince = unusedSince
					}
				}
				break
			}
		}
		if reviewSince.IsZero() || now.Sub(reviewSince.Time) <= pvcReviewAge {
			continue
		}
		complete := consumerNamespaces[pvc.Namespace] && input.Pods != nil && input.Deployments != nil && input.ReplicaSets != nil && input.StatefulSets != nil && input.DaemonSets != nil && input.Jobs != nil && input.CronJobs != nil
		if !consumed && !complete {
			tr.skip("pvc-consumers")
			continue
		}
		checkID, message := "", ""
		switch pvc.Status.Phase {
		case corev1.ClaimBound:
			tr.record("pvcNoConsumer", pvc.Namespace)
			if consumed {
				continue
			}
			checkID = "pvcNoConsumer"
			if !unusedSince.IsZero() {
				message = fmt.Sprintf("Kubernetes reports it unused by any Pod since %s (%s ago).", unusedSince.UTC().Format(time.RFC3339), timeutil.FormatAgeShort(now.Sub(unusedSince.Time)))
			}
		case corev1.ClaimPending:
			tr.record("pvcLongPending", pvc.Namespace)
			if consumed {
				continue
			}
			checkID = "pvcLongPending"
			message = "Pending since creation, " + storageAge(pvc.CreationTimestamp, now) + " ago."
			if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != "" {
				sc := defaultClass
				if pvc.Spec.StorageClassName != nil {
					sc = classes[*pvc.Spec.StorageClassName]
				}
				if input.StorageClasses == nil {
					message += " StorageClass binding mode not visible."
				} else if sc == nil {
					if pvc.Spec.StorageClassName == nil {
						message += " No default StorageClass applies."
					} else {
						message += fmt.Sprintf(" StorageClass %q not found.", *pvc.Spec.StorageClassName)
					}
				} else {
					classDescription := "StorageClass"
					if pvc.Spec.StorageClassName == nil {
						classDescription = fmt.Sprintf("Default StorageClass %q", sc.Name)
					}
					if sc.VolumeBindingMode != nil && *sc.VolumeBindingMode == storagev1.VolumeBindingWaitForFirstConsumer {
						message += " " + classDescription + " uses WaitForFirstConsumer."
					} else if pvc.Spec.StorageClassName == nil {
						message += " " + classDescription + " applies."
					}
				}
			}
		}
		if message != "" {
			message += " "
		}
		message += pvcStorageDetails(pvc, pvs[pvc.Spec.VolumeName], now)
		findings = append(findings, Finding{Kind: "PersistentVolumeClaim", Namespace: pvc.Namespace, Name: pvc.Name, CheckID: checkID, Category: CategoryEfficiency, Severity: SeverityWarning, Message: message})
	}
	if input.PersistentVolumes == nil {
		tr.skip("persistentvolumes")
	}
	reclaimWarnings := latestPVReclaimWarnings(input.PersistentVolumes, input.Events, now)
	deletionEventsComplete := input.Events != nil && input.PVDeletionEventsComplete
	for _, pv := range input.PersistentVolumes {
		if pv.DeletionTimestamp != nil {
			// A deleting volume is a stuckTerminating subject, as a deleting
			// claim is.
			continue
		}
		warning, hasWarning := reclaimWarnings[pv.UID]
		switch pv.Status.Phase {
		case corev1.VolumeBound:
			tr.record("releasedPV", "")
			continue
		case corev1.VolumeFailed:
			// The PV controller sets Failed only when reclaiming the volume
			// failed, so the phase alone establishes the finding; events add
			// detail and never decide whether the volume is evaluated.
			tr.record("releasedPV", "")
			message := fmt.Sprintf("Failed PV: reclaiming the volume failed after its claim was deleted (reclaim policy %s).", pv.Spec.PersistentVolumeReclaimPolicy)
			if hasWarning {
				message += " Latest reclaim warning (" + timeutil.FormatAgeShort(now.Sub(warning.at)) + " ago): " + asSentence(warning.event.Message)
			} else if pv.Status.Message != "" {
				message += " Status message: " + asSentence(pv.Status.Message)
			}
			if !deletionEventsComplete {
				message += " Deletion warning events not fully visible."
			}
			findings = append(findings, releasedPVFinding(pv, message, now))
			continue
		case corev1.VolumeReleased:
		default:
			// An Available or Pending volume has no claim to have outlived;
			// this check has no verdict on it, so it is not a passing subject.
			continue
		}
		var releasedAt metav1.Time
		if pv.Status.LastPhaseTransitionTime != nil && !pv.Status.LastPhaseTransitionTime.IsZero() {
			releasedAt = *pv.Status.LastPhaseTransitionTime
		}
		recentFailure := hasWarning && now.Sub(warning.at) <= pvDeleteEventWindow
		if pv.Spec.PersistentVolumeReclaimPolicy == corev1.PersistentVolumeReclaimDelete && releasedAt.IsZero() && !recentFailure {
			tr.skip("pv-release-time")
			continue
		}
		inDeleteGrace := !releasedAt.IsZero() && now.Sub(releasedAt.Time) <= pvDeleteGrace
		if pv.Spec.PersistentVolumeReclaimPolicy == corev1.PersistentVolumeReclaimDelete && inDeleteGrace && !recentFailure && !deletionEventsComplete {
			// A warning Radar can't see may already show this deletion failing.
			tr.skip("pv-deletion-events")
			continue
		}
		tr.record("releasedPV", "")
		message := ""
		switch pv.Spec.PersistentVolumeReclaimPolicy {
		case corev1.PersistentVolumeReclaimRetain:
			if !releasedAt.IsZero() && now.Sub(releasedAt.Time) <= pvRetainGrace {
				continue
			}
			message = "Released PV is kept after its claim was deleted (reclaim policy Retain)."
			if releasedAt.IsZero() {
				message = "Released PV is kept after its claim was deleted (reclaim policy Retain); release time unknown."
			}
		case corev1.PersistentVolumeReclaimDelete:
			if recentFailure {
				message = "Released PV deletion is failing: " + asSentence(warning.event.Message)
			} else {
				if inDeleteGrace {
					continue
				}
				message = "Released PV deletion has not completed (reclaim policy Delete)."
				if hasWarning {
					message += " Latest deletion warning (" + timeutil.FormatAgeShort(now.Sub(warning.at)) + " ago): " + asSentence(warning.event.Message)
				}
			}
			if !deletionEventsComplete {
				message += " Deletion warning events not fully visible."
			}
		default:
			continue
		}
		findings = append(findings, releasedPVFinding(pv, message, now))
	}
	return findings
}

func releasedPVFinding(pv *corev1.PersistentVolume, message string, now time.Time) Finding {
	age := "PV age " + storageAge(pv.CreationTimestamp, now)
	if pv.Status.LastPhaseTransitionTime != nil && !pv.Status.LastPhaseTransitionTime.IsZero() {
		age = string(pv.Status.Phase) + " for " + storageAge(*pv.Status.LastPhaseTransitionTime, now)
	}
	formerClaim := "unknown"
	if pv.Spec.ClaimRef != nil {
		formerClaim = pv.Spec.ClaimRef.Namespace + "/" + pv.Spec.ClaimRef.Name
	}
	message += fmt.Sprintf(" Capacity %s; storage class %q; former claim %s; %s.", storageQuantity(pv.Spec.Capacity), pv.Spec.StorageClassName, formerClaim, age)
	return Finding{Kind: "PersistentVolume", Name: pv.Name, CheckID: "releasedPV", Category: CategoryEfficiency, Severity: SeverityWarning, Message: message}
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
	for _, pvc := range input.PersistentVolumeClaims {
		if pvc.Status.Phase != corev1.ClaimPending {
			continue
		}
		if source := pvc.Spec.DataSource; source != nil && source.Kind == "PersistentVolumeClaim" && (source.APIGroup == nil || *source.APIGroup == "") {
			refs[pvc.Namespace+"/"+source.Name] = true
		}
		if source := pvc.Spec.DataSourceRef; source != nil && source.Kind == "PersistentVolumeClaim" && (source.APIGroup == nil || *source.APIGroup == "") {
			namespace := pvc.Namespace
			if source.Namespace != nil && *source.Namespace != "" {
				namespace = *source.Namespace
			}
			refs[namespace+"/"+source.Name] = true
		}
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

// PVNeedsReclaimEvents reports whether a releasedPV finding for pv can draw
// on reclaim warning events: a Released volume awaiting deletion, or a volume
// whose reclaim already failed.
func PVNeedsReclaimEvents(pv *corev1.PersistentVolume) bool {
	if pv.DeletionTimestamp != nil {
		return false
	}
	return pv.Status.Phase == corev1.VolumeFailed || pv.Status.Phase == corev1.VolumeReleased && pv.Spec.PersistentVolumeReclaimPolicy == corev1.PersistentVolumeReclaimDelete
}

// isReclaimWarning matches the warnings the PV controllers record when a
// reclaim fails. Recycling failures only apply once the volume is Failed: a
// Released volume is judged on deletion alone.
func isReclaimWarning(pv *corev1.PersistentVolume, reason string) bool {
	return reason == "VolumeFailedDelete" || reason == "VolumeFailedRecycle" && pv.Status.Phase == corev1.VolumeFailed
}

type pvReclaimWarning struct {
	event *corev1.Event
	at    time.Time
}

func latestPVReclaimWarnings(pvs []*corev1.PersistentVolume, events []*corev1.Event, now time.Time) map[types.UID]pvReclaimWarning {
	volumes := map[types.UID]*corev1.PersistentVolume{}
	for _, pv := range pvs {
		if pv.UID != "" && PVNeedsReclaimEvents(pv) {
			volumes[pv.UID] = pv
		}
	}
	latest := map[types.UID]pvReclaimWarning{}
	if len(volumes) == 0 {
		return latest
	}
	for _, e := range events {
		ref := e.InvolvedObject
		pv := volumes[ref.UID]
		if pv == nil || e.Type != corev1.EventTypeWarning || !isReclaimWarning(pv, e.Reason) || ref.Kind != "PersistentVolume" || ref.Name != pv.Name || ref.Namespace != "" || resourceid.GroupFromAPIVersion(ref.APIVersion) != "" {
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
		if pv.Status.LastPhaseTransitionTime != nil && !pv.Status.LastPhaseTransitionTime.IsZero() && at.Before(pv.Status.LastPhaseTransitionTime.Time) {
			continue
		}
		latest[ref.UID] = pvReclaimWarning{event: e, at: at}
	}
	return latest
}

func asSentence(message string) string {
	message = strings.TrimSpace(message)
	if message == "" || strings.HasSuffix(message, ".") || strings.HasSuffix(message, "!") || strings.HasSuffix(message, "?") {
		return message
	}
	return message + "."
}
