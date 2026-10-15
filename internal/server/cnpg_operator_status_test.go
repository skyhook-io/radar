package server

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	cnpgsvc "github.com/skyhook-io/radar/internal/cnpg"
)

func TestCNPGOperatorStatusNamespaceParameters(t *testing.T) {
	seedCNPGWorkspace(t, cnpgWorkspaceTestKinds)
	for _, query := range []string{"?namespace=db", "?namespaces=db"} {
		resp, err := http.Get(testServer.URL + "/api/cnpg/operator/status" + query)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d, %s, %v", query, resp.StatusCode, body, err)
		}
		var result cnpgsvc.CNPGOperatorStatusResponse
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatal(err)
		}
		if _, present := result.Namespaces["db"]; !present {
			t.Fatalf("%s omitted the requested namespace: %s", query, body)
		}
	}
}
