package audit

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func gitopsWorkload(name, ns string, labels, annos map[string]string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels, Annotations: annos},
	}
}

// TestCheckGitOpsCoverage_Classification pins each management tier to its
// finding: Flux/Argo signals pass, native-Helm is helm-only, generic app labels
// (or no labels) are unmanaged. Every eligible subject is recorded as evaluated
// against BOTH checks regardless of outcome.
func TestCheckGitOpsCoverage_Classification(t *testing.T) {
	cases := []struct {
		name      string
		labels    map[string]string
		annos     map[string]string
		wantCheck string // "" = GitOps-managed, no finding
	}{
		{
			name:  "argo tracking-id",
			annos: map[string]string{"argocd.argoproj.io/tracking-id": "guestbook:apps/Deployment:prod/web"},
		},
		{
			name:   "flux kustomize labels",
			labels: map[string]string{"kustomize.toolkit.fluxcd.io/name": "apps", "kustomize.toolkit.fluxcd.io/namespace": "flux-system"},
		},
		{
			name:      "native helm only",
			annos:     map[string]string{"meta.helm.sh/release-name": "web", "meta.helm.sh/release-namespace": "prod"},
			wantCheck: checkGitOpsHelmOnly,
		},
		{
			name:      "bare app label",
			labels:    map[string]string{"app": "web"},
			wantCheck: checkGitOpsUnmanaged,
		},
		{
			name:      "app.kubernetes.io/name label",
			labels:    map[string]string{"app.kubernetes.io/name": "web"},
			wantCheck: checkGitOpsUnmanaged,
		},
		{
			name:      "no labels",
			wantCheck: checkGitOpsUnmanaged,
		},
		{
			// Argo label tracking mode (application.resourceTrackingMethod:
			// label) stamps ONLY app.kubernetes.io/instance; it counts as
			// GitOps-managed when the value names a real Application.
			name:   "argo label tracking, app exists",
			labels: map[string]string{"app.kubernetes.io/instance": "guestbook"},
		},
		{
			name:      "instance label, no matching app",
			labels:    map[string]string{"app.kubernetes.io/instance": "not-an-app"},
			wantCheck: checkGitOpsUnmanaged,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := &CheckInput{
				GitOpsToolsPresent: true,
				ArgoAppNames:       map[string]struct{}{"guestbook": {}},
				Deployments:        []*appsv1.Deployment{gitopsWorkload("web", "prod", tc.labels, tc.annos)},
			}
			tr := newEvalTracker()
			findings := checkGitOpsCoverage(tr, input)

			if tr.counts[checkGitOpsUnmanaged]["prod"] != 1 || tr.counts[checkGitOpsHelmOnly]["prod"] != 1 {
				t.Fatalf("expected each coverage check evaluated once in prod, got unmanaged=%d helmOnly=%d",
					tr.counts[checkGitOpsUnmanaged]["prod"], tr.counts[checkGitOpsHelmOnly]["prod"])
			}

			if tc.wantCheck == "" {
				if len(findings) != 0 {
					t.Fatalf("GitOps-managed workload should produce no finding, got %+v", findings)
				}
				return
			}
			if len(findings) != 1 {
				t.Fatalf("expected 1 finding, got %d: %+v", len(findings), findings)
			}
			f := findings[0]
			if f.CheckID != tc.wantCheck {
				t.Errorf("CheckID = %q, want %q", f.CheckID, tc.wantCheck)
			}
			if f.Severity != SeverityWarning {
				t.Errorf("Severity = %q, want %q (posture, not breakage)", f.Severity, SeverityWarning)
			}
			if f.Category != CategoryReliability {
				t.Errorf("Category = %q, want %q", f.Category, CategoryReliability)
			}
		})
	}
}

// TestCheckGitOpsCoverage_GateClosed pins the gate: no GitOps controller in the
// cluster means the check emits nothing AND records nothing as evaluated — so a
// non-GitOps shop never sees every workload flagged.
func TestCheckGitOpsCoverage_GateClosed(t *testing.T) {
	input := &CheckInput{
		GitOpsToolsPresent: false,
		Deployments:        []*appsv1.Deployment{gitopsWorkload("web", "prod", nil, nil)},
	}
	tr := newEvalTracker()
	findings := checkGitOpsCoverage(tr, input)
	if len(findings) != 0 {
		t.Errorf("gate closed: expected no findings, got %+v", findings)
	}
	if len(tr.counts) != 0 {
		t.Errorf("gate closed: expected nothing recorded as evaluated, got %+v", tr.counts)
	}
}

