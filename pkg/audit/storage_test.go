package audit

import (
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func storageInput() *CheckInput {
	return &CheckInput{
		PVCConsumerNamespaces:    []string{"app"},
		PVDeletionEventsComplete: true,
		Pods:                     []*corev1.Pod{}, Deployments: []*appsv1.Deployment{}, ReplicaSets: []*appsv1.ReplicaSet{},
		StatefulSets: []*appsv1.StatefulSet{}, DaemonSets: []*appsv1.DaemonSet{}, Jobs: []*batchv1.Job{}, CronJobs: []*batchv1.CronJob{},
		PersistentVolumeClaims: []*corev1.PersistentVolumeClaim{{
			ObjectMeta: metav1.ObjectMeta{Name: "data-db-0", Namespace: "app", CreationTimestamp: metav1.NewTime(time.Now().Add(-48 * time.Hour))},
			Spec:       corev1.PersistentVolumeClaimSpec{StorageClassName: ptr("local"), VolumeName: "disk", Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("8Gi")}}},
			Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
		}},
		PersistentVolumes: []*corev1.PersistentVolume{{ObjectMeta: metav1.ObjectMeta{Name: "disk", UID: "disk-uid", CreationTimestamp: metav1.NewTime(time.Now().Add(-72 * time.Hour))}, Spec: corev1.PersistentVolumeSpec{Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("8Gi")}, StorageClassName: "local", PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain, ClaimRef: &corev1.ObjectReference{Namespace: "app", Name: "data-db-0"}}}},
		StorageClasses:    []*storagev1.StorageClass{{ObjectMeta: metav1.ObjectMeta{Name: "local"}, VolumeBindingMode: ptr(storagev1.VolumeBindingWaitForFirstConsumer)}},
		Events:            []*corev1.Event{},
	}
}

func pvcPodSpec(name string) corev1.PodSpec {
	return corev1.PodSpec{Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: name}}}}}
}

