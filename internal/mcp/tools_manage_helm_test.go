package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
)

func TestManageHelmReleaseValidationBeforeClusterAccess(t *testing.T) {
	no := false
	for _, input := range []manageHelmReleaseInput{
		{Action: "uninstall", Name: "demo"},
		{Action: "delete", Namespace: "demo", Name: "demo"},
		{Action: "rollback", Namespace: "demo", Name: "demo"},
		{Action: "rollback", Namespace: "demo", Name: "demo", Revision: -1},
		{Action: "rollback", Namespace: "demo", Name: "demo", Revision: 1, KeepHistory: true},
		{Action: "uninstall", Namespace: "demo", Name: "demo", Revision: 1},
		{Action: "uninstall", Namespace: "demo", Name: "demo", DryRun: &no},
	} {
		if _, _, err := handleManageHelmRelease(context.Background(), nil, input); err == nil || strings.Contains(err.Error(), "not initialized") {
			t.Fatalf("invalid input reached cluster: %+v %v", input, err)
		}
	}
}

func TestManageHelmConfirmationIsolation(t *testing.T) {
	ctx := auth.ContextWithUser(context.Background(), &auth.User{Username: "alice"})
	target, err := helmConfirmationTarget("storage", "demo", "snapshot-with-options")
	if err != nil {
		t.Fatal(err)
	}
	token, err := issueMutationConfirmation(ctx, "manage_helm_release", target)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyMutationConfirmation(ctx, "manage_helm_release", target, token); err != nil {
		t.Fatal(err)
	}
	for _, other := range []string{"another-snapshot", "different-options"} {
		changed, _ := helmConfirmationTarget("storage", "demo", other)
		if err := verifyMutationConfirmation(ctx, "manage_helm_release", changed, token); err == nil {
			t.Fatal("changed snapshot accepted")
		}
	}
	if err := verifyMutationConfirmation(ctx, "delete_resource", target, token); err == nil {
		t.Fatal("Helm confirmation authorized delete_resource")
	}
}
