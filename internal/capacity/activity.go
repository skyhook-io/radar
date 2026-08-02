package capacity

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/skyhook-io/radar/pkg/capacityapi"
	"github.com/skyhook-io/radar/pkg/karpenter"
	"github.com/skyhook-io/radar/pkg/subject"
	"github.com/skyhook-io/radar/pkg/timeline"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const activityEvidenceLimit = 20

type ActivityRecord struct {
	Episode capacityapi.ActivityEpisode
	MaxSeq  int64

	terminalAt         *time.Time
	terminalReasonCode string
}

type activityClassification struct {
	activityType capacityapi.ActivityType
	state        capacityapi.ActivityState
	reasonCode   string
	relationship capacityapi.EvidenceRelationship
	confidence   capacityapi.Confidence
}

func directActivity(activityType capacityapi.ActivityType, state capacityapi.ActivityState, reasonCode string) activityClassification {
	return activityClassification{
		activityType: activityType,
		state:        state,
		reasonCode:   reasonCode,
		relationship: capacityapi.EvidenceDirect,
		confidence:   capacityapi.ConfidenceHigh,
	}
}

func inferredActivity(activityType capacityapi.ActivityType, state capacityapi.ActivityState, reasonCode string) activityClassification {
	return activityClassification{
		activityType: activityType,
		state:        state,
		reasonCode:   reasonCode,
		relationship: capacityapi.EvidenceCorrelated,
		confidence:   capacityapi.ConfidenceLow,
	}
}

func AggregateActivityRecords(records []ActivityRecord) capacityapi.ActivityAggregate {
	aggregate := capacityapi.NewActivityAggregate()
	for _, record := range records {
		aggregate.Total++
		counts := aggregate.ByType[record.Episode.Type]
		counts.Total++
		if counts.ByState == nil {
			counts.ByState = map[capacityapi.ActivityState]int{}
		}
		counts.ByState[record.Episode.State]++
		aggregate.ByType[record.Episode.Type] = counts
	}
	return aggregate
}

