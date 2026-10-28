// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  AnalysisStory,
  resolveStoryPlacements,
  type StoryPlacementLoss,
  type StoryPlacementTarget,
} from "./AnalysisStory";
import type { InvestigationCaseItem } from "./investigationCase";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

function target(index: number, observationId: string): StoryPlacementTarget {
  return {
    item: {
      index,
      role: "context",
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

const table: Record<number, StoryPlacementTarget | StoryPlacementLoss> = {
  0: target(0, "crash"),
  1: target(1, "config"),
  2: "unlinked",
};

const REPORT =
  "**Why api is down:** the port is `abc`.\n\nThe log shows the check failing:\n\n[[radar:evidence=0]]\n\nThe value comes from the ConfigMap:\n\n[[radar:evidence=1]]\n\nOther causes don't fit. [[radar:evidence=2]]\n\n**The fix:** set a numeric port.";

let width = 0;
let resizeCallbacks: (() => void)[] = [];
let container: HTMLDivElement;
let root: Root;

class FakeResizeObserver {
  constructor(private callback: () => void) {}
  observe() {
    resizeCallbacks.push(this.callback);
  }
  disconnect() {
    resizeCallbacks = resizeCallbacks.filter((cb) => cb !== this.callback);
  }
}

beforeEach(() => {
  resizeCallbacks = [];
  vi.stubGlobal("ResizeObserver", FakeResizeObserver);
  vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockImplementation(
    () => width,
  );
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

function mount(at: number, report = REPORT) {
  width = at;
  const placements = resolveStoryPlacements(report, (index) =>
    index in table ? table[index] : "invalid",
  );
  act(() =>
    root.render(
      <AnalysisStory
        placements={placements}
        renderPlacement={(t, { compact }) => (
          <div data-placed={t.domId} data-compact={compact ? "1" : "0"}>
            {t.item.observation?.title}
          </div>
        )}
        onReveal={vi.fn()}
        trailing={<p data-trailing>Ruled out: an out-of-memory kill.</p>}
      />,
    ),
  );
}

function resize(to: number) {
  width = to;
  act(() => resizeCallbacks.forEach((cb) => cb()));
}

const story = () => container.querySelector("[data-story]")!;
const toggle = () =>
  container.querySelector<HTMLButtonElement>("button[aria-expanded]")!;
const isOpen = () => story().getAttribute("data-story-open") === "true";
// Collapsed regions stay mounted once opened; what the reader can reach is
// what no inert ancestor hides.
const reachable = (selector: string) =>
  [...container.querySelectorAll(selector)].filter(
    (element) => !element.closest("[inert]"),
  );

describe("AnalysisStory at reading width", () => {
  it("opens the whole story with full cards and the ruled-out block when there is room", () => {
    mount(900);
    expect(isOpen()).toBe(true);
    expect(container.textContent).toContain("Why api is down:");
    expect(container.textContent).toContain("The fix:");
    expect(
      reachable("[data-placed]").map((el) => el.getAttribute("data-compact")),
    ).toEqual(["0", "0"]);
    expect(reachable("[data-trailing]")).toHaveLength(1);
    expect(reachable('[data-story-lost-support="unlinked"]')).toHaveLength(1);
    expect(container.textContent).toContain(
      "1 cited result could not be shown here.",
    );
    expect(toggle().textContent).toBe("Show less");
    expect(container.querySelector("[data-story-teaser]")).toBeNull();
  });

  it("keeps a bounded preview in a narrow panel, naming the real card count", () => {
    mount(500);
    expect(isOpen()).toBe(false);
    expect(toggle().textContent).toBe("Read full analysis · 2 evidence cards");
    expect(
      reachable("[data-placed]").map((el) => el.getAttribute("data-compact")),
    ).toEqual(["1"]);
    expect(reachable("[data-trailing]")).toHaveLength(0);
    // The fold never hides that a citation failed.
    expect(container.textContent).toContain(
      "1 cited result could not be shown here.",
    );
  });

  it("follows the width until the reader chooses, without flapping at the edge", () => {
    mount(500);
    resize(700);
    expect(isOpen()).toBe(true);
    // Opening can bring a scrollbar that narrows the pane a little.
    resize(620);
    expect(isOpen()).toBe(true);
    resize(500);
    expect(isOpen()).toBe(false);
  });

  it("stays open when narrowing would fold away the element holding focus", () => {
    mount(900);
    const trailing = container.querySelector<HTMLElement>("[data-trailing]")!;
    trailing.tabIndex = -1;
    act(() => trailing.focus());
    expect(document.activeElement).toBe(trailing);
    resize(500);
    expect(isOpen()).toBe(true);
    expect(trailing.closest("[inert]")).toBeNull();
    // That counts as the reader's choice: it no longer follows the width.
    resize(450);
    expect(isOpen()).toBe(true);
  });

  it("still folds for width when focus is on something the fold keeps visible", () => {
    mount(900);
    act(() => toggle().focus());
    resize(500);
    expect(isOpen()).toBe(false);
    resize(900);
    expect(isOpen()).toBe(true);
  });

  it("keeps the reader's fold choice whatever the width does", () => {
    mount(900);
    act(() => toggle().click());
    expect(isOpen()).toBe(false);
    resize(1000);
    expect(isOpen()).toBe(false);

    act(() => toggle().click());
    resize(450);
    expect(isOpen()).toBe(true);
    expect(toggle().textContent).toBe("Show less");
  });

  it("keeps the reader's choice when the story is re-rendered with new evidence", () => {
    mount(500);
    act(() => toggle().click());
    expect(isOpen()).toBe(true);
    mount(500, `${REPORT}\n\nA later note.`);
    expect(isOpen()).toBe(true);
    expect(container.textContent).toContain("A later note.");
  });
});
