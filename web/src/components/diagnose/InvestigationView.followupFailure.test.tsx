// @vitest-environment jsdom
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

import { DiagnoseError, type SubscribeHandlers } from "../../api/diagnose";
import { EXAMPLE_INVESTIGATION_RUN } from "./exampleInvestigation";
import { InvestigationView } from "./InvestigationView";

const addTurn = vi.fn();
vi.mock("../../api/diagnose", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../api/diagnose")>()),
  subscribeRun: (_id: string, handlers: SubscribeHandlers) => {
    queueMicrotask(() => handlers.onEvent({ type: "replay_complete" } as never));
    return () => {};
  },
  addTurn: (...args: unknown[]) => addTurn(...args),
}));
vi.mock("./DiagnoseContext", () => ({
  useDiagnose: () => ({
    refreshRuns: vi.fn(),
    openInvestigation: vi.fn(),
    openQuestion: vi.fn(),
    startError: null,
    startRefusal: null,
    dismissError: vi.fn(),
    agents: [],
  }),
}));
vi.mock("../../context/ThemeContext", () => ({
  useTheme: () => ({ theme: "light", setTheme: vi.fn(), toggleTheme: vi.fn() }),
}));
vi.mock("../ui/Tooltip", () => ({
  Tooltip: ({ children }: { children: ReactNode }) => children,
}));

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
let root: Root;
let container: HTMLDivElement;

beforeEach(() => {
  vi.stubGlobal("fetch", vi.fn());
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
  addTurn.mockReset();
});

function typeInto(textarea: HTMLTextAreaElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
  setter.call(textarea, value);
  textarea.dispatchEvent(new Event("input", { bubbles: true }));
}

async function mountFollowupBox() {
  await act(async () => {
    root.render(
      <QueryClientProvider client={new QueryClient()}>
        <MemoryRouter>
          <InvestigationView run={{ ...EXAMPLE_INVESTIGATION_RUN, id: "run-1", canContinue: true }} agentLabel="Claude Code" maximized />
        </MemoryRouter>
      </QueryClientProvider>,
    );
  });
  const box = await vi.waitFor(() => {
    const el = container.querySelector<HTMLTextAreaElement>('textarea[placeholder="Ask a follow-up or refine…"]');
    expect(el).not.toBeNull();
    return el!;
  });
  return box;
}

async function send(box: HTMLTextAreaElement, question: string) {
  await act(async () => typeInto(box, question));
  await act(async () => {
    container.querySelector<HTMLButtonElement>('button[aria-label="Send follow-up"]')!.click();
  });
}

it("gives a refused follow-up back with the reason, instead of losing it", async () => {
  const question = "é".repeat(2001);
  addTurn.mockRejectedValue(new DiagnoseError(400, "question is too long (at most 2000 characters)"));
  const box = await mountFollowupBox();
  await send(box, question);

  expect(addTurn).toHaveBeenCalledWith("run-1", { question });
  await vi.waitFor(() => expect(container.textContent).toContain("question is too long (at most 2000 characters)"));
  expect(box.value).toBe(question);
});

it("does not give back a follow-up when the network failed, since the server may have taken it", async () => {
  addTurn.mockRejectedValue(new TypeError("Failed to fetch"));
  const box = await mountFollowupBox();
  await send(box, "why did it restart?");

  await vi.waitFor(() => expect(container.textContent).toContain("Couldn't send."));
  expect(box.value).toBe("");
});
