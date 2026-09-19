import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import {
  AnalysisStory,
  resolveStoryPlacements,
  type StoryPlacementLoss,
  type StoryPlacementTarget,
} from "./AnalysisStory";
import type { DiagnosisEvidenceItem } from "../../api/diagnose";
import { metricsChangeCoverage } from "./investigationMetrics";
import { groupEvidenceCoverage } from "./investigationEvidencePresentation";
import type {
  InvestigationEvidenceProjection,
  InvestigationRootCauseEvidenceResolution,
} from "./investigationEvidence";
import type { DiagnosisResourceRef } from "./diagnoseEvidenceTypes";
import type { InvestigationSourceExcerpt } from "./investigationSourceFocus";
import type {
  InvestigationCaseItem,
  InvestigationCaseResolution,
} from "./investigationCase";
import {
  investigationDisclosureSettleDelay,
  prefersReducedMotion,
} from "./useDisclosureReveal";
import {
  EvidenceNavigationContext,
  type InvestigationTimelineScope,
} from "./investigationEvidence/navigation";
import {
  investigationCaseByGroup,
  investigationCaseItemKey,
  investigationEvidenceRevealCollection,
  partitionInvestigationEvidence,
} from "./investigationEvidencePartition";
import { EvidenceCard } from "./EvidenceCard";
import {
  AssessmentEvidenceQualification,
  CollapsedEvidenceCollection,
  CoverageStrip,
  EmptyCollection,
  RuledOutBlock,
} from "./InvestigationEvidenceSections";

export {
  INVESTIGATION_DISCLOSURE_SETTLE_MS,
  investigationDisclosureSettleDelay,
  investigationDisclosureScrollTop,
} from "./useDisclosureReveal";

export type { InvestigationTimelineScope } from "./investigationEvidence/navigation";

