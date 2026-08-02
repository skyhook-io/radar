package capacity

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/capacityapi"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/timeline"
)

func TestAggregateActivityRecordsCountsByTypeAndState(t *testing.T) {
	records := []ActivityRecord{
		{Episode: capacityapi.ActivityEpisode{Type: capacityapi.ActivityProvision, State: capacityapi.ActivityCompleted}},
		{Episode: capacityapi.ActivityEpisode{Type: capacityapi.ActivityProvision, State: capacityapi.ActivityFailed}},
		{Episode: capacityapi.ActivityEpisode{Type: capacityapi.ActivityProvision, State: capacityapi.ActivityOpen}},
		{Episode: capacityapi.ActivityEpisode{Type: capacityapi.ActivityProvision, State: capacityapi.ActivityEnded}},
		{Episode: capacityapi.ActivityEpisode{Type: capacityapi.ActivityDisruption, State: capacityapi.ActivityBlocked}},
	}
	aggregate := AggregateActivityRecords(records)
	if aggregate.Total != 5 {
		t.Fatalf("total = %d, want 5", aggregate.Total)
	}
	provision := aggregate.ByType[capacityapi.ActivityProvision]
	if provision.Total != 4 || provision.ByState[capacityapi.ActivityCompleted] != 1 ||
		provision.ByState[capacityapi.ActivityFailed] != 1 || provision.ByState[capacityapi.ActivityOpen] != 1 {
		t.Fatalf("provision counts = %+v", provision)
	}
	// The rollup strip must count cause-unknown endings as their own bucket —
	// folding them into failed is the very conflation the state exists to undo.
	if provision.ByState[capacityapi.ActivityEnded] != 1 {
		t.Fatalf("aggregate dropped the ended bucket: %+v", provision.ByState)
	}
	disruption := aggregate.ByType[capacityapi.ActivityDisruption]
	if disruption.Total != 1 || disruption.ByState[capacityapi.ActivityBlocked] != 1 {
		t.Fatalf("disruption counts = %+v", disruption)
	}

	empty := AggregateActivityRecords(nil)
	if empty.Total != 0 || empty.ByType == nil {
		t.Fatalf("empty aggregate = %+v, want zero total with a non-null byType map", empty)
	}
}

func TestBuildActivityRecordsCorrelatesAndClosesProvisionEpisode(t *testing.T) {
	start := time.Date(2026, time.July, 13, 10, 0, 0, 0, time.UTC)
	events := []timeline.TimelineEvent{
		{
			ID: "ready-event", Seq: 12, Timestamp: start.Add(2 * time.Minute), Source: timeline.SourceK8sEvent,
			Kind: "NodeClaim", APIVersion: "karpenter.sh/v1", Namespace: "default", Name: "claim-a", UID: "claim-a-uid", Reason: "Ready", EventType: timeline.EventTypeNormal,
		},
		{
			ID: "claim-a-rv-1", Seq: 10, Timestamp: start, Source: timeline.SourceInformer,
			Kind: "NodeClaim", APIVersion: "karpenter.sh/v1", Name: "claim-a", UID: "claim-a-uid", EventType: timeline.EventTypeAdd,
			Owner: &k8score.OwnerInfo{Kind: "NodePool", Name: "general"},
		},
	}

	records := BuildActivityRecords(events)
	if len(records) != 1 {
		t.Fatalf("records = %#v, want one correlated provision episode", records)
	}
	episode := records[0].Episode
	if episode.Type != capacityapi.ActivityProvision || episode.State != capacityapi.ActivityCompleted {
		t.Fatalf("episode type/state = %q/%q", episode.Type, episode.State)
	}
	if episode.Claim == nil || episode.Claim.UID != "claim-a-uid" || episode.Pool == nil || episode.Pool.Ref.Name != "general" {
		t.Fatalf("episode subjects = claim:%+v pool:%+v", episode.Claim, episode.Pool)
	}
	if len(episode.Evidence) != 2 || episode.EndedAt == nil || !episode.EndedAt.Equal(start.Add(2*time.Minute)) {
		t.Fatalf("episode evidence/end = %d/%v", len(episode.Evidence), episode.EndedAt)
	}
	if records[0].MaxSeq != 12 || episode.DurationSeconds == nil || *episode.DurationSeconds != 120 {
		t.Fatalf("episode seq/duration = %d/%v", records[0].MaxSeq, episode.DurationSeconds)
	}
	for _, evidence := range episode.Evidence {
		if evidence.Relationship != capacityapi.EvidenceDirect || evidence.Confidence != capacityapi.ConfidenceHigh {
			t.Fatalf("structured provisioning evidence truth = %q/%q, want direct/high", evidence.Relationship, evidence.Confidence)
		}
	}

	reversed := []timeline.TimelineEvent{events[1], events[0]}
	if !reflect.DeepEqual(records, BuildActivityRecords(reversed)) {
		t.Fatal("activity correlation changed with input order")
	}
}

func TestBuildActivityRecordsUsesDiffSummaryAsConfigEvidence(t *testing.T) {
	now := time.Date(2026, time.July, 13, 10, 0, 0, 0, time.UTC)
	records := BuildActivityRecords([]timeline.TimelineEvent{{
		ID: "pool-update", Seq: 1, Timestamp: now, Source: timeline.SourceInformer,
		Kind: "NodePool", APIVersion: "karpenter.sh/v1", Name: "general", UID: "pool-uid", EventType: timeline.EventTypeUpdate,
		Diff: &k8score.DiffInfo{Summary: "spec.disruption changed", Fields: []k8score.FieldChange{{Path: "spec.disruption"}}},
	}})
	if len(records) != 1 || len(records[0].Episode.Evidence) != 1 {
		t.Fatalf("records = %#v", records)
	}
	if got := records[0].Episode.Evidence[0].RawMessage; got != "spec.disruption changed" {
		t.Fatalf("config evidence message = %q", got)
	}
}

