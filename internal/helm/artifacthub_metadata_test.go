package helm

import (
	"encoding/json"
	"testing"
)

func TestArtifactHubPublisherMetadata(t *testing.T) {
	var raw artifactHubPackage
	if err := json.Unmarshal([]byte(`{"package_id":"package","name":"podinfo","official":true,"repository":{"name":"charts","url":"https://charts.example.test","organization_name":"example","organization_display_name":"Example Team","user_alias":"publisher","display_name":"Example Charts","verified_publisher":true}}`), &raw); err != nil {
		t.Fatal(err)
	}
	chart := convertArtifactHubPackage(raw)
	if !chart.Official || chart.Repository.Official || !chart.Repository.VerifiedPublisher {
		t.Fatalf("package/repository badge scopes changed: %#v", chart)
	}
	if chart.Repository.OrganizationName != "example" || chart.Repository.OrganizationDisplayName != "Example Team" || chart.Repository.UserAlias != "publisher" || chart.Repository.DisplayName != "Example Charts" {
		t.Fatalf("publisher metadata lost: %#v", chart.Repository)
	}
	body, err := json.Marshal(chart)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	if string(wire["official"]) != "true" {
		t.Fatalf("package official flag missing from public response: %s", body)
	}
}