func TestPVCNoConsumer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*CheckInput)
		want   bool
	}{
		{"no consumer", func(*CheckInput) {}, true},
		{"Pod", func(i *CheckInput) {
			i.Pods = append(i.Pods, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "app"}, Spec: pvcPodSpec("data-db-0")})
		}, false},
		{"terminal Pod", func(i *CheckInput) {
			i.Pods = append(i.Pods, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "app"}, Spec: pvcPodSpec("data-db-0"), Status: corev1.PodStatus{Phase: corev1.PodSucceeded}})
		}, false},
		{"generic ephemeral Pod", func(i *CheckInput) {
			i.Pods = append(i.Pods, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "data-db"}, Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "0", VolumeSource: corev1.VolumeSource{Ephemeral: &corev1.EphemeralVolumeSource{}}}}}})
		}, false},
		{"StatefulSet direct claim", func(i *CheckInput) {
			i.StatefulSets = append(i.StatefulSets, &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: "app"}, Spec: appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{Spec: pvcPodSpec("data-db-0")}}})
		}, false},
		{"Deployment at zero", func(i *CheckInput) {
			i.Deployments = append(i.Deployments, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "app"}, Spec: appsv1.DeploymentSpec{Replicas: ptr(int32(0)), Template: corev1.PodTemplateSpec{Spec: pvcPodSpec("data-db-0")}}})
		}, false},
		{"ReplicaSet", func(i *CheckInput) {
			i.ReplicaSets = append(i.ReplicaSets, &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "app"}, Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{Spec: pvcPodSpec("data-db-0")}}})
		}, false},
		{"DaemonSet", func(i *CheckInput) {
			i.DaemonSets = append(i.DaemonSets, &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: "app"}, Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: pvcPodSpec("data-db-0")}}})
		}, false},
		{"StatefulSet generated at zero", func(i *CheckInput) {
			i.StatefulSets = append(i.StatefulSets, &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "db"}, Spec: appsv1.StatefulSetSpec{Replicas: ptr(int32(0)), VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{ObjectMeta: metav1.ObjectMeta{Name: "data"}}}}})
		}, false},
		{"StatefulSet retained ordinal", func(i *CheckInput) {
			i.PersistentVolumeClaims[0].Name = "data-db-12"
			i.StatefulSets = append(i.StatefulSets, &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "db"}, Spec: appsv1.StatefulSetSpec{Replicas: ptr(int32(0)), VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{ObjectMeta: metav1.ObjectMeta{Name: "data"}}}}})
		}, false},
		{"not a generated ordinal", func(i *CheckInput) {
			i.PersistentVolumeClaims[0].Name = "data-db-backup"
			i.StatefulSets = append(i.StatefulSets, &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "db"}, Spec: appsv1.StatefulSetSpec{VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{ObjectMeta: metav1.ObjectMeta{Name: "data"}}}}})
		}, true},
		{"Job including complete", func(i *CheckInput) {
			i.Jobs = append(i.Jobs, &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: "app"}, Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: pvcPodSpec("data-db-0")}}, Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}}})
		}, false},
		{"CronJob", func(i *CheckInput) {
			i.CronJobs = append(i.CronJobs, &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Namespace: "app"}, Spec: batchv1.CronJobSpec{JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: pvcPodSpec("data-db-0")}}}}})
		}, false},
		{"other namespace", func(i *CheckInput) {
			i.Pods = append(i.Pods, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "other"}, Spec: pvcPodSpec("data-db-0")})
		}, true},
		{"unreadable Pods", func(i *CheckInput) { i.Pods = nil }, false},
		{"unknown coverage", func(i *CheckInput) { i.PVCConsumerNamespaces = nil }, false},
		{"CNPG controller owner", func(i *CheckInput) {
			i.PersistentVolumeClaims[0].OwnerReferences = []metav1.OwnerReference{{APIVersion: "postgresql.cnpg.io/v1", Kind: "Cluster", Name: "hibernated", Controller: ptr(true)}}
		}, false},
		{"non-controller owner", func(i *CheckInput) {
			i.PersistentVolumeClaims[0].OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "ConfigMap", Name: "metadata"}}
		}, true},
		{"unreadable PVs", func(i *CheckInput) { i.PersistentVolumes = nil }, true},
		{"Pending", func(i *CheckInput) { i.PersistentVolumeClaims[0].Status.Phase = corev1.ClaimPending }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := storageInput()
			tc.change(i)
			r := RunChecks(i)
			if got := findingNames(r.Findings, "pvcNoConsumer")[i.PersistentVolumeClaims[0].Name]; got != tc.want {
				t.Fatalf("finding=%v want=%v: %+v", got, tc.want, r.Findings)
			}
			if tc.want {
				for _, f := range r.Findings {
					if f.CheckID == "pvcNoConsumer" {
						for _, s := range []string{"No consumer observed", "8Gi", "local", "disk", "age", "CRD"} {
							if !strings.Contains(f.Message, s) {
								t.Errorf("missing %q in %q", s, f.Message)
							}
						}
					}
				}
			}
			if tc.name == "unreadable PVs" {
				if !slices.Contains(r.MissingInputs, "persistentvolumes") || !strings.Contains(r.Findings[0].Message, "reclaim policy not visible") {
					t.Fatalf("unreadable retention evidence: %+v", r)
				}
			}
			if tc.name == "unknown coverage" && (r.CheckCounts["pvcNoConsumer"].Evaluated != 0 || !slices.Contains(r.MissingInputs, "pvc-consumers")) {
				t.Fatalf("unknown counted as passing: %+v", r)
			}
		})
	}
}

