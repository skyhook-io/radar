package agentsandbox

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/health"
)

const (
	// RecentExpiry is how far back the workspace names what expired.
	RecentExpiry = 7 * 24 * time.Hour

	maxAttention = 200
	maxRecent    = 20
	maxWarmPools = 500
)

// Objects is what one kind's read returned.
type Objects struct {
	Items []*unstructured.Unstructured
	// Read reports whether every object of the kind in namespace was read.
	// Only then does an object missing from Items not exist.
	Read func(namespace string) bool
	// Usable: something was read, in all of the request scope or part of it.
	Usable bool
	// Complete: every namespace of the request scope was read. A kind that is
	// not installed is complete: there is nothing to read.
	Complete bool
}

func (o Objects) read(namespace string) bool { return o.Read != nil && o.Read(namespace) }

// Input is one request's reads.
type Input struct {
	Now                                     time.Time
	Sandboxes, Claims, WarmPools, Templates Objects
	Controller                              Controller
}

// Presence is what Radar can say about an object another one names.
type Presence string

const (
	Present Presence = "present"
	// Absent: the kind was read in full in that namespace and the object is not there.
	Absent Presence = "absent"
	Unread Presence = "unread"
)

// Relation is one object a row rests on, with what Radar can say about it.
type Relation struct {
	// Role: warmPool, template, sandbox, owner.
	Role     string   `json:"role"`
	Ref      Ref      `json:"ref"`
	Presence Presence `json:"presence"`
}

// Category groups the Needs attention rows.
type Category string

const (
	CategoryController Category = "controller"
	CategorySandbox    Category = "sandbox"
	CategoryClaim      Category = "claim"
	CategoryWarmPool   Category = "warmPool"
)

// Rule is which rule raised a row. Clients branch on it, never on titles.
type Rule string

const (
	RuleControllerNotRunning Rule = "controllerNotRunning"
	RuleFailed               Rule = "failed"
	RuleReconcilerError      Rule = "reconcilerError"
	RuleSuspensionBlocked    Rule = "suspensionBlocked"
	// RuleStalled: in progress for longer than IncidentAfter. Whether Issues
	// list it is the row's Issue field: a claim forwarding its Sandbox's
	// progress is not, since Issues report that on the Sandbox.
	RuleStalled Rule = "stalled"
	// RuleSlow: in progress past SlowAfter; an advisory, never an Issue.
	RuleSlow Rule = "slow"
	// RuleCannotBind: a claim the controller reports it can't bind, or one
	// whose warm pool or template Radar read in full and did not find.
	RuleCannotBind         Rule = "cannotBind"
	RuleTemplateMissing    Rule = "templateMissing"
	RuleReserveBelowTarget Rule = "reserveBelowTarget"
)

// Severity is a row's prominence, in the ladder GitOps, Velero, CNPG and
// Substrate rows use: critical (red), alert (orange), warning (amber).
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityAlert    Severity = "alert"
	SeverityWarning  Severity = "warning"
)

// DetailSource says whose words RawDetail holds.
type DetailSource string

const (
	DetailScheduler  DetailSource = "scheduler"
	DetailController DetailSource = "controller"
)

// Related are the objects a row points at for the next step.
type Related struct {
	Lead string `json:"lead"`
	Refs []Ref  `json:"refs"`
}

// Attention is one Needs attention row. One fault is one row: a warm pool's
// not-ready members are named on its row, and a stopped controller's row
// names the Sandboxes left starting.
type Attention struct {
	ID       string   `json:"id"`
	Category Category `json:"category"`
	Rule     Rule     `json:"rule"`
	// Reason is the controller's own reason where it reported one (PodFailed,
	// Unschedulable, WarmPoolNotFound); an AgentSandbox-prefixed reason where
	// Radar found it (AgentSandboxWarmPoolNotFound, AgentSandboxControllerNotReady).
	Reason   string   `json:"reason,omitempty"`
	Severity Severity `json:"severity"`
	// Title carries no elapsed time; Since does.
	Title   string `json:"title"`
	Reading string `json:"reading,omitempty"`
	// RawDetail is the controller's or scheduler's own message, folded under the reading.
	RawDetail       string       `json:"rawDetail,omitempty"`
	RawDetailLabel  string       `json:"rawDetailLabel,omitempty"`
	RawDetailSource DetailSource `json:"rawDetailSource,omitempty"`
	NextStep        string       `json:"nextStep"`
	Subject         Ref          `json:"subject"`
	Related         *Related     `json:"related,omitempty"`
	Relations       []Relation   `json:"relations,omitempty"`
	// Since is when the state began, where that is known.
	Since *time.Time `json:"since,omitempty"`
	// Issue: Radar's Issues list the subject for the same state. It is the
	// authoritative parity bit; no rule implies it.
	Issue bool `json:"issue"`
}

// SandboxCounts is the status line's Sandboxes. LowerBound: read in part, so
// every count is at least this and none says how the rest read.
type SandboxCounts struct {
	Total      int  `json:"total"`
	LowerBound bool `json:"lowerBound"`
	Running    int  `json:"running"`
	// Starting: in progress, other than Unschedulable.
	Starting int `json:"starting"`
	// Unschedulable: in progress, and the scheduler can't place its Pod
	// (PodScheduled=False with the scheduler's reason).
	Unschedulable int `json:"unschedulable"`
	// Suspended: confirmed suspended, or suspending. A suspension the
	// controller refuses is SuspensionBlocked: its Pod may still be running.
	Suspended         int `json:"suspended"`
	SuspensionBlocked int `json:"suspensionBlocked"`
	Expired           int `json:"expired"`
	Finished          int `json:"finished"`
	Failed            int `json:"failed"`
	Terminating       int `json:"terminating"`
	Unknown           int `json:"unknown"`
}

