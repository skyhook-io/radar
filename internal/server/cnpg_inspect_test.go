package server

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestParseCNPGTimelineHistoryChainsSwitches(t *testing.T) {
	text := "# comment\n1\t0/3000000\tno recovery target specified\n\n2\t0/5000A28\tbefore 2026-10-01 12:00:00.123456+00\n"
	got := parseCNPGTimelineHistory(text, 3)
	want := []CNPGTimelineSwitch{
		{From: 1, To: 2, SwitchLSN: "0/3000000", Reason: "no recovery target specified"},
		{From: 2, To: 3, SwitchLSN: "0/5000A28", Reason: "before 2026-10-01 12:00:00.123456+00"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("history = %+v", got)
	}
}

func cnpgRestoreFactsJSON(history string) string {
	h := "null"
	if history != "" {
		h = `"` + strings.ReplaceAll(strings.ReplaceAll(history, "\t", `\t`), "\n", `\n`) + `"`
	}
	return `{"inRecovery" : false, "timeline" : 2, "history" : ` + h + `, "databaseCount" : 2, "databases" : [{"name":"app","bytes":8000000},{"name":"postgres","bytes":7000000}], "roleCount" : 3, "roles" : [{"name":"app","canLogin":true},{"name":"postgres","canLogin":true},{"name":"streaming_replica","canLogin":true}]}`
}

func cnpgSequencedExec(calls *[]cnpgExecCall, outs ...string) cnpgExecFunc {
	return func(_ context.Context, _ string, pod, container string, argv []string, stdin string) ([]byte, error) {
		*calls = append(*calls, cnpgExecCall{pod: pod, container: container, argv: argv, stdin: stdin})
		if len(*calls) > len(outs) {
			return nil, errors.New("unexpected exec")
		}
		return []byte(outs[len(*calls)-1]), nil
	}
}

func TestReadCNPGRestoreChecksReadsFactsThenTheBootstrapDatabase(t *testing.T) {
	var calls []cnpgExecCall
	contents := `{"tables" : 3, "estimatedRows" : 12000, "noEstimate" : 1, "largest" : [{"name":"public.orders","estimatedRows":10000,"bytes":4096000}]}`
	resp := CNPGRestoreChecksResponse{Database: "app"}
	readCNPGRestoreChecks(context.Background(), cnpgSequencedExec(&calls, cnpgRestoreFactsJSON("1\t0/5000A28\tbefore 2026-10-01 12:00:00+00\n"), contents), "db", "pg-r-1", &resp)
	if resp.State != cnpgRuntimeStateOK || resp.CNPGRestoreFacts == nil || resp.Timeline != 2 || len(resp.History) != 1 || resp.History[0].To != 2 {
		t.Fatalf("facts = %+v / %+v", resp.CNPGRuntimeSource, resp.CNPGRestoreFacts)
	}
	if resp.Contents == nil || resp.Contents.Tables != 3 || resp.ContentsSource == nil || resp.ContentsSource.State != cnpgRuntimeStateOK {
		t.Fatalf("contents = %+v / %+v", resp.Contents, resp.ContentsSource)
	}
	if len(calls) != 2 || strings.Join(calls[0].argv, " ") != "psql -XAtq -v ON_ERROR_STOP=1 -d postgres -f -" || calls[0].stdin != cnpgRestoreFactsSQL {
		t.Errorf("first exec = %+v", calls[0])
	}
	if strings.Join(calls[1].argv, " ") != "psql -XAtq -v ON_ERROR_STOP=1 -d app -f -" || calls[1].stdin != cnpgDatabaseContentsSQL {
		t.Errorf("second exec = %+v", calls[1])
	}
	for _, sql := range []string{cnpgRestoreFactsSQL, cnpgDatabaseContentsSQL} {
		if !strings.Contains(sql, "statement_timeout") || strings.Contains(strings.ToUpper(sql), "INSERT") || strings.Contains(strings.ToUpper(sql), "UPDATE ") {
			t.Error("inspection SQL must be bounded and read-only")
		}
	}
}

func TestReadCNPGRestoreChecksWithoutHistoryOrUsableDatabase(t *testing.T) {
	var calls []cnpgExecCall
	resp := CNPGRestoreChecksResponse{Database: "host=elsewhere dbname=app"}
	readCNPGRestoreChecks(context.Background(), cnpgSequencedExec(&calls, cnpgRestoreFactsJSON("")), "db", "pg-r-1", &resp)
	if !resp.HistoryMissing || len(resp.History) != 0 {
		t.Errorf("a missing history file is reported as such: %+v", resp.CNPGRestoreFacts)
	}
	// A connection string is never handed to psql's -d.
	if len(calls) != 1 || resp.Contents != nil || resp.ContentsSource == nil || resp.ContentsSource.State != cnpgRuntimeStateError {
		t.Errorf("calls = %d, contents source = %+v", len(calls), resp.ContentsSource)
	}

	calls = nil
	resp = CNPGRestoreChecksResponse{Database: "shop"}
	readCNPGRestoreChecks(context.Background(), cnpgSequencedExec(&calls, cnpgRestoreFactsJSON("")), "db", "pg-r-1", &resp)
	if len(calls) != 1 || resp.ContentsSource == nil || !strings.Contains(resp.ContentsSource.Error, `"shop" is not in the list`) {
		t.Errorf("a database the complete list lacks is not connected to: %+v", resp.ContentsSource)
	}

	resp = CNPGRestoreChecksResponse{Database: "app"}
	readCNPGRestoreChecks(context.Background(), cnpgFakeExec(&calls, "", errors.New(`pods "pg-r-1" is forbidden: User "bob" cannot create resource "pods/exec"`)), "db", "pg-r-1", &resp)
	if resp.State != cnpgRuntimeStateDenied || resp.CNPGRestoreFacts != nil {
		t.Errorf("forbidden exec = %+v", resp.CNPGRuntimeSource)
	}
}

func TestCNPGDeclaredParametersSkipsUnsafeNames(t *testing.T) {
	cluster := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"postgresql": map[string]any{"parameters": map[string]any{
			"shared_buffers":         "256MB",
			"pg_stat_statements.max": "10000",
			"Work_Mem":               "8MB",
			"bad,name":               "x",
		}}},
	}}
	declared, query, skipped, omitted := cnpgDeclaredParameters(cluster)
	if len(declared) != 4 || omitted != 0 {
		t.Errorf("declared = %+v", declared)
	}
	if !slices.Equal(query, []string{"work_mem", "pg_stat_statements.max", "shared_buffers"}) {
		t.Errorf("query = %v", query)
	}
	if !slices.Equal(skipped, []string{"bad,name"}) {
		t.Errorf("skipped = %v", skipped)
	}
}

