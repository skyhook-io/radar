package substrate

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/skyhook-io/radar/pkg/timeutil"
)

// Severities of a needs-attention row.
const (
	SeverityCritical = "critical"
	SeverityWarning  = "warning"
	// SeverityInfo: expected and still within its budget, shown so the
	// operator knows it is in progress.
	SeverityInfo = "info"
)

var severityRank = map[string]int{SeverityCritical: 0, SeverityWarning: 1, SeverityInfo: 2}

// Needs-attention rules.
const (
	RuleCrashedActors     = "crashedActors"
	RuleStuckTransition   = "stuckTransition"
	RuleGoldenFailed      = "goldenSnapshotFailed"
	RuleNoMatchingPool    = "noMatchingPool"
	RuleMissingTemplate   = "missingTemplate"
	RuleWorkerUnavailable = "workerUnavailable"
	RuleWorkerDraining    = "workerDraining"
	RuleWorkerRestarting  = "workerRestarting"
	RulePoolShort         = "poolShort"
	RulePoolFull          = "poolFull"
	RuleUploadOverdue     = "uploadOverdue"
	// RuleNodeUploadsOverdue is atelet's own count of uploads past their
	// deadline, read from Prometheus.
	RuleNodeUploadsOverdue = "nodeUploadsOverdue"
)

// Thresholds a state must hold, at least, before it needs attention.
const (
	StuckAfter = 5 * time.Minute
	// UploadCriticalAfter is when an overdue upload becomes critical even
	// with its node ready.
	UploadCriticalAfter = 60 * time.Minute
	UnavailableAfter    = 5 * time.Minute
	// DefaultDrainGrace is the termination grace Substrate gives worker Pods
	// (workerpool_apply.go), used when Radar cannot read the Pod's own
	// deadline. The worker allows its applications 30 minutes of it.
	DefaultDrainGrace = 60 * time.Minute
	// UploadPendingAfter is how long a suspended actor with a pending newest
	// snapshot must have gone without an update before it is listed. It is
	// Radar's threshold on the actor's last change, not Substrate's upload
	// deadline, which atelet tracks (RuleNodeUploadsOverdue).
	UploadPendingAfter   = 10 * time.Minute
	attentionSampleLimit = 20
)

// Subject is what a row or link points at. Kind is Actor, ActorTemplate,
// Worker, WorkerPool, Node, Pod, or Actors (a filtered actor list, with
// Filter holding the /actors query parameters).
type Subject struct {
	Kind      string            `json:"kind"`
	Atespace  string            `json:"atespace,omitempty"`
	Namespace string            `json:"namespace,omitempty"`
	Name      string            `json:"name,omitempty"`
	Filter    map[string]string `json:"filter,omitempty"`
}

// Tally is a count of one thing: actors per template, crashes per message.
type Tally struct {
	Subject *Subject `json:"subject,omitempty"`
	Text    string   `json:"text,omitempty"`
	Count   int      `json:"count"`
	// Cause is what every crash in a template group shares, when Substrate's
	// messages name one: CauseMemoryLimit. Absent otherwise.
	Cause string `json:"cause,omitempty"`
	// NextStep and Command are the group's remedy when Cause names one.
	NextStep string `json:"nextStep,omitempty"`
	Command  string `json:"command,omitempty"`
}

// Attention is one needs-attention row.
type Attention struct {
	// ID is stable across reads for the same rule and subject.
	ID       string `json:"id"`
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	// Reading is what Radar read, in a sentence.
	Reading string `json:"reading"`
	// NextStep is one action in a short sentence.
	NextStep string `json:"nextStep"`
	// Command runs the next step, when one command does; absent otherwise. A
	// row covering several actors writes <name> and <atespace> for them;
	// ActorAttention fills them in.
	Command string  `json:"command,omitempty"`
	Subject Subject `json:"subject"`
	// Related are links beside the subject, such as a worker's Pod and node.
	Related []Subject `json:"related,omitempty"`
	// Count is how many things the row covers (actors, workers).
	Count int `json:"count"`
	// Since is the newest change among them, for ordering; it is a lower
	// bound on how long the condition has held.
	Since time.Time `json:"since,omitzero"`
	// Groups breaks the count down, largest first (crashes by template).
	Groups []Tally `json:"groups,omitempty"`
	// Messages are the most common Substrate messages, largest first.
	Messages []Tally `json:"messages,omitempty"`
	// Nodes breaks an upload row down by the node holding the files, most
	// actors first.
	Nodes []UploadNode `json:"nodes,omitempty"`
	// Sample is up to 20 of the actors covered, for display. Which actors a
	// row covers is ActorAttention's to say, not the sample's.
	Sample []Ref `json:"sample,omitempty"`

	// key is the rule's subject: the state, node, worker or template a
	// membership predicate compares against. epoch is a restarting worker's
	// registered epoch.
	key   string
	epoch int64
}

// Node states of an upload row's node, from Radar's Node cache.
const (
	NodeReady    = "ready"
	NodeNotReady = "notReady"
	// NodeGone: Radar's Node list was read and does not hold it.
	NodeGone = "gone"
	// NodeUnknown: Nodes were not read, or Substrate names no node.
	NodeUnknown = "unknown"
)

// UploadNode is one node's share of the snapshots not yet stored.
type UploadNode struct {
	// Node is empty when Substrate names none.
	Node  string `json:"node"`
	State string `json:"state"`
	Count int    `json:"count"`
	// Oldest is the earliest last update among them; the wait is at least
	// since then.
	Oldest time.Time `json:"oldest,omitzero"`
}

// Inputs is what the rules read. A false *Read means the list is unknown, and
// rules that need it are skipped rather than reading it as empty.
type Inputs struct {
	Actors        []Actor
	ActorsRead    bool
	Templates     []Template
	TemplatesRead bool
	Workers       []Worker
	WorkersRead   bool
	// WorkersComplete: every worker was read, which a rule needs before it
	// says a pool is short or has no free slot.
	WorkersComplete bool
	Pools           []Pool
	// TemplatesComplete: every template was read, none denied, so a template
	// an actor names and the list lacks does not exist.
	TemplatesComplete bool
	// PoolsUsable: some WorkerPools were read. PoolsComplete: all of them,
	// which a rule needs before it says no pool exists.
	PoolsUsable   bool
	PoolsComplete bool
	// NodeReady maps each Kubernetes Node to its Ready condition; nil when
	// Nodes were not read.
	NodeReady map[string]bool
	// WorkerPods are the worker Pods Radar's cache holds: the evidence for
	// whether Kubernetes or Substrate holds an unavailable worker back, and a
	// draining Pod's deadline.
	WorkerPods WorkerPods
	// Service is what Prometheus says about delivery; nil when not queried.
	Service *Service
	Now     time.Time
}

// Service is Substrate's own measurements of pools and nodes, from
// Prometheus. Pools and Nodes hold only subjects Radar asked about.
type Service struct {
	// Read: Prometheus was connected and queried. Why says why not.
	Read bool   `json:"read"`
	Why  string `json:"why,omitempty"`
	// ReadAt is when the answer was collected; it may be up to a minute old.
	ReadAt time.Time `json:"readAt,omitzero"`
	// Window is the range the resume and crash facts cover, such as 1h.
	Window string                 `json:"window,omitempty"`
	Pools  map[string]PoolService `json:"pools,omitempty"`
	Nodes  map[string]NodeService `json:"nodes,omitempty"`
}

