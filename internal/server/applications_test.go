package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/pkg/packages"
	"github.com/skyhook-io/radar/pkg/subject"
	"github.com/skyhook-io/radar/pkg/topology"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// rawInput builds a workload with no label overlay and its own structural root
// (a singleton, raw-always).
func rawInput(kind, ns, name, version, health string) appWorkloadInput {
	return appWorkloadInput{
		wl:       appWorkload{Kind: kind, Namespace: ns, Name: name, Version: version, Health: health, WorkloadClass: classifyWorkload(kind, nil)},
		rootKey:  ns + "/" + kind + "/" + name,
		rootKind: kind,
	}
}

func TestBatchHealthPrefersActiveRunsOverRetainedFailure(t *testing.T) {
	batch := &appBatchSummary{ActiveRuns: 1, LatestRunPhase: "Failed"}
	if got := batchHealth(batch, packages.HealthUnknown); got != packages.HealthNeutral {
		t.Fatalf("batchHealth = %q, want neutral while a run is active", got)
	}
}

func TestBatchHealthPreservesLauncherProblems(t *testing.T) {
	tests := []struct {
		name     string
		batch    *appBatchSummary
		fallback packages.Health
		want     packages.Health
	}{
		{
			name:     "stale schedule stays degraded after an older success",
			batch:    &appBatchSummary{LatestRunPhase: "Succeeded"},
			fallback: packages.HealthDegraded,
			want:     packages.HealthDegraded,
		},
		{
			name:     "unhealthy launcher stays unhealthy while a retained run is active",
			batch:    &appBatchSummary{ActiveRuns: 1},
			fallback: packages.HealthUnhealthy,
			want:     packages.HealthUnhealthy,
		},
		{
			name:     "failed run is worse than a degraded launcher",
			batch:    &appBatchSummary{LatestRunPhase: "Failed"},
			fallback: packages.HealthDegraded,
			want:     packages.HealthUnhealthy,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := batchHealth(tt.batch, tt.fallback); got != tt.want {
				t.Fatalf("batchHealth = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBatchHealthUsesStandaloneJobResult(t *testing.T) {
	batch := &appBatchSummary{LatestRunPhase: "Succeeded"}
	if got := batchHealth(batch, packages.HealthNeutral); got != packages.HealthHealthy {
		t.Fatalf("batchHealth = %q, want healthy for a succeeded standalone Job", got)
	}
}

func TestWorkflowPrimaryImageUsesStoredTemplateSpec(t *testing.T) {
	wf := &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{
			"storedWorkflowTemplateSpec": map[string]any{
				"templates": []any{map[string]any{"container": map[string]any{"image": "example.com/migrate:v2"}}},
			},
		},
	}}

	if got := workflowPrimaryImage(wf); got != "example.com/migrate:v2" {
		t.Fatalf("workflowPrimaryImage() = %q", got)
	}
}

// overlayInput builds a workload carrying a Tier-2 label overlay (Argo/Flux/Helm
// /part-of), keyed by its own structural root.
func overlayInput(kind, ns, name, version, health string, tier subject.Tier, key string, conf subject.Confidence) appWorkloadInput {
	in := rawInput(kind, ns, name, version, health)
	in.overlay = &subject.AppOverlay{Winner: subject.Signal{Tier: tier, Key: key, Confidence: conf}}
	return in
}

func rowByName(rows []appRow, name string) *appRow {
	for i := range rows {
		if rows[i].Name == name {
			return &rows[i]
		}
	}
	return nil
}

func workflowRun(ns, name, template, phase, startedAt string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"name":      name,
			"namespace": ns,
		},
		"spec": map[string]any{
			"workflowTemplateRef": map[string]any{"name": template},
		},
		"status": map[string]any{
			"phase":     phase,
			"startedAt": startedAt,
		},
	}}
}

func clusterWorkflowRun(ns, name, template, phase, startedAt string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"name":      name,
			"namespace": ns,
		},
		"spec": map[string]any{
			"workflowTemplateRef": map[string]any{"name": template, "clusterScope": true},
		},
		"status": map[string]any{
			"phase":     phase,
			"startedAt": startedAt,
		},
	}}
}

func TestEventsForWorkload_MatchesKindAndName(t *testing.T) {
	byObject := map[string][]*corev1.Event{
		"Service/api": {
			{InvolvedObject: corev1.ObjectReference{Kind: "Service", Name: "api"}, Reason: "NoEndpoints", Type: "Warning", Message: "service has no endpoints"},
		},
		"Deployment/api": {
			{InvolvedObject: corev1.ObjectReference{Kind: "Deployment", Name: "api"}, Reason: "ProgressDeadlineExceeded", Type: "Warning", Message: "deployment stalled"},
		},
	}
	got := eventsForWorkload(byObject, "Deployment", "api", nil)
	if len(got) != 1 {
		t.Fatalf("eventsForWorkload returned %d events: %+v", len(got), got)
	}
	if got[0].Object != "Deployment/api" || got[0].Reason != "ProgressDeadlineExceeded" {
		t.Fatalf("eventsForWorkload picked wrong event: %+v", got[0])
	}
}

func TestApplyRunToBatchUsesNewestRunAndLatestSchedule(t *testing.T) {
	batch := &appBatchSummary{}

	applyRunToBatch(batch, WorkloadRun{
		Name:        "success-new",
		Phase:       "Succeeded",
		StartedAt:   "2026-01-03T00:00:00Z",
		FinishedAt:  "2026-01-03T00:01:00Z",
		ScheduledAt: "2026-01-03T00:00:00Z",
	})
	applyRunToBatch(batch, WorkloadRun{
		Name:        "failed-old",
		Phase:       "Failed",
		StartedAt:   "2026-01-02T00:00:00Z",
		FinishedAt:  "2026-01-02T00:01:00Z",
		ScheduledAt: "2026-01-02T00:00:00Z",
	})

	if batch.LatestRunName != "success-new" || batch.LatestRunPhase != "Succeeded" {
		t.Fatalf("latest run = %s/%s, want success-new/Succeeded", batch.LatestRunName, batch.LatestRunPhase)
	}
	if batch.LastScheduledAt != "2026-01-03T00:00:00Z" {
		t.Fatalf("last scheduled = %q, want newest schedule", batch.LastScheduledAt)
	}
	if batch.LastSuccessfulAt != "2026-01-03T00:01:00Z" {
		t.Fatalf("last successful = %q, want successful run finish time", batch.LastSuccessfulAt)
	}
}

func TestApplyRunToBatchLastSuccessfulAtFallbacks(t *testing.T) {
	batch := &appBatchSummary{}
	applyRunToBatch(batch, WorkloadRun{
		Name:        "scheduled-only",
		Phase:       "Succeeded",
		ScheduledAt: "2026-01-01T00:00:00Z",
	})
	applyRunToBatch(batch, WorkloadRun{
		Name:       "started-only",
		Phase:      "Succeeded",
		StartedAt:  "2026-01-02T00:00:00Z",
		FinishedAt: "",
	})
	applyRunToBatch(batch, WorkloadRun{
		Name:       "failed-newer",
		Phase:      "Failed",
		FinishedAt: "2026-01-03T00:00:00Z",
	})

	if batch.LastSuccessfulAt != "2026-01-02T00:00:00Z" {
		t.Fatalf("last successful = %q, want newest successful fallback time", batch.LastSuccessfulAt)
	}
}

