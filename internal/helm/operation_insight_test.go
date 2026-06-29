package helm

import (
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/helmhistory"
)

func TestBuildOperationInsightActiveUpgradePicksWorkloadAndCompare(t *testing.T) {
	detail := &HelmReleaseDetail{
		LastOperation: &HelmOperation{
			Kind:     helmhistory.KindUpgradeFailed,
			Status:   helmhistory.StatusFailed,
			Revision: 2,
		},
		History: []HelmRevision{
			{Revision: 2, Status: "failed"},
			{Revision: 1, Status: "deployed"},
		},
		Resources: []OwnedResource{
			{Kind: "Service", Name: "api", Namespace: "default", Status: "Active"},
			{Kind: "Deployment", Name: "api", Namespace: "default", Status: "Progressing", Ready: "0/1", Issue: "ImagePullBackOff"},
		},
	}

	got := buildOperationInsight(detail)
	if got == nil {
		t.Fatal("buildOperationInsight returned nil")
	}
	if got.State != HelmOperationInsightActive {
		t.Fatalf("State = %q, want %q", got.State, HelmOperationInsightActive)
	}
	if got.PrimaryResource == nil || got.PrimaryResource.Kind != "Deployment" || got.PrimaryResource.Name != "api" {
		t.Fatalf("PrimaryResource = %#v, want default Deployment/api", got.PrimaryResource)
	}
	if got.SuggestedCompare == nil {
		t.Fatal("SuggestedCompare = nil")
	}
	if got.SuggestedCompare.Revision1 != 1 || got.SuggestedCompare.Revision2 != 2 {
		t.Fatalf("SuggestedCompare = %d -> %d, want 1 -> 2", got.SuggestedCompare.Revision1, got.SuggestedCompare.Revision2)
	}
}

func TestBuildOperationInsightSuppressesFluxOwnedReleases(t *testing.T) {
	detail := &HelmReleaseDetail{
		ManagedByFluxHelmRelease: "flux-system/api",
		LastOperation: &HelmOperation{
			Kind:     helmhistory.KindUpgradeFailed,
			Status:   helmhistory.StatusFailed,
			Revision: 2,
		},
		History: []HelmRevision{
			{Revision: 2, Status: "failed"},
			{Revision: 1, Status: "deployed"},
		},
		Resources: []OwnedResource{
			{Kind: "Deployment", Name: "api", Namespace: "default", Status: "Progressing", Ready: "0/1"},
		},
	}

	if got := buildOperationInsight(detail); got != nil {
		t.Fatalf("buildOperationInsight = %#v, want nil for Flux-owned release", got)
	}
}

func TestBuildOperationInsightWorkloadBeatsServiceSymptom(t *testing.T) {
	detail := &HelmReleaseDetail{
		LastOperation: &HelmOperation{
			Kind:     helmhistory.KindUpgradeFailed,
			Status:   helmhistory.StatusFailed,
			Revision: 3,
		},
		Resources: []OwnedResource{
			{Kind: "Service", Name: "api", Namespace: "default", Status: "Failed", Issue: "No endpoints"},
			{Kind: "Deployment", Name: "api", Namespace: "default", Status: "Progressing", Ready: "0/2"},
		},
	}

	got := buildOperationInsight(detail)
	if got == nil || got.PrimaryResource == nil {
		t.Fatalf("PrimaryResource = %#v, want Deployment/api", got)
	}
	if got.PrimaryResource.Kind != "Deployment" || got.PrimaryResource.Name != "api" {
		t.Fatalf("PrimaryResource = %#v, want Deployment/api", got.PrimaryResource)
	}
	if len(got.RelatedResources) != 1 || got.RelatedResources[0].Kind != "Service" {
		t.Fatalf("RelatedResources = %#v, want Service symptom", got.RelatedResources)
	}
}

func TestBuildOperationInsightFailedJobBeatsProgressingDeployment(t *testing.T) {
	detail := &HelmReleaseDetail{
		LastOperation: &HelmOperation{
			Kind:     helmhistory.KindUpgradeFailed,
			Status:   helmhistory.StatusFailed,
			Revision: 3,
		},
		Resources: []OwnedResource{
			{Kind: "Deployment", Name: "api", Namespace: "default", Status: "Progressing", Ready: "1/2"},
			{Kind: "Job", Name: "migrate", Namespace: "default", Status: "Failed"},
		},
	}

	got := buildOperationInsight(detail)
	if got == nil || got.PrimaryResource == nil {
		t.Fatalf("PrimaryResource = %#v, want Job/migrate", got)
	}
	if got.PrimaryResource.Kind != "Job" || got.PrimaryResource.Name != "migrate" {
		t.Fatalf("PrimaryResource = %#v, want Job/migrate", got.PrimaryResource)
	}
}

