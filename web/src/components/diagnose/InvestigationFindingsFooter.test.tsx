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

it("opens the original Kubernetes read after a second read updates its Findings card", async () => {
  const findings = render();
  const activity = container.querySelector<HTMLElement>(
    "[data-investigation-activity-scroll]",
  )!;
  activity.scrollTo = vi.fn();
  const newerRef = "ev_sampleinvestigationaaaaaaa_seconddeploymentreadbaaaaa";
  function read(id: string, ref: string, version: string): DiagnoseStreamEvent {
    return {
      type: "step",
      step: {
        id,
        tool: "get_resource",
        status: "done",
        isError: false,
        radarEvidence: true,
        evidenceRef: ref,
        result: JSON.stringify({
          apiVersion: "apps/v1",
          kind: "Deployment",
          metadata: {
            name: run.name,
            namespace: run.namespace,
            resourceVersion: version,
          },
          status: { readyReplicas: 2, replicas: 3 },
        }),
      },
    };
  }
  replay([
    { type: "turn" },
    read("initial-read", evidenceRef, "before-change"),
    { type: "done" },
    { type: "turn", question: "Check the current workload", verify: true },
    read("verified-read", newerRef, "after-change"),
    { type: "done" },
  ]);
  act(() => footer.evidenceLink(evidenceRef)!());
  await vi.waitFor(() => {
    expect(document.activeElement?.id).toBe(
      "investigation-activity-turn-0-step-initial-read",
    );
    expect(document.activeElement?.textContent).toContain("before-change");
    expect(document.activeElement?.textContent).not.toContain("after-change");
  });
  act(() => footer.evidenceLink(newerRef)!());
  await vi.waitFor(() => {
    expect(findings.contains(document.activeElement)).toBe(true);
    expect(document.activeElement?.hasAttribute("data-evidence-card")).toBe(
      true,
    );
  });
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
    for (const [i, event] of events.slice(1).entries())
      stream.onEvent(event, i + 2);
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

it("expands Captured results and focuses an unplaced main read in a story-shaped investigation", async () => {
  const findings = render();
  const proposalRef = "ev_sampleinvestigationaaaaaaa_proposalgetresourcereadaaa";
  const proposalRead: DiagnoseStreamEvent = {
    type: "step",
    step: {
      id: "proposal-read",
      tool: "get_resource",
      status: "done",
      isError: false,
      radarEvidence: true,
      evidenceRef: proposalRef,
      result: JSON.stringify({
        apiVersion: "apps/v1",
        kind: "Deployment",
        metadata: {
          name: run.name,
          namespace: run.namespace,
          resourceVersion: "42",
        },
        status: { readyReplicas: 1, replicas: 2 },
      }),
    },
  };
  replay([...events.slice(0, -1), proposalRead, events[events.length - 1]]);

  const resultsToggle = findings.querySelector<HTMLButtonElement>(
    'button[aria-controls="investigation-captured-results"]',
  )!;
  const resultsPanel = findings.querySelector<HTMLElement>(
    "#investigation-captured-results",
  )!;
  const targetCard = resultsPanel.querySelector<HTMLElement>(
    '[data-evidence-source="turn-0-step-proposal-read"]',
  )!;
  expect(resultsToggle.getAttribute("aria-expanded")).toBe("false");
  expect(targetCard).toBeTruthy();
  expect(targetCard.closest("[inert]")).not.toBeNull();

  const open = footer.evidenceLink(proposalRef);
  expect(open).toEqual(expect.any(Function));
  act(() => open!());

  await vi.waitFor(() => {
    expect(resultsToggle.getAttribute("aria-expanded")).toBe("true");
    expect(targetCard.closest("[inert]")).toBeNull();
    expect(document.activeElement).toBe(targetCard);
    expect(document.activeElement?.hasAttribute("data-evidence-card")).toBe(
      true,
    );
  });
  expect(resultsPanel.contains(document.activeElement)).toBe(true);
});

it("preserves visible story cards and focuses the non-inert Captured results card when a story duplicate is inert", async () => {
  const findings = render();
  replay();
  const placedRef = "ev_sampleinvestigationaaaaaaa_applicationdiagnosebundlea";
  const resultsToggle = findings.querySelector<HTMLButtonElement>(
    'button[aria-controls="investigation-captured-results"]',
  )!;
  const resultsPanel = findings.querySelector<HTMLElement>(
    "#investigation-captured-results",
  )!;
  const story = findings.querySelector<HTMLElement>("[data-story]")!;
  const storyToggle = findings.querySelector<HTMLButtonElement>(
    "[data-story] > button[aria-expanded]",
  )!;

  act(() => storyToggle.click());
  expect(story.getAttribute("data-story-open")).toBe("true");
  expect(resultsToggle.getAttribute("aria-expanded")).toBe("false");

  const open = footer.evidenceLink(placedRef);
  expect(open).toEqual(expect.any(Function));
  act(() => open!());

  await vi.waitFor(() => {
    expect(story.contains(document.activeElement)).toBe(true);
    expect(document.activeElement?.id.startsWith("story-")).toBe(true);
    expect(document.activeElement?.closest("[inert]")).toBeNull();
  });
  const storyCard = document.activeElement as HTMLElement;
  expect(resultsToggle.getAttribute("aria-expanded")).toBe("false");

  act(() => storyToggle.click());
  expect(story.getAttribute("data-story-open")).toBe("false");
  expect(story.contains(storyCard)).toBe(true);
  expect(storyCard.closest("[inert]")).not.toBeNull();

  act(() => footer.evidenceLink(placedRef)!());
  await vi.waitFor(() => {
    expect(resultsToggle.getAttribute("aria-expanded")).toBe("true");
    expect(resultsPanel.contains(document.activeElement)).toBe(true);
    expect(document.activeElement).not.toBe(storyCard);
    expect(document.activeElement?.hasAttribute("data-evidence-card")).toBe(
      true,
    );
    expect(document.activeElement?.closest("[inert]")).toBeNull();
  });
});
