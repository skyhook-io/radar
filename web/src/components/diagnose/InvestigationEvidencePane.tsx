import {
  createContext,
  useCallback,
  useContext,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { clsx } from "clsx";
import {
  Activity,
  AlertTriangle,
  BellRing,
  Boxes,
  Bug,
  ChartLine,
  CheckCircle2,
  CircleAlert,
  Clock3,
  FileClock,
  FileSearch,
  Info,
  KeyRound,
  ListTree,
  Network,
  Package,
  ScrollText,
  SearchCheck,
  ShieldAlert,
  SquareArrowOutUpRight,
} from "lucide-react";
import {
  Badge,
  Collapse,
  CollapseChevron,
  DiffViewer,
  StatusDot,
  TerminalBlock,
  defaultConditionTone,
  displayKind,
  formatRelativeAgeTime,
  hpaStateLabel,
  hpaStateLevel,
  mapHealthToTone,
  ResourceLink,
  stripAnsi,
} from "@skyhook-io/k8s-ui";
import {
  AreaChart,
  SERIES_COLORS,
  SeriesLegend,
  formatMetricValue,
  seriesDisplayLabels,
  seriesFill,
  type ChartAnnotation,
  type TimeSeries,
} from "@skyhook-io/k8s-ui/components/charts";
import { apiVersionToGroup } from "../../utils/navigation";
import { parseLogLine } from "../../utils/log-format";
import { metricsChangeMarkers, metricsDomain } from "./investigationMetrics";
import {
  evidenceDisplaySnapshot,
  groupEvidenceCoverage,
  type EvidenceCoverageGroup,
} from "./investigationEvidencePresentation";

import {
  investigationEvidenceSubjectRef,
  investigationEvidenceSourceDomId,
  type InvestigationEvidenceData,
  type InvestigationEvidenceGroup,
  type InvestigationEvidenceObservation,
  type InvestigationEvidenceProjection,
  type InvestigationRootCauseEvidenceResolution,
  type InvestigationEvidenceSource,
  type InvestigationEvidenceTier,
} from "./investigationEvidence";
import type {
  DiagnosisResourceContext,
  DiagnosisResourceRef,
  DiagnosisScalerRef,
} from "./diagnoseEvidenceTypes";
import { InvestigationResourceEvidence } from "./InvestigationResourceEvidence";
import { investigationResourceEvidenceHasDetails } from "./investigationResourceEvidenceModel";
import type { InvestigationSourceExcerpt } from "./investigationSourceFocus";
import { evidenceSourceExcerpt } from "./investigationSourceFocus";
import { Tooltip } from "../ui/Tooltip";

import {
  investigationDisclosureSettleDelay,
  prefersReducedMotion,
  useDisclosureReveal,
} from "./useDisclosureReveal";
export {
  INVESTIGATION_DISCLOSURE_SETTLE_MS,
  investigationDisclosureSettleDelay,
  investigationDisclosureScrollTop,
} from "./useDisclosureReveal";
export const VISIBLE_LOG_EVIDENCE_LINES = 12;

/** The Timeline scope a changes card can open: one resource name in one namespace. */
export interface InvestigationTimelineScope {
  namespace?: string;
  name: string;
}

const EvidenceNavigationContext = createContext<{
  onOpenResource?: (ref: DiagnosisResourceRef) => void;
  onOpenTimeline?: (scope: InvestigationTimelineScope) => void;
  revealSourceId?: string;
  revealRequestId?: number;
  expandedGroupIds?: ReadonlySet<string>;
  onGroupOpenChange?: (id: string, open: boolean) => void;
  citedOrderByGroup?: ReadonlyMap<string, number>;
  /** Change markers for each metrics observation, keyed by its source id. */
  metricsMarkersBySource?: ReadonlyMap<string, ChartAnnotation[]>;
}>({});

function evidenceTypePrefersFullRow(
  type: InvestigationEvidenceData["type"],
): boolean {
  return (
    type === "logs" ||
    type === "events" ||
    type === "alerts" ||
    type === "metrics"
  );
}

// Supporting evidence becomes a two-column grid when the pane is wide enough.
// Keep compact cards paired only with an adjacent compact card. Otherwise a
// full-row card between them strands a conspicuous empty half-row (and makes the
// visual order look accidental), as does an odd card at the end of a run.
export function investigationEvidenceFullRowFlags(
  types: readonly InvestigationEvidenceData["type"][],
): boolean[] {
  const fullRow = types.map(evidenceTypePrefersFullRow);
  let compactRunStart = 0;

  for (let index = 0; index <= types.length; index += 1) {
    if (index < types.length && !fullRow[index]) continue;
    const compactRunLength = index - compactRunStart;
    if (compactRunLength % 2 === 1) fullRow[index - 1] = true;
    compactRunStart = index + 1;
  }

  return fullRow;
}

type EvidenceCollection = "main" | "workload" | "earlier";

export function partitionInvestigationEvidence(
  groups: InvestigationEvidenceGroup[],
  resolution?: InvestigationRootCauseEvidenceResolution,
) {
  const selected = new Set(
    resolution?.status === "linked"
      ? resolution.links.map((link) => link.originalGroupId)
      : [],
  );
  const collections: Record<EvidenceCollection, InvestigationEvidenceGroup[]> =
    {
      main: [],
      workload: [],
      earlier: [],
    };
  const collectionByGroup = new Map<string, EvidenceCollection>();
  const adverse = (group: InvestigationEvidenceGroup) =>
    ["error", "alert", "warning"].includes(group.latest.tone);
  // Broader cards are facts about something other than the target. They are
  // withheld unless cited, and the pane says how many were withheld so a
  // reader knows the agent looked at things it did not build its case on.
  let hiddenBroader = 0;
  // Broader metrics are facts about something other than the target. They are
  // withheld unless cited, and the pane says how many were withheld so a
  // reader knows the agent ran queries it did not build its case on.
  let hiddenMetrics = 0;
  for (const group of groups) {
    const broader = group.latest.relevance === "broader";
    // Citations select tool results, not individual rows in a broad search.
    // Only a focused, unambiguous fact can be promoted by a source citation.
    if (broader) {
      const link = resolution?.links.find(
        (item) => item.originalGroupId === group.id,
      );
      const focused = FOCUSED_EVIDENCE_TYPES.includes(group.latest.data.type);
      const sourceGroups = link
        ? groups.filter(
            (candidate) =>
              FOCUSED_EVIDENCE_TYPES.includes(candidate.latest.data.type) &&
              candidate.observations.some(
                (observation) => observation.source.id === link.source.id,
              ),
          )
        : [];
      if (!selected.has(group.id) || !focused || sourceGroups.length !== 1) {
        // The two counts head separate lines in the pane, so they have to be
        // disjoint: a withheld chart announced by both would read as two
        // withheld results.
        if (!group.historical) {
          if (group.latest.data.type === "metrics") hiddenMetrics += 1;
          else hiddenBroader += 1;
        }
        continue;
      }
    }
    const main =
      !group.historical &&
      (selected.has(group.id) ||
        (!broader &&
          (group.latest.tier === "key" ||
            (group.latest.tier === "supporting" && adverse(group)))));
    const collection = main
      ? "main"
      : group.historical
        ? "earlier"
        : "workload";
    collections[collection].push(group);
    collectionByGroup.set(group.id, collection);
  }
  collections.main.sort(
    (left, right) =>
      Number(right.latest.tier === "key") -
        Number(left.latest.tier === "key") ||
      Number(adverse(right)) - Number(adverse(left)) ||
      left.firstOrder - right.firstOrder,
  );
  // A healthy workload's collection opens with several "nothing here"
  // receipts; the cards that carry facts (its vitals above all) come first,
  // and the receipts keep their order among themselves.
  collections.workload.sort(
    (left, right) =>
      Number(left.kind === "receipt") - Number(right.kind === "receipt"),
  );
  return { ...collections, collectionByGroup, hiddenBroader, hiddenMetrics };
}

// Kinds whose card is one unambiguous subject, so a citation of their source
// can promote exactly that fact. A broad search (issues, inventory, events)
// yields many rows per source; a citation cannot pick one of those.
const FOCUSED_EVIDENCE_TYPES: readonly InvestigationEvidenceData["type"][] = [
  "resource",
  "logs",
  "crash",
  "helm",
  "alerts",
  "permissions",
  "metrics",
];

export function investigationEvidenceRevealCollection(
  projection: InvestigationEvidenceProjection,
  sourceId: string,
  partition = partitionInvestigationEvidence(projection.groups),
): Exclude<EvidenceCollection, "main"> | "coverage" | undefined {
  const source = projection.sources.find((item) => item.id === sourceId);
  const collection = source?.primaryGroupId
    ? partition.collectionByGroup.get(source.primaryGroupId)
    : undefined;
  if (collection) return collection === "main" ? undefined : collection;
  if (
    projection.limitations.some((limitation) =>
      limitation.sources.some((source) => source.id === sourceId),
    )
  )
    return "coverage";
  return undefined;
}

export function InvestigationEvidencePane({
  projection,
  rootCauseEvidence,
  collecting,
  animateGroupIds,
  onViewSource,
  onViewActivity,
  onOpenResource,
  onOpenTimeline,
  afterEvidence,
  revealRequest,
  onRevealReady,
}: {
  projection: InvestigationEvidenceProjection;
  /** Server-validated links for the current root cause; absent without one. */
  rootCauseEvidence?: InvestigationRootCauseEvidenceResolution;
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
  /** Actions follow the complete evidence section, including its disclosures. */
  afterEvidence?: ReactNode;
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
  const handledRevealRequestRef = useRef<number | undefined>(undefined);
  const openingForRevealRequestRef = useRef<number | undefined>(undefined);
  const partition = partitionInvestigationEvidence(
    projection.groups,
    rootCauseEvidence,
  );
  const hasCurrentEvidence =
    partition.main.length + partition.workload.length > 0;
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
  ]);

  const coverageGroups = groupEvidenceCoverage(projection.limitations);
  const limitationSummary = coverageGroups
    .map((group) => `${group.label}: ${group.summary}`)
    .join(" · ");
  const content = (
    <section
      aria-labelledby="investigation-radar-evidence"
      className="investigation-evidence @container/evidence space-y-4 rounded-xl border p-4"
    >
      <span className="sr-only" role="status" aria-live="polite">
        {projection.limitations.length > 0
          ? `Evidence coverage update: ${limitationSummary}`
          : ""}
      </span>
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

      {projection.limitations.length > 0 ? (
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

        <div className="grid items-start gap-2.5">
          {partition.main.map((group) => (
            <EvidenceCard
              key={group.id}
              group={group}
              spanFullRow
              animateArrival={animateGroupIds.has(group.id)}
              onViewSource={onViewSource}
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
          title="More evidence about this workload"
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
        revealSourceId: revealRequest?.sourceId,
        revealRequestId: revealRequest?.requestId,
        metricsMarkersBySource: new Map(
          projection.groups.flatMap((group) =>
            group.observations.flatMap((observation) =>
              observation.data.type === "metrics"
                ? [
                    [
                      observation.source.id,
                      metricsChangeMarkers(projection.groups, observation),
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
      {afterEvidence}
    </EvidenceNavigationContext.Provider>
  );
}

function AssessmentEvidenceQualification({
  resolution,
  onViewActivity,
}: {
  resolution: InvestigationRootCauseEvidenceResolution;
  onViewActivity: () => void;
}) {
  return (
    <div className="flex items-start gap-2 rounded-lg border border-amber-500/30 bg-amber-500/5 px-3 py-2.5">
      <AlertTriangle
        className="mt-0.5 h-4 w-4 shrink-0 text-amber-500"
        aria-hidden
      />
      <div className="min-w-0 flex-1">
        <p className="text-xs font-medium text-theme-text-primary">
          {resolution.status === "invalid"
            ? "Assessment references could not be matched"
            : "Assessment does not cite specific Radar evidence"}
        </p>
        <p className="mt-0.5 text-xs leading-relaxed text-theme-text-tertiary">
          {resolution.status === "invalid"
            ? "Radar could not match the assessment’s references to this investigation. Review Activity before acting."
            : "Review Activity and the Radar evidence below before acting on the agent’s conclusion."}
        </p>
      </div>
      <button
        type="button"
        onClick={onViewActivity}
        className="shrink-0 rounded-md px-2 py-1 text-xs font-medium text-accent-text hover:bg-theme-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/50"
      >
        View Activity
      </button>
    </div>
  );
}

function EmptyCollection({
  collecting,
  hasEarlierEvidence,
  onViewActivity,
}: {
  collecting: boolean;
  hasEarlierEvidence: boolean;
  onViewActivity: () => void;
}) {
  return (
    <div className="rounded-lg border border-dashed border-theme-border px-4 py-5 text-center">
      {collecting ? (
        <Activity className="mx-auto h-5 w-5 text-accent" aria-hidden />
      ) : (
        <SearchCheck
          className="mx-auto h-5 w-5 text-theme-text-tertiary"
          aria-hidden
        />
      )}
      <p className="mt-2 text-sm font-medium text-theme-text-secondary">
        {collecting
          ? "Evidence will appear here"
          : hasEarlierEvidence
            ? "No current evidence was captured during verification"
            : "No relevant evidence to show yet"}
      </p>
      <p className="mx-auto mt-1 max-w-md text-xs leading-relaxed text-theme-text-tertiary">
        {collecting
          ? "The Activity pane remains the live record while the agent investigates."
          : hasEarlierEvidence
            ? "Earlier observations remain below. This does not prove that those conditions resolved."
            : "Investigation details are still available in Activity. This does not mean the resource is healthy."}
      </p>
      <button
        type="button"
        onClick={onViewActivity}
        className="mt-2 rounded-md px-2 py-1 text-xs font-medium text-accent-text hover:bg-theme-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/50"
      >
        View Activity
      </button>
    </div>
  );
}

function CollapsedEvidenceCollection({
  id,
  title,
  description,
  groups,
  animateGroupIds,
  onViewSource,
  open,
  onOpenChange,
  totalCount = groups.length,
  children,
}: {
  id: string;
  title: string;
  description: string;
  groups: InvestigationEvidenceGroup[];
  animateGroupIds: ReadonlySet<string>;
  onViewSource: (
    sourceId: string,
    excerpt?: InvestigationSourceExcerpt,
  ) => void;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  totalCount?: number;
  children?: ReactNode;
}) {
  const { elementRef, revealAfterToggle } = useDisclosureReveal<HTMLElement>();
  if (totalCount === 0) return null;
  const fullRowFlags = investigationEvidenceFullRowFlags(
    groups.map((group) => group.latest.data.type),
  );
  return (
    <section
      ref={elementRef}
      className="overflow-hidden rounded-lg border border-theme-border/80 bg-theme-base/20"
    >
      <button
        type="button"
        aria-expanded={open}
        aria-controls={id}
        onClick={() => {
          onOpenChange(!open);
          revealAfterToggle(!open);
        }}
        className="flex w-full min-w-0 items-center gap-2 px-3 py-2.5 text-left hover:bg-theme-hover/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/50"
      >
        <span className="min-w-0 flex-1">
          <span className="block text-xs font-semibold text-theme-text-secondary">
            {title}
          </span>
          {description ? (
            <span className="block text-xs text-theme-text-tertiary">
              {description}
            </span>
          ) : null}
        </span>
        <span className="shrink-0 font-mono text-xs text-theme-text-tertiary">
          {totalCount}
        </span>
        <CollapseChevron open={open} className="h-4 w-4" />
      </button>
      <div id={id}>
        <Collapse open={open}>
          <div
            className={clsx(
              "grid items-start gap-2.5 @min-[760px]/evidence:grid-cols-2",
              "border-t border-theme-border/60 p-2.5",
            )}
          >
            {groups.map((group, index) => (
              <EvidenceCard
                key={group.id}
                group={group}

                animateArrival={animateGroupIds.has(group.id)}
                onViewSource={onViewSource}
                spanFullRow={fullRowFlags[index]}
                prominence="secondary"
              />
            ))}
            {children ? <div className="col-span-full">{children}</div> : null}
          </div>
        </Collapse>
      </div>
    </section>
  );
}

function CoverageStrip({
  groups,
  visibleGroupIds,
  summary,
  onViewSource,
  open,
  onOpenChange,
}: {
  groups: EvidenceCoverageGroup[];
  visibleGroupIds: ReadonlySet<string>;
  summary: string;
  onViewSource: (
    sourceId: string,
    excerpt?: InvestigationSourceExcerpt,
  ) => void;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const hasError = groups.some((group) => group.hasError);
  const historyOnly = groups.every((group) => group.historyOnly);
  const quiet = historyOnly;
  const { elementRef, revealAfterToggle } =
    useDisclosureReveal<HTMLDivElement>();
  const regionId = "investigation-evidence-coverage";
  const anchoredSources = new Set<string>();
  return (
    <div
      ref={elementRef}
      className="overflow-hidden rounded-lg border border-theme-border/80 bg-theme-base/20"
    >
      <button
        type="button"
        aria-expanded={open}
        aria-controls={regionId}
        onClick={() => {
          onOpenChange(!open);
          revealAfterToggle(!open);
        }}
        className="flex w-full min-w-0 items-center gap-2 px-3 py-2 text-left hover:bg-theme-hover/50"
      >
        {quiet ? (
          <Info
            className="h-4 w-4 shrink-0 text-theme-text-tertiary"
            aria-hidden
          />
        ) : hasError ? (
          <CircleAlert className="h-4 w-4 shrink-0 text-red-400" aria-hidden />
        ) : (
          <AlertTriangle
            className="h-4 w-4 shrink-0 text-amber-500"
            aria-hidden
          />
        )}
        <span className="min-w-0 flex-1">
          <span
            className={clsx(
              "block text-xs",
              quiet
                ? "font-medium text-theme-text-secondary"
                : "font-semibold text-theme-text-primary",
            )}
          >
            {historyOnly
              ? "Change history is limited"
              : "Evidence coverage is incomplete"}
          </span>
          {!historyOnly && !open ? (
            <span className="line-clamp-2 text-xs text-theme-text-tertiary [overflow-wrap:anywhere]">
              {summary}
            </span>
          ) : null}
        </span>
        <CollapseChevron open={open} className="h-4 w-4" />
      </button>
      <div id={regionId}>
        <Collapse open={open}>
          <div className="divide-y divide-theme-border/60 border-t border-theme-border/60">
            {groups.map((group) => {
              const sourceIds = group.limitations
                .flatMap((item) => item.sources)
                .filter((source) => {
                  if (
                    (source.primaryGroupId &&
                      visibleGroupIds.has(source.primaryGroupId)) ||
                    anchoredSources.has(source.id)
                  )
                    return false;
                  anchoredSources.add(source.id);
                  return true;
                })
                .map((source) => source.id);
              return (
                <CoverageGroupRow
                  key={group.label}
                  group={group}
                  sourceIds={sourceIds}
                  onViewSource={onViewSource}
                />
              );
            })}
          </div>
        </Collapse>
      </div>
    </div>
  );
}

function CoverageGroupRow({
  group,
  sourceIds,
  onViewSource,
}: {
  group: EvidenceCoverageGroup;
  sourceIds: string[];
  onViewSource: (
    sourceId: string,
    excerpt?: InvestigationSourceExcerpt,
  ) => void;
}) {
  const [open, setOpen] = useState(false);
  const regionId = useId();
  const { elementRef, revealAfterToggle } =
    useDisclosureReveal<HTMLDivElement>();
  // One limitation whose message is the whole summary would repeat itself as
  // its own detail row; show its source link on the summary row instead.
  const single =
    group.limitations.length === 1 &&
    group.limitations[0].message === group.summary
      ? group.limitations[0]
      : undefined;
  const anchors = sourceIds.map((id) => (
    <span
      key={id}
      id={investigationEvidenceSourceDomId(id)}
      className="sr-only scroll-mt-14"
      aria-hidden
    />
  ));
  if (single) {
    return (
      <div
        ref={elementRef}
        data-evidence-source-container
        tabIndex={-1}
        aria-label={`Evidence limitation for ${group.label}: ${group.summary}`}
        className="flex items-center gap-2 px-3 py-2.5 text-xs outline-none focus:ring-2 focus:ring-accent/40"
      >
        {anchors}
        {group.hasError ? (
          <CircleAlert
            className="h-3.5 w-3.5 shrink-0 text-red-400"
            aria-hidden
          />
        ) : single.kind === "truncated" ? (
          <AlertTriangle
            className="h-3.5 w-3.5 shrink-0 text-amber-500"
            aria-hidden
          />
        ) : (
          <Info
            className="h-3.5 w-3.5 shrink-0 text-theme-text-tertiary"
            aria-hidden
          />
        )}
        <p className="min-w-0 flex-1 leading-relaxed text-theme-text-secondary">
          <span className="font-medium text-theme-text-primary">
            {group.label}:
          </span>{" "}
          {group.summary}
        </p>
        {single.sources.at(-1) ? (
          <SourceButton
            ariaLabel={`View source for ${group.label}`}
            buttonLabel={
              single.sources.length > 1 ? "View latest in Activity" : undefined
            }
            onClick={() => onViewSource(single.sources.at(-1)!.id)}
          />
        ) : null}
      </div>
    );
  }
  return (
    <div
      ref={elementRef}
      data-evidence-source-container
      tabIndex={-1}
      aria-label={`Evidence limitation for ${group.label}: ${group.summary}`}
      className="outline-none focus:ring-2 focus:ring-accent/40"
    >
      {anchors}
      <button
        type="button"
        aria-expanded={open}
        aria-controls={regionId}
        onClick={() => {
          setOpen(!open);
          revealAfterToggle(!open);
        }}
        className="flex w-full items-center gap-2 px-3 py-2.5 text-left text-xs hover:bg-theme-hover/50"
      >
        {group.hasError ? (
          <CircleAlert
            className="h-3.5 w-3.5 shrink-0 text-red-400"
            aria-hidden
          />
        ) : (
          <Info
            className="h-3.5 w-3.5 shrink-0 text-theme-text-tertiary"
            aria-hidden
          />
        )}
        <span className="min-w-0 flex-1 leading-relaxed text-theme-text-secondary">
          <span className="font-medium text-theme-text-primary">
            {group.label}:{" "}
          </span>
          {group.summary}
        </span>
        <CollapseChevron open={open} className="h-3.5 w-3.5 shrink-0" />
      </button>
      <div id={regionId}>
        <Collapse open={open}>
          <ul className="space-y-2 border-t border-theme-border/60 px-3 py-2.5">
            {group.limitations.map((limitation, index) => (
              <li
                key={index}
                className="flex min-w-0 items-start gap-2 text-xs"
              >
                {limitation.kind === "error" ? (
                  <CircleAlert
                    className="mt-0.5 h-3.5 w-3.5 shrink-0 text-red-400"
                    aria-hidden
                  />
                ) : limitation.kind === "truncated" ? (
                  <AlertTriangle
                    className="mt-0.5 h-3.5 w-3.5 shrink-0 text-amber-500"
                    aria-hidden
                  />
                ) : (
                  <Info
                    className="mt-0.5 h-3.5 w-3.5 shrink-0 text-theme-text-tertiary"
                    aria-hidden
                  />
                )}
                <p className="min-w-0 flex-1 leading-relaxed text-theme-text-secondary">
                  <span className="font-medium text-theme-text-primary">
                    {limitation.source}:
                  </span>{" "}
                  {limitation.message}
                </p>
                {limitation.sources.at(-1) ? (
                  <SourceButton
                    ariaLabel={`View source for ${limitation.source}`}
                    buttonLabel={
                      limitation.sources.length > 1
                        ? "View latest in Activity"
                        : undefined
                    }
                    onClick={() => onViewSource(limitation.sources.at(-1)!.id)}
                  />
                ) : null}
              </li>
            ))}
          </ul>
        </Collapse>
      </div>
    </div>
  );
}

function previousDifferentObservations(
  group: InvestigationEvidenceGroup,
  citedOrder?: number,
) {
  const seen = new Set([evidenceDisplaySnapshot(group.latest)]);
  return [...group.observations].reverse().filter((observation) => {
    if (
      observation.source.order !== citedOrder &&
      group.observations.some(
        (other) =>
          other.source.order === citedOrder &&
          evidenceDisplaySnapshot(other) ===
            evidenceDisplaySnapshot(observation),
      )
    )
      return false;
    const snapshot = evidenceDisplaySnapshot(observation);
    if (seen.has(snapshot)) return false;
    seen.add(snapshot);
    return true;
  });
}

export function investigationEvidenceShouldRevealHistory(
  group: InvestigationEvidenceGroup,
  sourceId?: string,
): boolean {
  return (
    Boolean(sourceId) &&
    group.latest.source.id !== sourceId &&
    group.observations.some(
      (observation) =>
        observation.source.id === sourceId &&
        evidenceDisplaySnapshot(observation) !==
          evidenceDisplaySnapshot(group.latest),
    )
  );
}

function EvidenceCard({
  group,
  domId = group.id,
  animateArrival,
  onViewSource,
  spanFullRow = false,
  prominence = "primary",
}: {
  group: InvestigationEvidenceGroup;
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
  } = useContext(EvidenceNavigationContext);
  const open = expandedGroupIds?.has(group.id) ?? false;
  const setOpen = useCallback(
    (value: boolean) => onGroupOpenChange?.(group.id, value),
    [onGroupOpenChange, group.id],
  );
  const { elementRef, revealAfterToggle } = useDisclosureReveal<HTMLElement>();
  const observation = group.latest;
  const displaySummary =
    observation.data.type === "crash" && observation.summary
      ? parseLogLine(observation.summary).content
      : observation.summary;
  const citedOrder = citedOrderByGroup?.get(group.id);
  const previousObservations = previousDifferentObservations(group, citedOrder);
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
  const canExpand = hasEvidenceDetails || meaningfulHistory;
  const revealHistory = investigationEvidenceShouldRevealHistory(
    group,
    revealSourceId,
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
        </span>
        {observation.relevance === "broader" &&
        resourceRef &&
        resourceIdentity &&
        !observation.title.includes(resourceIdentity) ? (
          <span className="mt-0.5 block text-xs text-theme-text-secondary">
            {resourceIdentity} · {resourceRef.kind}
          </span>
        ) : null}
        {displaySummary ? (
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
        {group.historical ? (
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
        "rounded-lg border bg-theme-surface",
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
            ariaLabel={`View source for ${observation.title}`}
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
      {canExpand ? (
        <div id={bodyId}>
          <Collapse open={open}>
            <div
              className={clsx(
                "space-y-3 border-t border-theme-border/60",
                prominence === "primary" ? "px-3 py-3" : "px-2.5 py-2.5",
              )}
            >
              {hasEvidenceDetails ? (
                <EvidenceBody
                  data={observation.data}
                  cardSummary={observation.summary}
                  annotations={metricsMarkersBySource?.get(
                    observation.source.id,
                  )}
                />
              ) : null}
              {meaningfulHistory ? (
                <RevisionHistory
                  observations={previousObservations}
                  citedOrder={citedOrder}
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

function OpenResourceButton({
  resourceRef,
  onOpenResource,
  compact = false,
}: {
  resourceRef?: DiagnosisResourceRef;
  onOpenResource?: (ref: DiagnosisResourceRef) => void;
  compact?: boolean;
}) {
  if (!resourceRef || !onOpenResource) return null;
  const identity = `${resourceRef.namespace ? `${resourceRef.namespace}/` : ""}${resourceRef.name}`;
  const label = `Open current ${displayKind(resourceRef.kind)} ${identity} in Radar`;
  return (
    <Tooltip
      content={label}
      delay={350}
      position="left"
      wrapperClassName="flex shrink-0"
    >
      <button
        type="button"
        aria-label={label}
        onClick={() => onOpenResource(resourceRef)}
        className={clsx(
          "flex h-7 shrink-0 items-center justify-center rounded text-theme-text-tertiary transition-colors hover:bg-theme-hover hover:text-accent-text focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent",
          "gap-1 px-2 text-xs font-medium",
        )}
      >
        {compact ? null : (
          <span>Open current {displayKind(resourceRef.kind)}</span>
        )}
        <SquareArrowOutUpRight className="h-3.5 w-3.5" aria-hidden />
      </button>
    </Tooltip>
  );
}

function OpenTimelineButton({
  scope,
  onOpenTimeline,
}: {
  scope: InvestigationTimelineScope;
  onOpenTimeline?: (scope: InvestigationTimelineScope) => void;
}) {
  if (!onOpenTimeline) return null;
  const identity = `${scope.namespace ? `${scope.namespace}/` : ""}${scope.name}`;
  const label = `Open the Timeline for ${identity}`;
  return (
    <Tooltip
      content={label}
      delay={350}
      position="left"
      wrapperClassName="flex shrink-0"
    >
      <button
        type="button"
        aria-label={label}
        onClick={() => onOpenTimeline(scope)}
        className="flex h-7 shrink-0 items-center justify-center gap-1 rounded px-2 text-xs font-medium text-theme-text-tertiary transition-colors hover:bg-theme-hover hover:text-accent-text focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
      >
        <FileClock className="h-3.5 w-3.5" aria-hidden />
        <span className="hidden @min-[540px]/card:inline">Timeline</span>
      </button>
    </Tooltip>
  );
}

function uniquePrimarySources(
  group: InvestigationEvidenceGroup,
): InvestigationEvidenceSource[] {
  // One tool call can contribute repeated revisions to the same semantic group.
  // It still owns one navigation destination, and DOM ids must remain unique.
  const sources = new Map<string, InvestigationEvidenceSource>();
  for (const observation of group.observations) {
    const { source } = observation;
    if (source.primaryGroupId === group.id && !sources.has(source.id)) {
      sources.set(source.id, source);
    }
  }
  return [...sources.values()];
}

function EvidenceBody({
  data,
  cardSummary,
  annotations,
}: {
  data: InvestigationEvidenceData;
  cardSummary?: string;
  /** Change markers for a metrics chart; derived by the pane, never by data. */
  annotations?: ChartAnnotation[];
}) {
  switch (data.type) {
    case "issue":
      return <IssueBody data={data} cardSummary={cardSummary} />;
    case "startup":
      return <StartupBody data={data} />;
    case "crash":
      return <CrashBody data={data} />;
    case "resource":
      return <ResourceBody data={data} />;
    case "logs":
      return <LogsBody data={data} />;
    case "events":
      return <EventsBody data={data} />;
    case "changes":
      return <ChangesBody data={data} />;
    case "dns":
      return <DNSBody data={data} />;
    case "network":
      return <NetworkBody data={data} />;
    case "relationships":
      return <RelationshipsBody data={data} />;
    case "topology":
      return <TopologyBody data={data} />;
    case "inventory":
      return <InventoryBody data={data} />;
    case "receipt":
      return (
        <p className="text-xs text-theme-text-secondary">{data.message}</p>
      );
    case "alerts":
      return <AlertsBody data={data} />;
    case "helm":
      return <HelmBody data={data} />;
    case "permissions":
      return <PermissionsBody data={data} />;
    case "metrics":
      return <MetricsBody data={data} annotations={annotations} />;
  }
}

function evidenceHasDetails(
  data: InvestigationEvidenceData,
  cardSummary?: string,
): boolean {
  switch (data.type) {
    case "issue": {
      const summary = cardSummary?.trim();
      const cause = data.issue.cause?.trim();
      const message = data.issue.message?.trim();
      return Boolean(
        (cause && cause !== summary) ||
        (message && message !== summary && message !== cause) ||
        data.pods?.length,
      );
    }
    case "startup":
      return (data.pods?.length ?? 0) > 1;
    case "receipt":
      return false;
    case "resource": {
      const replicas = data.resourceContext?.workloadSummary?.replicas;
      return Boolean(
        investigationResourceEvidenceHasDetails(data.resource) ||
        replicas?.desired !== undefined ||
        data.resourceContext?.statusSummary?.conditions?.length ||
        diagnosedScalers(data.resourceContext).length ||
        data.gitOpsDiagnosis ||
        data.warnings.length,
      );
    }
    case "logs":
      return (data.logs?.lines?.length ?? 0) > 0 || Boolean(data.error);
    case "changes":
      return data.changes.length > 0 || Boolean(data.changeContext?.evidence);
    default:
      return true;
  }
}

type EvidenceDataOf<T extends InvestigationEvidenceData["type"]> = Extract<
  InvestigationEvidenceData,
  { type: T }
>;

function IssueBody({
  data,
  cardSummary,
}: {
  data: EvidenceDataOf<"issue">;
  cardSummary?: string;
}) {
  const issue = data.issue;
  const showCause =
    Boolean(issue.cause) && issue.cause?.trim() !== cardSummary?.trim();
  const showMessage =
    Boolean(issue.message) &&
    issue.message?.trim() !== cardSummary?.trim() &&
    issue.message?.trim() !== issue.cause?.trim();
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap gap-1.5">
        <Badge
          severity={issue.severity === "critical" ? "error" : "warning"}
          size="sm"
        >
          {issue.severity}
        </Badge>
        <Badge tone="structural" size="sm">
          {issue.kind}
        </Badge>
        <Badge tone="structural" size="sm">
          {issue.namespace ? `${issue.namespace}/` : ""}
          {issue.name}
        </Badge>
        {data.pods && data.pods.length > 0 ? (
          <>
            <Badge tone="structural" size="sm">
              {data.pods.length === 1 ? "1 Pod" : `${data.pods.length} Pods`}
            </Badge>
            {data.pods.map((pod) => (
              <Badge key={pod} tone="structural" size="sm">
                {pod}
              </Badge>
            ))}
          </>
        ) : null}
      </div>
      {showCause ? (
        <p className="text-sm font-medium leading-relaxed text-theme-text-primary">
          {issue.cause}
        </p>
      ) : null}
      {showMessage ? (
        <p className="text-xs leading-relaxed text-theme-text-secondary">
          {issue.message}
        </p>
      ) : null}
    </div>
  );
}

function StartupBody({ data }: { data: EvidenceDataOf<"startup"> }) {
  const blocker = data.blocker;
  const pods = data.pods && data.pods.length > 1 ? data.pods : [blocker.name];
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap gap-1.5">
        <Badge tone="structural" size="sm">
          {pods.length > 1 ? `${pods.length} ${blocker.kind}s` : blocker.kind}
        </Badge>
        {pods.map((pod) => (
          <Badge key={pod} tone="structural" size="sm">
            {pod}
          </Badge>
        ))}
        <Badge severity={severityBadge(blocker.severity)} size="sm">
          {blocker.severity}
        </Badge>
      </div>
      <p className="text-sm leading-relaxed text-theme-text-primary">
        {blocker.message}
      </p>
    </div>
  );
}

function CrashBody({ data }: { data: EvidenceDataOf<"crash"> }) {
  const crash = data.crash;
  return (
    <div className="space-y-2.5">
      <div className="flex flex-wrap gap-1.5">
        <Badge severity="error" size="sm">
          {crash.reason || crash.state}
        </Badge>
        <Badge tone="structural" size="sm">
          exit {crash.exitCode}
        </Badge>
        <Badge tone="structural" size="sm">
          {crash.container}
        </Badge>
      </div>
      <p className="text-xs text-theme-text-tertiary">
        {crash.pods.join(", ")} · {crash.logSource.replaceAll("_", " ")}
      </p>
      <TerminalBlock label="Selected crash line">{crash.logLine}</TerminalBlock>
    </div>
  );
}

function ResourceBody({ data }: { data: EvidenceDataOf<"resource"> }) {
  const replicas = data.resourceContext?.workloadSummary?.replicas;
  // SealedSecret's dedicated body renders the resource's conditions beside
  // its controller state, so repeating the derived summary here adds noise.
  const conditions =
    data.resource.kind.toLowerCase() === "sealedsecret"
      ? []
      : (data.resourceContext?.statusSummary?.conditions ?? []);
  const desired = replicas?.desired;
  const ready = replicas ? (replicas.ready ?? 0) : undefined;
  const shortfall =
    desired !== undefined && ready !== undefined && ready < desired;
  const scalers = diagnosedScalers(data.resourceContext);
  return (
    <div className="space-y-3">
      <InvestigationResourceEvidence resource={data.resource} />
      {data.gitOpsDiagnosis ? (
        <GitOpsStatusBody status={data.gitOpsDiagnosis} />
      ) : null}
      {desired !== undefined && ready !== undefined ? (
        <dl className="flex flex-wrap gap-x-6 gap-y-2 text-xs">
          <div>
            <dt className="text-theme-text-tertiary">Ready replicas</dt>
            <dd
              className={clsx(
                "font-mono font-semibold tabular-nums",
                shortfall ? "text-warning-text" : "text-theme-text-primary",
              )}
            >
              {ready}/{desired}
            </dd>
          </div>
          <ResourceFact label="Available" value={replicas?.available} />
          <ResourceFact label="Updated" value={replicas?.updated} />
          <ResourceFact label="Unavailable" value={replicas?.unavailable} />
          <ResourceFact
            label="Phase"
            value={data.resourceContext?.statusSummary?.phase}
          />
        </dl>
      ) : null}
      {conditions.length > 0 ? (
        <div>
          <div className="mb-1.5 text-xs font-semibold uppercase tracking-wide text-theme-text-tertiary">
            Conditions
          </div>
          <div className="max-h-52 space-y-1.5 overflow-y-auto pr-1">
            {conditions.map((condition) => (
              <div
                key={`${condition.type}-${condition.status}-${condition.reason ?? ""}`}
                className="flex min-w-0 items-start gap-2 text-xs"
              >
                <StatusDot
                  tone={conditionStatusTone(condition)}
                  className="mt-1 shrink-0"
                />
                <p className="min-w-0 text-theme-text-secondary">
                  <span className="font-medium text-theme-text-primary">
                    {condition.type}={condition.status}
                  </span>
                  {condition.reason ? ` · ${condition.reason}` : ""}
                  {condition.message ? ` — ${condition.message}` : ""}
                </p>
              </div>
            ))}
          </div>
        </div>
      ) : null}
      {scalers.length > 0 ? (
        <ScaledBySection
          scalers={scalers}
          allScalers={data.resourceContext?.scaledBy ?? []}
        />
      ) : null}
      {data.warnings.length > 0 ? (
        <ul className="space-y-1 text-xs text-theme-text-secondary">
          {data.warnings.map((warning) => (
            <li key={warning} className="flex items-start gap-1.5">
              <Info className="mt-0.5 h-3.5 w-3.5 shrink-0 text-theme-text-tertiary" />
              <span>{warning}</span>
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}

type DiagnosedScaler = DiagnosisScalerRef &
  Required<Pick<DiagnosisScalerRef, "hpaSummary">>;

function diagnosedScalers(
  context: DiagnosisResourceContext | undefined,
): DiagnosedScaler[] {
  return (context?.scaledBy ?? []).filter(
    (scaler): scaler is DiagnosedScaler => scaler.hpaSummary !== undefined,
  );
}

function ScaledBySection({
  scalers,
  allScalers,
}: {
  scalers: DiagnosedScaler[];
  allScalers: DiagnosisScalerRef[];
}) {
  const { onOpenResource } = useContext(EvidenceNavigationContext);
  // The ScaledObject is only a link when Radar listed it as a scaler too; a
  // name read off the HPA's owner reference alone is not a resource we hold.
  const listedScaler = (ref: DiagnosisResourceRef) =>
    allScalers.some(
      (scaler) =>
        scaler.kind === ref.kind &&
        scaler.name === ref.name &&
        (scaler.namespace ?? "") === (ref.namespace ?? ""),
    );
  return (
    <div>
      <div className="mb-1.5 text-xs font-semibold uppercase tracking-wide text-theme-text-tertiary">
        Scaled by
      </div>
      <div className="space-y-2">
        {scalers.map((scaler) => {
          const summary = scaler.hpaSummary;
          const bounds = summary.bounds;
          // The reason that named the state is the summary in other words;
          // only the controller's own sentence behind it adds anything.
          const stateReasons = (summary.reasons ?? []).filter(
            (reason) =>
              reason.id === summary.state || reason.message === summary.summary,
          );
          const otherReasons = (summary.reasons ?? []).filter(
            (reason) => !stateReasons.includes(reason),
          );
          return (
            <div
              key={`${scaler.namespace ?? ""}/${scaler.name}`}
              className="space-y-1 text-xs"
            >
              <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
                <span className="font-medium text-theme-text-primary">
                  {displayKind(scaler.kind)} {scaler.name}
                </span>
                {scaler.managedBy ? (
                  <span className="text-theme-text-tertiary">
                    managed by KEDA {displayKind(scaler.managedBy.kind)}{" "}
                    <ResourceLink
                      name={scaler.managedBy.name}
                      kind={scaler.managedBy.kind}
                      namespace={scaler.managedBy.namespace ?? ""}
                      group={scaler.managedBy.group}
                      onNavigate={
                        onOpenResource && listedScaler(scaler.managedBy)
                          ? (ref) => onOpenResource(ref)
                          : undefined
                      }
                    />
                  </span>
                ) : null}
                <span className="inline-flex items-center gap-1.5 text-theme-text-secondary">
                  <StatusDot
                    tone={mapHealthToTone(hpaStateLevel(summary.state))}
                    className="shrink-0"
                  />
                  {hpaStateLabel(summary.state)}
                </span>
                {bounds ? (
                  <span className="font-mono tabular-nums text-theme-text-tertiary">
                    {bounds.current}/{bounds.desired} replicas · bounds{" "}
                    {bounds.min}-{bounds.max}
                  </span>
                ) : null}
              </div>
              <p className="text-theme-text-secondary">{summary.summary}</p>
              {stateReasons.map((reason) =>
                reason.detail ? (
                  <p
                    key={`${reason.id}-${reason.detail}`}
                    className="text-theme-text-tertiary"
                  >
                    Kubernetes
                    {reason.conditionType ? ` ${reason.conditionType}` : ""}
                    {reason.conditionReason
                      ? ` · ${reason.conditionReason}`
                      : ""}
                    : &ldquo;{reason.detail}&rdquo;
                  </p>
                ) : null,
              )}
              {otherReasons.length > 0 ? (
                <ul className="list-disc space-y-0.5 pl-4 text-theme-text-secondary marker:text-theme-text-tertiary">
                  {otherReasons.map((reason) => (
                    <li key={`${reason.id}-${reason.message}`}>
                      {reason.message}
                      {reason.detail ? (
                        <span className="text-theme-text-tertiary">
                          {" "}
                          · {reason.detail}
                        </span>
                      ) : null}
                    </li>
                  ))}
                </ul>
              ) : null}
            </div>
          );
        })}
      </div>
    </div>
  );
}

function GitOpsStatusBody({
  status,
}: {
  status: NonNullable<EvidenceDataOf<"resource">["gitOpsDiagnosis"]>;
}) {
  const fields = [
    ["Sync", status.sync],
    ["Health", status.health],
    ["Operation", status.operationPhase],
    ["Ready", status.ready],
  ] as const;
  return (
    <div className="rounded-md border border-theme-border bg-theme-base/40 p-2.5">
      <div className="mb-2 flex flex-wrap items-center gap-1.5">
        <span className="text-xs font-semibold uppercase tracking-wide text-theme-text-tertiary">
          GitOps controller status
        </span>
        <Badge tone="note" size="sm">
          {status.tool === "argocd" ? "Argo CD" : "Flux"}
        </Badge>
        {status.suspended ? (
          <Badge severity="info" size="sm">
            Suspended
          </Badge>
        ) : null}
      </div>
      <div className="flex flex-wrap gap-1.5">
        {fields.map(([label, value]) =>
          value ? (
            <Badge
              key={label}
              severity={gitOpsValueSeverity(label, value)}
              size="sm"
            >
              {label}: {value}
            </Badge>
          ) : null,
        )}
        {status.appliedRevision ? (
          <Badge tone="structural" size="sm">
            {status.appliedRevision}
          </Badge>
        ) : null}
      </div>
    </div>
  );
}

function gitOpsValueSeverity(label: string, value: string) {
  const normalized = value.toLowerCase();
  if (
    normalized === "healthy" ||
    normalized === "synced" ||
    normalized === "succeeded" ||
    (label === "Ready" && normalized.startsWith("true"))
  ) {
    return "success" as const;
  }
  if (
    normalized === "degraded" ||
    normalized === "missing" ||
    normalized === "failed" ||
    normalized === "error" ||
    (label === "Ready" && normalized.startsWith("false"))
  ) {
    return "error" as const;
  }
  if (normalized === "outofsync") return "warning" as const;
  if (normalized === "progressing" || normalized === "running")
    return "info" as const;
  return "neutral" as const;
}

function ResourceFact({ label, value }: { label: string; value: unknown }) {
  if (value === undefined || value === null || value === "") return null;
  return (
    <div className="flex justify-between gap-2 @min-[560px]/evidence:block">
      <dt className="text-theme-text-tertiary">{label}</dt>
      <dd className="font-mono font-medium text-theme-text-primary">
        {String(value)}
      </dd>
    </div>
  );
}

function conditionStatusTone(condition: {
  type: string;
  status: string;
}): "healthy" | "degraded" | "unhealthy" | "unknown" {
  switch (defaultConditionTone(condition)) {
    case "ok":
      return "healthy";
    case "warning":
      return "degraded";
    case "fail":
      return "unhealthy";
    case "unknown":
      return "unknown";
  }
}

function LogsBody({ data }: { data: EvidenceDataOf<"logs"> }) {
  const lines = data.logs?.lines ?? [];
  const visibleLines = lines
    .slice(-VISIBLE_LOG_EVIDENCE_LINES)
    .map((line) => stripAnsi(line));
  const omittedLines = lines.length - visibleLines.length;
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-1.5">
        <Badge tone="structural" size="sm">
          {data.pod} / {data.container}
        </Badge>
        <Badge severity="neutral" size="sm">
          {data.previous ? "previous instance" : "current instance"}
        </Badge>
        {data.logs?.fallback ? (
          <Badge severity="warning" size="sm">
            Unfiltered log tail
          </Badge>
        ) : null}
        {data.logs ? (
          <span className="text-xs text-theme-text-tertiary">
            {data.logs.matchedLines} matching lines · {data.logs.totalLines}{" "}
            processed from the requested log tail
          </span>
        ) : null}
      </div>
      {visibleLines.length > 0 ? (
        <TerminalBlock
          label={
            omittedLines > 0
              ? `Selected log excerpt · last ${visibleLines.length} of ${lines.length} lines`
              : "Selected log excerpt"
          }
        >
          {visibleLines.join("\n")}
        </TerminalBlock>
      ) : (
        <p className="text-xs italic text-theme-text-tertiary">
          No lines were captured from this stream.
        </p>
      )}
      {data.error ? (
        <p className="text-xs leading-relaxed text-red-400">{data.error}</p>
      ) : null}
      {data.warnings.map((warning) => (
        <p key={warning} className="text-xs text-warning-text">
          {warning}
        </p>
      ))}
    </div>
  );
}

function EventsBody({ data }: { data: EvidenceDataOf<"events"> }) {
  return (
    <div>
      <p className="mb-2 text-xs text-theme-text-tertiary">{data.scope}</p>
      <ol className="max-h-[28rem] space-y-0 overflow-y-auto pr-1">
        {data.events.map((event, index) => (
          <li
            key={`${event.reason}-${event.lastTimestamp}-${index}`}
            className="relative flex gap-3 pb-3 last:pb-0"
          >
            {index < data.events.length - 1 ? (
              <span className="absolute bottom-0 left-[5px] top-3 w-px bg-theme-border" />
            ) : null}
            <span className="relative mt-1.5 h-2.5 w-2.5 shrink-0 rounded-full border-2 border-amber-500 bg-theme-surface" />
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5">
                <span className="text-xs font-semibold text-theme-text-primary">
                  {event.reason}
                  {event.count > 1 ? (
                    <span className="ml-1.5 font-mono text-[11px] font-normal text-theme-text-tertiary">
                      ×{event.count}
                    </span>
                  ) : null}
                </span>
                <Tooltip
                  content={new Date(event.lastTimestamp).toLocaleString()}
                  delay={150}
                  position="left"
                >
                  <time
                    dateTime={event.lastTimestamp}
                    className="text-xs text-theme-text-tertiary"
                  >
                    {formatRelativeAgeTime(event.lastTimestamp)}
                  </time>
                </Tooltip>
              </div>
              <p className="mt-0.5 text-xs leading-relaxed text-theme-text-secondary">
                {event.message}
              </p>
            </div>
          </li>
        ))}
      </ol>
    </div>
  );
}

function ChangesBody({ data }: { data: EvidenceDataOf<"changes"> }) {
  const { onOpenResource } = useContext(EvidenceNavigationContext);
  return (
    <div className="space-y-2.5">
      {data.changeContext?.evidence ? (
        <p className="text-xs leading-relaxed text-theme-text-secondary [overflow-wrap:anywhere]">
          {data.changeContext.evidence}
        </p>
      ) : null}
      {data.changes.map((change, index) => (
        <div
          key={`${change.kind}-${change.namespace ?? ""}-${change.name}-${change.timestamp}-${index}`}
          className="rounded-md border border-theme-border/70 bg-theme-base/30 p-2.5"
        >
          <div className="flex flex-wrap items-center gap-1.5">
            <Badge tone="structural" size="sm">
              {change.kind}
            </Badge>
            <span className="font-mono text-xs">
              <ResourceLink
                name={change.name}
                kind={change.kind}
                namespace={change.namespace ?? ""}
                group={
                  change.apiVersion
                    ? apiVersionToGroup(change.apiVersion)
                    : undefined
                }
                label={`${change.namespace ? `${change.namespace}/` : ""}${change.name}`}
                onNavigate={
                  change.apiVersion && onOpenResource
                    ? (ref) => onOpenResource(ref)
                    : undefined
                }
              />
            </span>
            <Badge tone="note" size="sm">
              {change.changeType.replaceAll("_", " ")}
            </Badge>
            <Tooltip
              content={new Date(change.timestamp).toLocaleString()}
              delay={150}
              position="left"
              wrapperClassName="ml-auto"
            >
              <time
                dateTime={change.timestamp}
                className="text-xs text-theme-text-tertiary"
              >
                {formatRelativeAgeTime(change.timestamp)}
              </time>
            </Tooltip>
          </div>
          {change.summary ? (
            <p className="mt-1.5 text-xs text-theme-text-secondary">
              {change.summary}
            </p>
          ) : null}
          {change.fields?.length ? (
            <div className="mt-2">
              <DiffViewer
                diff={{
                  summary: `${change.fields.length} changed ${change.fields.length === 1 ? "field" : "fields"}`,
                  fields: change.fields.map((field) => ({
                    path: field.path,
                    oldValue: field.oldValue ?? null,
                    newValue: field.newValue ?? null,
                  })),
                }}
              />
            </div>
          ) : null}
        </div>
      ))}
    </div>
  );
}

function DNSBody({ data }: { data: EvidenceDataOf<"dns"> }) {
  const { onOpenResource } = useContext(EvidenceNavigationContext);
  return (
    <div className="space-y-2">
      {(data.dns.signals ?? []).map((signal) => (
        <p
          key={signal}
          className="text-xs leading-relaxed text-theme-text-secondary"
        >
          {signal}
        </p>
      ))}
      {(data.dns.coreDNSFindings ?? []).map((finding) => (
        <div
          key={`${finding.kind}-${finding.namespace}-${finding.name}-${finding.reason}`}
          className="rounded-md border border-theme-border bg-theme-base/40 p-2"
        >
          <div className="flex flex-wrap gap-1.5">
            <Badge tone="structural" size="sm">
              {finding.kind}
            </Badge>
            <span className="font-mono text-xs">
              <ResourceLink
                name={finding.name}
                kind={finding.kind}
                namespace={finding.namespace}
                group=""
                label={`${finding.namespace}/${finding.name}`}
                onNavigate={
                  finding.kind.toLowerCase() === "configmap" && onOpenResource
                    ? (ref) => onOpenResource(ref)
                    : undefined
                }
              />
            </span>
            <Badge severity={severityBadge(finding.severity)} size="sm">
              {finding.severity}
            </Badge>
          </div>
          <p className="mt-1 text-xs text-theme-text-secondary">
            {finding.reason}
            {finding.message ? ` — ${finding.message}` : ""}
          </p>
        </div>
      ))}
    </div>
  );
}

function NetworkBody({ data }: { data: EvidenceDataOf<"network"> }) {
  const { network } = data;
  const stats = [
    ["Tested", network.summary.tested],
    ["Passed", network.summary.passed],
    ["Failed", network.summary.failed],
    ["Inferred", network.summary.derived ?? 0],
    ["Skipped", network.summary.skipped],
  ] as const;
  return (
    <div className="space-y-3">
      <div className="grid grid-cols-3 gap-1.5 @min-[620px]/evidence:grid-cols-5">
        {stats.map(([label, value]) => (
          <div
            key={label}
            className="rounded-md border border-theme-border bg-theme-base/40 px-2 py-1.5 text-center"
          >
            <div className="font-mono text-sm font-semibold text-theme-text-primary">
              {value}
            </div>
            <div className="text-xs uppercase tracking-wide text-theme-text-tertiary">
              {label}
            </div>
          </div>
        ))}
      </div>
      {network.diagnosis ? (
        <div className="rounded-md border border-theme-border bg-theme-base/40 px-2.5 py-2">
          <p className="text-xs font-medium leading-relaxed text-theme-text-primary">
            {network.diagnosis.summary}
          </p>
          {network.diagnosis.nextAction ? (
            <p className="mt-1 border-l-2 border-accent/50 pl-2 text-xs leading-relaxed text-theme-text-secondary">
              Next check: {network.diagnosis.nextAction}
            </p>
          ) : null}
        </div>
      ) : (
        <p className="text-xs leading-relaxed text-theme-text-secondary">
          {network.summary.headline}
        </p>
      )}
      {network.routes.length > 0 ? (
        <ol className="max-h-64 space-y-1.5 overflow-y-auto pr-1">
          {network.routes.map((route, index) => (
            <li
              key={`${route.route}-${route.target ?? ""}-${index}`}
              className="flex min-w-0 items-start gap-2 rounded-md border border-theme-border/70 bg-theme-base/30 px-2.5 py-2"
            >
              <StatusDot
                tone={networkOutcomeTone(route.outcome, route.benign)}
                className="mt-1 shrink-0"
              />
              <span className="min-w-0 flex-1">
                <span className="block truncate font-mono text-xs text-theme-text-primary">
                  {route.route}
                  {route.target ? ` → ${route.target}` : ""}
                </span>
                {route.evidence ? (
                  <span className="mt-0.5 block text-xs leading-relaxed text-theme-text-secondary">
                    {route.evidence}
                  </span>
                ) : null}
              </span>
              <Badge
                severity={networkOutcomeSeverity(route.outcome, route.benign)}
                size="sm"
              >
                {route.benign
                  ? "intentional"
                  : route.outcome.replaceAll("_", " ")}
              </Badge>
            </li>
          ))}
        </ol>
      ) : null}
    </div>
  );
}

function networkOutcomeTone(
  outcome: string,
  benign?: boolean,
): "healthy" | "degraded" | "unhealthy" | "unknown" {
  if (benign) return "degraded";
  const normalized = outcome.toLowerCase();
  if (normalized.includes("verified") || normalized.includes("reached"))
    return "healthy";
  if (normalized.includes("fail") || normalized.includes("unreachable"))
    return "unhealthy";
  if (normalized.includes("skip") || normalized.includes("not"))
    return "unknown";
  return "degraded";
}

function networkOutcomeSeverity(outcome: string, benign?: boolean) {
  switch (networkOutcomeTone(outcome, benign)) {
    case "healthy":
      return "success" as const;
    case "degraded":
      return "warning" as const;
    case "unhealthy":
      return "error" as const;
    case "unknown":
      return "neutral" as const;
  }
}

function RelationshipsBody({
  data,
}: {
  data: EvidenceDataOf<"relationships">;
}) {
  return (
    <div className="space-y-2.5">
      <div className="flex flex-wrap items-center gap-1.5">
        <Badge tone="structural" size="sm">
          {data.root.kind}
        </Badge>
        <span className="font-mono text-xs text-theme-text-secondary">
          {data.root.namespace ? `${data.root.namespace}/` : ""}
          {data.root.name}
        </span>
        <span className="text-xs text-theme-text-tertiary">
          {data.nodes.length} resources · {data.edges.length} direct
          relationships
        </span>
      </div>
      <div className="max-h-44 overflow-y-auto pr-1">
        <div className="flex flex-wrap gap-1.5">
          {data.nodes.map((node) => (
            <span
              key={node.id}
              className="inline-flex items-center gap-1 rounded-md border border-theme-border bg-theme-base px-2 py-1 text-xs"
            >
              <Badge tone="structural" size="sm">
                {node.kind}
              </Badge>
              <span className="font-mono text-theme-text-secondary">
                {node.name}
              </span>
            </span>
          ))}
        </div>
      </div>
      {data.edges.length > 0 ? (
        <div className="grid max-h-52 gap-1 overflow-y-auto pr-1 @min-[680px]/evidence:grid-cols-2">
          {data.edges.map((edge, index) => (
            <div
              key={
                edge.id ?? `${edge.source}-${edge.target}-${edge.type}-${index}`
              }
              className="flex min-w-0 items-center gap-1.5 rounded bg-theme-base/50 px-2 py-1.5 font-mono text-xs text-theme-text-secondary"
            >
              <span className="truncate">{edge.source}</span>
              <span className="shrink-0 text-theme-text-tertiary">→</span>
              <span className="truncate">{edge.target}</span>
              <Badge tone="structural" size="sm">
                {edge.label || edge.type}
              </Badge>
            </div>
          ))}
        </div>
      ) : null}
    </div>
  );
}

function TopologyBody({ data }: { data: EvidenceDataOf<"topology"> }) {
  return (
    <div className="space-y-2.5">
      <div className="grid grid-cols-2 gap-2">
        <TopologyStat label="Nodes" value={data.stats.nodes} />
        <TopologyStat label="Relationships" value={data.stats.edges} />
      </div>
      {data.problems.map((problem) => (
        <p
          key={problem}
          className="flex items-start gap-1.5 text-xs text-warning-text"
        >
          <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
          {problem}
        </p>
      ))}
      <div className="max-h-64 space-y-2 overflow-y-auto pr-1">
        {data.namespaces.map((namespace) => (
          <div key={namespace.namespace}>
            <div className="text-xs font-semibold uppercase tracking-wide text-theme-text-tertiary">
              {namespace.namespace || "cluster-scoped"}
            </div>
            <ul className="mt-1 space-y-1 font-mono text-xs text-theme-text-secondary">
              {namespace.chains.map((chain) => (
                <li key={chain}>{chain}</li>
              ))}
            </ul>
          </div>
        ))}
      </div>
      {data.warnings.map((warning) => (
        <p key={warning} className="text-xs text-warning-text">
          {warning}
        </p>
      ))}
    </div>
  );
}

function TopologyStat({ label, value }: { label: string; value: number }) {
  return (
    <div className="rounded-md border border-theme-border bg-theme-base/40 px-3 py-2">
      <div className="font-mono text-lg font-semibold text-theme-text-primary">
        {value}
      </div>
      <div className="text-xs uppercase tracking-wide text-theme-text-tertiary">
        {label}
      </div>
    </div>
  );
}

const METRICS_AXIS_LABEL_MAX_CHARS = 72;

function finiteSamples(series: TimeSeries): TimeSeries["dataPoints"] {
  return series.dataPoints.filter((point) => typeof point.value === "number");
}

function MetricsValueTable({
  series,
  labels,
  unit,
  withTime,
}: {
  series: TimeSeries[];
  /** Display name per series, derived from the complete result. */
  labels: string[];
  unit: string;
  withTime: boolean;
}) {
  return (
    <table className="w-full text-xs">
      <tbody>
        {series.map((item, index) => {
          const sample = finiteSamples(item).at(-1);
          return (
            <tr
              key={`${labels[index]}-${index}`}
              className="border-b border-theme-border/60 last:border-b-0"
            >
              <td className="py-1 pr-3 font-mono text-theme-text-secondary [overflow-wrap:anywhere]">
                {labels[index]}
              </td>
              {withTime ? (
                <td className="py-1 pr-3 text-right text-theme-text-tertiary tabular-nums">
                  {sample
                    ? new Date(sample.timestamp * 1000).toLocaleTimeString()
                    : ""}
                </td>
              ) : null}
              <td className="py-1 text-right font-mono tabular-nums text-theme-text-primary">
                {sample && typeof sample.value === "number"
                  ? formatMetricValue(sample.value, unit)
                  : "no value"}
              </td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

function MetricsBody({
  data,
  annotations,
}: {
  data: EvidenceDataOf<"metrics">;
  annotations?: ChartAnnotation[];
}) {
  const unit = data.unit ?? "";
  const axisLabel = data.label ?? data.query;
  const axisTruncated = axisLabel.length > METRICS_AXIS_LABEL_MAX_CHARS;
  const axisText = axisTruncated
    ? `${axisLabel.slice(0, METRICS_AXIS_LABEL_MAX_CHARS - 1)}…`
    : axisLabel;
  const domain = metricsDomain(data);
  const windowText = domain
    ? `${new Date(domain.start * 1000).toLocaleString()} to ${new Date(domain.end * 1000).toLocaleString()}`
    : undefined;
  if (data.series.length === 0) {
    return (
      <div className="space-y-2">
        <p className="text-xs text-theme-text-secondary">
          No series matched this query
          {data.mode === "range" ? " in the window" : ""}.
        </p>
        <pre className="whitespace-pre-wrap break-all rounded-md border border-theme-border/70 bg-theme-base/30 p-2 font-mono text-xs text-theme-text-secondary">
          {data.query}
        </pre>
        {data.note ? (
          <p className="text-xs text-theme-text-tertiary">{data.note}</p>
        ) : null}
      </div>
    );
  }
  if (data.mode === "instant") {
    return (
      <div className="space-y-2">
        <MetricsValueTable
          series={data.series}
          labels={seriesDisplayLabels(data.series)}
          unit={unit}
          withTime={false}
        />
        <pre className="whitespace-pre-wrap break-all rounded-md border border-theme-border/70 bg-theme-base/30 p-2 font-mono text-xs text-theme-text-secondary">
          {data.query}
        </pre>
        {data.note ? (
          <p className="text-xs text-theme-text-tertiary">{data.note}</p>
        ) : null}
      </div>
    );
  }
  // Names come from the whole result so two series that differ only by a
  // label the chart would hide stay distinguishable wherever they are listed.
  const labels = seriesDisplayLabels(data.series);
  const indexed = data.series.map((item, index) => ({
    item,
    label: labels[index],
    finite: finiteSamples(item).length,
  }));
  // A series with one finite sample has no line to draw, and one with none
  // (Prometheus serializes NaN and infinities as gaps) has nothing to show.
  // Listing both keeps what was captured readable and states what was not.
  const charted = indexed.filter((entry) => entry.finite >= 2);
  const single = indexed.filter((entry) => entry.finite === 1);
  const empty = indexed.filter((entry) => entry.finite === 0);
  return (
    <div className="space-y-2">
      <div className="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-0.5 text-xs">
        <Tooltip
          content={axisLabel}
          delay={200}
          position="top"
          disabled={!axisTruncated}
        >
          <span
            className="min-w-0 truncate font-mono text-theme-text-secondary"
            data-testid="investigation-metrics-axis-label"
          >
            {axisText}
          </span>
        </Tooltip>
        {unit ? (
          <span className="text-theme-text-tertiary">({unit})</span>
        ) : null}
        {windowText ? (
          <span className="ml-auto text-theme-text-tertiary">{windowText}</span>
        ) : null}
      </div>
      {charted.length > 0 ? (
        <div className="space-y-1.5 rounded-md border border-theme-border/70 bg-theme-base/30 p-2">
          <AreaChart
            series={charted.map((entry) => entry.item)}
            seriesLabels={charted.map((entry) => entry.label)}
            color={SERIES_COLORS[0]}
            fillColor={seriesFill(0, SERIES_COLORS[0])}
            unit={unit}
            annotations={annotations}
            domain={domain}
            layout="auto"
          />
          {charted.length > 1 ? (
            <SeriesLegend
              series={charted.map((entry) => entry.item)}
              seriesLabels={charted.map((entry) => entry.label)}
              color={SERIES_COLORS[0]}
            />
          ) : null}
        </div>
      ) : null}
      {single.length > 0 ? (
        <div
          className="space-y-1"
          data-testid="investigation-metrics-sparse-series"
        >
          <p className="text-xs text-theme-text-tertiary">
            {single.length === 1
              ? "1 series has a single sample in this window, listed with its time:"
              : `${single.length} series have a single sample in this window, listed with their times:`}
          </p>
          <MetricsValueTable
            series={single.map((entry) => entry.item)}
            labels={single.map((entry) => entry.label)}
            unit={unit}
            withTime
          />
        </div>
      ) : null}
      {empty.length > 0 ? (
        <p
          className="text-xs text-theme-text-tertiary [overflow-wrap:anywhere]"
          data-testid="investigation-metrics-empty-series"
        >
          {empty.length === 1
            ? "1 series returned no finite values in this window: "
            : `${empty.length} series returned no finite values in this window: `}
          <span className="font-mono">
            {empty.map((entry) => entry.label).join("; ")}
          </span>
        </p>
      ) : null}
      {annotations?.length && charted.length > 0 ? (
        <p className="text-xs text-theme-text-tertiary">
          {annotations.length === 1
            ? "1 change recorded in this window is marked on the chart."
            : `${annotations.length} changes recorded in this window are marked on the chart.`}
        </p>
      ) : null}
      {axisTruncated ? (
        <pre className="whitespace-pre-wrap break-all rounded-md border border-theme-border/70 bg-theme-base/30 p-2 font-mono text-xs text-theme-text-secondary">
          {data.query}
        </pre>
      ) : null}
      {data.note ? (
        <p className="text-xs text-theme-text-tertiary">{data.note}</p>
      ) : null}
    </div>
  );
}

function InventoryBody({ data }: { data: EvidenceDataOf<"inventory"> }) {
  return (
    <div className="max-h-72 overflow-y-auto rounded-md border border-theme-border">
      {data.resources.map((resource, index) => (
        <div
          key={`${resource.kind}-${resource.namespace ?? ""}-${resource.name}`}
          className={clsx(
            "flex min-w-0 items-center gap-2 px-2.5 py-1.5",
            index > 0 && "border-t border-theme-border/60",
          )}
        >
          <StatusDot
            tone={mapHealthToTone(resource.summaryContext?.health ?? "")}
            className="shrink-0"
          />
          <Badge tone="structural" size="sm">
            {resource.kind}
          </Badge>
          <span className="min-w-0 flex-1">
            <span className="block truncate font-mono text-xs text-theme-text-secondary">
              {resource.namespace ? `${resource.namespace}/` : ""}
              {resource.name}
            </span>
            {resource.issue ? (
              <span className="block truncate text-xs text-warning-text">
                {resource.issue}
              </span>
            ) : null}
          </span>
          {resource.ready || resource.status ? (
            <span className="ml-auto shrink-0 font-mono text-xs text-theme-text-tertiary">
              {resource.ready || resource.status}
            </span>
          ) : null}
          {(resource.summaryContext?.issueCount ?? 0) > 0 ? (
            <Badge severity="warning" size="sm">
              {resource.summaryContext?.issueCount} issues
            </Badge>
          ) : null}
        </div>
      ))}
    </div>
  );
}

const VISIBLE_ALERT_LABELS = 6;

function alertStateSeverity(state: string | undefined) {
  switch ((state ?? "").toLowerCase()) {
    case "firing":
      return "error" as const;
    case "pending":
      return "warning" as const;
    case "inactive":
      return "success" as const;
    default:
      return "neutral" as const;
  }
}

function AlertsBody({ data }: { data: EvidenceDataOf<"alerts"> }) {
  const { rule, instances, annotations } = data;
  const health = rule.health?.toLowerCase();
  // Target instances lead; the rest stay visible in producer order.
  const ordered = [...instances].sort(
    (left, right) => Number(right.namesTarget) - Number(left.namesTarget),
  );
  const annotationEntries = Object.entries(annotations);
  return (
    <div className="space-y-2.5">
      <div className="flex flex-wrap items-center gap-1.5">
        {rule.state ? (
          <Badge severity={alertStateSeverity(rule.state)} size="sm">
            {rule.state}
          </Badge>
        ) : null}
        {rule.labels.severity ? (
          <Badge severity={severityBadge(rule.labels.severity)} size="sm">
            severity {rule.labels.severity}
          </Badge>
        ) : null}
        <Badge tone="structural" size="sm">
          {rule.group}
        </Badge>
        {health && health !== "ok" ? (
          <Badge severity={health === "err" ? "error" : "neutral"} size="sm">
            health {health}
          </Badge>
        ) : null}
      </div>
      {ordered.length > 0 ? (
        <ol className="max-h-64 space-y-1.5 overflow-y-auto pr-1">
          {ordered.map((instance, index) => {
            const labels = Object.entries(instance.labels).filter(
              ([key]) => key !== "alertname" && key !== "severity",
            );
            const hidden = labels.length - VISIBLE_ALERT_LABELS;
            return (
              <li
                key={`${instance.state}-${index}-${labels.map(([k, v]) => `${k}=${v}`).join(",")}`}
                className="rounded-md border border-theme-border/70 bg-theme-base/30 px-2.5 py-2"
              >
                <div className="flex flex-wrap items-center gap-1.5">
                  <Badge
                    severity={alertStateSeverity(instance.state)}
                    size="sm"
                  >
                    {instance.state}
                  </Badge>
                  {instance.namesTarget ? (
                    <Badge severity="info" size="sm">
                      names this resource
                    </Badge>
                  ) : null}
                  {instance.value ? (
                    <span className="font-mono text-xs text-theme-text-secondary">
                      value {instance.value}
                    </span>
                  ) : null}
                  {instance.activeAt ? (
                    <Tooltip
                      content={new Date(instance.activeAt).toLocaleString()}
                      delay={150}
                      position="left"
                      wrapperClassName="ml-auto"
                    >
                      <time
                        dateTime={instance.activeAt}
                        className="text-xs text-theme-text-tertiary"
                      >
                        active {formatRelativeAgeTime(instance.activeAt)}
                      </time>
                    </Tooltip>
                  ) : null}
                </div>
                {labels.length > 0 ? (
                  <div className="mt-1.5 flex flex-wrap gap-1 font-mono text-[11px] text-theme-text-secondary">
                    {labels
                      .slice(0, VISIBLE_ALERT_LABELS)
                      .map(([key, value]) => (
                        <span
                          key={key}
                          className="rounded bg-theme-base px-1.5 py-0.5 [overflow-wrap:anywhere]"
                        >
                          {key}={value}
                        </span>
                      ))}
                    {hidden > 0 ? (
                      <span className="px-1 py-0.5 text-theme-text-tertiary">
                        +{hidden} more
                      </span>
                    ) : null}
                  </div>
                ) : null}
              </li>
            );
          })}
        </ol>
      ) : (
        <p className="text-xs italic text-theme-text-tertiary">
          No active instances were reported for this rule.
        </p>
      )}
      {annotationEntries.length > 0 ? (
        <dl className="space-y-1 text-xs">
          {annotationEntries.map(([key, value]) => (
            <div key={key} className="flex min-w-0 gap-2">
              <dt className="shrink-0 text-theme-text-tertiary">{key}</dt>
              <dd className="min-w-0 leading-relaxed text-theme-text-secondary [overflow-wrap:anywhere]">
                {value}
              </dd>
            </div>
          ))}
        </dl>
      ) : null}
      {rule.query ? (
        <TerminalBlock label="Rule expression">{rule.query}</TerminalBlock>
      ) : null}
    </div>
  );
}

function helmStatusSeverity(status: string) {
  const normalized = status.toLowerCase();
  if (normalized === "deployed") return "success" as const;
  if (normalized.includes("failed")) return "error" as const;
  if (normalized.startsWith("pending") || normalized === "uninstalling")
    return "info" as const;
  return "warning" as const;
}

function HelmBody({ data }: { data: EvidenceDataOf<"helm"> }) {
  const { onOpenResource } = useContext(EvidenceNavigationContext);
  const { release } = data;
  const operation = release.lastOperation;
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-1.5">
        <Badge severity={helmStatusSeverity(release.status)} size="sm">
          {release.status}
        </Badge>
        <Badge tone="structural" size="sm">
          {release.chart}
          {release.chartVersion ? ` ${release.chartVersion}` : ""}
        </Badge>
        <Badge tone="structural" size="sm">
          revision {release.revision}
        </Badge>
        {release.appVersion ? (
          <Badge tone="note" size="sm">
            app {release.appVersion}
          </Badge>
        ) : null}
        {release.resourceHealth ? (
          <Badge
            severity={
              mapHealthToTone(release.resourceHealth) === "healthy"
                ? "success"
                : mapHealthToTone(release.resourceHealth) === "unhealthy"
                  ? "error"
                  : "warning"
            }
            size="sm"
          >
            resources {release.resourceHealth}
          </Badge>
        ) : null}
        <Tooltip
          content={new Date(release.updated).toLocaleString()}
          delay={150}
          position="left"
          wrapperClassName="ml-auto"
        >
          <time
            dateTime={release.updated}
            className="text-xs text-theme-text-tertiary"
          >
            updated {formatRelativeAgeTime(release.updated)}
          </time>
        </Tooltip>
      </div>
      {release.healthIssue ? (
        <p className="text-xs leading-relaxed text-warning-text">
          {release.healthIssue}
        </p>
      ) : null}
      {release.healthSummary &&
      release.healthSummary !== release.healthIssue ? (
        <p className="text-xs leading-relaxed text-theme-text-secondary">
          {release.healthSummary}
        </p>
      ) : null}
      {operation ? (
        <div className="rounded-md border border-theme-border bg-theme-base/40 px-2.5 py-2">
          <div className="flex flex-wrap items-center gap-1.5">
            <span className="text-xs font-semibold uppercase tracking-wide text-theme-text-tertiary">
              Last operation
            </span>
            <Badge tone="note" size="sm">
              {operation.kind.replaceAll("_", " ")}
            </Badge>
            <Badge
              severity={
                operation.status === "completed"
                  ? "success"
                  : operation.status === "failed"
                    ? "error"
                    : "warning"
              }
              size="sm"
            >
              {operation.status.replaceAll("_", " ")}
            </Badge>
          </div>
          {operation.message ? (
            <p className="mt-1 text-xs leading-relaxed text-theme-text-secondary [overflow-wrap:anywhere]">
              {operation.message}
            </p>
          ) : null}
        </div>
      ) : null}
      {release.description ? (
        <p className="text-xs text-theme-text-tertiary [overflow-wrap:anywhere]">
          {release.description}
        </p>
      ) : null}
      {release.storageNamespace &&
      release.storageNamespace !== release.namespace ? (
        <p className="text-xs text-theme-text-tertiary">
          Release metadata stored in namespace {release.storageNamespace}
        </p>
      ) : null}
      {release.managedByFluxHelmRelease ? (
        <p className="text-xs text-theme-text-tertiary">
          Managed by Flux HelmRelease {release.managedByFluxHelmRelease}
        </p>
      ) : null}
      {release.resources.length > 0 ? (
        <div className="max-h-52 overflow-y-auto rounded-md border border-theme-border">
          {release.resources.map((owned, index) => (
            <div
              key={`${owned.kind}-${owned.namespace}-${owned.name}`}
              className={clsx(
                "flex min-w-0 items-center gap-2 px-2.5 py-1.5",
                index > 0 && "border-t border-theme-border/60",
              )}
            >
              <StatusDot
                tone={mapHealthToTone(
                  owned.issue ? "unhealthy" : (owned.status ?? ""),
                )}
                className="shrink-0"
              />
              <Badge tone="structural" size="sm">
                {owned.kind}
              </Badge>
              <span className="min-w-0 flex-1">
                <span className="block truncate font-mono text-xs">
                  <ResourceLink
                    name={owned.name}
                    kind={owned.kind}
                    namespace={owned.namespace}
                    group={
                      owned.apiVersion
                        ? apiVersionToGroup(owned.apiVersion)
                        : undefined
                    }
                    label={`${owned.namespace ? `${owned.namespace}/` : ""}${owned.name}`}
                    onNavigate={
                      owned.apiVersion && onOpenResource
                        ? (ref) => onOpenResource(ref)
                        : undefined
                    }
                  />
                </span>
                {owned.issue ? (
                  <span className="block truncate text-xs text-warning-text">
                    {owned.issue}
                  </span>
                ) : owned.summary || owned.message ? (
                  <span className="block truncate text-xs text-theme-text-tertiary">
                    {owned.summary || owned.message}
                  </span>
                ) : null}
              </span>
              {owned.ready || owned.status ? (
                <span className="ml-auto shrink-0 font-mono text-xs text-theme-text-tertiary">
                  {owned.ready || owned.status}
                </span>
              ) : null}
            </div>
          ))}
        </div>
      ) : null}
    </div>
  );
}

function PermissionsBody({ data }: { data: EvidenceDataOf<"permissions"> }) {
  const { subject, accessCheck } = data;
  const subjectBadges = (
    <>
      <Badge tone="structural" size="sm">
        {subject.kind}
      </Badge>
      <span className="font-mono text-xs text-theme-text-secondary">
        {subject.namespace ? `${subject.namespace}/` : ""}
        {subject.name}
      </span>
    </>
  );
  if (accessCheck) {
    const facts = [
      ["Verb", accessCheck.verb],
      ["Resource", accessCheck.resource],
      ["Subresource", accessCheck.subresource],
      ["API group", accessCheck.group || "core"],
      ["Namespace", accessCheck.namespace || "cluster-wide"],
      ["Name", accessCheck.resourceName],
    ] as const;
    return (
      <div className="space-y-2.5">
        <div className="flex flex-wrap items-center gap-1.5">
          {subjectBadges}
          <Badge
            severity={accessCheck.allowed ? "success" : "warning"}
            size="sm"
          >
            {accessCheck.allowed
              ? "allowed"
              : accessCheck.denied
                ? "denied"
                : "not allowed"}
          </Badge>
        </div>
        <dl className="flex flex-wrap gap-x-6 gap-y-2 text-xs">
          {facts.map(([label, value]) => (
            <ResourceFact key={label} label={label} value={value} />
          ))}
        </dl>
        {accessCheck.reason ? (
          <p className="text-xs leading-relaxed text-theme-text-secondary [overflow-wrap:anywhere]">
            {accessCheck.reason}
          </p>
        ) : null}
        {accessCheck.evaluationError ? (
          <p className="text-xs leading-relaxed text-semantic-error">
            {accessCheck.evaluationError}
          </p>
        ) : null}
      </div>
    );
  }
  const bindings = data.bindings ?? [];
  const usedByPods = data.usedByPods ?? [];
  return (
    <div className="space-y-2.5">
      <div className="flex flex-wrap items-center gap-1.5">
        {subjectBadges}
        <Badge tone="note" size="sm">
          {data.flatRulesCount ?? 0}
          {data.truncated ? "+" : ""} effective rules
        </Badge>
        {data.truncated ? (
          <Badge severity="warning" size="sm">
            rule list truncated
          </Badge>
        ) : null}
      </div>
      {bindings.length > 0 ? (
        <div className="max-h-52 overflow-y-auto rounded-md border border-theme-border">
          {bindings.map((binding, index) => (
            <div
              key={`${binding.bindingKind}-${binding.bindingNamespace ?? ""}-${binding.bindingName}`}
              className={clsx(
                "flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 px-2.5 py-1.5 text-xs",
                index > 0 && "border-t border-theme-border/60",
              )}
            >
              <Badge tone="structural" size="sm">
                {binding.bindingKind}
              </Badge>
              <span className="min-w-0 truncate font-mono text-theme-text-secondary">
                {binding.bindingNamespace ? `${binding.bindingNamespace}/` : ""}
                {binding.bindingName}
              </span>
              <span className="text-theme-text-tertiary">→</span>
              <Badge tone="structural" size="sm">
                {binding.roleKind}
              </Badge>
              <span className="min-w-0 truncate font-mono text-theme-text-primary">
                {binding.roleNamespace ? `${binding.roleNamespace}/` : ""}
                {binding.roleName}
              </span>
              <span className="ml-auto shrink-0 font-mono text-theme-text-tertiary">
                {binding.rulesCount} rule{binding.rulesCount === 1 ? "" : "s"}
              </span>
              {binding.inheritedFromGroup ? (
                <Badge tone="note" size="sm">
                  via {binding.inheritedFromGroup}
                </Badge>
              ) : null}
            </div>
          ))}
        </div>
      ) : (
        <p className="text-xs italic text-theme-text-tertiary">
          No RoleBinding or ClusterRoleBinding grants this subject anything.
        </p>
      )}
      {usedByPods.length > 0 ? (
        <p className="text-xs leading-relaxed text-theme-text-secondary [overflow-wrap:anywhere]">
          <span className="text-theme-text-tertiary">Used by pods: </span>
          <span className="font-mono">{usedByPods.join(", ")}</span>
          {data.podsTotal && data.podsTotal > usedByPods.length
            ? ` and ${data.podsTotal - usedByPods.length} more`
            : ""}
        </p>
      ) : null}
    </div>
  );
}

function RevisionHistory({
  observations,
  citedOrder,
  reveal,
  revealRequestId,
  onViewSource,
}: {
  observations: InvestigationEvidenceObservation[];
  citedOrder?: number;
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
            {observations.map((observation) => (
              <li
                key={`${observation.source.id}-${observation.revision}`}
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
                  <p>{observation.summary || observation.title}</p>
                  {evidenceHasDetails(
                    observation.data,
                    observation.summary,
                  ) && (
                    <EvidenceBody
                      data={observation.data}
                      cardSummary={observation.summary}
                      annotations={metricsMarkersBySource?.get(
                        observation.source.id,
                      )}
                    />
                  )}
                </div>
                <SourceButton
                  ariaLabel={`View source for ${phaseLabel(observation.source.phase).toLowerCase()} observation of ${observation.title}`}
                  onClick={() =>
                    onViewSource(
                      observation.source.id,
                      evidenceSourceExcerpt(observation.data),
                    )
                  }
                />
              </li>
            ))}
          </ol>
        </Collapse>
      </div>
    </div>
  );
}

function EvidenceCaveat({ data }: { data: InvestigationEvidenceData }) {
  let text: string | undefined;
  if (data.type === "events") {
    text =
      "Events support the timeline; proximity alone does not establish cause.";
  } else if (data.type === "changes") {
    return (
      <Tooltip
        content={`A change alone does not establish the cause.${data.changeContext?.when ? " The reported age is as of collection." : ""}`}
        position="left"
        className="pointer-events-none"
        wrapperClassName="flex shrink-0"
      >
        <button
          type="button"
          aria-label="About change evidence"
          className="flex h-7 w-7 items-center justify-center rounded-md text-theme-text-tertiary hover:bg-theme-hover hover:text-theme-text-secondary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/50"
        >
          <Info className="h-3.5 w-3.5" aria-hidden />
        </button>
      </Tooltip>
    );
  } else if (data.type === "relationships" || data.type === "topology") {
    text =
      "This shows direct relationships Radar found, not an inferred blast radius.";
  } else if (data.type === "alerts") {
    text =
      "Instances are matched to this investigation by their Prometheus labels; a firing rule alone does not establish the cause.";
  } else if (data.type === "permissions") {
    text =
      "This is what RBAC grants the subject, not what the workload has exercised.";
  }
  if (!text) return null;
  return (
    <p className="flex items-start gap-1.5 text-xs leading-relaxed text-theme-text-tertiary">
      <Info className="mt-0.5 h-3 w-3 shrink-0" aria-hidden />
      {text}
    </p>
  );
}

function EvidenceIcon({
  observation,
  prominence,
}: {
  observation: InvestigationEvidenceObservation;
  prominence: "primary" | "supporting" | "secondary";
}) {
  const Icon = evidenceIcon(observation.data.type);
  return (
    <span
      className={clsx(
        "flex shrink-0 items-center justify-center text-theme-text-tertiary",
        prominence === "primary" ? "h-7 w-7" : "h-6 w-6",
      )}
    >
      <Icon
        className={prominence === "primary" ? "h-4 w-4" : "h-3.5 w-3.5"}
        aria-hidden
      />
    </span>
  );
}

function SourceButton({
  ariaLabel,
  buttonLabel = "View source",
  compact = false,
  onClick,
}: {
  ariaLabel?: string;
  buttonLabel?: string;
  compact?: boolean;
  onClick: () => void;
}) {
  const tooltip = "Open the original tool result";
  return (
    <Tooltip
      content={tooltip}
      delay={350}
      position="left"
      wrapperClassName="flex shrink-0"
    >
      <button
        type="button"
        aria-label={ariaLabel ?? tooltip}
        onClick={onClick}
        className={clsx(
          "flex h-7 shrink-0 items-center justify-center rounded-md text-theme-text-tertiary hover:bg-theme-hover hover:text-accent-text focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/50",
          "gap-1 px-2 text-xs font-medium",
        )}
      >
        <FileSearch className="h-3.5 w-3.5" aria-hidden />
        <span
          className={compact ? "hidden @min-[540px]/card:inline" : undefined}
        >
          {buttonLabel}
        </span>
      </button>
    </Tooltip>
  );
}

function evidenceIcon(type: InvestigationEvidenceData["type"]) {
  switch (type) {
    case "issue":
      return CircleAlert;
    case "startup":
      return ShieldAlert;
    case "crash":
      return Bug;
    case "resource":
      return Boxes;
    case "logs":
      return ScrollText;
    case "events":
      return Clock3;
    case "changes":
      return FileClock;
    case "dns":
      return Activity;
    case "network":
      return Network;
    case "relationships":
    case "topology":
      return Network;
    case "inventory":
      return ListTree;
    case "receipt":
      return CheckCircle2;
    case "alerts":
      return BellRing;
    case "helm":
      return Package;
    case "permissions":
      return KeyRound;
    case "metrics":
      return ChartLine;
  }
}

function severityBadge(value: string) {
  const tone = value.toLowerCase();
  if (tone === "error" || tone === "critical" || tone === "failed")
    return "error" as const;
  if (tone === "alert" || tone === "high") return "alert" as const;
  if (tone === "warning" || tone === "medium") return "warning" as const;
  if (tone === "info" || tone === "low") return "info" as const;
  return "neutral" as const;
}

function phaseLabel(
  phase: InvestigationEvidenceObservation["source"]["phase"],
): string {
  switch (phase) {
    case "initial":
      return "Initial";
    case "followup":
      return "Follow-up";
    case "verification":
      return "Verification";
    case "apply":
      return "Apply";
  }
}

function toneBorder(
  tone: InvestigationEvidenceObservation["tone"],
  tier: InvestigationEvidenceTier,
  prominence: "primary" | "supporting" | "secondary",
): string {
  if (prominence !== "primary") return "border-theme-border/70";
  if (tier !== "key") return "border-theme-border";
  if (tone === "error")
    return "border-l-[3px] border-l-red-500 border-theme-border";
  if (tone === "alert")
    return "border-l-[3px] border-l-orange-500 border-theme-border";
  return "border-l-[3px] border-l-amber-500 border-theme-border";
}
