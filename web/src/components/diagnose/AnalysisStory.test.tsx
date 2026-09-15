import { describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";

import {
  AnalysisStory,
  resolveStoryPlacements,
  type StoryPlacementLoss,
  type StoryPlacementTarget,
} from "./AnalysisStory";
import type { InvestigationCaseItem } from "./investigationCase";

function target(index: number, observationId: string): StoryPlacementTarget {
  return {
    item: {
      index,
      role: "cause",
      claim: "",
      placement: "card",
      groupId: `group-${observationId}`,
      source: { id: `src-${observationId}` },
      observation: {
        source: { id: `src-${observationId}` },
        revision: 1,
        title: `Card ${observationId}`,
      },
    } as unknown as InvestigationCaseItem,
    domId: `story-group-${observationId}`,
  };
}

const resolver =
  (table: Record<number, StoryPlacementTarget | StoryPlacementLoss>) =>
  (index: number) =>
    table[index] ?? ("invalid" as const);

describe("resolveStoryPlacements", () => {
  it("places the first block marker per observation and references the rest", () => {
    const story = resolveStoryPlacements(
      "A.\n\n[[radar:evidence=0]]\n\nAgain [[radar:evidence=0]] and [[radar:evidence=1]].\n\n[[radar:evidence=1]]\n\n[[radar:evidence=0]]",
      resolver({ 0: target(0, "a"), 1: target(1, "a") }),
    );
    // Item 1 resolves to the same observation as item 0: one card, not two.
    expect(story.byIndex.get(0)?.kind).toBe("placed");
    expect(story.byIndex.get(1)?.kind).toBe("reference");
    expect(story.placedCount).toBe(1);
    expect(story.lostItems).toBe(0);
  });

  it("caps placed cards and reports every distinct lost item once", () => {
    const markers = Array.from({ length: 8 }, (_, i) => `[[radar:evidence=${i}]]`).join(
      "\n\n",
    );
    const table: Record<number, StoryPlacementTarget | StoryPlacementLoss> = {};
    for (let i = 0; i < 8; i += 1) table[i] = target(i, `o${i}`);
    const story = resolveStoryPlacements(markers, resolver(table));
    expect(story.placedCount).toBe(6);
    expect(story.byIndex.get(7)?.kind).toBe("reference");

    const lost = resolveStoryPlacements(
      "[[radar:evidence=3]]\n\nx [[radar:evidence=3]] [[radar:evidence=4]]",
      resolver({ 3: "unlinked", 4: "unplaced" }),
    );
    expect(lost.lostItems).toBe(2);
    expect(lost.byIndex.get(3)).toEqual({ kind: "lost", reason: "unlinked" });
  });
});

describe("AnalysisStory", () => {
  const render = (report: string, open = false) =>
    renderToStaticMarkup(
      <AnalysisStory
        report={report}
        resolveItem={resolver({
          0: target(0, "crash"),
          1: target(1, "chart"),
          2: "unlinked",
        })}
        renderPlacement={(t, { compact }) => (
          <div data-placed={t.domId} data-compact={compact ? "1" : "0"}>
            {t.item.observation?.title}
          </div>
        )}
        onReveal={vi.fn()}
        defaultOpen={open}
      />,
    );

  it("previews the paragraph before the first placed card plus that card, compact, and nothing after", () => {
    const html = render(
      "Intro paragraph.\n\nThe container dies on start.\n\n[[radar:evidence=0]]\n\nThen a second thought.\n\n[[radar:evidence=1]]",
    );
    expect(html).toContain("The container dies on start.");
    expect(html).toContain('data-placed="story-group-crash"');
    expect(html).toContain('data-compact="1"');
    expect(html).not.toContain("Intro paragraph.");
    expect(html).not.toContain("Then a second thought.");
    expect(html).not.toContain("story-group-chart");
    expect(html).toContain("Read the full analysis");
    expect(html).toContain('data-story-open="false"');
  });

  it("opens to the whole story with full cards, reference chips and visible lost support", () => {
    const html = render(
      "Intro.\n\n[[radar:evidence=0]]\n\nSee [[radar:evidence=0]] and [[radar:evidence=2]].",
      true,
    );
    expect(html).toContain("Intro.");
    expect(html).toContain('data-compact="0"');
    expect(html).toContain("Show Card crash");
    expect(html).toContain('data-story-lost-support="unlinked"');
    expect(html).toContain("1 cited result could not be shown here.");
    expect(html).toContain("Show less");
  });

  it("does not offer a fold when everything already fits the preview", () => {
    const html = render("One paragraph only.");
    expect(html).toContain("One paragraph only.");
    expect(html).not.toContain("Read the full analysis");
    expect(html).toContain('data-story-open="true"');
  });
});
