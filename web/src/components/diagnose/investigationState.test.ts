import { describe, expect, it } from "vitest";
import { DiagnoseError } from "../../api/diagnose";
import {
  canOfferInvestigationApply,
  initialInvestigationPane,
  investigationEvidenceAnnouncement,
  investigationEvidenceShouldMarkUnread,
  investigationApplyCompletionEffects,
  investigationApplyAttemptVerified,
  investigationAssessmentNeedsCurrentStateVerification,
  investigationApplyRejectionIsDefinitive,
  investigationApplyTerminalNeedsClusterRefresh,
  investigationTurnWithTerminalEvent,
  investigationClosedEventIsLive,
  investigationClosedRunIsUnavailable,
  investigationEvidenceInputsEqual,
  investigationEvidenceCoverageLimited,
  investigationEvidenceConflictsWithHealthy,
  investigationHealthConflictExplainedBy,
  investigationEndedBeforeConclusion,
  investigationHistoryUnavailablePresentation,
  investigationInteractionsBlocked,
  investigationIsReadOnly,
  investigationPaneCenteredScrollTop,
  investigationLiveCaseTurnIndex,
} from "./investigationState";

describe("investigation terminal presentation", () => {
  it("centers source navigation inside its pane and clamps at both ends", () => {
    expect(
      investigationPaneCenteredScrollTop({
        scrollTop: 400,
        viewportHeight: 600,
        contentHeight: 2_000,
        targetTop: 500,
        targetHeight: 100,
      }),
    ).toBe(650);
    expect(
      investigationPaneCenteredScrollTop({
        scrollTop: 0,
        viewportHeight: 600,
        contentHeight: 2_000,
        targetTop: 20,
        targetHeight: 40,
      }),
    ).toBe(0);
    expect(
      investigationPaneCenteredScrollTop({
        scrollTop: 1_300,
        viewportHeight: 600,
        contentHeight: 2_000,
        targetTop: 580,
        targetHeight: 80,
      }),
    ).toBe(1_400);
  });

  it("opens successful and stale runs on Evidence", () => {
    expect(initialInvestigationPane("done")).toBe("evidence");
    expect(initialInvestigationPane("stale")).toBe("evidence");
  });

  it("opens running and ended-early runs on Activity", () => {
    expect(initialInvestigationPane("running")).toBe("activity");
    expect(initialInvestigationPane("error")).toBe("activity");
    expect(initialInvestigationPane("stopped")).toBe("activity");
  });

  it("marks new evidence unread only when Findings is actually hidden", () => {
    expect(
      investigationEvidenceShouldMarkUnread({
        hasNewLiveSource: true,
        selectedPane: "activity",
        evidencePaneVisible: false,
      }),
    ).toBe(true);
    expect(
      investigationEvidenceShouldMarkUnread({
        hasNewLiveSource: true,
        selectedPane: "activity",
        evidencePaneVisible: true,
      }),
    ).toBe(false);
    expect(
      investigationEvidenceShouldMarkUnread({
        hasNewLiveSource: true,
        selectedPane: "evidence",
        evidencePaneVisible: true,
      }),
    ).toBe(false);
  });

  it("announces hidden and scrolled-away evidence without duplicating updates", () => {
    expect(
      investigationEvidenceAnnouncement({
        unreadEvidence: true,
        evidenceUpdateAvailable: true,
      }),
    ).toBe("New evidence available");
    expect(
      investigationEvidenceAnnouncement({
        unreadEvidence: false,
        evidenceUpdateAvailable: true,
      }),
    ).toBe("New evidence available in Findings.");
    expect(
      investigationEvidenceAnnouncement({
        unreadEvidence: false,
        evidenceUpdateAvailable: false,
      }),
    ).toBe("");
  });

  it("makes stale and unavailable runs read-only", () => {
    expect(investigationIsReadOnly("stale", false)).toBe(true);
    expect(investigationIsReadOnly("done", true)).toBe(true);
    expect(investigationIsReadOnly("done", false)).toBe(false);
  });

  it("distinguishes retrying from permanent history failures", () => {
    expect(
      investigationHistoryUnavailablePresentation({
        error: "history store is busy.",
        retryable: true,
      }),
    ).toMatchObject({
      loading: true,
      title: expect.stringContaining("temporarily"),
    });
    expect(
      investigationHistoryUnavailablePresentation({
        error: "history cannot be decoded",
        retryable: false,
      }),
    ).toEqual({
      loading: false,
      title: "Saved history is unavailable",
      detail: "history cannot be decoded",
    });
  });

  it("does not call a completed apply failure an incomplete investigation", () => {
    expect(
      investigationEndedBeforeConclusion("error", {
        status: "error",
        apply: true,
      }),
    ).toBe(false);
    expect(
      investigationEndedBeforeConclusion("error", {
        status: "error",
        apply: false,
      }),
    ).toBe(true);
  });

  it.each(["error", "stopped"] as const)(
    "does not discard the assessment after an explanation is %s",
    (status) => {
      expect(
        investigationEndedBeforeConclusion(status, {
          status: "error",
          explainAssessment: 2,
        }),
      ).toBe(false);
    },
  );
});