func TestBuildActivityRecordsClassifiesKarpenterNodeTimelineLabels(t *testing.T) {
	records := BuildActivityRecords([]timeline.TimelineEvent{{
		ID: "node-delete", Seq: 1, Timestamp: time.Now(), Source: timeline.SourceInformer,
		Kind: "Node", APIVersion: "v1", Name: "ip-10-0-0-1", UID: "node-uid", EventType: timeline.EventTypeDelete,
		Labels: map[string]string{"karpenter.sh/nodepool": "general"},
	}})
	if len(records) != 1 || records[0].Episode.Type != capacityapi.ActivityTermination {
		t.Fatalf("node activity = %#v", records)
	}
	if records[0].Episode.Pool == nil || records[0].Episode.Pool.Ref.Name != "general" {
		t.Fatalf("node pool subject = %#v", records[0].Episode.Pool)
	}
}

func TestBuildActivityRecordsSeparatesFailuresAndConfigChanges(t *testing.T) {
	now := time.Date(2026, time.July, 13, 10, 0, 0, 0, time.UTC)
	records := BuildActivityRecords([]timeline.TimelineEvent{
		{
			ID: "failed", Seq: 2, Timestamp: now, Source: timeline.SourceK8sEvent,
			Kind: "NodeClaim", APIVersion: "karpenter.sh/v1", Name: "claim-a", EventType: timeline.EventTypeWarning,
			Reason: "LaunchFailed", Message: "provider rejected launch",
		},
		{
			ID: "pool-update", Seq: 3, Timestamp: now.Add(time.Minute), Source: timeline.SourceInformer,
			Kind: "NodePool", APIVersion: "karpenter.sh/v1", Name: "general", UID: "pool-uid", EventType: timeline.EventTypeUpdate,
			Diff: &k8score.DiffInfo{Fields: []k8score.FieldChange{{Path: "metadata.generation"}}},
		},
		{ID: "unrelated", Seq: 4, Timestamp: now, Source: timeline.SourceInformer, Kind: "Deployment", APIVersion: "apps/v1", Name: "web", EventType: timeline.EventTypeUpdate},
	})
	if len(records) != 2 {
		t.Fatalf("records = %#v, want failure and config change only", records)
	}
	byType := map[capacityapi.ActivityType]capacityapi.ActivityEpisode{}
	for _, record := range records {
		byType[record.Episode.Type] = record.Episode
	}
	if byType[capacityapi.ActivityLaunchFailure].State != capacityapi.ActivityFailed {
		t.Fatalf("launch failure = %#v", byType[capacityapi.ActivityLaunchFailure])
	}
	failureEvidence := byType[capacityapi.ActivityLaunchFailure].Evidence
	if len(failureEvidence) != 1 || failureEvidence[0].Relationship != capacityapi.EvidenceDirect || failureEvidence[0].Confidence != capacityapi.ConfidenceHigh {
		t.Fatalf("exact-reason failure evidence = %#v, want direct/high", failureEvidence)
	}
	if byType[capacityapi.ActivityConfigChange].State != capacityapi.ActivityCompleted {
		t.Fatalf("config change = %#v", byType[capacityapi.ActivityConfigChange])
	}
	configEvidence := byType[capacityapi.ActivityConfigChange].Evidence
	if len(configEvidence) != 1 || configEvidence[0].Relationship != capacityapi.EvidenceDirect || configEvidence[0].Confidence != capacityapi.ConfidenceHigh {
		t.Fatalf("diff-classified config evidence = %#v, want direct/high", configEvidence)
	}
}

func TestBuildActivityRecordsKeepsStructuredConditionEvidenceDirect(t *testing.T) {
	now := time.Date(2026, time.July, 13, 10, 0, 0, 0, time.UTC)
	records := BuildActivityRecords([]timeline.TimelineEvent{{
		ID: "condition-failed", Seq: 1, Timestamp: now, Source: timeline.SourceInformer,
		Kind: "NodeClaim", APIVersion: "karpenter.sh/v1", Name: "claim-a", UID: "claim-uid", EventType: timeline.EventTypeUpdate,
		Diff: &k8score.DiffInfo{Fields: []k8score.FieldChange{{Path: "status.conditions[Launched]", NewValue: "False\x00LaunchFailed"}}},
	}})
	if len(records) != 1 || len(records[0].Episode.Evidence) != 1 {
		t.Fatalf("structured condition records = %#v", records)
	}
	evidence := records[0].Episode.Evidence[0]
	if evidence.Relationship != capacityapi.EvidenceDirect || evidence.Confidence != capacityapi.ConfidenceHigh {
		t.Fatalf("structured condition evidence = %q/%q, want direct/high", evidence.Relationship, evidence.Confidence)
	}
}

