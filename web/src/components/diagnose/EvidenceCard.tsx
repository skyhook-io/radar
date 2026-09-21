import { StoryExcerpt } from "./StoryExcerpt";
import { storyExcerpt } from "./investigationEvidence/excerpt";
import {
  type InvestigationCaseItem,
  investigationCaseObservationKey,
} from "./investigationCase";
import {
  useCallback,
  useContext,
  useLayoutEffect,
  useState,
  useId,
} from "react";
import { clsx } from "clsx";
import { Collapse, CollapseChevron } from "@skyhook-io/k8s-ui";
import { parseLogLine } from "../../utils/log-format";
import { evidenceDisplaySnapshot } from "./investigationEvidencePresentation";
import {
  investigationEvidenceSubjectRef,
  investigationEvidenceSourceDomId,
  type InvestigationEvidenceGroup,
  type InvestigationEvidenceObservation,
} from "./investigationEvidence";
import type { InvestigationSourceExcerpt } from "./investigationSourceFocus";
import { evidenceSourceExcerpt } from "./investigationSourceFocus";
import { Tooltip } from "../ui/Tooltip";
import { AgentClaimNote, AgentRoleChip } from "./AgentCase";
import { useDisclosureReveal } from "./useDisclosureReveal";
import { EvidenceNavigationContext } from "./investigationEvidence/navigation";
import {
  EvidenceCaveat,
  EvidenceIcon,
  OpenResourceButton,
  OpenTimelineButton,
  SourceButton,
  toneBorder,
  uniquePrimarySources,
  phaseLabel,
} from "./investigationEvidence/cardParts";
import {
  listingScopeNamespace,
  namedInventoryRows,
} from "./investigationEvidence/bodies/inventory";
import {
  EvidenceBody,
  evidenceHasDetails,
} from "./investigationEvidence/bodies/index";
import {
  evidenceTypePrefersFullRow,
  investigationCaseItemKey,
  investigationEvidenceShouldRevealHistory,
  previousDifferentObservations,
} from "./investigationEvidencePartition";