// TestCheckGitOpsCoverage_SkipsOwnedWorkloads pins the ownerReference skip:
// controller-created workloads don't carry the owner's GitOps labels, so the
// coverage question belongs to the owner — an owned workload is out of scope,
// neither flagged nor counted.
func TestCheckGitOpsCoverage_SkipsOwnedWorkloads(t *testing.T) {
	owned := gitopsWorkload("web", "prod", nil, nil)
	owned.OwnerReferences = []metav1.OwnerReference{{Kind: "FooController", Name: "owner"}}
	input := &CheckInput{
		GitOpsToolsPresent: true,
		Deployments:        []*appsv1.Deployment{owned},
	}
	tr := newEvalTracker()
	findings := checkGitOpsCoverage(tr, input)
	if len(findings) != 0 {
		t.Errorf("owned workload should be skipped, got %+v", findings)
	}
	if len(tr.counts) != 0 {
		t.Errorf("owned workload must not be recorded as evaluated, got %+v", tr.counts)
	}
}

// TestCheckGitOpsCoverage_CronJobSubject pins CronJob as an in-scope top-level
// workload (not only Deployments/StatefulSets/DaemonSets).
func TestCheckGitOpsCoverage_CronJobSubject(t *testing.T) {
	cj := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: "prod"}}
	input := &CheckInput{GitOpsToolsPresent: true, CronJobs: []*batchv1.CronJob{cj}}
	tr := newEvalTracker()
	findings := checkGitOpsCoverage(tr, input)
	if len(findings) != 1 || findings[0].CheckID != checkGitOpsUnmanaged || findings[0].Kind != "CronJob" {
		t.Fatalf("expected 1 unmanaged CronJob finding, got %+v", findings)
	}
}

// TestCheckGitOpsCoverage_CountsRollup runs the full pipeline and pins the
// evaluated/passed arithmetic: a GitOps-managed, a helm-only, and an unmanaged
// workload → each check evaluated 3, failing its own one.
func TestCheckGitOpsCoverage_CountsRollup(t *testing.T) {
	input := &CheckInput{
		GitOpsToolsPresent: true,
		Deployments: []*appsv1.Deployment{
			gitopsWorkload("managed", "prod", map[string]string{
				"kustomize.toolkit.fluxcd.io/name":      "apps",
				"kustomize.toolkit.fluxcd.io/namespace": "flux-system",
			}, nil),
			gitopsWorkload("helmish", "prod", nil, map[string]string{"meta.helm.sh/release-name": "helmish"}),
			gitopsWorkload("bare", "prod", nil, nil),
		},
	}
	results := RunChecks(input)

	if got := results.CheckCounts[checkGitOpsUnmanaged]; got != (CheckCount{Evaluated: 3, Passed: 2}) {
		t.Errorf("%s counts = %+v, want {Evaluated:3 Passed:2}", checkGitOpsUnmanaged, got)
	}
	if got := results.CheckCounts[checkGitOpsHelmOnly]; got != (CheckCount{Evaluated: 3, Passed: 2}) {
		t.Errorf("%s counts = %+v, want {Evaluated:3 Passed:2}", checkGitOpsHelmOnly, got)
	}
	for _, id := range []string{checkGitOpsUnmanaged, checkGitOpsHelmOnly} {
		if _, ok := CheckRegistry[id]; !ok {
			t.Errorf("%s missing from CheckRegistry", id)
		}
	}
}

// TestCheckGitOpsCoverage_GateClosedIntegration pins that a closed gate keeps
// the two checks out of CheckCounts entirely — and out of MissingInputs (the
// gate is "not applicable here", not "couldn't list").
func TestCheckGitOpsCoverage_GateClosedIntegration(t *testing.T) {
	input := &CheckInput{
		Deployments: []*appsv1.Deployment{gitopsWorkload("bare", "prod", nil, nil)},
	}
	results := RunChecks(input)
	for _, id := range []string{checkGitOpsUnmanaged, checkGitOpsHelmOnly} {
		if _, ok := results.CheckCounts[id]; ok {
			t.Errorf("%s must be absent from CheckCounts when GitOpsToolsPresent=false", id)
		}
	}
	for _, in := range results.MissingInputs {
		if in == checkGitOpsUnmanaged || in == checkGitOpsHelmOnly {
			t.Errorf("gate-closed coverage check must not appear in MissingInputs: %v", results.MissingInputs)
		}
	}
}