func TestBuildActivityRecordsClosesProvisionAttemptWithFailure(t *testing.T) {
	now := time.Date(2026, time.July, 13, 10, 0, 0, 0, time.UTC)
	records := BuildActivityRecords([]timeline.TimelineEvent{
		{ID: "created", Seq: 1, Timestamp: now, Source: timeline.SourceInformer, Kind: "NodeClaim", APIVersion: "karpenter.sh/v1", Name: "claim-a", UID: "claim-uid", EventType: timeline.EventTypeAdd},
		{ID: "failed", Seq: 2, Timestamp: now.Add(time.Minute), Source: timeline.SourceK8sEvent, Kind: "NodeClaim", APIVersion: "karpenter.sh/v1", Namespace: "default", Name: "claim-a", UID: "claim-uid", EventType: timeline.EventTypeWarning, Reason: "LaunchFailed"},
	})
	if len(records) != 1 {
		t.Fatalf("records = %#v, want one provisioning attempt", records)
	}
	episode := records[0].Episode
	if episode.Type != capacityapi.ActivityLaunchFailure || episode.State != capacityapi.ActivityFailed || episode.EndedAt == nil || len(episode.Evidence) != 2 {
		t.Fatalf("failed attempt = %#v", episode)
	}
}

func TestBuildActivityRecordsRecoversFailedProvisionWhenClaimBecomesReady(t *testing.T) {
	now := time.Date(2026, time.July, 13, 10, 0, 0, 0, time.UTC)
	records := BuildActivityRecords([]timeline.TimelineEvent{
		{ID: "created", Seq: 1, Timestamp: now, Source: timeline.SourceInformer, Kind: "NodeClaim", APIVersion: "karpenter.sh/v1", Name: "claim-a", UID: "claim-uid", EventType: timeline.EventTypeAdd},
		{ID: "failed", Seq: 2, Timestamp: now.Add(time.Minute), Source: timeline.SourceK8sEvent, Kind: "NodeClaim", APIVersion: "karpenter.sh/v1", Name: "claim-a", UID: "claim-uid", EventType: timeline.EventTypeWarning, Reason: "LaunchFailed"},
		{ID: "ready", Seq: 3, Timestamp: now.Add(2 * time.Minute), Source: timeline.SourceK8sEvent, Kind: "NodeClaim", APIVersion: "karpenter.sh/v1", Name: "claim-a", UID: "claim-uid", EventType: timeline.EventTypeNormal, Reason: "Ready"},
	})
	if len(records) != 1 {
		t.Fatalf("records = %#v, want one provisioning attempt", records)
	}
	episode := records[0].Episode
	if episode.Type != capacityapi.ActivityProvision || episode.State != capacityapi.ActivityCompleted {
		t.Fatalf("recovered attempt = %q/%q, want provision/completed", episode.Type, episode.State)
	}
	if episode.PrimaryReasonCode != "nodeclaim_ready" || episode.EndedAt == nil || !episode.EndedAt.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("recovered attempt reason/end = %q/%v", episode.PrimaryReasonCode, episode.EndedAt)
	}
	if len(episode.Evidence) != 3 {
		t.Fatalf("recovered attempt evidence = %#v, want creation, failure, and ready", episode.Evidence)
	}
}