func TestWorkflowTemplateBatchSummariesGroupGeneratedWorkflows(t *testing.T) {
	workflows := []*unstructured.Unstructured{
		workflowRun("dev", "migration-a", "migration-template", "Failed", "2026-01-02T00:00:00Z"),
		workflowRun("dev", "migration-b", "migration-template", "Succeeded", "2026-01-03T00:00:00Z"),
	}

	summaries := workflowTemplateBatchSummaries(workflows, nil)
	batch := summaries["WorkflowTemplate/dev/migration-template"]
	if batch == nil {
		t.Fatalf("missing WorkflowTemplate batch summary: %#v", summaries)
	}
	if batch.RetainedRuns != 2 || batch.FailedRuns != 1 || batch.SucceededRuns != 1 {
		t.Fatalf("unexpected counts: %#v", batch)
	}
	if batch.LatestRunName != "migration-b" || batch.LatestRunPhase != "Succeeded" {
		t.Fatalf("latest run = %s/%s, want newest run", batch.LatestRunName, batch.LatestRunPhase)
	}
}

func TestWorkflowTemplateBatchSummariesGroupClusterWorkflowTemplates(t *testing.T) {
	workflows := []*unstructured.Unstructured{
		clusterWorkflowRun("dev", "global-a", "cluster-migration", "Succeeded", "2026-01-03T00:00:00Z"),
		clusterWorkflowRun("prod", "global-b", "cluster-migration", "Running", "2026-01-04T00:00:00Z"),
	}

	summaries := workflowTemplateBatchSummaries(workflows, nil)
	batch := summaries["ClusterWorkflowTemplate//cluster-migration"]
	if batch == nil {
		t.Fatalf("missing ClusterWorkflowTemplate batch summary: %#v", summaries)
	}
	if batch.RetainedRuns != 2 || batch.ActiveRuns != 1 || batch.SucceededRuns != 1 {
		t.Fatalf("unexpected counts: %#v", batch)
	}
	if batch.LatestRunName != "global-b" || batch.LatestRunPhase != "Running" {
		t.Fatalf("latest run = %s/%s, want global-b/Running", batch.LatestRunName, batch.LatestRunPhase)
	}
}

func TestClusterWorkflowTemplateVisibility(t *testing.T) {
	workflows := []*unstructured.Unstructured{
		clusterWorkflowRun("dev", "global-a", "referenced", "Succeeded", "2026-01-03T00:00:00Z"),
	}

	if shouldIncludeClusterWorkflowTemplate([]string{"dev"}, workflows, "referenced", false) {
		t.Fatal("denied ClusterWorkflowTemplate must not be included even when referenced")
	}
	if !shouldIncludeClusterWorkflowTemplate([]string{"dev"}, workflows, "referenced", true) {
		t.Fatal("referenced ClusterWorkflowTemplate should be included in a namespace-filtered view")
	}
	if shouldIncludeClusterWorkflowTemplate([]string{"dev"}, workflows, "unused", true) {
		t.Fatal("unused ClusterWorkflowTemplate should not be included in a namespace-filtered view")
	}
	if !shouldIncludeClusterWorkflowTemplate(nil, workflows, "unused", true) {
		t.Fatal("all-namespace view should include unused ClusterWorkflowTemplates")
	}
}

func TestApplicationsCacheKeySeparatesClusterWorkflowTemplatePermission(t *testing.T) {
	visible := applicationsCacheKeyFor([]string{"prod", "dev"}, true)
	denied := applicationsCacheKeyFor([]string{"dev", "prod"}, false)
	if visible == denied {
		t.Fatalf("cache keys collide across ClusterWorkflowTemplate permission modes: %q", visible)
	}
	if visible != applicationsCacheKeyFor([]string{"dev", "prod"}, true) {
		t.Fatal("cache key should remain namespace-order independent")
	}
}

func TestWorkflowTemplateBatchSummariesKeepsStaleCronLabel(t *testing.T) {
	wf := workflowRun("dev", "migration-a", "migration-template", "Succeeded", "2026-01-03T00:00:00Z")
	wf.SetLabels(map[string]string{"workflows.argoproj.io/cron-workflow": "deleted-parent"})

	summaries := workflowTemplateBatchSummaries([]*unstructured.Unstructured{wf}, map[string]bool{})
	if batch := summaries["WorkflowTemplate/dev/migration-template"]; batch == nil || batch.RetainedRuns != 1 {
		t.Fatalf("stale CronWorkflow label dropped template run: %#v", summaries)
	}
}

func TestWorkflowTemplateBatchSummariesExcludesExistingCronOwner(t *testing.T) {
	wf := workflowRun("dev", "migration-a", "migration-template", "Succeeded", "2026-01-03T00:00:00Z")
	wf.SetLabels(map[string]string{"workflows.argoproj.io/cron-workflow": "nightly"})

	summaries := workflowTemplateBatchSummaries([]*unstructured.Unstructured{wf}, map[string]bool{"dev/nightly": true})
	if len(summaries) != 0 {
		t.Fatalf("existing CronWorkflow run also appeared in template summary: %#v", summaries)
	}
}

func TestArgoWorkflowTemplateRefFallsBackToWorkflowTemplateLabel(t *testing.T) {
	wf := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"name":      "migration-x",
			"namespace": "dev",
			"labels": map[string]any{
				"workflows.argoproj.io/workflow-template": "migration-template",
			},
		},
	}}

	ref, ok := argoWorkflowTemplateRef(wf)
	if !ok {
		t.Fatal("expected template ref from workflow-template label")
	}
	if ref.key() != "WorkflowTemplate/dev/migration-template" {
		t.Fatalf("template ref = %q", ref.key())
	}
}

func TestCronWorkflowOwnerNamePrefersControllerOwnerReference(t *testing.T) {
	controller := true
	wf := &unstructured.Unstructured{}
	wf.SetLabels(map[string]string{"workflows.argoproj.io/cron-workflow": "label-owner"})
	wf.SetOwnerReferences([]metav1.OwnerReference{{
		Kind:       "CronWorkflow",
		Name:       "owner-ref",
		Controller: &controller,
	}})

	if got := cronWorkflowOwnerName(wf); got != "owner-ref" {
		t.Fatalf("cronWorkflowOwnerName = %q, want owner-ref", got)
	}
}

func TestScaledJobHealthFollowsKedaConditions(t *testing.T) {
	tests := []struct {
		name       string
		conditions []any
		want       packages.Health
	}{
		{
			name: "not ready is unhealthy",
			conditions: []any{
				map[string]any{"type": "Ready", "status": "False"},
				map[string]any{"type": "Active", "status": "False"},
			},
			want: packages.HealthUnhealthy,
		},
		{
			name: "active is healthy",
			conditions: []any{
				map[string]any{"type": "Ready", "status": "True"},
				map[string]any{"type": "Active", "status": "True"},
			},
			want: packages.HealthHealthy,
		},
		{
			name: "ready but idle is neutral",
			conditions: []any{
				map[string]any{"type": "Ready", "status": "True"},
				map[string]any{"type": "Active", "status": "False"},
			},
			want: packages.HealthNeutral,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sj := &unstructured.Unstructured{Object: map[string]any{
				"status": map[string]any{"conditions": tt.conditions},
			}}
			if got := scaledJobHealth(sj); got != tt.want {
				t.Fatalf("scaledJobHealth = %q, want %q", got, tt.want)
			}
		})
	}
}

