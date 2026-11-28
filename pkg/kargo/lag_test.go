package kargo

import (
	"slices"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var (
	shop = Origin{Kind: "Warehouse", Name: "shop"}
	now  = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
)

// lagFreight is a Freight from shop discovered at hour:00, with the given
// status per stage.
func lagFreight(t *testing.T, name string, hour int, status map[string]any) *unstructured.Unstructured {
	t.Helper()
	u := parse(t, `
apiVersion: kargo.akuity.io/v1alpha1
kind: Freight
metadata: {name: x, namespace: shop}
origin: {kind: Warehouse, name: shop}
images: [{repoURL: nginx, tag: "1"}]
`)
	u.SetName(name)
	set(t, u, time.Date(2026, 11, 25, hour, 0, 0, 0, time.UTC).Format(time.RFC3339), "discoveredAt")
	if status != nil {
		set(t, u, status, "status")
	}
	return u
}

func holding(name string, freight string, upstream ...string) Stage {
	s := Stage{Name: name, Namespace: "shop", AutoPromotion: true, Requests: []FreightRequest{{Origin: shop, Stages: upstream, Direct: len(upstream) == 0}}}
	if freight != "" {
		s.Current = []FreightRef{{Name: freight, Origin: shop}}
	}
	return s
}

func verified(stages ...string) map[string]any {
	in := map[string]any{}
	for _, s := range stages {
		in[s] = map[string]any{"verifiedAt": "2026-11-25T09:00:00Z"}
	}
	return map[string]any{"verifiedIn": in}
}

func TestWhyNotHere(t *testing.T) {
	old := func(t *testing.T) *unstructured.Unstructured { return lagFreight(t, "old", 8, nil) }
	for _, tc := range []struct {
		name        string
		stage       func() Stage
		upstream    []Stage
		freight     func(t *testing.T) []*unstructured.Unstructured
		freightRead bool
		want        Lag
	}{
		{
			name:     "holds the upstream freight",
			stage:    func() Stage { return holding("prod", "new", "uat") },
			upstream: []Stage{holding("uat", "new")},
			want:     Lag{State: LagCurrent, Reason: ReasonUpToDate},
		},
		{
			name:     "upstream went back to older freight",
			stage:    func() Stage { return holding("prod", "new", "uat") },
			upstream: []Stage{holding("uat", "old")},
			freight: func(t *testing.T) []*unstructured.Unstructured {
				return []*unstructured.Unstructured{lagFreight(t, "old", 8, verified("uat")), lagFreight(t, "new", 10, nil)}
			},
			freightRead: true,
			want:        Lag{State: LagUnknown, Reason: ReasonUnknown, Cause: CauseNotPromotedYet},
		},
		{
			name:     "nothing upstream",
			stage:    func() Stage { return holding("prod", "", "uat") },
			upstream: []Stage{holding("uat", "")},
			want:     Lag{State: LagCurrent, Reason: ReasonNothingUpstream},
		},
		{
			name:     "not verified in any of several upstream stages",
			stage:    func() Stage { return holding("prod", "old", "uat-east", "uat-west") },
			upstream: []Stage{holding("uat-east", "new"), holding("uat-west", "new")},
			freight: func(t *testing.T) []*unstructured.Unstructured {
				return []*unstructured.Unstructured{old(t), lagFreight(t, "new", 10, nil)}
			},
			freightRead: true,
			want:        Lag{State: LagBehind, Reason: ReasonNotVerified, Stages: []string{"uat-east", "uat-west"}},
		},
		{
			name: "all upstream stages required, one missing",
			stage: func() Stage {
				s := holding("prod", "old", "uat-east", "uat-west")
				s.Requests[0].All = true
				return s
			},
			upstream: []Stage{holding("uat-east", "new"), holding("uat-west", "new")},
			freight: func(t *testing.T) []*unstructured.Unstructured {
				return []*unstructured.Unstructured{old(t), lagFreight(t, "new", 10, verified("uat-east"))}
			},
			freightRead: true,
			want:        Lag{State: LagBehind, Reason: ReasonNotVerified, Stages: []string{"uat-west"}},
		},
		{
			name: "one upstream stage is enough without All",
			stage: func() Stage {
				s := holding("prod", "old", "uat-east", "uat-west")
				s.AutoPromotion = false
				return s
			},
			upstream: []Stage{holding("uat-east", "new"), holding("uat-west", "new")},
			freight: func(t *testing.T) []*unstructured.Unstructured {
				return []*unstructured.Unstructured{old(t), lagFreight(t, "new", 10, verified("uat-east"))}
			},
			freightRead: true,
			want:        Lag{State: LagBehind, Reason: ReasonAutoPromotionOff},
		},
		{
			name: "soaking: the shortest wait counts without All",
			stage: func() Stage {
				s := holding("prod", "old", "uat-east", "uat-west")
				s.Requests[0].RequiredSoak = 2 * time.Hour
				return s
			},
			upstream: []Stage{holding("uat-east", "new"), holding("uat-west", "new")},
			freight: func(t *testing.T) []*unstructured.Unstructured {
				return []*unstructured.Unstructured{old(t), lagFreight(t, "new", 10, map[string]any{
					"verifiedIn":  map[string]any{"uat-east": map[string]any{}, "uat-west": map[string]any{"longestSoak": "90m"}},
					"currentlyIn": map[string]any{"uat-east": map[string]any{"since": "2026-11-25T11:30:00Z"}},
				})}
			},
			freightRead: true,
			want:        Lag{State: LagBehind, Reason: ReasonSoaking, Stages: []string{"uat-east", "uat-west"}, SoakLeft: 30 * time.Minute},
		},
		{
			name: "soaking: the longest wait counts with All",
			stage: func() Stage {
				s := holding("prod", "old", "uat-east", "uat-west")
				s.Requests[0].RequiredSoak, s.Requests[0].All = 2*time.Hour, true
				return s
			},
			upstream: []Stage{holding("uat-east", "new"), holding("uat-west", "new")},
			freight: func(t *testing.T) []*unstructured.Unstructured {
				return []*unstructured.Unstructured{old(t), lagFreight(t, "new", 10, map[string]any{
					"verifiedIn":  map[string]any{"uat-east": map[string]any{}, "uat-west": map[string]any{"longestSoak": "90m"}},
					"currentlyIn": map[string]any{"uat-east": map[string]any{"since": "2026-11-25T11:30:00Z"}},
				})}
			},
			freightRead: true,
			want:        Lag{State: LagBehind, Reason: ReasonSoaking, Stages: []string{"uat-east", "uat-west"}, SoakLeft: 90 * time.Minute},
		},
		{
			name: "approved for the stage skips upstream checks",
			stage: func() Stage {
				s := holding("prod", "old", "uat")
				s.AutoPromotion = false
				return s
			},
			upstream: []Stage{holding("uat", "new")},
			freight: func(t *testing.T) []*unstructured.Unstructured {
				return []*unstructured.Unstructured{old(t), lagFreight(t, "new", 10, map[string]any{"approvedFor": map[string]any{"prod": map[string]any{}}})}
			},
			freightRead: true,
			want:        Lag{State: LagBehind, Reason: ReasonAutoPromotionOff},
		},
		{
			name:  "direct source offers the warehouse's newest",
			stage: func() Stage { return holding("dev", "old") },
			freight: func(t *testing.T) []*unstructured.Unstructured {
				return []*unstructured.Unstructured{old(t), lagFreight(t, "new", 10, nil)}
			},
			freightRead: true,
			want:        Lag{State: LagUnknown, Reason: ReasonUnknown, Cause: CauseNotPromotedYet},
		},
		{
			name: "last promotion of the newest freight failed",
			stage: func() Stage {
				s := holding("prod", "old", "uat")
				s.LastPromotion = &PromotionRecord{Name: "prod.1", Phase: PhaseErrored, Message: "boom", Freight: &FreightRef{Name: "new"}}
				return s
			},
			upstream: []Stage{holding("uat", "new")},
			want:     Lag{State: LagBehind, Reason: ReasonPromotionFailed, Promotion: "prod.1", Failure: &Failure{Failed: true, Step: -1, Message: "boom"}},
		},
		{
			name: "a failed promotion of other freight is no answer",
			stage: func() Stage {
				s := holding("prod", "old", "uat")
				s.LastPromotion = &PromotionRecord{Name: "prod.0", Phase: PhaseFailed, Freight: &FreightRef{Name: "old"}}
				return s
			},
			upstream: []Stage{holding("uat", "new")},
			want:     Lag{State: LagUnknown, Reason: ReasonUnknown, Cause: CauseFreightUnreadable},
		},
		{
			name: "an aborted last promotion is no answer",
			stage: func() Stage {
				s := holding("prod", "old", "uat")
				s.LastPromotion = &PromotionRecord{Name: "prod.1", Phase: PhaseAborted, Freight: &FreightRef{Name: "new"}}
				return s
			},
			upstream: []Stage{holding("uat", "new")},
			freight: func(t *testing.T) []*unstructured.Unstructured {
				return []*unstructured.Unstructured{old(t), lagFreight(t, "new", 10, nil)}
			},
			freightRead: true,
			want:        Lag{State: LagBehind, Reason: ReasonNotVerified, Stages: []string{"uat"}},
		},
		{
			name: "a running promotion",
			stage: func() Stage {
				s := holding("prod", "old", "uat")
				s.CurrentPromotion = &PromotionRecord{Name: "prod.2"}
				return s
			},
			upstream: []Stage{holding("uat", "new")},
			want:     Lag{State: LagUnknown, Reason: ReasonUnknown, Cause: CausePromotionRunning, Promotion: "prod.2"},
		},
		{
			name:     "freight unreadable",
			stage:    func() Stage { return holding("prod", "old", "uat") },
			upstream: []Stage{holding("uat", "new")},
			want:     Lag{State: LagUnknown, Reason: ReasonUnknown, Cause: CauseFreightUnreadable},
		},
		{
			name:        "freight object missing",
			stage:       func() Stage { return holding("prod", "", "uat") },
			upstream:    []Stage{holding("uat", "new")},
			freightRead: true,
			want:        Lag{State: LagUnknown, Reason: ReasonUnknown, Cause: CauseFreightMissing},
		},
		{
			name:  "direct source with freight unreadable",
			stage: func() Stage { return holding("dev", "old") },
			want:  Lag{State: LagUnknown, Reason: ReasonUnknown, Cause: CauseFreightUnreadable},
		},
		{
			name:  "upstream stage unread",
			stage: func() Stage { return holding("prod", "old", "uat") },
			want:  Lag{State: LagUnknown, Reason: ReasonUnknown, Cause: CauseUpstreamUnreadable},
		},
		{
			name: "auto-promotion held",
			stage: func() Stage {
				s := holding("prod", "old", "uat")
				s.Held, s.HeldOrigins = true, map[string]bool{"Warehouse/shop": true}
				return s
			},
			upstream: []Stage{holding("uat", "new")},
			freight: func(t *testing.T) []*unstructured.Unstructured {
				return []*unstructured.Unstructured{lagFreight(t, "new", 10, verified("uat"))}
			},
			freightRead: true,
			want:        Lag{State: LagUnknown, Reason: ReasonUnknown, Cause: CauseAutoPromotionHeld},
		},
		{
			name: "match upstream policy",
			stage: func() Stage {
				s := holding("prod", "", "uat")
				s.Requests[0].SelectionPolicy = SelectionMatchUpstream
				return s
			},
			upstream: []Stage{holding("uat", "new")},
			freight: func(t *testing.T) []*unstructured.Unstructured {
				return []*unstructured.Unstructured{lagFreight(t, "new", 10, verified("uat"))}
			},
			freightRead: true,
			want:        Lag{State: LagUnknown, Reason: ReasonUnknown, Cause: CauseMatchUpstream},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stage := tc.stage()
			var freight []*unstructured.Unstructured
			if tc.freight != nil {
				freight = tc.freight(t)
			}
			got := WhyNotHere(stage, NewProject(append([]Stage{stage}, tc.upstream...), freight, tc.freightRead), now)
			if len(got) != 1 {
				t.Fatalf("lags = %+v", got)
			}
			g := got[0]
			if g.Message == "" {
				t.Errorf("every answer says it in words: %+v", g)
			}
			if g.State != tc.want.State || g.Reason != tc.want.Reason || g.Cause != tc.want.Cause || !slices.Equal(g.Stages, tc.want.Stages) ||
				g.SoakLeft != tc.want.SoakLeft || g.Promotion != tc.want.Promotion || (g.Failure == nil) != (tc.want.Failure == nil) ||
				(g.Failure != nil && *g.Failure != *tc.want.Failure) {
				t.Fatalf("lag = %+v\nwant %+v", g, tc.want)
			}
		})
	}
}