export function EvidenceCard({
  group,
  observation: observationOverride,
  domId = group.id,
  animateArrival,
  onViewSource,
  spanFullRow = false,
  prominence = "primary",
  compact = false,
  placedInStory = false,
  gapRoles,
  noteMode = "full",
}: {
  group: InvestigationEvidenceGroup;
  /**
   * Render this exact observation instead of the group's current one — a
   * story places the read the agent cited, which may since have been
   * superseded, and says so rather than swapping in today's.
   */
  observation?: InvestigationEvidenceObservation;
  /** Stable layout/scroll identity when an existing card changes section. */
  domId?: string;
  animateArrival: boolean;
  onViewSource: (
    sourceId: string,
    excerpt?: InvestigationSourceExcerpt,
  ) => void;
  /** Fill both columns when this card has no compact row partner. */
  spanFullRow?: boolean;
  prominence?: "primary" | "supporting" | "secondary";
  /**
   * Header and summary only, so the collapsed story preview never slices a
   * card. A story log card keeps its cited lines: they are the card.
   */
  compact?: boolean;
  /** This card also appears inside the story above. */
  placedInStory?: boolean;
  /** Roles whose "does not cover" line is shown; undefined shows every gap. */
  gapRoles?: ReadonlySet<string>;
  /**
   * Inside the story the prose above the card is the agent's reading of it,
   * so the note row keeps only the role chip (and an excluded hypothesis);
   * repeating the sentence under the card doubled the text for nothing.
   */
  noteMode?: "full" | "chip";
}) {
  const {
    onOpenResource,
    onOpenTimeline,
    revealSourceId,
    revealRequestId,
    citedOrderByGroup,
    expandedGroupIds,
    onGroupOpenChange,
    metricsMarkersBySource,
    caseByGroup,
    excludedByItem,
  } = useContext(EvidenceNavigationContext);
  const open = expandedGroupIds?.has(group.id) ?? false;
  const setOpen = useCallback(
    (value: boolean) => onGroupOpenChange?.(group.id, value),
    [onGroupOpenChange, group.id],
  );
  const { elementRef, revealAfterToggle } = useDisclosureReveal<HTMLElement>();
  const observation = observationOverride ?? group.latest;
  const supersededRead =
    observationOverride !== undefined && observationOverride !== group.latest;
  const displaySummary =
    observation.data.type === "crash" && observation.summary
      ? parseLogLine(observation.summary).content
      : observation.summary;
  const citedOrder = citedOrderByGroup?.get(group.id);
  const caseItems = caseByGroup?.get(group.id) ?? [];
  // Annotations belong to the observation on screen: a card rendering an
  // earlier read shows the note the agent attached to that read, not the
  // latest one's.
  const cardItems = caseItems.filter((item) =>
    observationOverride
      ? item.observation !== undefined &&
        item.observation.source.id === observation.source.id &&
        item.observation.revision === observation.revision
      : item.placement === "card",
  );
  const revisionItems = caseItems.filter(
    (item) => item.placement === "revision",
  );
  const agentCause = cardItems.some((item) => item.role === "cause");
  // A card placed in the story: the prose carries the claim, so the card
  // shows its role beside the title, its scope line if any, and for a log
  // stream the decisive lines themselves; the rest waits behind the chevron.
  const storyCard = noteMode === "chip";
  const storyGaps = storyCard
    ? cardItems
        .map((item) =>
          item.gap && (!gapRoles || gapRoles.has(item.role)) ? item.gap : "",
        )
        .filter(Boolean)
    : [];
  // A compact card keeps its single row; the agent's clause on what the
  // result does not cover rides on it after the status, cut to the row, whole
  // on hover. The clause is written to stand alone, so it carries no label.
  const gapOnRow = compact && storyGaps.length > 0;
  // What a rules_out card excludes is the point of placing it; it stays on
  // the story card as its one line.
  const storyExcludes = storyCard
    ? cardItems
        .filter((item) => item.role === "rules_out")
        .map((item) => excludedByItem?.get(investigationCaseItemKey(item)))
        .filter((entry): entry is string => Boolean(entry))
    : [];
  const previousObservations = previousDifferentObservations(
    group,
    citedOrder,
    new Set(revisionItems.map((item) => item.observation!.source.order)),
  );
  const meaningfulHistory = previousObservations.length > 0;
  const citedObservation = group.observations.find(
    (item) => item.source.order === citedOrder,
  );
  const differsFromAssessment =
    citedObservation &&
    evidenceDisplaySnapshot(citedObservation) !==
      evidenceDisplaySnapshot(observation);
  const bodyId = `${domId}-body`;
  const hasEvidenceDetails = evidenceHasDetails(
    observation.data,
    observation.summary,
  );
  const excerpt = storyCard
    ? storyExcerpt(observation, cardItems, {
        compact,
        scopeNamespace: listingScopeNamespace(observation.source),
      })
    : undefined;
  const citedRows =
    observation.data.type === "inventory"
      ? namedInventoryRows(
          observation.data.resources,
          cardItems,
          listingScopeNamespace(observation.source),
        ).flatMap((named) => (named.row ? [named.row] : []))
      : undefined;
  // A card whose content shows inline has nothing left behind the fold but
  // the earlier lines or the history.
  const inlineLogs = excerpt?.kind === "lines";
  const inlineMetrics = excerpt?.kind === "chart";
  const storyLogRest = excerpt?.kind === "lines" ? excerpt.rest : [];
  const hasExpandableBody =
    (hasEvidenceDetails && !inlineLogs && !inlineMetrics) || meaningfulHistory;
  const canExpand = !compact && (hasExpandableBody || storyLogRest.length > 0);
  const revealHistory = investigationEvidenceShouldRevealHistory(
    group,
    revealSourceId,
    new Set(revisionItems.map((item) => item.observation!.source.id)),
  );
  useLayoutEffect(() => {
    const destination = revealSourceId
      ? document.getElementById(
          investigationEvidenceSourceDomId(revealSourceId),
        )
      : null;
    if (
      revealHistory ||
      (destination && elementRef.current?.contains(destination))
    )
      setOpen(true);
  }, [revealHistory, revealSourceId, revealRequestId, elementRef, setOpen]);
  const wide = spanFullRow || evidenceTypePrefersFullRow(observation.data.type);
  const resourceRef = investigationEvidenceSubjectRef(observation.data);
  const resourceIdentity = resourceRef
    ? `${resourceRef.namespace ? `${resourceRef.namespace}/` : ""}${resourceRef.name}`
    : undefined;
  const primarySources = uniquePrimarySources(group);
  const inlineSecretKeys =
    observation.data.type === "resource" &&
    observation.data.resource.kind.toLowerCase() === "secret" &&
    !canExpand;
  const headerContent = (
    <>
      <EvidenceIcon observation={observation} prominence={prominence} />
      <span className="min-w-0 flex-1">
        <span className="flex flex-wrap items-center gap-1.5">
          <span className="text-sm font-semibold leading-snug text-theme-text-primary">
            {observation.title}
          </span>
          {storyCard
            ? [...new Set(cardItems.map((item) => item.role))].map((role) =>
                role ? <AgentRoleChip key={role} role={role} /> : null,
              )
            : null}
        </span>
        {observation.relevance === "broader" &&
        resourceRef &&
        resourceIdentity &&
        !observation.title.includes(resourceIdentity) ? (
          <span className="mt-0.5 block text-xs text-theme-text-secondary">
            {resourceIdentity} · {resourceRef.kind}
          </span>
        ) : null}
        {gapOnRow ? (
          <Tooltip
            content={storyGaps.join(" · ")}
            wrapperClassName="mt-0.5 w-full min-w-0"
          >
            <span className="min-w-0 flex-1 truncate text-xs leading-relaxed text-theme-text-secondary">
              {displaySummary}
              {displaySummary ? " · " : null}
              <span data-agent-gap-hint className="text-theme-text-tertiary">
                {storyGaps.join(" · ")}
              </span>
            </span>
          </Tooltip>
        ) : displaySummary ? (
          <span
            className={clsx(
              "mt-0.5 block text-xs leading-relaxed text-theme-text-secondary",
              !open && !inlineSecretKeys && "line-clamp-2",
              "[overflow-wrap:anywhere]",
            )}
          >
            {displaySummary}
          </span>
        ) : null}
        {supersededRead ? (
          <span className="mt-0.5 block text-xs text-theme-text-tertiary">
            Captured in turn {observation.source.turnIndex + 1} · a newer read
            of this exists in Captured results
          </span>
        ) : placedInStory ? (
          <span className="mt-0.5 block text-xs text-theme-text-tertiary">
            In the analysis
          </span>
        ) : group.historical ? (
          <span className="mt-0.5 block text-xs text-theme-text-tertiary">
            Previous observation · not confirmed by the latest check
          </span>
        ) : differsFromAssessment &&
          citedOrder != null &&
          observation.source.order !== citedOrder ? (
          <span className="mt-0.5 block text-xs text-theme-text-tertiary">
            {observation.source.order > citedOrder
              ? "Observed after the assessment’s source"
              : "Earlier observation retained from a more direct source"}
          </span>
        ) : null}
      </span>
      {canExpand ? (
        <CollapseChevron open={open} className="h-4 w-4 self-center" />
      ) : null}
    </>
  );
  return (
    <article
      data-evidence-source={observation.source.id}
      ref={elementRef}
      id={domId}
      data-evidence-card
      tabIndex={-1}
      aria-label={`${observation.title} evidence`}
      className={clsx(
        "@container/card scroll-mt-14 overflow-hidden outline-none focus:ring-2 focus:ring-accent/50 data-[source-related]:ring-2 data-[source-related]:ring-accent/35",
        "rounded-lg border",
        // The card the agent calls the cause sorts first and is often the
        // calmest thing on screen: a cause is frequently a Secret or a
        // ConfigMap that Radar has no reason to colour, while every card
        // echoing the failure below it carries red. Lifting the surface marks
        // it without borrowing a severity hue, and without touching the left
        // rule (Radar's severity) or the ring (the source you are viewing).
        agentCause ? "bg-theme-elevated shadow-theme-sm" : "bg-theme-surface",
        toneBorder(observation.tone, observation.tier, prominence),
        wide && "@min-[760px]/evidence:col-span-2",
        animateArrival && "animate-transcript-enter",
      )}
    >
      {primarySources.map((source) => (
        <span
          key={source.id}
          id={investigationEvidenceSourceDomId(source.id)}
          className="block scroll-mt-14"
          aria-hidden
        />
      ))}
      <div className="flex min-w-0 items-stretch">
        {canExpand ? (
          <button
            type="button"
            aria-expanded={open}
            aria-controls={bodyId}
            onClick={() => {
              const opening = !open;
              setOpen(opening);
              revealAfterToggle(opening);
            }}
            className={clsx(
              "flex min-w-0 flex-1 items-start text-left hover:bg-theme-hover/60",
              prominence === "primary"
                ? "gap-2.5 px-3 py-2.5"
                : "gap-2 px-2.5 py-2",
            )}
          >
            {headerContent}
          </button>
        ) : (
          <div
            className={clsx(
              "flex min-w-0 flex-1 items-start text-left",
              prominence === "primary"
                ? "gap-2.5 px-3 py-2.5"
                : "gap-2 px-2.5 py-2",
            )}
          >
            {headerContent}
          </div>
        )}
        <div className="flex shrink-0 items-center gap-0.5 px-2">
          {observation.data.type === "changes" ? (
            <EvidenceCaveat data={observation.data} />
          ) : null}
          <SourceButton
            ariaLabel={`View result for ${observation.title}`}
            compact
            onClick={() =>
              onViewSource(
                observation.source.id,
                evidenceSourceExcerpt(observation.data),
              )
            }
          />
          <OpenResourceButton
            resourceRef={resourceRef}
            onOpenResource={onOpenResource}
            compact
          />
          {observation.data.type === "changes" && observation.data.subject ? (
            <OpenTimelineButton
              scope={observation.data.subject}
              onOpenTimeline={onOpenTimeline}
            />
          ) : null}
        </div>
      </div>
      {storyCard ? (
        <>
          {(storyGaps.length > 0 || storyExcludes.length > 0) && !compact ? (
            <div
              className={clsx(
                "space-y-0.5 text-[11px] text-theme-text-tertiary",
                prominence === "primary" ? "px-3 pb-2.5" : "px-2.5 pb-2",
              )}
            >
              {storyExcludes.map((hypothesis) => (
                <p key={hypothesis} data-agent-excludes>
                  Rules out: {hypothesis}
                </p>
              ))}
              {storyGaps.map((gap) => (
                <p key={gap} data-agent-gap>
                  {gap}
                </p>
              ))}
            </div>
          ) : null}
          {excerpt ? (
            <StoryExcerpt
              excerpt={excerpt}
              data={observation.data}
              prominence={prominence}
              open={open}
              canExpand={canExpand}
              changeCoverage={metricsMarkersBySource?.get(
                observation.source.id,
              )}
            />
          ) : null}
        </>
      ) : cardItems.some((item) => item.claim || item.role) ? (
        <div
          className={clsx(
            "space-y-1.5",
            prominence === "primary" ? "px-3 pb-2.5" : "px-2.5 pb-2",
          )}
        >
          {cardItems.map((item) => (
            <AgentClaimNote
              key={item.index}
              claim={compact ? "" : item.claim}
              role={item.role}
              gap={
                compact || (gapRoles && !gapRoles.has(item.role))
                  ? undefined
                  : item.gap
              }
              excludes={excludedByItem?.get(investigationCaseItemKey(item))}
              className="pt-1.5"
            />
          ))}
        </div>
      ) : null}
      {canExpand && hasExpandableBody ? (
        <div id={bodyId}>
          <Collapse open={open}>
            <div
              className={clsx(
                "space-y-3 border-t border-theme-border/60",
                prominence === "primary" ? "px-3 py-3" : "px-2.5 py-2.5",
              )}
            >
              {hasEvidenceDetails && !inlineLogs && !inlineMetrics ? (
                <EvidenceBody
                  data={observation.data}
                  condensed={storyCard}
                  cardSummary={observation.summary}
                  changeCoverage={metricsMarkersBySource?.get(
                    observation.source.id,
                  )}
                  citedRows={citedRows}
                />
              ) : null}
              {meaningfulHistory ? (
                <RevisionHistory
                  observations={previousObservations}
                  citedOrder={citedOrder}
                  caseItems={revisionItems}
                  reveal={revealHistory}
                  revealRequestId={revealRequestId}
                  onViewSource={onViewSource}
                />
              ) : null}
              {observation.data.type !== "changes" ? (
                <EvidenceCaveat data={observation.data} />
              ) : null}
            </div>
          </Collapse>
        </div>
      ) : null}
    </article>
  );
}

