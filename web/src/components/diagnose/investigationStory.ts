/**
 * The story is the agent's prose with Radar results placed inside it. A
 * placement is `[[radar:evidence=N]]` alone on a line, N being the 0-based
 * index into the verdict's `evidence` array; the same marker inside a sentence
 * is a reference back to the placed card. Markers inside fenced code, inline
 * code or a blockquote are the agent quoting something and never place: a
 * tool result pasted into the story must not read as a citation.
 *
 * This is a line tokenizer that tracks fences and inline code, not a full
 * Markdown parser; the grammar it accepts is deliberately small and the same
 * fixtures pin it on both sides of the wire (see internal/ai/story_test.go).
 */

export const STORY_PLACEMENT_RE = /\[\[radar:evidence=(\d+)(?:\|(compact))?\]\]/g;
const BLOCK_PLACEMENT_RE = /^\s*\[\[radar:evidence=(\d+)(?:\|(compact))?\]\]\s*$/;
const FENCE_RE = /^\s{0,3}(`{3,}|~{3,})/;

/** Maximum results placed as cards; later placements become references. */
export const MAX_STORY_PLACEMENTS = 6;

export type StorySegment =
  | { kind: "prose"; markdown: string }
  | {
      kind: "placement";
      index: number;
      /** The agent asked for the header only: the reader needs the fact, not the detail. */
      compact?: boolean;
    };

export interface StorySplit {
  segments: StorySegment[];
  /** Every marker that appeared inline, in order, including repeats. */
  inlineRefs: number[];
}

/** The href an inline reference is rewritten to; the Markdown link renderer reads it back. */
export function storyReferenceHref(index: number): string {
  return `#radar-evidence-${index}`;
}

export function storyReferenceIndex(href: string | undefined): number | undefined {
  const match = href ? /^#radar-evidence-(\d+)$/.exec(href) : null;
  return match ? Number(match[1]) : undefined;
}

/**
 * Rewrites inline markers in one line to reference links, leaving markers
 * inside inline code spans alone. A backtick count is enough because a marker
 * contains none: the span state at each marker is the parity of backticks
 * before it.
 */
function rewriteInlineMarkers(line: string, inlineRefs: number[]): string {
  let out = "";
  let cursor = 0;
  let backticks = 0;
  STORY_PLACEMENT_RE.lastIndex = 0;
  for (const match of line.matchAll(STORY_PLACEMENT_RE)) {
    const start = match.index ?? 0;
    const before = line.slice(cursor, start);
    backticks += (before.match(/`/g) ?? []).length;
    out += before;
    if (backticks % 2 === 1) {
      out += match[0];
    } else {
      const index = Number(match[1]);
      inlineRefs.push(index);
      out += `[${match[0]}](${storyReferenceHref(index)})`;
    }
    cursor = start + match[0].length;
  }
  return out + line.slice(cursor);
}

export function splitStory(report: string): StorySplit {
  const segments: StorySegment[] = [];
  const inlineRefs: number[] = [];
  const prose: string[] = [];
  let fence: string | undefined;
  const flush = () => {
    const markdown = prose.join("\n").trim();
    if (markdown) segments.push({ kind: "prose", markdown });
    prose.length = 0;
  };
  for (const line of report.split(/\r?\n/)) {
    const fenceMatch = FENCE_RE.exec(line);
    if (fence) {
      prose.push(line);
      if (fenceMatch && fenceMatch[1].startsWith(fence[0]) && fenceMatch[1].length >= fence.length) {
        fence = undefined;
      }
      continue;
    }
    if (fenceMatch) {
      fence = fenceMatch[1];
      prose.push(line);
      continue;
    }
    const block = BLOCK_PLACEMENT_RE.exec(line);
    if (block) {
      flush();
      segments.push({
        kind: "placement",
        index: Number(block[1]),
        ...(block[2] ? { compact: true } : {}),
      });
      continue;
    }
    if (/^\s{0,3}>/.test(line)) {
      prose.push(line);
      continue;
    }
    // A blank line outside a fence ends a paragraph. Prose is segmented per
    // paragraph so the collapsed preview can show exactly the paragraph
    // before the first placed card, and a line clamp applies to one
    // paragraph rather than to everything before the card.
    if (line.trim() === "") {
      flush();
      continue;
    }
    prose.push(rewriteInlineMarkers(line, inlineRefs));
  }
  flush();
  return { segments, inlineRefs };
}

/** The story without any markers, for copying and for models reading it back. */
export function storyPlainText(report: string): string {
  return report.replace(STORY_PLACEMENT_RE, "").replace(/\n{3,}/g, "\n\n").trim();
}

/** True when a report carries the story contract (any placement or reference). */
export function storyHasPlacements(report: string | undefined): boolean {
  if (!report) return false;
  STORY_PLACEMENT_RE.lastIndex = 0;
  return STORY_PLACEMENT_RE.test(report);
}