func TestWhyNotHereAnswersEachOrigin(t *testing.T) {
	infra := Origin{Kind: "Warehouse", Name: "infra"}
	stage := holding("prod", "new", "uat")
	stage.Requests = append(stage.Requests, FreightRequest{Origin: infra, Stages: []string{"uat"}})
	up := holding("uat", "new")
	up.Current = append(up.Current, FreightRef{Name: "redis", Origin: infra})
	got := WhyNotHere(stage, NewProject([]Stage{stage, up}, nil, false), now)
	if len(got) != 2 || got[0].State != LagCurrent || got[1].Origin != infra || got[1].State != LagUnknown || got[1].Newest.Name != "redis" || got[1].Current != nil {
		t.Fatalf("lags = %+v", got)
	}
}

func TestShortDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{30 * time.Second: "less than a minute", 90 * time.Minute: "1h30m", 2 * time.Hour: "2h", 5 * time.Minute: "5m"} {
		if got := shortDuration(d); got != want {
			t.Errorf("%v: %q, want %q", d, got, want)
		}
	}
}

// Review findings, each against Kargo v1.12.7 behavior.
func TestWhyNotHereFollowsKargoCandidateSelection(t *testing.T) {
	soakedInUAT := map[string]any{"verifiedIn": map[string]any{"uat": map[string]any{"longestSoak": "2h"}}}

	// uat verified B, then went back to A. B stays available to prod
	// (pkg/api/stage.go ListFreightAvailableToStage reads verifiedIn), and
	// NewestFreight picks it (pkg/api/auto_promotion.go).
	prod := holding("prod", "a", "uat")
	prod.Requests[0].RequiredSoak = time.Hour
	project := NewProject([]Stage{prod, holding("uat", "a")},
		[]*unstructured.Unstructured{lagFreight(t, "a", 8, soakedInUAT), lagFreight(t, "b", 10, soakedInUAT)}, true)
	if got := WhyNotHere(prod, project, now)[0]; got.Newest == nil || got.Newest.Name != "b" || got.State == LagCurrent {
		t.Errorf("historically verified Freight: %+v", got)
	}

	// Freight approved for prod is available too.
	approved := map[string]any{"approvedFor": map[string]any{"prod": map[string]any{}}}
	project = NewProject([]Stage{prod, holding("uat", "a")},
		[]*unstructured.Unstructured{lagFreight(t, "a", 8, soakedInUAT), lagFreight(t, "c", 11, approved)}, true)
	if got := WhyNotHere(prod, project, now)[0]; got.Newest == nil || got.Newest.Name != "c" {
		t.Errorf("approved Freight: %+v", got)
	}

	// A hold on one origin doesn't stop another (regular_stages.go checks
	// EffectiveAutoPromotionHolds[origin]).
	web := Origin{Kind: "Warehouse", Name: "web"}
	two := holding("dev", "")
	two.Requests = append(two.Requests, FreightRequest{Origin: web, Direct: true})
	two.HeldOrigins = map[string]bool{"Warehouse/shop": true}
	webFreight := lagFreight(t, "w1", 10, nil)
	set(t, webFreight, map[string]any{"kind": "Warehouse", "name": "web"}, "origin")
	lags := WhyNotHere(two, NewProject([]Stage{two}, []*unstructured.Unstructured{lagFreight(t, "s1", 10, nil), webFreight}, true), now)
	if lags[0].Cause != CauseAutoPromotionHeld || lags[1].Cause == CauseAutoPromotionHeld {
		t.Errorf("holds per origin: %+v", lags)
	}

	// A running retry is the current explanation, not the older failure.
	retry := holding("prod", "old", "uat")
	retry.LastPromotion = &PromotionRecord{Name: "prod.1", Phase: PhaseFailed, Freight: &FreightRef{Name: "new"}}
	retry.CurrentPromotion = &PromotionRecord{Name: "prod.2"}
	if got := WhyNotHere(retry, NewProject([]Stage{retry, holding("uat", "new")}, nil, false), now)[0]; got.Cause != CausePromotionRunning {
		t.Errorf("running retry: %+v", got)
	}
}

