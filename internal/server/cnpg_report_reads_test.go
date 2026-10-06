package server

import (
	"encoding/json"
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
		builder.readJSON("Runtime", "runtime.json", nil, &cnpgReadFailure{tc.status, "unavailable"})
		if len(index.Contents) != 1 || index.Contents[0].State != tc.state || index.Contents[0].File != "" {
			t.Fatalf("read error: %+v", index.Contents)
		}
	}
}
