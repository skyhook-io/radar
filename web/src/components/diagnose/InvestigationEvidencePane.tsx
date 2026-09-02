import { useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { clsx } from "clsx";
import {
  Activity,
  AlertTriangle,
  Boxes,
  Bug,
  CheckCircle2,
  CircleAlert,
  Clock3,
  FileClock,
  Info,
  ListTree,
  Network,
  ScrollText,
  SearchCheck,
  ShieldAlert,
} from "lucide-react";
import {
  Badge,
  Collapse,
  CollapseChevron,
  DiffViewer,
  StatusDot,
  TerminalBlock,
  defaultConditionTone,
  formatRelativeAgeTime,
  mapHealthToTone,
  pluralize,
  stripAnsi,
} from "@skyhook-io/k8s-ui";

import {
  investigationEvidenceGroupWithoutSources,
  investigationEvidenceSourceDomId,
  type InvestigationEvidenceData,
  type InvestigationEvidenceGroup,
  type InvestigationEvidenceLimitation,
  type InvestigationEvidenceObservation,
  type InvestigationEvidenceProjection,
  type InvestigationRootCauseEvidenceResolution,
  type InvestigationEvidenceSource,
  type InvestigationEvidenceTier,
} from "./investigationEvidence";
import { InvestigationResourceEvidence } from "./InvestigationResourceEvidence";
import { investigationResourceEvidenceHasDetails } from "./investigationResourceEvidenceModel";
import { prettyTool } from "./parts";

// Shared Collapse uses a 200 ms grid-row transition. Keep a small paint margin
// before moving focus so the destination is stationary. Reduced-motion users get
// an immediate disclosure and focus hand-off because Collapse disables motion.
export const INVESTIGATION_DISCLOSURE_SETTLE_MS = 220;
export const VISIBLE_ADDITIONAL_KEY_EVIDENCE = 2;
export const VISIBLE_SUPPORTING_EVIDENCE = 4;
export const VISIBLE_LOG_EVIDENCE_LINES = 12;

function prefersReducedMotion(): boolean {
  return (
    typeof window !== "undefined" &&
    typeof window.matchMedia === "function" &&
    window.matchMedia("(prefers-reduced-motion: reduce)").matches
  );
}

export function investigationDisclosureSettleDelay(
  reducedMotion: boolean,
): number {
  return reducedMotion ? 0 : INVESTIGATION_DISCLOSURE_SETTLE_MS;
}

export function investigationEvidenceRevealCollection(
  projection: InvestigationEvidenceProjection,
  sourceId: string,
):
  | "more-key"
  | "more-supporting"
  | "earlier"
  | "context"
  | "coverage"
  | undefined {
  const source = projection.sources.find((item) => item.id === sourceId);
  if (source?.primaryGroupId) {
    const primaryGroup = projection.groups.find(
      (group) => group.id === source.primaryGroupId,
    );
    if (primaryGroup?.historical) return "earlier";
    if (primaryGroup?.latest.tier === "context") return "context";
    if (primaryGroup?.latest.tier === "key") {
      const keyIndex = projection.groups
        .filter((group) => !group.historical && group.latest.tier === "key")
        .findIndex((group) => group.id === primaryGroup.id);
      if (keyIndex > VISIBLE_ADDITIONAL_KEY_EVIDENCE) return "more-key";
    }
    if (primaryGroup?.latest.tier === "supporting") {
      const supportingIndex = projection.groups
        .filter(
          (group) => !group.historical && group.latest.tier === "supporting",
        )
        .findIndex((group) => group.id === primaryGroup.id);
      if (supportingIndex >= VISIBLE_SUPPORTING_EVIDENCE)
        return "more-supporting";
    }
    // One bundled tool call can fan out into several semantic groups. Its sole
    // DOM anchor lives on the ranked primary group, so secondary Context or
    // Earlier observations must not redirect disclosure navigation elsewhere.
    return undefined;
  }
  if (
    projection.limitations.some((limitation) =>
      limitation.sources.some((source) => source.id === sourceId),
    )
  ) {
    return "coverage";
  }
  return undefined;
}

export function InvestigationEvidencePane({
  projection,
  rootCauseEvidence,
  collecting,
  animateGroupIds,
  onViewSource,
  afterMaterialEvidence,
  revealRequest,
  onRevealReady,
}: {
  projection: InvestigationEvidenceProjection;
  /** Server-validated links for the current root cause; absent without one. */
  rootCauseEvidence?: InvestigationRootCauseEvidenceResolution;
  collecting: boolean;
  animateGroupIds: ReadonlySet<string>;
  onViewSource: (sourceId: string) => void;
  /** Keeps the workspace story ordered: assessment → key evidence/limits → action. */
  afterMaterialEvidence?: ReactNode;
  /** Explicit Activity → Findings navigation, including repeat clicks. */
  revealRequest?: { sourceId: string; requestId: number };
  onRevealReady?: (sourceId: string) => void;
}) {
  const [coverageOpen, setCoverageOpen] = useState(false);
  const [moreKeyOpen, setMoreKeyOpen] = useState(false);
  const [moreSupportingOpen, setMoreSupportingOpen] = useState(false);
  const [earlierOpen, setEarlierOpen] = useState(false);
  const [contextOpen, setContextOpen] = useState(false);
  const handledRevealRequestRef = useRef<number | undefined>(undefined);
  const openingForRevealRequestRef = useRef<number | undefined>(undefined);
  const tiers = new Map<
    InvestigationEvidenceTier,
    InvestigationEvidenceGroup[]
  >([
    ["key", []],
    ["supporting", []],
    ["context", []],
    ["checked", []],
  ]);
  const promotedSourceIds = new Set(
    rootCauseEvidence?.links.map((link) => link.source.id) ?? [],
  );
  const promotedSourcesByGroup = new Map<string, Set<string>>();
  for (const link of rootCauseEvidence?.links ?? []) {
    if (!link.originalGroupId) continue;
    const sourceIds =
      promotedSourcesByGroup.get(link.originalGroupId) ?? new Set();
    sourceIds.add(link.source.id);
    promotedSourcesByGroup.set(link.originalGroupId, sourceIds);
  }
  const historical: InvestigationEvidenceGroup[] = [];
  const ordinaryGroups: InvestigationEvidenceGroup[] = [];
  const relocatedSourceIds = new Set<string>();
  for (const group of projection.groups) {
    const excludedSources = promotedSourcesByGroup.get(group.id);
    const ordinaryGroup = excludedSources
      ? investigationEvidenceGroupWithoutSources(group, excludedSources)
      : group;
    if (!ordinaryGroup) {
      // A terminal assessment can promote an already-rendered card without a
      // new evidence revision. Retain that card's DOM id at its new location so
      // InvestigationView's existing layout anchor can keep the reader's scroll
      // position stable. Shared groups are split rather than relocated and keep
      // separate ids to avoid duplicate DOM anchors.
      // A bundled check can theoretically cite multiple revisions that shared
      // one card. Only one destination may inherit the old id.
      const owner = rootCauseEvidence?.links.find(
        (link) => link.originalGroupId === group.id,
      );
      if (owner) relocatedSourceIds.add(owner.source.id);
      continue;
    }
    ordinaryGroups.push(ordinaryGroup);
    if (ordinaryGroup.historical) historical.push(ordinaryGroup);
    else tiers.get(ordinaryGroup.latest.tier)!.push(ordinaryGroup);
  }
  const hasCurrentEvidence =
    (rootCauseEvidence?.links.length ?? 0) > 0 ||
    projection.groups.some((group) => !group.historical);
  const keyGroups = tiers.get("key")!;
  const primaryKeyGroups = keyGroups.slice(0, 1);
  const additionalKeyGroups = keyGroups.slice(
    1,
    1 + VISIBLE_ADDITIONAL_KEY_EVIDENCE,
  );
  const overflowKeyGroups = keyGroups.slice(
    1 + VISIBLE_ADDITIONAL_KEY_EVIDENCE,
  );
  const supportingGroups = tiers.get("supporting")!;
  const visibleSupportingGroups = supportingGroups.slice(
    0,
    VISIBLE_SUPPORTING_EVIDENCE,
  );
  const overflowSupportingGroups = supportingGroups.slice(
    VISIBLE_SUPPORTING_EVIDENCE,
  );
  const revealCollection = revealRequest
    ? promotedSourceIds.has(revealRequest.sourceId)
      ? undefined
      : investigationEvidenceRevealCollection(
          { ...projection, groups: ordinaryGroups },
          revealRequest.sourceId,
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
    if (revealCollection === "earlier" && !earlierOpen) {
      openingForRevealRequestRef.current = requestId;
      setEarlierOpen(true);
      return;
    }
    if (revealCollection === "context" && !contextOpen) {
      openingForRevealRequestRef.current = requestId;
      setContextOpen(true);
      return;
    }
    if (revealCollection === "coverage" && !coverageOpen) {
      openingForRevealRequestRef.current = requestId;
      setCoverageOpen(true);
      return;
    }
    if (revealCollection === "more-key" && !moreKeyOpen) {
      openingForRevealRequestRef.current = requestId;
      setMoreKeyOpen(true);
      return;
    }
    if (revealCollection === "more-supporting" && !moreSupportingOpen) {
      openingForRevealRequestRef.current = requestId;
      setMoreSupportingOpen(true);
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
    contextOpen,
    coverageOpen,
    moreKeyOpen,
    moreSupportingOpen,
  ]);

  return (
    <section
      aria-labelledby="investigation-radar-evidence"
      className="@container/evidence space-y-3"
    >
      <div className="flex min-w-0 items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <h2
              id="investigation-radar-evidence"
              className="text-sm font-semibold text-theme-text-primary"
            >
              {rootCauseEvidence?.status === "linked"
                ? "Evidence cited by assessment"
                : "Radar evidence"}
            </h2>
            {collecting ? (
              <span className="inline-flex items-center gap-1.5 text-[11px] text-accent-text">
                <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-accent" />
                collecting
              </span>
            ) : null}
          </div>
          <p className="mt-0.5 text-xs leading-relaxed text-theme-text-tertiary">
            {rootCauseEvidence?.status === "linked"
              ? "Exact successful Radar checks selected by the agent to support its assessment."
              : "Structured observations from completed Radar tool calls in this run."}
          </p>
        </div>
      </div>

      <div className="space-y-4">
        {rootCauseEvidence ? (
          <RootCauseEvidenceLinks
            resolution={rootCauseEvidence}
            relocatedSourceIds={relocatedSourceIds}
            animateGroupIds={animateGroupIds}
            onViewSource={onViewSource}
          />
        ) : null}

        {rootCauseEvidence?.status === "linked" &&
        (ordinaryGroups.length > 0 || projection.limitations.length > 0) ? (
          <div className="border-t border-theme-border/60 pt-3">
            <h3 className="text-xs font-semibold uppercase tracking-wide text-theme-text-secondary">
              Other Radar observations
            </h3>
            <p className="mt-0.5 text-[11px] leading-relaxed text-theme-text-tertiary">
              Additional captured signals; these were not cited as the basis for
              the assessment above.
            </p>
          </div>
        ) : null}

        <EvidenceTier
          headingId="investigation-observed-failure-heading"
          tier="key"
          title="Observed failure"
          description="The first failure signal Radar captured during this run."
          groups={primaryKeyGroups}
          animateGroupIds={animateGroupIds}
          onViewSource={onViewSource}
        />
        <EvidenceTier
          headingId="investigation-other-key-evidence-heading"
          tier="key"
          title="Other key evidence"
          description="Additional failure signals captured during this run."
          groups={additionalKeyGroups}
          expandFirst={false}
          animateGroupIds={animateGroupIds}
          onViewSource={onViewSource}
        />
        <CollapsedEvidenceCollection
          id="investigation-more-key-evidence"
          title="More key evidence"
          description="Additional failure signals, collapsed to keep the assessment scannable."
          groups={overflowKeyGroups}
          animateGroupIds={animateGroupIds}
          onViewSource={onViewSource}
          open={moreKeyOpen}
          onOpenChange={setMoreKeyOpen}
        />

        {projection.limitations.length > 0 ? (
          <CoverageStrip
            limitations={projection.limitations}
            coverage={projection.coverage}
            excludedSourceIds={promotedSourceIds}
            onViewSource={onViewSource}
            open={coverageOpen}
            onOpenChange={setCoverageOpen}
          />
        ) : null}

        {!hasCurrentEvidence ? (
          <EmptyCollection
            collecting={collecting}
            hasEarlierEvidence={historical.length > 0}
          />
        ) : null}

        {afterMaterialEvidence}

        <EvidenceTier
          headingId="investigation-supporting-evidence-heading"
          tier="supporting"
          title="Supporting evidence"
          description="State and signals that support the assessment without proving the cause alone."
          groups={visibleSupportingGroups}
          animateGroupIds={animateGroupIds}
          onViewSource={onViewSource}
        />
        <CollapsedEvidenceCollection
          id="investigation-more-supporting-evidence"
          title="More supporting evidence"
          description="Additional corroborating signals, available without extending the main story."
          groups={overflowSupportingGroups}
          animateGroupIds={animateGroupIds}
          onViewSource={onViewSource}
          open={moreSupportingOpen}
          onOpenChange={setMoreSupportingOpen}
        />

        <CheckedReceipts
          groups={tiers.get("checked")!}
          animateGroupIds={animateGroupIds}
          onViewSource={onViewSource}
        />
        <CollapsedEvidenceCollection
          id="investigation-earlier-evidence"
          title="Earlier evidence"
          description="Observed before a later verification. Kept for comparison; this does not mean it was resolved."
          groups={historical}
          animateGroupIds={animateGroupIds}
          onViewSource={onViewSource}
          open={earlierOpen}
          onOpenChange={setEarlierOpen}
        />
        <CollapsedEvidenceCollection
          id="investigation-context-evidence"
          title="Context"
          description="Orientation, broader-scope signals, and direct relationships kept secondary to the finding."
          groups={tiers.get("context")!}
          animateGroupIds={animateGroupIds}
          onViewSource={onViewSource}
          open={contextOpen}
          onOpenChange={setContextOpen}
        />
      </div>
    </section>
  );
}

function RootCauseEvidenceLinks({
  resolution,
  relocatedSourceIds,
  animateGroupIds,
  onViewSource,
}: {
  resolution: InvestigationRootCauseEvidenceResolution;
  relocatedSourceIds: ReadonlySet<string>;
  animateGroupIds: ReadonlySet<string>;
  onViewSource: (sourceId: string) => void;
}) {
  if (resolution.status !== "linked") {
    return (
      <div className="flex items-start gap-2 rounded-lg border border-amber-500/30 bg-amber-500/5 px-3 py-2.5">
        <AlertTriangle
          className="mt-0.5 h-4 w-4 shrink-0 text-amber-500"
          aria-hidden
        />
        <div className="min-w-0">
          <p className="text-xs font-medium text-theme-text-primary">
            {resolution.status === "invalid"
              ? "Cited checks could not be validated"
              : "Assessment is not linked to specific checks"}
          </p>
          <p className="mt-0.5 text-[11px] leading-relaxed text-theme-text-tertiary">
            {resolution.status === "invalid"
              ? "Radar did not promote those references as evidence. Review the Activity record before acting."
              : "Review the Activity record and Radar observations below before acting on the agent’s conclusion."}
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-2.5">
      {resolution.links.map((link, index) => (
        <div key={link.source.id} className="space-y-1.5">
          <div className="flex min-w-0 items-center justify-between gap-2 px-0.5">
            <Badge severity="neutral" size="sm">
              Agent-selected check {index + 1}
            </Badge>
            {link.additionalGroupCount > 0 ? (
              <span className="truncate text-[10px] text-theme-text-tertiary">
                +{pluralize(link.additionalGroupCount, "other observation")}{" "}
                from this check below
              </span>
            ) : null}
          </div>
          {link.group ? (
            <EvidenceCard
              group={link.group}
              domId={
                link.originalGroupId && relocatedSourceIds.has(link.source.id)
                  ? link.originalGroupId
                  : undefined
              }
              initiallyOpen={index === 0}
              animateArrival={
                Boolean(link.originalGroupId) &&
                animateGroupIds.has(link.originalGroupId!)
              }
              onViewSource={onViewSource}
            />
          ) : (
            <div
              id={investigationEvidenceSourceDomId(link.source.id)}
              data-evidence-card
              tabIndex={-1}
              className="flex min-w-0 items-center gap-2 rounded-lg border border-emerald-500/25 bg-theme-surface px-3 py-2.5 outline-none focus:ring-2 focus:ring-accent/50"
            >
              <CheckCircle2
                className="h-4 w-4 shrink-0 text-emerald-500"
                aria-hidden
              />
              <span className="min-w-0 flex-1">
                <span className="block text-xs font-medium text-theme-text-primary">
                  {prettyTool(link.source.tool)}
                </span>
                <span className="block truncate text-[11px] text-theme-text-tertiary">
                  Successful Radar check
                </span>
              </span>
              <SourceButton
                label={link.source.tool}
                ariaLabel={`View cited ${link.source.tool} result in Activity`}
                onClick={() => onViewSource(link.source.id)}
              />
            </div>
          )}
        </div>
      ))}
    </div>
  );
}

function EmptyCollection({
  collecting,
  hasEarlierEvidence,
}: {
  collecting: boolean;
  hasEarlierEvidence: boolean;
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
          ? "Completed checks will appear here"
          : hasEarlierEvidence
            ? "No current evidence was captured during verification"
            : "No structured evidence was collected"}
      </p>
      <p className="mx-auto mt-1 max-w-md text-xs leading-relaxed text-theme-text-tertiary">
        {collecting
          ? "The Activity pane remains the live record while the agent investigates."
          : hasEarlierEvidence
            ? "Earlier observations remain below. This does not prove that those conditions resolved."
            : "The raw tool activity is still available. This does not establish that the resource is healthy."}
      </p>
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
}: {
  id: string;
  title: string;
  description: string;
  groups: InvestigationEvidenceGroup[];
  animateGroupIds: ReadonlySet<string>;
  onViewSource: (sourceId: string) => void;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  if (groups.length === 0) return null;
  return (
    <section className="overflow-hidden rounded-lg border border-theme-border bg-theme-base/25">
      <button
        type="button"
        aria-expanded={open}
        aria-controls={id}
        onClick={() => onOpenChange(!open)}
        className="flex w-full min-w-0 items-center gap-2 px-3 py-2.5 text-left hover:bg-theme-hover/60"
      >
        <CollapseChevron open={open} className="h-4 w-4" />
        <span className="min-w-0 flex-1">
          <span className="block text-xs font-semibold uppercase tracking-wide text-theme-text-secondary">
            {title}
          </span>
          <span className="block truncate text-[11px] text-theme-text-tertiary">
            {description}
          </span>
        </span>
        <Badge severity="neutral" size="sm">
          {groups.length}
        </Badge>
      </button>
      <div id={id}>
        <Collapse open={open}>
          <div className="grid gap-2 border-t border-theme-border/60 p-2.5 @min-[760px]/evidence:grid-cols-2">
            {groups.map((group) => (
              <EvidenceCard
                key={group.id}
                group={group}
                initiallyOpen={false}
                animateArrival={animateGroupIds.has(group.id)}
                onViewSource={onViewSource}
              />
            ))}
          </div>
        </Collapse>
      </div>
    </section>
  );
}

function CoverageStrip({
  limitations,
  coverage,
  excludedSourceIds,
  onViewSource,
  open,
  onOpenChange,
}: {
  limitations: InvestigationEvidenceLimitation[];
  coverage: InvestigationEvidenceProjection["coverage"];
  /** Sources promoted above already own the page's sole navigation anchor. */
  excludedSourceIds: ReadonlySet<string>;
  onViewSource: (sourceId: string) => void;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const hasError = limitations.some((item) => item.kind === "error");
  const regionId = "investigation-evidence-coverage";
  const anchorLimitationBySource = new Map<string, number>();
  limitations.forEach((limitation, index) => {
    for (const source of limitation.sources) {
      if (!anchorLimitationBySource.has(source.id)) {
        anchorLimitationBySource.set(source.id, index);
      }
    }
  });
  return (
    <div
      className={clsx(
        "overflow-hidden rounded-lg border",
        hasError
          ? "border-red-500/35 bg-red-500/5"
          : "border-amber-500/35 bg-amber-500/5",
      )}
    >
      <span className="sr-only" role="status" aria-live="polite">
        Evidence coverage has {pluralize(limitations.length, "limit")}.
      </span>
      <button
        type="button"
        aria-expanded={open}
        aria-controls={regionId}
        onClick={() => onOpenChange(!open)}
        className="flex w-full min-w-0 items-center gap-2 px-3 py-2 text-left hover:bg-theme-hover/50"
      >
        {hasError ? (
          <CircleAlert className="h-4 w-4 shrink-0 text-red-400" aria-hidden />
        ) : (
          <AlertTriangle
            className="h-4 w-4 shrink-0 text-amber-500"
            aria-hidden
          />
        )}
        <span className="min-w-0 flex-1">
          <span className="block text-xs font-semibold text-theme-text-primary">
            Evidence coverage has {pluralize(limitations.length, "limit")}
          </span>
          <span className="block truncate text-[11px] text-theme-text-tertiary">
            {pluralize(coverage.limited, "check")} incomplete · raw results
            remain in Activity
          </span>
        </span>
        <Badge severity={hasError ? "error" : "warning"} size="sm">
          {limitations.length}
        </Badge>
        <CollapseChevron open={open} className="h-4 w-4" />
      </button>
      <div id={regionId}>
        <Collapse open={open}>
          <ul className="space-y-2 border-t border-theme-border/60 px-3 py-2.5">
            {limitations.map((limitation, index) => (
              <li
                key={`${limitation.kind}-${limitation.source}-${limitation.message}-${index}`}
                data-evidence-source-container
                tabIndex={-1}
                className="flex min-w-0 items-start gap-2 rounded-md text-xs outline-none focus:relative focus:z-10 focus:ring-2 focus:ring-accent/40"
              >
                {limitation.sources
                  .filter(
                    (source) =>
                      !source.primaryGroupId &&
                      !excludedSourceIds.has(source.id) &&
                      anchorLimitationBySource.get(source.id) === index,
                  )
                  .map((source) => (
                    <span
                      key={source.id}
                      id={investigationEvidenceSourceDomId(source.id)}
                      className="sr-only scroll-mt-14"
                      aria-hidden
                    />
                  ))}
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
                {limitation.sources.length > 1 ? (
                  <span className="shrink-0 font-mono text-[10px] text-theme-text-tertiary">
                    {limitation.sources.length} checks
                  </span>
                ) : null}
                {limitation.sources.at(-1) ? (
                  <SourceButton
                    label={limitation.sources.at(-1)!.tool}
                    ariaLabel={`View Activity source for ${limitation.source}`}
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

function EvidenceTier({
  headingId,
  tier,
  title,
  description,
  groups,
  expandFirst = true,
  animateGroupIds,
  onViewSource,
}: {
  headingId: string;
  tier: Exclude<InvestigationEvidenceTier, "checked">;
  title: string;
  description: string;
  groups: InvestigationEvidenceGroup[];
  expandFirst?: boolean;
  animateGroupIds: ReadonlySet<string>;
  onViewSource: (sourceId: string) => void;
}) {
  if (groups.length === 0) return null;
  return (
    <section aria-labelledby={headingId}>
      <div className="mb-2 flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h3
            id={headingId}
            className="text-xs font-semibold uppercase tracking-wide text-theme-text-secondary"
          >
            {title}
          </h3>
          <p className="mt-0.5 text-[11px] leading-relaxed text-theme-text-tertiary">
            {description}
          </p>
        </div>
        <span className="font-mono text-[11px] text-theme-text-tertiary">
          {groups.length}
        </span>
      </div>
      <div
        className={clsx(
          "grid gap-2.5",
          tier === "supporting" && "@min-[760px]/evidence:grid-cols-2",
        )}
      >
        {groups.map((group, index) => (
          <EvidenceCard
            key={group.id}
            group={group}
            initiallyOpen={
              (tier === "key" && expandFirst && index === 0) ||
              (tier === "supporting" && index === 0)
            }
            animateArrival={animateGroupIds.has(group.id)}
            onViewSource={onViewSource}
          />
        ))}
      </div>
    </section>
  );
}

function EvidenceCard({
  group,
  domId = group.id,
  initiallyOpen,
  animateArrival,
  onViewSource,
}: {
  group: InvestigationEvidenceGroup;
  /** Stable layout/scroll identity when an existing card changes section. */
  domId?: string;
  initiallyOpen: boolean;
  animateArrival: boolean;
  onViewSource: (sourceId: string) => void;
}) {
  const [open, setOpen] = useState(initiallyOpen);
  const observation = group.latest;
  const newerLowerProvenanceObservation =
    group.chronologicalLatest.revision > observation.revision
      ? group.chronologicalLatest
      : undefined;
  const bodyId = `${domId}-body`;
  const canExpand = evidenceHasDetails(observation.data);
  const wide =
    observation.data.type === "logs" || observation.data.type === "events";
  const primarySources = uniquePrimarySources(group);
  const headerContent = (
    <>
      <EvidenceIcon observation={observation} />
      <span className="min-w-0 flex-1">
        <span className="flex flex-wrap items-center gap-1.5">
          <span className="text-sm font-semibold leading-snug text-theme-text-primary">
            {observation.title}
          </span>
          <EvidenceToneBadge observation={observation} />
          {observation.relevance === "broader" ? (
            <Badge severity="neutral" size="sm">
              Broader context
            </Badge>
          ) : null}
          {observation.relevance === "producer-related" &&
          (observation.tier === "key" || observation.tier === "supporting") ? (
            <Badge severity="neutral" size="sm">
              Related resource
            </Badge>
          ) : null}
          {group.observations.length > 1 ? (
            <Badge severity="neutral" size="sm">
              {group.observations.length} checks
            </Badge>
          ) : null}
          {newerLowerProvenanceObservation ? (
            <Badge severity="neutral" size="sm">
              Later broader check
            </Badge>
          ) : null}
          {observation.changedFromPrevious ? (
            <Badge tone="note" size="sm">
              changed
            </Badge>
          ) : null}
        </span>
        {observation.summary ? (
          <span
            className={clsx(
              "mt-0.5 block text-xs leading-relaxed text-theme-text-secondary",
              !open && "line-clamp-2",
            )}
          >
            {observation.summary}
          </span>
        ) : null}
        {newerLowerProvenanceObservation?.changedFromPrevious ? (
          <span className="mt-1 block line-clamp-2 text-[11px] leading-relaxed text-theme-text-tertiary">
            Later broader observation:{" "}
            {newerLowerProvenanceObservation.summary ||
              newerLowerProvenanceObservation.title}
          </span>
        ) : null}
      </span>
      {canExpand ? (
        <CollapseChevron open={open} className="mt-0.5 h-4 w-4" />
      ) : null}
    </>
  );
  return (
    <article
      id={domId}
      data-evidence-card
      tabIndex={-1}
      className={clsx(
        "scroll-mt-14 overflow-hidden rounded-lg border bg-theme-surface outline-none focus:ring-2 focus:ring-accent/50",
        toneBorder(observation.tone, observation.tier),
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
            onClick={() => setOpen((value) => !value)}
            className="flex min-w-0 flex-1 items-start gap-2.5 px-3 py-2.5 text-left hover:bg-theme-hover/60"
          >
            {headerContent}
          </button>
        ) : (
          <div className="flex min-w-0 flex-1 items-start gap-2.5 px-3 py-2.5 text-left">
            {headerContent}
          </div>
        )}
        <div className="flex shrink-0 items-center border-l border-theme-border/60 px-2">
          <SourceButton
            label={observation.source.tool}
            ariaLabel={`View Activity source for ${observation.title}`}
            onClick={() => onViewSource(observation.source.id)}
          />
        </div>
      </div>
      {canExpand ? (
        <div id={bodyId}>
          <Collapse open={open}>
            <div className="space-y-3 border-t border-theme-border/60 px-3 py-3">
              <EvidenceBody
                data={observation.data}
                cardSummary={observation.summary}
              />
              {group.observations.length > 1 ? (
                <RevisionHistory
                  observations={group.observations}
                  onViewSource={onViewSource}
                />
              ) : null}
              <EvidenceCaveat data={observation.data} />
            </div>
          </Collapse>
        </div>
      ) : null}
    </article>
  );
}

function CheckedReceipts({
  groups,
  animateGroupIds,
  onViewSource,
}: {
  groups: InvestigationEvidenceGroup[];
  animateGroupIds: ReadonlySet<string>;
  onViewSource: (sourceId: string) => void;
}) {
  if (groups.length === 0) return null;
  return (
    <section aria-labelledby="investigation-checked-heading">
      <div className="mb-2 flex items-center justify-between gap-2">
        <div>
          <h3
            id="investigation-checked-heading"
            className="text-xs font-semibold uppercase tracking-wide text-theme-text-secondary"
          >
            Checked
          </h3>
          <p className="mt-0.5 text-[11px] text-theme-text-tertiary">
            Confirmed successful checks with a meaningful empty result.
          </p>
        </div>
        <span className="font-mono text-[11px] text-theme-text-tertiary">
          {groups.length}
        </span>
      </div>
      <div className="overflow-hidden rounded-lg border border-theme-border bg-theme-base/35">
        {groups.map((group, index) => {
          const item = group.latest;
          const primarySources = uniquePrimarySources(group);
          return (
            <div
              key={group.id}
              id={group.id}
              data-evidence-card
              tabIndex={-1}
              className={clsx(
                "flex min-w-0 items-center gap-2 px-3 py-2 outline-none focus:ring-2 focus:ring-inset focus:ring-accent/50",
                index > 0 && "border-t border-theme-border/60",
                animateGroupIds.has(group.id) && "animate-transcript-enter",
              )}
            >
              {primarySources.map((source) => (
                <span
                  key={source.id}
                  id={investigationEvidenceSourceDomId(source.id)}
                  aria-hidden
                />
              ))}
              <CheckCircle2
                className="h-4 w-4 shrink-0 text-emerald-500"
                aria-hidden
              />
              <span className="min-w-0 flex-1">
                <span className="block text-xs font-medium text-theme-text-primary">
                  {item.title}
                </span>
                <span className="block truncate text-[11px] text-theme-text-tertiary">
                  {item.summary}
                </span>
              </span>
              {group.observations.length > 1 ? (
                <Badge severity="neutral" size="sm">
                  {group.observations.length}×
                </Badge>
              ) : null}
              <SourceButton
                label={item.source.tool}
                ariaLabel={`View Activity source for ${item.title}`}
                onClick={() => onViewSource(item.source.id)}
              />
            </div>
          );
        })}
      </div>
    </section>
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
}: {
  data: InvestigationEvidenceData;
  cardSummary?: string;
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
  }
}

function evidenceHasDetails(data: InvestigationEvidenceData): boolean {
  switch (data.type) {
    case "receipt":
      return false;
    case "resource": {
      const replicas = data.resourceContext?.workloadSummary?.replicas;
      return Boolean(
        investigationResourceEvidenceHasDetails(data.resource) ||
        replicas?.desired !== undefined ||
        data.resourceContext?.statusSummary?.conditions?.length ||
        data.gitOpsDiagnosis ||
        data.warnings.length,
      );
    }
    case "logs":
      return (data.logs?.lines?.length ?? 0) > 0 || Boolean(data.error);
    case "changes":
      return data.changes.length > 0 || Boolean(data.changeContext?.changed);
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
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap gap-1.5">
        <Badge
          severity={issue.severity === "critical" ? "error" : "warning"}
          size="sm"
        >
          {issue.severity}
        </Badge>
        <Badge kind={issue.kind} size="sm">
          {issue.kind}
        </Badge>
        <Badge tone="structural" size="sm">
          {issue.namespace ? `${issue.namespace}/` : ""}
          {issue.name}
        </Badge>
      </div>
      {showCause ? (
        <p className="text-sm font-medium leading-relaxed text-theme-text-primary">
          {issue.cause}
        </p>
      ) : null}
      {issue.message ? (
        <p className="text-xs leading-relaxed text-theme-text-secondary">
          {issue.message}
        </p>
      ) : null}
      {issue.action ? (
        <p className="border-l-2 border-accent/50 pl-2 text-xs leading-relaxed text-theme-text-secondary">
          Suggested check: {issue.action}
        </p>
      ) : null}
    </div>
  );
}

function StartupBody({ data }: { data: EvidenceDataOf<"startup"> }) {
  const blocker = data.blocker;
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap gap-1.5">
        <Badge kind={blocker.kind} size="sm">
          {blocker.kind}
        </Badge>
        <Badge tone="structural" size="sm">
          {blocker.name}
        </Badge>
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
  const conditions = data.resourceContext?.statusSummary?.conditions ?? [];
  const desired = replicas?.desired;
  const ready = replicas ? (replicas.ready ?? 0) : undefined;
  const percentage =
    desired && ready !== undefined
      ? Math.min(100, Math.round((ready / desired) * 100))
      : 0;
  const shortfall =
    desired !== undefined && ready !== undefined && ready < desired;
  return (
    <div className="space-y-3">
      <InvestigationResourceEvidence resource={data.resource} />
      {data.gitOpsDiagnosis ? (
        <GitOpsStatusBody status={data.gitOpsDiagnosis} />
      ) : null}
      {desired !== undefined && ready !== undefined ? (
        <div className="rounded-md border border-theme-border bg-theme-base/40 p-2.5">
          <div className="flex items-center justify-between gap-3 text-xs">
            <span className="font-medium text-theme-text-secondary">
              Ready replicas
            </span>
            <span
              className={clsx(
                "font-mono font-semibold tabular-nums",
                shortfall ? "text-warning-text" : "text-theme-text-primary",
              )}
            >
              {ready}/{desired}
            </span>
          </div>
          <div className="mt-2 h-1.5 overflow-hidden rounded-full bg-theme-hover">
            <div
              className={clsx(
                "h-full rounded-full",
                shortfall ? "bg-amber-500" : "bg-theme-text-tertiary/60",
              )}
              style={{ width: `${percentage}%` }}
            />
          </div>
          <dl className="mt-2 grid grid-cols-2 gap-x-4 gap-y-1.5 text-[11px] @min-[560px]/evidence:grid-cols-4">
            <ResourceFact label="Available" value={replicas?.available} />
            <ResourceFact label="Updated" value={replicas?.updated} />
            <ResourceFact label="Unavailable" value={replicas?.unavailable} />
            <ResourceFact
              label="Phase"
              value={data.resourceContext?.statusSummary?.phase}
            />
          </dl>
        </div>
      ) : null}
      {conditions.length > 0 ? (
        <div>
          <div className="mb-1.5 text-[10px] font-semibold uppercase tracking-wide text-theme-text-tertiary">
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
        <span className="text-[10px] font-semibold uppercase tracking-wide text-theme-text-tertiary">
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
            raw-tail fallback
          </Badge>
        ) : null}
        {data.logs ? (
          <span className="text-[11px] text-theme-text-tertiary">
            {data.logs.matchedLines} matched · {data.logs.totalLines} scanned
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
      <p className="mb-2 text-[11px] text-theme-text-tertiary">{data.scope}</p>
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
                  {event.count > 1 ? ` ×${event.count}` : ""}
                </span>
                <time
                  dateTime={event.lastTimestamp}
                  title={event.lastTimestamp}
                  className="text-[11px] text-theme-text-tertiary"
                >
                  {formatRelativeAgeTime(event.lastTimestamp)}
                </time>
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
  return (
    <div className="space-y-2.5">
      {data.changeContext?.changed ? (
        <p className="rounded-md border border-theme-border bg-theme-base/40 px-2.5 py-2 text-xs text-theme-text-secondary">
          <span className="font-medium text-theme-text-primary">
            Radar correlation:
          </span>{" "}
          {data.changeContext.what || "A related workload change was observed."}
          {data.changeContext.when ? ` · ${data.changeContext.when}` : ""}
          {data.changeContext.evidence
            ? ` — ${data.changeContext.evidence}`
            : ""}
        </p>
      ) : null}
      {data.changes.map((change, index) => (
        <div
          key={`${change.kind}-${change.namespace ?? ""}-${change.name}-${change.timestamp}-${index}`}
          className="rounded-md border border-theme-border/70 bg-theme-base/30 p-2.5"
        >
          <div className="flex flex-wrap items-center gap-1.5">
            <Badge kind={change.kind} size="sm">
              {change.kind}
            </Badge>
            <span className="font-mono text-[11px] text-theme-text-secondary">
              {change.namespace ? `${change.namespace}/` : ""}
              {change.name}
            </span>
            <Badge tone="note" size="sm">
              {change.changeType.replaceAll("_", " ")}
            </Badge>
            <time
              dateTime={change.timestamp}
              title={change.timestamp}
              className="ml-auto text-[11px] text-theme-text-tertiary"
            >
              {formatRelativeAgeTime(change.timestamp)}
            </time>
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
            <Badge kind={finding.kind} size="sm">
              {finding.kind}
            </Badge>
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
    ["Derived", network.summary.derived ?? 0],
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
            <div className="text-[10px] uppercase tracking-wide text-theme-text-tertiary">
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
                  <span className="mt-0.5 block text-[11px] leading-relaxed text-theme-text-secondary">
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
        <Badge kind={data.root.kind} size="sm">
          {data.root.kind}
        </Badge>
        <span className="font-mono text-xs text-theme-text-secondary">
          {data.root.namespace ? `${data.root.namespace}/` : ""}
          {data.root.name}
        </span>
        <span className="text-[11px] text-theme-text-tertiary">
          {data.nodes.length} nodes · {data.edges.length} direct relationships
        </span>
      </div>
      <div className="max-h-44 overflow-y-auto pr-1">
        <div className="flex flex-wrap gap-1.5">
          {data.nodes.map((node) => (
            <span
              key={node.id}
              className="inline-flex items-center gap-1 rounded-md border border-theme-border bg-theme-base px-2 py-1 text-[11px]"
            >
              <Badge kind={node.kind} size="sm">
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
              className="flex min-w-0 items-center gap-1.5 rounded bg-theme-base/50 px-2 py-1.5 font-mono text-[11px] text-theme-text-secondary"
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
            <div className="text-[10px] font-semibold uppercase tracking-wide text-theme-text-tertiary">
              {namespace.namespace || "cluster scoped"}
            </div>
            <ul className="mt-1 space-y-1 font-mono text-[11px] text-theme-text-secondary">
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
      <div className="text-[10px] uppercase tracking-wide text-theme-text-tertiary">
        {label}
      </div>
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
          <Badge kind={resource.kind} size="sm">
            {resource.kind}
          </Badge>
          <span className="min-w-0 flex-1">
            <span className="block truncate font-mono text-xs text-theme-text-secondary">
              {resource.namespace ? `${resource.namespace}/` : ""}
              {resource.name}
            </span>
            {resource.issue ? (
              <span className="block truncate text-[11px] text-warning-text">
                {resource.issue}
              </span>
            ) : null}
          </span>
          {resource.ready || resource.status ? (
            <span className="ml-auto shrink-0 font-mono text-[11px] text-theme-text-tertiary">
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

function RevisionHistory({
  observations,
  onViewSource,
}: {
  observations: InvestigationEvidenceObservation[];
  onViewSource: (sourceId: string) => void;
}) {
  return (
    <div className="border-t border-theme-border/60 pt-2">
      <div className="mb-1.5 flex items-center gap-1.5 text-[10px] font-semibold uppercase tracking-wide text-theme-text-tertiary">
        <FileClock className="h-3.5 w-3.5" aria-hidden />
        Observation history
      </div>
      <ol className="space-y-1.5">
        {observations.map((observation) => (
          <li
            key={`${observation.source.id}-${observation.revision}`}
            className="flex min-w-0 items-start gap-2 text-[11px]"
          >
            <span className="mt-0.5 shrink-0 text-theme-text-tertiary">
              <span className="font-medium text-theme-text-secondary">
                {phaseLabel(observation.source.phase)}
              </span>{" "}
              <span className="font-mono">#{observation.revision}</span>
            </span>
            <Badge severity="neutral" size="sm">
              {observation.relevance === "target"
                ? "Target"
                : observation.relevance === "producer-related"
                  ? "Related"
                  : "Broader"}
            </Badge>
            <span className="min-w-0 flex-1 text-theme-text-secondary">
              {observation.summary || observation.title}
            </span>
            <SourceButton
              label={observation.source.tool}
              ariaLabel={`View Activity source for check ${observation.revision} of ${observation.title}`}
              onClick={() => onViewSource(observation.source.id)}
            />
          </li>
        ))}
      </ol>
    </div>
  );
}

function EvidenceCaveat({ data }: { data: InvestigationEvidenceData }) {
  let text: string | undefined;
  if (data.type === "events") {
    text =
      "Events support the timeline; proximity alone does not establish cause.";
  } else if (data.type === "changes") {
    text =
      "A recent change is context unless Radar explicitly correlated it to the issue.";
  } else if (data.type === "relationships" || data.type === "topology") {
    text =
      "This shows captured direct relationships, not an inferred blast radius.";
  }
  if (!text) return null;
  return (
    <p className="flex items-start gap-1.5 text-[11px] leading-relaxed text-theme-text-tertiary">
      <Info className="mt-0.5 h-3 w-3 shrink-0" aria-hidden />
      {text}
    </p>
  );
}

function EvidenceIcon({
  observation,
}: {
  observation: InvestigationEvidenceObservation;
}) {
  const Icon = evidenceIcon(observation.data.type);
  return (
    <span
      className={clsx(
        "flex h-7 w-7 shrink-0 items-center justify-center rounded-md bg-theme-elevated",
        toneText(observation.tone),
      )}
    >
      <Icon className="h-4 w-4" aria-hidden />
    </span>
  );
}

function EvidenceToneBadge({
  observation,
}: {
  observation: InvestigationEvidenceObservation;
}) {
  if (observation.tier === "key") {
    // The section heading and strong left rule already establish priority.
    // Repeating "key" on every card adds noise without new information.
    return null;
  }
  if (observation.tone === "neutral") return null;
  return (
    <Badge severity={severityBadge(observation.tone)} size="sm">
      {observation.tone}
    </Badge>
  );
}

function SourceButton({
  label,
  ariaLabel,
  onClick,
}: {
  label: string;
  ariaLabel?: string;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      aria-label={ariaLabel}
      title={`Source: ${label}`}
      onClick={onClick}
      className="max-w-28 shrink-0 rounded px-1.5 py-1 text-right hover:bg-accent/10"
    >
      <span className="block text-[10px] font-medium leading-tight text-accent-text">
        View source
      </span>
      <span className="block truncate font-mono text-[9px] leading-tight text-theme-text-tertiary">
        {label}
      </span>
    </button>
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

function toneText(tone: InvestigationEvidenceObservation["tone"]): string {
  if (tone === "error") return "text-red-400";
  if (tone === "alert") return "text-orange-400";
  if (tone === "warning") return "text-amber-500";
  if (tone === "info") return "text-accent-text";
  return "text-theme-text-tertiary";
}

function toneBorder(
  tone: InvestigationEvidenceObservation["tone"],
  tier: InvestigationEvidenceTier,
): string {
  if (tier !== "key") return "border-theme-border";
  if (tone === "error")
    return "border-l-[3px] border-l-red-500 border-theme-border";
  if (tone === "alert")
    return "border-l-[3px] border-l-orange-500 border-theme-border";
  return "border-l-[3px] border-l-amber-500 border-theme-border";
}