func BuildActivityRecords(events []timeline.TimelineEvent) []ActivityRecord {
	orderedEvents := append([]timeline.TimelineEvent{}, events...)
	sort.Slice(orderedEvents, func(i, j int) bool {
		if !orderedEvents[i].Timestamp.Equal(orderedEvents[j].Timestamp) {
			return orderedEvents[i].Timestamp.Before(orderedEvents[j].Timestamp)
		}
		if orderedEvents[i].Seq != orderedEvents[j].Seq {
			return orderedEvents[i].Seq < orderedEvents[j].Seq
		}
		return orderedEvents[i].ID < orderedEvents[j].ID
	})
	uidByResource := map[string]string{}
	ambiguousUID := map[string]bool{}
	for _, event := range orderedEvents {
		if event.UID != "" {
			key := activityResourceKey(event)
			if existing := uidByResource[key]; existing != "" && existing != event.UID {
				ambiguousUID[key] = true
				continue
			}
			uidByResource[key] = event.UID
		}
	}
	for key := range ambiguousUID {
		delete(uidByResource, key)
	}

	records := map[string]*ActivityRecord{}
	for _, event := range orderedEvents {
		if strings.TrimSpace(event.Name) == "" {
			continue
		}
		classification, ok := classifyActivityEvent(event)
		if !ok {
			continue
		}
		activityType := classification.activityType
		state := classification.state
		uid := event.UID
		if uid == "" {
			uid = uidByResource[activityResourceKey(event)]
		}
		correlationKey := activityCorrelationKey(event, uid, activityType, state)
		record := records[correlationKey]
		startedAt := activityEventStart(event, activityType, state)
		if record == nil {
			episode := capacityapi.NewActivityEpisode()
			episode.ID = stableActivityID(correlationKey)
			episode.Type = activityType
			episode.State = state
			episode.StartedAt = startedAt
			record = &ActivityRecord{Episode: episode}
			records[correlationKey] = record
		}
		attachActivitySubjects(&record.Episode, event, uid)
		if startedAt.Before(record.Episode.StartedAt) {
			record.Episode.StartedAt = startedAt
		}
		if state == capacityapi.ActivityCompleted || state == capacityapi.ActivityFailed {
			// Failed→completed stays allowed — recovery IS the provisioning
			// outcome. The reverse is not: a completed provision is history,
			// and a later failure signal (post-Ready health degradation) must
			// not rewrite it. The evidence still records the failure.
			failureAfterCompletion := record.terminalAt != nil &&
				record.Episode.State == capacityapi.ActivityCompleted &&
				state == capacityapi.ActivityFailed
			if !failureAfterCompletion {
				record.Episode.State = state
				record.Episode.Type = activityType
				terminalAt := event.Timestamp
				record.terminalAt = &terminalAt
				record.terminalReasonCode = classification.reasonCode
			}
		} else if activityStateRank(state) > activityStateRank(record.Episode.State) {
			record.Episode.State = state
			record.Episode.Type = activityType
		}
		if event.Seq > record.MaxSeq {
			record.MaxSeq = event.Seq
		}
		evidence := capacityapi.NewActivityEvidence()
		evidence.At = event.Timestamp
		evidence.Source = activityEvidenceSource(event.Source)
		evidence.ReasonCode = classification.reasonCode
		evidence.RawReason = event.Reason
		evidence.RawMessage = event.Message
		if evidence.RawMessage == "" && event.Diff != nil {
			evidence.RawMessage = event.Diff.Summary
		}
		evidence.Relationship = classification.relationship
		evidence.Confidence = classification.confidence
		evidence.Refs = append(evidence.Refs, activityResourceIdentity(event, uid))
		record.Episode.Evidence = append(record.Episode.Evidence, evidence)

		// A claim deleted before ever reaching Ready ends its provision episode —
		// "nodes not joining" must never read as provisioning still in progress
		// next to a completed termination. The deletion terminalizes as ENDED,
		// not failed, because the delete event carries no cause of its own.
		//
		// That loses nothing on the real failure paths: Karpenter records the
		// failing stage (LaunchFailed, a Launched=Unknown/ICE condition
		// transition) BEFORE it deletes the claim, and any such evidence has
		// already terminalized this episode as failed — which the already-
		// terminal guard then skips. So an episode still open when the delete
		// arrives is precisely one where no failure was ever observed, and
		// calling it "failed" would manufacture a diagnosis out of an absence.
		if classification.reasonCode == "nodeclaim_deleted" {
			provision := records[provisionCorrelationKey(event, uid)]
			if provision != nil && provision.terminalAt != nil {
				provision = nil
			}
			if provision != nil {
				const terminalReason = "nodeclaim_deleted_cause_unknown"
				provision.Episode.State = capacityapi.ActivityEnded
				provision.Episode.Type = capacityapi.ActivityProvision
				terminalAt := event.Timestamp
				provision.terminalAt = &terminalAt
				provision.terminalReasonCode = terminalReason
				// Deliberately NOT bumping provision.MaxSeq to the delete's
				// seq: pagination cursors cut on MaxSeq alone, so two records
				// sharing one seq would let a page boundary drop one of the
				// pair. The episode keeps its own history position; only its
				// outcome and end time come from the delete.
				closing := capacityapi.NewActivityEvidence()
				closing.At = event.Timestamp
				closing.Source = activityEvidenceSource(event.Source)
				closing.ReasonCode = terminalReason
				closing.RawReason = event.Reason
				closing.RawMessage = event.Message
				closing.Relationship = classification.relationship
				closing.Confidence = classification.confidence
				closing.Refs = append(closing.Refs, activityResourceIdentity(event, uid))
				provision.Episode.Evidence = append(provision.Episode.Evidence, closing)
			}
		}
	}

	result := make([]ActivityRecord, 0, len(records))
	for _, record := range records {
		sort.Slice(record.Episode.Evidence, func(i, j int) bool {
			left, right := record.Episode.Evidence[i], record.Episode.Evidence[j]
			if !left.At.Equal(right.At) {
				return left.At.Before(right.At)
			}
			return left.ReasonCode < right.ReasonCode
		})
		if record.terminalAt != nil {
			endedAt := *record.terminalAt
			record.Episode.EndedAt = &endedAt
			duration := endedAt.Sub(record.Episode.StartedAt).Seconds()
			if duration < 0 {
				duration = 0
			}
			record.Episode.DurationSeconds = &duration
		}
		if record.terminalReasonCode != "" {
			record.Episode.PrimaryReasonCode = record.terminalReasonCode
		} else {
			for index := len(record.Episode.Evidence) - 1; index >= 0; index-- {
				if record.Episode.Evidence[index].ReasonCode != "" {
					record.Episode.PrimaryReasonCode = record.Episode.Evidence[index].ReasonCode
					break
				}
			}
		}
		record.Episode.Summary = activityEpisodeSummary(record.Episode)
		record.Episode.EvidenceMeta.Total = len(record.Episode.Evidence)
		if len(record.Episode.Evidence) > activityEvidenceLimit {
			latest := append([]capacityapi.ActivityEvidence{}, record.Episode.Evidence[len(record.Episode.Evidence)-(activityEvidenceLimit-1):]...)
			record.Episode.Evidence = append(record.Episode.Evidence[:1], latest...)
			record.Episode.EvidenceMeta.Truncated = true
		}
		record.Episode.EvidenceMeta.Returned = len(record.Episode.Evidence)
		result = append(result, *record)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].MaxSeq != result[j].MaxSeq {
			return result[i].MaxSeq < result[j].MaxSeq
		}
		return result[i].Episode.ID < result[j].Episode.ID
	})
	return result
}

