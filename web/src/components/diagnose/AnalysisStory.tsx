import { useEffect, useId, useMemo, useState, type ReactNode } from "react";
import { clsx } from "clsx";
import { TRANSITION_CHEVRON } from "@skyhook-io/k8s-ui/utils/animation";
import { AlertTriangle, ChevronDown, FileSearch } from "lucide-react";
import { Tooltip } from "../ui/Tooltip";
import { Collapse } from "@skyhook-io/k8s-ui";

import { Markdown } from "../ui/Markdown";
import type { InvestigationCaseItem } from "./investigationCase";
import {
  MAX_STORY_PLACEMENTS,
  splitStory,
  storyReferenceIndex,
  type StorySegment,
  type StoryRefResolver,
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
  resolveRef?: StoryRefResolver,
): {
  segments: StorySegment[];
  byIndex: Map<number, StoryPlacementResolution>;
  placedAt: Map<number, number>;
  placedCount: number;
  lostItems: number;
} {
  const { segments: written } = splitStory(report, resolveRef);
  const byIndex = new Map<number, StoryPlacementResolution>();
  // The segment position that renders each placed index's card; a repeated
  // marker for the same index is a reference back to it, not a second card.
  const placedAt = new Map<number, number>();
  const lostIndexes = new Set<number>();
  let placedCount = 0;
  // A marker on its own line places its card there. A marker inside a
  // sentence places the card under that paragraph when nothing else does:
  // the reader wants the receipt beside the claim, and whether the agent
  // put the marker on its own line is a formatting detail the page must not
  // depend on. An index the agent does place on its own line somewhere keeps
  // that spot, so an early inline mention of it stays a reference.
  const blockIndexes = new Set(
    written
      .filter((segment) => segment.kind === "placement")
      .map((segment) => (segment as { index: number }).index),
  );
  const target = (index: number): StoryPlacementTarget | undefined => {
    const existing = byIndex.get(index);
    if (existing) return existing.kind === "lost" ? undefined : existing.target;
    const resolved = resolveItem(index);
    if (isLoss(resolved)) {
      byIndex.set(index, { kind: "lost", reason: resolved });
      lostIndexes.add(index);
      return undefined;
    }
    byIndex.set(index, { kind: "reference", target: resolved });
    return resolved;
  };
  // Revisions count within a group, so one call's resource, logs and events
  // share a source and a revision; the group tells them apart.
  const observationKey = (index: number, resolved: StoryPlacementTarget) =>
    resolved.item.observation
      ? `${resolved.item.groupId ?? ""}|${resolved.item.observation.source.id}#${resolved.item.observation.revision}`
      : `item-${index}`;
  // Two items on one observation (a twin) share its card: the second points
  // at the card the first placed, wherever that is.
  const positionByKey = new Map<string, number>();
  const place = (
    index: number,
    resolved: StoryPlacementTarget,
    position: number,
  ): boolean => {
    const key = observationKey(index, resolved);
    const twin = positionByKey.get(key);
    if (twin !== undefined) {
      placedAt.set(index, twin);
      return false;
    }
    if (placedCount >= MAX_STORY_PLACEMENTS) return false;
    positionByKey.set(key, position);
    placedCount += 1;
    placedAt.set(index, position);
    byIndex.set(index, { kind: "placed", target: resolved });
    return true;
  };
  // An observation the agent places on its own line somewhere keeps that
  // spot even when a twin item on it is mentioned inline first.
  const blockKeys = new Set<string>();
  for (const index of blockIndexes) {
    const resolved = target(index);
    if (resolved) blockKeys.add(observationKey(index, resolved));
  }
  const segments: StorySegment[] = [];
  for (const segment of written) {
    if (segment.kind === "placement") {
      const resolved = target(segment.index);
      const position = segments.length;
      segments.push(segment);
      if (resolved && !placedAt.has(segment.index))
        place(segment.index, resolved, position);
      continue;
    }
    let markdown = segment.markdown;
    const autoPlaced: number[] = [];
    const autoCompact = new Set<number>();
    for (const match of markdown.matchAll(
      /\[\[\[radar:evidence([^\]]*)\]\]\]\(#radar-evidence-(-?\d+)\)/g,
    )) {
      const index = Number(match[2]);
      if (match[1].endsWith("|compact")) autoCompact.add(index);
      const resolved = target(index);
      if (
        !resolved ||
        blockIndexes.has(index) ||
        blockKeys.has(observationKey(index, resolved)) ||
        placedAt.has(index)
      )
        continue;
      // The card lands right under this paragraph, so the sentence keeps its
      // words and drops the marker: the card's own title says what it is.
      if (place(index, resolved, segments.length + 1 + autoPlaced.length))
        autoPlaced.push(index);
    }
    for (const index of autoPlaced)
      markdown = removeInlineMarker(markdown, index);
    segments.push({ kind: "prose", markdown });
    for (const index of autoPlaced)
      segments.push({
        kind: "placement",
        index,
        auto: true,
        ...(autoCompact.has(index) ? { compact: true } : {}),
      });
  }
  // A twin item that was only ever mentioned inline still points at the card
  // its sibling placed.
  for (const [index, resolution] of byIndex) {
    if (resolution.kind !== "reference" || placedAt.has(index)) continue;
    const at = positionByKey.get(observationKey(index, resolution.target));
    if (at !== undefined) placedAt.set(index, at);
  }
  return {
    segments,
    byIndex,
    placedAt,
    placedCount,
    lostItems: lostIndexes.size,
  };
}

