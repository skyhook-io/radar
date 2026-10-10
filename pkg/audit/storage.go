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
)

const (
	pvcPendingAge       = 24 * time.Hour
	pvDeleteEventWindow = 24 * time.Hour
)

func checkStorage(tr *evalTracker, input *CheckInput, now time.Time) []Finding {
	for resource, missing := range map[string]bool{
		"persistentvolumeclaims": input.PersistentVolumeClaims == nil,
		"persistentvolumes":      input.PersistentVolumes == nil,
		"storageclasses":         input.StorageClasses == nil,
	} {
		if missing {
			tr.missingInputs = append(tr.missingInputs, resource)
		}
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
		if pvc.DeletionTimestamp != nil {
			continue
		}
		consumed := consumers[pvc.Namespace+"/"+pvc.Name]
		complete := slices.Contains(input.PVCConsumerNamespaces, pvc.Namespace) && input.Pods != nil && input.Deployments != nil && input.ReplicaSets != nil && input.StatefulSets != nil && input.DaemonSets != nil && input.Jobs != nil && input.CronJobs != nil
		if !consumed && !complete {
			tr.missingInputs = append(tr.missingInputs, "pvc-consumers")
			continue
		}
		checkID, message := "", ""
		switch pvc.Status.Phase {
		case corev1.ClaimBound:
			if !consumed && input.PersistentVolumes == nil {
				continue
			}
			tr.record("pvcNoConsumer", pvc.Namespace)
			if consumed {
				continue
			}
			checkID = "pvcNoConsumer"
			message = "No consumer observed among Pods or built-in workload templates Radar can see. CRD consumers may still reference this claim."
		case corev1.ClaimPending:
			classKnown := pvc.Spec.StorageClassName != nil && *pvc.Spec.StorageClassName == ""
			wffc := false
			if pvc.Spec.StorageClassName != nil {
				if sc := classes[*pvc.Spec.StorageClassName]; sc != nil {
					classKnown = true
					wffc = sc.VolumeBindingMode != nil && *sc.VolumeBindingMode == storagev1.VolumeBindingWaitForFirstConsumer
				}
			}
			if consumed && !classKnown {
				tr.missingInputs = append(tr.missingInputs, "pvc-binding-mode")
				continue
			}
			if pvc.CreationTimestamp.IsZero() {
				continue
			}
			tr.record("pvcLongPending", pvc.Namespace)
			if now.Sub(pvc.CreationTimestamp.Time) <= pvcPendingAge || (wffc && consumed) {
				continue
			}
			checkID = "pvcLongPending"
			message = "PVC is Pending and was created more than 24h ago; this snapshot does not establish how long it has been Pending."
			if !consumed {
				message += " No consumer observed among Pods or built-in workload templates Radar can see; CRD consumers may still reference it."
			}
			if wffc {
				message += " StorageClass uses WaitForFirstConsumer."
			}
		default:
			continue
		}
		findings = append(findings, Finding{Kind: "PersistentVolumeClaim", Namespace: pvc.Namespace, Name: pvc.Name, CheckID: checkID, Category: CategoryEfficiency, Severity: SeverityWarning, Message: message + " " + pvcStorageDetails(pvc, pvs[pvc.Spec.VolumeName], now)})
	}
	for _, pv := range input.PersistentVolumes {
		if pv.Status.Phase != corev1.VolumeReleased {
			continue
		}
		message := ""
		switch pv.Spec.PersistentVolumeReclaimPolicy {
		case corev1.PersistentVolumeReclaimRetain:
			message = "Released PV is kept after its claim was deleted (reclaim policy Retain)."
		case corev1.PersistentVolumeReclaimDelete:
			failure := latestPVDeleteFailure(pv, input.Events, now)
			// An authorized event can establish failure, but an empty or partial
			// event inventory cannot establish successful deletion.
			if failure == nil {
				if input.Events == nil || !input.PVDeletionEventsComplete {
					tr.missingInputs = append(tr.missingInputs, "pv-deletion-events")
				} else {
					tr.record("releasedPV", "")
				}
				continue
			}
			message = "Released PV deletion is failing: " + failure.Message
		default:
			continue
		}
		tr.record("releasedPV", "")
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
	class, volume, reclaim := "not assigned", "not bound", "unknown"
	if pvc.Spec.StorageClassName != nil {
		class = fmt.Sprintf("%q", *pvc.Spec.StorageClassName)
	}
	if pvc.Spec.VolumeName != "" {
		volume = pvc.Spec.VolumeName
	}
	if pv != nil {
		reclaim = string(pv.Spec.PersistentVolumeReclaimPolicy)
	}
	return fmt.Sprintf("Requested %s; storage class %s; PVC age %s; bound PV %s; reclaim policy %s.", storageQuantity(pvc.Spec.Resources.Requests), class, storageAge(pvc.CreationTimestamp, now), volume, reclaim)
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

func latestPVDeleteFailure(pv *corev1.PersistentVolume, events []*corev1.Event, now time.Time) *corev1.Event {
	var latest *corev1.Event
	var latestTime time.Time
	for _, e := range events {
		ref := e.InvolvedObject
		if e.Type != corev1.EventTypeWarning || e.Reason != "VolumeFailedDelete" || ref.Kind != "PersistentVolume" || ref.Name != pv.Name || ref.Namespace != "" || resourceid.GroupFromAPIVersion(ref.APIVersion) != "" || pv.UID == "" || ref.UID != pv.UID {
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
		if at.IsZero() || at.After(now) || now.Sub(at) > pvDeleteEventWindow || !at.After(latestTime) {
			continue
		}
		latest, latestTime = e, at
	}
	return latest
}