describe("investigation evidence projection stability", () => {
  it("treats zero projected producer evidence as limited coverage", () => {
    expect(
      investigationEvidenceCoverageLimited({
        limitations: [],
        coverage: { attempted: 0, projected: 0, limited: 0, checked: 0 },
        sources: [],
        groups: [],
      }),
    ).toBe(true);
    expect(
      investigationEvidenceCoverageLimited({
        limitations: [],
        coverage: { attempted: 1, projected: 1, limited: 0, checked: 0 },
        sources: [
          {
            id: "resource",
            tool: "get_resource",
            confirmedSuccess: true,
          },
        ],
        groups: [
          {
            latest: {
              relevance: "target",
              source: { id: "resource" },
            },
          },
        ],
      }),
    ).toBe(true);
    expect(
      investigationEvidenceCoverageLimited({
        limitations: [],
        coverage: { attempted: 1, projected: 1, limited: 0, checked: 0 },
        sources: [
          {
            id: "diagnose",
            tool: "diagnose",
            confirmedSuccess: true,
          },
        ],
        groups: [
          {
            latest: {
              relevance: "target",
              source: { id: "diagnose" },
            },
          },
        ],
      }),
    ).toBe(false);
    expect(
      investigationEvidenceCoverageLimited({
        limitations: [],
        coverage: { attempted: 1, projected: 1, limited: 0, checked: 0 },
        sources: [
          {
            id: "diagnose",
            tool: "diagnose",
            confirmedSuccess: true,
          },
        ],
        groups: [
          {
            latest: {
              relevance: "broader",
              source: { id: "diagnose" },
            },
          },
        ],
      }),
    ).toBe(true);
  });

  it("qualifies a healthy conclusion when active adverse Radar evidence disagrees", () => {
    const group = (
      kind: string,
      tier: "key" | "supporting" | "context",
      relevance: "target" | "producer-related" | "broader" = "target",
      historical = false,
    ) => ({
      kind,
      historical,
      latest: { tier, relevance, tone: "warning" },
    });

    expect(
      investigationEvidenceConflictsWithHealthy({
        groups: [group("issue", "supporting")],
      }),
    ).toBe(true);
    expect(
      investigationEvidenceConflictsWithHealthy({
        groups: [group("logs", "supporting")],
      }),
    ).toBe(true);
    expect(
      investigationEvidenceConflictsWithHealthy({
        groups: [group("events", "supporting")],
      }),
    ).toBe(true);
    expect(
      investigationEvidenceConflictsWithHealthy({
        groups: [group("resource", "context")],
      }),
    ).toBe(false);
    expect(
      investigationEvidenceConflictsWithHealthy({
        groups: [group("issue", "supporting", "broader")],
      }),
    ).toBe(false);
    expect(
      investigationEvidenceConflictsWithHealthy({
        groups: [group("issue", "supporting", "target", true)],
      }),
    ).toBe(false);
    expect(
      investigationEvidenceConflictsWithHealthy({
        groups: [group("changes", "supporting")],
      }),
    ).toBe(false);
  });
  it("names the explained cards only when the agent addressed every conflict", () => {
    const group = (id: string, title: string, tone = "warning") => ({
      id,
      identity: `issue:${id}`,
      kind: "issue",
      historical: false,
      latest: {
        tier: "supporting" as const,
        relevance: "target" as const,
        tone,
        title,
      },
    });
    const projection = {
      groups: [group("g1", "CrashLoopBackOff"), group("g2", "OOMKilled")],
    };
    const note = (
      groupId: string,
      role: string,
      placement: "card" | "revision" | "source" = "card",
      claim = "the agent's reading of this card",
    ) => ({ role, placement, claim, groupId });

    expect(investigationHealthConflictExplainedBy(projection, undefined)).toBe(
      null,
    );
    expect(
      investigationHealthConflictExplainedBy(projection, [
        note("g1", "benign"),
      ]),
    ).toBe(null);
    expect(
      investigationHealthConflictExplainedBy(projection, [
        note("g1", "benign"),
        note("g2", "cause"),
      ]),
    ).toBe(null);
    expect(
      investigationHealthConflictExplainedBy(projection, [
        note("g1", "benign"),
        note("g2", "benign", "source"),
      ]),
    ).toBe(null);
    // A note pinned to a superseded read addressed the card as it was then,
    // not the card the banner is qualifying now.
    expect(
      investigationHealthConflictExplainedBy(projection, [
        note("g1", "benign"),
        note("g2", "benign", "revision"),
      ]),
    ).toBe(null);
    // An empty claim renders nothing, so the banner would be pointing at a
    // note the reader cannot find.
    expect(
      investigationHealthConflictExplainedBy(projection, [
        note("g1", "benign"),
        note("g2", "benign", "card", "   "),
      ]),
    ).toBe(null);
    // "Excludes some hypothesis" and "is peripheral here" are both true of a
    // problem that is still live, so neither reconciles a healthy verdict.
    expect(
      investigationHealthConflictExplainedBy(projection, [
        note("g1", "benign"),
        note("g2", "rules_out"),
      ]),
    ).toBe(null);
    expect(
      investigationHealthConflictExplainedBy(projection, [
        note("g1", "benign"),
        note("g2", "demoted"),
      ]),
    ).toBe(null);
    expect(
      investigationHealthConflictExplainedBy(projection, [
        note("g1", "benign"),
        note("g2", "benign"),
      ]),
    ).toEqual(["CrashLoopBackOff", "OOMKilled"]);
    // The agent contradicting itself on the same card is not an explanation.
    expect(
      investigationHealthConflictExplainedBy(projection, [
        note("g1", "benign"),
        note("g2", "benign"),
        note("g2", "cause"),
      ]),
    ).toBe(null);
    expect(
      investigationHealthConflictExplainedBy({ groups: [] }, [
        note("g1", "benign"),
      ]),
    ).toBe(null);

    // One log stream read through two calls lands in two groups sharing an
    // identity, so the partition must not decide whether the agent addressed
    // it: a note on either twin counts for the conflict recorded on the other.
    const stream = (id: string) => ({
      id,
      identity: "logs:previous:api-abc:api",
      kind: "logs",
      historical: false,
      latest: {
        tier: "supporting" as const,
        relevance: "target" as const,
        tone: "warning",
        title: "Previous logs · api-abc / api",
      },
    });
    expect(
      investigationHealthConflictExplainedBy(
        { groups: [stream("scope-a"), stream("scope-b")] },
        [note("scope-b", "benign")],
      ),
    ).toEqual([
      "Previous logs · api-abc / api",
      "Previous logs · api-abc / api",
    ]);
  });

  it("ignores reasoning-only transcript updates while retaining completed tool identity", () => {
    const completedTool = {
      kind: "tool" as const,
      id: "events-1",
      tool: "get_events",
      status: "done",
      result: '{"events":[]}',
      isError: false,
    };
    const previous = [
      {
        status: "running" as const,
        timeline: [
          completedTool,
          { kind: "thinking" as const, text: "Checking pods" },
        ],
      },
    ];
    const next = [
      {
        status: "running" as const,
        timeline: [
          completedTool,
          { kind: "thinking" as const, text: "Checking pods and owners" },
        ],
      },
    ];

    expect(investigationEvidenceInputsEqual(previous, next)).toBe(true);
  });

  it("invalidates when a completed tool record or verification status changes", () => {
    const completedTool = {
      kind: "tool" as const,
      id: "events-1",
      tool: "get_events",
      status: "done",
      result: '{"events":[]}',
      isError: false,
    };
    const previous = [
      { verify: true, status: "running" as const, timeline: [completedTool] },
    ];

    expect(
      investigationEvidenceInputsEqual(previous, [
        {
          ...previous[0],
          timeline: [{ ...completedTool, result: '{"events":[{}]}' }],
        },
      ]),
    ).toBe(false);
    expect(
      investigationEvidenceInputsEqual(previous, [
        { ...previous[0], status: "done" as const },
      ]),
    ).toBe(false);
  });

  it("checks every turn after an evidence-free prefix", () => {
    const firstTurn = {
      status: "done" as const,
      timeline: [],
    };
    const completedTool = {
      kind: "tool" as const,
      id: "pods-1",
      tool: "get_pods",
      status: "done",
      result: '{"pods":[]}',
      isError: false,
    };
    const previous = [
      firstTurn,
      { status: "running" as const, timeline: [completedTool] },
    ];

    expect(
      investigationEvidenceInputsEqual(previous, [
        firstTurn,
        { status: "done" as const, timeline: [completedTool] },
      ]),
    ).toBe(false);
  });
});