// ClaimCounts is the status line's claims. Bound: names its Sandbox and has
// not expired.
type ClaimCounts struct {
	Total      int  `json:"total"`
	LowerBound bool `json:"lowerBound"`
	Bound      int  `json:"bound"`
	Waiting    int  `json:"waiting"`
	Expired    int  `json:"expired"`
}

// WarmPoolCounts sums ready and desired sandboxes across pools; desired counts
// only pools that set spec.replicas.
type WarmPoolCounts struct {
	Total      int   `json:"total"`
	LowerBound bool  `json:"lowerBound"`
	Ready      int64 `json:"ready"`
	Desired    int64 `json:"desired"`
}

// Counts is the status line. A kind is absent when nothing of it was read.
type Counts struct {
	Sandboxes *SandboxCounts  `json:"sandboxes,omitempty"`
	Claims    *ClaimCounts    `json:"claims,omitempty"`
	WarmPools *WarmPoolCounts `json:"warmPools,omitempty"`
}

// Count is a badge. Count is null when the read was partial and found
// nothing: zero would claim too much.
type Count struct {
	Count      *int `json:"count"`
	LowerBound bool `json:"lowerBound"`
}

// Sidebar is the badges: the total and each list's rows.
type Sidebar struct {
	Total     Count `json:"total"`
	Sandboxes Count `json:"sandboxes"`
	Claims    Count `json:"claims"`
	WarmPools Count `json:"warmPools"`
	// Controller: the total includes the controller's row, which is shared by
	// every namespace rather than about the selected ones.
	Controller bool `json:"controller"`
}

// Reading is one object's state.
type Reading struct {
	Text  string       `json:"text"`
	Level health.Level `json:"level"`
}

// ClaimsServed counts the claims that name a pool.
type ClaimsServed struct {
	Total int `json:"total"`
	Bound int `json:"bound"`
}

// WarmPoolRow is one pool in the Overview's table.
type WarmPoolRow struct {
	Namespace string  `json:"namespace"`
	Name      string  `json:"name"`
	State     Reading `json:"state"`
	Ready     int64   `json:"ready"`
	// Desired is absent when the pool sets no spec.replicas.
	Desired          *int64   `json:"desired,omitempty"`
	Template         string   `json:"template,omitempty"`
	TemplatePresence Presence `json:"templatePresence,omitempty"`
	// Claims is absent when the namespace's claims were not read.
	Claims  *ClaimsServed `json:"claims,omitempty"`
	Created time.Time     `json:"created,omitzero"`
}

// ExpiredEntry is a Sandbox or claim that reached its shutdown time.
type ExpiredEntry struct {
	Ref Ref `json:"ref"`
	// At is the configured shutdown time.
	At time.Time `json:"at"`
}

// Expired names what expired within RecentExpiry, newest first. An expiry
// with no shutdown time recorded can't be placed in the window and is left
// out; counts.sandboxes.expired still counts it.
type Expired struct {
	Recent []ExpiredEntry `json:"recent"`
	// RecentTotal counts the recent entries before the cap.
	RecentTotal int  `json:"recentTotal"`
	LowerBound  bool `json:"lowerBound"`
}

// Summary is the workspace's answer, all of it from objects the caller read.
type Summary struct {
	Controller Controller  `json:"controller"`
	Counts     Counts      `json:"counts"`
	Attention  []Attention `json:"attention"`
	// AttentionTotal counts the rows before the cap.
	AttentionTotal int `json:"attentionTotal"`
	// AttentionComplete: every kind and the controller were read in full, so
	// no rows means nothing needs attention.
	AttentionComplete bool          `json:"attentionComplete"`
	Sidebar           Sidebar       `json:"sidebar"`
	WarmPools         []WarmPoolRow `json:"warmPools"`
	WarmPoolsTotal    int           `json:"warmPoolsTotal"`
	Expired           Expired       `json:"expired"`
}

type index map[string]*unstructured.Unstructured

func indexOf(items []*unstructured.Unstructured) index {
	out := make(index, len(items))
	for _, u := range items {
		out[key(u)] = u
	}
	return out
}

type builder struct {
	in                                  Input
	sandboxes, claims, pools, templates index
	// represented: Sandboxes some row covers, their own or one they fold into.
	represented map[string]bool
}

func (b *builder) presence(o Objects, idx index, namespace, name string) Presence {
	if _, ok := idx[namespace+"/"+name]; ok {
		return Present
	}
	if o.read(namespace) {
		return Absent
	}
	return Unread
}

func since(r health.AgentSandboxReadiness) *time.Time {
	if !r.SinceKnown {
		return nil
	}
	t := r.Since.UTC()
	return &t
}

var failureTitles = map[string]string{
	"ImagePullBackOff":           "Image can’t be pulled",
	"ErrImagePull":               "Image can’t be pulled",
	"InvalidImageName":           "Image name is invalid",
	"CrashLoopBackOff":           "Container keeps crashing",
	"CreateContainerConfigError": "Container can’t be configured",
	"PodFailed":                  "Pod failed",
	"InvalidConfiguration":       "Invalid configuration",
	"MultiplePods":               "More than one Pod matches",
}

