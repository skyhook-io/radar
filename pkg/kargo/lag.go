package kargo

import (
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// LagState says whether a Stage holds the newest Freight its sources offer.
type LagState string

const (
	LagCurrent LagState = "current"
	LagBehind  LagState = "behind"
	LagUnknown LagState = "unknown"
)

// LagReason is why a Stage is where it is. The Behind reasons are the only
// ones this package asserts; anything else is Unknown with a cause.
type LagReason string

const (
	// ReasonUpToDate: the Stage holds the Freight its sources offer.
	ReasonUpToDate LagReason = "upToDate"
	// ReasonNothingUpstream: no source holds Freight from this origin yet.
	ReasonNothingUpstream LagReason = "nothingUpstream"
	// ReasonNotVerified: the newest Freight isn't verified in the upstream
	// Stages the request needs (one of them, or all with All), and no
	// verification of it there has started or failed: it waits for them.
	ReasonNotVerified LagReason = "notVerifiedUpstream"
	// ReasonVerifying: its verification is running upstream.
	ReasonVerifying LagReason = "verifyingUpstream"
	// ReasonVerificationFailed: its verification failed upstream, in every
	// upstream Stage it could pass (or in one of them, with All).
	ReasonVerificationFailed LagReason = "verificationFailedUpstream"
	// ReasonSoaking: it is verified upstream but hasn't soaked long enough.
	ReasonSoaking LagReason = "soaking"
	// ReasonAutoPromotionOff: it is available, and the Stage waits for a
	// person to promote it.
	ReasonAutoPromotionOff LagReason = "autoPromotionOff"
	// ReasonPromotionFailed: the Stage's last Promotion, of this Freight,
	// failed.
	ReasonPromotionFailed LagReason = "lastPromotionFailed"
	// ReasonUnknown: the objects don't say; Cause says what is missing.
	ReasonUnknown LagReason = "unknown"
)

// UnknownCause is what kept a lag answer unknown.
type UnknownCause string

const (
	CauseFreightUnreadable  UnknownCause = "freightUnreadable"
	CauseFreightMissing     UnknownCause = "freightMissing"
	CauseUpstreamUnreadable UnknownCause = "upstreamUnreadable"
	CausePromotionRunning   UnknownCause = "promotionRunning"
	CauseAutoPromotionHeld  UnknownCause = "autoPromotionHeld"
	CauseMatchUpstream      UnknownCause = "matchUpstream"
	CauseNotPromotedYet     UnknownCause = "notPromotedYet"
	CauseControlFlow        UnknownCause = "controlFlow"
)

// Lag answers, for one origin of a Stage, whether the newest Freight its
// sources offer is there, and if not, why.
type Lag struct {
	Origin Origin
	// Current is the Freight the Stage holds from Origin; nil when none.
	Current *FreightRef
	// Newest is the newest Freight its sources offer: the Freight in its
	// upstream Stages, and for a direct source the Warehouse's newest.
	Newest *FreightRef
	State  LagState
	Reason LagReason
	Cause  UnknownCause
	// Message says it in plain words.
	Message string
	// Stages are the upstream Stages the reason is about: those the Freight
	// isn't verified in, or is soaking in.
	Stages []string
	// AnalysisRuns are the AnalysisRuns of the failed upstream verifications,
	// when Kargo recorded them.
	AnalysisRuns []ObjectRef
	// SoakLeft is how much longer the Freight must soak.
	SoakLeft time.Duration
	// Promotion names the failed or running Promotion; Failure is why the
	// failed one failed.
	Promotion string
	Failure   *Failure
}

// Project is what lag answers read from a Stage's project.
type Project struct {
	stages      map[string]Stage
	freight     FreightIndex
	freightRead bool
}

// NewProject collects a project's Stages and Freight. freightRead is false
// when the caller couldn't read the project's Freight: answers that need a
// Freight's age or status are then unknown, never guessed.
func NewProject(stages []Stage, freight []*unstructured.Unstructured, freightRead bool) Project {
	p := Project{stages: make(map[string]Stage, len(stages)), freight: IndexFreight(freight), freightRead: freightRead}
	for _, s := range stages {
		p.stages[s.Name] = s
	}
	return p
}

// WhyNotHere answers Lag for each origin the Stage requests, in request
// order. now is the time soak durations are measured at.
func WhyNotHere(stage Stage, project Project, now time.Time) []Lag {
	out := make([]Lag, 0, len(stage.Requests))
	for _, req := range stage.Requests {
		out = append(out, lagFor(stage, req, project, now))
	}
	return out
}

func lagFor(stage Stage, req FreightRequest, p Project, now time.Time) Lag {
	lag := Lag{Origin: req.Origin}
	if cur, ok := stage.CurrentFor(req.Origin); ok {
		lag.Current = &cur
	}
	newest, unreadUpstream, cause := p.newestOffered(stage.Name, lag.Current, req, now)
	if cause != "" {
		return lag.unknown(cause, unknownMessage(cause, unreadUpstream))
	}
	if newest == nil {
		if len(unreadUpstream) > 0 {
			return lag.unknown(CauseUpstreamUnreadable, unknownMessage(CauseUpstreamUnreadable, unreadUpstream))
		}
		lag.State, lag.Reason, lag.Message = LagCurrent, ReasonNothingUpstream, "No Freight upstream yet."
		return lag
	}
	lag.Newest = newest
	// Kargo orders Freight by discovery, so a hand-made Freight can be
	// "newer" than the one upstream verified. What a Stage waits for is what
	// its sources offer, so anything else is behind them.
	if lag.Current != nil && lag.Current.Name == newest.Name {
		return lag.upToDate()
	}
	lag.State = LagBehind
	if running := stage.CurrentPromotion; running != nil {
		lag = lag.unknown(CausePromotionRunning, "A promotion to "+stage.Name+" is running.")
		lag.Promotion = running.Name
		return lag
	}
	if last := stage.LastPromotion; last != nil && PromotionFailed(last.Phase) && last.Freight != nil && last.Freight.Name == newest.Name {
		failure := last.Failure()
		lag.Reason, lag.Promotion, lag.Failure = ReasonPromotionFailed, last.Name, &failure
		lag.Message = "The last promotion of this Freight to " + stage.Name + " failed."
		return lag
	}
	if stage.ControlFlow {
		return lag.unknown(CauseControlFlow, stage.Name+" has no promotion steps, so Kargo moves Freight through it on its own.")
	}
	if !p.freightRead {
		return lag.unknown(CauseFreightUnreadable, unknownMessage(CauseFreightUnreadable, nil))
	}
	obj := p.freight.Object(newest.Name)
	if obj == nil {
		return lag.unknown(CauseFreightMissing, "Radar can't find Freight "+newest.Name+".")
	}
	if !StatusIn(obj, stage.Name).Approved && !req.Direct {
		if blocked := upstreamBlock(obj, req, now); blocked != nil {
			lag.Reason, lag.Stages, lag.SoakLeft = blocked.reason, blocked.stages, blocked.soakLeft
			if blocked.reason == ReasonSoaking {
				lag.Message = blocked.soakMessage()
			} else {
				lag.Reason, lag.Message, lag.AnalysisRuns = p.verificationBlock(newest.Name, stage.Name, blocked)
			}
			return lag
		}
	}
	switch {
	case !stage.AutoPromotion:
		lag.Reason = ReasonAutoPromotionOff
		lag.Message = "Ready to promote. Auto-promotion is off for " + stage.Name + ", so it waits for someone to promote it in Kargo."
	case req.SelectionPolicy == SelectionMatchUpstream:
		lag = lag.unknown(CauseMatchUpstream, stage.Name+" only auto-promotes the Freight its upstream Stage holds.")
	case stage.HeldFor(req.Origin):
		lag = lag.unknown(CauseAutoPromotionHeld, "Auto-promotion is paused for "+stage.Name+".")
	default:
		lag = lag.unknown(CauseNotPromotedYet, "It can be promoted and auto-promotion is on, but Kargo hasn't promoted it yet.")
	}
	return lag
}

func (l Lag) upToDate() Lag {
	l.State, l.Reason, l.Message = LagCurrent, ReasonUpToDate, "Up to date."
	return l
}

// unknown keeps Newest and Current, which are facts, and drops any claim
// about why.
func (l Lag) unknown(cause UnknownCause, message string) Lag {
	l.State, l.Reason, l.Cause, l.Message = LagUnknown, ReasonUnknown, cause, message
	return l
}

func unknownMessage(cause UnknownCause, stages []string) string {
	switch cause {
	case CauseFreightUnreadable:
		return "Radar can't read this project's Freight, so it can't compare versions."
	case CauseUpstreamUnreadable:
		return "Radar can't read upstream " + plural(len(stages), "Stage", "Stages") + " " + strings.Join(stages, ", ") + "."
	}
	return ""
}

// newestOffered is the Freight a Stage should hold next from req's origin,
// against current (nil when the Stage holds none). It is the newest Freight
// its upstream Stages hold, the version people see upstream, unless the
// Stage already holds it; then it is the newest Freight Kargo makes
// available to the Stage (verified and soaked upstream, approved for the
// Stage, or anything from a direct Warehouse), the candidates of pkg/api
// ListFreightAvailableToStage, which can be Freight upstream verified and
// moved past. With MatchUpstream only the upstream Stage's Freight counts.
// An upstream Stage that isn't among the project's Stages is returned
// unread; without the Freight objects, several distinct candidates can't be
// ordered.
func (p Project) newestOffered(stage string, current *FreightRef, req FreightRequest, now time.Time) (*FreightRef, []string, UnknownCause) {
	var held, available []FreightRef
	var unread []string
	add := func(list *[]FreightRef, f FreightRef) {
		if !slices.ContainsFunc(*list, func(c FreightRef) bool { return c.Name == f.Name }) {
			*list = append(*list, f)
		}
	}
	for _, name := range req.Stages {
		up, ok := p.stages[name]
		if !ok {
			unread = append(unread, name)
			continue
		}
		if f, ok := up.CurrentFor(req.Origin); ok {
			add(&held, f)
		}
	}
	if req.Direct && !p.freightRead {
		return nil, unread, CauseFreightUnreadable
	}
	if p.freightRead && req.SelectionPolicy != SelectionMatchUpstream {
		for _, f := range p.freight.Newest(req.Origin) {
			obj := p.freight.Object(f.Name)
			if req.Direct || StatusIn(obj, stage).Approved || upstreamBlock(obj, req, now) == nil {
				add(&available, FreightRef{Name: f.Name, Origin: f.Origin, Versions: f.Versions})
			}
		}
	}
	isCurrent := func(f *FreightRef) bool { return f != nil && current != nil && f.Name == current.Name }
	newest, cause := p.newestOf(held)
	if cause != "" {
		return nil, unread, cause
	}
	if newest == nil || isCurrent(newest) {
		next, cause := p.newestOf(available)
		if cause != "" {
			return nil, unread, cause
		}
		if next != nil {
			newest = next
		}
	}
	return newest, unread, ""
}

// newestOf orders candidates by the Freight objects' discovery time.
func (p Project) newestOf(candidates []FreightRef) (*FreightRef, UnknownCause) {
	switch {
	case len(candidates) == 0:
		return nil, ""
	case len(candidates) == 1:
		return &candidates[0], ""
	case !p.freightRead:
		return nil, CauseFreightUnreadable
	}
	best := candidates[0]
	bestF, _ := p.freight.Get(best.Name)
	for _, c := range candidates[1:] {
		f, _ := p.freight.Get(c.Name)
		if newestFirst(f, bestF) < 0 {
			best, bestF = c, f
		}
	}
	return &best, ""
}

type upstreamBlocked struct {
	reason   LagReason
	stages   []string
	soakLeft time.Duration
	all      bool
}

// upstreamBlock applies Kargo's availability rule for Freight that reaches
// a Stage through upstream Stages: verified, and soaked for the required
// time, in one upstream Stage (or every one with All). nil means available.
func upstreamBlock(freight *unstructured.Unstructured, req FreightRequest, now time.Time) *upstreamBlocked {
	if len(req.Stages) == 0 {
		return nil
	}
	var unverified, soaking []string
	var soakLeft []time.Duration
	passed := 0
	for _, up := range req.Stages {
		s := StatusIn(freight, up)
		switch {
		case !s.Verified:
			unverified = append(unverified, up)
		case req.RequiredSoak > 0 && s.SoakedFor(now) < req.RequiredSoak:
			soaking = append(soaking, up)
			soakLeft = append(soakLeft, req.RequiredSoak-s.SoakedFor(now))
		default:
			passed++
		}
	}
	if (req.All && passed == len(req.Stages)) || (!req.All && passed > 0) {
		return nil
	}
	if (req.All && len(unverified) > 0) || len(soaking) == 0 {
		return &upstreamBlocked{reason: ReasonNotVerified, stages: unverified, all: req.All}
	}
	// One upstream Stage is enough without All, so the shortest wait counts;
	// with All, the longest.
	left := slices.Min(soakLeft)
	if req.All {
		left = slices.Max(soakLeft)
	}
	return &upstreamBlocked{reason: ReasonSoaking, stages: soaking, soakLeft: left, all: req.All}
}

func (b upstreamBlocked) soakMessage() string {
	join := " or "
	if b.all {
		join = " and "
	}
	return "Verified in " + strings.Join(b.stages, join) + ", soaking for " + shortDuration(b.soakLeft) + " more."
}

// verificationBlock says, for Freight not verified in the upstream Stages it
// needs, whether each one's verification of it failed, is running or hasn't
// started, from that Stage's freight history. An upstream Stage Radar can't
// read, or whose verification of it was aborted or inconclusive, waits.
// It names no approval: approving bypasses verification, which is a
// hotfix path, not advice.
func (p Project) verificationBlock(freight, stage string, b *upstreamBlocked) (LagReason, string, []ObjectRef) {
	var failed, running, waiting []string
	var runs []ObjectRef
	for _, name := range b.stages {
		v, ok := p.stages[name].Verifications[freight]
		switch {
		case ok && v.Failed():
			label := name
			if metrics := v.Metrics(); len(metrics) > 0 {
				label += " (" + strings.Join(metrics, ", ") + ")"
			}
			failed = append(failed, label)
			if v.AnalysisRun != nil {
				runs = append(runs, *v.AnalysisRun)
			}
		case ok && v.Running():
			running = append(running, name)
		default:
			waiting = append(waiting, name)
		}
	}
	reason := ReasonNotVerified
	switch {
	case len(failed) > 0 && (b.all || len(failed) == len(b.stages)):
		reason = ReasonVerificationFailed
	case len(running) > 0:
		reason = ReasonVerifying
	}
	join := " or "
	if b.all {
		join = " and "
	}
	var parts []string
	if len(failed) > 0 {
		parts = append(parts, "Failed verification in "+strings.Join(failed, " and ")+".")
		if reason == ReasonVerificationFailed {
			where := "there"
			if !b.all && len(failed) > 1 {
				where = "in one of them"
			}
			parts = append(parts, "It can't reach "+stage+" until it passes "+where+".")
		}
	}
	if len(running) > 0 {
		parts = append(parts, "Verifying in "+strings.Join(running, " and ")+".")
	}
	if len(waiting) > 0 {
		parts = append(parts, "Waiting for "+strings.Join(waiting, join)+" to verify it.")
	}
	return reason, strings.Join(parts, " "), runs
}

func shortDuration(d time.Duration) string {
	if d < time.Minute {
		return "less than a minute"
	}
	s := strings.TrimSuffix(d.Truncate(time.Minute).String(), "0s")
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
