package cnpg

import (
	"encoding/json"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"net/http"
	"strings"
	"testing"
)

func TestCNPGReportOperatorProjection(t *testing.T) {
	resp := &CNPGOperatorResponse{Config: []CNPGOperatorConfigRef{
		{Kind: "ConfigMap", Name: "operator", CNPGOperatorConfigMapState: &CNPGOperatorConfigMapState{Data: map[string]string{"Z": "private-z", "A": "private-a"}}},
		{Kind: "Secret", Name: "operator-credentials"},
	}}
	data, err := json.Marshal(cnpgReportOperator(resp))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private-") || !strings.Contains(string(data), `"keys":["A","Z"]`) || !strings.Contains(string(data), "operator-credentials") {
		t.Fatalf("report projection: %s", data)
	}
	if resp.Config[0].Data["A"] != "private-a" {
		t.Fatal("report mutated the endpoint's response")
	}
}

func TestCNPGReportReadErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		state  string
	}{{http.StatusForbidden, cnpgReadDenied}, {http.StatusNotFound, cnpgReadNotFound}, {http.StatusServiceUnavailable, cnpgReadError}} {
		index := CNPGReportIndex{}
		builder := cnpgReportBuilder{index: &index}
		builder.readJSON("Runtime", "runtime.json", nil, &ReadFailure{tc.status, "unavailable"})
		if len(index.Contents) != 1 || index.Contents[0].State != tc.state || index.Contents[0].File != "" {
			t.Fatalf("read error: %+v", index.Contents)
		}
	}
}

func TestReportWithholdsInitializationSQLAndInlineConnectionPasswords(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{
		"bootstrap": map[string]any{"initdb": map[string]any{
			"postInitSQL":            []any{"CREATE ROLE app PASSWORD 'secret-sql'"},
			"postInitApplicationSQL": []any{"private-application-sql"},
			"postInitTemplateSQL":    []any{"private-template-sql"},
			"postInitSQLRefs":        map[string]any{"secretRefs": []any{map[string]any{"name": "sql-secret", "key": "sql"}}},
		}},
		"externalClusters": []any{map[string]any{"name": "origin", "connectionParameters": map[string]any{"host": "pg-rw", "password": "secret-inline", "sslpassword": "secret-ssl"}, "password": map[string]any{"name": "password-secret", "key": "password"}}},
	}}}
	data, err := json.Marshal(cnpgReportCleanObject(obj))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret-sql", "private-application-sql", "private-template-sql", "secret-inline", "secret-ssl"} {
		if strings.Contains(string(data), secret) {
			t.Errorf("report exposed %s: %s", secret, data)
		}
	}
	for _, reference := range []string{"pg-rw", "sql-secret", "password-secret"} {
		if !strings.Contains(string(data), reference) {
			t.Errorf("report lost reference %s", reference)
		}
	}
	original, _ := json.Marshal(obj)
	if !strings.Contains(string(original), "secret-sql") {
		t.Fatal("redaction changed source")
	}
}