// Pool service verdicts, the pool verdicts of Radar's Substrate metrics.
const (
	ServiceWorkerShortfall      = "workerShortfall"
	ServiceNoFreeActorSlots     = "noFreeActorSlots"
	ServiceFreeActorSlots       = "freeActorSlots"
	ServiceNoSchedulableWorkers = "noSchedulableWorkers"
	ServiceUploadsOverdue       = "uploadsOverdue"
	ServiceNoUploadsOverdue     = "noUploadsOverdue"
	ServiceUnknown              = "unknown"
)

// PoolService is one pool's verdict and facts. A nil fact is unknown: not
// collected, no series, or the query failed; Reason says which when the
// verdict is unknown.
type PoolService struct {
	Verdict string `json:"verdict"`
	Reason  string `json:"reason,omitempty"`
	// ResumeP95Seconds and FailedResumesPercent cover resumes placed on the
	// pool; a resume that fails before placement names no pool.
	ResumeP95Seconds     *float64 `json:"resumeP95Seconds,omitempty"`
	FailedResumesPercent *float64 `json:"failedResumesPercent,omitempty"`
	Crashes              *float64 `json:"crashes,omitempty"`
	// SchedulableWorkers out of DesiredWorkers, when the pool is short.
	SchedulableWorkers *float64 `json:"schedulableWorkers,omitempty"`
	DesiredWorkers     *float64 `json:"desiredWorkers,omitempty"`
}

// NodeService is one node's snapshot upload verdict.
type NodeService struct {
	Verdict string `json:"verdict"`
	Reason  string `json:"reason,omitempty"`
	// OverdueUploads is atelet's count of uploads past their deadline, by
	// band (1m_to_5m, 5m_to_15m, over_15m).
	OverdueUploads *float64           `json:"overdueUploads,omitempty"`
	Bands          map[string]float64 `json:"bands,omitempty"`
	// OldestUnsavedSeconds is the oldest actor state on the node not yet in
	// durable storage, whatever the actor's state.
	OldestUnsavedSeconds *float64 `json:"oldestUnsavedSeconds,omitempty"`
}

// ruleRank orders rows of one severity: what already broke actors first,
// what may lose state last.
var ruleRank = map[string]int{
	RuleCrashedActors: 0, RuleNoMatchingPool: 1, RuleMissingTemplate: 2,
	RuleWorkerUnavailable: 3, RuleWorkerDraining: 4, RuleWorkerRestarting: 5,
	RulePoolShort: 6, RulePoolFull: 7,
	RuleStuckTransition: 8, RuleGoldenFailed: 9, RuleNodeUploadsOverdue: 10, RuleUploadOverdue: 11,
}

