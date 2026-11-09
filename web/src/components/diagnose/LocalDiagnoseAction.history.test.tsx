// @vitest-environment jsdom
import { act, useEffect } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { RunSummary } from "../../api/diagnose";
import { DiagnoseProvider, useDiagnose } from "./DiagnoseContext";
import {
  defaultDiagnoseAction,
  IssueDiagnoseButton,
} from "./LocalDiagnoseAction";

const api = vi.hoisted(() => ({
  runs: [] as RunSummary[],
  hosted: false,
  listRuns: vi.fn(),
  recordUsageEvent: vi.fn(),
}));

vi.mock("../../api/usage-data", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  recordUsageEvent: api.recordUsageEvent,
}));

vi.mock("../../api/diagnose", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  fetchAgents: async () => ({
    enabled: true,
    eligible: true,
    consented: { "claude:safeguarded": true },
    agents: [
      {
        name: "claude",
        label: "Claude Code",
        path: "/bin/claude",
        version: "",
        present: true,
        supported: true,
        hosted: api.hosted,
        questions: true,
        profiles: ["safeguarded"],
        consentSurfaces: { safeguarded: "claude:safeguarded" },
      },
    ],
  }),
  listRuns: api.listRuns,
  getRun: vi.fn(() => new Promise(() => {})),
}));

vi.mock("../../api/client", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  useContexts: () => ({ data: [{ name: "cluster-a", isCurrent: true }] }),
}));

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
let root: Root;
const probe = {} as { d: ReturnType<typeof useDiagnose> };

function Probe() {
  const d = useDiagnose();
  useEffect(() => {
    probe.d = d;
  });
  return null;
}

const flush = () =>
  act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });

const ago = (h: number) => new Date(Date.now() - h * 3600_000).toISOString();
const investigated = (over: Partial<RunSummary> = {}): RunSummary => ({
  id: "run-cart",
  kind: "Deployment",
  group: "apps",
  namespace: "shop",
  name: "cart",
  context: "cluster-a",
  status: "done",
  preview: "Pods crash on a bad flag",
  assessedAt: ago(2),
  createdAt: ago(3),
  updatedAt: ago(2),
  ...over,
});

async function renderButton(
  health: "problem" | "healthy" = "problem",
  createdAt?: string,
) {
  await act(async () =>
    root.render(
      <DiagnoseProvider browserURLState={false}>
        <Probe />
        {defaultDiagnoseAction({
          kind: "Deployment",
          group: "apps",
          namespace: "shop",
          name: "cart",
          health,
          createdAt,
        })}
      </DiagnoseProvider>,
    ),
  );
  await flush();
  await flush();
}

const trigger = () =>
  document.querySelector<HTMLButtonElement>("button[aria-label]")!;
const dialog = () => document.querySelector('[role="dialog"]');
const buttonNamed = (text: string) =>
  [...document.querySelectorAll("button")].find((b) => b.textContent === text)!;

beforeEach(() => {
  api.runs = [];
  api.hosted = false;
  api.listRuns.mockReset();
  api.listRuns.mockImplementation(async () => ({ runs: api.runs }));
  localStorage.clear();
  const element = document.createElement("div");
  document.body.appendChild(element);
  root = createRoot(element);
});

afterEach(async () => {
  await act(async () => root.unmount());
  document.body.replaceChildren();
});

