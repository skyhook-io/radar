package audit

import (
	"sort"
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

// cnpgInTree is a Cluster spec with an in-tree backup destination, so the
// schedule check evaluates it (a Cluster with no destination is the
// destination rule's finding, not the schedule's).
func cnpgInTree() map[string]any {
	return map[string]any{"backup": map[string]any{"barmanObjectStore": map[string]any{"destinationPath": "s3://b"}}}
}

func TestCNPGDeclarativeBackup_FlagsClusterWithNoSchedule(t *testing.T) {
	input := &CheckInput{
		CNPGClusters:                      []*unstructured.Unstructured{cnpgObj("Cluster", "pg", "unprotected", cnpgInTree())},
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
	if f.Group != "postgresql.cnpg.io" {
		t.Errorf("Group = %q, want postgresql.cnpg.io", f.Group)
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
				CNPGClusters:                      []*unstructured.Unstructured{cnpgObj("Cluster", "pg", "protected", cnpgInTree())},
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
		CNPGClusters:                      []*unstructured.Unstructured{cnpgObj("Cluster", "pg", "app", cnpgInTree())},
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
			spec: map[string]any{"plugins": []any{map[string]any{"name": cnpgBarmanPluginName, "parameters": map[string]any{"barmanObjectName": "store"}}}},
			want: "spec.method: plugin",
		},
		{
			name: "in-tree",
			spec: map[string]any{"backup": map[string]any{"barmanObjectStore": map[string]any{"destinationPath": "s3://b"}}},
			want: "spec.backup.barmanObjectStore",
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
			cnpgObj("Cluster", "pg", "protected", cnpgInTree()),
			cnpgObj("Cluster", "pg", "unprotected", cnpgInTree()),
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

// A schedule can't run without a destination, so a Cluster with none gets the
// destination finding only, and the schedule check doesn't evaluate it.
func TestCNPGDeclarativeBackup_SkipsClusterWithNoDestination(t *testing.T) {
	for name, spec := range map[string]map[string]any{
		"nothing":         nil,
		"disabled plugin": {"plugins": []any{map[string]any{"name": cnpgBarmanPluginName, "enabled": false}}},
	} {
		t.Run(name, func(t *testing.T) {
			tr := newEvalTracker()
			input := &CheckInput{CNPGClusters: []*unstructured.Unstructured{cnpgObj("Cluster", "pg", "c", spec)}, CNPGScheduledBackupsAuthoritative: true}
			if findings := checkCNPGDeclarativeBackup(tr, input); len(findings) != 0 {
				t.Fatalf("expected no schedule finding, got %+v", findings)
			}
			if tr.counts[checkCNPGNoDeclarativeBackup]["pg"] != 0 {
				t.Errorf("a Cluster the check skipped must not count as evaluated")
			}
		})
	}
}

func cnpgPostureIDs(findings []Finding, name string) []string {
	var ids []string
	for _, f := range findings {
		if f.Name == name && strings.HasPrefix(f.CheckID, "cnpg") {
			ids = append(ids, f.CheckID)
		}
	}
	sort.Strings(ids)
	return ids
}

func barmanPlugin(store string, archiver bool) map[string]any {
	p := map[string]any{"name": cnpgBarmanPluginName}
	if store != "" {
		p["parameters"] = map[string]any{"barmanObjectName": store}
	}
	if archiver {
		p["isWALArchiver"] = true
	}
	return p
}

func TestCNPGClusterPosture_NoDestinationIsOneFinding(t *testing.T) {
	results := RunChecks(&CheckInput{
		CNPGClusters:                      []*unstructured.Unstructured{cnpgObj("Cluster", "pg", "c", map[string]any{"instances": int64(3)})},
		CNPGScheduledBackupsAuthoritative: true,
	})
	if got := cnpgPostureIDs(results.Findings, "c"); len(got) != 1 || got[0] != checkCNPGNoBackupDestination {
		t.Fatalf("findings = %v, want only %s", got, checkCNPGNoBackupDestination)
	}
	for _, id := range []string{checkCNPGNoDeclarativeBackup, checkCNPGNoWALArchiving} {
		if c, ok := results.CheckCounts[id]; ok && c.Evaluated != 0 {
			t.Errorf("%s evaluated a Cluster with no destination: %+v", id, c)
		}
	}
}

func TestCNPGClusterPosture_Matrix(t *testing.T) {
	inTree := map[string]any{"barmanObjectStore": map[string]any{"destinationPath": "s3://b"}}
	cases := []struct {
		name string
		spec map[string]any
		want []string
	}{
		{"empty spec", map[string]any{}, []string{checkCNPGNoBackupDestination, checkCNPGSingleInstance}},
		{"archiver plugin without a store", map[string]any{"instances": int64(2), "plugins": []any{barmanPlugin("", true)}}, []string{checkCNPGNoBackupDestination}},
		{"snapshots only", map[string]any{"instances": int64(2), "backup": map[string]any{"volumeSnapshot": map[string]any{}}}, []string{checkCNPGNoWALArchiving}},
		{"in-tree", map[string]any{"instances": int64(2), "backup": inTree}, []string{checkCNPGInTreeBackupDeprecated}},
		{"in-tree beside the plugin", map[string]any{"instances": int64(2), "backup": inTree, "plugins": []any{barmanPlugin("store", true)}}, nil},
		{"plugin that does not archive WAL", map[string]any{"instances": int64(2), "plugins": []any{barmanPlugin("store", false)}}, []string{checkCNPGNoWALArchiving}},
		{"one instance", map[string]any{"instances": int64(1), "plugins": []any{barmanPlugin("store", true)}}, []string{checkCNPGSingleInstance}},
		{"empty in-tree block", map[string]any{"instances": int64(2), "backup": map[string]any{"barmanObjectStore": map[string]any{}}}, []string{checkCNPGNoBackupDestination}},
		{"snapshots beside an archiver plugin without a store", map[string]any{"instances": int64(2), "backup": map[string]any{"volumeSnapshot": map[string]any{}}, "plugins": []any{barmanPlugin("", true)}}, []string{checkCNPGNoWALArchiving}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			findings := checkCNPGClusterPosture(newEvalTracker(), &CheckInput{CNPGClusters: []*unstructured.Unstructured{cnpgObj("Cluster", "pg", "c", tc.spec)}})
			want := append([]string(nil), tc.want...)
			sort.Strings(want)
			if got := cnpgPostureIDs(findings, "c"); strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("findings = %v, want %v", got, want)
			}
			for _, f := range findings {
				if f.Kind != "Cluster" || f.Group != "postgresql.cnpg.io" || f.Category != CategoryReliability || f.Severity != SeverityWarning {
					t.Errorf("finding shape: %+v", f)
				}
			}
		})
	}
}

func TestCNPGClusterPosture_MessagesStateWhatIsDeclared(t *testing.T) {
	specs := []map[string]any{
		{},
		{"instances": int64(2), "backup": map[string]any{"volumeSnapshot": map[string]any{}}},
		{"instances": int64(2), "backup": map[string]any{"barmanObjectStore": map[string]any{"destinationPath": "s3://b"}}},
	}
	seen := map[string]bool{}
	for i, spec := range specs {
		for _, f := range checkCNPGClusterPosture(newEvalTracker(), &CheckInput{CNPGClusters: []*unstructured.Unstructured{cnpgObj("Cluster", "pg", "c", spec)}}) {
			seen[f.CheckID] = true
			if !strings.Contains(f.Message, "CNPG") {
				t.Errorf("case %d %s: message should say what CNPG is configured to do: %q", i, f.CheckID, f.Message)
			}
			for _, bad := range []string{"no backups", "unprotected", "not protected", "is not backed up"} {
				if strings.Contains(f.Message, bad) {
					t.Errorf("%s message overclaims (%q): %q", f.CheckID, bad, f.Message)
				}
			}
			if f.CheckID == checkCNPGNoBackupDestination && !strings.Contains(f.Message, "outside CNPG") {
				t.Errorf("destination message should name what Radar can't see: %q", f.Message)
			}
		}
	}
	if len(seen) != 4 {
		t.Fatalf("expected all four rules covered, saw %v", seen)
	}
}

func TestCNPGClusterPosture_RegistryAndCounts(t *testing.T) {
	for _, id := range []string{checkCNPGNoBackupDestination, checkCNPGNoWALArchiving, checkCNPGSingleInstance, checkCNPGInTreeBackupDeprecated, checkCNPGNoDeclarativeBackup} {
		meta, ok := CheckRegistry[id]
		if !ok {
			t.Fatalf("%s has no CheckRegistry entry", id)
		}
		// The titles say what the Cluster declares, like the messages.
		for _, bad := range []string{"has backups", "no backups", "unprotected", "not protected"} {
			if strings.Contains(meta.Title, bad) {
				t.Errorf("%s title overclaims (%q): %q", id, bad, meta.Title)
			}
		}
		if meta.Category != CategoryReliability || meta.DefaultSeverity != sevMedium || meta.BadgeWorthy {
			t.Errorf("%s registry = %+v", id, meta)
		}
	}
	healthy := cnpgObj("Cluster", "pg", "healthy", map[string]any{
		"instances": int64(2),
		"backup":    map[string]any{"barmanObjectStore": map[string]any{"destinationPath": "s3://b"}},
		"plugins":   []any{barmanPlugin("store", true)},
	})
	bare := cnpgObj("Cluster", "pg", "bare", nil)
	results := RunChecks(&CheckInput{CNPGClusters: []*unstructured.Unstructured{healthy, bare}})
	if got := results.CheckCounts[checkCNPGSingleInstance]; got.Evaluated != 2 || got.Passed != 1 {
		t.Errorf("single-instance counts = %+v, want Evaluated 2 / Passed 1", got)
	}

	bare.SetAnnotations(map[string]string{IgnoreChecksAnnotation: checkCNPGSingleInstance})
	results = RunChecks(&CheckInput{CNPGClusters: []*unstructured.Unstructured{healthy, bare}})
	for _, f := range results.Findings {
		if f.Name == "bare" && f.CheckID == checkCNPGSingleInstance {
			t.Fatalf("the ignore-checks annotation should withdraw %s", f.CheckID)
		}
	}
	if got := results.CheckCounts[checkCNPGSingleInstance]; got.Evaluated != 1 || got.Passed != 1 {
		t.Errorf("an ignored Cluster should leave the tally too: %+v, want Evaluated 1 / Passed 1", got)
	}
}
