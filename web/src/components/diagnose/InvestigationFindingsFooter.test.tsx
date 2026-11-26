// @vitest-environment jsdom
import { act, type ReactNode } from "react";
import { createRoot } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  subscribeRun,
  type DiagnoseStreamEvent,
  type RunSummary,
  type SubscribeHandlers,
} from "../../api/diagnose";
import {
  DiagnoseCustomizationProvider,
  type RenderInvestigationFindingsFooter,
} from "../../context/DiagnoseCustomization";
import { EXAMPLE_INVESTIGATION_RUN } from "./exampleInvestigation";
import transcript from "./exampleInvestigation.events.json";
import { InvestigationView } from "./InvestigationView";

vi.mock("../../api/diagnose", async (original) => ({
  ...(await original<typeof import("../../api/diagnose")>()),
  subscribeRun: vi.fn(),
}));
vi.mock("./DiagnoseContext", () => ({
  useDiagnose: () => ({
    refreshRuns: vi.fn(),
    agents: [{ name: "claude", hosted: true, assessmentExplanations: true }],
  }),
}));
vi.mock("../../context/ThemeContext", () => ({
  useTheme: () => ({ theme: "light" }),
}));
vi.mock("../ui/Tooltip", () => ({
  Tooltip: ({ children }: { children: ReactNode }) => children,
}));

const events = transcript as unknown as DiagnoseStreamEvent[];
const evidenceRef = events.find((e) => e.step?.evidenceRef)?.step?.evidenceRef;
if (!evidenceRef) throw new Error("The example transcript needs a cited read");
const run = {
  ...EXAMPLE_INVESTIGATION_RUN,
  id: "hosted-run",
  ownedByMe: false,
  visibility: "organization" as const,
  canContinue: true,
};
let container: HTMLDivElement;
let root: ReturnType<typeof createRoot>;
let stream: SubscribeHandlers;
let footer: Parameters<RenderInvestigationFindingsFooter>[0];
const scrollTo = vi.fn();
const renderFooter: RenderInvestigationFindingsFooter = (props) => {
  footer = props;
  return (
    <article aria-label="Proposed change" tabIndex={-1}>
      Review change
    </article>
  );
};
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      disconnect() {}
    },
  );
  vi.stubGlobal("matchMedia", () => ({
    matches: true,
    addEventListener() {},
    removeEventListener() {},
  }));
  vi.mocked(subscribeRun).mockImplementation((_id, handlers) => {
    stream = handlers;
    return () => {};
  });
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});
afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.unstubAllGlobals();
});

function render(canContinue = true, summary: Partial<RunSummary> = {}) {
  act(() =>
    root.render(
      <QueryClientProvider client={new QueryClient()}>
        <MemoryRouter>
          <DiagnoseCustomizationProvider
            value={undefined}
            renderFindingsFooter={renderFooter}
          >
            <InvestigationView
              run={{ ...run, canContinue, ...summary }}
              agentLabel="Hosted"
              maximized={false}
            />
          </DiagnoseCustomizationProvider>
        </MemoryRouter>
      </QueryClientProvider>,
    ),
  );
  const findings = container.querySelector<HTMLElement>(
    "[data-investigation-findings-scroll]",
  )!;
  scrollTo.mockClear();
  findings.scrollTo = scrollTo;
  return findings;
}

function replay(saved: DiagnoseStreamEvent[] = events) {
  act(() => {
    for (const [i, event] of saved.entries()) stream.onEvent(event, i + 1);
    stream.onEvent({ type: "replay_complete" });
  });
}

it("waits for the replay boundary before exposing a landing target", () => {
  const findings = render();
  for (const [i, event] of events.entries()) {
    act(() => stream.onEvent(event, i + 1));
    expect(findings.querySelector("[aria-label='Proposed change']")).toBeNull();
  }
  act(() => stream.onEvent({ type: "replay_complete" }));
  expect(findings.querySelector("[aria-label='Proposed change']")).toBeTruthy();
});