var failureNextSteps = map[string]string{
	"ImagePullBackOff":           "Check the image name and the registry credentials (imagePullSecrets) in the Sandbox’s Pod template",
	"ErrImagePull":               "Check the image name and the registry credentials (imagePullSecrets) in the Sandbox’s Pod template",
	"InvalidImageName":           "Fix the image reference in the Sandbox’s Pod template",
	"CrashLoopBackOff":           "Read the container’s logs to see why it exits",
	"CreateContainerConfigError": "Check the ConfigMaps and Secrets the Pod template references",
	"PodFailed":                  "Read the Pod’s logs to see why it exited",
	"InvalidConfiguration":       "Fix what the controller reports in the Sandbox’s spec",
	"MultiplePods":               "Delete the extra Pods that carry this Sandbox’s labels",
}

var claimTitles = map[string]string{
	"WarmPoolNotFound":          "Warm pool not found",
	"TemplateNotFound":          "Template not found",
	"InvalidMetadata":           "Invalid Pod metadata",
	"EnvVarsInjectionRejected":  "Environment variables rejected",
	"VolumeClaimTemplatesError": "Volume claim templates rejected",
	"SandboxMissing":            "Its Sandbox is gone",
}

const (
	schedulerNextStep   = "Free capacity on a node, or change the Pod template’s requests, node selector or tolerations"
	schedulerLabel      = "Scheduler’s message"
	controllerLabel     = "Controller’s message"
	reconcilerTitle     = "Controller can’t reconcile it"
	reconcilerNextStep  = "Read the agent-sandbox controller’s logs; it retries on its own once the cause is fixed"
	unreportedNextStep  = "Check that the agent-sandbox controller is running and reconciles this namespace"
	defaultFailNextStep = "Read the Sandbox’s conditions and its Pod’s events"
)

func failureTitle(reason string) string {
	if t, ok := failureTitles[reason]; ok {
		return t
	}
	return "Failed: " + reason
}

func failureNextStep(reason string) string {
	if s, ok := failureNextSteps[reason]; ok {
		return s
	}
	return defaultFailNextStep
}

// notRepeating is the controller's message unless it only repeats the title
// ("Pod failed" under "Pod failed").
func notRepeating(message, title string) string {
	if strings.EqualFold(strings.TrimSuffix(message, "."), title) {
		return ""
	}
	return message
}

// schedulerBlocked: the reading is the scheduler's reason for not placing the
// Pod (pkg/health reads PodScheduled=False's reason as the state).
func schedulerBlocked(u *unstructured.Unstructured, r health.AgentSandboxReadiness) bool {
	scheduled, ok := condition(u, "PodScheduled")
	return ok && scheduled.status == "False" && scheduled.reason != "" && scheduled.reason == r.Verdict.Reason
}

func progressRule(p Progress) (Rule, Severity) {
	if p == ProgressStalled {
		return RuleStalled, SeverityAlert
	}
	return RuleSlow, SeverityWarning
}

// progressDetail fills a row for a state still in progress: the scheduler's
// verdict when its Pod can't be placed, or that the controller never reported.
func progressDetail(a *Attention, u *unstructured.Unstructured, r health.AgentSandboxReadiness, noun string) {
	if Unreported(u) {
		a.Title = "No status from the controller"
		a.Reading = fmt.Sprintf("The agent-sandbox controller hasn’t reported on this %s since it was created.", noun)
		a.NextStep = unreportedNextStep
		return
	}
	if schedulerBlocked(u, r) {
		scheduled, _ := condition(u, "PodScheduled")
		if scheduled.reason == "Unschedulable" {
			a.Title = "Pod can’t be scheduled"
		} else {
			a.Title = "Pod not scheduled: " + scheduled.reason
		}
		a.Reading = "The scheduler can’t place its Pod."
		a.RawDetail, a.RawDetailLabel, a.RawDetailSource = scheduled.message, schedulerLabel, DetailScheduler
		a.NextStep = schedulerNextStep
		a.Severity = SeverityAlert
		return
	}
	ready, _ := condition(u, "Ready")
	if r.Verdict.Reason == "Binding" {
		a.Title = "Still binding"
	} else {
		a.Title = "Still starting"
	}
	a.Reading = ready.message
	a.NextStep = fmt.Sprintf("Check the %s’s Pod and its events for what it waits on", noun)
}

func (b *builder) sandboxOwnerRelation(u *unstructured.Unstructured) []Relation {
	owner, ok := sandboxOwner(u)
	if !ok {
		return nil
	}
	p := b.presence(b.in.Claims, b.claims, owner.Namespace, owner.Name)
	if owner.Kind == "SandboxWarmPool" {
		p = b.presence(b.in.WarmPools, b.pools, owner.Namespace, owner.Name)
	}
	return []Relation{{Role: "owner", Ref: owner, Presence: p}}
}