// A label overlay shared by two workloads collapses them into one app; an
// unrelated raw workload stays its own app (raw-always).
func TestGroupApplications_OverlayConsolidationAndRawAlways(t *testing.T) {
	rows := groupApplications([]appWorkloadInput{
		overlayInput("Deployment", "prod", "api", "1.2.0", "healthy", subject.TierPartOf, "prod/app/checkout", subject.ConfidenceMedium),
		overlayInput("Deployment", "prod", "worker", "1.2.0", "healthy", subject.TierPartOf, "prod/app/checkout", subject.ConfidenceMedium),
		rawInput("StatefulSet", "prod", "lonely-db", "15", "healthy"),
	})

	if len(rows) != 2 {
		t.Fatalf("want 2 apps (checkout + lonely-db), got %d: %+v", len(rows), rows)
	}
	checkout := rowByName(rows, "checkout")
	if checkout == nil {
		t.Fatalf("checkout app missing: %+v", rows)
	}
	if len(checkout.Workloads) != 2 {
		t.Errorf("checkout should hold api+worker (2 workloads), got %d", len(checkout.Workloads))
	}
	if checkout.Tier != int(subject.TierPartOf) || checkout.Confidence != string(subject.ConfidenceMedium) {
		t.Errorf("checkout tier/confidence = %d/%s, want %d/%s", checkout.Tier, checkout.Confidence, subject.TierPartOf, subject.ConfidenceMedium)
	}
	lonely := rowByName(rows, "lonely-db")
	if lonely == nil || lonely.Tier != 0 || len(lonely.Workloads) != 1 {
		t.Errorf("lonely-db should be a raw single-workload app at tier 0, got %+v", lonely)
	}
}

// ArgoCD tracking-id mode ("<ns>/Application/<name>") and instance-label mode
// ("/Application/<name>", empty ns) name the same app — they must collapse into
// one row. This is the declaration/workload-collapse fix.
func TestGroupApplications_ArgoTrackingModesCollapse(t *testing.T) {
	rows := groupApplications([]appWorkloadInput{
		overlayInput("Deployment", "prod", "api", "2.0.0", "healthy", subject.TierArgoTrackingID, "argocd/Application/storefront", subject.ConfidenceHigh),
		overlayInput("Deployment", "prod", "cache", "7.2", "healthy", subject.TierArgoInstance, "/Application/storefront", subject.ConfidenceHigh),
	})

	if len(rows) != 1 {
		t.Fatalf("Argo tracking-id + instance modes must collapse to 1 app, got %d: %+v", len(rows), rows)
	}
	if len(rows[0].Workloads) != 2 {
		t.Errorf("collapsed Argo app should hold both workloads, got %d", len(rows[0].Workloads))
	}
	// Tracking-id (tier 3) outranks instance (tier 4) for identity.
	if rows[0].Tier != int(subject.TierArgoTrackingID) {
		t.Errorf("identity tier = %d, want tracking-id %d", rows[0].Tier, subject.TierArgoTrackingID)
	}
}

func TestGroupApplications_SameNameArgoAppsInDifferentNamespacesStaySeparate(t *testing.T) {
	a := rawInput("Deployment", "team-a", "api", "1.0.0", "healthy")
	a.rootKey, a.rootKind = "team-a-argocd/Application/storefront", "Application"
	b := rawInput("Deployment", "team-b", "api", "1.0.0", "healthy")
	b.rootKey, b.rootKind = "team-b-argocd/Application/storefront", "Application"

	rows := groupApplications([]appWorkloadInput{a, b})
	if len(rows) != 2 {
		t.Fatalf("same-name Argo Applications in different namespaces must stay separate, got %d: %+v", len(rows), rows)
	}
}

// An in-cluster GitOps manager (an ArgoCD Application node managing workloads
// via EdgeManages) collapses its workloads even when they carry no label, and
// its kind synthesizes provenance (Argo/Flux tier) for the surface.
func TestGroupApplications_StructuralManagerRoot(t *testing.T) {
	// Two unlabeled Deployments whose structural root is the same Argo App node.
	a := rawInput("Deployment", "prod", "api", "3.1.0", "healthy")
	a.rootKey, a.rootKind = "argocd/Application/billing", "Application"
	b := rawInput("Deployment", "prod", "worker", "3.1.0", "degraded")
	b.rootKey, b.rootKind = "argocd/Application/billing", "Application"

	rows := groupApplications([]appWorkloadInput{a, b})
	if len(rows) != 1 {
		t.Fatalf("workloads under one Argo App must be one app, got %d: %+v", len(rows), rows)
	}
	r := rows[0]
	if r.Name != "billing" || len(r.Workloads) != 2 {
		t.Errorf("billing app malformed: name=%q workloads=%d", r.Name, len(r.Workloads))
	}
	if r.Tier != int(subject.TierArgoTrackingID) || r.Confidence != string(subject.ConfidenceHigh) {
		t.Errorf("structural Argo root should synthesize Argo tier/high, got %d/%s", r.Tier, r.Confidence)
	}
	if r.Health != "degraded" {
		t.Errorf("app health is worst-of workloads, want degraded got %q", r.Health)
	}
}

func TestGroupApplications_SourceRefRequiresExactSource(t *testing.T) {
	helmApp := overlayInput("Deployment", "prod", "api", "1.0", "healthy", subject.TierHelmRelease, "prod/HelmRelease/checkout", subject.ConfidenceMedium)
	helmApp.overlay.Winner.Ref = subject.Ref{Kind: "HelmRelease", Namespace: "prod", Name: "checkout"}
	helmApp.source = sourceRefForInput(helmApp.overlay, helmApp.rootKind, helmApp.rootKey)

	labelApp := overlayInput("Deployment", "prod", "payments-worker", "1.0", "healthy", subject.TierPartOf, "prod/app/payments", subject.ConfidenceMedium)
	labelApp.overlay.Winner.Ref = subject.Ref{Kind: "app", Namespace: "prod", Name: "payments"}
	labelApp.source = sourceRefForInput(labelApp.overlay, labelApp.rootKind, labelApp.rootKey)

	structuralApp := rawInput("Deployment", "prod", "admin", "1.0", "healthy")
	structuralApp.rootKey, structuralApp.rootKind = "argocd/Application/admin", "Application"
	structuralApp.source = sourceRefForInput(structuralApp.overlay, structuralApp.rootKind, structuralApp.rootKey)

	rows := groupApplications([]appWorkloadInput{helmApp, labelApp, structuralApp})

	helmRow := rowByName(rows, "checkout")
	if helmRow == nil || helmRow.SourceRef == nil || helmRow.SourceRef.Type != "helm" || helmRow.SourceRef.Name != "checkout" {
		t.Fatalf("native Helm exact source ref missing: %+v", helmRow)
	}
	labelRow := rowByName(rows, "payments")
	if labelRow == nil || labelRow.SourceRef != nil {
		t.Fatalf("label-inferred app should not expose source ref, got %+v", labelRow)
	}
	structuralRow := rowByName(rows, "admin")
	if structuralRow == nil || structuralRow.SourceRef == nil || structuralRow.SourceRef.Type != "gitops" || structuralRow.SourceRef.Tool != "argocd" {
		t.Fatalf("structural GitOps source ref missing: %+v", structuralRow)
	}
}