function RevisionHistory({
  observations,
  citedOrder,
  caseItems = [],
  reveal,
  revealRequestId,
  onViewSource,
}: {
  observations: InvestigationEvidenceObservation[];
  citedOrder?: number;
  /** Agent items bound to a superseded observation of this group. */
  caseItems?: InvestigationCaseItem[];
  reveal: boolean;
  revealRequestId?: number;
  onViewSource: (
    sourceId: string,
    excerpt?: InvestigationSourceExcerpt,
  ) => void;
}) {
  const [open, setOpen] = useState(false);
  const regionId = useId();
  const { metricsMarkersBySource } = useContext(EvidenceNavigationContext);
  const { elementRef, revealAfterToggle } =
    useDisclosureReveal<HTMLDivElement>();
  useLayoutEffect(() => {
    if (reveal) setOpen(true);
  }, [reveal, revealRequestId]);
  return (
    <div>
      <button
        type="button"
        aria-expanded={open}
        aria-controls={regionId}
        onClick={() => {
          setOpen(!open);
          revealAfterToggle(!open);
        }}
        className="flex items-center gap-1.5 rounded-md px-1 py-1 text-xs text-theme-text-tertiary hover:bg-theme-hover hover:text-theme-text-secondary"
      >
        <CollapseChevron open={open} className="h-3.5 w-3.5" />
        Previous observations · {observations.length}
      </button>
      <div id={regionId} ref={elementRef}>
        <Collapse open={open}>
          <ol className="space-y-3 pt-2">
            {observations.map((observation) => {
              const key = investigationCaseObservationKey(observation);
              const rowItems = caseItems.filter(
                (item) =>
                  item.observation &&
                  investigationCaseObservationKey(item.observation) === key,
              );
              const rowRoles = [...new Set(rowItems.map((item) => item.role))];
              return (
                <li
                  key={`${observation.source.id}-${observation.revision}`}
                  data-case-observation={rowItems.length > 0 ? key : undefined}
                  className="flex min-w-0 items-start gap-2 text-xs"
                >
                  <span className="mt-0.5 shrink-0 text-theme-text-tertiary">
                    <span className="font-medium text-theme-text-secondary">
                      {observation.source.order === citedOrder
                        ? "Used for assessment"
                        : phaseLabel(observation.source.phase)}
                    </span>
                  </span>
                  <div className="min-w-0 flex-1 space-y-1 text-theme-text-secondary">
                    {rowRoles.length > 0 ? (
                      <p className="flex flex-wrap items-center gap-1.5">
                        <span>{observation.summary || observation.title}</span>
                        {rowRoles.map((role) => (
                          <AgentRoleChip key={role} role={role} />
                        ))}
                      </p>
                    ) : (
                      <p>{observation.summary || observation.title}</p>
                    )}
                    {rowItems
                      .filter((item) => item.claim)
                      .map((item) => (
                        <AgentClaimNote
                          key={item.index}
                          claim={item.claim}
                          role={rowRoles.length > 1 ? item.role : undefined}
                          className="pt-1"
                        />
                      ))}
                    {evidenceHasDetails(
                      observation.data,
                      observation.summary,
                    ) && (
                      <EvidenceBody
                        data={observation.data}
                        cardSummary={observation.summary}
                        changeCoverage={metricsMarkersBySource?.get(
                          observation.source.id,
                        )}
                      />
                    )}
                  </div>
                  <SourceButton
                    ariaLabel={`View result for ${phaseLabel(observation.source.phase).toLowerCase()} observation of ${observation.title}`}
                    onClick={() =>
                      onViewSource(
                        observation.source.id,
                        evidenceSourceExcerpt(observation.data),
                      )
                    }
                  />
                </li>
              );
            })}
          </ol>
        </Collapse>
      </div>
    </div>
  );
}
