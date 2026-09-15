import { useId, useMemo, useState, type ReactNode } from "react";
import { clsx } from "clsx";
import { AlertTriangle, FileSearch } from "lucide-react";
import { CollapseChevron } from "@skyhook-io/k8s-ui";

import { Markdown } from "../ui/Markdown";
import type { InvestigationCaseItem } from "./investigationCase";
import {
  MAX_STORY_PLACEMENTS,
  splitStory,
  storyReferenceIndex,
  type StorySegment,
} from "./investigationStory";

/**
 * Why a placement could not render. Each is shown at the sentence it belongs
 * to — a vanished marker would leave the assertion standing with no sign that
 * its support failed — and counted once per evidence item, however many
 * markers point at it.
 */
export type StoryPlacementLoss =
  | "invalid" // no such index, or not a number Radar recognises
  | "unlinked" // the server could not bind the item's ref
  | "unplaced" // bound, but resolves to no single observation
  | { kind: "nocard"; sourceId: string }; // bound, but Radar renders no card for that call

export interface StoryPlacementTarget {
  item: InvestigationCaseItem;
  /** DOM id of the card rendered for this placement, or of the inventory card when it was not placed. */
  domId: string;
}

export type StoryPlacementResolution =
  | { kind: "placed"; target: StoryPlacementTarget }
  | { kind: "reference"; target: StoryPlacementTarget }
  | { kind: "lost"; reason: StoryPlacementLoss };

function isLoss(
  value: StoryPlacementTarget | StoryPlacementLoss,
): value is StoryPlacementLoss {
  return typeof value === "string" || "kind" in value;
}

/**
 * Decides what every marker in the story renders as: the first block marker
 * for an observation places its card, later block markers and every inline
 * marker reference it, and anything unresolvable is a visible loss. Dedupe is
 * by resolved observation, not by index — two items can address one read.
 */
