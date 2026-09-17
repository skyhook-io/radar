import { describe, expect, it } from "vitest";
import {
  splitStory,
  storyHasPlacements,
  storyPlainText,
  storyReferenceHref,
  storyReferenceIndex,
  UNRESOLVED_STORY_INDEX,
} from "./investigationStory";

describe("splitStory", () => {
  it("places a marker on its own line and references one inside a sentence", () => {
    const { segments, inlineRefs } = splitStory(
      "The pod crashes.\n\n[[radar:evidence=0]]\n\nAs [[radar:evidence=0]] shows, auth fails.\n  [[radar:evidence=2]]  \nDone.",
    );
    expect(segments).toEqual([
      { kind: "prose", markdown: "The pod crashes." },
      { kind: "placement", index: 0 },
      {
        kind: "prose",
        markdown:
          "As [[[radar:evidence=0]]](#radar-evidence-0) shows, auth fails.",
      },
      { kind: "placement", index: 2 },
      { kind: "prose", markdown: "Done." },
    ]);
    expect(inlineRefs).toEqual([0]);
  });

  it("treats markers inside fences, inline code and blockquotes as literal text", () => {
    const report = [
      "Quoted output:",
      "```",
      "[[radar:evidence=1]]",
      "```",
      "Inline `[[radar:evidence=1]]` is code, but [[radar:evidence=3]] is not.",
      "> [[radar:evidence=4]]",
      "~~~txt",
      "[[radar:evidence=5]]",
      "~~~",
    ].join("\n");
    const { segments, inlineRefs } = splitStory(report);
    expect(segments.filter((s) => s.kind === "placement")).toEqual([]);
    expect(inlineRefs).toEqual([3]);
    const prose = segments
      .map((s) => (s.kind === "prose" ? s.markdown : ""))
      .join("\n");
    expect(prose).toContain("`[[radar:evidence=1]]`");
    expect(prose).toContain("> [[radar:evidence=4]]");
    expect(prose).toContain("[[[radar:evidence=3]]](#radar-evidence-3)");
  });

  it("keeps indented-code markers and unbalanced closing fences literal", () => {
    const indented = splitStory("Text.\n\n    [[radar:evidence=0]]");
    expect(indented.segments.every((s) => s.kind === "prose")).toBe(true);
    expect(indented.inlineRefs).toEqual([]);
    expect(
      indented.segments.map((s) => s.kind === "prose" && s.markdown).join("\n"),
    ).toContain("    [[radar:evidence=0]]");
    const fence = splitStory(
      "```\n````not-a-close\n[[radar:evidence=0]]\n```\n\n[[radar:evidence=1]]",
    );
    expect(
      fence.segments
        .filter((s) => s.kind === "placement")
        .map((s) => s.kind === "placement" && s.index),
    ).toEqual([1]);
  });

  it("resolves a ref-form marker to its item, and flags one nothing cites", () => {
    const ref = "ev_" + "a".repeat(26) + "_" + "b".repeat(26);
    const stray = "ev_" + "c".repeat(26) + "_" + "d".repeat(26);
    const resolve = (candidate: string) => (candidate === ref ? 2 : undefined);
    const { segments, inlineRefs } = splitStory(
      `The log shows it:\n\n[[radar:evidence-ref=${ref}]]\n\nSee [[radar:evidence-ref=${ref}|compact]] and [[radar:evidence-ref=${stray}]].`,
      resolve,
    );
    expect(segments[1]).toEqual({ kind: "placement", index: 2 });
    expect(inlineRefs).toEqual([2, UNRESOLVED_STORY_INDEX]);
    expect(segments[2]).toMatchObject({ kind: "prose" });
    const inline = (segments[2] as { markdown: string }).markdown;
    expect(inline).toContain("](#radar-evidence-2)");
    expect(inline).toContain(`](#radar-evidence-${UNRESOLVED_STORY_INDEX})`);
    expect(storyPlainText(`x [[radar:evidence-ref=${ref}]] y`)).toBe("x  y");
  });
  it("keeps a marker inside a double-backtick code span literal", () => {
    const { segments, inlineRefs } = splitStory(
      "Write `` [[radar:evidence=0]] `` to place, then see [[radar:evidence=0]].",
    );
    expect(inlineRefs).toEqual([0]);
    expect((segments[0] as { markdown: string }).markdown).toContain(
      "`` [[radar:evidence=0]] ``",
    );
  });

  it("keeps a marker literal inside a code span that runs across lines", () => {
    const split = splitStory(
      "Quoted `example\n[[radar:evidence=0]]\nend` here.\n\n[[radar:evidence=0]]",
    );
    expect(split.segments.map((segment) => segment.kind)).toEqual([
      "prose",
      "placement",
    ]);
    expect(split.inlineRefs).toEqual([]);
    expect((split.segments[0] as { markdown: string }).markdown).toContain(
      "[[radar:evidence=0]]\nend`",
    );
  });

  it("reads the compact variant as a placement flag", () => {
    const { segments } = splitStory(
      "A.\n[[radar:evidence=2|compact]]\nB [[radar:evidence=2|compact]].",
    );
    expect(segments[1]).toEqual({ kind: "placement", index: 2, compact: true });
    expect(segments[2]).toEqual({
      kind: "prose",
      markdown: "B [[[radar:evidence=2|compact]]](#radar-evidence-2).",
    });
  });

  it("keeps an unterminated fence literal to the end", () => {
    const { segments } = splitStory("```\n[[radar:evidence=0]]\nstill code");
    expect(segments).toEqual([
      { kind: "prose", markdown: "```\n[[radar:evidence=0]]\nstill code" },
    ]);
  });

  it("round-trips reference hrefs and strips markers for plain text", () => {
    expect(storyReferenceIndex(storyReferenceHref(7))).toBe(7);
    expect(storyReferenceIndex("#other")).toBeUndefined();
    expect(
      storyPlainText("A.\n\n[[radar:evidence=0]]\n\nB [[radar:evidence=1]]."),
    ).toBe("A.\n\nB .");
    expect(storyHasPlacements("no markers")).toBe(false);
    expect(storyHasPlacements("x [[radar:evidence=0]]")).toBe(true);
  });
});