// activityAttemptScoped reports whether an episode is keyed per occurrence
// rather than folded into its subject's provisioning lifecycle. The
// provision family folds by NodeClaim UID — one claim, one episode — but a
// BLOCKED provision is emitted by the NodePool before any claim exists, so
// folding it would collapse every occurrence, forever, into one open episode.
func activityAttemptScoped(activityType capacityapi.ActivityType, state capacityapi.ActivityState) bool {
	if state == capacityapi.ActivityBlocked {
		return true
	}
	switch activityType {
	case capacityapi.ActivityProvision, capacityapi.ActivityLaunchFailure, capacityapi.ActivityRegistrationFailure, capacityapi.ActivityInitializationFailure:
		return false
	}
	return true
}

func activityEventStart(event timeline.TimelineEvent, activityType capacityapi.ActivityType, state capacityapi.ActivityState) time.Time {
	if activityAttemptScoped(activityType, state) {
		return event.Timestamp
	}
	if event.CreatedAt != nil && !event.CreatedAt.IsZero() && event.CreatedAt.Before(event.Timestamp) {
		return *event.CreatedAt
	}
	return event.Timestamp
}

func activityEpisodeSummary(episode capacityapi.ActivityEpisode) string {
	state := episode.State
	switch episode.Type {
	case capacityapi.ActivityProvision:
		if state == capacityapi.ActivityBlocked {
			// Nothing was ever launched, so naming a NodeClaim would invent one.
			subject := "Provisioning"
			if episode.Pool != nil && episode.Pool.Ref.Name != "" {
				subject = "NodePool " + episode.Pool.Ref.Name + ": provisioning"
			}
			if episode.PrimaryReasonCode == "no_compatible_instance_types" {
				return subject + " could not start — no compatible instance types"
			}
			return subject + " could not start"
		}
		if state == capacityapi.ActivityCompleted {
			return "NodeClaim provisioning completed"
		}
		if state == capacityapi.ActivityFailed {
			return "NodeClaim provisioning failed"
		}
		if state == capacityapi.ActivityEnded {
			return "NodeClaim deleted before becoming Ready — cause not recorded"
		}
		return "NodeClaim provisioning is in progress"
	case capacityapi.ActivityLaunchFailure:
		return "NodeClaim launch failed"
	case capacityapi.ActivityRegistrationFailure:
		return "NodeClaim registration failed"
	case capacityapi.ActivityInitializationFailure:
		return "NodeClaim initialization failed"
	case capacityapi.ActivityDisruption:
		if state == capacityapi.ActivityBlocked {
			return "Karpenter disruption is blocked"
		}
		return "Karpenter disruption activity was observed"
	case capacityapi.ActivityInterruption:
		return "Capacity interruption was observed"
	case capacityapi.ActivityTermination:
		if state == capacityapi.ActivityCompleted {
			return "Karpenter-managed capacity terminated"
		}
		return "Karpenter termination activity was observed"
	case capacityapi.ActivityConfigChange:
		return "NodePool configuration changed"
	default:
		return "Karpenter capacity activity was observed"
	}
}