// Only the hole the marker leaves is tidied: a space before punctuation or a
// doubled space there. The rest of the paragraph, including a two-space hard
// line break, is the agent's.
function removeInlineMarker(markdown: string, index: number): string {
  const match = new RegExp(
    `\\[\\[\\[radar:evidence[^\\]]*\\]\\]\\]\\(#radar-evidence-${index}\\)`,
  ).exec(markdown);
  if (!match) return markdown;
  let before = markdown.slice(0, match.index);
  let after = markdown.slice(match.index + match[0].length);
  // Spaces and tabs only: a newline, and the two spaces of a hard break
  // before it, are the agent's.
  if (/^[ \t]{2,}\n/.test(after)) before = before.replace(/[ \t]+$/, "");
  else if (
    /[ \t]$/.test(before) &&
    (/^[,.;:!?]/.test(after) || after === "" || after.startsWith("\n"))
  )
    before = before.replace(/[ \t]+$/, "");
  else if (
    /^[ \t]/.test(after) &&
    (/[ \t]$/.test(before) || before === "" || before.endsWith("\n"))
  )
    after = after.replace(/^[ \t]+/, "");
  return before + after;
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
      // A result with no card is not a loss the reader must weigh; the
      // sentence above already makes its claim. One quiet link to the raw
      // result, in the register of an inline reference.
      <span
        role="note"
        data-story-lost-support="nocard"
        className="inline-flex items-center gap-1 text-[11px] text-theme-text-tertiary"
      >
        <FileSearch className="h-3 w-3 shrink-0" aria-hidden />
        {onViewSource ? (
          <button
            type="button"
            onClick={() => onViewSource(reason.sourceId)}
            className="hover:text-accent-text hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/50"
          >
            Result in Activity
          </button>
        ) : (
          "Result in Activity"
        )}
      </span>
    );
  }
  // Loud enough to notice, quiet enough to read past: the reason on hover.
  return (
    <Tooltip content={LOSS_COPY[reason]} wrapperClassName="align-baseline">
      <span
        role="note"
        data-story-lost-support={reason}
        className="inline-flex items-center gap-1 rounded border border-dashed border-theme-border px-1 py-px align-baseline text-[11px] text-theme-text-tertiary"
      >
        <AlertTriangle
          className="h-3 w-3 shrink-0 text-amber-500"
          aria-hidden
        />
        unverified
      </span>
    </Tooltip>
  );
}