// sandboxRow is a Sandbox's own row. progress reports a row that is only
// late, which a stopped controller's row takes over.
func (b *builder) sandboxRow(u *unstructured.Unstructured) (a *Attention, progress bool) {
	now := b.in.Now
	r := health.ReadAgentSandbox(u)
	if u.GetDeletionTimestamp() != nil || r.Verdict.Reason == "Terminating" {
		return nil, false
	}
	ready, _ := condition(u, "Ready")
	row := Attention{
		ID: "sandbox:" + key(u), Category: CategorySandbox, Subject: sandboxRef(u),
		Reason: r.Verdict.Reason, Issue: IssueRaised(u, r, now), Relations: b.sandboxOwnerRelation(u),
	}
	pod := &Related{Lead: "Its Pod:", Refs: []Ref{podRef(u)}}
	switch {
	case r.SuspensionRefused:
		suspended, _ := condition(u, "Suspended")
		name := health.AgentSandboxPodName(u)
		row.Rule, row.Severity, row.Reason = RuleSuspensionBlocked, SeverityAlert, "PodNotOwned"
		row.Title = "Suspension blocked"
		row.Reading = "The controller refuses to delete a Pod this Sandbox doesn’t own, so the suspension waits."
		row.RawDetail, row.RawDetailLabel, row.RawDetailSource = suspended.message, controllerLabel, DetailController
		row.NextStep = fmt.Sprintf("Remove Pod %s, which this Sandbox doesn’t own, or recreate the Sandbox under another name", name)
		row.Related = &Related{Lead: "The Pod it won’t delete:", Refs: []Ref{podRef(u)}}
		return &row, false
	case ready.status == "False" && ready.reason == "ReconcilerError":
		row.Rule, row.Severity, row.Title = RuleReconcilerError, SeverityAlert, reconcilerTitle
		row.RawDetail, row.RawDetailLabel, row.RawDetailSource = ready.message, controllerLabel, DetailController
		row.NextStep = reconcilerNextStep
		row.Since = reportedSince(u)
		return &row, false
	case Failed(u, r):
		row.Rule, row.Severity, row.Title = RuleFailed, SeverityCritical, failureTitle(r.Verdict.Reason)
		row.RawDetail = notRepeating(ready.message, row.Title)
		if row.RawDetail != "" {
			row.RawDetailLabel, row.RawDetailSource = controllerLabel, DetailController
		}
		row.NextStep = failureNextStep(r.Verdict.Reason)
		row.Related = pod
		row.Since = reportedSince(u)
		return &row, false
	}
	p := ProgressOf(r, now)
	if p == ProgressOnTime {
		return nil, false
	}
	row.Rule, row.Severity = progressRule(p)
	row.Since = since(r)
	progressDetail(&row, u, r, "Sandbox")
	row.Related = pod
	return &row, true
}

func (b *builder) claimRelations(u *unstructured.Unstructured) (rels []Relation, pool, template Presence) {
	ns := u.GetNamespace()
	if name := ClaimWarmPool(u); name != "" {
		pool = b.presence(b.in.WarmPools, b.pools, ns, name)
		rels = append(rels, Relation{Role: "warmPool", Ref: poolRef(ns, name), Presence: pool})
		if p, ok := b.pools[ns+"/"+name]; ok {
			if t := WarmPoolTemplate(p); t != "" {
				template = b.presence(b.in.Templates, b.templates, ns, t)
				rels = append(rels, Relation{Role: "template", Ref: templateRef(ns, t), Presence: template})
			}
		}
	}
	if name := ClaimSandbox(u); name != "" {
		rels = append(rels, Relation{Role: "sandbox", Ref: Ref{Kind: "Sandbox", Group: Group, Namespace: ns, Name: name}, Presence: b.presence(b.in.Sandboxes, b.sandboxes, ns, name)})
	}
	return rels, pool, template
}