func classifyActivityEvent(event timeline.TimelineEvent) (activityClassification, bool) {
	group := schema.FromAPIVersionAndKind(event.APIVersion, event.Kind).Group
	if event.Kind == karpenter.NodePoolKind && group == karpenter.Group {
		if event.EventType == timeline.EventTypeUpdate && event.Diff != nil {
			for _, field := range event.Diff.Fields {
				if field.Path == "metadata.generation" || field.Path == "spec" || strings.HasPrefix(field.Path, "spec.") {
					return directActivity(capacityapi.ActivityConfigChange, capacityapi.ActivityCompleted, "nodepool_spec_changed"), true
				}
			}
		}
		return classifyKarpenterReason(event)
	}
	if event.Kind == karpenter.NodeClaimKind && group == karpenter.Group {
		if event.EventType == timeline.EventTypeAdd {
			return directActivity(capacityapi.ActivityProvision, capacityapi.ActivityOpen, "nodeclaim_created"), true
		}
		if event.EventType == timeline.EventTypeDelete {
			return directActivity(capacityapi.ActivityTermination, capacityapi.ActivityCompleted, "nodeclaim_deleted"), true
		}
		if classification, ok := classifyNodeClaimConditionChange(event); ok {
			return classification, true
		}
		if classification, ok := classifyKarpenterReason(event); ok {
			return classification, true
		}
		// Exact reasons only — a substring match would let failure vocabulary
		// like NodeNotInitialized read as a completed provision.
		if strings.EqualFold(event.Reason, "Ready") || strings.EqualFold(event.Reason, "Initialized") {
			return directActivity(capacityapi.ActivityProvision, capacityapi.ActivityCompleted, "nodeclaim_ready"), true
		}
	}
	if event.Kind == "Node" && event.Labels[karpenter.NodePoolLabelKey] != "" {
		if event.EventType == timeline.EventTypeDelete {
			return directActivity(capacityapi.ActivityTermination, capacityapi.ActivityCompleted, "node_deleted"), true
		}
		return classifyKarpenterReason(event)
	}
	return activityClassification{}, false
}

func classifyNodeClaimConditionChange(event timeline.TimelineEvent) (activityClassification, bool) {
	if event.Diff == nil {
		return activityClassification{}, false
	}
	for _, field := range event.Diff.Fields {
		conditionType := strings.TrimSuffix(strings.TrimPrefix(field.Path, "status.conditions["), "]")
		if conditionType == field.Path {
			continue
		}
		signal, ok := field.NewValue.(string)
		if !ok {
			continue
		}
		status, reason, _ := strings.Cut(signal, "\x00")
		if conditionType == "Ready" && status == "True" {
			return directActivity(capacityapi.ActivityProvision, capacityapi.ActivityCompleted, "nodeclaim_ready"), true
		}
		if !karpenter.IsFailedLifecycleConditionForVersion(event.APIVersion, metav1.Condition{Status: metav1.ConditionStatus(status), Reason: reason}) {
			continue
		}
		switch conditionType {
		case "Launched":
			return directActivity(capacityapi.ActivityLaunchFailure, capacityapi.ActivityFailed, "launch_failed"), true
		case "Registered":
			return directActivity(capacityapi.ActivityRegistrationFailure, capacityapi.ActivityFailed, "registration_failed"), true
		case "Initialized":
			return directActivity(capacityapi.ActivityInitializationFailure, capacityapi.ActivityFailed, "initialization_failed"), true
		}
	}
	// Ready is a rollup: a bare False is just "not ready yet", so only
	// failure-flavored reasons classify — and only after the specific
	// sub-condition pass above found nothing to attribute it to.
	for _, field := range event.Diff.Fields {
		if strings.TrimSuffix(strings.TrimPrefix(field.Path, "status.conditions["), "]") != "Ready" {
			continue
		}
		signal, ok := field.NewValue.(string)
		if !ok {
			continue
		}
		status, reason, _ := strings.Cut(signal, "\x00")
		if (status == string(metav1.ConditionFalse) || status == string(metav1.ConditionUnknown)) && karpenter.IsFailureReason(reason) {
			return directActivity(capacityapi.ActivityProvision, capacityapi.ActivityFailed, "nodeclaim_not_ready"), true
		}
	}
	return activityClassification{}, false
}

