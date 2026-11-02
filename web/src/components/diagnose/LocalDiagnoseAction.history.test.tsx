// @vitest-environment jsdom
import { act, useEffect } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { RunSummary } from "../../api/diagnose";
import { DiagnoseProvider, useDiagnose } from "./DiagnoseContext";
import { defaultDiagnoseAction } from "./LocalDiagnoseAction";

const api = vi.hoisted(() => ({
  runs: [] as RunSummary[],
  hosted: false,
  listRuns: vi.fn(),
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
