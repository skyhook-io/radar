package runtimeevidence

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestVaultHAFactsPreservePresence(t *testing.T) {
	for _, test := range []struct {
		name        string
		status      int
		state       string
		optional    string
		wantHA      string
		wantRemoved string
	}{
		{"connected standby", 429, `"initialized":true,"sealed":false,"standby":true`, `,"ha_connection_healthy":true,"removed_from_cluster":false`, "true", "false"},
		{"disconnected standby", 474, `"initialized":true,"sealed":false,"standby":true`, `,"ha_connection_healthy":false,"removed_from_cluster":false`, "false", "false"},
		{"sealed removed node", 530, `"initialized":true,"sealed":true,"standby":true`, `,"removed_from_cluster":true`, "", "true"},
		{"HTTP status does not override body", 200, `"initialized":true,"sealed":false,"standby":true`, `,"ha_connection_healthy":false,"removed_from_cluster":true`, "false", "true"},
		{"active leader without heartbeat", 200, `"initialized":true,"sealed":false,"standby":false`, `,"removed_from_cluster":false`, "", "false"},
		{"absent", 429, `"initialized":true,"sealed":false,"standby":true`, "", "", ""},
		{"null", 429, `"initialized":true,"sealed":false,"standby":true`, `,"ha_connection_healthy":null,"removed_from_cluster":null`, "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			facts, err := Parse(Vault, test.status, []byte("{"+test.state+test.optional+"}"))
			if err != nil || facts.Vault == nil {
				t.Fatalf("Parse: %v", err)
			}
			encoded, err := json.Marshal(facts.Vault)
			if err != nil {
				t.Fatal(err)
			}
			var copied map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &copied); err != nil {
				t.Fatal(err)
			}
			if string(copied["haConnectionHealthy"]) != test.wantHA || string(copied["removedFromCluster"]) != test.wantRemoved {
				t.Fatalf("optional facts lost presence or value: %s", encoded)
			}
		})
	}
}

func TestVaultMalformedOptionalHAFactsDoNotDiscardBaseFacts(t *testing.T) {
	for _, invalid := range []string{`"false"`, `0`, `1`, `{}`, `[]`} {
		for _, field := range []string{"ha_connection_healthy", "removed_from_cluster"} {
			t.Run(field+"/"+invalid, func(t *testing.T) {
				other := "removed_from_cluster"
				if field == other {
					other = "ha_connection_healthy"
				}
				facts, err := Parse(Vault, 429, []byte(`{"initialized":true,"sealed":false,"standby":true,"`+field+`":`+invalid+`,"`+other+`":false}`))
				if err != nil || facts.Vault == nil {
					t.Fatalf("valid base facts discarded: %v", err)
				}
				v := facts.Vault
				if !v.Initialized || v.Sealed || !v.Standby {
					t.Fatalf("base facts changed: %+v", v)
				}
				missing, retained := v.HAConnectionHealthy, v.RemovedFromCluster
				if field == "removed_from_cluster" {
					missing, retained = v.RemovedFromCluster, v.HAConnectionHealthy
				}
				if missing != nil || retained == nil || *retained {
					t.Fatalf("malformed optional fact became false or discarded sibling: %+v", v)
				}
			})
		}
	}
}

func TestVaultHAFactsRejectAmbiguousJSON(t *testing.T) {
	for _, optional := range []string{
		`"ha_connection_healthy":true,"ha_connection_healthy":false`,
		`"removed_from_cluster":false,"Removed_From_Cluster":true`,
		`"ha_connection_healthy":true,`,
	} {
		facts, err := Parse(Vault, 429, []byte(`{"initialized":true,"sealed":false,"standby":true,`+optional+`}`))
		if err != UnexpectedShape || facts.Vault != nil {
			t.Fatalf("accepted ambiguous JSON: %s (%v)", optional, err)
		}
	}
}

func TestVaultHAFactsExcludeUnselectedEndpointData(t *testing.T) {
	facts, err := Parse(Vault, 429, []byte(`{"initialized":true,"sealed":false,"standby":true,"ha_connection_healthy":false,"removed_from_cluster":false,"last_request_forwarding_heartbeat_ms":100000,"cluster_name":"private-sentinel","token":"private-sentinel"}`))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-sentinel") || strings.Contains(string(encoded), "heartbeat") {
		t.Fatalf("unselected data escaped: %s", encoded)
	}
}