func TestWhyNotHereUpstreamVerification(t *testing.T) {
	failed := Verification{Phase: "Failed", Message: `Metric "error-rate" assessed Failed due to failed (1) > failureLimit (0)`, AnalysisRun: &ObjectRef{Namespace: "shop", Name: "uat.1"}}
	errored := Verification{Phase: "Error", Message: `error getting AnalysisRun "uat.1" in namespace "shop": not found`}
	upstream := func(name string, v *Verification) Stage {
		s := holding(name, "new")
		if v != nil {
			s.Verifications = map[string]Verification{"new": *v}
		}
		return s
	}
	for _, tc := range []struct {
		name     string
		all      bool
		upstream []Stage
		reason   LagReason
		message  string
		runs     int
	}{
		{
			name:     "failed, naming the metric",
			upstream: []Stage{upstream("uat", &failed)},
			reason:   ReasonVerificationFailed,
			message:  "Failed verification in uat (error-rate). It can't reach prod until it passes there.",
			runs:     1,
		},
		{
			name:     "errored without a metric",
			upstream: []Stage{upstream("uat", &errored)},
			reason:   ReasonVerificationFailed,
			message:  "Failed verification in uat. It can't reach prod until it passes there.",
		},
		{
			name:     "running",
			upstream: []Stage{upstream("uat", &Verification{Phase: "Running"})},
			reason:   ReasonVerifying,
			message:  "Verifying in uat.",
		},
		{
			name:     "pending counts as running",
			upstream: []Stage{upstream("uat", &Verification{Phase: "Pending"})},
			reason:   ReasonVerifying,
			message:  "Verifying in uat.",
		},
		{
			name:     "not started",
			upstream: []Stage{upstream("uat", nil)},
			reason:   ReasonNotVerified,
			message:  "Waiting for uat to verify it.",
		},
		{
			name: "a failure on older freight is not this freight's",
			upstream: []Stage{func() Stage {
				s := upstream("uat", nil)
				s.Verifications = map[string]Verification{"old": failed}
				return s
			}()},
			reason:  ReasonNotVerified,
			message: "Waiting for uat to verify it.",
		},
		{
			name:     "one failed, another still verifying: one is enough, so it isn't stuck",
			upstream: []Stage{upstream("uat-east", &failed), upstream("uat-west", &Verification{Phase: "Running"})},
			reason:   ReasonVerifying,
			message:  "Failed verification in uat-east (error-rate). Verifying in uat-west.",
			runs:     1,
		},
		{
			name:     "failed in every upstream stage it could pass",
			upstream: []Stage{upstream("uat-east", &failed), upstream("uat-west", &errored)},
			reason:   ReasonVerificationFailed,
			message:  "Failed verification in uat-east (error-rate) and uat-west. It can't reach prod until it passes in one of them.",
			runs:     1,
		},
		{
			name:     "with All, one failure blocks",
			all:      true,
			upstream: []Stage{upstream("uat-east", &failed), upstream("uat-west", nil)},
			reason:   ReasonVerificationFailed,
			message:  "Failed verification in uat-east (error-rate). It can't reach prod until it passes there. Waiting for uat-west to verify it.",
			runs:     1,
		},
		{
			name:     "waiting for several",
			upstream: []Stage{upstream("uat-east", nil), upstream("uat-west", nil)},
			reason:   ReasonNotVerified,
			message:  "Waiting for uat-east or uat-west to verify it.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			names := make([]string, len(tc.upstream))
			for i, s := range tc.upstream {
				names[i] = s.Name
			}
			prod := holding("prod", "old", names...)
			prod.Requests[0].All = tc.all
			freight := []*unstructured.Unstructured{lagFreight(t, "old", 8, nil), lagFreight(t, "new", 10, nil)}
			got := WhyNotHere(prod, NewProject(append([]Stage{prod}, tc.upstream...), freight, true), now)[0]
			if got.State != LagBehind || got.Reason != tc.reason || got.Message != tc.message || len(got.AnalysisRuns) != tc.runs {
				t.Fatalf("lag = %+v\nwant %s %q with %d runs", got, tc.reason, tc.message, tc.runs)
			}
			if strings.Contains(got.Message, "pprov") {
				t.Fatalf("never suggests approving: %q", got.Message)
			}
		})
	}
}

func TestStageVerificationsByFreight(t *testing.T) {
	u := parse(t, `
apiVersion: kargo.akuity.io/v1alpha1
kind: Stage
metadata: {name: uat, namespace: shop}
status:
  freightHistory:
  - items: {Warehouse/shop: {name: c}}
  - items: {Warehouse/shop: {name: b}}
    verificationHistory:
    - {id: "2", phase: Running}
    - {id: "1", phase: Failed}
  - items: {Warehouse/shop: {name: c}}
    verificationHistory:
    - {id: "0", phase: Failed}
  - items: {Warehouse/shop: {name: a}}
    verificationHistory:
    - {id: "x", phase: Failed, message: 'Metric "latency" assessed Failed due to x', analysisRun: {name: uat.a}}
`)
	got := ParseStage(u).Verifications
	if _, ok := got["c"]; ok {
		t.Errorf("c's newest collection has no verification yet: %+v", got["c"])
	}
	if got["b"].Phase != "Running" {
		t.Errorf("b = %+v, want its latest verification", got["b"])
	}
	if a := got["a"]; !a.Failed() || a.AnalysisRun == nil || !slices.Equal(a.Metrics(), []string{"latency"}) {
		t.Errorf("a = %+v", a)
	}
}
