package audit

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func cnpgObj(kind, ns, name string, spec map[string]any) *unstructured.Unstructured {
	if spec == nil {
		spec = map[string]any{}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       kind,
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"spec":       spec,
	}}
}

func cnpgScheduledBackup(ns, name, clusterName, method string) *unstructured.Unstructured {
	return cnpgObj("ScheduledBackup", ns, name, map[string]any{
		"cluster":  map[string]any{"name": clusterName},
		"schedule": "0 0 2 * * *", // CNPG crons are six-field
		"method":   method,
	})
}

func TestCNPGDeclarativeBackup_FlagsClusterWithNoSchedule(t *testing.T) {
	input := &CheckInput{
		CNPGClusters:                      []*unstructured.Unstructured{cnpgObj("Cluster", "pg", "unprotected", nil)},
		CNPGScheduledBackups:              nil,
		CNPGScheduledBackupsAuthoritative: true,
	}
	tr := newEvalTracker()
	findings := checkCNPGDeclarativeBackup(tr, input)

	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(findings), findings)
	}
	f := findings[0]
	if f.CheckID != checkCNPGNoDeclarativeBackup {
		t.Errorf("CheckID = %q, want %q", f.CheckID, checkCNPGNoDeclarativeBackup)
	}
	if f.Severity != SeverityWarning {
		t.Errorf("Severity = %q, want %q (posture, not breakage)", f.Severity, SeverityWarning)
	}
	if f.Category != CategoryReliability {
		t.Errorf("Category = %q, want %q", f.Category, CategoryReliability)
	}
	if f.Kind != "Cluster" || f.Namespace != "pg" || f.Name != "unprotected" {
		t.Errorf("wrong subject: %+v", f)
	}
	// Group must stay empty — the audit backfills it, and the per-resource
	// drill-down looks CRDs up under "".
	if f.Group != "" {
		t.Errorf("Group = %q, want empty", f.Group)
	}
	if tr.counts[checkCNPGNoDeclarativeBackup]["pg"] != 1 {
		t.Errorf("expected the cluster to be counted as evaluated once")
	}
}

func TestCNPGDeclarativeBackup_ScheduleSatisfiesCheck(t *testing.T) {
	// All three methods are declarative schedules; the plugin one is the case
	// that a barmanObjectStore-only heuristic would miss.
	for _, method := range []string{"barmanObjectStore", "volumeSnapshot", "plugin"} {
		t.Run(method, func(t *testing.T) {
			input := &CheckInput{
				CNPGClusters:                      []*unstructured.Unstructured{cnpgObj("Cluster", "pg", "protected", nil)},
				CNPGScheduledBackups:              []*unstructured.Unstructured{cnpgScheduledBackup("pg", "nightly", "protected", method)},
				CNPGScheduledBackupsAuthoritative: true,
			}
			tr := newEvalTracker()
			if findings := checkCNPGDeclarativeBackup(tr, input); len(findings) != 0 {
				t.Fatalf("expected no findings, got %+v", findings)
			}
			if tr.counts[checkCNPGNoDeclarativeBackup]["pg"] != 1 {
				t.Errorf("a passing cluster must still count as evaluated")
			}
		})
	}
}

func TestCNPGDeclarativeBackup_SuspendedScheduleStillCounts(t *testing.T) {
	// Suspension is deliberate operator intent; re-reporting it here would
	// duplicate intent as a defect.
	sb := cnpgScheduledBackup("pg", "nightly", "protected", "barmanObjectStore")
	_ = unstructured.SetNestedField(sb.Object, true, "spec", "suspend")
	input := &CheckInput{
		CNPGClusters:                      []*unstructured.Unstructured{cnpgObj("Cluster", "pg", "protected", nil)},
		CNPGScheduledBackups:              []*unstructured.Unstructured{sb},
		CNPGScheduledBackupsAuthoritative: true,
	}
	if findings := checkCNPGDeclarativeBackup(newEvalTracker(), input); len(findings) != 0 {
		t.Fatalf("suspended schedule should still count as declared, got %+v", findings)
	}
}

func TestCNPGDeclarativeBackup_ScheduleInOtherNamespaceDoesNotCount(t *testing.T) {
	input := &CheckInput{
		CNPGClusters:                      []*unstructured.Unstructured{cnpgObj("Cluster", "pg", "app", nil)},
		CNPGScheduledBackups:              []*unstructured.Unstructured{cnpgScheduledBackup("other", "nightly", "app", "barmanObjectStore")},
		CNPGScheduledBackupsAuthoritative: true,
	}
	if findings := checkCNPGDeclarativeBackup(newEvalTracker(), input); len(findings) != 1 {
		t.Fatalf("a schedule in a different namespace targets a different cluster, got %+v", findings)
	}
}