func TestBuildActivityRecordsDeleteClosesUnfinishedProvision(t *testing.T) {
	now := time.Date(2026, time.July, 13, 10, 0, 0, 0, time.UTC)
	claim := func(id string, seq int64, at time.Time, eventType timeline.EventType, reason string) timeline.TimelineEvent {
		return timeline.TimelineEvent{
			ID: id, Seq: seq, Timestamp: at, Source: timeline.SourceInformer,
			Kind: "NodeClaim", APIVersion: "karpenter.sh/v1", Name: "claim-a", UID: "claim-uid",
			EventType: eventType, Reason: reason,
		}
	}
	byType := func(records []ActivityRecord) map[capacityapi.ActivityType]capacityapi.ActivityEpisode {
		out := map[capacityapi.ActivityType]capacityapi.ActivityEpisode{}
		for _, record := range records {
			out[record.Episode.Type] = record.Episode
		}
		return out
	}

	// Karpenter's registration-timeout path records the failing stage BEFORE it
	// deletes the claim, so a real timeout still terminalizes as failed.
	records := BuildActivityRecords([]timeline.TimelineEvent{
		claim("created", 1, now, timeline.EventTypeAdd, ""),
		claim("launch-failed", 2, now.Add(time.Minute), timeline.EventTypeWarning, "LaunchFailed"),
		claim("deleted", 3, now.Add(15*time.Minute), timeline.EventTypeDelete, ""),
	})
	// The claim's own attempt episode is already terminal-failed on that
	// evidence when the delete arrives, so the fleet still reads as broken.
	failed := byType(records)[capacityapi.ActivityLaunchFailure]
	if failed.State != capacityapi.ActivityFailed || failed.PrimaryReasonCode != "launch_failed" {
		t.Fatalf("deletion after a recorded failure = %q/%q, want failed/launch_failed", failed.State, failed.PrimaryReasonCode)
	}
	if _, ended := byType(records)[capacityapi.ActivityProvision]; ended {
		t.Fatalf("a failed attempt must not also produce a cause-unknown provision episode: %#v", records)
	}

	// The same holds for a failing lifecycle CONDITION transition, which is what
	// the informer records on the ICE and registration-timeout paths — the real
	// storms Codex's contest was worried about regressing.
	conditionFailure := claim("launched-unknown", 2, now.Add(time.Minute), timeline.EventTypeUpdate, "")
	conditionFailure.Diff = &k8score.DiffInfo{Fields: []k8score.FieldChange{{
		Path: "status.conditions[Launched]", NewValue: "Unknown\x00InsufficientInstanceCapacity",
	}}}
	records = BuildActivityRecords([]timeline.TimelineEvent{
		claim("created", 1, now, timeline.EventTypeAdd, ""),
		conditionFailure,
		claim("deleted", 3, now.Add(15*time.Minute), timeline.EventTypeDelete, ""),
	})
	ice := byType(records)[capacityapi.ActivityLaunchFailure]
	if ice.State != capacityapi.ActivityFailed {
		t.Fatalf("deletion after an ICE condition transition = %q, want failed", ice.State)
	}
	for _, record := range records {
		if record.Episode.State == capacityapi.ActivityEnded {
			t.Fatalf("an ICE-failed claim was softened to ended: %#v", record.Episode)
		}
	}

	// A bare create→delete carries NO cause. The episode is over, but calling it
	// "failed" would manufacture a diagnosis out of an absence of evidence.
	records = BuildActivityRecords([]timeline.TimelineEvent{
		claim("created", 1, now, timeline.EventTypeAdd, ""),
		claim("deleted", 2, now.Add(15*time.Minute), timeline.EventTypeDelete, ""),
	})
	if len(records) != 2 {
		t.Fatalf("records = %#v, want provision and termination", records)
	}
	provision := byType(records)[capacityapi.ActivityProvision]
	if provision.State != capacityapi.ActivityEnded || provision.PrimaryReasonCode != "nodeclaim_deleted_cause_unknown" {
		t.Fatalf("cause-free deletion = %q/%q, want ended/nodeclaim_deleted_cause_unknown", provision.State, provision.PrimaryReasonCode)
	}
	if provision.Summary != "NodeClaim deleted before becoming Ready — cause not recorded" {
		t.Fatalf("ended provision summary = %q, must not assert a failure", provision.Summary)
	}
	if strings.Contains(strings.ToLower(provision.Summary), "failed") {
		t.Fatalf("ended provision summary claims failure: %q", provision.Summary)
	}
	// It is still terminal: closed out with an end time and the closing evidence.
	if provision.EndedAt == nil || !provision.EndedAt.Equal(now.Add(15*time.Minute)) || len(provision.Evidence) != 2 {
		t.Fatalf("unfinished provision end/evidence = %v/%d", provision.EndedAt, len(provision.Evidence))
	}
	if byType(records)[capacityapi.ActivityTermination].State != capacityapi.ActivityCompleted {
		t.Fatalf("termination = %#v", byType(records)[capacityapi.ActivityTermination])
	}
	// Pagination cursors cut on MaxSeq alone — the closed pair must never
	// share a sequence or a page boundary between them drops one.
	if records[0].MaxSeq == records[1].MaxSeq {
		t.Fatalf("paired episodes share MaxSeq %d — a page boundary would drop one", records[0].MaxSeq)
	}

	// A claim that became Ready first keeps its completed provision intact.
	records = BuildActivityRecords([]timeline.TimelineEvent{
		{ID: "created", Seq: 1, Timestamp: now, Source: timeline.SourceInformer, Kind: "NodeClaim", APIVersion: "karpenter.sh/v1", Name: "claim-b", UID: "claim-b-uid", EventType: timeline.EventTypeAdd},
		{ID: "ready", Seq: 2, Timestamp: now.Add(2 * time.Minute), Source: timeline.SourceK8sEvent, Kind: "NodeClaim", APIVersion: "karpenter.sh/v1", Name: "claim-b", UID: "claim-b-uid", EventType: timeline.EventTypeNormal, Reason: "Ready"},
		{ID: "deleted", Seq: 3, Timestamp: now.Add(3 * time.Hour), Source: timeline.SourceInformer, Kind: "NodeClaim", APIVersion: "karpenter.sh/v1", Name: "claim-b", UID: "claim-b-uid", EventType: timeline.EventTypeDelete},
	})
	provision = byType(records)[capacityapi.ActivityProvision]
	if provision.State != capacityapi.ActivityCompleted || provision.PrimaryReasonCode != "nodeclaim_ready" {
		t.Fatalf("ready-then-deleted provision = %q/%q, want completed/nodeclaim_ready", provision.State, provision.PrimaryReasonCode)
	}
}

func TestBuildActivityRecordsCompletedProvisionSurvivesLaterFailureSignal(t *testing.T) {
	now := time.Date(2026, time.July, 13, 10, 0, 0, 0, time.UTC)
	records := BuildActivityRecords([]timeline.TimelineEvent{
		{ID: "created", Seq: 1, Timestamp: now, Source: timeline.SourceInformer, Kind: "NodeClaim", APIVersion: "karpenter.sh/v1", Name: "claim-a", UID: "claim-uid", EventType: timeline.EventTypeAdd},
		{ID: "ready", Seq: 2, Timestamp: now.Add(2 * time.Minute), Source: timeline.SourceK8sEvent, Kind: "NodeClaim", APIVersion: "karpenter.sh/v1", Name: "claim-a", UID: "claim-uid", EventType: timeline.EventTypeNormal, Reason: "Ready"},
		// Post-Ready health degradation — a failure signal, not a
		// provisioning outcome. History must survive it.
		{ID: "degraded", Seq: 3, Timestamp: now.Add(time.Hour), Source: timeline.SourceInformer, Kind: "NodeClaim", APIVersion: "karpenter.sh/v1", Name: "claim-a", UID: "claim-uid", EventType: timeline.EventTypeUpdate,
			Diff: &k8score.DiffInfo{Fields: []k8score.FieldChange{{Path: "status.conditions[Ready]", NewValue: "Unknown\x00NodeRepairFailed"}}}},
	})
	if len(records) != 1 {
		t.Fatalf("records = %#v, want one provision episode", records)
	}
	episode := records[0].Episode
	if episode.State != capacityapi.ActivityCompleted || episode.PrimaryReasonCode != "nodeclaim_ready" {
		t.Fatalf("completed provision after later failure = %q/%q, want completed/nodeclaim_ready", episode.State, episode.PrimaryReasonCode)
	}
	if len(episode.Evidence) != 3 {
		t.Fatalf("evidence = %d, want the failure signal retained as history", len(episode.Evidence))
	}
}