func TestGroupApplications_SourceRefRequiresEveryWorkload(t *testing.T) {
	api := overlayInput("Deployment", "prod", "checkout-api", "1.0", "healthy", subject.TierHelmRelease, "prod/HelmRelease/checkout", subject.ConfidenceMedium)
	api.overlay.Winner.Ref = subject.Ref{Kind: "HelmRelease", Namespace: "prod", Name: "checkout"}
	api.source = sourceRefForInput(api.overlay, api.rootKind, api.rootKey)

	worker := overlayInput("Deployment", "prod", "checkout-worker", "1.0", "healthy", subject.TierHelmRelease, "prod/HelmRelease/checkout", subject.ConfidenceMedium)

	rows := groupApplications([]appWorkloadInput{api, worker})
	checkout := rowByName(rows, "checkout")
	if checkout == nil || len(checkout.Workloads) != 2 {
		t.Fatalf("checkout app missing or malformed: %+v", rows)
	}
	if checkout.SourceRef != nil {
		t.Fatalf("partial source evidence should not expose an app-level source ref: %+v", checkout.SourceRef)
	}
}

func TestSetStrictSourceRefMarksConflictingSources(t *testing.T) {
	row := appRow{}
	setStrictSourceRef(&row, []appWorkloadInput{
		{source: &appSourceRef{Type: "helm", Tool: "helm", Kind: "HelmRelease", Namespace: "prod", Name: "checkout"}},
		{source: &appSourceRef{Type: "gitops", Tool: "argocd", Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "checkout"}},
	})
	if row.SourceRef != nil || row.sourceStrict || !row.SourceConflict {
		t.Fatalf("conflicting strict sources should mark conflict only: ref=%+v strict=%v conflict=%v", row.SourceRef, row.sourceStrict, row.SourceConflict)
	}
	payload, err := json.Marshal(row)
	if err != nil || !strings.Contains(string(payload), `"sourceConflict":true`) {
		t.Fatalf("conflict must be exposed to clients: payload=%s err=%v", payload, err)
	}
	mergeSourceRef(&row, &appSourceRef{Type: "gitops", Tool: "argocd", Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "checkout"})
	if row.SourceRef != nil {
		t.Fatalf("managed source should not attach after strict source conflict: %+v", row.SourceRef)
	}
}

func TestManagedSourceRefs_CrossNamespaceArgoApplication(t *testing.T) {
	app := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"namespace": "argocd", "name": "billing"},
		"spec": map[string]any{
			"destination": map[string]any{"namespace": "team-a"},
		},
		"status": map[string]any{"resources": []any{
			map[string]any{"kind": "Deployment", "name": "api"},
			map[string]any{"kind": "Deployment", "namespace": "team-a", "name": "worker"},
		}},
	}}
	sources := map[string][]appSourceRef{}
	addArgoManagedSourceRefs(sources, []*unstructured.Unstructured{app})
	ref := commonManagedSourceRef([]appWorkload{
		{Kind: "Deployment", Namespace: "team-a", Name: "api"},
		{Kind: "Deployment", Namespace: "team-a", Name: "worker"},
	}, sources)
	if ref == nil || ref.Tool != "argocd" || ref.Namespace != "argocd" || ref.Name != "billing" {
		t.Fatalf("cross-namespace Argo source ref = %+v, want argocd/Application/billing", ref)
	}
}

func TestMergeSourceRefPreservesStrictSourceRefOnManagedConflict(t *testing.T) {
	row := appRow{
		SourceRef:    &appSourceRef{Type: "helm", Tool: "helm", Kind: "HelmRelease", Namespace: "prod", Name: "checkout"},
		sourceStrict: true,
	}
	mergeSourceRef(&row, &appSourceRef{Type: "gitops", Tool: "argocd", Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "checkout"})
	if row.SourceRef == nil || row.SourceRef.Type != "helm" || row.SourceRef.Name != "checkout" {
		t.Fatalf("strict source ref should survive managed conflict: %+v", row.SourceRef)
	}
	if row.SourceConflict {
		t.Fatalf("strict source ref conflict should not mark row conflicted")
	}
}

func TestMergeSourceRefClearsWeakConflict(t *testing.T) {
	row := appRow{SourceRef: &appSourceRef{Type: "helm", Tool: "helm", Kind: "HelmRelease", Namespace: "prod", Name: "checkout"}}
	mergeSourceRef(&row, &appSourceRef{Type: "gitops", Tool: "argocd", Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "checkout"})
	if row.SourceRef != nil || !row.SourceConflict {
		t.Fatalf("weak source conflict should clear source ref and mark conflict: ref=%+v conflict=%v", row.SourceRef, row.SourceConflict)
	}
}

func TestHistorySummaryPrioritizesCurrentIncidents(t *testing.T) {
	summary := historySummary(
		[]appHistoryAnchor{{Title: "Helm revision 3", Status: "deployed", Revision: "3", Timestamp: "2026-07-08T10:00:00Z"}},
		[]appHistoryIncident{{Title: "FailedScheduling on Pod/api", Message: "0/9 nodes are available", LastSeen: "2026-07-08T11:00:00Z"}},
	)
	if summary == nil || summary.State != "incident" || summary.Title != "Current incident: FailedScheduling on Pod/api" || summary.Detail != "0/9 nodes are available" {
		t.Fatalf("incident summary = %+v", summary)
	}

	summary = historySummary(
		[]appHistoryAnchor{{Title: "Helm revision 3", Status: "deployed", Revision: "3", Message: "Upgrade complete", Timestamp: "2026-07-08T10:00:00Z"}},
		nil,
	)
	if summary == nil || summary.State != "change" || summary.Detail != "deployed · 3 · Upgrade complete" {
		t.Fatalf("change summary = %+v", summary)
	}

	summary = historySummary(nil, nil)
	if summary == nil || summary.State != "none" {
		t.Fatalf("empty summary = %+v", summary)
	}
}

func TestHistoryIncidentsSortAndTimestampFallbacks(t *testing.T) {
	events := []appEvent{
		{Reason: "Older", Object: "Pod/old", Message: "old", Count: 1, LastSeen: "2026-07-08T10:00:00Z"},
		{Reason: "Newer", Object: "Pod/new", Message: "new", Count: 3, FirstSeen: "2026-07-08T09:00:00Z", LastSeen: "2026-07-08T11:00:00Z"},
	}
	incidents := historyIncidents(events)
	if len(incidents) != 2 || incidents[0].Title != "Newer on Pod/new" || incidents[0].Count != 3 || incidents[0].FirstSeen == "" {
		t.Fatalf("incidents = %+v", incidents)
	}
}

func TestGitOpsPluralKind(t *testing.T) {
	cases := map[string]string{
		"Application":    "applications",
		"ApplicationSet": "applicationsets",
		"AppProject":     "appprojects",
		"Kustomization":  "kustomizations",
		"HelmRelease":    "helmreleases",
		"Deployment":     "",
	}
	for kind, want := range cases {
		if got := gitOpsPluralKind(kind); got != want {
			t.Fatalf("gitOpsPluralKind(%q) = %q, want %q", kind, got, want)
		}
	}
}

// Over-merge guardrail: two distinct apps that share a satellite Service must
// NOT fuse. Satellites are attached, never used to partition.
func TestGroupApplications_SharedSatelliteDoesNotMerge(t *testing.T) {
	a := overlayInput("Deployment", "prod", "api", "1.0", "healthy", subject.TierPartOf, "prod/app/alpha", subject.ConfidenceMedium)
	a.rels = &appRelationships{Services: []string{"shared-gateway"}}
	b := overlayInput("Deployment", "prod", "web", "1.0", "healthy", subject.TierPartOf, "prod/app/beta", subject.ConfidenceMedium)
	b.rels = &appRelationships{Services: []string{"shared-gateway"}}

	rows := groupApplications([]appWorkloadInput{a, b})
	if len(rows) != 2 {
		t.Fatalf("apps sharing only a Service must stay separate, got %d: %+v", len(rows), rows)
	}
	for _, r := range rows {
		if r.Relationships == nil || len(r.Relationships.Services) != 1 || r.Relationships.Services[0] != "shared-gateway" {
			t.Errorf("each app should still list the shared service, got %+v", r.Relationships)
		}
	}
}

