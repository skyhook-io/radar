package mcp

import (
	"testing"

	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/pkg/helmhistory"
)

func TestMCPHelmDashboardPrioritySurfacesOperationSignals(t *testing.T) {
	currentFailure := helm.HelmRelease{
		Status: "failed",
	}
	rolledBack := helm.HelmRelease{
		Status:         "deployed",
		ResourceHealth: "healthy",
		LastOperation:  &helm.HelmOperation{Kind: helmhistory.KindUpgradeRolledBack},
	}
	unhealthy := helm.HelmRelease{
		Status:         "deployed",
		ResourceHealth: "unhealthy",
	}
	manualRollback := helm.HelmRelease{
		Status:         "deployed",
		ResourceHealth: "healthy",
		LastOperation:  &helm.HelmOperation{Kind: helmhistory.KindRollback},
	}
	healthy := helm.HelmRelease{
		Status:         "deployed",
		ResourceHealth: "healthy",
	}

	if mcpHelmDashboardPriority(currentFailure) >= mcpHelmDashboardPriority(rolledBack) {
		t.Fatalf("current failure should outrank rolled-back failed upgrade")
	}
	if mcpHelmDashboardPriority(rolledBack) >= mcpHelmDashboardPriority(unhealthy) {
		t.Fatalf("rolled-back failed upgrade should outrank unhealthy ordinary release for Helm debugging")
	}
	if mcpHelmDashboardPriority(unhealthy) >= mcpHelmDashboardPriority(manualRollback) {
		t.Fatalf("unhealthy release should outrank manual rollback")
	}
	if mcpHelmDashboardPriority(manualRollback) >= mcpHelmDashboardPriority(healthy) {
		t.Fatalf("manual rollback should outrank ordinary healthy release")
	}
}