describe("a resource's last investigation", () => {
  it("shows what the earlier investigation found before starting another", async () => {
    api.runs = [investigated()];
    await renderButton("problem");
    const openRun = vi.spyOn(probe.d, "openRun");
    const openInvestigation = vi.spyOn(probe.d, "openInvestigation");
    await act(async () => trigger().click());
    expect(dialog()?.textContent).toContain("Pods crash on a bad flag");
    expect(openInvestigation).not.toHaveBeenCalled();
    await act(async () => buttonNamed("Open investigation").click());
    expect(openRun).toHaveBeenCalledWith("run-cart");
  });

  it("marks the button with a filled sparkle only when there is history", async () => {
    await renderButton("problem");
    expect(trigger().querySelector("svg")?.getAttribute("fill")).toBe("none");
    api.runs = [investigated()];
    await act(async () => window.dispatchEvent(new Event("focus")));
    await flush();
    expect(trigger().querySelector("svg")?.getAttribute("fill")).toBe(
      "currentColor",
    );
  });

  it("keeps the usual action one click further on", async () => {
    api.runs = [investigated()];
    await renderButton("problem");
    const openInvestigation = vi.spyOn(probe.d, "openInvestigation");
    await act(async () => trigger().click());
    await act(async () => buttonNamed("Investigate again").click());
    expect(openInvestigation).toHaveBeenCalledWith({
      kind: "Deployment",
      group: "apps",
      namespace: "shop",
      name: "cart",
    });
  });

  it("offers a question on a healthy resource", async () => {
    api.runs = [investigated({ preview: "Healthy" })];
    await renderButton("healthy");
    const openQuestion = vi.spyOn(probe.d, "openQuestion");
    await act(async () => trigger().click());
    expect(dialog()?.textContent).toContain("Healthy");
    await act(async () => buttonNamed("Ask a question").click());
    expect(openQuestion).toHaveBeenCalled();
  });

  it.each([
    ["the object was recreated since", () => [investigated()], ago(1)],
    [
      "it ran on another cluster",
      () => [investigated({ context: "cluster-b" })],
      undefined,
    ],
  ])("stays out of the way when %s", async (_, runs, createdAt) => {
    api.runs = runs();
    await renderButton("problem", createdAt);
    const openInvestigation = vi.spyOn(probe.d, "openInvestigation");
    await act(async () => trigger().click());
    expect(dialog()).toBeNull();
    expect(openInvestigation).toHaveBeenCalled();
  });

  it("is off for hosted agents, which keep their own shared history", async () => {
    api.hosted = true;
    api.runs = [investigated()];
    await renderButton("problem");
    await act(async () => trigger().click());
    expect(dialog()).toBeNull();
  });

  it("never opens a live run on another cluster's same-named resource", async () => {
    api.runs = [
      investigated({
        status: "running",
        context: "cluster-b",
        assessedAt: undefined,
      }),
    ];
    await renderButton("problem");
    const openRun = vi.spyOn(probe.d, "openRun");
    await act(async () => trigger().click());
    expect(openRun).not.toHaveBeenCalled();
  });

  it("takes focus, and gives Escape back to the button without closing what's under it", async () => {
    api.runs = [investigated()];
    await renderButton("problem");
    const underneath = vi.fn();
    document.addEventListener("keydown", underneath);
    const button = trigger();
    await act(async () => button.click());
    await flush();
    expect(document.activeElement?.textContent).toBe("Open investigation");
    await act(async () =>
      document.activeElement!.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
      ),
    );
    expect(underneath).not.toHaveBeenCalled();
    expect(button.isConnected).toBe(true);
    expect(document.activeElement).toBe(button);
    document.removeEventListener("keydown", underneath);
  });

  it("still opens a hosted agent's live run, whose cluster label differs", async () => {
    api.hosted = true;
    api.runs = [
      investigated({
        status: "running",
        context: "Prod cluster",
        assessedAt: undefined,
      }),
    ];
    await renderButton("healthy");
    const openRun = vi.spyOn(probe.d, "openRun");
    await act(async () => trigger().click());
    expect(openRun).toHaveBeenCalledWith("run-cart");
  });

  it("ignores a list that resolves after a newer one", async () => {
    api.runs = [investigated()];
    await renderButton("problem");
    let resolveOld!: (v: { runs: RunSummary[] }) => void;
    api.listRuns.mockImplementationOnce(
      () => new Promise((resolve) => (resolveOld = resolve)),
    );
    await act(async () => window.dispatchEvent(new Event("focus")));
    api.runs = [];
    await act(async () => window.dispatchEvent(new Event("focus")));
    await flush();
    await act(async () => resolveOld({ runs: [investigated()] }));
    await flush();
    await act(async () => trigger().click());
    expect(dialog()).toBeNull();
  });

  it("doesn't reopen the panel by itself after a new run finishes", async () => {
    api.runs = [investigated()];
    await renderButton("problem");
    await act(async () => trigger().click());
    expect(dialog()).not.toBeNull();
    api.runs = [
      investigated({ id: "run-new", status: "running", assessedAt: undefined }),
      investigated(),
    ];
    await act(async () => probe.d.refreshRuns());
    await flush();
    api.runs = [
      investigated({ id: "run-new", assessedAt: new Date().toISOString() }),
      investigated(),
    ];
    await act(async () => probe.d.refreshRuns());
    await flush();
    await flush();
    expect(dialog()).toBeNull();
  });

  it("catches up when the window regains focus", async () => {
    await renderButton("problem");
    expect(dialog()).toBeNull();
    api.runs = [investigated()];
    await act(async () => window.dispatchEvent(new Event("focus")));
    await flush();
    await act(async () => trigger().click());
    expect(dialog()?.textContent).toContain("Pods crash on a bad flag");
  });
});