export function resolveStoryPlacements(
  report: string,
  resolveItem: (index: number) => StoryPlacementTarget | StoryPlacementLoss,
): {
  segments: StorySegment[];
  byIndex: Map<number, StoryPlacementResolution>;
  placedCount: number;
  lostItems: number;
} {
  const { segments } = splitStory(report);
  const byIndex = new Map<number, StoryPlacementResolution>();
  const placedObservations = new Set<string>();
  const lostIndexes = new Set<number>();
  let placedCount = 0;
  const resolve = (index: number, block: boolean) => {
    const existing = byIndex.get(index);
    if (existing) {
      if (existing.kind === "placed" || existing.kind === "lost") return;
      if (!block) return;
    }
    const resolved = resolveItem(index);
    if (isLoss(resolved)) {
      byIndex.set(index, { kind: "lost", reason: resolved });
      lostIndexes.add(index);
      return;
    }
    const observationKey = resolved.item.observation
      ? `${resolved.item.observation.source.id}#${resolved.item.observation.revision}`
      : `item-${index}`;
    if (
      block &&
      !placedObservations.has(observationKey) &&
      placedCount < MAX_STORY_PLACEMENTS
    ) {
      placedObservations.add(observationKey);
      placedCount += 1;
      byIndex.set(index, { kind: "placed", target: resolved });
      return;
    }
    if (!existing) byIndex.set(index, { kind: "reference", target: resolved });
  };
  for (const segment of segments) {
    if (segment.kind === "placement") {
      resolve(segment.index, true);
    } else {
      for (const match of segment.markdown.matchAll(/#radar-evidence-(\d+)\)/g)) {
        resolve(Number(match[1]), false);
      }
    }
  }
  return { segments, byIndex, placedCount, lostItems: lostIndexes.size };
}

const LOSS_COPY: Record<Exclude<StoryPlacementLoss, object>, string> = {
  invalid: "The agent cited a result Radar cannot identify.",
  unlinked: "Radar could not verify this citation against its record.",
  unplaced:
    "Radar could not pin this citation to one result; see Captured results.",
};

/**
 * A citation that cannot render at the claim still says so at the claim. A
 * bound result Radar has no card for is the common case — a cluster-wide
 * search, a metrics table — and is one click from its raw result in
 * Activity, so that case carries the link rather than a dead end.
 */
function LostSupport({
  reason,
  onViewSource,
}: {
  reason: StoryPlacementLoss;
  onViewSource?: (sourceId: string) => void;
}) {
  if (typeof reason !== "string") {
    return (
      <span
        role="note"
        data-story-lost-support="nocard"
        className="my-1 inline-flex flex-wrap items-center gap-1.5 rounded-md border border-dashed border-theme-border px-2 py-1 text-[11px] text-theme-text-tertiary"
      >
        <FileSearch className="h-3 w-3 shrink-0" aria-hidden />
        No card for this result.
        {onViewSource ? (
          <button
            type="button"
            onClick={() => onViewSource(reason.sourceId)}
            className="font-medium text-accent-text hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/50"
          >
            View it in Activity
          </button>
        ) : null}
      </span>
    );
  }
  return (
    <span
      role="note"
      data-story-lost-support={reason}
      className="my-1 inline-flex items-center gap-1.5 rounded-md border border-dashed border-theme-border px-2 py-1 text-[11px] text-theme-text-tertiary"
    >
      <AlertTriangle className="h-3 w-3 shrink-0" aria-hidden />
      Support unavailable · {LOSS_COPY[reason]}
    </span>
  );
}

function ReferenceChip({
  target,
  onReveal,
}: {
  target: StoryPlacementTarget;
  onReveal: (target: StoryPlacementTarget) => void;
}) {
  const title = target.item.observation?.title ?? "the cited result";
  return (
    <button
      type="button"
      onClick={() => onReveal(target)}
      className="inline-flex max-w-full items-baseline gap-1 rounded border border-theme-border bg-theme-base px-1.5 py-px align-baseline text-[11px] font-medium text-accent-text hover:bg-theme-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/50"
      aria-label={`Show ${title}`}
    >
      <span aria-hidden>↑</span>
      <span className="truncate">{title}</span>
    </button>
  );
}

const STORY_PROSE_CLASS =
  "text-sm leading-relaxed text-theme-text-primary [overflow-wrap:anywhere] [&_code]:font-normal [&_h2]:mb-1 [&_h2]:mt-3 [&_h2]:text-xs [&_h2]:font-semibold [&_h2]:uppercase [&_h2]:tracking-wide [&_h2]:text-theme-text-tertiary [&_h3]:text-sm [&_li]:text-theme-text-primary [&_p]:my-1.5 [&_p]:text-theme-text-primary [&_p:first-child]:mt-0 [&_p:last-child]:mb-0";

/**
 * The agent's story with Radar's results placed where it cites them. The
 * prose is the agent's; every placed card is rendered by the host from Radar's
 * record, so a reader checks each sentence against the fact beneath it.
 *
 * Collapsed by default to a bounded preview: the paragraph before the first
 * placed card, line-clamped, plus that card in compact form — never a sliced
 * card, and never more than the agent's prose decides. The fold bounds the
 * scroll to the next steps below it; it does not decide what matters, which is
 * why the summary and the unresolved list live above it.
 */
export function AnalysisStory({
  report,
  resolveItem,
  renderPlacement,
  onReveal,
  onViewSource,
  defaultOpen = false,
  className,
}: {
  report: string;
  resolveItem: (index: number) => StoryPlacementTarget | StoryPlacementLoss;
  /** Renders the card for a placed item; `compact` when it sits in the collapsed preview. */
  renderPlacement: (
    target: StoryPlacementTarget,
    options: { compact: boolean },
  ) => ReactNode;
  onReveal: (target: StoryPlacementTarget) => void;
  /** Opens a cited call's raw result in Activity when Radar renders no card for it. */
  onViewSource?: (sourceId: string) => void;
  defaultOpen?: boolean;
  className?: string;
}) {
  const [open, setOpen] = useState(defaultOpen);
  const regionId = useId();
  const story = useMemo(
    () => resolveStoryPlacements(report, resolveItem),
    [report, resolveItem],
  );
  const firstPlacedAt = story.segments.findIndex(
    (segment) =>
      segment.kind === "placement" &&
      story.byIndex.get(segment.index)?.kind === "placed",
  );
  // The preview is the prose immediately before the first placed card plus
  // that card; with nothing placed, the first prose block. Everything else
  // is what "Read the full analysis" reveals.
  const firstProseAt = story.segments.findIndex(
    (segment) => segment.kind === "prose",
  );
  // With nothing placed, the preview is the first paragraph — and the
  // placement right after it when there is one, so a citation Radar could
  // not render is visible as such rather than hidden behind the fold.
  const previewEnd =
    firstPlacedAt >= 0
      ? firstPlacedAt
      : firstProseAt >= 0 &&
          story.segments[firstProseAt + 1]?.kind === "placement"
        ? firstProseAt + 1
        : firstProseAt;
  const previewStart =
    firstPlacedAt > 0 && story.segments[firstPlacedAt - 1]?.kind === "prose"
      ? firstPlacedAt - 1
      : firstPlacedAt >= 0
        ? firstPlacedAt
        : Math.max(previewEnd, 0);
  const hiddenSegments =
    story.segments.length - (previewEnd - previewStart + 1);
  const foldable = hiddenSegments > 0 || previewStart > 0;
  const linkRenderer = (href: string | undefined) => {
    const index = storyReferenceIndex(href);
    if (index === undefined) return null;
    const resolution = story.byIndex.get(index);
    if (!resolution) return null;
    if (resolution.kind === "lost")
      return (
        <LostSupport reason={resolution.reason} onViewSource={onViewSource} />
      );
    return <ReferenceChip target={resolution.target} onReveal={onReveal} />;
  };
  const renderSegment = (
    segment: StorySegment,
    position: number,
    compact: boolean,
  ) => {
    if (segment.kind === "prose") {
      return (
        <Markdown
          key={`prose-${position}`}
          className={clsx(
            STORY_PROSE_CLASS,
            compact && "[&_p]:line-clamp-6",
          )}
          linkRenderer={linkRenderer}
        >
          {segment.markdown}
        </Markdown>
      );
    }
    const resolution = story.byIndex.get(segment.index);
    if (!resolution || resolution.kind === "lost") {
      return (
        <div key={`lost-${position}`}>
          <LostSupport
            reason={resolution?.reason ?? "invalid"}
            onViewSource={onViewSource}
          />
        </div>
      );
    }
    if (resolution.kind === "reference") {
      return (
        <div key={`ref-${position}`}>
          <ReferenceChip target={resolution.target} onReveal={onReveal} />
        </div>
      );
    }
    return (
      <div key={`placed-${position}`} data-story-placement={segment.index}>
        {renderPlacement(resolution.target, { compact })}
      </div>
    );
  };
  const visible = open
    ? story.segments.map((segment, position) =>
        renderSegment(segment, position, false),
      )
    : story.segments
        .slice(previewStart, previewEnd + 1)
        .map((segment, offset) =>
          renderSegment(segment, previewStart + offset, true),
        );
  return (
    <section
      aria-label="Analysis"
      data-story
      data-story-open={open || !foldable ? "true" : "false"}
      className={clsx("space-y-2", className)}
    >
      <div id={regionId} className="space-y-2">
        {visible}
      </div>
      {story.lostItems > 0 ? (
        <p className="text-[11px] text-theme-text-tertiary">
          {story.lostItems === 1
            ? "1 cited result could not be shown here."
            : `${story.lostItems} cited results could not be shown here.`}
        </p>
      ) : null}
      {foldable ? (
        <button
          type="button"
          aria-expanded={open}
          aria-controls={regionId}
          onClick={() => setOpen(!open)}
          className="inline-flex items-center gap-1.5 rounded-md px-1.5 py-1 text-xs font-medium text-accent-text hover:bg-theme-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/50"
        >
          <CollapseChevron open={open} className="h-3.5 w-3.5" />
          {open ? "Show less" : "Read the full analysis"}
        </button>
      ) : null}
    </section>
  );
}