func TestBuildOperationInsightProgressingWorkloadBeatsTerminatingPod(t *testing.T) {
	detail := &HelmReleaseDetail{
		LastOperation: &HelmOperation{
			Kind:     helmhistory.KindUpgradeFailed,
			Status:   helmhistory.StatusFailed,
			Revision: 3,
		},
		Resources: []OwnedResource{
			{Kind: "Pod", Name: "old", Namespace: "default", Status: "Terminating"},
			{Kind: "Deployment", Name: "api", Namespace: "default", Status: "Progressing", Ready: "1/2"},
		},
	}

	got := buildOperationInsight(detail)
	if got == nil || got.PrimaryResource == nil {
		t.Fatalf("PrimaryResource = %#v, want Deployment/api", got)
	}
	if got.PrimaryResource.Kind != "Deployment" || got.PrimaryResource.Name != "api" {
		t.Fatalf("PrimaryResource = %#v, want Deployment/api", got.PrimaryResource)
	}
}

func TestBuildOperationInsightIssueBearingPVCBeatsProgressingDeployment(t *testing.T) {
	detail := &HelmReleaseDetail{
		LastOperation: &HelmOperation{
			Kind:     helmhistory.KindUpgradeFailed,
			Status:   helmhistory.StatusFailed,
			Revision: 3,
		},
		Resources: []OwnedResource{
			{Kind: "Deployment", Name: "api", Namespace: "default", Status: "Progressing", Ready: "0/1"},
			{Kind: "PersistentVolumeClaim", Name: "data", Namespace: "default", Status: "Pending", Issue: "waiting for volume binding"},
		},
	}

	got := buildOperationInsight(detail)
	if got == nil || got.PrimaryResource == nil {
		t.Fatalf("PrimaryResource = %#v, want PersistentVolumeClaim/data", got)
	}
	if got.PrimaryResource.Kind != "PersistentVolumeClaim" || got.PrimaryResource.Name != "data" {
		t.Fatalf("PrimaryResource = %#v, want PersistentVolumeClaim/data", got.PrimaryResource)
	}
}

func TestBuildOperationInsightUsesTrueSignalCountWhenRelatedResourcesAreCapped(t *testing.T) {
	resources := []OwnedResource{
		{Kind: "Deployment", Name: "api", Namespace: "default", Status: "Progressing", Ready: "0/1"},
	}
	for i := 0; i < maxRelatedOperationResources+2; i++ {
		resources = append(resources, OwnedResource{
			Kind:      "Pod",
			Name:      "api-" + string(rune('a'+i)),
			Namespace: "default",
			Status:    "Pending",
		})
	}
	detail := &HelmReleaseDetail{
		LastOperation: &HelmOperation{
			Kind:     helmhistory.KindReleaseFailed,
			Status:   helmhistory.StatusFailed,
			Revision: 1,
		},
		Resources: resources,
	}

	got := buildOperationInsight(detail)
	if got == nil {
		t.Fatal("buildOperationInsight returned nil")
	}
	if got.SignalCount != len(resources) {
		t.Fatalf("SignalCount = %d, want %d", got.SignalCount, len(resources))
	}
	if len(got.RelatedResources) != maxRelatedOperationResources {
		t.Fatalf("len(RelatedResources) = %d, want cap %d", len(got.RelatedResources), maxRelatedOperationResources)
	}
}

func TestBuildOperationInsightRecoveredRollbackSuggestsCompareWithoutPrimaryResource(t *testing.T) {
	detail := &HelmReleaseDetail{
		Status: "deployed",
		LastOperation: &HelmOperation{
			Kind:             helmhistory.KindUpgradeRolledBack,
			Status:           helmhistory.StatusRolledBack,
			FailedRevision:   2,
			RollbackRevision: 3,
			TargetRevision:   1,
			Updated:          time.Now(),
		},
		History: []HelmRevision{
			{Revision: 3, Status: "deployed", Description: "Rollback to 1"},
			{Revision: 2, Status: "failed"},
			{Revision: 1, Status: "superseded"},
		},
		Resources: []OwnedResource{
			{Kind: "Deployment", Name: "api", Namespace: "default", Status: "Running", Ready: "1/1"},
		},
	}

	got := buildOperationInsight(detail)
	if got == nil {
		t.Fatal("buildOperationInsight returned nil")
	}
	if got.State != HelmOperationInsightRecovered {
		t.Fatalf("State = %q, want %q", got.State, HelmOperationInsightRecovered)
	}
	if got.PrimaryResource != nil {
		t.Fatalf("PrimaryResource = %#v, want nil for recovered rollback", got.PrimaryResource)
	}
	if got.SuggestedCompare == nil || got.SuggestedCompare.Revision1 != 1 || got.SuggestedCompare.Revision2 != 2 {
		t.Fatalf("SuggestedCompare = %#v, want 1 -> 2", got.SuggestedCompare)
	}
}

