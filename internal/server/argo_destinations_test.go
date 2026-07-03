package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestArgoDestinations_InventoryWithoutSecretData(t *testing.T) {
	resp, err := http.Get(testServer.URL + "/api/argo/destinations")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)

	// The backing Secret carries credentials in data.config — they must
	// never appear anywhere in the response, under any key.
	if strings.Contains(string(raw), "SUPER-SECRET-TOKEN") || strings.Contains(string(raw), "bearerToken") {
		t.Fatalf("response leaks secret material: %s", raw)
	}
	if strings.Contains(string(raw), "NOT-A-URL-TOKEN") {
		t.Fatalf("response reflects non-URL server bytes: %s", raw)
	}
	if strings.Contains(string(raw), "EMBEDDED-CRED-TOKEN") {
		t.Fatalf("response reflects URL userinfo credentials: %s", raw)
	}
	if strings.Contains(string(raw), "QUERY-CRED-TOKEN") || strings.Contains(string(raw), "FRAG-CRED") {
		t.Fatalf("response reflects query/fragment credentials: %s", raw)
	}

	var got ArgoDestinationsResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Destinations) != 2 {
		t.Fatalf("destinations = %+v, want the valid fixture + the sanitized query-token one", got.Destinations)
	}
	for _, d := range got.Destinations {
		if d.Name == "querytoken-cluster" && d.Server != "https://apiserver2.example" {
			t.Errorf("query-token server not reduced to scheme://host: %q", d.Server)
		}
	}
	d := got.Destinations[0]
	if d.Name != "prod-us-east1" || d.Server != "https://34.10.0.1" || d.SecretNamespace != "argocd" || d.SecretName != "cluster-prod" {
		t.Errorf("destination = %+v", d)
	}
	if !got.Completeness.Complete {
		t.Errorf("completeness = %+v, want complete (fixture cache is cluster-wide)", got.Completeness)
	}
}
