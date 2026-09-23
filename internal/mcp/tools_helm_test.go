package mcp

import (
	"testing"

	"github.com/skyhook-io/radar/internal/helm"
)

func TestRedactedHelmValuesRedactsSecretsAndKeepsReferences(t *testing.T) {
	input := map[string]any{
		"image": map[string]any{
			"repository": "ghcr.io/acme/cart",
			"tag":        "1.2.3",
		},
		"password":   "supersecret",
		"secretName": "cart-db-secret",
		"nested": []any{
			map[string]any{"token": "short-token"},
			"Bearer abcdefghijklmnopqrstuvwxyz123456",
		},
	}

	got := redactedHelmValues(input)

	if got["password"] != "[REDACTED]" {
		t.Fatalf("password = %#v, want redacted", got["password"])
	}
	if got["secretName"] != "cart-db-secret" {
		t.Fatalf("secretName = %#v, want reference preserved", got["secretName"])
	}
	nested := got["nested"].([]any)
	nestedMap := nested[0].(map[string]any)
	if nestedMap["token"] != "[REDACTED]" {
		t.Fatalf("nested token = %#v, want redacted", nestedMap["token"])
	}
	if nested[1] != "Bearer [REDACTED]" {
		t.Fatalf("bearer token = %#v, want redacted", nested[1])
	}
	if input["password"] != "supersecret" {
		t.Fatalf("input mutated: %#v", input["password"])
	}
}

func TestNewestHelmRevisionsKeepsTheNewestAndTheOrderOfShortHistories(t *testing.T) {
	short := []helm.HelmRevision{{Revision: 1}, {Revision: 2}}
	if got := newestHelmRevisions(short, 10); len(got) != 2 || got[0].Revision != 1 {
		t.Fatalf("short history changed: %+v", got)
	}
	long := make([]helm.HelmRevision, 0, 12)
	for i := 1; i <= 12; i++ {
		long = append(long, helm.HelmRevision{Revision: i})
	}
	got := newestHelmRevisions(long, 10)
	if len(got) != 10 || got[0].Revision != 12 || got[9].Revision != 3 {
		t.Fatalf("long history = %d entries, first %d, last %d; want 10, 12, 3", len(got), got[0].Revision, got[len(got)-1].Revision)
	}
}