export function InvestigationEvidencePane({
  projection,
  rootCauseEvidence,
  investigationCase,
  story,
  collecting,
  animateGroupIds,
  onViewSource,
  onViewActivity,
  onOpenResource,
  onOpenTimeline,
  afterEvidence,
  recordNotes,
  storyShell = false,
  summarizedLimits = [],
  revealRequest,
  onRevealReady,
}: {
  projection: InvestigationEvidenceProjection;
  /** Server-validated links for the current root cause; absent without one. */
  rootCauseEvidence?: InvestigationRootCauseEvidenceResolution;
  /** The current assessment's agent case, resolved against this projection. */
  investigationCase?: InvestigationCaseResolution;
  /**
   * The assessment's story: the agent's prose with `[[radar:evidence=N]]`
   * placements resolved against `investigationCase` and the verdict's
   * `evidence` array. Absent on backends and runs without the story contract,
   * which keep the previous layout.
   */
  story?: {
    summary?: string;
    report: string;
    evidence?: DiagnosisEvidenceItem[];
  };
  /** Open the story fully instead of the bounded preview — when the layout has room for it. */
  collecting: boolean;
  animateGroupIds: ReadonlySet<string>;
  onViewSource: (
    sourceId: string,
    excerpt?: InvestigationSourceExcerpt,
  ) => void;
  /** Opens the Activity record when no exact result can be identified. */
  onViewActivity: () => void;
  /** Opens an evidence subject in Radar when the producer identified it exactly. */
  onOpenResource?: (ref: DiagnosisResourceRef) => void;
  /** Opens Radar's Timeline filtered to the resource a changes card is about. */
  onOpenTimeline?: (scope: InvestigationTimelineScope) => void;
  /** Next steps. Under a story they precede the inventory of captured results; otherwise they follow the evidence section. */
  afterEvidence?: ReactNode;
  /** The agent's notes that reached no card, shown inside Captured results. */
  recordNotes?: ReactNode;
  /** No verdict yet, but the story shape is coming: keep its cards in place. */
  storyShell?: boolean;
  /** Coverage lines already visible in the current assessment's Still open list. */
  summarizedLimits?: readonly string[];
  /** Explicit Activity → Findings navigation, including repeat clicks. */
  revealRequest?: { sourceId: string; requestId: number };
  onRevealReady?: (sourceId: string) => void;
}) {
  const [expandedGroupIds, setExpandedGroupIds] = useState<ReadonlySet<string>>(
    new Set(),
  );
  const onGroupOpenChange = useCallback((id: string, open: boolean) => {
    setExpandedGroupIds((previous) => {
      if (previous.has(id) === open) return previous;
      const next = new Set(previous);
      if (open) next.add(id);
      else next.delete(id);
      return next;
    });
  }, []);
  const [coverageOpen, setCoverageOpen] = useState(false);
  const [workloadOpen, setWorkloadOpen] = useState(false);
  const [earlierOpen, setEarlierOpen] = useState(false);
  // Null until the reader touches the fold: with nothing placed above it, a
  // finished story opens Captured results by itself, and it stays theirs to close.
  const [resultsOpen, setResultsOpen] = useState<boolean | null>(null);
  const handledRevealRequestRef = useRef<number | undefined>(undefined);
  const openingForRevealRequestRef = useRef<number | undefined>(undefined);
  const storyMode = story !== undefined || storyShell;
  const partition = partitionInvestigationEvidence(
    projection.groups,
    rootCauseEvidence,
    investigationCase,
    storyMode,
  );
  const hasCurrentEvidence =
    partition.main.length + partition.workload.length > 0;
  // A "Ruled out" link is an in-pane navigation to the placed observation. It
  // reuses the Activity → Findings reveal path (open the card, open history
  // when the observation is a superseded revision) and yields to a newer
  // request from the parent.
  const [caseReveal, setCaseReveal] = useState<
    { sourceId: string; requestId: number } | undefined
  >(undefined);
  // A ruled-out reveal belongs to the case that produced it; a new parent
  // request or a new assessment's case supersedes it.
  useEffect(() => {
    setCaseReveal(undefined);
  }, [revealRequest?.requestId, investigationCase]);
  const collectionByGroup = partition.collectionByGroup;
  // A placed item can still sit on a withheld broader card; a link to it
  // would have nowhere to go.
  const visibleRuledOut = (investigationCase?.ruledOut ?? []).filter(
    (entry) => entry.item.groupId && collectionByGroup.has(entry.item.groupId),
  );
  const revealCaseItem = useCallback(
    (item: InvestigationCaseItem) => {
      const { groupId, observation } = item;
      if (!groupId || !observation) return;
      // A placed group is normally in main, but a historical one lives in the
      // nested "Previous observations" collection; open the way to it first.
      const collection = collectionByGroup.get(groupId);
      let settle = 0;
      if (storyMode && !resultsOpen) {
        setResultsOpen(true);
        settle = investigationDisclosureSettleDelay(prefersReducedMotion());
      }
      if (collection === "workload" || collection === "earlier") {
        setWorkloadOpen(true);
        settle = investigationDisclosureSettleDelay(prefersReducedMotion());
      }
      if (collection === "earlier") setEarlierOpen(true);
      onGroupOpenChange(groupId, true);
      setCaseReveal({ sourceId: observation.source.id, requestId: Date.now() });
      window.setTimeout(() => {
        window.requestAnimationFrame(() => {
          const element = document.getElementById(groupId);
          element?.scrollIntoView({
            block: "start",
            behavior: prefersReducedMotion() ? "auto" : "smooth",
          });
          element?.focus({ preventScroll: true });
        });
      }, settle);
    },
    [onGroupOpenChange, collectionByGroup, storyMode, resultsOpen],
  );
  const revealCollection = revealRequest
    ? investigationEvidenceRevealCollection(
        projection,
        revealRequest.sourceId,
        partition,
      )
    : undefined;

  // A source link is a navigation request, not a disclosure preference. Open
  // whichever collection owns the source first, then tell the workspace that
  // its double-rAF focus/scroll can safely run outside an inert subtree.
  useLayoutEffect(() => {
    if (
      !revealRequest ||
      handledRevealRequestRef.current === revealRequest.requestId
    ) {
      return;
    }
    const { sourceId, requestId } = revealRequest;
    if (
      (revealCollection === "workload" || revealCollection === "earlier") &&
      !workloadOpen
    ) {
      openingForRevealRequestRef.current = requestId;
      setWorkloadOpen(true);
      return;
    }
    if (revealCollection === "earlier" && !earlierOpen) {
      openingForRevealRequestRef.current = requestId;
      setEarlierOpen(true);
      return;
    }
    if (storyMode && revealCollection && !resultsOpen) {
      openingForRevealRequestRef.current = requestId;
      setResultsOpen(true);
      return;
    }
    if (revealCollection === "coverage" && !coverageOpen) {
      openingForRevealRequestRef.current = requestId;
      setCoverageOpen(true);
      return;
    }
    const finishReveal = () => {
      if (handledRevealRequestRef.current === requestId) return;
      handledRevealRequestRef.current = requestId;
      openingForRevealRequestRef.current = undefined;
      onRevealReady?.(sourceId);
    };
    if (openingForRevealRequestRef.current !== requestId) {
      finishReveal();
      return;
    }

    // Two animation frames alone can target an item that is still moving. Wait
    // through the shared Collapse transition only when motion is enabled.
    const settleDelay = investigationDisclosureSettleDelay(
      prefersReducedMotion(),
    );
    if (settleDelay === 0) {
      finishReveal();
      return;
    }
    const timer = window.setTimeout(finishReveal, settleDelay);
    return () => window.clearTimeout(timer);
  }, [
    revealRequest,
    revealCollection,
    onRevealReady,
    earlierOpen,
    coverageOpen,
    workloadOpen,
    storyMode,
    resultsOpen,
  ]);

  const coverageGroups = groupEvidenceCoverage(projection.limitations);
  // A caveat belongs on the claim it bounds: the "does not cover" line shows
  // under the cause card. A verdict with no cause card (healthy) keeps it on
  // every placed card, since that is where its scope lives.
  const storyGapRoles = useMemo(
    () =>
      investigationCase?.items.some(
        (item) => item.role === "cause" && item.placement === "card",
      )
        ? new Set(["cause"])
        : undefined,
    [investigationCase],
  );
  const limitationSummary = coverageGroups
    .map((group) => `${group.label}: ${group.summary}`)
    .join(" · ");
  const additionalLimitations = coverageGroups
    .map((group) => `${group.label}: ${group.summary}`)
    .filter(
      (line) => !story?.summary?.trim() || !summarizedLimits.includes(line),
    )
    .join(" · ");
  // Resolves a story placement to the agent item it names, or says why it
  // cannot: an index Radar does not know, an item the server could not bind,
  // or a bound item that matches no single observation. Only the last two
  // distinguish "the agent was wrong" from "Radar could not tell".
  const resolveStoryItem = useCallback(
    (index: number): StoryPlacementTarget | StoryPlacementLoss => {
      const evidence = story?.evidence ?? [];
      if (!Number.isInteger(index) || index < 0 || index >= evidence.length)
        return "invalid";
      if (evidence[index]?.status === "unlinked") return "unlinked";
      const item = investigationCase?.items.find(
        (candidate) => candidate.index === index,
      );
      if (!item) return "unplaced";
      if (item.groupId && item.observation)
        return { item, domId: `story-${item.groupId}` };
      // A subject-less citation of a call that fans out into several cards
      // (the diagnose bundle above all) names the call, not one of its
      // results. Placing the call's primary card is not guessing which claim
      // the agent meant: it is rendering the headline result of what it
      // cited, the same card Activity's "Show evidence" opens for that call.
      if (item.subject === undefined && item.source.primaryGroupId) {
        const group = projection.groups.find(
          (candidate) => candidate.id === item.source.primaryGroupId,
        );
        // The card is the one the cited call itself produced. A later read of
        // the same thing lives on the same group; substituting it would show
        // newer evidence under an older claim.
        const observation = group?.observations.find(
          (candidate) => candidate.source.id === item.source.id,
        );
        if (group && observation)
          return {
            item: {
              ...item,
              groupId: group.id,
              observation,
              placement: observation === group.latest ? "card" : "revision",
            },
            domId: `story-${group.id}`,
          };
      }
      // A bound call that produced no card at all (a search, a metrics
      // table) is not ambiguous, it is unrendered; its raw result is in
      // Activity and the note at the claim says so.
      const hasCard = projection.groups.some((group) =>
        group.observations.some(
          (observation) => observation.source.id === item.source.id,
        ),
      );
      return hasCard
        ? "unplaced"
        : { kind: "nocard", sourceId: item.source.id };
    },
    [story?.evidence, investigationCase, projection.groups],
  );
  const groupsById = new Map(
    projection.groups.map((group) => [group.id, group] as const),
  );
  const renderStoryPlacement = useCallback(
    (target: StoryPlacementTarget, { compact }: { compact: boolean }) => {
      const group = target.item.groupId
        ? groupsById.get(target.item.groupId)
        : undefined;
      if (!group || !target.item.observation) return null;
      return (
        <EvidenceCard
          group={group}
          observation={target.item.observation}
          domId={target.domId}
          animateArrival={false}
          onViewSource={onViewSource}
          spanFullRow
          compact={compact}
          noteMode="chip"
          gapRoles={storyGapRoles}
        />
      );
    },
    // groupsById is rebuilt per render from projection.groups.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [projection.groups, onViewSource, storyGapRoles],
  );
  // A card behind the story's fold stays mounted but inert once the fold has
  // been opened and closed; a reveal has to open the fold first, then land.
  const [storyOpenRequest, setStoryOpenRequest] = useState(0);
  // What the story actually placed, not what the case could have: a cited
  // card the agent never placed is inventory, and the inventory says so.
  // A marker written in the ledger's ref form names the item carrying that ref.
  const storyEvidence = story?.evidence;
  const resolveStoryRef = useCallback(
    (ref: string): number | undefined => {
      const matches = (storyEvidence ?? [])
        .map((item, index) => (item.ref === ref ? index : -1))
        .filter((index) => index >= 0);
      // Two items on one ref name no single card; the renderer flags it.
      return matches.length === 1 ? matches[0] : undefined;
    },
    [storyEvidence],
  );
  const storyReport = story?.report;
  const storyPlacements = useMemo(
    () =>
      storyReport === undefined
        ? undefined
        : resolveStoryPlacements(
            storyReport,
            resolveStoryItem,
            resolveStoryRef,
          ),
    [storyReport, resolveStoryItem, resolveStoryRef],
  );
  const placedCount = storyPlacements?.placedCount ?? 0;
  // A card behind the fold may be mounted and inert, or not mounted at all
  // before the fold first opens; either way the fold opens first and the
  // card is found once it has settled.
  const revealStoryTarget = useCallback(
    (target: StoryPlacementTarget) => {
      const land = (placed: HTMLElement) => {
        placed.scrollIntoView({
          block: "center",
          behavior: prefersReducedMotion() ? "auto" : "smooth",
        });
        placed.focus({ preventScroll: true });
      };
      const placed = document.getElementById(target.domId);
      // A twin's chip shares its sibling's card, so the card's position, not
      // the item's own kind, says whether the story holds it.
      const placedInStory =
        storyPlacements?.placedAt.has(target.item.index) ?? false;
      if (placed && !placed.closest("[inert]")) {
        land(placed);
        return;
      }
      if (placed || placedInStory) {
        setStoryOpenRequest((n) => n + 1);
        window.setTimeout(() => {
          const settled = document.getElementById(target.domId);
          if (settled) land(settled);
          else revealCaseItem(target.item);
        }, investigationDisclosureSettleDelay(prefersReducedMotion()));
        return;
      }
      revealCaseItem(target.item);
    },
    [revealCaseItem, storyPlacements],
  );
  const resultsShown =
    resultsOpen ?? (placedCount === 0 && !collecting && !!story);
  const placedGroupIds = new Set(
    [...(storyPlacements?.byIndex.values() ?? [])].flatMap((entry) =>
      entry.kind === "placed" && entry.target.item.groupId
        ? [entry.target.item.groupId]
        : [],
    ),
  );
  const capturedTotal =
    partition.main.length +
    partition.workload.length +
    partition.earlier.length;
  const resultsSummary = [
    partition.hiddenBroader > 0
      ? `${partition.hiddenBroader} about other resources not shown`
      : undefined,
    partition.hiddenMetrics > 0
      ? `${partition.hiddenMetrics} broader metric ${partition.hiddenMetrics === 1 ? "result" : "results"} not shown`
      : undefined,
    projection.limitations.length > 0
      ? `${projection.limitations.length} ${projection.limitations.length === 1 ? "check" : "checks"} limited`
      : undefined,
  ]
    .filter((part): part is string => Boolean(part))
    .join(" · ");
  const mainCards = (
    <>
      <div className="grid items-start gap-2.5">
        {partition.main.map((group) => (
          <EvidenceCard
            key={group.id}
            group={group}
            spanFullRow
            animateArrival={animateGroupIds.has(group.id)}
            onViewSource={onViewSource}
            placedInStory={placedGroupIds.has(group.id)}
          />
        ))}
      </div>

      {partition.hiddenBroader > 0 ? (
        <p
          className="text-xs text-theme-text-tertiary"
          data-testid="investigation-hidden-evidence"
        >
          {partition.hiddenBroader === 1
            ? "1 result about another resource is not shown; it appears here when the assessment cites it."
            : `${partition.hiddenBroader} results about other resources are not shown; they appear here when the assessment cites them.`}
        </p>
      ) : null}

      {partition.hiddenMetrics > 0 ? (
        <p
          className="text-xs text-theme-text-tertiary"
          data-testid="investigation-hidden-metrics"
        >
          {partition.hiddenMetrics === 1
            ? "1 broader metric result is not shown; it appears here when the assessment cites it."
            : `${partition.hiddenMetrics} broader metric results are not shown; they appear here when the assessment cites them.`}
        </p>
      ) : null}

      {!hasCurrentEvidence ? (
        <EmptyCollection
          collecting={collecting}
          hasEarlierEvidence={partition.earlier.length > 0}
          onViewActivity={onViewActivity}
        />
      ) : null}

      <CollapsedEvidenceCollection
        id="investigation-workload-evidence"
        title="More evidence about this resource"
        description=""
        groups={partition.workload}
        totalCount={partition.workload.length + partition.earlier.length}
        animateGroupIds={animateGroupIds}
        onViewSource={onViewSource}
        open={workloadOpen}
        onOpenChange={setWorkloadOpen}
      >
        <CollapsedEvidenceCollection
          id="investigation-earlier-evidence"
          title="Previous observations"
          description="Earlier does not mean resolved."
          groups={partition.earlier}
          animateGroupIds={animateGroupIds}
          onViewSource={onViewSource}
          open={earlierOpen}
          onOpenChange={setEarlierOpen}
        />
      </CollapsedEvidenceCollection>
    </>
  );
  const content = (
    <section
      aria-labelledby="investigation-radar-evidence"
      className={
        // In the story shape this is no card: the analysis, the steps and the
        // record are siblings. The element stays only as the container the
        // cards' queries measure.
        storyMode
          ? "@container/evidence space-y-4"
          : "investigation-evidence @container/evidence space-y-3 rounded-xl border p-3"
      }
    >
      <span className="sr-only" role="status" aria-live="polite">
        {projection.limitations.length > 0
          ? `Evidence coverage update: ${limitationSummary}`
          : ""}
      </span>
      {!storyMode ? (
        <div className="flex min-w-0 items-start justify-between gap-3">
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <h2
                id="investigation-radar-evidence"
                className="text-lg font-semibold text-theme-text-primary"
              >
                Evidence
              </h2>
              {collecting ? (
                <span className="inline-flex items-center gap-1.5 text-xs text-accent-text">
                  <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-accent" />
                  collecting
                </span>
              ) : null}
            </div>
          </div>
        </div>
      ) : (
        <h2 id="investigation-radar-evidence" className="sr-only">
          Analysis and evidence
        </h2>
      )}

      {projection.limitations.length > 0 && !storyMode ? (
        <CoverageStrip
          groups={coverageGroups}
          visibleGroupIds={new Set(partition.collectionByGroup.keys())}
          summary={limitationSummary}
          onViewSource={onViewSource}
          open={coverageOpen}
          onOpenChange={setCoverageOpen}
        />
      ) : null}

      <div className="space-y-4">
        {rootCauseEvidence && rootCauseEvidence.status !== "linked" ? (
          <AssessmentEvidenceQualification
            resolution={rootCauseEvidence}
            onViewActivity={onViewActivity}
          />
        ) : null}

        {storyMode ? (
          <div
            data-story-card
            data-story-shell={story ? undefined : ""}
            className="investigation-evidence rounded-xl border p-4"
          >
            <div className="mb-2 flex items-center gap-2 text-xs font-semibold text-theme-text-secondary">
              Why
              <span className="font-normal text-theme-text-tertiary">
                · Agent analysis
              </span>
              {collecting ? (
                <span className="ml-auto inline-flex items-center gap-1.5 text-xs font-normal text-accent-text">
                  <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-accent" />
                  collecting
                </span>
              ) : null}
            </div>
            {story && storyPlacements ? (
              <AnalysisStory
                placements={storyPlacements}
                trailing={
                  visibleRuledOut.length > 0 ? (
                    <RuledOutBlock
                      entries={visibleRuledOut}
                      onReveal={revealCaseItem}
                    />
                  ) : undefined
                }
                renderPlacement={renderStoryPlacement}
                onReveal={revealStoryTarget}
                openRequest={storyOpenRequest}
                onViewSource={(sourceId) => onViewSource(sourceId)}
              />
            ) : (
              <p className="text-sm text-theme-text-tertiary">
                The analysis appears when the investigation finishes.
              </p>
            )}
          </div>
        ) : null}

        {storyMode ? afterEvidence : null}

        {!storyMode && visibleRuledOut.length > 0 ? (
          <RuledOutBlock entries={visibleRuledOut} onReveal={revealCaseItem} />
        ) : null}

        {storyMode ? (
          <CollapsedEvidenceCollection
            id="investigation-captured-results"
            title="Captured results"
            description={[
              placedCount > 0
                ? `${placedCount} shown above`
                : collecting
                  ? `${capturedTotal} collected so far`
                  : "None shown above",
              resultsSummary,
            ]
              .filter(Boolean)
              .join(" · ")}
            groups={[]}
            totalCount={capturedTotal}
            keepWhenEmpty
            animateGroupIds={animateGroupIds}
            onViewSource={onViewSource}
            open={resultsShown}
            onOpenChange={setResultsOpen}
          >
            <div className="space-y-4">
              {projection.limitations.length > 0 ? (
                <CoverageStrip
                  groups={coverageGroups}
                  visibleGroupIds={new Set(partition.collectionByGroup.keys())}
                  summary={additionalLimitations}
                  onViewSource={onViewSource}
                  open={coverageOpen}
                  onOpenChange={setCoverageOpen}
                />
              ) : null}
              {mainCards}
              {recordNotes ? (
                <div data-record-notes>
                  <div className="mb-1 text-xs font-semibold text-theme-text-secondary">
                    Notes without a card
                  </div>
                  {recordNotes}
                </div>
              ) : null}
            </div>
          </CollapsedEvidenceCollection>
        ) : (
          mainCards
        )}
      </div>
    </section>
  );

  return (
    <EvidenceNavigationContext.Provider
      value={{
        onOpenResource,
        onOpenTimeline,
        expandedGroupIds,
        onGroupOpenChange,
        revealSourceId: caseReveal?.sourceId ?? revealRequest?.sourceId,
        revealRequestId: caseReveal?.requestId ?? revealRequest?.requestId,
        caseByGroup: investigationCaseByGroup(investigationCase),
        excludedByItem: new Map(
          (investigationCase?.ruledOut ?? []).map((entry) => [
            investigationCaseItemKey(entry.item),
            entry.hypothesis,
          ]),
        ),
        metricsMarkersBySource: new Map(
          projection.groups.flatMap((group) =>
            group.observations.flatMap((observation) =>
              observation.data.type === "metrics"
                ? [
                    [
                      observation.source.id,
                      metricsChangeCoverage(projection.groups, observation),
                    ] as const,
                  ]
                : [],
            ),
          ),
        ),
        citedOrderByGroup: new Map(
          rootCauseEvidence?.links.flatMap((link) =>
            link.originalGroupId
              ? [[link.originalGroupId, link.source.order] as const]
              : [],
          ) ?? [],
        ),
      }}
    >
      {content}
      {storyMode ? null : afterEvidence}
    </EvidenceNavigationContext.Provider>
  );
}
