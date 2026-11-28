package kargo

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/conditions"
)

// AvailabilityAll is the FreightSources.availabilityStrategy under which
// Freight must be verified (and soaked) in every upstream Stage. Anything
// else, including empty, means one upstream Stage is enough.
const AvailabilityAll = "All"

// SelectionMatchUpstream is the auto-promotion selection policy that only
// promotes the Freight currently in the single upstream Stage.
const SelectionMatchUpstream = "MatchUpstream"

// FreightRequest is one Warehouse origin a Stage takes Freight from and
// where that Freight may come from.
type FreightRequest struct {
	Origin Origin
	// Direct: the Stage may take Freight straight from the Warehouse.
	Direct bool
	// Stages are the upstream Stages the Freight may come through.
	Stages []string
	// RequiredSoak is how long Freight must have stayed verified in an
	// upstream Stage; zero means none.
	RequiredSoak time.Duration
	// All: the Freight must be verified (and soaked) in every upstream
	// Stage, not just one.
	All bool
	// SelectionPolicy is the auto-promotion selection policy; empty means
	// Kargo's default, NewestFreight.
	SelectionPolicy string
}

// Verification is one verification of the Stage's current Freight.
type Verification struct {
	ID      string
	Phase   string
	Message string
	// AnalysisRun is the Argo Rollouts AnalysisRun that ran it, when one did.
	AnalysisRun *ObjectRef
	StartTime   time.Time
	FinishTime  time.Time
}

// Failed reports a verification that found a problem (Failed) or couldn't
// run (Error). Aborted and Inconclusive are not failures.
func (v Verification) Failed() bool {
	return v.Phase == "Failed" || v.Phase == "Error"
}

// Running reports a verification Kargo has started and not finished.
func (v Verification) Running() bool {
	return v.Phase == "Pending" || v.Phase == "Running"
}

// Metrics are the metrics a failed verification names. Kargo copies the
// AnalysisRun's message, which Argo Rollouts writes as
// `Metric "<name>" assessed <phase> due to ...` for the worst metric.
func (v Verification) Metrics() []string {
	rest, ok := strings.CutPrefix(v.Message, `Metric "`)
	if !ok {
		return nil
	}
	name, tail, ok := strings.Cut(rest, `"`)
	if !ok || name == "" || !strings.HasPrefix(tail, " assessed ") {
		return nil
	}
	return []string{name}
}

// ObjectRef names a namespaced object.
type ObjectRef struct {
	Namespace string
	Name      string
}

// Health is the Stage's health as its health checks reported it.
type Health struct {
	// Status is Healthy, Progressing, Unhealthy, Unknown or NotApplicable.
	Status string
	Issues []string
	// Apps are the Argo CD Applications whose health the checks read.
	Apps []AppStatus
}

// AppStatus is an Argo CD Application's state as a Stage's health check
// last read it.
type AppStatus struct {
	Namespace     string
	Name          string
	Health        string
	HealthMessage string
	Sync          string
	Revision      string
}

// PromotionRecord is a Stage's record of its current or last Promotion.
type PromotionRecord struct {
	Name       string
	Freight    *FreightRef
	Phase      string
	Message    string
	FinishedAt time.Time
	// Steps are the step results the record carries, without the steps'
	// definitions, which only the Promotion object has.
	Steps []StepResult
}

// Failure is the reason the recorded Promotion failed, chosen as Failure
// chooses it for a Promotion.
func (r PromotionRecord) Failure() Failure {
	return failureOf(r.Phase, r.Message, r.Steps)
}

// Stage is what a Stage object says about its pipeline position, contents
// and state.
type Stage struct {
	Namespace string
	Name      string
	Requests  []FreightRequest
	// Current is the Freight the Stage holds, one per origin, ordered by
	// origin. A Stage that takes Freight from several Warehouses holds one
	// from each.
	Current []FreightRef
	// Verification is the latest verification of the current Freight.
	Verification *Verification
	// Verifications are the latest verification of each Freight in the
	// Stage's freight history, by Freight name, from the newest collection
	// holding it. A Freight absent here has no verification on record.
	Verifications map[string]Verification
	Health        *Health
	// CurrentPromotion is a Promotion running now; LastPromotion is the last
	// one that finished.
	CurrentPromotion *PromotionRecord
	LastPromotion    *PromotionRecord
	// AutoPromotion is whether the project's policy lets Kargo promote into
	// this Stage on its own, as the Stage's status reports it.
	AutoPromotion bool
	// Held: auto-promotion is paused for at least one origin; HeldOrigins
	// are those origins' keys ("Warehouse/name"). Kargo checks the hold per
	// origin, so use HeldFor for one origin.
	Held        bool
	HeldOrigins map[string]bool
	// ControlFlow: the Stage has no promotion steps and only passes Freight on.
	ControlFlow bool
	// ArgoCDApps are the Argo CD Applications the last Promotion updated or
	// waited on, from the Stage's argocd-context annotation.
	ArgoCDApps []ObjectRef
}