func (b *builder) claimRow(u *unstructured.Unstructured) *Attention {
	now := b.in.Now
	r := health.ReadAgentSandbox(u)
	if u.GetDeletionTimestamp() != nil || r.Settled {
		return nil
	}
	ns := u.GetNamespace()
	ready, _ := condition(u, "Ready")
	rels, poolPresence, templatePresence := b.claimRelations(u)
	row := Attention{
		ID: "claim:" + key(u), Category: CategoryClaim, Subject: claimRef(u), Reason: r.Verdict.Reason,
		Issue: IssueRaised(u, r, now), Relations: rels,
	}
	bound := ClaimSandbox(u)
	if r.Mirrored {
		// The bound Sandbox reports the same state under its own name. Fold
		// into it only where it is actually on screen.
		if bound != "" {
			if b.represented[ns+"/"+bound] {
				return nil
			}
			if _, read := b.sandboxes[ns+"/"+bound]; read && r.InProgress {
				return nil
			}
		}
		if Failed(u, r) {
			row.Rule, row.Severity, row.Title = RuleFailed, SeverityCritical, failureTitle(r.Verdict.Reason)
			row.RawDetail = notRepeating(ready.message, row.Title)
			if row.RawDetail != "" {
				row.RawDetailLabel, row.RawDetailSource = controllerLabel, DetailController
			}
			row.NextStep = failureNextStep(r.Verdict.Reason)
			row.Since = reportedSince(u)
			if bound != "" {
				row.Related = &Related{Lead: "Its Sandbox:", Refs: []Ref{{Kind: "Sandbox", Group: Group, Namespace: ns, Name: bound}}}
			}
			return &row
		}
		p := ProgressOf(r, now)
		if p == ProgressOnTime {
			return nil
		}
		row.Rule, row.Severity = progressRule(p)
		row.Since = since(r)
		progressDetail(&row, u, r, "SandboxClaim")
		return &row
	}

	switch {
	case ready.status == "False" && ready.reason == "ReconcilerError":
		row.Rule, row.Severity, row.Title = RuleReconcilerError, SeverityAlert, reconcilerTitle
		row.RawDetail, row.RawDetailLabel, row.RawDetailSource = ready.message, controllerLabel, DetailController
		row.NextStep = reconcilerNextStep
		row.Since = reportedSince(u)
		return &row
	case !r.InProgress && ready.status == "False" && (r.Verdict.Level == health.LevelUnhealthy || ready.reason == "WarmPoolNotFound" || ready.reason == "TemplateNotFound"):
		// Not terminal: fixing the reference binds it. A warning, as its Issue is.
		row.Rule, row.Severity = RuleCannotBind, SeverityWarning
		row.Title = claimTitles[ready.reason]
		if row.Title == "" {
			row.Title = "Can’t bind: " + r.Verdict.Reason
		}
		row.RawDetail, row.RawDetailLabel, row.RawDetailSource = ready.message, controllerLabel, DetailController
		row.Since = reportedSince(u)
		pool := ClaimWarmPool(u)
		row.NextStep = "Fix what the controller reports in the claim’s spec"
		switch ready.reason {
		case "WarmPoolNotFound":
			if pool != "" {
				row.NextStep = fmt.Sprintf("Create SandboxWarmPool %s in %s, or point the claim at an existing pool", pool, ns)
			} else {
				row.NextStep = "Point the claim at an existing warm pool"
			}
			if poolPresence == Present {
				row.Reading = fmt.Sprintf("Radar finds SandboxWarmPool %s in %s, so the controller’s report may predate it.", pool, ns)
			}
		case "TemplateNotFound":
			row.NextStep = "Create the SandboxTemplate the warm pool names, or fix the pool’s template reference"
			if poolPresence == Present {
				row.Related = &Related{Lead: "The warm pool:", Refs: []Ref{poolRef(ns, pool)}}
			}
			if templatePresence == Present {
				row.Reading = "Radar finds the template the warm pool names, so the controller’s report may predate it."
			}
		case "SandboxMissing":
			row.NextStep = "Its Sandbox was deleted; delete the claim and create it again"
		}
		return &row
	}

	// Radar's own reads, where the controller has not said why the claim waits.
	if bound == "" {
		if pool := ClaimWarmPool(u); pool != "" && poolPresence == Absent {
			row.Rule, row.Severity, row.Reason, row.Issue = RuleCannotBind, SeverityWarning, ReasonWarmPoolNotFound, false
			row.Title = claimTitles["WarmPoolNotFound"]
			row.Reading = fmt.Sprintf("The claim names SandboxWarmPool %s, which doesn’t exist in %s.", pool, ns)
			row.NextStep = fmt.Sprintf("Create SandboxWarmPool %s in %s, or point the claim at an existing pool", pool, ns)
			return &row
		}
		if pool := ClaimWarmPool(u); pool != "" && templatePresence == Absent {
			t := WarmPoolTemplate(b.pools[ns+"/"+pool])
			row.Rule, row.Severity, row.Reason, row.Issue = RuleCannotBind, SeverityWarning, ReasonTemplateNotFound, false
			row.Title = claimTitles["TemplateNotFound"]
			row.Reading = fmt.Sprintf("Its warm pool %s names SandboxTemplate %s, which doesn’t exist in %s.", pool, t, ns)
			row.NextStep = "Create the SandboxTemplate the warm pool names, or fix the pool’s template reference"
			row.Related = &Related{Lead: "The warm pool:", Refs: []Ref{poolRef(ns, pool)}}
			return &row
		}
	}
	p := ProgressOf(r, now)
	if p == ProgressOnTime {
		return nil
	}
	row.Rule, row.Severity = progressRule(p)
	row.Since = since(r)
	progressDetail(&row, u, r, "SandboxClaim")
	return &row
}