describe("investigation action gating", () => {
  it("retains apply mutation truth across replayed and live terminal events", () => {
    const applyTurn = {
      apply: true,
      timeline: [],
      diagnosis: null,
      error: null,
      status: "running" as const,
    };

    expect(
      investigationTurnWithTerminalEvent(
        applyTurn,
        {
          type: "done",
          diagnosis: {
            rootCause: "",
            report: "Deployment updated.",
            remediation: [],
          },
          applyOutcome: "confirmed",
        },
        false,
      ),
    ).toMatchObject({
      status: "done",
      applyOutcome: "confirmed",
      animateResult: false,
    });
    expect(
      investigationTurnWithTerminalEvent(
        applyTurn,
        {
          type: "error",
          error: "The mutation result could not be established.",
          applyOutcome: "unknown",
        },
        true,
      ),
    ).toMatchObject({
      status: "error",
      error: "The mutation result could not be established.",
      applyOutcome: "unknown",
      animateResult: true,
    });
  });

  it("blocks actions until replay is complete and while a request or verification handoff is pending", () => {
    const ready = {
      streamReady: true,
      busy: false,
      requestPending: false,
      readOnly: false,
      verificationPending: false,
    };
    expect(investigationInteractionsBlocked(ready)).toBe(false);
    expect(
      investigationInteractionsBlocked({ ...ready, streamReady: false }),
    ).toBe(true);
    expect(
      investigationInteractionsBlocked({ ...ready, requestPending: true }),
    ).toBe(true);
    expect(
      investigationInteractionsBlocked({
        ...ready,
        verificationPending: true,
      }),
    ).toBe(true);
  });

  it("does not re-offer an assessment after any apply attempt", () => {
    const base = {
      currentAssessmentIdx: 0,
      lastRemediationIdx: 0,
      lastApplyAttemptIdx: -1,
      localApplyAttemptAssessmentIdx: -1,
      interactionsBlocked: false,
      hosted: false,
      hasNewerEvidence: false,
    };
    expect(canOfferInvestigationApply(base)).toBe(true);
    expect(
      canOfferInvestigationApply({ ...base, hasNewerEvidence: true }),
    ).toBe(false);
    expect(canOfferInvestigationApply({ ...base, hosted: true })).toBe(false);
    expect(
      canOfferInvestigationApply({ ...base, interactionsBlocked: true }),
    ).toBe(false);
    expect(
      canOfferInvestigationApply({
        ...base,
        localApplyAttemptAssessmentIdx: 0,
      }),
    ).toBe(false);
    expect(
      canOfferInvestigationApply({
        ...base,
        lastApplyAttemptIdx: 0,
      }),
    ).toBe(false);
    expect(
      canOfferInvestigationApply({
        ...base,
        currentAssessmentIdx: 2,
        lastRemediationIdx: 2,
        lastApplyAttemptIdx: 1,
        localApplyAttemptAssessmentIdx: 0,
      }),
    ).toBe(true);
  });

  it("clears a pessimistic local attempt only after a later verification assessment", () => {
    const attempt = {
      localApplyAttemptAssessmentIdx: 3,
      currentAssessmentIdx: 4,
      currentAssessmentIsVerification: false,
    };
    expect(investigationApplyAttemptVerified(attempt)).toBe(false);
    expect(
      investigationApplyAttemptVerified({
        ...attempt,
        currentAssessmentIsVerification: true,
      }),
    ).toBe(true);
    expect(
      investigationApplyAttemptVerified({
        ...attempt,
        currentAssessmentIdx: 3,
        currentAssessmentIsVerification: true,
      }),
    ).toBe(false);
  });

  it("marks an assessment pre-change until structured verification replaces a possible write", () => {
    const base = {
      currentAssessmentIdx: 2,
      lastApplyAttemptIdx: -1,
      lastApplyOutcome: undefined,
      localApplyAttemptAssessmentIdx: -1,
    };
    expect(investigationAssessmentNeedsCurrentStateVerification(base)).toBe(
      false,
    );
    expect(
      investigationAssessmentNeedsCurrentStateVerification({
        ...base,
        localApplyAttemptAssessmentIdx: 2,
      }),
    ).toBe(true);
    expect(
      investigationAssessmentNeedsCurrentStateVerification({
        ...base,
        lastApplyAttemptIdx: 3,
        lastApplyOutcome: "confirmed",
      }),
    ).toBe(true);
    expect(
      investigationAssessmentNeedsCurrentStateVerification({
        ...base,
        lastApplyAttemptIdx: 3,
        lastApplyOutcome: "unknown",
      }),
    ).toBe(true);
    expect(
      investigationAssessmentNeedsCurrentStateVerification({
        ...base,
        lastApplyAttemptIdx: 3,
        lastApplyOutcome: "failed",
        localApplyAttemptAssessmentIdx: 2,
      }),
    ).toBe(false);
    expect(
      investigationAssessmentNeedsCurrentStateVerification({
        ...base,
        currentAssessmentIdx: 4,
        lastApplyAttemptIdx: 3,
        lastApplyOutcome: "confirmed",
      }),
    ).toBe(false);
  });

  it("restores Apply only when the server definitively rejects the request", () => {
    expect(
      investigationApplyRejectionIsDefinitive(
        new DiagnoseError(409, "Run is no longer writable"),
      ),
    ).toBe(true);
    expect(
      investigationApplyRejectionIsDefinitive(
        new DiagnoseError(500, "Apply outcome is unknown"),
      ),
    ).toBe(false);
    expect(
      investigationApplyRejectionIsDefinitive(new TypeError("fetch failed")),
    ).toBe(false);
  });

  it("does not refresh live state or await verification for replayed apply completion", () => {
    expect(
      investigationApplyCompletionEffects({
        live: false,
        applyStartedLive: false,
        stale: false,
      }),
    ).toEqual({ refreshClusterState: false, verificationPending: false });
    expect(
      investigationApplyCompletionEffects({
        live: true,
        applyStartedLive: false,
        stale: false,
      }),
    ).toEqual({ refreshClusterState: true, verificationPending: true });
    expect(
      investigationApplyCompletionEffects({
        live: true,
        applyStartedLive: false,
        stale: true,
      }),
    ).toEqual({ refreshClusterState: true, verificationPending: false });
  });

  it("refreshes and preserves verification handoff when a live apply completes during replay", () => {
    expect(
      investigationApplyCompletionEffects({
        live: false,
        applyStartedLive: true,
        stale: false,
      }),
    ).toEqual({ refreshClusterState: true, verificationPending: true });
  });

  it("does not refresh current-cluster queries for a historical apply error replay", () => {
    expect(
      investigationApplyTerminalNeedsClusterRefresh({
        localApplyRequestPending: false,
        streamedApplyPending: true,
        streamedApplyStartedLive: false,
        terminalEventIsLive: false,
      }),
    ).toBe(false);
    expect(
      investigationApplyTerminalNeedsClusterRefresh({
        localApplyRequestPending: false,
        streamedApplyPending: true,
        streamedApplyStartedLive: false,
        terminalEventIsLive: true,
      }),
    ).toBe(true);
  });

  it("distinguishes context-switch closure from eviction", () => {
    expect(
      investigationClosedRunIsUnavailable({
        reason: "run_closed",
        subscribedRunStatus: "running",
      }),
    ).toBe(false);
    expect(
      investigationClosedRunIsUnavailable({
        reason: "run_closed",
        subscribedRunStatus: "stale",
      }),
    ).toBe(false);
    for (const subscribedRunStatus of ["done", "error", "stopped"] as const) {
      expect(
        investigationClosedRunIsUnavailable({
          reason: "run_closed",
          subscribedRunStatus,
        }),
      ).toBe(true);
    }
    expect(
      investigationClosedRunIsUnavailable({
        reason: "unavailable",
        subscribedRunStatus: "running",
      }),
    ).toBe(true);
  });

  it("does not refresh for replayed close but preserves live and local ambiguity", () => {
    const replayedCloseIsLive = investigationClosedEventIsLive({
      reason: "run_closed",
      subscribedRunStatus: "stale",
      // Retained stale streams send replay_complete before their closed sentinel.
      replayComplete: true,
    });
    const replayedClose = {
      localApplyRequestPending: false,
      streamedApplyPending: true,
      streamedApplyStartedLive: false,
      terminalEventIsLive: replayedCloseIsLive,
    };
    expect(replayedCloseIsLive).toBe(false);
    expect(investigationApplyTerminalNeedsClusterRefresh(replayedClose)).toBe(
      false,
    );
    expect(
      investigationClosedEventIsLive({
        reason: "run_closed",
        subscribedRunStatus: "running",
        replayComplete: true,
      }),
    ).toBe(true);
    expect(
      investigationApplyTerminalNeedsClusterRefresh({
        ...replayedClose,
        streamedApplyStartedLive: true,
      }),
    ).toBe(true);
    expect(
      investigationApplyTerminalNeedsClusterRefresh({
        ...replayedClose,
        streamedApplyPending: false,
        localApplyRequestPending: true,
      }),
    ).toBe(true);
  });
});

