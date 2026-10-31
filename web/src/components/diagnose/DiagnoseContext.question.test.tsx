// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { DiagnoseError } from "../../api/diagnose";
import {
  DiagnoseProvider,
  useDiagnose,
  useDiagnoseLayout,
} from "./DiagnoseContext";
import { runTargetKey } from "./target";

const api = vi.hoisted(() => ({
  createRun: vi.fn(),
  recordConsent: vi.fn(),
  consented: {} as Record<string, boolean>,
  runs: [] as unknown[],
}));

vi.mock("../../api/diagnose", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  fetchAgents: async () => ({
    enabled: true,
    eligible: true,
    consented: api.consented,
    agents: [
      {
        name: "claude",
        label: "Claude Code",
        path: "/bin/claude",
        version: "",
        present: true,
        supported: true,
        profiles: ["safeguarded"],
        consentSurfaces: { safeguarded: "claude:safeguarded" },
        questions: true,
      },
    ],
  }),
  listRuns: async () => ({ runs: api.runs }),
  getRun: vi.fn(),
  createRun: api.createRun,
  recordConsent: api.recordConsent,
}));

const cluster = vi.hoisted(() => ({ name: "cluster-a" as string | undefined }));
vi.mock("../../api/client", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  useContexts: () => ({
    data: cluster.name ? [{ name: cluster.name, isCurrent: true }] : undefined,
  }),
}));

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
let root: Root;
let diagnose: ReturnType<typeof useDiagnose>;
let layout: ReturnType<typeof useDiagnoseLayout>;

function capture(seenDiagnose: typeof diagnose, seenLayout: typeof layout) {
  diagnose = seenDiagnose;
  layout = seenLayout;
}

function Probe() {
  capture(useDiagnose(), useDiagnoseLayout());
  return null;
}

const flush = () =>
  act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });

const question = {
  kind: "",
  group: "",
  namespace: "",
  name: "",
  question: "checkout returns 502s",
  namespaces: ["shop"],
  lookBack: "1h",
  fromComposer: true,
};

beforeEach(async () => {
  api.createRun.mockReset();
  api.recordConsent.mockReset();
  api.consented = {};
  api.runs = [];
  cluster.name = "cluster-a";
  localStorage.clear();
  const element = document.createElement("div");
  document.body.appendChild(element);
  root = createRoot(element);
  await act(async () =>
    root.render(
      <DiagnoseProvider browserURLState={false}>
        <Probe />
      </DiagnoseProvider>,
    ),
  );
  await flush();
});

afterEach(async () => {
  await act(async () => root.unmount());
  document.body.replaceChildren();
});

describe("starting from a question", () => {
  it("never starts a run whose consent was cancelled while being recorded", async () => {
    let recorded!: () => void;
    api.recordConsent.mockReturnValue(
      new Promise<void>((resolve) => (recorded = resolve)),
    );
    await act(async () => diagnose.openInvestigation(question));
    expect(diagnose.needsConsent).toBe(true);
    await act(async () => diagnose.approveConsent());
    await act(async () => diagnose.cancelConsent());
    await act(async () => recorded());
    await flush();
    expect(api.createRun).not.toHaveBeenCalled();
    expect(diagnose.questionDraft?.text).toBe("checkout returns 502s");
  });

  it("gives a refused start's question back to the composer", async () => {
    api.consented = { "claude:safeguarded": true };
    await act(async () => diagnose.recheckAgents());
    api.createRun.mockRejectedValue(
      new DiagnoseError(409, "too many investigations running"),
    );
    await act(async () => diagnose.openInvestigation(question));
    await flush();
    expect(diagnose.startError).toContain("too many");
    expect(diagnose.questionDraft).toMatchObject({
      text: "checkout returns 502s",
      namespaces: ["shop"],
      restore: true,
    });
  });

  it("leaves a saved draft alone when a run is started from elsewhere", async () => {
    api.consented = { "claude:safeguarded": true };
    await act(async () => diagnose.recheckAgents());
    await act(async () => diagnose.saveQuestionDraft({ text: "draft B" }));
    api.createRun.mockRejectedValue(new DiagnoseError(409, "refused"));
    await act(async () =>
      diagnose.openInvestigation({ ...question, fromComposer: undefined }),
    );
    await flush();
    expect(diagnose.questionDraft?.text).toBe("draft B");
  });

  it("hands back an explicit all-namespaces choice as it was", async () => {
    api.consented = { "claude:safeguarded": true };
    await act(async () => diagnose.recheckAgents());
    api.createRun.mockRejectedValue(new DiagnoseError(409, "refused"));
    await act(async () =>
      diagnose.openInvestigation({ ...question, namespaces: [] }),
    );
    await flush();
    expect(diagnose.questionDraft?.namespaces).toEqual([]);
  });

  it("points a resource's running indicator at the live run, a question run included", async () => {
    const run = (id: string, question?: string) => ({
      id,
      kind: "Deployment",
      group: "apps",
      namespace: "shop",
      name: "api",
      context: "ctx",
      status: "running",
      question,
      createdAt: "2026-10-28T09:00:00Z",
      updatedAt: "2026-10-28T09:00:00Z",
    });
    const key = runTargetKey("Deployment", "shop", "api", "apps");
    api.runs = [run("asked", "why?")];
    await act(async () => diagnose.refreshRuns());
    await flush();
    expect(layout.runningRunIds.get(key)).toBe("asked");
    api.runs = [run("asked", "why?"), run("plain")];
    await act(async () => diagnose.refreshRuns());
    await flush();
    expect(layout.runningRunIds.get(key)).toBe("plain");
  });

  it("drops a start waiting on consent when the cluster changes", async () => {
    let recorded!: () => void;
    api.recordConsent.mockReturnValue(
      new Promise<void>((resolve) => (recorded = resolve)),
    );
    await act(async () =>
      diagnose.openInvestigation({
        ...question,
        kind: "Deployment",
        group: "apps",
        namespace: "shop",
        name: "api",
      }),
    );
    expect(diagnose.needsConsent).toBe(true);
    await act(async () => diagnose.approveConsent());
    cluster.name = "cluster-b";
    await act(async () =>
      root.render(
        <DiagnoseProvider browserURLState={false}>
          <Probe />
        </DiagnoseProvider>,
      ),
    );
    await act(async () => recorded());
    await flush();
    expect(diagnose.needsConsent).toBe(false);
    expect(api.createRun).not.toHaveBeenCalled();
    expect(diagnose.questionDraft).toMatchObject({
      text: "checkout returns 502s",
      context: "cluster-a",
    });
  });

  it("protects a start opened before the cluster was known", async () => {
    const rerender = () =>
      act(async () =>
        root.render(
          <DiagnoseProvider browserURLState={false}>
            <Probe />
          </DiagnoseProvider>,
        ),
      );
    cluster.name = undefined;
    await rerender();
    await act(async () => diagnose.openInvestigation(question));
    expect(diagnose.needsConsent).toBe(true);
    cluster.name = "cluster-a";
    await rerender();
    cluster.name = "cluster-b";
    await rerender();
    await flush();
    expect(diagnose.needsConsent).toBe(false);
    expect(diagnose.questionDraft?.context).toBe("cluster-a");
  });
});