// ParseStage reads a Stage object.
func ParseStage(u *unstructured.Unstructured) Stage {
	if u == nil {
		return Stage{}
	}
	s := Stage{
		Namespace:     u.GetNamespace(),
		Name:          u.GetName(),
		Requests:      requests(u),
		Current:       currentFreight(u),
		Verification:  currentVerification(u),
		Verifications: verifications(u),
		Health:        health(u),
		AutoPromotion: boolean(u.Object, "status", "autoPromotionEnabled"),
		HeldOrigins:   heldOrigins(u),
		ControlFlow:   len(objects(u.Object, "spec", "promotionTemplate", "spec", "steps")) == 0,
	}
	s.Held = len(s.HeldOrigins) > 0
	s.CurrentPromotion = promotionRecord(object(u.Object, "status", "currentPromotion"))
	s.LastPromotion = promotionRecord(object(u.Object, "status", "lastPromotion"))
	s.ArgoCDApps, _ = ArgoCDApps(u)
	return s
}

// CurrentFor is the Freight the Stage holds from origin.
func (s Stage) CurrentFor(origin Origin) (FreightRef, bool) {
	for _, f := range s.Current {
		if f.Origin == origin {
			return f, true
		}
	}
	return FreightRef{}, false
}

// RequestFor is the Stage's request for Freight from origin.
func (s Stage) RequestFor(origin Origin) (FreightRequest, bool) {
	for _, r := range s.Requests {
		if r.Origin == origin {
			return r, true
		}
	}
	return FreightRequest{}, false
}

// HeldFor reports whether auto-promotion is paused for origin.
func (s Stage) HeldFor(origin Origin) bool {
	return s.HeldOrigins[origin.String()]
}

func heldOrigins(u *unstructured.Unstructured) map[string]bool {
	holds := object(u.Object, "status", "effectiveAutoPromotionHolds")
	if len(holds) == 0 {
		return nil
	}
	out := make(map[string]bool, len(holds))
	for origin := range holds {
		out[origin] = true
	}
	return out
}