func TestLongPendingPVC(t *testing.T) {
	for _, tc := range []struct {
		name                                                string
		age                                                 time.Duration
		consumer, immediate, unknownClass, unknownConsumers bool
		want                                                bool
	}{
		{"WFFC no consumer", 48 * time.Hour, false, false, false, false, true},
		{"WFFC with consumer", 48 * time.Hour, true, false, false, false, false},
		{"WFFC with Pod", 48 * time.Hour, true, false, false, false, false},
		{"Immediate with consumer", 48 * time.Hour, true, true, false, false, false},
		{"young", time.Hour, false, false, false, false, false},
		{"exact threshold", 24 * time.Hour, false, false, false, false, false},
		{"unknown class with consumer", 48 * time.Hour, true, false, true, false, false},
		{"unknown class no consumer", 48 * time.Hour, false, false, true, false, true},
		{"unknown consumers", 48 * time.Hour, false, false, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := storageInput()
			now := time.Now()
			i.PersistentVolumeClaims[0].Status.Phase = corev1.ClaimPending
			i.PersistentVolumeClaims[0].CreationTimestamp = metav1.NewTime(now.Add(-tc.age))
			if tc.consumer {
				if tc.name == "WFFC with Pod" {
					i.Pods = append(i.Pods, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "app"}, Spec: pvcPodSpec("data-db-0")})
				} else {
					i.CronJobs = append(i.CronJobs, &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Namespace: "app"}, Spec: batchv1.CronJobSpec{JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: pvcPodSpec("data-db-0")}}}}})
				}
			}
			if tc.immediate {
				i.StorageClasses[0].VolumeBindingMode = ptr(storagev1.VolumeBindingImmediate)
			}
			if tc.unknownClass {
				i.StorageClasses = nil
			}
			if tc.unknownConsumers {
				i.PVCConsumerNamespaces = nil
			}
			tr := newEvalTracker()
			fs := checkStorage(tr, i, now)
			if got := findingNames(fs, "pvcLongPending")["data-db-0"]; got != tc.want {
				t.Fatalf("finding=%v want=%v: %+v", got, tc.want, fs)
			}
			if tc.want {
				if !strings.Contains(fs[0].Message, "Pending since creation, 2d ago") || strings.Contains(fs[0].Message, "bound PV") || strings.Contains(fs[0].Message, "reclaim policy") || strings.Contains(fs[0].Message, "snapshot does not establish") {
					t.Fatalf("inaccurate Pending detail: %+v", fs[0])
				}
			}
		})
	}
}

func TestPVCStorageEligibility(t *testing.T) {
	now := time.Now()
	for _, phase := range []corev1.PersistentVolumeClaimPhase{corev1.ClaimBound, corev1.ClaimPending} {
		for _, age := range []time.Duration{time.Hour, 24 * time.Hour, 25 * time.Hour} {
			t.Run(string(phase)+"/"+age.String(), func(t *testing.T) {
				i := storageInput()
				i.PersistentVolumeClaims[0].Status.Phase = phase
				i.PersistentVolumeClaims[0].CreationTimestamp = metav1.NewTime(now.Add(-age))
				fs := checkStorage(newEvalTracker(), i, now)
				if (len(fs) > 0) != (age > 24*time.Hour) {
					t.Fatalf("age eligibility: %+v", fs)
				}
				i.PersistentVolumeClaims[0].OwnerReferences = []metav1.OwnerReference{{APIVersion: "postgresql.cnpg.io/v1", Kind: "Cluster", Name: "hibernated", Controller: ptr(true)}}
				tr := newEvalTracker()
				if fs := checkStorage(tr, i, now); len(fs) != 0 || tr.counts["pvcNoConsumer"]["app"] != 0 || tr.counts["pvcLongPending"]["app"] != 0 {
					t.Fatalf("controller-owned claim evaluated: %+v", fs)
				}
			})
		}
	}
}

func TestStorageClusterInputsOnlyWhenNeeded(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*CheckInput)
		pv, sc bool
	}{
		{"no claims", func(i *CheckInput) { i.PersistentVolumeClaims = []*corev1.PersistentVolumeClaim{} }, false, false},
		{"Bound absence", func(*CheckInput) {}, true, false},
		{"Bound consumed", func(i *CheckInput) {
			i.Pods = []*corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Namespace: "app"}, Spec: pvcPodSpec("data-db-0")}}
		}, false, false},
		{"Pending absence", func(i *CheckInput) { i.PersistentVolumeClaims[0].Status.Phase = corev1.ClaimPending }, false, true},
		{"Pending no class", func(i *CheckInput) {
			i.PersistentVolumeClaims[0].Status.Phase = corev1.ClaimPending
			i.PersistentVolumeClaims[0].Spec.StorageClassName = ptr("")
		}, false, false},
		{"young claim", func(i *CheckInput) { i.PersistentVolumeClaims[0].CreationTimestamp = metav1.Now() }, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := storageInput()
			i.PersistentVolumes, i.StorageClasses = nil, nil
			tc.change(i)
			r := RunChecks(i)
			if slices.Contains(r.MissingInputs, "persistentvolumes") != tc.pv || slices.Contains(r.MissingInputs, "storageclasses") != tc.sc {
				t.Fatalf("unused optional inputs reported: %+v", r.MissingInputs)
			}
		})
	}
}

