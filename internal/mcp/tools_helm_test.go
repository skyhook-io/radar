package mcp

import (
	"testing"

	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/pkg/helmhistory"
)

func TestMergeHelmOperationsIncludesLiveOperation(t *testing.T) {
	history := []helm.HelmOperation{
		{
			Kind:             helmhistory.KindUpgradeRolledBack,
			Status:           helmhistory.StatusRolledBack,
			FailedRevision:   2,
			RollbackRevision: 3,
			TargetRevision:   1,
		},
	}
	live := &helm.HelmOperation{
		Kind:     helmhistory.KindUpgradeFailed,
		Status:   helmhistory.StatusFailed,
		Revision: 4,
	}

	got := mergeHelmOperations(history, live)

	if len(got) != 2 {
		t.Fatalf("len(operations) = %d, want 2: %#v", len(got), got)
	}
	if got[0].Kind != helmhistory.KindUpgradeFailed || got[0].Revision != 4 {
		t.Fatalf("operations[0] = %#v, want live failed upgrade rev 4", got[0])
	}
	if got[1].Kind != helmhistory.KindUpgradeRolledBack {
		t.Fatalf("operations[1].Kind = %q, want %q", got[1].Kind, helmhistory.KindUpgradeRolledBack)
	}
}

func TestMergeHelmOperationsDeduplicatesLastOperation(t *testing.T) {
	live := helm.HelmOperation{
		Kind:     helmhistory.KindUpgradeFailed,
		Status:   helmhistory.StatusFailed,
		Revision: 4,
	}

	got := mergeHelmOperations([]helm.HelmOperation{live}, &live)

	if len(got) != 1 {
		t.Fatalf("len(operations) = %d, want 1: %#v", len(got), got)
	}
}

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
