package kargo

import (
	"slices"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const stageDoc = `
apiVersion: kargo.akuity.io/v1alpha1
kind: Stage
metadata:
  name: uat
  namespace: shop
  annotations:
    kargo.akuity.io/argocd-context: '[{"name":"shop-uat","namespace":"argocd"},{"name":"shop-uat","namespace":"argocd"},{"name":"","namespace":"argocd"}]'
spec:
  requestedFreight:
  - origin: {kind: Warehouse, name: shop}
    sources:
      stages: [dev, qa]
      requiredSoakTime: 1h
      availabilityStrategy: All
      autoPromotionOptions: {selectionPolicy: MatchUpstream}
  - origin: {kind: Warehouse, name: infra}
    sources: {direct: true}
  promotionTemplate:
    spec:
      steps:
      - uses: git-clone
status:
  autoPromotionEnabled: true
  conditions:
  - {type: Ready, status: "False", reason: VerificationFailed, message: "metric failed"}
  - {type: Verified, status: "False", reason: VerificationFailed}
  freightHistory:
  - id: abc
    items:
      Warehouse/shop:
        name: 3f2a9c1d0e
        origin: {kind: Warehouse, name: shop}
        images: [{repoURL: nginx, tag: 1.31.9}]
      Warehouse/infra:
        name: 77aa
        images: [{repoURL: redis, tag: "8"}]
    verificationHistory:
    - id: v2
      phase: Failed
      message: "Metric \"smoke\" assessed Failed"
      analysisRun: {name: uat.01abc, namespace: shop, phase: Failed}
      startTime: "2026-11-25T11:00:00Z"
      finishTime: "2026-11-25T11:02:00Z"
    - id: v1
      phase: Successful
  - id: older
    items:
      Warehouse/shop: {name: old, origin: {kind: Warehouse, name: shop}}
  health:
    status: Unhealthy
    issues: ["app degraded"]
    output:
    - applicationStatuses:
      - Name: shop-uat
        Namespace: argocd
        health: {status: Degraded, message: "Deployment has 0 ready"}
        sync: {status: Synced, revision: 3f2a9c1}
  lastPromotion:
    name: uat.01promo
    finishedAt: "2026-11-25T10:59:00Z"
    freight: {name: 3f2a9c1d0e, origin: {kind: Warehouse, name: shop}}
    status:
      phase: Errored
      message: "step 1 errored"
      stepExecutionMetadata:
      - {alias: step-0, status: Succeeded}
      - {alias: step-1, status: Errored, message: "failed to push: authentication required", errorCount: 3}
`

func TestParseStage(t *testing.T) {
	s := ParseStage(parse(t, stageDoc))
	if len(s.Current) != 2 || s.Current[0].Origin.Name != "infra" || s.Current[1].Name != "3f2a9c1d0e" {
		t.Fatalf("current = %+v; want one per origin, ordered by origin", s.Current)
	}
	if s.Current[0].Origin != (Origin{Kind: "Warehouse", Name: "infra"}) {
		t.Fatalf("an item without origin takes it from its key: %+v", s.Current[0])
	}
	shop, _ := s.RequestFor(Origin{Kind: "Warehouse", Name: "shop"})
	if !shop.All || shop.RequiredSoak != time.Hour || shop.SelectionPolicy != SelectionMatchUpstream || !slices.Equal(shop.Stages, []string{"dev", "qa"}) || shop.Direct {
		t.Fatalf("shop request = %+v", shop)
	}
	if !slices.Equal(s.Upstream(), []string{"dev", "qa"}) || !slices.Equal(s.Warehouses(), []string{"infra"}) {
		t.Fatalf("sources = %v %v", s.Upstream(), s.Warehouses())
	}
	v := s.Verification
	if v == nil || !v.Failed() || v.ID != "v2" || v.AnalysisRun == nil || v.AnalysisRun.Name != "uat.01abc" {
		t.Fatalf("verification = %+v", v)
	}
	if s.Health == nil || s.Health.Status != "Unhealthy" || len(s.Health.Apps) != 1 || s.Health.Apps[0] != (AppStatus{Namespace: "argocd", Name: "shop-uat", Health: "Degraded", HealthMessage: "Deployment has 0 ready", Sync: "Synced", Revision: "3f2a9c1"}) {
		t.Fatalf("health = %+v", s.Health)
	}
	if s.LastPromotion == nil || s.LastPromotion.Phase != PhaseErrored || s.LastPromotion.Freight.Name != "3f2a9c1d0e" {
		t.Fatalf("last promotion = %+v", s.LastPromotion)
	}
	if f := s.LastPromotion.Failure(); !f.Failed || f.Step != 1 || f.Message != "failed to push: authentication required" {
		t.Fatalf("failure = %+v", f)
	}
	if !s.AutoPromotion || s.ControlFlow || s.Held || s.CurrentPromotion != nil {
		t.Fatalf("flags = %+v", s)
	}
	if !slices.Equal(s.ArgoCDApps, []ObjectRef{{Namespace: "argocd", Name: "shop-uat"}}) {
		t.Fatalf("argocd apps = %v; want deduplicated, unnamed skipped", s.ArgoCDApps)
	}
}

func TestParseStageWithoutStatus(t *testing.T) {
	u := parse(t, stageDoc)
	delete(u.Object, "status")
	unstructured.RemoveNestedField(u.Object, "spec", "promotionTemplate")
	s := ParseStage(u)
	if s.Current != nil || s.Verification != nil || s.Health != nil || s.LastPromotion != nil || !s.ControlFlow {
		t.Fatalf("stage = %+v", s)
	}
}

func TestPhaseMirrorsKargoUI(t *testing.T) {
	cond := func(typ, status, reason string) any {
		return map[string]any{"type": typ, "status": status, "reason": reason}
	}
	for _, tc := range []struct {
		name  string
		conds []any
		want  string
	}{
		{"promoting", []any{cond("Promoting", "True", "Running"), cond("Ready", "False", "x")}, "Promoting"},
		{"verifying", []any{cond("Verified", "Unknown", "VerificationRunning")}, "Verifying"},
		{"reconciling", []any{cond("Reconciling", "True", "x")}, "Reconciling"},
		{"failed", []any{cond("Ready", "False", "LastPromotionFailed")}, "Failed"},
		{"no freight is not failed", []any{cond("Ready", "False", "NoFreight")}, "Unknown"},
		{"ready", []any{cond("Ready", "True", "Verified")}, "Ready"},
		{"nothing", nil, "Unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := parse(t, stageDoc)
			set(t, u, tc.conds, "status", "conditions")
			if got := Phase(u); got != tc.want {
				t.Fatalf("phase = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestPipelineOrdersByDepth(t *testing.T) {
	stage := func(name string, upstream ...string) Stage {
		req := FreightRequest{Origin: Origin{Kind: "Warehouse", Name: "shop"}, Stages: upstream, Direct: len(upstream) == 0}
		return Stage{Name: name, Requests: []FreightRequest{req}}
	}
	got := Pipeline([]Stage{stage("prod", "uat-east", "uat-west"), stage("uat-west", "dev"), stage("dev"), stage("uat-east", "dev"), stage("hotfix", "missing")})
	var names []string
	var depths []int
	for _, p := range got {
		names, depths = append(names, p.Name), append(depths, p.Depth)
	}
	if !slices.Equal(names, []string{"dev", "hotfix", "uat-east", "uat-west", "prod"}) || !slices.Equal(depths, []int{0, 0, 1, 1, 2}) {
		t.Fatalf("pipeline = %v %v", names, depths)
	}
	if !slices.Equal(got[1].MissingUpstream, []string{"missing"}) || !slices.Equal(got[0].Warehouses, []string{"shop"}) {
		t.Fatalf("pipeline = %+v", got)
	}
	cycle := Pipeline([]Stage{stage("a", "b"), stage("b", "a")})
	if len(cycle) != 2 || cycle[0].Depth == cycle[1].Depth {
		t.Fatalf("a cycle is cut, not looped: %+v", cycle)
	}
}

func TestArgoCDAppsRejectsMalformedAnnotation(t *testing.T) {
	u := parse(t, stageDoc)
	u.SetAnnotations(map[string]string{ArgoCDContextAnnotation: "{"})
	if apps, err := ArgoCDApps(u); err == nil || apps != nil {
		t.Fatalf("apps = %v, err = %v", apps, err)
	}
	u.SetAnnotations(nil)
	if apps, err := ArgoCDApps(u); err != nil || apps != nil {
		t.Fatalf("apps = %v, err = %v", apps, err)
	}
}

func TestAuthorizedStages(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    []StageID
		wantErr bool
	}{
		{"shop:uat", []StageID{{"shop", "uat"}}, false},
		{" shop : uat , shop:prod,", []StageID{{"shop", "uat"}, {"shop", "prod"}}, false},
		{"shop:*", nil, true},
		{"uat", nil, true},
		{"", nil, true},
	} {
		got, err := AuthorizedStages(tc.in)
		if (err != nil) != tc.wantErr || !slices.Equal(got, tc.want) {
			t.Errorf("%q: got %v, %v", tc.in, got, err)
		}
	}
}

// Kargo writes these Ready reasons while work is under way; React shows
// them as neutral progress, and so does Phase.
func TestPhaseWaitingIsNotFailed(t *testing.T) {
	for reason, want := range map[string]string{
		"Progressing": "Progressing", "WaitingForHealthCheck": "Progressing", "ActivePromotion": "Promoting",
		"WaitingForVerification": "Verifying", "PendingVerification": "Verifying", "VerificationPending": "Verifying", "VerificationRunning": "Verifying",
		"LastPromotionAborted": "Aborted", "VerificationAborted": "Aborted", "LastPromotionErrored": "Failed", "Unhealthy": "Failed",
	} {
		u := parse(t, stageDoc)
		set(t, u, []any{
			map[string]any{"type": "Ready", "status": "False", "reason": reason},
			map[string]any{"type": "Verified", "status": "True", "reason": "Verified"},
		}, "status", "conditions")
		if got := Phase(u); got != want {
			t.Errorf("%s: phase %s, want %s", reason, got, want)
		}
	}
}

func TestHeldForReadsTheOrigin(t *testing.T) {
	u := parse(t, stageDoc)
	set(t, u, map[string]any{"Warehouse/shop": map[string]any{"freightName": "x"}}, "status", "effectiveAutoPromotionHolds")
	s := ParseStage(u)
	if !s.Held || !s.HeldFor(Origin{Kind: "Warehouse", Name: "shop"}) || s.HeldFor(Origin{Kind: "Warehouse", Name: "infra"}) {
		t.Fatalf("holds = %+v", s.HeldOrigins)
	}
}

// regular_stages.go summarizeConditions: on a reconcile error Kargo writes
// Ready=False/ReconcileError with Reconciling=True/RetryAfterError, and copies
// an unknown health ("Unknown") onto Ready. The first is a failure, the
// second unknown, as resource-utils-kargo.ts reads them.
func TestPhaseReconcileErrorAndUnknownHealth(t *testing.T) {
	cond := func(typ, status, reason string) any {
		return map[string]any{"type": typ, "status": status, "reason": reason}
	}
	for _, tc := range []struct {
		name  string
		conds []any
		want  string
	}{
		{"reconcile error while retrying", []any{cond("Ready", "False", "ReconcileError"), cond("Reconciling", "True", "RetryAfterError")}, "Failed"},
		{"ordinary reconcile", []any{cond("Ready", "True", "Verified"), cond("Reconciling", "True", "Reconciling")}, "Reconciling"},
		{"reconciling while waiting", []any{cond("Ready", "False", "WaitingForHealthCheck"), cond("Reconciling", "True", "Reconciling")}, "Reconciling"},
		{"unknown health", []any{cond("Ready", "False", "Unknown"), cond("Healthy", "Unknown", "Unknown"), cond("Verified", "True", "Verified")}, "Unknown"},
	} {
		u := parse(t, stageDoc)
		set(t, u, tc.conds, "status", "conditions")
		if got := Phase(u); got != tc.want {
			t.Errorf("%s: phase %s, want %s", tc.name, got, tc.want)
		}
	}
}
