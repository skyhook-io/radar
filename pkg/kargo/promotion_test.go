package kargo

import (
	"testing"
)

const promotionDoc = `
apiVersion: kargo.akuity.io/v1alpha1
kind: Promotion
metadata: {name: uat.01promo, namespace: shop}
spec:
  stage: uat
  freight: 3f2a9c1d0e
  steps:
  - {uses: git-clone, as: clone}
  - {uses: http, as: notify, continueOnError: true}
  - {uses: git-push, as: push}
  - {task: {name: deploy}, as: deploy}
status:
  phase: Failed
  message: "step push failed"
  freight: {name: 3f2a9c1d0e, origin: {kind: Warehouse, name: shop}, images: [{repoURL: nginx, tag: 1.31.9}]}
  stepExecutionMetadata:
  - {alias: clone, status: Succeeded}
  - {alias: notify, status: Errored, message: "notify timed out", continueOnError: true}
  - {alias: push, status: Failed, message: "remote rejected", errorCount: 1}
`

func TestParsePromotionAlignsStepsWithResults(t *testing.T) {
	p := ParsePromotion(parse(t, promotionDoc))
	if p.Stage != "uat" || p.FreightName != "3f2a9c1d0e" || p.Freight == nil || p.Freight.Labels()[0] != "nginx:1.31.9" {
		t.Fatalf("promotion = %+v", p)
	}
	if len(p.Steps) != 4 || p.Steps[2].Uses != "git-push" || p.Steps[2].Status != PhaseFailed || p.Steps[2].ErrorCount != 1 {
		t.Fatalf("steps = %+v", p.Steps)
	}
	if s := p.Steps[3]; s.Task != "deploy" || s.Status != "" || s.Alias != "deploy" {
		t.Fatalf("a step that hasn't run keeps its definition: %+v", s)
	}
}

func TestPromotionFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(p *Promotion)
		want   Failure
	}{
		{"first failed step without continueOnError", func(*Promotion) {}, Failure{Failed: true, Step: 2, StepAlias: "push", Message: "remote rejected"}},
		{"failed step without a message uses the promotion's", func(p *Promotion) { p.Steps[2].Message = "" }, Failure{Failed: true, Step: 2, StepAlias: "push", Message: "step push failed"}},
		{"no failed step uses the promotion's message", func(p *Promotion) { p.Steps[2].Status = PhaseSucceeded }, Failure{Failed: true, Step: -1, Message: "step push failed"}},
		{"nothing known", func(p *Promotion) { p.Steps[2].Status, p.Message = PhaseSucceeded, "" }, Failure{Failed: true, Step: -1}},
		{"errored counts as failed", func(p *Promotion) { p.Phase = PhaseErrored }, Failure{Failed: true, Step: 2, StepAlias: "push", Message: "remote rejected"}},
		{"aborted is not a failure", func(p *Promotion) { p.Phase = PhaseAborted }, Failure{Step: -1}},
		{"succeeded is not a failure", func(p *Promotion) { p.Phase = PhaseSucceeded }, Failure{Step: -1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := ParsePromotion(parse(t, promotionDoc))
			tc.change(&p)
			if got := p.Failure(); got != tc.want {
				t.Fatalf("failure = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestPromotionPhases(t *testing.T) {
	for phase, want := range map[string][2]bool{
		PhasePending: {false, false}, PhaseRunning: {false, false}, PhaseSucceeded: {false, true},
		PhaseFailed: {true, true}, PhaseErrored: {true, true}, PhaseAborted: {false, true}, "": {false, false},
	} {
		if got := [2]bool{PromotionFailed(phase), PromotionTerminal(phase)}; got != want {
			t.Errorf("%q: failed/terminal = %v, want %v", phase, got, want)
		}
	}
}

func TestCause(t *testing.T) {
	const pushErr = `error pushing commits to remote: error pushing branch: error executing cmd [/usr/bin/git push origin HEAD:stage/test]: fatal: could not read Username for 'http://gitea.gitea.svc.cluster.local:3000': No such device or address`
	for in, want := range map[string]string{
		// A live Kargo v1.12.7 Errored promotion, as the Stage records it.
		`step "promo-process::step-6" met error threshold of 1: error running step "promo-process::step-6": ` + pushErr + "\n": pushErr,
		`an unrecoverable error occurred: error running step "clone": error cloning repo: auth failed`:                         "error cloning repo: auth failed",
		`step "failer": failed: deployment blocked by policy`:                                                                  "failed: deployment blocked by policy",
		`step "argocd-update" errored: no details provided`:                                                                    "no details provided",
		// A timeout says why; it stays.
		`step "wait" timed out after 5m0s: app not healthy`: `step "wait" timed out after 5m0s: app not healthy`,
		`change freeze until Monday`:                        `change freeze until Monday`,
		`step "x" met error threshold of: oops`:             `step "x" met error threshold of: oops`,
		`step "x": `:                                        `step "x":`,
	} {
		if got := Cause(in); got != want {
			t.Errorf("Cause(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}