// karpenterEventClassifications maps exact event reasons Karpenter emits to
// their real meaning. Exact matches are direct/high-confidence evidence; the
// substring fallback below stays inferred/low. Without this table,
// DisruptionBlocked and Unconsolidatable read as disruption *happening* — the
// opposite of what they mean — and launch failures like
// InsufficientCapacityError disappear into generic termination.
var karpenterEventClassifications = map[string]activityClassification{
	"InsufficientCapacityError": directActivity(capacityapi.ActivityLaunchFailure, capacityapi.ActivityFailed, "insufficient_capacity"),
	"LaunchFailed":              directActivity(capacityapi.ActivityLaunchFailure, capacityapi.ActivityFailed, "launch_failed"),
	"CreateError":               directActivity(capacityapi.ActivityLaunchFailure, capacityapi.ActivityFailed, "launch_failed"),
	"NodeClassNotReady":         directActivity(capacityapi.ActivityLaunchFailure, capacityapi.ActivityFailed, "nodeclass_not_ready"),
	// Emitted by the NodePool, never by a NodeClaim: Karpenter could not even
	// create one. Blocked, not failed — and never folded into a claim's
	// provisioning lifecycle, which has not started.
	"NoCompatibleInstanceTypes": directActivity(capacityapi.ActivityProvision, capacityapi.ActivityBlocked, "no_compatible_instance_types"),
	"UnregisteredTaintMissing":  directActivity(capacityapi.ActivityRegistrationFailure, capacityapi.ActivityFailed, "registration_failed"),

	"DisruptionBlocked":     directActivity(capacityapi.ActivityDisruption, capacityapi.ActivityBlocked, "disruption_blocked"),
	"Unconsolidatable":      directActivity(capacityapi.ActivityDisruption, capacityapi.ActivityBlocked, "unconsolidatable"),
	"ConsolidationRejected": directActivity(capacityapi.ActivityDisruption, capacityapi.ActivityBlocked, "consolidation_rejected"),
	"NodeRepairBlocked":     directActivity(capacityapi.ActivityDisruption, capacityapi.ActivityBlocked, "node_repair_blocked"),

	"Disrupted":                  directActivity(capacityapi.ActivityDisruption, capacityapi.ActivityObserved, "disrupted"),
	"DisruptionLaunching":        directActivity(capacityapi.ActivityDisruption, capacityapi.ActivityOpen, "disruption_launching"),
	"DisruptionWaitingReadiness": directActivity(capacityapi.ActivityDisruption, capacityapi.ActivityOpen, "disruption_waiting_readiness"),
	"DisruptionTerminating":      directActivity(capacityapi.ActivityDisruption, capacityapi.ActivityOpen, "disruption_terminating"),
	"ConsolidationCandidate":     directActivity(capacityapi.ActivityDisruption, capacityapi.ActivityObserved, "consolidation_candidate"),

	"SpotInterrupted":             directActivity(capacityapi.ActivityInterruption, capacityapi.ActivityObserved, "spot_interrupted"),
	"SpotRebalanceRecommendation": directActivity(capacityapi.ActivityInterruption, capacityapi.ActivityObserved, "rebalance_recommendation"),
	"InstanceStopping":            directActivity(capacityapi.ActivityInterruption, capacityapi.ActivityObserved, "instance_stopping"),
	"InstanceTerminating":         directActivity(capacityapi.ActivityInterruption, capacityapi.ActivityObserved, "instance_terminating"),
	"InstanceUnhealthy":           directActivity(capacityapi.ActivityInterruption, capacityapi.ActivityObserved, "instance_unhealthy"),

	"FailedDraining":                 directActivity(capacityapi.ActivityTermination, capacityapi.ActivityFailed, "draining_failed"),
	"FailedTermination":              directActivity(capacityapi.ActivityTermination, capacityapi.ActivityFailed, "termination_failed"),
	"TerminationGracePeriodExpiring": directActivity(capacityapi.ActivityTermination, capacityapi.ActivityOpen, "grace_period_expiring"),
	// An open termination wait — the node cannot finish terminating until its
	// volumes detach. Waiting, not failed.
	"AwaitingVolumeDetachment": directActivity(capacityapi.ActivityTermination, capacityapi.ActivityOpen, "awaiting_volume_detachment"),

	// The consolidation decision itself — disruption is proceeding, distinct
	// from the blocked/rejected family above.
	"ConsolidationApproved": directActivity(capacityapi.ActivityDisruption, capacityapi.ActivityOpen, "consolidation_approved"),
	// A claim whose node no longer matches what was requested — claim-lifecycle
	// evidence, folded onto the claim's provision episode without changing a
	// terminal state.
	"FailedConsistencyCheck": directActivity(capacityapi.ActivityProvision, capacityapi.ActivityObserved, "consistency_check_failed"),

	"TerminatingOnInterruption":              directActivity(capacityapi.ActivityInterruption, capacityapi.ActivityObserved, "terminating_on_interruption"),
	"CapacityReservationInstanceInterrupted": directActivity(capacityapi.ActivityInterruption, capacityapi.ActivityObserved, "capacity_reservation_interrupted"),
	"ZonalShiftActive":                       directActivity(capacityapi.ActivityInterruption, capacityapi.ActivityObserved, "zonal_shift_active"),
	"ZonalShiftCleared":                      directActivity(capacityapi.ActivityInterruption, capacityapi.ActivityObserved, "zonal_shift_cleared"),
}