func TestReleasedPVHealthyCounts(t *testing.T) {
	i := storageInput()
	i.PersistentVolumeClaims = []*corev1.PersistentVolumeClaim{}
	i.PersistentVolumes[0].Status.Phase = corev1.VolumeBound
	i.PersistentVolumes = append(i.PersistentVolumes, &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "available"}, Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeAvailable}})
	r := RunChecks(i)
	if count := r.CheckCounts["releasedPV"]; count.Evaluated != 2 || count.Passed != 2 || len(r.Findings) != 0 {
		t.Fatalf("healthy PVs lost from counts: %+v", count)
	}
}

func TestReleasedPV(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name       string
		change     func(*CheckInput)
		want, text string
	}{
		{"Retain", func(*CheckInput) {}, "disk", "kept after its claim was deleted"},
		{"Bound", func(i *CheckInput) { i.PersistentVolumes[0].Status.Phase = corev1.VolumeBound }, "", ""},
		{"Delete no event", func(i *CheckInput) {
			i.PersistentVolumes[0].Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimDelete
		}, "disk", "deletion has not completed"},
		{"Delete recent event", func(i *CheckInput) {
			i.PersistentVolumes[0].Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimDelete
			i.Events = []*corev1.Event{deleteFailure(now.Add(-time.Minute), "latest failure")}
		}, "disk", "deletion is failing: latest failure"},
		{"Delete old event", func(i *CheckInput) {
			i.PersistentVolumes[0].Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimDelete
			i.Events = []*corev1.Event{deleteFailure(now.Add(-48*time.Hour), "stale failure")}
		}, "disk", "Latest deletion warning (2d ago): stale failure"},
		{"Delete old UID", func(i *CheckInput) {
			i.PersistentVolumes[0].Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimDelete
			e := deleteFailure(now, "other object")
			e.InvolvedObject.UID = "old-uid"
			i.Events = []*corev1.Event{e}
		}, "disk", "deletion has not completed"},
		{"Delete unreadable events", func(i *CheckInput) {
			i.PersistentVolumes[0].Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimDelete
			i.Events = nil
		}, "disk", "deletion has not completed"},
		{"Retain unreadable events", func(i *CheckInput) { i.Events = nil }, "disk", "kept after its claim was deleted"},
		{"transition time", func(i *CheckInput) {
			i.PersistentVolumes[0].Status.LastPhaseTransitionTime = ptr(metav1.NewTime(now.Add(-time.Hour)))
		}, "", ""},
		{"Retain recent release", func(i *CheckInput) {
			i.PersistentVolumes[0].Status.LastPhaseTransitionTime = ptr(metav1.NewTime(now.Add(-23 * time.Hour)))
		}, "", ""},
		{"Delete recent release", func(i *CheckInput) {
			i.PersistentVolumes[0].Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimDelete
			i.PersistentVolumes[0].Status.LastPhaseTransitionTime = ptr(metav1.NewTime(now.Add(-time.Minute)))
		}, "", ""},
		{"Delete recent release with warning", func(i *CheckInput) {
			i.PersistentVolumes[0].Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimDelete
			i.PersistentVolumes[0].Status.LastPhaseTransitionTime = ptr(metav1.NewTime(now.Add(-time.Minute)))
			i.Events = []*corev1.Event{deleteFailure(now, "immediate failure")}
		}, "disk", "deletion is failing: immediate failure"},
		{"Delete exact threshold", func(i *CheckInput) {
			i.PersistentVolumes[0].Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimDelete
			i.PersistentVolumes[0].Status.LastPhaseTransitionTime = ptr(metav1.NewTime(now.Add(-time.Hour)))
		}, "", ""},
		{"Delete old release", func(i *CheckInput) {
			i.PersistentVolumes[0].Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimDelete
			i.PersistentVolumes[0].Status.LastPhaseTransitionTime = ptr(metav1.NewTime(now.Add(-2 * time.Hour)))
		}, "disk", "Released for 2h"},
		{"latest event series", func(i *CheckInput) {
			i.PersistentVolumes[0].Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimDelete
			e := deleteFailure(now.Add(-48*time.Hour), "series failure")
			e.Series = &corev1.EventSeries{LastObservedTime: metav1.NewMicroTime(now.Add(-time.Minute))}
			i.Events = []*corev1.Event{deleteFailure(now.Add(-time.Hour), "earlier failure"), e}
		}, "disk", "series failure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := storageInput()
			i.PersistentVolumes[0].Status.Phase = corev1.VolumeReleased
			tc.change(i)
			tr := newEvalTracker()
			fs := checkStorage(tr, i, now)
			if got := findingNames(fs, "releasedPV")["disk"]; got != (tc.want != "") {
				t.Fatalf("finding=%v want=%q: %+v", got, tc.want, fs)
			}
			for _, f := range fs {
				if f.CheckID == "releasedPV" && (!strings.Contains(f.Message, tc.text) || !strings.Contains(f.Message, "8Gi") || !strings.Contains(f.Message, "app/data-db-0")) {
					t.Errorf("missing evidence: %+v", f)
				}
			}
			if tr.counts["releasedPV"][""] != 1 {
				t.Fatal("readable PV not evaluated")
			}
			if tc.name == "Delete unreadable events" && !slices.Contains(tr.missingInputs, "pv-deletion-events") {
				t.Fatal("unavailable supplemental deletion evidence not reported")
			}
			if tc.name == "Delete no event" && !strings.Contains(fs[len(fs)-1].Message, "PV age") {
				t.Fatal("fallback age not labelled")
			}
		})
	}
}

