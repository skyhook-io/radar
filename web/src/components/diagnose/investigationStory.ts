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

// An agent that writes the ledger's ref form, [[radar:evidence-ref=ev_…]],
// meant a placement; the parser rewrites a cited ref to its index, and one
// nothing cites reaches here as a ref the resolver cannot place. Either way
// it is a marker, never literal text.
export const STORY_PLACEMENT_RE =
  /\[\[radar:evidence(?:=(\d+)|-ref=([A-Za-z0-9_]+))(?:\|(compact))?\]\]/g;
// Four or more leading spaces is Markdown indented code, so a marker there
// stays literal like one inside a fence.
const BLOCK_PLACEMENT_RE =
  /^\s{0,3}\[\[radar:evidence(?:=(\d+)|-ref=([A-Za-z0-9_]+))(?:\|(compact))?\]\]\s*$/;
/** A reference the story cannot resolve renders as a placement Radar cannot identify. */
export const UNRESOLVED_STORY_INDEX = -1;
export type StoryRefResolver = (ref: string) => number | undefined;
function markerIndex(
  index: string | undefined,
  ref: string | undefined,
  resolveRef: StoryRefResolver | undefined,
): number {
  if (index !== undefined) return Number(index);
  const resolved = ref && resolveRef ? resolveRef(ref) : undefined;
  return resolved === undefined || resolved < 0
    ? UNRESOLVED_STORY_INDEX
    : resolved;
}
const FENCE_RE = /^\s{0,3}(`{3,}|~{3,})/;
// A closing fence is bare: same character, at least the opening length, and
// nothing but whitespace after it. "````not-a-close" is still code.
const FENCE_CLOSE_RE = /^\s{0,3}(`{3,}|~{3,})\s*$/;

/** Maximum results placed as cards; later placements become references. */
export const MAX_STORY_PLACEMENTS = 6;

export type StorySegment =
  | { kind: "prose"; markdown: string }
  | {
      kind: "placement";
      index: number;
      /** The agent asked for the header only: the reader needs the fact, not the detail. */
      compact?: boolean;
      /** Placed by Radar under the paragraph that first mentioned it inline. */
      auto?: boolean;
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

export function storyReferenceIndex(
  href: string | undefined,
): number | undefined {
  const match = href ? /^#radar-evidence-(-?\d+)$/.exec(href) : null;
  return match ? Number(match[1]) : undefined;
}

/**
 * Rewrites inline markers in one line to reference links, leaving markers
 * inside inline code spans alone. A backtick count is enough because a marker
 * contains none: the span state at each marker is the parity of backticks
 * before it.
 */
// A code span opens with a run of backticks and closes with a run of the same
// length; a run of another length inside it is literal. Counting single
// backticks would open a span at `` and never close it.
function codeSpanAfter(text: string, openRun: number): number {
  for (const run of text.match(/`+/g) ?? []) {
    if (openRun === 0) openRun = run.length;
    else if (run.length === openRun) openRun = 0;
  }
  return openRun;
}

// A code span may run across lines within one paragraph, so the open run
// travels from line to line and a blank line ends it with the paragraph.
function rewriteInlineMarkers(
  line: string,
  inlineRefs: number[],
  resolveRef: StoryRefResolver | undefined,
  openRun: number,
): { text: string; openRun: number } {
  let out = "";
  let cursor = 0;
  STORY_PLACEMENT_RE.lastIndex = 0;
  for (const match of line.matchAll(STORY_PLACEMENT_RE)) {
    const start = match.index ?? 0;
    const before = line.slice(cursor, start);
    openRun = codeSpanAfter(before, openRun);
    out += before;
    if (openRun > 0) {
      out += match[0];
    } else {
      const index = markerIndex(match[1], match[2], resolveRef);
      inlineRefs.push(index);
      out += `[${match[0]}](${storyReferenceHref(index)})`;
    }
    cursor = start + match[0].length;
  }
  const tail = line.slice(cursor);
  return { text: out + tail, openRun: codeSpanAfter(tail, openRun) };
}

export function splitStory(
  report: string,
  resolveRef?: StoryRefResolver,
): StorySplit {
  const segments: StorySegment[] = [];
  const inlineRefs: number[] = [];
  const prose: string[] = [];
  let fence: string | undefined;
  let openRun = 0;
  const flush = () => {
    openRun = 0;
    // Trim blank lines, not indentation: a segment that opens with indented
    // code must keep its four spaces to stay code.
    const markdown = prose.join("\n").replace(/^\n+/, "").trimEnd();
    if (markdown) segments.push({ kind: "prose", markdown });
    prose.length = 0;
  };
  for (const line of report.split(/\r?\n/)) {
    const fenceMatch = FENCE_RE.exec(line);
    if (fence) {
      prose.push(line);
      const close = FENCE_CLOSE_RE.exec(line);
      if (
        close &&
        close[1].startsWith(fence[0]) &&
        close[1].length >= fence.length
      ) {
        fence = undefined;
      }
      continue;
    }
    if (fenceMatch) {
      fence = fenceMatch[1];
      prose.push(line);
      continue;
    }
    const block = openRun === 0 ? BLOCK_PLACEMENT_RE.exec(line) : null;
    if (block) {
      flush();
      segments.push({
        kind: "placement",
        index: markerIndex(block[1], block[2], resolveRef),
        ...(block[3] ? { compact: true } : {}),
      });
      continue;
    }
    // Blockquotes and indented code (four spaces or a tab) are quoted text:
    // markers there stay literal, never citations.
    if (/^\s{0,3}>/.test(line) || /^( {4,}|\t)/.test(line)) {
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
    const rewritten = rewriteInlineMarkers(
      line,
      inlineRefs,
      resolveRef,
      openRun,
    );
    openRun = rewritten.openRun;
    prose.push(rewritten.text);
  }
  flush();
  return { segments, inlineRefs };
}

/** The story without any markers, for copying and for models reading it back. */
export function storyPlainText(report: string): string {
  return report
    .replace(STORY_PLACEMENT_RE, "")
    .replace(/\n{3,}/g, "\n\n")
    .trim();
}

/** The one definition of "renders as a story": a summary headline or any marker. */
export function diagnosisHasStoryShape(
  diagnosis: { summary?: string; report?: string } | null | undefined,
): boolean {
  if (!diagnosis) return false;
  return !!diagnosis.summary?.trim() || storyHasPlacements(diagnosis.report);
}

/** True when a report carries the story contract (any placement or reference). */
export function storyHasPlacements(report: string | undefined): boolean {
  if (!report) return false;
  STORY_PLACEMENT_RE.lastIndex = 0;
  return STORY_PLACEMENT_RE.test(report);
}
