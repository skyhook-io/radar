import { useId, useState, type ReactNode } from "react";
import { clsx } from "clsx";
import {
  Activity,
  AlertTriangle,
  CircleAlert,
  Info,
  SearchCheck,
} from "lucide-react";
import { Collapse, CollapseChevron } from "@skyhook-io/k8s-ui";
import type { EvidenceCoverageGroup } from "./investigationEvidencePresentation";
import {
  investigationEvidenceSourceDomId,
  type InvestigationEvidenceGroup,
  type InvestigationRootCauseEvidenceResolution,
} from "./investigationEvidence";
import type { InvestigationSourceExcerpt } from "./investigationSourceFocus";
import type {
  InvestigationCaseItem,
  InvestigationCaseResolution,
} from "./investigationCase";
import { useDisclosureReveal } from "./useDisclosureReveal";
import { SourceButton } from "./investigationEvidence/cardParts";
import { investigationEvidenceFullRowFlags } from "./investigationEvidencePartition";
import { EvidenceCard } from "./EvidenceCard";

/**
 * Hypotheses the agent dropped, each pointing at the Radar observation whose
 * result contradicted it. Entries whose item could not be placed on an
 * observation are filtered out by the resolver and never rendered here.
 */
export function RuledOutBlock({
  entries,
  onReveal,
}: {
  entries: InvestigationCaseResolution["ruledOut"];
  onReveal: (item: InvestigationCaseItem) => void;
}) {
  const headingId = useId();
  return (
    <section
      aria-labelledby={headingId}
      data-testid="investigation-ruled-out"
      className="rounded-lg border border-theme-border/80 bg-theme-base/20 px-3 py-2.5"
    >
      <h3
        id={headingId}
        className="flex items-center gap-2 text-xs font-semibold text-theme-text-secondary"
      >
        Also checked
        <span className="text-[10px] font-semibold uppercase tracking-wide text-accent-text">
          Agent
        </span>
      </h3>
      <ul className="mt-1.5 space-y-1.5">
        {entries.map((entry, position) => {
          const observation = entry.item.observation!;
          return (
            <li
              key={position}
              className="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-0.5 text-xs"
            >
              <span className="min-w-0 text-theme-text-secondary [overflow-wrap:anywhere]">
                {entry.hypothesis}
              </span>
              <button
                type="button"
                onClick={() => onReveal(entry.item)}
                className="shrink-0 rounded text-accent-text hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/50"
              >
                See {observation.title}
                {entry.item.placement === "revision"
                  ? " (earlier observation)"
                  : ""}
              </button>
            </li>
          );
        })}
      </ul>
    </section>
  );
}

export function AssessmentEvidenceQualification({
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

export function EmptyCollection({
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

export function CollapsedEvidenceCollection({
  id,
  title,
  description,
  groups,
  animateGroupIds,
  onViewSource,
  open,
  onOpenChange,
  totalCount = groups.length,
  keepWhenEmpty = false,
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
  /** Render at zero: the children carry the empty state and the withheld-results note. */
  keepWhenEmpty?: boolean;
  children?: ReactNode;
}) {
  const { elementRef, revealAfterToggle } = useDisclosureReveal<HTMLElement>();
  if (totalCount === 0 && !keepWhenEmpty) return null;
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

export function CoverageStrip({
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
          {!historyOnly && !open && summary ? (
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
            ariaLabel={`View result for ${group.label}`}
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
                    ariaLabel={`View result for ${limitation.source}`}
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
