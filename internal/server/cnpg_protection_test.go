package server

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/integration"
)

func TestCNPGArchivingPreviewRejectsInvalidAndOversizedBodies(t *testing.T) {
	seedCNPGWorkspace(t, cnpgWorkspaceTestKinds)
	for _, tc := range []struct {
		name   string
		body   string
		status int
	}{
		{"malformed", `{`, http.StatusBadRequest},
		{"unknown field", `{"reviewedContext":"ctx","objectStore":"store","serverName":"pg","extra":true}`, http.StatusBadRequest},
		{"too large", `{"objectStore":"` + strings.Repeat("x", integration.ActionBodyLimit) + `"}`, http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response, err := http.Post(testServer.URL+"/api/cnpg/clusters/db/pg/protection/preview", "application/json", strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, _ := io.ReadAll(response.Body)
			if response.StatusCode != tc.status {
				t.Fatalf("status=%d body=%s", response.StatusCode, body)
			}
		})
	}
}

func TestCNPGDraftSchedulePreviewReadsTargetAndUsesNow(t *testing.T) {
	cluster := cnpgObj("postgresql.cnpg.io/v1", "Cluster", "setup", "pg", map[string]any{}, nil)
	seedCNPGWorkspace(t, cnpgWorkspaceTestKinds, cluster)
	for _, tc := range []struct {
		name   string
		status int
	}{{"pg", http.StatusOK}, {"missing", http.StatusNotFound}} {
		response, err := http.Get(testServer.URL + "/api/cnpg/clusters/setup/" + tc.name + "/schedule-preview?schedule=0%200%202%20*%20*%20*")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != tc.status {
			t.Fatalf("status=%d body=%s", response.StatusCode, body)
		}
		if tc.status == http.StatusOK && (!strings.Contains(string(body), `"basis":"now"`) || !strings.Contains(string(body), `"valid":true`)) {
			t.Fatalf("draft preview=%s", body)
		}
	}
}