func TestGroupApplications_RelationshipCountsDeduplicateSharedRefs(t *testing.T) {
	ref := func(kind, ns, name string) topology.ResourceRef {
		return topology.ResourceRef{Kind: kind, Namespace: ns, Name: name}
	}
	a := overlayInput("Deployment", "prod", "api", "1.0", "healthy", subject.TierPartOf, "prod/app/checkout", subject.ConfidenceMedium)
	a.rels = &appRelationships{
		configRefs:        refsByKey([]topology.ResourceRef{ref("ConfigMap", "prod", "shared-config")}),
		scalerRefs:        refsByKey([]topology.ResourceRef{ref("HorizontalPodAutoscaler", "prod", "checkout")}),
		storageRefs:       refsByKey([]topology.ResourceRef{ref("PersistentVolumeClaim", "prod", "checkout-data")}),
		pdbRefs:           refsByKey([]topology.ResourceRef{ref("PodDisruptionBudget", "prod", "checkout")}),
		networkPolicyRefs: refsByKey([]topology.ResourceRef{ref("NetworkPolicy", "prod", "checkout-net")}),
	}
	b := overlayInput("Deployment", "prod", "worker", "1.0", "healthy", subject.TierPartOf, "prod/app/checkout", subject.ConfidenceMedium)
	b.rels = &appRelationships{
		configRefs:        refsByKey([]topology.ResourceRef{ref("ConfigMap", "prod", "shared-config")}),
		scalerRefs:        refsByKey([]topology.ResourceRef{ref("HorizontalPodAutoscaler", "prod", "checkout")}),
		storageRefs:       refsByKey([]topology.ResourceRef{ref("PersistentVolumeClaim", "prod", "checkout-data")}),
		pdbRefs:           refsByKey([]topology.ResourceRef{ref("PodDisruptionBudget", "prod", "checkout")}),
		networkPolicyRefs: refsByKey([]topology.ResourceRef{ref("NetworkPolicy", "prod", "checkout-net")}),
	}

	rows := groupApplications([]appWorkloadInput{a, b})
	r := rowByName(rows, "checkout")
	if r == nil || r.Relationships == nil {
		t.Fatalf("checkout relationships missing: %+v", rows)
	}
	if r.Relationships.Configs != 1 || r.Relationships.Scalers != 1 || r.Relationships.Storage != 1 || r.Relationships.PDBs != 1 || r.Relationships.NetworkPolicies != 1 {
		t.Fatalf("relationship counts = %+v, want each shared ref counted once", r.Relationships)
	}
	if len(r.Relationships.ConfigRefs) != 1 || r.Relationships.ConfigRefs[0].Name != "shared-config" {
		t.Fatalf("config refs = %+v, want shared-config once", r.Relationships.ConfigRefs)
	}
	if len(r.Relationships.NetworkPolicyRefs) != 1 || r.Relationships.NetworkPolicyRefs[0].Name != "checkout-net" {
		t.Fatalf("network policy refs = %+v, want checkout-net once", r.Relationships.NetworkPolicyRefs)
	}
}

func TestGroupApplications_EntrypointRefsDeduplicateAndClassify(t *testing.T) {
	ref := func(kind, ns, name string) topology.ResourceRef {
		return topology.ResourceRef{Kind: kind, Namespace: ns, Name: name}
	}
	a := overlayInput("Deployment", "prod", "api", "1.0", "healthy", subject.TierPartOf, "prod/app/checkout", subject.ConfidenceMedium)
	a.rels = &appRelationships{
		serviceRefs: refsByKey([]topology.ResourceRef{ref("Service", "prod", "checkout-api")}),
		ingressRefs: refsByKey([]topology.ResourceRef{ref("Ingress", "prod", "checkout")}),
		routeRefs:   refsByKey([]topology.ResourceRef{ref("HTTPRoute", "prod", "checkout")}),
	}
	a.wl.WorkloadClass = classifyWorkload(a.wl.Kind, a.rels)
	b := overlayInput("Deployment", "prod", "api-canary", "1.0", "healthy", subject.TierPartOf, "prod/app/checkout", subject.ConfidenceMedium)
	b.rels = &appRelationships{
		serviceRefs: refsByKey([]topology.ResourceRef{ref("Service", "prod", "checkout-api")}),
		ingressRefs: refsByKey([]topology.ResourceRef{ref("Ingress", "prod", "checkout")}),
		routeRefs:   refsByKey([]topology.ResourceRef{ref("HTTPRoute", "prod", "checkout")}),
	}
	b.wl.WorkloadClass = classifyWorkload(b.wl.Kind, b.rels)

	rows := groupApplications([]appWorkloadInput{a, b})
	r := rowByName(rows, "checkout")
	if r == nil || r.Relationships == nil {
		t.Fatalf("checkout relationships missing: %+v", rows)
	}
	if r.WorkloadClass != "service" {
		t.Fatalf("workload class = %q, want service", r.WorkloadClass)
	}
	if len(r.Relationships.ServiceRefs) != 1 || r.Relationships.ServiceRefs[0].Name != "checkout-api" || r.Relationships.ServiceRefs[0].Namespace != "prod" {
		t.Fatalf("service refs = %+v, want exact checkout-api ref once", r.Relationships.ServiceRefs)
	}
	if len(r.Relationships.IngressRefs) != 1 || r.Relationships.IngressRefs[0].Name != "checkout" {
		t.Fatalf("ingress refs = %+v, want checkout once", r.Relationships.IngressRefs)
	}
	if len(r.Relationships.RouteRefs) != 1 || r.Relationships.RouteRefs[0].Kind != "HTTPRoute" {
		t.Fatalf("route refs = %+v, want HTTPRoute once", r.Relationships.RouteRefs)
	}
}

