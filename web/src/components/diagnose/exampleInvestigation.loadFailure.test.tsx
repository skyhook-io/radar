// @vitest-environment jsdom
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { SubscribeHandlers } from "../../api/diagnose";
import { EXAMPLE_INVESTIGATION_RUN } from "./exampleInvestigation";
import { InvestigationView } from "./InvestigationView";

const openInvestigation = vi.fn();
vi.mock("./exampleInvestigation", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./exampleInvestigation")>()),
  // The transcript chunk failed to load.
  replayExampleInvestigation: (handlers: SubscribeHandlers) => {
    queueMicrotask(() => handlers.onClosed?.("unavailable"));
    return () => {};
  },
}));
vi.mock("./DiagnoseContext", () => ({
  useDiagnose: () => ({
    refreshRuns: vi.fn(),
    openInvestigation,
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

describe("the example when its transcript can't load", () => {
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
  });

  it("says so and never offers to investigate the sample resource", async () => {
    await act(async () => {
      root.render(
        <QueryClientProvider client={new QueryClient()}>
          <MemoryRouter>
            <InvestigationView
              run={EXAMPLE_INVESTIGATION_RUN}
              agentLabel="Claude Code"
              maximized
              example
            />
          </MemoryRouter>
        </QueryClientProvider>,
      );
    });
    await vi.waitFor(() =>
      expect(container.textContent).toContain("couldn't load the example"),
    );
    expect(container.textContent).not.toMatch(/Investigate again/);
    expect(openInvestigation).not.toHaveBeenCalled();
  });
});
