package velero

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

func scheduleForBackup(name string, labels, annotations map[string]string, spec map[string]any) *unstructured.Unstructured {
	s := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "velero.io/v1", "kind": "Schedule", "spec": spec}}
	s.SetNamespace("velero")
	s.SetName(name)
	s.SetUID(types.UID("sched-uid"))
	s.SetLabels(labels)
	s.SetAnnotations(annotations)
	return s
}

// The shape Velero's own `velero backup create --from-schedule` produces.
func TestBackupFromScheduleMatchesVelerosCLI(t *testing.T) {
	at := time.Date(2026, 11, 15, 21, 5, 9, 0, time.FixedZone("x", 3600))
	template := map[string]any{
		"includedNamespaces": []any{"payments"},
		"storageLocation":    "default",
		"ttl":                "720h0m0s",
	}
	s := scheduleForBackup("nightly", map[string]string{"team": "core"},
		map[string]string{"owner": "sre", lastAppliedAnnotation: "{...}"},
		map[string]any{"schedule": "0 1 * * *", "template": template, "useOwnerReferencesInBackup": true})

	b, err := BackupFromSchedule(s, at)
	if err != nil {
		t.Fatal(err)
	}
	if b.GetName() != "nightly-20261115200509" || b.GetNamespace() != "velero" || b.GetKind() != "Backup" {
		t.Errorf("identity %s/%s %s, want velero/nightly-<UTC timestamp> Backup", b.GetNamespace(), b.GetName(), b.GetKind())
	}
	if want := map[string]string{"team": "core", ScheduleNameLabel: "nightly"}; !reflect.DeepEqual(b.GetLabels(), want) {
		t.Errorf("labels %v, want the schedule's plus the schedule-name label %v", b.GetLabels(), want)
	}
	if want := map[string]string{"owner": "sre"}; !reflect.DeepEqual(b.GetAnnotations(), want) {
		t.Errorf("annotations %v, want the schedule's without kubectl's last-applied %v", b.GetAnnotations(), want)
	}
	spec, _, _ := unstructured.NestedMap(b.Object, "spec")
	if !reflect.DeepEqual(spec, template) {
		t.Errorf("spec %v, want the schedule's template %v", spec, template)
	}
	refs := b.GetOwnerReferences()
	if len(refs) != 1 || refs[0].Kind != "Schedule" || refs[0].UID != "sched-uid" || refs[0].Controller == nil || !*refs[0].Controller {
		t.Errorf("owner references %+v, want the schedule as controller", refs)
	}
}

func TestBackupFromScheduleTemplateLabelsReplaceTheSchedules(t *testing.T) {
	s := scheduleForBackup("nightly", map[string]string{"team": "core"}, nil, map[string]any{
		"template": map[string]any{"metadata": map[string]any{"labels": map[string]any{"tier": "gold"}}},
	})
	b, err := BackupFromSchedule(s, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"tier": "gold", ScheduleNameLabel: "nightly"}; !reflect.DeepEqual(b.GetLabels(), want) {
		t.Errorf("labels %v, want %v", b.GetLabels(), want)
	}
	if len(b.GetOwnerReferences()) != 0 || b.GetAnnotations() != nil {
		t.Errorf("no owner reference or annotations unless the schedule has them: %+v %v", b.GetOwnerReferences(), b.GetAnnotations())
	}
}

func TestBackupFromScheduleRefusesANameVeleroCannotLabel(t *testing.T) {
	s := scheduleForBackup(strings.Repeat("x", 64), nil, nil, map[string]any{"template": map[string]any{}})
	if _, err := BackupFromSchedule(s, time.Now()); !errors.Is(err, ErrScheduleCannotRun) {
		t.Errorf("err = %v, want ErrScheduleCannotRun", err)
	}
}