function ReferenceChip({
  target,
  onReveal,
  direction = "up",
}: {
  target: StoryPlacementTarget;
  onReveal: (target: StoryPlacementTarget) => void;
  /** Where the card is from here: above, below, or in Captured results. */
  direction?: "up" | "down";
}) {
  const title = target.item.observation?.title ?? "the cited result";
  return (
    <button
      type="button"
      onClick={() => onReveal(target)}
      data-story-ref-direction={direction}
      className="inline-flex max-w-full items-baseline gap-1 rounded border border-theme-border bg-theme-base px-1.5 py-px align-baseline text-[11px] font-medium text-accent-text hover:bg-theme-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/50"
      aria-label={`Show ${title}`}
    >
      <span aria-hidden>{direction === "down" ? "↓" : "↑"}</span>
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
  resolveRef,
  renderPlacement,
  onReveal,
  onViewSource,
  trailing,
  defaultOpen = false,
  openRequest = 0,
  className,
}: {
  report: string;
  resolveItem: (index: number) => StoryPlacementTarget | StoryPlacementLoss;
  /** Maps a ledger ref an agent wrote as a marker to its evidence index. */
  resolveRef?: StoryRefResolver;
  /** Renders the card for a placed item; `compact` when it sits in the collapsed preview. */
  renderPlacement: (
    target: StoryPlacementTarget,
    options: { compact: boolean },
  ) => ReactNode;
  onReveal: (target: StoryPlacementTarget) => void;
  /** Opens a cited call's raw result in Activity when Radar renders no card for it. */
  onViewSource?: (sourceId: string) => void;
  /** Rendered at the end of the full analysis, behind the fold. */
  trailing?: ReactNode;
  defaultOpen?: boolean;
  /** Bumped by the host when a reveal needs the story open first. */
  openRequest?: number;
  className?: string;
}) {
  const [open, setOpen] = useState(defaultOpen);
  // The wide layout is measured after mount; when it asks for the story open,
  // open it. Narrowing never closes what the reader opened.
  useEffect(() => {
    if (defaultOpen) setOpen(true);
  }, [defaultOpen]);
  useEffect(() => {
    if (openRequest > 0) setOpen(true);
  }, [openRequest]);
  const regionId = useId();
  const story = useMemo(
    () => resolveStoryPlacements(report, resolveItem, resolveRef),
    [report, resolveItem, resolveRef],
  );
  const firstPlacedAt = story.segments.findIndex(
    (segment) =>
      segment.kind === "placement" &&
      story.byIndex.get(segment.index)?.kind === "placed",
  );
  // The preview is the prose immediately before the first placed card plus
  // that card; with nothing placed, the first prose block. Everything else
  // is what "Expand" reveals.
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
        : Math.max(firstProseAt, 0);
  const hiddenSegments =
    story.segments.length - (previewEnd - previewStart + 1);
  const foldable = hiddenSegments > 0 || previewStart > 0 || !!trailing;
  const linkRendererAt = (position: number) => (href: string | undefined) => {
    const index = storyReferenceIndex(href);
    if (index === undefined) return null;
    const resolution = story.byIndex.get(index);
    if (!resolution) return null;
    if (resolution.kind === "lost")
      return (
        <LostSupport reason={resolution.reason} onViewSource={onViewSource} />
      );
    const at = story.placedAt.get(index);
    return (
      <ReferenceChip
        target={resolution.target}
        onReveal={onReveal}
        direction={at === undefined || at > position ? "down" : "up"}
      />
    );
  };
  const renderSegment = (
    segment: StorySegment,
    position: number,
    compact: boolean,
  ) => {
    if (segment.kind === "prose") {
      const prose = (
        <Markdown
          key={`prose-${position}`}
          className={clsx(STORY_PROSE_CLASS, compact && "[&_p]:line-clamp-6")}
          linkRenderer={linkRendererAt(position)}
        >
          {segment.markdown}
        </Markdown>
      );
      return prose;
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
    const at = story.placedAt.get(segment.index);
    if (resolution.kind === "reference" || at !== position) {
      return (
        <div key={`ref-${position}`}>
          <ReferenceChip
            target={resolution.target}
            onReveal={onReveal}
            direction={at === undefined || at > position ? "down" : "up"}
          />
        </div>
      );
    }
    return (
      <div
        key={`placed-${position}`}
        data-story-placement={segment.index}
        data-story-placement-compact={segment.compact ? "1" : undefined}
      >
        {renderPlacement(resolution.target, {
          compact: compact || segment.compact === true,
        })}
      </div>
    );
  };
  // The preview stays mounted in both states; what the fold hides sits in
  // animated collapses on either side of it, mounted on first open so the
  // collapsed page carries no hidden prose.
  const before = story.segments
    .slice(0, previewStart)
    .map((segment, position) => renderSegment(segment, position, false));
  const folded = foldable && !open;
  const preview = story.segments
    .slice(previewStart, previewEnd + 1)
    .map((segment, offset) =>
      renderSegment(segment, previewStart + offset, folded),
    );
  // The hint that more follows: the first two lines of the hidden paragraph
  // after the preview, faded out. It points at what the fold hides instead of
  // greying text the reader can already see.
  const teaser =
    folded && story.segments[previewEnd + 1]?.kind === "prose"
      ? story.segments[previewEnd + 1]
      : undefined;
  const after = story.segments
    .slice(previewEnd + 1)
    .map((segment, offset) =>
      renderSegment(segment, previewEnd + 1 + offset, false),
    );
  return (
    <section
      aria-label="Analysis"
      data-story
      data-story-open={open || !foldable ? "true" : "false"}
      className={clsx("space-y-2", className)}
    >
      <div id={regionId}>
        {before.length > 0 ? (
          <Collapse open={open} mountLazily>
            <div className="space-y-2 pb-2">{before}</div>
          </Collapse>
        ) : null}
        <div className="space-y-2">{preview}</div>
        {teaser && teaser.kind === "prose" ? (
          // The inert inner block keeps the teaser out of the tab order and
          // hit-testing; the click lands on this wrapper instead.
          <div
            data-story-teaser
            className="relative mt-2 cursor-pointer"
            onClick={() => setOpen(true)}
          >
            <div
              aria-hidden
              inert
              className="relative max-h-12 overflow-hidden"
            >
              <Markdown
                className={clsx(STORY_PROSE_CLASS, "[&_p]:line-clamp-2")}
                linkRenderer={(href) => {
                  const index = storyReferenceIndex(href);
                  const resolution =
                    index === undefined ? undefined : story.byIndex.get(index);
                  return (
                    <span>
                      {resolution && resolution.kind !== "lost"
                        ? (resolution.target.item.observation?.title ?? "")
                        : ""}
                    </span>
                  );
                }}
              >
                {teaser.markdown}
              </Markdown>
              <div className="pointer-events-none absolute inset-x-0 bottom-0 h-10 bg-gradient-to-t from-[var(--color-investigation-evidence)] to-transparent" />
            </div>
          </div>
        ) : null}
        {after.length > 0 || trailing ? (
          <Collapse open={open} mountLazily>
            <div className="space-y-2 pt-2">
              {after}
              {trailing}
            </div>
          </Collapse>
        ) : null}
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
          className="flex w-full items-center justify-center gap-1.5 rounded-md py-1.5 text-xs font-medium text-accent-text hover:bg-theme-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/50"
        >
          <ChevronDown
            aria-hidden
            className={clsx(
              "h-3.5 w-3.5",
              TRANSITION_CHEVRON,
              open && "rotate-180",
            )}
          />
          {open ? "Collapse" : "Expand"}
        </button>
      ) : null}
    </section>
  );
}
