// @vitest-environment jsdom
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type {
  DiagnoseStreamEvent,
  Diagnosis,
  RunSummary,
} from "../../api/diagnose";
import events from "./exampleInvestigation.events.json";
import {
  EXAMPLE_INVESTIGATION_RUN,
  exampleInvestigationOffered,
} from "./exampleInvestigation";
import { isInvestigationEvidenceRef } from "./investigationEvidence/identity";
import { projectInvestigationEvidence } from "./investigationEvidence";
import { resolveInvestigationCase } from "./investigationCase";
import { upsertTool, type Turn } from "./parts";
import { InvestigationView } from "./InvestigationView";

vi.mock("./DiagnoseContext", () => ({
  useDiagnose: () => ({
    refreshRuns: vi.fn(),
    openInvestigation: vi.fn(),
    openQuestion: vi.fn(),
    startError: null,
    startRefusal: null,
    dismissError: vi.fn(),
    // An agent that could apply and explain: the example must still offer neither.
    agents: [
      {
        name: "claude",
        label: "Claude Code",
        path: "",
        version: "",
        present: true,
        supported: true,
        apply: true,
        verification: true,
        assessmentExplanations: true,
      },
    ],
  }),
}));
vi.mock("../../context/ThemeContext", () => ({
  useTheme: () => ({ theme: "light", setTheme: vi.fn(), toggleTheme: vi.fn() }),
}));
vi.mock("../ui/Tooltip", () => ({
  Tooltip: ({ children }: { children: ReactNode }) => children,
}));

const transcript = events as unknown as DiagnoseStreamEvent[];
const fixture = JSON.stringify(events) + JSON.stringify(EXAMPLE_INVESTIGATION_RUN);

describe("the bundled example's data", () => {
  it("carries none of the values it was recorded with", () => {
    for (const leaked of [
      "radar-test",
      "nonprod",
      "wg-",
      "+03:00",
      "10.0.27.",
      "toolu_01",
      "sessionId",
      "costUsd",
      "ownerPid",
    ]) {
      expect(fixture).not.toContain(leaked);
    }
    expect(fixture).not.toMatch(/\b\d{12}\b/);
    expect(fixture).not.toMatch(/[\w.+-]+@[\w-]+\.[\w.]+/);
  });

  it("ends in a complete verdict whose citations all land on a card", () => {
    let timeline: Turn["timeline"] = [];
    let diagnosis: Diagnosis | undefined;
    for (const event of transcript) {
      if (event.type === "step" && event.step)
        timeline = upsertTool(timeline, event.step, false);
      if (event.type === "done") diagnosis = event.diagnosis;
    }
    expect(transcript.some((event) => event.type === "error")).toBe(false);
    expect(diagnosis?.summary).toBeTruthy();
    const refs = timeline.flatMap((item) =>
      item.kind === "tool" && item.evidenceRef ? [item.evidenceRef] : [],
    );
    expect(refs).toHaveLength(2);
    for (const ref of refs) expect(isInvestigationEvidenceRef(ref)).toBe(true);
    for (const ref of diagnosis!.rootCauseEvidence!.refs!)
      expect(refs).toContain(ref);

    const projection = projectInvestigationEvidence(
      [{ timeline }],
      EXAMPLE_INVESTIGATION_RUN,
    );
    const investigationCase = resolveInvestigationCase(
      projection,
      diagnosis!,
      0,
    );
    expect(investigationCase.items).toHaveLength(diagnosis!.evidence!.length);
    for (const item of investigationCase.items)
      expect(item.placement).toBe("card");
  });
});

describe("exampleInvestigationOffered", () => {
  const run = (patch: Partial<RunSummary> = {}): RunSummary => ({
    ...EXAMPLE_INVESTIGATION_RUN,
    id: "run-1",
    ...patch,
  });
  const offered = (
    runs: RunSummary[],
    { runsLoaded = true, setupPending = false, needsConsent = false } = {},
  ) =>
    exampleInvestigationOffered({ runs, runsLoaded, setupPending, needsConsent });

  it("is offered only while the user has no investigations of their own", () => {
    expect(offered([])).toBe(true);
    expect(offered([run()])).toBe(false);
    expect(offered([run({ ownedByMe: true })])).toBe(false);
    expect(offered([run()], { runsLoaded: false, setupPending: true })).toBe(
      false,
    );
  });

  it("does not count automatic runs or runs others shared", () => {
    expect(
      offered([run({ trigger: "background" }), run({ ownedByMe: false })]),
    ).toBe(true);
  });

  it("waits for the history before claiming it is empty", () => {
    expect(offered([], { runsLoaded: false })).toBe(false);
  });

  it("is offered on the setup step, where no history is fetched", () => {
    expect(offered([], { runsLoaded: false, setupPending: true })).toBe(true);
  });

  it("is not offered while a start waits for consent", () => {
    expect(offered([], { needsConsent: true })).toBe(false);
  });
});

describe("InvestigationView showing the example", () => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  let root: Root;
  let container: HTMLDivElement;
  const fetchMock = vi.fn();

  beforeEach(() => {
    vi.stubGlobal("fetch", fetchMock);
    // jsdom has no EventSource: a server subscription would throw.
    vi.stubGlobal(
      "ResizeObserver",
      class {
        observe() {}
        unobserve() {}
        disconnect() {}
      },
    );
    vi.stubGlobal("matchMedia", (query: string) => ({
      matches: false,
      media: query,
      addEventListener() {},
      removeEventListener() {},
      addListener() {},
      removeListener() {},
    }));
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.unstubAllGlobals();
    fetchMock.mockReset();
  });

  async function renderExample(onCloseExample = vi.fn()) {
    const client = new QueryClient();
    await act(async () => {
      root.render(
        <QueryClientProvider client={client}>
          <MemoryRouter>
            <InvestigationView
              run={EXAMPLE_INVESTIGATION_RUN}
              agentLabel="Claude Code"
              maximized
              example
              onCloseExample={onCloseExample}
            />
          </MemoryRouter>
        </QueryClientProvider>,
      );
    });
    await vi.waitFor(() =>
      expect(container.textContent).toContain(
        "A direct edit set guestbook-ui to a nonexistent image tag",
      ),
    );
    return onCloseExample;
  }

  it("labels it as an example and renders its findings from the bundle", async () => {
    await renderExample();
    const label = container.querySelector("[data-investigation-example]");
    expect(label?.textContent).toContain("Example");
    expect(label?.textContent).toContain("test cluster");
    expect(container.textContent).toContain("Next steps");
    // Nothing about the example is read from the server or the cluster.
    expect(
      fetchMock.mock.calls
        .map((call) => String(call[0]))
        .filter((url) => /diagnose|resources|guestbook/.test(url)),
    ).toEqual([]);
  });

  it("offers nothing that acts on it or sends it anywhere", async () => {
    await renderExample();
    expect(container.querySelector("textarea")).toBeNull();
    const buttons = [...container.querySelectorAll("button")].map(
      (button) =>
        `${button.textContent ?? ""} ${button.getAttribute("aria-label") ?? ""}`,
    );
    for (const label of buttons) {
      expect(label).not.toMatch(
        /copy|apply|send|follow-up|investigate again|check status|explain/i,
      );
    }
  });

  it("closes from its label", async () => {
    const onClose = await renderExample();
    const close = [...container.querySelectorAll("button")].find(
      (button) => button.textContent === "Close example",
    );
    act(() => close!.click());
    expect(onClose).toHaveBeenCalledOnce();
  });
});