it("lands on the resolved card in Findings and returns to the actual assessment and composer", () => {
  const findings = render();
  expect(
    findings.querySelector("article[aria-label='Proposed change']"),
  ).toBeNull();
  replay();
  const card = findings.querySelector<HTMLElement>(
    "[aria-label='Proposed change']",
  )!;
  const assessment = findings.querySelector<HTMLElement>(
    ".investigation-assessment",
  )!;
  expect(
    assessment.compareDocumentPosition(card) & Node.DOCUMENT_POSITION_FOLLOWING,
  ).toBeTruthy();
  expect(scrollTo).not.toHaveBeenCalled();
  act(() => footer.askQuestion!());
  expect(document.activeElement?.tagName).toBe("TEXTAREA");
  act(() => footer.revealInFindings(card));
  expect(document.activeElement).toBe(card);
  expect(findings.parentElement?.classList.contains("hidden")).toBe(false);
  expect(scrollTo).toHaveBeenCalledTimes(1);
  act(() => footer.viewAssessment!());
  expect(document.activeElement).toBe(assessment);
  expect(footer.evidenceLink(evidenceRef)).toEqual(expect.any(Function));
  expect(footer.evidenceLink("unrelated")).toBeUndefined();
});

it("opens the precise raw tool result when Findings has no card for its read", async () => {
  render();
  const original = events.find(
    (event) => event.step?.evidenceRef === evidenceRef,
  )!;
  const step = {
    ...original.step!,
    tool: "read_cluster_provider",
    id: "raw-receipt",
  };
  replay([{ type: "turn" }, { type: "step", step }, { type: "done" }]);
  const activity = container.querySelector<HTMLElement>(
    "[data-investigation-activity-scroll]",
  )!;
  activity.scrollTo = vi.fn();
  const open = footer.evidenceLink(evidenceRef);
  expect(open).toEqual(expect.any(Function));
  act(() => open!());
  await vi.waitFor(() => {
    expect(document.activeElement?.id).toBe(
      "investigation-activity-turn-0-step-raw-receipt",
    );
  });
  expect(activity.contains(document.activeElement)).toBe(true);
});

it("keeps a proposal bound to its earlier read and withholds ambiguous references", () => {
  render();
  const original = events.find(
    (event) => event.step?.evidenceRef === evidenceRef,
  )!;
  const followUp: DiagnoseStreamEvent = {
    type: "turn",
    question: "Check another resource",
  };
  replay([...events, followUp]);
  expect(footer.evidenceLink(evidenceRef)).toEqual(expect.any(Function));
  act(() =>
    stream.onEvent({
      ...original,
      step: { ...original.step!, id: "duplicate" },
    }),
  );
  expect(footer.evidenceLink(evidenceRef)).toBeUndefined();
});

it("keeps read-only proposals in Findings without exposing a follow-up action", () => {
  const findings = render(false);
  replay();
  expect(findings.textContent).toContain("Review change");
  expect(footer.askQuestion).toBeUndefined();
});

it("lets an assigned reader see the private proposal and its evidence through a running turn and replay", () => {
  const explanation = "Only the investigator can ask follow-up questions.";
  const findings = render(false, {
    visibility: "private",
    status: "running",
    ownedByMe: false,
    continueBlockedReason: explanation,
  });
  replay([{ type: "turn" }]);
  expect(container.textContent).not.toContain("Stop agent");
  expect(footer.askQuestion).toBeUndefined();
  // The server's run summary still says running after the terminal event.
  act(() => {
    for (const [i, event] of events.slice(1).entries()) stream.onEvent(event, i + 2);
  });
  expect(findings.querySelector("[aria-label='Proposed change']")).toBeTruthy();
  expect(footer.evidenceLink(evidenceRef)).toEqual(expect.any(Function));
  expect(container.textContent).toContain(explanation);
  expect(container.querySelector("textarea")).toBeNull();
  expect(findings.textContent).not.toContain("Explain simply");
  expect(footer.askQuestion).toBeUndefined();
});

it("can land on a proposal even when saved evidence is permanently unavailable", () => {
  const findings = render();
  act(() =>
    stream.onEvent({
      type: "history_unavailable",
      error: "Could not restore history",
      retryable: false,
    }),
  );
  expect(findings.textContent).toContain("Saved evidence unavailable");
  const card = findings.querySelector<HTMLElement>(
    "[aria-label='Proposed change']",
  )!;
  act(() => footer.revealInFindings(card));
  expect(document.activeElement).toBe(card);
  expect(footer.viewAssessment).toBeUndefined();
  expect(footer.evidenceLink(evidenceRef)).toBeUndefined();
  expect(footer.askQuestion).toBeUndefined();
});