func TestAppGraphRelationshipsForIncludesServiceUpstreamEntrypoints(t *testing.T) {
	node := func(kind, ns, name string) topology.Node {
		id := strings.ToLower(kind) + "/" + ns + "/" + name
		return topology.Node{ID: id, Kind: topology.NodeKind(kind), Name: name, Data: map[string]any{"namespace": ns}}
	}
	edge := func(src, dst string, typ topology.EdgeType) topology.Edge {
		return topology.Edge{ID: src + "->" + dst, Source: src, Target: dst, Type: typ}
	}
	topo := &topology.Topology{
		Nodes: []topology.Node{
			node("Deployment", "prod", "api"),
			node("Service", "prod", "api"),
			node("Ingress", "prod", "api"),
			node("HTTPRoute", "prod", "api-route"),
			node("IngressRoute", "prod", "api-traefik"),
			node("VirtualService", "prod", "api-istio"),
			node("HTTPProxy", "prod", "api-contour"),
		},
		Edges: []topology.Edge{
			edge("service/prod/api", "deployment/prod/api", topology.EdgeExposes),
			edge("ingress/prod/api", "service/prod/api", topology.EdgeRoutesTo),
			edge("httproute/prod/api-route", "service/prod/api", topology.EdgeRoutesTo),
			edge("ingressroute/prod/api-traefik", "service/prod/api", topology.EdgeExposes),
			edge("virtualservice/prod/api-istio", "service/prod/api", topology.EdgeExposes),
			edge("httpproxy/prod/api-contour", "service/prod/api", topology.EdgeExposes),
		},
	}
	g := &appGraph{topo: topo, idx: topology.IndexByResource(topo)}

	rels := g.relationshipsFor("Deployment", "prod", "api")
	if rels == nil {
		t.Fatal("relationshipsFor returned nil")
	}
	serviceRefs := sortedRefs(rels.serviceRefs, 10)
	if len(serviceRefs) != 1 || serviceRefs[0].Kind != "Service" || serviceRefs[0].Namespace != "prod" || serviceRefs[0].Name != "api" {
		t.Fatalf("service refs = %+v, want prod/api", serviceRefs)
	}
	ingressRefs := sortedRefs(rels.ingressRefs, 10)
	if len(ingressRefs) != 1 || ingressRefs[0].Kind != "Ingress" || ingressRefs[0].Namespace != "prod" || ingressRefs[0].Name != "api" {
		t.Fatalf("ingress refs = %+v, want prod/api", ingressRefs)
	}
	routeRefs := sortedRefs(rels.routeRefs, 10)
	if len(routeRefs) != 4 {
		t.Fatalf("route refs = %+v, want HTTPRoute, HTTPProxy, IngressRoute, VirtualService", routeRefs)
	}
	gotRoutes := map[string]bool{}
	for _, ref := range routeRefs {
		gotRoutes[strings.ToLower(ref.Kind)+"/"+ref.Name] = true
	}
	for _, want := range []string{"httproute/api-route", "ingressroute/api-traefik", "virtualservice/api-istio", "httpproxy/api-contour"} {
		if !gotRoutes[want] {
			t.Fatalf("route refs = %+v, missing %s", routeRefs, want)
		}
	}
}

// structuralRoot must stop AT the in-cluster GitOps manager (Flux
// Kustomization) and NOT climb the EdgeManages edge to the GitRepository source
// that feeds it. The topology builder models GitRepository → Kustomization as
// EdgeManages too, so without the stop-at-manager rule a Flux mono-repo (one
// GitRepository sourcing N Kustomizations) resolves every workload to the same
// GitRepository root and union-find merges all installations into one app.
func TestStructuralRoot_StopsAtManagerNotSource(t *testing.T) {
	node := func(id, kind, ns, name string) topology.Node {
		return topology.Node{ID: id, Kind: topology.NodeKind(kind), Name: name, Data: map[string]any{"namespace": ns}}
	}
	manages := func(src, dst string) topology.Edge {
		return topology.Edge{ID: src + "->" + dst, Source: src, Target: dst, Type: topology.EdgeManages}
	}
	topo := &topology.Topology{
		Nodes: []topology.Node{
			node("gitrepo", "GitRepository", "flux-system", "monorepo"),
			node("ks-apps", "Kustomization", "flux-system", "apps"),
			node("ks-infra", "Kustomization", "flux-system", "infrastructure"),
			node("dep-api", "Deployment", "prod", "api"),
			node("dep-grafana", "Deployment", "monitoring", "grafana"),
		},
		Edges: []topology.Edge{
			manages("gitrepo", "ks-apps"),      // source ref — must NOT be climbed through
			manages("gitrepo", "ks-infra"),     // source ref — must NOT be climbed through
			manages("ks-apps", "dep-api"),      // manager → workload
			manages("ks-infra", "dep-grafana"), // manager → workload
		},
	}
	g := &appGraph{byID: map[string]topology.Node{}, byKNN: map[string]string{}, topo: topo, idx: topology.IndexByResource(topo)}
	for _, n := range topo.Nodes {
		g.byID[n.ID] = n
		ns, _ := n.Data["namespace"].(string)
		g.byKNN[knnKey(string(n.Kind), ns, n.Name)] = n.ID
	}

	apiRoot, _ := g.rootOf("Deployment", "prod", "api")
	grafanaRoot, _ := g.rootOf("Deployment", "monitoring", "grafana")

	if apiRoot != "flux-system/Kustomization/apps" {
		t.Errorf("api root = %q, want the apps Kustomization (not the GitRepository)", apiRoot)
	}
	if grafanaRoot != "flux-system/Kustomization/infrastructure" {
		t.Errorf("grafana root = %q, want the infrastructure Kustomization (not the GitRepository)", grafanaRoot)
	}
	if apiRoot == grafanaRoot {
		t.Fatalf("two Kustomizations under one GitRepository share root %q — the mono-repo over-merge", apiRoot)
	}
}

// relationshipsFor ships routes as "Kind/name" so the client can index them
// under the concrete (polymorphic) route kind — HTTPRoute/GRPCRoute/… — matching
// the route lane id. A bare name would collapse to a generic "Route" that never
// resolves.
func TestRelationshipsFor_RoutesCarryConcreteKind(t *testing.T) {
	node := func(id, kind, ns, name string) topology.Node {
		return topology.Node{ID: id, Kind: topology.NodeKind(kind), Name: name, Data: map[string]any{"namespace": ns}}
	}
	topo := &topology.Topology{
		Nodes: []topology.Node{
			node("gateway/prod/gw", "Gateway", "prod", "gw"),
			node("httproute/prod/web", "HTTPRoute", "prod", "web"),
		},
		Edges: []topology.Edge{
			// A Gateway routes to an HTTPRoute → rel.Routes for the Gateway query.
			{ID: "gw->web", Source: "gateway/prod/gw", Target: "httproute/prod/web", Type: topology.EdgeRoutesTo},
		},
	}
	g := &appGraph{byID: map[string]topology.Node{}, byKNN: map[string]string{}, topo: topo, idx: topology.IndexByResource(topo)}

	rels := g.relationshipsFor("Gateway", "prod", "gw")
	if rels == nil {
		t.Fatal("relationshipsFor returned nil; want Routes populated")
	}
	if len(rels.Routes) != 1 || rels.Routes[0] != "HTTPRoute/web" {
		t.Fatalf("Routes = %v, want [\"HTTPRoute/web\"] (concrete kind, not bare name)", rels.Routes)
	}
}

// Add-ons are classified with evidence, never dropped (raw-always). A user
// workload named "grafana" still appears — tagged, explained, foldable.
func TestClassifyAddon_ClassifiesNotHides(t *testing.T) {
	addon, why := packages.ClassifyAddon("", "grafana", "", "grafana-0", "", "")
	if !addon || why == "" {
		t.Fatalf("grafana should classify as addon with evidence, got addon=%v why=%q", addon, why)
	}

	rows := groupApplications([]appWorkloadInput{
		func() appWorkloadInput {
			in := rawInput("Deployment", "monitoring", "grafana", "10.0", "healthy")
			in.addon, in.addonWhy = packages.ClassifyAddon("", "grafana", "", "grafana", "", "")
			return in
		}(),
		rawInput("Deployment", "prod", "my-service", "1.0", "healthy"),
	})
	if len(rows) != 2 {
		t.Fatalf("add-on must remain a row (not dropped), got %d apps", len(rows))
	}
	g := rowByName(rows, "grafana")
	if g == nil || g.Category != "addon" || g.AddonReason == "" {
		t.Errorf("grafana row should be Category=addon with a reason, got %+v", g)
	}
	svc := rowByName(rows, "my-service")
	if svc == nil || svc.Category != "app" {
		t.Errorf("my-service should be Category=app, got %+v", svc)
	}
}