func TestReadCNPGParametersPassesNamesAsAVariable(t *testing.T) {
	var calls []cnpgExecCall
	rows := `[{"name":"application_name","value":null,"setByClient":true,"source":"client","context":"user","pendingRestart":false},{"name":"shared_buffers","value":"256MB","setByClient":false,"source":"configuration file","context":"postmaster","pendingRestart":true}]`
	pods := []*corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "pg-1"}}}
	got := readCNPGParameters(context.Background(), cnpgFakeExec(&calls, rows, nil), "db", pods, []string{"shared_buffers", "work_mem"})
	if len(got) != 1 || got[0].State != cnpgRuntimeStateOK || len(got[0].Settings) != 2 {
		t.Fatalf("settings = %+v", got)
	}
	if sb := got[0].Settings[1]; sb.Value == nil || *sb.Value != "256MB" || !sb.PendingRestart || sb.Context != "postmaster" {
		t.Errorf("shared_buffers = %+v", sb)
	}
	// The connection sets application_name itself, so its server value is not shown.
	if an := got[0].Settings[0]; an.Value != nil || !an.SetByClient {
		t.Errorf("application_name = %+v", an)
	}
	if strings.Count(cnpgParametersSQL, "SET ") != 1 || !strings.HasPrefix(cnpgParametersSQL, "SET search_path = pg_catalog;") {
		t.Error("the parameters read sets only search_path: any other SET hides the server value of what it sets")
	}
	if !slices.Contains(calls[0].argv, "names=shared_buffers,work_mem") || strings.Contains(calls[0].stdin, "shared_buffers") || !strings.Contains(calls[0].stdin, ":'names'") {
		t.Errorf("names must travel as a psql variable, never in the SQL text: %+v", calls[0])
	}

	got = readCNPGParameters(context.Background(), cnpgFakeExec(&calls, "", context.DeadlineExceeded), "db", pods, []string{"work_mem"})
	if got[0].State != cnpgRuntimeStateUnreachable || got[0].Settings != nil {
		t.Errorf("an instance that does not answer reads unreachable: %+v", got[0])
	}
}

// Every diagnostic runs as the postgres superuser, so each pins search_path
// before anything else; an application schema's function must never stand in
// for a built-in.
func TestCNPGDiagnosticSQLPinsSearchPath(t *testing.T) {
	for name, sql := range map[string]string{
		"blocking": cnpgBlockingSQL,
		"signal":   cnpgSignalSQL("pg_cancel_backend"),
		"restore":  cnpgRestoreFactsSQL,
		"contents": cnpgDatabaseContentsSQL,
		"params":   cnpgParametersSQL,
	} {
		if !strings.HasPrefix(sql, "SET search_path = pg_catalog;") {
			t.Errorf("%s SQL does not start by pinning search_path", name)
		}
	}
	for name, sql := range map[string]string{"blocking": cnpgBlockingSQL, "restore": cnpgRestoreFactsSQL, "contents": cnpgDatabaseContentsSQL} {
		if !strings.Contains(sql, "default_transaction_read_only = on") {
			t.Errorf("%s SQL only reads, so it runs read-only", name)
		}
	}
}
