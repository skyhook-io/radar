// @vitest-environment jsdom
import { act, useEffect } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { DiagnoseError } from "../../api/diagnose";
import { DiagnoseProvider, useDiagnose } from "./DiagnoseContext";

const api = vi.hoisted(() => ({ createRun: vi.fn() }));

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
        profiles: ["safeguarded"],
        consentSurfaces: { safeguarded: "claude:safeguarded" },
      },
    ],
  }),
  listRuns: async () => ({ runs: [] }),
  getRun: vi.fn(() => new Promise(() => {})),
  createRun: api.createRun,
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

const target = { kind: "Pod", group: "", namespace: "shop", name: "cart-0" };
const atCapacity = () =>
  new DiagnoseError(409, "3 investigations are already running.", {
    code: "investigations_at_capacity",
    limit: 3,
    context: "cluster-a",
  });

beforeEach(async () => {
  api.createRun.mockReset();
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

describe("a start refused at capacity", () => {
  it("keeps the refused start so it can be retried", async () => {
    api.createRun.mockRejectedValue(atCapacity());
    await act(async () => probe.d.openInvestigation(target));
    await flush();
    expect(probe.d.startCapacity).toEqual({
      target,
      limit: 3,
      context: "cluster-a",
    });
    expect(probe.d.startRefusal).toBeNull();
  });

  it("does not take over a run the user opened while it was in flight", async () => {
    let refuse!: () => void;
    api.createRun.mockReturnValue(
      new Promise((_, reject) => (refuse = () => reject(atCapacity()))),
    );
    await act(async () => probe.d.openInvestigation(target));
    await act(async () => probe.d.openRun("run-other"));
    await act(async () => refuse());
    await flush();
    expect(probe.d.startCapacity).toBeNull();
    expect(probe.d.activeRunId).toBe("run-other");
  });
});