func TestClassifyAddon_MixedEvidenceDoesNotForceAddon(t *testing.T) {
	addon := rawInput("Deployment", "prod", "grafana-sidecar", "10.0", "healthy")
	addon.addon, addon.addonWhy = packages.ClassifyAddon("", "grafana", "", "grafana-sidecar", "", "")
	app := rawInput("Deployment", "prod", "api", "1.0", "healthy")
	addon.overlay = &subject.AppOverlay{Winner: subject.Signal{Tier: subject.TierPartOf, Key: "prod/app/checkout", Confidence: subject.ConfidenceMedium}}
	app.overlay = &subject.AppOverlay{Winner: subject.Signal{Tier: subject.TierPartOf, Key: "prod/app/checkout", Confidence: subject.ConfidenceMedium}}

	rows := groupApplications([]appWorkloadInput{addon, app})
	if len(rows) != 1 {
		t.Fatalf("shared overlay should produce one app, got %d: %+v", len(rows), rows)
	}
	if rows[0].Category != "mixed" {
		t.Fatalf("mixed add-on evidence should classify as mixed, got %q", rows[0].Category)
	}
	if rows[0].AddonReason == "" {
		t.Fatalf("mixed classification should preserve add-on evidence")
	}
}

func TestWorkloadClass_FacetIsDerivedFromRuntimeShape(t *testing.T) {
	service := rawInput("Deployment", "prod", "api", "1.0", "healthy")
	service.wl.WorkloadClass = classifyWorkload("Deployment", &appRelationships{Services: []string{"api"}})
	service.rels = &appRelationships{Services: []string{"api"}}
	worker := rawInput("Deployment", "prod", "worker", "1.0", "healthy")
	job := rawInput("CronJob", "prod", "nightly", "", "healthy")

	rows := groupApplications([]appWorkloadInput{service, worker, job})
	if got := rowByName(rows, "api"); got == nil || got.WorkloadClass != "service" {
		t.Fatalf("service row class = %+v, want service", got)
	}
	if got := rowByName(rows, "worker"); got == nil || got.WorkloadClass != "worker" {
		t.Fatalf("worker row class = %+v, want worker", got)
	}
	if got := rowByName(rows, "nightly"); got == nil || got.WorkloadClass != "job" {
		t.Fatalf("cronjob row class = %+v, want job", got)
	}
}

func TestGroupApplications_MixedHealthUsesServingWorkloads(t *testing.T) {
	service := overlayInput("Deployment", "prod", "api", "1.0", "healthy", subject.TierPartOf, "prod/app/checkout", subject.ConfidenceMedium)
	service.wl.WorkloadClass = "service"
	failedBatch := overlayInput("CronWorkflow", "prod", "nightly", "", "unhealthy", subject.TierPartOf, "prod/app/checkout", subject.ConfidenceMedium)

	rows := groupApplications([]appWorkloadInput{service, failedBatch})
	if len(rows) != 1 {
		t.Fatalf("shared overlay should produce one mixed app, got %+v", rows)
	}
	if rows[0].WorkloadClass != "mixed" {
		t.Fatalf("workload class = %q, want mixed", rows[0].WorkloadClass)
	}
	if rows[0].Health != "healthy" {
		t.Fatalf("mixed app health = %q, want serving-only healthy verdict", rows[0].Health)
	}

	pureBatch := groupApplications([]appWorkloadInput{rawInput("CronWorkflow", "prod", "nightly", "", "unhealthy")})
	if len(pureBatch) != 1 || pureBatch[0].Health != "unhealthy" {
		t.Fatalf("pure batch health = %+v, want unhealthy batch verdict", pureBatch)
	}
}

// The app's namespace is where its WORKLOADS run, not where the GitOps manager
// lives: a Flux HelmRelease in flux-system deploying into demo is a demo app.
// The residence override must win over identifyApp's provenance-key namespace.
func TestGroupApplications_NamespaceIsWorkloadResidence(t *testing.T) {
	rows := groupApplications([]appWorkloadInput{
		overlayInput("Deployment", "demo", "podinfo", "6.13.0", "healthy", subject.TierFluxHelmRelease, "flux-system/HelmRelease/podinfo", subject.ConfidenceHigh),
	})
	r := rowByName(rows, "podinfo")
	if r == nil {
		t.Fatalf("podinfo app missing: %+v", rows)
	}
	if r.Namespace != "demo" {
		t.Errorf("Namespace = %q, want workload residence %q (not the HelmRelease's flux-system)", r.Namespace, "demo")
	}
	if len(r.Namespaces) != 1 || r.Namespaces[0] != "demo" {
		t.Errorf("Namespaces = %v, want [demo]", r.Namespaces)
	}
}

// An app spanning namespaces reports Namespace empty (no arbitrary pick) and
// the full sorted list in Namespaces.
func TestGroupApplications_MultiNamespaceApp(t *testing.T) {
	rows := groupApplications([]appWorkloadInput{
		overlayInput("Deployment", "prometheus", "server", "v2.49.1", "healthy", subject.TierArgoTrackingID, "/Application/prom", subject.ConfidenceHigh),
		overlayInput("Deployment", "opencost", "opencost", "1.108.0", "healthy", subject.TierArgoTrackingID, "/Application/prom", subject.ConfidenceHigh),
	})
	r := rowByName(rows, "prom")
	if r == nil {
		t.Fatalf("prom app missing: %+v", rows)
	}
	if r.Namespace != "" {
		t.Errorf("Namespace = %q, want empty for a multi-namespace app", r.Namespace)
	}
	want := []string{"opencost", "prometheus"}
	if len(r.Namespaces) != 2 || r.Namespaces[0] != want[0] || r.Namespaces[1] != want[1] {
		t.Errorf("Namespaces = %v, want %v", r.Namespaces, want)
	}
}

// Version skew means the SAME image runs different tags; different components
// shipping different images at different versions is diversity, not skew.
func TestGroupApplications_VersionSkew(t *testing.T) {
	skewA := overlayInput("Deployment", "prod", "api", "1.2.0", "healthy", subject.TierPartOf, "prod/app/checkout", subject.ConfidenceMedium)
	skewA.wl.Image = "ghcr.io/acme/api:1.2.0"
	skewB := overlayInput("Deployment", "prod", "api-canary", "1.3.0", "healthy", subject.TierPartOf, "prod/app/checkout", subject.ConfidenceMedium)
	skewB.wl.Image = "ghcr.io/acme/api:1.3.0"
	rows := groupApplications([]appWorkloadInput{skewA, skewB})
	if r := rowByName(rows, "checkout"); r == nil || !r.VersionSkew {
		t.Errorf("same image at two tags should set VersionSkew, got %+v", r)
	}

	divA := overlayInput("Deployment", "prod", "server", "v3.2.6", "healthy", subject.TierPartOf, "prod/app/argo", subject.ConfidenceMedium)
	divA.wl.Image = "quay.io/argoproj/argocd:v3.2.6"
	divB := overlayInput("Deployment", "prod", "redis", "8.2.2", "healthy", subject.TierPartOf, "prod/app/argo", subject.ConfidenceMedium)
	divB.wl.Image = "ecr.io/redis:8.2.2"
	rows = groupApplications([]appWorkloadInput{divA, divB})
	if r := rowByName(rows, "argo"); r == nil || r.VersionSkew {
		t.Errorf("different images at different tags is diversity, not skew, got %+v", r)
	}
}