describe("investigationLiveCaseTurnIndex", () => {
  const linkedItem = {
    status: "linked" as const,
    ref: "ev_x",
    role: "context" as const,
    claim: "c",
  };
  const assessment = {
    status: "done" as const,
    diagnosis: { healthy: true, rootCause: "", report: "", remediation: [] },
  };
  it("keeps the assessment when no later turn cites evidence", () => {
    const answer = {
      status: "done" as const,
      question: "why?",
      diagnosis: { rootCause: "", report: "because", remediation: [] },
    };
    expect(investigationLiveCaseTurnIndex([assessment, answer], 0)).toBe(0);
  });
  it("moves to an answer turn that carries a bound case or linked root-cause refs", () => {
    const cited = {
      status: "done" as const,
      question: "chart it and cite it",
      diagnosis: {
        rootCause: "",
        report: "",
        remediation: [],
        evidence: [linkedItem],
      },
    };
    expect(investigationLiveCaseTurnIndex([assessment, cited], 0)).toBe(1);
    const legacy = {
      status: "done" as const,
      question: "what broke?",
      diagnosis: {
        rootCause: "x",
        report: "",
        remediation: [],
        rootCauseEvidence: { status: "linked" as const, refs: ["ev_x"] },
      },
    };
    expect(investigationLiveCaseTurnIndex([assessment, legacy], 0)).toBe(1);
    const unlinkedOnly = {
      ...cited,
      diagnosis: {
        ...cited.diagnosis,
        evidence: [{ status: "unlinked" as const }],
      },
    };
    expect(investigationLiveCaseTurnIndex([assessment, unlinkedOnly], 0)).toBe(
      0,
    );
  });
  it("ignores apply, explanation, running, and superseded turns", () => {
    const cited = {
      status: "done" as const,
      question: "cite",
      diagnosis: {
        rootCause: "",
        report: "",
        remediation: [],
        evidence: [linkedItem],
      },
    };
    expect(
      investigationLiveCaseTurnIndex(
        [
          assessment,
          { ...cited, apply: true },
          { ...cited, explainAssessment: 2 },
          { ...cited, status: "running" as const },
        ],
        0,
      ),
    ).toBe(0);
    // A newer assessment after the cited answer is the current one.
    expect(
      investigationLiveCaseTurnIndex([assessment, cited, assessment], 2),
    ).toBe(2);
  });
});