func TestBuildActivityRecordsDoesNotReadFailureVocabularyAsCompletion(t *testing.T) {
	now := time.Date(2026, time.July, 13, 10, 0, 0, 0, time.UTC)
	records := BuildActivityRecords([]timeline.TimelineEvent{
		{ID: "created", Seq: 1, Timestamp: now, Source: timeline.SourceInformer, Kind: "NodeClaim", APIVersion: "karpenter.sh/v1", Name: "claim-a", UID: "claim-uid", EventType: timeline.EventTypeAdd},
		{ID: "not-initialized", Seq: 2, Timestamp: now.Add(time.Minute), Source: timeline.SourceK8sEvent, Kind: "NodeClaim", APIVersion: "karpenter.sh/v1", Name: "claim-a", UID: "claim-uid", EventType: timeline.EventTypeWarning, Reason: "NodeNotInitialized"},
	})
	if len(records) != 1 {
		t.Fatalf("records = %#v, want the open provision only", records)
	}
	episode := records[0].Episode
	// In-progress vocabulary is neither a completion nor a failure — the
	// claim is still provisioning and the episode must stay open.
	if episode.State != capacityapi.ActivityOpen || episode.Type != capacityapi.ActivityProvision {
		t.Fatalf("NodeNotInitialized episode = %q/%q, want open provision", episode.Type, episode.State)
	}

	// Failure text still infers a stage failure.
	records = BuildActivityRecords([]timeline.TimelineEvent{
		{ID: "created", Seq: 1, Timestamp: now, Source: timeline.SourceInformer, Kind: "NodeClaim", APIVersion: "karpenter.sh/v1", Name: "claim-b", UID: "claim-b-uid", EventType: timeline.EventTypeAdd},
		{ID: "init-failed", Seq: 2, Timestamp: now.Add(time.Minute), Source: timeline.SourceK8sEvent, Kind: "NodeClaim", APIVersion: "karpenter.sh/v1", Name: "claim-b", UID: "claim-b-uid", EventType: timeline.EventTypeWarning, Reason: "InitializationTimeout", Message: "node failed to initialize"},
	})
	if len(records) != 1 || records[0].Episode.State != capacityapi.ActivityFailed {
		t.Fatalf("failure-text warning = %#v, want failed provision attempt", records)
	}
}

func TestClassifyNodeClaimReadyRollupFailure(t *testing.T) {
	now := time.Date(2026, time.July, 13, 10, 0, 0, 0, time.UTC)
	records := BuildActivityRecords([]timeline.TimelineEvent{{
		ID: "ready-failed", Seq: 1, Timestamp: now, Source: timeline.SourceInformer,
		Kind: "NodeClaim", APIVersion: "karpenter.sh/v1", Name: "claim-a", UID: "claim-uid", EventType: timeline.EventTypeUpdate,
		Diff: &k8score.DiffInfo{Fields: []k8score.FieldChange{{Path: "status.conditions[Ready]", NewValue: "Unknown\x00LaunchFailed"}}},
	}})
	if len(records) != 1 || records[0].Episode.State != capacityapi.ActivityFailed || records[0].Episode.PrimaryReasonCode != "nodeclaim_not_ready" {
		t.Fatalf("ready rollup failure = %#v, want provision failed nodeclaim_not_ready", records)
	}

	// A bare Ready=False is a claim still provisioning — never a failure.
	records = BuildActivityRecords([]timeline.TimelineEvent{{
		ID: "still-provisioning", Seq: 1, Timestamp: now, Source: timeline.SourceInformer,
		Kind: "NodeClaim", APIVersion: "karpenter.sh/v1", Name: "claim-b", UID: "claim-b-uid", EventType: timeline.EventTypeUpdate,
		Diff: &k8score.DiffInfo{Fields: []k8score.FieldChange{{Path: "status.conditions[Ready]", NewValue: "False\x00NotInitialized"}}},
	}})
	if len(records) != 0 {
		t.Fatalf("bare Ready=False classified: %#v", records)
	}

	// When the failing sub-condition arrives in the same diff, it wins the
	// attribution even when Ready appears first.
	records = BuildActivityRecords([]timeline.TimelineEvent{{
		ID: "both", Seq: 1, Timestamp: now, Source: timeline.SourceInformer,
		Kind: "NodeClaim", APIVersion: "karpenter.sh/v1", Name: "claim-c", UID: "claim-c-uid", EventType: timeline.EventTypeUpdate,
		Diff: &k8score.DiffInfo{Fields: []k8score.FieldChange{
			{Path: "status.conditions[Ready]", NewValue: "Unknown\x00RegistrationFailed"},
			{Path: "status.conditions[Registered]", NewValue: "Unknown\x00RegistrationFailed"},
		}},
	}})
	if len(records) != 1 || records[0].Episode.Type != capacityapi.ActivityRegistrationFailure {
		t.Fatalf("sub-condition attribution = %#v, want registration failure", records)
	}
}