describe("an issue row's Investigate action", () => {
  const rowToggle = vi.fn();
  async function renderIssueRow(size: "default" | "compact" = "default") {
    rowToggle.mockReset();
    await act(async () =>
      root.render(
        <DiagnoseProvider browserURLState={false}>
          <Probe />
          <div role="button" onClick={rowToggle} className="overflow-hidden">
            <IssueDiagnoseButton
              kind="Deployment"
              group="apps"
              namespace="shop"
              name="cart"
              size={size}
            />
          </div>
        </DiagnoseProvider>,
      ),
    );
    await flush();
    await flush();
  }
  const issueButton = () =>
    [...document.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("Investigate"),
    )!;

  it.each(["default", "compact"] as const)(
    "shows the last investigation outside the row, without toggling it (%s)",
    async (size) => {
      api.runs = [investigated()];
      await renderIssueRow(size);
      const openRun = vi.spyOn(probe.d, "openRun");
      await act(async () => issueButton().click());
      // Portaled out of the row, which clips its contents.
      expect(dialog()?.parentElement).toBe(document.body);
      expect(dialog()?.textContent).toContain("Pods crash on a bad flag");
      await act(async () => buttonNamed("Open investigation").click());
      expect(openRun).toHaveBeenCalledWith("run-cart");
      expect(rowToggle).not.toHaveBeenCalled();
    },
  );

  const investigateEvent = { type: "ui", name: "issue_action:investigate" };

  it("counts starting an investigation", async () => {
    api.runs = [];
    await renderIssueRow();
    api.recordUsageEvent.mockClear();
    await act(async () => issueButton().click());
    expect(api.recordUsageEvent.mock.calls).toEqual([[investigateEvent]]);
  });

  it("counts investigating again, not opening the last investigation", async () => {
    api.runs = [investigated()];
    await renderIssueRow();
    api.recordUsageEvent.mockClear();
    await act(async () => issueButton().click());
    expect(api.recordUsageEvent).not.toHaveBeenCalled();
    await act(async () => buttonNamed("Investigate again").click());
    expect(api.recordUsageEvent.mock.calls).toEqual([[investigateEvent]]);
  });

  it("re-investigates from the panel", async () => {
    api.runs = [investigated()];
    await renderIssueRow();
    const openInvestigation = vi.spyOn(probe.d, "openInvestigation");
    await act(async () => issueButton().click());
    await act(async () => buttonNamed("Investigate again").click());
    expect(openInvestigation).toHaveBeenCalledWith({
      kind: "Deployment",
      group: "apps",
      namespace: "shop",
      name: "cart",
    });
    expect(rowToggle).not.toHaveBeenCalled();
  });

  it("starts straight away when there's nothing recent", async () => {
    api.runs = [];
    await renderIssueRow();
    const openInvestigation = vi.spyOn(probe.d, "openInvestigation");
    await act(async () => issueButton().click());
    expect(dialog()).toBeNull();
    expect(openInvestigation).toHaveBeenCalled();
  });

  it("tabs out of the panel back to the button", async () => {
    api.runs = [investigated()];
    await renderIssueRow();
    const button = issueButton();
    await act(async () => button.click());
    await flush();
    const last = buttonNamed("Investigate again");
    last.focus();
    await act(async () =>
      last.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Tab", bubbles: true }),
      ),
    );
    expect(document.activeElement).toBe(button);
    expect(dialog()?.getAttribute("inert")).not.toBeNull();
    expect(rowToggle).not.toHaveBeenCalled();
  });

  it("opens above a button near the bottom of the screen", async () => {
    api.runs = [investigated()];
    await renderIssueRow();
    const button = issueButton();
    const rect = vi
      .spyOn(HTMLElement.prototype, "getBoundingClientRect")
      .mockReturnValue({
        top: 700,
        bottom: 720,
        left: 900,
        right: 1000,
        width: 100,
        height: 20,
        x: 900,
        y: 700,
        toJSON: () => ({}),
      });
    const height = vi
      .spyOn(HTMLElement.prototype, "offsetHeight", "get")
      .mockReturnValue(150);
    vi.stubGlobal("innerHeight", 768);
    await act(async () => button.click());
    await flush();
    expect(dialog()!.getAttribute("style")).toContain("top: 544px");
    rect.mockRestore();
    height.mockRestore();
    vi.unstubAllGlobals();
  });

  it("closes when the list scrolls, keeping keyboard focus on the button", async () => {
    api.runs = [investigated()];
    await renderIssueRow();
    const button = issueButton();
    await act(async () => button.click());
    await flush();
    expect(document.activeElement?.textContent).toBe("Open investigation");
    await act(async () => document.dispatchEvent(new Event("scroll")));
    await flush();
    expect(dialog()?.getAttribute("inert")).not.toBeNull();
    expect(document.activeElement).toBe(button);
  });
});