func classifyKarpenterReason(event timeline.TimelineEvent) (activityClassification, bool) {
	if classification, ok := karpenterEventClassifications[event.Reason]; ok {
		return classification, true
	}
	reason := strings.ToLower(event.Reason + " " + event.Message)
	// Inferred stage failures need failure text, not just a Warning — warnings
	// carrying in-progress vocabulary (NodeNotInitialized, not registered yet)
	// must never terminalize a provision episode as failed.
	switch {
	case strings.Contains(reason, "launch") && (strings.Contains(reason, "fail") || strings.Contains(reason, "error")):
		return inferredActivity(capacityapi.ActivityLaunchFailure, capacityapi.ActivityFailed, "launch_failed"), true
	case strings.Contains(reason, "register") && (strings.Contains(reason, "fail") || strings.Contains(reason, "error")):
		return inferredActivity(capacityapi.ActivityRegistrationFailure, capacityapi.ActivityFailed, "registration_failed"), true
	case strings.Contains(reason, "initializ") && (strings.Contains(reason, "fail") || strings.Contains(reason, "error")):
		return inferredActivity(capacityapi.ActivityInitializationFailure, capacityapi.ActivityFailed, "initialization_failed"), true
	case strings.Contains(reason, "interrupt") || strings.Contains(reason, "spot interruption"):
		return inferredActivity(capacityapi.ActivityInterruption, capacityapi.ActivityObserved, "interruption"), true
	case strings.Contains(reason, "disrupt") || strings.Contains(reason, "consolidat") || strings.Contains(reason, "drift"):
		return inferredActivity(capacityapi.ActivityDisruption, capacityapi.ActivityObserved, "disruption"), true
	case strings.Contains(reason, "terminat") || strings.Contains(reason, "delete"):
		return inferredActivity(capacityapi.ActivityTermination, capacityapi.ActivityObserved, "termination_observed"), true
	}
	return activityClassification{}, false
}