// poolRow is a warm pool's row and the members it names, which get no row of
// their own.
func (b *builder) poolRow(u *unstructured.Unstructured) (*Attention, []string) {
	now := b.in.Now
	ns := u.GetNamespace()
	// A pool being deleted will create nothing more; Issues skip it too.
	if u.GetDeletionTimestamp() != nil {
		return nil, nil
	}
	row := Attention{ID: "warmPool:" + key(u), Category: CategoryWarmPool, Subject: poolRef(ns, u.GetName())}
	if t := WarmPoolTemplate(u); t != "" {
		p := b.presence(b.in.Templates, b.templates, ns, t)
		row.Relations = append(row.Relations, Relation{Role: "template", Ref: templateRef(ns, t), Presence: p})
		if p == Absent {
			// A warning, as its Issue and every other Ready=False Issue are.
			row.Rule, row.Severity, row.Reason, row.Issue = RuleTemplateMissing, SeverityWarning, ReasonTemplateNotFound, true
			row.Title = "Template not found"
			row.Reading = fmt.Sprintf("The pool names SandboxTemplate %s, which doesn’t exist in %s, so it can’t create sandboxes and its claims can’t bind.", t, ns)
			row.NextStep = fmt.Sprintf("Create SandboxTemplate %s in %s, or point the pool at an existing template", t, ns)
			return &row, nil
		}
	}
	if ReadWarmPool(u).Reason != "Warming" {
		return nil, nil
	}
	created := u.GetCreationTimestamp().Time
	if created.IsZero() || now.Sub(created) < SlowAfter {
		return nil, nil
	}
	membersRead := b.in.Sandboxes.read(ns)
	members := poolMembers(u, b.in.Sandboxes.Items)
	type member struct {
		u *unstructured.Unstructured
		r health.AgentSandboxReadiness
	}
	var notReady []member
	for _, m := range members {
		r := health.ReadAgentSandbox(m)
		if r.Verdict.Level == health.LevelHealthy {
			continue
		}
		// A member still within its start-up time means the pool is refilling.
		start := m.GetCreationTimestamp().Time
		if r.SinceKnown {
			start = r.Since
		}
		if start.IsZero() || now.Sub(start) < SlowAfter {
			return nil, nil
		}
		notReady = append(notReady, member{m, r})
	}
	ready, _ := nestedCount(u.Object, "status", "readyReplicas")
	desired, _ := nestedCount(u.Object, "spec", "replicas")

	counts := map[string]int{}
	var order []string
	worst := -1
	rank := map[health.Level]int{health.LevelUnhealthy: 0, health.LevelDegraded: 1, health.LevelUnknown: 2, health.LevelNeutral: 3}
	sameReason := true
	var folds []string
	var refs []Ref
	for i, m := range notReady {
		if counts[m.r.Verdict.Reason] == 0 {
			order = append(order, m.r.Verdict.Reason)
		}
		counts[m.r.Verdict.Reason]++
		if worst < 0 || rank[m.r.Verdict.Level] < rank[notReady[worst].r.Verdict.Level] {
			worst = i
		}
		if m.r.Verdict.Reason != notReady[0].r.Verdict.Reason {
			sameReason = false
		}
		folds = append(folds, key(m.u))
		refs = append(refs, sandboxRef(m.u))
	}

	row.Rule, row.Reason = RuleReserveBelowTarget, "Warming"
	row.Title = fmt.Sprintf("Warm reserve below target: %d/%d ready", ready, desired)
	var parts []string
	var scheduler cond
	if worst >= 0 {
		if schedulerBlocked(notReady[worst].u, notReady[worst].r) {
			scheduler, _ = condition(notReady[worst].u, "PodScheduled")
		}
	}
	switch {
	case scheduler.reason != "" && sameReason:
		who := "Its sandbox"
		if len(notReady) > 1 {
			who = fmt.Sprintf("%d of its sandboxes", len(notReady))
			if len(notReady) == len(members) {
				who = fmt.Sprintf("Its %d sandboxes", len(notReady))
			}
		}
		parts = append(parts, who+" can’t be scheduled.")
	case len(notReady) > 0:
		var text []string
		for _, reason := range order {
			text = append(text, fmt.Sprintf("%d %s", counts[reason], reason))
		}
		parts = append(parts, "Not ready: "+strings.Join(text, ", ")+".")
	}
	if !membersRead {
		parts = append(parts, "Radar couldn’t read its sandboxes.")
	} else if int64(len(members)) < desired {
		parts = append(parts, fmt.Sprintf("It has %d of %d sandboxes.", len(members), desired))
	}
	// Only an empty reserve says anything about claims: with any sandbox
	// ready, the next claim can still take a warm one.
	if ready == 0 {
		parts = append(parts, "No sandbox is ready in reserve, so a new claim can’t take a warm one and waits for a sandbox to start.")
	}
	row.Reading = strings.Join(parts, " ")
	switch {
	case scheduler.reason != "":
		row.RawDetail, row.RawDetailLabel, row.RawDetailSource = scheduler.message, schedulerLabel, DetailScheduler
		row.NextStep = schedulerNextStep
	case worst >= 0:
		if ready, _ := condition(notReady[worst].u, "Ready"); ready.message != "" {
			row.RawDetail, row.RawDetailLabel, row.RawDetailSource = ready.message, controllerLabel, DetailController
		}
		if notReady[worst].r.Verdict.Level == health.LevelUnhealthy {
			row.NextStep = failureNextStep(notReady[worst].r.Verdict.Reason)
		}
	}
	if row.NextStep == "" {
		row.NextStep = "Check the pool’s sandboxes and the agent-sandbox controller’s logs"
	}
	switch {
	case worst >= 0 && notReady[worst].r.Verdict.Level == health.LevelUnhealthy:
		row.Severity = SeverityCritical
	case ready == 0:
		row.Severity = SeverityAlert
	default:
		row.Severity = SeverityWarning
	}
	if len(refs) > 0 {
		row.Related = &Related{Lead: "Not ready:", Refs: refs}
	}
	return &row, folds
}

func (b *builder) controllerRow(waiting []Ref) *Attention {
	c := b.in.Controller
	if !c.down() {
		return nil
	}
	row := Attention{
		ID: "controller", Category: CategoryController, Rule: RuleControllerNotRunning, Severity: SeverityCritical,
		Reason: ReasonControllerNotReady, Title: "Agent Sandbox controller not running",
		Subject: Ref{Kind: "Deployment", Group: "apps", Namespace: c.Namespace, Name: c.Name},
		Since:   c.Since,
	}
	state := "No ready replica"
	row.NextStep = "Find out why the controller’s Pods aren’t ready, from their events and logs"
	if c.State == ControllerScaledToZero {
		state = "Scaled to zero"
		row.Reason = ReasonControllerScaledToZero
		row.NextStep = "Scale the controller Deployment back up"
	}
	row.Reading = state + ", so no Sandbox starts, suspends or expires, no claim binds and no warm pool refills. Their status stays as last written."
	if len(waiting) > 0 {
		lead := fmt.Sprintf("%d Sandboxes wait on it:", len(waiting))
		if len(waiting) == 1 {
			lead = "A Sandbox waits on it:"
		}
		row.Related = &Related{Lead: lead, Refs: waiting}
	}
	return &row
}