func TestPVDeletionEventIdentityAndTimes(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name   string
		change func(*corev1.Event)
		want   bool
	}{
		{"current UID", func(*corev1.Event) {}, true},
		{"old UID", func(e *corev1.Event) { e.InvolvedObject.UID = "previous" }, false},
		{"wrong name", func(e *corev1.Event) { e.InvolvedObject.Name = "other" }, false},
		{"namespaced ref", func(e *corev1.Event) { e.InvolvedObject.Namespace = "app" }, false},
		{"CRD ref", func(e *corev1.Event) { e.InvolvedObject.APIVersion = "storage.example.com/v1" }, false},
		{"wrong kind", func(e *corev1.Event) { e.InvolvedObject.Kind = "PersistentVolumeClaim" }, false},
		{"Normal", func(e *corev1.Event) { e.Type = corev1.EventTypeNormal }, false},
		{"wrong reason", func(e *corev1.Event) { e.Reason = "ProvisioningFailed" }, false},
		{"future timestamp", func(e *corev1.Event) { e.LastTimestamp = metav1.NewTime(now.Add(time.Hour)) }, false},
		{"event time", func(e *corev1.Event) { e.LastTimestamp = metav1.Time{}; e.EventTime = metav1.NewMicroTime(now) }, true},
		{"creation time", func(e *corev1.Event) { e.LastTimestamp = metav1.Time{}; e.CreationTimestamp = metav1.NewTime(now) }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := storageInput()
			i.PersistentVolumes[0].Status.Phase = corev1.VolumeReleased
			i.PersistentVolumes[0].Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimDelete
			e := deleteFailure(now, "warning")
			tc.change(e)
			got := latestPVDeleteFailures(i.PersistentVolumes, []*corev1.Event{e}, now)
			if (got["disk-uid"].event != nil) != tc.want {
				t.Fatalf("event identity/timestamp: %+v", got)
			}
		})
	}
}

func deleteFailure(at time.Time, message string) *corev1.Event {
	return &corev1.Event{ObjectMeta: metav1.ObjectMeta{Namespace: "app"}, InvolvedObject: corev1.ObjectReference{APIVersion: "v1", Kind: "PersistentVolume", Name: "disk", UID: "disk-uid"}, Type: corev1.EventTypeWarning, Reason: "VolumeFailedDelete", Message: message, LastTimestamp: metav1.NewTime(at)}
}
