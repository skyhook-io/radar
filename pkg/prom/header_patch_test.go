package prom

import "testing"

func TestHeaderOperationsKeepSecretsForTenantFork(t *testing.T) {
	original := Connection{URL: "https://mimir", Headers: map[string]string{"Authorization": "private-token", "X-Scope-OrgID": "a"}}
	got, err := ApplyHeaderOperations(original, original.URL, []HeaderOperation{{Key: "x-scope-orgid", Action: "set", Value: "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Headers["Authorization"] != "private-token" || got.Headers["X-Scope-Orgid"] != "b" || original.Headers["X-Scope-OrgID"] != "a" {
		t.Fatal("fork failed to preserve independent secret values")
	}
	if _, err := ApplyHeaderOperations(original, "https://other", []HeaderOperation{{Key: "X-Scope-OrgID", Action: "set", Value: "b"}}); err == nil {
		t.Fatal("implicit keep crossed origins")
	}
	_, err = ApplyHeaderOperations(original, "https://other", []HeaderOperation{{Key: "Authorization", Action: "clear"}, {Key: "X-Scope-OrgID", Action: "set", Value: "b"}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestHeaderOperationsRejectAmbiguousAndEnvironmentChanges(t *testing.T) {
	original := Connection{URL: "https://mimir", Headers: map[string]string{"Authorization": "secret"}, HeadersFromEnv: map[string]string{"X-Scope-OrgID": "TENANT"}}
	for _, ops := range [][]HeaderOperation{
		{{Key: "Authorization", Action: "set", Value: "x"}, {Key: "authorization", Action: "clear"}},
		{{Key: "Authorization", Action: "keep", Value: "placeholder"}},
		{{Key: "Missing", Action: "keep"}},
		{{Key: "X-Scope-OrgID", Action: "clear"}},
		{{Key: "X-Scope-OrgID", Action: "set", Value: "literal"}},
		{{Key: "Authorization", Action: "set", Value: "bad\r\nheader"}},
	} {
		if _, err := ApplyHeaderOperations(original, original.URL, ops); err == nil {
			t.Errorf("accepted invalid operations: %+v", ops)
		}
	}
	got, err := ApplyHeaderOperations(original, original.URL, []HeaderOperation{{Key: "Authorization", Action: "set", Value: "new"}})
	if err != nil || got.HeadersFromEnv["X-Scope-OrgID"] != "TENANT" {
		t.Fatal("environment reference not preserved", err)
	}
	if _, err := ApplyHeaderOperations(original, "http://mimir", nil); err == nil {
		t.Fatal("scheme change preserved credentials")
	}
}
