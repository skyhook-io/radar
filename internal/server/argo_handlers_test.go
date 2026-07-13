package server

import (
	"testing"

	"github.com/skyhook-io/radar/pkg/gitops"
)

func TestEnforceArgoSelectiveSyncSafety(t *testing.T) {
	trueValue := true
	resource := gitops.ArgoSyncResource{Group: "apps", Kind: "Deployment", Namespace: "default", Name: "api"}
	opts := enforceArgoSelectiveSyncSafety(gitops.ArgoSyncOptions{
		Resources: []gitops.ArgoSyncResource{resource},
		Revision:  "unsafe-revision",
		Prune:     &trueValue,
		DryRun:    &trueValue,
		Force:     &trueValue,
		ApplyOnly: &trueValue,
	})
	if opts.Revision != "" || opts.Prune == nil || *opts.Prune || opts.ApplyOnly == nil || *opts.ApplyOnly {
		t.Fatalf("selective sync safety not enforced: %#v", opts)
	}
	if opts.DryRun == nil || !*opts.DryRun || opts.Force == nil || !*opts.Force || len(opts.Resources) != 1 {
		t.Fatalf("allowed selective sync options changed: %#v", opts)
	}
}

func TestEnforceArgoSelectiveSyncSafetyLeavesFullSyncUnchanged(t *testing.T) {
	trueValue := true
	original := gitops.ArgoSyncOptions{Revision: "release", Prune: &trueValue, ApplyOnly: &trueValue}
	opts := enforceArgoSelectiveSyncSafety(original)
	if opts.Revision != "release" || opts.Prune != original.Prune || opts.ApplyOnly != original.ApplyOnly {
		t.Fatalf("full sync options changed: %#v", opts)
	}
}