// Upstream are the Stages this Stage takes Freight through, each once, in
// declaration order.
func (s Stage) Upstream() []string {
	var out []string
	for _, r := range s.Requests {
		for _, name := range r.Stages {
			if !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
	}
	return out
}

// Warehouses are the Warehouses this Stage takes Freight straight from.
func (s Stage) Warehouses() []string {
	var out []string
	for _, r := range s.Requests {
		if r.Direct && !slices.Contains(out, r.Origin.Name) {
			out = append(out, r.Origin.Name)
		}
	}
	return out
}

func requests(u *unstructured.Unstructured) []FreightRequest {
	var out []FreightRequest
	for _, r := range objects(u.Object, "spec", "requestedFreight") {
		req := FreightRequest{
			Origin:          parseOrigin(object(r, "origin")),
			Direct:          boolean(r, "sources", "direct"),
			Stages:          stringList(r, "sources", "stages"),
			All:             str(r, "sources", "availabilityStrategy") == AvailabilityAll,
			SelectionPolicy: str(r, "sources", "autoPromotionOptions", "selectionPolicy"),
		}
		req.RequiredSoak, _ = duration(r, "sources", "requiredSoakTime")
		out = append(out, req)
	}
	return out
}

// currentFreight reads freightHistory[0], the Stage's current collection.
func currentFreight(u *unstructured.Unstructured) []FreightRef {
	history := objects(u.Object, "status", "freightHistory")
	if len(history) == 0 {
		return nil
	}
	items := object(history[0], "items")
	out := make([]FreightRef, 0, len(items))
	for key, raw := range items {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		ref := parseFreightRef(m)
		if ref.Origin == (Origin{}) {
			if origin, ok := parseOriginKey(key); ok {
				ref.Origin = origin
			}
		}
		out = append(out, ref)
	}
	slices.SortFunc(out, func(a, b FreightRef) int { return cmp.Compare(a.Origin.String(), b.Origin.String()) })
	return out
}

func parseOriginKey(key string) (Origin, bool) {
	kind, name, ok := strings.Cut(key, "/")
	return Origin{Kind: kind, Name: name}, ok && kind != "" && name != ""
}

func currentVerification(u *unstructured.Unstructured) *Verification {
	history := objects(u.Object, "status", "freightHistory")
	if len(history) == 0 {
		return nil
	}
	return latestVerification(history[0])
}

// verifications reads, newest collection first, the latest verification of
// each Freight the history holds.
func verifications(u *unstructured.Unstructured) map[string]Verification {
	var out map[string]Verification
	seen := map[string]bool{}
	for _, collection := range objects(u.Object, "status", "freightHistory") {
		v := latestVerification(collection)
		for _, raw := range object(collection, "items") {
			item, ok := raw.(map[string]any)
			name := str(item, "name")
			if !ok || name == "" || seen[name] {
				continue
			}
			seen[name] = true
			if v != nil {
				if out == nil {
					out = map[string]Verification{}
				}
				out[name] = *v
			}
		}
	}
	return out
}

// latestVerification is verificationHistory[0] of a freight collection,
// which Kargo keeps newest first.
func latestVerification(collection map[string]any) *Verification {
	verifications := objects(collection, "verificationHistory")
	if len(verifications) == 0 {
		return nil
	}
	v := verifications[0]
	out := &Verification{
		ID:         str(v, "id"),
		Phase:      str(v, "phase"),
		Message:    str(v, "message"),
		StartTime:  timeAt(v, "startTime"),
		FinishTime: timeAt(v, "finishTime"),
	}
	if run := object(v, "analysisRun"); str(run, "name") != "" {
		out.AnalysisRun = &ObjectRef{Namespace: str(run, "namespace"), Name: str(run, "name")}
	}
	return out
}

func health(u *unstructured.Unstructured) *Health {
	h := object(u.Object, "status", "health")
	if h == nil {
		return nil
	}
	out := &Health{Status: str(h, "status"), Issues: stringList(h, "issues")}
	// output is untyped: each health check contributes one entry, and the
	// Argo CD check's lists applicationStatuses. Kargo serializes their name
	// and namespace with Go field names, Name and Namespace.
	for _, entry := range objects(h, "output") {
		for _, app := range objects(entry, "applicationStatuses") {
			name, ns := cmp.Or(str(app, "Name"), str(app, "name")), cmp.Or(str(app, "Namespace"), str(app, "namespace"))
			if name == "" {
				continue
			}
			out.Apps = append(out.Apps, AppStatus{
				Namespace:     ns,
				Name:          name,
				Health:        str(app, "health", "status"),
				HealthMessage: str(app, "health", "message"),
				Sync:          str(app, "sync", "status"),
				Revision:      str(app, "sync", "revision"),
			})
		}
	}
	return out
}

func promotionRecord(m map[string]any) *PromotionRecord {
	if str(m, "name") == "" {
		return nil
	}
	r := &PromotionRecord{
		Name:       str(m, "name"),
		Phase:      str(m, "status", "phase"),
		Message:    str(m, "status", "message"),
		FinishedAt: timeAt(m, "finishedAt"),
		Steps:      stepResults(object(m, "status")),
	}
	if f := object(m, "freight"); f != nil {
		ref := parseFreightRef(f)
		r.Freight = &ref
	}
	return r
}

// Phase is the Stage's state from its conditions, read as Kargo's own UI
// reads them except that a Ready=False Kargo writes while waiting, or for an
// unknown health, is not a failure, and a failure on Ready wins over
// Verifying and Reconciling: Promoting, Verifying, Progressing (health checks running),
// Reconciling, Aborted (a person cancelled the last promotion or
// verification), Failed, Ready or Unknown. A Stage without Freight is never
// Failed for that alone.
func Phase(u *unstructured.Unstructured) string {
	const noFreight = "NoFreight"
	if c, ok := conditions.Find(u, "Promoting"); ok && c.Status == "True" && c.Reason != noFreight {
		return "Promoting"
	}
	ready, readyOK := conditions.Find(u, "Ready")
	notReady := readyOK && ready.Status == "False"
	// A failure on Ready wins over Verifying and Reconciling: after a
	// reconcile error Kargo writes Ready=False/ReconcileError beside
	// Reconciling=True/RetryAfterError.
	failed := notReady && !WaitingReason(ready.Reason) && !cancelledReasons[ready.Reason] && ready.Reason != "Unknown"
	if failed {
		return "Failed"
	}
	if c, ok := conditions.Find(u, "Verified"); ok && c.Status == "Unknown" && c.Reason != noFreight {
		return "Verifying"
	}
	if c, ok := conditions.Find(u, "Reconciling"); ok && c.Status == "True" {
		return "Reconciling"
	}
	switch {
	case notReady && WaitingReason(ready.Reason):
		return waitingReasons[ready.Reason]
	case notReady && cancelledReasons[ready.Reason]:
		return "Aborted"
	case readyOK && ready.Status == "True":
		return "Ready"
	}
	// Includes Ready=False/Unknown, Kargo copying an unknown health.
	return "Unknown"
}