func TestBuildOperationInsightManualRollbackSuggestsPreviousToRollbackRevision(t *testing.T) {
	detail := &HelmReleaseDetail{
		LastOperation: &HelmOperation{
			Kind:           helmhistory.KindRollback,
			Status:         helmhistory.StatusCompleted,
			Revision:       3,
			TargetRevision: 1,
		},
		History: []HelmRevision{
			{Revision: 3, Status: "deployed", Description: "Rollback to 1"},
			{Revision: 2, Status: "superseded"},
			{Revision: 1, Status: "superseded"},
		},
	}

	got := buildOperationInsight(detail)
	if got == nil {
		t.Fatal("buildOperationInsight returned nil")
	}
	if got.State != HelmOperationInsightRecovered {
		t.Fatalf("State = %q, want %q", got.State, HelmOperationInsightRecovered)
	}
	if got.SuggestedCompare == nil || got.SuggestedCompare.Revision1 != 2 || got.SuggestedCompare.Revision2 != 3 {
		t.Fatalf("SuggestedCompare = %#v, want 2 -> 3", got.SuggestedCompare)
	}
}

func TestBuildOperationInsightOmitsRecoveredInsightWithoutActionableData(t *testing.T) {
	detail := &HelmReleaseDetail{
		LastOperation: &HelmOperation{
			Kind:           helmhistory.KindRollback,
			Status:         helmhistory.StatusCompleted,
			Revision:       1,
			TargetRevision: 1,
		},
		History: []HelmRevision{{Revision: 1, Status: "deployed", Description: "Rollback to 1"}},
	}

	got := buildOperationInsight(detail)
	if got != nil {
		t.Fatalf("buildOperationInsight = %#v, want nil", got)
	}
}

func TestBuildOperationInsightPendingUpgradeSuggestsPreviousCompletedRevision(t *testing.T) {
	detail := &HelmReleaseDetail{
		LastOperation: &HelmOperation{
			Kind:          helmhistory.KindPending,
			Status:        helmhistory.StatusStuck,
			Revision:      4,
			PendingStatus: "pending-upgrade",
		},
		History: []HelmRevision{
			{Revision: 4, Status: "pending-upgrade"},
			{Revision: 3, Status: "failed"},
			{Revision: 2, Status: "superseded"},
			{Revision: 1, Status: "deployed"},
		},
		Resources: []OwnedResource{
			{Kind: "StatefulSet", Name: "db", Namespace: "default", Status: "Progressing", Ready: "1/3"},
		},
	}

	got := buildOperationInsight(detail)
	if got == nil {
		t.Fatal("buildOperationInsight returned nil")
	}
	if got.State != HelmOperationInsightActive {
		t.Fatalf("State = %q, want %q", got.State, HelmOperationInsightActive)
	}
	if got.SuggestedCompare == nil || got.SuggestedCompare.Revision1 != 2 || got.SuggestedCompare.Revision2 != 4 {
		t.Fatalf("SuggestedCompare = %#v, want 2 -> 4", got.SuggestedCompare)
	}
}

func TestBuildOperationInsightPendingInstallHasNoSuggestedCompare(t *testing.T) {
	detail := &HelmReleaseDetail{
		LastOperation: &HelmOperation{
			Kind:          helmhistory.KindPending,
			Status:        helmhistory.StatusStuck,
			Revision:      1,
			PendingStatus: "pending-install",
		},
		History: []HelmRevision{{Revision: 1, Status: "pending-install"}},
	}

	got := buildOperationInsight(detail)
	if got != nil {
		t.Fatalf("buildOperationInsight = %#v, want nil without actionable insight", got)
	}
}

func TestBuildOperationInsightIgnoresUnknownHealthyResources(t *testing.T) {
	detail := &HelmReleaseDetail{
		LastOperation: &HelmOperation{
			Kind:     helmhistory.KindReleaseFailed,
			Status:   helmhistory.StatusFailed,
			Revision: 1,
		},
		Resources: []OwnedResource{
			{Kind: "Cluster", APIVersion: "postgresql.cnpg.io/v1", Name: "db", Namespace: "default"},
			{Kind: "Service", Name: "db", Namespace: "default", Status: "Active"},
		},
	}

	got := buildOperationInsight(detail)
	if got != nil {
		t.Fatalf("buildOperationInsight = %#v, want nil without actionable insight", got)
	}
}