var severityRank = map[Severity]int{SeverityCritical: 0, SeverityAlert: 1, SeverityWarning: 2}

// ruleRank orders rows of one severity: what stops everything, then what is
// broken, then what needs a hand, then what is only late.
var ruleRank = map[Rule]int{
	RuleControllerNotRunning: 0,
	RuleFailed:               1,
	RuleCannotBind:           2,
	RuleTemplateMissing:      3,
	RuleSuspensionBlocked:    4,
	RuleReconcilerError:      5,
	RuleStalled:              6,
	RuleReserveBelowTarget:   7,
	RuleSlow:                 8,
}

// sortRows orders Needs attention by severity across every kind, then by
// rule, then newest first; rows with no known start follow those with one.
func sortRows(rows []Attention) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if ta, tb := severityRank[a.Severity], severityRank[b.Severity]; ta != tb {
			return ta < tb
		}
		if ra, rb := ruleRank[a.Rule], ruleRank[b.Rule]; ra != rb {
			return ra < rb
		}
		switch {
		case a.Since != nil && b.Since != nil && !a.Since.Equal(*b.Since):
			return a.Since.After(*b.Since)
		case (a.Since == nil) != (b.Since == nil):
			return a.Since != nil
		}
		if a.Subject.Namespace != b.Subject.Namespace {
			return a.Subject.Namespace < b.Subject.Namespace
		}
		return a.Subject.Name < b.Subject.Name
	})
}

func badge(n int, lowerBound bool) Count {
	if lowerBound && n == 0 {
		return Count{LowerBound: true}
	}
	return Count{Count: &n, LowerBound: lowerBound}
}

// Summarize builds the workspace from one request's reads.
func Summarize(in Input) Summary {
	b := &builder{
		in: in, sandboxes: indexOf(in.Sandboxes.Items), claims: indexOf(in.Claims.Items), pools: indexOf(in.WarmPools.Items),
		templates: indexOf(in.Templates.Items), represented: map[string]bool{},
	}
	out := Summary{Controller: in.Controller, Attention: []Attention{}, WarmPools: []WarmPoolRow{}, Expired: Expired{Recent: []ExpiredEntry{}}}

	var poolRows, sandboxRows, claimRows []Attention
	for _, p := range in.WarmPools.Items {
		row, folds := b.poolRow(p)
		if row != nil {
			poolRows = append(poolRows, *row)
		}
		for _, f := range folds {
			b.represented[f] = true
		}
	}
	var waiting []Ref
	for _, s := range in.Sandboxes.Items {
		if b.represented[key(s)] {
			continue
		}
		row, progress := b.sandboxRow(s)
		if row == nil {
			continue
		}
		b.represented[key(s)] = true
		if progress && in.Controller.down() {
			waiting = append(waiting, row.Subject)
			continue
		}
		sandboxRows = append(sandboxRows, *row)
	}
	for _, c := range in.Claims.Items {
		if row := b.claimRow(c); row != nil {
			claimRows = append(claimRows, *row)
		}
	}
	controller := b.controllerRow(waiting)
	var rows []Attention
	if controller != nil {
		rows = append(rows, *controller)
	}
	rows = append(append(append(rows, sandboxRows...), claimRows...), poolRows...)
	sortRows(rows)
	out.AttentionTotal = len(rows)
	if len(rows) > maxAttention {
		rows = rows[:maxAttention]
	}
	out.Attention = append(out.Attention, rows...)

	controllerUnread := in.Controller.State == ControllerUnread
	out.AttentionComplete = in.Sandboxes.Complete && in.Claims.Complete && in.WarmPools.Complete && in.Templates.Complete && !controllerUnread

	// A list's badge is a lower bound when a kind its rows rest on was read
	// in part: those reads only ever add rows.
	sandboxesPartial := !in.Sandboxes.Complete
	claimsPartial := !in.Claims.Complete || !in.WarmPools.Complete || !in.Templates.Complete
	poolsPartial := !in.WarmPools.Complete || !in.Templates.Complete
	out.Sidebar = Sidebar{
		Total:      badge(out.AttentionTotal, sandboxesPartial || claimsPartial || poolsPartial || controllerUnread),
		Sandboxes:  badge(len(sandboxRows), sandboxesPartial),
		Claims:     badge(len(claimRows), claimsPartial),
		WarmPools:  badge(len(poolRows), poolsPartial),
		Controller: controller != nil,
	}

	out.Counts = counts(in)
	out.WarmPools, out.WarmPoolsTotal = b.warmPoolRows()
	out.Expired = expired(in)
	return out
}