func TestBuildActivityRecordsKeepsTerminalMetadataWhenInformerAddArrivesLater(t *testing.T) {
	createdAt := time.Date(2026, time.July, 13, 9, 58, 0, 0, time.UTC)
	readyAt := createdAt.Add(90 * time.Second)
	observedAt := createdAt.Add(3 * time.Minute)
	records := BuildActivityRecords([]timeline.TimelineEvent{
		{ID: "ready", Seq: 2, Timestamp: readyAt, Source: timeline.SourceK8sEvent, Kind: "NodeClaim", APIVersion: "karpenter.sh/v1", Name: "claim-a", UID: "claim-uid", CreatedAt: &createdAt, EventType: timeline.EventTypeNormal, Reason: "Ready"},
		{ID: "created", Seq: 1, Timestamp: observedAt, Source: timeline.SourceInformer, Kind: "NodeClaim", APIVersion: "karpenter.sh/v1", Name: "claim-a", UID: "claim-uid", CreatedAt: &createdAt, EventType: timeline.EventTypeAdd},
	})
	if len(records) != 1 {
		t.Fatalf("records = %#v, want one provisioning attempt", records)
	}
	episode := records[0].Episode
	if episode.Type != capacityapi.ActivityProvision || episode.State != capacityapi.ActivityCompleted || episode.PrimaryReasonCode != "nodeclaim_ready" {
		t.Fatalf("terminal type/state/reason = %q/%q/%q", episode.Type, episode.State, episode.PrimaryReasonCode)
	}
	if !episode.StartedAt.Equal(createdAt) || episode.EndedAt == nil || !episode.EndedAt.Equal(readyAt) || episode.DurationSeconds == nil || *episode.DurationSeconds != 90 {
		t.Fatalf("terminal timing = start %s end %v duration %v", episode.StartedAt, episode.EndedAt, episode.DurationSeconds)
	}
}

func TestBuildActivityRecordsSkipsKarpenterEventsWithoutResourceNames(t *testing.T) {
	records := BuildActivityRecords([]timeline.TimelineEvent{{
		ID: "controller-finalized", Seq: 1, Timestamp: time.Now(), Source: timeline.SourceK8sEvent,
		Kind: "NodeClaim", APIVersion: "karpenter.sh/v1", EventType: timeline.EventTypeNormal,
		Reason: "Finalized", Message: "Finalized karpenter.sh/termination",
	}})
	if len(records) != 0 {
		t.Fatalf("subjectless controller event produced activity: %#v", records)
	}
}

func TestBuildActivityRecordsKeepsRepeatedDisruptionsDistinct(t *testing.T) {
	now := time.Date(2026, time.July, 13, 10, 0, 0, 0, time.UTC)
	records := BuildActivityRecords([]timeline.TimelineEvent{
		{ID: "disrupt-1", Seq: 1, Timestamp: now, Source: timeline.SourceK8sEvent, Kind: "NodePool", APIVersion: "karpenter.sh/v1", Name: "general", UID: "pool-uid", EventType: timeline.EventTypeNormal, Reason: "DisruptionBlocked"},
		{ID: "disrupt-2", Seq: 2, Timestamp: now.Add(time.Hour), Source: timeline.SourceK8sEvent, Kind: "NodePool", APIVersion: "karpenter.sh/v1", Name: "general", UID: "pool-uid", EventType: timeline.EventTypeNormal, Reason: "DisruptionBlocked"},
	})
	if len(records) != 2 || records[0].Episode.ID == records[1].Episode.ID {
		t.Fatalf("disruption attempts = %#v", records)
	}
	for _, record := range records {
		if record.Episode.State != capacityapi.ActivityBlocked || record.Episode.EndedAt != nil {
			t.Fatalf("disruption-blocked observation = %#v", record.Episode)
		}
		if record.Episode.Summary != "Karpenter disruption is blocked" {
			t.Fatalf("blocked summary = %q", record.Episode.Summary)
		}
	}
}

func TestBuildActivityRecordsDoesNotInventEventLifecycle(t *testing.T) {
	now := time.Date(2026, time.July, 13, 10, 0, 0, 0, time.UTC)
	records := BuildActivityRecords([]timeline.TimelineEvent{
		{ID: "interrupt", Seq: 1, Timestamp: now, Source: timeline.SourceK8sEvent, Kind: "NodeClaim", APIVersion: "karpenter.sh/v1", Name: "claim-a", UID: "claim-uid", EventType: timeline.EventTypeNormal, Reason: "SpotInterruption"},
		{ID: "terminating", Seq: 2, Timestamp: now.Add(time.Minute), Source: timeline.SourceK8sEvent, Kind: "NodeClaim", APIVersion: "karpenter.sh/v1", Name: "claim-a", UID: "claim-uid", EventType: timeline.EventTypeNormal, Reason: "Terminating"},
	})
	if len(records) != 2 {
		t.Fatalf("records = %#v", records)
	}
	for _, record := range records {
		if record.Episode.State != capacityapi.ActivityObserved {
			t.Fatalf("event-only activity state = %q, want observed", record.Episode.State)
		}
	}
}

