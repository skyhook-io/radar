package velero

import (
	"errors"
	"fmt"
	"maps"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ErrScheduleCannotRun: a backup from this schedule would fail, because
// Velero labels it with the schedule's name and the name does not fit a label.
var ErrScheduleCannotRun = errors.New("the schedule's name is longer than 63 characters, so Velero cannot label a backup from it and the backup would fail")

const lastAppliedAnnotation = "kubectl.kubernetes.io/last-applied-configuration"

// BackupFromSchedule is the Backup `velero backup create --from-schedule`
// creates (Velero v1.18.2 pkg/builder BackupBuilder.FromSchedule and
// pkg/cmd/cli/backup/create.go): the schedule's template as its spec, the
// template's labels (else the schedule's own) plus the schedule-name label,
// the schedule's annotations, an owner reference when the schedule asks for
// one, and the name <schedule>-<UTC timestamp>.
//
// The schedule-name label makes it one of the schedule's runs: Velero holds
// the schedule's next run while it is in progress, and Radar counts it among
// the schedule's runs. The one difference from the CLI: the schedule's kubectl
// last-applied annotation is not copied, since it describes the Schedule.
func BackupFromSchedule(schedule *unstructured.Unstructured, now time.Time) (*unstructured.Unstructured, error) {
	if len(schedule.GetName()) > maxLabelValue {
		return nil, ErrScheduleCannotRun
	}
	template, found, err := unstructured.NestedMap(schedule.Object, "spec", "template")
	if err != nil {
		return nil, fmt.Errorf("read the schedule's template: %w", err)
	}
	if !found {
		template = map[string]any{}
	}

	labels := map[string]string{}
	if tl, ok, _ := unstructured.NestedStringMap(template, "metadata", "labels"); ok && tl != nil {
		maps.Copy(labels, tl)
	} else {
		maps.Copy(labels, schedule.GetLabels())
	}
	labels[ScheduleNameLabel] = schedule.GetName()

	backup := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": Group + "/v1",
		"kind":       "Backup",
		"spec":       template,
	}}
	backup.SetNamespace(schedule.GetNamespace())
	backup.SetName(fmt.Sprintf("%s-%s", schedule.GetName(), now.UTC().Format("20060102150405")))
	backup.SetLabels(labels)
	if annotations := schedule.GetAnnotations(); len(annotations) > 0 {
		copied := map[string]string{}
		for k, v := range annotations {
			if k != lastAppliedAnnotation {
				copied[k] = v
			}
		}
		if len(copied) > 0 {
			backup.SetAnnotations(copied)
		}
	}
	if owner, _, _ := unstructured.NestedBool(schedule.Object, "spec", "useOwnerReferencesInBackup"); owner {
		controller := true
		backup.SetOwnerReferences([]metav1.OwnerReference{{
			APIVersion: Group + "/v1", Kind: "Schedule", Name: schedule.GetName(), UID: schedule.GetUID(), Controller: &controller,
		}})
	}
	return backup, nil
}