// The coverage gate: without a synced cluster-wide ScheduledBackup informer the
// inventory may cover a subset of namespaces, so absence proves nothing.
func TestCNPGDeclarativeBackup_NonAuthoritativeInventoryIsNotEvaluated(t *testing.T) {
	input := &CheckInput{
		CNPGClusters:                      []*unstructured.Unstructured{cnpgObj("Cluster", "pg", "unprotected", nil)},
		CNPGScheduledBackups:              nil,
		CNPGScheduledBackupsAuthoritative: false,
	}
	tr := newEvalTracker()
	if findings := checkCNPGDeclarativeBackup(tr, input); len(findings) != 0 {
		t.Fatalf("expected no findings when absence isn't provable, got %+v", findings)
	}
	if len(tr.counts) != 0 {
		t.Fatalf("a check that couldn't run must not record evaluations, got %+v", tr.counts)
	}
}

func TestCNPGDeclarativeBackup_NoClustersIsNotEvaluated(t *testing.T) {
	tr := newEvalTracker()
	if findings := checkCNPGDeclarativeBackup(tr, &CheckInput{CNPGScheduledBackupsAuthoritative: true}); len(findings) != 0 {
		t.Fatalf("expected no findings without CNPG, got %+v", findings)
	}
	if len(tr.counts) != 0 {
		t.Fatalf("CNPG-absent cluster must record nothing, got %+v", tr.counts)
	}
}

func TestCNPGDeclarativeBackup_MessageNamesTheBackupMode(t *testing.T) {
	cases := []struct {
		name string
		spec map[string]any
		want string
	}{
		{
			name: "plugin",
			spec: map[string]any{"plugins": []any{map[string]any{"name": cnpgBarmanPluginName}}},
			want: "spec.method: plugin",
		},
		{
			name: "in-tree",
			spec: map[string]any{"backup": map[string]any{"barmanObjectStore": map[string]any{"destinationPath": "s3://b"}}},
			want: "spec.backup.barmanObjectStore",
		},
		{
			name: "nothing configured",
			spec: map[string]any{},
			want: "No backup destination is configured",
		},
		{
			name: "explicitly disabled plugin reads as unconfigured",
			spec: map[string]any{"plugins": []any{map[string]any{"name": cnpgBarmanPluginName, "enabled": false}}},
			want: "No backup destination is configured",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := &CheckInput{
				CNPGClusters:                      []*unstructured.Unstructured{cnpgObj("Cluster", "pg", "c", tc.spec)},
				CNPGScheduledBackupsAuthoritative: true,
			}
			findings := checkCNPGDeclarativeBackup(newEvalTracker(), input)
			if len(findings) != 1 {
				t.Fatalf("expected 1 finding, got %+v", findings)
			}
			if !strings.Contains(findings[0].Message, tc.want) {
				t.Errorf("message %q should mention %q", findings[0].Message, tc.want)
			}
			// Never assert backups don't exist — only that none is declared.
			if strings.Contains(findings[0].Message, "is not backed up") {
				t.Errorf("message overclaims: %q", findings[0].Message)
			}
		})
	}
}

func TestCNPGDeclarativeBackup_CountsRollupAndRegistry(t *testing.T) {
	input := &CheckInput{
		CNPGClusters: []*unstructured.Unstructured{
			cnpgObj("Cluster", "pg", "protected", nil),
			cnpgObj("Cluster", "pg", "unprotected", nil),
		},
		CNPGScheduledBackups:              []*unstructured.Unstructured{cnpgScheduledBackup("pg", "nightly", "protected", "plugin")},
		CNPGScheduledBackupsAuthoritative: true,
	}
	results := RunChecks(input)

	got, ok := results.CheckCounts[checkCNPGNoDeclarativeBackup]
	if !ok {
		t.Fatalf("check missing from CheckCounts: %+v", results.CheckCounts)
	}
	if got.Evaluated != 2 || got.Passed != 1 {
		t.Errorf("counts = %+v, want Evaluated 2 / Passed 1", got)
	}
	if _, ok := CheckRegistry[checkCNPGNoDeclarativeBackup]; !ok {
		t.Errorf("check has no CheckRegistry entry — it would be untoggleable and uncatalogued")
	}
	for _, in := range results.MissingInputs {
		if strings.Contains(strings.ToLower(in), "cnpg") {
			t.Errorf("a self-gated check must not report a missing input, got %q", in)
		}
	}
}