func TestBuildActivityRecordsKeepsConfigChangesAsDistinctEpisodes(t *testing.T) {
	now := time.Date(2026, time.July, 13, 10, 0, 0, 0, time.UTC)
	events := []timeline.TimelineEvent{
		{
			ID: "pool-update-1", Seq: 10, Timestamp: now, Source: timeline.SourceInformer,
			Kind: "NodePool", APIVersion: "karpenter.sh/v1", Name: "general", UID: "pool-uid", EventType: timeline.EventTypeUpdate,
			Diff: &k8score.DiffInfo{Fields: []k8score.FieldChange{{Path: "metadata.generation"}}},
		},
		{
			ID: "pool-update-2", Seq: 20, Timestamp: now.Add(time.Hour), Source: timeline.SourceInformer,
			Kind: "NodePool", APIVersion: "karpenter.sh/v1", Name: "general", UID: "pool-uid", EventType: timeline.EventTypeUpdate,
			Diff: &k8score.DiffInfo{Fields: []k8score.FieldChange{{Path: "spec.disruption"}}},
		},
	}

	records := BuildActivityRecords(events)
	if len(records) != 2 || records[0].Episode.ID == records[1].Episode.ID {
		t.Fatalf("config changes = %#v, want two stable episodes", records)
	}
}

func TestBuildActivityRecordsDoesNotGuessUIDAcrossResourceRecreation(t *testing.T) {
	now := time.Date(2026, time.July, 13, 10, 0, 0, 0, time.UTC)
	events := []timeline.TimelineEvent{
		{ID: "old", Seq: 1, Timestamp: now, Source: timeline.SourceInformer, Kind: "NodeClaim", APIVersion: "karpenter.sh/v1", Name: "claim-a", UID: "old-uid", EventType: timeline.EventTypeAdd},
		{ID: "new", Seq: 2, Timestamp: now.Add(time.Hour), Source: timeline.SourceInformer, Kind: "NodeClaim", APIVersion: "karpenter.sh/v1", Name: "claim-a", UID: "new-uid", EventType: timeline.EventTypeAdd},
		{ID: "uidless-ready", Seq: 3, Timestamp: now.Add(2 * time.Hour), Source: timeline.SourceK8sEvent, Kind: "NodeClaim", APIVersion: "karpenter.sh/v1", Name: "claim-a", Reason: "Ready", EventType: timeline.EventTypeNormal},
	}

	records := BuildActivityRecords(events)
	if len(records) != 3 {
		t.Fatalf("records = %#v, want ambiguous UID-less evidence kept separate", records)
	}
	for _, record := range records {
		if record.MaxSeq == 3 && record.Episode.Claim != nil && record.Episode.Claim.UID != "" {
			t.Fatalf("UID-less evidence was attributed to recreated claim UID %q", record.Episode.Claim.UID)
		}
	}
}

func TestClassifyKarpenterExactEventVocabulary(t *testing.T) {
	now := time.Date(2026, time.July, 13, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		reason    string
		eventType timeline.EventType
		wantType  capacityapi.ActivityType
		wantState capacityapi.ActivityState
	}{
		// InsufficientCapacityError previously matched no substring at all —
		// an ICE storm was invisible except as successful-looking terminations.
		{"InsufficientCapacityError", timeline.EventTypeWarning, capacityapi.ActivityLaunchFailure, capacityapi.ActivityFailed},
		// Blocked, not failed: the NodePool matches no instance type, so
		// provisioning never started — there is no NodeClaim that failed.
		{"NoCompatibleInstanceTypes", timeline.EventTypeNormal, capacityapi.ActivityProvision, capacityapi.ActivityBlocked},
		// DisruptionBlocked/Unconsolidatable previously read as disruption
		// *happening* — the opposite of their meaning.
		{"DisruptionBlocked", timeline.EventTypeNormal, capacityapi.ActivityDisruption, capacityapi.ActivityBlocked},
		{"Unconsolidatable", timeline.EventTypeNormal, capacityapi.ActivityDisruption, capacityapi.ActivityBlocked},
		{"SpotInterrupted", timeline.EventTypeWarning, capacityapi.ActivityInterruption, capacityapi.ActivityObserved},
		{"FailedDraining", timeline.EventTypeWarning, capacityapi.ActivityTermination, capacityapi.ActivityFailed},
	}
	for _, tc := range cases {
		records := BuildActivityRecords([]timeline.TimelineEvent{{
			ID: "e", Seq: 1, Timestamp: now, Source: timeline.SourceK8sEvent,
			Kind: "NodeClaim", APIVersion: "karpenter.sh/v1", Name: "claim-a", UID: "claim-uid",
			EventType: tc.eventType, Reason: tc.reason,
		}})
		if len(records) != 1 {
			t.Fatalf("%s: records = %#v", tc.reason, records)
		}
		episode := records[0].Episode
		if episode.Type != tc.wantType || episode.State != tc.wantState {
			t.Errorf("%s: classified %s/%s, want %s/%s", tc.reason, episode.Type, episode.State, tc.wantType, tc.wantState)
		}
		if len(episode.Evidence) != 1 || episode.Evidence[0].Relationship != capacityapi.EvidenceDirect || episode.Evidence[0].Confidence != capacityapi.ConfidenceHigh {
			t.Errorf("%s: exact-reason evidence should be direct/high, got %#v", tc.reason, episode.Evidence)
		}
	}
}

func TestClassifyNodeClaimUnknownFailureConditionDiff(t *testing.T) {
	now := time.Date(2026, time.July, 13, 10, 0, 0, 0, time.UTC)
	records := BuildActivityRecords([]timeline.TimelineEvent{{
		ID: "diff", Seq: 1, Timestamp: now, Source: timeline.SourceInformer,
		Kind: "NodeClaim", APIVersion: "karpenter.sh/v1", Name: "claim-a", UID: "claim-uid",
		EventType: timeline.EventTypeUpdate,
		Diff:      &k8score.DiffInfo{Fields: []k8score.FieldChange{{Path: "status.conditions[Launched]", NewValue: "Unknown\x00LaunchFailed"}}},
	}})
	if len(records) != 1 || records[0].Episode.Type != capacityapi.ActivityLaunchFailure || records[0].Episode.State != capacityapi.ActivityFailed {
		t.Fatalf("Unknown/LaunchFailed condition diff = %#v, want launch_failure/failed", records)
	}
}