func attachActivitySubjects(episode *capacityapi.ActivityEpisode, event timeline.TimelineEvent, uid string) {
	identity := activityResourceIdentity(event, uid)
	switch event.Kind {
	case karpenter.NodePoolKind:
		episode.Pool = &identity
	case karpenter.NodeClaimKind:
		episode.Claim = &identity
		if event.Owner != nil && event.Owner.Kind == karpenter.NodePoolKind {
			pool := capacityapi.ResourceIdentity{Ref: subject.Ref{Group: karpenter.Group, Kind: karpenter.NodePoolKind, Name: event.Owner.Name}}
			episode.Pool = &pool
		} else if poolName := event.Labels[karpenter.NodePoolLabelKey]; poolName != "" {
			pool := capacityapi.ResourceIdentity{Ref: subject.Ref{Group: karpenter.Group, Kind: karpenter.NodePoolKind, Name: poolName}}
			episode.Pool = &pool
		}
	case "Node":
		episode.Node = &identity
		if poolName := event.Labels[karpenter.NodePoolLabelKey]; poolName != "" {
			pool := capacityapi.ResourceIdentity{Ref: subject.Ref{Group: karpenter.Group, Kind: karpenter.NodePoolKind, Name: poolName}}
			episode.Pool = &pool
		}
	}
}

func activityResourceIdentity(event timeline.TimelineEvent, uid string) capacityapi.ResourceIdentity {
	return capacityapi.ResourceIdentity{
		Ref: subject.Ref{
			Group: schema.FromAPIVersionAndKind(event.APIVersion, event.Kind).Group,
			Kind:  event.Kind, Namespace: activitySubjectNamespace(event), Name: event.Name,
		},
		APIVersion: event.APIVersion,
		UID:        uid,
	}
}

func activityCorrelationKey(event timeline.TimelineEvent, uid string, activityType capacityapi.ActivityType, state capacityapi.ActivityState) string {
	if !activityAttemptScoped(activityType, state) {
		return provisionCorrelationKey(event, uid)
	}
	identity := uid
	if identity == "" {
		identity = activityResourceKey(event)
	}
	attemptID := event.CorrelationID
	if attemptID == "" {
		attemptID = event.ID
	}
	if attemptID == "" && event.Seq > 0 {
		attemptID = strconv.FormatInt(event.Seq, 10)
	}
	if attemptID == "" {
		attemptID = event.Timestamp.UTC().Format(time.RFC3339Nano)
	}
	return string(activityType) + "\x00" + identity + "\x00" + attemptID
}

// provisionCorrelationKey is the one bucket a NodeClaim's whole provisioning
// lifecycle folds into, regardless of which stage reported it.
func provisionCorrelationKey(event timeline.TimelineEvent, uid string) string {
	identity := uid
	if identity == "" {
		identity = activityResourceKey(event)
	}
	return string(capacityapi.ActivityProvision) + "\x00" + identity
}

func activityResourceKey(event timeline.TimelineEvent) string {
	return event.APIVersion + "\x00" + event.Kind + "\x00" + activitySubjectNamespace(event) + "\x00" + event.Name
}

func activitySubjectNamespace(event timeline.TimelineEvent) string {
	group := schema.FromAPIVersionAndKind(event.APIVersion, event.Kind).Group
	if event.Kind == "Node" || ((event.Kind == karpenter.NodePoolKind || event.Kind == karpenter.NodeClaimKind) && group == karpenter.Group) {
		return ""
	}
	return event.Namespace
}

func stableActivityID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "capacity-" + hex.EncodeToString(sum[:12])
}

func activityEvidenceSource(source timeline.EventSource) capacityapi.EvidenceSource {
	switch source {
	case timeline.SourceK8sEvent:
		return capacityapi.EvidenceKubernetesEvent
	case timeline.SourceHistorical:
		return capacityapi.EvidenceCondition
	default:
		return capacityapi.EvidenceResourceChange
	}
}

func activityStateRank(state capacityapi.ActivityState) int {
	switch state {
	case capacityapi.ActivityFailed:
		return 6
	case capacityapi.ActivityBlocked:
		return 5
	case capacityapi.ActivityCompleted:
		return 4
	// Ended outranks an in-progress state (the episode really is over) but never
	// a completed one, whose outcome is actually known.
	case capacityapi.ActivityEnded:
		return 3
	case capacityapi.ActivityOpen:
		return 2
	case capacityapi.ActivityObserved:
		return 1
	default:
		return 0
	}
}