func counts(in Input) Counts {
	var c Counts
	if in.Sandboxes.Usable {
		s := &SandboxCounts{Total: len(in.Sandboxes.Items), LowerBound: !in.Sandboxes.Complete}
		for _, u := range in.Sandboxes.Items {
			r := health.ReadAgentSandbox(u)
			switch {
			case r.Verdict.Level == health.LevelHealthy:
				s.Running++
			case r.Verdict.Reason == "Terminating":
				s.Terminating++
			case r.Settled && r.Verdict.Reason == "Expired":
				s.Expired++
			case r.Settled && r.Verdict.Reason == "Finished":
				s.Finished++
			case r.SuspensionRefused:
				s.SuspensionBlocked++
			case r.Verdict.Reason == "Suspended" || r.Verdict.Reason == "Suspending":
				s.Suspended++
			case r.Verdict.Level == health.LevelUnhealthy || r.Verdict.Reason == "ReconcilerError":
				s.Failed++
			case r.Verdict.Level == health.LevelUnknown:
				s.Unknown++
			case r.InProgress && schedulerBlocked(u, r):
				s.Unschedulable++
			default:
				s.Starting++
			}
		}
		c.Sandboxes = s
	}
	if in.Claims.Usable {
		cl := &ClaimCounts{Total: len(in.Claims.Items), LowerBound: !in.Claims.Complete}
		for _, u := range in.Claims.Items {
			r := health.ReadAgentSandbox(u)
			switch {
			case r.Settled && r.Verdict.Reason == "Expired":
				cl.Expired++
			case ClaimSandbox(u) != "":
				cl.Bound++
			default:
				cl.Waiting++
			}
		}
		c.Claims = cl
	}
	if in.WarmPools.Usable {
		w := &WarmPoolCounts{Total: len(in.WarmPools.Items), LowerBound: !in.WarmPools.Complete}
		for _, u := range in.WarmPools.Items {
			if n, ok := nestedCount(u.Object, "status", "readyReplicas"); ok {
				w.Ready += n
			}
			if n, ok := nestedCount(u.Object, "spec", "replicas"); ok {
				w.Desired += n
			}
		}
		c.WarmPools = w
	}
	return c
}

func (b *builder) warmPoolRows() ([]WarmPoolRow, int) {
	served := map[string]*ClaimsServed{}
	for _, c := range b.in.Claims.Items {
		pool := ClaimWarmPool(c)
		if pool == "" {
			continue
		}
		k := c.GetNamespace() + "/" + pool
		if served[k] == nil {
			served[k] = &ClaimsServed{}
		}
		served[k].Total++
		if ClaimSandbox(c) != "" {
			served[k].Bound++
		}
	}
	rows := make([]WarmPoolRow, 0, len(b.in.WarmPools.Items))
	for _, u := range b.in.WarmPools.Items {
		v := ReadWarmPool(u)
		row := WarmPoolRow{Namespace: u.GetNamespace(), Name: u.GetName(), State: Reading{Text: v.Reason, Level: v.Level}, Created: u.GetCreationTimestamp().UTC()}
		row.Ready, _ = nestedCount(u.Object, "status", "readyReplicas")
		if d, ok := nestedCount(u.Object, "spec", "replicas"); ok {
			row.Desired = &d
		}
		if t := WarmPoolTemplate(u); t != "" {
			row.Template, row.TemplatePresence = t, b.presence(b.in.Templates, b.templates, row.Namespace, t)
		}
		if b.in.Claims.read(row.Namespace) {
			row.Claims = &ClaimsServed{}
			if s := served[key(u)]; s != nil {
				*row.Claims = *s
			}
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		a, c := rows[i], rows[j]
		if ra, rc := levelRank(a.State.Level), levelRank(c.State.Level); ra != rc {
			return ra < rc
		}
		if a.Namespace != c.Namespace {
			return a.Namespace < c.Namespace
		}
		return a.Name < c.Name
	})
	total := len(rows)
	if total > maxWarmPools {
		rows = rows[:maxWarmPools]
	}
	return rows, total
}

func levelRank(l health.Level) int {
	switch l {
	case health.LevelUnhealthy:
		return 0
	case health.LevelDegraded:
		return 1
	case health.LevelUnknown:
		return 2
	case health.LevelHealthy:
		return 3
	}
	return 4
}

func expired(in Input) Expired {
	var entries []ExpiredEntry
	collect := func(items []*unstructured.Unstructured, ref func(*unstructured.Unstructured) Ref) {
		for _, u := range items {
			r := health.ReadAgentSandbox(u)
			if !r.Settled || r.Verdict.Reason != "Expired" {
				continue
			}
			if t, ok := ShutdownTime(u); ok && in.Now.Sub(t) <= RecentExpiry {
				entries = append(entries, ExpiredEntry{Ref: ref(u), At: t.UTC()})
			}
		}
	}
	collect(in.Sandboxes.Items, sandboxRef)
	collect(in.Claims.Items, claimRef)
	sort.SliceStable(entries, func(i, j int) bool {
		if !entries[i].At.Equal(entries[j].At) {
			return entries[i].At.After(entries[j].At)
		}
		return entries[i].Ref.Name < entries[j].Ref.Name
	})
	out := Expired{Recent: []ExpiredEntry{}, RecentTotal: len(entries), LowerBound: !in.Sandboxes.Complete || !in.Claims.Complete}
	if len(entries) > maxRecent {
		entries = entries[:maxRecent]
	}
	out.Recent = append(out.Recent, entries...)
	return out
}