// NeedsAttention applies every rule whose inputs were read (Checks lists
// them): critical first, then warning, then info, then by rule (ruleRank),
// then newest first.
func NeedsAttention(in Inputs) []Attention {
	out := []Attention{}
	if in.ActorsRead {
		out = append(out, crashedActors(in)...)
		out = append(out, stuckTransitions(in)...)
		out = append(out, uploadsOverdue(in)...)
		if in.TemplatesComplete {
			out = append(out, missingTemplates(in)...)
		}
	}
	if in.TemplatesRead {
		out = append(out, goldenFailed(in)...)
		if in.PoolsComplete {
			out = append(out, noMatchingPool(in)...)
		}
	}
	if in.WorkersRead {
		out = append(out, workerRows(in)...)
	}
	if in.PoolsUsable || in.WorkersRead || serviceRead(in) {
		out = append(out, poolRows(in)...)
	}
	if serviceRead(in) {
		out = append(out, nodeUploadRows(in)...)
	}
	slices.SortStableFunc(out, func(a, b Attention) int {
		if c := cmp.Compare(severityRank[a.Severity], severityRank[b.Severity]); c != 0 {
			return c
		}
		if c := cmp.Compare(ruleRank[a.Rule], ruleRank[b.Rule]); c != 0 {
			return c
		}
		if c := b.Since.Compare(a.Since); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func refName(r Ref) string { return r.Atespace + " / " + r.Name }

func tallies(counts map[string]int, subjects map[string]*Subject, limit int) []Tally {
	keys := slices.Collect(maps.Keys(counts))
	slices.SortFunc(keys, func(a, b string) int {
		if c := cmp.Compare(counts[b], counts[a]); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	if limit > 0 && len(keys) > limit {
		keys = keys[:limit]
	}
	out := make([]Tally, 0, len(keys))
	for _, k := range keys {
		t := Tally{Count: counts[k]}
		if s := subjects[k]; s != nil {
			t.Subject = s
		} else {
			t.Text = k
		}
		out = append(out, t)
	}
	return out
}

func newestFirst(actors []Actor) {
	slices.SortFunc(actors, func(a, b Actor) int {
		if c := b.UpdateTime.Compare(a.UpdateTime); c != 0 {
			return c
		}
		return strings.Compare(a.Atespace+"/"+a.Name, b.Atespace+"/"+b.Name)
	})
}

func sample(actors []Actor) []Ref {
	n := min(len(actors), attentionSampleLimit)
	out := make([]Ref, 0, n)
	for _, a := range actors[:n] {
		out = append(out, a.Ref())
	}
	return out
}

func crashTime(a Actor) time.Time {
	if a.Crash != nil && !a.Crash.Time.IsZero() {
		return a.Crash.Time
	}
	return a.UpdateTime
}

// Rule 1: crashed actors, one row, grouped by template.
func crashedActors(in Inputs) []Attention {
	var crashed []Actor
	byTemplate := map[string]int{}
	templateSubjects := map[string]*Subject{}
	messages := map[string]int{}
	// mixed holds the templates with a crash that is not a memory-limit kill.
	mixed := map[string]bool{}
	for _, a := range in.Actors {
		if a.State != StateCrashed {
			continue
		}
		crashed = append(crashed, a)
		key := a.Template.Atespace + "/" + a.Template.Name
		byTemplate[key]++
		templateSubjects[key] = &Subject{Kind: "ActorTemplate", Atespace: a.Template.Atespace, Name: a.Template.Name}
		msg := ""
		if a.Crash != nil {
			msg = a.Crash.Message
		}
		if !IsMemoryLimitCrash(a) {
			mixed[key] = true
		}
		if msg == "" {
			msg = "No crash message"
		}
		messages[msg]++
	}
	if len(crashed) == 0 {
		return nil
	}
	slices.SortFunc(crashed, func(a, b Actor) int {
		if c := crashTime(b).Compare(crashTime(a)); c != 0 {
			return c
		}
		return strings.Compare(a.Atespace+"/"+a.Name, b.Atespace+"/"+b.Name)
	})
	groups := tallies(byTemplate, templateSubjects, 0)
	allMemory := true
	for i := range groups {
		key := groups[i].Subject.Atespace + "/" + groups[i].Subject.Name
		if mixed[key] {
			allMemory = false
		} else {
			groups[i].Cause, groups[i].NextStep, groups[i].Command = CauseMemoryLimit, CrashedMemoryNextStep, retemplateCommand
		}
	}
	nextStep, command := crashedNextStep, revertCommand
	if allMemory {
		nextStep, command = CrashedMemoryNextStep, retemplateCommand
	}
	title := plural(len(crashed), "actor is crashed", "actors are crashed")
	if len(groups) > 1 && groups[0].Count*2 > len(crashed) {
		title += fmt.Sprintf(", %d of them from one template", groups[0].Count)
	}
	latest := crashed[0]
	reading := fmt.Sprintf("Latest: %s", refName(latest.Ref()))
	if latest.Crash != nil && latest.Crash.Message != "" {
		reading += ". Substrate's message: " + latest.Crash.Message
	}
	return []Attention{{
		ID: RuleCrashedActors, Rule: RuleCrashedActors, Severity: SeverityCritical,
		Title: title, Reading: reading,
		NextStep: nextStep, Command: command,
		Subject: Subject{Kind: "Actors", Filter: map[string]string{"state": StateCrashed}},
		Count:   len(crashed), Since: crashTime(latest),
		Groups: groups, Messages: tallies(messages, nil, 3), Sample: sample(crashed),
	}}
}

// Crash causes Radar can name from Substrate's message.
const (
	// CauseMemoryLimit: the worker killed the sandbox for exceeding the
	// actor's memory limit.
	CauseMemoryLimit = "memoryLimit"
)

// memoryLimitMessage is the worker's crash report for a sandbox it killed
// over its memory limit, alone or wrapping the exit error.
const memoryLimitMessage = "killed for exceeding its memory limit"

// IsMemoryLimitCrash reports a crashed actor whose worker killed it for
// exceeding its memory limit.
func IsMemoryLimitCrash(a Actor) bool {
	return a.State == StateCrashed && a.Crash != nil && strings.Contains(a.Crash.Message, memoryLimitMessage)
}

// The crashed next steps offer only what can be done after a crash: Substrate
// clears the worker assignment, so no worker holds the actor's output.
const (
	crashedNextStep = "Fix the cause Substrate's crash message names, then revert each actor to its newest stored generation."
	// CrashedMemoryNextStep is the step for memory-limit kills. Templates
	// cannot be edited, but an actor's template can be changed. A snapshot
	// stamped with the template it was taken under restores only its volumes
	// under another (Substrate's templateReplaced).
	CrashedMemoryNextStep = "Move the actors to a template with more memory, then revert them; on the new template their stored snapshots restore volumes only."
	// crashedActorNextStep is crashedNextStep on one actor's own page.
	crashedActorNextStep = "Fix the cause Substrate's crash message names, then revert this actor to its newest stored generation."
)

// Commands of the next steps, in kubectl ate's and kubectl's own syntax.
const (
	revertCommand     = "kubectl ate revert actor <name> -a <atespace>"
	retemplateCommand = "kubectl ate update actor <name> -a <atespace> --template <new-template>"
	resumeCommand     = "kubectl ate resume actor <name> -a <atespace>"
	suspendCommand    = "kubectl ate suspend actor <name> -a <atespace>"
	apiServerLogs     = "kubectl logs -n ate-system deploy/ate-api-server"
	controllerLogs    = "kubectl logs -n ate-system deploy/ate-controller"
	describePodFmt    = "kubectl describe pod %s -n %s"
	getPodFmt         = "kubectl get pod %s -n %s"
)

// CrashedMemoryActorStep is the memory-limit remedy on a's own page, worded
// by the snapshot its revert keeps (RecoverySource) and what that snapshot
// restores under the new template. It names that condition: the actor page
// also says how the snapshot resumes as things stand.
func CrashedMemoryActorStep(a Actor) string {
	const move = "Move this actor to a template with more memory, then revert it; "
	src := RecoverySource(a)
	switch {
	case src == nil:
		return move + "it has no stored snapshot, so it boots fresh."
	case src.Fidelity == FidelityVolumes:
		return move + "on the new template it resumes from its volumes."
	case src.ActorTemplateUID != "" && src.Fidelity == FidelityMemory:
		return move + "on the new template it resumes from volumes, without memory or files."
	case src.ActorTemplateUID != "" && src.Fidelity == FidelityRootFS:
		return move + "on the new template it resumes from volumes, without its saved files."
	case src.ActorTemplateUID == "" && src.Fidelity == FidelityRootFS:
		return move + "on the new template it resumes from its saved files."
	}
	return move + "on the new template it resumes from its stored snapshot."
}

// CrashedMemoryCommand is the command of the memory-limit step for a: moving
// it to the new template, which the user names.
func CrashedMemoryCommand(a Actor) string { return forActor(retemplateCommand, a) }

func forActor(command string, a Actor) string {
	return strings.NewReplacer("<name>", a.Name, "<atespace>", a.Atespace).Replace(command)
}

// stuckNextStep follows what each state holds: resuming and suspending actors
// keep a worker; reverting and deleting ones have none to read.
var stuckNextStep = map[string]string{
	StateResuming:   "Resume each again: Substrate continues an unfinished resume only on the next resume call. If it stalls again, read its worker's log.",
	StateSuspending: "Suspend each again: Substrate continues an unfinished suspend only on the next suspend call. If it stalls again, read its worker's log.",
	StateReverting:  "Read the Substrate API server's log for the revert; a reverting actor has no worker.",
	StateDeleting:   "Read the Substrate API server's log for the delete; a deleting actor has no worker.",
}

// stuckActorNextStep is stuckNextStep on one actor's own page.
var stuckActorNextStep = map[string]string{
	StateResuming:   "Resume it again: Substrate continues an unfinished resume only on the next resume call. If it stalls again, read its worker's log.",
	StateSuspending: "Suspend it again: Substrate continues an unfinished suspend only on the next suspend call. If it stalls again, read its worker's log.",
	StateReverting:  stuckNextStep[StateReverting],
	StateDeleting:   stuckNextStep[StateDeleting],
}

var stuckCommand = map[string]string{
	StateResuming: resumeCommand, StateSuspending: suspendCommand,
	StateReverting: apiServerLogs, StateDeleting: apiServerLogs,
}

// stuckWhere groups a stuck state's actors by the worker they wait on
// (WaitingOn), largest first, and says it in a sentence: "2 placed and
// restoring on agent-ns/worker-1; 1 on agent-ns/worker-3."
func stuckWhere(state string, actors []Actor) ([]Tally, string) {
	counts := map[string]int{}
	subjects := map[string]*Subject{}
	unrecorded := 0
	for _, a := range actors {
		w := WaitingOn(a)
		if w == nil || w.Worker == nil {
			if state == StateResuming || state == StateSuspending {
				unrecorded++
			}
			continue
		}
		key := w.Worker.Namespace + "/" + w.Worker.Pod
		counts[key]++
		subjects[key] = &Subject{Kind: "Worker", Namespace: w.Worker.Namespace, Name: w.Worker.Pod}
	}
	if len(counts) == 0 && unrecorded == 0 {
		return nil, ""
	}
	groups := tallies(counts, subjects, 0)
	verb := map[string]string{StateResuming: "placed and restoring", StateSuspending: "checkpointing"}[state]
	// One namespace is said once, not before every Pod.
	namespaces := map[string]bool{}
	for _, g := range groups {
		namespaces[g.Subject.Namespace] = true
	}
	var parts []string
	for i, g := range groups {
		// Four or more workers: three by name, then the rest counted. A
		// fourth alone is named rather than called "1 more worker".
		if i == 3 && len(groups) > 4 {
			parts = append(parts, fmt.Sprintf("%d on %d more workers", sumCounts(groups[3:]), len(groups)-3))
			break
		}
		name := g.Subject.Name
		if len(namespaces) > 1 {
			name = g.Subject.Namespace + "/" + name
		}
		if i == 0 {
			parts = append(parts, fmt.Sprintf("%d %s on %s", g.Count, verb, name))
		} else {
			parts = append(parts, fmt.Sprintf("%d on %s", g.Count, name))
		}
	}
	if unrecorded > 0 {
		parts = append(parts, fmt.Sprintf("%d with no worker recorded", unrecorded))
	}
	where := strings.Join(parts, "; ")
	if len(namespaces) == 1 {
		for ns := range namespaces {
			where += " (namespace " + ns + ")"
		}
	}
	return groups, where + ". "
}

func sumCounts(ts []Tally) int {
	n := 0
	for _, t := range ts {
		n += t.Count
	}
	return n
}

// Rule 2: actors held in a transition longer than StuckAfter, one row per state.
func stuckTransitions(in Inputs) []Attention {
	byState := map[string][]Actor{}
	for _, a := range in.Actors {
		if stuck(a, in.Now) {
			byState[a.State] = append(byState[a.State], a)
		}
	}
	var out []Attention
	for _, state := range slices.Sorted(maps.Keys(byState)) {
		actors := byState[state]
		newestFirst(actors)
		oldest := actors[len(actors)-1]
		groups, where := stuckWhere(state, actors)
		out = append(out, Attention{
			ID: RuleStuckTransition + ":" + state, Rule: RuleStuckTransition, Severity: SeverityWarning, key: state,
			Title:    fmt.Sprintf("%s %s for more than %s", plural(len(actors), "actor has been", "actors have been"), state, timeutil.FormatAgeShort(StuckAfter)),
			Reading:  fmt.Sprintf("%sThe longest, %s, has been %s for at least %s.", where, refName(oldest.Ref()), state, timeutil.FormatAgeShort(HeldFor(oldest.UpdateTime, in.Now))),
			NextStep: stuckNextStep[state], Command: stuckCommand[state],
			Subject: Subject{Kind: "Actors", Filter: map[string]string{"state": state}},
			Count:   len(actors), Since: actors[0].UpdateTime, Sample: sample(actors), Groups: groups,
		})
	}
	return out
}

// IsStuck reports an actor held in a transition longer than StuckAfter, the
// stuckTransition rule's own test.
func IsStuck(a Actor, now time.Time) bool { return stuck(a, now) }

func stuck(a Actor, now time.Time) bool {
	return transitional[a.State] && HeldFor(a.UpdateTime, now) > StuckAfter
}

// uploadOverdue reports a suspended actor whose newest snapshot is pending
// upload and which was last updated more than UploadPendingAfter ago, and the
// node holding its files.
func uploadOverdue(a Actor, now time.Time) (string, bool) {
	if a.State != StateSuspended {
		return "", false
	}
	s := NewestSnapshot(a)
	if s == nil || s.Durable != DurablePending || HeldFor(a.UpdateTime, now) <= UploadPendingAfter {
		return "", false
	}
	return cmp.Or(s.Locality, ActorNode(a)), true
}

// Rule 8: suspended actors whose newest snapshot is pending upload, last
// updated more than UploadPendingAfter ago, one row with a per-node
// breakdown. A warning, critical when a node holding the only copy is
// NotReady or gone, or the last update is over UploadCriticalAfter ago. It
// reads the API only: an actor that keeps resuming and suspending resets its
// update time and never shows here, which is what atelet's own durability
// clock (RuleNodeUploadsOverdue) covers.
func uploadsOverdue(in Inputs) []Attention {
	var actors []Actor
	byNode := map[string]*UploadNode{}
	for _, a := range in.Actors {
		node, ok := uploadOverdue(a, in.Now)
		if !ok {
			continue
		}
		actors = append(actors, a)
		n := byNode[node]
		if n == nil {
			n = &UploadNode{Node: node, State: nodeState(in.NodeReady, node)}
			byNode[node] = n
		}
		n.Count++
		if n.Oldest.IsZero() || a.UpdateTime.Before(n.Oldest) {
			n.Oldest = a.UpdateTime
		}
	}
	if len(actors) == 0 {
		return nil
	}
	newestFirst(actors)
	nodes := make([]UploadNode, 0, len(byNode))
	for _, n := range byNode {
		nodes = append(nodes, *n)
	}
	slices.SortFunc(nodes, func(a, b UploadNode) int {
		if c := cmp.Compare(b.Count, a.Count); c != 0 {
			return c
		}
		return strings.Compare(a.Node, b.Node)
	})
	oldestWait := HeldFor(actors[len(actors)-1].UpdateTime, in.Now)

	title := "1 suspended actor's newest snapshot is pending upload"
	if len(actors) > 1 {
		title = fmt.Sprintf("%d suspended actors' newest snapshots are pending upload", len(actors))
	}
	reading := fmt.Sprintf("Each is suspended with its newest snapshot pending upload and was last updated at least %s ago; the oldest last update was %s ago. Substrate does not record when an upload started. Until one is stored, that state is only on its node.", timeutil.FormatAgeShort(UploadPendingAfter), timeutil.FormatAgeShort(oldestWait))
	severity := SeverityWarning
	var lost []string
	for _, n := range nodes {
		switch n.State {
		case NodeNotReady:
			lost = append(lost, n.Node+" is NotReady")
		case NodeGone:
			lost = append(lost, n.Node+" is gone")
		}
	}
	switch {
	case len(lost) > 0:
		severity = SeverityCritical
		reading += " Node " + strings.Join(lost, ", node ") + ", so the snapshots it holds may be lost."
	case oldestWait > UploadCriticalAfter:
		severity = SeverityCritical
		reading += " The oldest last update is over an hour ago."
	}
	return []Attention{{
		ID: RuleUploadOverdue, Rule: RuleUploadOverdue, Severity: severity,
		Title:    title,
		Reading:  reading,
		NextStep: "Keep these nodes until the uploads finish, and check that each node's atelet can reach object storage.",
		Subject:  Subject{Kind: "Actors", Filter: map[string]string{"state": StateSuspended}},
		Count:    len(actors), Since: actors[0].UpdateTime, Nodes: nodes, Sample: sample(actors),
	}}
}

func nodeState(ready map[string]bool, node string) string {
	if ready == nil || node == "" {
		return NodeUnknown
	}
	r, ok := ready[node]
	switch {
	case !ok:
		return NodeGone
	case r:
		return NodeReady
	}
	return NodeNotReady
}

// Rule 3: templates whose golden snapshot failed terminally.
func goldenFailed(in Inputs) []Attention {
	var out []Attention
	for _, t := range in.Templates {
		g := GoldenOf(t)
		if g.State != GoldenFailed {
			continue
		}
		step := "Fix the cause in Substrate's message, then recreate the template: templates can't be edited, and each new one gets a golden snapshot build."
		if strings.Contains(g.Error, memoryLimitMessage) {
			step = "Recreate the template with a higher memory limit: its golden actor was killed for exceeding it, and templates can't be edited."
		}
		out = append(out, Attention{
			ID: RuleGoldenFailed + ":" + t.Atespace + "/" + t.Name, Rule: RuleGoldenFailed, Severity: SeverityWarning,
			Title:    fmt.Sprintf("%s has no golden snapshot, so new actors using the template's default boot cold", refName(t.Ref())),
			Reading:  "Substrate's message: " + GoldenMessage(g.Error) + ". Substrate does not retry a failed build.",
			NextStep: step,
			Subject:  Subject{Kind: "ActorTemplate", Atespace: t.Atespace, Name: t.Name},
			Count:    1, Since: t.UpdateTime,
		})
	}
	return out
}

// GoldenMessage is a golden snapshot error without Substrate's reason code:
// its template reconciler records "<Reason>: <message>".
func GoldenMessage(err string) string {
	reason, msg, ok := strings.Cut(err, ": ")
	if !ok || reason == "" || strings.ContainsAny(reason, " .") || reason[0] < 'A' || reason[0] > 'Z' {
		return err
	}
	return msg
}

// Rule 4: templates whose selector matches no known pool.
func noMatchingPool(in Inputs) []Attention {
	pools := KnownPools(in.Pools, in.Workers)
	waiting := map[Ref]int{}
	if in.ActorsRead {
		for _, a := range in.Actors {
			if a.State != StateRunning {
				waiting[a.Template]++
			}
		}
	}
	var out []Attention
	for _, t := range in.Templates {
		if len(MatchingPools(t, pools)) > 0 {
			continue
		}
		severity := SeverityWarning
		reading := "No pool's labels match its worker selector, so Substrate can place none of its actors."
		if GoldenOf(t).State == GoldenBuilding {
			reading += " Its golden snapshot cannot be built either: the build runs on a worker too."
		}
		if n := waiting[t.Ref()]; n > 0 {
			severity = SeverityCritical
			reading += fmt.Sprintf(" %s of it %s not running.", plural(n, "actor", "actors"), map[bool]string{true: "is", false: "are"}[n == 1])
		}
		out = append(out, Attention{
			ID: RuleNoMatchingPool + ":" + t.Atespace + "/" + t.Name, Rule: RuleNoMatchingPool, Severity: severity, key: t.Atespace + "/" + t.Name,
			Title:    fmt.Sprintf("No pool matches the selector of %s", refName(t.Ref())),
			Reading:  reading,
			NextStep: "Label a pool to match the template's worker selector, or recreate the template with a selector a pool matches.",
			Subject:  Subject{Kind: "ActorTemplate", Atespace: t.Atespace, Name: t.Name},
			Count:    1, Since: t.UpdateTime,
		})
	}
	return out
}

// actorsByWorker groups the actors holding a worker by its name, newest
// first, in one pass.
func actorsByWorker(actors []Actor) map[string][]Actor {
	out := map[string][]Actor{}
	for _, a := range actors {
		if a.Assignment != nil && a.Assignment.Worker != "" {
			out[a.Assignment.Worker] = append(out[a.Assignment.Worker], a)
		}
	}
	for _, as := range out {
		newestFirst(as)
	}
	return out
}

// workerActors links to the actors on w that its state affects that way.
func workerActors(w Worker, impact string) Subject {
	return Subject{Kind: "Actors", Filter: map[string]string{"worker": w.Pod, "impact": impact}}
}

// LostActors are the actors assigned to w that ran under an earlier process
// of it (ImpactRestarted): their sandboxes are gone.
func LostActors(w Worker, assigned []Actor) []Actor {
	var lost []Actor
	for _, a := range assigned {
		if WorkerImpactKind(a, w) == ImpactRestarted {
			lost = append(lost, a)
		}
	}
	return lost
}

func workerSubjects(w Worker) (Subject, []Subject) {
	related := []Subject{{Kind: "Pod", Namespace: w.Namespace, Name: w.Pod}, {Kind: "WorkerPool", Namespace: w.Namespace, Name: w.Pool}}
	if w.Node != "" {
		related = append(related, Subject{Kind: "Node", Name: w.Node})
	}
	return Subject{Kind: "Worker", Namespace: w.Namespace, Name: w.Pod}, related
}

// Rules 5 and 6: workers unavailable or draining too long, and workers whose
// process restarted before Substrate released the actors it ran.
func workerRows(in Inputs) []Attention {
	var out []Attention
	var byWorker map[string][]Actor
	if in.ActorsRead {
		byWorker = actorsByWorker(in.Actors)
	}
	for _, w := range in.Workers {
		subject, related := workerSubjects(w)
		held := HeldFor(w.UpdateTime, in.Now)
		assigned := byWorker[w.Name]
		switch {
		case w.State == WorkerUnavailable && held > UnavailableAfter:
			severity := SeverityWarning
			podState := in.WorkerPods.PodStateOf(w)
			title := fmt.Sprintf("Worker %s has been unavailable for at least %s", w.Pod, timeutil.FormatAgeShort(held))
			reading := workerUnavailableCause[podState] + " Substrate places no new actors on it."
			rowRelated := related
			if len(assigned) > 0 {
				severity = SeverityCritical
				title = fmt.Sprintf("%s still assigned to %s, unavailable for at least %s", plural(len(assigned), "actor is", "actors are"), w.Pod, timeutil.FormatAgeShort(held))
				reading = fmt.Sprintf("%s still assigned to it. ", plural(len(assigned), "actor is", "actors are")) + reading
				rowRelated = append([]Subject{workerActors(w, ImpactUnavailable)}, related...)
			}
			step := workerUnavailableStep[podState]
			out = append(out, Attention{
				ID: RuleWorkerUnavailable + ":" + w.Name, Rule: RuleWorkerUnavailable, Severity: severity, key: w.Name,
				Title:    title,
				Reading:  reading,
				NextStep: step.text, Command: step.command(w),
				Subject: subject, Related: rowRelated, Count: max(len(assigned), 1), Since: w.UpdateTime, Sample: sample(assigned),
			})
		case w.State == WorkerDraining && len(assigned) > 0:
			out = append(out, drainingRow(in, w, held, assigned, subject, related))
		}
		if w.RegisteredEpoch > w.ObservedEpoch {
			lost := LostActors(w, assigned)
			row := Attention{
				ID: RuleWorkerRestarting + ":" + w.Name, Rule: RuleWorkerRestarting, Severity: SeverityWarning, key: w.Name, epoch: w.RegisteredEpoch,
				Title:    fmt.Sprintf("Worker %s's registration is being reconciled", w.Pod),
				Reading:  fmt.Sprintf("Substrate has not yet reconciled the worker's registration (registered epoch %d, observed %d). No actor still assigned ran under the earlier process.", w.RegisteredEpoch, w.ObservedEpoch),
				NextStep: "Wait a few minutes for Substrate to reconcile the worker; if it does not, read the Substrate API server's log.",
				Command:  apiServerLogs,
				Subject:  subject, Related: related, Count: 1, Since: w.UpdateTime,
			}
			if !in.ActorsRead {
				row.Reading = fmt.Sprintf("Substrate has not yet reconciled the worker's registration (registered epoch %d, observed %d). Actors were not read, so Radar cannot say which ran under the earlier process.", w.RegisteredEpoch, w.ObservedEpoch)
			}
			// The actors' sandboxes are already gone; Substrate only records it.
			if len(lost) > 0 {
				row.Severity = SeverityCritical
				row.Title = fmt.Sprintf("%s when %s restarted", plural(len(lost), "actor lost its sandbox", "actors lost their sandboxes"), w.Pod)
				row.Reading = fmt.Sprintf("%s under an earlier process of the worker, replaced at epoch %d, so %s. Substrate marks them crashed when it reconciles the worker (observed epoch %d); until then they may still show running.",
					plural(len(lost), "actor ran", "actors ran"), w.RegisteredEpoch, map[bool]string{true: "its sandbox is lost", false: "their sandboxes are lost"}[len(lost) == 1], w.ObservedEpoch)
				row.NextStep = "Revert each to its newest stored generation now; Substrate marks them crashed when it reconciles the worker anyway."
				row.Command = revertCommand
				row.Subject = workerActors(w, ImpactRestarted)
				row.Related = append([]Subject{subject}, related...)
				row.Count, row.Sample = len(lost), sample(lost)
			}
			out = append(out, row)
		}
	}
	return out
}

// workerUnavailableCause says, from the worker's Pod, which side holds an
// unavailable worker back: Substrate's controller marks a worker unavailable
// when its Pod stops being Ready with an IP, and available once it is again.
var workerUnavailableCause = map[string]string{
	PodNotReady: "Its Pod is not Ready, so Kubernetes is the cause.",
	PodNoIP:     "Its Pod has no IP, so Kubernetes is the cause.",
	PodReady:    "Its Pod is Ready with an IP, so Kubernetes is not the cause: Substrate's controller has not marked the worker available again.",
	PodUnread:   "Radar could not read its Pod, so it cannot tell whether the Pod is not Ready or Substrate's controller has not marked the worker available.",
}

type workerStep struct {
	text string
	// format takes the Pod's name and namespace; a fixed command has none.
	format string
}

func (s workerStep) command(w Worker) string {
	if strings.Contains(s.format, "%s") {
		return fmt.Sprintf(s.format, w.Pod, w.Namespace)
	}
	return s.format
}

var workerUnavailableStep = map[string]workerStep{
	PodNotReady: {"Find why the worker Pod is not Ready in its events and container statuses.", describePodFmt},
	PodNoIP:     {"Find why the worker Pod has no IP in its events.", describePodFmt},
	PodReady:    {"Read Substrate's controller log: it marks a worker available once its Pod is Ready with an IP.", controllerLogs},
	PodUnread:   {"Check whether the worker Pod is Ready; if it is, read Substrate's controller log.", getPodFmt},
}

// PoolSettleAfter is how long a new pool has to bring up its workers before a
// shortfall is critical.
const PoolSettleAfter = 5 * time.Minute

// convergingReasons are pkg/health's WorkerPool readings of a controller still
// working toward the spec, not stuck.
var convergingReasons = map[string]string{"Pending": "pending", "Resolving": "resolving its sandbox classes", "Scaling": "scaling", "Scaling down": "scaling down"}

// poolConverging reports a pool whose shortfall is expected for now: its
// health says it is converging, or it is younger than PoolSettleAfter.
func poolConverging(p Pool, now time.Time) (bool, string) {
	if why, ok := convergingReasons[p.HealthReason]; ok {
		return true, why
	}
	if !p.CreateTime.IsZero() && HeldFor(p.CreateTime, now) < PoolSettleAfter {
		return true, "less than " + timeutil.FormatAgeShort(PoolSettleAfter) + " old"
	}
	return false, ""
}

// Rule 7: pools short of workers or with no free actor slots, from the
// inventory and, when Prometheus was read, Substrate's own pool verdicts. One
// row per pool: a verdict confirms an inventory row or stands alone.
func poolRows(in Inputs) []Attention {
	var out []Attention
	var svc map[string]PoolService
	if serviceRead(in) {
		svc = in.Service.Pools
	}
	for _, r := range JoinPools(in.Pools, in.Workers, in.WorkersRead, in.WorkersComplete, in.Actors, in.ActorsRead) {
		subject := Subject{Kind: "WorkerPool", Namespace: r.Namespace, Name: r.Name}
		name := r.Namespace + " / " + r.Name
		ps, hasSvc := svc[r.Ref().Key()]
		var row *Attention
		switch r.Capacity {
		case CapacityShort:
			desired := *r.Kubernetes.Desired
			taking := r.Kubernetes.ReadyWorkers()
			source := "ready"
			if r.Placement != nil && r.Placement.Complete {
				taking = int64(r.Placement.Workers.Active)
				source = "active"
			}
			severity := SeverityWarning
			reading := fmt.Sprintf("%d of %d desired workers are %s.", taking, desired, source)
			if pods := r.Kubernetes.Pods; pods != nil {
				if int64(pods.Created) < desired {
					reading += fmt.Sprintf(" Kubernetes has created %d of %d worker Pods.", pods.Created, desired)
				}
				if int64(pods.Ready) != r.Kubernetes.Ready {
					reading += fmt.Sprintf(" Radar holds %d ready worker Pods; the WorkerPool's status says %d.", pods.Ready, r.Kubernetes.Ready)
				}
			}
			reading += " New and resuming actors may wait or fail to start."
			if converging, why := poolConverging(*r.Kubernetes, in.Now); converging {
				reading += " The pool is " + why + ", so this may be temporary."
			} else if taking == 0 {
				severity = SeverityCritical
			}
			row = &Attention{
				ID: RulePoolShort + ":" + r.Ref().Key(), Rule: RulePoolShort, Severity: severity,
				Title:    name + " is short of workers",
				Reading:  reading,
				NextStep: "Open the pool: compare its worker Pods with Substrate's worker states to see which side is missing.",
				Subject:  subject, Count: int(desired - taking),
			}
		case CapacityFull:
			row = &Attention{
				ID: RulePoolFull + ":" + r.Ref().Key(), Rule: RulePoolFull, Severity: SeverityWarning,
				Title:    name + " has no free actor slots",
				Reading:  fmt.Sprintf("None of its %s has a free actor slot, so Substrate places no more actors on it. New actors whose selectors match only this pool cannot start.", plural(r.Placement.Workers.Active, "active worker", "active workers")),
				NextStep: "Add workers to the pool, or let templates select another pool.",
				Subject:  subject, Count: r.Placement.Workers.Active,
			}
		}
		if hasSvc {
			row = withPoolVerdict(row, ps, name, subject, r.Ref())
		}
		if row != nil {
			out = append(out, *row)
		}
	}
	return out
}

// withPoolVerdict folds Substrate's pool verdict into the inventory row, or
// makes a row from a definite verdict the inventory did not raise.
func withPoolVerdict(row *Attention, ps PoolService, name string, subject Subject, ref PoolRef) *Attention {
	switch ps.Verdict {
	case ServiceWorkerShortfall:
		confirm := "Substrate's own capacity alert condition has held for 5 minutes: fewer schedulable workers than desired"
		if ps.SchedulableWorkers != nil && ps.DesiredWorkers != nil {
			confirm += fmt.Sprintf(" (%.0f of %.0f)", *ps.SchedulableWorkers, *ps.DesiredWorkers)
		}
		confirm += "."
		if row != nil && row.Rule == RulePoolShort {
			row.Reading += " " + confirm
			return row
		}
		count := 1
		if ps.SchedulableWorkers != nil && ps.DesiredWorkers != nil && *ps.DesiredWorkers > *ps.SchedulableWorkers {
			count = int(*ps.DesiredWorkers - *ps.SchedulableWorkers)
		}
		return &Attention{
			ID: RulePoolShort + ":" + ref.Key(), Rule: RulePoolShort, Severity: SeverityWarning,
			Title: name + " is short of workers", Reading: confirm + " New and resuming actors may wait or fail to start.",
			NextStep: "Open the pool: its charts show which workers Substrate counts and when the shortfall began.",
			Subject:  subject, Count: count,
		}
	case ServiceNoSchedulableWorkers:
		if row != nil {
			row.Reading += " Substrate's API servers count no schedulable worker of a known sandbox class in it."
			row.Severity = SeverityCritical
			return row
		}
		return &Attention{
			ID: RulePoolShort + ":" + ref.Key(), Rule: RulePoolShort, Severity: SeverityCritical,
			Title:    name + " has no schedulable workers",
			Reading:  "Substrate's API servers count no schedulable worker of a known sandbox class in it, so Substrate places nothing on it.",
			NextStep: "Open the pool: compare its worker Pods with Substrate's worker states.",
			Subject:  subject, Count: 1,
		}
	case ServiceNoFreeActorSlots:
		if row != nil {
			if row.Rule == RulePoolFull {
				row.Reading += " Substrate's API servers agree: none sees a free slot."
			}
			return row
		}
		return &Attention{
			ID: RulePoolFull + ":" + ref.Key(), Rule: RulePoolFull, Severity: SeverityWarning,
			Title:    name + " has no free actor slots",
			Reading:  "No Substrate API server sees a free actor slot on its workers. New actors whose selectors match only this pool cannot start.",
			NextStep: "Add workers to the pool, or let templates select another pool.",
			Subject:  subject, Count: 1,
		}
	}
	return row
}

func serviceRead(in Inputs) bool { return in.Service != nil && in.Service.Read }

// RuleNodeUploadsOverdue: nodes whose atelet counts snapshot uploads past
// their deadline. atelet's clock runs from when the state stopped being
// durable, across suspend and resume cycles, so it covers active actors the
// API-only upload rule cannot.
func nodeUploadRows(in Inputs) []Attention {
	var out []Attention
	for _, node := range slices.Sorted(maps.Keys(in.Service.Nodes)) {
		ns := in.Service.Nodes[node]
		if ns.Verdict != ServiceUploadsOverdue || ns.OverdueUploads == nil {
			continue
		}
		n := int(*ns.OverdueUploads)
		reading := fmt.Sprintf("atelet on this node counts %s past their deadline", plural(n, "snapshot upload", "snapshot uploads"))
		if len(ns.Bands) > 0 {
			var parts []string
			for _, b := range []string{"over_15m", "5m_to_15m", "1m_to_5m"} {
				if v := ns.Bands[b]; v > 0 {
					parts = append(parts, fmt.Sprintf("%.0f %s", v, strings.ReplaceAll(strings.ReplaceAll(b, "_to_", "–"), "_", " ")))
				}
			}
			if len(parts) > 0 {
				reading += " (" + strings.Join(parts, ", ") + " overdue)"
			}
		}
		reading += ". Until they are stored, that actor state is only on this node."
		if ns.OldestUnsavedSeconds != nil {
			reading += " The oldest state not yet stored is " + timeutil.FormatAgeShort(time.Duration(*ns.OldestUnsavedSeconds*float64(time.Second))) + " old."
		}
		severity := SeverityWarning
		switch nodeState(in.NodeReady, node) {
		case NodeNotReady:
			severity, reading = SeverityCritical, reading+" The node is NotReady."
		case NodeGone:
			severity, reading = SeverityCritical, reading+" The node is gone."
		}
		out = append(out, Attention{
			ID: RuleNodeUploadsOverdue + ":" + node, Rule: RuleNodeUploadsOverdue, Severity: severity, key: node,
			Title:    "Snapshot uploads are overdue on " + node,
			Reading:  reading,
			NextStep: "Keep the node until its uploads finish, and check that its atelet can reach object storage: open the node's Substrate charts.",
			Subject:  Subject{Kind: "Node", Name: node}, Count: n,
		})
	}
	return out
}

// RuleMissingTemplate: actors naming a template Substrate does not have.
// Resuming one fails before its state changes, so it stays as it is and no
// other rule sees it. Only on a complete template read: a denied or partial
// list cannot tell a missing template from one Radar did not see.
func missingTemplates(in Inputs) []Attention {
	have := map[Ref]bool{}
	for _, t := range in.Templates {
		have[t.Ref()] = true
	}
	byTemplate := map[Ref][]Actor{}
	for _, a := range in.Actors {
		if !have[a.Template] {
			byTemplate[a.Template] = append(byTemplate[a.Template], a)
		}
	}
	var out []Attention
	for _, ref := range slices.SortedFunc(maps.Keys(byTemplate), func(a, b Ref) int { return strings.Compare(a.Atespace+"/"+a.Name, b.Atespace+"/"+b.Name) }) {
		actors := byTemplate[ref]
		newestFirst(actors)
		out = append(out, Attention{
			ID: RuleMissingTemplate + ":" + ref.Atespace + "/" + ref.Name, Rule: RuleMissingTemplate, Severity: SeverityWarning, key: ref.Atespace + "/" + ref.Name,
			Title:    fmt.Sprintf("%s %s, which Substrate does not have", plural(len(actors), "actor uses template", "actors use template"), refName(ref)),
			Reading:  "Substrate refuses to resume an actor whose template is gone (FailedPrecondition) without changing its state, so these stay as they are. Deleting a template does not delete its actors.",
			NextStep: "Recreate the template under the same atespace and name, or delete the actors.",
			Subject:  Subject{Kind: "Actors", Filter: map[string]string{"template": ref.Atespace + "/" + ref.Name}},
			Count:    len(actors), Since: actors[0].UpdateTime, Sample: sample(actors),
		})
	}
	return out
}

// drainingRow reads a draining worker that still holds actors. Draining only
// stops new placements: Substrate does not suspend or move the actors. They
// run until their applications shut down or someone suspends or deletes
// them; when the Pod is gone Substrate crashes any still assigned. So it is
// informational until the Pod's termination deadline, and stalled after it.
func drainingRow(in Inputs, w Worker, held time.Duration, assigned []Actor, subject Subject, related []Subject) Attention {
	pod, _ := in.WorkerPods.Of(w)
	deadline, known := pod.Deadline, !pod.Deadline.IsZero()
	if !known && !w.UpdateTime.IsZero() {
		deadline = w.UpdateTime.Add(DefaultDrainGrace)
	}
	base := fmt.Sprintf("Draining stops new placements; it does not suspend or move actors. %s still assigned and run until their applications shut down, or until someone suspends or deletes them. When the Pod is gone, Substrate marks any still assigned crashed.", plural(len(assigned), "actor is", "actors are"))
	row := Attention{
		ID: RuleWorkerDraining + ":" + w.Name, Rule: RuleWorkerDraining, Severity: SeverityInfo, key: w.Name,
		Title:   fmt.Sprintf("Worker %s is draining with %s", w.Pod, plural(len(assigned), "actor", "actors")),
		Subject: subject, Related: related, Count: len(assigned), Since: w.UpdateTime, Sample: sample(assigned),
	}
	switch {
	case deadline.IsZero():
		row.Reading = base + " Radar cannot tell when the drain began or when the Pod is killed."
		row.NextStep = "Suspend any actor whose state must be kept before its worker Pod is gone."
	case in.Now.Before(deadline):
		when := "Its Pod's termination deadline is in " + timeutil.FormatAgeShort(deadline.Sub(in.Now)) + "."
		if !known {
			when = "Radar cannot read the Pod's deadline; Substrate gives worker Pods " + timeutil.FormatAgeShort(DefaultDrainGrace) + ", which from the drain's start ends in about " + timeutil.FormatAgeShort(deadline.Sub(in.Now)) + "."
		}
		row.Reading = base + " " + when
		row.NextStep = "Nothing to do unless an actor's state must be kept: suspend it before the deadline."
	default:
		row.Severity = SeverityWarning
		row.Title = fmt.Sprintf("Worker %s is past its Pod's termination deadline with %s", w.Pod, plural(len(assigned), "actor", "actors"))
		passed := "Its Pod's termination deadline passed " + timeutil.FormatAgeShort(in.Now.Sub(deadline)) + " ago."
		if !known {
			passed = fmt.Sprintf("It has been draining for at least %s, longer than the %s Substrate gives worker Pods; Radar cannot read the Pod's own deadline.", timeutil.FormatAgeShort(held), timeutil.FormatAgeShort(DefaultDrainGrace))
		}
		row.Reading = base + " " + passed
		row.NextStep = "Check the worker Pod: whether it is still terminating, and the worker's log for applications that did not stop."
	}
	return row
}

// Check is one needs-attention rule and whether it ran on this read.
type Check struct {
	Rule  string `json:"rule"`
	Label string `json:"label"`
	Ran   bool   `json:"ran"`
	// Why says what was missing when it did not run.
	Why string `json:"why,omitempty"`
}

// NotAssessed are health surfaces no check reads, so an empty list never
// means they are healthy.
var NotAssessed = []string{
	"API operation errors (resume, suspend and tag calls)",
	"Resumes that fail before placement, such as no worker fitting the actor",
	"Request routing and egress",
	"Worker log collection",
}

// Checks lists the rules and whether each ran on these inputs, so an empty
// NeedsAttention can say which checks found nothing.
func Checks(in Inputs) []Check {
	need := func(rule, label string, reqs ...struct {
		ok  bool
		why string
	}) Check {
		for _, r := range reqs {
			if !r.ok {
				return Check{Rule: rule, Label: label, Why: r.why}
			}
		}
		return Check{Rule: rule, Label: label, Ran: true}
	}
	type req = struct {
		ok  bool
		why string
	}
	actors := req{in.ActorsRead, "actors were not read"}
	templates := req{in.TemplatesRead, "templates were not read"}
	templatesAll := req{in.TemplatesComplete, "the template list is incomplete"}
	workers := req{in.WorkersRead, "workers were not read"}
	poolsAll := req{in.PoolsComplete, "the WorkerPool list is incomplete"}
	pools := req{in.PoolsUsable || in.WorkersRead, "neither WorkerPools nor workers were read"}
	svcWhy := "Prometheus was not queried"
	if in.Service != nil && in.Service.Why != "" {
		svcWhy = in.Service.Why
	}
	svc := req{serviceRead(in), svcWhy}
	nodes := req{serviceRead(in) && len(in.Service.Nodes) > 0, "no node holding actor state was known"}
	return []Check{
		need(RuleCrashedActors, "Crashed actors", actors),
		need(RuleStuckTransition, "Actors stuck resuming, suspending, reverting or deleting", actors),
		need(RuleUploadOverdue, "Suspended actors with a snapshot pending upload", actors),
		need(RuleMissingTemplate, "Actors whose template is gone", actors, templatesAll),
		need(RuleGoldenFailed, "Failed golden snapshots", templates),
		need(RuleNoMatchingPool, "Templates whose selector matches no pool", templates, poolsAll),
		need(RuleWorkerUnavailable, "Unavailable workers", workers),
		need(RuleWorkerDraining, "Draining workers", workers),
		need(RuleWorkerRestarting, "Worker registrations not yet reconciled", workers),
		need(RulePoolShort, "Pools short of workers", pools),
		need(RulePoolFull, "Pools with no free actor slots", pools),
		need("poolService", "Substrate's pool capacity verdicts (Prometheus)", svc),
		need(RuleNodeUploadsOverdue, "Overdue snapshot uploads per node (Prometheus)", svc, nodes),
	}
}

// ActorAttention returns the rows that cover actor a, judged by each rule's
// own predicate on a, which may be fresher than the read the rows came from.
// Fleet-level rows about templates' golden snapshots and about pools name no
// actor and are not returned.
func ActorAttention(rows []Attention, a Actor, now time.Time) []Attention {
	out := []Attention{}
	for _, r := range rows {
		if !covers(r, a, now) {
			continue
		}
		switch r.Rule {
		case RuleCrashedActors:
			r.NextStep, r.Command = crashedActorNextStep, revertCommand
			if IsMemoryLimitCrash(a) {
				r.NextStep, r.Command = CrashedMemoryActorStep(a), retemplateCommand
			}
		case RuleStuckTransition:
			r.NextStep = stuckActorNextStep[r.key]
		}
		r.Command = forActor(r.Command, a)
		out = append(out, r)
	}
	return out
}

func covers(r Attention, a Actor, now time.Time) bool {
	worker := ""
	var epoch int64
	if a.Assignment != nil {
		worker, epoch = a.Assignment.Worker, a.Assignment.Epoch
	}
	switch r.Rule {
	case RuleCrashedActors:
		return a.State == StateCrashed
	case RuleStuckTransition:
		return a.State == r.key && stuck(a, now)
	case RuleUploadOverdue:
		_, ok := uploadOverdue(a, now)
		return ok
	case RuleNoMatchingPool:
		return a.State != StateRunning && a.Template.Atespace+"/"+a.Template.Name == r.key
	case RuleMissingTemplate:
		return a.Template.Atespace+"/"+a.Template.Name == r.key
	case RuleWorkerUnavailable, RuleWorkerDraining:
		return worker != "" && worker == r.key
	case RuleWorkerRestarting:
		return worker != "" && worker == r.key && epoch < r.epoch && a.State != StateResuming
	}
	return false
}

// ServiceSummary is the workspace's one-line reading of Service.
type ServiceSummary struct {
	Read   bool      `json:"read"`
	Why    string    `json:"why,omitempty"`
	ReadAt time.Time `json:"readAt,omitzero"`
	Window string    `json:"window,omitempty"`
	// Crashes sums the pools that reported one; absent when none did.
	Crashes *float64 `json:"crashes,omitempty"`
	// SlowestResume is the pool with the highest resume p95.
	SlowestResume *PoolFact `json:"slowestResume,omitempty"`
	// MostFailedResumes is the pool with the highest failed-resume share.
	MostFailedResumes *PoolFact `json:"mostFailedResumes,omitempty"`
	// PoolsUnknown counts pools Prometheus gave no verdict for.
	PoolsUnknown int `json:"poolsUnknown"`
	// NodesOverdue counts nodes whose atelet reports overdue uploads;
	// NodesUnknown those without a verdict.
	NodesOverdue int `json:"nodesOverdue"`
	NodesUnknown int `json:"nodesUnknown"`
}

// PoolFact is one pool's number.
type PoolFact struct {
	Pool  PoolRef `json:"pool"`
	Value float64 `json:"value"`
}

// SummarizeService reads the fleet out of the per-pool and per-node verdicts.
func SummarizeService(svc *Service) ServiceSummary {
	if svc == nil {
		return ServiceSummary{Why: "Prometheus was not queried"}
	}
	out := ServiceSummary{Read: svc.Read, Why: svc.Why, ReadAt: svc.ReadAt, Window: svc.Window}
	for _, key := range slices.Sorted(maps.Keys(svc.Pools)) {
		ps := svc.Pools[key]
		ns, name, _ := strings.Cut(key, "/")
		ref := PoolRef{Namespace: ns, Name: name}
		if ps.Verdict == ServiceUnknown {
			out.PoolsUnknown++
		}
		if ps.Crashes != nil {
			total := *ps.Crashes
			if out.Crashes != nil {
				total += *out.Crashes
			}
			out.Crashes = &total
		}
		if v := ps.ResumeP95Seconds; v != nil && (out.SlowestResume == nil || *v > out.SlowestResume.Value) {
			out.SlowestResume = &PoolFact{Pool: ref, Value: *v}
		}
		if v := ps.FailedResumesPercent; v != nil && *v > 0 && (out.MostFailedResumes == nil || *v > out.MostFailedResumes.Value) {
			out.MostFailedResumes = &PoolFact{Pool: ref, Value: *v}
		}
	}
	for _, ns := range svc.Nodes {
		switch ns.Verdict {
		case ServiceUploadsOverdue:
			out.NodesOverdue++
		case ServiceUnknown:
			out.NodesUnknown++
		}
	}
	return out
}