// TestNoCompatibleInstanceTypesKeepsPerOccurrenceEpisodes pins the correlation
// boundary. The event is emitted by the NODEPOOL, so folding it into the
// provision-by-UID bucket produced one eternal open episode summarized as a
// NodeClaim provisioning failure — for a claim Karpenter never created.
func TestNoCompatibleInstanceTypesKeepsPerOccurrenceEpisodes(t *testing.T) {
	now := time.Date(2026, time.July, 13, 10, 0, 0, 0, time.UTC)
	event := func(id string, at time.Time, seq int64) timeline.TimelineEvent {
		return timeline.TimelineEvent{
			ID: id, Seq: seq, Timestamp: at, Source: timeline.SourceK8sEvent,
			Kind: "NodePool", APIVersion: "karpenter.sh/v1", Name: "gpu", UID: "gpu-uid",
			EventType: timeline.EventTypeNormal, Reason: "NoCompatibleInstanceTypes",
			Message: "no instance type satisfied the requirements",
		}
	}
	records := BuildActivityRecords([]timeline.TimelineEvent{
		event("attempt-1", now, 1),
		event("attempt-2", now.Add(20*time.Minute), 2),
	})
	if len(records) != 2 {
		t.Fatalf("records = %d, want one per occurrence: %#v", len(records), records)
	}
	for _, record := range records {
		episode := record.Episode
		if episode.Type != capacityapi.ActivityProvision || episode.State != capacityapi.ActivityBlocked {
			t.Fatalf("episode = %s/%s, want provision/blocked", episode.Type, episode.State)
		}
		if episode.Summary != "NodePool gpu: provisioning could not start — no compatible instance types" {
			t.Fatalf("summary = %q — it must name the NodePool and never claim a NodeClaim failed", episode.Summary)
		}
		if episode.Claim != nil {
			t.Fatalf("blocked provisioning invented a NodeClaim: %#v", episode.Claim)
		}
		if !episode.StartedAt.Equal(episode.Evidence[0].At) {
			t.Fatalf("startedAt = %s, want the occurrence time %s (nothing was created to backdate to)", episode.StartedAt, episode.Evidence[0].At)
		}
	}

	// The claim-lifecycle path is untouched: every stage of ONE claim still
	// folds into a single provision episode keyed by the claim's UID.
	claimRecords := BuildActivityRecords([]timeline.TimelineEvent{
		{
			ID: "launched", Seq: 1, Timestamp: now, Source: timeline.SourceK8sEvent,
			Kind: "NodeClaim", APIVersion: "karpenter.sh/v1", Name: "claim-a", UID: "claim-uid",
			EventType: timeline.EventTypeNormal, Reason: "Launched",
		},
		{
			ID: "failed", Seq: 2, Timestamp: now.Add(time.Minute), Source: timeline.SourceK8sEvent,
			Kind: "NodeClaim", APIVersion: "karpenter.sh/v1", Name: "claim-a", UID: "claim-uid",
			EventType: timeline.EventTypeWarning, Reason: "LaunchFailed",
		},
	})
	if len(claimRecords) != 1 {
		t.Fatalf("claim lifecycle records = %d, want one merged provision episode: %#v", len(claimRecords), claimRecords)
	}
	if claimRecords[0].Episode.State != capacityapi.ActivityFailed {
		t.Fatalf("claim lifecycle state = %q, want failed", claimRecords[0].Episode.State)
	}
}

func TestKarpenterEventVocabularyTailClassifiesDirect(t *testing.T) {
	for reason, want := range map[string]struct {
		activityType capacityapi.ActivityType
		state        capacityapi.ActivityState
		code         string
	}{
		"AwaitingVolumeDetachment":               {capacityapi.ActivityTermination, capacityapi.ActivityOpen, "awaiting_volume_detachment"},
		"ConsolidationApproved":                  {capacityapi.ActivityDisruption, capacityapi.ActivityOpen, "consolidation_approved"},
		"FailedConsistencyCheck":                 {capacityapi.ActivityProvision, capacityapi.ActivityObserved, "consistency_check_failed"},
		"TerminatingOnInterruption":              {capacityapi.ActivityInterruption, capacityapi.ActivityObserved, "terminating_on_interruption"},
		"CapacityReservationInstanceInterrupted": {capacityapi.ActivityInterruption, capacityapi.ActivityObserved, "capacity_reservation_interrupted"},
		"ZonalShiftActive":                       {capacityapi.ActivityInterruption, capacityapi.ActivityObserved, "zonal_shift_active"},
		"ZonalShiftCleared":                      {capacityapi.ActivityInterruption, capacityapi.ActivityObserved, "zonal_shift_cleared"},
	} {
		classification, ok := karpenterEventClassifications[reason]
		if !ok {
			t.Errorf("%s: missing from the exact table — would degrade to inferred or drop", reason)
			continue
		}
		if classification.activityType != want.activityType || classification.state != want.state || classification.reasonCode != want.code {
			t.Errorf("%s = %+v, want %+v", reason, classification, want)
		}
	}
}