// AppVersion is the app's "main version" only when EVERY workload declares
// app.kubernetes.io/version and they agree.
func TestGroupApplications_AppVersionUnanimity(t *testing.T) {
	mk := func(name, appVer string) appWorkloadInput {
		in := overlayInput("Deployment", "prod", name, "x", "healthy", subject.TierPartOf, "prod/app/argo", subject.ConfidenceMedium)
		in.wl.AppVersion = appVer
		return in
	}
	if r := rowByName(groupApplications([]appWorkloadInput{mk("a", "v3.2.6"), mk("b", "v3.2.6")}), "argo"); r == nil || r.AppVersion != "v3.2.6" {
		t.Errorf("unanimous labels should set AppVersion, got %+v", r)
	}
	if r := rowByName(groupApplications([]appWorkloadInput{mk("a", "v3.2.6"), mk("b", "")}), "argo"); r == nil || r.AppVersion != "" {
		t.Errorf("a labeled workload among unlabeled ones must not set AppVersion, got %+v", r)
	}
	if r := rowByName(groupApplications([]appWorkloadInput{mk("a", "v3.2.6"), mk("b", "v2.44.0")}), "argo"); r == nil || r.AppVersion != "" {
		t.Errorf("disagreeing labels must not set AppVersion, got %+v", r)
	}
}

// matchKeys carry the EXACT grouping-signal values the server keyed on, so the
// client can re-join timeline events (including deleted members) to an app.
// They collect from every member's overlay winner + retained conflicts, deduped
// and sorted, and use the tier→kind mapping (part-of/name/instance/app/helm/argo).
func TestGroupApplications_MatchKeys(t *testing.T) {
	// A workload whose winner is part-of but which ALSO carries a bare-app
	// conflict — both surface as match keys.
	partOf := overlayInput("Deployment", "team-a", "billing-api", "1.0", "healthy", subject.TierPartOf, "team-a/app/checkout", subject.ConfidenceMedium)
	partOf.overlay.Conflicts = []subject.Signal{{Tier: subject.TierBareApp, Key: "team-a/app/checkout", Confidence: subject.ConfidenceLow}}
	// A second member keyed identically — dedup must collapse the repeated key.
	partOfSibling := overlayInput("Deployment", "team-a", "billing-worker", "1.0", "healthy", subject.TierPartOf, "team-a/app/checkout", subject.ConfidenceMedium)

	rows := groupApplications([]appWorkloadInput{partOf, partOfSibling})
	r := rowByName(rows, "checkout")
	if r == nil {
		t.Fatalf("checkout app missing: %+v", rows)
	}
	want := []string{"app:team-a:checkout", "part-of:team-a:checkout"} // namespace-scoped, sorted, deduped
	if len(r.MatchKeys) != len(want) {
		t.Fatalf("matchKeys = %v, want %v", r.MatchKeys, want)
	}
	for i := range want {
		if r.MatchKeys[i] != want[i] {
			t.Errorf("matchKeys[%d] = %q, want %q (full: %v)", i, r.MatchKeys[i], want[i], r.MatchKeys)
		}
	}

	// Flux HelmRelease winner → "helm:<name>".
	helm := groupApplications([]appWorkloadInput{
		overlayInput("Deployment", "demo", "podinfo", "6.13.0", "healthy", subject.TierFluxHelmRelease, "flux-system/HelmRelease/podinfo", subject.ConfidenceHigh),
	})
	if r := rowByName(helm, "podinfo"); r == nil || len(r.MatchKeys) != 1 || r.MatchKeys[0] != "helm:demo:podinfo" {
		t.Errorf("helm matchKeys = %+v, want [helm:demo:podinfo]", r)
	}

	// Native Helm winner → NO match key: release identity is an annotation
	// (meta.helm.sh/release-name), which events never carry, so the key could
	// never join a timeline event to the app.
	nativeHelm := groupApplications([]appWorkloadInput{
		overlayInput("Deployment", "prod", "checkout-api", "1.0", "healthy", subject.TierHelmRelease, "prod/HelmRelease/checkout", subject.ConfidenceMedium),
	})
	if r := rowByName(nativeHelm, "checkout"); r == nil || len(r.MatchKeys) != 0 {
		t.Errorf("native helm matchKeys = %+v, want none", r)
	}

	// Argo tracking-id winner → "argo:<name>".
	argo := groupApplications([]appWorkloadInput{
		overlayInput("Deployment", "team-a", "storefront-api", "2.0.0", "healthy", subject.TierArgoTrackingID, "argocd/Application/storefront", subject.ConfidenceHigh),
	})
	if r := rowByName(argo, "storefront"); r == nil || len(r.MatchKeys) != 1 || r.MatchKeys[0] != "argo:team-a:storefront" {
		t.Errorf("argo matchKeys = %+v, want [argo:team-a:storefront]", r)
	}

	// A raw (no-overlay) singleton has no exact match keys.
	raw := groupApplications([]appWorkloadInput{rawInput("StatefulSet", "team-a", "lonely-db", "15", "healthy")})
	if r := rowByName(raw, "lonely-db"); r == nil || len(r.MatchKeys) != 0 {
		t.Errorf("raw app should have no matchKeys, got %+v", r)
	}
}

func TestImageRepo(t *testing.T) {
	cases := map[string]string{
		"nginx:1.27":                "nginx",
		"ghcr.io/acme/api:1.2.0":    "ghcr.io/acme/api",
		"registry:5000/team/img:v1": "registry:5000/team/img", // registry port colon is not the tag separator
		"repo/img@sha256:abc":       "repo/img",
		"registry:5000/team/img":    "registry:5000/team/img", // no tag
		"":                          "",
	}
	for in, want := range cases {
		if got := imageRepo(in); got != want {
			t.Errorf("imageRepo(%q) = %q, want %q", in, got, want)
		}
	}
}

// A real class mix (service + scheduled job) reports "mixed", not "unknown" —
// the per-workload classes are confident and the UI shows the composition.
// service+worker stays "service" (operated primarily as a service), and an
// unclassifiable member (bare Pod) doesn't poison a known class.
func TestWorkloadClass_MixedComposition(t *testing.T) {
	mk := func(name, kind, class string) appWorkloadInput {
		in := overlayInput(kind, "prod", name, "1.0", "healthy", subject.TierPartOf, "prod/app/shop", subject.ConfidenceMedium)
		in.wl.WorkloadClass = class
		return in
	}
	cases := []struct {
		name string
		ins  []appWorkloadInput
		want string
	}{
		{"service+job", []appWorkloadInput{mk("api", "Deployment", "service"), mk("nightly", "CronJob", "job")}, "mixed"},
		{"worker+job", []appWorkloadInput{mk("proc", "Deployment", "worker"), mk("nightly", "CronJob", "job")}, "mixed"},
		{"service+worker", []appWorkloadInput{mk("api", "Deployment", "service"), mk("proc", "Deployment", "worker")}, "service"},
		{"service+unknown", []appWorkloadInput{mk("api", "Deployment", "service"), mk("dbg", "Pod", "unknown")}, "service"},
		{"only-unknown", []appWorkloadInput{mk("dbg", "Pod", "unknown")}, "unknown"},
	}
	for _, c := range cases {
		rows := groupApplications(c.ins)
		if r := rowByName(rows, "shop"); r == nil || r.WorkloadClass != c.want {
			t.Errorf("%s: WorkloadClass = %v, want %s", c.name, r, c.want)
		}
	}
}
